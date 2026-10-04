package providers

import (
	"strings"
	"testing"
)

func FuzzParseConn(f *testing.F) {
	for _, s := range []string{"bedrock://us-east-1?auth=irsa", "Provider=AzureOpenAI;Endpoint=https://x;ApiKey=k", "groq://?api_key=x", "vertex://global?project=p", "openai://h?base_url=${env:X}", "a=b;c"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if c, err := ParseConn(s); err == nil {
			_ = c.Redacted()
		}
	})
}

func FuzzSSEReader(f *testing.F) {
	f.Add("event: a\ndata: {\"x\":1}\n\ndata: [DONE]\n\n")
	f.Add(": comment\n\ndata: x\ndata: y\n\n")
	f.Fuzz(func(t *testing.T, s string) {
		r := NewSSEReader(strings.NewReader(s))
		for i := 0; i < 1000; i++ {
			if _, err := r.Next(); err != nil {
				return
			}
		}
	})
}

func FuzzResponsesTranslation(f *testing.F) {
	f.Add(`{"type":"message_start","message":{"id":"m"}}`)
	f.Add(`{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{"}}`)
	f.Fuzz(func(t *testing.T, ev string) {
		s := &AnthropicToResponsesStream{Model: "m"}
		_ = s.Convert(&SSEEvent{Data: []byte(ev)})
		_, _, _ = AnthropicToResponses([]byte(ev), "m", nil)
	})
}
