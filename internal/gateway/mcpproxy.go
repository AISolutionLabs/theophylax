package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"theophylax/internal/auth"
	"theophylax/internal/guardrails"
	"theophylax/internal/providers"
	"theophylax/internal/util"
)

// The MCP gateway proxies remote MCP servers from the registry at
// /mcp/{slug} (Streamable HTTP transport). Clients authenticate with their
// own gateway credentials; the gateway
//
//   - serves only servers installed for the caller's org, team or project,
//     at the version that applies to it;
//   - injects the server's stored authorization token, so people never hold
//     upstream credentials;
//   - enforces the server's tool policy (allow/deny lists) on tools/list and
//     tools/call;
//   - runs the caller's guardrail policies over tool arguments (before the
//     tool sees them) and over tool results, as untrusted content, before
//     the model sees them — so prompt-injection detection, Secret Shield and
//     PII rules apply to tool traffic;
//   - logs every exchange and signs a receipt for it, like model traffic;
//   - binds upstream session IDs to the caller, so one person's session
//     cannot be used with another person's credentials.

type mcpServer struct {
	itemID  string
	slug    string
	url     string
	token   string
	allow   []string
	deny    []string
	version string
}

const mcpMaxBody = 16 << 20

func (g *Gateway) MCPRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /mcp/{slug}", g.mcpProxy)
	mux.HandleFunc("GET /mcp/{slug}", g.mcpProxy)
	mux.HandleFunc("DELETE /mcp/{slug}", g.mcpProxy)
}

// mcpLookup finds an approved remote MCP server installed for p.
func (g *Gateway) mcpLookup(ctx context.Context, p *auth.Principal, slug string) (*mcpServer, error) {
	var id, version string
	var raw json.RawMessage
	err := g.St.Pool.QueryRow(ctx, `SELECT i.id, v.version, v.content FROM registry_items i
		JOIN installations x ON x.item_id=i.id
		JOIN registry_versions v ON v.item_id=i.id AND v.version = CASE WHEN x.pinned THEN x.version ELSE i.approved_version END
		WHERE i.kind='mcp_server' AND i.slug=$4 AND i.approved_version<>'' AND i.status<>'deprecated' AND i.org_id=$1
		AND ((x.scope_type='org' AND x.scope_id=$1) OR (x.scope_type='team' AND x.scope_id=$2) OR (x.scope_type='project' AND x.scope_id=$3))
		ORDER BY CASE x.scope_type WHEN 'project' THEN 0 WHEN 'team' THEN 1 ELSE 2 END, i.id LIMIT 1`,
		p.OrgID, p.TeamID, p.ProjectID, slug).Scan(&id, &version, &raw)
	if err != nil {
		return nil, util.Err(404, "not_found_error", "no MCP server "+slug+" is installed for this project")
	}
	var c struct {
		Transport  string `json:"transport"`
		URL        string `json:"url"`
		TokenEnc   string `json:"authorization_token_enc"`
		ToolPolicy struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"tool_policy"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	if c.URL == "" {
		return nil, util.Err(400, "invalid_request_error", "MCP server "+slug+" runs locally (stdio) and cannot be reached through the gateway")
	}
	if c.Transport == "sse" {
		return nil, util.Err(501, "api_error", "MCP server "+slug+" uses the legacy HTTP+SSE transport; the gateway proxies Streamable HTTP servers")
	}
	s := &mcpServer{itemID: id, slug: slug, url: c.URL, allow: c.ToolPolicy.Allow, deny: c.ToolPolicy.Deny, version: version}
	if c.TokenEnc != "" {
		b, err := base64.StdEncoding.DecodeString(c.TokenEnc)
		if err == nil {
			s.token, err = g.Box.OpenString(b)
		}
		if err != nil {
			return nil, fmt.Errorf("MCP server %s: stored token unreadable: %w", slug, err)
		}
	}
	return s, nil
}

// ToolAllowed applies an MCP server's tool policy. Patterns may end in * to
// match a prefix. A non-empty allow list admits only matching tools; the
// deny list then removes tools from what is admitted.
func ToolAllowed(name string, allow, deny []string) bool {
	match := func(pats []string) bool {
		for _, p := range pats {
			if p == name || (strings.HasSuffix(p, "*") && strings.HasPrefix(name, strings.TrimSuffix(p, "*"))) {
				return true
			}
		}
		return false
	}
	if len(allow) > 0 && !match(allow) {
		return false
	}
	return !match(deny)
}

// sessionMAC binds an upstream MCP session ID to a caller and server.
func (g *Gateway) sessionMAC(p *auth.Principal, itemID, sid string) string {
	who := p.UserID()
	if p.Key != nil {
		who = "key:" + p.Key.ID
	}
	m := hmac.New(sha256.New, append([]byte("theophylax/mcp-session/v1"), g.Cfg.MasterKey...))
	m.Write([]byte(who + "\x00" + p.ProjectID + "\x00" + itemID + "\x00" + sid))
	return hex.EncodeToString(m.Sum(nil))[:32]
}

func (g *Gateway) wrapSession(p *auth.Principal, itemID, sid string) string {
	if sid == "" {
		return ""
	}
	return sid + "." + g.sessionMAC(p, itemID, sid)
}

func (g *Gateway) unwrapSession(p *auth.Principal, itemID, v string) (string, bool) {
	if v == "" {
		return "", true
	}
	i := strings.LastIndexByte(v, '.')
	if i < 0 {
		return "", false
	}
	sid := v[:i]
	return sid, hmac.Equal([]byte(v[i+1:]), []byte(g.sessionMAC(p, itemID, sid)))
}

type rpcMsg = map[string]any

func rpcID(m rpcMsg) string {
	if id, ok := m["id"]; ok && id != nil {
		b, _ := json.Marshal(id)
		return string(b)
	}
	return ""
}

func rpcError(id any, code int, msg string) rpcMsg {
	return rpcMsg{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}}
}

