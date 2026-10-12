package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestResponseCache checks the cache end to end against an upstream that
// counts calls: off by default, hits served without calling the provider,
// isolation per project, per-request opt-out, streaming replay, $0 accounting,
// receipts marking hits, project override and purge.
func TestResponseCache(t *testing.T) {
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		n := calls.Load()
		if in["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, part := range []string{"cached ", "stream ", fmt.Sprint(n)} {
				fmt.Fprintf(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"model\":\"up-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", part)
			}
			fmt.Fprint(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"model\":\"up-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"c","object":"chat.completion","model":"up-model","choices":[{"index":0,"message":{"role":"assistant","content":"answer %d"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`, n)
	}))
	defer up.Close()

	ts, s := setup(t)
	ctx := context.Background()
	root := newClient(t, ts.URL)
	root.must("POST", "/api/auth/login", map[string]string{"email": "root@example.com", "password": "root-password-123"}, 200)
	org := root.list("/api/orgs")[0]["id"].(string)
	team := root.must("POST", "/api/orgs/"+org+"/teams", map[string]any{"name": "T"}, 201)["id"].(string)
	projA := root.must("POST", "/api/teams/"+team+"/projects", map[string]any{"name": "A"}, 201)["id"].(string)
	projB := root.must("POST", "/api/teams/"+team+"/projects", map[string]any{"name": "B"}, 201)["id"].(string)
	prov := root.must("POST", "/api/providers", map[string]any{"name": "up", "connection_string": "openai://up?api_key=x&base_url=" + up.URL + "/v1"}, 201)["id"].(string)
	m := root.must("POST", "/api/models", map[string]any{"name": "m"}, 201)["id"].(string)
	root.must("POST", "/api/models/"+m+"/deployments", map[string]any{"provider_id": prov, "upstream_model": "up-model"}, 201)
	keyA := root.must("POST", "/api/keys", map[string]any{"name": "a", "project_id": projA}, 201)["secret"].(string)
	keyB := root.must("POST", "/api/keys", map[string]any{"name": "b", "project_id": projB}, 201)["secret"].(string)
	waitReload(s)

	chat := map[string]any{"model": "m", "messages": []any{map[string]any{"role": "user", "content": "what is 2+2?"}}}
	call := func(key string, extra ...string) (string, string, string) {
		code, body, raw, hdr := gw(t, ts.URL, key, "/v1/chat/completions", chat, extra...)
		if code != 200 {
			t.Fatalf("call: %d %s", code, raw)
		}
		content := body["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
		return hdr.Get("X-Theophylax-Cache"), content, hdr.Get("X-Request-Id")
	}
	stored := func(n int) {
		for i := 0; i < 100; i++ {
			var c int
			_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM response_cache`).Scan(&c)
			if c >= n {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("cache never reached %d entries", n)
	}

	// Off by default.
	if h, _, _ := call(keyA); h != "" {
		t.Fatalf("cache header without caching enabled: %q", h)
	}
	call(keyA)
	if calls.Load() != 2 {
		t.Fatalf("uncached calls: %d", calls.Load())
	}
	if code, _, _, _ := root.do("PATCH", "/api/orgs/"+org, map[string]any{"settings": map[string]any{"cache_ttl_seconds": 999999999}}); code != 400 {
		t.Fatalf("invalid TTL accepted: %d", code)
	}
	root.must("PATCH", "/api/orgs/"+org, map[string]any{"settings": map[string]any{"cache_ttl_seconds": 600}}, 200)
	waitReload(s)

	h1, c1, _ := call(keyA)
	stored(1)
	h2, c2, hitID := call(keyA)
	if h1 != "miss" || h2 != "hit" || c1 != c2 || calls.Load() != 3 {
		t.Fatalf("hit: %s/%s %q/%q upstream calls=%d", h1, h2, c1, c2, calls.Load())
	}
	// Another project never sees project A's entries.
	if h, _, _ := call(keyB); h != "miss" || calls.Load() != 4 {
		t.Fatalf("cross-project: %s calls=%d", h, calls.Load())
	}
	// Per-request opt-out.
	if h, _, _ := call(keyA, "X-Theophylax-Cache", "off"); h != "" || calls.Load() != 5 {
		t.Fatalf("opt-out: %q calls=%d", h, calls.Load())
	}

	// Streaming responses are replayed.
	streamText := func() (string, string) {
		sb := map[string]any{"model": "m", "stream": true, "messages": []any{map[string]any{"role": "user", "content": "stream please"}}}
		b, _ := json.Marshal(sb)
		req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(string(b)))
		req.Header.Set("Authorization", "Bearer "+keyA)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var text strings.Builder
		for _, line := range strings.Split(string(raw), "\n") {
			if d, ok := strings.CutPrefix(line, "data: "); ok && d != "[DONE]" {
				var ev map[string]any
				if json.Unmarshal([]byte(d), &ev) == nil {
					if ch, _ := ev["choices"].([]any); len(ch) > 0 {
						if delta, _ := ch[0].(map[string]any)["delta"].(map[string]any); delta != nil {
							s, _ := delta["content"].(string)
							text.WriteString(s)
						}
					}
				}
			}
		}
		return resp.Header.Get("X-Theophylax-Cache"), text.String()
	}
	sh1, st1 := streamText()
	stored(3)
	sh2, st2 := streamText()
	if sh1 != "miss" || sh2 != "hit" || st1 != st2 || !strings.HasPrefix(st1, "cached stream ") || calls.Load() != 6 {
		t.Fatalf("stream replay: %s/%s %q/%q calls=%d", sh1, sh2, st1, st2, calls.Load())
	}

	// Hits cost nothing and the receipt says so.
	time.Sleep(1500 * time.Millisecond) // async log flush
	var priceKey string
	var cost float64
	_ = s.Pool.QueryRow(ctx, `SELECT price_key, cost_usd FROM request_logs WHERE id=$1`, hitID).Scan(&priceKey, &cost)
	if priceKey != "cache" || cost != 0 {
		t.Fatalf("hit accounting: price_key=%q cost=%v", priceKey, cost)
	}
	rreq, _ := http.NewRequest("GET", ts.URL+"/v1/receipts/"+hitID, nil)
	rreq.Header.Set("Authorization", "Bearer "+keyA)
	rresp, err := http.DefaultClient.Do(rreq)
	if err != nil {
		t.Fatal(err)
	}
	var rc map[string]any
	_ = json.NewDecoder(rresp.Body).Decode(&rc)
	rresp.Body.Close()
	jws, _ := rc["jws"].(string)
	parts := strings.Split(jws, ".")
	if len(parts) != 3 {
		t.Fatalf("receipt: %v", rc)
	}
	pl, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var receipt struct {
		Route struct {
			Cache string `json:"cache"`
		} `json:"route"`
	}
	_ = json.Unmarshal(pl, &receipt)
	if receipt.Route.Cache != "hit" {
		t.Fatalf("receipt route.cache = %q", receipt.Route.Cache)
	}

	// A project can turn caching off under an org default.
	root.must("PATCH", "/api/projects/"+projA, map[string]any{"settings": map[string]any{"cache_ttl_seconds": -1}}, 200)
	waitReload(s)
	before := calls.Load()
	if h, _, _ := call(keyA); h != "" || calls.Load() != before+1 {
		t.Fatalf("project override: %q calls=%d", h, calls.Load()-before)
	}

	// Purge.
	if r := root.must("DELETE", "/api/orgs/"+org+"/cache", nil, 200); r["deleted"].(float64) < 3 {
		t.Fatalf("purge: %v", r)
	}
}

// TestResponseCacheSecretShield: cached entries hold only placeholders (the
// upstream never saw the secret), restoration still happens per request on a
// hit, and hits consume no budget.
func TestResponseCacheSecretShield(t *testing.T) {
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
	pol := root.must("POST", "/api/orgs/"+org+"/guardrails", map[string]any{"name": "Secret Shield", "scope_type": "org", "scope_id": org}, 201)["id"].(string)
	root.must("PATCH", "/api/guardrails/"+pol, map[string]any{"rules": []any{map[string]any{"type": "secrets", "action": "redact", "restore": true}}}, 200)
	root.must("PATCH", "/api/orgs/"+org, map[string]any{"settings": map[string]any{"cache_ttl_seconds": 600}}, 200)
	waitReload(s)

	const secret = "AKIAIOSFODNN7EXAMPLE"
	ask := func() (string, string) {
		code, body, raw, hdr := gw(t, ts.URL, key, "/v1/messages", msg("m", "my key is "+secret))
		if code != 200 {
			t.Fatalf("call: %d %s", code, raw)
		}
		return hdr.Get("X-Theophylax-Cache"), body["content"].([]any)[0].(map[string]any)["text"].(string)
	}
	h1, t1 := ask()
	var enc []byte
	for i := 0; i < 100 && enc == nil; i++ {
		_ = s.Pool.QueryRow(ctx, `SELECT body_enc FROM response_cache LIMIT 1`).Scan(&enc)
		time.Sleep(20 * time.Millisecond)
	}
	if enc == nil {
		t.Fatal("nothing cached")
	}
	plain, err := s.API.Box.Open(enc)
	if err != nil || strings.Contains(string(plain), secret) || !strings.Contains(string(plain), "[REDACTED:aws_key:") {
		t.Fatalf("cached body must hold only the placeholder: %v %s", err, plain)
	}
	if strings.Contains(string(enc), secret) {
		t.Fatal("cache entry not encrypted")
	}
	// Wait until the first (live) request's spend has reached the budget:
	// request logs are written asynchronously, more slowly under -race.
	var before int64
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if _, before = s.API.Budgets.Current("project", proj, "monthly"); before > 0 {
			break
		}
	}
	if before == 0 {
		t.Fatal("the live request never reached the budget")
	}
	h2, t2 := ask()
	if h1 != "miss" || h2 != "hit" || !strings.Contains(t2, secret) || t1 != t2 {
		t.Fatalf("restore on hit: %s/%s %q", h1, h2, t2)
	}
	// Let the hit's log entry be persisted (which is when spend moves into
	// the shared counters) before comparing.
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		var n int
		_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM request_logs WHERE project_id=$1 AND price_key='cache'`, proj).Scan(&n)
		if n > 0 {
			break
		}
	}
	time.Sleep(100 * time.Millisecond)
	if _, after := s.API.Budgets.Current("project", proj, "monthly"); after != before {
		t.Fatalf("cache hit consumed budget: %d -> %d tokens", before, after)
	}
}
