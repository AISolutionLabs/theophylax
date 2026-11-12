// Command gen builds the project's GitHub Pages site: the landing page in
// site/index.html plus every page in docs/, rendered from Markdown into
// site/layout.html. It uses only the standard library.
//
//	go run ./site/gen -repo https://github.com/OWNER/REPO -out _site
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// docOrder sets the navigation order. Pages in docs/ that are not listed are
// appended alphabetically; pages in skip are not published.
var docOrder = []struct{ file, nav string }{
	{"getting-started.md", "Getting started"},
	{"architecture.md", "Architecture"},
	{"providers.md", "Providers & connection strings"},
	{"routing.md", "Complexity routing"},
	{"guardrails.md", "Guardrails & Secret Shield"},
	{"receipts.md", "Receipts"},
	{"receipt-spec.md", "Receipt format spec"},
	{"rollouts.md", "Model rollouts"},
	{"identity.md", "Identity, SSO & SCIM"},
	{"operations.md", "Configuration & operations"},
	{"config-as-code.md", "Configuration as code"},
	{"api.md", "API reference"},
}

var skip = map[string]bool{"upstream-watch.md": true}

type navItem struct {
	File  string // output file, e.g. "providers.html"
	Title string
	Src   string // source path in the repository
}

type pageData struct {
	Title, Description string
	Content            template.HTML
	Nav                []navItem
	Current            string
	Outline            []Heading
	RepoURL, EditURL   string
	Root               string // relative path to the site root
}

func main() {
	repo := flag.String("repo", "https://github.com/Theophylax/phylax", "repository URL, used for links to source files")
	branch := flag.String("branch", "main", "branch for source links")
	docsDir := flag.String("docs", "docs", "Markdown documentation directory")
	siteDir := flag.String("site", "site", "directory with layout.html, index.html and assets/")
	out := flag.String("out", "_site", "output directory")
	flag.Parse()
	if err := build(strings.TrimSuffix(*repo, "/"), *branch, *docsDir, *siteDir, *out); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func build(repo, branch, docsDir, siteDir, out string) error {
	files, err := docFiles(docsDir)
	if err != nil {
		return err
	}
	layout, err := template.ParseFiles(filepath.Join(siteDir, "layout.html"))
	if err != nil {
		return err
	}
	index, err := template.ParseFiles(filepath.Join(siteDir, "index.html"))
	if err != nil {
		return err
	}
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(out, "docs"), 0o755); err != nil {
		return err
	}

	blob := repo + "/blob/" + branch + "/"
	type rendered struct {
		item navItem
		page Page
	}
	var pages []rendered
	var nav []navItem
	for _, f := range files {
		src, err := os.ReadFile(filepath.Join(docsDir, f.file))
		if err != nil {
			return err
		}
		item := navItem{File: strings.TrimSuffix(f.file, ".md") + ".html", Title: f.nav, Src: path.Join(filepath.ToSlash(docsDir), f.file)}
		p := Render(string(src), func(u string) string { return rewriteLink(u, docsDir, repo, blob) })
		if item.Title == "" {
			item.Title = p.Title
		}
		nav = append(nav, item)
		pages = append(pages, rendered{item, p})
	}

	// Check links between published pages, including #anchors, so a renamed
	// heading fails the build instead of shipping a broken link.
	ids := map[string]map[string]bool{}
	for _, p := range pages {
		ids[p.item.File] = map[string]bool{"": true}
		for _, h := range p.page.Headings {
			ids[p.item.File][h.ID] = true
		}
	}
	var broken []string
	for _, p := range pages {
		for _, l := range p.page.Links {
			target, frag, local := localTarget(l, p.item.File)
			if !local {
				continue
			}
			if target == "" { // a link to a repository file outside docs/
				continue
			}
			if anchors, ok := ids[target]; !ok {
				broken = append(broken, fmt.Sprintf("%s: link to missing page %q", p.item.Src, l))
			} else if !anchors[frag] {
				broken = append(broken, fmt.Sprintf("%s: link to missing heading %q", p.item.Src, l))
			}
		}
	}
	if len(broken) > 0 {
		return errors.New("broken links:\n  " + strings.Join(broken, "\n  "))
	}

	for _, p := range pages {
		var outline []Heading
		for _, h := range p.page.Headings {
			if h.Level == 2 || h.Level == 3 {
				outline = append(outline, h)
			}
		}
		html := p.page.HTML
		if p.page.Title != "" { // the layout prints the title
			if i := strings.Index(html, "</h1>\n"); i >= 0 {
				html = html[i+len("</h1>\n"):]
			}
		}
		title := p.page.Title
		if title == "" {
			title = p.item.Title
		}
		data := pageData{
			Title: title, Description: description(p.page.HTML), Content: template.HTML(html),
			Nav: nav, Current: p.item.File, Outline: outline,
			RepoURL: repo, EditURL: repo + "/edit/" + branch + "/" + p.item.Src, Root: "../",
		}
		if err := write(layout, filepath.Join(out, "docs", p.item.File), data); err != nil {
			return err
		}
	}
	if err := write(index, filepath.Join(out, "index.html"), pageData{Nav: nav, RepoURL: repo, Root: ""}); err != nil {
		return err
	}
	if err := copyDir(filepath.Join(siteDir, "assets"), filepath.Join(out, "assets")); err != nil {
		return err
	}
	// Serve files as they are; there is nothing for Jekyll to do.
	return os.WriteFile(filepath.Join(out, ".nojekyll"), nil, 0o644)
}

