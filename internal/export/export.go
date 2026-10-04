// Package export streams request telemetry and audit events to external
// systems without touching the request path: OpenTelemetry traces over
// OTLP/HTTP (Grafana Tempo via Alloy, Langfuse, Datadog, Honeycomb, Jaeger…)
// and batched JSON logs over HTTP (Splunk HEC, Elastic, Datadog logs, or any
// webhook). Everything is buffered, batched and retried; when a destination
// is down, events are dropped and counted rather than slowing traffic.
package export

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"theophylax/internal/metrics"
	"theophylax/internal/store"
)

var (
	exported = metrics.Default.Counter("theophylax_export_events_total", "Events delivered to external sinks", "sink")
	dropped  = metrics.Default.Counter("theophylax_export_dropped_total", "Events dropped because a sink was unavailable or saturated", "sink")
)

// Request is a completed request as exported (metadata only, never payloads).
type Request struct {
	Log         store.RequestLog
	TraceParent string // W3C traceparent sent by the client, if any
	ReceiptHash string
}

// Config is read from the environment.
//
//	THEOPHYLAX_OTLP_ENDPOINT          e.g. http://alloy:4318 or https://cloud.langfuse.com/api/public/otel
//	THEOPHYLAX_OTLP_HEADERS           k=v,k2=v2 (values may be ${env:NAME})
//	  (OTEL_EXPORTER_OTLP_ENDPOINT / OTEL_EXPORTER_OTLP_HEADERS are honoured too)
//	THEOPHYLAX_LOG_SINK_URL           e.g. https://splunk:8088/services/collector/event
//	THEOPHYLAX_LOG_SINK_FORMAT        json (array, default) | ndjson | splunk_hec | elastic_bulk
//	THEOPHYLAX_LOG_SINK_HEADERS       k=v,… e.g. Authorization=Splunk ${env:HEC_TOKEN}
//	THEOPHYLAX_LOG_SINK_AUDIT         true (default) to also stream the audit log
type Config struct {
	OTLPEndpoint string
	OTLPHeaders  map[string]string
	SinkURL      string
	SinkFormat   string
	SinkHeaders  map[string]string
	SinkAudit    bool
	ServiceName  string
}

func ConfigFromEnv() Config {
	c := Config{
		OTLPEndpoint: firstEnv("THEOPHYLAX_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_ENDPOINT"),
		OTLPHeaders:  parseHeaders(firstEnv("THEOPHYLAX_OTLP_HEADERS", "OTEL_EXPORTER_OTLP_HEADERS")),
		SinkURL:      os.Getenv("THEOPHYLAX_LOG_SINK_URL"),
		SinkFormat:   strings.ToLower(os.Getenv("THEOPHYLAX_LOG_SINK_FORMAT")),
		SinkHeaders:  parseHeaders(os.Getenv("THEOPHYLAX_LOG_SINK_HEADERS")),
		SinkAudit:    os.Getenv("THEOPHYLAX_LOG_SINK_AUDIT") != "false",
		ServiceName:  firstEnv("OTEL_SERVICE_NAME"),
	}
	if c.ServiceName == "" {
		c.ServiceName = "theophylax"
	}
	return c
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func parseHeaders(s string) map[string]string {
	out := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(kv, "=")
		if ok && strings.TrimSpace(k) != "" {
			out[strings.TrimSpace(k)] = resolveEnv(strings.TrimSpace(v))
		}
	}
	return out
}

func resolveEnv(v string) string {
	for {
		i := strings.Index(v, "${env:")
		if i < 0 {
			return v
		}
		j := strings.Index(v[i:], "}")
		if j < 0 {
			return v
		}
		v = v[:i] + os.Getenv(v[i+6:i+j]) + v[i+j+1:]
	}
}

// Exporter fans events out to the configured sinks.
type Exporter struct {
	cfg   Config
	otlp  *batcher
	sink  *batcher
	start time.Time
}

// New returns nil when nothing is configured, so callers can skip work.
func New(cfg Config) *Exporter {
	if cfg.OTLPEndpoint == "" && cfg.SinkURL == "" {
		return nil
	}
	e := &Exporter{cfg: cfg}
	client := &http.Client{Timeout: 10 * time.Second}
	if cfg.OTLPEndpoint != "" {
		url := strings.TrimRight(cfg.OTLPEndpoint, "/")
		if !strings.HasSuffix(url, "/v1/traces") {
			url += "/v1/traces"
		}
		e.otlp = newBatcher("otlp", 512, func(ctx context.Context, items []any) error {
			return post(ctx, client, url, "application/json", cfg.OTLPHeaders, otlpPayload(cfg.ServiceName, items))
		})
	}
	if cfg.SinkURL != "" {
		e.sink = newBatcher("log_sink", 500, func(ctx context.Context, items []any) error {
			body, ct := encodeLogs(cfg.SinkFormat, items)
			return post(ctx, client, cfg.SinkURL, ct, cfg.SinkHeaders, body)
		})
	}
	return e
}

