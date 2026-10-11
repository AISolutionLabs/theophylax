package api

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"theophylax/internal/auth"
	"theophylax/internal/gateway"
	"theophylax/internal/guardrails"
	"theophylax/internal/store"
	"theophylax/internal/util"
)

// --- Guardrails

type guardrailView struct {
	store.GuardrailPolicy
	ScopeName string `json:"scope_name"`
	CanEdit   bool   `json:"can_edit"`
}

func (a *API) scopeName(snap *gateway.Snapshot, typ, id string) string {
	switch typ {
	case "org":
		if o := snap.Orgs[id]; o != nil {
			return o.Name
		}
	case "team":
		if t := snap.Teams[id]; t != nil {
			return t.Name
		}
	case "project":
		if p := snap.Projects[id]; p != nil {
			if t := snap.Teams[p.TeamID]; t != nil {
				return t.Name + " / " + p.Name
			}
			return p.Name
		}
	case "key":
		return "API key"
	case "user":
		return "User"
	}
	return id
}

func (a *API) listGuardrails(c *Ctx) (any, error) {
	o, _, err := a.orgCtx(c, "")
	if err != nil {
		return nil, err
	}
	list, err := store.List[store.GuardrailPolicy](c, a.St.Pool, `SELECT * FROM guardrail_policies WHERE org_id=$1 ORDER BY CASE scope_type WHEN 'org' THEN 0 WHEN 'team' THEN 1 ELSE 2 END, created_at`, o.ID)
	if err != nil {
		return nil, err
	}
	snap := a.Loader.Snapshot()
	out := []guardrailView{}
	for _, g := range list {
		s, err := a.scopeOf(c, g.ScopeType, g.ScopeID)
		if err != nil || !c.P.Can(s, "viewer") && g.ScopeType != "org" {
			continue
		}
		out = append(out, guardrailView{GuardrailPolicy: g, ScopeName: a.scopeName(snap, g.ScopeType, g.ScopeID), CanEdit: c.P.Can(s, "admin")})
	}
	return out, nil
}

type guardrailIn struct {
	ScopeType   *string          `json:"scope_type"`
	ScopeID     *string          `json:"scope_id"`
	Name        *string          `json:"name"`
	Description *string          `json:"description"`
	Enabled     *bool            `json:"enabled"`
	Mode        *string          `json:"mode"`
	Rules       *json.RawMessage `json:"rules"`
}

func (a *API) createGuardrail(c *Ctx) (any, error) {
	o, _, err := a.orgCtx(c, "")
	if err != nil {
		return nil, err
	}
	var in guardrailIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	st, sid := util.Deref(in.ScopeType), util.Deref(in.ScopeID)
	if st == "" {
		st, sid = "org", o.ID
	}
	s, err := a.scopeOf(c, st, sid)
	if err != nil || s.OrgID != o.ID {
		return nil, badReq("invalid scope")
	}
	if err := c.Require(s, "admin"); err != nil {
		return nil, err
	}
	g := &store.GuardrailPolicy{ID: util.NewID("grd"), OrgID: o.ID, ScopeType: s.Type, ScopeID: s.ID, Name: util.Deref(in.Name), Description: util.Deref(in.Description), Enabled: in.Enabled == nil || *in.Enabled, Mode: "enforce", Rules: json.RawMessage("[]")}
	if in.Mode != nil {
		g.Mode = *in.Mode
	}
	if in.Rules != nil {
		if _, err := guardrails.ParseRules(*in.Rules); err != nil {
			return nil, badReq(err.Error())
		}
		g.Rules = *in.Rules
	}
	if g.Name == "" {
		return nil, badReq("name is required")
	}
	if err := a.St.Insert(c, a.St.Pool, "guardrail_policies", g); err != nil {
		return nil, err
	}
	c.Audit("guardrail.create", o.ID, "guardrail", g.ID, g)
	a.changed(c)
	return created{g}, nil
}

func (a *API) guardrailCtx(c *Ctx) (*store.GuardrailPolicy, error) {
	g, err := store.GetByID[store.GuardrailPolicy](c, a.St.Pool, "guardrail_policies", c.Param("id"))
	if err != nil {
		return nil, err
	}
	s, err := a.scopeOf(c, g.ScopeType, g.ScopeID)
	if err != nil {
		return nil, err
	}
	return g, c.Require(s, "admin")
}

