package providers

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// OpenAI Responses API ⇄ Anthropic Messages translation, so Responses clients
// (Codex CLI, the OpenAI Agents SDK, newer OpenAI SDK code) can run on Claude
// through Anthropic, Bedrock, Vertex or Azure AI Foundry.

// ResponsesToAnthropicRequest converts a Responses request body.
func ResponsesToAnthropicRequest(in map[string]any) (map[string]any, error) {
	if id, _ := in["previous_response_id"].(string); id != "" {
		return nil, errors.New("previous_response_id is not supported when the model runs on Claude; send the full conversation in input (Codex CLI does this by default)")
	}
	out := map[string]any{"model": in["model"]}
	var system []string
	if s, _ := in["instructions"].(string); s != "" {
		system = append(system, s)
	}
	var msgs []map[string]any
	push := func(role string, blocks ...map[string]any) {
		if len(blocks) == 0 {
			return
		}
		if n := len(msgs); n > 0 && msgs[n-1]["role"] == role {
			msgs[n-1]["content"] = append(msgs[n-1]["content"].([]any), anySlice(blocks)...)
			return
		}
		msgs = append(msgs, map[string]any{"role": role, "content": anySlice(blocks)})
	}
	switch v := in["input"].(type) {
	case string:
		push("user", map[string]any{"type": "text", "text": v})
	case []any:
		for _, it := range v {
			m, ok := it.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := m["type"].(string)
			switch typ {
			case "function_call", "custom_tool_call":
				input := map[string]any{}
				if typ == "function_call" {
					if a, _ := m["arguments"].(string); a != "" {
						if err := json.Unmarshal([]byte(a), &input); err != nil {
							input = map[string]any{"_raw": a}
						}
					}
				} else {
					input["input"] = m["input"]
				}
				push("assistant", map[string]any{"type": "tool_use", "id": callID(m), "name": m["name"], "input": input})
			case "function_call_output", "custom_tool_call_output":
				push("user", map[string]any{"type": "tool_result", "tool_use_id": callID(m), "content": outputText(m["output"])})
			case "reasoning", "item_reference":
				// Reasoning items are provider-specific; Claude re-derives its own.
			case "", "message":
				role, _ := m["role"].(string)
				blocks := contentBlocks(m["content"], role == "assistant")
				switch role {
				case "system", "developer":
					for _, b := range blocks {
						if t, ok := b["text"].(string); ok {
							system = append(system, t)
						}
					}
				case "assistant":
					push("assistant", blocks...)
				default:
					push("user", blocks...)
				}
			default:
				return nil, fmt.Errorf("input item type %q is not supported when the model runs on Claude", typ)
			}
		}
	}
	if len(msgs) == 0 {
		return nil, errors.New("input is required")
	}
	out["messages"] = anySliceM(msgs)
	if len(system) > 0 {
		out["system"] = strings.Join(system, "\n\n")
	}
	out["max_tokens"] = 8192
	if n, ok := in["max_output_tokens"]; ok && n != nil {
		out["max_tokens"] = n
	}
	for _, k := range []string{"temperature", "top_p", "stream"} {
		if v, ok := in[k]; ok && v != nil {
			out[k] = v
		}
	}
	if md, ok := in["metadata"].(map[string]any); ok {
		if u, ok := md["user_id"].(string); ok {
			out["metadata"] = map[string]any{"user_id": u}
		}
	}
	if u, ok := in["user"].(string); ok && u != "" {
		out["metadata"] = map[string]any{"user_id": u}
	}
	if tools, ok := in["tools"].([]any); ok {
		var ts []any
		for _, t := range tools {
			tm, _ := t.(map[string]any)
			switch tm["type"] {
			case "function":
				params, _ := tm["parameters"].(map[string]any)
				if params == nil {
					params = map[string]any{"type": "object", "properties": map[string]any{}}
				}
				ts = append(ts, map[string]any{"name": tm["name"], "description": tm["description"], "input_schema": params})
			case "custom":
				desc, _ := tm["description"].(string)
				ts = append(ts, map[string]any{"name": tm["name"], "description": desc + "\n\nPut the complete tool input in the `input` string.",
					"input_schema": map[string]any{"type": "object", "properties": map[string]any{"input": map[string]any{"type": "string"}}, "required": []any{"input"}}})
			}
			// Hosted tools (web_search, file_search, code_interpreter…) have no
			// Claude equivalent here and are dropped.
		}
		if len(ts) > 0 {
			out["tools"] = ts
		}
	}
	switch tc := in["tool_choice"].(type) {
	case string:
		switch tc {
		case "required":
			out["tool_choice"] = map[string]any{"type": "any"}
		case "none":
			out["tool_choice"] = map[string]any{"type": "none"}
		}
	case map[string]any:
		if n, _ := tc["name"].(string); n != "" {
			out["tool_choice"] = map[string]any{"type": "tool", "name": n}
		}
	}
	if pt, ok := in["parallel_tool_calls"].(bool); ok && !pt {
		if c, ok := out["tool_choice"].(map[string]any); ok {
			c["disable_parallel_tool_use"] = true
		} else if out["tools"] != nil {
			out["tool_choice"] = map[string]any{"type": "auto", "disable_parallel_tool_use": true}
		}
	}
	return out, nil
}

