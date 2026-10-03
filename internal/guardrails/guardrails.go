// Package guardrails evaluates layered policies (org → team → project) on
// requests and responses: PII/secret detection and redaction, regex and
// keyword deny-lists, prompt-injection heuristics, token caps, external
// webhooks and AWS Bedrock Guardrails.
package guardrails

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Rule struct {
	Type      string   `json:"type"`
	Action    string   `json:"action,omitempty"`   // block | redact | log
	ApplyTo   string   `json:"apply_to,omitempty"` // input | output | both
	Entities  []string `json:"entities,omitempty"`
	Patterns  []string `json:"patterns,omitempty"`
	Words     []string `json:"words,omitempty"`
	Models    []string `json:"models,omitempty"`
	Max       int      `json:"max,omitempty"`
	Threshold float64  `json:"threshold,omitempty"`
	Message   string   `json:"message,omitempty"`
	Scope     string   `json:"scope,omitempty"`   // all | latest (which messages to scan)
	Restore   bool     `json:"restore,omitempty"` // put original values back into the model's response
	// prompt_injection options
	Detector     string   `json:"detector,omitempty"`    // heuristic (default) | hybrid | judge
	JudgeModel   string   `json:"judge_model,omitempty"` // model used by hybrid/judge
	Canary       bool     `json:"canary,omitempty"`      // plant a canary and block responses that leak it
	ExfilGuard   bool     `json:"exfil_guard,omitempty"` // block data-smuggling links/images in responses
	ToolGuard    bool     `json:"tool_guard,omitempty"`  // block risky tool calls after untrusted injection signals
	AllowDomains []string `json:"allow_domains,omitempty"`
	// webhook
	URL       string `json:"url,omitempty"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
	FailOpen  *bool  `json:"fail_open,omitempty"`
	// bedrock_guardrail
	ProviderID  string `json:"provider_id,omitempty"`
	GuardrailID string `json:"guardrail_id,omitempty"`
	Version     string `json:"version,omitempty"`
}

type Policy struct {
	ID, Name, Mode, ScopeType string
	Digest                    string // sha256 of mode+rules: identifies the exact policy version
	Rules                     []Rule
}

type Finding struct {
	Policy string `json:"policy"`
	Rule   string `json:"rule"`
	Action string `json:"action"`
	Detail string `json:"detail,omitempty"`
	Count  int    `json:"count,omitempty"`
	Phase  string `json:"phase"`
}

type Result struct {
	Blocked  bool
	Message  string
	Findings []Finding
	// Restore maps placeholders to original values for "restore on return".
	Restore map[string]string
	// Prompt-injection state carried from the input phase to the output phase.
	Canaries       []string
	Tainted        bool
	InjectionScore float64
}

func (r *Result) add(f Finding) { r.Findings = append(r.Findings, f) }

// BedrockApplier is implemented by the Bedrock provider.
type BedrockApplier interface {
	ApplyGuardrail(ctx context.Context, id, version, source string, texts []string) (bool, string, error)
}

type Engine struct {
	client  *http.Client
	bedrock func(providerID string) BedrockApplier
	reCache sync.Map
	hmacKey []byte
	// Judge calls a gateway-routed model for hybrid/judge injection detection.
	Judge JudgeFunc
}

// New creates an engine. hmacKey keys the stable redaction placeholders.
func New(hmacKey []byte, bedrock func(string) BedrockApplier) *Engine {
	return &Engine{client: &http.Client{Timeout: 10 * time.Second}, bedrock: bedrock, hmacKey: hmacKey}
}

// ---------------------------------------------------------------------------
// Detectors

type detector struct {
	re    *regexp.Regexp
	check func(string) bool
}

var detectors = map[string]detector{
	"email":       {re: regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)},
	"phone":       {re: regexp.MustCompile(`(?:\+\d{1,3}[\s.-]?)?\(?\d{2,4}\)?[\s.-]\d{3,4}[\s.-]\d{3,4}\b`)},
	"credit_card": {re: regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`), check: luhn},
	"us_ssn":      {re: regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)},
	"uk_nino":     {re: regexp.MustCompile(`\b[A-CEGHJ-PR-TW-Z]{2}\s?\d{2}\s?\d{2}\s?\d{2}\s?[A-D]\b`)},
	"iban":        {re: regexp.MustCompile(`\b[A-Z]{2}\d{2}(?:\s?[A-Z0-9]{4}){3,7}(?:\s?[A-Z0-9]{1,3})?\b`)},
	"ip_address":  {re: regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4]\d|1?\d?\d)\.){3}(?:25[0-5]|2[0-4]\d|1?\d?\d)\b`)},
}

// DefaultSecrets is the entity set used when a pii rule lists none.
var defaultPIIEntities = []string{"email", "credit_card", "us_ssn", "uk_nino", "iban"}

// Entities lists PII and secret entities (for the console's pickers).
func Entities() (pii []string, secrets []string) {
	for k := range detectors {
		pii = append(pii, k)
	}
	for k := range secretDetectors {
		secrets = append(secrets, k)
	}
	return pii, secrets
}

func validEntity(en string) bool { _, ok := detectors[en]; return ok || isSecretEntity(en) }

func luhn(s string) bool {
	sum, n, alt := 0, 0, false
	for i := len(s) - 1; i >= 0; i-- {
		c := s[i]
		if c < '0' || c > '9' {
			continue
		}
		d := int(c - '0')
		if alt {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		alt = !alt
		n++
	}
	return n >= 13 && n <= 19 && sum%10 == 0
}

func (e *Engine) compile(p string) (*regexp.Regexp, error) {
	if v, ok := e.reCache.Load(p); ok {
		return v.(*regexp.Regexp), nil
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, err
	}
	e.reCache.Store(p, re)
	return re, nil
}

// ---------------------------------------------------------------------------
// Text access across both wire formats

// TextRef points at a mutable string inside a request/response body.
type TextRef struct {
	Get    func() string
	Set    func(string)
	Role   string
	Latest bool
}

// RequestTexts enumerates every text field of an Anthropic or OpenAI request.
func RequestTexts(body map[string]any, openai bool) []TextRef {
	var refs []TextRef
	addStr := func(container map[string]any, key, role string, latest bool) {
		if _, ok := container[key].(string); ok {
			refs = append(refs, TextRef{Get: func() string { s, _ := container[key].(string); return s }, Set: func(v string) { container[key] = v }, Role: role, Latest: latest})
		}
	}
	addBlocks := func(arr []any, role string, latest bool) {
		for _, b := range arr {
			bm, ok := b.(map[string]any)
			if !ok {
				continue
			}
			switch bm["type"] {
			case "tool_use":
				addDeep(bm["input"], role, latest, &refs)
			case "text":
				addStr(bm, "text", role, latest)
			case "tool_result": // untrusted: files, web pages, command output
				if _, ok := bm["content"].(string); ok {
					addStr(bm, "content", "tool", latest)
				} else if inner, ok := bm["content"].([]any); ok {
					for _, ib := range inner {
						if im, ok := ib.(map[string]any); ok && im["type"] == "text" {
							addStr(im, "text", "tool", latest)
						}
					}
				}
			}
		}
	}
	if !openai {
		switch s := body["system"].(type) {
		case string:
			addStr(body, "system", "system", false)
		case []any:
			addBlocks(s, "system", false)
		}
	}
	msgs, _ := body["messages"].([]any)
	// "Latest" = everything after the last assistant turn (the new user input
	// and/or tool results the model is about to act on).
	lastAssistant := -1
	for i, m := range msgs {
		if mm, ok := m.(map[string]any); ok && mm["role"] == "assistant" {
			lastAssistant = i
		}
	}
	for i, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := mm["role"].(string)
		latest := i > lastAssistant
		switch c := mm["content"].(type) {
		case string:
			addStr(mm, "content", role, latest)
		case []any:
			addBlocks(c, role, latest)
		}
		if calls, ok := mm["tool_calls"].([]any); ok { // OpenAI assistant tool calls
			for _, tc := range calls {
				if fn, ok := tc.(map[string]any)["function"].(map[string]any); ok {
					addStr(fn, "arguments", role, latest)
				}
			}
		}
	}
	return refs
}

// addDeep registers every string inside a JSON value (tool inputs echo file
// contents and commands, which often carry secrets).
func addDeep(v any, role string, latest bool, refs *[]TextRef) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if _, ok := val.(string); ok {
				m, key := x, k
				*refs = append(*refs, TextRef{Get: func() string { s, _ := m[key].(string); return s }, Set: func(s string) { m[key] = s }, Role: role, Latest: latest})
			} else {
				addDeep(val, role, latest, refs)
			}
		}
	case []any:
		for i, val := range x {
			if _, ok := val.(string); ok {
				arr, idx := x, i
				*refs = append(*refs, TextRef{Get: func() string { s, _ := arr[idx].(string); return s }, Set: func(s string) { arr[idx] = s }, Role: role, Latest: latest})
			} else {
				addDeep(val, role, latest, refs)
			}
		}
	}
}

// ResponseTexts enumerates text fields of a non-streaming response body.
func ResponseTexts(body map[string]any, openai bool) []TextRef {
	var refs []TextRef
	if openai {
		ch, _ := body["choices"].([]any)
		for _, c := range ch {
			msg, _ := c.(map[string]any)["message"].(map[string]any)
			if calls, ok := msg["tool_calls"].([]any); ok {
				for _, tc := range calls {
					if fn, ok := tc.(map[string]any)["function"].(map[string]any); ok {
						f := fn
						refs = append(refs, TextRef{Get: func() string { s, _ := f["arguments"].(string); return s }, Set: func(v string) { f["arguments"] = v }, Role: "assistant_tool", Latest: true})
					}
				}
			}
			if msg != nil {
				if _, ok := msg["content"].(string); ok {
					m := msg
					refs = append(refs, TextRef{Get: func() string { s, _ := m["content"].(string); return s }, Set: func(v string) { m["content"] = v }, Role: "assistant", Latest: true})
				}
			}
		}
		return refs
	}
	content, _ := body["content"].([]any)
	for _, b := range content {
		bm, ok := b.(map[string]any)
		if ok && bm["type"] == "text" {
			refs = append(refs, TextRef{Get: func() string { s, _ := bm["text"].(string); return s }, Set: func(v string) { bm["text"] = v }, Role: "assistant", Latest: true})
		}
		if ok && bm["type"] == "tool_use" {
			addDeep(bm["input"], "assistant_tool", true, &refs)
		}
	}
	return refs
}

// ---------------------------------------------------------------------------
// Evaluation

type Input struct {
	Phase    string // input | output
	Model    string
	Body     map[string]any // may be mutated (redaction, max_tokens cap)
	Texts    []TextRef
	OpenAI   bool
	Metadata map[string]string
	// ScopeKey keys canary tokens (e.g. org/project). Canaries and Tainted carry input-phase state into the output phase.
	ScopeKey string
	Canaries []string
	Tainted  bool
}

func applies(r Rule, phase string) bool {
	if r.Restore && phase == "output" && (r.Type == "pii" || r.Type == "secrets") {
		return false // restoring rules must not re-redact the restored values
	}
	switch r.ApplyTo {
	case "", "both":
		return true
	default:
		return r.ApplyTo == phase
	}
}

func defaultAction(r Rule, def string) string {
	if r.Action == "" {
		return def
	}
	return r.Action
}

// Evaluate runs all policies. In monitor mode, block/redact actions are recorded but not applied.
func (e *Engine) Evaluate(ctx context.Context, policies []Policy, in Input) Result {
	var res Result
	for _, p := range policies {
		monitor := p.Mode == "monitor"
		for _, r := range p.Rules {
			if !applies(r, in.Phase) {
				continue
			}
			e.evalRule(ctx, p, r, monitor, in, &res)
			if res.Blocked {
				return res
			}
		}
	}
	return res
}

func (e *Engine) evalRule(ctx context.Context, p Policy, r Rule, monitor bool, in Input, res *Result) {
	texts := in.Texts
	if r.Scope == "latest" {
		var lt []TextRef
		for _, t := range texts {
			if t.Latest {
				lt = append(lt, t)
			}
		}
		texts = lt
	}
	block := func(detail string) {
		f := Finding{Policy: p.Name, Rule: r.Type, Action: "block", Detail: detail, Phase: in.Phase}
		if monitor {
			f.Action = "monitor"
			res.add(f)
			return
		}
		res.add(f)
		res.Blocked = true
		res.Message = r.Message
		if res.Message == "" {
			res.Message = fmt.Sprintf("blocked by guardrail policy %q (%s): %s", p.Name, r.Type, detail)
		}
	}
	switch r.Type {
	case "pii", "secrets":
		ents := r.Entities
		if len(ents) == 0 {
			if r.Type == "secrets" {
				ents = DefaultSecretEntities
			} else {
				ents = append(append([]string{}, defaultPIIEntities...), DefaultSecretEntities...)
			}
		}
		action := defaultAction(r, "redact")
		apply := action == "redact" && !monitor
		var secretEnts, piiEnts []string
		for _, en := range ents {
			if isSecretEntity(en) {
				secretEnts = append(secretEnts, en)
			} else {
				piiEnts = append(piiEnts, en)
			}
		}
		var restore map[string]string
		if apply && r.Restore {
			if res.Restore == nil {
				res.Restore = map[string]string{}
			}
			restore = res.Restore
		}
		counts := map[string]int{}
		for _, t := range texts {
			s := t.Get()
			changed := false
			for _, en := range piiEnts {
				d := detectors[en]
				if !d.re.MatchString(s) {
					continue
				}
				s = d.re.ReplaceAllStringFunc(s, func(m string) string {
					if d.check != nil && !d.check(m) {
						return m
					}
					counts[en]++
					if !apply {
						return m
					}
					changed = true
					ph := e.Placeholder(en, m)
					if restore != nil {
						restore[ph] = m
					}
					return ph
				})
			}
			if len(secretEnts) > 0 {
				var c bool
				s, c = e.redactSecrets(s, secretEnts, apply, counts, restore)
				changed = changed || c
			}
			if changed {
				t.Set(s)
			}
		}
		for en, n := range counts {
			switch {
			case action == "block":
				block(fmt.Sprintf("%d %s detected", n, en))
				return
			default:
				a := action
				if monitor {
					a = "monitor"
				}
				res.add(Finding{Policy: p.Name, Rule: r.Type, Action: a, Detail: en, Count: n, Phase: in.Phase})
			}
		}
	case "regex", "keywords":
		pats := r.Patterns
		if r.Type == "keywords" {
			for _, w := range r.Words {
				pats = append(pats, `(?i)\b`+regexp.QuoteMeta(w)+`\b`)
			}
		}
		action := defaultAction(r, "block")
		for _, pat := range pats {
			re, err := e.compile(pat)
			if err != nil {
				continue
			}
			for _, t := range texts {
				s := t.Get()
				if !re.MatchString(s) {
					continue
				}
				switch action {
				case "block":
					block("matched " + truncate(re.FindString(s), 40))
					return
				case "redact":
					if !monitor {
						t.Set(re.ReplaceAllString(s, "[REDACTED]"))
					}
					res.add(Finding{Policy: p.Name, Rule: r.Type, Action: map[bool]string{true: "monitor", false: "redact"}[monitor], Detail: pat, Phase: in.Phase})
				default:
					res.add(Finding{Policy: p.Name, Rule: r.Type, Action: "log", Detail: pat, Phase: in.Phase})
				}
			}
		}
	case "prompt_injection":
		e.evalInjection(ctx, p, r, monitor, in, res, block)
	case "model_allowlist":
		if in.Phase != "input" {
			return
		}
		ok := false
		for _, m := range r.Models {
			if m == in.Model || (strings.HasSuffix(m, "*") && strings.HasPrefix(in.Model, strings.TrimSuffix(m, "*"))) {
				ok = true
			}
		}
		if !ok {
			block("model " + in.Model + " is not allowed")
		}
	case "max_tokens":
		if in.Phase != "input" || r.Max <= 0 {
			return
		}
		key := "max_tokens"
		if in.OpenAI {
			if _, ok := in.Body["max_completion_tokens"]; ok {
				key = "max_completion_tokens"
			}
		}
		cur := toInt(in.Body[key])
		if cur > r.Max || (cur == 0 && !in.OpenAI) {
			if defaultAction(r, "cap") == "block" {
				block(fmt.Sprintf("max_tokens %d exceeds %d", cur, r.Max))
				return
			}
			if !monitor {
				in.Body[key] = r.Max
			}
			res.add(Finding{Policy: p.Name, Rule: r.Type, Action: "cap", Detail: fmt.Sprintf("%d→%d", cur, r.Max), Phase: in.Phase})
		}
	case "max_input_chars":
		if in.Phase != "input" || r.Max <= 0 {
			return
		}
		total := 0
		for _, t := range in.Texts {
			total += len(t.Get())
		}
		if total > r.Max {
			block(fmt.Sprintf("input of %d chars exceeds %d", total, r.Max))
		}
	case "webhook":
		e.webhook(ctx, p, r, monitor, in, texts, res, block)
	case "bedrock_guardrail":
		if e.bedrock == nil {
			return
		}
		ap := e.bedrock(r.ProviderID)
		if ap == nil {
			res.add(Finding{Policy: p.Name, Rule: r.Type, Action: "error", Detail: "provider is not a bedrock connection", Phase: in.Phase})
			return
		}
		src := "INPUT"
		if in.Phase == "output" {
			src = "OUTPUT"
		}
		var ts []string
		for _, t := range texts {
			if t.Role != "system" {
				ts = append(ts, t.Get())
			}
		}
		if len(ts) == 0 {
			return
		}
		blocked, out, err := ap.ApplyGuardrail(ctx, r.GuardrailID, orDefault(r.Version, "DRAFT"), src, ts)
		if err != nil {
			if r.FailOpen != nil && !*r.FailOpen {
				block("bedrock guardrail unavailable: " + err.Error())
				return
			}
			res.add(Finding{Policy: p.Name, Rule: r.Type, Action: "error", Detail: err.Error(), Phase: in.Phase})
			return
		}
		if blocked {
			block("intervened: " + truncate(out, 120))
		}
	}
}

func (e *Engine) webhook(ctx context.Context, p Policy, r Rule, monitor bool, in Input, texts []TextRef, res *Result, block func(string)) {
	timeout := time.Duration(r.TimeoutMS) * time.Millisecond
	if timeout == 0 {
		timeout = 2 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ts := make([]string, len(texts))
	for i, t := range texts {
		ts[i] = t.Get()
	}
	payload, _ := json.Marshal(map[string]any{"phase": in.Phase, "model": in.Model, "texts": ts, "metadata": in.Metadata})
	req, _ := http.NewRequestWithContext(cctx, http.MethodPost, r.URL, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	failOpen := r.FailOpen == nil || *r.FailOpen
	if err != nil || resp.StatusCode >= 300 {
		if resp != nil {
			resp.Body.Close()
		}
		if !failOpen {
			block("guardrail webhook unavailable")
			return
		}
		res.add(Finding{Policy: p.Name, Rule: "webhook", Action: "error", Detail: "webhook unavailable (fail-open)", Phase: in.Phase})
		return
	}
	defer resp.Body.Close()
	var out struct {
		Action  string   `json:"action"`
		Message string   `json:"message"`
		Texts   []string `json:"texts"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	switch out.Action {
	case "block":
		block(orDefault(out.Message, "rejected by webhook"))
	case "redact":
		if !monitor && len(out.Texts) == len(texts) {
			for i, t := range texts {
				t.Set(out.Texts[i])
			}
		}
		res.add(Finding{Policy: p.Name, Rule: "webhook", Action: "redact", Phase: in.Phase})
	}
}

