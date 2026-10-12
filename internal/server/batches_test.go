package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMessageBatches(t *testing.T) {
	ts, s := setup(t)
	root := newClient(t, ts.URL)
	root.must("POST", "/api/auth/login", map[string]string{"email": "root@example.com", "password": "root-password-123"}, 200)
	org := root.list("/api/orgs")[0]["id"].(string)
	team := root.must("POST", "/api/orgs/"+org+"/teams", map[string]any{"name": "Batch"}, 201)["id"].(string)
	proj := root.must("POST", "/api/teams/"+team+"/projects", map[string]any{"name": "Jobs"}, 201)["id"].(string)
	other := root.must("POST", "/api/teams/"+team+"/projects", map[string]any{"name": "Other"}, 201)["id"].(string)
	fast := root.must("POST", "/api/providers", map[string]any{"name": "fast", "connection_string": "mock://f"}, 201)["id"].(string)
	slow := root.must("POST", "/api/providers", map[string]any{"name": "slow", "connection_string": "mock://s?latency_ms=400"}, 201)["id"].(string)
	m1 := root.must("POST", "/api/models", map[string]any{"name": "m1"}, 201)["id"].(string)
	root.must("POST", "/api/models/"+m1+"/deployments", map[string]any{"provider_id": fast, "upstream_model": "a"}, 201)
	ms := root.must("POST", "/api/models", map[string]any{"name": "slowpoke"}, 201)["id"].(string)
	root.must("POST", "/api/models/"+ms+"/deployments", map[string]any{"provider_id": slow, "upstream_model": "b"}, 201)
	key := root.must("POST", "/api/keys", map[string]any{"name": "batch", "project_id": proj}, 201)["secret"].(string)
	otherKey := root.must("POST", "/api/keys", map[string]any{"name": "other", "project_id": other}, 201)["secret"].(string)
	waitReload(s)

	call := func(k, method, path string, body any) (int, map[string]any, []byte) {
		c := &client{t: t, base: ts.URL, hc: http.DefaultClient, auth: k}
		code, m, raw, _ := c.do(method, path, body)
		return code, m, raw
	}
	item := func(id, model, text string) map[string]any {
		return map[string]any{"custom_id": id, "params": msg(model, text)}
	}
	waitEnded := func(id string) map[string]any {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			_, b, raw := call(key, "GET", "/v1/messages/batches/"+id, nil)
			if b["processing_status"] == "ended" {
				return b
			}
			if b["type"] != "message_batch" {
				t.Fatalf("get: %s", raw)
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("batch %s did not end", id)
		return nil
	}
	results := func(id string) []map[string]any {
		t.Helper()
		req, _ := http.NewRequest("GET", ts.URL+"/v1/messages/batches/"+id+"/results", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("results: %v %v", err, resp)
		}
		defer resp.Body.Close()
		var out []map[string]any
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Fatalf("results line %q: %v", sc.Text(), err)
			}
			out = append(out, m)
		}
		return out
	}

	// Validation happens up front.
	for name, body := range map[string]any{
		"empty":     map[string]any{"requests": []any{}},
		"duplicate": map[string]any{"requests": []any{item("a", "m1", "x"), item("a", "m1", "y")}},
		"bad id":    map[string]any{"requests": []any{item("has space", "m1", "x")}},
		"stream":    map[string]any{"requests": []any{map[string]any{"custom_id": "s", "params": map[string]any{"model": "m1", "stream": true, "max_tokens": 5, "messages": []any{}}}}},
		"no model":  map[string]any{"requests": []any{map[string]any{"custom_id": "s", "params": map[string]any{"max_tokens": 5}}}},
	} {
		if code, _, raw := call(key, "POST", "/v1/messages/batches", body); code != 400 {
			t.Fatalf("%s: want 400, got %d %s", name, code, raw)
		}
	}

	// A batch runs through the normal pipeline; an unknown model errors
	// that request only.
	code, b, raw := call(key, "POST", "/v1/messages/batches", map[string]any{"requests": []any{
		item("first", "m1", "hello one"), item("second", "m1", "hello two"), item("broken", "no-such-model", "x")}})
	if code != 200 || b["type"] != "message_batch" || !strings.HasPrefix(b["id"].(string), "msgbatch_") || b["processing_status"] != "in_progress" || b["results_url"] != nil {
		t.Fatalf("create: %d %s", code, raw)
	}
	id := b["id"].(string)
	if code, _, _ := call(key, "GET", "/v1/messages/batches/"+id+"/results", nil); code != 400 {
		t.Fatalf("results before end: %d", code)
	}
	if code, _, _ := call(key, "DELETE", "/v1/messages/batches/"+id, nil); code != 400 {
		t.Fatalf("delete while running: %d", code)
	}
	if code, _, _ := call(otherKey, "GET", "/v1/messages/batches/"+id, nil); code != 404 {
		t.Fatalf("another project must not see the batch: %d", code)
	}
	b = waitEnded(id)
	counts := b["request_counts"].(map[string]any)
	if counts["succeeded"].(float64) != 2 || counts["errored"].(float64) != 1 || counts["processing"].(float64) != 0 || b["results_url"] != "http://gw.test/v1/messages/batches/"+id+"/results" {
		t.Fatalf("ended batch: %v", b)
	}
	res := results(id)
	if len(res) != 3 || res[0]["custom_id"] != "first" || res[2]["custom_id"] != "broken" {
		t.Fatalf("results: %v", res)
	}
	r0 := res[0]["result"].(map[string]any)
	if r0["type"] != "succeeded" || r0["message"].(map[string]any)["type"] != "message" {
		t.Fatalf("succeeded result: %v", r0)
	}
	r2 := res[2]["result"].(map[string]any)
	if r2["type"] != "errored" || r2["error"].(map[string]any)["type"] != "error" {
		t.Fatalf("errored result: %v", r2)
	}
	// Each request was logged with its key and project, like live traffic.
	deadline := time.Now().Add(10 * time.Second)
	var n int
	for time.Now().Before(deadline) {
		_ = s.Pool.QueryRow(t.Context(), `SELECT count(*) FROM request_logs WHERE endpoint='/v1/messages/batches' AND project_id=$1 AND status=200`, proj).Scan(&n)
		if n == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if n != 2 {
		t.Fatalf("want 2 logged batch requests, got %d", n)
	}
	// Parameters are encrypted at rest and dropped once a request is done.
	rows, err := s.Pool.Query(t.Context(), `SELECT params_enc IS NOT NULL, result_enc FROM message_batch_items WHERE batch_id=$1`, id)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var hasParams bool
		var enc []byte
		_ = rows.Scan(&hasParams, &enc)
		if hasParams || len(enc) == 0 || bytes.Contains(enc, []byte(`"type"`)) {
			t.Fatal("finished requests must not keep parameters, and results must be encrypted")
		}
	}
	rows.Close()

	// List, newest first, with pagination.
	_, l, raw := call(key, "GET", "/v1/messages/batches?limit=1", nil)
	if len(l["data"].([]any)) != 1 || l["first_id"] != id || l["has_more"] != false {
		t.Fatalf("list: %s", raw)
	}

	// Cancel: requests not yet started are canceled.
	reqs := []any{}
	for i := range 24 {
		reqs = append(reqs, item(fmt.Sprintf("r%d", i), "slowpoke", "wait"))
	}
	_, b, _ = call(key, "POST", "/v1/messages/batches", map[string]any{"requests": reqs})
	cid := b["id"].(string)
	time.Sleep(150 * time.Millisecond)
	_, b, raw = call(key, "POST", "/v1/messages/batches/"+cid+"/cancel", nil)
	if b["processing_status"] != "canceling" && b["processing_status"] != "ended" || b["cancel_initiated_at"] == nil {
		t.Fatalf("cancel: %s", raw)
	}
	b = waitEnded(cid)
	counts = b["request_counts"].(map[string]any)
	if counts["canceled"].(float64) < 10 || counts["canceled"].(float64)+counts["succeeded"].(float64) != 24 {
		t.Fatalf("canceled batch: %v", counts)
	}

	// Revoking the key stops a batch: remaining requests fail authentication.
	reqs = reqs[:16]
	_, b, _ = call(key, "POST", "/v1/messages/batches", map[string]any{"requests": reqs})
	rid := b["id"].(string)
	var keyID string
	for _, k := range root.list("/api/orgs/" + org + "/keys") {
		if k["name"] == "batch" {
			keyID = k["id"].(string)
		}
	}
	time.Sleep(100 * time.Millisecond)
	root.must("POST", "/api/keys/"+keyID+"/revoke", nil, 200)
	deadline = time.Now().Add(30 * time.Second)
	var errored, total int
	for time.Now().Before(deadline) {
		_ = s.Pool.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE status='errored'), count(*) FILTER (WHERE status IN ('succeeded','errored')) FROM message_batch_items WHERE batch_id=$1`, rid).Scan(&errored, &total)
		if total == 16 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if total != 16 || errored < 8 {
		t.Fatalf("after revocation: %d errored of %d settled", errored, total)
	}
	var sample []byte
	_ = s.Pool.QueryRow(t.Context(), `SELECT result_enc FROM message_batch_items WHERE batch_id=$1 AND status='errored' LIMIT 1`, rid).Scan(&sample)
	if plain, _ := s.API.Box.Open(sample); !bytes.Contains(plain, []byte("revoked")) {
		t.Fatalf("errored result should say the key was revoked: %s", plain)
	}

	// Ended batches can be deleted (with a new key: the old one is revoked).
	key = root.must("POST", "/api/keys", map[string]any{"name": "batch2", "project_id": proj}, 201)["secret"].(string)
	if code, b, raw := call(key, "DELETE", "/v1/messages/batches/"+id, nil); code != 200 || b["type"] != "message_batch_deleted" {
		t.Fatalf("delete: %d %s", code, raw)
	}
	if code, _, _ := call(key, "GET", "/v1/messages/batches/"+id, nil); code != 404 {
		t.Fatalf("deleted batch still visible: %d", code)
	}
}
