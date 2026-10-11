package guardrails

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"regexp"
	"strings"
)

// Secret Shield: detection of credentials and stable, restorable placeholders.

// secretDetector replaces either the whole match or only capture group 1.
type secretDetector struct {
	re    *regexp.Regexp
	group bool
	check func(string) bool
}

var secretDetectors = map[string]secretDetector{
	"aws_key":             {re: regexp.MustCompile(`\b(?:AKIA|ASIA|ABIA|ACCA)[0-9A-Z]{16}\b`)},
	"aws_secret":          {re: regexp.MustCompile(`(?i)aws.{0,24}?(?:secret|sk)[a-z_ ]{0,16}["'\s:=]+([A-Za-z0-9/+]{40})\b`), group: true},
	"private_key":         {re: regexp.MustCompile(`-----BEGIN (?:[A-Z]+ )?PRIVATE KEY(?: BLOCK)?-----[\s\S]*?(?:-----END (?:[A-Z]+ )?PRIVATE KEY(?: BLOCK)?-----|$)`)},
	"jwt":                 {re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)},
	"anthropic_key":       {re: regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)},
	"openai_key":          {re: regexp.MustCompile(`\bsk-(?:proj-|svcacct-|admin-)?[A-Za-z0-9_-]{32,}`)},
	"github_token":        {re: regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{40,})\b`)},
	"gitlab_token":        {re: regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}`)},
	"slack_token":         {re: regexp.MustCompile(`\bxox[abprse]-[A-Za-z0-9-]{10,}`)},
	"slack_webhook":       {re: regexp.MustCompile(`https://hooks\.slack\.com/services/[A-Za-z0-9/_-]{20,}`)},
	"stripe_key":          {re: regexp.MustCompile(`\b(?:sk|rk|pk)_(?:live|test)_[A-Za-z0-9]{16,}`)},
	"google_api_key":      {re: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	"npm_token":           {re: regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}\b`)},
	"pypi_token":          {re: regexp.MustCompile(`\bpypi-AgEIcHlwaS5vcmc[A-Za-z0-9_-]{50,}`)},
	"azure_key":           {re: regexp.MustCompile(`(?i)\b(?:AccountKey|SharedAccessKey)=([A-Za-z0-9+/=]{40,})`), group: true},
	"azure_sas":           {re: regexp.MustCompile(`(?i)[?&]sig=([A-Za-z0-9%+/=]{30,})`), group: true},
	"azure_client_secret": {re: regexp.MustCompile(`\b[A-Za-z0-9_~.-]{3}\dQ~[A-Za-z0-9_~.-]{31,34}\b`)},
	"connection_string":   {re: regexp.MustCompile(`(?i)\b(?:postgres(?:ql)?|mysql|mariadb|mongodb(?:\+srv)?|redis|rediss|amqps?|mssql|sqlserver)://[^\s:@/]+:([^\s@/]+)@`), group: true},
	"bearer_token":        {re: regexp.MustCompile(`(?i)\bauthorization["']?\s*[:=]\s*["']?bearer\s+([A-Za-z0-9._~+/-]{20,}=*)`), group: true},
	// The key may be a compound identifier (DB_PASSWORD, jwt.secret, X-Api-Key),
	// so the keyword is allowed to be a suffix joined by _, - or . rather than
	// requiring a bare word boundary on its left (a plain \b never matches
	// between '_' and the keyword, which let DB_PASSWORD=… through).
	"password_assignment": {re: regexp.MustCompile("(?i)(?:^|[^\\w-])[\\w.-]{0,40}?(?:password|passwd|pwd|passphrase|secret|credentials?|api[_-]?key|apikey|access[_-]?key|access[_-]?token|auth[_-]?token|client[_-]?secret|private[_-]?key)s?[\"']?\\s*[:=]\\s*[\"']?([^\\s\"'`,;)}]{6,})"), group: true, check: looksAssignedSecret},
	"high_entropy":        {re: regexp.MustCompile(`\b[A-Za-z0-9+/_=-]{32,200}\b`), check: highEntropy},
}

