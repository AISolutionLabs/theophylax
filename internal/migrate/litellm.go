// Package migrate imports an existing LiteLLM proxy deployment: its model
// list (config.yaml or LiteLLM_ProxyModelTable) becomes providers, models and
// deployments; its database (organizations, teams, users, keys, budgets)
// becomes orgs, teams, projects, memberships, gateway keys and budgets.
// LiteLLM stores keys as SHA-256 of the plaintext, which is exactly how
// Theophylax stores them, so existing sk-… keys keep working unchanged.
package migrate

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Plan is what an import will create. Building it never touches a database.
type Plan struct {
	Providers []ProviderPlan
	Models    []ModelPlan
	Warnings  []string
}

type ProviderPlan struct {
	Name string
	Conn string
	Kind string
}

type ModelPlan struct {
	Name        string
	Aliases     []string
	InputPrice  *float64 // USD per million tokens
	OutputPrice *float64
	CacheRead   *float64
	CacheWrite  *float64
	MaxOutput   int
	Context     int
	Strategy    string
	Deployments []DeploymentPlan
}

type DeploymentPlan struct {
	Provider string // ProviderPlan.Name
	Upstream string
	Weight   int
	Priority int
}

func (p *Plan) warn(f string, a ...any) { p.Warnings = append(p.Warnings, fmt.Sprintf(f, a...)) }

// ParseConfig reads a LiteLLM config.yaml.
func ParseConfig(src string) (*Plan, error) {
	v, err := parseYAML(src)
	if err != nil {
		return nil, fmt.Errorf("config.yaml: %w", err)
	}
	root, _ := v.(map[string]any)
	if root == nil {
		return nil, fmt.Errorf("config.yaml: expected a mapping at the top level")
	}
	entries, _ := root["model_list"].([]any)
	plan := &Plan{}
	b := newBuilder(plan)
	strategy := "weighted"
	rs, _ := root["router_settings"].(map[string]any)
	switch str(rs["routing_strategy"]) {
	case "latency-based-routing":
		strategy = "latency"
	case "", "simple-shuffle", "least-busy", "usage-based-routing", "usage-based-routing-v2", "cost-based-routing":
		if s := str(rs["routing_strategy"]); s != "" && s != "simple-shuffle" {
			plan.warn("router_settings.routing_strategy %q has no exact equivalent; models use weighted routing with health-based failover", s)
		}
	}
	for _, e := range entries {
		m, _ := e.(map[string]any)
		b.add(str(m["model_name"]), asMap(m["litellm_params"]), asMap(m["model_info"]))
	}
	aliases := map[string][]string{}
	for alias, target := range asMap(rs["model_group_alias"]) {
		switch t := target.(type) {
		case string:
			aliases[t] = append(aliases[t], alias)
		case map[string]any:
			aliases[str(t["model"])] = append(aliases[str(t["model"])], alias)
		}
	}
	b.finish(strategy, aliases)
	ls := asMap(root["litellm_settings"])
	if fb, ok := ls["fallbacks"].([]any); ok && len(fb) > 0 {
		plan.warn("litellm_settings.fallbacks: cross-model fallbacks are not imported; add the fallback provider as a lower-priority deployment of the same model instead")
	}
	for _, k := range []string{"success_callback", "callbacks", "failure_callback"} {
		if cb, ok := ls[k].([]any); ok && len(cb) > 0 {
			plan.warn("litellm_settings.%s %v: use THEOPHYLAX_OTLP_ENDPOINT (OpenTelemetry, Langfuse, Datadog) or THEOPHYLAX_LOG_SINK_URL instead", k, cb)
		}
	}
	if _, ok := ls["guardrails"]; ok {
		plan.warn("litellm_settings.guardrails: recreate them as guardrail policies (Lakera, Bedrock, Azure Content Safety, webhook and native rules are supported)")
	}
	if _, ok := root["guardrails"]; ok {
		plan.warn("guardrails: recreate them as guardrail policies (Lakera, Bedrock, Azure Content Safety, webhook and native rules are supported)")
	}
	if gs := asMap(root["general_settings"]); str(gs["master_key"]) != "" {
		plan.warn("general_settings.master_key is not imported; use platform admins and personal access tokens for admin access")
	}
	return plan, nil
}

