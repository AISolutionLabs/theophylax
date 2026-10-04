package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestModelWatch(t *testing.T) {
	var mu sync.Mutex
	models := []string{"model-a", "model-b"}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/catalog.json":
			_, _ = w.Write([]byte(`{"sample_spec":{},"model-c":{"input_cost_per_token":0.000002,"output_cost_per_token":0.000008,"max_input_tokens":200000,"max_output_tokens":32000,"mode":"chat","supports_function_calling":true},
				"model-a":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000004,"deprecation_date":"2020-01-01"}}`))
		case "/v1/models":
			mu.Lock()
			data := []map[string]string{}
			for _, m := range models {
				data = append(data, map[string]string{"id": m})
			}
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
		case "/v1/chat/completions":
			_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi from c"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
		}
	}))
	defer up.Close()
	t.Setenv("THEOPHYLAX_MODEL_CATALOG_URL", up.URL+"/catalog.json")
	ts, s := setup(t)
	root := newClient(t, ts.URL)
	root.must("POST", "/api/auth/login", map[string]string{"email": "root@example.com", "password": "root-password-123"}, 200)
	prov := root.must("POST", "/api/providers", map[string]any{"name": "upstream", "connection_string": "openai://x?api_key=k&base_url=" + up.URL + "/v1"}, 201)["id"].(string)
	ma := root.must("POST", "/api/models", map[string]any{"name": "model-a"}, 201)["id"].(string)
	root.must("POST", "/api/models/"+ma+"/deployments", map[string]any{"provider_id": prov, "upstream_model": "model-a"}, 201)
	waitReload(s)

	// First scan: a silent baseline (model-b already existed, so it is not "new").
	root.must("POST", "/api/model-watch/scan", map[string]any{}, 200)
	w := root.must("GET", "/api/model-watch", nil, 200)
	if len(w["new"].([]any)) != 0 || w["catalog_models"].(float64) != 2 {
		t.Fatalf("baseline: %v", w)
	}
	// model-a is unpriced and deprecated per the catalog.
	reasons := map[string]bool{}
	for _, x := range w["suggestions"].([]any) {
		reasons[x.(map[string]any)["reason"].(string)] = true
	}
	if !reasons["price_missing"] || !reasons["deprecated"] {
		t.Fatalf("suggestions: %v", w["suggestions"])
	}

	// The provider launches model-c.
	mu.Lock()
	models = append(models, "model-c")
	mu.Unlock()
	res := root.must("POST", "/api/model-watch/scan", map[string]any{}, 200)
	if len(res["new"].([]any)) != 1 {
		t.Fatalf("scan: %v", res)
	}
	w = root.must("GET", "/api/model-watch", nil, 200)
	nw := w["new"].([]any)
	if len(nw) != 1 || nw[0].(map[string]any)["upstream_model"] != "model-c" || nw[0].(map[string]any)["catalog"].(map[string]any)["input_price"].(float64) != 2 {
		t.Fatalf("new: %v", nw)
	}
	added := root.must("POST", "/api/model-watch/discoveries/"+nw[0].(map[string]any)["id"].(string)+"/add", map[string]any{"name": "model-c"}, 200)
	if added["priced"] != true {
		t.Fatalf("add: %v", added)
	}
	waitReload(s)
	key := root.must("POST", "/api/keys", map[string]any{"name": "k", "project_id": root.list("/api/orgs/" + root.list("/api/orgs")[0]["id"].(string) + "/projects")[0]["id"]}, 201)["secret"].(string)
	code, _, raw, _ := gw(t, ts.URL, key, "/v1/chat/completions", map[string]any{"model": "model-c", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	if code != 200 || !strings.Contains(string(raw), "hi from c") {
		t.Fatalf("serve new model: %d %s", code, raw)
	}
	// Applying the catalog prices model-a; the new-models list is now empty.
	root.must("POST", "/api/model-watch/apply-catalog", map[string]any{"model_ids": []string{ma}}, 200)
	var price float64
	_ = s.Pool.QueryRow(t.Context(), `SELECT input_price FROM models WHERE id=$1`, ma).Scan(&price)
	if price != 1 {
		t.Fatalf("model-a price %v", price)
	}
	if w = root.must("GET", "/api/model-watch", nil, 200); len(w["new"].([]any)) != 0 {
		t.Fatalf("after add: %v", w["new"])
	}
	var n int
	_ = s.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_log WHERE action='model_watch.new_models'`).Scan(&n)
	if n != 1 {
		t.Fatalf("audit events: %d", n)
	}
}