func (a *API) updateGuardrail(c *Ctx) (any, error) {
	g, err := a.guardrailCtx(c)
	if err != nil {
		return nil, err
	}
	before := *g
	var in guardrailIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if in.Name != nil {
		g.Name = *in.Name
	}
	if in.Description != nil {
		g.Description = *in.Description
	}
	if in.Enabled != nil {
		g.Enabled = *in.Enabled
	}
	if in.Mode != nil {
		g.Mode = *in.Mode
	}
	if in.Rules != nil {
		if _, err := guardrails.ParseRules(*in.Rules); err != nil {
			return nil, badReq(err.Error())
		}
		g.Rules = *in.Rules
	}
	if err := a.St.Update(c, a.St.Pool, "guardrail_policies", g); err != nil {
		return nil, err
	}
	c.Audit("guardrail.update", g.OrgID, "guardrail", g.ID, map[string]any{"before": before, "after": g})
	a.changed(c)
	return g, nil
}

func (a *API) deleteGuardrail(c *Ctx) (any, error) {
	g, err := a.guardrailCtx(c)
	if err != nil {
		return nil, err
	}
	if err := a.St.Delete(c, a.St.Pool, "guardrail_policies", g.ID); err != nil {
		return nil, err
	}
	c.Audit("guardrail.delete", g.OrgID, "guardrail", g.ID, g)
	a.changed(c)
	return nil, nil
}

// testGuardrail dry-runs rules against sample text (no side effects).
func (a *API) testGuardrail(c *Ctx) (any, error) {
	var in struct {
		Rules json.RawMessage `json:"rules"`
		Mode  string          `json:"mode"`
		Text  string          `json:"text"`
		Phase string          `json:"phase"`
		Model string          `json:"model"`
	}
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	rules, err := guardrails.ParseRules(in.Rules)
	if err != nil {
		return nil, badReq(err.Error())
	}
	if in.Phase == "" {
		in.Phase = "input"
	}
	body := map[string]any{"model": in.Model, "max_tokens": 1024, "messages": []any{map[string]any{"role": "user", "content": in.Text}}}
	res := a.Guard.Evaluate(c, []guardrails.Policy{{Name: "test", Mode: in.Mode, Rules: rules}}, guardrails.Input{Phase: in.Phase, Model: in.Model, Body: body, Texts: guardrails.RequestTexts(body, false)})
	out := body["messages"].([]any)[0].(map[string]any)["content"]
	if res.Findings == nil {
		res.Findings = []guardrails.Finding{}
	}
	return map[string]any{"blocked": res.Blocked, "message": res.Message, "findings": res.Findings, "output": out, "max_tokens": body["max_tokens"]}, nil
}

func (a *API) guardrailCatalog(c *Ctx) (any, error) {
	pii, sec := guardrails.Entities()
	sort.Strings(pii)
	sort.Strings(sec)
	return map[string]any{
		"entities": pii, "secret_entities": sec, "default_secret_entities": guardrails.DefaultSecretEntities,
		"types": []map[string]any{
			{"type": "secrets", "label": "Secret Shield", "description": "Redact credentials (cloud keys, tokens, private keys, passwords, connection strings) before they reach any model. Optionally restore them in responses.", "actions": []string{"redact", "block", "log"}},
			{"type": "pii", "label": "Personal data", "description": "Detect personal data (email, cards, national IDs, IBAN); redact, block or log.", "actions": []string{"redact", "block", "log"}},
			{"type": "prompt_injection", "label": "Prompt injection", "description": "Native multi-layer detection (de-obfuscation, multilingual, encoded and indirect attacks via tool results), optional judge model, canary tokens, exfiltration and tool-call guards.", "actions": []string{"block", "log"}},
			{"type": "keywords", "label": "Keyword deny-list", "description": "Block or redact specific words or codenames.", "actions": []string{"block", "redact", "log"}},
			{"type": "regex", "label": "Regex rule", "description": "Custom regular expressions for org-specific patterns.", "actions": []string{"block", "redact", "log"}},
			{"type": "model_allowlist", "label": "Model allow-list", "description": "Only allow listed models (wildcards supported)."},
			{"type": "max_tokens", "label": "Output token cap", "description": "Cap max_tokens on every request.", "actions": []string{"cap", "block"}},
			{"type": "max_input_chars", "label": "Input size limit", "description": "Reject oversized prompts."},
			{"type": "webhook", "label": "External webhook", "description": "Call your own classifier for allow/block/redact verdicts. Headers can reference secrets with ${env:NAME}."},
			{"type": "lakera", "label": "Lakera Guard", "description": "Prompt attacks, PII and content checks from your Lakera Guard policy."},
			{"type": "azure_prompt_shield", "label": "Azure Prompt Shields", "description": "Azure AI Content Safety detection of direct and indirect (document or tool result) prompt attacks."},
			{"type": "azure_content_safety", "label": "Azure Content Safety", "description": "Hate, sexual, violence and self-harm classification with a severity threshold."},
			{"type": "openai_moderation", "label": "OpenAI moderation", "description": "omni-moderation via OpenAI or any compatible /moderations endpoint."},
			{"type": "bedrock_guardrail", "label": "AWS Bedrock Guardrail", "description": "Apply a Bedrock Guardrail via an existing Bedrock connection."},
		},
	}, nil
}

