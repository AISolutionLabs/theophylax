package providers

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Anthropic speaks the native Messages API. It also backs Claude on Azure AI
// Foundry, which exposes the same API under https://<resource>.services.ai.azure.com/anthropic.
//
//	anthropic://api.anthropic.com?api_key=sk-ant-...         (x-api-key)
//	anthropic://gateway.internal?auth_token=...&base_url=..   (Authorization: Bearer)
//	azure-foundry://<resource>.services.ai.azure.com?auth=api_key|entra|workload|managed
//
// Claude Models on Google Cloud (Anthropic managed) is the same API at a
// project- and workspace-scoped URL, authenticated with a Google access token:
//
//	anthropic://claude.googleapis.com?auth=metadata&project=my-proj&workspace=wrkspc_…
//	anthropic://claude.googleapis.com?auth=workload_identity&project=…&workspace=…&audience=…&token_file=…
//	anthropic://claude.googleapis.com?auth=service_account&project=…&workspace=…&credentials=${file:/secrets/sa.json}
type Anthropic struct {
	baseProvider
	base     string
	apiKey   string
	bearer   string
	azure    *azureToken
	azureKey string
	google   *googleToken
	version  string
	// external: credentials are added by the HTTP transport (custom providers).
	external bool
}

// googleClaudeHost serves Claude Models on Google Cloud (Anthropic managed).
const googleClaudeHost = "claude.googleapis.com"

// googleAuthModes select Google credentials for an anthropic:// provider;
// "google" means Application Default Credentials.
var googleAuthModes = map[string]bool{"google": true, "gcp": true, "metadata": true, "gke": true, "gce": true,
	"workload_identity": true, "federated": true, "external_account": true, "service_account": true}

func newAnthropic(b baseProvider, c *Conn) (*Anthropic, error) {
	if googleAuthModes[strings.ToLower(c.Get("auth"))] {
		return newAnthropicGoogle(b, c)
	}
	p := &Anthropic{baseProvider: b, version: c.Get("anthropic_version")}
	p.base = strings.TrimRight(c.Get("base_url"), "/")
	if p.base == "" {
		host := c.Host
		if host == "" {
			host = "api.anthropic.com"
		}
		p.base = "https://" + host
	}
	p.apiKey = c.Get("api_key")
	p.bearer = c.Get("auth_token")
	if p.apiKey == "" && p.bearer == "" {
		return nil, fmt.Errorf("anthropic: api_key or auth_token is required")
	}
	return p, nil
}

func newAnthropicGoogle(b baseProvider, c *Conn) (*Anthropic, error) {
	gc := &Conn{Kind: c.Kind, Host: c.Host, Params: make(map[string]string, len(c.Params))}
	for k, v := range c.Params {
		gc.Params[k] = v
	}
	if m := strings.ToLower(c.Get("auth")); m == "google" || m == "gcp" {
		gc.Params["auth"] = "default"
	}
	tok, err := newGoogleToken(gc, b.client)
	if err != nil {
		return nil, err
	}
	p := &Anthropic{baseProvider: b, version: c.Get("anthropic_version"), google: tok}
	p.base = strings.TrimRight(c.Get("base_url"), "/")
	if p.base == "" {
		if host := c.Host; host != "" && host != "api.anthropic.com" && host != googleClaudeHost {
			p.base = "https://" + strings.TrimRight(host, "/")
			return p, nil
		}
		project, ws := c.Get("project"), c.Get("workspace")
		if project == "" {
			project = c.Get("project_id")
		}
		if ws == "" {
			ws = c.Get("workspace_id")
		}
		if project == "" || ws == "" {
			return nil, fmt.Errorf("anthropic on Google Cloud: project= and workspace= are required")
		}
		loc := c.Get("location")
		if loc == "" {
			loc = "global"
		}
		p.base = "https://" + googleClaudeHost + "/v1/projects/" + url.PathEscape(project) + "/locations/" + url.PathEscape(loc) + "/workspaces/" + url.PathEscape(ws) + "/invoke"
	}
	return p, nil
}

func newAzureFoundry(b baseProvider, c *Conn) (*Anthropic, error) {
	p := &Anthropic{baseProvider: b, version: c.Get("anthropic_version")}
	p.base = strings.TrimRight(c.Get("base_url"), "/")
	if p.base == "" {
		if c.Host == "" {
			return nil, fmt.Errorf("azure-foundry: resource host required, e.g. azure-foundry://my-res.services.ai.azure.com")
		}
		host := c.Host
		if !strings.Contains(host, ".") {
			host += ".services.ai.azure.com"
		}
		p.base = "https://" + host + "/anthropic"
	}
	key, tok, err := newAzureAuth(c, b.client)
	if err != nil {
		return nil, err
	}
	p.azureKey, p.azure = key, tok
	return p, nil
}

func (p *Anthropic) Format() Format { return FormatAnthropic }

func (p *Anthropic) auth(ctx context.Context, req *http.Request) error {
	switch {
	case p.external:
	case p.google != nil:
		t, err := p.google.Get(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+t)
	case p.azure != nil:
		t, err := p.azure.Get(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+t)
	case p.azureKey != "":
		req.Header.Set("api-key", p.azureKey)
		req.Header.Set("x-api-key", p.azureKey)
	case p.bearer != "":
		req.Header.Set("Authorization", "Bearer "+p.bearer)
	default:
		req.Header.Set("x-api-key", p.apiKey)
	}
	return nil
}

func (p *Anthropic) Send(ctx context.Context, r *Request) (*http.Response, error) {
	body := p.prepBody(r.Body)
	body["model"] = r.UpstreamModel
	if r.Stream {
		body["stream"] = true
	} else {
		delete(body, "stream")
	}
	payload := marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	ver := r.Header.Get("anthropic-version")
	if ver == "" {
		ver = p.version
	}
	if ver == "" {
		ver = "2023-06-01"
	}
	req.Header.Set("anthropic-version", ver)
	for _, b := range r.Header.Values("anthropic-beta") {
		req.Header.Add("anthropic-beta", b)
	}
	if r.Stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	p.applyHeaders(req)
	if err := p.auth(ctx, req); err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// CountTokens proxies /v1/messages/count_tokens (Anthropic API only).
func (p *Anthropic) CountTokens(ctx context.Context, body map[string]any, model string, hdr http.Header) (*http.Response, error) {
	b := p.prepBody(body)
	b["model"] = model
	payload := marshal(b)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/v1/messages/count_tokens", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	for _, v := range hdr.Values("anthropic-beta") {
		req.Header.Add("anthropic-beta", v)
	}
	if err := p.auth(ctx, req); err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	return resp, checkStatus(resp)
}

func (p *Anthropic) ListModels(ctx context.Context) ([]DiscoveredModel, error) {
	if p.kind == KindAzureFoundry {
		return nil, fmt.Errorf("model discovery is not available for Azure AI Foundry; add deployments by name")
	}
	var out []DiscoveredModel
	after := ""
	for i := 0; i < 20; i++ {
		u := p.base + "/v1/models?limit=1000"
		if after != "" {
			u += "&after_id=" + after
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		req.Header.Set("anthropic-version", "2023-06-01")
		if err := p.auth(ctx, req); err != nil {
			return nil, err
		}
		var page struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := getJSON(ctx, p.client, req, &page); err != nil {
			return nil, err
		}
		for _, m := range page.Data {
			out = append(out, DiscoveredModel{ID: m.ID, DisplayName: m.DisplayName, Kind: "foundation"})
		}
		if !page.HasMore || page.LastID == "" {
			break
		}
		after = page.LastID
	}
	return out, nil
}
