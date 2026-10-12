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
	"net/http"
	"time"

	"theophylax/internal/guardrails"
	"theophylax/internal/metrics"
	"theophylax/internal/providers"
	"theophylax/internal/store"
	"theophylax/internal/util"
)

// embeddings serves POST /v1/embeddings (OpenAI format) with the same
// identity, limits, input guardrails, failover, logging and receipts as chat.
func (g *Gateway) embeddings(w http.ResponseWriter, r *http.Request) {
	const cf = providers.FormatOpenAI
	st := &reqState{start: time.Now(), traceparent: r.Header.Get("traceparent")}
	reqID := util.NewID("req")
	w.Header().Set("X-Request-Id", reqID)
	st.log = store.RequestLog{ID: reqID, TS: st.start.UTC(), Endpoint: r.URL.Path, Status: 200,
		ClientIP: util.ClientIP(r, g.Cfg.TrustedProxies), UserAgent: truncate(r.UserAgent(), 200)}
	p, err := g.authenticate(r)
	if err != nil {
		code := writeErr(w, cf, err)
		metrics.Requests.Inc("", "", fmt.Sprint(code))
		return
	}
	snap := g.Loader.Snapshot()
	st.p = p
	st.hw = &hashWriter{ResponseWriter: w, h: sha256.New()}
	w = st.hw
	w.Header().Set("X-Theophylax-Receipt", reqID)
	st.log.OrgID, st.log.TeamID, st.log.ProjectID = strPtr(p.OrgID), strPtr(p.TeamID), strPtr(p.ProjectID)
	setTags(w, r, p, st)
	if p.Key != nil {
		st.log.KeyID = &p.Key.ID
	}
	if uid := p.UserID(); uid != "" {
		st.log.UserID = &uid
	}
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
	body := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		fail(util.Err(400, "invalid_request_error", "invalid JSON body: "+err.Error()))
		return
	}
	model, _ := body["model"].(string)
	st.log.Model = model
	if model == "" {
		fail(util.Err(400, "invalid_request_error", "model is required"))
		return
	}
	// Normalise input to a list of strings so guardrails can inspect each one.
	var inputs []any
	switch v := body["input"].(type) {
	case string:
		inputs = []any{v}
	case []any:
		for _, x := range v {
			if _, ok := x.(string); !ok {
				fail(util.Err(400, "invalid_request_error", "input must be a string or an array of strings (token arrays are not supported)"))
				return
			}
		}
		inputs = v
	default:
		fail(util.Err(400, "invalid_request_error", "input is required"))
		return
	}
	if len(inputs) == 0 || len(inputs) > 2048 {
		fail(util.Err(400, "invalid_request_error", "input must contain between 1 and 2048 items"))
		return
	}
	body["input"] = inputs

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
	}
	if err := g.checkLimits(w, snap, p, st.scopes); err != nil {
		fail(err)
		return
	}

	policies := snap.PoliciesFor(p.OrgID, p.TeamID, p.ProjectID)
	st.policies = policies
	if len(policies) > 0 {
		refs := make([]guardrails.TextRef, len(inputs))
		for i := range inputs {
			i := i
			refs[i] = guardrails.TextRef{Get: func() string { s, _ := inputs[i].(string); return s }, Set: func(v string) { inputs[i] = v }, Role: "user", Latest: true}
		}
		res := g.Guard.Evaluate(r.Context(), policies, guardrails.Input{Phase: "input", Model: route.Model.Name, Body: body, Texts: refs, OpenAI: true,
			ScopeKey: p.OrgID + "/" + p.ProjectID, Metadata: map[string]string{"org_id": p.OrgID, "team_id": p.TeamID, "project_id": p.ProjectID, "request_id": reqID}})
		st.gr = append(st.gr, res.Findings...)
		if res.Blocked {
			w.Header().Set("X-Theophylax-Guardrail", "blocked")
			fail(util.Err(400, "invalid_request_error", res.Message))
			return
		}
	}
	texts := make([]string, len(inputs))
	for i, x := range inputs {
		texts[i], _ = x.(string)
	}
	st.capture = g.capturePayloads(snap, p)
	if st.capture {
		st.reqPL, _ = json.Marshal(body)
	}

	var res *providers.EmbedResult
	var lastErr error
	tried := 0
	for _, d := range route.Order(p.OrgID) {
		emb, ok := d.Provider.P.(providers.Embedder)
		if !ok {
			continue
		}
		if tried++; tried > 4 {
			break
		}
		st.log.Attempts++
		upstream := d.Upstream(model)
		t0 := time.Now()
		res, lastErr = emb.Embed(r.Context(), &providers.EmbedRequest{Input: texts, Body: body, Model: upstream})
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
		slog.Warn("embeddings attempt failed", "request_id", reqID, "provider", d.Provider.Row.Name, "status", status, "err", truncate(lastErr.Error(), 300))
		if ue != nil && !ue.Retryable() {
			fail(upstreamHTTPError(ue))
			return
		}
		d.health.fail()
	}
	if res == nil {
		var ue *providers.UpstreamError
		switch {
		case tried == 0:
			fail(util.Err(400, "invalid_request_error", fmt.Sprintf("model %q has no deployment on a provider that serves embeddings", route.Model.Name)))
		case errors.As(lastErr, &ue):
			fail(upstreamHTTPError(ue))
		default:
			fail(util.Err(502, "api_error", "all upstream deployments failed: "+truncate(fmt.Sprint(lastErr), 300)))
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Theophylax-Model", route.Model.Name)
	_, _ = w.Write(res.Body)
	if st.capture {
		st.respPL = res.Body
	}
	st.log.InputTokens = res.InputTokens
	st.log.CostUSD, st.log.PriceKey = g.Pricing.Cost(st.log.ProviderKind, providers.Usage{Input: res.InputTokens}, st.log.UpstreamModel)
	g.finish(st)
}
