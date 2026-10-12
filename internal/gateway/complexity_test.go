package gateway

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func msgBody(text string, extra map[string]any) map[string]any {
	b := map[string]any{"model": "auto", "max_tokens": json.Number("1024"), "messages": []any{map[string]any{"role": "user", "content": text}}}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

func TestScoreComplexity(t *testing.T) {
	simple, _ := ScoreComplexity(msgBody("hi, what is the capital of France?", nil), false)
	translate, _ := ScoreComplexity(msgBody("Translate 'good morning' into Spanish.", nil), false)
	hard, why := ScoreComplexity(msgBody(`Our Go service deadlocks under load. Analyze the root cause step by step, consider the race condition between the two goroutines, and design a fix that keeps the invariant. Compare two approaches and explain the trade-offs.

`+"```go\nfunc (s *Store) Put(k string) {\n\ts.mu.Lock()\n\tdefer s.mu.Unlock()\n\ts.cache.Lock()\n}\n```", nil), false)
	thinking, _ := ScoreComplexity(msgBody("Plan the migration.", map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 8000}}), false)
	plain, _ := ScoreComplexity(msgBody("Plan the migration.", nil), false)

	if simple > 0.2 || translate > 0.2 {
		t.Errorf("simple requests should score low: %v %v", simple, translate)
	}
	if hard < 0.6 {
		t.Errorf("a hard debugging request should score high: %v %v", hard, why)
	}
	if thinking < plain+0.2 {
		t.Errorf("requested thinking should raise the score: %v vs %v", thinking, plain)
	}
	// OpenAI shape, agent turns and tools.
	oa := map[string]any{"model": "auto", "tools": []any{map[string]any{}, map[string]any{}},
		"messages": []any{map[string]any{"role": "user", "content": "fix the failing test"},
			map[string]any{"role": "tool", "content": "x"}, map[string]any{"role": "tool", "content": "y"}, map[string]any{"role": "tool", "content": "z"}}}
	agentic, why := ScoreComplexity(oa, true)
	if !strings.Contains(strings.Join(why, " "), "agent_turns") || !strings.Contains(strings.Join(why, " "), "tools") {
		t.Errorf("agent signals missing: %v %v", agentic, why)
	}
	// Deterministic.
	again, _ := ScoreComplexity(msgBody("hi, what is the capital of France?", nil), false)
	if again != simple {
		t.Error("scoring must be deterministic")
	}
}

func TestComplexityRoutingConfig(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	ok := `{"tiers":[{"max_score":0.3,"model":"small"},{"max_score":0.7,"model":"medium"},{"model":"large"}]}`
	rt, err := ParseComplexityRouting(json.RawMessage(ok), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		score float64
		tier  int
		model string
	}{{0, 0, "small"}, {0.3, 0, "small"}, {0.31, 1, "medium"}, {0.7, 1, "medium"}, {0.95, 2, "large"}} {
		if i, m := rt.Pick(c.score); i != c.tier || m != c.model {
			t.Errorf("Pick(%v) = %d %s", c.score, i, m)
		}
	}
	if !rt.nearBoundary(0.25, 0.1) || rt.nearBoundary(0.5, 0.1) {
		t.Error("nearBoundary")
	}
	_ = f
	for _, bad := range []string{
		`{"tiers":[{"model":"only"}]}`,
		`{"tiers":[{"model":"a"},{"model":"b"}]}`,
		`{"tiers":[{"max_score":0.5,"model":"a"},{"max_score":0.4,"model":"b"},{"model":"c"}]}`,
		`{"tiers":[{"max_score":0.5,"model":"a"},{"max_score":0.5,"model":"b"}]}`,
		`{"tiers":[{"max_score":1.2,"model":"a"},{"model":"b"}]}`,
		`{"tiers":[{"max_score":0.5,"model":""},{"model":"b"}]}`,
		`{"tiers":[{"max_score":0.5,"model":"a"},{"model":"b"}],"classifier":"hybrid"}`,
		`{"tiers":[{"max_score":0.5,"model":"a"},{"model":"b"}],"classifier":"magic"}`,
		`{"tiers":[{"max_score":0.5,"model":"a"},{"model":"b"}],"unknown":1}`,
	} {
		if _, err := ParseComplexityRouting(json.RawMessage(bad), nil); err == nil {
			t.Errorf("want error for %s", bad)
		}
	}
	_, err = ParseComplexityRouting(json.RawMessage(ok), func(n string) error {
		if n == "medium" {
			return errors.New("missing")
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "tier 2") {
		t.Errorf("exists check: %v", err)
	}
}