func (g *Gateway) mcpProxy(w http.ResponseWriter, r *http.Request) {
	st, p, snap, hw, ok := g.begin(w, r, providers.FormatAnthropic)
	if !ok {
		return
	}
	slug := r.PathValue("slug")
	st.log.Endpoint = path.Join("/mcp", slug)
	st.log.Model, st.canonical, st.log.ProviderKind = "mcp:"+slug, "mcp:"+slug, "mcp"
	st.log.Attempts = 1
	fail := func(err error) {
		code := writeErr(hw, providers.FormatAnthropic, err)
		st.log.Status, st.log.Error = code, truncate(err.Error(), 1000)
		g.finish(st)
	}
	if p.ProjectID == "" {
		fail(util.Err(403, "permission_error", "MCP servers are reached with a project key"))
		return
	}
	srv, err := g.mcpLookup(r.Context(), p, slug)
	if err != nil {
		fail(err)
		return
	}
	st.log.UpstreamModel = r.Method
	sid, valid := g.unwrapSession(p, srv.itemID, r.Header.Get("Mcp-Session-Id"))
	if !valid {
		fail(util.Err(404, "not_found_error", "unknown MCP session")) // 404 tells MCP clients to start a new session
		return
	}

	var raw []byte
	pending := map[string]string{} // request id → method, to recognise responses
	var calls []string
	policies := snap.PoliciesFor(p.OrgID, p.TeamID, p.ProjectID)
	st.policies = policies
	meta := map[string]string{"org_id": p.OrgID, "team_id": p.TeamID, "project_id": p.ProjectID, "request_id": st.log.ID, "mcp_server": slug}
	if r.Method == http.MethodPost {
		if raw, err = io.ReadAll(http.MaxBytesReader(w, r.Body, mcpMaxBody)); err != nil {
			fail(util.Err(413, "request_too_large", "request body too large"))
			return
		}
		d := sha256.Sum256(raw)
		st.reqDigest = hex.EncodeToString(d[:])
		msgs, batch, err := decodeRPC(raw)
		if err != nil {
			fail(util.Err(400, "invalid_request_error", "invalid JSON-RPC message: "+err.Error()))
			return
		}
		var refusals []rpcMsg
		changed := false
		for _, m := range msgs {
			method, _ := m["method"].(string)
			if id := rpcID(m); id != "" && method != "" {
				pending[id] = method
			}
			if method != "tools/call" {
				continue
			}
			params, _ := m["params"].(map[string]any)
			name, _ := params["name"].(string)
			calls = append(calls, name)
			if !ToolAllowed(name, srv.allow, srv.deny) {
				st.gr = append(st.gr, guardrails.Finding{Policy: "MCP tool policy", Rule: "tool_policy", Action: "block", Detail: name, Phase: "input"})
				refusals = append(refusals, rpcError(m["id"], -32602, fmt.Sprintf("tool %q is not allowed on %s by your organization's policy", name, slug)))
				continue
			}
			if len(policies) > 0 && params != nil {
				res := g.Guard.Evaluate(r.Context(), policies, guardrails.Input{Phase: "input", Model: st.log.Model, Body: params,
					Texts: guardrails.DeepTexts(params["arguments"], "user"), ScopeKey: p.OrgID + "/" + p.ProjectID, Metadata: meta})
				st.gr = append(st.gr, res.Findings...)
				if res.Blocked {
					refusals = append(refusals, rpcError(m["id"], -32602, "blocked by the gateway: "+res.Message))
					continue
				}
				changed = changed || len(res.Findings) > 0
			}
		}
		if len(calls) > 0 {
			st.log.UpstreamModel = "tools/call " + strings.Join(calls, ",")
		} else if len(msgs) == 1 {
			if m, _ := msgs[0]["method"].(string); m != "" {
				st.log.UpstreamModel = m
			}
		}
		if len(refusals) > 0 {
			// Refused calls never reach the server. In a batch, every
			// request is answered with the refusal so nothing runs partially.
			hw.Header().Set("Content-Type", "application/json")
			hw.Header().Set("X-Theophylax-Guardrail", "blocked")
			var out any = refusals[0]
			if batch {
				all := make([]rpcMsg, 0, len(msgs))
				for _, m := range msgs {
					if rpcID(m) != "" {
						all = append(all, rpcError(m["id"], -32602, "request refused: another call in this batch was blocked"))
					}
				}
				copy(all, refusals)
				out = all
			}
			b, _ := json.Marshal(out)
			hw.WriteHeader(http.StatusOK)
			_, _ = hw.Write(b)
			st.log.Status = http.StatusOK
			st.log.Error = "tool call refused by the gateway"
			g.finish(st)
			return
		}
		if changed {
			raw = encodeRPC(msgs, batch)
		}
	}

	ctx := r.Context()
	if r.Method == http.MethodPost {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, g.Cfg.UpstreamTimeout)
		defer cancel()
	}
	var body io.Reader
	if raw != nil {
		body = bytes.NewReader(raw)
	}
	up, err := http.NewRequestWithContext(ctx, r.Method, srv.url, body)
	if err != nil {
		fail(err)
		return
	}
	for _, h := range []string{"Accept", "Content-Type", "Mcp-Protocol-Version", "Last-Event-Id"} {
		if v := r.Header.Get(h); v != "" {
			up.Header.Set(h, v)
		}
	}
	if sid != "" {
		up.Header.Set("Mcp-Session-Id", sid)
	}
	if srv.token != "" {
		up.Header.Set("Authorization", "Bearer "+srv.token)
	}
	up.Header.Set("User-Agent", "theophylax-mcp-gateway")
	t0 := time.Now()
	resp, err := g.MCPClient.Do(up)
	if err != nil {
		if r.Context().Err() != nil {
			st.log.Status = 499
			g.finish(st)
			return
		}
		fail(util.Err(502, "api_error", "MCP server "+slug+" is unreachable"))
		return
	}
	defer resp.Body.Close()
	st.log.TTFBMS = int(time.Since(t0).Milliseconds())

	for _, h := range []string{"Content-Type", "Cache-Control", "Mcp-Protocol-Version"} {
		if v := resp.Header.Get(h); v != "" {
			hw.Header().Set(h, v)
		}
	}
	if v := resp.Header.Get("Mcp-Session-Id"); v != "" {
		hw.Header().Set("Mcp-Session-Id", g.wrapSession(p, srv.itemID, v))
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		// The server refused the gateway's stored credential; never relay
		// an upstream auth challenge as if the caller's key were wrong.
		fail(util.Err(502, "api_error", fmt.Sprintf("MCP server %s rejected the gateway's credentials (%d)", slug, resp.StatusCode)))
		return
	}
	filter := func(m rpcMsg) rpcMsg { return g.mcpFilterResponse(r.Context(), st, srv, policies, meta, pending, m) }
	ct := resp.Header.Get("Content-Type")
	st.log.Status = resp.StatusCode
	switch {
	case strings.HasPrefix(ct, "text/event-stream"):
		st.log.Stream = true
		hw.Header().Del("Content-Length")
		hw.WriteHeader(resp.StatusCode)
		st.sseStarted = true
		relaySSE(resp.Body, hw, filter)
		if r.Context().Err() != nil {
			st.log.Status = 499
		}
	case strings.HasPrefix(ct, "application/json") && len(pending) > 0:
		rb, err := io.ReadAll(io.LimitReader(resp.Body, mcpMaxBody))
		if err == nil {
			if msgs, batch, err := decodeRPC(rb); err == nil {
				for i := range msgs {
					msgs[i] = filter(msgs[i])
				}
				rb = encodeRPC(msgs, batch)
			}
		}
		hw.Header().Del("Content-Length")
		hw.WriteHeader(resp.StatusCode)
		_, _ = hw.Write(rb)
	default:
		hw.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(hw, io.LimitReader(resp.Body, mcpMaxBody))
	}
	if resp.StatusCode >= 400 && st.log.Error == "" {
		st.log.Error = fmt.Sprintf("MCP server returned %d", resp.StatusCode)
	}
	// Streams carry the receipt as an SSE comment in finish; others in a header
	// cannot (the body is already sent), so they are fetched by request ID.
	g.finish(st)
}

