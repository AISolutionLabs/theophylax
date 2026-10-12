package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"theophylax/internal/gateway"
	"theophylax/internal/secrets"
	"theophylax/internal/store"
)

const blockWindow = 5 * time.Minute

// Alerter evaluates budgets, summarises guardrail blocks and delivers alerts.
// Every replica runs it; each alert is claimed in alert_log before sending, so
// it is delivered once across the fleet.
type Alerter struct {
	St        *store.Store
	Box       *secrets.Box
	Loader    *gateway.Loader
	Budgets   *gateway.Budgets
	Deliver   *Deliverer
	PublicURL string

	mu     sync.Mutex
	blocks map[blockKey]*blockAgg
}

type blockKey struct{ org, policy string }

type blockAgg struct {
	count        int64
	rule, detail string
}

// GuardrailBlocked records a blocked request (called from the request path).
func (a *Alerter) GuardrailBlocked(orgID, policy, rule, detail string) {
	if a == nil || orgID == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.blocks == nil {
		a.blocks = map[blockKey]*blockAgg{}
	}
	k := blockKey{orgID, policy}
	g := a.blocks[k]
	if g == nil {
		g = &blockAgg{}
		a.blocks[k] = g
	}
	g.count++
	g.rule, g.detail = rule, truncate(detail, 200)
}

// Run evaluates alerts every 30 seconds until ctx is cancelled.
func (a *Alerter) Run(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.Tick(ctx, time.Now())
		}
	}
}

// Tick runs one evaluation round. Exposed for tests.
func (a *Alerter) Tick(ctx context.Context, now time.Time) {
	if err := a.flushBlocks(ctx, now); err != nil {
		slog.Warn("alerts: recording guardrail blocks failed", "err", err)
	}
	a.budgets(ctx, now)
	a.sendBlockSummaries(ctx, now)
	_, _ = a.St.Pool.Exec(ctx, `DELETE FROM guardrail_block_counts WHERE window_start < $1`, now.Add(-7*24*time.Hour))
	_, _ = a.St.Pool.Exec(ctx, `DELETE FROM alert_log WHERE sent_at < $1`, now.Add(-120*24*time.Hour))
}

func (a *Alerter) flushBlocks(ctx context.Context, now time.Time) error {
	a.mu.Lock()
	pending := a.blocks
	a.blocks = nil
	a.mu.Unlock()
	win := now.UTC().Truncate(blockWindow)
	for k, g := range pending {
		if _, err := a.St.Pool.Exec(ctx, `INSERT INTO guardrail_block_counts (org_id, policy, window_start, count, last_rule, last_detail) VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (org_id, policy, window_start) DO UPDATE SET count = guardrail_block_counts.count + EXCLUDED.count, last_rule = EXCLUDED.last_rule, last_detail = EXCLUDED.last_detail`,
			k.org, k.policy, win, g.count, g.rule, g.detail); err != nil {
			return err
		}
	}
	return nil
}

func (a *Alerter) sendBlockSummaries(ctx context.Context, now time.Time) {
	// A window is summarised once it has closed and every replica has had a
	// tick to flush into it.
	cutoff := now.UTC().Truncate(blockWindow).Add(-blockWindow)
	rows, err := a.St.Pool.Query(ctx, `SELECT org_id, policy, window_start, count, last_rule, last_detail FROM guardrail_block_counts
		WHERE window_start <= $1 AND window_start > $2 AND NOT EXISTS (SELECT 1 FROM alert_log WHERE key = 'guardrail:' || org_id || ':' || policy || ':' || to_char(window_start AT TIME ZONE 'UTC', 'YYYYMMDDHH24MI'))`,
		cutoff, now.Add(-24*time.Hour))
	if err != nil {
		return
	}
	type row struct {
		org, policy, rule, detail string
		win                       time.Time
		count                     int64
	}
	var list []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.org, &r.policy, &r.win, &r.count, &r.rule, &r.detail) == nil {
			list = append(list, r)
		}
	}
	rows.Close()
	for _, r := range list {
		key := "guardrail:" + r.org + ":" + r.policy + ":" + r.win.UTC().Format("200601021504")
		plural := map[bool]string{true: "request", false: "requests"}[r.count == 1]
		e := Event{Type: GuardrailBlocked, OrgID: r.org, TS: now,
			Title: fmt.Sprintf("Guardrail \"%s\" blocked %d %s", r.policy, r.count, plural),
			Text: fmt.Sprintf("Between %s and %s UTC, policy \"%s\" blocked %d %s (latest: %s — %s).",
				r.win.UTC().Format("15:04"), r.win.Add(blockWindow).UTC().Format("15:04"), r.policy, r.count, plural, r.rule, r.detail),
			Link:   a.link("/requests"),
			Fields: map[string]any{"policy": r.policy, "count": r.count, "rule": r.rule, "window_start": r.win.UTC()}}
		a.claimAndSend(ctx, key, e)
	}
}

