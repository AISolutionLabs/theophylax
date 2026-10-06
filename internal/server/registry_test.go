package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// A skill installed org-wide follows its approved version; pinning lets an
// admin hold the org on one version while a team tries the next, and roll
// back by pinning an earlier approved version.
func TestRegistryStagedRollout(t *testing.T) {
	ts, _ := setup(t)
	root := newClient(t, ts.URL)
	root.must("POST", "/api/auth/login", map[string]string{"email": "root@example.com", "password": "root-password-123"}, 200)
	org := root.list("/api/orgs")[0]["id"].(string)
	teamA := root.must("POST", "/api/orgs/"+org+"/teams", map[string]any{"name": "A"}, 201)["id"].(string)
	teamB := root.must("POST", "/api/orgs/"+org+"/teams", map[string]any{"name": "B"}, 201)["id"].(string)
	projA := root.must("POST", "/api/teams/"+teamA+"/projects", map[string]any{"name": "PA"}, 201)["id"].(string)
	projB := root.must("POST", "/api/teams/"+teamB+"/projects", map[string]any{"name": "PB"}, 201)["id"].(string)
	keyA := root.must("POST", "/api/keys", map[string]any{"name": "a", "project_id": projA}, 201)["secret"].(string)
	keyB := root.must("POST", "/api/keys", map[string]any{"name": "b", "project_id": projB}, 201)["secret"].(string)

	skill := func(v string) map[string]any { return map[string]any{"skill_md": "# Review\nversion " + v} }
	// Org admins publishing with submit are trusted reviewers: v1 is approved at once.
	item := root.must("POST", "/api/orgs/"+org+"/registry", map[string]any{"kind": "skill", "name": "Code review", "version": "1.0.0", "content": skill("1.0.0"), "submit": true}, 201)["id"].(string)
	inst := root.must("POST", "/api/registry/"+item+"/install", map[string]any{"scope_type": "org", "scope_id": org}, 201)["installation"].(map[string]any)
	if inst["pinned"] != false {
		t.Fatalf("install without a version should follow the approved version: %v", inst)
	}
	orgInst := inst["id"].(string)

	served := func(key string) (string, string) {
		t.Helper()
		c := &client{t: t, base: ts.URL, hc: http.DefaultClient, auth: key}
		code, body, raw, _ := c.do("GET", "/v1/skills", nil)
		if code != 200 {
			t.Fatalf("/v1/skills: %d %s", code, raw)
		}
		data := body["data"].([]any)
		if len(data) != 1 {
			t.Fatalf("want one skill, got %s", raw)
		}
		d := data[0].(map[string]any)
		// download_url uses the configured public URL; fetch the same path here.
		u := d["download_url"].(string)
		req, _ := http.NewRequest("GET", ts.URL+u[strings.Index(u, "/v1/"):], nil)
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		zb, _ := io.ReadAll(resp.Body)
		zr, err := zip.NewReader(bytes.NewReader(zb), int64(len(zb)))
		if err != nil {
			t.Fatalf("download: %d %s", resp.StatusCode, zb)
		}
		f, _ := zr.Open("code-review/SKILL.md")
		md, _ := io.ReadAll(f)
		return d["version"].(string), string(md)
	}
	check := func(key, want string) {
		t.Helper()
		v, md := served(key)
		if v != want || !strings.Contains(md, "version "+want) {
			t.Fatalf("served %s (%q), want %s", v, md, want)
		}
	}
	check(keyA, "1.0.0")
	check(keyB, "1.0.0")

	// Hold the org on 1.0.0, then approve 1.1.0 through review.
	root.must("PATCH", "/api/installations/"+orgInst, map[string]any{"version": "1.0.0"}, 200)
	root.must("POST", "/api/registry/"+item+"/versions", map[string]any{"version": "1.1.0", "content": skill("1.1.0")}, 201)
	if code, _, raw, _ := root.do("POST", "/api/registry/"+item+"/install", map[string]any{"scope_type": "team", "scope_id": teamA, "version": "1.1.0"}); code != 400 {
		t.Fatalf("pinning an unapproved version must fail: %d %s", code, raw)
	}
	root.must("POST", "/api/registry/"+item+"/submit", nil, 200)
	root.must("POST", "/api/registry/"+item+"/review", map[string]any{"decision": "approve"}, 200)
	check(keyA, "1.0.0")
	check(keyB, "1.0.0")

	// Team A tries 1.1.0; its installation is more specific than the org's.
	teamInst := root.must("POST", "/api/registry/"+item+"/install", map[string]any{"scope_type": "team", "scope_id": teamA, "version": "1.1.0"}, 201)["installation"].(map[string]any)
	if teamInst["pinned"] != true {
		t.Fatalf("install with a version should be pinned: %v", teamInst)
	}
	check(keyA, "1.1.0")
	check(keyB, "1.0.0")

	// Roll out to everyone: the org follows the approved version again.
	root.must("PATCH", "/api/installations/"+orgInst, map[string]any{"version": ""}, 200)
	check(keyB, "1.1.0")

	// Roll back: pin the org to the earlier approved version; team A stays on its pin.
	root.must("PATCH", "/api/installations/"+orgInst, map[string]any{"version": "1.0.0"}, 200)
	check(keyB, "1.0.0")
	check(keyA, "1.1.0")
	if code, _, raw, _ := root.do("PATCH", "/api/installations/"+orgInst, map[string]any{"version": "9.9.9"}); code != 400 {
		t.Fatalf("pinning an unknown version must fail: %d %s", code, raw)
	}

	// Versions record when they were approved.
	vers := root.must("GET", "/api/registry/"+item, nil, 200)["versions"].([]any)
	for _, v := range vers {
		if v.(map[string]any)["approved_at"] == nil {
			t.Fatalf("version not marked approved: %v", v)
		}
	}
}

