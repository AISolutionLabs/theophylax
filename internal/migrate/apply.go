package migrate

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"theophylax/internal/auth"
	"theophylax/internal/providers"
	"theophylax/internal/secrets"
	"theophylax/internal/store"
	"theophylax/internal/util"
)

type Options struct {
	OrgSlug string // where unassigned LiteLLM teams and keys land (default: the first org)
	DryRun  bool
}

// Report counts what the import created and what it left alone.
type Report struct {
	Created  map[string]int `json:"created"`
	Existing map[string]int `json:"already_present"`
	Warnings []string       `json:"warnings"`
}

func (r *Report) made(kind string)        { r.Created[kind]++ }
func (r *Report) had(kind string)         { r.Existing[kind]++ }
func (r *Report) warn(f string, a ...any) { r.Warnings = append(r.Warnings, fmt.Sprintf(f, a...)) }

// Apply writes the plan and the LiteLLM database snapshot (either may be nil)
// in one transaction. Re-running is safe: anything already present (by name,
// external ID, email or key hash) is left unchanged.
func Apply(ctx context.Context, st *store.Store, box *secrets.Box, plan *Plan, data *LiteLLMData, opt Options) (*Report, error) {
	rep := &Report{Created: map[string]int{}, Existing: map[string]int{}}
	if plan != nil {
		rep.Warnings = append(rep.Warnings, plan.Warnings...)
	}
	err := st.Tx(ctx, func(tx pgx.Tx) error {
		if plan != nil {
			if err := applyModels(ctx, tx, st, box, plan, rep); err != nil {
				return err
			}
		}
		if data != nil {
			if err := applyDB(ctx, tx, st, data, opt, rep); err != nil {
				return err
			}
		}
		if opt.DryRun {
			return errDryRun
		}
		return nil
	})
	if err == errDryRun {
		err = nil
	}
	if err == nil && !opt.DryRun {
		_, _ = st.Pool.Exec(ctx, "SELECT pg_notify('theophylax_config', '')")
	}
	return rep, err
}

var errDryRun = fmt.Errorf("dry run")

func applyModels(ctx context.Context, tx pgx.Tx, st *store.Store, box *secrets.Box, plan *Plan, rep *Report) error {
	provIDs := map[string]string{}
	for _, p := range plan.Providers {
		var id string
		err := tx.QueryRow(ctx, `SELECT id FROM providers WHERE name=$1 AND org_id IS NULL`, p.Name).Scan(&id)
		if err == nil {
			provIDs[p.Name] = id
			rep.had("providers")
			continue
		}
		c, err := providers.ParseConn(p.Conn)
		if err != nil {
			rep.warn("provider %s: %v — skipped", p.Name, err)
			continue
		}
		row := &store.Provider{ID: util.NewID("prv"), Name: p.Name, Kind: c.Kind, ConnEnc: box.SealString(p.Conn), ConnRedacted: c.Redacted(), Config: json.RawMessage("{}"), Enabled: true}
		if err := st.Insert(ctx, tx, "providers", row); err != nil {
			return fmt.Errorf("provider %s: %w", p.Name, err)
		}
		provIDs[p.Name] = row.ID
		rep.made("providers")
	}
	for _, m := range plan.Models {
		var id string
		if tx.QueryRow(ctx, `SELECT id FROM models WHERE name=$1`, m.Name).Scan(&id) == nil {
			rep.had("models")
			continue
		}
		aliases := m.Aliases
		if aliases == nil {
			aliases = []string{}
		}
		row := &store.Model{ID: util.NewID("mdl"), Name: m.Name, DisplayName: m.Name, Aliases: aliases, InputPrice: m.InputPrice, OutputPrice: m.OutputPrice,
			CacheReadPrice: m.CacheRead, CacheWritePrice: m.CacheWrite, MaxOutput: m.MaxOutput, ContextWindow: m.Context, Capabilities: []string{"streaming", "tools"},
			Status: "active", Strategy: m.Strategy, Source: "litellm"}
		if err := st.Insert(ctx, tx, "models", row); err != nil {
			return fmt.Errorf("model %s: %w", m.Name, err)
		}
		rep.made("models")
		for _, d := range m.Deployments {
			pid := provIDs[d.Provider]
			if pid == "" {
				continue
			}
			if err := st.Insert(ctx, tx, "deployments", &store.Deployment{ID: util.NewID("dep"), ModelID: row.ID, ProviderID: pid, UpstreamModel: d.Upstream, Weight: d.Weight, Priority: d.Priority, Enabled: true}); err != nil {
				return err
			}
			rep.made("deployments")
		}
	}
	return nil
}

