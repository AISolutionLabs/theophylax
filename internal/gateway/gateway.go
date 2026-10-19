// Package gateway is the inference data plane: authentication, routing with
// failover, guardrails, limits, streaming, format translation and accounting.
package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"theophylax/internal/audit"
	"theophylax/internal/auth"
	"theophylax/internal/config"
	"theophylax/internal/export"
	"theophylax/internal/guardrails"
	"theophylax/internal/metrics"
	"theophylax/internal/pricing"
	"theophylax/internal/providers"
	"theophylax/internal/receipts"
	"theophylax/internal/store"
	"theophylax/internal/util"
)

type Gateway struct {
	Cfg      *config.Config
	St       *store.Store
	Loader   *Loader
	Guard    *guardrails.Engine
	RL       *RateLimiter
	Budgets  *Budgets
	Logs     *RequestLogger
	Resolver *auth.Resolver
	Signer   *receipts.Signer
	Audit    *audit.Logger
	Export   *export.Exporter // optional OTLP / log sink streaming
	Pricing  *pricing.Catalog
	// OnBlock, if set, is told about every request a guardrail blocked (for alerts).
	OnBlock func(orgID, policy, rule, detail string)
	Cache   *ResponseCache // optional response cache
}

func (g *Gateway) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) { g.inference(w, r, providers.FormatAnthropic, "") })
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) { g.inference(w, r, providers.FormatOpenAI, "") })
	mux.HandleFunc("POST /v1/agents/{agent}/messages", func(w http.ResponseWriter, r *http.Request) {
		g.inference(w, r, providers.FormatAnthropic, r.PathValue("agent"))
	})
	mux.HandleFunc("POST /v1/agents/{agent}/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		g.inference(w, r, providers.FormatOpenAI, r.PathValue("agent"))
	})
	mux.HandleFunc("POST /v1/embeddings", g.embeddings)
	g.relayRoutes(mux)
	mux.HandleFunc("POST /v1/messages/count_tokens", g.countTokens)
	mux.HandleFunc("GET /v1/models", g.listModels)
	mux.HandleFunc("GET /v1/whoami", g.whoami)
}

// ---------------------------------------------------------------------------
// Authentication

func (g *Gateway) authenticate(r *http.Request) (*auth.Principal, error) {
	ctx := r.Context()
	if p, ok := ctx.Value(principalCtxKey{}).(*auth.Principal); ok {
		return p, nil // internal request (message batches), authenticated by its creator
	}
	tok := auth.BearerToken(r)
	snap := g.Loader.Snapshot()
	var p *auth.Principal
	var err error
	switch {
	case strings.HasPrefix(tok, auth.GatewayKeyPrefix):
		return g.Resolver.FromKey(ctx, tok, "gateway")
	case strings.HasPrefix(tok, auth.PATPrefix):
		return nil, util.Err(401, "authentication_error", "personal access tokens are for the admin API; create a gateway key for inference")
	case strings.Count(tok, ".") == 2:
		p, err = g.Resolver.FromIdPToken(ctx, tok, snap.IdPs)
	case tok == "" && r.Header.Get("X-Theophylax-Client") == "web":
		p, err = g.Resolver.FromSession(ctx, r) // console playground
	case len(tok) >= 16:
		// Keys imported from LiteLLM (sk-…) keep their original format.
		return g.Resolver.FromKey(ctx, tok, "gateway")
	default:
		return nil, util.Err(401, "authentication_error", "missing or invalid API key (send x-api-key or Authorization: Bearer)")
	}
	if err != nil {
		return nil, err
	}
	// User principals choose a project explicitly.
	pid := r.Header.Get("X-Theophylax-Project")
	if pid == "" {
		return nil, util.Err(400, "invalid_request_error", "X-Theophylax-Project header is required when authenticating as a user")
	}
	proj := snap.Projects[pid]
	if proj == nil {
		return nil, util.Err(404, "not_found_error", "project not found")
	}
	if !p.Can(auth.Scope{Type: "project", ID: proj.ID, OrgID: proj.OrgID, TeamID: proj.TeamID}, "member") {
		return nil, util.Err(403, "permission_error", "you are not a member of this project")
	}
	p.OrgID, p.TeamID, p.ProjectID = proj.OrgID, proj.TeamID, proj.ID
	return p, nil
}

// ---------------------------------------------------------------------------
// Errors in each client's wire format

func writeErr(w http.ResponseWriter, f providers.Format, err error) int {
	var he *util.HTTPError
	if !errors.As(err, &he) {
		he = util.Err(500, "api_error", err.Error())
	}
	if f == providers.FormatOpenAI {
		util.WriteJSON(w, he.Status, map[string]any{"error": map[string]any{"message": he.Msg, "type": he.Type, "code": he.Type}})
	} else {
		util.WriteJSON(w, he.Status, map[string]any{"type": "error", "error": he})
	}
	return he.Status
}

func upstreamHTTPError(ue *providers.UpstreamError) *util.HTTPError {
	typ := "api_error"
	switch ue.Status {
	case 400, 422:
		typ = "invalid_request_error"
	case 401, 403:
		typ = "api_error" // never surface upstream credential failures as client auth errors
	case 404:
		typ = "not_found_error"
	case 413:
		typ = "request_too_large"
	case 429:
		typ = "rate_limit_error"
	case 529, 503:
		typ = "overloaded_error"
	}
	status := ue.Status
	if status == 401 || status == 403 {
		status = 502
	}
	return util.Err(status, typ, "upstream: "+ue.Message())
}

// ---------------------------------------------------------------------------
// Inference

type reqState struct {
	start        time.Time
	log          store.RequestLog
	scopes       []ScopeRef
	gr           []guardrails.Finding
	reqPL        []byte
	respPL       []byte
	capture      bool
	includeUsage bool
	// Proof-of-Inference
	p          *auth.Principal
	hw         *hashWriter
	reqDigest  string
	canonical  string
	policies   []guardrails.Policy
	sseStarted bool
	restorer   *guardrails.Restorer
	canaries   []string
	tainted    bool
	// model rollouts
	plan       *rolloutPlan
	shadowText strings.Builder
	// W3C trace context from the client, carried into exported spans
	traceparent string
	// respModel is the model name the upstream reported serving, used to
	// price deployments whose configured ID is an alias (e.g. Azure deployment names).
	respModel string
	// response cache
	cacheHit bool
	// router records a complexity-routing decision for the receipt.
	router *receipts.RouterDecision
}

