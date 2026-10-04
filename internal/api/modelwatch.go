package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"theophylax/internal/modelwatch"
	"theophylax/internal/store"
	"theophylax/internal/util"
)

func (a *API) ModelWatchRoutes(mux *http.ServeMux) {
	h := a.handle
	mux.HandleFunc("GET /api/model-watch", h(a.modelWatch))
	mux.HandleFunc("POST /api/model-watch/scan", h(a.modelWatchScan))
	mux.HandleFunc("POST /api/model-watch/discoveries/{id}/dismiss", h(a.modelWatchDismiss))
	mux.HandleFunc("POST /api/model-watch/discoveries/{id}/add", h(a.modelWatchAdd))
	mux.HandleFunc("POST /api/model-watch/apply-catalog", h(a.modelWatchApplyCatalog))
}

type discoveryView struct {
	modelwatch.Discovery
	ProviderName  string            `json:"provider_name"`
	ProviderKind  string            `json:"provider_kind"`
	SuggestedName string            `json:"suggested_name"`
	Catalog       *modelwatch.Entry `json:"catalog,omitempty"`
}

type catalogSuggestion struct {
	ModelID   string            `json:"model_id"`
	ModelName string            `json:"model_name"`
	Upstream  string            `json:"upstream_model"`
	Reason    string            `json:"reason"` // price_missing | price_changed | deprecated | deprecating
	Current   map[string]any    `json:"current"`
	Catalog   *modelwatch.Entry `json:"catalog"`
}