// Run drives the batchers until ctx is done, then flushes what is queued.
func (e *Exporter) Run(ctx context.Context) {
	if e == nil {
		return
	}
	done := make(chan struct{}, 2)
	for _, b := range []*batcher{e.otlp, e.sink} {
		if b != nil {
			go func(b *batcher) { b.run(ctx); done <- struct{}{} }(b)
		} else {
			done <- struct{}{}
		}
	}
	<-done
	<-done
}

// Describe summarises active sinks for the System page (no secrets).
func (e *Exporter) Describe() map[string]any {
	if e == nil {
		return map[string]any{"otlp": nil, "log_sink": nil}
	}
	out := map[string]any{"otlp": nil, "log_sink": nil}
	if e.otlp != nil {
		out["otlp"] = map[string]any{"endpoint": e.cfg.OTLPEndpoint}
	}
	if e.sink != nil {
		out["log_sink"] = map[string]any{"url": e.cfg.SinkURL, "format": orDefault(e.cfg.SinkFormat, "json"), "audit": e.cfg.SinkAudit}
	}
	return out
}

func (e *Exporter) Request(r Request) {
	if e == nil {
		return
	}
	if e.otlp != nil {
		e.otlp.add(r)
	}
	if e.sink != nil {
		e.sink.add(r)
	}
}

func (e *Exporter) Audit(a store.AuditEntry) {
	if e == nil || e.sink == nil || !e.cfg.SinkAudit {
		return
	}
	e.sink.add(a)
}

// --- batching ----------------------------------------------------------------

type batcher struct {
	name string
	max  int
	ch   chan any
	send func(context.Context, []any) error
}

func newBatcher(name string, max int, send func(context.Context, []any) error) *batcher {
	return &batcher{name: name, max: max, ch: make(chan any, 20000), send: send}
}

func (b *batcher) add(v any) {
	select {
	case b.ch <- v:
	default:
		dropped.Inc(b.name)
	}
}

