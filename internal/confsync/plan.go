package confsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"theophylax/internal/providers"
)

// Options control how a document is applied.
type Options struct {
	// Prune deletes providers, models, deployments, guardrails and budgets
	// that the gateway has but the document does not list. It only acts on
	// lists the document includes, and only within the organizations it
	// names. Organizations, teams and projects are never deleted.
	Prune bool
}

// Change is one API call in a plan.
type Change struct {
	Action string   `json:"action"` // create, update, delete
	Kind   string   `json:"kind"`
	Name   string   `json:"name"`
	Fields []string `json:"fields,omitempty"`
	run    func(ctx context.Context, c *Client, ids map[string]string) error
}

// Plan is the ordered set of changes needed to make the gateway match a
// document.
type Plan struct {
	Changes []Change
	ids     map[string]string
}

func (p *Plan) Empty() bool { return len(p.Changes) == 0 }

// Write prints the plan in a terraform-like form.
func (p *Plan) Write(w io.Writer) {
	n := map[string]int{}
	for _, c := range p.Changes {
		sym := map[string]string{"create": "+", "update": "~", "delete": "-"}[c.Action]
		line := fmt.Sprintf("%s %s %s %s", sym, c.Action, c.Kind, c.Name)
		if len(c.Fields) > 0 {
			line += " (" + strings.Join(c.Fields, ", ") + ")"
		}
		fmt.Fprintln(w, line)
		n[c.Action]++
	}
	if p.Empty() {
		fmt.Fprintln(w, "No changes: the gateway matches the document.")
		return
	}
	fmt.Fprintf(w, "\nPlan: %d to create, %d to update, %d to delete.\n", n["create"], n["update"], n["delete"])
}

// Apply makes the changes in order and stops at the first error, which
// names the change that failed. Changes before it stay applied; running the
// plan again picks up from where it stopped.
func (p *Plan) Apply(ctx context.Context, c *Client, progress io.Writer) (int, error) {
	for i, ch := range p.Changes {
		if err := ch.run(ctx, c, p.ids); err != nil {
			return i, fmt.Errorf("%s %s %s: %w", ch.Action, ch.Kind, ch.Name, err)
		}
		if progress != nil {
			fmt.Fprintf(progress, "%sd %s %s\n", strings.TrimSuffix(ch.Action, "e"), ch.Kind, ch.Name)
		}
	}
	return len(p.Changes), nil
}

type planner struct {
	l       *live
	d       *Doc
	opt     Options
	out     []Change
	ids     map[string]string
	errs    []string
	planned map[string]bool // keys of things this plan creates
}

func (pl *planner) add(action, kind, name string, fields []string, run func(ctx context.Context, c *Client, ids map[string]string) error) {
	pl.out = append(pl.out, Change{Action: action, Kind: kind, Name: name, Fields: fields, run: run})
}

func (pl *planner) fail(format string, a ...any) {
	pl.errs = append(pl.errs, fmt.Sprintf(format, a...))
}

// known reports whether an object exists now or will be created earlier in
// the plan.
func (pl *planner) known(key string) bool { return pl.ids[key] != "" || pl.planned[key] }

// Compute works out the changes needed to make the gateway match d. It
// checks the whole document first and reports every problem it finds.
func Compute(ctx context.Context, c *Client, d *Doc, opt Options) (*Plan, error) {
	needUsers := false
	for _, o := range d.Organizations {
		for _, b := range o.Budgets {
			needUsers = needUsers || strings.HasPrefix(b.Scope, "user:")
		}
	}
	l, err := fetch(ctx, c, "", needUsers)
	if err != nil {
		return nil, err
	}
	pl := &planner{l: l, d: d, opt: opt, ids: map[string]string{}, planned: map[string]bool{}}
	for _, o := range l.orgs {
		pl.ids["org:"+o.Slug] = o.ID
		for _, t := range o.teams {
			pl.ids["team:"+o.Slug+"/"+t.Slug] = t.ID
		}
		for _, p := range o.projects {
			if ref, ok := o.scopeRef("project", p.ID); ok {
				pl.ids["project:"+o.Slug+"/"+strings.TrimPrefix(ref, "project:")] = p.ID
			}
		}
		for _, u := range o.users {
			pl.ids["user:"+o.Slug+"/"+u.Email] = u.ID
		}
	}
	for _, p := range l.providers {
		pl.ids["provider:"+l.providerRef(p)] = p.ID
	}
	for _, m := range l.models {
		pl.ids["model:"+m.Name] = m.ID
	}

	pl.orgs()
	pl.providers()
	pl.models()
	pl.policies()
	if len(pl.errs) > 0 {
		return nil, fmt.Errorf("the document cannot be applied:\n  %s", strings.Join(pl.errs, "\n  "))
	}
	// Deletions go last, most dependent first, so nothing is removed while
	// something that is still to be created or updated refers to it.
	var keep, del []Change
	for _, ch := range pl.out {
		if ch.Action == "delete" {
			del = append(del, ch)
		} else {
			keep = append(keep, ch)
		}
	}
	order := map[string]int{"budget": 0, "guardrail": 1, "deployment": 2, "model": 3, "provider": 4}
	slices.SortStableFunc(del, func(a, b Change) int { return order[a.Kind] - order[b.Kind] })
	return &Plan{Changes: append(keep, del...), ids: pl.ids}, nil
}

