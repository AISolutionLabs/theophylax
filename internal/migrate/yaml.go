package migrate

import (
	"fmt"
	"strconv"
	"strings"
)

// parseYAML reads the block-style YAML subset used by LiteLLM configs:
// nested mappings and sequences by indentation, "- key: value" sequence
// items, plain/single/double-quoted scalars, flow sequences and mappings
// ([a, b] / {k: v}), comments, literal (|) and folded (>) block scalars, and
// "---" document markers. Anchors, tags and multi-document streams are not
// supported; they are rare in proxy configs and produce an error instead of a
// silent misread.
func parseYAML(src string) (any, error) {
	var lines []yline
	for i, raw := range strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(raw) == "---" || strings.TrimSpace(raw) == "..." {
			continue
		}
		if strings.Contains(raw, "\t") && strings.TrimLeft(raw, " ") != strings.TrimLeft(raw, " \t") {
			return nil, fmt.Errorf("line %d: tabs are not allowed for indentation", i+1)
		}
		ind := len(raw) - len(strings.TrimLeft(raw, " "))
		lines = append(lines, yline{n: i + 1, ind: ind, text: raw[ind:], raw: raw})
	}
	p := &yparser{lines: lines}
	p.skipBlank()
	if p.i >= len(p.lines) {
		return map[string]any{}, nil
	}
	v, err := p.block(p.lines[p.i].ind)
	if err != nil {
		return nil, err
	}
	p.skipBlank()
	if p.i < len(p.lines) {
		return nil, fmt.Errorf("line %d: unexpected indentation", p.lines[p.i].n)
	}
	return v, nil
}

type yline struct {
	n, ind    int
	text, raw string
}

type yparser struct {
	lines []yline
	i     int
}

func isBlank(t string) bool {
	t = strings.TrimSpace(t)
	return t == "" || strings.HasPrefix(t, "#")
}

func (p *yparser) skipBlank() {
	for p.i < len(p.lines) && isBlank(p.lines[p.i].text) {
		p.i++
	}
}

func (p *yparser) block(ind int) (any, error) {
	p.skipBlank()
	if p.i >= len(p.lines) {
		return nil, nil
	}
	if strings.HasPrefix(p.lines[p.i].text, "- ") || strings.TrimSpace(p.lines[p.i].text) == "-" {
		return p.seq(ind)
	}
	return p.mapping(ind)
}

func (p *yparser) seq(ind int) (any, error) {
	var out []any
	for {
		p.skipBlank()
		if p.i >= len(p.lines) || p.lines[p.i].ind != ind || !(strings.HasPrefix(p.lines[p.i].text, "- ") || strings.TrimSpace(p.lines[p.i].text) == "-") {
			return out, nil
		}
		l := p.lines[p.i]
		rest := strings.TrimSpace(strings.TrimPrefix(l.text, "-"))
		if rest == "" || strings.HasPrefix(rest, "#") {
			p.i++
			v, err := p.childBlock(ind)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			continue
		}
		// "- key: value" starts an inline mapping whose further keys are
		// indented to the column after "- ".
		col := ind + (len(l.text) - len(strings.TrimLeft(strings.TrimPrefix(l.text, "-"), " "))) + 0
		if strings.HasPrefix(rest, "- ") || rest == "-" { // "- - x": a sequence nested inline
			p.lines[p.i] = yline{n: l.n, ind: col, text: rest, raw: l.raw}
			v, err := p.seq(col)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			continue
		}
		if k, _, ok := splitKey(rest); ok && k != "" && !strings.HasPrefix(rest, "{") && !strings.HasPrefix(rest, "[") && !strings.HasPrefix(rest, `"`) && !strings.HasPrefix(rest, "'") {
			p.lines[p.i] = yline{n: l.n, ind: col, text: rest, raw: l.raw}
			v, err := p.mapping(col)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			continue
		}
		p.i++
		v, err := p.scalarOrBlock(rest, ind)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", l.n, err)
		}
		out = append(out, v)
	}
}

