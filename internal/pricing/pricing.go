// Package pricing prices requests from the upstream provider's published list
// prices, keyed by the exact upstream model ID that served the request.
//
// Prices come from a public machine-readable catalog (LiteLLM's
// model_prices_and_context_window.json by default), fetched periodically and
// persisted in Postgres so restarts and replicas without egress keep pricing.
// No markup, multipliers or defaults are applied: every token type is billed
// at the catalog's rate for that model, including long-context tiers and
// 1-hour cache writes.
package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"theophylax/internal/providers"
)

// DefaultURL is the public catalog used when THEOPHYLAX_PRICING_URL is unset.
const DefaultURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// Rates are USD per token. Nil means the catalog does not list that rate.
type Rates struct {
	Input        *float64 `json:"input,omitempty"`
	Output       *float64 `json:"output,omitempty"`
	CacheRead    *float64 `json:"cache_read,omitempty"`
	CacheWrite   *float64 `json:"cache_write,omitempty"`
	CacheWrite1h *float64 `json:"cache_write_1h,omitempty"`
}

// Tier overrides rates once the prompt exceeds Above tokens.
type Tier struct {
	Above int   `json:"above"`
	Rates Rates `json:"rates"`
}

// Price is one catalog entry.
type Price struct {
	Key      string `json:"key"`
	Provider string `json:"provider"`
	Rates    Rates  `json:"rates"`
	Tiers    []Tier `json:"tiers,omitempty"`
}

// Quote is a price in USD per million tokens, for display.
type Quote struct {
	Key          string   `json:"key"`
	Input        *float64 `json:"input"`
	Output       *float64 `json:"output"`
	CacheRead    *float64 `json:"cache_read"`
	CacheWrite   *float64 `json:"cache_write"`
	CacheWrite1h *float64 `json:"cache_write_1h"`
	Tiers        []int    `json:"tiers,omitempty"`
}

// Status describes the loaded catalog.
type Status struct {
	Source    string     `json:"source"`
	FetchedAt *time.Time `json:"fetched_at"`
	Models    int        `json:"models"`
	LastError string     `json:"last_error,omitempty"`
	Refresh   string     `json:"refresh_every"`
}

type Catalog struct {
	pool    *pgxpool.Pool
	url     string
	every   time.Duration
	client  *http.Client
	mu      sync.RWMutex
	entries map[string]*Price
	source  string
	fetched time.Time
	lastErr string
}

// New creates a catalog. url may be http(s)://, file:// or "off" (use the
// stored copy only).
func New(pool *pgxpool.Pool, url string, every time.Duration) *Catalog {
	if url == "" {
		url = DefaultURL
	}
	if every <= 0 {
		every = 6 * time.Hour
	}
	return &Catalog{pool: pool, url: url, every: every, client: &http.Client{Timeout: 30 * time.Second}, entries: map[string]*Price{}}
}

// Init loads the stored catalog, fetching synchronously when none exists yet.
func (c *Catalog) Init(ctx context.Context) {
	if err := c.loadStored(ctx); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("pricing: loading stored catalog", "err", err)
	}
	if c.Len() == 0 && c.url != "off" {
		fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := c.Refresh(fctx); err != nil {
			slog.Warn("pricing: initial fetch failed; requests are unpriced until it succeeds", "url", c.url, "err", err)
		}
	}
}

