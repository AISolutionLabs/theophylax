package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"theophylax/internal/metrics"
	"theophylax/internal/store"
)

// LogEntry is a completed request queued for persistence.
type LogEntry struct {
	Log      store.RequestLog
	Scopes   []ScopeRef
	Request  []byte // optional payload capture
	Response []byte
	// Proof-of-Inference receipt
	ReceiptJWS  string
	ReceiptHash []byte
	ReceiptKID  string
}

// RequestLogger persists request logs and usage counters asynchronously in
// batches (COPY), so logging never adds latency to the hot path.
type RequestLogger struct {
	st      *store.Store
	budgets *Budgets
	ch      chan *LogEntry
	stdout  bool
	enc     *json.Encoder
	encMu   sync.Mutex
	done    chan struct{}
}

func NewRequestLogger(st *store.Store, b *Budgets, buf int, stdout bool) *RequestLogger {
	return &RequestLogger{st: st, budgets: b, ch: make(chan *LogEntry, buf), stdout: stdout, enc: json.NewEncoder(os.Stdout), done: make(chan struct{})}
}

func (l *RequestLogger) Enqueue(e *LogEntry) {
	if l.stdout {
		l.encMu.Lock()
		_ = l.enc.Encode(map[string]any{"level": "INFO", "msg": "request", "time": e.Log.TS, "request": e.Log})
		l.encMu.Unlock()
	}
	select {
	case l.ch <- e:
	default:
		metrics.LogDropped.Inc()
	}
}

func (l *RequestLogger) QueueDepth() int { return len(l.ch) }

func (l *RequestLogger) Run(ctx context.Context) {
	defer close(l.done)
	batch := make([]*LogEntry, 0, 512)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	flush := func() {
		if len(batch) == 0 {
			return
		}
		fctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := l.flush(fctx, batch); err != nil {
			slog.Error("request log flush failed", "rows", len(batch), "err", err)
		}
		cancel()
		batch = batch[:0]
	}
	for {
		select {
		case e := <-l.ch:
			batch = append(batch, e)
			if len(batch) >= 500 {
				flush()
			}
		case <-t.C:
			flush()
		case <-ctx.Done():
			// Drain what is queued before exit.
			for {
				select {
				case e := <-l.ch:
					batch = append(batch, e)
				default:
					flush()
					return
				}
			}
		}
	}
}

// Wait blocks until Run has drained on shutdown.
func (l *RequestLogger) Wait() { <-l.done }

var logCols = []string{"id", "ts", "org_id", "team_id", "project_id", "user_id", "key_id", "agent_id", "endpoint", "model", "upstream_model", "provider_id", "provider_kind", "status", "error", "stream", "latency_ms", "ttfb_ms", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "cost_usd", "price_key", "attempts", "guardrails", "client_ip", "user_agent"}

func (l *RequestLogger) flush(ctx context.Context, batch []*LogEntry) error {
	agg := map[counterKey]*spend{}
	reqs := map[counterKey]int64{}
	for _, e := range batch {
		tok := int64(e.Log.InputTokens + e.Log.OutputTokens + e.Log.CacheReadTokens + e.Log.CacheWriteTokens)
		for _, sc := range e.Scopes {
			for _, p := range Periods {
				k := counterKey{sc.Type, sc.ID, p, PeriodStart(p, e.Log.TS)}
				s := agg[k]
				if s == nil {
					s = &spend{}
					agg[k] = s
				}
				s.pendCost += e.Log.CostUSD
				s.pendTok += tok
				reqs[k]++
			}
		}
	}
	err := l.st.Tx(ctx, func(tx pgx.Tx) error {
		rows := make([][]any, len(batch))
		for i, e := range batch {
			g := e.Log
			gr := g.Guardrails
			if len(gr) == 0 {
				gr = json.RawMessage("[]")
			}
			rows[i] = []any{g.ID, g.TS, g.OrgID, g.TeamID, g.ProjectID, g.UserID, g.KeyID, g.AgentID, g.Endpoint, g.Model, g.UpstreamModel, g.ProviderID, g.ProviderKind, g.Status, g.Error, g.Stream, g.LatencyMS, g.TTFBMS, g.InputTokens, g.OutputTokens, g.CacheReadTokens, g.CacheWriteTokens, g.CostUSD, g.PriceKey, g.Attempts, string(gr), g.ClientIP, g.UserAgent}
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"request_logs"}, logCols, pgx.CopyFromRows(rows)); err != nil {
			return err
		}
		b := &pgx.Batch{}
		for k, s := range agg {
			b.Queue(`INSERT INTO usage_counters (scope_type, scope_id, period, period_start, cost_usd, tokens, requests) VALUES ($1,$2,$3,$4::date,$5,$6,$7)
				ON CONFLICT (scope_type, scope_id, period, period_start) DO UPDATE SET cost_usd=usage_counters.cost_usd+EXCLUDED.cost_usd, tokens=usage_counters.tokens+EXCLUDED.tokens, requests=usage_counters.requests+EXCLUDED.requests`,
				k.ScopeType, k.ScopeID, k.Period, k.Start, s.pendCost, s.pendTok, reqs[k])
		}
		for _, e := range batch {
			if e.ReceiptJWS != "" {
				b.Queue(`INSERT INTO receipts (request_id, ts, org_id, key_id, kid, jws, hash) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`,
					e.Log.ID, e.Log.TS, e.Log.OrgID, e.Log.KeyID, e.ReceiptKID, e.ReceiptJWS, e.ReceiptHash)
			}
			if e.Request != nil || e.Response != nil {
				b.Queue(`INSERT INTO request_payloads (request_id, ts, org_id, request, response) VALUES ($1,$2,$3,$4::jsonb,$5::jsonb) ON CONFLICT DO NOTHING`,
					e.Log.ID, e.Log.TS, e.Log.OrgID, jsonOrNull(e.Request), jsonOrNull(e.Response))
			}
		}
		if b.Len() > 0 {
			return tx.SendBatch(ctx, b).Close()
		}
		return nil
	})
	if err == nil {
		l.budgets.Flushed(agg)
	}
	return err
}

func jsonOrNull(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	if json.Valid(b) {
		return string(b)
	}
	s, _ := json.Marshal(string(b))
	return string(s)
}

// Retention deletes logs and payloads older than the configured horizon.
func Retention(ctx context.Context, st *store.Store, days int) {
	if days <= 0 {
		return
	}
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		cut := time.Now().AddDate(0, 0, -days)
		if _, err := st.Pool.Exec(ctx, `DELETE FROM request_logs WHERE ts < $1`, cut); err != nil && ctx.Err() == nil {
			slog.Warn("retention", "err", err)
		}
		_, _ = st.Pool.Exec(ctx, `DELETE FROM request_payloads WHERE ts < $1`, cut)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
