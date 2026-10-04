package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"theophylax/internal/migrate"
	"theophylax/internal/secrets"
	"theophylax/internal/store"
)

// TestLiteLLMImport imports a LiteLLM snapshot and calls the gateway with the
// original LiteLLM key, which must keep working unchanged.
func TestLiteLLMImport(t *testing.T) {
	ts, s := setup(t)
	ctx := context.Background()
	plan, err := migrate.ParseConfig("model_list:\n  - model_name: echo\n    litellm_params:\n      model: openai/echo\n      api_base: " + "http://127.0.0.1:1/v1\n")
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte("sk-litellm-original-key"))
	data := &migrate.LiteLLMData{
		Teams: []map[string]any{{"team_id": "t1", "team_alias": "Payments", "models": []any{"echo"}, "rpm_limit": 30.0, "max_budget": 100.0, "budget_duration": "30d"}},
		Users: []map[string]any{{"user_id": "u1", "user_email": "Dev@Example.com"}},
		Keys: []map[string]any{
			{"token": hex.EncodeToString(h[:]), "key_alias": "ci", "team_id": "t1", "user_id": "u1", "models": []any{"all-proxy-models"}, "max_budget": 5.0, "budget_duration": "1d"},
			{"token": hex.EncodeToString(make([]byte, 32)), "key_alias": "blocked", "team_id": "t1", "blocked": true},
		},
		Budgets: map[string]map[string]any{},
	}
	box, _ := secrets.New(s.Cfg.MasterKey)
	rep, err := migrate.Apply(ctx, store.New(s.Pool), box, plan, data, migrate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Created["keys"] != 2 || rep.Created["teams"] != 1 || rep.Created["budgets"] != 2 || rep.Created["users"] != 1 || rep.Created["providers"] != 1 {
		t.Fatalf("report: %+v", rep)
	}
	again, err := migrate.Apply(ctx, store.New(s.Pool), box, plan, data, migrate.Options{})
	if err != nil || len(again.Created) > 1 || again.Existing["keys"] != 2 {
		t.Fatalf("re-run should be a no-op: %+v %v", again, err)
	}
	// Point the imported model at the built-in mock so the call succeeds.
	root := newClient(t, ts.URL)
	root.must("POST", "/api/auth/login", map[string]string{"email": "root@example.com", "password": "root-password-123"}, 200)
	mp := root.must("POST", "/api/providers", map[string]any{"name": "mock", "connection_string": "mock://x"}, 201)["id"].(string)
	var mid string
	_ = s.Pool.QueryRow(ctx, `SELECT id FROM models WHERE name='echo'`).Scan(&mid)
	_, _ = s.Pool.Exec(ctx, `UPDATE deployments SET provider_id=$1 WHERE model_id=$2`, mp, mid)
	waitReload(s)
	code, _, raw, _ := gw(t, ts.URL, "sk-litellm-original-key", "/v1/messages", msg("echo", "hello from litellm"))
	if code != 200 || !strings.Contains(string(raw), "hello from litellm") {
		t.Fatalf("imported key: %d %s", code, raw)
	}
	var rpm int
	_ = s.Pool.QueryRow(ctx, `SELECT (settings->>'rpm')::int FROM teams WHERE external_id='litellm:t1'`).Scan(&rpm)
	if rpm != 30 {
		t.Fatalf("team rpm %d", rpm)
	}
}
