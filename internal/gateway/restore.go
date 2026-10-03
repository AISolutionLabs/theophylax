package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"

	"theophylax/internal/guardrails"
	"theophylax/internal/providers"
)

// streamRestorer applies Secret Shield "restore on return" to native upstream
// SSE events before format translation and before they reach the client.
type streamRestorer struct {
	r          *guardrails.Restorer
	blockTypes map[int]string
	toolIdx    map[int]bool
}

func newStreamRestorer(r *guardrails.Restorer) *streamRestorer {
	if r == nil {
		return nil
	}
	return &streamRestorer{r: r, blockTypes: map[int]string{}, toolIdx: map[int]bool{}}
}

// anthropic transforms one Messages API event, possibly into zero or two events.
func (s *streamRestorer) anthropic(ev *providers.SSEEvent) []providers.SSEEvent {
	switch ev.Event {
	case "content_block_start":
		var m struct {
			Index        int `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
			} `json:"content_block"`
		}
		if json.Unmarshal(ev.Data, &m) == nil {
			s.blockTypes[m.Index] = m.ContentBlock.Type
		}
		return []providers.SSEEvent{*ev}
	case "content_block_delta":
		if !bytes.Contains(ev.Data, []byte(`"text_delta"`)) && !bytes.Contains(ev.Data, []byte(`"input_json_delta"`)) {
			return []providers.SSEEvent{*ev}
		}
		var m map[string]any
		if json.Unmarshal(ev.Data, &m) != nil {
			return []providers.SSEEvent{*ev}
		}
		idx := fmt.Sprint(m["index"])
		d, _ := m["delta"].(map[string]any)
		switch d["type"] {
		case "text_delta":
			t, _ := d["text"].(string)
			d["text"] = s.r.Feed(idx, t, false)
			if d["text"] == "" {
				return nil
			}
		case "input_json_delta":
			t, _ := d["partial_json"].(string)
			d["partial_json"] = s.r.Feed(idx, t, true)
			if d["partial_json"] == "" {
				return nil
			}
		}
		b, _ := json.Marshal(m)
		return []providers.SSEEvent{{Event: ev.Event, Data: b}}
	case "content_block_stop":
		var m struct {
			Index int `json:"index"`
		}
		_ = json.Unmarshal(ev.Data, &m)
		bt := s.blockTypes[m.Index]
		rest := s.r.Flush(fmt.Sprint(m.Index), bt == "tool_use")
		if rest == "" {
			return []providers.SSEEvent{*ev}
		}
		delta := map[string]any{"type": "text_delta", "text": rest}
		if bt == "tool_use" {
			delta = map[string]any{"type": "input_json_delta", "partial_json": rest}
		}
		b, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": m.Index, "delta": delta})
		return []providers.SSEEvent{{Event: "content_block_delta", Data: b}, *ev}
	}
	return []providers.SSEEvent{*ev}
}

// openai transforms one chat.completion.chunk.
func (s *streamRestorer) openai(ev *providers.SSEEvent) providers.SSEEvent {
	if len(ev.Data) == 0 || ev.Data[0] != '{' {
		return *ev
	}
	var m map[string]any
	if json.Unmarshal(ev.Data, &m) != nil {
		return *ev
	}
	choices, _ := m["choices"].([]any)
	if len(choices) == 0 {
		return *ev
	}
	c0, _ := choices[0].(map[string]any)
	d, _ := c0["delta"].(map[string]any)
	if d == nil {
		d = map[string]any{}
		c0["delta"] = d
	}
	if t, ok := d["content"].(string); ok {
		d["content"] = s.r.Feed("content", t, false)
	}
	if calls, ok := d["tool_calls"].([]any); ok {
		for _, c := range calls {
			cm, _ := c.(map[string]any)
			idx := int(toFloat(cm["index"]))
			s.toolIdx[idx] = true
			if fn, ok := cm["function"].(map[string]any); ok {
				if a, ok := fn["arguments"].(string); ok {
					fn["arguments"] = s.r.Feed(fmt.Sprintf("tool%d", idx), a, true)
				}
			}
		}
	}
	if fr, _ := c0["finish_reason"].(string); fr != "" {
		if rest := s.r.Flush("content", false); rest != "" {
			cur, _ := d["content"].(string)
			d["content"] = cur + rest
		}
		var extra []any
		for idx := range s.toolIdx {
			if rest := s.r.Flush(fmt.Sprintf("tool%d", idx), true); rest != "" {
				extra = append(extra, map[string]any{"index": idx, "function": map[string]any{"arguments": rest}})
			}
		}
		if len(extra) > 0 {
			calls, _ := d["tool_calls"].([]any)
			d["tool_calls"] = append(calls, extra...)
		}
	}
	b, _ := json.Marshal(m)
	return providers.SSEEvent{Event: ev.Event, Data: b}
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case json.Number:
		f, _ := x.Float64()
		return f
	}
	return 0
}