type scopeIDs struct{ org, team, project string }

func applyDB(ctx context.Context, tx pgx.Tx, st *store.Store, d *LiteLLMData, opt Options, rep *Report) error {
	// Target org for anything not in a LiteLLM organization.
	var defOrg string
	q := `SELECT id FROM orgs ORDER BY created_at LIMIT 1`
	args := []any{}
	if opt.OrgSlug != "" {
		q, args = `SELECT id FROM orgs WHERE slug=$1`, []any{opt.OrgSlug}
	}
	if err := tx.QueryRow(ctx, q, args...).Scan(&defOrg); err != nil {
		return fmt.Errorf("target organization %q not found", opt.OrgSlug)
	}

	// Organizations.
	orgs := map[string]string{} // litellm org id → theophylax org id
	for _, o := range d.Orgs {
		lid := str(o["organization_id"])
		name := orStr(str(o["organization_alias"]), lid)
		slug := "litellm-" + util.Slugify(name)
		var id string
		if tx.QueryRow(ctx, `SELECT id FROM orgs WHERE slug=$1`, slug).Scan(&id) == nil {
			rep.had("orgs")
		} else {
			id = util.NewID("org")
			if err := st.Insert(ctx, tx, "orgs", &store.Org{ID: id, Slug: slug, Name: name, Settings: settingsJSON(o, nil)}); err != nil {
				return err
			}
			rep.made("orgs")
		}
		orgs[lid] = id
		if b := d.Budgets[str(o["budget_id"])]; b != nil {
			addBudget(ctx, tx, rep, id, "org", id, b)
		}
	}
	orgOf := func(lid string) string {
		if id := orgs[lid]; id != "" {
			return id
		}
		return defOrg
	}

	// Users (by email). Passwords are not migrated; people sign in with SSO
	// or an admin issues a reset.
	users := map[string]string{} // litellm user id → theophylax user id
	userOrg := map[string]string{}
	for _, u := range d.Users {
		email := strings.ToLower(strings.TrimSpace(str(u["user_email"])))
		lid := str(u["user_id"])
		if email == "" || !strings.Contains(email, "@") {
			if strings.Contains(lid, "@") {
				email = strings.ToLower(lid)
			} else {
				continue
			}
		}
		var id string
		if tx.QueryRow(ctx, `SELECT id FROM users WHERE lower(email)=$1`, email).Scan(&id) == nil {
			rep.had("users")
		} else {
			id = util.NewID("usr")
			ext := "litellm:" + lid
			if err := st.Insert(ctx, tx, "users", &store.User{ID: id, Email: email, Name: str(u["user_alias"]), Status: "active", ExternalID: &ext}); err != nil {
				return err
			}
			rep.made("users")
		}
		users[lid] = id
		org := orgOf(str(u["organization_id"]))
		userOrg[lid] = org
		role := "member"
		switch str(u["user_role"]) {
		case "proxy_admin":
			role = "admin"
			rep.warn("%s was a LiteLLM proxy admin; imported as an admin of the organization (grant platform admin explicitly if needed)", email)
		case "proxy_admin_viewer", "internal_user_viewer", "org_admin_viewer":
			role = "viewer"
		case "org_admin":
			role = "admin"
		}
		addMembership(ctx, tx, rep, id, org, "org", org, role)
	}
	for _, m := range d.OrgMembers {
		uid, org := users[str(m["user_id"])], orgs[str(m["organization_id"])]
		if uid == "" || org == "" {
			continue
		}
		role := "member"
		if strings.Contains(str(m["user_role"]), "admin") {
			role = "admin"
		}
		addMembership(ctx, tx, rep, uid, org, "org", org, role)
	}

	// Teams, each with a project for its keys.
	teams := map[string]scopeIDs{}
	ensureTeam := func(lid, name, org string, row map[string]any) (scopeIDs, error) {
		ext := "litellm:" + lid
		var tid string
		if tx.QueryRow(ctx, `SELECT id FROM teams WHERE external_id=$1`, ext).Scan(&tid) == nil {
			rep.had("teams")
		} else {
			tid = util.NewID("team")
			slug := util.Slugify(name)
			var n int
			_ = tx.QueryRow(ctx, `SELECT count(*) FROM teams WHERE org_id=$1 AND slug=$2`, org, slug).Scan(&n)
			if n > 0 || slug == "" {
				slug = strings.Trim(slug+"-"+strings.ToLower(lid[:min(6, len(lid))]), "-")
			}
			if err := st.Insert(ctx, tx, "teams", &store.Team{ID: tid, OrgID: org, Slug: slug, Name: name, Description: "Imported from LiteLLM", Settings: settingsJSON(row, nil), ExternalID: &ext}); err != nil {
				return scopeIDs{}, fmt.Errorf("team %s: %w", name, err)
			}
			rep.made("teams")
		}
		var pid string
		if tx.QueryRow(ctx, `SELECT id FROM projects WHERE team_id=$1 AND slug='litellm-keys'`, tid).Scan(&pid) != nil {
			pid = util.NewID("prj")
			if err := st.Insert(ctx, tx, "projects", &store.Project{ID: pid, OrgID: org, TeamID: tid, Slug: "litellm-keys", Name: "LiteLLM keys", Description: "Keys imported from LiteLLM", Settings: json.RawMessage("{}")}); err != nil {
				return scopeIDs{}, err
			}
			rep.made("projects")
		}
		return scopeIDs{org, tid, pid}, nil
	}
	for _, t := range d.Teams {
		lid := str(t["team_id"])
		s, err := ensureTeam(lid, orStr(str(t["team_alias"]), lid), orgOf(str(t["organization_id"])), t)
		if err != nil {
			return err
		}
		teams[lid] = s
		if t["max_budget"] != nil {
			addBudget(ctx, tx, rep, s.org, "team", s.team, t)
		}
		var members []map[string]any
		_ = json.Unmarshal(jsonOf(t["members_with_roles"]), &members)
		for _, m := range members {
			uid := users[str(m["user_id"])]
			if uid == "" {
				continue
			}
			role := "member"
			if str(m["role"]) == "admin" {
				role = "admin"
			}
			addMembership(ctx, tx, rep, uid, s.org, "team", s.team, role)
		}
		for _, a := range asList(t["admins"]) {
			if uid := users[str(a)]; uid != "" {
				addMembership(ctx, tx, rep, uid, s.org, "team", s.team, "admin")
			}
		}
	}

	// Keys keep their hashes, so every existing sk-… key keeps working.
	var unassigned *scopeIDs
	for _, k := range d.Keys {
		token := str(k["token"])
		hash, err := hex.DecodeString(token)
		if err != nil || len(hash) != 32 {
			if strings.HasPrefix(token, "sk-") { // very old LiteLLM stored plaintext
				hash = auth.HashKey(token)
			} else {
				rep.warn("a key with an unrecognised token format was skipped")
				continue
			}
		}
		s, ok := teams[str(k["team_id"])]
		if !ok {
			if unassigned == nil {
				u, err := ensureTeam("unassigned", "LiteLLM (no team)", orgOf(str(k["organization_id"])), nil)
				if err != nil {
					return err
				}
				unassigned = &u
			}
			s = *unassigned
		}
		name := orStr(str(k["key_alias"]), orStr(str(k["key_name"]), "LiteLLM key"))
		prefix := str(k["key_name"])
		if prefix == "" {
			prefix = "sk-…"
		}
		var allowed []string
		for _, m := range asList(k["models"]) {
			if ms := str(m); ms != "" && ms != "all-proxy-models" && ms != "all-team-models" {
				allowed = append(allowed, ms)
			}
		}
		if allowed == nil {
			allowed = []string{}
		}
		var expires, revoked *time.Time
		if t, err := time.Parse(time.RFC3339Nano, str(k["expires"])); err == nil {
			expires = &t
		}
		if k["blocked"] == true {
			now := time.Now()
			revoked = &now
		}
		var uid *string
		if id := users[str(k["user_id"])]; id != "" {
			uid = &id
		}
		meta, _ := json.Marshal(map[string]any{"imported_from": "litellm", "litellm_key_alias": k["key_alias"], "litellm_spend_usd": k["spend"], "litellm_user_id": k["user_id"], "litellm_team_id": k["team_id"]})
		id := util.NewID("key")
		tag, err := tx.Exec(ctx, `INSERT INTO api_keys (id, kind, org_id, team_id, project_id, user_id, name, prefix, hash, allowed_models, rpm, tpm, expires_at, revoked_at, created_by, metadata)
			VALUES ($1,'gateway',$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'litellm-import',$14) ON CONFLICT (hash) DO NOTHING`,
			id, s.org, s.team, s.project, uid, name, prefix, hash, allowed, int(num(k["rpm_limit"])), int(num(k["tpm_limit"])), expires, revoked, meta)
		if err != nil {
			return fmt.Errorf("key %s: %w", name, err)
		}
		if tag.RowsAffected() == 0 {
			rep.had("keys")
			continue
		}
		rep.made("keys")
		if k["max_budget"] != nil {
			addBudget(ctx, tx, rep, s.org, "key", id, k)
		} else if b := d.Budgets[str(k["budget_id"])]; b != nil {
			addBudget(ctx, tx, rep, s.org, "key", id, b)
		}
		if mb := asMap(k["model_max_budget"]); len(mb) > 0 {
			rep.warn("key %s has per-model budgets; Theophylax budgets apply across models, so set allowed models plus a key budget instead", name)
		}
	}
	if n := countSpend(d.Keys); n > 0 {
		rep.warn("historical spend (%.2f USD across keys) is kept in key metadata; usage dashboards start fresh from the import", n)
	}
	return nil
}