// Approved MCP servers installed for a key's scope are listed for clients at
// the version that applies, without their stored tokens; managed agents send
// them to the Claude API with the current connector beta and a toolset each.
func TestMCPServersSyncAndAgent(t *testing.T) {
	var gotBeta []string
	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBeta = r.Header.Values("anthropic-beta")
		gotBody = nil
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-x","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	ts, s := setup(t)
	root := newClient(t, ts.URL)
	root.must("POST", "/api/auth/login", map[string]string{"email": "root@example.com", "password": "root-password-123"}, 200)
	org := root.list("/api/orgs")[0]["id"].(string)
	team := root.must("POST", "/api/orgs/"+org+"/teams", map[string]any{"name": "T"}, 201)["id"].(string)
	proj := root.must("POST", "/api/teams/"+team+"/projects", map[string]any{"name": "P"}, 201)["id"].(string)
	key := root.must("POST", "/api/keys", map[string]any{"name": "k", "project_id": proj}, 201)["secret"].(string)

	jira := root.must("POST", "/api/orgs/"+org+"/registry", map[string]any{"kind": "mcp_server", "name": "Jira", "version": "1.0.0",
		"content": map[string]any{"transport": "http", "url": "https://mcp.example/jira/v1", "authorization_token": "s3cret-token"}, "submit": true}, 201)["id"].(string)
	root.must("POST", "/api/registry/"+jira+"/versions", map[string]any{"version": "2.0.0", "submit": true,
		"content": map[string]any{"transport": "http", "url": "https://mcp.example/jira/v2", "authorization_token": "s3cret-token"}}, 201)
	local := root.must("POST", "/api/orgs/"+org+"/registry", map[string]any{"kind": "mcp_server", "name": "Files", "version": "1.0.0",
		"content": map[string]any{"transport": "stdio", "command": "npx -y @acme/files-mcp"}, "submit": true}, 201)["id"].(string)
	root.must("POST", "/api/orgs/"+org+"/registry", map[string]any{"kind": "mcp_server", "name": "Other", "version": "1.0.0",
		"content": map[string]any{"url": "https://mcp.example/other"}, "submit": true}, 201)
	root.must("POST", "/api/registry/"+jira+"/install", map[string]any{"scope_type": "org", "scope_id": org, "version": "1.0.0"}, 201)
	root.must("POST", "/api/registry/"+local+"/install", map[string]any{"scope_type": "team", "scope_id": team}, 201)

	c := &client{t: t, base: ts.URL, hc: http.DefaultClient, auth: key}
	code, body, raw, _ := c.do("GET", "/v1/mcp/servers", nil)
	if code != 200 {
		t.Fatalf("/v1/mcp/servers: %d %s", code, raw)
	}
	if strings.Contains(string(raw), "s3cret") || strings.Contains(string(raw), "authorization_token_enc") {
		t.Fatalf("stored token leaked to client: %s", raw)
	}
	if n := len(body["data"].([]any)); n != 2 {
		t.Fatalf("want 2 installed servers, got %d: %s", n, raw)
	}
	cfg := body["mcp_json"].(map[string]any)["mcpServers"].(map[string]any)
	if got := cfg["jira"]; !reflect.DeepEqual(got, map[string]any{"type": "http", "url": "https://mcp.example/jira/v1"}) {
		t.Fatalf("jira should be served at its pinned version: %v", got)
	}
	if got := cfg["files"]; !reflect.DeepEqual(got, map[string]any{"type": "stdio", "command": "npx", "args": []any{"-y", "@acme/files-mcp"}}) {
		t.Fatalf("files: %v", got)
	}
	for _, d := range body["data"].([]any) {
		d := d.(map[string]any)
		if d["requires_authorization"] != (d["slug"] == "jira") {
			t.Fatalf("requires_authorization: %v", d)
		}
	}

	// A managed agent with the Jira server, on the Claude API.
	prov := root.must("POST", "/api/providers", map[string]any{"name": "claude", "connection_string": "anthropic://x?api_key=k&base_url=" + upstream.URL}, 201)["id"].(string)
	m := root.must("POST", "/api/models", map[string]any{"name": "claude-x"}, 201)["id"].(string)
	root.must("POST", "/api/models/"+m+"/deployments", map[string]any{"provider_id": prov, "upstream_model": "claude-x"}, 201)
	root.must("POST", "/api/orgs/"+org+"/agents", map[string]any{"team_id": team, "name": "Triage", "slug": "triage", "model": "claude-x", "mcp_servers": []string{jira}}, 201)
	waitReload(s)
	if code, _, raw, _ := gw(t, ts.URL, key, "/v1/agents/triage/messages", msg("claude-x", "hi")); code != 200 {
		t.Fatalf("agent call: %d %s", code, raw)
	}
	if !reflect.DeepEqual(gotBeta, []string{"mcp-client-2025-11-20"}) {
		t.Fatalf("beta: %v", gotBeta)
	}
	servers, _ := gotBody["mcp_servers"].([]any)
	if len(servers) != 1 || servers[0].(map[string]any)["authorization_token"] != "s3cret-token" {
		t.Fatalf("mcp_servers sent upstream: %v", gotBody["mcp_servers"])
	}
	if tools := gotBody["tools"]; !reflect.DeepEqual(tools, []any{map[string]any{"type": "mcp_toolset", "mcp_server_name": "jira"}}) {
		t.Fatalf("tools sent upstream: %v", tools)
	}
}
