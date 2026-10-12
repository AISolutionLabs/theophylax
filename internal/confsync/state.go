package confsync

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The live configuration as the admin API returns it.

type lOrg struct {
	ID       string          `json:"id"`
	Slug     string          `json:"slug"`
	Name     string          `json:"name"`
	Settings json.RawMessage `json:"settings"`
}

type lTeam struct {
	ID          string          `json:"id"`
	Slug        string          `json:"slug"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Settings    json.RawMessage `json:"settings"`
}

type lProject struct {
	ID          string          `json:"id"`
	TeamID      string          `json:"team_id"`
	Slug        string          `json:"slug"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Settings    json.RawMessage `json:"settings"`
}

type lProvider struct {
	ID               string          `json:"id"`
	OrgID            *string         `json:"org_id"`
	Name             string          `json:"name"`
	ConnectionString string          `json:"connection_string"`
	Config           json.RawMessage `json:"config"`
	Enabled          bool            `json:"enabled"`
	// CanManage is false for providers the token may use but not change;
	// their connection string is not shown.
	CanManage bool `json:"can_manage"`
}

type lDeployment struct {
	ID            string `json:"id"`
	ProviderID    string `json:"provider_id"`
	UpstreamModel string `json:"upstream_model"`
	Weight        int    `json:"weight"`
	Priority      int    `json:"priority"`
	Enabled       bool   `json:"enabled"`
}

type lModel struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	DisplayName   string        `json:"display_name"`
	Family        string        `json:"family"`
	Aliases       []string      `json:"aliases"`
	ContextWindow int           `json:"context_window"`
	MaxOutput     int           `json:"max_output"`
	Capabilities  []string      `json:"capabilities"`
	Status        string        `json:"status"`
	Strategy      string        `json:"strategy"`
	Deployments   []lDeployment `json:"deployments"`
}

