package providers

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// AWS SigV4 test suite vector "get-vanilla".
func TestSigV4GetVanilla(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://example.amazonaws.com/", nil)
	now, _ := time.Parse("20060102T150405Z", "20150830T123600Z")
	SignV4(req, nil, AWSCreds{AccessKey: "AKIDEXAMPLE", Secret: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}, "us-east-1", "service", now)
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("signature mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestSigV4DoubleEncodesModelPath(t *testing.T) {
	u := "https://bedrock-runtime.us-east-1.amazonaws.com/model/" + bedrockModelPath("us.anthropic.claude-x-v1:0") + "/invoke"
	req, _ := http.NewRequest("POST", u, nil)
	if !strings.Contains(req.URL.EscapedPath(), "%3A0") {
		t.Fatalf("expected escaped colon in %s", req.URL.EscapedPath())
	}
	if got := awsURIEncode(req.URL.EscapedPath(), false); !strings.Contains(got, "%253A0") {
		t.Fatalf("expected double encoding, got %s", got)
	}
}

func TestEventStreamToSSE(t *testing.T) {
	var buf bytes.Buffer
	for _, ev := range []string{`{"type":"message_start","message":{"usage":{"input_tokens":7}}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`} {
		payload, _ := json.Marshal(map[string]string{"bytes": base64.StdEncoding.EncodeToString([]byte(ev))})
		buf.Write(EncodeEventStream(map[string]string{":event-type": "chunk", ":message-type": "event", ":content-type": "application/json"}, payload))
	}
	buf.Write(EncodeEventStream(map[string]string{":message-type": "exception", ":exception-type": "throttlingException"}, []byte(`{"message":"slow down"}`)))
	r := NewSSEReader(bedrockStreamToSSE(io.NopCloser(&buf)))
	var got []string
	for {
		ev, err := r.Next()
		if err != nil {
			break
		}
		got = append(got, ev.Event)
	}
	if strings.Join(got, ",") != "message_start,content_block_delta,error" {
		t.Fatalf("events: %v", got)
	}
}

func TestEventStreamCRC(t *testing.T) {
	f := EncodeEventStream(map[string]string{"a": "b"}, []byte("x"))
	f[len(f)-5] ^= 0xff
	if _, err := readEventStream(bufio.NewReader(bytes.NewReader(f))); err == nil {
		t.Fatal("expected crc error")
	}
}

func TestConnParsing(t *testing.T) {
	c, err := ParseConn("Provider=AzureOpenAI;Endpoint=https://acme.openai.azure.com/;Auth=ApiKey;ApiKey=abcdef123456;ApiVersion=2024-10-21")
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != KindAzureOpenAI || c.Host != "acme.openai.azure.com" || c.Get("api_key") != "abcdef123456" || c.Params["auth"] != "apikey" && c.Params["auth"] != "api_key" {
		t.Fatalf("bad parse: %+v", c)
	}
	if strings.Contains(c.Redacted(), "abcdef12") {
		t.Fatalf("secret leaked: %s", c.Redacted())
	}
	t.Setenv("MY_KEY", "sekret")
	c2, _ := ParseConn("anthropic://api.anthropic.com?api_key=${env:MY_KEY}")
	if c2.Get("api_key") != "sekret" || !strings.Contains(c2.Redacted(), "${env:MY_KEY}") {
		t.Fatalf("env ref: %q %q", c2.Get("api_key"), c2.Redacted())
	}
	c3, _ := ParseConn("bedrock://eu-west-1?auth=irsa")
	if c3.Kind != KindBedrock || c3.Host != "eu-west-1" {
		t.Fatalf("bedrock parse: %+v", c3)
	}
}

func TestTranslateRoundTrip(t *testing.T) {
	oai := map[string]any{"model": "x", "messages": []any{
		map[string]any{"role": "system", "content": "be nice"},
		map[string]any{"role": "user", "content": "weather?"},
		map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": "wx", "arguments": `{"city":"London"}`}}}},
		map[string]any{"role": "tool", "tool_call_id": "c1", "content": "rain"},
	}, "tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "wx", "parameters": map[string]any{"type": "object"}}}}}
	a := OpenAIToAnthropicRequest(oai)
	if a["system"] != "be nice" || a["max_tokens"] != 4096 {
		t.Fatalf("system/max_tokens: %v", a)
	}
	msgs := a["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("want 3 msgs got %d", len(msgs))
	}
	back := AnthropicToOpenAIRequest(a)
	bm := back["messages"].([]any)
	if len(bm) != 4 || asMap(bm[3])["role"] != "tool" {
		t.Fatalf("round trip messages: %s", marshal(bm))
	}
}

func TestAnthropicToOpenAIStream(t *testing.T) {
	s := &AnthropicToOpenAIStream{Model: "m", IncludeUsage: true}
	evs := []string{
		`{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":5}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
	}
	var out []SSEEvent
	for _, e := range evs {
		out = append(out, s.Feed(&SSEEvent{Data: []byte(e)})...)
	}
	out = append(out, s.Finish()...)
	if s.Usage.Input != 5 || s.Usage.Output != 3 {
		t.Fatalf("usage %+v", s.Usage)
	}
	if string(out[len(out)-1].Data) != "[DONE]" || !strings.Contains(string(out[1].Data), `"content":"Hel"`) {
		t.Fatalf("stream: %s", out[1].Data)
	}
}

func TestOpenAIToAnthropicStream(t *testing.T) {
	s := &OpenAIToAnthropicStream{Model: "m"}
	chunks := []string{
		`{"id":"chatcmpl-1","choices":[{"delta":{"role":"assistant","content":"Hi"}}]}`,
		`{"id":"chatcmpl-1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"t1","function":{"name":"f","arguments":"{\"a\""}}]}}]}`,
		`{"id":"chatcmpl-1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":1}"}}]},"finish_reason":"tool_calls"}]}`,
		`{"id":"chatcmpl-1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":4}}`,
	}
	var types []string
	for _, c := range chunks {
		for _, e := range s.Feed(&SSEEvent{Data: []byte(c)}) {
			types = append(types, e.Event)
		}
	}
	for _, e := range s.Finish() {
		types = append(types, e.Event)
	}
	want := "message_start,content_block_start,content_block_delta,content_block_stop,content_block_start,content_block_delta,content_block_delta,content_block_stop,message_delta,message_stop"
	if strings.Join(types, ",") != want {
		t.Fatalf("got %s", strings.Join(types, ","))
	}
	if s.Usage.Input != 10 || s.stop != "tool_use" {
		t.Fatalf("usage/stop %+v %s", s.Usage, s.stop)
	}
}