// ---- organizations, teams, projects ----

func (pl *planner) orgs() {
	seen := map[string]bool{}
	for _, do := range pl.d.Organizations {
		if do.Slug == "" {
			pl.fail("an organization has no slug")
			continue
		}
		if seen[do.Slug] {
			pl.fail("organization %q is listed twice", do.Slug)
			continue
		}
		seen[do.Slug] = true
		if err := checkSettings(do.Settings); err != nil {
			pl.fail("organization %s: %v", do.Slug, err)
		}
		lo := pl.l.org(do.Slug)
		key := "org:" + do.Slug
		if lo == nil {
			pl.planned[key] = true
			body := map[string]any{"slug": do.Slug, "name": orDefault(do.Name, do.Slug)}
			if do.Settings != nil {
				body["settings"] = do.Settings
			}
			pl.add("create", "organization", do.Slug, nil, func(ctx context.Context, c *Client, ids map[string]string) error {
				var r struct{ ID string }
				err := c.Do(ctx, "POST", "/api/orgs", body, &r)
				ids[key] = r.ID
				return err
			})
		} else {
			patch, fields := map[string]any{}, []string(nil)
			if do.Name != "" && do.Name != lo.Name {
				patch["name"], fields = do.Name, append(fields, "name")
			}
			if do.Settings != nil && !jsonEq(do.Settings, lo.Settings) {
				patch["settings"], fields = do.Settings, append(fields, "settings")
			}
			if len(patch) > 0 {
				pl.add("update", "organization", do.Slug, fields, func(ctx context.Context, c *Client, ids map[string]string) error {
					return c.Do(ctx, "PATCH", "/api/orgs/"+esc(ids[key]), patch, nil)
				})
			}
		}
		pl.teams(do, lo)
	}
}

func (pl *planner) teams(do Org, lo *orgState) {
	seen := map[string]bool{}
	for _, dt := range do.Teams {
		name := do.Slug + "/" + dt.Slug
		if dt.Slug == "" {
			pl.fail("a team in %s has no slug", do.Slug)
			continue
		}
		if seen[dt.Slug] {
			pl.fail("team %s is listed twice", name)
			continue
		}
		seen[dt.Slug] = true
		if err := checkSettings(dt.Settings); err != nil {
			pl.fail("team %s: %v", name, err)
		}
		var lt *lTeam
		if lo != nil {
			lt = lo.team(dt.Slug)
		}
		key, orgKey := "team:"+name, "org:"+do.Slug
		if lt == nil {
			pl.planned[key] = true
			body := map[string]any{"slug": dt.Slug, "name": orDefault(dt.Name, dt.Slug), "description": dt.Description}
			if dt.Settings != nil {
				body["settings"] = dt.Settings
			}
			pl.add("create", "team", name, nil, func(ctx context.Context, c *Client, ids map[string]string) error {
				var r struct{ ID string }
				err := c.Do(ctx, "POST", "/api/orgs/"+esc(ids[orgKey])+"/teams", body, &r)
				ids[key] = r.ID
				return err
			})
		} else {
			patch, fields := map[string]any{}, []string(nil)
			if dt.Name != "" && dt.Name != lt.Name {
				patch["name"], fields = dt.Name, append(fields, "name")
			}
			if dt.Description != "" && dt.Description != lt.Description {
				patch["description"], fields = dt.Description, append(fields, "description")
			}
			if dt.Settings != nil && !jsonEq(dt.Settings, lt.Settings) {
				patch["settings"], fields = dt.Settings, append(fields, "settings")
			}
			if len(patch) > 0 {
				pl.add("update", "team", name, fields, func(ctx context.Context, c *Client, ids map[string]string) error {
					return c.Do(ctx, "PATCH", "/api/teams/"+esc(ids[key]), patch, nil)
				})
			}
		}
		pl.projects(do, dt, lo, lt)
	}
}

