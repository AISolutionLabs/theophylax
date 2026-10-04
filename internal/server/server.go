// Package server wires every component into a runnable gateway.
package server

import (
	"context"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"theophylax/internal/api"
	"theophylax/internal/audit"
	"theophylax/internal/auth"
	"theophylax/internal/config"
	"theophylax/internal/db"
	"theophylax/internal/export"
	"theophylax/internal/gateway"
	"theophylax/internal/guardrails"
	"theophylax/internal/metrics"
	"theophylax/internal/modelwatch"
	"theophylax/internal/providers"
	"theophylax/internal/receipts"
	"theophylax/internal/secrets"
	"theophylax/internal/store"
	"theophylax/web"
)

type Server struct {
	Cfg        *config.Config
	Pool       *pgxpool.Pool
	Gateway    *gateway.Gateway
	API        *api.API
	Handler    http.Handler
	stop       context.CancelFunc
	logs       *gateway.RequestLogger
	exportDone chan struct{}
}

// New connects, migrates, bootstraps and starts background workers.
func New(ctx context.Context, cfg *config.Config, version string) (*Server, error) {
	pool, err := db.Connect(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	st := store.New(pool)
	box, err := secrets.New(cfg.MasterKey)
	if err != nil {
		return nil, err
	}
	loader := gateway.NewLoader(st, box, cfg.UpstreamTimeout)
	if err := loader.Load(ctx); err != nil {
		return nil, err
	}
	redactKey, err := hkdf.Key(sha256.New, cfg.MasterKey, nil, "theophylax/redaction/v1", 32)
	if err != nil {
		return nil, err
	}
	guard := guardrails.New(redactKey, func(id string) guardrails.BedrockApplier {
		if e := loader.Snapshot().Providers[id]; e != nil {
			if b, ok := e.P.(*providers.Bedrock); ok {
				return b
			}
		}
		return nil
	})
	budgets := gateway.NewBudgets(st)
	logs := gateway.NewRequestLogger(st, budgets, cfg.LogBufferSize, cfg.StdoutRequestLogs)
	resolver := auth.NewResolver(st, box.SessionKey(), cfg.KeyCacheTTL)
	al := audit.New(st)

	var seed []byte
	if v := os.Getenv("THEOPHYLAX_RECEIPT_SEED"); v != "" {
		if seed, err = base64.StdEncoding.DecodeString(v); err != nil {
			return nil, fmt.Errorf("THEOPHYLAX_RECEIPT_SEED must be base64: %w", err)
		}
	}
	signer, err := receipts.NewSigner(cfg.MasterKey, seed)
	if err != nil {
		return nil, err
	}
	exp := export.New(export.ConfigFromEnv())
	if exp != nil {
		al.OnRecord = exp.Audit
	}
	gw := &gateway.Gateway{Cfg: cfg, St: st, Loader: loader, Guard: guard, RL: gateway.NewSharedRateLimiter(st, cfg.RateLimitSync), Budgets: budgets, Logs: logs, Resolver: resolver, Signer: signer, Audit: al, Export: exp}
	guard.Judge = gw.Judge
	api.Version = version
	ap := &api.API{Cfg: cfg, St: st, Box: box, Res: resolver, Audit: al, Loader: loader, Guard: guard, Budgets: budgets, Signer: signer, Export: exp, RateLimitsGlobal: gw.RL.Global()}
	watch := modelwatch.New(st, func() map[string]providers.Provider {
		out := map[string]providers.Provider{}
		for id, e := range loader.Snapshot().Providers {
			if e.P != nil && e.Row != nil && e.Row.Enabled {
				out[id] = e.P
			}
		}
		return out
	})
	watch.OnNew = func(ctx context.Context, found []modelwatch.Discovery) {
		ids := make([]string, len(found))
		for i, f := range found {
			ids[i] = f.UpstreamModel
		}
		al.Record(ctx, audit.Event{Actor: audit.Actor{Type: "system", ID: "model_watch"}, Action: "model_watch.new_models", TargetType: "model_watch", Details: map[string]any{"models": ids}})
	}
	ap.Watch = watch
	if err := ap.Bootstrap(ctx); err != nil {
		return nil, err
	}
	_ = loader.Load(ctx)

	bg, cancel := context.WithCancel(context.Background())
	go loader.Run(bg, pool)
	go budgets.Run(bg)
	go logs.Run(bg)
	go gw.RL.Run(bg)
	go watch.Run(bg)
	expDone := make(chan struct{})
	go func() { exp.Run(bg); close(expDone) }()
	go gateway.Retention(bg, st, cfg.RetentionDays)
	ckEvery := 2 * time.Minute
	if d, err := time.ParseDuration(os.Getenv("THEOPHYLAX_RECEIPT_CHECKPOINT_INTERVAL")); err == nil && d > 0 {
		ckEvery = d
	}
	go gateway.RunCheckpoints(bg, st, al, ckEvery)
	metrics.Default.GaugeFunc("theophylax_log_queue_depth", "Request logs waiting to be persisted", func() float64 { return float64(logs.QueueDepth()) })

	mux := http.NewServeMux()
	gw.Routes(mux)
	gw.ReceiptRoutes(mux)
	ap.Routes(mux)
	ap.SkillRoutes(mux)
	ap.ModelWatchRoutes(mux)
	mux.Handle("GET /metrics", metrics.Default.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		c, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(c); err != nil || loader.Snapshot() == nil {
			http.Error(w, "not ready", 503)
			return
		}
		_, _ = w.Write([]byte("ready"))
	})
	mux.Handle("/", web.Handler())
	return &Server{Cfg: cfg, Pool: pool, Gateway: gw, API: ap, Handler: securityHeaders(mux), stop: cancel, logs: logs, exportDone: expDone}, nil
}

// Close stops background workers, flushes logs and closes the pool.
func (s *Server) Close() {
	s.stop()
	s.logs.Wait()
	select { // let exporters flush, but never hang shutdown on a dead sink
	case <-s.exportDone:
	case <-time.After(15 * time.Second):
	}
	s.Pool.Close()
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			h.Set("X-Frame-Options", "DENY")
			h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		}
		next.ServeHTTP(w, r)
	})
}
