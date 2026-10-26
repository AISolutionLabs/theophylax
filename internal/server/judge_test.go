package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// Claude 4.7 and later reject non-default sampling values, so a judge on a
// current model must not fail because the gateway asks for temperature 0.
func TestJudgeRetriesWithoutTemperature(t *testing.T) {
	var mu sync.Mutex
	var seen []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &b)
		mu.Lock()
		seen = append(seen, b)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, hasTemp := b["temperature"]
		switch {
		case b["model"] == "claude-new" && hasTemp:
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"temperature is deprecated for this model."}}`))
			return
		case b["model"] == "claude-broken":
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: too large"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"x","content":[{"type":"text","text":"SAFE"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	ts, s := setup(t)
	root := newClient(t, ts.URL)
	root.must("POST", "/api/auth/login", map[string]string{"email": "root@example.com", "password": "root-password-123"}, 200)
	prov := root.must("POST", "/api/providers", map[string]any{"name": "claude", "connection_string": "anthropic://x?api_key=k&base_url=" + upstream.URL}, 201)["id"].(string)
	for name, up := range map[string]string{"judge-new": "claude-new", "judge-old": "claude-old", "judge-broken": "claude-broken"} {
		m := root.must("POST", "/api/models", map[string]any{"name": name}, 201)["id"].(string)
		root.must("POST", "/api/models/"+m+"/deployments", map[string]any{"provider_id": prov, "upstream_model": up}, 201)
	}
	waitReload(s)
	ctx := context.Background()
	reset := func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		out := seen
		seen = nil
		return out
	}

	// A model that rejects temperature: one retry without it.
	out, err := s.Gateway.Judge(ctx, "judge-new", "classify", "hello")
	if err != nil || out != "SAFE" {
		t.Fatalf("judge on a defaults-only model: %q %v", out, err)
	}
	calls := reset()
	if len(calls) != 2 {
		t.Fatalf("want 2 upstream calls, got %d", len(calls))
	}
	if _, ok := calls[0]["temperature"]; !ok {
		t.Error("first call should ask for temperature 0")
	}
	if _, ok := calls[1]["temperature"]; ok {
		t.Error("retry should drop temperature")
	}

	// A model that accepts temperature keeps deterministic sampling, one call.
	if out, err := s.Gateway.Judge(ctx, "judge-old", "classify", "hello"); err != nil || out != "SAFE" {
		t.Fatalf("judge on an older model: %q %v", out, err)
	}
	if calls := reset(); len(calls) != 1 || calls[0]["temperature"] != float64(0) {
		t.Fatalf("older model should get one call at temperature 0: %v", calls)
	}

	// Other 400s are not retried.
	if _, err := s.Gateway.Judge(ctx, "judge-broken", "classify", "hello"); err == nil {
		t.Fatalf("unrelated 400 should surface: %v", err)
	}
	if calls := reset(); len(calls) != 1 {
		t.Fatalf("unrelated 400 should not be retried: %d calls", len(calls))
	}
}
