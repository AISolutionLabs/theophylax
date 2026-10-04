package modelwatch

import (
	"os"
	"testing"
)

// CATALOG=/path/model_prices_and_context_window.json go test -run TestRealCatalog -v
func TestRealCatalog(t *testing.T) {
	p := os.Getenv("CATALOG")
	if p == "" {
		t.Skip()
	}
	b, _ := os.ReadFile(p)
	c, err := ParseCatalog(b, p)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d models", c.Len())
	for _, q := range [][2]string{
		{"anthropic", "claude-sonnet-4-5-20250929"}, {"anthropic", "claude-opus-4-1-20250805"}, {"bedrock", "us.anthropic.claude-sonnet-4-5-20250929-v1:0"},
		{"bedrock", "eu.anthropic.claude-haiku-4-5-20251001-v1:0"}, {"bedrock", "amazon.titan-embed-text-v2:0"}, {"azure-openai", "gpt-4o"},
		{"vertex", "claude-sonnet-4-5@20250929"}, {"vertex-openai", "google/gemini-2.5-pro"}, {"gemini", "gemini-2.5-flash"}, {"openai", "gpt-4.1-mini"},
	} {
		e, ok := c.Lookup(q[0], q[1])
		if !ok {
			t.Errorf("no catalog entry for %s %s", q[0], q[1])
			continue
		}
		in, out := 0.0, 0.0
		if e.InputPrice != nil {
			in = *e.InputPrice
		}
		if e.OutputPrice != nil {
			out = *e.OutputPrice
		}
		t.Logf("%-14s %-48s → %-52s $%.2f/$%.2f per M  ctx %d", q[0], q[1], e.Key, in, out, e.MaxInput)
	}
}