// Run refreshes the catalog periodically. Each replica refreshes on its own;
// the stored copy is shared.
func (c *Catalog) Run(ctx context.Context) {
	if c.url == "off" {
		return
	}
	t := time.NewTicker(c.every)
	defer t.Stop()
	for {
		if c.stale() {
			fctx, cancel := context.WithTimeout(ctx, time.Minute)
			if err := c.Refresh(fctx); err != nil {
				slog.Warn("pricing: refresh failed; keeping previous prices", "url", c.url, "err", err)
			}
			cancel()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (c *Catalog) stale() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return time.Since(c.fetched) >= c.every
}

// Refresh fetches the catalog from its source and stores it.
func (c *Catalog) Refresh(ctx context.Context) error {
	if c.url == "off" {
		return c.loadStored(ctx)
	}
	raw, err := c.fetch(ctx)
	if err == nil {
		var entries map[string]*Price
		if entries, err = Parse(raw); err == nil && len(entries) == 0 {
			err = errors.New("catalog contains no token prices")
		}
		if err == nil {
			now := time.Now().UTC()
			c.set(entries, c.url, now)
			if c.pool != nil {
				if serr := c.store(ctx, entries, now); serr != nil {
					slog.Warn("pricing: storing catalog", "err", serr)
				}
			}
			slog.Info("pricing: catalog loaded", "source", c.url, "models", len(entries))
			return nil
		}
	}
	c.mu.Lock()
	c.lastErr = err.Error()
	c.mu.Unlock()
	return err
}

func (c *Catalog) fetch(ctx context.Context) ([]byte, error) {
	if p, ok := strings.CutPrefix(c.url, "file://"); ok {
		return os.ReadFile(p)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", c.url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

func (c *Catalog) set(entries map[string]*Price, source string, at time.Time) {
	c.mu.Lock()
	c.entries, c.source, c.fetched, c.lastErr = entries, source, at, ""
	c.mu.Unlock()
}

func (c *Catalog) store(ctx context.Context, entries map[string]*Price, at time.Time) error {
	b, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	_, err = c.pool.Exec(ctx, `INSERT INTO pricing_catalog (id, source, fetched_at, entries) VALUES (1, $1, $2, $3)
		ON CONFLICT (id) DO UPDATE SET source=EXCLUDED.source, fetched_at=EXCLUDED.fetched_at, entries=EXCLUDED.entries`, c.url, at, b)
	return err
}

func (c *Catalog) loadStored(ctx context.Context) error {
	if c.pool == nil {
		return pgx.ErrNoRows
	}
	var source string
	var at time.Time
	var raw []byte
	if err := c.pool.QueryRow(ctx, `SELECT source, fetched_at, entries FROM pricing_catalog WHERE id=1`).Scan(&source, &at, &raw); err != nil {
		return err
	}
	entries := map[string]*Price{}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return err
	}
	c.set(entries, source, at)
	return nil
}

func (c *Catalog) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

func (c *Catalog) Status() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s := Status{Source: c.source, Models: len(c.entries), LastError: c.lastErr, Refresh: c.every.String()}
	if s.Source == "" {
		s.Source = c.url
	}
	if !c.fetched.IsZero() {
		t := c.fetched
		s.FetchedAt = &t
	}
	return s
}

// ---------------------------------------------------------------------------
// Parsing

var tierRe = regexp.MustCompile(`^(input_cost_per_token|output_cost_per_token|cache_read_input_token_cost|cache_creation_input_token_cost|cache_creation_input_token_cost_above_1hr)_above_(\d+)k_tokens$`)

// Parse reads a LiteLLM-format catalog, keeping entries priced per token.
func Parse(raw []byte) (map[string]*Price, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parsing pricing catalog: %w", err)
	}
	out := make(map[string]*Price, len(doc))
	for key, v := range doc {
		var fields map[string]any
		if json.Unmarshal(v, &fields) != nil {
			continue // e.g. "sample_spec" documentation entries
		}
		p := &Price{Key: key}
		p.Provider, _ = fields["litellm_provider"].(string)
		tiers := map[int]*Rates{}
		for f, val := range fields {
			n, ok := val.(float64)
			if !ok {
				continue
			}
			if r := ratePtr(&p.Rates, f); r != nil {
				*r = &n
				continue
			}
			if m := tierRe.FindStringSubmatch(f); m != nil {
				k, _ := strconv.Atoi(m[2])
				if tiers[k] == nil {
					tiers[k] = &Rates{}
				}
				*ratePtr(tiers[k], m[1]) = &n
			}
		}
		if p.Rates.Input == nil && p.Rates.Output == nil {
			continue
		}
		for k, r := range tiers {
			p.Tiers = append(p.Tiers, Tier{Above: k * 1000, Rates: *r})
		}
		sort.Slice(p.Tiers, func(i, j int) bool { return p.Tiers[i].Above < p.Tiers[j].Above })
		out[key] = p
	}
	return out, nil
}

func ratePtr(r *Rates, field string) **float64 {
	switch field {
	case "input_cost_per_token":
		return &r.Input
	case "output_cost_per_token":
		return &r.Output
	case "cache_read_input_token_cost":
		return &r.CacheRead
	case "cache_creation_input_token_cost":
		return &r.CacheWrite
	case "cache_creation_input_token_cost_above_1hr":
		return &r.CacheWrite1h
	}
	return nil
}

// ---------------------------------------------------------------------------
// Lookup

// Catalog provider names accepted for each gateway provider kind, in order of
// preference. Azure AI Foundry bills Claude at Anthropic's list price, so
// Anthropic's entry is an exact fallback there.
var kindProviders = map[string][]string{
	providers.KindAnthropic:    {"anthropic"},
	providers.KindBedrock:      {"bedrock_converse", "bedrock"},
	providers.KindAzureOpenAI:  {"azure"},
	providers.KindAzureFoundry: {"azure_ai", "anthropic"},
	providers.KindOpenAI:       {"openai", "text-completion-openai"},
}

// Key prefixes the catalog uses for each catalog provider.
var providerPrefix = map[string]string{
	"bedrock_converse": "bedrock/", "bedrock": "bedrock/", "azure": "azure/", "azure_ai": "azure_ai/", "anthropic": "anthropic/", "openai": "openai/",
}

// Lookup finds the catalog entry for a provider kind and upstream model ID.
// Several IDs may be given (e.g. the configured ID, then the model name the
// upstream reported); the first match wins.
func (c *Catalog) Lookup(kind string, ids ...string) *Price {
	allowed := kindProviders[kind]
	if len(allowed) == 0 {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, id := range ids {
		for _, cand := range candidates(id) {
			for _, prov := range allowed {
				for _, key := range []string{cand, providerPrefix[prov] + cand} {
					if p := c.entries[key]; p != nil && p.Provider == prov {
						return p
					}
				}
			}
		}
	}
	return nil
}

// candidates normalises an upstream ID: Bedrock ARNs reduce to their model or
// inference-profile ID; a trailing "/" path segment is tried bare.
func candidates(id string) []string {
	id = strings.TrimSpace(id)
	if id == "" || strings.Contains(id, "{model}") {
		return nil
	}
	out := []string{id}
	if strings.HasPrefix(id, "arn:") {
		if i := strings.LastIndex(id, "/"); i >= 0 {
			out = append(out, id[i+1:])
		}
	}
	if l := strings.ToLower(id); l != id {
		out = append(out, l)
	}
	return out
}

// Effective returns the rates that apply for a prompt of the given size.
func (p *Price) Effective(promptTokens int) Rates {
	r := p.Rates
	for _, t := range p.Tiers {
		if promptTokens <= t.Above {
			break
		}
		r = overlay(r, t.Rates)
	}
	return r
}

func overlay(base, o Rates) Rates {
	if o.Input != nil {
		base.Input = o.Input
	}
	if o.Output != nil {
		base.Output = o.Output
	}
	if o.CacheRead != nil {
		base.CacheRead = o.CacheRead
	}
	if o.CacheWrite != nil {
		base.CacheWrite = o.CacheWrite
	}
	if o.CacheWrite1h != nil {
		base.CacheWrite1h = o.CacheWrite1h
	}
	return base
}

// Cost is the exact list-price cost of a usage record. Rates the catalog
// omits fall back to the next most specific listed rate (1h write → write,
// cache read/write → input), never to an invented multiplier.
func (p *Price) Cost(u providers.Usage) float64 {
	r := p.Effective(u.Input + u.CacheRead + u.CacheWrite)
	in := deref(r.Input)
	cr, cw := or(r.CacheRead, in), or(r.CacheWrite, in)
	cw1h := or(r.CacheWrite1h, cw)
	w1h := min(u.CacheWrite1h, u.CacheWrite)
	return float64(u.Input)*in + float64(u.Output)*deref(r.Output) + float64(u.CacheRead)*cr +
		float64(u.CacheWrite-w1h)*cw + float64(w1h)*cw1h
}

// Quote returns the base rates per million tokens.
func (p *Price) Quote() *Quote {
	q := &Quote{Key: p.Key, Input: perM(p.Rates.Input), Output: perM(p.Rates.Output), CacheRead: perM(p.Rates.CacheRead), CacheWrite: perM(p.Rates.CacheWrite), CacheWrite1h: perM(p.Rates.CacheWrite1h)}
	for _, t := range p.Tiers {
		q.Tiers = append(q.Tiers, t.Above)
	}
	return q
}

// Cost prices one request. The mock provider is free; anything the catalog
// cannot identify is unpriced (zero cost, empty key) so it shows up as such.
func (c *Catalog) Cost(kind string, u providers.Usage, ids ...string) (usd float64, key string) {
	if kind == providers.KindMock {
		return 0, "mock"
	}
	p := c.Lookup(kind, ids...)
	if p == nil {
		return 0, ""
	}
	return p.Cost(u), p.Key
}

// QuoteFor returns per-million rates for a kind and upstream ID, or nil.
func (c *Catalog) QuoteFor(kind string, ids ...string) *Quote {
	if kind == providers.KindMock {
		z := 0.0
		return &Quote{Key: "mock", Input: &z, Output: &z, CacheRead: &z, CacheWrite: &z, CacheWrite1h: &z}
	}
	if p := c.Lookup(kind, ids...); p != nil {
		return p.Quote()
	}
	return nil
}

// QuoteKey returns per-million rates for an exact catalog key, or nil.
func (c *Catalog) QuoteKey(key string) *Quote {
	if key == "mock" {
		return c.QuoteFor(providers.KindMock)
	}
	c.mu.RLock()
	p := c.entries[key]
	c.mu.RUnlock()
	if p == nil {
		return nil
	}
	return p.Quote()
}

func deref(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func or(v *float64, def float64) float64 {
	if v == nil {
		return def
	}
	return *v
}

func perM(v *float64) *float64 {
	if v == nil {
		return nil
	}
	// Round away float noise from per-token values (3e-06 * 1e6 = 2.9999999999999996).
	x, _ := strconv.ParseFloat(strconv.FormatFloat(*v*1e6, 'g', 12, 64), 64)
	return &x
}