// hashWriter hashes every response body byte so receipts attest to exactly what the client received.
type hashWriter struct {
	http.ResponseWriter
	h hash.Hash
}

func (w *hashWriter) Write(b []byte) (int, error) {
	w.h.Write(b)
	return w.ResponseWriter.Write(b)
}

func (w *hashWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *Gateway) inference(w http.ResponseWriter, r *http.Request, cf providers.Format, agentRef string) {
	st := &reqState{start: time.Now(), traceparent: r.Header.Get("traceparent")}
	reqID := util.NewID("req")
	w.Header().Set("X-Request-Id", reqID)
	st.log = store.RequestLog{ID: reqID, TS: st.start.UTC(), Endpoint: r.URL.Path, Status: 200, Attempts: 0,
		ClientIP: util.ClientIP(r, g.Cfg.TrustedProxies), UserAgent: truncate(r.UserAgent(), 200)}
	clientOpenAI := cf == providers.FormatOpenAI

	br := bridgeFrom(r.Context())
	p, err := g.authenticate(r)
	if err != nil {
		ef := cf
		if br != nil {
			ef = providers.FormatOpenAI
		}
		code := writeErr(w, ef, err)
		metrics.Requests.Inc("", "", fmt.Sprint(code))
		return
	}
	snap := g.Loader.Snapshot()
	st.p = p
	st.hw = &hashWriter{ResponseWriter: w, h: sha256.New()}
	w = st.hw
	if br != nil {
		w = newResponsesWriter(st.hw, br)
	}
	w.Header().Set("X-Theophylax-Receipt", reqID)
	st.log.OrgID, st.log.TeamID, st.log.ProjectID = strPtr(p.OrgID), strPtr(p.TeamID), strPtr(p.ProjectID)
	setTags(w, r, p, st)
	if p.Key != nil {
		st.log.KeyID = &p.Key.ID
	}
	if uid := p.UserID(); uid != "" {
		st.log.UserID = &uid
	}
	finish := func(status int, errMsg string) {
		st.log.Status = status
		st.log.Error = truncate(errMsg, 1000)
		g.finish(st)
	}
	fail := func(err error) {
		code := writeErr(w, cf, err)
		finish(code, err.Error())
	}

	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, g.Cfg.MaxBodyBytes))
	if err != nil {
		fail(util.Err(413, "request_too_large", "request body too large or unreadable"))
		return
	}
	rd := sha256.Sum256(raw)
	st.reqDigest = hex.EncodeToString(rd[:])
	if br != nil {
		st.reqDigest = br.reqDigest // receipts attest to the client's Responses request
	}
	body := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		fail(util.Err(400, "invalid_request_error", "invalid JSON body: "+err.Error()))
		return
	}
	if so, ok := body["stream_options"].(map[string]any); ok && so["include_usage"] == true {
		st.includeUsage = true
	}

	// Managed agents overlay system prompt, skills, tools and model.
	var agent *store.Agent
	if agentRef != "" || p.AgentID != "" {
		ref := agentRef
		if ref == "" {
			ref = p.AgentID
		}
		agent = snap.Agents[ref]
		if agent == nil {
			agent = snap.Agents[p.OrgID+"/"+ref]
		}
		if agent == nil || agent.OrgID != p.OrgID {
			fail(util.Err(404, "not_found_error", "agent not found"))
			return
		}
		if agent.Status != "active" {
			fail(util.Err(403, "permission_error", "agent is disabled"))
			return
		}
		if p.AgentID != "" && p.AgentID != agent.ID {
			fail(util.Err(403, "permission_error", "this key is bound to a different agent"))
			return
		}
		st.log.AgentID = &agent.ID
		g.applyAgent(snap, agent, body, clientOpenAI)
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
	if route.Router != nil {
		// A router model: score the request and continue with a tier model
		// this caller may use (see checkModelAllowed for how allow-lists
		// that name the router are read).
		score, cls := g.classify(r.Context(), route.Router, body, clientOpenAI)
		chosen, _ := route.Router.Pick(score)
		// Prefer the chosen tier; if this caller may not use it, the
		// nearest allowed tier below it, then above it.
		order := []int{chosen}
		for i := chosen - 1; i >= 0; i-- {
			order = append(order, i)
		}
		for i := chosen + 1; i < len(route.Router.Tiers); i++ {
			order = append(order, i)
		}
		var tr *Route
		var firstErr error
		tier := -1
		for _, i := range order {
			name := route.Router.Tiers[i].Model
			cand := snap.Resolve(name)
			if cand == nil || cand.Router != nil {
				if firstErr == nil {
					firstErr = util.Err(503, "overloaded_error", fmt.Sprintf("router %s: tier model %q is not available", route.Model.Name, name))
				}
				continue
			}
			if err := g.checkModelAllowed(snap, p, name, cand.Model.Name, route); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			tr, tier = cand, i
			break
		}
		if tr == nil {
			fail(firstErr)
			return
		}
		st.router = &receipts.RouterDecision{Model: route.Model.Name, Score: score, Classifier: cls, Tier: tier + 1}
		setRouteHeader(w.Header(), score, cls, tr.Model.Name)
		metrics.Routed.Inc(route.Model.Name, tr.Model.Name)
		route, model = tr, tr.Model.Name
		st.canonical, st.log.Model = model, model
	}

	st.scopes = []ScopeRef{{"org", p.OrgID}}
	if p.TeamID != "" {
		st.scopes = append(st.scopes, ScopeRef{"team", p.TeamID})
	}
	if p.ProjectID != "" {
		st.scopes = append(st.scopes, ScopeRef{"project", p.ProjectID})
	}
	if p.Key != nil {
		st.scopes = append(st.scopes, ScopeRef{"key", p.Key.ID})
	}
	if uid := p.UserID(); uid != "" {
		st.scopes = append(st.scopes, ScopeRef{"user", uid})
		st.scopes = append(st.scopes, PersonScopes(p.OrgID, p.TeamID, uid)...)
	}
	if err := g.checkLimits(w, snap, p, st.scopes); err != nil {
		fail(err)
		return
	}

	// Input guardrails (may redact in place or cap max_tokens).
	policies := snap.PoliciesFor(p.OrgID, p.TeamID, p.ProjectID)
	st.policies = policies
	if len(policies) > 0 {
		res := g.Guard.Evaluate(r.Context(), policies, guardrails.Input{Phase: "input", Model: route.Model.Name, Body: body, Texts: guardrails.RequestTexts(body, clientOpenAI), OpenAI: clientOpenAI,
			ScopeKey: p.OrgID + "/" + p.ProjectID, Metadata: map[string]string{"org_id": p.OrgID, "team_id": p.TeamID, "project_id": p.ProjectID, "request_id": reqID}})
		st.gr = append(st.gr, res.Findings...)
		if res.Blocked {
			w.Header().Set("X-Theophylax-Guardrail", "blocked")
			fail(util.Err(400, "invalid_request_error", res.Message))
			return
		}
		st.restorer = guardrails.NewRestorer(res.Restore, res.RestoreAllow, res.Tainted)
		st.canaries, st.tainted = res.Canaries, res.Tainted
	}
	st.capture = g.capturePayloads(snap, p)
	if st.capture {
		st.reqPL, _ = json.Marshal(body)
	}

	// Try deployments in order; fail over on retryable errors before any byte is sent.
	deps := route.Order(p.OrgID)
	st.plan = planRollout(snap, route.Model.ID)
	if st.plan != nil && st.plan.canary {
		deps = append([]*Dep{st.plan.ro.dep}, deps...)
	}
	if len(deps) == 0 {
		fail(util.Err(503, "overloaded_error", "no healthy deployment is configured for "+route.Model.Name))
		return
	}
	metrics.Overhead.Observe(time.Since(st.start).Seconds())
	var resp *http.Response
	var used *Dep
	var lastErr error
	clientHdr := http.Header{}
	for _, h := range []string{"anthropic-version", "anthropic-beta"} {
		for _, v := range r.Header.Values(h) {
			clientHdr.Add(h, v)
		}
	}
	// Response cache: off unless the scope sets cache_ttl_seconds; the client
	// can opt out per request.
	cacheFor, cacheMode := time.Duration(0), strings.ToLower(r.Header.Get(CacheHeader))
	if g.Cache != nil && cacheMode != "off" {
		cacheFor = cacheTTL(snap, p)
	}
	var capture *captureBody
	var captureKey []byte
	var captureCT string
	for i, d := range deps {
		if i >= 4 {
			break
		}
		st.log.Attempts++
		prov := d.Provider.P
		upstream := d.Upstream(model)
		nb := body
		if prov.Format() != cf {
			if clientOpenAI {
				nb = providers.OpenAIToAnthropicRequest(body)
			} else {
				nb = providers.AnthropicToOpenAIRequest(body)
			}
		}
		var hdr http.Header
		nb, hdr = applyMCP(nb, clientHdr, prov.Kind())
		var ckey []byte
		if cacheFor > 0 && !strings.HasPrefix(d.ID, "rollout:") {
			ckey = cacheKey(p, d.ProviderID, upstream, stream, nb, hdr)
			if cacheMode != "refresh" {
				if hit := g.Cache.Get(r.Context(), ckey); hit != nil {
					metrics.Cache.Inc("hit")
					resp, used, st.cacheHit = hit, d, true
					st.log.TTFBMS = int(time.Since(st.start).Milliseconds())
					st.log.UpstreamModel = upstream
					st.log.ProviderID = &d.ProviderID
					st.log.ProviderKind = prov.Kind()
					break
				}
				metrics.Cache.Inc("miss")
			}
		}
		t0 := time.Now()
		resp, lastErr = prov.Send(r.Context(), &providers.Request{Body: nb, Stream: stream, UpstreamModel: upstream, Header: hdr})
		if lastErr == nil {
			used = d
			d.health.success(time.Since(t0))
			st.log.TTFBMS = int(time.Since(st.start).Milliseconds())
			metrics.TTFB.Observe(time.Since(t0).Seconds(), route.Model.Name)
			st.log.UpstreamModel = upstream
			st.log.ProviderID = &d.ProviderID
			st.log.ProviderKind = prov.Kind()
			if ckey != nil {
				capture = &captureBody{ReadCloser: resp.Body}
				resp.Body, captureKey, captureCT = capture, ckey, resp.Header.Get("Content-Type")
			}
			break
		}
		if r.Context().Err() != nil {
			finish(499, "client closed request")
			return
		}
		var ue *providers.UpstreamError
		status := 502
		if errors.As(lastErr, &ue) {
			status = ue.Status
		}
		metrics.UpstreamErrors.Inc(prov.Kind(), fmt.Sprint(status))
		slog.Warn("upstream attempt failed", "request_id", reqID, "provider", d.Provider.Row.Name, "model", upstream, "status", status, "err", truncate(lastErr.Error(), 300))
		if strings.HasPrefix(d.ID, "rollout:") {
			go g.recordCanary(strings.TrimPrefix(d.ID, "rollout:"), status, time.Since(t0))
		}
		if ue != nil && !ue.Retryable() {
			fail(upstreamHTTPError(ue))
			return
		}
		d.health.fail()
	}
	if resp == nil {
		var ue *providers.UpstreamError
		if errors.As(lastErr, &ue) {
			fail(upstreamHTTPError(ue))
		} else {
			fail(util.Err(502, "api_error", "all upstream deployments failed: "+truncate(fmt.Sprint(lastErr), 300)))
		}
		return
	}
	defer resp.Body.Close()
	pf := used.Provider.P.Format()
	w.Header().Set("X-Theophylax-Provider", used.Provider.Row.Name)
	w.Header().Set("X-Theophylax-Model", route.Model.Name)
	if cacheFor > 0 {
		state := "miss"
		if st.cacheHit {
			state = "hit"
		} else if cacheMode == "refresh" {
			state = "refresh"
		}
		w.Header().Set(CacheHeader, state)
	}

	var usage providers.Usage
	var status int
	var errMsg string
	if stream {
		usage, status, errMsg = g.streamResponse(w, r, resp, cf, pf, model, policies, st)
	} else {
		usage, status, errMsg = g.unaryResponse(w, r, resp, cf, pf, model, route.Model.Name, policies, st)
	}
	st.log.InputTokens, st.log.OutputTokens = usage.Input, usage.Output
	st.log.CacheReadTokens, st.log.CacheWriteTokens = usage.CacheRead, usage.CacheWrite
	st.log.CostUSD, st.log.PriceKey = g.Pricing.Cost(st.log.ProviderKind, usage, st.log.UpstreamModel, st.respModel)
	if st.cacheHit {
		// Served from cache: no provider call, so nothing is billed.
		st.log.CostUSD, st.log.PriceKey = 0, "cache"
	}
	if capture != nil && status == 200 {
		// Stream readers may stop at the final event; read any remainder so a
		// complete response is recognised.
		_, _ = io.Copy(io.Discard, io.LimitReader(capture, maxCacheEntry))
		if capture.complete() {
			body := append([]byte(nil), capture.buf.Bytes()...)
			org, proj := p.OrgID, p.ProjectID
			go g.Cache.Put(context.Background(), captureKey, org, proj, captureCT, body, cacheFor)
		}
	}
	finish(status, errMsg)
	if strings.HasPrefix(used.ID, "rollout:") {
		go g.recordCanary(strings.TrimPrefix(used.ID, "rollout:"), status, time.Since(st.start))
	} else if st.plan != nil && st.plan.shadow && status == 200 {
		go g.runShadow(&shadowJob{ro: st.plan.ro, requestID: reqID, clientFmt: cf, body: body, header: clientHdr, model: route.Model,
			incText: st.shadowText.String(), incLatency: time.Since(st.start), incUsage: usage, incCost: st.log.CostUSD})
	}
}

