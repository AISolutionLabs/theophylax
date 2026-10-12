package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"theophylax/internal/confsync"
)

func TestComplexityRouting(t *testing.T) {
	ts, s := setup(t)
	root := newClient(t, ts.URL)
	root.must("POST", "/api/auth/login", map[string]string{"email": "root@example.com", "password": "root-password-123"}, 200)
	org := root.list("/api/orgs")[0]["id"].(string)
	team := root.must("POST", "/api/orgs/"+org+"/teams", map[string]any{"name": "T"}, 201)["id"].(string)
	proj := root.must("POST", "/api/teams/"+team+"/projects", map[string]any{"name": "P"}, 201)["id"].(string)
	small := root.must("POST", "/api/providers", map[string]any{"name": "small-prov", "connection_string": "mock://s"}, 201)["id"].(string)
	large := root.must("POST", "/api/providers", map[string]any{"name": "large-prov", "connection_string": "mock://l"}, 201)["id"].(string)
	ms := root.must("POST", "/api/models", map[string]any{"name": "fast"}, 201)["id"].(string)
	root.must("POST", "/api/models/"+ms+"/deployments", map[string]any{"provider_id": small, "upstream_model": "fast-1"}, 201)
	ml := root.must("POST", "/api/models", map[string]any{"name": "strong"}, 201)["id"].(string)
	root.must("POST", "/api/models/"+ml+"/deployments", map[string]any{"provider_id": large, "upstream_model": "strong-1"}, 201)

	tiers := map[string]any{"tiers": []any{map[string]any{"max_score": 0.3, "model": "fast"}, map[string]any{"model": "strong"}}}
	// Validation.
	root.must("POST", "/api/models", map[string]any{"name": "bad1", "strategy": "complexity"}, 400)
	root.must("POST", "/api/models", map[string]any{"name": "bad2", "strategy": "complexity",
		"routing": map[string]any{"tiers": []any{map[string]any{"max_score": 0.3, "model": "fast"}, map[string]any{"model": "nope"}}}}, 400)
	root.must("POST", "/api/models", map[string]any{"name": "bad3", "strategy": "priority", "routing": tiers}, 400)
	root.must("POST", "/api/models", map[string]any{"name": "bad4", "strategy": "fastest"}, 400)
	auto := root.must("POST", "/api/models", map[string]any{"name": "auto", "strategy": "complexity", "routing": tiers}, 201)["id"].(string)
	root.must("POST", "/api/models", map[string]any{"name": "meta", "strategy": "complexity",
		"routing": map[string]any{"tiers": []any{map[string]any{"max_score": 0.3, "model": "auto"}, map[string]any{"model": "strong"}}}}, 400)
	// A model a router depends on cannot be deleted.
	root.must("DELETE", "/api/models/"+ms, nil, 400)

	// A key allowed only the router may use the models it routes to.
	key := root.must("POST", "/api/keys", map[string]any{"name": "k", "project_id": proj, "allowed_models": []string{"auto"}}, 201)["secret"].(string)
	waitReload(s)

	ask := func(text string, extra map[string]any) (int, http.Header, map[string]any) {
		b := msg("auto", text)
		for k, v := range extra {
			b[k] = v
		}
		code, body, raw, hdr := gw(t, ts.URL, key, "/v1/messages", b)
		if code != 200 {
			t.Fatalf("%d %s", code, raw)
		}
		return code, hdr, body
	}
	if _, _, raw, _ := (&client{t: t, base: ts.URL, hc: http.DefaultClient, auth: key}).do("GET", "/v1/models", nil); !strings.Contains(string(raw), `"auto"`) {
		t.Fatalf("/v1/models should list the router: %s", raw)
	}
	_, h, _ := ask("hi, what is the capital of France?", nil)
	if h.Get("X-Theophylax-Provider") != "small-prov" || !strings.Contains(h.Get("X-Theophylax-Complexity"), "model=fast") || h.Get("X-Theophylax-Model") != "fast" {
		t.Fatalf("simple request should go to the fast model: %v", h)
	}
	simpleID := h.Get("X-Request-Id")
	_, h, _ = ask("Our service deadlocks under load. Analyze the root cause step by step, consider the race condition, design a fix that keeps the invariant and compare the trade-offs.", nil)
	if h.Get("X-Theophylax-Provider") != "large-prov" || !strings.Contains(h.Get("X-Theophylax-Complexity"), "model=strong") {
		t.Fatalf("hard request should go to the strong model: %v", h)
	}
	// Requested extended thinking pushes a short request up a tier.
	_, h, _ = ask("Plan the database migration.", map[string]any{"max_tokens": 9000, "thinking": map[string]any{"type": "enabled", "budget_tokens": 4000}})
	if h.Get("X-Theophylax-Provider") != "large-prov" {
		t.Fatalf("thinking request: %v", h)
	}

	// The receipt records the router's decision; the log records the model used.
	var rc map[string]any
	for i := 0; i < 50 && rc == nil; i++ {
		time.Sleep(100 * time.Millisecond) // async log flush
		c := &client{t: t, base: ts.URL, hc: http.DefaultClient, auth: key}
		if code, body, _, _ := c.do("GET", "/v1/receipts/"+simpleID, nil); code == 200 {
			rc = body
		}
	}
	if rc == nil {
		t.Fatal("no receipt")
	}
	route := rc["receipt"].(map[string]any)["route"].(map[string]any)
	router, _ := route["router"].(map[string]any)
	if route["model"] != "fast" || router == nil || router["model"] != "auto" || router["tier"].(float64) != 1 || router["classifier"] != "heuristic" {
		t.Fatalf("receipt route: %v", route)
	}

	// A list that names some tier models caps the router at those: a hard
	// request goes to the best allowed tier instead.
	root.must("PATCH", "/api/projects/"+proj, map[string]any{"settings": map[string]any{"allowed_models": []string{"auto*", "fast"}}}, 200)
	waitReload(s)
	hard := msg("auto", "Analyze the root cause of this deadlock step by step, design a fix and compare the trade-offs of each approach.")
	code, _, raw, hdr := gw(t, ts.URL, key, "/v1/messages", hard)
	if code != 200 || hdr.Get("X-Theophylax-Provider") != "small-prov" || !strings.Contains(hdr.Get("X-Theophylax-Complexity"), "model=fast") {
		t.Fatalf("project allow-list should cap routing at fast: %d %s %v", code, raw, hdr)
	}
	// A list without the router refuses it.
	root.must("PATCH", "/api/projects/"+proj, map[string]any{"settings": map[string]any{"allowed_models": []string{"fast", "strong"}}}, 200)
	waitReload(s)
	if code, _, raw, _ := gw(t, ts.URL, key, "/v1/messages", hard); code != 403 || !strings.Contains(string(raw), "auto") {
		t.Fatalf("router not in the project's list: %d %s", code, raw)
	}
	_ = auto

	// Config as code round-trips routers, and creates a router listed
	// before the models it routes to.
	pat := root.must("POST", "/api/keys", map[string]any{"name": "gitops", "kind": "pat", "org_id": org}, 201)["secret"].(string)
	ctx := context.Background()
	cc := confsync.NewClient(ts.URL, pat)
	d, _, err := confsync.Export(ctx, cc, "")
	if err != nil {
		t.Fatal(err)
	}
	y, _ := confsync.MarshalYAML(d, "")
	doc, err := confsync.Parse(y)
	if err != nil {
		t.Fatalf("%v\n%s", err, y)
	}
	if p, err := confsync.Compute(ctx, cc, doc, confsync.Options{}); err != nil || !p.Empty() {
		t.Fatalf("router round trip should be a no-op: %v %+v\n%s", err, p, y)
	}
	doc, err = confsync.Parse([]byte(`version: 1
models:
  - name: auto2
    strategy: complexity
    routing:
      tiers:
        - max_score: 0.4
          model: mid
        - model: strong
  - name: mid
    deployments:
      - provider: small-prov
        upstream_model: mid-1
`))
	if err != nil {
		t.Fatal(err)
	}
	p, err := confsync.Compute(ctx, cc, doc, confsync.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Apply(ctx, cc, nil); err != nil {
		t.Fatal(err)
	}
	if p, err := confsync.Compute(ctx, cc, doc, confsync.Options{}); err != nil || !p.Empty() {
		t.Fatalf("after apply: %v %+v", err, p)
	}
}