func (p *yparser) mapping(ind int) (any, error) {
	out := map[string]any{}
	for {
		p.skipBlank()
		if p.i >= len(p.lines) || p.lines[p.i].ind != ind {
			return out, nil
		}
		l := p.lines[p.i]
		if strings.HasPrefix(l.text, "- ") {
			return out, nil
		}
		k, rest, ok := splitKey(l.text)
		if !ok {
			return nil, fmt.Errorf("line %d: expected \"key: value\"", l.n)
		}
		if strings.HasPrefix(k, "&") || strings.HasPrefix(rest, "&") || strings.HasPrefix(rest, "*") || strings.HasPrefix(k, "<<") {
			return nil, fmt.Errorf("line %d: YAML anchors and aliases are not supported; expand them first", l.n)
		}
		p.i++
		rest = stripComment(rest)
		if rest == "" {
			p.skipBlank()
			// A sequence may sit at the same indentation as its key.
			if p.i < len(p.lines) && p.lines[p.i].ind == ind && strings.HasPrefix(p.lines[p.i].text, "- ") {
				v, err := p.seq(ind)
				if err != nil {
					return nil, err
				}
				out[k] = v
				continue
			}
			v, err := p.childBlock(ind)
			if err != nil {
				return nil, err
			}
			out[k] = v
			continue
		}
		v, err := p.scalarOrBlock(rest, ind)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", l.n, err)
		}
		out[k] = v
	}
}

func (p *yparser) childBlock(parent int) (any, error) {
	p.skipBlank()
	if p.i >= len(p.lines) || p.lines[p.i].ind <= parent {
		return nil, nil
	}
	return p.block(p.lines[p.i].ind)
}

func (p *yparser) scalarOrBlock(s string, parent int) (any, error) {
	if s == "|" || s == "|-" || s == "|+" || s == ">" || s == ">-" || s == ">+" {
		return p.blockScalar(s, parent), nil
	}
	return parseScalar(s)
}

func (p *yparser) blockScalar(style string, parent int) string {
	var parts []string
	ind := -1
	for p.i < len(p.lines) {
		l := p.lines[p.i]
		if strings.TrimSpace(l.raw) == "" {
			parts = append(parts, "")
			p.i++
			continue
		}
		if l.ind <= parent {
			break
		}
		if ind < 0 {
			ind = l.ind
		}
		parts = append(parts, l.raw[min(ind, len(l.raw)):])
		p.i++
	}
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	sep := "\n"
	if strings.HasPrefix(style, ">") {
		sep = " "
	}
	s := strings.Join(parts, sep)
	if !strings.HasSuffix(style, "-") {
		s += "\n"
	}
	return s
}

// splitKey splits "key: value" honouring quoted keys.
func splitKey(t string) (string, string, bool) {
	if strings.HasPrefix(t, `"`) || strings.HasPrefix(t, "'") {
		q := t[:1]
		end := strings.Index(t[1:], q)
		if end < 0 {
			return "", "", false
		}
		k := t[1 : end+1]
		rest := strings.TrimSpace(t[end+2:])
		if !strings.HasPrefix(rest, ":") {
			return "", "", false
		}
		return k, strings.TrimSpace(rest[1:]), true
	}
	for i := 0; i < len(t); i++ {
		if t[i] == ':' && (i == len(t)-1 || t[i+1] == ' ') {
			return strings.TrimSpace(t[:i]), strings.TrimSpace(t[i+1:]), true
		}
		if t[i] == '#' && i > 0 && t[i-1] == ' ' {
			break
		}
	}
	return "", "", false
}

func stripComment(s string) string {
	inS, inD := false, false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			if !inD {
				inS = !inS
			}
		case '"':
			if !inS && (i == 0 || s[i-1] != '\\') {
				inD = !inD
			}
		case '#':
			if !inS && !inD && (i == 0 || s[i-1] == ' ') {
				return strings.TrimSpace(s[:i])
			}
		}
	}
	return strings.TrimSpace(s)
}

