package migrate

import (
	"os"
	"strings"
	"testing"

	"theophylax/internal/providers"
)

func TestParseLiteLLMConfig(t *testing.T) {
	src, _ := os.ReadFile("testdata/litellm_config.yaml")
	plan, err := ParseConfig(string(src))
	if err != nil {
		t.Fatal(err)
	}
	conns := map[string]string{}
	for _, p := range plan.Providers {
		conns[p.Name] = p.Conn
		// Every generated connection string must be accepted by the gateway.
		if _, err := providers.ParseConn(p.Conn); err != nil {
			t.Errorf("%s: %s: %v", p.Name, p.Conn, err)
		}
	}
	want := map[string]string{
		"Bedrock us-east-1":              "bedrock://us-east-1?auth=assume_role&role_arn=arn:aws:iam::111122223333:role/bedrock-team-a",
		"Bedrock eu-west-1":              "bedrock://eu-west-1?access_key_id=${env:AWS_ACCESS_KEY_ID}&auth=static&secret_access_key=${env:AWS_SECRET_ACCESS_KEY}",
		"Anthropic":                      "anthropic://api.anthropic.com?api_key=${env:ANTHROPIC_API_KEY}",
		"Azure OpenAI acme-openai":       "azure-openai://acme-openai.openai.azure.com?api_key=${env:AZURE_API_KEY}&api_version=2024-10-21&auth=api_key",
		"Vertex AI (Gemini) us-central1": "vertex-openai://us-central1?auth=service_account&credentials=${env:VERTEX_CREDS_PATH}&project=acme-ml",
		"Vertex AI us-east5":             "vertex://us-east5?auth=default&project=acme-ml",
		"Ollama":                         "ollama://gpu-box:11434?base_url=http://gpu-box:11434/v1",
		"Groq":                           "groq://?api_key=${env:GROQ_API_KEY}",
	}
	for n, c := range want {
		if conns[n] != c {
			t.Errorf("provider %q:\n got %q\nwant %q", n, conns[n], c)
		}
	}
	if len(plan.Providers) != len(want) {
		t.Errorf("providers: %v", conns)
	}
	var sonnet *ModelPlan
	for i := range plan.Models {
		if plan.Models[i].Name == "claude-sonnet" {
			sonnet = &plan.Models[i]
		}
	}
	if sonnet == nil || len(sonnet.Deployments) != 2 || sonnet.Strategy != "weighted" || sonnet.Deployments[1].Weight != 2 ||
		sonnet.InputPrice == nil || *sonnet.InputPrice < 2.99 || *sonnet.InputPrice > 3.01 || strings.Join(sonnet.Aliases, ",") != "sonnet" {
		t.Fatalf("claude-sonnet: %+v", sonnet)
	}
	for _, m := range plan.Models {
		if m.Name == "gemini-pro" && m.Deployments[0].Upstream != "google/gemini-2.5-pro" {
			t.Errorf("gemini upstream %q", m.Deployments[0].Upstream)
		}
		if m.Name == "titan-embed" && m.Deployments[0].Provider != "Bedrock us-east-1" {
			t.Errorf("titan should share the us-east-1 provider: %+v", m.Deployments)
		}
	}
	joined := strings.Join(plan.Warnings, "\n")
	for _, w := range []string{"sagemaker", "wildcard", "fallbacks", "langfuse", "master_key", "rpm/tpm"} {
		if !strings.Contains(joined, w) {
			t.Errorf("missing warning about %s in:\n%s", w, joined)
		}
	}
}
