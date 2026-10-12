package gateway

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"theophylax/internal/auth"
)

// TagsHeader carries request tags: comma-separated key=value pairs, e.g.
// "feature=search, env=prod". Tags are labels for chargeback and analytics;
// they are stored with the request log and exported with traces, never sent to
// the model.
const TagsHeader = "X-Theophylax-Tags"

const maxTags = 10

var tagKeyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,39}$`)

// ValidTagKey reports whether k is an acceptable tag key.
func ValidTagKey(k string) bool { return tagKeyRe.MatchString(k) }

func validTagValue(v string) bool {
	if v == "" || len(v) > 128 {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// requestTags merges a gateway key's default tags (key metadata
// {"tags": {"k": "v"}}) with the request's tags header, which wins on
// conflict. It returns the tags as JSON and how many pairs were dropped as
// invalid or over the limit.
func requestTags(r *http.Request, p *auth.Principal) (json.RawMessage, int) {
	tags := map[string]string{}
	dropped := 0
	if p != nil && p.Key != nil && len(p.Key.Metadata) > 0 {
		var md struct {
			Tags map[string]string `json:"tags"`
		}
		if json.Unmarshal(p.Key.Metadata, &md) == nil {
			for k, v := range md.Tags {
				k = strings.ToLower(strings.TrimSpace(k))
				if ValidTagKey(k) && validTagValue(v) {
					tags[k] = v
				}
			}
		}
	}
	for _, h := range r.Header.Values(TagsHeader) {
		for _, pair := range strings.Split(h, ",") {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			k, v, ok := strings.Cut(pair, "=")
			k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
			if !ok || !ValidTagKey(k) || !validTagValue(v) {
				dropped++
				continue
			}
			tags[k] = v
		}
	}
	if len(tags) > maxTags {
		keys := make([]string, 0, len(tags))
		for k := range tags {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys[maxTags:] {
			delete(tags, k)
			dropped++
		}
	}
	b, _ := json.Marshal(tags)
	return b, dropped
}

// setTags records the request's tags on the log and reports dropped pairs.
func setTags(w http.ResponseWriter, r *http.Request, p *auth.Principal, st *reqState) {
	var dropped int
	st.log.Tags, dropped = requestTags(r, p)
	if dropped > 0 {
		w.Header().Set("X-Theophylax-Tags-Dropped", strconv.Itoa(dropped))
	}
}
