package gateway

import (
	"net/http"
	"reflect"
	"testing"

	"theophylax/internal/providers"
)

func mcpBody(extraTools ...any) map[string]any {
	b := map[string]any{
		"model":       "m",
		"mcp_servers": []any{map[string]any{"type": "url", "name": "jira", "url": "https://mcp.example/jira"}, map[string]any{"type": "url", "name": "wiki", "url": "https://mcp.example/wiki"}},
	}
	if len(extraTools) > 0 {
		b["tools"] = extraTools
	}
	return b
}

func TestApplyMCPAddsToolsetsAndCurrentBeta(t *testing.T) {
	fn := map[string]any{"name": "lookup", "input_schema": map[string]any{"type": "object"}}
	own := map[string]any{"type": "mcp_toolset", "mcp_server_name": "wiki", "default_config": map[string]any{"enabled": false}}
	in := mcpBody(fn, own)
	client := http.Header{"Anthropic-Beta": {"files-api-2025-04-14"}}
	for _, kind := range []string{providers.KindAnthropic, providers.KindAzureFoundry} {
		out, h := applyMCP(in, client, kind)
		if got := h.Values("anthropic-beta"); !reflect.DeepEqual(got, []string{"files-api-2025-04-14", mcpBeta}) {
			t.Fatalf("%s: beta %v", kind, got)
		}
		tools := out["tools"].([]any)
		want := []any{fn, own, map[string]any{"type": "mcp_toolset", "mcp_server_name": "jira"}}
		if !reflect.DeepEqual(tools, want) {
			t.Fatalf("%s: tools %v", kind, tools)
		}
	}
	// The caller's body and header are not modified.
	if len(in["tools"].([]any)) != 2 || len(client.Values("anthropic-beta")) != 1 {
		t.Fatalf("input mutated: %v %v", in["tools"], client)
	}
}

func TestApplyMCPKeepsClientChoice(t *testing.T) {
	in := mcpBody()
	client := http.Header{"Anthropic-Beta": {"context-1m-2025-08-07, mcp-client-2026-09-15"}}
	out, h := applyMCP(in, client, providers.KindAnthropic)
	if out["tools"] != nil || len(h.Values("anthropic-beta")) != 1 {
		t.Fatalf("client's MCP beta should be left alone: %v %v", out["tools"], h)
	}
	// Servers in the deprecated shape keep the deprecated beta.
	legacy := map[string]any{"mcp_servers": []any{map[string]any{"type": "url", "name": "jira", "url": "u", "tool_configuration": map[string]any{"enabled": true}}}}
	out, h = applyMCP(legacy, http.Header{}, providers.KindAnthropic)
	if h.Get("anthropic-beta") != mcpBetaLegacy || out["tools"] != nil {
		t.Fatalf("legacy request: %v %v", out, h)
	}
}

func TestApplyMCPStripsWhereUnsupported(t *testing.T) {
	fn := map[string]any{"name": "lookup", "input_schema": map[string]any{"type": "object"}}
	for _, kind := range []string{providers.KindBedrock, providers.KindVertex} {
		out, h := applyMCP(mcpBody(fn, map[string]any{"type": "mcp_toolset", "mcp_server_name": "jira"}), http.Header{}, kind)
		if _, ok := out["mcp_servers"]; ok || !reflect.DeepEqual(out["tools"], []any{fn}) || h.Get("anthropic-beta") != "" {
			t.Fatalf("%s: %v %v", kind, out, h)
		}
		out, _ = applyMCP(mcpBody(map[string]any{"type": "mcp_toolset", "mcp_server_name": "jira"}), http.Header{}, kind)
		if _, ok := out["tools"]; ok {
			t.Fatalf("%s: empty tools should be removed: %v", kind, out)
		}
	}
	plain := map[string]any{"model": "m"}
	if out, h := applyMCP(plain, nil, providers.KindAnthropic); !reflect.DeepEqual(out, plain) || h != nil {
		t.Fatalf("request without MCP changed: %v %v", out, h)
	}
}

func TestToolAllowed(t *testing.T) {
	for _, c := range []struct {
		name        string
		allow, deny []string
		want        bool
	}{
		{"search", nil, nil, true},
		{"search", []string{"search"}, nil, true},
		{"delete", []string{"search"}, nil, false},
		{"delete_repo", nil, []string{"delete_*"}, false},
		{"deleter", nil, []string{"delete_*"}, true},
		{"read_file", []string{"read_*"}, []string{"read_secrets"}, true},
		{"read_secrets", []string{"read_*"}, []string{"read_secrets"}, false},
		{"anything", []string{"*"}, nil, true},
	} {
		if got := ToolAllowed(c.name, c.allow, c.deny); got != c.want {
			t.Errorf("%s allow=%v deny=%v: got %v", c.name, c.allow, c.deny, got)
		}
	}
}
