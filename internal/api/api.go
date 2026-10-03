// Package api is the control-plane REST API consumed by the web console,
// automation (personal access tokens) and SCIM provisioning.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"theophylax/internal/audit"
	"theophylax/internal/auth"
	"theophylax/internal/config"
	"theophylax/internal/gateway"
	"theophylax/internal/guardrails"
	"theophylax/internal/receipts"
	"theophylax/internal/secrets"
	"theophylax/internal/store"
	"theophylax/internal/util"
)

type API struct {
	Cfg     *config.Config
	St      *store.Store
	Box     *secrets.Box
	Res     *auth.Resolver
	Audit   *audit.Logger
	Loader  *gateway.Loader
	Guard   *guardrails.Engine
	Budgets *gateway.Budgets
	Signer  *receipts.Signer
}

type Ctx struct {
	context.Context
	W http.ResponseWriter
	R *http.Request
	P *auth.Principal
	a *API
}

func (c *Ctx) Param(n string) string { return c.R.PathValue(n) }
func (c *Ctx) Query(n string) string { return c.R.URL.Query().Get(n) }

func (c *Ctx) Decode(v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(c.W, c.R.Body, 8<<20))
	if err := dec.Decode(v); err != nil {
		return util.Err(400, "invalid_request_error", "invalid JSON: "+err.Error())
	}
	return nil
}

func (c *Ctx) actor() audit.Actor {
	a := audit.Actor{Type: "system", IP: util.ClientIP(c.R, c.a.Cfg.TrustedProxies), UA: c.R.UserAgent()}
	if c.P != nil {
		if c.P.User != nil {
			a.Type, a.ID, a.Email = "user", c.P.User.ID, c.P.User.Email
			if c.P.Via == "pat" {
				a.Type = "pat"
			}
		} else if c.P.Key != nil {
			a.Type, a.ID = "key", c.P.Key.ID
		}
	}
	return a
}

// Audit records a control-plane event.
func (c *Ctx) Audit(action, orgID, targetType, targetID string, details any) {
	c.a.Audit.Record(c, audit.Event{Actor: c.actor(), OrgID: orgID, Action: action, TargetType: targetType, TargetID: targetID, Details: details})
}

// Require enforces a minimum effective role at a scope.
func (c *Ctx) Require(s auth.Scope, role string) error {
	if !c.P.Can(s, role) {
		return util.ErrForbidden
	}
	return nil
}

func (c *Ctx) RequirePlatformAdmin() error {
	if !c.P.IsPlatformAdmin() {
		return util.Err(403, "permission_error", "platform administrator role required")
	}
	return nil
}

type created struct{ v any }

func (a *API) authenticate(r *http.Request) (*auth.Principal, error) {
	tok := auth.BearerToken(r)
	if strings.HasPrefix(tok, auth.PATPrefix) {
		return a.Res.FromKey(r.Context(), tok, "pat")
	}
	if tok != "" {
		return nil, util.Err(401, "authentication_error", "use a personal access token (tpxp_…) or a browser session for the admin API")
	}
	p, err := a.Res.FromSession(r.Context(), r)
	if err != nil {
		return nil, err
	}
	// CSRF: cookie-authenticated mutations must carry a custom header, which
	// browsers will not send cross-site without a CORS preflight.
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-Theophylax-Client") != "web" {
		return nil, util.Err(403, "permission_error", "missing X-Theophylax-Client header")
	}
	return p, nil
}

func (a *API) handle(fn func(*Ctx) (any, error)) http.HandlerFunc {
	return a.wrap(true, fn)
}

func (a *API) public(fn func(*Ctx) (any, error)) http.HandlerFunc {
	return a.wrap(false, fn)
}