func addMembership(ctx context.Context, tx pgx.Tx, rep *Report, user, org, scopeType, scopeID, role string) {
	tag, err := tx.Exec(ctx, `INSERT INTO memberships (id, user_id, org_id, scope_type, scope_id, role, source) VALUES ($1,$2,$3,$4,$5,$6,'litellm') ON CONFLICT (user_id, scope_type, scope_id) DO NOTHING`,
		util.NewID("mbr"), user, org, scopeType, scopeID, role)
	if err == nil && tag.RowsAffected() > 0 {
		rep.made("memberships")
	}
}

// addBudget maps max_budget + budget_duration to a Theophylax budget.
func addBudget(ctx context.Context, tx pgx.Tx, rep *Report, org, scopeType, scopeID string, row map[string]any) {
	limit := num(row["max_budget"])
	if limit <= 0 {
		return
	}
	period := "total"
	dur := strings.ToLower(str(row["budget_duration"]))
	switch {
	case dur == "":
	case dur == "1d" || dur == "24h" || dur == "daily":
		period = "daily"
	case dur == "30d" || dur == "1mo" || dur == "monthly" || dur == "31d":
		period = "monthly"
	default:
		period = "monthly"
		rep.warn("%s budget with duration %q imported as monthly", scopeType, dur)
	}
	tag, err := tx.Exec(ctx, `INSERT INTO budgets (id, org_id, scope_type, scope_id, period, limit_usd, alert_pct, action) VALUES ($1,$2,$3,$4,$5,$6,80,'block') ON CONFLICT (scope_type, scope_id, period) DO NOTHING`,
		util.NewID("bud"), org, scopeType, scopeID, period, limit)
	if err == nil && tag.RowsAffected() > 0 {
		rep.made("budgets")
	}
}

func settingsJSON(row map[string]any, _ any) json.RawMessage {
	s := store.ScopeSettings{RPM: int(num(row["rpm_limit"])), TPM: int(num(row["tpm_limit"]))}
	for _, m := range asList(row["models"]) {
		if ms := str(m); ms != "" && ms != "all-proxy-models" && ms != "all-team-models" {
			s.AllowedModels = append(s.AllowedModels, ms)
		}
	}
	sort.Strings(s.AllowedModels)
	b, _ := json.Marshal(s)
	return b
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func jsonOf(v any) []byte {
	if s, ok := v.(string); ok {
		return []byte(s)
	}
	b, _ := json.Marshal(v)
	return b
}

func countSpend(keys []map[string]any) float64 {
	t := 0.0
	for _, k := range keys {
		t += num(k["spend"])
	}
	return t
}
