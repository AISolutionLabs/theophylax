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
// Limits are per replica; divide by replica count or front with a shared
// limiter if exact global limits are required.
type RateLimiter struct {
	mu sync.Mutex
	m  map[string]*rlWindow
}

type rlWindow struct {
	minute           int64
	reqs, toks       int64
	prevReqs, prevTk int64
}

func NewRateLimiter() *RateLimiter {
	rl := &RateLimiter{m: map[string]*rlWindow{}}
	go func() {
		for range time.Tick(2 * time.Minute) {
			now := time.Now().Unix() / 60
			rl.mu.Lock()
			for k, w := range rl.m {
				if now-w.minute > 2 {
					delete(rl.m, k)
				}
			}
			rl.mu.Unlock()
		}
	}()
	return rl
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
		w.prevReqs, w.prevTk, w.reqs, w.toks, w.minute = w.reqs, w.toks, 0, 0, m
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
	estR := float64(w.prevReqs)*(1-frac) + float64(w.reqs)
	estT := float64(w.prevTk)*(1-frac) + float64(w.toks)
	retry := int(60 - now.Unix()%60)
	if rpm > 0 && estR+1 > float64(rpm) {
		return false, retry, "requests per minute"
	}
	if tpm > 0 && estT >= float64(tpm) {
		return false, retry, "tokens per minute"
	}
	w.reqs++
	return true, 0, ""
}

func (rl *RateLimiter) AddTokens(key string, n int) {
	rl.mu.Lock()
	rl.win(key, time.Now()).toks += int64(n)
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

// Exceeded returns the first blocking budget that is exhausted.
func (b *Budgets) Exceeded(snap *Snapshot, scopes []ScopeRef) *store.Budget {
	for _, sc := range scopes {
		for _, bud := range snap.Budgets[sc.Type+":"+sc.ID] {
			if bud.Action != "block" {
				continue
			}
			cost, tok := b.Current(sc.Type, sc.ID, bud.Period)
			if (bud.LimitUSD != nil && cost >= *bud.LimitUSD) || (bud.LimitTokens != nil && tok >= *bud.LimitTokens) {
				return bud
			}
		}
	}
	return nil
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
