#!/usr/bin/env bash
# Configures a running Theophylax for the benchmark: an OpenAI-compatible
# provider pointing at the mock upstream, a model called "fast", and a gateway
# key. Prints the key on stdout.
#
#   bench/setup-theophylax.sh            # guardrails as bootstrapped ("full")
#   GUARDRAILS=off bench/setup-theophylax.sh   # disable all org guardrails ("lean")
#   GUARDRAILS=on  bench/setup-theophylax.sh   # re-enable them
#
# Requires curl and jq. Safe to run more than once: re-runs reuse the provider
# and model and only mint a new key.
set -euo pipefail

TPX=${TPX:-http://127.0.0.1:8080}
EMAIL=${EMAIL:-admin@example.com}
PASSWORD=${PASSWORD:-change-me-please}
UPSTREAM=${UPSTREAM:-http://127.0.0.1:9000/v1}
GUARDRAILS=${GUARDRAILS:-}

jar=$(mktemp); trap 'rm -f "$jar"' EXIT
api() { # method path [json]
  local args=(-sS -f -b "$jar" -c "$jar" -X "$1" "$TPX$2" -H 'Content-Type: application/json' -H 'X-Theophylax-Client: web')
  [[ $# -ge 3 ]] && args+=(-d "$3")
  curl "${args[@]}"
}

api POST /api/auth/login "$(jq -nc --arg e "$EMAIL" --arg p "$PASSWORD" '{email:$e,password:$p}')" >/dev/null
org=$(api GET /api/orgs | jq -r '(if type=="array" then . else .data end)[0].id')
project=$(api GET "/api/orgs/$org/projects" | jq -r '(if type=="array" then . else .data end)[0].id')

host=${UPSTREAM#*://}; host=${host%%/*}
prov=$(api GET /api/providers | jq -r '(if type=="array" then . else .data end)[] | select(.name=="bench-mock") | .id' | head -1)
if [[ -z $prov ]]; then
  prov=$(api POST /api/providers "$(jq -nc --arg cs "openai://$host?base_url=$UPSTREAM&api_key=x" '{name:"bench-mock",connection_string:$cs}')" | jq -r .id)
fi
model=$(api GET /api/models | jq -r '(if type=="array" then . else .data end)[] | select(.name=="fast") | .id' | head -1)
if [[ -z $model ]]; then
  model=$(api POST /api/models '{"name":"fast","display_name":"Benchmark mock"}' | jq -r .id)
  api POST "/api/models/$model/deployments" "$(jq -nc --arg p "$prov" '{provider_id:$p,upstream_model:"fast"}')" >/dev/null
fi

if [[ -n $GUARDRAILS ]]; then
  enabled=$([[ $GUARDRAILS == off ]] && echo false || echo true)
  for g in $(api GET "/api/orgs/$org/guardrails" | jq -r '(if type=="array" then . else .data end)[].id'); do
    api PATCH "/api/guardrails/$g" "{\"enabled\":$enabled}" >/dev/null
  done
  echo "guardrails: $GUARDRAILS" >&2
fi

api POST /api/keys "$(jq -nc --arg p "$project" '{name:"bench",project_id:$p}')" | jq -r .secret
