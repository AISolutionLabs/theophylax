package main

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode"
)

// This is a small Markdown renderer for the subset the project's docs use:
// ATX headings, paragraphs, fenced code, pipe tables, block quotes, nested
// ordered/unordered/task lists, thematic breaks, and inline code, emphasis,
// links, images and autolinks. Raw HTML is escaped, never passed through.

// Heading is one rendered heading, used for the page outline.
type Heading struct {
	Level int
	ID    string
	Text  string
}

// Page is the result of rendering one Markdown document.
type Page struct {
	Title    string
	HTML     string
	Headings []Heading
	Links    []string // every link target, before rewriting
}

type renderer struct {
	b        strings.Builder
	link     func(string) string
	ids      map[string]int
	headings []Heading
	links    []string
}

// Render converts Markdown to HTML. link rewrites each link and image target.
func Render(src string, link func(string) string) Page {
	if link == nil {
		link = func(s string) string { return s }
	}
	r := &renderer{link: link, ids: map[string]int{}}
	src = strings.ReplaceAll(src, "\r\n", "\n")
	src = strings.ReplaceAll(src, "\t", "    ")
	r.blocks(strings.Split(src, "\n"), false)
	p := Page{HTML: r.b.String(), Headings: r.headings, Links: r.links}
	for _, h := range r.headings {
		if h.Level == 1 {
			p.Title = h.Text
			break
		}
	}
	return p
}

var (
	reHeading = regexp.MustCompile(`^ {0,3}(#{1,6})(?:[ \t]+(.*?))?[ \t]*#*[ \t]*$`)
	reFence   = regexp.MustCompile("^( {0,3})(`{3,}|~{3,})[ \t]*([^`]*)$")
	reHR      = regexp.MustCompile(`^ {0,3}((\*[ \t]*){3,}|(-[ \t]*){3,}|(_[ \t]*){3,})$`)
	reList    = regexp.MustCompile(`^( {0,3})([-*+]|\d{1,9}[.)])( +|$)`)
	reQuote   = regexp.MustCompile(`^ {0,3}> ?`)
	reTableSp = regexp.MustCompile(`^ {0,3}\|?[ \t]*:?-+:?[ \t]*(\|[ \t]*:?-+:?[ \t]*)*\|?[ \t]*$`)
	reTask    = regexp.MustCompile(`^\[([ xX])\][ \t]+`)
)

func blank(s string) bool { return strings.TrimSpace(s) == "" }

func indentOf(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }

func isTableStart(lines []string, i int) bool {
	return i+1 < len(lines) && strings.Contains(lines[i], "|") && reTableSp.MatchString(lines[i+1]) && strings.Contains(lines[i+1], "-")
}

// startsBlock reports whether line i begins a block that interrupts a paragraph.
func startsBlock(lines []string, i int) bool {
	l := lines[i]
	return reHeading.MatchString(l) || reFence.MatchString(l) || reHR.MatchString(l) ||
		reQuote.MatchString(l) || reList.MatchString(l) && !blank(reList.ReplaceAllString(l, "")) ||
		isTableStart(lines, i)
}

// blocks renders a sequence of lines. tight renders paragraphs without <p>
// (list items with no blank lines between their blocks).
func (r *renderer) blocks(lines []string, tight bool) {
	for i := 0; i < len(lines); {
		l := lines[i]
		switch {
		case blank(l):
			i++
		case reFence.MatchString(l):
			i = r.fence(lines, i)
		case reHeading.MatchString(l):
			m := reHeading.FindStringSubmatch(l)
			r.heading(len(m[1]), m[2])
			i++
		case reHR.MatchString(l):
			r.b.WriteString("<hr>\n")
			i++
		case isTableStart(lines, i):
			i = r.table(lines, i)
		case reQuote.MatchString(l):
			var q []string
			for i < len(lines) && reQuote.MatchString(lines[i]) {
				q = append(q, reQuote.ReplaceAllString(lines[i], ""))
				i++
			}
			r.b.WriteString("<blockquote>\n")
			r.blocks(q, false)
			r.b.WriteString("</blockquote>\n")
		case reList.MatchString(l):
			i = r.list(lines, i)
		default:
			var para []string
			for i < len(lines) && !blank(lines[i]) && (len(para) == 0 || !startsBlock(lines, i)) {
				para = append(para, strings.TrimSpace(lines[i]))
				i++
			}
			text := r.inline(strings.Join(para, "\n"))
			if tight {
				r.b.WriteString(text + "\n")
			} else {
				r.b.WriteString("<p>" + text + "</p>\n")
			}
		}
	}
}

