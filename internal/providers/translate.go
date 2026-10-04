package providers

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Usage is normalised token accounting.
type Usage struct {
	Input      int `json:"input_tokens"`
	Output     int `json:"output_tokens"`
	CacheRead  int `json:"cache_read_tokens"`
	CacheWrite int `json:"cache_write_tokens"`
	// CacheWrite1h is the part of CacheWrite written with a 1-hour TTL, which
	// is billed at a higher rate than the default 5-minute TTL.
	CacheWrite1h int `json:"cache_write_1h_tokens,omitempty"`
}

func (u Usage) Total() int { return u.Input + u.Output + u.CacheRead + u.CacheWrite }

func num(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	}
	return 0
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// AnthropicUsage reads a Messages API usage object.
func AnthropicUsage(u map[string]any) Usage {
	if u == nil {
		return Usage{}
	}
	return Usage{
		Input:        num(u["input_tokens"]),
		Output:       num(u["output_tokens"]),
		CacheRead:    num(u["cache_read_input_tokens"]),
		CacheWrite:   num(u["cache_creation_input_tokens"]),
		CacheWrite1h: num(asMap(u["cache_creation"])["ephemeral_1h_input_tokens"]),
	}
}

// OpenAIUsage reads a chat.completions usage object. Cached prompt tokens are
// reported separately so they are not double counted as input.
func OpenAIUsage(u map[string]any) Usage {
	if u == nil {
		return Usage{}
	}
	cached := 0
	if d, ok := u["prompt_tokens_details"].(map[string]any); ok {
		cached = num(d["cached_tokens"])
	}
	return Usage{Input: num(u["prompt_tokens"]) - cached, Output: num(u["completion_tokens"]), CacheRead: cached}
}

// ---------------------------------------------------------------------------
// Anthropic request -> OpenAI chat.completions request

func blocksOf(content any) []map[string]any {
	switch c := content.(type) {
	case string:
		return []map[string]any{{"type": "text", "text": c}}
	case []any:
		out := make([]map[string]any, 0, len(c))
		for _, b := range c {
			if m, ok := b.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

func textOfBlocks(content any) string {
	var sb strings.Builder
	for _, b := range blocksOf(content) {
		if str(b["type"]) == "text" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(str(b["text"]))
		}
	}
	return sb.String()
}

func AnthropicToOpenAIRequest(a map[string]any) map[string]any {
	o := map[string]any{}
	var msgs []any
	if sys := textOfBlocks(a["system"]); sys != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": sys})
	}
	rawMsgs, _ := a["messages"].([]any)
	for _, rm := range rawMsgs {
		m, _ := rm.(map[string]any)
		role := str(m["role"])
		blocks := blocksOf(m["content"])
		if role == "assistant" {
			am := map[string]any{"role": "assistant"}
			var text strings.Builder
			var calls []any
			for _, b := range blocks {
				switch str(b["type"]) {
				case "text":
					text.WriteString(str(b["text"]))
				case "tool_use":
					args, _ := json.Marshal(b["input"])
					calls = append(calls, map[string]any{"id": str(b["id"]), "type": "function", "function": map[string]any{"name": str(b["name"]), "arguments": string(args)}})
				}
			}
			if text.Len() > 0 {
				am["content"] = text.String()
			} else {
				am["content"] = nil
			}
			if len(calls) > 0 {
				am["tool_calls"] = calls
			}
			msgs = append(msgs, am)
			continue
		}
		// user: tool_result blocks become role=tool messages, others become content parts.
		var parts []any
		for _, b := range blocks {
			switch str(b["type"]) {
			case "text":
				parts = append(parts, map[string]any{"type": "text", "text": str(b["text"])})
			case "image":
				src, _ := b["source"].(map[string]any)
				switch str(src["type"]) {
				case "base64":
					parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + str(src["media_type"]) + ";base64," + str(src["data"])}})
				case "url":
					parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": str(src["url"])}})
				}
			case "tool_result":
				content := b["content"]
				var ct string
				if s, ok := content.(string); ok {
					ct = s
				} else {
					ct = textOfBlocks(content)
				}
				if b["is_error"] == true {
					ct = "[error] " + ct
				}
				msgs = append(msgs, map[string]any{"role": "tool", "tool_call_id": str(b["tool_use_id"]), "content": ct})
			}
		}
		if len(parts) == 1 {
			if p := parts[0].(map[string]any); p["type"] == "text" {
				msgs = append(msgs, map[string]any{"role": "user", "content": p["text"]})
				continue
			}
		}
		if len(parts) > 0 {
			msgs = append(msgs, map[string]any{"role": "user", "content": parts})
		}
	}
	o["messages"] = msgs
	if v, ok := a["max_tokens"]; ok {
		o["max_tokens"] = v
	}
	for _, k := range []string{"temperature", "top_p"} {
		if v, ok := a[k]; ok {
			o[k] = v
		}
	}
	if ss, ok := a["stop_sequences"].([]any); ok && len(ss) > 0 {
		o["stop"] = ss
	}
	if tools, ok := a["tools"].([]any); ok && len(tools) > 0 {
		var ot []any
		for _, t := range tools {
			tm, _ := t.(map[string]any)
			if tm["input_schema"] == nil { // server tools (web_search etc.) have no OpenAI equivalent
				continue
			}
			ot = append(ot, map[string]any{"type": "function", "function": map[string]any{"name": tm["name"], "description": tm["description"], "parameters": tm["input_schema"]}})
		}
		if len(ot) > 0 {
			o["tools"] = ot
		}
	}
	if tc, ok := a["tool_choice"].(map[string]any); ok {
		switch str(tc["type"]) {
		case "auto":
			o["tool_choice"] = "auto"
		case "any":
			o["tool_choice"] = "required"
		case "none":
			o["tool_choice"] = "none"
		case "tool":
			o["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": tc["name"]}}
		}
	}
	if md, ok := a["metadata"].(map[string]any); ok {
		if uid := str(md["user_id"]); uid != "" {
			o["user"] = uid
		}
	}
	return o
}

