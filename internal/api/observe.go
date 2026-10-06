package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"theophylax/internal/auth"
	"theophylax/internal/gateway"
	"theophylax/internal/pricing"
	"theophylax/internal/store"
	"theophylax/internal/util"
)

var Version = "dev"

// logFilter builds a visibility predicate on request_logs for the caller:
// org admins see the whole org; team/project admins their scopes; everyone their own traffic.
func (a *API) logFilter(c *Ctx, orgID string, args *[]any) string {
	*args = append(*args, orgID)
	cond := fmt.Sprintf("l.org_id=$%d", len(*args))
	if c.P.Can(auth.Scope{Type: "org", ID: orgID, OrgID: orgID}, "viewer") {
		return cond
	}
	var teams, projects []string
	for _, m := range c.P.Memberships {
		if m.OrgID != orgID || auth.RoleRank(m.Role) < auth.RoleRank("admin") {
			continue
		}
		if m.ScopeType == "team" {
			teams = append(teams, m.ScopeID)
		} else if m.ScopeType == "project" {
			projects = append(projects, m.ScopeID)
		}
	}
	*args = append(*args, teams, projects, c.P.UserID())
	n := len(*args)
	return fmt.Sprintf("%s AND (l.team_id = ANY($%d) OR l.project_id = ANY($%d) OR l.user_id=$%d)", cond, n-2, n-1, n)
}

type logView struct {
	store.RequestLog
	KeyName     *string `json:"key_name" db:"key_name"`
	ProjectName *string `json:"project_name" db:"project_name"`
	UserEmail   *string `json:"user_email" db:"user_email"`
	HasPayload  bool    `json:"has_payload" db:"has_payload"`
}

func (a *API) listLogs(c *Ctx) (any, error) {
	o, _, err := a.orgCtx(c, "")
	if err != nil {
		return nil, err
	}
	var args []any
	where := a.logFilter(c, o.ID, &args)
	add := func(cond string, v any) {
		args = append(args, v)
		where += fmt.Sprintf(" AND "+cond, len(args))
	}
	q := c.R.URL.Query()
	if v := q.Get("model"); v != "" {
		add("l.model=$%d", v)
	}
	if v := q.Get("project_id"); v != "" {
		add("l.project_id=$%d", v)
	}
	if v := q.Get("team_id"); v != "" {
		add("l.team_id=$%d", v)
	}
	if v := q.Get("key_id"); v != "" {
		add("l.key_id=$%d", v)
	}
	if v := q.Get("user_id"); v != "" {
		add("l.user_id=$%d", v)
	}
	if v := q.Get("agent_id"); v != "" {
		add("l.agent_id=$%d", v)
	}
	if v := q.Get("request_id"); v != "" {
		add("l.id=$%d", v)
	}
	switch q.Get("status") {
	case "ok":
		where += " AND l.status < 400"
	case "error":
		where += " AND l.status >= 400"
	case "guardrail":
		where += " AND l.guardrails <> '[]'::jsonb"
	}
	if v := q.Get("before"); v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			add("l.ts < $%d", t)
		}
	}
	add("l.ts > $%d", since(q.Get("since"), 7*24*time.Hour))
	limit := 100
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n <= 500 {
		limit = n
	}
	sql := `SELECT l.*, k.name AS key_name, p.name AS project_name, u.email AS user_email,
		EXISTS(SELECT 1 FROM request_payloads rp WHERE rp.request_id=l.id) AS has_payload
		FROM request_logs l LEFT JOIN api_keys k ON k.id=l.key_id LEFT JOIN projects p ON p.id=l.project_id LEFT JOIN users u ON u.id=l.user_id
		WHERE ` + where + fmt.Sprintf(" ORDER BY l.ts DESC LIMIT %d", limit)
	return store.List[logView](c, a.St.Pool, sql, args...)
}