// builder turns LiteLLM deployments into providers/models, de-duplicating
// providers that share kind, endpoint and credentials.
type builder struct {
	plan   *Plan
	prov   map[string]*ProviderPlan // by connection string
	models map[string]*ModelPlan
	order  []string
}

func newBuilder(p *Plan) *builder {
	return &builder{plan: p, prov: map[string]*ProviderPlan{}, models: map[string]*ModelPlan{}}
}

func (b *builder) add(name string, lp, info map[string]any) {
	if name == "" || lp == nil {
		return
	}
	model := str(lp["model"])
	if strings.Contains(name, "*") || strings.Contains(model, "*") {
		b.plan.warn("%s: wildcard routes are not imported; add the specific models you use (or a wildcard model in the console)", name)
		return
	}
	conn, kind, upstream, pname, err := mapDeployment(model, lp)
	if err != nil {
		b.plan.warn("%s (%s): %v — skipped", name, model, err)
		return
	}
	pp := b.prov[conn]
	if pp == nil {
		pp = &ProviderPlan{Name: b.uniqueProviderName(pname), Conn: conn, Kind: kind}
		b.prov[conn] = pp
		b.plan.Providers = append(b.plan.Providers, *pp)
	}
	mp := b.models[name]
	if mp == nil {
		mp = &ModelPlan{Name: name}
		b.models[name] = mp
		b.order = append(b.order, name)
	}
	w := int(num(lp["weight"]))
	if w <= 0 {
		w = 1
	}
	mp.Deployments = append(mp.Deployments, DeploymentPlan{Provider: pp.Name, Upstream: upstream, Weight: w, Priority: int(num(lp["order"]))})
	perM := func(k string) *float64 {
		if v, ok := info[k]; ok && v != nil {
			if f := num(v) * 1e6; f > 0 {
				return &f
			}
		}
		if v, ok := lp[k]; ok && v != nil {
			if f := num(v) * 1e6; f > 0 {
				return &f
			}
		}
		return nil
	}
	if mp.InputPrice == nil {
		mp.InputPrice, mp.OutputPrice = perM("input_cost_per_token"), perM("output_cost_per_token")
		mp.CacheRead, mp.CacheWrite = perM("cache_read_input_token_cost"), perM("cache_creation_input_token_cost")
	}
	if v := int(num(info["max_output_tokens"])); v > 0 {
		mp.MaxOutput = v
	} else if v := int(num(info["max_tokens"])); v > 0 && mp.MaxOutput == 0 {
		mp.MaxOutput = v
	}
	if v := int(num(info["max_input_tokens"])); v > 0 {
		mp.Context = v
	}
	if rpm := num(lp["rpm"]); rpm > 0 {
		b.plan.warn("%s: per-deployment rpm/tpm limits are not imported; set RPM/TPM on the key, project, team or org", name)
	}
}

func (b *builder) uniqueProviderName(n string) string {
	used := map[string]bool{}
	for _, p := range b.plan.Providers {
		used[p.Name] = true
	}
	if !used[n] {
		return n
	}
	for i := 2; ; i++ {
		c := fmt.Sprintf("%s (%d)", n, i)
		if !used[c] {
			return c
		}
	}
}

func (b *builder) finish(strategy string, aliases map[string][]string) {
	for _, n := range b.order {
		m := b.models[n]
		m.Strategy = "priority"
		if len(m.Deployments) > 1 {
			m.Strategy = strategy
		}
		m.Aliases = aliases[n]
		sort.Strings(m.Aliases)
		b.plan.Models = append(b.plan.Models, *m)
	}
	// Providers were appended before their name could change; refresh from map.
	byConn := map[string]string{}
	for c, p := range b.prov {
		byConn[c] = p.Name
	}
	for i := range b.plan.Providers {
		b.plan.Providers[i].Name = byConn[b.plan.Providers[i].Conn]
	}
}