func (a *Alerter) budgets(ctx context.Context, now time.Time) {
	if a.Loader == nil || a.Budgets == nil {
		return
	}
	snap := a.Loader.Snapshot()
	seen := map[string]bool{}
	for _, list := range snap.Budgets {
		for _, b := range list {
			// Per-person default budgets have no single figure to alert on.
			if seen[b.ID] || b.PerUser {
				continue
			}
			seen[b.ID] = true
			cost, tok := a.Budgets.Spent(b)
			pct := 0.0
			if b.LimitUSD != nil && *b.LimitUSD > 0 {
				pct = max(pct, 100*cost / *b.LimitUSD)
			}
			if b.LimitTokens != nil && *b.LimitTokens > 0 {
				pct = max(pct, 100*float64(tok)/float64(*b.LimitTokens))
			}
			period := gateway.PeriodStart(b.Period, now)
			name := a.scopeName(ctx, snap, b)
			used := describeUse(b, cost, tok)
			switch {
			case pct >= 100:
				text := fmt.Sprintf("The %s budget for %s is exhausted: %s.", b.Period, name, used)
				if b.Action == "block" {
					text += " New requests in this scope are being blocked until the period resets or the limit is raised."
				}
				a.claimAndSend(ctx, "budget:"+b.ID+":"+period+":exhausted", Event{Type: BudgetExhausted, OrgID: b.OrgID, TS: now,
					Title: fmt.Sprintf("Budget exhausted: %s (%s)", name, b.Period), Text: text, Link: a.link("/budgets"),
					Fields: map[string]any{"budget_id": b.ID, "scope_type": b.ScopeType, "scope_id": b.ScopeID, "period": b.Period, "percent": pct, "spent_usd": cost, "spent_tokens": tok}})
			case b.AlertPct > 0 && pct >= float64(b.AlertPct):
				a.claimAndSend(ctx, "budget:"+b.ID+":"+period+":threshold", Event{Type: BudgetThreshold, OrgID: b.OrgID, TS: now,
					Title:  fmt.Sprintf("Budget at %.0f%%: %s (%s)", pct, name, b.Period),
					Text:   fmt.Sprintf("The %s budget for %s has passed its %d%% alert threshold: %s.", b.Period, name, b.AlertPct, used),
					Link:   a.link("/budgets"),
					Fields: map[string]any{"budget_id": b.ID, "scope_type": b.ScopeType, "scope_id": b.ScopeID, "period": b.Period, "percent": pct, "alert_pct": b.AlertPct, "spent_usd": cost, "spent_tokens": tok}})
			}
		}
	}
}

func describeUse(b *store.Budget, cost float64, tok int64) string {
	var parts []string
	if b.LimitUSD != nil {
		parts = append(parts, fmt.Sprintf("$%.2f of $%.2f", cost, *b.LimitUSD))
	}
	if b.LimitTokens != nil {
		parts = append(parts, fmt.Sprintf("%d of %d tokens", tok, *b.LimitTokens))
	}
	return strings.Join(parts, ", ")
}

func (a *Alerter) scopeName(ctx context.Context, snap *gateway.Snapshot, b *store.Budget) string {
	switch b.ScopeType {
	case "org":
		if o := snap.Orgs[b.ScopeID]; o != nil {
			return "organization " + o.Name
		}
	case "team":
		if t := snap.Teams[b.ScopeID]; t != nil {
			return "team " + t.Name
		}
	case "project":
		if p := snap.Projects[b.ScopeID]; p != nil {
			return "project " + p.Name
		}
	case "user":
		var email string
		if a.St.Pool.QueryRow(ctx, `SELECT email FROM users WHERE id=$1`, b.ScopeID).Scan(&email) == nil {
			return email
		}
	case "key":
		var name string
		if a.St.Pool.QueryRow(ctx, `SELECT name FROM api_keys WHERE id=$1`, b.ScopeID).Scan(&name) == nil {
			return "key " + name
		}
	}
	return b.ScopeType + " " + b.ScopeID
}

func (a *Alerter) link(path string) string {
	if a.PublicURL == "" {
		return ""
	}
	return strings.TrimRight(a.PublicURL, "/") + path
}

// claimAndSend delivers e once across replicas.
func (a *Alerter) claimAndSend(ctx context.Context, key string, e Event) {
	tag, err := a.St.Pool.Exec(ctx, `INSERT INTO alert_log (key, org_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, key, e.OrgID)
	if err != nil || tag.RowsAffected() == 0 {
		return
	}
	a.Notify(ctx, e)
}

// Notify sends e to every enabled channel of its organization subscribed to its type.
func (a *Alerter) Notify(ctx context.Context, e Event) int {
	chans, err := store.List[store.NotificationChannel](ctx, a.St.Pool,
		`SELECT * FROM notification_channels WHERE org_id=$1 AND enabled AND (cardinality(events)=0 OR $2 = ANY(events))`, e.OrgID, e.Type)
	if err != nil {
		slog.Warn("alerts: listing channels failed", "err", err)
		return 0
	}
	sent := 0
	for i := range chans {
		if err := a.SendTo(ctx, &chans[i], e); err != nil {
			slog.Warn("alerts: delivery failed", "channel", chans[i].Name, "type", chans[i].Type, "event", e.Type, "err", err)
			continue
		}
		sent++
	}
	return sent
}

// SendTo decrypts ch and delivers e to it.
func (a *Alerter) SendTo(ctx context.Context, ch *store.NotificationChannel, e Event) error {
	t, err := a.Target(ch)
	if err != nil {
		return err
	}
	return a.Deliver.Send(ctx, t, e)
}

// Target decrypts a channel.
func (a *Alerter) Target(ch *store.NotificationChannel) (Target, error) {
	raw, err := a.Box.OpenString(ch.TargetEnc)
	if err != nil {
		return Target{}, fmt.Errorf("cannot decrypt channel target (master key changed?)")
	}
	t := Target{Type: ch.Type}
	if ch.Type == "email" {
		for _, s := range strings.Split(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				t.To = append(t.To, s)
			}
		}
	} else {
		t.URL = raw
	}
	if len(ch.SecretEnc) > 0 {
		t.Secret, _ = a.Box.OpenString(ch.SecretEnc)
	}
	return t, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
