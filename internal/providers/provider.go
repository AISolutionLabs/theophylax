// Package providers implements upstream model provider adapters (Anthropic,
// AWS Bedrock, Azure AI Foundry, Azure OpenAI, OpenAI-compatible, custom
// credential providers) and the
// request/response translation between Anthropic and OpenAI wire formats.
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Format string

const (
	FormatAnthropic Format = "anthropic"
	FormatOpenAI    Format = "openai"
)

// Request is an inference call already shaped in the provider's native Format.
type Request struct {
	Body          map[string]any
	Stream        bool
	UpstreamModel string
	Header        http.Header // client headers worth forwarding (anthropic-version, anthropic-beta)
}

type DiscoveredModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Kind        string `json:"kind"` // foundation | inference_profile | deployment
}

type Provider interface {
	ID() string
	Kind() string
	Format() Format
	// Send performs the call. Streaming responses are always returned as SSE
	// in the provider's native Format (Bedrock event-stream is converted).
	Send(ctx context.Context, r *Request) (*http.Response, error)
	ListModels(ctx context.Context) ([]DiscoveredModel, error)
}

type Options struct {
	ID         string
	Timeout    time.Duration
	DropParams []string
	Headers    map[string]string
	// Tokens shares custom-provider access/refresh tokens across replicas.
	Tokens TokenStore
}

// New builds a provider from a connection string.
func New(conn string, opt Options) (Provider, error) {
	c, err := ParseConn(conn)
	if err != nil {
		return nil, err
	}
	if opt.Timeout == 0 {
		opt.Timeout = 10 * time.Minute
	}
	client, err := c.HTTPClient(opt.Timeout)
	if err != nil {
		return nil, err
	}
	base := baseProvider{id: opt.ID, kind: c.Kind, client: client, drop: opt.DropParams, headers: opt.Headers, conn: c}
	if c.Get("auth") == "oauth" && (c.Kind == KindAnthropic || c.Kind == KindOpenAI) {
		return newOAuthProvider(base, c, opt)
	}
	switch c.Kind {
	case KindAnthropic:
		return newAnthropic(base, c)
	case KindAzureFoundry:
		return newAzureFoundry(base, c)
	case KindBedrock:
		return newBedrock(base, c)
	case KindAzureOpenAI, KindOpenAI:
		return newOpenAI(base, c)
	case KindVertex:
		return newVertex(base, c)
	case KindVertexOpenAI, KindGemini:
		return newGoogleOpenAI(base, c)
	case KindCustom:
		return newCustom(base, c, opt)
	case KindMock:
		return newMock(base, c), nil
	}
	return nil, fmt.Errorf("unsupported provider kind %q", c.Kind)
}

type baseProvider struct {
	id      string
	kind    string
	client  *http.Client
	drop    []string
	headers map[string]string
	conn    *Conn
}

func (b *baseProvider) ID() string   { return b.id }
func (b *baseProvider) Kind() string { return b.kind }

func (b *baseProvider) prepBody(body map[string]any) map[string]any {
	out := make(map[string]any, len(body))
	for k, v := range body {
		out[k] = v
	}
	for _, d := range b.drop {
		delete(out, d)
	}
	return out
}

func (b *baseProvider) applyHeaders(req *http.Request) {
	for k, v := range b.headers {
		req.Header.Set(k, resolveRefs(v))
	}
}

func marshal(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// UpstreamError carries a non-2xx upstream response for retry decisions.
type UpstreamError struct {
	Status int
	Body   []byte
	Header http.Header
}

func (e *UpstreamError) Error() string {
	msg := string(e.Body)
	if len(msg) > 500 {
		msg = msg[:500]
	}
	return fmt.Sprintf("upstream status %d: %s", e.Status, msg)
}

// Retryable reports whether another deployment should be attempted.
func (e *UpstreamError) Retryable() bool {
	switch e.Status {
	case 408, 409, 425, 429, 500, 502, 503, 504, 529:
		return true
	}
	return false
}

// Message extracts a human-readable message from common upstream error envelopes.
func (e *UpstreamError) Message() string {
	var m map[string]any
	if json.Unmarshal(e.Body, &m) == nil {
		if er, ok := m["error"].(map[string]any); ok {
			if s, ok := er["message"].(string); ok {
				return s
			}
		}
		for _, k := range []string{"message", "Message"} {
			if s, ok := m[k].(string); ok {
				return s
			}
		}
	}
	s := strings.TrimSpace(string(e.Body))
	if len(s) > 300 {
		s = s[:300]
	}
	if s == "" {
		s = http.StatusText(e.Status)
	}
	return s
}

func checkStatus(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	return &UpstreamError{Status: resp.StatusCode, Body: b, Header: resp.Header}
}

func getJSON(ctx context.Context, c *http.Client, req *http.Request, out any) error {
	resp, err := c.Do(req.WithContext(ctx))
	if err != nil {
		return err
	}
	if err := checkStatus(resp); err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}