func (g *Gateway) unaryResponse(w http.ResponseWriter, r *http.Request, resp *http.Response, cf, pf providers.Format, requested, canonical string, policies []guardrails.Policy, st *reqState) (providers.Usage, int, string) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		code := writeErr(w, cf, util.Err(502, "api_error", "reading upstream response: "+err.Error()))
		return providers.Usage{}, code, err.Error()
	}
	var usage providers.Usage
	var served struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(raw, &served)
	st.respModel = served.Model
	out := raw
	switch {
	case pf == cf && cf == providers.FormatAnthropic:
		var m struct {
			Usage map[string]any `json:"usage"`
		}
		_ = json.Unmarshal(raw, &m)
		usage = providers.AnthropicUsage(m.Usage)
	case pf == cf:
		var m struct {
			Usage map[string]any `json:"usage"`
		}
		_ = json.Unmarshal(raw, &m)
		usage = providers.OpenAIUsage(m.Usage)
	case cf == providers.FormatAnthropic:
		out, usage, err = providers.OpenAIToAnthropicResponse(raw, requested)
	default:
		out, usage, err = providers.AnthropicToOpenAIResponse(raw, requested)
	}
	if err != nil {
		code := writeErr(w, cf, util.Err(502, "api_error", "translating upstream response: "+err.Error()))
		return usage, code, err.Error()
	}
	if hasOutputRules(policies) {
		var m map[string]any
		dec := json.NewDecoder(bytes.NewReader(out))
		dec.UseNumber()
		if dec.Decode(&m) == nil {
			res := g.Guard.Evaluate(r.Context(), policies, guardrails.Input{Phase: "output", Model: canonical, Body: m, Texts: guardrails.ResponseTexts(m, cf == providers.FormatOpenAI), OpenAI: cf == providers.FormatOpenAI,
				Canaries: st.canaries, Tainted: st.tainted})
			st.gr = append(st.gr, res.Findings...)
			if res.Blocked {
				w.Header().Set("X-Theophylax-Guardrail", "blocked")
				code := writeErr(w, cf, util.Err(400, "invalid_request_error", "response withheld: "+res.Message))
				return usage, code, res.Message
			}
			for _, f := range res.Findings {
				if f.Action == "redact" {
					out, _ = json.Marshal(m)
					break
				}
			}
		}
	}
	if st.plan != nil && st.plan.shadow {
		var m map[string]any
		if json.Unmarshal(out, &m) == nil {
			for _, t := range guardrails.ResponseTexts(m, cf == providers.FormatOpenAI) {
				if t.Role == "assistant" {
					st.shadowText.WriteString(t.Get())
				}
			}
		}
	}
	if st.capture {
		st.respPL = out // captured before restoration: stored payloads never hold restored secrets
	}
	if st.restorer != nil {
		var m any
		dec := json.NewDecoder(bytes.NewReader(out))
		dec.UseNumber()
		if dec.Decode(&m) == nil {
			if b, err := json.Marshal(st.restorer.Walk(m)); err == nil {
				out = b
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(out)
	return usage, resp.StatusCode, ""
}

func hasOutputRules(ps []guardrails.Policy) bool {
	for _, p := range ps {
		for _, r := range p.Rules {
			if r.ApplyTo == "" || r.ApplyTo == "both" || r.ApplyTo == "output" {
				switch r.Type {
				case "pii", "secrets", "regex", "keywords", "webhook", "bedrock_guardrail", "lakera", "azure_content_safety", "openai_moderation":
					return true
				}
			}
		}
	}
	return false
}

type flushWriter struct {
	w  http.ResponseWriter
	fl http.Flusher
}

func (f flushWriter) emit(ev providers.SSEEvent) error {
	if err := providers.WriteSSE(f.w, ev); err != nil {
		return err
	}
	if f.fl != nil {
		f.fl.Flush()
	}
	return nil
}

func (g *Gateway) streamResponse(w http.ResponseWriter, r *http.Request, resp *http.Response, cf, pf providers.Format, requested string, policies []guardrails.Policy, st *reqState) (providers.Usage, int, string) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	st.sseStarted = true
	fl, _ := w.(http.Flusher)
	fw := flushWriter{w: w, fl: fl}
	if fl != nil {
		fl.Flush()
	}

	guard := g.Guard.NewStreamGuard(policies, st.canaries, st.tainted)
	var text strings.Builder
	wantText := guard != nil || st.capture || (st.plan != nil && st.plan.shadow)
	var usage providers.Usage
	var a2o *providers.AnthropicToOpenAIStream
	var o2a *providers.OpenAIToAnthropicStream
	includeUsage := st.includeUsage
	if cf == providers.FormatOpenAI && pf == providers.FormatAnthropic {
		a2o = &providers.AnthropicToOpenAIStream{Model: requested, IncludeUsage: includeUsage}
	}
	if cf == providers.FormatAnthropic && pf == providers.FormatOpenAI {
		o2a = &providers.OpenAIToAnthropicStream{Model: requested}
	}
	reader := providers.NewSSEReader(resp.Body)
	sr := newStreamRestorer(st.restorer)
	errMsg := ""
	status := 200
	terminate := func(policy string) {
		msg := fmt.Sprintf("response stopped by guardrail policy %q", policy)
		errMsg, status = msg, 400
		if cf == providers.FormatOpenAI {
			_ = fw.emit(providers.SSEEvent{Data: mustJSON(map[string]any{"error": map[string]any{"message": msg, "type": "guardrail_violation"}})})
			_ = fw.emit(providers.SSEEvent{Data: []byte("[DONE]")})
		} else {
			_ = fw.emit(providers.SSEEvent{Event: "error", Data: mustJSON(map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "message": msg}})})
		}
	}
	for {
		ev, err := reader.Next()
		if err != nil {
			if err != io.EOF && r.Context().Err() == nil {
				errMsg, status = "upstream stream error: "+err.Error(), 502
			}
			if r.Context().Err() != nil {
				errMsg, status = "client disconnected", 499
			}
			break
		}
		var outEvents []providers.SSEEvent
		var delta string
		switch {
		case pf == providers.FormatAnthropic:
			// Track usage and text from native Anthropic events.
			switch ev.Event {
			case "message_start", "message_delta":
				var m struct {
					Message struct {
						Model string         `json:"model"`
						Usage map[string]any `json:"usage"`
					} `json:"message"`
					Usage map[string]any `json:"usage"`
				}
				_ = json.Unmarshal(ev.Data, &m)
				if ev.Event == "message_start" {
					usage = providers.AnthropicUsage(m.Message.Usage)
					st.respModel = m.Message.Model
				} else if m.Usage != nil {
					u := providers.AnthropicUsage(m.Usage)
					usage.Output = u.Output
					if u.Input > 0 {
						usage.Input = u.Input
					}
					if u.CacheRead > 0 {
						usage.CacheRead = u.CacheRead
					}
					if u.CacheWrite > 0 {
						usage.CacheWrite, usage.CacheWrite1h = u.CacheWrite, u.CacheWrite1h
					}
					if u.Speed != "" {
						usage.Speed = u.Speed
					}
				}
			case "content_block_delta":
				if wantText {
					var m struct {
						Delta struct {
							Text        string `json:"text"`
							PartialJSON string `json:"partial_json"`
						} `json:"delta"`
					}
					_ = json.Unmarshal(ev.Data, &m)
					delta = m.Delta.Text
					if delta == "" && guard != nil {
						delta = m.Delta.PartialJSON // tool inputs are checked by the tool-call guard
					}
				}
			case "error":
				errMsg, status = "upstream stream error: "+truncate(string(ev.Data), 300), 502
			}
			native := []providers.SSEEvent{*ev}
			if sr != nil {
				native = sr.anthropic(ev)
			}
			for i := range native {
				if a2o != nil {
					outEvents = append(outEvents, a2o.Feed(&native[i])...)
				} else {
					outEvents = append(outEvents, native[i])
				}
			}
		default: // OpenAI upstream
			if bytes.Contains(ev.Data, []byte(`"usage"`)) || (st.respModel == "" && bytes.Contains(ev.Data, []byte(`"model"`))) {
				var m struct {
					Model string         `json:"model"`
					Usage map[string]any `json:"usage"`
				}
				if json.Unmarshal(ev.Data, &m) == nil {
					if m.Usage != nil {
						usage = providers.OpenAIUsage(m.Usage)
					}
					if st.respModel == "" {
						st.respModel = m.Model
					}
				}
			}
			if wantText && len(ev.Data) > 0 && ev.Data[0] == '{' {
				var m struct {
					Choices []struct {
						Delta struct {
							Content   string `json:"content"`
							ToolCalls []struct {
								Function struct {
									Arguments string `json:"arguments"`
								} `json:"function"`
							} `json:"tool_calls"`
						} `json:"delta"`
					} `json:"choices"`
				}
				if json.Unmarshal(ev.Data, &m) == nil && len(m.Choices) > 0 {
					delta = m.Choices[0].Delta.Content
					for _, tc := range m.Choices[0].Delta.ToolCalls {
						delta += tc.Function.Arguments
					}
				}
			}
			if sr != nil {
				re := sr.openai(ev)
				ev = &re
			}
			if o2a != nil {
				outEvents = o2a.Feed(ev)
			} else {
				if string(ev.Data) == "[DONE]" {
					outEvents = nil // re-emitted at the end
				} else {
					outEvents = []providers.SSEEvent{*ev}
				}
			}
		}
		if delta != "" {
			if st.capture && text.Len() < 256<<10 {
				text.WriteString(delta)
			}
			if st.plan != nil && st.plan.shadow && st.shadowText.Len() < 64<<10 {
				st.shadowText.WriteString(delta)
			}
			if guard != nil {
				if pol := guard.Feed(delta); pol != "" {
					st.gr = append(st.gr, guard.Findings...)
					terminate(pol)
					break
				}
			}
		}
		for _, oe := range outEvents {
			if err := fw.emit(oe); err != nil {
				errMsg, status = "client disconnected", 499
				goto done
			}
		}
	}
	if status != 400 {
		var tail []providers.SSEEvent
		if a2o != nil {
			tail = a2o.Finish()
			usage = a2o.Usage
		} else if o2a != nil {
			tail = o2a.Finish()
			usage = o2a.Usage
		} else if pf == providers.FormatOpenAI {
			tail = []providers.SSEEvent{{Data: []byte("[DONE]")}}
		}
		for _, t := range tail {
			_ = fw.emit(t)
		}
	}