// --- Budgets

type budgetView struct {
	store.Budget
	ScopeName   string  `json:"scope_name"`
	SpentUSD    float64 `json:"spent_usd"`
	SpentTokens int64   `json:"spent_tokens"`
	CanEdit     bool    `json:"can_edit"`
}

func (a *API) budgetScope(c *Ctx, typ, id string) (auth.Scope, error) {
	switch typ {
	case "key":
		k, err := store.GetByID[store.APIKey](c, a.St.Pool, "api_keys", id)
		if err != nil {
			return auth.Scope{}, err
		}
		return keyScope(k), nil
	case "user":
		return auth.Scope{}, util.Err(400, "invalid_request_error", "user budgets are set at org scope with scope_type=user")
	}
	return a.scopeOf(c, typ, id)
}

func (a *API) listBudgets(c *Ctx) (any, error) {
	o, os, err := a.orgCtx(c, "")
	if err != nil {
		return nil, err
	}
	list, err := store.List[store.Budget](c, a.St.Pool, `SELECT * FROM budgets WHERE org_id=$1 ORDER BY scope_type, created_at`, o.ID)
	if err != nil {
		return nil, err
	}
	snap := a.Loader.Snapshot()
	out := []budgetView{}
	for _, b := range list {
		s := os
		if b.ScopeType != "user" {
			if s, err = a.budgetScope(c, b.ScopeType, b.ScopeID); err != nil {
				continue
			}
		}
		if !c.P.Can(s, "viewer") && !(b.ScopeType == "user" && b.ScopeID == c.P.UserID()) {
			continue
		}
		var cost float64
		var tok int64
		if !b.PerUser {
			cost, tok = a.Budgets.Spent(&b)
		}
		name := a.scopeName(snap, b.ScopeType, b.ScopeID)
		if b.ScopeType == "user" {
			if u, err := a.St.UserByID(c, b.ScopeID); err == nil {
				name = u.Email
			}
		}
		out = append(out, budgetView{Budget: b, ScopeName: name, SpentUSD: cost, SpentTokens: tok, CanEdit: c.P.Can(s, "admin")})
	}
	return out, nil
}

type budgetIn struct {
	ScopeType   *string  `json:"scope_type"`
	ScopeID     *string  `json:"scope_id"`
	Period      *string  `json:"period"`
	LimitUSD    *float64 `json:"limit_usd"`
	LimitTokens *int64   `json:"limit_tokens"`
	AlertPct    *int     `json:"alert_pct"`
	Action      *string  `json:"action"`
	ClearUSD    bool     `json:"clear_usd"`
	ClearTokens bool     `json:"clear_tokens"`
	PerUser     bool     `json:"per_user"`
}

func applyBudgetIn(b *store.Budget, in *budgetIn) {
	if in.Period != nil {
		b.Period = *in.Period
	}
	if in.ClearUSD {
		b.LimitUSD = nil
	}
	if in.ClearTokens {
		b.LimitTokens = nil
	}
	if in.LimitUSD != nil {
		b.LimitUSD = in.LimitUSD
	}
	if in.LimitTokens != nil {
		b.LimitTokens = in.LimitTokens
	}
	if in.AlertPct != nil {
		b.AlertPct = *in.AlertPct
	}
	if in.Action != nil {
		b.Action = *in.Action
	}
}

