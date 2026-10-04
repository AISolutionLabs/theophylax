package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"theophylax/internal/auth"
	"theophylax/internal/guardrails"
	"theophylax/internal/metrics"
	"theophylax/internal/providers"
	"theophylax/internal/store"
	"theophylax/internal/util"
)

// Relayed endpoints are OpenAI APIs the gateway forwards to OpenAI-format
// providers without translation, while still applying identity, model
// access, limits, input guardrails, Secret Shield restore, failover, logging,
// cost and receipts.
var relayPaths = []string{
	"/v1/responses",
	"/v1/images/generations", "/v1/images/edits", "/v1/images/variations",
	"/v1/audio/speech", "/v1/audio/transcriptions", "/v1/audio/translations",
	"/v1/moderations",
}

func (g *Gateway) relayRoutes(mux *http.ServeMux) {
	for _, p := range relayPaths {
		if p == "/v1/responses" {
			mux.HandleFunc("POST "+p, g.responses)
			continue
		}
		mux.HandleFunc("POST "+p, g.relay)
	}
}

// begin authenticates and initialises request state shared by every endpoint.
func (g *Gateway) begin(w http.ResponseWriter, r *http.Request, cf providers.Format) (*reqState, *auth.Principal, *Snapshot, http.ResponseWriter, bool) {
	st := &reqState{start: time.Now()}
	reqID := util.NewID("req")
	w.Header().Set("X-Request-Id", reqID)
	st.log = store.RequestLog{ID: reqID, TS: st.start.UTC(), Endpoint: r.URL.Path, Status: 200,
		ClientIP: util.ClientIP(r, g.Cfg.TrustedProxies), UserAgent: truncate(r.UserAgent(), 200)}
	p, err := g.authenticate(r)
	if err != nil {
		code := writeErr(w, cf, err)
		metrics.Requests.Inc("", "", fmt.Sprint(code))
		return nil, nil, nil, w, false
	}
	st.p = p
	st.hw = &hashWriter{ResponseWriter: w, h: sha256.New()}
	st.hw.Header().Set("X-Theophylax-Receipt", reqID)
	st.log.OrgID, st.log.TeamID, st.log.ProjectID = strPtr(p.OrgID), strPtr(p.TeamID), strPtr(p.ProjectID)
	if p.Key != nil {
		st.log.KeyID = &p.Key.ID
	}
	if uid := p.UserID(); uid != "" {
		st.log.UserID = &uid
	}
	st.scopes = scopesFor(p)
	return st, p, g.Loader.Snapshot(), st.hw, true
}

func scopesFor(p *auth.Principal) []ScopeRef {
	s := []ScopeRef{{"org", p.OrgID}}
	if p.TeamID != "" {
		s = append(s, ScopeRef{"team", p.TeamID})
	}
	if p.ProjectID != "" {
		s = append(s, ScopeRef{"project", p.ProjectID})
	}
	if p.Key != nil {
		s = append(s, ScopeRef{"key", p.Key.ID})
	}
	if uid := p.UserID(); uid != "" {
		s = append(s, ScopeRef{"user", uid})
	}
	return s
}

