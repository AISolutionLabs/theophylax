package providers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Vertex serves Claude on Google Cloud Vertex AI through the Anthropic
// Messages API (rawPredict / streamRawPredict).
//
//	vertex://us-east5?project=my-proj&auth=workload_identity&audience=…&token_file=…
//	vertex://global?project=my-proj&credentials=${file:/secrets/sa.json}
//
// Gemini and other Vertex models use the OpenAI-compatible surface instead:
//
//	vertex-openai://us-central1?project=my-proj        (models like google/gemini-2.5-pro)
//	gemini://generativelanguage.googleapis.com?api_key=…   (Google AI Studio)
type Vertex struct {
	baseProvider
	base    string
	region  string
	project string
	token   *googleToken
}

func vertexHost(region string) string {
	if region == "" || region == "global" {
		return "aiplatform.googleapis.com"
	}
	return region + "-aiplatform.googleapis.com"
}

func vertexRegionProject(c *Conn) (string, string, error) {
	region := c.Get("region")
	if region == "" {
		region = c.Host
	}
	project := c.Get("project")
	if project == "" {
		project = c.Get("project_id")
	}
	if region == "" || project == "" {
		return "", "", errors.New("vertex: region (vertex://<region>) and project= are required")
	}
	return region, project, nil
}

func newVertex(b baseProvider, c *Conn) (*Vertex, error) {
	region, project, err := vertexRegionProject(c)
	if err != nil {
		return nil, err
	}
	tok, err := newGoogleToken(c, b.client)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(c.Get("base_url"), "/")
	if base == "" {
		base = "https://" + vertexHost(region)
	}
	return &Vertex{baseProvider: b, base: base, region: region, project: project, token: tok}, nil
}

func (p *Vertex) Format() Format { return FormatAnthropic }

func (p *Vertex) Send(ctx context.Context, r *Request) (*http.Response, error) {
	body := p.prepBody(r.Body)
	delete(body, "model")
	if _, ok := body["anthropic_version"]; !ok {
		body["anthropic_version"] = "vertex-2023-10-16"
	}
	verb := "rawPredict"
	if r.Stream {
		body["stream"] = true
		verb = "streamRawPredict"
	} else {
		delete(body, "stream")
	}
	u := fmt.Sprintf("%s/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:%s", p.base, url.PathEscape(p.project), url.PathEscape(p.region), url.PathEscape(r.UpstreamModel), verb)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(marshal(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for _, b := range r.Header.Values("anthropic-beta") {
		req.Header.Add("anthropic-beta", b)
	}
	if r.Stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	p.applyHeaders(req)
	t, err := p.token.Get(ctx)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+t)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ListModels: Vertex has no API listing the Claude versions enabled in a
// project, so return the publisher's current lineup for convenience.
func (p *Vertex) ListModels(ctx context.Context) ([]DiscoveredModel, error) {
	return nil, errors.New("Vertex does not list enabled Claude models; add them by ID (e.g. claude-sonnet-4-5@20250929)")
}

// newGoogleOpenAI builds the OpenAI-compatible provider for Gemini (AI Studio
// API key) or Vertex AI (Google OAuth).
func newGoogleOpenAI(b baseProvider, c *Conn) (*OpenAI, error) {
	p := &OpenAI{baseProvider: b}
	if c.Kind == KindGemini {
		p.base = strings.TrimRight(c.Get("base_url"), "/")
		if p.base == "" {
			host := c.Host
			if host == "" {
				host = "generativelanguage.googleapis.com"
			}
			p.base = "https://" + host + "/v1beta/openai"
		}
		p.apiKey = c.Get("api_key")
		if p.apiKey == "" {
			return nil, errors.New("gemini: api_key is required")
		}
		return p, nil
	}
	region, project, err := vertexRegionProject(c)
	if err != nil {
		return nil, err
	}
	p.base = strings.TrimRight(c.Get("base_url"), "/")
	if p.base == "" {
		p.base = fmt.Sprintf("https://%s/v1/projects/%s/locations/%s/endpoints/openapi", vertexHost(region), url.PathEscape(project), url.PathEscape(region))
	}
	if p.gtoken, err = newGoogleToken(c, b.client); err != nil {
		return nil, err
	}
	return p, nil
}