func (a *API) createBudget(c *Ctx) (any, error) {
	o, os, err := a.orgCtx(c, "")
	if err != nil {
		return nil, err
	}
	var in budgetIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	st, sid := util.Deref(in.ScopeType), util.Deref(in.ScopeID)
	var s auth.Scope
	if in.PerUser && st != "org" && st != "team" {
		return nil, badReq("per-person budgets are set on an org or a team")
	}
	if st == "user" {
		s = os
		var member bool
		_ = a.St.Pool.QueryRow(c, `SELECT EXISTS(SELECT 1 FROM memberships WHERE user_id=$1 AND org_id=$2)`, sid, o.ID).Scan(&member)
		if !member {
			return nil, badReq("user is not a member of this organization")
		}
	} else {
		if s, err = a.budgetScope(c, st, sid); err != nil || s.OrgID != o.ID {
			return nil, badReq("invalid scope")
		}
		sid = s.ID
		if st == "key" {
			sid = util.Deref(in.ScopeID)
		}
	}
	if err := c.Require(s, "admin"); err != nil {
		return nil, err
	}
	b := &store.Budget{ID: util.NewID("bud"), OrgID: o.ID, ScopeType: st, ScopeID: sid, Period: "monthly", AlertPct: 80, Action: "block", PerUser: in.PerUser}
	applyBudgetIn(b, &in)
	if b.LimitUSD == nil && b.LimitTokens == nil {
		return nil, badReq("set limit_usd and/or limit_tokens")
	}
	var dup bool
	_ = a.St.Pool.QueryRow(c, `SELECT EXISTS(SELECT 1 FROM budgets WHERE scope_type=$1 AND scope_id=$2 AND period=$3 AND per_user=$4)`, b.ScopeType, b.ScopeID, b.Period, b.PerUser).Scan(&dup)
	if dup {
		return nil, util.Err(409, "invalid_request_error", "a "+b.Period+" budget already exists for this scope; edit it instead")
	}
	if err := a.St.Insert(c, a.St.Pool, "budgets", b); err != nil {
		return nil, err
	}
	c.Audit("budget.create", o.ID, "budget", b.ID, b)
	a.changed(c)
	return created{b}, nil
}

func (a *API) budgetCtx(c *Ctx) (*store.Budget, error) {
	b, err := store.GetByID[store.Budget](c, a.St.Pool, "budgets", c.Param("id"))
	if err != nil {
		return nil, err
	}
	s := auth.Scope{Type: "org", ID: b.OrgID, OrgID: b.OrgID}
	if b.ScopeType != "user" {
		if s, err = a.budgetScope(c, b.ScopeType, b.ScopeID); err != nil {
			return nil, err
		}
	}
	return b, c.Require(s, "admin")
}

func (a *API) updateBudget(c *Ctx) (any, error) {
	b, err := a.budgetCtx(c)
	if err != nil {
		return nil, err
	}
	var in budgetIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	applyBudgetIn(b, &in)
	if err := a.St.Update(c, a.St.Pool, "budgets", b); err != nil {
		return nil, err
	}
	c.Audit("budget.update", b.OrgID, "budget", b.ID, b)
	a.changed(c)
	return b, nil
}

func (a *API) deleteBudget(c *Ctx) (any, error) {
	b, err := a.budgetCtx(c)
	if err != nil {
		return nil, err
	}
	if err := a.St.Delete(c, a.St.Pool, "budgets", b.ID); err != nil {
		return nil, err
	}
	c.Audit("budget.delete", b.OrgID, "budget", b.ID, b)
	a.changed(c)
	return nil, nil
}

// --- Identity providers (org owners)

type idpIn struct {
	Name          *string          `json:"name"`
	Issuer        *string          `json:"issuer"`
	ClientID      *string          `json:"client_id"`
	ClientSecret  *string          `json:"client_secret"`
	ClearSecret   bool             `json:"clear_secret"`
	Scopes        *[]string        `json:"scopes"`
	GroupsClaim   *string          `json:"groups_claim"`
	EmailDomains  *[]string        `json:"email_domains"`
	JIT           *bool            `json:"jit"`
	DefaultRole   *string          `json:"default_role"`
	RoleMappings  *json.RawMessage `json:"role_mappings"`
	GatewayTokens *bool            `json:"gateway_tokens"`
	Audience      *string          `json:"audience"`
	Enabled       *bool            `json:"enabled"`
}

type idpView struct {
	store.IdP
	HasSecret   bool   `json:"has_secret"`
	HasSCIM     bool   `json:"has_scim_token"`
	RedirectURI string `json:"redirect_uri"`
	SCIMURL     string `json:"scim_url"`
}