// mcpFilterResponse applies the tool policy to tools/list results and the
// caller's guardrails to tools/call results.
func (g *Gateway) mcpFilterResponse(ctx context.Context, st *reqState, srv *mcpServer, policies []guardrails.Policy, meta map[string]string, pending map[string]string, m rpcMsg) rpcMsg {
	id := rpcID(m)
	method := pending[id]
	if id == "" || method == "" {
		return m
	}
	res, _ := m["result"].(map[string]any)
	if res == nil {
		return m
	}
	switch method {
	case "tools/list":
		tools, _ := res["tools"].([]any)
		kept := make([]any, 0, len(tools))
		for _, t := range tools {
			tm, _ := t.(map[string]any)
			name, _ := tm["name"].(string)
			if ToolAllowed(name, srv.allow, srv.deny) {
				kept = append(kept, t)
			}
		}
		if len(kept) != len(tools) {
			res["tools"] = kept
		}
	case "tools/call":
		if len(policies) == 0 {
			return m
		}
		// Tool output is untrusted input to the model that will read it.
		out := g.Guard.Evaluate(ctx, policies, guardrails.Input{Phase: "input", Model: st.log.Model, Body: res,
			Texts: guardrails.DeepTexts(res, "tool"), ScopeKey: meta["org_id"] + "/" + meta["project_id"], Metadata: meta})
		for i := range out.Findings {
			out.Findings[i].Phase = "tool_result"
		}
		st.gr = append(st.gr, out.Findings...)
		if out.Blocked {
			m["result"] = map[string]any{"isError": true, "content": []any{map[string]any{"type": "text",
				"text": "The gateway withheld this tool result: " + out.Message}}}
		}
	}
	return m
}