done:
	if st.capture {
		st.respPL = mustJSON(map[string]any{"streamed_text": text.String()})
	}
	return usage, status, errMsg
}

// ---------------------------------------------------------------------------

// router, when set, is the complexity router the model was reached through.
// A list that admits the router but names none of its tier models leaves
// the choice to the router; a list that names tier models admits only those.
func (g *Gateway) checkModelAllowed(snap *Snapshot, p *auth.Principal, requested, canonical string, router ...*Route) error {
	matches := func(list []string, name string) bool {
		for _, pat := range list {
			if util.MatchModel(pat, name) {
				return true
			}
		}
		return false
	}
	check := func(list []string, where string) error {
		if len(list) == 0 || matches(list, requested) || matches(list, canonical) {
			return nil
		}
		if len(router) > 0 && router[0] != nil && router[0].Router != nil {
			rt := router[0]
			names := append([]string{rt.Model.Name}, rt.Model.Aliases...)
			admitsRouter := false
			for _, n := range names {
				admitsRouter = admitsRouter || matches(list, n)
			}
			namesTier := false
			for _, t := range rt.Router.Tiers {
				namesTier = namesTier || matches(list, t.Model)
			}
			if admitsRouter && !namesTier {
				return nil
			}
		}
		return util.Err(403, "permission_error", fmt.Sprintf("model %q is not allowed for this %s", requested, where))
	}
	if p.Key != nil {
		if err := check(p.Key.AllowedModels, "API key"); err != nil {
			return err
		}
	}
	if o := snap.Orgs[p.OrgID]; o != nil {
		if err := check(o.Settings.AllowedModels, "organization"); err != nil {
			return err
		}
	}
	if t := snap.Teams[p.TeamID]; t != nil {
		if err := check(t.Settings.AllowedModels, "team"); err != nil {
			return err
		}
	}
	if pr := snap.Projects[p.ProjectID]; pr != nil {
		if err := check(pr.Settings.AllowedModels, "project"); err != nil {
			return err
		}
	}
	return nil
}

