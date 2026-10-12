// Theophylax — enterprise AI gateway.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"theophylax/internal/config"
	"theophylax/internal/server"
)

var version = "dev"

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "healthcheck":
			resp, err := http.Get("http://127.0.0.1" + addrPort(os.Getenv("THEOPHYLAX_ADDR")) + "/healthz")
			if err != nil || resp.StatusCode != 200 {
				os.Exit(1)
			}
			os.Exit(0)
		case "verify-receipt":
			os.Exit(verifyReceiptCLI(os.Args[2:]))
		case "import-litellm":
			os.Exit(importLiteLLMCLI(os.Args[2:]))
		case "config":
			os.Exit(configCLI(os.Args[2:]))
		case "reset-password":
			os.Exit(resetPasswordCLI(os.Args[2:]))
		case "version":
			os.Stdout.WriteString(version + "\n")
			os.Exit(0)
		}
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func addrPort(a string) string {
	if a == "" {
		return ":8080"
	}
	if i := strings.LastIndex(a, ":"); i >= 0 {
		return a[i:]
	}
	return a
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s, err := server.New(ctx, cfg, version)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           s.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: streaming responses can legitimately run for many minutes.
	}
	errc := make(chan error, 1)
	go func() {
		slog.Info("theophylax listening", "addr", cfg.Addr, "version", version, "public_url", cfg.PublicURL)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			s.Close()
			return err
		}
	case <-ctx.Done():
	}
	slog.Info("shutting down: draining in-flight requests")
	sctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	s.Close()
	return nil
}