func (r *renderer) fence(lines []string, i int) int {
	m := reFence.FindStringSubmatch(lines[i])
	ind, marker, info := len(m[1]), m[2], strings.TrimSpace(m[3])
	i++
	var code []string
	for ; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, marker[:1]) && strings.Trim(t, marker[:1]) == "" && len(t) >= len(marker) && indentOf(lines[i]) <= 3 {
			i++
			break
		}
		l := lines[i]
		if n := min(ind, indentOf(l)); n > 0 {
			l = l[n:]
		}
		code = append(code, l)
	}
	lang := strings.Fields(info)
	if len(lang) > 0 {
		fmt.Fprintf(&r.b, `<pre><code class="language-%s">`, html.EscapeString(lang[0]))
	} else {
		r.b.WriteString("<pre><code>")
	}
	r.b.WriteString(html.EscapeString(strings.Join(code, "\n")))
	if len(code) > 0 {
		r.b.WriteString("\n")
	}
	r.b.WriteString("</code></pre>\n")
	return i
}

func (r *renderer) heading(level int, raw string) {
	inner := r.inline(strings.TrimSpace(raw))
	text := stripTags(inner)
	id := slug(text)
	if n, ok := r.ids[id]; ok {
		r.ids[id] = n + 1
		id = fmt.Sprintf("%s-%d", id, n+1)
	} else {
		r.ids[id] = 0
	}
	r.headings = append(r.headings, Heading{Level: level, ID: id, Text: text})
	if level == 1 {
		fmt.Fprintf(&r.b, "<h1 id=\"%s\">%s</h1>\n", id, inner)
		return
	}
	fmt.Fprintf(&r.b, "<h%d id=\"%s\"><a class=\"anchor\" href=\"#%s\" aria-label=\"Link to this section\">#</a>%s</h%d>\n", level, id, id, inner, level)
}