func (g *Gateway) checkLimits(w http.ResponseWriter, snap *Snapshot, p *auth.Principal, scopes []ScopeRef) error {
	type lim struct {
		key      string
		rpm, tpm int
		what     string
	}
	var ls []lim
	if p.Key != nil {
		ls = append(ls, lim{"key:" + p.Key.ID, p.Key.RPM, p.Key.TPM, "API key"})
	}
	if pr := snap.Projects[p.ProjectID]; pr != nil {
		ls = append(ls, lim{"project:" + pr.ID, pr.Settings.RPM, pr.Settings.TPM, "project"})
	}
	if t := snap.Teams[p.TeamID]; t != nil {
		ls = append(ls, lim{"team:" + t.ID, t.Settings.RPM, t.Settings.TPM, "team"})
	}
	if o := snap.Orgs[p.OrgID]; o != nil {
		ls = append(ls, lim{"org:" + o.ID, o.Settings.RPM, o.Settings.TPM, "organization"})
	}
	limits := make([]Limit, len(ls))
	whatOf := make(map[string]string, len(ls))
	for i, l := range ls {
		limits[i] = Limit{Key: l.key, RPM: l.rpm, TPM: l.tpm}
		whatOf[l.key] = l.what
	}
	// Enforce all scopes atomically: a request rejected by one scope must not
	// consume a count from the scopes that would have allowed it.
	if ok, retry, why, key := g.RL.AllowAll(limits); !ok {
		w.Header().Set("Retry-After", fmt.Sprint(retry))
		return util.Err(429, "rate_limit_error", fmt.Sprintf("%s rate limit exceeded (%s); retry in %ds", whatOf[key], why, retry))
	}
	if b := g.Budgets.Exceeded(snap, p.OrgID, p.TeamID, p.UserID(), scopes); b != nil {
		return util.Err(429, "rate_limit_error", Describe(b)+" exhausted")
	}
	return nil
}