// DefaultSecretEntities is what Secret Shield scans for unless told otherwise.
// high_entropy is opt-in because it is noisier on code and data.
var DefaultSecretEntities = []string{"aws_key", "aws_secret", "private_key", "jwt", "anthropic_key", "openai_key", "github_token", "gitlab_token", "slack_token",
	"slack_webhook", "stripe_key", "google_api_key", "npm_token", "pypi_token", "azure_key", "azure_sas", "azure_client_secret", "connection_string", "bearer_token", "password_assignment"}

func shannon(s string) float64 {
	var freq [256]int
	for i := 0; i < len(s); i++ {
		freq[s[i]]++
	}
	h := 0.0
	for _, f := range freq {
		if f > 0 {
			p := float64(f) / float64(len(s))
			h -= p * math.Log2(p)
		}
	}
	return h
}

func classes(s string) int {
	var lo, up, dg, sy bool
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z':
			lo = true
		case c >= 'A' && c <= 'Z':
			up = true
		case c >= '0' && c <= '9':
			dg = true
		default:
			sy = true
		}
	}
	n := 0
	for _, b := range []bool{lo, up, dg, sy} {
		if b {
			n++
		}
	}
	return n
}

var placeholderish = regexp.MustCompile(`(?i)^(?:\*+|x+|\.{3,}|<[^>]*>|\$\{[^}]*\}|\$[A-Z_]+|%[^%]+%|\{\{.*\}\}|changeme|password|secret|example|your[_-].*|xxx.*|redacted.*|\[REDACTED.*|process\.env.*|os\.environ.*|env\(.*|getenv.*|none|null|true|false|undefined)$`)

func looksSecret(v string) bool {
	if placeholderish.MatchString(v) || strings.HasPrefix(v, "[REDACTED:") || strings.HasPrefix(v, "$") || strings.HasPrefix(v, "%") || strings.HasPrefix(v, "{{") {
		return false
	}
	// Identifiers like user_input or self.password are code, not secrets.
	if identLike.MatchString(v) {
		return false
	}
	return shannon(v) >= 2.5 && classes(v) >= 2
}

var identLike = regexp.MustCompile(`^(?:[a-z_][a-z0-9_]*\.)*[a-z_][a-z0-9_]*(?:\(\))?$|^[A-Z_][A-Z0-9_]*$`)

// codeRefLike matches values that are code references rather than literal
// secrets: dotted paths (process.env.X, config.get, self.password). It is a
// prefix match because the value capture stops at '(' or '"', so a call like
// os.getenv("X") arrives truncated as "os.getenv(".
var codeRefLike = regexp.MustCompile(`^[A-Za-z_$][\w$]*\.[A-Za-z_$]`)

// codeIdentLike matches values shaped like source identifiers rather than
// secrets: camelCase/lowercase alphabetic names, snake_case, and CONSTANT_CASE
// env-var names. A real credential almost always mixes in digits or symbols in
// a way these do not.
var codeIdentLike = regexp.MustCompile(`^[a-z][a-zA-Z]*$|^[a-z][a-z0-9]*(?:_[a-z0-9]+)+$|^[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)*$`)

// looksAssignedSecret decides whether the value captured from a <key>=<value>
// assignment (where the key already named a credential) is a literal secret.
// Unlike looksSecret it does not require high entropy — the key name is strong
// evidence on its own — but it still rejects placeholders, environment/file
// references and code identifiers so ordinary source like `password = userPass`
// or `api_key: process.env.KEY` is left untouched.
func looksAssignedSecret(v string) bool {
	if v == "" || placeholderish.MatchString(v) || strings.HasPrefix(v, "[REDACTED:") {
		return false
	}
	switch v[0] {
	case '$', '%', '<', '@', '#', '&':
		return false // ${VAR}, %VAR%, <value>, @ref, interpolations
	}
	// A '(' means a truncated call expression (the capture stops at ')'), e.g.
	// os.getenv( or config.get( — a reference, not a literal secret.
	if strings.HasPrefix(v, "{{") || strings.ContainsRune(v, '(') || codeRefLike.MatchString(v) || codeIdentLike.MatchString(v) {
		return false
	}
	return len(v) >= 6
}

var hexOnly = regexp.MustCompile(`^[a-fA-F0-9]+$`)

func highEntropy(v string) bool {
	if hexOnly.MatchString(v) || strings.Contains(v, "REDACTED") { // git SHAs, digests
		return false
	}
	return shannon(v) >= 4.3 && classes(v) >= 3
}

