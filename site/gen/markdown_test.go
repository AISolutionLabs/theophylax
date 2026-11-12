package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"heading slug like GitHub", "## 1. Trust model — what a receipt proves", `<h2 id="1-trust-model--what-a-receipt-proves">`},
		{"duplicate heading ids", "## A\n\n## A", `id="a-1"`},
		{"paragraph", "one\ntwo", "<p>one\ntwo</p>"},
		{"strong and em", "**bold** and *it* and _it_", "<strong>bold</strong> and <em>it</em> and <em>it</em>"},
		{"intraword underscore", "snake_case_name", "snake_case_name"},
		{"code span escapes", "use `<kind>://x` here", "<code>&lt;kind&gt;://x</code>"},
		{"code span keeps stars", "`a*b*c`", "<code>a*b*c</code>"},
		{"raw html escaped", "a <script>x</script>", "a &lt;script&gt;x&lt;/script&gt;"},
		{"link", "[Providers](providers.md#x)", `<a href="providers.md#x">Providers</a>`},
		{"link with code", "[`theophylax config`](config-as-code.md)", "<code>theophylax config</code></a>"},
		{"autolink", "<http://localhost:8080>", `<a href="http://localhost:8080" rel="noopener">http://localhost:8080</a>`},
		{"bare url", "see https://example.com/x.", `<a href="https://example.com/x" rel="noopener">https://example.com/x</a>.`},
		{"fence", "```bash\necho <hi>\n```", `<pre><code class="language-bash">echo &lt;hi&gt;` + "\n</code></pre>"},
		{"tight list", "- a\n- b", "<ul>\n<li>a</li>\n<li>b</li>\n</ul>"},
		{"nested list", "- a\n  - b\n- c", "<li>a\n<ul>\n<li>b</li>\n</ul></li>"},
		{"ordered start", "3. x\n4. y", `<ol start="3">`},
		{"loose list", "- a\n\n- b", "<li><p>a</p></li>"},
		{"tasks", "- [ ] todo\n- [x] done", `<input type="checkbox" disabled> todo`},
		{"code in list", "1. step\n\n   ```bash\n   run\n   ```\n2. next", "<li><p>step</p>\n<pre><code class=\"language-bash\">run\n</code></pre></li>"},
		{"table", "| A | B |\n|---|---|\n| `a|b` | x |", "<td><code>a|b</code></td><td>x</td>"},
		{"table empty head", "| | |\n|---|---|\n| a | b |", "<tbody>\n<tr><td>a</td>"},
		{"blockquote", "> note **this**", "<blockquote>\n<p>note <strong>this</strong></p>\n</blockquote>"},
		{"hr", "a\n\n---\n\nb", "<hr>"},
		{"escaped pipe", `a \| b`, "a | b"},
	}
	for _, c := range cases {
		got := Render(c.in, nil).HTML
		if !strings.Contains(got, c.want) {
			t.Errorf("%s:\n in: %q\nwant substring: %q\n got: %q", c.name, c.in, c.want, got)
		}
	}
}

func TestRewriteLink(t *testing.T) {
	repo := "https://github.com/o/r"
	blob := repo + "/blob/main/"
	for in, want := range map[string]string{
		"providers.md":                         "providers.html",
		"receipt-spec.md#1-trust":              "receipt-spec.html#1-trust",
		"#local":                               "#local",
		"../ROADMAP.md":                        blob + "ROADMAP.md",
		"../bench/README.md":                   blob + "bench/README.md",
		"../internal/receipts/testdata/v.json": blob + "internal/receipts/testdata/v.json",
		"../../issues?q=x":                     repo + "/issues?q=x",
		"https://example.com":                  "https://example.com",
	} {
		if got := rewriteLink(in, "docs", repo, blob); got != want {
			t.Errorf("rewriteLink(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBuild renders the real docs, which also checks every link between pages.
func TestBuild(t *testing.T) {
	out := t.TempDir()
	if err := build("https://github.com/o/r", "main", "../../docs", "..", out); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"index.html", "docs/getting-started.html", "docs/api.html", "assets/style.css", ".nojekyll"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Error(err)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "docs/upstream-watch.html")); err == nil {
		t.Error("upstream-watch.md should not be published")
	}
}