func (a *API) wrap(needAuth bool, fn func(*Ctx) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := &Ctx{Context: r.Context(), W: w, R: r, a: a}
		if needAuth {
			p, err := a.authenticate(r)
			if err != nil {
				util.WriteError(w, err)
				return
			}
			c.P = p
		}
		w.Header().Set("Cache-Control", "no-store")
		v, err := fn(c)
		if err != nil {
			var he *util.HTTPError
			if !errors.As(err, &he) {
				slog.Error("api error", "path", r.URL.Path, "err", err)
				err = util.Err(500, "api_error", "internal error")
			}
			util.WriteError(w, err)
			return
		}
		switch x := v.(type) {
		case nil:
			w.WriteHeader(http.StatusNoContent)
		case created:
			util.WriteJSON(w, http.StatusCreated, x.v)
		case rawResponse:
			// handler already wrote the response
		default:
			util.WriteJSON(w, http.StatusOK, v)
		}
	}
}

type rawResponse struct{}

// changed propagates config changes to every replica.
func (a *API) changed(ctx context.Context) {
	gateway.NotifyChange(ctx, a.St.Pool)
	a.Loader.Kick()
}

func (a *API) Routes(mux *http.ServeMux) {
	h, pub := a.handle, a.public
	// Auth & identity
	mux.HandleFunc("GET /api/auth/config", pub(a.authConfig))
	mux.HandleFunc("POST /api/auth/login", pub(a.login))
	mux.HandleFunc("POST /api/auth/logout", pub(a.logout))
	mux.HandleFunc("GET /auth/oidc/{idp}/start", a.oidcStart)
	mux.HandleFunc("GET /auth/oidc/callback", a.oidcCallback)
	mux.HandleFunc("GET /api/me", h(a.me))
	mux.HandleFunc("POST /api/me/password", h(a.changePassword))

	// Hierarchy
	mux.HandleFunc("GET /api/orgs", h(a.listOrgs))
	mux.HandleFunc("POST /api/orgs", h(a.createOrg))
	mux.HandleFunc("GET /api/orgs/{org}", h(a.getOrg))
	mux.HandleFunc("PATCH /api/orgs/{org}", h(a.updateOrg))
	mux.HandleFunc("DELETE /api/orgs/{org}", h(a.deleteOrg))
	mux.HandleFunc("GET /api/orgs/{org}/overview", h(a.overview))
	mux.HandleFunc("GET /api/orgs/{org}/teams", h(a.listTeams))
	mux.HandleFunc("POST /api/orgs/{org}/teams", h(a.createTeam))
	mux.HandleFunc("GET /api/teams/{id}", h(a.getTeam))
	mux.HandleFunc("PATCH /api/teams/{id}", h(a.updateTeam))
	mux.HandleFunc("DELETE /api/teams/{id}", h(a.deleteTeam))
	mux.HandleFunc("GET /api/orgs/{org}/projects", h(a.listProjects))
	mux.HandleFunc("POST /api/teams/{id}/projects", h(a.createProject))
	mux.HandleFunc("GET /api/projects/{id}", h(a.getProject))
	mux.HandleFunc("PATCH /api/projects/{id}", h(a.updateProject))
	mux.HandleFunc("DELETE /api/projects/{id}", h(a.deleteProject))
	mux.HandleFunc("GET /api/members", h(a.listMembers))
	mux.HandleFunc("POST /api/members", h(a.addMember))
	mux.HandleFunc("PATCH /api/members/{id}", h(a.updateMember))
	mux.HandleFunc("DELETE /api/members/{id}", h(a.removeMember))
	mux.HandleFunc("GET /api/orgs/{org}/users", h(a.orgUsers))
	mux.HandleFunc("GET /api/users", h(a.listUsers))
	mux.HandleFunc("POST /api/users", h(a.createUser))
	mux.HandleFunc("PATCH /api/users/{id}", h(a.updateUser))

	// Keys
	mux.HandleFunc("GET /api/orgs/{org}/keys", h(a.listKeys))
	mux.HandleFunc("POST /api/keys", h(a.createKey))
	mux.HandleFunc("PATCH /api/keys/{id}", h(a.updateKey))
	mux.HandleFunc("POST /api/keys/{id}/revoke", h(a.revokeKey))
	mux.HandleFunc("POST /api/keys/{id}/rotate", h(a.rotateKey))

	// Providers & model catalog
	mux.HandleFunc("GET /api/providers", h(a.listProviders))
	mux.HandleFunc("POST /api/providers", h(a.createProvider))
	mux.HandleFunc("PATCH /api/providers/{id}", h(a.updateProvider))
	mux.HandleFunc("DELETE /api/providers/{id}", h(a.deleteProvider))
	mux.HandleFunc("POST /api/providers/{id}/discover", h(a.discoverModels))
	mux.HandleFunc("POST /api/providers/validate", h(a.validateConn))
	mux.HandleFunc("GET /api/models", h(a.listModels))
	mux.HandleFunc("POST /api/models", h(a.createModel))
	mux.HandleFunc("PATCH /api/models/{id}", h(a.updateModel))
	mux.HandleFunc("DELETE /api/models/{id}", h(a.deleteModel))
	mux.HandleFunc("POST /api/models/{id}/deployments", h(a.createDeployment))
	mux.HandleFunc("PATCH /api/deployments/{id}", h(a.updateDeployment))
	mux.HandleFunc("DELETE /api/deployments/{id}", h(a.deleteDeployment))
	mux.HandleFunc("POST /api/models/import", h(a.importModels))

	// Policy
	mux.HandleFunc("GET /api/orgs/{org}/guardrails", h(a.listGuardrails))
	mux.HandleFunc("POST /api/orgs/{org}/guardrails", h(a.createGuardrail))
	mux.HandleFunc("PATCH /api/guardrails/{id}", h(a.updateGuardrail))
	mux.HandleFunc("DELETE /api/guardrails/{id}", h(a.deleteGuardrail))
	mux.HandleFunc("POST /api/guardrails/test", h(a.testGuardrail))
	mux.HandleFunc("GET /api/guardrails/catalog", h(a.guardrailCatalog))
	mux.HandleFunc("GET /api/orgs/{org}/budgets", h(a.listBudgets))
	mux.HandleFunc("POST /api/orgs/{org}/budgets", h(a.createBudget))
	mux.HandleFunc("PATCH /api/budgets/{id}", h(a.updateBudget))
	mux.HandleFunc("DELETE /api/budgets/{id}", h(a.deleteBudget))

	// Identity providers
	mux.HandleFunc("GET /api/orgs/{org}/idps", h(a.listIdPs))
	mux.HandleFunc("POST /api/orgs/{org}/idps", h(a.createIdP))
	mux.HandleFunc("PATCH /api/idps/{id}", h(a.updateIdP))
	mux.HandleFunc("DELETE /api/idps/{id}", h(a.deleteIdP))
	mux.HandleFunc("POST /api/idps/{id}/scim-token", h(a.scimToken))

	// Registry, marketplace, agents
	mux.HandleFunc("GET /api/orgs/{org}/registry", h(a.listRegistry))
	mux.HandleFunc("POST /api/orgs/{org}/registry", h(a.createRegistryItem))
	mux.HandleFunc("GET /api/registry/{id}", h(a.getRegistryItem))
	mux.HandleFunc("PATCH /api/registry/{id}", h(a.updateRegistryItem))
	mux.HandleFunc("DELETE /api/registry/{id}", h(a.deleteRegistryItem))
	mux.HandleFunc("POST /api/registry/{id}/versions", h(a.publishVersion))
	mux.HandleFunc("POST /api/registry/{id}/submit", h(a.submitForReview))
	mux.HandleFunc("POST /api/registry/{id}/review", h(a.reviewItem))
	mux.HandleFunc("POST /api/registry/{id}/install", h(a.installItem))
	mux.HandleFunc("DELETE /api/installations/{id}", h(a.uninstall))
	mux.HandleFunc("POST /api/registry/{id}/rate", h(a.rateItem))
	mux.HandleFunc("GET /api/registry/{id}/download", a.downloadItem)
	mux.HandleFunc("GET /api/orgs/{org}/agents", h(a.listAgents))
	mux.HandleFunc("POST /api/orgs/{org}/agents", h(a.createAgent))
	mux.HandleFunc("GET /api/agents/{id}", h(a.getAgent))
	mux.HandleFunc("PATCH /api/agents/{id}", h(a.updateAgent))
	mux.HandleFunc("DELETE /api/agents/{id}", h(a.deleteAgent))

	// Observability
	mux.HandleFunc("GET /api/orgs/{org}/logs", h(a.listLogs))
	mux.HandleFunc("GET /api/logs/{id}", h(a.getLog))
	mux.HandleFunc("GET /api/orgs/{org}/usage", h(a.usage))
	mux.HandleFunc("GET /api/orgs/{org}/audit", h(a.orgAudit))
	mux.HandleFunc("GET /api/audit", h(a.platformAudit))
	mux.HandleFunc("GET /api/audit/verify", h(a.verifyAudit))
	mux.HandleFunc("GET /api/system", h(a.system))
	mux.HandleFunc("GET /api/receipts/{id}", h(a.getReceipt))
	mux.HandleFunc("GET /api/receipts/checkpoints", h(a.listCheckpoints))

	// Model rollouts
	a.rolloutRoutes(mux)

	// SCIM 2.0
	a.scimRoutes(mux)
}