func splitRow(l string) []string {
	l = strings.TrimSpace(l)
	l = strings.TrimPrefix(l, "|")
	if strings.HasSuffix(l, "|") && !strings.HasSuffix(l, `\|`) {
		l = l[:len(l)-1]
	}
	var cells []string
	var cur strings.Builder
	inCode := 0
	for i := 0; i < len(l); i++ {
		c := l[i]
		switch {
		case c == '\\' && i+1 < len(l) && l[i+1] == '|':
			cur.WriteByte('|')
			i++
		case c == '`':
			n := 1
			for i+n < len(l) && l[i+n] == '`' {
				n++
			}
			if inCode == 0 {
				inCode = n
			} else if inCode == n {
				inCode = 0
			}
			cur.WriteString(l[i : i+n])
			i += n - 1
		case c == '|' && inCode == 0:
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	return append(cells, strings.TrimSpace(cur.String()))
}

func (r *renderer) table(lines []string, i int) int {
	head := splitRow(lines[i])
	var align []string
	for _, c := range splitRow(lines[i+1]) {
		switch l, rt := strings.HasPrefix(c, ":"), strings.HasSuffix(c, ":"); {
		case l && rt:
			align = append(align, "center")
		case rt:
			align = append(align, "right")
		default:
			align = append(align, "")
		}
	}
	attr := func(j int) string {
		if j < len(align) && align[j] != "" {
			return ` style="text-align:` + align[j] + `"`
		}
		return ""
	}
	// A header row of empty cells (`| | |`) is dropped, as GitHub shows it empty.
	empty := true
	for _, c := range head {
		empty = empty && c == ""
	}
	r.b.WriteString("<div class=\"table\"><table>\n")
	if !empty {
		r.b.WriteString("<thead><tr>")
		for j, c := range head {
			fmt.Fprintf(&r.b, "<th%s>%s</th>", attr(j), r.inline(c))
		}
		r.b.WriteString("</tr></thead>\n")
	}
	r.b.WriteString("<tbody>\n")
	for i += 2; i < len(lines) && !blank(lines[i]) && strings.Contains(lines[i], "|"); i++ {
		r.b.WriteString("<tr>")
		cells := splitRow(lines[i])
		for j := range head {
			c := ""
			if j < len(cells) {
				c = cells[j]
			}
			fmt.Fprintf(&r.b, "<td%s>%s</td>", attr(j), r.inline(c))
		}
		r.b.WriteString("</tr>\n")
	}
	r.b.WriteString("</tbody></table></div>\n")
	return i
}

type listItem struct {
	lines []string
	task  string // "", " " or "x"
}

func (r *renderer) list(lines []string, i int) int {
	m := reList.FindStringSubmatch(lines[i])
	baseIndent := len(m[1])
	ordered := m[2][0] >= '0' && m[2][0] <= '9'
	delim := m[2][len(m[2])-1]
	start := 1
	if ordered {
		fmt.Sscanf(m[2], "%d", &start)
	}
	var items []listItem
	loose := false
	for i < len(lines) {
		m := reList.FindStringSubmatch(lines[i])
		if m == nil || len(m[1]) > baseIndent+1 || (m[2][0] >= '0' && m[2][0] <= '9') != ordered || m[2][len(m[2])-1] != delim {
			break
		}
		contentIndent := len(m[0])
		if blank(lines[i][len(m[0]):]) {
			contentIndent = len(m[1]) + len(m[2]) + 1
		}
		first := lines[i][len(m[0]):]
		if len(m[3]) > 4 { // indented code after the marker: content starts after one space
			contentIndent = len(m[1]) + len(m[2]) + 1
			first = lines[i][contentIndent:]
		}
		it := listItem{lines: []string{first}}
		i++
		sawBlank := false
		for i < len(lines) {
			l := lines[i]
			if blank(l) {
				sawBlank = true
				it.lines = append(it.lines, "")
				i++
				continue
			}
			if indentOf(l) >= contentIndent {
				if sawBlank {
					loose = loose || !inFence(it.lines)
				}
				sawBlank = false
				it.lines = append(it.lines, l[contentIndent:])
				i++
				continue
			}
			if sawBlank {
				break
			}
			// Lazy continuation of the item's last paragraph.
			if !startsBlock(lines, i) && !inFence(it.lines) {
				it.lines = append(it.lines, strings.TrimSpace(l))
				i++
				continue
			}
			break
		}
		for len(it.lines) > 0 && blank(it.lines[len(it.lines)-1]) {
			it.lines = it.lines[:len(it.lines)-1]
		}
		if t := reTask.FindStringSubmatch(it.lines[0]); t != nil {
			it.task = strings.ToLower(t[1])
			it.lines[0] = it.lines[0][len(t[0]):]
		}
		items = append(items, it)
		if sawBlank && i < len(lines) {
			if n := reList.FindStringSubmatch(lines[i]); n != nil && len(n[1]) <= baseIndent+1 {
				loose = true
				continue
			}
			break
		}
	}
	tag := "ul"
	if ordered {
		tag = "ol"
	}
	cls := ""
	for _, it := range items {
		if it.task != "" {
			cls = ` class="tasks"`
		}
	}
	if ordered && start != 1 {
		fmt.Fprintf(&r.b, "<ol start=\"%d\"%s>\n", start, cls)
	} else {
		fmt.Fprintf(&r.b, "<%s%s>\n", tag, cls)
	}
	for _, it := range items {
		r.b.WriteString("<li>")
		if it.task != "" {
			checked := ""
			if it.task == "x" {
				checked = " checked"
			}
			fmt.Fprintf(&r.b, "<input type=\"checkbox\" disabled%s> ", checked)
		}
		r.blocks(it.lines, !loose)
		s := strings.TrimRight(r.b.String(), "\n")
		r.b.Reset()
		r.b.WriteString(s)
		r.b.WriteString("</li>\n")
	}
	fmt.Fprintf(&r.b, "</%s>\n", tag)
	return i
}

// inFence reports whether lines end inside an open fenced code block.
func inFence(lines []string) bool {
	open := ""
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if open == "" {
			if m := reFence.FindStringSubmatch(l); m != nil {
				open = m[2]
			}
		} else if strings.HasPrefix(t, open[:1]) && strings.Trim(t, open[:1]) == "" && len(t) >= len(open) {
			open = ""
		}
	}
	return open != ""
}

// inline renders inline Markdown.
func (r *renderer) inline(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && strings.IndexByte("\\`*_{}[]()#+-.!|<>~\"'", s[i+1]) >= 0:
			b.WriteString(html.EscapeString(s[i+1 : i+2]))
			i += 2
		case c == '\n':
			b.WriteByte('\n')
			i++
		case c == '`':
			n := 1
			for i+n < len(s) && s[i+n] == '`' {
				n++
			}
			fence := s[i : i+n]
			end := -1
			for j := i + n; j < len(s); {
				k := strings.Index(s[j:], fence)
				if k < 0 {
					break
				}
				k += j
				if (k+n >= len(s) || s[k+n] != '`') && (k == 0 || s[k-1] != '`') {
					end = k
					break
				}
				j = k + n
				for j < len(s) && s[j] == '`' {
					j++
				}
			}
			if end < 0 {
				b.WriteString(fence)
				i += n
				continue
			}
			code := strings.ReplaceAll(s[i+n:end], "\n", " ")
			if len(code) > 2 && code[0] == ' ' && code[len(code)-1] == ' ' && strings.TrimSpace(code) != "" {
				code = code[1 : len(code)-1]
			}
			b.WriteString("<code>" + html.EscapeString(code) + "</code>")
			i = end + n
		case c == '!' && i+1 < len(s) && s[i+1] == '[':
			if text, url, n, ok := parseLink(s[i+1:]); ok {
				r.links = append(r.links, url)
				fmt.Fprintf(&b, `<img src="%s" alt="%s" loading="lazy">`, html.EscapeString(r.link(url)), html.EscapeString(stripTags(r.inline(text))))
				i += 1 + n
				continue
			}
			b.WriteByte('!')
			i++
		case c == '[':
			if text, url, n, ok := parseLink(s[i:]); ok {
				r.links = append(r.links, url)
				href := r.link(url)
				ext := ""
				if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
					ext = ` rel="noopener"`
				}
				fmt.Fprintf(&b, `<a href="%s"%s>%s</a>`, html.EscapeString(href), ext, r.inline(text))
				i += n
				continue
			}
			b.WriteByte('[')
			i++
		case c == '<':
			if j := strings.IndexByte(s[i:], '>'); j > 0 {
				u := s[i+1 : i+j]
				if (strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")) && !strings.ContainsAny(u, " <") {
					fmt.Fprintf(&b, `<a href="%s" rel="noopener">%s</a>`, html.EscapeString(u), html.EscapeString(u))
					i += j + 1
					continue
				}
			}
			b.WriteString("&lt;")
			i++
		case c == '*' || c == '_':
			n := 1
			for i+n < len(s) && s[i+n] == c && n < 3 {
				n++
			}
			if out, adv, ok := r.emphasis(s, i, c, n); ok {
				b.WriteString(out)
				i += adv
				continue
			}
			b.WriteString(s[i : i+n])
			i += n
		case (c == 'h') && (strings.HasPrefix(s[i:], "https://") || strings.HasPrefix(s[i:], "http://")) && (i == 0 || !isWordByte(s[i-1])):
			j := i
			for j < len(s) && !strings.ContainsRune(" \n<>\"'`", rune(s[j])) {
				j++
			}
			for j > i && strings.ContainsRune(".,;:!?)", rune(s[j-1])) {
				j--
			}
			u := s[i:j]
			fmt.Fprintf(&b, `<a href="%s" rel="noopener">%s</a>`, html.EscapeString(u), html.EscapeString(u))
			i = j
		default:
			j := i + 1
			for j < len(s) && !strings.ContainsRune("\\\n`![<*_h", rune(s[j])) {
				j++
			}
			b.WriteString(html.EscapeString(s[i:j]))
			i = j
		}
	}
	return b.String()
}

