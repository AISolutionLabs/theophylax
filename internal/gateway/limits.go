package gateway

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"theophylax/internal/store"
)

// RateLimiter enforces requests-per-minute and tokens-per-minute using a
// sliding window approximation (current + weighted previous minute).
//
// With a store and a sync interval, limits are global across replicas: each
// replica counts locally, and every interval flushes its deltas to Postgres
// and reads back the fleet-wide totals for the keys it is tracking. The
// estimate a replica enforces is (fleet total at last sync) + (its own
// unflushed count), so overshoot is bounded by roughly
// replicas × request rate × interval. Without a store it is per replica.
type RateLimiter struct {
	mu       sync.Mutex
	m        map[string]*rlWindow
	st       *store.Store
	interval time.Duration
}

type rlCount struct {
	gR, gT int64 // fleet totals as of the last sync (includes our flushed counts)
	pR, pT int64 // counted here, not yet flushed
}

func (c rlCount) reqs() int64 { return c.gR + c.pR }
func (c rlCount) toks() int64 { return c.gT + c.pT }

type rlWindow struct {
	minute    int64
	cur, prev rlCount
}

// NewRateLimiter returns a per-replica limiter.
func NewRateLimiter() *RateLimiter { return NewSharedRateLimiter(nil, 0) }

// NewSharedRateLimiter returns a limiter that synchronises counts through
// Postgres every interval. A nil store or zero interval means per replica.
func NewSharedRateLimiter(st *store.Store, interval time.Duration) *RateLimiter {
	rl := &RateLimiter{m: map[string]*rlWindow{}, st: st, interval: interval}
	go func() {
		for range time.Tick(2 * time.Minute) {
			now := time.Now().Unix() / 60
			rl.mu.Lock()
			for k, w := range rl.m {
				if now-w.minute > 2 && w.cur.pR == 0 && w.cur.pT == 0 && w.prev.pR == 0 && w.prev.pT == 0 {
					delete(rl.m, k)
				}
			}
			rl.mu.Unlock()
		}
	}()
	return rl
}

// Global reports whether counts are shared across replicas.
func (rl *RateLimiter) Global() bool { return rl.st != nil && rl.interval > 0 }

// Run synchronises with the other replicas until ctx is cancelled.
func (rl *RateLimiter) Run(ctx context.Context) {
	if !rl.Global() {
		return
	}
	t := time.NewTicker(rl.interval)
	defer t.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := rl.sync(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("rate limit sync failed; enforcing local counts until it recovers", "err", err)
			}
			if n++; n%int(max(1, time.Minute/rl.interval)) == 0 {
				_, _ = rl.st.Pool.Exec(ctx, `DELETE FROM rate_windows WHERE minute < $1`, time.Now().Unix()/60-5)
			}
		}
	}
}

type rlDelta struct {
	key    string
	minute int64
	r, t   int64
}