func (pl *planner) projects(do Org, dt Team, lo *orgState, lt *lTeam) {
	seen := map[string]bool{}
	teamKey := "team:" + do.Slug + "/" + dt.Slug
	for _, dp := range dt.Projects {
		name := do.Slug + "/" + dt.Slug + "/" + dp.Slug
		if dp.Slug == "" {
			pl.fail("a project in team %s/%s has no slug", do.Slug, dt.Slug)
			continue
		}
		if seen[dp.Slug] {
			pl.fail("project %s is listed twice", name)
			continue
		}
		seen[dp.Slug] = true
		if err := checkSettings(dp.Settings); err != nil {
			pl.fail("project %s: %v", name, err)
		}
		var lp *lProject
		if lt != nil {
			lp = lo.project(lt.ID, dp.Slug)
		}
		key := "project:" + name
		if lp == nil {
			pl.planned[key] = true
			body := map[string]any{"slug": dp.Slug, "name": orDefault(dp.Name, dp.Slug), "description": dp.Description}
			if dp.Settings != nil {
				body["settings"] = dp.Settings
			}
			pl.add("create", "project", name, nil, func(ctx context.Context, c *Client, ids map[string]string) error {
				var r struct{ ID string }
				err := c.Do(ctx, "POST", "/api/teams/"+esc(ids[teamKey])+"/projects", body, &r)
				ids[key] = r.ID
				return err
			})
			continue
		}
		patch, fields := map[string]any{}, []string(nil)
		if dp.Name != "" && dp.Name != lp.Name {
			patch["name"], fields = dp.Name, append(fields, "name")
		}
		if dp.Description != "" && dp.Description != lp.Description {
			patch["description"], fields = dp.Description, append(fields, "description")
		}
		if dp.Settings != nil && !jsonEq(dp.Settings, lp.Settings) {
			patch["settings"], fields = dp.Settings, append(fields, "settings")
		}
		if len(patch) > 0 {
			pl.add("update", "project", name, fields, func(ctx context.Context, c *Client, ids map[string]string) error {
				return c.Do(ctx, "PATCH", "/api/projects/"+esc(ids[key]), patch, nil)
			})
		}
	}
}

// ---- providers ----

// managedOrg reports whether the document lists the organization, which is
// what limits pruning to the organizations a document is about.
func (pl *planner) managedOrg(slug string) bool {
	for _, o := range pl.d.Organizations {
		if o.Slug == slug {
			return true
		}
	}
	return false
}