// Placeholder returns a stable token for a secret: the same secret always maps
// to the same placeholder (keyed HMAC), so conversations and prompt caches stay
// consistent while the value itself never reaches the model.
func (e *Engine) Placeholder(entity, secret string) string {
	m := hmac.New(sha256.New, e.hmacKey)
	m.Write([]byte(secret))
	return "[REDACTED:" + entity + ":" + hex.EncodeToString(m.Sum(nil)[:4]) + "]"
}

// redactSecrets applies secret detectors to s, recording restorable values.
func (e *Engine) redactSecrets(s string, ents []string, apply bool, counts map[string]int, restore map[string]string) (string, bool) {
	changed := false
	for _, en := range ents {
		d, ok := secretDetectors[en]
		if !ok {
			continue
		}
		if !d.re.MatchString(s) {
			continue
		}
		s = d.re.ReplaceAllStringFunc(s, func(m string) string {
			target := m
			if d.group {
				sm := d.re.FindStringSubmatch(m)
				if len(sm) < 2 || sm[1] == "" {
					return m
				}
				target = sm[1]
			}
			if strings.HasPrefix(target, "[REDACTED:") || (d.check != nil && !d.check(target)) {
				return m
			}
			counts[en]++
			if !apply {
				return m
			}
			ph := e.Placeholder(en, target)
			if restore != nil {
				restore[ph] = target
			}
			changed = true
			if d.group {
				return strings.Replace(m, target, ph, 1)
			}
			return ph
		})
	}
	return s, changed
}

func isSecretEntity(en string) bool { _, ok := secretDetectors[en]; return ok }

// ---------------------------------------------------------------------------
// Restoration ("restore on return")

// Restorer swaps placeholders back to original values in model output. It is
// streaming-safe: text that might be the start of a placeholder is held back
// until it can be resolved.
type Restorer struct {
	m       map[string]string
	pending map[string]string
}

func NewRestorer(m map[string]string) *Restorer {
	if len(m) == 0 {
		return nil
	}
	return &Restorer{m: m, pending: map[string]string{}}
}

const phPrefix = "[REDACTED:"

func (r *Restorer) replace(s string, jsonEscaped bool) string {
	if !strings.Contains(s, phPrefix) {
		return s
	}
	for ph, orig := range r.m {
		if strings.Contains(s, ph) {
			v := orig
			if jsonEscaped {
				b, _ := json.Marshal(orig)
				v = string(b[1 : len(b)-1])
			}
			s = strings.ReplaceAll(s, ph, v)
		}
	}
	return s
}

// String restores a complete string.
func (r *Restorer) String(s string, jsonEscaped bool) string {
	if r == nil {
		return s
	}
	return r.replace(s, jsonEscaped)
}

// Feed processes a streamed fragment for a given stream key (e.g. a content block).
func (r *Restorer) Feed(key, s string, jsonEscaped bool) string {
	buf := r.pending[key] + s
	hold := ""
	if i := strings.LastIndex(buf, "["); i >= 0 {
		tail := buf[i:]
		if !strings.Contains(tail, "]") && len(tail) < 64 && (strings.HasPrefix(phPrefix, tail) || strings.HasPrefix(tail, phPrefix)) {
			hold, buf = tail, buf[:i]
		}
	}
	r.pending[key] = hold
	return r.replace(buf, jsonEscaped)
}

// Flush releases anything held for key.
func (r *Restorer) Flush(key string, jsonEscaped bool) string {
	s := r.pending[key]
	delete(r.pending, key)
	return r.replace(s, jsonEscaped)
}

// Walk restores placeholders in every string of a decoded JSON value.
// OpenAI tool-call "arguments" are JSON text, so their replacements are escaped.
func (r *Restorer) Walk(v any) any {
	if r == nil {
		return v
	}
	switch x := v.(type) {
	case string:
		return r.replace(x, false)
	case map[string]any:
		for k, val := range x {
			if s, ok := val.(string); ok && (k == "arguments" || k == "partial_json") {
				x[k] = r.replace(s, true)
				continue
			}
			x[k] = r.Walk(val)
		}
		return x
	case []any:
		for i := range x {
			x[i] = r.Walk(x[i])
		}
		return x
	}
	return v
}