func decodeRPC(b []byte) ([]rpcMsg, bool, error) {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '[' {
		var arr []rpcMsg
		if err := json.Unmarshal(b, &arr); err != nil {
			return nil, true, err
		}
		return arr, true, nil
	}
	var m rpcMsg
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, false, err
	}
	if m == nil {
		return nil, false, errors.New("empty message")
	}
	return []rpcMsg{m}, false, nil
}

func encodeRPC(msgs []rpcMsg, batch bool) []byte {
	var v any = msgs
	if !batch && len(msgs) == 1 {
		v = msgs[0]
	}
	b, _ := json.Marshal(v)
	return b
}

// relaySSE copies a Streamable HTTP event stream, passing each event's
// JSON-RPC payload through filter.
func relaySSE(src io.Reader, w http.ResponseWriter, filter func(rpcMsg) rpcMsg) {
	fl, _ := w.(http.Flusher)
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 64<<10), mcpMaxBody)
	var data []string
	var other []string
	emit := func() {
		if len(data) == 0 && len(other) == 0 {
			return
		}
		var b strings.Builder
		for _, l := range other {
			b.WriteString(l + "\n")
		}
		if len(data) > 0 {
			payload := strings.Join(data, "\n")
			var m rpcMsg
			if json.Unmarshal([]byte(payload), &m) == nil && m != nil {
				if nb, err := json.Marshal(filter(m)); err == nil {
					payload = string(nb)
				}
			}
			for _, l := range strings.Split(payload, "\n") {
				b.WriteString("data: " + l + "\n")
			}
		}
		b.WriteString("\n")
		_, _ = io.WriteString(w, b.String())
		if fl != nil {
			fl.Flush()
		}
		data, other = data[:0], other[:0]
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			emit()
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		default:
			other = append(other, line)
		}
	}
	emit()
}

// MCPServerURL is where clients reach a registry MCP server through the gateway.
func MCPServerURL(public, slug string) string { return public + "/mcp/" + slug }
