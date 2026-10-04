package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"theophylax/internal/providers"
	"theophylax/internal/util"
)

// The Responses bridge lets Responses API clients use models served by
// Anthropic-format providers. The request is translated to the Messages API
// and run through the normal inference pipeline (failover, guardrails, Secret
// Shield, rollouts, budgets, receipts); a writer placed beneath the receipt
// hasher translates the output back, so receipts attest to the bytes the
// client actually received.

type bridgeKey struct{}

type bridge struct {
	model     string
	custom    map[string]bool
	reqDigest string
}

func bridgeFrom(ctx context.Context) *bridge {
	b, _ := ctx.Value(bridgeKey{}).(*bridge)
	return b
}

// responses dispatches /v1/responses: relay when every deployment speaks the
// OpenAI wire format, bridge to Messages otherwise.
func (g *Gateway) responses(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, g.Cfg.MaxBodyBytes))
	if err != nil {
		writeErr(w, providers.FormatOpenAI, util.Err(413, "request_too_large", "request body too large or unreadable"))
		return
	}
	var body map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&body)
	model, _ := body["model"].(string)
	route := g.Loader.Snapshot().Resolve(model)
	if body == nil || route == nil || !needsBridge(route) {
		r.Body = io.NopCloser(bytes.NewReader(raw))
		g.relay(w, r)
		return
	}
	msgBody, err := providers.ResponsesToAnthropicRequest(body)
	if err != nil {
		writeErr(w, providers.FormatOpenAI, util.Err(400, "invalid_request_error", err.Error()))
		return
	}
	d := sha256.Sum256(raw)
	b := &bridge{model: model, custom: providers.ResponsesCustomTools(body), reqDigest: hex.EncodeToString(d[:])}
	nb, _ := json.Marshal(msgBody)
	r2 := r.WithContext(context.WithValue(r.Context(), bridgeKey{}, b))
	r2.Body = io.NopCloser(bytes.NewReader(nb))
	r2.ContentLength = int64(len(nb))
	g.inference(w, r2, providers.FormatAnthropic, "")
}

func needsBridge(rt *Route) bool {
	for _, d := range rt.Deps {
		if d.Provider == nil || d.Provider.P == nil {
			continue
		}
		if _, ok := d.Provider.P.(providers.Forwarder); !ok {
			return true
		}
	}
	return false
}

// responsesWriter converts Messages output (JSON or SSE) to Responses output.
type responsesWriter struct {
	http.ResponseWriter
	b       *bridge
	status  int
	sse     bool
	conv    *providers.AnthropicToResponsesStream
	pending []byte
	wrote   bool
}

func newResponsesWriter(w http.ResponseWriter, b *bridge) *responsesWriter {
	return &responsesWriter{ResponseWriter: w, b: b, conv: &providers.AnthropicToResponsesStream{Model: b.model, Custom: b.custom}}
}

func (w *responsesWriter) WriteHeader(code int) {
	w.status = code
	w.sse = strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream")
	if w.sse || code < 300 {
		if !w.sse {
			w.Header().Del("Content-Length")
		}
		w.ResponseWriter.WriteHeader(code)
		w.wrote = true
	}
}

func (w *responsesWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	if w.sse {
		w.pending = append(w.pending, p...)
		for {
			i := bytes.Index(w.pending, []byte("\n\n"))
			if i < 0 {
				break
			}
			frame := w.pending[:i+2]
			w.pending = w.pending[i+2:]
			ev, err := providers.NewSSEReader(bytes.NewReader(frame)).Next()
			if err != nil || ev == nil {
				continue
			}
			for _, out := range w.conv.Convert(ev) {
				if err := providers.WriteSSE(w.ResponseWriter, out); err != nil {
					return 0, err
				}
			}
		}
		return len(p), nil
	}
	if w.status >= 300 {
		// Messages error envelope → OpenAI error envelope.
		var e struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(p, &e)
		out, _ := json.Marshal(map[string]any{"error": map[string]any{"message": e.Error.Message, "type": e.Error.Type, "param": nil, "code": nil}})
		w.Header().Set("Content-Type", "application/json")
		w.ResponseWriter.WriteHeader(w.status)
		_, err := w.ResponseWriter.Write(out)
		return len(p), err
	}
	out, _, err := providers.AnthropicToResponses(p, w.b.model, w.b.custom)
	if err != nil {
		return 0, err
	}
	_, err = w.ResponseWriter.Write(out)
	return len(p), err
}

func (w *responsesWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