func (rl *RateLimiter) sync(ctx context.Context) error {
	now := time.Now()
	rl.mu.Lock()
	var ds []rlDelta
	keys := make([]string, 0, len(rl.m))
	for k, w := range rl.m {
		rl.win(k, now)
		keys = append(keys, k)
		for _, x := range []struct {
			c *rlCount
			m int64
		}{{&w.cur, w.minute}, {&w.prev, w.minute - 1}} {
			if x.c.pR != 0 || x.c.pT != 0 {
				ds = append(ds, rlDelta{k, x.m, x.c.pR, x.c.pT})
				x.c.gR += x.c.pR
				x.c.gT += x.c.pT
				x.c.pR, x.c.pT = 0, 0
			}
		}
	}
	rl.mu.Unlock()
	if len(keys) == 0 {
		return nil
	}
	restore := func() {
		rl.mu.Lock()
		defer rl.mu.Unlock()
		for _, d := range ds {
			if w := rl.m[d.key]; w != nil {
				c := &w.cur
				if d.minute == w.minute-1 {
					c = &w.prev
				} else if d.minute != w.minute {
					continue
				}
				c.gR, c.gT, c.pR, c.pT = c.gR-d.r, c.gT-d.t, c.pR+d.r, c.pT+d.t
			}
		}
	}
	if len(ds) > 0 {
		ks, ms, rs, ts := make([]string, len(ds)), make([]int64, len(ds)), make([]int64, len(ds)), make([]int64, len(ds))
		for i, d := range ds {
			ks[i], ms[i], rs[i], ts[i] = d.key, d.minute, d.r, d.t
		}
		if _, err := rl.st.Pool.Exec(ctx, `INSERT INTO rate_windows (key, minute, reqs, toks)
			SELECT * FROM unnest($1::text[], $2::bigint[], $3::bigint[], $4::bigint[])
			ON CONFLICT (key, minute) DO UPDATE SET reqs = rate_windows.reqs + EXCLUDED.reqs, toks = rate_windows.toks + EXCLUDED.toks`, ks, ms, rs, ts); err != nil {
			restore()
			return err
		}
	}
	cur := now.Unix() / 60
	rows, err := rl.st.Pool.Query(ctx, `SELECT key, minute, reqs, toks FROM rate_windows WHERE key = ANY($1) AND minute >= $2`, keys, cur-1)
	if err != nil {
		return err
	}
	defer rows.Close()
	type tot struct{ r, t int64 }
	got := map[string]map[int64]tot{}
	for rows.Next() {
		var k string
		var m, r, t int64
		if err := rows.Scan(&k, &m, &r, &t); err != nil {
			return err
		}
		if got[k] == nil {
			got[k] = map[int64]tot{}
		}
		got[k][m] = tot{r, t}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	for _, k := range keys {
		w := rl.m[k]
		if w == nil {
			continue
		}
		rl.win(k, time.Now())
		if v, ok := got[k][w.minute]; ok {
			w.cur.gR, w.cur.gT = v.r, v.t
		}
		if v, ok := got[k][w.minute-1]; ok {
			w.prev.gR, w.prev.gT = v.r, v.t
		}
	}
	return nil
}

func (rl *RateLimiter) win(key string, now time.Time) *rlWindow {
	m := now.Unix() / 60
	w := rl.m[key]
	if w == nil {
		w = &rlWindow{minute: m}
		rl.m[key] = w
	}
	switch {
	case w.minute == m:
	case w.minute == m-1:
		// Unflushed counts from two minutes ago are dropped: they no longer
		// affect any window being enforced.
		w.prev, w.cur, w.minute = w.cur, rlCount{}, m
	default:
		*w = rlWindow{minute: m}
	}
	return w
}

// Allow checks and, if allowed, counts a request. Returns seconds to wait when limited.
func (rl *RateLimiter) Allow(key string, rpm, tpm int) (bool, int, string) {
	if rpm <= 0 && tpm <= 0 {
		return true, 0, ""
	}
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	w := rl.win(key, now)
	frac := float64(now.Unix()%60) / 60
	estR := float64(w.prev.reqs())*(1-frac) + float64(w.cur.reqs())
	estT := float64(w.prev.toks())*(1-frac) + float64(w.cur.toks())
	retry := int(60 - now.Unix()%60)
	if rpm > 0 && estR+1 > float64(rpm) {
		return false, retry, "requests per minute"
	}
	if tpm > 0 && estT >= float64(tpm) {
		return false, retry, "tokens per minute"
	}
	w.cur.pR++
	return true, 0, ""
}

func (rl *RateLimiter) AddTokens(key string, n int) {
	rl.mu.Lock()
	rl.win(key, time.Now()).cur.pT += int64(n)
	rl.mu.Unlock()
}

// ---------------------------------------------------------------------------

type ScopeRef struct{ Type, ID string }

type counterKey struct {
	ScopeType, ScopeID, Period string
	Start                      string
}

type spend struct {
	baseCost, pendCost float64
	baseTok, pendTok   int64
}

// Budgets tracks spend per scope/period. Values are DB truth (refreshed every
// few seconds, shared across replicas) plus this replica's unflushed deltas.
type Budgets struct {
	st *store.Store
	mu sync.Mutex
	m  map[counterKey]*spend
}

func NewBudgets(st *store.Store) *Budgets { return &Budgets{st: st, m: map[counterKey]*spend{}} }

var Periods = []string{"daily", "monthly", "total"}

func PeriodStart(p string, t time.Time) string {
	t = t.UTC()
	switch p {
	case "daily":
		return t.Format("2006-01-02")
	case "monthly":
		return t.Format("2006-01") + "-01"
	}
	return "1970-01-01"
}

func (b *Budgets) Current(scopeType, scopeID, period string) (float64, int64) {
	k := counterKey{scopeType, scopeID, period, PeriodStart(period, time.Now())}
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.m[k]
	if s == nil {
		return 0, 0
	}
	return s.baseCost + s.pendCost, s.baseTok + s.pendTok
}

// PersonScopes are the counters a person's spend is recorded under: their
// spend in the org and their spend in the team the request is attributed to.
func PersonScopes(orgID, teamID, userID string) []ScopeRef {
	if userID == "" {
		return nil
	}
	out := []ScopeRef{{"org_user", orgID + "/" + userID}}
	if teamID != "" {
		out = append(out, ScopeRef{"team_user", teamID + "/" + userID})
	}
	return out
}

// PersonalBudget returns the budget that limits one person for a period, and
// the counter it is measured against. Precedence: the person's own budget,
// then their team's per-person default, then the org's per-person default.
func PersonalBudget(snap *Snapshot, orgID, teamID, userID, period string) (*store.Budget, ScopeRef) {
	if userID == "" {
		return nil, ScopeRef{}
	}
	pick := func(key string, perUser bool) *store.Budget {
		for _, b := range snap.Budgets[key] {
			if b.OrgID == orgID && b.Period == period && b.PerUser == perUser {
				return b
			}
		}
		return nil
	}
	if b := pick("user:"+userID, false); b != nil {
		return b, ScopeRef{"org_user", orgID + "/" + userID}
	}
	if teamID != "" {
		if b := pick("team:"+teamID, true); b != nil {
			return b, ScopeRef{"team_user", teamID + "/" + userID}
		}
	}
	if b := pick("org:"+orgID, true); b != nil {
		return b, ScopeRef{"org_user", orgID + "/" + userID}
	}
	return nil, ScopeRef{}
}

// Spent is a budget's usage in its current period. Per-person defaults have no
// single figure; use PersonalBudget for a given person.
func (b *Budgets) Spent(bud *store.Budget) (float64, int64) {
	if bud.ScopeType == "user" {
		return b.Current("org_user", bud.OrgID+"/"+bud.ScopeID, bud.Period)
	}
	return b.Current(bud.ScopeType, bud.ScopeID, bud.Period)
}

func overLimit(bud *store.Budget, cost float64, tok int64) bool {
	return (bud.LimitUSD != nil && cost >= *bud.LimitUSD) || (bud.LimitTokens != nil && tok >= *bud.LimitTokens)
}

// Exceeded returns the first blocking budget that is exhausted: the person's
// effective per-person budget for each period, then every pooled budget on
// the request's scopes.
func (b *Budgets) Exceeded(snap *Snapshot, orgID, teamID, userID string, scopes []ScopeRef) *store.Budget {
	for _, p := range Periods {
		if bud, ctr := PersonalBudget(snap, orgID, teamID, userID, p); bud != nil && bud.Action == "block" {
			if cost, tok := b.Current(ctr.Type, ctr.ID, p); overLimit(bud, cost, tok) {
				return bud
			}
		}
	}
	for _, sc := range scopes {
		if sc.Type == "user" {
			continue // personal budgets are resolved above
		}
		for _, bud := range snap.Budgets[sc.Type+":"+sc.ID] {
			if bud.Action != "block" || bud.PerUser {
				continue
			}
			if cost, tok := b.Current(sc.Type, sc.ID, bud.Period); overLimit(bud, cost, tok) {
				return bud
			}
		}
	}
	return nil
}

// Describe names a budget for error messages.
func Describe(b *store.Budget) string {
	switch {
	case b.ScopeType == "user":
		return b.Period + " personal budget"
	case b.PerUser:
		return b.Period + " per-person " + b.ScopeType + " budget"
	}
	return b.Period + " " + b.ScopeType + " budget"
}

func (b *Budgets) AddPending(scopes []ScopeRef, cost float64, tokens int64, at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, sc := range scopes {
		for _, p := range Periods {
			k := counterKey{sc.Type, sc.ID, p, PeriodStart(p, at)}
			s := b.m[k]
			if s == nil {
				s = &spend{}
				b.m[k] = s
			}
			s.pendCost += cost
			s.pendTok += tokens
		}
	}
}

// Flushed moves amounts from pending to base after the logger commits them.
func (b *Budgets) Flushed(agg map[counterKey]*spend) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, d := range agg {
		s := b.m[k]
		if s == nil {
			s = &spend{}
			b.m[k] = s
		}
		s.pendCost -= d.pendCost
		s.pendTok -= d.pendTok
		s.baseCost += d.pendCost
		s.baseTok += d.pendTok
		if s.pendCost < 1e-12 {
			s.pendCost = 0
		}
		if s.pendTok < 0 {
			s.pendTok = 0
		}
	}
}