func (g *Gateway) relay(w http.ResponseWriter, r *http.Request) {
	const cf = providers.FormatOpenAI
	st, p, snap, w, ok := g.begin(w, r, cf)
	if !ok {
		return
	}
	reqID := st.log.ID
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	fail := func(err error) {
		code := writeErr(w, cf, err)
		st.log.Status, st.log.Error = code, truncate(err.Error(), 1000)
		g.finish(st)
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, g.Cfg.MaxBodyBytes))
	if err != nil {
		fail(util.Err(413, "request_too_large", "request body too large or unreadable"))
		return
	}
	rd := sha256.Sum256(raw)
	st.reqDigest = hex.EncodeToString(rd[:])

	ctype := r.Header.Get("Content-Type")
	mt, params, _ := mime.ParseMediaType(ctype)
	var body map[string]any
	var form *multipartForm
	if mt == "multipart/form-data" {
		if form, err = readMultipart(raw, params["boundary"]); err != nil {
			fail(util.Err(400, "invalid_request_error", "invalid multipart body: "+err.Error()))
			return
		}
		body = form.fields
	} else {
		body = map[string]any{}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&body); err != nil {
			fail(util.Err(400, "invalid_request_error", "invalid JSON body: "+err.Error()))
			return
		}
	}
	model, _ := body["model"].(string)
	st.log.Model = model
	if model == "" {
		fail(util.Err(400, "invalid_request_error", "model is required"))
		return
	}
	stream := body["stream"] == true
	st.log.Stream = stream
	route := snap.Resolve(model)
	if route == nil {
		fail(util.Err(404, "not_found_error", fmt.Sprintf("model %q is not available on this gateway", model)))
		return
	}
	st.canonical = route.Model.Name
	if err := g.checkModelAllowed(snap, p, model, route.Model.Name); err != nil {
		fail(err)
		return
	}
	if err := g.checkLimits(w, snap, p, st.scopes); err != nil {
		fail(err)
		return
	}

	policies := snap.PoliciesFor(p.OrgID, p.TeamID, p.ProjectID)
	st.policies = policies
	if len(policies) > 0 {
		var refs []guardrails.TextRef
		if path == "/responses" {
			refs = responsesTexts(body)
		} else {
			refs = fieldTexts(body, "input", "prompt", "instructions")
		}
		res := g.Guard.Evaluate(r.Context(), policies, guardrails.Input{Phase: "input", Model: route.Model.Name, Body: body, Texts: refs, OpenAI: true,
			ScopeKey: p.OrgID + "/" + p.ProjectID, Metadata: map[string]string{"org_id": p.OrgID, "team_id": p.TeamID, "project_id": p.ProjectID, "request_id": reqID}})
		st.gr = append(st.gr, res.Findings...)
		if res.Blocked {
			w.Header().Set("X-Theophylax-Guardrail", "blocked")
			fail(util.Err(400, "invalid_request_error", res.Message))
			return
		}
		st.restorer = guardrails.NewRestorer(res.Restore)
	}
	st.capture = g.capturePayloads(snap, p) && form == nil
	if st.capture {
		st.reqPL, _ = json.Marshal(body)
	}

	var resp *http.Response
	var lastErr error
	tried := 0
	for _, d := range route.Order(p.OrgID) {
		fwd, ok := d.Provider.P.(providers.Forwarder)
		if !ok {
			continue
		}
		if tried++; tried > 4 {
			break
		}
		st.log.Attempts++
		upstream := d.Upstream(model)
		var payload []byte
		ct := "application/json"
		if form != nil {
			payload, ct = form.encode(upstream)
		} else {
			nb := shallowCopy(body)
			nb["model"] = upstream
			payload, _ = json.Marshal(nb)
		}
		t0 := time.Now()
		resp, lastErr = fwd.Forward(r.Context(), path, ct, payload, upstream)
		if lastErr == nil {
			d.health.success(time.Since(t0))
			st.log.TTFBMS = int(time.Since(st.start).Milliseconds())
			st.log.UpstreamModel, st.log.ProviderID, st.log.ProviderKind = upstream, &d.ProviderID, d.Provider.P.Kind()
			w.Header().Set("X-Theophylax-Provider", d.Provider.Row.Name)
			break
		}
		if r.Context().Err() != nil {
			st.log.Status, st.log.Error = 499, "client closed request"
			g.finish(st)
			return
		}
		var ue *providers.UpstreamError
		status := 502
		if errors.As(lastErr, &ue) {
			status = ue.Status
		}
		metrics.UpstreamErrors.Inc(d.Provider.P.Kind(), fmt.Sprint(status))
		slog.Warn("relay attempt failed", "request_id", reqID, "path", path, "provider", d.Provider.Row.Name, "status", status, "err", truncate(lastErr.Error(), 300))
		if ue != nil && !ue.Retryable() {
			fail(upstreamHTTPError(ue))
			return
		}
		d.health.fail()
	}
	if resp == nil {
		var ue *providers.UpstreamError
		switch {
		case tried == 0:
			fail(util.Err(400, "invalid_request_error", fmt.Sprintf("%s is served by OpenAI-compatible providers (OpenAI, Azure OpenAI, Gemini, Vertex, vLLM…); model %q has no deployment on one", r.URL.Path, route.Model.Name)))
		case errors.As(lastErr, &ue):
			fail(upstreamHTTPError(ue))
		default:
			fail(util.Err(502, "api_error", "all upstream deployments failed: "+truncate(fmt.Sprint(lastErr), 300)))
		}
		return
	}
	defer resp.Body.Close()
	w.Header().Set("X-Theophylax-Model", route.Model.Name)

	var usage providers.Usage
	rct := resp.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(rct, "text/event-stream"):
		usage = g.relayStream(w, resp, st)
	case strings.HasPrefix(rct, "application/json"):
		b, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
		if err != nil {
			fail(util.Err(502, "api_error", "reading upstream response: "+err.Error()))
			return
		}
		usage = parseAnyUsage(b)
		if st.capture {
			st.respPL = b // captured before restoration: stored payloads never hold restored secrets
		}
		if st.restorer != nil {
			var v any
			d := json.NewDecoder(bytes.NewReader(b))
			d.UseNumber()
			if d.Decode(&v) == nil {
				b, _ = json.Marshal(st.restorer.Walk(v))
			}
		}
		w.Header().Set("Content-Type", rct)
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(b)
	default: // audio and other binary bodies stream straight through
		w.Header().Set("Content-Type", rct)
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}
	st.log.InputTokens, st.log.OutputTokens, st.log.CacheReadTokens = usage.Input, usage.Output, usage.CacheRead
	st.log.CostUSD = Cost(route.Model, usage)
	g.finish(st)
}