func (pl *planner) providers() {
	if pl.d.Providers == nil {
		return
	}
	seen := map[string]bool{}
	for _, dp := range pl.d.Providers {
		ref := dp.ref()
		if dp.Name == "" {
			pl.fail("a provider has no name")
			continue
		}
		if seen[ref] {
			pl.fail("provider %q is listed twice", ref)
			continue
		}
		seen[ref] = true
		if dp.Organization != "" && !pl.known("org:"+dp.Organization) {
			pl.fail("provider %s: organization %q does not exist and is not in the document", ref, dp.Organization)
			continue
		}
		key := "provider:" + ref
		lp := pl.l.providerByRef(ref)
		if lp == nil {
			if dp.ConnectionString == "" {
				pl.fail("provider %s: connection_string is required to create it", ref)
				continue
			}
			if strings.Contains(dp.ConnectionString, "****") {
				pl.fail("provider %s: connection_string contains a masked secret; give the secret or a ${env:…} reference", ref)
				continue
			}
			if _, err := providers.ParseConn(dp.ConnectionString); err != nil {
				pl.fail("provider %s: %v", ref, err)
				continue
			}
			pl.planned[key] = true
			dp := dp
			pl.add("create", "provider", ref, nil, func(ctx context.Context, c *Client, ids map[string]string) error {
				body := map[string]any{"name": dp.Name, "connection_string": dp.ConnectionString}
				if dp.Organization != "" {
					body["org_id"] = ids["org:"+dp.Organization]
				}
				if dp.Config != nil {
					body["config"] = dp.Config
				}
				if dp.Enabled != nil {
					body["enabled"] = *dp.Enabled
				}
				var r struct{ ID string }
				err := c.Do(ctx, "POST", "/api/providers", body, &r)
				ids[key] = r.ID
				return err
			})
			continue
		}
		patch, fields := map[string]any{}, []string(nil)
		// The connection string of a provider the token cannot manage is
		// not visible, so it cannot be compared.
		if cs := dp.ConnectionString; cs != "" && lp.CanManage && cs != lp.ConnectionString {
			pc, err := providers.ParseConn(cs)
			switch {
			case err != nil:
				pl.fail("provider %s: %v", ref, err)
			case strings.Contains(cs, "****"):
				pl.fail("provider %s: connection_string contains a masked secret that does not match the stored one; give the secret or a ${env:…} reference", ref)
			case pc.Redacted() != lp.ConnectionString:
				// A literal secret whose masked form matches the stored one
				// is taken to be unchanged; anything else is sent.
				patch["connection_string"], fields = cs, append(fields, "connection_string")
			}
		}
		if dp.Config != nil && !jsonEq(dp.Config, lp.Config) {
			patch["config"], fields = dp.Config, append(fields, "config")
		}
		if dp.Enabled != nil && *dp.Enabled != lp.Enabled {
			patch["enabled"], fields = *dp.Enabled, append(fields, "enabled")
		}
		if len(patch) > 0 {
			id := lp.ID
			pl.add("update", "provider", ref, fields, func(ctx context.Context, c *Client, _ map[string]string) error {
				return c.Do(ctx, "PATCH", "/api/providers/"+esc(id), patch, nil)
			})
		}
	}
	if !pl.opt.Prune {
		return
	}
	for _, lp := range pl.l.providers {
		ref := pl.l.providerRef(lp)
		if seen[ref] || !lp.CanManage {
			continue
		}
		if lp.OrgID != nil {
			if o := pl.l.orgByID(*lp.OrgID); o == nil || !pl.managedOrg(o.Slug) {
				continue
			}
		}
		id := lp.ID
		pl.add("delete", "provider", ref, nil, func(ctx context.Context, c *Client, _ map[string]string) error {
			return c.Do(ctx, "DELETE", "/api/providers/"+esc(id), nil, nil)
		})
	}
}

// ---- models and deployments ----

func (pl *planner) models() {
	if pl.d.Models == nil {
		return
	}
	seen := map[string]bool{}
	for _, dm := range pl.d.Models {
		if dm.Name == "" {
			pl.fail("a model has no name")
			continue
		}
		if seen[dm.Name] {
			pl.fail("model %q is listed twice", dm.Name)
			continue
		}
		seen[dm.Name] = true
		key := "model:" + dm.Name
		lm := pl.l.model(dm.Name)
		fields := map[string]any{}
		var changed []string
		set := func(name string, v any, differs bool) {
			if differs {
				fields[name] = v
				changed = append(changed, name)
			}
		}
		var cur lModel
		if lm != nil {
			cur = *lm
		}
		set("display_name", dm.DisplayName, dm.DisplayName != "" && dm.DisplayName != cur.DisplayName)
		set("family", dm.Family, dm.Family != "" && dm.Family != cur.Family)
		set("aliases", dm.Aliases, dm.Aliases != nil && !slices.Equal(dm.Aliases, cur.Aliases))
		set("context_window", deref(dm.ContextWindow), dm.ContextWindow != nil && *dm.ContextWindow != cur.ContextWindow)
		set("max_output", deref(dm.MaxOutput), dm.MaxOutput != nil && *dm.MaxOutput != cur.MaxOutput)
		set("capabilities", dm.Capabilities, dm.Capabilities != nil && !slices.Equal(dm.Capabilities, cur.Capabilities))
		set("status", dm.Status, dm.Status != "" && dm.Status != cur.Status)
		set("strategy", dm.Strategy, dm.Strategy != "" && dm.Strategy != cur.Strategy)
		if lm == nil {
			pl.planned[key] = true
			fields["name"] = dm.Name
			pl.add("create", "model", dm.Name, nil, func(ctx context.Context, c *Client, ids map[string]string) error {
				var r struct{ ID string }
				err := c.Do(ctx, "POST", "/api/models", fields, &r)
				ids[key] = r.ID
				return err
			})
		} else if len(changed) > 0 {
			id := lm.ID
			pl.add("update", "model", dm.Name, changed, func(ctx context.Context, c *Client, _ map[string]string) error {
				return c.Do(ctx, "PATCH", "/api/models/"+esc(id), fields, nil)
			})
		}
		pl.deployments(dm, lm)
	}
	if !pl.opt.Prune {
		return
	}
	for _, lm := range pl.l.models {
		if !seen[lm.Name] {
			id := lm.ID
			pl.add("delete", "model", lm.Name, nil, func(ctx context.Context, c *Client, _ map[string]string) error {
				return c.Do(ctx, "DELETE", "/api/models/"+esc(id), nil, nil)
			})
		}
	}
}

