// Command loadgen is the benchmark harness used to compare Theophylax with
// other AI gateways. It has two modes:
//
//	loadgen mock <addr>        a zero-latency OpenAI-compatible upstream
//	loadgen load -url ... [-n 8000 -c 10 -stream -H "k: v" -label name]
//
// The mock answers /v1/chat/completions (JSON or SSE) and /v1/models. The
// load generator sends the same chat request to every gateway, warms up,
// then reports throughput, latency percentiles, time to first byte and errors.
// Stdlib only, so it builds anywhere Go does.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const chatResp = `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"fast","choices":[{"index":0,"message":{"role":"assistant","content":"The deployment runbook covers build, canary, verification and rollback steps for the service."},"finish_reason":"stop"}],"usage":{"prompt_tokens":42,"completion_tokens":17,"total_tokens":59}}`

var words = strings.Fields("The deployment runbook covers build, canary, verification and rollback steps for the service.")

func mock(addr string, delay time.Duration) {
	http.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if delay > 0 {
			time.Sleep(delay)
		}
		if !bytes.Contains(b, []byte(`"stream":true`)) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(chatResp))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for i, wd := range words {
			if i > 0 {
				wd = " " + wd
			}
			fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fast\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q},\"finish_reason\":null}]}\n\n", wd)
			fl.Flush()
		}
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fast\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":42,\"completion_tokens\":17,\"total_tokens\":59}}\n\ndata: [DONE]\n\n")
		fl.Flush()
	})
	http.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"object":"list","data":[{"id":"fast","object":"model"}]}`))
	})
	fmt.Fprintf(os.Stderr, "mock upstream listening on %s (delay %s)\n", addr, delay)
	panic(http.ListenAndServe(addr, nil))
}

type hdrs []string

func (h *hdrs) String() string     { return strings.Join(*h, ",") }
func (h *hdrs) Set(v string) error { *h = append(*h, v); return nil }

const defaultBody = `{"model":"fast","max_tokens":64,"stream":%v,"messages":[{"role":"system","content":"You are a helpful assistant for the platform team."},{"role":"user","content":"Summarise the deployment runbook for service alpha in three sentences. Region eu-west-1, cluster blue, owner team platform."}]}`

type result struct {
	Label    string  `json:"label"`
	Requests int     `json:"requests"`
	Conc     int     `json:"concurrency"`
	Stream   bool    `json:"stream"`
	RPS      float64 `json:"rps"`
	P50      float64 `json:"p50_ms"`
	P95      float64 `json:"p95_ms"`
	P99      float64 `json:"p99_ms"`
	TTFBP50  float64 `json:"ttfb_p50_ms"`
	Errors   int64   `json:"errors"`
}

func load() {
	fs := flag.NewFlagSet("load", flag.ExitOnError)
	url := fs.String("url", "", "gateway chat completions URL")
	n := fs.Int("n", 8000, "measured requests")
	c := fs.Int("c", 10, "concurrent workers")
	stream := fs.Bool("stream", false, "request SSE streaming")
	label := fs.String("label", "", "name printed with the result")
	warm := fs.Int("warm", 300, "warm-up requests (not measured)")
	bodyFile := fs.String("body", "", "file with a custom JSON request body (overrides the default prompt; set \"stream\" yourself)")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	var hs hdrs
	fs.Var(&hs, "H", "request header 'Name: value' (repeatable)")
	fs.Parse(os.Args[2:])
	if *url == "" {
		fmt.Fprintln(os.Stderr, "-url is required")
		os.Exit(2)
	}
	body := fmt.Sprintf(defaultBody, *stream)
	if *bodyFile != "" {
		b, err := os.ReadFile(*bodyFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		body = string(b)
	}
	client := &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: *c * 2, MaxIdleConns: *c * 2}}
	var debugOnce atomic.Bool
	one := func() (time.Duration, time.Duration, bool) {
		req, _ := http.NewRequest("POST", *url, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for _, h := range hs {
			k, v, _ := strings.Cut(h, ":")
			req.Header.Set(strings.TrimSpace(k), strings.TrimSpace(v))
		}
		t := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			if debugOnce.CompareAndSwap(false, true) {
				fmt.Fprintln(os.Stderr, "first error:", err)
			}
			return time.Since(t), 0, false
		}
		ttfb := time.Duration(0)
		buf := make([]byte, 4096)
		ok := resp.StatusCode == 200
		var all []byte
		for {
			k, err := resp.Body.Read(buf)
			if k > 0 && ttfb == 0 {
				ttfb = time.Since(t)
			}
			all = append(all, buf[:k]...)
			if err != nil {
				break
			}
		}
		resp.Body.Close()
		if *stream && !bytes.Contains(all, []byte("[DONE]")) && !bytes.Contains(all, []byte("message_stop")) {
			ok = false
		}
		if !ok && debugOnce.CompareAndSwap(false, true) {
			fmt.Fprintln(os.Stderr, "first error:", resp.StatusCode, string(all[:min(len(all), 300)]))
		}
		return time.Since(t), ttfb, ok
	}
	for i := 0; i < *warm; i++ {
		one()
	}
	lat := make([]time.Duration, *n)
	tt := make([]time.Duration, *n)
	var errs atomic.Int64
	ch := make(chan int)
	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < *c; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ch {
				l, f, ok := one()
				lat[i], tt[i] = l, f
				if !ok {
					errs.Add(1)
				}
			}
		}()
	}
	for i := 0; i < *n; i++ {
		ch <- i
	}
	close(ch)
	wg.Wait()
	el := time.Since(start)
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	sort.Slice(tt, func(i, j int) bool { return tt[i] < tt[j] })
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	r := result{Label: *label, Requests: *n, Conc: *c, Stream: *stream, RPS: float64(*n) / el.Seconds(),
		P50: ms(lat[*n/2]), P95: ms(lat[*n*95/100]), P99: ms(lat[*n*99/100]), TTFBP50: ms(tt[*n/2]), Errors: errs.Load()}
	if *asJSON {
		json.NewEncoder(os.Stdout).Encode(r)
		return
	}
	fmt.Printf("%-34s rps=%7.0f p50=%7.2fms p95=%7.2fms p99=%7.2fms ttfb_p50=%6.2fms errors=%d\n", r.Label, r.RPS, r.P50, r.P95, r.P99, r.TTFBP50, r.Errors)
}

// coldstart runs nothing itself: it polls a URL until it answers 2xx and
// prints how long that took, measured from the moment it was invoked. Start
// the gateway and this in the same shell line to measure time to ready.
func coldstart() {
	fs := flag.NewFlagSet("coldstart", flag.ExitOnError)
	url := fs.String("url", "", "health URL to poll")
	timeout := fs.Duration("timeout", 2*time.Minute, "give up after")
	fs.Parse(os.Args[2:])
	t := time.Now()
	cl := &http.Client{Timeout: time.Second}
	for time.Since(t) < *timeout {
		if r, err := cl.Get(*url); err == nil {
			r.Body.Close()
			if r.StatusCode < 300 {
				fmt.Printf("ready in %d ms\n", time.Since(t).Milliseconds())
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	fmt.Println("timed out")
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: loadgen mock <addr> [delay] | loadgen load -url ... | loadgen coldstart -url ...")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "mock":
		addr := "127.0.0.1:9000"
		if len(os.Args) > 2 {
			addr = os.Args[2]
		}
		var d time.Duration
		if len(os.Args) > 3 {
			d, _ = time.ParseDuration(os.Args[3])
		}
		mock(addr, d)
	case "load":
		load()
	case "coldstart":
		coldstart()
	default:
		fmt.Fprintln(os.Stderr, "unknown mode", os.Args[1])
		os.Exit(2)
	}
}