// Refresh reloads current-period totals from the database (other replicas' spend).
func (b *Budgets) Refresh(ctx context.Context) error {
	now := time.Now()
	rows, err := b.st.Pool.Query(ctx, `SELECT scope_type, scope_id, period, period_start::text, cost_usd, tokens FROM usage_counters
		WHERE (period='daily' AND period_start=$1::date) OR (period='monthly' AND period_start=$2::date) OR period='total'`,
		PeriodStart("daily", now), PeriodStart("monthly", now))
	if err != nil {
		return err
	}
	defer rows.Close()
	fresh := map[counterKey][2]float64{}
	for rows.Next() {
		var k counterKey
		var c float64
		var t int64
		if err := rows.Scan(&k.ScopeType, &k.ScopeID, &k.Period, &k.Start, &c, &t); err != nil {
			return err
		}
		fresh[k] = [2]float64{c, float64(t)}
	}
	if rows.Err() != nil {
		return rows.Err()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, s := range b.m {
		if v, ok := fresh[k]; ok {
			s.baseCost, s.baseTok = v[0], int64(v[1])
			delete(fresh, k)
		} else if k.Start != PeriodStart(k.Period, now) {
			delete(b.m, k) // rolled over
		}
	}
	for k, v := range fresh {
		b.m[k] = &spend{baseCost: v[0], baseTok: int64(v[1])}
	}
	return nil
}

func (b *Budgets) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		if err := b.Refresh(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("budget refresh failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