var finishToStop = map[string]string{"stop": "end_turn", "length": "max_tokens", "tool_calls": "tool_use", "function_call": "tool_use", "content_filter": "refusal"}
var stopToFinish = map[string]string{"end_turn": "stop", "stop_sequence": "stop", "max_tokens": "length", "tool_use": "tool_calls", "refusal": "content_filter", "pause_turn": "stop"}

// OpenAIToAnthropicResponse converts a chat.completion into a Messages API response.
func OpenAIToAnthropicResponse(b []byte, model string) ([]byte, Usage, error) {
	var o map[string]any
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, Usage{}, err
	}
	usage := OpenAIUsage(asMap(o["usage"]))
	var content []any
	stop := "end_turn"
	if ch, ok := o["choices"].([]any); ok && len(ch) > 0 {
		c0 := asMap(ch[0])
		msg := asMap(c0["message"])
		if t := str(msg["content"]); t != "" {
			content = append(content, map[string]any{"type": "text", "text": t})
		}
		if calls, ok := msg["tool_calls"].([]any); ok {
			for _, c := range calls {
				cm := asMap(c)
				fn := asMap(cm["function"])
				var input any = map[string]any{}
				if args := str(fn["arguments"]); args != "" {
					_ = json.Unmarshal([]byte(args), &input)
				}
				content = append(content, map[string]any{"type": "tool_use", "id": str(cm["id"]), "name": str(fn["name"]), "input": input})
			}
		}
		if s, ok := finishToStop[str(c0["finish_reason"])]; ok {
			stop = s
		}
	}
	if content == nil {
		content = []any{}
	}
	out := map[string]any{
		"id": "msg_" + strings.TrimPrefix(str(o["id"]), "chatcmpl-"), "type": "message", "role": "assistant",
		"model": model, "content": content, "stop_reason": stop, "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": usage.Input, "output_tokens": usage.Output, "cache_read_input_tokens": usage.CacheRead, "cache_creation_input_tokens": 0},
	}
	return marshal(out), usage, nil
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// OpenAIToAnthropicStream converts chat.completion.chunk SSE into Messages SSE.
type OpenAIToAnthropicStream struct {
	Model     string
	started   bool
	blockOpen bool
	blockType string
	index     int
	toolIdx   map[int]int // openai tool index -> anthropic block index
	stop      string
	Usage     Usage
	id        string
}

func (s *OpenAIToAnthropicStream) ev(typ string, v map[string]any) SSEEvent {
	v["type"] = typ
	return SSEEvent{Event: typ, Data: marshal(v)}
}

func (s *OpenAIToAnthropicStream) start(out []SSEEvent) []SSEEvent {
	if s.started {
		return out
	}
	s.started = true
	if s.id == "" {
		s.id = fmt.Sprintf("msg_%d", time.Now().UnixNano())
	}
	return append(out, s.ev("message_start", map[string]any{"message": map[string]any{
		"id": s.id, "type": "message", "role": "assistant", "model": s.Model, "content": []any{},
		"stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
	}}))
}