func (a *API) modelWatch(c *Ctx) (any, error) {
	if err := c.RequirePlatformAdmin(); err != nil {
		return nil, err
	}
	snap := a.Loader.Snapshot()
	cat := a.Watch.Catalog()
	rows, err := store.List[modelwatch.Discovery](c, a.St.Pool, `SELECT id, provider_id, upstream_model, display_name, kind, status, first_seen, last_seen, gone_at
		FROM model_discoveries WHERE status='new' AND gone_at IS NULL ORDER BY first_seen DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	newOnes := []discoveryView{}
	for _, d := range rows {
		v := discoveryView{Discovery: d, SuggestedName: SuggestName(d.UpstreamModel)}
		if p := snap.Providers[d.ProviderID]; p != nil {
			v.ProviderName, v.ProviderKind = p.Row.Name, p.Row.Kind
		}
		if e, ok := cat.Lookup(v.ProviderKind, d.UpstreamModel); ok {
			v.Catalog = &e
		}
		newOnes = append(newOnes, v)
	}
	sugg := []catalogSuggestion{}
	if cat.Len() > 0 {
		soon := time.Now().AddDate(0, 0, 90)
		done := map[string]bool{} // routes are indexed by name and alias
		for _, rt := range snap.Models {
			m := rt.Model
			if done[m.ID] {
				continue
			}
			done[m.ID] = true
			for _, d := range rt.Deps {
				if d.Provider == nil || d.Provider.Row == nil {
					continue
				}
				e, ok := cat.Lookup(d.Provider.Row.Kind, d.UpstreamModel)
				if !ok {
					continue
				}
				cur := map[string]any{"input_price": m.InputPrice, "output_price": m.OutputPrice}
				add := func(reason string) {
					ec := e
					sugg = append(sugg, catalogSuggestion{ModelID: m.ID, ModelName: m.Name, Upstream: d.UpstreamModel, Reason: reason, Current: cur, Catalog: &ec})
				}
				switch {
				case m.InputPrice == nil && e.InputPrice != nil:
					add("price_missing")
				case m.InputPrice != nil && e.InputPrice != nil && differs(*m.InputPrice, *e.InputPrice) ||
					m.OutputPrice != nil && e.OutputPrice != nil && differs(*m.OutputPrice, *e.OutputPrice):
					add("price_changed")
				}
				if e.Deprecation != "" {
					if t, err := time.Parse("2006-01-02", e.Deprecation); err == nil {
						if t.Before(time.Now()) {
							add("deprecated")
						} else if t.Before(soon) {
							add("deprecating")
						}
					}
				}
				break // one deployment per model is enough to price it
			}
		}
		sort.Slice(sugg, func(i, j int) bool { return sugg[i].ModelName < sugg[j].ModelName })
	}
	var last map[string]any
	if r, err := store.List[struct {
		RanAt     time.Time `db:"ran_at" json:"ran_at"`
		Providers int       `db:"providers" json:"providers"`
		NewModels int       `db:"new_models" json:"new_models"`
		Catalog   int       `db:"catalog_models" json:"catalog_models"`
		Errors    []byte    `db:"errors" json:"-"`
	}](c, a.St.Pool, `SELECT ran_at, providers, new_models, catalog_models, errors FROM model_watch_runs ORDER BY ran_at DESC LIMIT 1`); err == nil && len(r) > 0 {
		last = map[string]any{"ran_at": r[0].RanAt, "providers": r[0].Providers, "new_models": r[0].NewModels, "catalog_models": r[0].Catalog, "errors": jsonRaw(r[0].Errors)}
	}
	return map[string]any{"new": newOnes, "suggestions": sugg, "last_run": last, "catalog_url": a.Watch.CatalogURL,
		"interval": a.Watch.Interval.String(), "catalog_models": cat.Len()}, nil
}

func jsonRaw(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("[]")
	}
	return json.RawMessage(b)
}

func differs(a, b float64) bool {
	if a == 0 {
		return b != 0
	}
	d := (a - b) / a
	return d > 0.005 || d < -0.005
}

func (a *API) modelWatchScan(c *Ctx) (any, error) {
	if err := c.RequirePlatformAdmin(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(c, 3*time.Minute)
	defer cancel()
	res, err := a.Watch.Scan(ctx)
	if err != nil {
		return nil, util.Err(409, "conflict_error", err.Error())
	}
	c.Audit("model_watch.scan", "", "model_watch", "", map[string]any{"providers": res.Providers, "new": len(res.New), "errors": res.Errors})
	return res, nil
}

func (a *API) modelWatchDismiss(c *Ctx) (any, error) {
	if err := c.RequirePlatformAdmin(); err != nil {
		return nil, err
	}
	t, err := a.St.Pool.Exec(c, `UPDATE model_discoveries SET status='dismissed' WHERE id=$1`, c.Param("id"))
	if err != nil {
		return nil, err
	}
	if t.RowsAffected() == 0 {
		return nil, util.ErrNotFound
	}
	c.Audit("model_watch.dismiss", "", "model_discovery", c.Param("id"), nil)
	return map[string]any{"ok": true}, nil
}

// modelWatchAdd turns a discovery into a catalog model (priced from the
// catalog) or an extra deployment of an existing model.
func (a *API) modelWatchAdd(c *Ctx) (any, error) {
	if err := c.RequirePlatformAdmin(); err != nil {
		return nil, err
	}
	var in struct {
		Name    string `json:"name"`     // new model name (default: suggested)
		ModelID string `json:"model_id"` // or: add as a deployment of this model
	}
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	d, err := store.Get[modelwatch.Discovery](c, a.St.Pool, `SELECT id, provider_id, upstream_model, display_name, kind, status, first_seen, last_seen, gone_at FROM model_discoveries WHERE id=$1`, c.Param("id"))
	if err != nil {
		return nil, err
	}
	prov, err := store.GetByID[store.Provider](c, a.St.Pool, "providers", d.ProviderID)
	if err != nil {
		return nil, err
	}
	var model *store.Model
	if in.ModelID != "" {
		if model, err = store.GetByID[store.Model](c, a.St.Pool, "models", in.ModelID); err != nil {
			return nil, err
		}
	} else {
		name := strings.TrimSpace(in.Name)
		if name == "" {
			name = SuggestName(d.UpstreamModel)
		}
		if existing, err := store.Get[store.Model](c, a.St.Pool, `SELECT * FROM models WHERE name=$1`, name); err == nil {
			model = existing
		} else {
			model = a.newModelFromCatalog(name, d.DisplayName, prov.Kind, d.UpstreamModel)
			model.Source = "model_watch"
			if err := a.St.Insert(c, a.St.Pool, "models", model); err != nil {
				return nil, err
			}
		}
	}
	dep := &store.Deployment{ID: util.NewID("dep"), ModelID: model.ID, ProviderID: d.ProviderID, UpstreamModel: d.UpstreamModel, Weight: 1, Enabled: true}
	if err := a.St.Insert(c, a.St.Pool, "deployments", dep); err != nil {
		return nil, err
	}
	_, _ = a.St.Pool.Exec(c, `UPDATE model_discoveries SET status='added' WHERE id=$1`, d.ID)
	c.Audit("model_watch.add", "", "model", model.ID, map[string]any{"upstream": d.UpstreamModel, "provider": prov.Name, "priced": model.InputPrice != nil})
	a.changed(c)
	return map[string]any{"model_id": model.ID, "model_name": model.Name, "deployment_id": dep.ID, "priced": model.InputPrice != nil}, nil
}

// newModelFromCatalog builds a model row with prices, limits and
// capabilities from the catalog when it knows the upstream model.
func (a *API) newModelFromCatalog(name, display, kind, upstream string) *store.Model {
	fam := "other"
	for _, f := range []string{"opus", "sonnet", "haiku", "fable", "mythos", "gpt", "gemini", "llama", "nova", "mistral", "deepseek", "grok", "embed"} {
		if strings.Contains(name, f) {
			fam = f
			break
		}
	}
	m := &store.Model{ID: util.NewID("mdl"), Name: name, DisplayName: display, Family: fam, Status: "active", Strategy: "priority", Source: "discovered", Aliases: []string{}, Capabilities: []string{}}
	if e, ok := a.Watch.Catalog().Lookup(kind, upstream); ok {
		m.InputPrice, m.OutputPrice, m.CacheReadPrice, m.CacheWritePrice = e.InputPrice, e.OutputPrice, e.CacheRead, e.CacheWrite
		m.ContextWindow, m.MaxOutput = e.MaxInput, e.MaxOutput
		m.Capabilities = append([]string{"streaming"}, e.Capabilities...)
		if e.Mode == "embedding" {
			m.Capabilities = []string{"embeddings"}
		}
	}
	return m
}

// modelWatchApplyCatalog applies catalog prices to the listed models.
func (a *API) modelWatchApplyCatalog(c *Ctx) (any, error) {
	if err := c.RequirePlatformAdmin(); err != nil {
		return nil, err
	}
	var in struct {
		ModelIDs []string `json:"model_ids"`
	}
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	snap := a.Loader.Snapshot()
	cat := a.Watch.Catalog()
	updated := []string{}
	for _, rt := range snap.Models {
		want := false
		for _, id := range in.ModelIDs {
			want = want || id == rt.Model.ID
		}
		if !want {
			continue
		}
		for _, d := range rt.Deps {
			if d.Provider == nil || d.Provider.Row == nil {
				continue
			}
			if e, ok := cat.Lookup(d.Provider.Row.Kind, d.UpstreamModel); ok && e.InputPrice != nil {
				if _, err := a.St.Pool.Exec(c, `UPDATE models SET input_price=$2, output_price=$3, cache_read_price=COALESCE($4, cache_read_price), cache_write_price=COALESCE($5, cache_write_price), updated_at=now() WHERE id=$1`,
					rt.Model.ID, e.InputPrice, e.OutputPrice, e.CacheRead, e.CacheWrite); err != nil {
					return nil, err
				}
				updated = append(updated, rt.Model.Name)
				break
			}
		}
	}
	c.Audit("model_watch.apply_catalog", "", "models", "", map[string]any{"models": updated, "source": a.Watch.CatalogURL})
	a.changed(c)
	return map[string]any{"updated": updated}, nil
}