func (e *Engine) evalInjection(ctx context.Context, p Policy, r Rule, monitor bool, in Input, res *Result, block func(string)) {
	if in.Phase == "output" {
		for _, t := range in.Texts {
			txt := t.Get()
			for _, c := range in.Canaries {
				if canaryLeaked(txt, c) {
					block("system prompt extraction detected (canary token appeared in the response)")
					return
				}
			}
			if r.ExfilGuard {
				if hosts := exfilLinks(txt, r.AllowDomains); len(hosts) > 0 {
					block("response contains a link that would send data to " + hosts[0])
					return
				}
			}
			if r.ToolGuard && in.Tainted && t.Role == "assistant_tool" {
				if m := riskyToolCall(txt); m != "" {
					block("risky tool call after untrusted content tried to inject instructions: " + truncate(m, 60))
					return
				}
			}
		}
		return
	}
	th := r.Threshold
	if th == 0 {
		th = 0.5
	}
	best := InjectionResult{}
	bestSrc, bestText := "", ""
	for _, t := range in.Texts {
		if t.Role == "system" || t.Role == "assistant" || t.Role == "developer" {
			continue
		}
		if r.Scope != "all" && !t.Latest {
			continue
		}
		untrusted := t.Role == "tool"
		ir := AnalyzeInjection(t.Get(), untrusted)
		if untrusted && ir.Score >= 0.25 {
			res.Tainted = true
		}
		if ir.Score > best.Score {
			best, bestText = ir, t.Get()
			bestSrc = map[bool]string{true: "tool result", false: "user message"}[untrusted]
		}
	}
	detector := r.Detector
	if detector == "" {
		detector = "heuristic"
	}
	judged := ""
	if detector == "judge" || (detector == "hybrid" && best.Score >= 0.2 && best.Score < th+0.25) {
		if bestText != "" && r.JudgeModel != "" {
			inj, conf, cat, err := e.judge(ctx, r.JudgeModel, bestText)
			switch {
			case err != nil:
				res.add(Finding{Policy: p.Name, Rule: r.Type, Action: "error", Detail: "judge unavailable: " + truncate(err.Error(), 80), Phase: in.Phase})
			case inj && conf >= 0.6:
				best.Score = math.Max(best.Score, conf)
				judged = " · judge: " + cat
			case !inj && conf >= 0.7 && detector == "hybrid":
				best.Score = math.Min(best.Score, th-0.01) // judge clears a grey-zone false positive
				judged = " · cleared by judge"
			}
		}
	}
	res.InjectionScore = math.Max(res.InjectionScore, best.Score)
	if r.Canary && !monitor {
		tok := e.CanaryFor(in.ScopeKey)
		injectCanary(in.Body, in.OpenAI, tok)
		res.Canaries = append(res.Canaries, tok)
	}
	if best.Score < th {
		if best.Score >= 0.25 {
			res.add(Finding{Policy: p.Name, Rule: r.Type, Action: "log", Detail: fmt.Sprintf("score %.2f below threshold (%s)%s", best.Score, topCategories(best.Categories), judged), Phase: in.Phase})
		}
		return
	}
	detail := fmt.Sprintf("score %.2f in %s: %s%s", best.Score, bestSrc, topCategories(best.Categories), judged)
	if defaultAction(r, "block") == "block" {
		block(detail)
		return
	}
	res.add(Finding{Policy: p.Name, Rule: r.Type, Action: "log", Detail: detail, Phase: in.Phase})
}