func callID(m map[string]any) string {
	if s, _ := m["call_id"].(string); s != "" {
		return s
	}
	s, _ := m["id"].(string)
	return s
}

func outputText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var b strings.Builder
		for _, p := range x {
			if pm, ok := p.(map[string]any); ok {
				if t, ok := pm["text"].(string); ok {
					b.WriteString(t)
				}
			}
		}
		return b.String()
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func contentBlocks(c any, assistant bool) []map[string]any {
	switch x := c.(type) {
	case string:
		if x == "" {
			return nil
		}
		return []map[string]any{{"type": "text", "text": x}}
	case []any:
		var out []map[string]any
		for _, p := range x {
			pm, _ := p.(map[string]any)
			switch pm["type"] {
			case "input_text", "output_text", "text":
				if t, _ := pm["text"].(string); t != "" {
					out = append(out, map[string]any{"type": "text", "text": t})
				}
			case "input_image":
				if u, _ := pm["image_url"].(string); u != "" && !assistant {
					if strings.HasPrefix(u, "data:") {
						mt, data, _ := strings.Cut(strings.TrimPrefix(u, "data:"), ";base64,")
						out = append(out, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": mt, "data": data}})
					} else {
						out = append(out, map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": u}})
					}
				}
			case "refusal":
				if t, _ := pm["refusal"].(string); t != "" {
					out = append(out, map[string]any{"type": "text", "text": t})
				}
			}
		}
		return out
	}
	return nil
}

func anySlice(b []map[string]any) []any {
	out := make([]any, len(b))
	for i := range b {
		out[i] = b[i]
	}
	return out
}

func anySliceM(b []map[string]any) []any { return anySlice(b) }

// ResponsesTools records which tool names were declared as custom (freeform)
// tools, so tool_use blocks map back to custom_tool_call items.
func ResponsesCustomTools(in map[string]any) map[string]bool {
	out := map[string]bool{}
	if tools, ok := in["tools"].([]any); ok {
		for _, t := range tools {
			if tm, _ := t.(map[string]any); tm["type"] == "custom" {
				if n, _ := tm["name"].(string); n != "" {
					out[n] = true
				}
			}
		}
	}
	return out
}

func responsesUsage(u Usage) map[string]any {
	in := u.Input + u.CacheRead + u.CacheWrite
	return map[string]any{"input_tokens": in, "input_tokens_details": map[string]any{"cached_tokens": u.CacheRead},
		"output_tokens": u.Output, "output_tokens_details": map[string]any{"reasoning_tokens": 0}, "total_tokens": in + u.Output}
}

// AnthropicToResponses converts a non-streaming Messages response.
func AnthropicToResponses(raw []byte, model string, custom map[string]bool) ([]byte, Usage, error) {
	var m struct {
		ID         string           `json:"id"`
		Content    []map[string]any `json:"content"`
		StopReason string           `json:"stop_reason"`
		Usage      map[string]any   `json:"usage"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, Usage{}, err
	}
	u := AnthropicUsage(m.Usage)
	var output []any
	var text strings.Builder
	for _, b := range m.Content {
		switch b["type"] {
		case "text":
			t, _ := b["text"].(string)
			text.WriteString(t)
		case "tool_use":
			output = append(output, toolItem(b, custom, "completed"))
		}
	}
	if text.Len() > 0 {
		output = append([]any{messageItem("msg_"+strings.TrimPrefix(m.ID, "msg_"), text.String(), "completed")}, output...)
	}
	resp := responseObject(strings.TrimPrefix(m.ID, "msg_"), model, output, u, m.StopReason)
	return marshal(resp), u, nil
}

func messageItem(id, text, status string) map[string]any {
	return map[string]any{"type": "message", "id": id, "status": status, "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
}

func toolItem(b map[string]any, custom map[string]bool, status string) map[string]any {
	id, _ := b["id"].(string)
	name, _ := b["name"].(string)
	if custom[name] {
		in, _ := b["input"].(map[string]any)
		s, _ := in["input"].(string)
		return map[string]any{"type": "custom_tool_call", "id": "ctc_" + id, "call_id": id, "name": name, "input": s, "status": status}
	}
	args := "{}"
	if in, ok := b["input"]; ok && in != nil {
		args = string(marshal(in))
	}
	return map[string]any{"type": "function_call", "id": "fc_" + id, "call_id": id, "name": name, "arguments": args, "status": status}
}

func responseObject(id, model string, output []any, u Usage, stop string) map[string]any {
	if output == nil {
		output = []any{}
	}
	r := map[string]any{"id": "resp_" + id, "object": "response", "created_at": time.Now().Unix(), "status": "completed", "model": model,
		"output": output, "usage": responsesUsage(u), "parallel_tool_calls": true, "tool_choice": "auto", "tools": []any{}, "error": nil, "incomplete_details": nil}
	if stop == "max_tokens" {
		r["status"] = "incomplete"
		r["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
	}
	return r
}

// AnthropicToResponsesStream converts Messages SSE events to Responses SSE events.
type AnthropicToResponsesStream struct {
	Model   string
	Custom  map[string]bool
	id      string
	seq     int
	usage   Usage
	stop    string
	output  []any
	blocks  map[int]*respBlock
	nextOut int
	started bool
}

type respBlock struct {
	kind    string // text | tool | skip
	outIdx  int
	itemID  string
	text    strings.Builder
	toolUse map[string]any
}

func (s *AnthropicToResponsesStream) ev(typ string, m map[string]any) SSEEvent {
	m["type"] = typ
	m["sequence_number"] = s.seq
	s.seq++
	return SSEEvent{Event: typ, Data: marshal(m)}
}

func (s *AnthropicToResponsesStream) snapshot(status string) map[string]any {
	r := responseObject(s.id, s.Model, append([]any{}, s.output...), s.usage, s.stop)
	if status != "" {
		r["status"] = status
		if status == "in_progress" {
			r["usage"] = nil
		}
	}
	return r
}

// Usage returns the token usage seen so far.
func (s *AnthropicToResponsesStream) Usage() Usage { return s.usage }

// Convert maps one Anthropic event to zero or more Responses events.
func (s *AnthropicToResponsesStream) Convert(ev *SSEEvent) []SSEEvent {
	var m map[string]any
	if json.Unmarshal(ev.Data, &m) != nil {
		return nil
	}
	if s.blocks == nil {
		s.blocks = map[int]*respBlock{}
	}
	typ, _ := m["type"].(string)
	if typ == "" {
		typ = ev.Event
	}
	var out []SSEEvent
	switch typ {
	case "message_start":
		msg, _ := m["message"].(map[string]any)
		id, _ := msg["id"].(string)
		s.id = strings.TrimPrefix(id, "msg_")
		if s.id == "" {
			s.id = fmt.Sprint(time.Now().UnixNano())
		}
		if u, ok := msg["usage"].(map[string]any); ok {
			s.usage = AnthropicUsage(u)
		}
		s.started = true
		out = append(out, s.ev("response.created", map[string]any{"response": s.snapshot("in_progress")}),
			s.ev("response.in_progress", map[string]any{"response": s.snapshot("in_progress")}))
	case "content_block_start":
		idx := intOf(m["index"])
		cb, _ := m["content_block"].(map[string]any)
		b := &respBlock{kind: "skip"}
		s.blocks[idx] = b
		switch cb["type"] {
		case "text":
			b.kind, b.outIdx, b.itemID = "text", s.nextOut, fmt.Sprintf("msg_%s_%d", s.id, idx)
			s.nextOut++
			out = append(out, s.ev("response.output_item.added", map[string]any{"output_index": b.outIdx, "item": map[string]any{"type": "message", "id": b.itemID, "status": "in_progress", "role": "assistant", "content": []any{}}}),
				s.ev("response.content_part.added", map[string]any{"item_id": b.itemID, "output_index": b.outIdx, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}}))
		case "tool_use":
			b.kind, b.outIdx, b.toolUse = "tool", s.nextOut, cb
			s.nextOut++
			it := toolItem(map[string]any{"id": cb["id"], "name": cb["name"]}, s.Custom, "in_progress")
			if it["type"] == "function_call" {
				it["arguments"] = ""
			}
			b.itemID, _ = it["id"].(string)
			out = append(out, s.ev("response.output_item.added", map[string]any{"output_index": b.outIdx, "item": it}))
		}
	case "content_block_delta":
		b := s.blocks[intOf(m["index"])]
		d, _ := m["delta"].(map[string]any)
		if b == nil {
			return nil
		}
		switch b.kind {
		case "text":
			t, _ := d["text"].(string)
			if t == "" {
				return nil
			}
			b.text.WriteString(t)
			out = append(out, s.ev("response.output_text.delta", map[string]any{"item_id": b.itemID, "output_index": b.outIdx, "content_index": 0, "delta": t, "logprobs": []any{}}))
		case "tool":
			pj, _ := d["partial_json"].(string)
			if pj == "" {
				return nil
			}
			b.text.WriteString(pj)
			if !s.Custom[fmt.Sprint(b.toolUse["name"])] {
				out = append(out, s.ev("response.function_call_arguments.delta", map[string]any{"item_id": b.itemID, "output_index": b.outIdx, "delta": pj}))
			}
		}
	case "content_block_stop":
		b := s.blocks[intOf(m["index"])]
		if b == nil {
			return nil
		}
		switch b.kind {
		case "text":
			t := b.text.String()
			item := messageItem(b.itemID, t, "completed")
			s.output = append(s.output, item)
			out = append(out, s.ev("response.output_text.done", map[string]any{"item_id": b.itemID, "output_index": b.outIdx, "content_index": 0, "text": t, "logprobs": []any{}}),
				s.ev("response.content_part.done", map[string]any{"item_id": b.itemID, "output_index": b.outIdx, "content_index": 0, "part": map[string]any{"type": "output_text", "text": t, "annotations": []any{}}}),
				s.ev("response.output_item.done", map[string]any{"output_index": b.outIdx, "item": item}))
		case "tool":
			input := map[string]any{}
			if a := b.text.String(); a != "" {
				_ = json.Unmarshal([]byte(a), &input)
			}
			item := toolItem(map[string]any{"id": b.toolUse["id"], "name": b.toolUse["name"], "input": input}, s.Custom, "completed")
			s.output = append(s.output, item)
			if item["type"] == "function_call" {
				out = append(out, s.ev("response.function_call_arguments.done", map[string]any{"item_id": b.itemID, "output_index": b.outIdx, "arguments": item["arguments"]}))
			} else {
				out = append(out, s.ev("response.custom_tool_call_input.done", map[string]any{"item_id": b.itemID, "output_index": b.outIdx, "input": item["input"]}))
			}
			out = append(out, s.ev("response.output_item.done", map[string]any{"output_index": b.outIdx, "item": item}))
		}
	case "message_delta":
		if d, ok := m["delta"].(map[string]any); ok {
			s.stop, _ = d["stop_reason"].(string)
		}
		if u, ok := m["usage"].(map[string]any); ok {
			nu := AnthropicUsage(u)
			if nu.Output > 0 {
				s.usage.Output = nu.Output
			}
			if nu.Input > 0 {
				s.usage.Input, s.usage.CacheRead, s.usage.CacheWrite = nu.Input, nu.CacheRead, nu.CacheWrite
			}
		}
	case "message_stop":
		final := "response.completed"
		if s.stop == "max_tokens" {
			final = "response.incomplete"
		}
		out = append(out, s.ev(final, map[string]any{"response": s.snapshot("")}))
	case "error":
		e, _ := m["error"].(map[string]any)
		out = append(out, s.ev("error", map[string]any{"code": e["type"], "message": e["message"], "param": nil}))
	}
	return out
}

func intOf(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	case int:
		return x
	}
	return 0
}