// envRef converts LiteLLM's os.environ/NAME secret syntax to ${env:NAME}.
func envRef(v any) string {
	s := str(v)
	if strings.HasPrefix(s, "os.environ/") {
		return "${env:" + strings.TrimPrefix(s, "os.environ/") + "}"
	}
	return s
}

func q(vals ...string) string {
	u := url.Values{}
	for i := 0; i+1 < len(vals); i += 2 {
		if vals[i+1] != "" {
			u.Set(vals[i], vals[i+1])
		}
	}
	s := u.Encode()
	// Keep secret references readable.
	s = strings.NewReplacer("%24%7B", "${", "%7D", "}", "%3A", ":", "%2F", "/").Replace(s)
	if s == "" {
		return ""
	}
	return "?" + s
}

func hostOf(apiBase string) string {
	u, err := url.Parse(apiBase)
	if err != nil || u.Host == "" {
		return strings.TrimRight(strings.TrimPrefix(strings.TrimPrefix(apiBase, "https://"), "http://"), "/")
	}
	return u.Host
}

var openAICompatible = map[string]string{
	"groq": "groq", "mistral": "mistral", "together_ai": "together", "deepseek": "deepseek", "xai": "xai", "openrouter": "openrouter",
	"fireworks_ai": "fireworks", "cerebras": "cerebras", "perplexity": "perplexity", "ollama": "ollama", "ollama_chat": "ollama",
	"hosted_vllm": "vllm", "vllm": "vllm", "lm_studio": "lmstudio", "openai_compatible": "openai", "text-completion-openai": "openai",
}

