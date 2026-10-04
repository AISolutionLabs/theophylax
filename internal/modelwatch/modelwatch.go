// Package modelwatch keeps a Theophylax deployment current with the model
// ecosystem without anyone editing configuration by hand:
//
//   - Discovery: every interval it asks each connected provider which models
//     it serves and records the ones nobody has added yet. The first scan of
//     a provider is a silent baseline, so admins only see genuinely new
//     launches afterwards.
//   - Catalog: it fetches a public model catalog (by default the community-
//     maintained LiteLLM price and context-window list) to price new models,
//     fill missing prices, flag price changes and warn about deprecations.
//
// Nothing changes routing on its own: admins review suggestions in the
// console and add a model (optionally as a canary rollout) with one click.
// One replica runs a scan at a time (Postgres advisory lock).
package modelwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"theophylax/internal/providers"
	"theophylax/internal/store"
	"theophylax/internal/util"
)

const DefaultCatalogURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// Entry is the catalog's view of one model. Prices are USD per million tokens.
type Entry struct {
	Key          string   `json:"key"`
	InputPrice   *float64 `json:"input_price,omitempty"`
	OutputPrice  *float64 `json:"output_price,omitempty"`
	CacheRead    *float64 `json:"cache_read_price,omitempty"`
	CacheWrite   *float64 `json:"cache_write_price,omitempty"`
	MaxInput     int      `json:"context_window,omitempty"`
	MaxOutput    int      `json:"max_output,omitempty"`
	Mode         string   `json:"mode,omitempty"`
	Deprecation  string   `json:"deprecation_date,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// Catalog is an in-memory model catalog keyed by provider-specific IDs.
type Catalog struct {
	entries map[string]Entry
	fetched time.Time
	source  string
}

func (c *Catalog) Len() int {
	if c == nil {
		return 0
	}
	return len(c.entries)
}

// ParseCatalog reads the LiteLLM model_prices_and_context_window.json format.
func ParseCatalog(raw []byte, source string) (*Catalog, error) {
	var m map[string]map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("model catalog: %w", err)
	}
	c := &Catalog{entries: make(map[string]Entry, len(m)), fetched: time.Now(), source: source}
	perM := func(v any) *float64 {
		f, ok := v.(float64)
		if !ok || f <= 0 {
			return nil
		}
		x := f * 1e6
		return &x
	}
	for k, v := range m {
		if k == "sample_spec" {
			continue
		}
		e := Entry{Key: k, InputPrice: perM(v["input_cost_per_token"]), OutputPrice: perM(v["output_cost_per_token"]),
			CacheRead: perM(v["cache_read_input_token_cost"]), CacheWrite: perM(v["cache_creation_input_token_cost"])}
		if f, ok := v["max_input_tokens"].(float64); ok {
			e.MaxInput = int(f)
		}
		if f, ok := v["max_output_tokens"].(float64); ok {
			e.MaxOutput = int(f)
		}
		e.Mode, _ = v["mode"].(string)
		e.Deprecation, _ = v["deprecation_date"].(string)
		for cap, key := range map[string]string{"tools": "supports_function_calling", "vision": "supports_vision", "prompt_caching": "supports_prompt_caching", "reasoning": "supports_reasoning", "pdf": "supports_pdf_input"} {
			if v[key] == true {
				e.Capabilities = append(e.Capabilities, cap)
			}
		}
		c.entries[strings.ToLower(k)] = e
	}
	return c, nil
}

// Lookup finds the catalog entry for a provider's upstream model ID.
func (c *Catalog) Lookup(kind, upstream string) (Entry, bool) {
	if c == nil || upstream == "" {
		return Entry{}, false
	}
	id := strings.ToLower(upstream)
	var tries []string
	switch kind {
	case providers.KindBedrock:
		base := id
		for _, p := range []string{"us.", "eu.", "apac.", "global.", "us-gov.", "jp.", "au."} {
			base = strings.TrimPrefix(base, p)
		}
		tries = []string{id, "bedrock/" + id, base, "bedrock/" + base}
		if i := strings.LastIndex(id, "/"); i >= 0 { // inference profile ARNs
			tries = append(tries, id[i+1:])
		}
	case providers.KindAzureOpenAI:
		tries = []string{"azure/" + id, id}
	case providers.KindAzureFoundry:
		tries = []string{"azure_ai/" + id, id}
	case providers.KindVertex:
		tries = []string{"vertex_ai/" + id, id, "vertex_ai/" + strings.ReplaceAll(id, "@", "-"), strings.Split(id, "@")[0]}
	case providers.KindVertexOpenAI:
		bare := strings.TrimPrefix(id, "google/")
		tries = []string{"vertex_ai/" + bare, bare, "gemini/" + bare}
	case providers.KindGemini:
		tries = []string{"gemini/" + strings.TrimPrefix(id, "models/"), strings.TrimPrefix(id, "models/")}
	default:
		tries = []string{id, kind + "/" + id}
	}
	// Claude is priced the same everywhere at list price; when the catalog
	// lacks a provider-specific entry, fall back to Bedrock's base (non-
	// regional) entry for the same model.
	if strings.Contains(id, "claude") {
		bare := strings.Split(strings.TrimPrefix(strings.TrimPrefix(id, "anthropic/"), "anthropic."), "@")[0]
		tries = append(tries, bare, "anthropic."+bare+"-v1:0", "anthropic."+bare+"-v1", "anthropic."+bare)
	}
	for _, t := range tries {
		if e, ok := c.entries[t]; ok {
			return e, true
		}
	}
	return Entry{}, false
}

// Watcher runs discovery and catalog refresh.
type Watcher struct {
	St         *store.Store
	Providers  func() map[string]providers.Provider // id → provider, from the live config snapshot
	CatalogURL string                               // "" disables catalog fetching
	Interval   time.Duration
	Client     *http.Client
	// OnNew is called after a scan that found new models (audit / export).
	OnNew func(ctx context.Context, found []Discovery)

	mu      sync.RWMutex
	catalog *Catalog
}

func New(st *store.Store, provs func() map[string]providers.Provider) *Watcher {
	url := os.Getenv("THEOPHYLAX_MODEL_CATALOG_URL")
	switch url {
	case "":
		url = DefaultCatalogURL
	case "off", "none", "false":
		url = ""
	}
	iv := 6 * time.Hour
	if d, err := time.ParseDuration(os.Getenv("THEOPHYLAX_MODEL_WATCH_INTERVAL")); err == nil {
		iv = d
	}
	return &Watcher{St: st, Providers: provs, CatalogURL: url, Interval: iv, Client: &http.Client{Timeout: 30 * time.Second}}
}

// Catalog returns the most recent catalog (nil if none was loaded).
func (w *Watcher) Catalog() *Catalog {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.catalog
}

// Run scans shortly after start and then every Interval. Interval 0 disables.
func (w *Watcher) Run(ctx context.Context) {
	if w == nil || w.Interval <= 0 {
		return
	}
	w.loadCachedCatalog(ctx)
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

// loadCachedCatalog lets every replica price models from the last catalog any
// replica fetched, without each one hitting the network at start.
func (w *Watcher) loadCachedCatalog(ctx context.Context) {
	var raw []byte
	if err := w.St.Pool.QueryRow(ctx, `SELECT value::text FROM settings WHERE key='model_catalog'`).Scan(&raw); err != nil {
		return
	}
	var cached struct {
		Source string          `json:"source"`
		Data   json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &cached) == nil {
		if c, err := ParseCatalog(cached.Data, cached.Source); err == nil {
			w.mu.Lock()
			w.catalog = c
			w.mu.Unlock()
		}
	}
}

func (w *Watcher) refreshCatalog(ctx context.Context) error {
	if w.CatalogURL == "" {
		return nil
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, w.CatalogURL, nil)
	resp, err := w.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("catalog %s: status %d", w.CatalogURL, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	c, err := ParseCatalog(raw, w.CatalogURL)
	if err != nil {
		return err
	}
	w.mu.Lock()
	w.catalog = c
	w.mu.Unlock()
	blob, _ := json.Marshal(map[string]any{"source": w.CatalogURL, "fetched_at": time.Now(), "data": json.RawMessage(raw)})
	_, err = w.St.Pool.Exec(ctx, `INSERT INTO settings (key, value) VALUES ('model_catalog', $1) ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value`, string(blob))
	return err
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
	if err := w.refreshCatalog(ctx); err != nil {
		res.Errors = append(res.Errors, "catalog: "+err.Error())
	}
	res.Catalog = w.Catalog().Len()
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
