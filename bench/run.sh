#!/usr/bin/env bash
# Runs the baseline suite against every gateway you point it at. Start the mock
# upstream and the gateways first (see bench/README.md), then:
#
#   TPX_KEY=tpx_... bench/run.sh                 # Theophylax only
#   TPX_KEY=... LITELLM=1 PORTKEY=1 bench/run.sh # all three
#
# Each gateway gets the same request, the same warm-up and the same load.
# Results print as one line per scenario and are appended as JSON lines to
# bench/results/<timestamp>.jsonl.
set -euo pipefail
cd "$(dirname "$0")"

N=${N:-8000}           # measured requests per scenario
C=${C:-10}             # concurrency for the overhead scenarios
SAT_C=${SAT_C:-64}     # concurrency for the saturation scenario
MOCK=${MOCK:-http://127.0.0.1:9000/v1}
TPX=${TPX:-http://127.0.0.1:8080}
LITELLM_URL=${LITELLM_URL:-http://127.0.0.1:4000}
LITELLM_KEY=${LITELLM_KEY:-sk-bench-1234}
PORTKEY_URL=${PORTKEY_URL:-http://127.0.0.1:8787}
LABEL=${LABEL:-}       # optional suffix, e.g. "full" or "lean"

[[ -x ./loadgen-bin ]] || go build -o ./loadgen-bin ./loadgen
mkdir -p results
out=results/$(date -u +%Y%m%dT%H%M%SZ).jsonl

suite() { # name url [headers...]
  local name=$1 url=$2; shift 2
  local r
  for mode in "" "-stream"; do
    r=$(./loadgen-bin load -url "$url" -n "$N" -c "$C" -label "$name${mode:+ stream}" -json $mode "$@")
    echo "$r" >>"$out"
    echo "$r" | jq -r '"\(.label|.[0:34]|. + (" " * (34-length)))  rps=\(.rps|floor)  p50=\(.p50_ms)ms  p99=\(.p99_ms)ms  ttfb_p50=\(.ttfb_p50_ms)ms  errors=\(.errors)"'
  done
  r=$(./loadgen-bin load -url "$url" -n "$N" -c "$SAT_C" -label "$name c=$SAT_C" -json "$@")
  echo "$r" >>"$out"
  echo "$r" | jq -r '"\(.label|.[0:34]|. + (" " * (34-length)))  rps=\(.rps|floor)  p50=\(.p50_ms)ms  p99=\(.p99_ms)ms  errors=\(.errors)"'
}

echo "# N=$N C=$C SAT_C=$SAT_C  $(nproc) vCPU  $(date -u)"
suite "direct (floor)" "$MOCK/chat/completions"
if [[ -n ${TPX_KEY:-} ]]; then
  suite "theophylax${LABEL:+ $LABEL}" "$TPX/v1/chat/completions" -H "Authorization: Bearer $TPX_KEY"
fi
if [[ -n ${LITELLM:-} ]]; then
  suite "litellm" "$LITELLM_URL/v1/chat/completions" -H "Authorization: Bearer $LITELLM_KEY"
fi
if [[ -n ${PORTKEY:-} ]]; then
  suite "portkey" "$PORTKEY_URL/v1/chat/completions" -H "x-portkey-provider: openai" -H "x-portkey-custom-host: $MOCK" -H "Authorization: Bearer x"
fi
echo "# results appended to bench/$out"
