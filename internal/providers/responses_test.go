package providers

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResponsesRequestTranslation(t *testing.T) {
	in := map[string]any{}
	_ = json.Unmarshal([]byte(`{
	  "model": "gpt-5-codex", "instructions": "You are Codex.", "max_output_tokens": 1000, "stream": true, "parallel_tool_calls": false,
	  "tools": [
	    {"type":"function","name":"shell","description":"run","parameters":{"type":"object","properties":{"command":{"type":"array"}}}},
	    {"type":"custom","name":"apply_patch","description":"patch files"},
	    {"type":"web_search"}
	  ],
	  "input": [
	    {"type":"message","role":"developer","content":[{"type":"input_text","text":"Sandbox: workspace-write"}]},
	    {"type":"message","role":"user","content":[{"type":"input_text","text":"list files"}]},
	    {"type":"reasoning","summary":[]},
	    {"type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"command\":[\"ls\"]}"},
	    {"type":"function_call_output","call_id":"call_1","output":"a.go\nb.go"},
	    {"type":"custom_tool_call","call_id":"call_2","name":"apply_patch","input":"*** Begin Patch"},
	    {"type":"custom_tool_call_output","call_id":"call_2","output":"Done"},
	    {"type":"message","role":"user","content":"thanks"}
	  ]}`), &in)
	out, err := ResponsesToAnthropicRequest(in)
	if err != nil {
		t.Fatal(err)
	}
	if out["system"] != "You are Codex.\n\nSandbox: workspace-write" || out["max_tokens"] == nil || out["stream"] != true {
		t.Fatalf("system/max_tokens/stream: %v", out)
	}
	msgs := out["messages"].([]any)
	roles := []string{}
	for _, m := range msgs {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	// Strict alternation is required by the Messages API.
	if strings.Join(roles, ",") != "user,assistant,user,assistant,user" {
		t.Fatalf("roles %v", roles)
	}
	tu := msgs[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	if tu["type"] != "tool_use" || tu["id"] != "call_1" || tu["input"].(map[string]any)["command"] == nil {
		t.Fatalf("tool_use %v", tu)
	}
	last := msgs[4].(map[string]any)["content"].([]any)
	if last[0].(map[string]any)["type"] != "tool_result" || last[1].(map[string]any)["text"] != "thanks" {
		t.Fatalf("merged user turn %v", last)
	}
	tools := out["tools"].([]any)
	if len(tools) != 2 || out["tool_choice"].(map[string]any)["disable_parallel_tool_use"] != true {
		t.Fatalf("tools %v choice %v", tools, out["tool_choice"])
	}
	if _, err := ResponsesToAnthropicRequest(map[string]any{"model": "x", "previous_response_id": "resp_1", "input": "hi"}); err == nil {
		t.Fatal("previous_response_id should be rejected")
	}
}

func TestAnthropicToResponsesUnary(t *testing.T) {
	raw := []byte(`{"id":"msg_1","content":[{"type":"text","text":"Running it."},{"type":"tool_use","id":"toolu_1","name":"shell","input":{"command":["ls"]}},{"type":"tool_use","id":"toolu_2","name":"apply_patch","input":{"input":"*** Begin Patch"}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"cache_read_input_tokens":90,"output_tokens":7}}`)
	b, u, err := AnthropicToResponses(raw, "gpt-5-codex", map[string]bool{"apply_patch": true})
	if err != nil {
		t.Fatal(err)
	}
	var r map[string]any
	_ = json.Unmarshal(b, &r)
	out := r["output"].([]any)
	if len(out) != 3 || out[0].(map[string]any)["type"] != "message" || out[1].(map[string]any)["arguments"] != `{"command":["ls"]}` || out[2].(map[string]any)["type"] != "custom_tool_call" {
		t.Fatalf("output %s", b)
	}
	us := r["usage"].(map[string]any)
	if us["input_tokens"].(float64) != 100 || us["input_tokens_details"].(map[string]any)["cached_tokens"].(float64) != 90 || u.CacheRead != 90 {
		t.Fatalf("usage %v", us)
	}
}

func TestAnthropicToResponsesStream(t *testing.T) {
	events := []string{
		`{"type":"message_start","message":{"id":"msg_9","usage":{"input_tokens":12,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"shell","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"[\"ls\"]}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`,
		`{"type":"message_stop"}`,
	}
	s := &AnthropicToResponsesStream{Model: "m"}
	var types []string
	var final map[string]any
	for _, e := range events {
		for _, o := range s.Convert(&SSEEvent{Data: []byte(e)}) {
			types = append(types, o.Event)
			if o.Event == "response.completed" {
				_ = json.Unmarshal(o.Data, &final)
			}
		}
	}
	want := "response.created,response.in_progress,response.output_item.added,response.content_part.added,response.output_text.delta,response.output_text.delta," +
		"response.output_text.done,response.content_part.done,response.output_item.done,response.output_item.added,response.function_call_arguments.delta," +
		"response.function_call_arguments.delta,response.function_call_arguments.done,response.output_item.done,response.completed"
	if strings.Join(types, ",") != want {
		t.Fatalf("events:\n%s\nwant\n%s", strings.Join(types, ","), want)
	}
	resp := final["response"].(map[string]any)
	out := resp["output"].([]any)
	if len(out) != 2 || out[1].(map[string]any)["arguments"] != `{"command":["ls"]}` || resp["usage"].(map[string]any)["output_tokens"].(float64) != 9 {
		t.Fatalf("final %v", resp)
	}
}