func (b *batcher) run(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	batch := make([]any, 0, b.max)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			fctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err = b.send(fctx, batch)
			cancel()
			if err == nil {
				break
			}
			time.Sleep(time.Duration(200*(1<<attempt)) * time.Millisecond)
		}
		if err != nil {
			slog.Warn("export failed; dropping batch", "sink", b.name, "events", len(batch), "err", err)
			for range batch {
				dropped.Inc(b.name)
			}
		} else {
			for range batch {
				exported.Inc(b.name)
			}
		}
		batch = batch[:0]
	}
	for {
		select {
		case v := <-b.ch:
			batch = append(batch, v)
			if len(batch) >= b.max {
				flush()
			}
		case <-t.C:
			flush()
		case <-ctx.Done():
			for {
				select {
				case v := <-b.ch:
					batch = append(batch, v)
					if len(batch) >= b.max {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

func post(ctx context.Context, c *http.Client, url, ctype string, hdr map[string]string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", ctype)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// --- log sink encoding ---------------------------------------------------------

func logDoc(v any) map[string]any {
	switch x := v.(type) {
	case Request:
		var m map[string]any
		b, _ := json.Marshal(x.Log)
		_ = json.Unmarshal(b, &m)
		m["type"] = "request"
		m["source"] = "theophylax"
		if x.ReceiptHash != "" {
			m["receipt_sha256"] = x.ReceiptHash
		}
		if x.TraceParent != "" {
			m["traceparent"] = x.TraceParent
		}
		return m
	case store.AuditEntry:
		var m map[string]any
		b, _ := json.Marshal(x)
		_ = json.Unmarshal(b, &m)
		m["type"] = "audit"
		m["source"] = "theophylax"
		return m
	}
	return map[string]any{}
}

func docTime(m map[string]any) time.Time {
	s, _ := m["ts"].(string)
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func encodeLogs(format string, items []any) ([]byte, string) {
	var buf bytes.Buffer
	switch format {
	case "ndjson":
		for _, it := range items {
			b, _ := json.Marshal(logDoc(it))
			buf.Write(b)
			buf.WriteByte('\n')
		}
		return buf.Bytes(), "application/x-ndjson"
	case "splunk_hec":
		for _, it := range items {
			d := logDoc(it)
			b, _ := json.Marshal(map[string]any{"time": float64(docTime(d).UnixMicro()) / 1e6, "source": "theophylax", "sourcetype": "theophylax:" + d["type"].(string), "event": d})
			buf.Write(b)
			buf.WriteByte('\n')
		}
		return buf.Bytes(), "application/json"
	case "elastic_bulk":
		for _, it := range items {
			d := logDoc(it)
			d["@timestamp"] = d["ts"]
			buf.WriteString(`{"create":{}}` + "\n")
			b, _ := json.Marshal(d)
			buf.Write(b)
			buf.WriteByte('\n')
		}
		return buf.Bytes(), "application/x-ndjson"
	}
	docs := make([]map[string]any, len(items))
	for i, it := range items {
		docs[i] = logDoc(it)
	}
	b, _ := json.Marshal(docs)
	return b, "application/json"
}

// --- OTLP/HTTP JSON ------------------------------------------------------------

type kv struct {
	Key   string         `json:"key"`
	Value map[string]any `json:"value"`
}

func sAttr(k, v string) kv     { return kv{k, map[string]any{"stringValue": v}} }
func iAttr(k string, v int) kv { return kv{k, map[string]any{"intValue": fmt.Sprint(v)}} }
func fAttr(k string, v float64) kv {
	return kv{k, map[string]any{"doubleValue": v}}
}
func bAttr(k string, v bool) kv { return kv{k, map[string]any{"boolValue": v}} }

// genAIProvider maps provider kinds to OpenTelemetry gen_ai.provider.name values.
func genAIProvider(kind string) string {
	switch kind {
	case "bedrock":
		return "aws.bedrock"
	case "azure-openai":
		return "azure.ai.openai"
	case "azure-foundry":
		return "azure.ai.inference"
	case "vertex", "vertex-openai":
		return "gcp.vertex_ai"
	case "gemini":
		return "gcp.gemini"
	case "":
		return "unknown"
	}
	return kind
}

func operation(endpoint string) string {
	switch {
	case strings.HasSuffix(endpoint, "/embeddings"):
		return "embeddings"
	case strings.Contains(endpoint, "/images"):
		return "generate_content"
	case strings.Contains(endpoint, "/audio"), strings.HasSuffix(endpoint, "/moderations"):
		return "execute"
	}
	return "chat"
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func otlpSpan(r Request) map[string]any {
	l := r.Log
	traceID, parent := randHex(16), ""
	if p := strings.Split(r.TraceParent, "-"); len(p) == 4 && len(p[1]) == 32 && len(p[2]) == 16 {
		traceID, parent = p[1], p[2]
	}
	op := operation(l.Endpoint)
	attrs := []kv{
		sAttr("gen_ai.operation.name", op), sAttr("gen_ai.provider.name", genAIProvider(l.ProviderKind)), sAttr("gen_ai.system", genAIProvider(l.ProviderKind)),
		sAttr("gen_ai.request.model", l.Model), sAttr("gen_ai.response.model", l.UpstreamModel),
		iAttr("gen_ai.usage.input_tokens", l.InputTokens+l.CacheReadTokens+l.CacheWriteTokens), iAttr("gen_ai.usage.output_tokens", l.OutputTokens),
		iAttr("gen_ai.usage.cache_read_input_tokens", l.CacheReadTokens), iAttr("gen_ai.usage.cache_creation_input_tokens", l.CacheWriteTokens),
		iAttr("http.response.status_code", l.Status), sAttr("url.path", l.Endpoint), bAttr("theophylax.stream", l.Stream),
		sAttr("theophylax.request_id", l.ID), fAttr("theophylax.cost_usd", l.CostUSD), iAttr("theophylax.attempts", l.Attempts),
		iAttr("theophylax.ttfb_ms", l.TTFBMS), sAttr("theophylax.model", l.Model),
	}
	for k, v := range map[string]*string{"theophylax.org_id": l.OrgID, "theophylax.team_id": l.TeamID, "theophylax.project_id": l.ProjectID,
		"theophylax.key_id": l.KeyID, "user.id": l.UserID, "theophylax.agent_id": l.AgentID, "theophylax.provider_id": l.ProviderID} {
		if v != nil && *v != "" {
			attrs = append(attrs, sAttr(k, *v))
		}
	}
	if len(l.Guardrails) > 2 {
		attrs = append(attrs, sAttr("theophylax.guardrails", string(l.Guardrails)))
	}
	if r.ReceiptHash != "" {
		attrs = append(attrs, sAttr("theophylax.receipt_sha256", r.ReceiptHash))
	}
	status := map[string]any{"code": 1}
	if l.Status >= 400 {
		status = map[string]any{"code": 2, "message": l.Error}
		attrs = append(attrs, sAttr("error.type", fmt.Sprint(l.Status)))
	}
	end := l.TS.Add(time.Duration(l.LatencyMS) * time.Millisecond)
	span := map[string]any{"traceId": traceID, "spanId": randHex(8), "name": op + " " + l.Model, "kind": 2,
		"startTimeUnixNano": fmt.Sprint(l.TS.UnixNano()), "endTimeUnixNano": fmt.Sprint(end.UnixNano()), "attributes": attrs, "status": status}
	if parent != "" {
		span["parentSpanId"] = parent
	}
	if l.TTFBMS > 0 {
		span["events"] = []any{map[string]any{"name": "first_upstream_byte", "timeUnixNano": fmt.Sprint(l.TS.Add(time.Duration(l.TTFBMS) * time.Millisecond).UnixNano())}}
	}
	return span
}

func otlpPayload(service string, items []any) []byte {
	spans := make([]any, 0, len(items))
	for _, it := range items {
		if r, ok := it.(Request); ok {
			spans = append(spans, otlpSpan(r))
		}
	}
	b, _ := json.Marshal(map[string]any{"resourceSpans": []any{map[string]any{
		"resource":   map[string]any{"attributes": []kv{sAttr("service.name", service), sAttr("telemetry.sdk.name", "theophylax")}},
		"scopeSpans": []any{map[string]any{"scope": map[string]any{"name": "theophylax.gateway"}, "spans": spans}},
	}}})
	return b
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