// StreamGuard scans streamed output incrementally (deny rules only) using a
// sliding window so matches spanning chunk boundaries are caught.
type StreamGuard struct {
	res      []*regexp.Regexp
	names    []string
	window   strings.Builder
	Findings []Finding
}

func (e *Engine) NewStreamGuard(policies []Policy, canaries []string, tainted bool) *StreamGuard {
	g := &StreamGuard{}
	for _, c := range canaries {
		g.res = append(g.res, regexp.MustCompile(`(?i)`+regexp.QuoteMeta(c)))
		g.names = append(g.names, "canary")
	}
	for _, p := range policies {
		if p.Mode == "monitor" {
			continue
		}
		for _, r := range p.Rules {
			if !applies(r, "output") || defaultAction(r, "block") != "block" {
				continue
			}
			switch r.Type {
			case "prompt_injection":
				if r.ExfilGuard {
					g.res = append(g.res, reExfilLink)
					g.names = append(g.names, p.Name)
				}
				if r.ToolGuard && tainted {
					for _, re := range riskyTool {
						g.res = append(g.res, regexp.MustCompile(`(?i)`+re.String()))
						g.names = append(g.names, p.Name)
					}
				}
			case "regex", "keywords":
				pats := r.Patterns
				for _, w := range r.Words {
					pats = append(pats, `(?i)\b`+regexp.QuoteMeta(w)+`\b`)
				}
				for _, pat := range pats {
					if re, err := e.compile(pat); err == nil {
						g.res = append(g.res, re)
						g.names = append(g.names, p.Name)
					}
				}
			case "pii", "secrets":
				if r.Action == "block" {
					ents := r.Entities
					if len(ents) == 0 {
						ents = append(append([]string{}, defaultPIIEntities...), DefaultSecretEntities...)
					}
					for _, en := range ents {
						if d, ok := detectors[en]; ok {
							g.res = append(g.res, d.re)
							g.names = append(g.names, p.Name)
						} else if sd, ok := secretDetectors[en]; ok && sd.check == nil {
							g.res = append(g.res, sd.re)
							g.names = append(g.names, p.Name)
						}
					}
				}
			}
		}
	}
	if len(g.res) == 0 {
		return nil
	}
	return g
}

