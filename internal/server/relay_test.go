package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeOpenAI echoes what it received so tests can see exactly what reached
// the model, and streams Responses API events.
func fakeOpenAI(t *testing.T) (*httptest.Server, *[]string) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, r.URL.Path+" "+string(b))
		switch r.URL.Path {
		case "/v1/responses":
			var req map[string]any
			_ = json.Unmarshal(b, &req)
			in, _ := req["input"].(string)
			echo := "you said: " + in
			if req["stream"] == true {
				w.Header().Set("Content-Type", "text/event-stream")
				// Split the echoed text mid-placeholder to exercise incremental restore.
				half := len(echo) / 2
				for _, part := range []string{echo[:half], echo[half:]} {
					d, _ := json.Marshal(map[string]any{"type": "response.output_text.delta", "item_id": "msg_1", "output_index": 0, "content_index": 0, "delta": part})
					fmt.Fprintf(w, "event: response.output_text.delta\ndata: %s\n\n", d)
				}
				done, _ := json.Marshal(map[string]any{"type": "response.output_text.done", "item_id": "msg_1", "output_index": 0, "content_index": 0, "text": echo})
				fmt.Fprintf(w, "event: response.output_text.done\ndata: %s\n\n", done)
				fin, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_1", "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": echo}}}},
					"usage": map[string]any{"input_tokens": 11, "output_tokens": 5, "input_tokens_details": map[string]any{"cached_tokens": 2}}}})
				fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", fin)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "resp_1", "object": "response", "model": req["model"],
				"output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": echo}}}},
				"usage":  map[string]any{"input_tokens": 9, "output_tokens": 4}})
		case "/v1/audio/transcriptions":
			_, params, _ := strings.Cut(r.Header.Get("Content-Type"), "boundary=")
			mr := multipart.NewReader(bytes.NewReader(b), params)
			fields := map[string]string{}
			for {
				p, err := mr.NextPart()
				if err != nil {
					break
				}
				v, _ := io.ReadAll(p)
				fields[p.FormName()] = string(v)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"text": "model=" + fields["model"] + " file=" + fields["file"]})
		case "/v1/audio/speech":
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write([]byte("ID3fakemp3"))
		default:
			http.Error(w, "unexpected "+r.URL.Path, 404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestRelayEndpoints(t *testing.T) {
	ts, s := setup(t)
	up, seen := fakeOpenAI(t)
	root := newClient(t, ts.URL)
	root.must("POST", "/api/auth/login", map[string]string{"email": "root@example.com", "password": "root-password-123"}, 200)
	org := root.list("/api/orgs")[0]["id"].(string)
	proj := root.list("/api/orgs/" + org + "/projects")[0]["id"].(string)
	key := root.must("POST", "/api/keys", map[string]any{"name": "codex", "project_id": proj}, 201)["secret"].(string)
	prov := root.must("POST", "/api/providers", map[string]any{"name": "openai-fake", "connection_string": "openai://x?api_key=k&base_url=" + up.URL + "/v1"}, 201)["id"].(string)
	in := 2.0
	m := root.must("POST", "/api/models", map[string]any{"name": "gpt-x", "input_price": in, "output_price": 8.0}, 201)["id"].(string)
	root.must("POST", "/api/models/"+m+"/deployments", map[string]any{"provider_id": prov, "upstream_model": "gpt-upstream"}, 201)
	waitReload(s)

	// Secret Shield is on by default: the upstream must only see a placeholder,
	// and the client must get the real value back, streamed or not.
	secret := "ghp_" + strings.Repeat("Qw7", 12)
	code, body, raw, hdr := gw(t, ts.URL, key, "/v1/responses", map[string]any{"model": "gpt-x", "input": "deploy with " + secret})
	if code != 200 || !strings.Contains(string(raw), secret) || hdr.Get("X-Theophylax-Receipt") == "" {
		t.Fatalf("responses: %d %s", code, raw)
	}
	if strings.Contains((*seen)[len(*seen)-1], secret) || !strings.Contains((*seen)[len(*seen)-1], `"model":"gpt-upstream"`) {
		t.Fatalf("upstream saw: %s", (*seen)[len(*seen)-1])
	}
	_ = body
	code, _, raw, _ = gw(t, ts.URL, key, "/v1/responses", map[string]any{"model": "gpt-x", "stream": true, "input": "deploy with " + secret})
	if code != 200 || strings.Count(string(raw), secret) < 2 || strings.Contains(string(raw), "[REDACTED") {
		t.Fatalf("responses stream: %d %s", code, raw)
	}
	var tokens, cached int
	var cost float64
	for i := 0; i < 40 && tokens == 0; i++ {
		time.Sleep(100 * time.Millisecond) // async log flush
		_ = s.Pool.QueryRow(t.Context(), `SELECT input_tokens, cache_read_tokens, cost_usd FROM request_logs WHERE endpoint='/v1/responses' AND stream ORDER BY ts DESC LIMIT 1`).Scan(&tokens, &cached, &cost)
	}
	if tokens != 9 || cached != 2 || cost <= 0 {
		t.Fatalf("stream usage: input=%d cached=%d cost=%v", tokens, cached, cost)
	}

	// Multipart audio: model is rewritten to the upstream name, file untouched.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("model", "gpt-x")
	fw, _ := mw.CreateFormFile("file", "a.wav")
	_, _ = fw.Write([]byte("RIFFdata"))
	_ = mw.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/v1/audio/transcriptions", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(out), "model=gpt-upstream file=RIFFdata") {
		t.Fatalf("transcription: %d %s", resp.StatusCode, out)
	}

	// Binary responses stream through unchanged.
	code, _, raw, hdr = gw(t, ts.URL, key, "/v1/audio/speech", map[string]any{"model": "gpt-x", "input": "hello", "voice": "alloy"})
	if code != 200 || string(raw) != "ID3fakemp3" || hdr.Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("speech: %d %q %v", code, raw, hdr)
	}

	// A model on an Anthropic-format provider is bridged: Responses in,
	// Messages to the provider, Responses out — with Secret Shield intact.
	mp := root.must("POST", "/api/providers", map[string]any{"name": "mock", "connection_string": "mock://x"}, 201)["id"].(string)
	m2 := root.must("POST", "/api/models", map[string]any{"name": "claude-x", "input_price": 3.0, "output_price": 15.0}, 201)["id"].(string)
	root.must("POST", "/api/models/"+m2+"/deployments", map[string]any{"provider_id": mp, "upstream_model": "claude-upstream"}, 201)
	waitReload(s)
	history := []any{
		map[string]any{"type": "message", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": "be brief"}}},
		map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "list files"}}},
		map[string]any{"type": "function_call", "call_id": "c1", "name": "shell", "arguments": `{"command":["ls"]}`},
		map[string]any{"type": "function_call_output", "call_id": "c1", "output": "main.go"},
		map[string]any{"type": "message", "role": "user", "content": "now use " + secret},
	}
	code, body, raw, hdr = gw(t, ts.URL, key, "/v1/responses", map[string]any{"model": "claude-x", "input": history,
		"tools": []any{map[string]any{"type": "function", "name": "shell", "parameters": map[string]any{"type": "object"}}}})
	if code != 200 || body["object"] != "response" || !strings.Contains(string(raw), secret) || hdr.Get("X-Theophylax-Provider") != "mock" {
		t.Fatalf("bridge unary: %d %s", code, raw)
	}
	code, _, raw, _ = gw(t, ts.URL, key, "/v1/responses", map[string]any{"model": "claude-x", "stream": true, "input": "say " + secret})
	if code != 200 || !strings.Contains(string(raw), "event: response.completed") || !strings.Contains(string(raw), "response.output_text.delta") ||
		strings.Count(string(raw), secret) < 2 || strings.Contains(string(raw), "[REDACTED") || !strings.Contains(string(raw), ": theophylax-receipt ") {
		t.Fatalf("bridge stream: %d %s", code, raw)
	}
	// The receipt attests to the client's Responses bytes, not the translated ones.
	reqBody := map[string]any{"model": "claude-x", "input": "receipt check"}
	reqRaw, _ := json.Marshal(reqBody)
	code, _, raw, hdr = gw(t, ts.URL, key, "/v1/responses", reqBody)
	if code != 200 {
		t.Fatalf("receipt request: %d %s", code, raw)
	}
	var rec map[string]any
	for i := 0; i < 40 && rec["jws"] == nil; i++ {
		time.Sleep(100 * time.Millisecond)
		_, rec, _, _ = (&client{t: t, base: ts.URL, hc: http.DefaultClient, auth: key}).do("GET", "/v1/receipts/"+hdr.Get("X-Request-Id"), nil)
	}
	_, v, _, _ := (&client{t: t, base: ts.URL, hc: http.DefaultClient}).do("POST", "/v1/receipts/verify", map[string]any{"jws": rec["jws"], "request": string(reqRaw), "response": string(raw)})
	if v["signature_valid"] != true || v["request_matches"] != true || v["response_matches"] != true {
		t.Fatalf("bridged receipt: %v", v)
	}
	code, _, raw, _ = gw(t, ts.URL, key, "/v1/responses", map[string]any{"model": "claude-x", "previous_response_id": "resp_1", "input": "hi"})
	if code != 400 || !strings.Contains(string(raw), `"error"`) {
		t.Fatalf("previous_response_id: %d %s", code, raw)
	}
	// Errors come back in the OpenAI envelope.
	code, _, raw, _ = gw(t, ts.URL, "tpx_bogus", "/v1/responses", map[string]any{"model": "claude-x", "input": "hi"})
	if code != 401 || !strings.Contains(string(raw), `"error"`) {
		t.Fatalf("bridge auth error: %d %s", code, raw)
	}
}