// relayStream forwards SSE, restoring Secret Shield placeholders in deltas
// and final payloads, and picks up usage from the terminal event.
func (g *Gateway) relayStream(w http.ResponseWriter, resp *http.Response, st *reqState) providers.Usage {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	st.sseStarted = true
	fl, _ := w.(http.Flusher)
	fw := flushWriter{w: w, fl: fl}
	rd := providers.NewSSEReader(resp.Body)
	var usage providers.Usage
	for {
		ev, err := rd.Next()
		if err != nil {
			break
		}
		if bytes.Equal(bytes.TrimSpace(ev.Data), []byte("[DONE]")) {
			_ = fw.emit(*ev)
			continue
		}
		out := []providers.SSEEvent{*ev}
		if st.restorer != nil {
			out = restoreResponsesEvent(st.restorer, ev)
		}
		if strings.Contains(ev.Event, "completed") || bytes.Contains(ev.Data, []byte(`"usage"`)) {
			if u := parseAnyUsage(ev.Data); u.Input+u.Output > 0 {
				usage = u
			}
		}
		for _, o := range out {
			if fw.emit(o) != nil {
				return usage
			}
		}
	}
	return usage
}

// restoreResponsesEvent handles Responses API events: deltas are fed through
// the incremental restorer (placeholders can span chunks), ".done" events
// first flush any held text, and every other payload is restored whole.
func restoreResponsesEvent(r *guardrails.Restorer, ev *providers.SSEEvent) []providers.SSEEvent {
	var m map[string]any
	d := json.NewDecoder(bytes.NewReader(ev.Data))
	d.UseNumber()
	if d.Decode(&m) != nil {
		return []providers.SSEEvent{*ev}
	}
	typ, _ := m["type"].(string)
	if typ == "" {
		typ = ev.Event
	}
	key := fmt.Sprint(m["item_id"], ":", m["content_index"], ":", m["output_index"])
	jsonText := strings.Contains(typ, "function_call_arguments") || strings.Contains(typ, "custom_tool_call_input")
	var out []providers.SSEEvent
	switch {
	case strings.HasSuffix(typ, ".delta"):
		if s, ok := m["delta"].(string); ok {
			m["delta"] = r.Feed(key, s, jsonText)
			if m["delta"] == "" {
				return nil
			}
		}
	case strings.HasSuffix(typ, ".done"):
		if rest := r.Flush(key, jsonText); rest != "" {
			extra := map[string]any{}
			for k, v := range m {
				if k != "text" && k != "arguments" && k != "input" {
					extra[k] = v
				}
			}
			extra["type"] = strings.TrimSuffix(typ, ".done") + ".delta"
			extra["delta"] = rest
			b, _ := json.Marshal(extra)
			ev2 := providers.SSEEvent{Data: b}
			if ev.Event != "" {
				ev2.Event = extra["type"].(string)
			}
			out = append(out, ev2)
		}
		r.Walk(m)
	default:
		r.Walk(m)
	}
	b, _ := json.Marshal(m)
	return append(out, providers.SSEEvent{Event: ev.Event, Data: b})
}