func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// emphasis renders *em*, **strong** or ***both*** starting at s[i].
func (r *renderer) emphasis(s string, i int, c byte, n int) (string, int, bool) {
	after := i + n
	if after >= len(s) || s[after] == ' ' || s[after] == '\n' {
		return "", 0, false
	}
	if c == '_' && i > 0 && isWordByte(s[i-1]) {
		return "", 0, false // intraword underscores are literal
	}
	delim := strings.Repeat(string(c), n)
	for j := after + 1; j <= len(s)-n; j++ {
		if s[j] == '`' { // skip code spans
			k := strings.IndexByte(s[j+1:], '`')
			if k >= 0 {
				j += k + 1
				continue
			}
		}
		if s[j:j+n] != delim || s[j-1] == ' ' || s[j-1] == '\n' {
			continue
		}
		if j+n < len(s) && s[j+n] == c {
			continue
		}
		if c == '_' && j+n < len(s) && isWordByte(s[j+n]) {
			continue
		}
		inner := r.inline(s[after:j])
		switch n {
		case 1:
			return "<em>" + inner + "</em>", j + n - i, true
		case 2:
			return "<strong>" + inner + "</strong>", j + n - i, true
		default:
			return "<strong><em>" + inner + "</em></strong>", j + n - i, true
		}
	}
	return "", 0, false
}

// parseLink parses [text](url "title") at the start of s and returns the
// number of bytes consumed.
func parseLink(s string) (text, url string, n int, ok bool) {
	depth := 0
	end := -1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '`':
			if k := strings.IndexByte(s[i+1:], '`'); k >= 0 {
				i += k + 1
			}
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 || end+1 >= len(s) || s[end+1] != '(' {
		return "", "", 0, false
	}
	depth = 0
	for j := end + 1; j < len(s); j++ {
		switch s[j] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				dest := strings.TrimSpace(s[end+2 : j])
				if k := strings.Index(dest, ` "`); k >= 0 {
					dest = strings.TrimSpace(dest[:k])
				}
				dest = strings.TrimSuffix(strings.TrimPrefix(dest, "<"), ">")
				return s[1:end], dest, j + 1, true
			}
		case '\n':
			return "", "", 0, false
		}
	}
	return "", "", 0, false
}

var reTag = regexp.MustCompile(`<[^>]*>`)

func stripTags(s string) string { return html.UnescapeString(reTag.ReplaceAllString(s, "")) }

// slug makes a heading ID the way GitHub does, so existing #anchors keep working.
func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}
