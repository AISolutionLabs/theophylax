# Gateway benchmarks

A small, reproducible harness for measuring what an AI gateway costs you in
latency, throughput, memory and start-up time, compared with calling the
upstream directly. It runs Theophylax, LiteLLM and Portkey against the same
zero-latency mock upstream with the same request and the same load.

Everything here is stdlib Go plus `curl` and `jq`.

| File | What it does |
|---|---|
| `loadgen/` | `mock` (OpenAI-compatible upstream), `load` (load generator), `coldstart` (time to healthy) |
| `run.sh` | Runs the suite against every configured gateway, prints a line per scenario, appends JSON lines to `results/` |
| `setup-theophylax.sh` | Points a running Theophylax at the mock, creates a `fast` model and a key, and can turn guardrails on or off |
| `configs/litellm.yaml` | The LiteLLM proxy config used for the comparison |

## Scenarios

For each gateway, `run.sh` measures:

1. **Overhead, non-streaming**: 8,000 requests at 10 concurrent.
2. **Overhead, streaming**: the same with SSE. Time to first byte is reported separately.
3. **Saturation**: 8,000 requests at 64 concurrent, which shows the throughput ceiling and the p99 tail under queueing.

Each run starts with 300 unmeasured warm-up requests. A request counts as an
error if it doesn't return 200, or if a stream doesn't finish with `[DONE]`.
The "direct (floor)" line is the mock called without a gateway, which is the
cost of the network and the load generator.

## Running it

Use a dedicated machine. Run the gateways one at a time, with CPU pinned
(`taskset` or container limits), and keep the load generator and the mock on
separate cores from the gateway.

```bash
cd bench
go build -o loadgen-bin ./loadgen
./loadgen-bin mock 127.0.0.1:9000 &        # optional 3rd arg adds upstream delay, e.g. 200ms
```

**Theophylax** (from the repo root; needs Postgres):

```bash
THEOPHYLAX_DATABASE_URL=postgres://... \
THEOPHYLAX_BOOTSTRAP_ADMIN_EMAIL=admin@example.com \
THEOPHYLAX_BOOTSTRAP_ADMIN_PASSWORD=change-me-please \
THEOPHYLAX_SEED_DEMO=true ./bin/theophylax &

KEY=$(bench/setup-theophylax.sh)                      # "full": Secret Shield, PII, injection on
TPX_KEY=$KEY LABEL=full bench/run.sh
KEY=$(GUARDRAILS=off bench/setup-theophylax.sh)       # "lean": guardrails off
TPX_KEY=$KEY LABEL=lean bench/run.sh
```

Logging to Postgres, budgets and signed receipts stay on in both modes.

**LiteLLM** (2 workers on 2 vCPU, per their deployment guidance):

```bash
pip install 'litellm[proxy]'
litellm --config bench/configs/litellm.yaml --port 4000 --num_workers 2 &
LITELLM=1 bench/run.sh
```

To compare like for like with Theophylax's always-on logging, add a
`database_url` to the config so LiteLLM records spend too.

**Portkey** (open-source gateway):

```bash
npx @portkey-ai/gateway &                  # listens on :8787
PORTKEY=1 bench/run.sh
```

Portkey routes to the mock through the `x-portkey-custom-host` header.
Streaming failed on Node 22 in our runs ("immutable headers"), so test it
with their Docker image if you need that number.

**Cold start:** start the gateway and the poller on the same line:

```bash
./bin/theophylax & bench/loadgen-bin coldstart -url http://127.0.0.1:8080/healthz
```

The first start includes migrations, so measure a second start for the
steady-state figure. For **memory**, read the RSS of the gateway's processes
(`ps -o rss= -p <pids>`, summed across workers) after the saturation run.

## Reference results

Measured on a shared 2-vCPU cloud VM, October 2026 (Theophylax `49b7a08`, LiteLLM
1.104.0 with 2 workers and no database, Portkey gateway 1.15.2 on Node 22).
These numbers are indicative only; rerun on your own hardware before quoting them.

| | rps (c=10) | p50 / p99 | stream p50 (TTFB) | rps (c=64) | RSS | cold start |
|---|---|---|---|---|---|---|
| Direct to mock | 23,600 | 0.27 / 1.8 ms | 0.8 ms | — | — | — |
| LiteLLM | 160 | 85 / 142 ms | 191 ms (124 ms) | 157 | ~1.1 GB | ~14 s |
| Portkey | 793 | 11.3 / 30 ms | failed | 978 | ~240 MB | 0.33 s |
| Theophylax lean | 2,820 | 2.9 / 13 ms | 5.9 ms (5.2 ms) | 2,838 | 35 MB | 0.07 s |
| Theophylax full | 1,433 | 5.9 / 24 ms | 10.7 ms (9.9 ms) | 1,454 | 37 MB | 0.07 s |

## What this harness does not tell you

- **Real providers dominate end-to-end latency.** A zero-latency mock exaggerates
  the differences between gateways. Use the mock's delay argument, or a real
  Bedrock or Anthropic endpoint, to see overhead as a share of total time.
- **Agent loops multiply overhead.** A 30-turn coding-agent session pays the
  gateway's cost 30 times, with a growing request body each turn. Pass a large
  recorded request with `-body` to measure this.
- **Failover and fault behaviour** (provider 5xx, timeouts, recovery time) aren't
  covered yet.
- **Vendor benchmarks are biased by construction**, including this one. Publish
  the configs and raw `results/*.jsonl` alongside any claim.