func (s *OpenAIToAnthropicStream) closeBlock(out []SSEEvent) []SSEEvent {
	if s.blockOpen {
		out = append(out, s.ev("content_block_stop", map[string]any{"index": s.index}))
		s.blockOpen = false
		s.index++
	}
	return out
}

func (s *OpenAIToAnthropicStream) Feed(e *SSEEvent) []SSEEvent {
	var out []SSEEvent
	d := strings.TrimSpace(string(e.Data))
	if d == "" || d == "[DONE]" {
		return nil
	}
	var chunk map[string]any
	if json.Unmarshal(e.Data, &chunk) != nil {
		return nil
	}
	if s.id == "" {
		s.id = "msg_" + strings.TrimPrefix(str(chunk["id"]), "chatcmpl-")
	}
	if s.toolIdx == nil {
		s.toolIdx = map[int]int{}
	}
	if u := asMap(chunk["usage"]); u != nil {
		s.Usage = OpenAIUsage(u)
	}
	out = s.start(out)
	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		return out
	}
	c0 := asMap(choices[0])
	delta := asMap(c0["delta"])
	if t := str(delta["content"]); t != "" {
		if !s.blockOpen || s.blockType != "text" {
			out = s.closeBlock(out)
			out = append(out, s.ev("content_block_start", map[string]any{"index": s.index, "content_block": map[string]any{"type": "text", "text": ""}}))
			s.blockOpen, s.blockType = true, "text"
		}
		out = append(out, s.ev("content_block_delta", map[string]any{"index": s.index, "delta": map[string]any{"type": "text_delta", "text": t}}))
	}
	if calls, ok := delta["tool_calls"].([]any); ok {
		for _, c := range calls {
			cm := asMap(c)
			ti := num(cm["index"])
			fn := asMap(cm["function"])
			bi, seen := s.toolIdx[ti]
			if !seen {
				out = s.closeBlock(out)
				bi = s.index
				s.toolIdx[ti] = bi
				out = append(out, s.ev("content_block_start", map[string]any{"index": bi, "content_block": map[string]any{"type": "tool_use", "id": str(cm["id"]), "name": str(fn["name"]), "input": map[string]any{}}}))
				s.blockOpen, s.blockType = true, "tool_use"
			}
			if args := str(fn["arguments"]); args != "" {
				out = append(out, s.ev("content_block_delta", map[string]any{"index": bi, "delta": map[string]any{"type": "input_json_delta", "partial_json": args}}))
			}
		}
	}
	if fr := str(c0["finish_reason"]); fr != "" {
		s.stop = finishToStop[fr]
		if s.stop == "" {
			s.stop = "end_turn"
		}
	}
	return out
}

func (s *OpenAIToAnthropicStream) Finish() []SSEEvent {
	var out []SSEEvent
	out = s.start(out)
	out = s.closeBlock(out)
	if s.stop == "" {
		s.stop = "end_turn"
	}
	out = append(out, s.ev("message_delta", map[string]any{"delta": map[string]any{"stop_reason": s.stop, "stop_sequence": nil},
		"usage": map[string]any{"input_tokens": s.Usage.Input, "output_tokens": s.Usage.Output, "cache_read_input_tokens": s.Usage.CacheRead}}))
	out = append(out, s.ev("message_stop", map[string]any{}))
	return out
}

// ---------------------------------------------------------------------------
// OpenAI chat.completions request -> Anthropic Messages request