func parseScalar(s string) (any, error) {
	s = stripComment(s)
	switch {
	case s == "":
		return nil, nil
	case strings.HasPrefix(s, `"`):
		v, err := strconv.Unquote(s)
		if err != nil {
			return nil, fmt.Errorf("bad double-quoted string %s", s)
		}
		return v, nil
	case strings.HasPrefix(s, "'"):
		if len(s) < 2 || !strings.HasSuffix(s, "'") {
			return nil, fmt.Errorf("bad single-quoted string %s", s)
		}
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'"), nil
	case strings.HasPrefix(s, "["):
		return parseFlow(s)
	case strings.HasPrefix(s, "{"):
		return parseFlow(s)
	case strings.HasPrefix(s, "&"), strings.HasPrefix(s, "*"), strings.HasPrefix(s, "!"):
		return nil, fmt.Errorf("anchors, aliases and tags are not supported (%s)", s)
	}
	switch strings.ToLower(s) {
	case "true", "yes", "on":
		return true, nil
	case "false", "no", "off":
		return false, nil
	case "null", "~":
		return nil, nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return float64(n), nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && !strings.HasPrefix(s, ".") {
		return f, nil
	}
	return s, nil
}

// parseFlow handles single-line flow collections: [a, "b", {k: v}].
func parseFlow(s string) (any, error) {
	fp := &flow{s: s}
	v, err := fp.value()
	if err != nil {
		return nil, err
	}
	fp.ws()
	if fp.i != len(fp.s) {
		return nil, fmt.Errorf("unexpected %q in flow collection", fp.s[fp.i:])
	}
	return v, nil
}

type flow struct {
	s string
	i int
}

func (f *flow) ws() {
	for f.i < len(f.s) && f.s[f.i] == ' ' {
		f.i++
	}
}

func (f *flow) value() (any, error) {
	f.ws()
	if f.i >= len(f.s) {
		return nil, fmt.Errorf("unterminated flow collection")
	}
	switch f.s[f.i] {
	case '[':
		f.i++
		out := []any{}
		for {
			f.ws()
			if f.i < len(f.s) && f.s[f.i] == ']' {
				f.i++
				return out, nil
			}
			v, err := f.value()
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			f.ws()
			switch {
			case f.i < len(f.s) && f.s[f.i] == ',':
				f.i++
			case f.i < len(f.s) && f.s[f.i] == ']':
			default:
				return nil, fmt.Errorf("expected , or ] in flow sequence")
			}
		}
	case '{':
		f.i++
		out := map[string]any{}
		for {
			f.ws()
			if f.i < len(f.s) && f.s[f.i] == '}' {
				f.i++
				return out, nil
			}
			k, err := f.token(":")
			if err != nil {
				return nil, err
			}
			if f.i >= len(f.s) || f.s[f.i] != ':' {
				return nil, fmt.Errorf("expected : in flow mapping")
			}
			f.i++ // ':'
			v, err := f.value()
			if err != nil {
				return nil, err
			}
			ks, _ := k.(string)
			out[ks] = v
			f.ws()
			switch {
			case f.i < len(f.s) && f.s[f.i] == ',':
				f.i++
			case f.i < len(f.s) && f.s[f.i] == '}':
			default:
				return nil, fmt.Errorf("expected , or } in flow mapping")
			}
		}
	}
	return f.token(",]}")
}

func (f *flow) token(stops string) (any, error) {
	f.ws()
	if f.i < len(f.s) && (f.s[f.i] == '"' || f.s[f.i] == '\'') {
		q := f.s[f.i]
		j := f.i + 1
		for j < len(f.s) && (f.s[j] != q || (q == '"' && f.s[j-1] == '\\')) {
			j++
		}
		if j >= len(f.s) {
			return nil, fmt.Errorf("unterminated string")
		}
		v, err := parseScalar(f.s[f.i : j+1])
		f.i = j + 1
		return v, err
	}
	j := f.i
	for j < len(f.s) && !strings.ContainsRune(stops, rune(f.s[j])) {
		j++
	}
	tok := strings.TrimSpace(f.s[f.i:j])
	f.i = j
	return parseScalar(tok)
}

// ParseYAML parses the block-style YAML subset described on parseYAML into
// map[string]any / []any / scalar values (numbers are float64).
func ParseYAML(src string) (any, error) { return parseYAML(src) }