// mapDeployment converts one litellm_params block.
func mapDeployment(model string, lp map[string]any) (conn, kind, upstream, name string, err error) {
	prov, rest, ok := strings.Cut(model, "/")
	if !ok {
		// Bare model names are OpenAI in LiteLLM.
		prov, rest = "openai", model
	}
	if p := str(lp["custom_llm_provider"]); p != "" {
		prov = p
	}
	key := envRef(lp["api_key"])
	base := str(lp["api_base"])
	switch prov {
	case "bedrock":
		rest = strings.TrimPrefix(strings.TrimPrefix(rest, "converse/"), "invoke/")
		region := str(lp["aws_region_name"])
		if region == "" {
			region = "${env:AWS_REGION}"
			if strings.HasPrefix(rest, "eu.") {
				region = "eu-west-1"
			} else if strings.HasPrefix(rest, "us.") {
				region = "us-east-1"
			}
		}
		if !strings.Contains(rest, "anthropic.") && !strings.Contains(rest, "titan-embed") && !strings.Contains(rest, "cohere.embed") {
			return "", "", "", "", fmt.Errorf("Bedrock models other than Claude (and Titan/Cohere embeddings) are not served; route them through an OpenAI-compatible endpoint")
		}
		args := []string{}
		switch {
		case str(lp["aws_role_name"]) != "":
			args = append(args, "auth", "assume_role", "role_arn", str(lp["aws_role_name"]), "external_id", str(lp["aws_external_id"]))
			if str(lp["aws_access_key_id"]) != "" {
				args = append(args, "access_key_id", envRef(lp["aws_access_key_id"]), "secret_access_key", envRef(lp["aws_secret_access_key"]))
			}
		case str(lp["aws_web_identity_token"]) != "":
			args = append(args, "auth", "irsa")
		case str(lp["aws_access_key_id"]) != "":
			args = append(args, "auth", "static", "access_key_id", envRef(lp["aws_access_key_id"]), "secret_access_key", envRef(lp["aws_secret_access_key"]), "session_token", envRef(lp["aws_session_token"]))
		case str(lp["aws_bedrock_runtime_endpoint"]) == "" && key != "":
			args = append(args, "auth", "api_key", "api_key", key)
		}
		if ep := str(lp["aws_bedrock_runtime_endpoint"]); ep != "" {
			args = append(args, "endpoint", ep)
		}
		return "bedrock://" + region + q(args...), "bedrock", rest, "Bedrock " + region, nil
	case "anthropic":
		args := []string{"api_key", orStr(key, "${env:ANTHROPIC_API_KEY}")}
		host := "api.anthropic.com"
		if base != "" {
			args = append(args, "base_url", strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1"))
			host = hostOf(base)
		}
		return "anthropic://" + host + q(args...), "anthropic", rest, "Anthropic", nil
	case "azure":
		if base == "" {
			base = "${env:AZURE_API_BASE}"
		}
		host := hostOf(base)
		ver := orStr(str(lp["api_version"]), "${env:AZURE_API_VERSION}")
		args := []string{}
		switch {
		case str(lp["tenant_id"]) != "" && str(lp["client_secret"]) != "":
			args = append(args, "auth", "entra", "tenant_id", envRef(lp["tenant_id"]), "client_id", envRef(lp["client_id"]), "client_secret", envRef(lp["client_secret"]))
		case key != "":
			args = append(args, "auth", "api_key", "api_key", key)
		default:
			args = append(args, "auth", "managed")
		}
		args = append(args, "api_version", ver)
		return "azure-openai://" + host + q(args...), "azure-openai", rest, "Azure OpenAI " + strings.Split(host, ".")[0], nil
	case "azure_ai":
		host := hostOf(base)
		if strings.Contains(rest, "claude") {
			return "azure-foundry://" + host + q("auth", map[bool]string{true: "api_key", false: "managed"}[key != ""], "api_key", key), "azure-foundry", rest, "Azure AI Foundry", nil
		}
		return "openai://" + host + q("base_url", strings.TrimRight(base, "/"), "api_key", key), "openai", rest, "Azure AI " + strings.Split(host, ".")[0], nil
	case "vertex_ai", "vertex_ai_beta":
		project := envRef(lp["vertex_project"])
		loc := orStr(envRef(lp["vertex_location"]), "us-central1")
		creds := envRef(lp["vertex_credentials"])
		auth := "default"
		if creds != "" {
			auth = "service_account"
		}
		if project == "" {
			project = "${env:VERTEXAI_PROJECT}"
		}
		if strings.Contains(rest, "claude") {
			return "vertex://" + loc + q("project", project, "auth", auth, "credentials", creds), "vertex", rest, "Vertex AI " + loc, nil
		}
		up := rest
		if !strings.Contains(up, "/") {
			up = "google/" + up
		}
		return "vertex-openai://" + loc + q("project", project, "auth", auth, "credentials", creds), "vertex-openai", up, "Vertex AI (Gemini) " + loc, nil
	case "gemini":
		return "gemini://generativelanguage.googleapis.com" + q("api_key", orStr(key, "${env:GEMINI_API_KEY}")), "gemini", rest, "Google Gemini", nil
	case "openai":
		args := []string{"api_key", orStr(key, map[bool]string{true: "", false: "${env:OPENAI_API_KEY}"}[base != ""])}
		host := "api.openai.com"
		if base != "" {
			args = append(args, "base_url", strings.TrimRight(base, "/"))
			host = hostOf(base)
		}
		return "openai://" + host + q(args...), "openai", rest, map[bool]string{true: "OpenAI-compatible " + host, false: "OpenAI"}[base != ""], nil
	}
	if preset, ok := openAICompatible[prov]; ok {
		args := []string{"api_key", key}
		host := ""
		if base != "" {
			b := strings.TrimRight(base, "/")
			if preset == "ollama" && !strings.HasSuffix(b, "/v1") {
				b += "/v1"
			}
			args = append(args, "base_url", b)
			host = hostOf(base)
		}
		return preset + "://" + host + q(args...), "openai", rest, strings.ToUpper(preset[:1]) + preset[1:], nil
	}
	return "", "", "", "", fmt.Errorf("provider %q is not supported", prov)
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprint(int64(x))
		}
		return fmt.Sprint(x)
	}
	return fmt.Sprint(v)
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case int:
		return float64(x)
	case string:
		var f float64
		_, _ = fmt.Sscan(x, &f)
		return f
	}
	return 0
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
