package api

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"theophylax/internal/auth"
	"theophylax/internal/pricing"
	"theophylax/internal/providers"
	"theophylax/internal/store"
	"theophylax/internal/util"
)

// --- Providers

type providerIn struct {
	Name             *string          `json:"name"`
	OrgID            *string          `json:"org_id"`
	ConnectionString *string          `json:"connection_string"`
	Config           *json.RawMessage `json:"config"`
	Enabled          *bool            `json:"enabled"`
}

func (a *API) providerPerm(c *Ctx, orgID *string) error {
	if orgID == nil || *orgID == "" {
		return c.RequirePlatformAdmin()
	}
	return c.Require(auth.Scope{Type: "org", ID: *orgID, OrgID: *orgID}, "admin")
}

type providerView struct {
	store.Provider
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	CanManage   bool   `json:"can_manage"`
	Deployments int    `json:"deployment_count"`
	// OAuth is set for providers using auth=oauth (visible to their managers).
	OAuth *providerOAuthStatus `json:"oauth,omitempty"`
}

func (a *API) listProviders(c *Ctx) (any, error) {
	list, err := store.List[store.Provider](c, a.St.Pool, `SELECT * FROM providers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	rows, err := a.St.Pool.Query(c, `SELECT provider_id, count(*) FROM deployments GROUP BY provider_id`)
	if err == nil {
		for rows.Next() {
			var id string
			var n int
			_ = rows.Scan(&id, &n)
			counts[id] = n
		}
		rows.Close()
	}
	snap := a.Loader.Snapshot()
	out := []providerView{}
	for _, p := range list {
		if p.OrgID != nil && !c.P.InOrg(*p.OrgID) {
			continue
		}
		v := providerView{Provider: p, CanManage: a.providerPerm(c, p.OrgID) == nil, Deployments: counts[p.ID], Status: "ready"}
		if e := snap.Providers[p.ID]; e != nil && e.Err != "" {
			v.Status, v.Error = "error", e.Err
		} else if !p.Enabled {
			v.Status = "disabled"
		}
		if !v.CanManage {
			v.ConnRedacted = p.Kind + "://…"
		} else if strings.Contains(p.ConnRedacted, "auth=oauth") {
			v.OAuth = a.oauthStatus(c, &p)
			if v.OAuth != nil && v.OAuth.Grant == "authorization_code" && !v.OAuth.Connected && v.Status == "ready" {
				v.Status = "not_connected"
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func (a *API) buildProvider(conn string, cfg json.RawMessage) (providers.Provider, *providers.Conn, error) {
	pc, err := providers.ParseConn(conn)
	if err != nil {
		return nil, nil, badReq(err.Error())
	}
	var opt struct {
		DropParams []string          `json:"drop_params"`
		Headers    map[string]string `json:"headers"`
	}
	_ = json.Unmarshal(cfg, &opt)
	p, err := providers.New(conn, providers.Options{ID: "validate", Timeout: 20 * time.Second, DropParams: opt.DropParams, Headers: opt.Headers})
	if err != nil {
		return nil, pc, badReq(err.Error())
	}
	return p, pc, nil
}

func (a *API) createProvider(c *Ctx) (any, error) {
	var in providerIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if in.OrgID != nil && *in.OrgID == "" {
		in.OrgID = nil
	}
	if err := a.providerPerm(c, in.OrgID); err != nil {
		return nil, err
	}
	if util.Deref(in.Name) == "" || util.Deref(in.ConnectionString) == "" {
		return nil, badReq("name and connection_string are required")
	}
	cfg := json.RawMessage("{}")
	if in.Config != nil {
		cfg = *in.Config
	}
	_, pc, err := a.buildProvider(*in.ConnectionString, cfg)
	if err != nil {
		return nil, err
	}
	p := &store.Provider{ID: util.NewID("prv"), OrgID: in.OrgID, Name: *in.Name, Kind: pc.Kind, ConnEnc: a.Box.SealString(*in.ConnectionString), ConnRedacted: pc.Redacted(), Config: cfg, Enabled: in.Enabled == nil || *in.Enabled}
	if err := a.St.Insert(c, a.St.Pool, "providers", p); err != nil {
		return nil, err
	}
	c.Audit("provider.create", util.Deref(p.OrgID), "provider", p.ID, map[string]any{"name": p.Name, "kind": p.Kind, "connection": p.ConnRedacted})
	a.changed(c)
	return created{p}, nil
}

func (a *API) updateProvider(c *Ctx) (any, error) {
	p, err := store.GetByID[store.Provider](c, a.St.Pool, "providers", c.Param("id"))
	if err != nil {
		return nil, err
	}
	if err := a.providerPerm(c, p.OrgID); err != nil {
		return nil, err
	}
	var in providerIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	before := p.ConnRedacted
	if in.Name != nil {
		p.Name = *in.Name
	}
	if in.Enabled != nil {
		p.Enabled = *in.Enabled
	}
	if in.Config != nil {
		p.Config = *in.Config
	}
	if cs := util.Deref(in.ConnectionString); cs != "" {
		_, pc, err := a.buildProvider(cs, p.Config)
		if err != nil {
			return nil, err
		}
		if suffix := providers.ModelSuffixFor(cs); suffix != "" {
			var bad []string
			rows, err := a.St.Pool.Query(c, `SELECT DISTINCT m.name FROM deployments d JOIN models m ON m.id=d.model_id WHERE d.provider_id=$1 AND right(m.name, $2) <> $3`, p.ID, len(suffix), suffix)
			if err == nil {
				for rows.Next() {
					var n string
					_ = rows.Scan(&n)
					bad = append(bad, n)
				}
				rows.Close()
			}
			if len(bad) > 0 {
				return nil, badReq("models routed through this credential must end with " + suffix + "; rename or remove: " + strings.Join(bad, ", "))
			}
		}
		p.ConnEnc, p.ConnRedacted, p.Kind = a.Box.SealString(cs), pc.Redacted(), pc.Kind
	} else {
		p.ConnEnc = nil // keep existing secret
	}
	if err := a.St.Update(c, a.St.Pool, "providers", p); err != nil {
		return nil, err
	}
	c.Audit("provider.update", util.Deref(p.OrgID), "provider", p.ID, map[string]any{"name": p.Name, "enabled": p.Enabled, "connection_before": before, "connection_after": p.ConnRedacted})
	a.changed(c)
	return p, nil
}

func (a *API) deleteProvider(c *Ctx) (any, error) {
	p, err := store.GetByID[store.Provider](c, a.St.Pool, "providers", c.Param("id"))
	if err != nil {
		return nil, err
	}
	if err := a.providerPerm(c, p.OrgID); err != nil {
		return nil, err
	}
	if err := a.St.Delete(c, a.St.Pool, "providers", p.ID); err != nil {
		return nil, err
	}
	c.Audit("provider.delete", util.Deref(p.OrgID), "provider", p.ID, map[string]string{"name": p.Name})
	a.changed(c)
	return nil, nil
}

func (a *API) validateConn(c *Ctx) (any, error) {
	var in struct {
		ConnectionString string          `json:"connection_string"`
		Config           json.RawMessage `json:"config"`
		Probe            bool            `json:"probe"`
	}
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	p, pc, err := a.buildProvider(in.ConnectionString, in.Config)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}, nil
	}
	out := map[string]any{"ok": true, "kind": pc.Kind, "redacted": pc.Redacted(), "host": pc.Host}
	if in.Probe {
		ctx, cancel := context.WithTimeout(c, 20*time.Second)
		defer cancel()
		models, err := p.ListModels(ctx)
		if err != nil {
			out["probe_error"] = err.Error()
		} else {
			out["models"] = len(models)
		}
	}
	return out, nil
}

var bedrockID = regexp.MustCompile(`^(?:(?:us|eu|apac|global|jp|au|ca|us-gov)\.)?(?:anthropic\.)?(.+?)(?:-v\d+(?::\d+)?)?$`)

// SuggestName derives a clean public model name from an upstream id.
func SuggestName(upstream string) string {
	s := upstream
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if m := bedrockID.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	return strings.ToLower(s)
}

func (a *API) discoverModels(c *Ctx) (any, error) {
	prow, err := store.GetByID[store.Provider](c, a.St.Pool, "providers", c.Param("id"))
	if err != nil {
		return nil, err
	}
	if err := a.providerPerm(c, prow.OrgID); err != nil {
		return nil, err
	}
	e := a.Loader.Snapshot().Providers[prow.ID]
	if e == nil || e.P == nil {
		return nil, badReq("provider is not ready: " + func() string {
			if e != nil {
				return e.Err
			}
			return "not loaded yet"
		}())
	}
	ctx, cancel := context.WithTimeout(c, 30*time.Second)
	defer cancel()
	models, err := e.P.ListModels(ctx)
	if err != nil {
		return nil, util.Err(502, "api_error", "discovery failed: "+err.Error())
	}
	existing := map[string]bool{}
	rows, err := a.St.Pool.Query(c, `SELECT upstream_model FROM deployments WHERE provider_id=$1`, prow.ID)
	if err == nil {
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			existing[s] = true
		}
		rows.Close()
	}
	suffix := providers.ModelSuffix(e.P)
	out := make([]map[string]any, 0, len(models))
	for _, m := range models {
		out = append(out, map[string]any{"id": m.ID, "display_name": m.DisplayName, "kind": m.Kind, "suggested_name": SuggestName(m.ID) + suffix, "configured": existing[m.ID]})
	}
	return out, nil
}

// --- Models & deployments

type deploymentView struct {
	store.Deployment
	ProviderName string  `json:"provider_name" db:"provider_name"`
	ProviderKind string  `json:"provider_kind" db:"provider_kind"`
	ProviderOrg  *string `json:"provider_org_id" db:"provider_org_id"`
	// Pricing is the upstream list price for this deployment, or nil when the
	// price catalog has no entry for its upstream model ID.
	Pricing *pricing.Quote `json:"pricing" db:"-"`
}

type modelView struct {
	store.Model
	Deployments []deploymentView `json:"deployments"`
	Requests24h int64            `json:"requests_24h"`
}

func (a *API) listModels(c *Ctx) (any, error) {
	models, err := store.List[store.Model](c, a.St.Pool, `SELECT * FROM models ORDER BY family, name`)
	if err != nil {
		return nil, err
	}
	deps, err := store.List[deploymentView](c, a.St.Pool, `SELECT d.*, p.name AS provider_name, p.kind AS provider_kind, p.org_id AS provider_org_id FROM deployments d JOIN providers p ON p.id=d.provider_id ORDER BY d.priority, d.created_at`)
	if err != nil {
		return nil, err
	}
	by := map[string][]deploymentView{}
	for _, d := range deps {
		if d.ProviderOrg != nil && !c.P.InOrg(*d.ProviderOrg) {
			continue
		}
		d.Pricing = a.Pricing.QuoteFor(d.ProviderKind, d.UpstreamModel)
		by[d.ModelID] = append(by[d.ModelID], d)
	}
	usage := map[string]int64{}
	rows, err := a.St.Pool.Query(c, `SELECT model, count(*) FROM request_logs WHERE ts > now() - interval '24 hours' GROUP BY model`)
	if err == nil {
		for rows.Next() {
			var m string
			var n int64
			_ = rows.Scan(&m, &n)
			usage[m] = n
		}
		rows.Close()
	}
	out := make([]modelView, 0, len(models))
	for _, m := range models {
		v := modelView{Model: m, Deployments: by[m.ID]}
		if v.Deployments == nil {
			v.Deployments = []deploymentView{}
		}
		v.Requests24h = usage[m.Name]
		for _, al := range m.Aliases {
			v.Requests24h += usage[al]
		}
		out = append(out, v)
	}
	return out, nil
}

type modelIn struct {
	Name          *string   `json:"name"`
	DisplayName   *string   `json:"display_name"`
	Family        *string   `json:"family"`
	Aliases       *[]string `json:"aliases"`
	ContextWindow *int      `json:"context_window"`
	MaxOutput     *int      `json:"max_output"`
	Capabilities  *[]string `json:"capabilities"`
	Status        *string   `json:"status"`
	Strategy      *string   `json:"strategy"`
}

func applyModelIn(m *store.Model, in *modelIn) {
	if in.Name != nil {
		m.Name = strings.TrimSpace(*in.Name)
	}
	if in.DisplayName != nil {
		m.DisplayName = *in.DisplayName
	}
	if in.Family != nil {
		m.Family = *in.Family
	}
	if in.Aliases != nil {
		m.Aliases = *in.Aliases
	}
	if in.ContextWindow != nil {
		m.ContextWindow = *in.ContextWindow
	}
	if in.MaxOutput != nil {
		m.MaxOutput = *in.MaxOutput
	}
	if in.Capabilities != nil {
		m.Capabilities = *in.Capabilities
	}
	if in.Status != nil {
		m.Status = *in.Status
	}
	if in.Strategy != nil {
		m.Strategy = *in.Strategy
	}
}

func (a *API) createModel(c *Ctx) (any, error) {
	if err := c.RequirePlatformAdmin(); err != nil {
		return nil, err
	}
	var in modelIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	m := &store.Model{ID: util.NewID("mdl"), Status: "active", Strategy: "priority", Source: "manual", Aliases: []string{}, Capabilities: []string{}}
	applyModelIn(m, &in)
	if m.Name == "" {
		return nil, badReq("name is required")
	}
	if err := a.St.Insert(c, a.St.Pool, "models", m); err != nil {
		return nil, err
	}
	c.Audit("model.create", "", "model", m.ID, m)
	a.changed(c)
	return created{m}, nil
}

func (a *API) updateModel(c *Ctx) (any, error) {
	if err := c.RequirePlatformAdmin(); err != nil {
		return nil, err
	}
	m, err := store.GetByID[store.Model](c, a.St.Pool, "models", c.Param("id"))
	if err != nil {
		return nil, err
	}
	before := *m
	var in modelIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	applyModelIn(m, &in)
	if m.Name != before.Name {
		var provs []string
		rows, err := a.St.Pool.Query(c, `SELECT DISTINCT provider_id FROM deployments WHERE model_id=$1`, m.ID)
		if err == nil {
			for rows.Next() {
				var id string
				_ = rows.Scan(&id)
				provs = append(provs, id)
			}
			rows.Close()
		}
		for _, pid := range provs {
			if err := a.checkModelSuffix(c, pid, m.Name); err != nil {
				return nil, err
			}
		}
	}
	if err := a.St.Update(c, a.St.Pool, "models", m); err != nil {
		return nil, err
	}
	c.Audit("model.update", "", "model", m.ID, map[string]any{"before": before, "after": m})
	a.changed(c)
	return m, nil
}

func (a *API) deleteModel(c *Ctx) (any, error) {
	if err := c.RequirePlatformAdmin(); err != nil {
		return nil, err
	}
	m, err := store.GetByID[store.Model](c, a.St.Pool, "models", c.Param("id"))
	if err != nil {
		return nil, err
	}
	if err := a.St.Delete(c, a.St.Pool, "models", m.ID); err != nil {
		return nil, err
	}
	c.Audit("model.delete", "", "model", m.ID, map[string]string{"name": m.Name})
	a.changed(c)
	return nil, nil
}

type deploymentIn struct {
	ProviderID    *string `json:"provider_id"`
	UpstreamModel *string `json:"upstream_model"`
	Weight        *int    `json:"weight"`
	Priority      *int    `json:"priority"`
	Enabled       *bool   `json:"enabled"`
}

// deploymentPerm: platform admins manage all; org admins manage deployments on their own providers.
func (a *API) deploymentPerm(c *Ctx, providerID string) error {
	p, err := store.GetByID[store.Provider](c, a.St.Pool, "providers", providerID)
	if err != nil {
		return badReq("provider not found")
	}
	return a.providerPerm(c, p.OrgID)
}

func (a *API) createDeployment(c *Ctx) (any, error) {
	var in deploymentIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if util.Deref(in.ProviderID) == "" || util.Deref(in.UpstreamModel) == "" {
		return nil, badReq("provider_id and upstream_model are required")
	}
	if err := a.deploymentPerm(c, *in.ProviderID); err != nil {
		return nil, err
	}
	m, err := store.GetByID[store.Model](c, a.St.Pool, "models", c.Param("id"))
	if err != nil {
		return nil, err
	}
	if err := a.checkModelSuffix(c, *in.ProviderID, m.Name); err != nil {
		return nil, err
	}
	d := &store.Deployment{ID: util.NewID("dep"), ModelID: m.ID, ProviderID: *in.ProviderID, UpstreamModel: *in.UpstreamModel, Weight: 1, Enabled: true}
	if in.Weight != nil {
		d.Weight = max(*in.Weight, 1)
	}
	if in.Priority != nil {
		d.Priority = *in.Priority
	}
	if in.Enabled != nil {
		d.Enabled = *in.Enabled
	}
	if err := a.St.Insert(c, a.St.Pool, "deployments", d); err != nil {
		return nil, err
	}
	c.Audit("deployment.create", "", "deployment", d.ID, d)
	a.changed(c)
	return created{d}, nil
}

func (a *API) updateDeployment(c *Ctx) (any, error) {
	d, err := store.GetByID[store.Deployment](c, a.St.Pool, "deployments", c.Param("id"))
	if err != nil {
		return nil, err
	}
	if err := a.deploymentPerm(c, d.ProviderID); err != nil {
		return nil, err
	}
	var in deploymentIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if in.UpstreamModel != nil {
		d.UpstreamModel = *in.UpstreamModel
	}
	if in.Weight != nil {
		d.Weight = max(*in.Weight, 1)
	}
	if in.Priority != nil {
		d.Priority = *in.Priority
	}
	if in.Enabled != nil {
		d.Enabled = *in.Enabled
	}
	if err := a.St.Update(c, a.St.Pool, "deployments", d); err != nil {
		return nil, err
	}
	c.Audit("deployment.update", "", "deployment", d.ID, d)
	a.changed(c)
	return d, nil
}

func (a *API) deleteDeployment(c *Ctx) (any, error) {
	d, err := store.GetByID[store.Deployment](c, a.St.Pool, "deployments", c.Param("id"))
	if err != nil {
		return nil, err
	}
	if err := a.deploymentPerm(c, d.ProviderID); err != nil {
		return nil, err
	}
	if err := a.St.Delete(c, a.St.Pool, "deployments", d.ID); err != nil {
		return nil, err
	}
	c.Audit("deployment.delete", "", "deployment", d.ID, d)
	a.changed(c)
	return nil, nil
}

// importModels adds discovered upstream models to the catalog in one step,
// creating the public model if needed and a deployment on the provider.
func (a *API) importModels(c *Ctx) (any, error) {
	var in struct {
		ProviderID string `json:"provider_id"`
		Items      []struct {
			UpstreamModel string `json:"upstream_model"`
			Name          string `json:"name"`
			DisplayName   string `json:"display_name"`
		} `json:"items"`
	}
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if err := a.deploymentPerm(c, in.ProviderID); err != nil {
		return nil, err
	}
	var kind string
	_ = a.St.Pool.QueryRow(c, `SELECT kind FROM providers WHERE id=$1`, in.ProviderID).Scan(&kind)
	suffix := a.modelSuffix(c, in.ProviderID)
	var results []map[string]any
	for _, it := range in.Items {
		name := it.Name
		if name == "" {
			name = SuggestName(it.UpstreamModel)
		}
		if !strings.HasSuffix(name, suffix) {
			name += suffix
		}
		m, err := store.Get[store.Model](c, a.St.Pool, `SELECT * FROM models WHERE name=$1`, name)
		if err == util.ErrNotFound {
			if !c.P.IsPlatformAdmin() {
				results = append(results, map[string]any{"name": name, "error": "model does not exist in catalog; ask a platform admin to add it"})
				continue
			}
			fam := "other"
			for _, f := range []string{"opus", "sonnet", "haiku", "fable", "mythos", "gpt", "llama", "nova", "mistral"} {
				if strings.Contains(name, f) {
					fam = f
					break
				}
			}
			m = &store.Model{ID: util.NewID("mdl"), Name: name, DisplayName: it.DisplayName, Family: fam, Status: "active", Strategy: "priority", Source: "discovered", Aliases: []string{}, Capabilities: []string{}}
			if err := a.St.Insert(c, a.St.Pool, "models", m); err != nil {
				results = append(results, map[string]any{"name": name, "error": err.Error()})
				continue
			}
		} else if err != nil {
			return nil, err
		}
		d := &store.Deployment{ID: util.NewID("dep"), ModelID: m.ID, ProviderID: in.ProviderID, UpstreamModel: it.UpstreamModel, Weight: 1, Enabled: true}
		if err := a.St.Insert(c, a.St.Pool, "deployments", d); err != nil {
			results = append(results, map[string]any{"name": name, "error": err.Error()})
			continue
		}
		results = append(results, map[string]any{"name": name, "model_id": m.ID, "deployment_id": d.ID, "priced": a.Pricing.QuoteFor(kind, it.UpstreamModel) != nil})
	}
	c.Audit("model.import", "", "provider", in.ProviderID, results)
	a.changed(c)
	return results, nil
}

// modelSuffix is the public-name suffix required for models routed through a
// custom credential provider ("@acme"), or "".
func (a *API) modelSuffix(ctx context.Context, providerID string) string {
	if e := a.Loader.Snapshot().Providers[providerID]; e != nil && e.P != nil {
		return providers.ModelSuffix(e.P)
	}
	p, err := store.GetByID[store.Provider](ctx, a.St.Pool, "providers", providerID)
	if err != nil || p.Kind != providers.KindCustom {
		return ""
	}
	conn, err := a.Box.OpenString(p.ConnEnc)
	if err != nil {
		return ""
	}
	return providers.ModelSuffixFor(conn)
}

func (a *API) checkModelSuffix(ctx context.Context, providerID, modelName string) error {
	if s := a.modelSuffix(ctx, providerID); s != "" && !strings.HasSuffix(modelName, s) {
		return badReq(fmt.Sprintf("models routed through credential %q must be named <model>%s (got %q)", strings.TrimPrefix(s, providers.ModelSuffixSep), s, modelName))
	}
	return nil
}