func (a *API) getLog(c *Ctx) (any, error) {
	l, err := store.Get[logView](c, a.St.Pool, `SELECT l.*, k.name AS key_name, p.name AS project_name, u.email AS user_email, false AS has_payload
		FROM request_logs l LEFT JOIN api_keys k ON k.id=l.key_id LEFT JOIN projects p ON p.id=l.project_id LEFT JOIN users u ON u.id=l.user_id WHERE l.id=$1`, c.Param("id"))
	if err != nil {
		return nil, err
	}
	orgID := util.Deref(l.OrgID)
	if !c.P.InOrg(orgID) {
		return nil, util.ErrNotFound
	}
	// Re-check visibility via the same predicate.
	var args []any
	where := a.logFilter(c, orgID, &args)
	args = append(args, l.ID)
	var ok bool
	_ = a.St.Pool.QueryRow(c, fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM request_logs l WHERE %s AND l.id=$%d)`, where, len(args)), args...).Scan(&ok)
	if !ok {
		return nil, util.ErrNotFound
	}
	out := map[string]any{"log": l}
	// Payloads are sensitive: only admins of the request's scope may read them.
	s := auth.Scope{Type: "org", ID: orgID, OrgID: orgID}
	if l.ProjectID != nil {
		s = auth.Scope{Type: "project", ID: *l.ProjectID, OrgID: orgID, TeamID: util.Deref(l.TeamID)}
	}
	if c.P.Can(s, "admin") {
		var req, resp *string
		if a.St.Pool.QueryRow(c, `SELECT request::text, response::text FROM request_payloads WHERE request_id=$1`, l.ID).Scan(&req, &resp) == nil {
			out["request"], out["response"] = req, resp
			c.Audit("log.payload_view", orgID, "request", l.ID, nil)
		}
	}
	return out, nil
}

type bucket struct {
	T        time.Time `json:"t"`
	Requests int64     `json:"requests"`
	Errors   int64     `json:"errors"`
	Tokens   int64     `json:"tokens"`
	Cost     float64   `json:"cost"`
}

type breakdownRow struct {
	ID       string  `json:"id"`
	Label    string  `json:"label"`
	Requests int64   `json:"requests"`
	Errors   int64   `json:"errors"`
	Input    int64   `json:"input_tokens"`
	Output   int64   `json:"output_tokens"`
	Cached   int64   `json:"cached_tokens"`
	Cost     float64 `json:"cost"`
	P50      float64 `json:"p50_ms"`
	P95      float64 `json:"p95_ms"`
}

func (a *API) usage(c *Ctx) (any, error) {
	o, _, err := a.orgCtx(c, "")
	if err != nil {
		return nil, err
	}
	days, _ := strconv.Atoi(c.Query("days"))
	if days <= 0 || days > 365 {
		days = 30
	}
	interval := "day"
	if days <= 2 {
		interval = "hour"
	}
	var args []any
	where := a.logFilter(c, o.ID, &args)
	args = append(args, time.Now().AddDate(0, 0, -days))
	where += fmt.Sprintf(" AND l.ts > $%d", len(args))
	if pid := c.Query("project_id"); pid != "" {
		args = append(args, pid)
		where += fmt.Sprintf(" AND l.project_id=$%d", len(args))
	}
	if tid := c.Query("team_id"); tid != "" {
		args = append(args, tid)
		where += fmt.Sprintf(" AND l.team_id=$%d", len(args))
	}
	if c.Query("mine") == "true" {
		args = append(args, c.P.UserID())
		where += fmt.Sprintf(" AND l.user_id=$%d", len(args))
	}

	series := []bucket{}
	rows, err := a.St.Pool.Query(c, `SELECT date_trunc('`+interval+`', l.ts AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS t, count(*), count(*) FILTER (WHERE status>=400),
		COALESCE(sum(input_tokens+output_tokens+cache_read_tokens+cache_write_tokens),0), COALESCE(sum(cost_usd),0)
		FROM request_logs l WHERE `+where+` GROUP BY 1 ORDER BY 1`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var b bucket
		if err := rows.Scan(&b.T, &b.Requests, &b.Errors, &b.Tokens, &b.Cost); err != nil {
			rows.Close()
			return nil, err
		}
		series = append(series, b)
	}
	rows.Close()
	series = fillBuckets(series, interval, days)

	groupCol, labelSQL := "l.model", "l.model"
	switch c.Query("group_by") {
	case "team":
		groupCol, labelSQL = "l.team_id", "COALESCE((SELECT name FROM teams WHERE id=l.team_id),'—')"
	case "project":
		groupCol, labelSQL = "l.project_id", "COALESCE((SELECT t.name||' / '||p.name FROM projects p JOIN teams t ON t.id=p.team_id WHERE p.id=l.project_id),'—')"
	case "key":
		groupCol, labelSQL = "l.key_id", "COALESCE((SELECT name FROM api_keys WHERE id=l.key_id),'(session / SSO)')"
	case "user":
		groupCol, labelSQL = "l.user_id", "COALESCE((SELECT email FROM users WHERE id=l.user_id),'(service key)')"
	case "provider":
		groupCol, labelSQL = "l.provider_id", "COALESCE((SELECT name FROM providers WHERE id=l.provider_id),'—')"
	case "agent":
		groupCol, labelSQL = "l.agent_id", "COALESCE((SELECT name FROM agents WHERE id=l.agent_id),'(no agent)')"
	}
	breakdown := []breakdownRow{}
	rows, err = a.St.Pool.Query(c, `SELECT COALESCE(`+groupCol+`,''), min(`+labelSQL+`), count(*), count(*) FILTER (WHERE status>=400),
		COALESCE(sum(input_tokens),0), COALESCE(sum(output_tokens),0), COALESCE(sum(cache_read_tokens),0), COALESCE(sum(cost_usd),0),
		COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY latency_ms),0), COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms),0)
		FROM request_logs l WHERE `+where+` GROUP BY 1 ORDER BY 8 DESC, 3 DESC LIMIT 50`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var b breakdownRow
		if err := rows.Scan(&b.ID, &b.Label, &b.Requests, &b.Errors, &b.Input, &b.Output, &b.Cached, &b.Cost, &b.P50, &b.P95); err != nil {
			rows.Close()
			return nil, err
		}
		breakdown = append(breakdown, b)
	}
	rows.Close()

	var tot struct {
		Requests, Errors, Tokens int64
		Cost, P50, P95, TTFB     float64
	}
	_ = a.St.Pool.QueryRow(c, `SELECT count(*), count(*) FILTER (WHERE status>=400), COALESCE(sum(input_tokens+output_tokens+cache_read_tokens+cache_write_tokens),0),
		COALESCE(sum(cost_usd),0), COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY latency_ms),0), COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms),0),
		COALESCE(avg(ttfb_ms) FILTER (WHERE status<400),0) FROM request_logs l WHERE `+where, args...).Scan(&tot.Requests, &tot.Errors, &tot.Tokens, &tot.Cost, &tot.P50, &tot.P95, &tot.TTFB)
	return map[string]any{"interval": interval, "series": series, "breakdown": breakdown,
		"totals": map[string]any{"requests": tot.Requests, "errors": tot.Errors, "tokens": tot.Tokens, "cost": tot.Cost, "p50_ms": tot.P50, "p95_ms": tot.P95, "avg_ttfb_ms": tot.TTFB}}, nil
}

func (a *API) overview(c *Ctx) (any, error) {
	o, os, err := a.orgCtx(c, "")
	if err != nil {
		return nil, err
	}
	var args []any
	where := a.logFilter(c, o.ID, &args)
	stat := func(from, to time.Duration) map[string]any {
		var req, errs, tok int64
		var cost, p95 float64
		ar := append(append([]any{}, args...), time.Now().Add(-from), time.Now().Add(-to))
		_ = a.St.Pool.QueryRow(c, fmt.Sprintf(`SELECT count(*), count(*) FILTER (WHERE status>=400), COALESCE(sum(input_tokens+output_tokens+cache_read_tokens+cache_write_tokens),0),
			COALESCE(sum(cost_usd),0), COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms),0) FROM request_logs l WHERE %s AND l.ts > $%d AND l.ts <= $%d`, where, len(ar)-1, len(ar)), ar...).
			Scan(&req, &errs, &tok, &cost, &p95)
		return map[string]any{"requests": req, "errors": errs, "tokens": tok, "cost": cost, "p95_ms": p95}
	}
	counts := map[string]int64{}
	for k, q := range map[string]string{
		"teams": `SELECT count(*) FROM teams WHERE org_id=$1`, "projects": `SELECT count(*) FROM projects WHERE org_id=$1`,
		"keys": `SELECT count(*) FROM api_keys WHERE org_id=$1 AND revoked_at IS NULL AND kind='gateway'`, "members": `SELECT count(DISTINCT user_id) FROM memberships WHERE org_id=$1`,
		"agents": `SELECT count(*) FROM agents WHERE org_id=$1`, "skills": `SELECT count(*) FROM registry_items WHERE org_id=$1 AND approved_version<>''`,
		"pending_reviews": `SELECT count(*) FROM registry_items WHERE org_id=$1 AND status='pending_review'`,
	} {
		var n int64
		_ = a.St.Pool.QueryRow(c, q, o.ID).Scan(&n)
		counts[k] = n
	}
	snap := a.Loader.Snapshot()
	models := 0
	seen := map[string]bool{}
	for _, r := range snap.Models {
		if !seen[r.Model.ID] && len(r.Order(o.ID)) > 0 {
			seen[r.Model.ID] = true
			models++
		}
	}
	counts["models"] = int64(models)
	var unhealthy []map[string]string
	for _, p := range snap.Providers {
		if p.Err != "" && (p.Row.OrgID == nil || *p.Row.OrgID == o.ID) {
			unhealthy = append(unhealthy, map[string]string{"name": p.Row.Name, "error": p.Err})
		}
	}
	type topRow struct {
		Model    string  `json:"model"`
		Requests int64   `json:"requests"`
		Cost     float64 `json:"cost"`
	}
	var top []topRow
	ar := append(append([]any{}, args...), time.Now().Add(-24*time.Hour))
	rows, err := a.St.Pool.Query(c, fmt.Sprintf(`SELECT model, count(*), COALESCE(sum(cost_usd),0) FROM request_logs l WHERE %s AND l.ts > $%d GROUP BY 1 ORDER BY 2 DESC LIMIT 5`, where, len(ar)), ar...)
	if err == nil {
		for rows.Next() {
			var t topRow
			_ = rows.Scan(&t.Model, &t.Requests, &t.Cost)
			top = append(top, t)
		}
		rows.Close()
	}
	var guard int64
	_ = a.St.Pool.QueryRow(c, fmt.Sprintf(`SELECT count(*) FROM request_logs l WHERE %s AND l.ts > $%d AND l.guardrails <> '[]'::jsonb`, where, len(ar)), ar...).Scan(&guard)
	// Budgets close to their limit.
	var alerts []map[string]any
	for key, list := range snap.Budgets {
		for _, b := range list {
			if b.OrgID != o.ID || b.PerUser {
				continue
			}
			cost, tok := a.Budgets.Spent(b)
			pct := 0.0
			if b.LimitUSD != nil && *b.LimitUSD > 0 {
				pct = cost / *b.LimitUSD * 100
			}
			if b.LimitTokens != nil && *b.LimitTokens > 0 {
				pct = max(pct, float64(tok)/float64(*b.LimitTokens)*100)
			}
			if pct >= float64(b.AlertPct) {
				alerts = append(alerts, map[string]any{"budget_id": b.ID, "scope": key, "scope_name": a.scopeName(snap, b.ScopeType, b.ScopeID), "period": b.Period, "pct": pct})
			}
		}
	}
	return map[string]any{"last_24h": stat(24*time.Hour, 0), "prev_24h": stat(48*time.Hour, 24*time.Hour), "counts": counts, "top_models": top,
		"guardrail_events_24h": guard, "budget_alerts": alerts, "unhealthy_providers": unhealthy, "role": c.P.RoleAt(os)}, nil
}

type auditView struct {
	store.AuditEntry
}

func (a *API) auditQuery(c *Ctx, orgID string) (any, error) {
	q := c.R.URL.Query()
	where := "TRUE"
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where += fmt.Sprintf(" AND "+cond, len(args))
	}
	if orgID != "" {
		add("org_id=$%d", orgID)
	}
	if v := q.Get("action"); v != "" {
		add("action LIKE $%d", strings.TrimSuffix(v, "*")+"%")
	}
	if v := q.Get("actor"); v != "" {
		add("(actor_email ILIKE $%d OR actor_id=$"+fmt.Sprint(len(args)+1)+")", "%"+v+"%")
		args = append(args, v)
	}
	if v := q.Get("target_id"); v != "" {
		add("target_id=$%d", v)
	}
	if v, err := strconv.ParseInt(q.Get("before"), 10, 64); err == nil {
		add("seq < $%d", v)
	}
	return store.List[store.AuditEntry](c, a.St.Pool, `SELECT * FROM audit_log WHERE `+where+` ORDER BY seq DESC LIMIT 200`, args...)
}

func (a *API) orgAudit(c *Ctx) (any, error) {
	o, _, err := a.orgCtx(c, "admin")
	if err != nil {
		return nil, err
	}
	return a.auditQuery(c, o.ID)
}

func (a *API) platformAudit(c *Ctx) (any, error) {
	if err := c.RequirePlatformAdmin(); err != nil {
		return nil, err
	}
	return a.auditQuery(c, "")
}

func (a *API) verifyAudit(c *Ctx) (any, error) {
	if err := c.RequirePlatformAdmin(); err != nil {
		return nil, err
	}
	return a.Audit.Verify(c)
}

func (a *API) system(c *Ctx) (any, error) {
	if err := c.RequirePlatformAdmin(); err != nil {
		return nil, err
	}
	snap := a.Loader.Snapshot()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	ps := a.St.Pool.Stat()
	return map[string]any{"version": Version, "go": runtime.Version(), "goroutines": runtime.NumGoroutine(), "heap_mb": ms.HeapAlloc >> 20,
		"config_loaded_at": snap.Loaded, "providers": len(snap.Providers), "routes": len(snap.Models) + len(snap.Wildcards),
		"db":     map[string]any{"total_conns": ps.TotalConns(), "idle_conns": ps.IdleConns(), "max_conns": ps.MaxConns()},
		"export": a.Export.Describe(), "rate_limits": map[bool]string{true: "fleet-wide", false: "per replica"}[a.RateLimitsGlobal]}, nil
}

// --- Skills for clients (Claude Code sync): gateway-key authenticated.

func (a *API) SkillRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/skills", a.wrap(false, a.clientSkills))
	mux.HandleFunc("GET /v1/mcp/servers", a.wrap(false, a.clientMCPServers))
	mux.HandleFunc("GET /v1/skills/{id}/download", func(w http.ResponseWriter, r *http.Request) {
		a.wrap(false, func(c *Ctx) (any, error) {
			p, err := a.Res.FromKey(c, auth.BearerToken(r), "gateway")
			if err != nil {
				return nil, err
			}
			items, err := a.installedItems(c, p, "skill", c.Param("id"))
			if err != nil {
				return nil, err
			}
			if len(items) == 0 {
				return nil, util.ErrNotFound
			}
			return rawResponse{}, writeSkillZip(c, a, &items[0].RegistryItem, items[0].EffectiveVersion, w)
		})(w, r)
	})
}

// installedItem is a registry item installed for a caller, with the version
// that applies to it.
type installedItem struct {
	store.RegistryItem
	EffectiveVersion string `db:"effective_version"`
}

// installedItems lists approved items of a kind installed for the caller's
// org, team or project. When an item is installed at several of those scopes
// the most specific installation (project, then team, then org) decides the
// version: its pinned version, or else the item's approved version.
func (a *API) installedItems(c *Ctx, p *auth.Principal, kind, itemID string) ([]installedItem, error) {
	q := `SELECT DISTINCT ON (i.id) i.*, CASE WHEN x.pinned THEN x.version ELSE i.approved_version END AS effective_version
	FROM registry_items i JOIN installations x ON x.item_id=i.id
	WHERE i.kind=$4 AND i.approved_version<>'' AND i.status<>'deprecated' AND i.org_id=$1
	AND ((x.scope_type='org' AND x.scope_id=$1) OR (x.scope_type='team' AND x.scope_id=$2) OR (x.scope_type='project' AND x.scope_id=$3))`
	args := []any{p.OrgID, p.TeamID, p.ProjectID, kind}
	if itemID != "" {
		args = append(args, itemID)
		q += ` AND i.id=$5`
	}
	q += ` ORDER BY i.id, CASE x.scope_type WHEN 'project' THEN 0 WHEN 'team' THEN 1 ELSE 2 END`
	return store.List[installedItem](c, a.St.Pool, q, args...)
}

// clientMCPServers lists the approved MCP servers installed for the caller's
// scope, at the version that applies to it, plus the same servers as an
// .mcp.json document for Claude Code. Stored authorization tokens stay in the
// gateway (they are only injected for managed agents); servers that have one
// are marked requires_authorization.
func (a *API) clientMCPServers(c *Ctx) (any, error) {
	p, err := a.Res.FromKey(c, auth.BearerToken(c.R), "gateway")
	if err != nil {
		return nil, err
	}
	items, err := a.installedItems(c, p, "mcp_server", "")
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(items))
	servers := map[string]any{}
	for _, it := range items {
		var raw json.RawMessage
		if err := a.St.Pool.QueryRow(c, `SELECT content FROM registry_versions WHERE item_id=$1 AND version=$2`, it.ID, it.EffectiveVersion).Scan(&raw); err != nil {
			continue
		}
		var content struct {
			Transport string   `json:"transport"`
			URL       string   `json:"url"`
			Command   string   `json:"command"`
			Args      []string `json:"args"`
			HasToken  bool     `json:"has_authorization_token"`
		}
		_ = json.Unmarshal(raw, &content)
		entry := map[string]any{}
		switch {
		case content.URL != "":
			if content.Transport != "sse" {
				content.Transport = "http"
			}
			entry["type"], entry["url"] = content.Transport, content.URL
		case content.Command != "":
			content.Transport = "stdio"
			cmd, args := content.Command, content.Args
			if args == nil {
				if f := strings.Fields(cmd); len(f) > 0 {
					cmd, args = f[0], f[1:]
				}
			}
			entry["type"], entry["command"], entry["args"] = "stdio", cmd, args
		default:
			continue
		}
		servers[it.Slug] = entry
		item := map[string]any{"id": it.ID, "slug": it.Slug, "name": it.Name, "description": it.Description, "version": it.EffectiveVersion,
			"transport": content.Transport, "requires_authorization": content.HasToken}
		for k, v := range entry {
			if k != "type" {
				item[k] = v
			}
		}
		out = append(out, item)
	}
	return map[string]any{"data": out, "mcp_json": map[string]any{"mcpServers": servers}}, nil
}

func (a *API) clientSkills(c *Ctx) (any, error) {
	p, err := a.Res.FromKey(c, auth.BearerToken(c.R), "gateway")
	if err != nil {
		return nil, err
	}
	items, err := a.installedItems(c, p, "skill", "")
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]any{"id": it.ID, "slug": it.Slug, "name": it.Name, "description": it.Description, "version": it.EffectiveVersion,
			"download_url": a.Cfg.PublicURL + "/v1/skills/" + it.ID + "/download"})
	}
	return map[string]any{"data": out}, nil
}

// getReceipt returns a verified Proof-of-Inference receipt to anyone who can see the request log.
func (a *API) getReceipt(c *Ctx) (any, error) {
	var orgID string
	if err := a.St.Pool.QueryRow(c, `SELECT COALESCE(org_id,'') FROM receipts WHERE request_id=$1`, c.Param("id")).Scan(&orgID); err != nil || !c.P.InOrg(orgID) {
		return nil, util.ErrNotFound
	}
	var args []any
	where := a.logFilter(c, orgID, &args)
	args = append(args, c.Param("id"))
	var ok bool
	_ = a.St.Pool.QueryRow(c, fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM request_logs l WHERE %s AND l.id=$%d)`, where, len(args)), args...).Scan(&ok)
	if !ok {
		return nil, util.ErrNotFound
	}
	out, err := gateway.LookupReceipt(c, a.St, a.Signer, c.Param("id"))
	if err != nil {
		return nil, err
	}
	out["public_keys_url"] = a.Cfg.PublicURL + "/.well-known/theophylax-receipt-keys.json"
	return out, nil
}

