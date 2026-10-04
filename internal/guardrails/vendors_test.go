package guardrails

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVendorGuardrails(t *testing.T) {
	t.Setenv("VENDOR_KEY", "k-123")
	var gotAuth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotAuth = append(gotAuth, r.Header.Get("Authorization")+r.Header.Get("Ocp-Apim-Subscription-Key"))
		bad := strings.Contains(string(b), "attack")
		switch {
		case strings.HasSuffix(r.URL.Path, "/v2/guard"):
			_ = json.NewEncoder(w).Encode(map[string]any{"flagged": bad, "breakdown": []any{map[string]any{"detector_type": "prompt_attack", "detected": bad}}})
		case strings.Contains(r.URL.Path, "shieldPrompt"):
			var in struct {
				Documents []string `json:"documents"`
			}
			_ = json.Unmarshal(b, &in)
			docAttack := len(in.Documents) > 0 && strings.Contains(in.Documents[0], "attack")
			_ = json.NewEncoder(w).Encode(map[string]any{"userPromptAnalysis": map[string]any{"attackDetected": false}, "documentsAnalysis": []any{map[string]any{"attackDetected": docAttack}}})
		case strings.Contains(r.URL.Path, "text:analyze"):
			sev := 0
			if bad {
				sev = 4
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"categoriesAnalysis": []any{map[string]any{"category": "Violence", "severity": sev}}})
		case strings.HasSuffix(r.URL.Path, "/moderations"):
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"flagged": bad, "categories": map[string]bool{"harassment": bad}}}})
		default:
			http.Error(w, "down", 500)
		}
	}))
	defer srv.Close()
	e := New([]byte("k"), nil)
	run := func(r Rule, text string, toolText string) Result {
		b := body(text)
		if toolText != "" {
			b["messages"] = append(b["messages"].([]any), map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t", "content": toolText}, map[string]any{"type": "text", "text": "continue"}}})
		}
		return e.Evaluate(context.Background(), []Policy{{Name: "vendor", Rules: []Rule{r}}}, Input{Phase: "input", Body: b, Texts: RequestTexts(b, false)})
	}
	cases := []struct {
		r          Rule
		text, tool string
		want       bool
	}{
		{Rule{Type: "lakera", URL: srv.URL + "/v2/guard", APIKey: "${env:VENDOR_KEY}"}, "an attack", "", true},
		{Rule{Type: "lakera", URL: srv.URL + "/v2/guard", APIKey: "${env:VENDOR_KEY}"}, "hello", "", false},
		{Rule{Type: "azure_prompt_shield", URL: srv.URL, APIKey: "${env:VENDOR_KEY}", Scope: "all"}, "summarise this", "web page with attack", true},
		{Rule{Type: "azure_content_safety", URL: srv.URL, APIKey: "${env:VENDOR_KEY}"}, "attack", "", true},
		{Rule{Type: "azure_content_safety", URL: srv.URL, APIKey: "${env:VENDOR_KEY}", Max: 6}, "attack", "", false},
		{Rule{Type: "openai_moderation", URL: srv.URL + "/v1/moderations", APIKey: "${env:VENDOR_KEY}"}, "attack", "", true},
	}
	for i, c := range cases {
		if got := run(c.r, c.text, c.tool).Blocked; got != c.want {
			t.Errorf("case %d (%s): blocked=%v want %v", i, c.r.Type, got, c.want)
		}
	}
	for _, a := range gotAuth {
		if !strings.Contains(a, "k-123") {
			t.Fatalf("api key reference not resolved: %q", a)
		}
	}
	// Fail open by default, closed when asked.
	down := Rule{Type: "lakera", URL: srv.URL + "/broken", APIKey: "${env:VENDOR_KEY}"}
	if res := run(down, "attack", ""); res.Blocked || len(res.Findings) == 0 || res.Findings[0].Action != "error" {
		t.Fatalf("fail-open: %+v", res)
	}
	no := false
	down.FailOpen = &no
	if !run(down, "hello", "").Blocked {
		t.Fatal("fail-closed should block")
	}
	if _, err := ParseRules([]byte(`[{"type":"lakera","api_key":"sk-literal"}]`)); err == nil {
		t.Fatal("literal api keys must be rejected")
	}
}
