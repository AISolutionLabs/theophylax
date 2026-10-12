# Configuration as code

`theophylax config` keeps a gateway's configuration in a YAML file you can review, version and apply from CI, the same way you would with Terraform. It covers organizations, teams, projects and their settings, providers, models and deployments, guardrails and budgets.

```sh
export THEOPHYLAX_URL=https://ai.example.com
export THEOPHYLAX_TOKEN=tpxp_…            # a personal access token

theophylax config export -o gateway.yaml  # what the gateway has now
theophylax config plan  -f gateway.yaml   # what applying would change
theophylax config apply -f gateway.yaml   # make the change
```

The tool calls the gateway's admin API with your token. It never touches the database, so every change goes through the same validation, permission checks, secret encryption and [audit log](operations.md) as a change made in the console. The token can only change what its owner could change by hand. For example, an organization admin's token can manage that organization's teams, guardrails and budgets, but not the global model catalog.

## The document

```yaml
version: 1
organizations:
  - slug: acme
    name: Acme
    settings:
      cache_ttl_seconds: 300
    teams:
      - slug: ml
        name: ML Platform
        projects:
          - slug: chat
            name: Chatbot
            settings:
              log_payloads: true
    guardrails:
      - name: deny-codenames
        scope: team:ml                    # org, team:<slug>, project:<team>/<slug>
        mode: enforce
        rules:
          - type: keywords
            words: [bluebird]
            action: block
    budgets:
      - scope: project:ml/chat            # also user:<email>
        period: monthly
        limit_usd: 500
        alert_pct: 80
        action: block
providers:
  - name: anthropic
    connection_string: "anthropic://?api_key=${env:ANTHROPIC_API_KEY}"
  - name: bedrock-eu
    organization: acme                    # owned by an organization (omit for global)
    connection_string: "bedrock://eu-west-1?auth=irsa"
models:
  - name: claude-sonnet
    aliases: [sonnet]
    strategy: priority
    deployments:
      - provider: anthropic               # "acme/bedrock-eu" for an organization's provider
        upstream_model: claude-sonnet-4-5
        priority: 0
      - provider: acme/bedrock-eu
        upstream_model: eu.anthropic.claude-sonnet-4-5-v1:0
        priority: 1
```

Each object is identified by a natural key, never an internal ID, so the same file can be applied to staging and production:

| Object | Key |
|---|---|
| organization | `slug` |
| team | `slug` within its organization |
| project | `slug` within its team |
| provider | `name`, plus `organization` for an organization's provider |
| model | `name` |
| deployment | `provider` and `upstream_model` within its model |
| guardrail | `name` within its organization |
| budget | `scope`, `period` and `per_user` within its organization |

**What a document leaves out is left alone.** A list that is missing (for example no `budgets:` key) is not managed, while an empty list (`budgets: []`) means "none". Within an item, a missing field keeps its current value. You can manage only part of a gateway, such as one organization's guardrails, without listing the rest. Budget limits are the exception: they are taken as written, so a `limit_usd` you remove is cleared.

Unknown keys are errors, so a typo cannot silently do nothing. JSON documents are accepted too.

## Secrets

`export` never writes secrets. A literal secret in a connection string comes out masked (`api_key=****3456`), and applying that masked value unchanged keeps the stored secret. That way an exported file can be applied as is.

For files kept in version control, use references instead of secrets: `${env:NAME}` or `${file:/path}`. The gateway resolves these from its own environment when it connects, and they are exported in full. A masked value that does not match the stored secret is an error. A literal secret in the file is sent only if it differs from the stored one in its masked form.

## Plan, apply, prune

- `plan` prints every change (`+` create, `~` update with the fields that differ, `-` delete) and changes nothing. With `-exit-code` it exits with status 3 when there are changes, which lets a scheduled CI job detect drift.
- `apply` checks the whole document first and reports every problem it finds before changing anything. It then makes the changes in order. If a call fails partway, for example on a permission error, the changes before it stay applied and the error names the one that failed. Running `apply` again continues from there. `apply -dry-run` is the same as `plan`.
- `-prune` also deletes providers, models, deployments, guardrails and budgets that the gateway has but the document does not list. It acts only on lists the document includes, and only within the organizations the document names. Organizations, teams and projects are never deleted by the tool. Delete those in the console, where their keys and spend history are shown first.

A typical pipeline runs `plan` on pull requests and `apply` on merge, with the token kept as a CI secret.

## What is not covered

API keys and personal access tokens are credentials, not configuration, so they are not exported. The same goes for guardrails and budgets attached to individual keys: `export` warns about them, and `-prune` never removes them. Members and roles come from your identity provider through [SSO and SCIM](identity.md). Alert channels, identity providers, agents and registry items are not yet covered. Contributions are welcome.
