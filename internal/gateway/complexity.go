package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"theophylax/internal/guardrails"
)

// Complexity routing: a model whose strategy is "complexity" is a router,
// not a deployment target. Each request to it is scored for how demanding
// it is (0 = trivial, 1 = hardest) and sent to the model of the first tier
// whose max_score covers that score — for example simple questions to a
// small fast model and hard reasoning or coding to the strongest one.
//
// The default classifier is a deterministic heuristic over the request
// (length of the ask, conversation size, code, reasoning and maths cues,
// tools and agent turns, attachments, requested thinking or output size),
// so routing adds no latency or cost and the same request always routes the
// same way. "hybrid" additionally asks a judge model when the heuristic
// score is close to a tier boundary.

// ComplexityRouting is a router model's configuration (models.routing).
type ComplexityRouting struct {
	Tiers []ComplexityTier `json:"tiers"`
	// Classifier is "heuristic" (default) or "hybrid".
	Classifier string `json:"classifier,omitempty"`
	// JudgeModel scores borderline requests in hybrid mode.
	JudgeModel string `json:"judge_model,omitempty"`
}

type ComplexityTier struct {
	// MaxScore is the highest score this tier takes (0..1); the last tier
	// takes everything above the previous one and leaves it unset.
	MaxScore *float64 `json:"max_score,omitempty"`
	Model    string   `json:"model"`
}

// ParseComplexityRouting validates a router configuration. exists reports
// whether a target model is in the catalog and is not itself a router.
func ParseComplexityRouting(raw json.RawMessage, exists func(name string) error) (*ComplexityRouting, error) {
	var c ComplexityRouting
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("routing: %v", err)
	}
	if len(c.Tiers) < 2 {
		return nil, errors.New("routing: complexity routing needs at least two tiers")
	}
	prev := -1.0
	for i, t := range c.Tiers {
		if strings.TrimSpace(t.Model) == "" {
			return nil, fmt.Errorf("routing: tier %d needs a model", i+1)
		}
		last := i == len(c.Tiers)-1
		switch {
		case last && t.MaxScore != nil && *t.MaxScore < 1:
			return nil, errors.New("routing: the last tier takes the hardest requests; leave its max_score unset")
		case !last && t.MaxScore == nil:
			return nil, fmt.Errorf("routing: tier %d needs max_score", i+1)
		case !last && (*t.MaxScore <= prev || *t.MaxScore <= 0 || *t.MaxScore >= 1):
			return nil, fmt.Errorf("routing: tier max_score values must increase strictly between 0 and 1 (tier %d)", i+1)
		}
		if t.MaxScore != nil {
			prev = *t.MaxScore
		}
		if exists != nil {
			if err := exists(t.Model); err != nil {
				return nil, fmt.Errorf("routing: tier %d: %v", i+1, err)
			}
		}
	}
	switch c.Classifier {
	case "", "heuristic":
	case "hybrid":
		if c.JudgeModel == "" {
			return nil, errors.New("routing: the hybrid classifier needs judge_model")
		}
		if exists != nil {
			if err := exists(c.JudgeModel); err != nil {
				return nil, fmt.Errorf("routing: judge_model: %v", err)
			}
		}
	default:
		return nil, errors.New(`routing: classifier must be "heuristic" or "hybrid"`)
	}
	return &c, nil
}

// Pick returns the tier for a score.
func (c *ComplexityRouting) Pick(score float64) (int, string) {
	for i, t := range c.Tiers {
		if t.MaxScore == nil || score <= *t.MaxScore {
			return i, t.Model
		}
	}
	return len(c.Tiers) - 1, c.Tiers[len(c.Tiers)-1].Model
}

// nearBoundary reports whether a score is within margin of a tier boundary.
func (c *ComplexityRouting) nearBoundary(score, margin float64) bool {
	for _, t := range c.Tiers {
		if t.MaxScore != nil && math.Abs(score-*t.MaxScore) <= margin {
			return true
		}
	}
	return false
}

var (
	reasoningCue = regexp.MustCompile(`(?i)\b(prove|proof|derive|analy[sz]e|analysis|architect\w*|design|trade-?offs?|optimi[sz]\w*|debug\w*|refactor\w*|root cause|step[- ]by[- ]step|reason\w*|plan|strategy|compare|evaluate|algorithm\w*|complexity|edge cases?|security|vulnerab\w*|concurren\w*|race condition|deadlock|migrat\w*|scal\w+|distributed|invariant|formal)\b`)
	mathCue      = regexp.MustCompile(`(?i)(\$\$|\\frac|\\sum|\\int|∑|∫|\btheorem\b|\blemma\b|\bintegral\b|\bderivative\b|\bmatrix\b|\bprobability\b)`)
	simpleStart  = regexp.MustCompile(`(?i)^\s*(hi|hello|hey|thanks|thank you|translate|summari[sz]e|tl;?dr|fix (the )?typos?|rephrase|reword|rewrite|proofread|what('s| is| are)|who (is|was)|when (is|was|did)|define|list|classify|extract|convert|format)\b`)
	codingTask   = regexp.MustCompile(`(?i)\b(write|implement|build|create|generate|fix|port|add)\b[^.?!\n]{0,60}\b(function|method|class|script|program|code|tests?|api|endpoint|query|regex|parser|module|service|component|cli)\b`)
	codeLine     = regexp.MustCompile(`(?m)^\s*(func |def |class |import |package |public |private |#include|const |let |var |return |if \(|for \(|while \(|SELECT |CREATE |\}|\{)`)
)