func (pl *planner) deployments(dm Model, lm *lModel) {
	if dm.Deployments == nil {
		return
	}
	matched := map[string]bool{}
	seen := map[string]bool{}
	for _, dd := range dm.Deployments {
		name := dm.Name + " → " + dd.Provider + ":" + dd.UpstreamModel
		if dd.Provider == "" || dd.UpstreamModel == "" {
			pl.fail("model %s: a deployment needs provider and upstream_model", dm.Name)
			continue
		}
		if seen[dd.Provider+"\x00"+dd.UpstreamModel] {
			pl.fail("deployment %s is listed twice", name)
			continue
		}
		seen[dd.Provider+"\x00"+dd.UpstreamModel] = true
		provKey := "provider:" + dd.Provider
		if !pl.known(provKey) {
			pl.fail("deployment %s: provider %q does not exist and is not in the document", name, dd.Provider)
			continue
		}
		var ld *lDeployment
		if lm != nil {
			for i := range lm.Deployments {
				d := &lm.Deployments[i]
				if d.ProviderID == pl.ids[provKey] && d.UpstreamModel == dd.UpstreamModel {
					ld = d
					break
				}
			}
		}
		if ld == nil {
			modelKey := "model:" + dm.Name
			dd := dd
			pl.add("create", "deployment", name, nil, func(ctx context.Context, c *Client, ids map[string]string) error {
				body := map[string]any{"provider_id": ids[provKey], "upstream_model": dd.UpstreamModel}
				if dd.Weight != nil {
					body["weight"] = *dd.Weight
				}
				if dd.Priority != nil {
					body["priority"] = *dd.Priority
				}
				if dd.Enabled != nil {
					body["enabled"] = *dd.Enabled
				}
				return c.Do(ctx, "POST", "/api/models/"+esc(ids[modelKey])+"/deployments", body, nil)
			})
			continue
		}
		matched[ld.ID] = true
		patch, fields := map[string]any{}, []string(nil)
		if dd.Weight != nil && *dd.Weight != ld.Weight {
			patch["weight"], fields = *dd.Weight, append(fields, "weight")
		}
		if dd.Priority != nil && *dd.Priority != ld.Priority {
			patch["priority"], fields = *dd.Priority, append(fields, "priority")
		}
		if dd.Enabled != nil && *dd.Enabled != ld.Enabled {
			patch["enabled"], fields = *dd.Enabled, append(fields, "enabled")
		}
		if len(patch) > 0 {
			id := ld.ID
			pl.add("update", "deployment", name, fields, func(ctx context.Context, c *Client, _ map[string]string) error {
				return c.Do(ctx, "PATCH", "/api/deployments/"+esc(id), patch, nil)
			})
		}
	}
	if !pl.opt.Prune || lm == nil {
		return
	}
	for _, ld := range lm.Deployments {
		if matched[ld.ID] {
			continue
		}
		ref := ld.ProviderID
		for _, p := range pl.l.providers {
			if p.ID == ld.ProviderID {
				ref = pl.l.providerRef(p)
			}
		}
		id := ld.ID
		pl.add("delete", "deployment", dm.Name+" → "+ref+":"+ld.UpstreamModel, nil, func(ctx context.Context, c *Client, _ map[string]string) error {
			return c.Do(ctx, "DELETE", "/api/deployments/"+esc(id), nil, nil)
		})
	}
}

// ---- guardrails and budgets ----