func (a *API) idpView(i store.IdP) idpView {
	return idpView{IdP: i, HasSecret: len(i.SecretEnc) > 0, HasSCIM: len(i.SCIMTokenHash) > 0, RedirectURI: a.redirectURI(), SCIMURL: a.Cfg.PublicURL + "/scim/v2"}
}

func (a *API) listIdPs(c *Ctx) (any, error) {
	o, _, err := a.orgCtx(c, "owner")
	if err != nil {
		return nil, err
	}
	list, err := store.List[store.IdP](c, a.St.Pool, `SELECT * FROM idp_providers WHERE org_id=$1 ORDER BY name`, o.ID)
	if err != nil {
		return nil, err
	}
	out := make([]idpView, 0, len(list))
	for _, i := range list {
		out = append(out, a.idpView(i))
	}
	return out, nil
}

func (a *API) applyIdP(ctx context.Context, i *store.IdP, in *idpIn) error {
	if in.Name != nil {
		i.Name = *in.Name
	}
	if in.Issuer != nil {
		i.Issuer = strings.TrimRight(strings.TrimSpace(*in.Issuer), "/")
	}
	if in.ClientID != nil {
		i.ClientID = strings.TrimSpace(*in.ClientID)
	}
	// An omitted or empty secret keeps the stored one; removing it is explicit.
	if in.ClearSecret {
		i.SecretEnc = nil
	} else if in.ClientSecret != nil && *in.ClientSecret != "" {
		i.SecretEnc = a.Box.SealString(*in.ClientSecret)
	}
	if in.Scopes != nil {
		i.Scopes = *in.Scopes
	}
	if in.GroupsClaim != nil {
		i.GroupsClaim = *in.GroupsClaim
	}
	if in.EmailDomains != nil {
		i.EmailDomains = *in.EmailDomains
	}
	if in.JIT != nil {
		i.JIT = *in.JIT
	}
	if in.DefaultRole != nil {
		if *in.DefaultRole != "none" && !auth.ValidRole(*in.DefaultRole) {
			return badReq("default_role must be owner, admin, member, viewer or none")
		}
		i.DefaultRole = *in.DefaultRole
	}
	if in.RoleMappings != nil {
		var maps []store.RoleMapping
		if err := json.Unmarshal(*in.RoleMappings, &maps); err != nil {
			return badReq("role_mappings must be an array of {group, scope_type, scope_id, role}")
		}
		for _, m := range maps {
			if !auth.ValidRole(m.Role) || m.Group == "" {
				return badReq("each role mapping needs a group and a valid role")
			}
			// An IdP belongs to one organization and may only grant roles inside it.
			if !a.Res.ScopeInOrg(ctx, m.ScopeType, m.ScopeID, i.OrgID) {
				return badReq(fmt.Sprintf("role mapping for group %q must target an org, team or project in this identity provider's organization", m.Group))
			}
		}
		i.RoleMappings = *in.RoleMappings
	}
	if in.GatewayTokens != nil {
		i.GatewayTokens = *in.GatewayTokens
	}
	if in.Audience != nil {
		i.Audience = *in.Audience
	}
	if in.Enabled != nil {
		i.Enabled = *in.Enabled
	}
	if i.Name == "" || i.Issuer == "" || i.ClientID == "" {
		return badReq("name, issuer and client_id are required")
	}
	return nil
}

func (a *API) createIdP(c *Ctx) (any, error) {
	o, _, err := a.orgCtx(c, "owner")
	if err != nil {
		return nil, err
	}
	var in idpIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	i := &store.IdP{ID: util.NewID("idp"), OrgID: o.ID, Type: "oidc", Scopes: []string{"openid", "email", "profile"}, GroupsClaim: "groups", EmailDomains: []string{}, JIT: true, DefaultRole: "member", RoleMappings: json.RawMessage("[]"), Enabled: true}
	if err := a.applyIdP(c, i, &in); err != nil {
		return nil, err
	}
	if _, err := a.Res.OIDC.Discover(c, i.Issuer); err != nil {
		return nil, badReq("issuer discovery failed (check the issuer URL): " + err.Error())
	}
	if err := a.St.Insert(c, a.St.Pool, "idp_providers", i); err != nil {
		return nil, err
	}
	c.Audit("idp.create", o.ID, "idp", i.ID, map[string]any{"name": i.Name, "issuer": i.Issuer})
	a.changed(c)
	return created{a.idpView(*i)}, nil
}

