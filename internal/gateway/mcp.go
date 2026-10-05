package gateway

import (
	"net/http"
	"strings"

	"theophylax/internal/providers"
)

// Anthropic MCP connector beta versions. 2025-11-20 moved tool selection out
// of each server's tool_configuration into an mcp_toolset entry in tools;
// 2025-04-04 is deprecated.
const (
	mcpBeta       = "mcp-client-2025-11-20"
	mcpBetaLegacy = "mcp-client-2025-04-04"
)

// mcpSupported reports whether a provider runs Anthropic's MCP connector:
// the Claude API and Microsoft Foundry do; Bedrock and Vertex do not.
func mcpSupported(kind string) bool {
	return kind == providers.KindAnthropic || kind == providers.KindAzureFoundry
}

// applyMCP prepares a Messages request that carries mcp_servers (from a
// managed agent, or sent by the client) for the provider that will serve it.
// Where the connector is unavailable the servers and their toolsets are
// removed. Otherwise, unless the client already chose a connector beta, the
// current beta is added with an mcp_toolset for each server that has none, so
// every server's tools are enabled as before. Requests in the deprecated
// 2025-04-04 shape (servers with tool_configuration) keep that beta.
func applyMCP(body map[string]any, hdr http.Header, kind string) (map[string]any, http.Header) {
	raw, ok := body["mcp_servers"]
	if !ok {
		return body, hdr
	}
	servers, _ := raw.([]any)
	tools, _ := body["tools"].([]any)
	if !mcpSupported(kind) {
		nb := shallowCopy(body)
		delete(nb, "mcp_servers")
		if len(tools) > 0 {
			kept := make([]any, 0, len(tools))
			for _, t := range tools {
				if m, _ := t.(map[string]any); m == nil || m["type"] != "mcp_toolset" {
					kept = append(kept, t)
				}
			}
			if len(kept) == 0 {
				delete(nb, "tools")
			} else {
				nb["tools"] = kept
			}
		}
		return nb, hdr
	}
	if hasBetaPrefix(hdr, "mcp-client-") {
		return body, hdr
	}
	h := hdr.Clone()
	if h == nil {
		h = http.Header{}
	}
	for _, s := range servers {
		if m, _ := s.(map[string]any); m != nil && m["tool_configuration"] != nil {
			h.Add("anthropic-beta", mcpBetaLegacy)
			return body, h
		}
	}
	h.Add("anthropic-beta", mcpBeta)
	have := map[string]bool{}
	for _, t := range tools {
		if m, _ := t.(map[string]any); m != nil && m["type"] == "mcp_toolset" {
			if n, _ := m["mcp_server_name"].(string); n != "" {
				have[n] = true
			}
		}
	}
	var add []any
	for _, s := range servers {
		m, _ := s.(map[string]any)
		name, _ := m["name"].(string)
		if name == "" || have[name] {
			continue
		}
		have[name] = true
		add = append(add, map[string]any{"type": "mcp_toolset", "mcp_server_name": name})
	}
	if len(add) > 0 {
		body = shallowCopy(body)
		body["tools"] = append(append(make([]any, 0, len(tools)+len(add)), tools...), add...)
	}
	return body, h
}

// hasBetaPrefix reports whether any anthropic-beta value starts with prefix.
// Values may be repeated headers or comma-separated.
func hasBetaPrefix(h http.Header, prefix string) bool {
	for _, v := range h.Values("anthropic-beta") {
		for _, b := range strings.Split(v, ",") {
			if strings.HasPrefix(strings.TrimSpace(b), prefix) {
				return true
			}
		}
	}
	return false
}