// ScoreComplexity estimates how demanding a Messages or Chat Completions
// request is, from 0 (trivial) to 1 (hardest), with the signals that
// contributed.
func ScoreComplexity(body map[string]any, openai bool) (float64, []string) {
	var latest, all strings.Builder
	for _, t := range guardrails.RequestTexts(body, openai) {
		s := t.Get()
		all.WriteString(s)
		if t.Role == "user" && t.Latest {
			latest.WriteString(s)
			latest.WriteByte('\n')
		}
	}
	ask := latest.String()
	score := 0.0
	var why []string
	add := func(v float64, reason string) {
		if v != 0 {
			score += v
			why = append(why, fmt.Sprintf("%s%+.2f", reason, v))
		}
	}
	words := len(strings.Fields(ask))
	add(round2(0.30*clamp01(math.Log2(1+float64(words))/math.Log2(1+1500))), "ask_length")
	add(round2(0.15*clamp01(math.Log10(1+float64(all.Len()))/math.Log10(1+200000))), "context_size")
	if strings.Contains(ask, "```") || len(codeLine.FindAllStringIndex(ask, 4)) >= 3 {
		add(0.15, "code")
	} else if codingTask.MatchString(ask) {
		add(0.15, "coding_task")
	}
	if n := len(reasoningCue.FindAllStringIndex(ask, 5)); n > 0 {
		add(round2(0.09*float64(min(n, 4))), "reasoning_cues")
	}
	if mathCue.MatchString(ask) {
		add(0.10, "math")
	}
	if words < 60 && simpleStart.MatchString(ask) {
		add(-0.15, "simple_request")
	}
	if tools, _ := body["tools"].([]any); len(tools) > 5 {
		add(0.10, "many_tools")
	} else if len(tools) > 0 {
		add(0.05, "tools")
	}
	if n := agentTurns(body, openai); n >= 3 {
		add(0.10, "agent_turns")
	}
	if hasAttachments(body) {
		add(0.05, "attachments")
	}
	if wantsReasoning(body) {
		add(0.25, "reasoning_requested")
	}
	if mt := maxTokens(body); mt >= 8000 {
		add(0.10, "long_output")
	}
	return round2(clamp01(score)), why
}

func agentTurns(body map[string]any, openai bool) int {
	n := 0
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if openai {
			if mm["role"] == "tool" {
				n++
			}
			continue
		}
		blocks, _ := mm["content"].([]any)
		for _, b := range blocks {
			if bm, _ := b.(map[string]any); bm != nil && bm["type"] == "tool_result" {
				n++
			}
		}
	}
	return n
}

func hasAttachments(body map[string]any) bool {
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		blocks, _ := mm["content"].([]any)
		for _, b := range blocks {
			bm, _ := b.(map[string]any)
			switch bm["type"] {
			case "image", "document", "image_url", "file", "input_audio":
				return true
			}
		}
	}
	return false
}

func wantsReasoning(body map[string]any) bool {
	if th, _ := body["thinking"].(map[string]any); th != nil && (th["type"] == "enabled" || th["type"] == "adaptive") {
		return true
	}
	switch body["reasoning_effort"] {
	case "high", "medium":
		return true
	}
	return false
}

func maxTokens(body map[string]any) int {
	for _, k := range []string{"max_tokens", "max_completion_tokens"} {
		switch v := body[k].(type) {
		case json.Number:
			n, _ := v.Int64()
			return int(n)
		case float64:
			return int(v)
		}
	}
	return 0
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }
func round2(v float64) float64  { return math.Round(v*100) / 100 }

const judgeComplexityPrompt = `You rate how demanding a request to an AI assistant is, so it can be sent to a small fast model or a stronger one. Consider the reasoning depth, expertise, length and precision the answer needs. Reply with only an integer from 0 (trivial: greetings, lookups, simple rewrites) to 10 (hardest: multi-step reasoning, complex code or maths, careful analysis).`

// classify scores a request for a router. Hybrid mode asks the judge model
// only when the heuristic lands near a tier boundary.
func (g *Gateway) classify(ctx context.Context, rt *ComplexityRouting, body map[string]any, openai bool) (score float64, classifier string) {
	score, _ = ScoreComplexity(body, openai)
	if rt.Classifier != "hybrid" || !rt.nearBoundary(score, 0.1) {
		return score, "heuristic"
	}
	var ask strings.Builder
	for _, t := range guardrails.RequestTexts(body, openai) {
		if t.Role == "user" && t.Latest {
			ask.WriteString(t.Get())
			ask.WriteByte('\n')
		}
	}
	text := ask.String()
	if len(text) > 8000 {
		text = text[:8000] + "\n[truncated]"
	}
	jctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	out, err := g.Judge(jctx, rt.JudgeModel, judgeComplexityPrompt, "<request>\n"+text+"\n</request>")
	if err != nil {
		return score, "heuristic"
	}
	n, err := strconv.Atoi(strings.TrimSpace(strings.Trim(strings.TrimSpace(out), ".")))
	if err != nil || n < 0 || n > 10 {
		return score, "heuristic"
	}
	return round2(float64(n) / 10), "judge"
}

// routeHeader is the response header that reports a complexity decision.
const routeHeader = "X-Theophylax-Complexity"

func setRouteHeader(h http.Header, score float64, classifier, target string) {
	h.Set(routeHeader, fmt.Sprintf("%.2f; classifier=%s; model=%s", score, classifier, target))
}