// Feed returns a non-empty policy name when the stream must be terminated.
func (g *StreamGuard) Feed(delta string) string {
	g.window.WriteString(delta)
	s := g.window.String()
	for i, re := range g.res {
		if re.MatchString(s) {
			g.Findings = append(g.Findings, Finding{Policy: g.names[i], Rule: "stream", Action: "block", Phase: "output"})
			return g.names[i]
		}
	}
	if len(s) > 4096 {
		tail := s[len(s)-1024:]
		g.window.Reset()
		g.window.WriteString(tail)
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func toInt(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	}
	return 0
}

// ParseRules decodes policy rules JSON and validates regexes.
func ParseRules(raw []byte) ([]Rule, error) {
	var rules []Rule
	if len(raw) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil, fmt.Errorf("rules must be a JSON array: %w", err)
	}
	for i, r := range rules {
		switch r.Type {
		case "pii", "secrets", "regex", "keywords", "prompt_injection", "model_allowlist", "max_tokens", "max_input_chars", "webhook", "bedrock_guardrail":
		default:
			return nil, fmt.Errorf("rule %d: unknown type %q", i, r.Type)
		}
		for _, p := range r.Patterns {
			if _, err := regexp.Compile(p); err != nil {
				return nil, fmt.Errorf("rule %d: invalid regex %q: %v", i, p, err)
			}
		}
		for _, en := range r.Entities {
			if !validEntity(en) {
				return nil, fmt.Errorf("rule %d: unknown entity %q", i, en)
			}
		}
		if r.Type == "webhook" && !strings.HasPrefix(r.URL, "http") {
			return nil, fmt.Errorf("rule %d: webhook requires url", i)
		}
	}
	return rules, nil
}
