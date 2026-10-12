package confsync

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// The emitter writes block-style YAML that the gateway's own YAML reader
// (migrate.ParseYAML) reads back to the same value. It works from JSON so
// that struct field order is kept; null members are left out.

type node struct {
	kind byte // 'o' object, 'a' array, 's' scalar
	keys []string
	vals []*node
	text string // scalar: the YAML text
	null bool
}

func decodeOrdered(dec *json.Decoder) (*node, error) {
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch v := t.(type) {
	case json.Delim:
		switch v {
		case '{':
			n := &node{kind: 'o'}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				child, err := decodeOrdered(dec)
				if err != nil {
					return nil, err
				}
				if child.null {
					continue
				}
				n.keys = append(n.keys, kt.(string))
				n.vals = append(n.vals, child)
			}
			_, err := dec.Token()
			return n, err
		case '[':
			n := &node{kind: 'a'}
			for dec.More() {
				child, err := decodeOrdered(dec)
				if err != nil {
					return nil, err
				}
				if child.null {
					child = &node{kind: 's', text: "null"}
				}
				n.vals = append(n.vals, child)
			}
			_, err := dec.Token()
			return n, err
		}
	case nil:
		return &node{kind: 's', null: true}, nil
	case bool:
		return &node{kind: 's', text: strconv.FormatBool(v)}, nil
	case json.Number:
		return &node{kind: 's', text: v.String()}, nil
	case string:
		return &node{kind: 's', text: yamlString(v)}, nil
	}
	return nil, fmt.Errorf("unexpected JSON token %v", t)
}

var (
	bareValue = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_./@+-]*$`)
	bareKey   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./@+-]*$`)
)

func yamlString(s string) string {
	if bareValue.MatchString(s) {
		switch strings.ToLower(s) {
		case "true", "false", "yes", "no", "on", "off", "null":
		default:
			return s
		}
	}
	return strconv.Quote(s)
}

func yamlKey(k string) (string, error) {
	if bareKey.MatchString(k) {
		return k, nil
	}
	if strings.ContainsAny(k, "\"\\\n") {
		return "", fmt.Errorf("cannot write key %q as YAML", k)
	}
	return `"` + k + `"`, nil
}

// MarshalYAML renders v (anything encoding/json accepts) as block YAML.
func MarshalYAML(v any, header string) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	n, err := decodeOrdered(dec)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	for _, l := range strings.Split(strings.TrimRight(header, "\n"), "\n") {
		if l != "" {
			b.WriteString("# " + l + "\n")
		} else if header != "" {
			b.WriteString("#\n")
		}
	}
	switch n.kind {
	case 'o':
		err = writeMap(&b, n, 0)
	case 'a':
		err = writeSeq(&b, n, 0)
	default:
		_, err = io.WriteString(&b, n.text+"\n")
	}
	return b.Bytes(), err
}

func inline(n *node) (string, bool) {
	switch {
	case n.kind == 's':
		return n.text, true
	case n.kind == 'o' && len(n.vals) == 0:
		return "{}", true
	case n.kind == 'a' && len(n.vals) == 0:
		return "[]", true
	}
	return "", false
}

func writeMap(b *bytes.Buffer, n *node, ind int) error {
	pad := strings.Repeat(" ", ind)
	for i, k := range n.keys {
		key, err := yamlKey(k)
		if err != nil {
			return err
		}
		v := n.vals[i]
		if s, ok := inline(v); ok {
			fmt.Fprintf(b, "%s%s: %s\n", pad, key, s)
			continue
		}
		fmt.Fprintf(b, "%s%s:\n", pad, key)
		if v.kind == 'o' {
			err = writeMap(b, v, ind+2)
		} else {
			err = writeSeq(b, v, ind+2)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func writeSeq(b *bytes.Buffer, n *node, ind int) error {
	pad := strings.Repeat(" ", ind)
	for _, v := range n.vals {
		if s, ok := inline(v); ok {
			fmt.Fprintf(b, "%s- %s\n", pad, s)
			continue
		}
		var sub bytes.Buffer
		var err error
		if v.kind == 'o' {
			err = writeMap(&sub, v, ind+2)
		} else {
			err = writeSeq(&sub, v, ind+2)
		}
		if err != nil {
			return err
		}
		// "- key: value" when the item is a mapping whose first key is bare;
		// otherwise the item goes on the following lines.
		if v.kind == 'o' && bareKey.MatchString(v.keys[0]) {
			b.WriteString(pad + "- ")
			b.Write(sub.Bytes()[ind+2:])
		} else {
			b.WriteString(pad + "-\n")
			b.Write(sub.Bytes())
		}
	}
	return nil
}
