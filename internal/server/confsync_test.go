package server

import (
	"context"
	"strings"
	"testing"

	"theophylax/internal/confsync"
)

// TestConfigAsCode exports a gateway's configuration, applies it back
// unchanged (no changes), applies an edited document, and prunes.
func TestConfigAsCode(t *testing.T) {
	ts, s := setup(t)
	t.Setenv("TPX_TEST_UPSTREAM_KEY", "from-the-environment")
	root := newClient(t, ts.URL)
	root.must("POST", "/api/auth/login", map[string]string{"email": "root@example.com", "password": "root-password-123"}, 200)
	org := root.list("/api/orgs")[0]
	orgID, orgSlug := org["id"].(string), org["slug"].(string)
	pat := root.must("POST", "/api/keys", map[string]any{"name": "gitops", "kind": "pat", "org_id": orgID}, 201)["secret"].(string)

	// Seed some configuration through the console API.
	team := root.must("POST", "/api/orgs/"+orgID+"/teams", map[string]any{"name": "ML", "slug": "ml", "settings": map[string]any{"log_payloads": true}}, 201)["id"].(string)
	chat := root.must("POST", "/api/teams/"+team+"/projects", map[string]any{"name": "Chat", "slug": "chat"}, 201)["id"].(string)
	secretProv := root.must("POST", "/api/providers", map[string]any{"name": "secret", "connection_string": "mock://s?api_key=sk-live-abcdef123456"}, 201)["id"].(string)
	refProv := root.must("POST", "/api/providers", map[string]any{"name": "byref", "connection_string": "mock://r?api_key=${env:TPX_TEST_UPSTREAM_KEY}"}, 201)["id"].(string)
	m1 := root.must("POST", "/api/models", map[string]any{"name": "m1", "aliases": []string{"m1-latest"}}, 201)["id"].(string)
	root.must("POST", "/api/models/"+m1+"/deployments", map[string]any{"provider_id": secretProv, "upstream_model": "a", "priority": 0}, 201)
	root.must("POST", "/api/models/"+m1+"/deployments", map[string]any{"provider_id": refProv, "upstream_model": "b", "priority": 1}, 201)
	root.must("POST", "/api/orgs/"+orgID+"/guardrails", map[string]any{"name": "words", "scope_type": "team", "scope_id": team,
		"rules": []any{map[string]any{"type": "keywords", "words": []string{"bluebird"}, "action": "block"}}}, 201)
	root.must("POST", "/api/orgs/"+orgID+"/guardrails", map[string]any{"name": "temporary", "mode": "monitor"}, 201)
	root.must("POST", "/api/orgs/"+orgID+"/budgets", map[string]any{"scope_type": "team", "scope_id": team, "period": "monthly", "limit_usd": 100}, 201)

	ctx := context.Background()
	c := confsync.NewClient(ts.URL, pat)
	d, warn, err := confsync.Export(ctx, c, "")
	if err != nil || len(warn) != 0 {
		t.Fatal(err, warn)
	}
	y, err := confsync.MarshalYAML(d, "test")
	if err != nil {
		t.Fatal(err)
	}
	ys := string(y)
	if strings.Contains(ys, "sk-live-abcdef123456") || !strings.Contains(ys, "****3456") || !strings.Contains(ys, "${env:TPX_TEST_UPSTREAM_KEY}") {
		t.Fatalf("export must mask literal secrets and keep references:\n%s", ys)
	}

	plan := func(src string, prune bool) *confsync.Plan {
		t.Helper()
		doc, err := confsync.Parse([]byte(src))
		if err != nil {
			t.Fatalf("%v\n%s", err, src)
		}
		p, err := confsync.Compute(ctx, c, doc, confsync.Options{Prune: prune})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	describe := func(p *confsync.Plan) string {
		var b strings.Builder
		p.Write(&b)
		return b.String()
	}

	// 1. The export applied back is a no-op, with and without pruning.
	for _, prune := range []bool{false, true} {
		if p := plan(ys, prune); !p.Empty() {
			t.Fatalf("round trip (prune=%v) should be a no-op:\n%s\n%s", prune, describe(p), ys)
		}
	}

	// 2. Edit the document: new team, project and budget on it, a changed
	// model, a new provider with a deployment, a guardrail switched to monitor.
	edited := strings.Replace(ys, "      - slug: ml\n", `      - slug: research
        name: Research
        projects:
          - slug: eval
            name: Evaluation
      - slug: ml
`, 1)
	edited = strings.Replace(edited, "    budgets:\n", `    budgets:
      - scope: project:research/eval
        period: daily
        limit_tokens: 50000
        action: block
`, 1)
	edited = strings.Replace(edited, "  - name: m1\n", "  - name: m1\n    display_name: Model One\n", 1)
	edited = strings.Replace(edited, "providers:\n", `providers:
  - name: fresh
    connection_string: "mock://f?api_key=sk-new-key-0000"
`, 1)
	edited = strings.Replace(edited, "    deployments:\n", `    deployments:
      - provider: fresh
        upstream_model: c
        priority: 2
`, 1)
	w := strings.Index(edited, "- name: words")
	edited = edited[:w] + strings.Replace(edited[w:], "mode: enforce", "mode: monitor", 1)
	p := plan(edited, false)
	got := describe(p)
	for _, want := range []string{"+ create team " + orgSlug + "/research", "+ create project " + orgSlug + "/research/eval",
		"+ create budget " + orgSlug + "/project:research/eval daily", "~ update model m1 (display_name)", "+ create provider fresh",
		"+ create deployment m1 → fresh:c", "~ update guardrail " + orgSlug + "/words (mode)", "Plan: 5 to create, 2 to update, 0 to delete."} {
		if !strings.Contains(got, want) {
			t.Fatalf("plan is missing %q:\n%s\n--- document:\n%s", want, got, edited)
		}
	}
	// Planning changes nothing.
	if p2 := plan(edited, false); describe(p2) != got {
		t.Fatalf("planning must not change the gateway:\n%s", describe(p2))
	}
	if _, err := p.Apply(ctx, c, nil); err != nil {
		t.Fatal(err)
	}
	if p := plan(edited, true); !p.Empty() {
		t.Fatalf("after apply the document should match:\n%s", describe(p))
	}
	waitReload(s)
	// The new deployment serves traffic, and the untouched secret still
	// belongs to the original provider.
	var enc []byte
	if err := s.Pool.QueryRow(ctx, `SELECT conn_enc FROM providers WHERE id=$1`, secretProv).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if conn, err := s.API.Box.OpenString(enc); err != nil || conn != "mock://s?api_key=sk-live-abcdef123456" {
		t.Fatalf("stored secret changed: %q %v", conn, err)
	}
	if code, _, raw, hdr := gw(t, ts.URL, root.must("POST", "/api/keys", map[string]any{"name": "k", "project_id": chat}, 201)["secret"].(string), "/v1/messages", msg("m1", "hi")); code != 200 {
		t.Fatalf("gateway after apply: %d %s %v", code, raw, hdr)
	}

	// 3. Remove the temporary guardrail and a deployment from the document:
	// without -prune nothing is deleted; with it, exactly those two are.
	pruned := cut(t, edited, "      - name: temporary\n")
	pruned = cut(t, pruned, "      - provider: byref\n")
	if p := plan(pruned, false); !p.Empty() {
		t.Fatalf("without prune nothing should be deleted:\n%s", describe(p))
	}
	p = plan(pruned, true)
	got = describe(p)
	if !strings.Contains(got, "- delete guardrail "+orgSlug+"/temporary") || !strings.Contains(got, "- delete deployment m1 → byref:b") || !strings.Contains(got, "0 to create, 0 to update, 2 to delete") {
		t.Fatalf("prune plan:\n%s", got)
	}
	if _, err := p.Apply(ctx, c, nil); err != nil {
		t.Fatal(err)
	}
	if p := plan(pruned, true); !p.Empty() {
		t.Fatalf("after prune:\n%s", describe(p))
	}

	// 4. Problems are reported before anything is changed.
	bad := strings.Replace(pruned, "****3456", "****9999", 1)
	bad = strings.Replace(bad, "scope: project:research/eval", "scope: project:research/nope", 1)
	doc, err := confsync.Parse([]byte(bad))
	if err != nil {
		t.Fatal(err)
	}
	_, err = confsync.Compute(ctx, c, doc, confsync.Options{})
	if err == nil || !strings.Contains(err.Error(), "masked secret") || !strings.Contains(err.Error(), "research/nope") {
		t.Fatalf("want both problems reported, got %v", err)
	}

	// 5. A PAT without platform admin cannot change global models; the
	// API's own permission check applies.
	root.must("POST", "/api/members", map[string]any{"email": "dev@example.com", "scope_type": "org", "scope_id": orgID, "role": "admin"}, 201)
	var devID string
	for _, u := range root.list("/api/users") {
		if u["email"] == "dev@example.com" {
			devID = u["id"].(string)
		}
	}
	root.must("PATCH", "/api/users/"+devID, map[string]any{"password": "dev-password-123"}, 200)
	dev := newClient(t, ts.URL)
	dev.must("POST", "/api/auth/login", map[string]string{"email": "dev@example.com", "password": "dev-password-123"}, 200)
	devPAT := dev.must("POST", "/api/keys", map[string]any{"name": "ci", "kind": "pat", "org_id": orgID}, 201)["secret"].(string)
	doc, _ = confsync.Parse([]byte(strings.Replace(pruned, "display_name: Model One", "display_name: Hijacked", 1)))
	dc := confsync.NewClient(ts.URL, devPAT)
	if p, err = confsync.Compute(ctx, dc, doc, confsync.Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Apply(ctx, dc, nil); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("org admin must not edit global models: %v", err)
	}
}

// cut removes the YAML list item that starts with the line start: that line
// and every following line indented deeper than it.
func cut(t *testing.T, s, start string) string {
	t.Helper()
	i := strings.Index(s, start)
	if i < 0 {
		t.Fatalf("%q not found", start)
	}
	ind := len(start) - len(strings.TrimLeft(start, " "))
	rest, n := s[i+len(start):], 0
	for _, l := range strings.SplitAfter(s[i+len(start):], "\n") {
		if strings.TrimSpace(l) != "" && len(l)-len(strings.TrimLeft(l, " ")) <= ind {
			break
		}
		n += len(l)
	}
	return s[:i] + rest[n:]
}
