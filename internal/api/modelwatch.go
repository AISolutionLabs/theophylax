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
}

type catalogInfo struct {
	Key           string   `json:"key"`
	InputPerM     *float64 `json:"input_price,omitempty"`
	OutputPerM    *float64 `json:"output_price,omitempty"`
	ContextWindow int      `json:"context_window,omitempty"`
	MaxOutput     int      `json:"max_output,omitempty"`
	Mode          string   `json:"mode,omitempty"`
	Deprecation   string   `json:"deprecation_date,omitempty"`
	Capabilities  []string `json:"capabilities,omitempty"`
}

func (a *API) catalogFor(kind, upstream string) *catalogInfo {
	if a.Pricing == nil {
		return nil
	}
	p := a.Pricing.Lookup(kind, upstream)
	if p == nil {
		return nil
	}
	q := p.Quote()
	return &catalogInfo{Key: p.Key, InputPerM: q.Input, OutputPerM: q.Output, ContextWindow: p.MaxInput, MaxOutput: p.MaxOutput, Mode: p.Mode, Deprecation: p.Deprecation, Capabilities: p.Capabilities}
}

type discoveryView struct {
	modelwatch.Discovery
	ProviderName  string       `json:"provider_name"`
	ProviderKind  string       `json:"provider_kind"`
	SuggestedName string       `json:"suggested_name"`
	Catalog       *catalogInfo `json:"catalog,omitempty"`
}

type lifecycleNotice struct {
	ModelID   string       `json:"model_id"`
	ModelName string       `json:"model_name"`
	Upstream  string       `json:"upstream_model"`
	Provider  string       `json:"provider_name"`
	Reason    string       `json:"reason"` // deprecated | deprecating
	Catalog   *catalogInfo `json:"catalog"`
}

func (a *API) modelWatch(c *Ctx) (any, error) {
	if err := c.RequirePlatformAdmin(); err != nil {
		return nil, err
	}
	snap := a.Loader.Snapshot()
	rows, err := store.List[modelwatch.Discovery](c, a.St.Pool, `SELECT id, provider_id, upstream_model, display_name, kind, status, first_seen, last_seen, gone_at
		FROM model_discoveries WHERE status='new' AND gone_at IS NULL ORDER BY first_seen DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	newOnes := []discoveryView{}
	for _, d := range rows {
		v := discoveryView{Discovery: d, SuggestedName: SuggestName(d.UpstreamModel)}
		if p := snap.Providers[d.ProviderID]; p != nil && p.Row != nil {
			v.ProviderName, v.ProviderKind = p.Row.Name, p.Row.Kind
		}
		v.Catalog = a.catalogFor(v.ProviderKind, d.UpstreamModel)
		newOnes = append(newOnes, v)
	}
	notices := []lifecycleNotice{}
	soon := time.Now().AddDate(0, 0, 90)
	done := map[string]bool{} // routes are indexed by name and by alias
	for _, rt := range snap.Models {
		for _, d := range rt.Deps {
			if d.Provider == nil || d.Provider.Row == nil || !d.Enabled || done[d.ID] {
				continue
			}
			done[d.ID] = true
			ci := a.catalogFor(d.Provider.Row.Kind, d.UpstreamModel)
			if ci == nil || ci.Deprecation == "" {
				continue
			}
			t, err := time.Parse("2006-01-02", ci.Deprecation)
			if err != nil || t.After(soon) {
				continue
			}
			reason := "deprecating"
			if t.Before(time.Now()) {
				reason = "deprecated"
			}
			notices = append(notices, lifecycleNotice{ModelID: rt.Model.ID, ModelName: rt.Model.Name, Upstream: d.UpstreamModel, Provider: d.Provider.Row.Name, Reason: reason, Catalog: ci})
		}
	}
	sort.Slice(notices, func(i, j int) bool { return notices[i].Catalog.Deprecation < notices[j].Catalog.Deprecation })
	var last map[string]any
	if r, err := store.List[struct {
		RanAt     time.Time `db:"ran_at"`
		Providers int       `db:"providers"`
		NewModels int       `db:"new_models"`
		Catalog   int       `db:"catalog_models"`
		Errors    []byte    `db:"errors"`
	}](c, a.St.Pool, `SELECT ran_at, providers, new_models, catalog_models, errors FROM model_watch_runs ORDER BY ran_at DESC LIMIT 1`); err == nil && len(r) > 0 {
		last = map[string]any{"ran_at": r[0].RanAt, "providers": r[0].Providers, "new_models": r[0].NewModels, "catalog_models": r[0].Catalog, "errors": jsonRaw(r[0].Errors)}
	}
	catalogModels := 0
	if a.Pricing != nil {
		catalogModels = a.Pricing.Len()
	}
	return map[string]any{"new": newOnes, "lifecycle": notices, "last_run": last, "interval": a.Watch.Interval.String(), "catalog_models": catalogModels}, nil
}

func jsonRaw(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("[]")
	}
	return json.RawMessage(b)
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

// modelWatchAdd turns a discovery into a catalog model or an extra
// deployment of an existing model. Pricing follows the deployment's upstream
// ID automatically.
func (a *API) modelWatchAdd(c *Ctx) (any, error) {
	if err := c.RequirePlatformAdmin(); err != nil {
		return nil, err
	}
	var in struct {
		Name    string `json:"name"`
		ModelID string `json:"model_id"`
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
	ci := a.catalogFor(prov.Kind, d.UpstreamModel)
	c.Audit("model_watch.add", "", "model", model.ID, map[string]any{"upstream": d.UpstreamModel, "provider": prov.Name, "priced": ci != nil && ci.InputPerM != nil})
	a.changed(c)
	return map[string]any{"model_id": model.ID, "model_name": model.Name, "deployment_id": dep.ID, "priced": ci != nil && ci.InputPerM != nil}, nil
}

// newModelFromCatalog fills context window, output limit and capabilities
// from the catalog when it knows the upstream model.
func (a *API) newModelFromCatalog(name, display, kind, upstream string) *store.Model {
	fam := "other"
	for _, f := range []string{"opus", "sonnet", "haiku", "fable", "mythos", "gpt", "gemini", "llama", "nova", "mistral", "deepseek", "grok", "embed"} {
		if strings.Contains(name, f) {
			fam = f
			break
		}
	}
	m := &store.Model{ID: util.NewID("mdl"), Name: name, DisplayName: display, Family: fam, Status: "active", Strategy: "priority", Source: "discovered", Aliases: []string{}, Capabilities: []string{}}
	if ci := a.catalogFor(kind, upstream); ci != nil {
		m.ContextWindow, m.MaxOutput = ci.ContextWindow, ci.MaxOutput
		m.Capabilities = append([]string{"streaming"}, ci.Capabilities...)
		if ci.Mode == "embedding" {
			m.Capabilities = []string{"embeddings"}
		}
	}
	return m
}
