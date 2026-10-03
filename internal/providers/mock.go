package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Mock is a built-in provider for demos, load tests and CI. It echoes the last
// user message in Anthropic format, with optional latency and failure injection.
//
//	mock://local?latency_ms=40&fail_rate=0.0&fail_status=529
type Mock struct {
	baseProvider
	latency    time.Duration
	failRate   float64
	failStatus int
}

func newMock(b baseProvider, c *Conn) *Mock {
	m := &Mock{baseProvider: b, failStatus: 529}
	if v, err := strconv.Atoi(c.Get("latency_ms")); err == nil {
		m.latency = time.Duration(v) * time.Millisecond
	}
	if v, err := strconv.ParseFloat(c.Get("fail_rate"), 64); err == nil {
		m.failRate = v
	}
	if v, err := strconv.Atoi(c.Get("fail_status")); err == nil {
		m.failStatus = v
	}
	return m
}

func (m *Mock) Format() Format { return FormatAnthropic }

func (m *Mock) ListModels(context.Context) ([]DiscoveredModel, error) {
	return []DiscoveredModel{{ID: "mock-echo", DisplayName: "Mock Echo", Kind: "foundation"}}, nil
}

func (m *Mock) Send(ctx context.Context, r *Request) (*http.Response, error) {
	if m.latency > 0 {
		select {
		case <-time.After(m.latency):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if m.failRate > 0 && rand.Float64() < m.failRate {
		return nil, &UpstreamError{Status: m.failStatus, Body: []byte(`{"type":"error","error":{"type":"overloaded_error","message":"mock injected failure"}}`), Header: http.Header{}}
	}
	last := ""
	msgs, _ := r.Body["messages"].([]any)
	if len(msgs) > 0 {
		last = textOfBlocks(asMap(msgs[len(msgs)-1])["content"])
	}
	inTok := 0
	for _, x := range msgs {
		inTok += len(strings.Fields(textOfBlocks(asMap(x)["content"])))
	}
	inTok += len(strings.Fields(textOfBlocks(r.Body["system"])))
	reply := fmt.Sprintf("Echo from %s: %s", r.UpstreamModel, last)
	// Demo judge behaviour so rollouts and hybrid guardrails work end to end without a real model.
	if sys := textOfBlocks(r.Body["system"]); strings.Contains(sys, `"winner"`) {
		reply = []string{`{"winner": "A", "reason": "mock judge preferred A"}`, `{"winner": "B", "reason": "mock judge preferred B"}`, `{"winner": "tie", "reason": "mock judge saw no difference"}`}[rand.IntN(3)]
	} else if strings.Contains(sys, "security classifier") {
		reply = `{"injection": false, "confidence": 0.8, "category": "none"}`
		if strings.Contains(strings.ToLower(last), "ignore") {
			reply = `{"injection": true, "confidence": 0.9, "category": "instruction_override"}`
		}
	}
	words := strings.Fields(reply)
	id := fmt.Sprintf("msg_mock%d", time.Now().UnixNano())
	if !r.Stream {
		b, _ := json.Marshal(map[string]any{"id": id, "type": "message", "role": "assistant", "model": r.UpstreamModel,
			"content": []any{map[string]any{"type": "text", "text": reply}}, "stop_reason": "end_turn", "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": inTok, "output_tokens": len(words)}})
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(b))}, nil
	}
	pr, pw := io.Pipe()
	go func() {
		w := func(typ string, v map[string]any) {
			v["type"] = typ
			_ = WriteSSE(pw, SSEEvent{Event: typ, Data: marshal(v)})
		}
		w("message_start", map[string]any{"message": map[string]any{"id": id, "type": "message", "role": "assistant", "model": r.UpstreamModel, "content": []any{}, "stop_reason": nil, "usage": map[string]any{"input_tokens": inTok, "output_tokens": 1}}})
		w("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		for i, wd := range words {
			if i > 0 {
				wd = " " + wd
			}
			w("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": wd}})
			select {
			case <-ctx.Done():
				pw.CloseWithError(ctx.Err())
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
		w("content_block_stop", map[string]any{"index": 0})
		w("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]any{"output_tokens": len(words)}})
		w("message_stop", map[string]any{})
		pw.Close()
	}()
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: pr}, nil
}
