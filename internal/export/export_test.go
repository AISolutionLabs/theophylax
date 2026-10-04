package export

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"theophylax/internal/store"
)

func TestExportOTLPAndSplunk(t *testing.T) {
	var mu sync.Mutex
	got := map[string][]byte{}
	hdrs := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got[r.URL.Path] = append(got[r.URL.Path], b...)
		hdrs[r.URL.Path] = r.Header.Get("Authorization")
		mu.Unlock()
	}))
	defer srv.Close()
	t.Setenv("HEC_TOKEN", "tok")
	t.Setenv("THEOPHYLAX_OTLP_ENDPOINT", srv.URL+"/otel")
	t.Setenv("THEOPHYLAX_OTLP_HEADERS", "Authorization=Basic abc")
	t.Setenv("THEOPHYLAX_LOG_SINK_URL", srv.URL+"/services/collector/event")
	t.Setenv("THEOPHYLAX_LOG_SINK_FORMAT", "splunk_hec")
	t.Setenv("THEOPHYLAX_LOG_SINK_HEADERS", "Authorization=Splunk ${env:HEC_TOKEN}")
	e := New(ConfigFromEnv())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	org, model := "org_1", "claude-sonnet"
	e.Request(Request{Log: store.RequestLog{ID: "req_1", TS: time.Now(), OrgID: &org, Endpoint: "/v1/messages", Model: model, UpstreamModel: "eu.anthropic.claude", ProviderKind: "bedrock",
		Status: 200, LatencyMS: 120, TTFBMS: 80, InputTokens: 10, CacheReadTokens: 90, OutputTokens: 5, CostUSD: 0.01, Guardrails: json.RawMessage("[]")},
		TraceParent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01", ReceiptHash: "abcd"})
	e.Audit(store.AuditEntry{ID: "aud_1", TS: time.Now(), Action: "key.create", ActorType: "user"})
	cancel()
	<-done

	var otlp struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []struct {
					TraceID, ParentSpanID, Name string
					Attributes                  []struct {
						Key   string
						Value map[string]any
					}
				}
			}
		}
	}
	if err := json.Unmarshal(got["/otel/v1/traces"], &otlp); err != nil {
		t.Fatalf("otlp: %v %s", err, got["/otel/v1/traces"])
	}
	sp := otlp.ResourceSpans[0].ScopeSpans[0].Spans[0]
	attrs := map[string]any{}
	for _, a := range sp.Attributes {
		for _, v := range a.Value {
			attrs[a.Key] = v
		}
	}
	if sp.TraceID != "0af7651916cd43dd8448eb211c80319c" || sp.ParentSpanID != "b7ad6b7169203331" || sp.Name != "chat claude-sonnet" {
		t.Fatalf("span identity: %+v", sp)
	}
	if attrs["gen_ai.provider.name"] != "aws.bedrock" || attrs["gen_ai.usage.input_tokens"] != "100" || attrs["theophylax.org_id"] != "org_1" || attrs["theophylax.receipt_sha256"] != "abcd" {
		t.Fatalf("attributes: %v", attrs)
	}
	if hdrs["/otel/v1/traces"] != "Basic abc" || hdrs["/services/collector/event"] != "Splunk tok" {
		t.Fatalf("headers: %v", hdrs)
	}
	lines := strings.Split(strings.TrimSpace(string(got["/services/collector/event"])), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"sourcetype":"theophylax:request"`) || !strings.Contains(lines[1], `"sourcetype":"theophylax:audit"`) {
		t.Fatalf("hec: %s", got["/services/collector/event"])
	}
}

func TestEncodeFormats(t *testing.T) {
	items := []any{Request{Log: store.RequestLog{ID: "r", TS: time.Now()}}}
	if b, ct := encodeLogs("elastic_bulk", items); !strings.HasPrefix(string(b), `{"create":{}}`) || ct != "application/x-ndjson" {
		t.Fatalf("bulk: %s", b)
	}
	if b, _ := encodeLogs("", items); !strings.HasPrefix(string(b), `[{`) {
		t.Fatalf("json: %s", b)
	}
	if New(Config{}) != nil {
		t.Fatal("no sinks should mean no exporter")
	}
}