// scopeKey maps a scope reference to the ids key of the object it names, so
// that it can be checked at plan time and resolved at apply time.
func (pl *planner) scopeKey(org, ref string) (typ, key string, err error) {
	typ, name, err := parseScope(ref)
	if err != nil {
		return "", "", err
	}
	if typ == "org" {
		key = "org:" + org
	} else {
		key = typ + ":" + org + "/" + name
	}
	if !pl.known(key) {
		return "", "", fmt.Errorf("scope %q does not exist and is not in the document", ref)
	}
	return typ, key, nil
}

func (pl *planner) policies() {
	for _, do := range pl.d.Organizations {
		lo := pl.l.org(do.Slug)
		pl.guardrails(do, lo)
		pl.budgets(do, lo)
	}
}

func (pl *planner) guardrails(do Org, lo *orgState) {
	if do.Guardrails == nil {
		return
	}
	orgKey := "org:" + do.Slug
	seen := map[string]bool{}
	for _, dg := range do.Guardrails {
		name := do.Slug + "/" + dg.Name
		if dg.Name == "" {
			pl.fail("a guardrail in %s has no name", do.Slug)
			continue
		}
		if seen[dg.Name] {
			pl.fail("guardrail %s is listed twice", name)
			continue
		}
		seen[dg.Name] = true
		scope := orDefault(dg.Scope, "org")
		typ, sk, err := pl.scopeKey(do.Slug, scope)
		if err != nil {
			pl.fail("guardrail %s: %v", name, err)
			continue
		}
		if typ == "user" {
			pl.fail("guardrail %s: guardrails cannot be scoped to a user", name)
			continue
		}
		var lg *lGuardrail
		if lo != nil {
			for i := range lo.guardrails {
				if lo.guardrails[i].Name == dg.Name {
					lg = &lo.guardrails[i]
				}
			}
		}
		create := func(ctx context.Context, c *Client, ids map[string]string) error {
			body := map[string]any{"name": dg.Name, "scope_type": typ, "scope_id": ids[sk], "description": dg.Description}
			if dg.Mode != "" {
				body["mode"] = dg.Mode
			}
			if dg.Enabled != nil {
				body["enabled"] = *dg.Enabled
			}
			if dg.Rules != nil {
				body["rules"] = dg.Rules
			}
			return c.Do(ctx, "POST", "/api/orgs/"+esc(ids[orgKey])+"/guardrails", body, nil)
		}
		if lg == nil {
			pl.add("create", "guardrail", name, nil, create)
			continue
		}
		if cur, _ := lo.scopeRef(lg.ScopeType, lg.ScopeID); cur != scope {
			// A guardrail's scope is fixed once created: replace it.
			id := lg.ID
			pl.add("update", "guardrail", name, []string{"scope (replaced)"}, func(ctx context.Context, c *Client, ids map[string]string) error {
				if err := c.Do(ctx, "DELETE", "/api/guardrails/"+esc(id), nil, nil); err != nil {
					return err
				}
				return create(ctx, c, ids)
			})
			continue
		}
		patch, fields := map[string]any{}, []string(nil)
		if dg.Description != "" && dg.Description != lg.Description {
			patch["description"], fields = dg.Description, append(fields, "description")
		}
		if dg.Mode != "" && dg.Mode != lg.Mode {
			patch["mode"], fields = dg.Mode, append(fields, "mode")
		}
		if dg.Enabled != nil && *dg.Enabled != lg.Enabled {
			patch["enabled"], fields = *dg.Enabled, append(fields, "enabled")
		}
		if dg.Rules != nil && !jsonEq(dg.Rules, lg.Rules) {
			patch["rules"], fields = dg.Rules, append(fields, "rules")
		}
		if len(patch) > 0 {
			id := lg.ID
			pl.add("update", "guardrail", name, fields, func(ctx context.Context, c *Client, _ map[string]string) error {
				return c.Do(ctx, "PATCH", "/api/guardrails/"+esc(id), patch, nil)
			})
		}
	}
	if !pl.opt.Prune || lo == nil {
		return
	}
	for _, lg := range lo.guardrails {
		if _, ok := lo.scopeRef(lg.ScopeType, lg.ScopeID); seen[lg.Name] || !ok {
			continue // scopes a document cannot express are left alone
		}
		id := lg.ID
		pl.add("delete", "guardrail", do.Slug+"/"+lg.Name, nil, func(ctx context.Context, c *Client, _ map[string]string) error {
			return c.Do(ctx, "DELETE", "/api/guardrails/"+esc(id), nil, nil)
		})
	}
}

