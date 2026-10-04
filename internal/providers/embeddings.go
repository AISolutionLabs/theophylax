package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
)

// EmbedRequest is an OpenAI-format embeddings call (POST /v1/embeddings).
type EmbedRequest struct {
	Input []string
	Body  map[string]any // original request, for passthrough of dimensions, encoding_format, …
	Model string         // upstream model
}

// EmbedResult is always an OpenAI-format embeddings response.
type EmbedResult struct {
	Body        []byte
	InputTokens int
}

// Embedder is implemented by providers that serve embeddings.
type Embedder interface {
	Embed(ctx context.Context, r *EmbedRequest) (*EmbedResult, error)
}

type embedding struct {
	Object    string    `json:"object"`
	Index     int       `json:"index"`
	Embedding []float64 `json:"embedding"`
}

func openAIEmbedBody(model string, vecs [][]float64, tokens int) []byte {
	data := make([]embedding, len(vecs))
	for i, v := range vecs {
		data[i] = embedding{Object: "embedding", Index: i, Embedding: v}
	}
	return marshal(map[string]any{"object": "list", "data": data, "model": model,
		"usage": map[string]int{"prompt_tokens": tokens, "total_tokens": tokens}})
}

// Embed passes the request through to /embeddings (OpenAI, Azure OpenAI,
// Gemini, Vertex and every OpenAI-compatible server).
func (p *OpenAI) Embed(ctx context.Context, r *EmbedRequest) (*EmbedResult, error) {
	body := p.prepBody(r.Body)
	body["model"] = r.Model
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(r.Model, "/embeddings"), bytes.NewReader(marshal(body)))
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
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return nil, err
	}
	var u struct {
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(b, &u)
	return &EmbedResult{Body: b, InputTokens: u.Usage.PromptTokens}, nil
}

// Embed supports Amazon Titan text embeddings and Cohere Embed on Bedrock,
// translated to the OpenAI response shape.
func (p *Bedrock) Embed(ctx context.Context, r *EmbedRequest) (*EmbedResult, error) {
	m := r.Model
	dims, _ := r.Body["dimensions"].(json.Number)
	invoke := func(payload []byte) ([]byte, http.Header, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+"/model/"+bedrockModelPath(m)+"/invoke", bytes.NewReader(payload))
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		p.applyHeaders(req)
		if err := p.sign(ctx, req, payload, "bedrock"); err != nil {
			return nil, nil, fmt.Errorf("bedrock credentials: %w", err)
		}
		resp, err := p.client.Do(req)
		if err != nil {
			return nil, nil, err
		}
		if err := checkStatus(resp); err != nil {
			return nil, nil, err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		return b, resp.Header, err
	}
	switch {
	case strings.Contains(m, "titan-embed"):
		vecs := make([][]float64, 0, len(r.Input))
		tokens := 0
		for _, in := range r.Input {
			req := map[string]any{"inputText": in}
			if d, err := dims.Int64(); err == nil && d > 0 {
				req["dimensions"] = d
			}
			b, _, err := invoke(marshal(req))
			if err != nil {
				return nil, err
			}
			var out struct {
				Embedding           []float64 `json:"embedding"`
				InputTextTokenCount int       `json:"inputTextTokenCount"`
			}
			if err := json.Unmarshal(b, &out); err != nil {
				return nil, fmt.Errorf("bedrock titan: %w", err)
			}
			vecs = append(vecs, out.Embedding)
			tokens += out.InputTextTokenCount
		}
		return &EmbedResult{Body: openAIEmbedBody(m, vecs, tokens), InputTokens: tokens}, nil
	case strings.Contains(m, "cohere.embed"):
		inputType, _ := r.Body["input_type"].(string)
		if inputType == "" {
			inputType = "search_document"
		}
		b, h, err := invoke(marshal(map[string]any{"texts": r.Input, "input_type": inputType, "embedding_types": []string{"float"}}))
		if err != nil {
			return nil, err
		}
		var out struct {
			Embeddings json.RawMessage `json:"embeddings"`
		}
		if err := json.Unmarshal(b, &out); err != nil {
			return nil, fmt.Errorf("bedrock cohere: %w", err)
		}
		var vecs [][]float64
		if json.Unmarshal(out.Embeddings, &vecs) != nil {
			var typed struct {
				Float [][]float64 `json:"float"`
			}
			if err := json.Unmarshal(out.Embeddings, &typed); err != nil {
				return nil, fmt.Errorf("bedrock cohere: %w", err)
			}
			vecs = typed.Float
		}
		tokens, _ := strconv.Atoi(h.Get("X-Amzn-Bedrock-Input-Token-Count"))
		return &EmbedResult{Body: openAIEmbedBody(m, vecs, tokens), InputTokens: tokens}, nil
	}
	return nil, &UpstreamError{Status: 400, Body: []byte(`{"message":"embeddings on Bedrock support amazon.titan-embed-* and cohere.embed-* models"}`), Header: http.Header{}}
}

// Embed returns deterministic unit vectors so tests and demos are repeatable.
func (m *Mock) Embed(ctx context.Context, r *EmbedRequest) (*EmbedResult, error) {
	vecs := make([][]float64, len(r.Input))
	tokens := 0
	for i, in := range r.Input {
		v := make([]float64, 8)
		var norm float64
		for j := range v {
			h := fnv.New64a()
			fmt.Fprintf(h, "%d:%s", j, in)
			v[j] = float64(h.Sum64()%2000)/1000 - 1
			norm += v[j] * v[j]
		}
		for j := range v {
			v[j] /= math.Sqrt(norm)
		}
		vecs[i] = v
		tokens += len(strings.Fields(in))
	}
	return &EmbedResult{Body: openAIEmbedBody(r.Model, vecs, tokens), InputTokens: tokens}, nil
}

// Forwarder is implemented by OpenAI-format providers for endpoints the
// gateway relays without translation (Responses, images, audio, moderations).
type Forwarder interface {
	Forward(ctx context.Context, path, contentType string, body []byte, model string) (*http.Response, error)
}

// Forward sends a pre-built request body to path (e.g. "/responses").
func (p *OpenAI) Forward(ctx context.Context, path, contentType string, body []byte, model string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(model, path), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
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
