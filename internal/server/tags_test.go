package server

import (
	"encoding/json"
	"net/url"
	"testing"
	"time"
)

// TestRequestTags covers tags from the X-Theophylax-Tags header and key
// defaults: storage, dropped-pair reporting, log filtering and usage grouping.
func TestRequestTags(t *testing.T) {
	ts, s := setup(t)
	root := newClient(t, ts.URL)
	root.must("POST", "/api/auth/login", map[string]string{"email": "root@example.com", "password": "root-password-123"}, 200)
	org := root.list("/api/orgs")[0]["id"].(string)
	team := root.must("POST", "/api/orgs/"+org+"/teams", map[string]any{"name": "T"}, 201)["id"].(string)
	proj := root.must("POST", "/api/teams/"+team+"/projects", map[string]any{"name": "P"}, 201)["id"].(string)
	prov := root.must("POST", "/api/providers", map[string]any{"name": "mock", "connection_string": "mock://x"}, 201)["id"].(string)
	m := root.must("POST", "/api/models", map[string]any{"name": "m"}, 201)["id"].(string)
	root.must("POST", "/api/models/"+m+"/deployments", map[string]any{"provider_id": prov, "upstream_model": "x"}, 201)
	key := root.must("POST", "/api/keys", map[string]any{"name": "k", "project_id": proj, "metadata": map[string]any{"tags": map[string]string{"cost_center": "cc-42", "env": "staging"}}}, 201)["secret"].(string)
	waitReload(s)

	call := func(tags string) (int, string) {
		code, _, _, hdr := gw(t, ts.URL, key, "/v1/messages", msg("m", "hi"), "X-Theophylax-Tags", tags)
		return code, hdr.Get("X-Theophylax-Tags-Dropped")
	}
	// Header tags merge with the key's defaults and win on conflict.
	if code, dropped := call("feature=search, env=prod"); code != 200 || dropped != "" {
		t.Fatalf("tagged call: %d dropped=%q", code, dropped)
	}
	if code, _ := call("feature=summarise"); code != 200 {
		t.Fatalf("second call: %d", code)
	}
	// Invalid pairs are dropped and reported, the request still succeeds.
	if code, dropped := call("Bad Key=x, novalue, feature=search"); code != 200 || dropped != "2" {
		t.Fatalf("invalid pairs: %d dropped=%q", code, dropped)
	}
	time.Sleep(1500 * time.Millisecond) // async log flush

	var stored string
	_ = s.Pool.QueryRow(t.Context(), `SELECT tags::text FROM request_logs ORDER BY ts LIMIT 1`).Scan(&stored)
	var first map[string]string
	_ = json.Unmarshal([]byte(stored), &first)
	if first["feature"] != "search" || first["env"] != "prod" || first["cost_center"] != "cc-42" {
		t.Fatalf("stored tags: %s", stored)
	}

	// Filter logs by tag.
	logs := root.list("/api/orgs/" + org + "/logs?tag=" + url.QueryEscape("feature:search"))
	if len(logs) != 2 {
		t.Fatalf("tag filter: got %d logs, want 2", len(logs))
	}
	if code, _, _, _ := root.do("GET", "/api/orgs/"+org+"/logs?tag=nope", nil); code != 400 {
		t.Fatalf("malformed tag filter accepted: %d", code)
	}

	// Usage broken down by a tag, with totals still computed.
	u := root.must("GET", "/api/orgs/"+org+"/usage?group_by="+url.QueryEscape("tag:feature"), nil, 200)
	got := map[string]float64{}
	for _, r := range u["breakdown"].([]any) {
		row := r.(map[string]any)
		got[row["label"].(string)] = row["requests"].(float64)
	}
	if got["search"] != 2 || got["summarise"] != 1 {
		t.Fatalf("usage by tag: %v", got)
	}
	if tot := u["totals"].(map[string]any)["requests"].(float64); tot != 3 {
		t.Fatalf("usage totals with tag grouping: %v", tot)
	}
	u = root.must("GET", "/api/orgs/"+org+"/usage?tag="+url.QueryEscape("cost_center:cc-42,env:prod"), nil, 200)
	if tot := u["totals"].(map[string]any)["requests"].(float64); tot != 1 {
		t.Fatalf("usage filtered by two tags: %v", tot)
	}
}
