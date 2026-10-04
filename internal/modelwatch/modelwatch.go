// Package modelwatch keeps a Theophylax deployment current with the model
// ecosystem without anyone editing configuration by hand:
//
//   - Discovery: every interval it asks each connected provider which models
//     it serves and records the ones nobody has added yet. The first scan of
//     a provider is a silent baseline, so admins only see genuinely new
//     launches afterwards.
//   - Catalog: new models are described (price, context window, limits,
//     capabilities) from the pricing catalog, and models the catalog marks
//     as retiring are flagged so admins can roll forward in time.
//
// Nothing changes routing on its own: admins review suggestions in the
// console and add a model (optionally as a canary rollout) with one click.
// One replica runs a scan at a time (Postgres advisory lock).
package modelwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"theophylax/internal/pricing"
	"theophylax/internal/providers"
	"theophylax/internal/store"
	"theophylax/internal/util"
)

// Watcher runs discovery and catalog refresh.
type Watcher struct {
	St        *store.Store
	Prices    *pricing.Catalog
	Providers func() map[string]providers.Provider // id → provider, from the live config snapshot
	Interval  time.Duration
	// OnNew is called after a scan that found new models (audit / export).
	OnNew func(ctx context.Context, found []Discovery)
}

// New reads THEOPHYLAX_MODEL_WATCH_INTERVAL (default 6h, 0 disables).
func New(st *store.Store, prices *pricing.Catalog, provs func() map[string]providers.Provider) *Watcher {
	iv := 6 * time.Hour
	if d, err := time.ParseDuration(os.Getenv("THEOPHYLAX_MODEL_WATCH_INTERVAL")); err == nil {
		iv = d
	}
	return &Watcher{St: st, Prices: prices, Providers: provs, Interval: iv}
}

// Run scans shortly after start and then every Interval. Interval 0 disables.
func (w *Watcher) Run(ctx context.Context) {
	if w == nil || w.Interval <= 0 {
		return
	}
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := w.Scan(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("model watch scan failed", "err", err)
			}
			t.Reset(w.Interval)
		}
	}
}

// Discovery is one model a provider offers.
type Discovery struct {
	ID            string     `json:"id" db:"id"`
	ProviderID    string     `json:"provider_id" db:"provider_id"`
	UpstreamModel string     `json:"upstream_model" db:"upstream_model"`
	DisplayName   string     `json:"display_name" db:"display_name"`
	Kind          string     `json:"kind" db:"kind"`
	Status        string     `json:"status" db:"status"`
	FirstSeen     time.Time  `json:"first_seen" db:"first_seen"`
	LastSeen      time.Time  `json:"last_seen" db:"last_seen"`
	GoneAt        *time.Time `json:"gone_at" db:"gone_at"`
}

// ScanResult summarises one scan.
type ScanResult struct {
	Providers int         `json:"providers"`
	New       []Discovery `json:"new"`
	Gone      int         `json:"gone"`
	Errors    []string    `json:"errors"`
	Catalog   int         `json:"catalog_models"`
}

// Scan discovers models now. Only one replica scans at a time.
func (w *Watcher) Scan(ctx context.Context) (*ScanResult, error) {
	conn, err := w.St.Pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(727272)`).Scan(&got); err != nil {
		return nil, err
	}
	if !got {
		return nil, fmt.Errorf("a scan is already running on another replica")
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(727272)`) //nolint:errcheck

	res := &ScanResult{Errors: []string{}, New: []Discovery{}}
	if w.Prices != nil {
		res.Catalog = w.Prices.Len()
	}
	for id, p := range w.Providers() {
		lctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		models, err := p.ListModels(lctx)
		cancel()
		if err != nil {
			// Providers without a listing API (Vertex, Foundry) are expected.
			if !strings.Contains(err.Error(), "does not list") && !strings.Contains(err.Error(), "requires api_version") {
				res.Errors = append(res.Errors, id+": "+truncate(err.Error(), 160))
			}
			continue
		}
		res.Providers++
		found, gone, err := w.record(ctx, id, models)
		if err != nil {
			return nil, err
		}
		res.New = append(res.New, found...)
		res.Gone += gone
	}
	errs, _ := json.Marshal(res.Errors)
	_, _ = w.St.Pool.Exec(ctx, `INSERT INTO model_watch_runs (id, providers, new_models, gone_models, catalog_models, errors) VALUES ($1,$2,$3,$4,$5,$6)`,
		util.NewID("mwr"), res.Providers, len(res.New), res.Gone, res.Catalog, string(errs))
	if len(res.New) > 0 && w.OnNew != nil {
		w.OnNew(ctx, res.New)
	}
	return res, nil
}

// record upserts a provider's model list. The first scan of a provider is
// recorded as a baseline; later IDs are "new" unless already deployed.
func (w *Watcher) record(ctx context.Context, providerID string, models []providers.DiscoveredModel) ([]Discovery, int, error) {
	var found []Discovery
	gone := 0
	err := w.St.Tx(ctx, func(tx pgx.Tx) error {
		var seen int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM model_discoveries WHERE provider_id=$1`, providerID).Scan(&seen); err != nil {
			return err
		}
		deployed := map[string]bool{}
		rows, err := tx.Query(ctx, `SELECT upstream_model FROM deployments WHERE provider_id=$1`, providerID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			deployed[s] = true
		}
		rows.Close()
		now := time.Now()
		ids := make([]string, 0, len(models))
		for _, m := range models {
			ids = append(ids, m.ID)
			status := "new"
			switch {
			case deployed[m.ID]:
				status = "added"
			case seen == 0:
				status = "baseline"
			}
			d := Discovery{ID: util.NewID("mdd"), ProviderID: providerID, UpstreamModel: m.ID, DisplayName: m.DisplayName, Kind: m.Kind, Status: status, FirstSeen: now, LastSeen: now}
			var created bool
			err := tx.QueryRow(ctx, `INSERT INTO model_discoveries (id, provider_id, upstream_model, display_name, kind, status, first_seen, last_seen)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$7)
				ON CONFLICT (provider_id, upstream_model) DO UPDATE SET last_seen=EXCLUDED.last_seen, gone_at=NULL, display_name=EXCLUDED.display_name
				RETURNING (xmax = 0)`,
				d.ID, d.ProviderID, d.UpstreamModel, d.DisplayName, d.Kind, d.Status, now).Scan(&created)
			if err != nil {
				return err
			}
			if created && status == "new" {
				found = append(found, d)
			}
		}
		t, err := tx.Exec(ctx, `UPDATE model_discoveries SET gone_at=$3 WHERE provider_id=$1 AND gone_at IS NULL AND NOT (upstream_model = ANY($2))`, providerID, ids, now)
		if err != nil {
			return err
		}
		gone = int(t.RowsAffected())
		return nil
	})
	return found, gone, err
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
