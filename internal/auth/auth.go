// Package auth covers credentials (API keys, passwords, sessions), OIDC/JWT
// verification, principals and role-based access control.
package auth

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"theophylax/internal/store"
	"theophylax/internal/util"
)

const (
	GatewayKeyPrefix = "tpx_"
	PATPrefix        = "tpxp_"
	SCIMPrefix       = "tpxs_"
)

// GenerateKey returns a new random secret, its display prefix and storage hash.
func GenerateKey(prefix string) (plain, display string, hash []byte) {
	plain = prefix + util.RandomToken(40)
	display = plain[:len(prefix)+6] + "…" + plain[len(plain)-4:]
	return plain, display, HashKey(plain)
}

// HashKey hashes a high-entropy API key. SHA-256 is appropriate here: keys are
// 200 bits of randomness, so slow hashing buys nothing and costs latency.
func HashKey(plain string) []byte {
	h := sha256.Sum256([]byte(plain))
	return h[:]
}

const pbkdfIter = 310000

func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	dk, err := pbkdf2.Key(sha256.New, pw, salt, pbkdfIter, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdfIter, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(dk)), nil
}

func CheckPassword(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[2])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	dk, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	return err == nil && subtle.ConstantTimeCompare(dk, want) == 1
}

// ---------------------------------------------------------------------------
// Signed tokens (session cookies, OAuth state). Format: b64(payload).b64(hmac)

func Sign(key []byte, v any) string {
	p, _ := json.Marshal(v)
	ps := base64.RawURLEncoding.EncodeToString(p)
	m := hmac.New(sha256.New, key)
	m.Write([]byte(ps))
	return ps + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func Verify(key []byte, tok string, v any) error {
	ps, sig, ok := strings.Cut(tok, ".")
	if !ok {
		return errors.New("malformed token")
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(ps))
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, m.Sum(nil)) {
		return errors.New("bad signature")
	}
	p, err := base64.RawURLEncoding.DecodeString(ps)
	if err != nil {
		return err
	}
	return json.Unmarshal(p, v)
}

type Session struct {
	UserID string `json:"uid"`
	Exp    int64  `json:"exp"`
	IdP    string `json:"idp,omitempty"`
}

func (s *Session) Valid() bool { return s.UserID != "" && time.Now().Unix() < s.Exp }

// ---------------------------------------------------------------------------
// Principals and RBAC

var roleRank = map[string]int{"": 0, "viewer": 1, "member": 2, "admin": 3, "owner": 4}

func RoleRank(r string) int { return roleRank[r] }

func ValidRole(r string) bool { _, ok := roleRank[r]; return ok && r != "" }

type Principal struct {
	Kind        string // user | key
	User        *store.User
	Key         *store.APIKey
	Memberships []store.Membership
	// Gateway context (from the key or the X-Theophylax-Project header).
	OrgID, TeamID, ProjectID, AgentID string
	Via                               string // api_key | session | oidc_token | pat
}

func (p *Principal) UserID() string {
	if p.User != nil {
		return p.User.ID
	}
	if p.Key != nil && p.Key.UserID != nil {
		return *p.Key.UserID
	}
	return ""
}

func (p *Principal) Email() string {
	if p.User != nil {
		return p.User.Email
	}
	return ""
}

func (p *Principal) IsPlatformAdmin() bool { return p.User != nil && p.User.IsPlatformAdmin }

// Scope is a resolved position in the org → team → project hierarchy.
type Scope struct {
	Type   string // org | team | project
	ID     string
	OrgID  string
	TeamID string
}

// RoleAt returns the effective role at a scope, applying inheritance:
// org roles flow down to every team and project; team roles flow to projects.
func (p *Principal) RoleAt(s Scope) string {
	if p.IsPlatformAdmin() {
		return "owner"
	}
	best := ""
	consider := func(r string) {
		if roleRank[r] > roleRank[best] {
			best = r
		}
	}
	for _, m := range p.Memberships {
		switch m.ScopeType {
		case "org":
			if m.ScopeID == s.OrgID {
				consider(m.Role)
			}
		case "team":
			if (s.Type == "team" && m.ScopeID == s.ID) || (s.Type == "project" && m.ScopeID == s.TeamID) {
				consider(m.Role)
			}
		case "project":
			if s.Type == "project" && m.ScopeID == s.ID {
				consider(m.Role)
			}
		}
	}
	return best
}

func (p *Principal) Can(s Scope, minRole string) bool {
	return roleRank[p.RoleAt(s)] >= roleRank[minRole]
}

// InOrg reports any membership in the org (at any level).
func (p *Principal) InOrg(orgID string) bool {
	if p.IsPlatformAdmin() {
		return true
	}
	for _, m := range p.Memberships {
		if m.OrgID == orgID {
			return true
		}
	}
	return false
}