func (pl *planner) budgets(do Org, lo *orgState) {
	if do.Budgets == nil {
		return
	}
	orgKey := "org:" + do.Slug
	seen := map[string]bool{}
	for _, db := range do.Budgets {
		scope := orDefault(db.Scope, "org")
		period := orDefault(db.Period, "monthly")
		id := scope + " " + period
		if db.PerUser {
			id += " per-user"
		}
		name := do.Slug + "/" + id
		if seen[id] {
			pl.fail("budget %s is listed twice", name)
			continue
		}
		seen[id] = true
		typ, sk, err := pl.scopeKey(do.Slug, scope)
		if err != nil {
			pl.fail("budget %s: %v", name, err)
			continue
		}
		if db.LimitUSD == nil && db.LimitTokens == nil {
			pl.fail("budget %s: set limit_usd and/or limit_tokens", name)
			continue
		}
		var lb *lBudget
		if lo != nil {
			for i := range lo.budgets {
				b := &lo.budgets[i]
				if ref, ok := lo.scopeRef(b.ScopeType, b.ScopeID); ok && ref == scope && b.Period == period && b.PerUser == db.PerUser {
					lb = b
				}
			}
		}
		if lb == nil {
			db := db
			pl.add("create", "budget", name, nil, func(ctx context.Context, c *Client, ids map[string]string) error {
				body := map[string]any{"scope_type": typ, "scope_id": ids[sk], "period": period, "per_user": db.PerUser}
				if db.LimitUSD != nil {
					body["limit_usd"] = *db.LimitUSD
				}
				if db.LimitTokens != nil {
					body["limit_tokens"] = *db.LimitTokens
				}
				if db.AlertPct != nil {
					body["alert_pct"] = *db.AlertPct
				}
				if db.Action != "" {
					body["action"] = db.Action
				}
				return c.Do(ctx, "POST", "/api/orgs/"+esc(ids[orgKey])+"/budgets", body, nil)
			})
			continue
		}
		patch, fields := map[string]any{}, []string(nil)
		if !eqPtr(db.LimitUSD, lb.LimitUSD) {
			fields = append(fields, "limit_usd")
			if db.LimitUSD == nil {
				patch["clear_usd"] = true
			} else {
				patch["limit_usd"] = *db.LimitUSD
			}
		}
		if !eqPtr(db.LimitTokens, lb.LimitTokens) {
			fields = append(fields, "limit_tokens")
			if db.LimitTokens == nil {
				patch["clear_tokens"] = true
			} else {
				patch["limit_tokens"] = *db.LimitTokens
			}
		}
		if db.AlertPct != nil && *db.AlertPct != lb.AlertPct {
			patch["alert_pct"], fields = *db.AlertPct, append(fields, "alert_pct")
		}
		if db.Action != "" && db.Action != lb.Action {
			patch["action"], fields = db.Action, append(fields, "action")
		}
		if len(patch) > 0 {
			bid := lb.ID
			pl.add("update", "budget", name, fields, func(ctx context.Context, c *Client, _ map[string]string) error {
				return c.Do(ctx, "PATCH", "/api/budgets/"+esc(bid), patch, nil)
			})
		}
	}
	if !pl.opt.Prune || lo == nil {
		return
	}
	for _, lb := range lo.budgets {
		ref, ok := lo.scopeRef(lb.ScopeType, lb.ScopeID)
		if !ok {
			continue
		}
		id := ref + " " + lb.Period
		if lb.PerUser {
			id += " per-user"
		}
		if seen[id] {
			continue
		}
		bid := lb.ID
		pl.add("delete", "budget", do.Slug+"/"+id, nil, func(ctx context.Context, c *Client, _ map[string]string) error {
			return c.Do(ctx, "DELETE", "/api/budgets/"+esc(bid), nil, nil)
		})
	}
}

// ---- helpers ----

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func deref[T any](p *T) T {
	var z T
	if p == nil {
		return z
	}
	return *p
}

func eqPtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// jsonEq compares two JSON values semantically; absent, null and {} are
// treated as the same empty object.
func jsonEq(a, b json.RawMessage) bool {
	if emptyObj(a) && emptyObj(b) {
		return true
	}
	return bytes.Equal(canon(a), canon(b))
}

func canon(raw json.RawMessage) []byte {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	b, _ := json.Marshal(v)
	return b
}

func checkSettings(raw json.RawMessage) error {
	if raw == nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("settings must be a mapping")
	}
	return nil
}