func (g *Gateway) capturePayloads(snap *Snapshot, p *auth.Principal) bool {
	for _, s := range []*ScopeInfo{snap.Projects[p.ProjectID], snap.Teams[p.TeamID], snap.Orgs[p.OrgID]} {
		if s != nil && s.Settings.LogPayloads != nil {
			return *s.Settings.LogPayloads
		}
	}
	return false
}

func (g *Gateway) finish(st *reqState) {
	st.log.LatencyMS = int(time.Since(st.start).Milliseconds())
	if n := st.restorer.Withheld(); n > 0 {
		st.gr = append(st.gr, guardrails.Finding{Policy: "Secret Shield", Rule: "secrets", Action: "restore_withheld", Count: n, Phase: "output",
			Detail: "placeholders left in place: inside a link to a host not in allow_domains, or in a tool call after injection signals"})
	}
	if len(st.gr) > 0 {
		st.log.Guardrails, _ = json.Marshal(st.gr)
		for _, f := range st.gr {
			metrics.Guardrail.Inc(f.Rule, f.Action)
			if f.Action == "block" && g.OnBlock != nil {
				g.OnBlock(util.Deref(st.log.OrgID), f.Policy, f.Rule, f.Detail)
			}
		}
	}
	prov := st.log.ProviderKind
	metrics.Requests.Inc(st.log.Model, prov, fmt.Sprint(st.log.Status))
	metrics.Latency.Observe(time.Since(st.start).Seconds(), st.log.Model)
	if st.log.InputTokens+st.log.OutputTokens > 0 {
		metrics.Tokens.Add(float64(st.log.InputTokens), st.log.Model, "input")
		metrics.Tokens.Add(float64(st.log.OutputTokens), st.log.Model, "output")
		metrics.Tokens.Add(float64(st.log.CacheReadTokens), st.log.Model, "cache_read")
		metrics.Tokens.Add(float64(st.log.CacheWriteTokens), st.log.Model, "cache_write")
		metrics.Cost.Add(st.log.CostUSD, st.log.Model)
	}
	tok := st.log.InputTokens + st.log.OutputTokens + st.log.CacheReadTokens + st.log.CacheWriteTokens
	if tok > 0 && !st.cacheHit { // cache hits use no provider tokens
		for _, sc := range st.scopes {
			if sc.Type != "user" && sc.Type != "org_user" && sc.Type != "team_user" {
				g.RL.AddTokens(sc.Type+":"+sc.ID, tok)
			}
		}
		g.Budgets.AddPending(st.scopes, st.log.CostUSD, int64(tok), st.log.TS)
	}
	if st.log.Guardrails == nil {
		st.log.Guardrails = json.RawMessage("[]")
	}
	le := &LogEntry{Log: st.log, Scopes: st.scopes, Request: st.reqPL, Response: st.respPL}
	if g.Signer != nil && st.p != nil && st.hw != nil {
		rc := g.buildReceipt(st)
		le.ReceiptJWS, le.ReceiptHash = g.Signer.Sign(rc)
		le.ReceiptKID = g.Signer.KID
		// Streams carry the receipt inline as an SSE comment (ignored by SSE clients).
		if st.sseStarted && st.log.Status != 499 {
			_, _ = st.hw.ResponseWriter.Write([]byte(": theophylax-receipt " + le.ReceiptJWS + "\n\n"))
			st.hw.Flush()
		}
	}
	g.Logs.Enqueue(le)
	if g.Export != nil {
		g.Export.Request(export.Request{Log: le.Log, TraceParent: st.traceparent, ReceiptHash: hex.EncodeToString(le.ReceiptHash)})
	}
}

