package server

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
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
