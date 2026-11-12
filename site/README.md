# Documentation site

This directory builds the project's GitHub Pages site. It is made of:

- `index.html`: the landing page.
- `layout.html`: the template for every page in `../docs/`.
- `assets/`: the stylesheet, a small script for copy buttons and the outline, and the icon.
- `gen/`: the generator. It is a separate Go module that uses only the standard library. It renders the Markdown in `docs/`, rewrites links, and fails on broken links between pages.

The site is plain HTML and CSS. It has no theme, no Jekyll and no Node build.

## Publish it (one-time setup)

1. In the repository, open **Settings → Pages**.
2. Under **Build and deployment**, set **Source** to **GitHub Actions**.
3. Push to `main`, or run the **Pages** workflow by hand from the **Actions** tab.

The workflow (`.github/workflows/pages.yml`) runs whenever `docs/`, `site/` or the workflow changes:

- **On `main`:** it deploys the site to `https://<owner>.github.io/<repo>/`.
- **On pull requests:** it builds the site and checks links, but does not deploy.

To use a custom domain, set it in **Settings → Pages** and add the DNS record GitHub shows you.

## Preview locally

You need Go 1.24 or later.

```bash
cd site/gen
go test ./...
go run . -docs ../../docs -site .. -out ../../_site
python3 -m http.server -d ../../_site 8000      # open http://localhost:8000
```

`_site/` is build output. Don't commit it.

## Adding or changing pages

- Every `docs/*.md` file is published, except the internal log `upstream-watch.md`.
- To set a page's position and sidebar label, add it to `docOrder` in `gen/main.go`. Pages that aren't listed are added at the end in alphabetical order.
- Write links the way GitHub renders them:
  - `providers.md#bedrock` becomes a link to the site page.
  - `../ROADMAP.md` and other repository paths link to the file on GitHub.
- Heading anchors match GitHub's, so the same links work in both places.
- The renderer supports the Markdown the docs use:
  - headings, paragraphs and block quotes
  - fenced code
  - tables
  - nested, ordered and task lists
  - emphasis, links, images and autolinks

  Raw HTML is shown as text, never rendered.