// ---------------------------------------------------------------------------
// Scope resolution helpers

func (a *API) orgByRef(ctx context.Context, ref string) (*store.Org, error) {
	return store.Get[store.Org](ctx, a.St.Pool, `SELECT * FROM orgs WHERE id=$1 OR slug=$1`, ref)
}

// orgCtx resolves {org} and requires a minimum org-level role ("" = any membership).
func (a *API) orgCtx(c *Ctx, minRole string) (*store.Org, auth.Scope, error) {
	o, err := a.orgByRef(c, c.Param("org"))
	if err != nil {
		return nil, auth.Scope{}, err
	}
	s := auth.Scope{Type: "org", ID: o.ID, OrgID: o.ID}
	if minRole == "" {
		if !c.P.InOrg(o.ID) {
			return nil, s, util.ErrNotFound
		}
		return o, s, nil
	}
	if err := c.Require(s, minRole); err != nil {
		if !c.P.InOrg(o.ID) {
			return nil, s, util.ErrNotFound
		}
		return nil, s, err
	}
	return o, s, nil
}

func (a *API) scopeOf(ctx context.Context, typ, id string) (auth.Scope, error) {
	switch typ {
	case "org":
		o, err := a.orgByRef(ctx, id)
		if err != nil {
			return auth.Scope{}, err
		}
		return auth.Scope{Type: "org", ID: o.ID, OrgID: o.ID}, nil
	case "team":
		t, err := store.GetByID[store.Team](ctx, a.St.Pool, "teams", id)
		if err != nil {
			return auth.Scope{}, err
		}
		return auth.Scope{Type: "team", ID: t.ID, OrgID: t.OrgID, TeamID: t.ID}, nil
	case "project":
		p, err := store.GetByID[store.Project](ctx, a.St.Pool, "projects", id)
		if err != nil {
			return auth.Scope{}, err
		}
		return auth.Scope{Type: "project", ID: p.ID, OrgID: p.OrgID, TeamID: p.TeamID}, nil
	}
	return auth.Scope{}, util.Err(400, "invalid_request_error", "scope_type must be org, team or project")
}

func jsonOr(raw json.RawMessage, def string) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(def)
	}
	return raw
}

func badReq(msg string) error { return util.Err(400, "invalid_request_error", msg) }

func since(q string, def time.Duration) time.Time {
	if d, err := time.ParseDuration(q); err == nil && d > 0 {
		return time.Now().Add(-d)
	}
	return time.Now().Add(-def)
}