func (g *Gateway) buildReceipt(st *reqState) *receipts.Receipt {
	p := st.p
	rc := &receipts.Receipt{Version: 1, ID: st.log.ID, Issuer: g.Cfg.PublicURL, IssuedAt: time.Now().Unix(), OrgID: p.OrgID, TeamID: p.TeamID, ProjectID: p.ProjectID,
		AgentID: util.Deref(st.log.AgentID), Endpoint: st.log.Endpoint, Requested: st.log.Model, ReqSHA256: st.reqDigest,
		RespSHA256: hex.EncodeToString(st.hw.h.Sum(nil)), Stream: st.log.Stream, Status: st.log.Status, CostUSD: st.log.CostUSD,
		Route:    receipts.Route{Model: st.canonical, ProviderID: util.Deref(st.log.ProviderID), ProviderKind: st.log.ProviderKind, UpstreamModel: st.log.UpstreamModel, Attempts: st.log.Attempts, Cache: map[bool]string{true: "hit"}[st.cacheHit], Router: st.router},
		Usage:    receipts.Usage{Input: st.log.InputTokens, Output: st.log.OutputTokens, CacheRead: st.log.CacheReadTokens, CacheWrite: st.log.CacheWriteTokens},
		Policies: []receipts.PolicyRef{}, Actions: []receipts.Action{}}
	if p.Key != nil {
		rc.Principal = receipts.Principal{Type: "key", ID: p.Key.ID}
	} else {
		rc.Principal = receipts.Principal{Type: "user", ID: p.UserID()}
	}
	for _, pol := range st.policies {
		rc.Policies = append(rc.Policies, receipts.PolicyRef{ID: pol.ID, Name: pol.Name, Scope: pol.ScopeType, Mode: pol.Mode, Digest: pol.Digest})
	}
	// Findings are recorded without matched content so receipts stay shareable.
	for _, f := range st.gr {
		rc.Actions = append(rc.Actions, receipts.Action{Policy: f.Policy, Rule: f.Rule, Action: f.Action, Phase: f.Phase, Count: f.Count})
	}
	return rc
}

