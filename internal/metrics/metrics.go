// Package metrics is a tiny dependency-free Prometheus exposition registry.
package metrics

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

type labelKey string

func key(labels []string) labelKey { return labelKey(strings.Join(labels, "\xff")) }

type Counter struct {
	name, help string
	labels     []string
	mu         sync.RWMutex
	vals       map[labelKey]*atomic.Uint64 // float64 bits
	lv         map[labelKey][]string
}

func (c *Counter) Add(v float64, labels ...string) {
	k := key(labels)
	c.mu.RLock()
	p := c.vals[k]
	c.mu.RUnlock()
	if p == nil {
		c.mu.Lock()
		if p = c.vals[k]; p == nil {
			p = new(atomic.Uint64)
			c.vals[k] = p
			c.lv[k] = append([]string(nil), labels...)
		}
		c.mu.Unlock()
	}
	for {
		old := p.Load()
		if p.CompareAndSwap(old, math.Float64bits(math.Float64frombits(old)+v)) {
			return
		}
	}
}

func (c *Counter) Inc(labels ...string) { c.Add(1, labels...) }

type Histogram struct {
	name, help string
	labels     []string
	buckets    []float64
	mu         sync.Mutex
	series     map[labelKey]*hseries
}

type hseries struct {
	lv     []string
	counts []uint64
	sum    float64
	n      uint64
}

func (h *Histogram) Observe(v float64, labels ...string) {
	k := key(labels)
	h.mu.Lock()
	s := h.series[k]
	if s == nil {
		s = &hseries{lv: append([]string(nil), labels...), counts: make([]uint64, len(h.buckets))}
		h.series[k] = s
	}
	for i, b := range h.buckets {
		if v <= b {
			s.counts[i]++
		}
	}
	s.sum += v
	s.n++
	h.mu.Unlock()
}

type Gauge struct {
	name, help string
	fn         func() float64
}

type Registry struct {
	mu         sync.Mutex
	counters   []*Counter
	histograms []*Histogram
	gauges     []*Gauge
}

func (r *Registry) Counter(name, help string, labels ...string) *Counter {
	c := &Counter{name: name, help: help, labels: labels, vals: map[labelKey]*atomic.Uint64{}, lv: map[labelKey][]string{}}
	r.mu.Lock()
	r.counters = append(r.counters, c)
	r.mu.Unlock()
	return c
}

func (r *Registry) Histogram(name, help string, buckets []float64, labels ...string) *Histogram {
	h := &Histogram{name: name, help: help, labels: labels, buckets: buckets, series: map[labelKey]*hseries{}}
	r.mu.Lock()
	r.histograms = append(r.histograms, h)
	r.mu.Unlock()
	return h
}

func (r *Registry) GaugeFunc(name, help string, fn func() float64) {
	r.mu.Lock()
	r.gauges = append(r.gauges, &Gauge{name: name, help: help, fn: fn})
	r.mu.Unlock()
}

func esc(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

func fmtLabels(names, vals []string, extra ...string) string {
	var parts []string
	for i, n := range names {
		v := ""
		if i < len(vals) {
			v = vals[i]
		}
		parts = append(parts, fmt.Sprintf(`%s="%s"`, n, esc(v)))
	}
	for i := 0; i+1 < len(extra); i += 2 {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, extra[i], esc(extra[i+1])))
	}
	if len(parts) == 0 {
		return ""
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func (r *Registry) Write(w io.Writer) {
	r.mu.Lock()
	cs, hs, gs := r.counters, r.histograms, r.gauges
	r.mu.Unlock()
	for _, c := range cs {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", c.name, c.help, c.name)
		c.mu.RLock()
		keys := make([]string, 0, len(c.vals))
		for k := range c.vals {
			keys = append(keys, string(k))
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "%s%s %g\n", c.name, fmtLabels(c.labels, c.lv[labelKey(k)]), math.Float64frombits(c.vals[labelKey(k)].Load()))
		}
		c.mu.RUnlock()
	}
	for _, h := range hs {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s histogram\n", h.name, h.help, h.name)
		h.mu.Lock()
		for _, s := range h.series {
			for i, b := range h.buckets {
				fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, fmtLabels(h.labels, s.lv, "le", fmt.Sprint(b)), s.counts[i])
			}
			fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, fmtLabels(h.labels, s.lv, "le", "+Inf"), s.n)
			fmt.Fprintf(w, "%s_sum%s %g\n%s_count%s %d\n", h.name, fmtLabels(h.labels, s.lv), s.sum, h.name, fmtLabels(h.labels, s.lv), s.n)
		}
		h.mu.Unlock()
	}
	for _, g := range gs {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %g\n", g.name, g.help, g.name, g.name, g.fn())
	}
}

func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		r.Write(w)
	})
}

// Gateway metrics.
var (
	Default        = &Registry{}
	Requests       = Default.Counter("theophylax_requests_total", "Inference requests", "model", "provider", "status")
	Tokens         = Default.Counter("theophylax_tokens_total", "Tokens processed", "model", "type")
	Cost           = Default.Counter("theophylax_cost_usd_total", "Estimated spend in USD", "model")
	UpstreamErrors = Default.Counter("theophylax_upstream_errors_total", "Upstream failures (incl. retried)", "provider", "status")
	Guardrail      = Default.Counter("theophylax_guardrail_actions_total", "Guardrail actions", "rule", "action")
	BatchRequests  = Default.Counter("theophylax_batch_requests_total", "Message batch requests by outcome (queued, succeeded, errored, canceled, expired)", "status")
	LogDropped     = Default.Counter("theophylax_log_dropped_total", "Request logs dropped due to back-pressure")
	Cache          = Default.Counter("theophylax_cache_total", "Response cache lookups", "result")
	Latency        = Default.Histogram("theophylax_request_duration_seconds", "End-to-end request latency", []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300}, "model")
	TTFB           = Default.Histogram("theophylax_ttfb_seconds", "Time to first byte from upstream", []float64{.05, .1, .25, .5, 1, 2, 5, 10, 30}, "model")
	Overhead       = Default.Histogram("theophylax_gateway_overhead_seconds", "Gateway processing time before upstream call", []float64{.0005, .001, .0025, .005, .01, .025, .05, .1})
)
