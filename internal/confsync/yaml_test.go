package confsync

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"theophylax/internal/migrate"
)

func TestYAMLRoundTrip(t *testing.T) {
	src := `{
	  "version": 1,
	  "strings": ["plain", "yes", "No", "null", "~", "1.5", "007", "", " padded ", "a: b", "x #y", "#z", "- dash",
	              "line\nbreak", "tab\there", "quote\"d", "back\\slash", "${env:OPENAI_KEY}", "openai://?api_key=****abcd", "ünïcödé", "[x]", "{y}", "&a", "*b", "!c"],
	  "numbers": [0, -3, 2.5, 1e-7, 1000000],
	  "bools": [true, false],
	  "empty_obj": {},
	  "empty_arr": [],
	  "nested": [[1, 2], [], [{"a": 1}], {"weird key": "v", "k": [{"x": {"y": "z"}}]}, {"first": {"deep": true}, "second": 2}],
	  "with null": [null, "after"],
	  "dropped": null,
	  "settings": {"cache_ttl_seconds": 300, "log_payloads": true, "allowed_models": ["a", "b"]}
	}`
	var want any
	if err := json.Unmarshal([]byte(src), &want); err != nil {
		t.Fatal(err)
	}
	b, err := MarshalYAML(json.RawMessage(src), "header line\n\nsecond")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), "# header line\n#\n# second\n") {
		t.Fatalf("header: %q", b[:40])
	}
	got, err := migrate.ParseYAML(string(b))
	if err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	delete(want.(map[string]any), "dropped") // null members are left out
	if !reflect.DeepEqual(got, want) {
		gj, _ := json.Marshal(got)
		wj, _ := json.Marshal(want)
		t.Fatalf("round trip differs\n got: %s\nwant: %s\nyaml:\n%s", gj, wj, b)
	}
}

func TestParseRejectsUnknownKeys(t *testing.T) {
	if _, err := Parse([]byte("version: 1\nmodles: []\n")); err == nil || !strings.Contains(err.Error(), "modles") {
		t.Fatalf("want unknown-key error, got %v", err)
	}
	if _, err := Parse([]byte("version: 2\n")); err == nil {
		t.Fatal("want version error")
	}
	d, err := Parse([]byte("version: 1\nmodels: []\n"))
	if err != nil || d.Models == nil || d.Providers != nil {
		t.Fatalf("an empty list must be kept apart from a missing one: %+v %v", d, err)
	}
}

func TestParseScope(t *testing.T) {
	for _, ok := range []string{"", "org", "team:ml", "project:ml/chat", "user:a@b.c"} {
		if _, _, err := parseScope(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"team:", "project:ml", "project:/x", "key:abc", "teams:x"} {
		if _, _, err := parseScope(bad); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}