// parseAnyUsage reads token usage in either the Responses/Anthropic naming
// (input_tokens/output_tokens) or Chat Completions naming, wherever "usage"
// sits (top level or under "response").
func parseAnyUsage(b []byte) providers.Usage {
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return providers.Usage{}
	}
	u, _ := m["usage"].(map[string]any)
	if u == nil {
		if r, ok := m["response"].(map[string]any); ok {
			u, _ = r["usage"].(map[string]any)
		}
	}
	if u == nil {
		return providers.Usage{}
	}
	n := func(k string) int { f, _ := u[k].(float64); return int(f) }
	out := providers.Usage{Input: n("input_tokens") + n("prompt_tokens"), Output: n("output_tokens") + n("completion_tokens")}
	for _, k := range []string{"input_tokens_details", "prompt_tokens_details"} {
		if d, ok := u[k].(map[string]any); ok {
			if c, ok := d["cached_tokens"].(float64); ok {
				out.CacheRead = int(c)
				out.Input -= int(c)
			}
		}
	}
	return out
}

// responsesTexts enumerates text the Responses API sends to the model.
func responsesTexts(body map[string]any) []guardrails.TextRef {
	var refs []guardrails.TextRef
	str := func(c map[string]any, k, role string, latest bool) {
		if _, ok := c[k].(string); ok {
			refs = append(refs, guardrails.TextRef{Get: func() string { s, _ := c[k].(string); return s }, Set: func(v string) { c[k] = v }, Role: role, Latest: latest})
		}
	}
	str(body, "instructions", "system", false)
	str(body, "input", "user", true)
	items, _ := body["input"].([]any)
	for i, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		latest := i == len(items)-1
		role, _ := m["role"].(string)
		switch m["type"] {
		case "function_call_output", "custom_tool_call_output", "local_shell_call_output":
			str(m, "output", "tool", latest)
			continue
		case "function_call":
			str(m, "arguments", "assistant", latest)
			continue
		}
		if role == "" {
			role = "user"
		}
		if role == "developer" {
			role = "system"
		}
		str(m, "content", role, latest)
		if parts, ok := m["content"].([]any); ok {
			for _, pt := range parts {
				if pm, ok := pt.(map[string]any); ok {
					str(pm, "text", role, latest)
				}
			}
		}
	}
	return refs
}

// fieldTexts exposes top-level string (or string list) fields to guardrails.
func fieldTexts(body map[string]any, keys ...string) []guardrails.TextRef {
	var refs []guardrails.TextRef
	for _, k := range keys {
		switch v := body[k].(type) {
		case string:
			k := k
			refs = append(refs, guardrails.TextRef{Get: func() string { s, _ := body[k].(string); return s }, Set: func(s string) { body[k] = s }, Role: "user", Latest: true})
		case []any:
			for i := range v {
				if _, ok := v[i].(string); ok {
					i := i
					refs = append(refs, guardrails.TextRef{Get: func() string { s, _ := v[i].(string); return s }, Set: func(s string) { v[i] = s }, Role: "user", Latest: true})
				}
			}
		}
	}
	return refs
}

type multipartForm struct {
	fields map[string]any // text fields (model, prompt, language…)
	order  []string
	files  []formFile
}

type formFile struct {
	field, name, ctype string
	data               []byte
}

func readMultipart(raw []byte, boundary string) (*multipartForm, error) {
	if boundary == "" {
		return nil, errors.New("missing boundary")
	}
	mr := multipart.NewReader(bytes.NewReader(raw), boundary)
	f := &multipartForm{fields: map[string]any{}}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return f, nil
		}
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(part)
		if err != nil {
			return nil, err
		}
		if part.FileName() != "" {
			f.files = append(f.files, formFile{field: part.FormName(), name: part.FileName(), ctype: part.Header.Get("Content-Type"), data: b})
			continue
		}
		if _, seen := f.fields[part.FormName()]; !seen {
			f.order = append(f.order, part.FormName())
		}
		f.fields[part.FormName()] = string(b)
	}
}

// encode rebuilds the form with the upstream model name (and any text fields
// guardrails rewrote).
func (f *multipartForm) encode(model string) ([]byte, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, k := range f.order {
		v := fmt.Sprint(f.fields[k])
		if k == "model" {
			v = model
		}
		_ = mw.WriteField(k, v)
	}
	for _, fl := range f.files {
		h := make(map[string][]string)
		h["Content-Disposition"] = []string{fmt.Sprintf(`form-data; name=%q; filename=%q`, fl.field, fl.name)}
		if fl.ctype != "" {
			h["Content-Type"] = []string{fl.ctype}
		}
		pw, _ := mw.CreatePart(h)
		_, _ = pw.Write(fl.data)
	}
	_ = mw.Close()
	return buf.Bytes(), mw.FormDataContentType()
}