func (a *API) listCheckpoints(c *Ctx) (any, error) {
	if err := c.RequirePlatformAdmin(); err != nil {
		return nil, err
	}
	type ck struct {
		ID         string    `json:"id" db:"id"`
		TS         time.Time `json:"ts" db:"ts"`
		FirstTS    time.Time `json:"first_ts" db:"first_ts"`
		LastTS     time.Time `json:"last_ts" db:"last_ts"`
		Count      int       `json:"count" db:"count"`
		MerkleRoot []byte    `json:"merkle_root" db:"merkle_root"`
		AuditID    *string   `json:"audit_id" db:"audit_id"`
	}
	return store.List[ck](c, a.St.Pool, `SELECT * FROM receipt_checkpoints ORDER BY ts DESC LIMIT 100`)
}

// fillBuckets inserts zero-valued buckets so charts show quiet periods honestly.
func fillBuckets(in []bucket, interval string, days int) []bucket {
	step := 24 * time.Hour
	if interval == "hour" {
		step = time.Hour
	}
	end := time.Now().UTC().Truncate(step)
	start := end.Add(-time.Duration(days) * 24 * time.Hour).Add(step)
	have := map[int64]bucket{}
	for _, b := range in {
		have[b.T.UTC().Truncate(step).Unix()] = b
	}
	out := make([]bucket, 0, int(end.Sub(start)/step)+1)
	for t := start; !t.After(end); t = t.Add(step) {
		if b, ok := have[t.Unix()]; ok {
			out = append(out, b)
		} else {
			out = append(out, bucket{T: t})
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Billing: exact consumption cost, priced from upstream list prices.

func (a *API) pricingStatus(c *Ctx) (any, error) {
	return a.Pricing.Status(), nil
}

func (a *API) refreshPricing(c *Ctx) (any, error) {
	if err := c.RequirePlatformAdmin(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(c, time.Minute)
	defer cancel()
	if err := a.Pricing.Refresh(ctx); err != nil {
		return nil, util.Err(502, "api_error", "refreshing price catalog: "+err.Error())
	}
	st := a.Pricing.Status()
	c.Audit("pricing.refresh", "", "pricing", "catalog", st)
	return st, nil
}

type billingLine struct {
	ProviderKind  string         `json:"provider_kind"`
	ProviderName  string         `json:"provider_name"`
	UpstreamModel string         `json:"upstream_model"`
	PriceKey      string         `json:"price_key"`
	Requests      int64          `json:"requests"`
	Input         int64          `json:"input_tokens"`
	Output        int64          `json:"output_tokens"`
	CacheRead     int64          `json:"cache_read_tokens"`
	CacheWrite    int64          `json:"cache_write_tokens"`
	Cost          float64        `json:"cost"`
	Rates         *pricing.Quote `json:"rates"`
}

// billing returns a calendar month's consumption, one line per upstream model
// and catalog price, as an invoice-style statement.
func (a *API) billing(c *Ctx) (any, error) {
	o, _, err := a.orgCtx(c, "")
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	if m := c.Query("month"); m != "" {
		t, err := time.Parse("2006-01", m)
		if err != nil {
			return nil, badReq("month must be YYYY-MM")
		}
		start = t
	}
	end := start.AddDate(0, 1, 0)

	var args []any
	where := a.logFilter(c, o.ID, &args)
	args = append(args, start, end)
	where += fmt.Sprintf(" AND l.ts >= $%d AND l.ts < $%d", len(args)-1, len(args))
	if c.Query("mine") == "true" {
		args = append(args, c.P.UserID())
		where += fmt.Sprintf(" AND l.user_id=$%d", len(args))
	}

	lines := []billingLine{}
	rows, err := a.St.Pool.Query(c, `SELECT l.provider_kind, COALESCE(min(p.name),''), l.upstream_model, l.price_key, count(*),
		COALESCE(sum(l.input_tokens),0), COALESCE(sum(l.output_tokens),0), COALESCE(sum(l.cache_read_tokens),0), COALESCE(sum(l.cache_write_tokens),0), COALESCE(sum(l.cost_usd),0)
		FROM request_logs l LEFT JOIN providers p ON p.id=l.provider_id
		WHERE `+where+` AND l.upstream_model <> ''
		GROUP BY l.provider_kind, l.upstream_model, l.price_key ORDER BY 10 DESC, 5 DESC`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var b billingLine
		if err := rows.Scan(&b.ProviderKind, &b.ProviderName, &b.UpstreamModel, &b.PriceKey, &b.Requests, &b.Input, &b.Output, &b.CacheRead, &b.CacheWrite, &b.Cost); err != nil {
			rows.Close()
			return nil, err
		}
		if b.PriceKey != "" {
			b.Rates = a.Pricing.QuoteKey(b.PriceKey)
		}
		lines = append(lines, b)
	}
	rows.Close()

	var tot struct {
		Cost                                   float64
		Requests, Input, Output, CRead, CWrite int64
		UnpricedRequests, UnpricedTokens       int64
	}
	for _, l := range lines {
		tot.Cost += l.Cost
		tot.Requests += l.Requests
		tot.Input += l.Input
		tot.Output += l.Output
		tot.CRead += l.CacheRead
		tot.CWrite += l.CacheWrite
		if l.PriceKey == "" {
			if tk := l.Input + l.Output + l.CacheRead + l.CacheWrite; tk > 0 {
				tot.UnpricedRequests += l.Requests
				tot.UnpricedTokens += tk
			}
		}
	}

	daily := []bucket{}
	rows, err = a.St.Pool.Query(c, `SELECT date_trunc('day', l.ts AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', count(*), count(*) FILTER (WHERE status>=400),
		COALESCE(sum(input_tokens+output_tokens+cache_read_tokens+cache_write_tokens),0), COALESCE(sum(cost_usd),0)
		FROM request_logs l WHERE `+where+` GROUP BY 1 ORDER BY 1`, args...)
	if err != nil {
		return nil, err
	}
	have := map[string]bucket{}
	for rows.Next() {
		var b bucket
		if err := rows.Scan(&b.T, &b.Requests, &b.Errors, &b.Tokens, &b.Cost); err != nil {
			rows.Close()
			return nil, err
		}
		have[b.T.UTC().Format("2006-01-02")] = b
	}
	rows.Close()
	last := end
	if today := now.Truncate(24 * time.Hour).Add(24 * time.Hour); today.Before(last) {
		last = today
	}
	for d := start; d.Before(last); d = d.AddDate(0, 0, 1) {
		b, ok := have[d.Format("2006-01-02")]
		if !ok {
			b = bucket{T: d}
		}
		daily = append(daily, b)
	}

	type teamRow struct {
		Label string  `json:"label"`
		Cost  float64 `json:"cost"`
	}
	teams := []teamRow{}
	rows, err = a.St.Pool.Query(c, `SELECT COALESCE((SELECT name FROM teams WHERE id=l.team_id),'—'), COALESCE(sum(cost_usd),0)
		FROM request_logs l WHERE `+where+` GROUP BY l.team_id HAVING sum(cost_usd) > 0 ORDER BY 2 DESC LIMIT 20`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t teamRow
		if err := rows.Scan(&t.Label, &t.Cost); err != nil {
			rows.Close()
			return nil, err
		}
		teams = append(teams, t)
	}
	rows.Close()

	months := []string{}
	for i := 0; i < 12; i++ {
		months = append(months, time.Date(now.Year(), now.Month()-time.Month(i), 1, 0, 0, 0, 0, time.UTC).Format("2006-01"))
	}
	return map[string]any{
		"period": map[string]any{"month": start.Format("2006-01"), "start": start, "end": end},
		"totals": map[string]any{"cost": tot.Cost, "requests": tot.Requests, "input_tokens": tot.Input, "output_tokens": tot.Output,
			"cache_read_tokens": tot.CRead, "cache_write_tokens": tot.CWrite, "unpriced_requests": tot.UnpricedRequests, "unpriced_tokens": tot.UnpricedTokens},
		"lines": lines, "daily": daily, "by_team": teams, "months": months, "pricing": a.Pricing.Status(),
	}, nil
}