type docFile struct{ file, nav string }

func docFiles(dir string) ([]docFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	present := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") && !skip[e.Name()] {
			present[e.Name()] = true
		}
	}
	var files []docFile
	for _, d := range docOrder {
		if present[d.file] {
			files = append(files, docFile{d.file, d.nav})
			delete(present, d.file)
		}
	}
	var rest []string
	for f := range present {
		rest = append(rest, f)
	}
	sort.Strings(rest)
	for _, f := range rest {
		files = append(files, docFile{f, ""})
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no Markdown files in %s", dir)
	}
	return files, nil
}

func isExternal(u string) bool {
	for _, p := range []string{"http://", "https://", "mailto:", "//"} {
		if strings.HasPrefix(u, p) {
			return true
		}
	}
	return false
}

// rewriteLink maps a link written for GitHub's Markdown view to the site:
// docs/x.md becomes x.html, and other repository paths point at GitHub.
func rewriteLink(u, docsDir, repo, blob string) string {
	if u == "" || isExternal(u) || strings.HasPrefix(u, "#") {
		return u
	}
	p, frag, _ := strings.Cut(u, "#")
	if frag != "" {
		frag = "#" + frag
	}
	if strings.HasSuffix(p, ".md") && !strings.Contains(p, "/") {
		return strings.TrimSuffix(p, ".md") + ".html" + frag
	}
	// Resolve against docs/ to get a repository path.
	rp := path.Clean(path.Join(filepath.ToSlash(docsDir), p))
	if strings.HasPrefix(rp, "../") {
		// ../../issues and similar: GitHub UI routes relative to the repo.
		return repo + "/" + strings.TrimLeft(strings.TrimPrefix(rp, ".."), "/") + frag
	}
	return blob + rp + frag
}

// localTarget says whether a link points at a published docs page and which.
func localTarget(u, current string) (file, frag string, local bool) {
	if u == "" || isExternal(u) {
		return "", "", false
	}
	p, frag, _ := strings.Cut(u, "#")
	if p == "" {
		return current, frag, true
	}
	if strings.HasSuffix(p, ".md") && !strings.Contains(p, "/") {
		return strings.TrimSuffix(p, ".md") + ".html", frag, true
	}
	return "", "", true
}

// description is the first paragraph's text, for <meta name="description">.
func description(html string) string {
	i := strings.Index(html, "<p>")
	if i < 0 {
		return ""
	}
	j := strings.Index(html[i:], "</p>")
	if j < 0 {
		return ""
	}
	d := strings.Join(strings.Fields(stripTags(html[i+3:i+j])), " ")
	if r := []rune(d); len(r) > 200 {
		d = string(r[:197]) + "…"
	}
	return d
}

func write(t *template.Template, file string, data pageData) error {
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	return os.WriteFile(file, buf.Bytes(), 0o644)
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}