// Judge runs a guardrail classification through the gateway's own routing
// (no guardrails, no budgets, not logged as user traffic).
func (g *Gateway) Judge(ctx context.Context, model, system, user string) (string, error) {
	snap := g.Loader.Snapshot()
	route := snap.Resolve(model)
	if route == nil {
		return "", fmt.Errorf("judge model %q is not in the catalog", model)
	}
	body := map[string]any{"max_tokens": 200, "temperature": 0, "system": system, "messages": []any{map[string]any{"role": "user", "content": user}}}
	var lastErr error
	for _, d := range route.Order("") {
		nb := body
		if d.Provider.P.Format() == providers.FormatOpenAI {
			nb = providers.AnthropicToOpenAIRequest(body)
		}
		resp, err := d.Provider.P.Send(ctx, &providers.Request{Body: nb, UpstreamModel: d.Upstream(model), Header: http.Header{}})
		if err != nil {
			lastErr = err
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			return "", err
		}
		if d.Provider.P.Format() == providers.FormatOpenAI {
			raw, _, _ = providers.OpenAIToAnthropicResponse(raw, model)
		}
		var m struct {
			Content []struct {
				Type, Text string
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			return "", err
		}
		var sb strings.Builder
		for _, c := range m.Content {
			sb.WriteString(c.Text)
		}
		return sb.String(), nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no deployment available for judge model")
	}
	return "", lastErr
}

// ---------------------------------------------------------------------------
// Agents

func (g *Gateway) applyAgent(snap *Snapshot, a *store.Agent, body map[string]any, openai bool) {
	var sys strings.Builder
	sys.WriteString(a.SystemPrompt)
	var skills []string
	var mcp []any
	for _, id := range append(append([]string{}, a.Skills...), a.MCPServers...) {
		e := snap.Registry[id]
		if e == nil {
			continue
		}
		switch e.Item.Kind {
		case "skill", "prompt":
			md, _ := e.Content["skill_md"].(string)
			if md == "" {
				md, _ = e.Content["template"].(string)
			}
			skills = append(skills, fmt.Sprintf("<skill name=%q version=%q>\n%s\n</skill>", e.Item.Slug, e.Version, md))
		case "mcp_server":
			srv := map[string]any{"type": "url", "name": e.Item.Slug}
			if u, _ := e.Content["url"].(string); u != "" {
				srv["url"] = u
			}
			if t, _ := e.Content["authorization_token"].(string); t != "" {
				srv["authorization_token"] = t
			}
			mcp = append(mcp, srv)
		}
	}
	if len(skills) > 0 {
		if sys.Len() > 0 {
			sys.WriteString("\n\n")
		}
		sys.WriteString("You have the following skills available. Apply them when relevant.\n<skills>\n" + strings.Join(skills, "\n") + "\n</skills>")
	}
	agentSys := sys.String()
	if openai {
		msgs, _ := body["messages"].([]any)
		if !a.AllowClientSystem {
			var kept []any
			for _, m := range msgs {
				if mm, ok := m.(map[string]any); ok && (mm["role"] == "system" || mm["role"] == "developer") {
					continue
				}
				kept = append(kept, m)
			}
			msgs = kept
		}
		if agentSys != "" {
			msgs = append([]any{map[string]any{"role": "system", "content": agentSys}}, msgs...)
		}
		body["messages"] = msgs
	} else if agentSys != "" {
		block := map[string]any{"type": "text", "text": agentSys}
		switch cs := body["system"].(type) {
		case string:
			if a.AllowClientSystem && cs != "" {
				body["system"] = []any{block, map[string]any{"type": "text", "text": cs}}
			} else {
				body["system"] = []any{block}
			}
		case []any:
			if a.AllowClientSystem {
				body["system"] = append([]any{block}, cs...)
			} else {
				body["system"] = []any{block}
			}
		default:
			body["system"] = []any{block}
		}
	} else if !a.AllowClientSystem {
		delete(body, "system")
	}
	if a.Model != "" {
		body["model"] = a.Model
	}
	var params map[string]any
	_ = json.Unmarshal(a.Params, &params)
	for k, v := range params {
		if _, ok := body[k]; !ok {
			body[k] = v
		}
	}
	if !openai {
		if _, ok := body["max_tokens"]; !ok {
			body["max_tokens"] = 4096
		}
		var tools []any
		_ = json.Unmarshal(a.Tools, &tools)
		if len(tools) > 0 {
			existing, _ := body["tools"].([]any)
			body["tools"] = append(tools, existing...)
		}
		if len(mcp) > 0 {
			body["mcp_servers"] = mcp
		}
	}
}

// ---------------------------------------------------------------------------
// Auxiliary endpoints

func (g *Gateway) countTokens(w http.ResponseWriter, r *http.Request) {
	p, err := g.authenticate(r)
	if err != nil {
		writeErr(w, providers.FormatAnthropic, err)
		return
	}
	body := map[string]any{}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, g.Cfg.MaxBodyBytes))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		writeErr(w, providers.FormatAnthropic, util.Err(400, "invalid_request_error", "invalid JSON"))
		return
	}
	model, _ := body["model"].(string)
	snap := g.Loader.Snapshot()
	route := snap.Resolve(model)
	if route == nil {
		writeErr(w, providers.FormatAnthropic, util.Err(404, "not_found_error", "model not found"))
		return
	}
	for _, d := range route.Order(p.OrgID) {
		if ap, ok := d.Provider.P.(*providers.Anthropic); ok && d.Provider.P.Kind() == providers.KindAnthropic {
			resp, err := ap.CountTokens(r.Context(), body, d.Upstream(model), r.Header)
			if err == nil {
				defer resp.Body.Close()
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.Copy(w, resp.Body)
				return
			}
		}
	}
	// Estimate (~4 chars/token) when the upstream has no counting endpoint.
	chars := 0
	for _, t := range guardrails.RequestTexts(body, false) {
		chars += len(t.Get())
	}
	if tools, ok := body["tools"]; ok {
		b, _ := json.Marshal(tools)
		chars += len(b)
	}
	w.Header().Set("X-Theophylax-Estimated", "true")
	util.WriteJSON(w, 200, map[string]any{"input_tokens": chars/4 + 1})
}

func (g *Gateway) listModels(w http.ResponseWriter, r *http.Request) {
	p, err := g.authenticate(r)
	if err != nil {
		writeErr(w, providers.FormatOpenAI, err)
		return
	}
	snap := g.Loader.Snapshot()
	seen := map[string]bool{}
	type mi struct {
		id, name string
		created  time.Time
	}
	var list []mi
	for _, rt := range snap.Models {
		m := rt.Model
		if seen[m.ID] || m.Status == "deprecated" || (rt.Router == nil && len(rt.Order(p.OrgID)) == 0) {
			continue
		}
		if g.checkModelAllowed(snap, p, m.Name, m.Name) != nil {
			continue
		}
		seen[m.ID] = true
		name := m.DisplayName
		if name == "" {
			name = m.Name
		}
		list = append(list, mi{m.Name, name, m.CreatedAt})
	}
	if r.Header.Get("anthropic-version") != "" {
		data := make([]any, 0, len(list))
		for _, m := range list {
			data = append(data, map[string]any{"type": "model", "id": m.id, "display_name": m.name, "created_at": m.created.Format(time.RFC3339)})
		}
		resp := map[string]any{"data": data, "has_more": false, "first_id": nil, "last_id": nil}
		if len(list) > 0 {
			resp["first_id"], resp["last_id"] = list[0].id, list[len(list)-1].id
		}
		util.WriteJSON(w, 200, resp)
		return
	}
	data := make([]any, 0, len(list))
	for _, m := range list {
		data = append(data, map[string]any{"id": m.id, "object": "model", "created": m.created.Unix(), "owned_by": "theophylax"})
	}
	util.WriteJSON(w, 200, map[string]any{"object": "list", "data": data})
}

func (g *Gateway) whoami(w http.ResponseWriter, r *http.Request) {
	p, err := g.authenticate(r)
	if err != nil {
		writeErr(w, providers.FormatAnthropic, err)
		return
	}
	snap := g.Loader.Snapshot()
	out := map[string]any{"org_id": p.OrgID, "team_id": p.TeamID, "project_id": p.ProjectID, "via": p.Via}
	if o := snap.Orgs[p.OrgID]; o != nil {
		out["org"] = o.Name
	}
	if pr := snap.Projects[p.ProjectID]; pr != nil {
		out["project"] = pr.Name
	}
	if p.Key != nil {
		out["key"] = map[string]any{"id": p.Key.ID, "name": p.Key.Name, "allowed_models": p.Key.AllowedModels, "rpm": p.Key.RPM, "tpm": p.Key.TPM}
	}
	if p.User != nil {
		out["user"] = p.User.Email
	}
	util.WriteJSON(w, 200, out)
}

// ---------------------------------------------------------------------------

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func shallowCopy(m map[string]any) map[string]any {
	o := make(map[string]any, len(m))
	for k, v := range m {
		o[k] = v
	}
	return o
}

var _ = context.Background
