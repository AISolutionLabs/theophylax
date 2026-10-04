package pricing

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"theophylax/internal/providers"
)

// A trimmed excerpt of the LiteLLM catalog format.
const fixture = `{
  "sample_spec": {"input_cost_per_token": 0, "litellm_provider": "doc"},
  "claude-sonnet-4-5-20250929": {"litellm_provider": "anthropic", "input_cost_per_token": 3e-06, "output_cost_per_token": 1.5e-05,
    "cache_read_input_token_cost": 3e-07, "cache_creation_input_token_cost": 3.75e-06, "cache_creation_input_token_cost_above_1hr": 6e-06,
    "input_cost_per_token_above_200k_tokens": 6e-06, "output_cost_per_token_above_200k_tokens": 2.25e-05,
    "cache_read_input_token_cost_above_200k_tokens": 6e-07, "cache_creation_input_token_cost_above_200k_tokens": 7.5e-06},
  "anthropic.claude-sonnet-4-5-20250929-v1:0": {"litellm_provider": "bedrock_converse", "input_cost_per_token": 3e-06, "output_cost_per_token": 1.5e-05},
  "us.anthropic.claude-sonnet-4-5-20250929-v1:0": {"litellm_provider": "bedrock_converse", "input_cost_per_token": 3.3e-06, "output_cost_per_token": 1.65e-05},
  "claude-sonnet-4-5-20250929-v1:0": {"litellm_provider": "vertex_ai", "input_cost_per_token": 9e-06, "output_cost_per_token": 9e-05},
  "azure/gpt-4o-2024-08-06": {"litellm_provider": "azure", "input_cost_per_token": 2.5e-06, "output_cost_per_token": 1e-05, "cache_read_input_token_cost": 1.25e-06},
  "gpt-4o": {"litellm_provider": "openai", "input_cost_per_token": 2.5e-06, "output_cost_per_token": 1e-05, "cache_read_input_token_cost": 1.25e-06},
  "azure_ai/claude-opus-4-1": {"litellm_provider": "azure_ai", "input_cost_per_token": 1.5e-05, "output_cost_per_token": 7.5e-05},
  "claude-haiku-4-5": {"litellm_provider": "anthropic", "input_cost_per_token": 1e-06, "output_cost_per_token": 5e-06},
  "text-embedding-3-small": {"litellm_provider": "openai", "input_cost_per_token": 2e-08, "output_cost_per_token": 0},
  "dall-e-3": {"litellm_provider": "openai", "output_cost_per_image": 0.04}
}`

