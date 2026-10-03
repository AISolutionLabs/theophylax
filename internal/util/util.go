// Package util holds small shared helpers: IDs, JSON responses, errors.
package util

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

const crockford = "0123456789abcdefghjkmnpqrstvwxyz"

var seq atomic.Uint32

// NewID returns a sortable, prefixed identifier (ULID-style, lowercase Crockford base32).
func NewID(prefix string) string {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	_, _ = rand.Read(b[6:])
	binary.BigEndian.PutUint16(b[14:], uint16(seq.Add(1))^binary.BigEndian.Uint16(b[14:]))
	// 128 bits -> 26 chars
	var out [26]byte
	hi := binary.BigEndian.Uint64(b[:8])
	lo := binary.BigEndian.Uint64(b[8:])
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = (lo >> 5) | (hi << 59)
		hi >>= 5
	}
	if prefix == "" {
		return string(out[:])
	}
	return prefix + "_" + string(out[:])
}

// RandomToken returns n random bytes encoded with the Crockford alphabet.
func RandomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	out := make([]byte, n)
	for i, c := range b {
		out[i] = crockford[c&31]
	}
	return string(out)
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func Slugify(s string) string {
	s = strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 63 {
		s = s[:63]
	}
	return s
}

var validSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func ValidSlug(s string) bool { return validSlug.MatchString(s) }

// HTTPError is an error that carries a status code and machine-readable type.
type HTTPError struct {
	Status int    `json:"-"`
	Type   string `json:"type"`
	Msg    string `json:"message"`
}

func (e *HTTPError) Error() string { return e.Msg }

func Err(status int, typ, msg string) *HTTPError {
	return &HTTPError{Status: status, Type: typ, Msg: msg}
}

var (
	ErrNotFound  = Err(404, "not_found_error", "not found")
	ErrForbidden = Err(403, "permission_error", "you do not have permission to perform this action")
	ErrUnauth    = Err(401, "authentication_error", "authentication required")
)

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// WriteError writes an error in Anthropic-compatible envelope: {"type":"error","error":{...}}.
func WriteError(w http.ResponseWriter, err error) {
	var he *HTTPError
	if !errors.As(err, &he) {
		he = &HTTPError{Status: 500, Type: "api_error", Msg: err.Error()}
	}
	WriteJSON(w, he.Status, map[string]any{"type": "error", "error": he})
}

func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
		if xr := r.Header.Get("X-Real-Ip"); xr != "" {
			return xr
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func Ptr[T any](v T) *T { return &v }

func Deref[T any](p *T) T {
	var z T
	if p == nil {
		return z
	}
	return *p
}

func Contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// MatchModel reports whether model matches pattern; pattern may end in '*'.
func MatchModel(pattern, model string) bool {
	if pattern == "*" || pattern == model {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(model, strings.TrimSuffix(pattern, "*"))
	}
	return false
}