func (a *API) idpCtx(c *Ctx) (*store.IdP, error) {
	i, err := store.GetByID[store.IdP](c, a.St.Pool, "idp_providers", c.Param("id"))
	if err != nil {
		return nil, err
	}
	return i, c.Require(auth.Scope{Type: "org", ID: i.OrgID, OrgID: i.OrgID}, "owner")
}

func (a *API) updateIdP(c *Ctx) (any, error) {
	i, err := a.idpCtx(c)
	if err != nil {
		return nil, err
	}
	var in idpIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if err := a.applyIdP(c, i, &in); err != nil {
		return nil, err
	}
	if err := a.St.Update(c, a.St.Pool, "idp_providers", i); err != nil {
		return nil, err
	}
	c.Audit("idp.update", i.OrgID, "idp", i.ID, map[string]any{"name": i.Name, "issuer": i.Issuer, "enabled": i.Enabled, "role_mappings": i.RoleMappings, "gateway_tokens": i.GatewayTokens})
	a.changed(c)
	return a.idpView(*i), nil
}

func (a *API) deleteIdP(c *Ctx) (any, error) {
	i, err := a.idpCtx(c)
	if err != nil {
		return nil, err
	}
	if err := a.St.Delete(c, a.St.Pool, "idp_providers", i.ID); err != nil {
		return nil, err
	}
	c.Audit("idp.delete", i.OrgID, "idp", i.ID, map[string]string{"name": i.Name})
	a.changed(c)
	return nil, nil
}

func (a *API) scimToken(c *Ctx) (any, error) {
	i, err := a.idpCtx(c)
	if err != nil {
		return nil, err
	}
	plain, _, hash := auth.GenerateKey(auth.SCIMPrefix)
	if _, err := a.St.Pool.Exec(c, `UPDATE idp_providers SET scim_token_hash=$2 WHERE id=$1`, i.ID, hash); err != nil {
		return nil, err
	}
	c.Audit("idp.scim_token_rotate", i.OrgID, "idp", i.ID, nil)
	return map[string]any{"token": plain, "scim_url": a.Cfg.PublicURL + "/scim/v2"}, nil
}

type personalLimit struct {
	TeamID      string        `json:"team_id"`
	TeamName    string        `json:"team_name"`
	Period      string        `json:"period"`
	Source      string        `json:"source"` // user | team | org
	Budget      *store.Budget `json:"budget"`
	SpentUSD    float64       `json:"spent_usd"`
	SpentTokens int64         `json:"spent_tokens"`
}

// personalBudgets shows which per-person budget governs a person in each of
// their teams and periods (own budget, else team default, else org default).
func (a *API) personalBudgets(c *Ctx) (any, error) {
	o, os, err := a.orgCtx(c, "")
	if err != nil {
		return nil, err
	}
	uid := c.Query("user_id")
	if uid == "" {
		uid = c.P.UserID()
	} else if uid != c.P.UserID() {
		if err := c.Require(os, "admin"); err != nil {
			return nil, err
		}
	}
	if uid == "" {
		return []personalLimit{}, nil
	}
	type team struct {
		ID   string `db:"id"`
		Name string `db:"name"`
	}
	teams, err := store.List[team](c, a.St.Pool, `SELECT DISTINCT t.id, t.name FROM memberships m
		LEFT JOIN projects p ON m.scope_type='project' AND p.id=m.scope_id
		JOIN teams t ON t.id = CASE m.scope_type WHEN 'team' THEN m.scope_id WHEN 'project' THEN p.team_id END
		WHERE m.user_id=$1 AND m.org_id=$2 ORDER BY t.name`, uid, o.ID)
	if err != nil {
		return nil, err
	}
	if len(teams) == 0 {
		teams = []team{{}}
	}
	snap := a.Loader.Snapshot()
	out := []personalLimit{}
	for _, t := range teams {
		for _, p := range gateway.Periods {
			b, ctr := gateway.PersonalBudget(snap, o.ID, t.ID, uid, p)
			if b == nil {
				continue
			}
			src := b.ScopeType
			cost, tok := a.Budgets.Current(ctr.Type, ctr.ID, p)
			out = append(out, personalLimit{TeamID: t.ID, TeamName: t.Name, Period: p, Source: src, Budget: b, SpentUSD: cost, SpentTokens: tok})
		}
	}
	return out, nil
}
