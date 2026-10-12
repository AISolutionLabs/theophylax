package confsync

import (
	"os"
	"regexp"
	"testing"
)

// The example in docs/config-as-code.md must stay a valid document.
func TestDocsExampleParses(t *testing.T) {
	b, err := os.ReadFile("../../docs/config-as-code.md")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile("(?s)```yaml\n(.*?)```").FindSubmatch(b)
	if m == nil {
		t.Fatal("no yaml example")
	}
	d, err := Parse(m[1])
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Organizations) != 1 || len(d.Models[0].Deployments) != 2 || d.Models[0].Deployments[1].UpstreamModel != "eu.anthropic.claude-sonnet-4-5-v1:0" ||
		d.Organizations[0].Budgets[0].Scope != "project:ml/chat" || d.Providers[1].Organization != "acme" {
		t.Fatalf("%+v", d)
	}
}
