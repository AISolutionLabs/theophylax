package providers

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// OpenAI handles OpenAI-compatible chat completions, including Azure OpenAI.
//
//	openai://api.openai.com?api_key=...                        (base_url defaults to https://<host>/v1)
//	openai://vllm.internal:8000?base_url=http://vllm.internal:8000/v1
//	azure-openai://<res>.openai.azure.com?auth=api_key&api_key=..&api_version=2024-10-21
//	azure-openai://<res>.openai.azure.com?auth=entra&...&api_version=v1   (new /openai/v1 surface)
type OpenAI struct {
	baseProvider
	azure      bool
	base       string
	apiVersion string
	apiKey     string
	token      *azureToken
}

func newOpenAI(b baseProvider, c *Conn) (*OpenAI, error) {
	p := &OpenAI{baseProvider: b, azure: c.Kind == KindAzureOpenAI}
	if p.azure {
		if c.Host == "" {
			return nil, fmt.Errorf("azure-openai: resource host required")
		}
		host := c.Host
		if !strings.Contains(host, ".") {
			host += ".openai.azure.com"
		}
		p.base = "https://" + host
		if bu := c.Get("base_url"); bu != "" {
			p.base = strings.TrimRight(bu, "/")
		}
		p.apiVersion = c.Get("api_version")
		if p.apiVersion == "" {
			p.apiVersion = "v1"
		}
		key, tok, err := newAzureAuth(c, b.client)
		if err != nil {
			return nil, err
		}
		p.apiKey, p.token = key, tok
		return p, nil
	}
	p.base = strings.TrimRight(c.Get("base_url"), "/")
	if p.base == "" {
		host := c.Host
		if host == "" {
			host = "api.openai.com"
		}
		p.base = "https://" + host + "/v1"
	}
	p.apiKey = c.Get("api_key")
	return p, nil
}

func (p *OpenAI) Format() Format { return FormatOpenAI }

func (p *OpenAI) endpoint(model, path string) string {
	if !p.azure {
		return p.base + path
	}
	if p.apiVersion == "v1" || p.apiVersion == "preview" {
		return p.base + "/openai/v1" + path
	}
	return p.base + "/openai/deployments/" + url.PathEscape(model) + path + "?api-version=" + url.QueryEscape(p.apiVersion)
}

func (p *OpenAI) auth(ctx context.Context, req *http.Request) error {
	if p.token != nil {
		t, err := p.token.Get(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+t)
		return nil
	}
	if p.apiKey == "" {
		return nil
	}
	if p.azure {
		req.Header.Set("api-key", p.apiKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	return nil
}

func (p *OpenAI) Send(ctx context.Context, r *Request) (*http.Response, error) {
	body := p.prepBody(r.Body)
	body["model"] = r.UpstreamModel
	if r.Stream {
		body["stream"] = true
		so, _ := body["stream_options"].(map[string]any)
		if so == nil {
			so = map[string]any{}
		}
		so["include_usage"] = true
		body["stream_options"] = so
	} else {
		delete(body, "stream")
		delete(body, "stream_options")
	}
	payload := marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(r.UpstreamModel, "/chat/completions"), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
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

func (p *OpenAI) ListModels(ctx context.Context) ([]DiscoveredModel, error) {
	if p.azure && p.apiVersion != "v1" {
		return nil, fmt.Errorf("model discovery requires api_version=v1 on Azure OpenAI")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint("", "/models"), nil)
	if err := p.auth(ctx, req); err != nil {
		return nil, err
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := getJSON(ctx, p.client, req, &out); err != nil {
		return nil, err
	}
	res := make([]DiscoveredModel, 0, len(out.Data))
	for _, m := range out.Data {
		res = append(res, DiscoveredModel{ID: m.ID, DisplayName: m.ID, Kind: "foundation"})
	}
	return res, nil
}