func OpenAIToAnthropicRequest(o map[string]any) map[string]any {
	a := map[string]any{}
	var system []string
	var msgs []map[string]any
	appendMsg := func(role string, blocks []any) {
		if n := len(msgs); n > 0 && msgs[n-1]["role"] == role {
			msgs[n-1]["content"] = append(msgs[n-1]["content"].([]any), blocks...)
			return
		}
		msgs = append(msgs, map[string]any{"role": role, "content": blocks})
	}
	rawMsgs, _ := o["messages"].([]any)
	for _, rm := range rawMsgs {
		m := asMap(rm)
		role := str(m["role"])
		switch role {
		case "system", "developer":
			if s, ok := m["content"].(string); ok {
				system = append(system, s)
			} else {
				system = append(system, openAIText(m["content"]))
			}
		case "user":
			appendMsg("user", openAIParts(m["content"]))
		case "assistant":
			var blocks []any
			if t := openAIText(m["content"]); t != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": t})
			}
			if calls, ok := m["tool_calls"].([]any); ok {
				for _, c := range calls {
					cm := asMap(c)
					fn := asMap(cm["function"])
					var input any = map[string]any{}
					_ = json.Unmarshal([]byte(str(fn["arguments"])), &input)
					blocks = append(blocks, map[string]any{"type": "tool_use", "id": str(cm["id"]), "name": str(fn["name"]), "input": input})
				}
			}
			if len(blocks) > 0 {
				appendMsg("assistant", blocks)
			}
		case "tool", "function":
			appendMsg("user", []any{map[string]any{"type": "tool_result", "tool_use_id": str(m["tool_call_id"]), "content": openAIText(m["content"])}})
		}
	}
	if len(system) > 0 {
		a["system"] = strings.Join(system, "\n\n")
	}
	am := make([]any, len(msgs))
	for i := range msgs {
		am[i] = msgs[i]
	}
	a["messages"] = am
	mt := num(o["max_completion_tokens"])
	if mt == 0 {
		mt = num(o["max_tokens"])
	}
	if mt == 0 {
		mt = 4096
	}
	a["max_tokens"] = mt
	for _, k := range []string{"temperature", "top_p"} {
		if v, ok := o[k]; ok {
			a[k] = v
		}
	}
	switch st := o["stop"].(type) {
	case string:
		a["stop_sequences"] = []any{st}
	case []any:
		a["stop_sequences"] = st
	}
	if tools, ok := o["tools"].([]any); ok {
		var at []any
		for _, t := range tools {
			fn := asMap(asMap(t)["function"])
			if fn == nil {
				continue
			}
			params := fn["parameters"]
			if params == nil {
				params = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			at = append(at, map[string]any{"name": fn["name"], "description": fn["description"], "input_schema": params})
		}
		if len(at) > 0 {
			a["tools"] = at
		}
	}
	switch tc := o["tool_choice"].(type) {
	case string:
		switch tc {
		case "auto":
			a["tool_choice"] = map[string]any{"type": "auto"}
		case "required":
			a["tool_choice"] = map[string]any{"type": "any"}
		case "none":
			a["tool_choice"] = map[string]any{"type": "none"}
		}
	case map[string]any:
		if fn := asMap(tc["function"]); fn != nil {
			a["tool_choice"] = map[string]any{"type": "tool", "name": fn["name"]}
		}
	}
	if u := str(o["user"]); u != "" {
		a["metadata"] = map[string]any{"user_id": u}
	}
	return a
}

func openAIText(c any) string {
	switch x := c.(type) {
	case string:
		return x
	case []any:
		var sb strings.Builder
		for _, p := range x {
			pm := asMap(p)
			if str(pm["type"]) == "text" {
				sb.WriteString(str(pm["text"]))
			}
		}
		return sb.String()
	}
	return ""
}

