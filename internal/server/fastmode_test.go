package server

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// Claude fast mode (speed: "fast") is billed at the catalog's fast multiplier,
// whether the response is streamed or not, and whichever API the client uses.
func TestFastModeCost(t *testing.T) {
	cat := t.TempDir() + "/catalog.json"
	_ = os.WriteFile(cat, []byte(`{"claude-opus-5-5":{"litellm_provider":"anthropic","input_cost_per_token":0.000004,"output_cost_per_token":0.00002,
		"provider_specific_entry":{"us":1.1,"fast":2},"supports_fast_mode":true}}`), 0o644)
	t.Setenv("THEOPHYLAX_TEST_PRICING_URL", "file://"+cat)
	ts, s := setup(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		speed := "standard"
		if req["speed"] == "fast" {
			speed = "fast"
		}
		if req["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, ev := range []struct {
				name string
				data map[string]any
			}{
				{"message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5-5", "content": []any{},
					"usage": map[string]any{"input_tokens": 1000, "output_tokens": 1, "speed": speed}}}},
				{"content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}}},
				{"content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "hi"}}},
				{"content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}},
				{"message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 500}}},
				{"message_stop", map[string]any{"type": "message_stop"}},
			} {
				d, _ := json.Marshal(ev.data)
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.name, d)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5-5",
			"content": []any{map[string]any{"type": "text", "text": "hi"}}, "stop_reason": "end_turn",
			"usage": map[string]any{"input_tokens": 1000, "output_tokens": 500, "speed": speed}})
	}))
	defer upstream.Close()

	root := newClient(t, ts.URL)
	root.must("POST", "/api/auth/login", map[string]string{"email": "root@example.com", "password": "root-password-123"}, 200)
	org := root.list("/api/orgs")[0]["id"].(string)
	proj := root.list("/api/orgs/" + org + "/projects")[0]["id"].(string)
	key := root.must("POST", "/api/keys", map[string]any{"name": "fast", "project_id": proj}, 201)["secret"].(string)
	prov := root.must("POST", "/api/providers", map[string]any{"name": "claude", "connection_string": "anthropic://x?api_key=k&base_url=" + upstream.URL}, 201)["id"].(string)
	m := root.must("POST", "/api/models", map[string]any{"name": "opus"}, 201)["id"].(string)
	root.must("POST", "/api/models/"+m+"/deployments", map[string]any{"provider_id": prov, "upstream_model": "claude-opus-5-5"}, 201)
	waitReload(s)

	standard := 1000*0.000004 + 500*0.00002
	costOf := func(reqID string) float64 {
		t.Helper()
		var cost float64
		for i := 0; i < 40; i++ {
			if s.Pool.QueryRow(t.Context(), `SELECT cost_usd FROM request_logs WHERE id=$1`, reqID).Scan(&cost) == nil {
				return cost
			}
			time.Sleep(100 * time.Millisecond) // async log flush
		}
		t.Fatalf("request %s was not logged", reqID)
		return 0
	}
	for _, tc := range []struct {
		name   string
		path   string
		body   map[string]any
		factor float64
	}{
		{"standard", "/v1/messages", msg("opus", "hi"), 1},
		{"fast", "/v1/messages", withFields(msg("opus", "hi"), "speed", "fast"), 2},
		{"fast stream", "/v1/messages", withFields(msg("opus", "hi"), "speed", "fast", "stream", true), 2},
	} {
		code, _, raw, hdr := gw(t, ts.URL, key, tc.path, tc.body)
		if code != 200 {
			t.Fatalf("%s: %d %s", tc.name, code, raw)
		}
		if got, want := costOf(hdr.Get("X-Request-Id")), standard*tc.factor; math.Abs(got-want) > 1e-9 {
			t.Errorf("%s: cost = %v, want %v", tc.name, got, want)
		}
	}
}

func withFields(m map[string]any, kv ...any) map[string]any {
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}