type lGuardrail struct {
	ID          string          `json:"id"`
	ScopeType   string          `json:"scope_type"`
	ScopeID     string          `json:"scope_id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Enabled     bool            `json:"enabled"`
	Mode        string          `json:"mode"`
	Rules       json.RawMessage `json:"rules"`
}

type lBudget struct {
	ID          string   `json:"id"`
	ScopeType   string   `json:"scope_type"`
	ScopeID     string   `json:"scope_id"`
	Period      string   `json:"period"`
	LimitUSD    *float64 `json:"limit_usd"`
	LimitTokens *int64   `json:"limit_tokens"`
	PerUser     bool     `json:"per_user"`
	AlertPct    int      `json:"alert_pct"`
	Action      string   `json:"action"`
}

type lUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

type orgState struct {
	lOrg
	teams      []lTeam
	projects   []lProject
	guardrails []lGuardrail
	budgets    []lBudget
	users      []lUser
}

type live struct {
	orgs      []*orgState
	providers []lProvider
	models    []lModel
}

func (l *live) org(slug string) *orgState {
	for _, o := range l.orgs {
		if o.Slug == slug {
			return o
		}
	}
	return nil
}

func (l *live) orgByID(id string) *orgState {
	for _, o := range l.orgs {
		if o.ID == id {
			return o
		}
	}
	return nil
}

func (l *live) providerRef(p lProvider) string {
	if p.OrgID != nil {
		if o := l.orgByID(*p.OrgID); o != nil {
			return o.Slug + "/" + p.Name
		}
		return *p.OrgID + "/" + p.Name
	}
	return p.Name
}

func (l *live) providerByRef(ref string) *lProvider {
	for i := range l.providers {
		if l.providerRef(l.providers[i]) == ref {
			return &l.providers[i]
		}
	}
	return nil
}

func (l *live) model(name string) *lModel {
	for i := range l.models {
		if l.models[i].Name == name {
			return &l.models[i]
		}
	}
	return nil
}

func (o *orgState) team(slug string) *lTeam {
	for i := range o.teams {
		if o.teams[i].Slug == slug {
			return &o.teams[i]
		}
	}
	return nil
}

func (o *orgState) project(teamID, slug string) *lProject {
	for i := range o.projects {
		if o.projects[i].TeamID == teamID && o.projects[i].Slug == slug {
			return &o.projects[i]
		}
	}
	return nil
}

// scopeRef names a guardrail or budget scope: "org", "team:<slug>",
// "project:<team>/<slug>" or "user:<email>". ok is false for scopes a
// document cannot express (API keys).
func (o *orgState) scopeRef(typ, id string) (string, bool) {
	switch typ {
	case "org":
		return "org", true
	case "team":
		for _, t := range o.teams {
			if t.ID == id {
				return "team:" + t.Slug, true
			}
		}
	case "project":
		for _, p := range o.projects {
			if p.ID == id {
				for _, t := range o.teams {
					if t.ID == p.TeamID {
						return "project:" + t.Slug + "/" + p.Slug, true
					}
				}
			}
		}
	case "user":
		for _, u := range o.users {
			if u.ID == id {
				return "user:" + u.Email, true
			}
		}
	}
	return "", false
}

// parseScope validates a scope reference and splits it into type and name.
func parseScope(ref string) (typ, name string, err error) {
	if ref == "" || ref == "org" {
		return "org", "", nil
	}
	typ, name, _ = strings.Cut(ref, ":")
	switch typ {
	case "team", "user":
		if name != "" {
			return typ, name, nil
		}
	case "project":
		if t, p, ok := strings.Cut(name, "/"); ok && t != "" && p != "" {
			return typ, name, nil
		}
	}
	return "", "", fmt.Errorf("invalid scope %q (use org, team:<slug>, project:<team>/<slug> or user:<email>)", ref)
}

func fetch(ctx context.Context, c *Client, onlyOrg string, needUsers bool) (*live, error) {
	l := &live{}
	var orgs []lOrg
	if err := c.Do(ctx, "GET", "/api/orgs", nil, &orgs); err != nil {
		return nil, err
	}
	for _, o := range orgs {
		if onlyOrg != "" && o.Slug != onlyOrg {
			continue
		}
		s := &orgState{lOrg: o}
		base := "/api/orgs/" + esc(o.ID)
		for _, f := range []struct {
			path string
			out  any
		}{
			{base + "/teams", &s.teams},
			{base + "/projects", &s.projects},
			{base + "/guardrails", &s.guardrails},
			{base + "/budgets", &s.budgets},
		} {
			if err := c.Do(ctx, "GET", f.path, nil, f.out); err != nil {
				return nil, err
			}
		}
		if needUsers {
			if err := c.Do(ctx, "GET", base+"/users", nil, &s.users); err != nil {
				return nil, err
			}
		}
		l.orgs = append(l.orgs, s)
	}
	if err := c.Do(ctx, "GET", "/api/providers", nil, &l.providers); err != nil {
		return nil, err
	}
	if err := c.Do(ctx, "GET", "/api/models", nil, &l.models); err != nil {
		return nil, err
	}
	return l, nil
}

// emptyObj reports whether raw is absent, null or {}.
func emptyObj(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s == "" || s == "null" || s == "{}"
}

// Export reads the configuration the token can see. org limits organizations
// to one slug (providers and models are still included). Warnings describe
// what a document cannot carry, such as budgets on individual API keys.
func Export(ctx context.Context, c *Client, org string) (*Doc, []string, error) {
	l, err := fetch(ctx, c, org, true)
	if err != nil {
		return nil, nil, err
	}
	if org != "" && len(l.orgs) == 0 {
		return nil, nil, fmt.Errorf("organization %q not found", org)
	}
	var warn []string
	d := &Doc{Version: Version, Organizations: []Org{}, Providers: []Provider{}, Models: []Model{}}
	for _, o := range l.orgs {
		do := Org{Slug: o.Slug, Name: o.Name, Teams: []Team{}, Guardrails: []Guardrail{}, Budgets: []Budget{}}
		if !emptyObj(o.Settings) {
			do.Settings = o.Settings
		}
		for _, t := range o.teams {
			dt := Team{Slug: t.Slug, Name: t.Name, Description: t.Description, Projects: []Project{}}
			if !emptyObj(t.Settings) {
				dt.Settings = t.Settings
			}
			for _, p := range o.projects {
				if p.TeamID != t.ID {
					continue
				}
				dp := Project{Slug: p.Slug, Name: p.Name, Description: p.Description}
				if !emptyObj(p.Settings) {
					dp.Settings = p.Settings
				}
				dt.Projects = append(dt.Projects, dp)
			}
			do.Teams = append(do.Teams, dt)
		}
		for _, g := range o.guardrails {
			ref, ok := o.scopeRef(g.ScopeType, g.ScopeID)
			if !ok {
				warn = append(warn, fmt.Sprintf("%s: guardrail %q is scoped to a %s and was not exported", o.Slug, g.Name, g.ScopeType))
				continue
			}
			do.Guardrails = append(do.Guardrails, Guardrail{Name: g.Name, Description: g.Description, Scope: ref, Mode: g.Mode, Enabled: ptr(g.Enabled), Rules: g.Rules})
		}
		for _, b := range o.budgets {
			ref, ok := o.scopeRef(b.ScopeType, b.ScopeID)
			if !ok {
				warn = append(warn, fmt.Sprintf("%s: a %s budget scoped to a %s was not exported", o.Slug, b.Period, b.ScopeType))
				continue
			}
			do.Budgets = append(do.Budgets, Budget{Scope: ref, Period: b.Period, PerUser: b.PerUser, LimitUSD: b.LimitUSD, LimitTokens: b.LimitTokens, AlertPct: ptr(b.AlertPct), Action: b.Action})
		}
		d.Organizations = append(d.Organizations, do)
	}
	for _, p := range l.providers {
		dp := Provider{Name: p.Name, Enabled: ptr(p.Enabled)}
		if p.CanManage {
			dp.ConnectionString = p.ConnectionString
		}
		if p.OrgID != nil {
			o := l.orgByID(*p.OrgID)
			if o == nil {
				continue // another organization's provider
			}
			dp.Organization = o.Slug
		}
		if !emptyObj(p.Config) {
			dp.Config = p.Config
		}
		d.Providers = append(d.Providers, dp)
	}
	for _, m := range l.models {
		dm := Model{Name: m.Name, DisplayName: m.DisplayName, Family: m.Family, Aliases: nonNil(m.Aliases), ContextWindow: ptr(m.ContextWindow), MaxOutput: ptr(m.MaxOutput),
			Capabilities: nonNil(m.Capabilities), Status: m.Status, Strategy: m.Strategy, Deployments: []Deployment{}}
		for _, dep := range m.Deployments {
			var ref string
			for _, p := range l.providers {
				if p.ID == dep.ProviderID {
					ref = l.providerRef(p)
				}
			}
			if ref == "" {
				continue
			}
			dm.Deployments = append(dm.Deployments, Deployment{Provider: ref, UpstreamModel: dep.UpstreamModel, Weight: ptr(dep.Weight), Priority: ptr(dep.Priority), Enabled: ptr(dep.Enabled)})
		}
		d.Models = append(d.Models, dm)
	}
	sort.Strings(warn)
	return d, warn, nil
}

func ptr[T any](v T) *T { return &v }

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