func openAIParts(c any) []any {
	switch x := c.(type) {
	case string:
		return []any{map[string]any{"type": "text", "text": x}}
	case []any:
		var out []any
		for _, p := range x {
			pm := asMap(p)
			switch str(pm["type"]) {
			case "text":
				out = append(out, map[string]any{"type": "text", "text": str(pm["text"])})
			case "image_url":
				u := str(asMap(pm["image_url"])["url"])
				if strings.HasPrefix(u, "data:") {
					meta, data, _ := strings.Cut(strings.TrimPrefix(u, "data:"), ",")
					out = append(out, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": strings.TrimSuffix(meta, ";base64"), "data": data}})
				} else {
					out = append(out, map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": u}})
				}
			}
		}
		return out
	}
	return nil
}

// AnthropicToOpenAIResponse converts a Messages response into chat.completion.
func AnthropicToOpenAIResponse(b []byte, model string) ([]byte, Usage, error) {
	var a map[string]any
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, Usage{}, err
	}
	usage := AnthropicUsage(asMap(a["usage"]))
	var text strings.Builder
	var calls []any
	for _, bl := range blocksOf(a["content"]) {
		switch str(bl["type"]) {
		case "text":
			text.WriteString(str(bl["text"]))
		case "tool_use":
			args, _ := json.Marshal(bl["input"])
			calls = append(calls, map[string]any{"id": str(bl["id"]), "type": "function", "function": map[string]any{"name": str(bl["name"]), "arguments": string(args)}})
		}
	}
	msg := map[string]any{"role": "assistant", "content": text.String()}
	if len(calls) > 0 {
		msg["tool_calls"] = calls
	}
	fr := stopToFinish[str(a["stop_reason"])]
	if fr == "" {
		fr = "stop"
	}
	out := map[string]any{
		"id": "chatcmpl-" + strings.TrimPrefix(str(a["id"]), "msg_"), "object": "chat.completion", "created": time.Now().Unix(), "model": model,
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": fr}},
		"usage":   openAIUsageObj(usage),
	}
	return marshal(out), usage, nil
}

func openAIUsageObj(u Usage) map[string]any {
	prompt := u.Input + u.CacheRead + u.CacheWrite
	return map[string]any{"prompt_tokens": prompt, "completion_tokens": u.Output, "total_tokens": prompt + u.Output,
		"prompt_tokens_details": map[string]any{"cached_tokens": u.CacheRead}}
}

// AnthropicToOpenAIStream converts Messages SSE into chat.completion.chunk SSE.
type AnthropicToOpenAIStream struct {
	Model        string
	IncludeUsage bool
	id           string
	created      int64
	toolIdx      map[int]int
	nextTool     int
	Usage        Usage
	finish       string
}

func (s *AnthropicToOpenAIStream) chunk(delta map[string]any, finish any) SSEEvent {
	if s.created == 0 {
		s.created = time.Now().Unix()
	}
	return SSEEvent{Data: marshal(map[string]any{
		"id": s.id, "object": "chat.completion.chunk", "created": s.created, "model": s.Model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
	})}
}

func (s *AnthropicToOpenAIStream) Feed(e *SSEEvent) []SSEEvent {
	var ev map[string]any
	if json.Unmarshal(e.Data, &ev) != nil {
		return nil
	}
	if s.toolIdx == nil {
		s.toolIdx = map[int]int{}
	}
	switch str(ev["type"]) {
	case "message_start":
		m := asMap(ev["message"])
		s.id = "chatcmpl-" + strings.TrimPrefix(str(m["id"]), "msg_")
		s.Usage = AnthropicUsage(asMap(m["usage"]))
		return []SSEEvent{s.chunk(map[string]any{"role": "assistant", "content": ""}, nil)}
	case "content_block_start":
		cb := asMap(ev["content_block"])
		if str(cb["type"]) == "tool_use" {
			idx := num(ev["index"])
			ti := s.nextTool
			s.nextTool++
			s.toolIdx[idx] = ti
			return []SSEEvent{s.chunk(map[string]any{"tool_calls": []any{map[string]any{"index": ti, "id": str(cb["id"]), "type": "function", "function": map[string]any{"name": str(cb["name"]), "arguments": ""}}}}, nil)}
		}
	case "content_block_delta":
		d := asMap(ev["delta"])
		switch str(d["type"]) {
		case "text_delta":
			return []SSEEvent{s.chunk(map[string]any{"content": str(d["text"])}, nil)}
		case "input_json_delta":
			ti := s.toolIdx[num(ev["index"])]
			return []SSEEvent{s.chunk(map[string]any{"tool_calls": []any{map[string]any{"index": ti, "function": map[string]any{"arguments": str(d["partial_json"])}}}}, nil)}
		}
	case "message_delta":
		if u := asMap(ev["usage"]); u != nil {
			if v, ok := u["output_tokens"]; ok {
				s.Usage.Output = num(v)
			}
			if v, ok := u["input_tokens"]; ok && num(v) > 0 {
				s.Usage.Input = num(v)
			}
		}
		s.finish = stopToFinish[str(asMap(ev["delta"])["stop_reason"])]
		if s.finish == "" {
			s.finish = "stop"
		}
		return []SSEEvent{s.chunk(map[string]any{}, s.finish)}
	case "error":
		return []SSEEvent{{Data: marshal(map[string]any{"error": ev["error"]})}}
	}
	return nil
}

func (s *AnthropicToOpenAIStream) Finish() []SSEEvent {
	var out []SSEEvent
	if s.IncludeUsage {
		out = append(out, SSEEvent{Data: marshal(map[string]any{"id": s.id, "object": "chat.completion.chunk", "created": s.created, "model": s.Model, "choices": []any{}, "usage": openAIUsageObj(s.Usage)})})
	}
	return append(out, SSEEvent{Data: []byte("[DONE]")})
}
