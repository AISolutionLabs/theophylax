package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type hookRecorder struct {
	mu     sync.Mutex
	events []map[string]any
	sigOK  []bool
	srv    *httptest.Server
}

func newHookRecorder(t *testing.T, secret string) *hookRecorder {
	h := &hookRecorder{}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var e map[string]any
		_ = json.Unmarshal(b, &e)
		m := hmac.New(sha256.New, []byte(secret))
		m.Write([]byte(r.Header.Get("X-Theophylax-Timestamp") + "."))
		m.Write(b)
		h.mu.Lock()
		h.events = append(h.events, e)
		h.sigOK = append(h.sigOK, r.Header.Get("X-Theophylax-Signature") == "sha256="+hex.EncodeToString(m.Sum(nil)))
		h.mu.Unlock()
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *hookRecorder) types() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, e := range h.events {
		out = append(out, e["event"].(string))
	}
	return out
}

func TestAlerts(t *testing.T) {
	ts, s := setup(t)
	ctx := context.Background()
	root := newClient(t, ts.URL)
	root.must("POST", "/api/auth/login", map[string]string{"email": "root@example.com", "password": "root-password-123"}, 200)
	org := root.list("/api/orgs")[0]["id"].(string)
	team := root.must("POST", "/api/orgs/"+org+"/teams", map[string]any{"name": "T"}, 201)["id"].(string)
	proj := root.must("POST", "/api/teams/"+team+"/projects", map[string]any{"name": "P"}, 201)["id"].(string)
	prov := root.must("POST", "/api/providers", map[string]any{"name": "mock", "connection_string": "mock://x"}, 201)["id"].(string)
	m := root.must("POST", "/api/models", map[string]any{"name": "m"}, 201)["id"].(string)
	root.must("POST", "/api/models/"+m+"/deployments", map[string]any{"provider_id": prov, "upstream_model": "x"}, 201)
	key := root.must("POST", "/api/keys", map[string]any{"name": "k", "project_id": proj}, 201)["secret"].(string)

	all := newHookRecorder(t, "hook-secret")
	onlyBlocks := newHookRecorder(t, "")
	ch := root.must("POST", "/api/orgs/"+org+"/notifications", map[string]any{"name": "ops", "type": "webhook", "target": all.srv.URL + "/hook?token=abc", "secret": "hook-secret"}, 201)
	if tgt := ch["target"].(string); strings.Contains(tgt, "token=abc") || !strings.HasSuffix(tgt, "/…") {
		t.Fatalf("target not masked: %q", tgt)
	}
	root.must("POST", "/api/orgs/"+org+"/notifications", map[string]any{"name": "security", "type": "webhook", "target": onlyBlocks.srv.URL, "events": []string{"guardrail.blocked"}}, 201)
	for _, bad := range []map[string]any{
		{"name": "x", "type": "pager", "target": "https://x.example"},
		{"name": "x", "type": "slack", "target": "not a url"},
		{"name": "x", "type": "webhook", "target": "https://x.example", "events": []string{"nope"}},
		{"name": "x", "type": "email", "target": "a@example.com"}, // SMTP not configured
	} {
		if code, _, _, _ := root.do("POST", "/api/orgs/"+org+"/notifications", bad); code != 400 {
			t.Fatalf("invalid channel %v accepted: %d", bad, code)
		}
	}

	// Test delivery.
	if r := root.must("POST", "/api/notifications/"+ch["id"].(string)+"/test", map[string]any{}, 200); r["ok"] != true {
		t.Fatalf("test delivery: %v", r)
	}

	// Budget alerts: 50% threshold, then exhausted. Each is sent once.
	root.must("POST", "/api/orgs/"+org+"/budgets", map[string]any{"scope_type": "project", "scope_id": proj, "period": "monthly", "limit_tokens": 8, "alert_pct": 50, "action": "alert"}, 201)
	waitReload(s)
	call := func(text string) int {
		code, _, _, _ := gw(t, ts.URL, key, "/v1/messages", msg("m", text))
		return code
	}
	// Spend is recorded when the request finishes, which can be just after the
	// client has read the response; wait for it before evaluating.
	waitSpend := func(atLeast int64) {
		for i := 0; i < 100; i++ {
			if _, tok := s.API.Budgets.Current("project", proj, "monthly"); tok >= atLeast {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("spend never reached %d tokens", atLeast)
	}
	if call("hello") != 200 {
		t.Fatal("call 1")
	}
	waitSpend(5)
	s.API.Alerts.Tick(ctx, time.Now())
	s.API.Alerts.Tick(ctx, time.Now())
	if got := all.types(); len(got) != 2 || got[1] != "budget.threshold" {
		t.Fatalf("after threshold: %v", got)
	}
	if call("hello again") != 200 {
		t.Fatal("call 2")
	}
	waitSpend(10)
	s.API.Alerts.Tick(ctx, time.Now())
	if got := all.types(); len(got) != 3 || got[2] != "budget.exhausted" {
		t.Fatalf("after exhaustion: %v", got)
	}

	// Guardrail blocks are summarised per 5-minute window.
	pol := root.must("POST", "/api/orgs/"+org+"/guardrails", map[string]any{"name": "Codenames", "scope_type": "org", "scope_id": org}, 201)["id"].(string)
	root.must("PATCH", "/api/guardrails/"+pol, map[string]any{"rules": []any{map[string]any{"type": "keywords", "words": []string{"bluebird"}, "action": "block"}}}, 200)
	waitReload(s)
	for i := 0; i < 3; i++ {
		if code := call("tell me about bluebird"); code != 400 {
			t.Fatalf("blocked call: %d", code)
		}
	}
	now := time.Now()
	s.API.Alerts.Tick(ctx, now)                     // record the window
	s.API.Alerts.Tick(ctx, now.Add(10*time.Minute)) // window closed: summarise
	s.API.Alerts.Tick(ctx, now.Add(11*time.Minute)) // and only once
	got := all.types()
	if len(got) != 4 || got[3] != "guardrail.blocked" {
		t.Fatalf("after blocks: %v", got)
	}
	all.mu.Lock()
	summary := all.events[3]
	sigs := append([]bool{}, all.sigOK...)
	all.mu.Unlock()
	if f := summary["fields"].(map[string]any); f["count"].(float64) != 3 || f["policy"] != "Codenames" {
		t.Fatalf("block summary: %v", summary)
	}
	for i, ok := range sigs {
		if !ok {
			t.Fatalf("event %d has a bad signature", i)
		}
	}
	// The security channel only subscribed to guardrail blocks.
	if got := onlyBlocks.types(); len(got) != 1 || got[0] != "guardrail.blocked" {
		t.Fatalf("event filter: %v", got)
	}
}