func catalog(t *testing.T) *Catalog {
	t.Helper()
	entries, err := Parse([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	c := New(nil, "off", 0)
	c.set(entries, "test", c.fetched)
	return c
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

func TestParseKeepsOnlyTokenPricedEntries(t *testing.T) {
	c := catalog(t)
	if c.entries["dall-e-3"] != nil {
		t.Fatal("image-only entries should be skipped")
	}
	p := c.entries["claude-sonnet-4-5-20250929"]
	if p == nil || len(p.Tiers) != 1 || p.Tiers[0].Above != 200000 || *p.Tiers[0].Rates.Input != 6e-06 {
		t.Fatalf("tiers: %+v", p)
	}
}

func TestLookup(t *testing.T) {
	c := catalog(t)
	cases := []struct {
		kind string
		ids  []string
		want string
	}{
		{providers.KindAnthropic, []string{"claude-sonnet-4-5-20250929"}, "claude-sonnet-4-5-20250929"},
		{providers.KindBedrock, []string{"anthropic.claude-sonnet-4-5-20250929-v1:0"}, "anthropic.claude-sonnet-4-5-20250929-v1:0"},
		// Regional inference profiles carry their own (premium) list price.
		{providers.KindBedrock, []string{"us.anthropic.claude-sonnet-4-5-20250929-v1:0"}, "us.anthropic.claude-sonnet-4-5-20250929-v1:0"},
		{providers.KindBedrock, []string{"arn:aws:bedrock:us-east-1:123456789012:inference-profile/us.anthropic.claude-sonnet-4-5-20250929-v1:0"}, "us.anthropic.claude-sonnet-4-5-20250929-v1:0"},
		// Azure deployment names are arbitrary; the model the upstream reports is used next.
		{providers.KindAzureOpenAI, []string{"prod-gpt4o", "gpt-4o-2024-08-06"}, "azure/gpt-4o-2024-08-06"},
		{providers.KindAzureFoundry, []string{"claude-opus-4-1"}, "azure_ai/claude-opus-4-1"},
		{providers.KindAzureFoundry, []string{"claude-haiku-4-5"}, "claude-haiku-4-5"},
		{providers.KindOpenAI, []string{"gpt-4o"}, "gpt-4o"},
		// Another provider's entry with the same ID is never used.
		{providers.KindAnthropic, []string{"claude-sonnet-4-5-20250929-v1:0"}, ""},
		{providers.KindOpenAI, []string{"llama3:8b"}, ""},
		{providers.KindAnthropic, []string{"{model}"}, ""},
	}
	for _, tc := range cases {
		got := ""
		if p := c.Lookup(tc.kind, tc.ids...); p != nil {
			got = p.Key
		}
		if got != tc.want {
			t.Errorf("Lookup(%s, %v) = %q, want %q", tc.kind, tc.ids, got, tc.want)
		}
	}
}

func TestCostIsExactListPrice(t *testing.T) {
	c := catalog(t)
	// 1,000 input + 500 output + 2,000 cache reads + 400 cache writes (100 of them 1h).
	u := providers.Usage{Input: 1000, Output: 500, CacheRead: 2000, CacheWrite: 400, CacheWrite1h: 100}
	got, key := c.Cost(providers.KindAnthropic, u, "claude-sonnet-4-5-20250929")
	want := 1000*3e-06 + 500*1.5e-05 + 2000*3e-07 + 300*3.75e-06 + 100*6e-06
	if key != "claude-sonnet-4-5-20250929" || !near(got, want) {
		t.Fatalf("cost = %v (%s), want %v", got, key, want)
	}
}

func TestLongContextTier(t *testing.T) {
	c := catalog(t)
	u := providers.Usage{Input: 150000, CacheRead: 60000, Output: 1000}
	got, _ := c.Cost(providers.KindAnthropic, u, "claude-sonnet-4-5-20250929")
	// Prompt is 210k tokens, so every token is billed at the >200k rate.
	want := 150000*6e-06 + 60000*6e-07 + 1000*2.25e-05
	if !near(got, want) {
		t.Fatalf("tiered cost = %v, want %v", got, want)
	}
	u.Input = 140000 // exactly 200k: base rates
	got, _ = c.Cost(providers.KindAnthropic, u, "claude-sonnet-4-5-20250929")
	if want := 140000*3e-06 + 60000*3e-07 + 1000*1.5e-05; !near(got, want) {
		t.Fatalf("base cost = %v, want %v", got, want)
	}
}

func TestMissingCacheRatesUseInputRate(t *testing.T) {
	c := catalog(t)
	got, _ := c.Cost(providers.KindBedrock, providers.Usage{Input: 10, CacheRead: 10, CacheWrite: 10}, "anthropic.claude-sonnet-4-5-20250929-v1:0")
	if want := 30 * 3e-06; !near(got, want) {
		t.Fatalf("cost = %v, want %v", got, want)
	}
}

func TestUnpricedAndMock(t *testing.T) {
	c := catalog(t)
	if usd, key := c.Cost(providers.KindOpenAI, providers.Usage{Input: 100}, "my-local-llama"); usd != 0 || key != "" {
		t.Fatalf("unknown model should be unpriced: %v %q", usd, key)
	}
	if usd, key := c.Cost(providers.KindMock, providers.Usage{Input: 100}, "mock-echo"); usd != 0 || key != "mock" {
		t.Fatalf("mock: %v %q", usd, key)
	}
}

func TestQuotePerMillion(t *testing.T) {
	q := catalog(t).QuoteFor(providers.KindAnthropic, "claude-sonnet-4-5-20250929")
	if q == nil || *q.Input != 3 || *q.Output != 15 || *q.CacheRead != 0.3 || *q.CacheWrite != 3.75 || *q.CacheWrite1h != 6 {
		t.Fatalf("quote: %+v", q)
	}
}

func TestRefreshFetchesCatalog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(fixture)) }))
	defer srv.Close()
	c := New(nil, srv.URL, 0)
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := c.Status(); st.Models != 10 || st.FetchedAt == nil || st.Source != srv.URL {
		t.Fatalf("status: %+v", st)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nope", 500) }))
	defer bad.Close()
	c.url = bad.URL
	if err := c.Refresh(context.Background()); err == nil || c.Len() != 10 || c.Status().LastError == "" {
		t.Fatal("a failed refresh must keep the previous prices and report the error")
	}
}
