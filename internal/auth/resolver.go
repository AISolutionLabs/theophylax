package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"theophylax/internal/store"
	"theophylax/internal/util"
)

const SessionCookie = "theophylax_session"

// Resolver turns credentials into Principals, with a short-lived key cache so
// the gateway hot path does not hit the database on every request.
type Resolver struct {
	St         *store.Store
	SessionKey []byte
	OIDC       *OIDC
	ttl        time.Duration
	mu         sync.RWMutex
	cache      map[string]keyCacheEntry
	touchMu    sync.Mutex
	touched    map[string]time.Time
}

type keyCacheEntry struct {
	key  *store.APIKey
	user *store.User
	mem  []store.Membership
	exp  time.Time
	err  error
}

func NewResolver(st *store.Store, sessionKey []byte, ttl time.Duration) *Resolver {
	r := &Resolver{St: st, SessionKey: sessionKey, OIDC: NewOIDC(), ttl: ttl, cache: map[string]keyCacheEntry{}, touched: map[string]time.Time{}}
	go r.flushTouches()
	return r
}

// Invalidate drops cached keys (call after revoke/update on this replica).
func (r *Resolver) Invalidate() {
	r.mu.Lock()
	r.cache = map[string]keyCacheEntry{}
	r.mu.Unlock()
}

// BearerToken extracts a credential from x-api-key or Authorization: Bearer.
func BearerToken(req *http.Request) string {
	if k := req.Header.Get("X-Api-Key"); k != "" {
		return strings.TrimSpace(k)
	}
	if a := req.Header.Get("Authorization"); len(a) > 7 && strings.EqualFold(a[:7], "bearer ") {
		return strings.TrimSpace(a[7:])
	}
	if k := req.Header.Get("Api-Key"); k != "" {
		return strings.TrimSpace(k)
	}
	return ""
}

func (r *Resolver) lookupKey(ctx context.Context, token string) keyCacheEntry {
	h := string(HashKey(token))
	r.mu.RLock()
	e, ok := r.cache[h]
	r.mu.RUnlock()
	if ok && time.Now().Before(e.exp) {
		return e
	}
	e = keyCacheEntry{exp: time.Now().Add(r.ttl)}
	k, err := r.St.KeyByHash(ctx, []byte(h))
	if err != nil {
		e.err = util.Err(401, "authentication_error", "invalid API key")
		if err != util.ErrNotFound {
			e.err = err
			e.exp = time.Now() // don't cache transient DB errors
		}
	} else {
		e.key = k
		if k.UserID != nil {
			if u, err := r.St.UserByID(ctx, *k.UserID); err == nil {
				e.user = u
				e.mem, _ = r.St.MembershipsOf(ctx, u.ID)
			}
		}
	}
	r.mu.Lock()
	if len(r.cache) > 100000 {
		r.cache = map[string]keyCacheEntry{}
	}
	r.cache[h] = e
	r.mu.Unlock()
	return e
}

// FromKey authenticates an API key of the given kind ("gateway" or "pat").
func (r *Resolver) FromKey(ctx context.Context, token, kind string) (*Principal, error) {
	e := r.lookupKey(ctx, token)
	if e.err != nil {
		return nil, e.err
	}
	k := e.key
	if k.Kind != kind {
		return nil, util.Err(401, "authentication_error", fmt.Sprintf("this credential is a %s key and cannot be used here", k.Kind))
	}
	if k.RevokedAt != nil {
		return nil, util.Err(401, "authentication_error", "API key has been revoked")
	}
	if k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt) {
		return nil, util.Err(401, "authentication_error", "API key has expired")
	}
	if e.user != nil && e.user.Status != "active" {
		return nil, util.Err(401, "authentication_error", "key owner is suspended")
	}
	p := &Principal{Kind: "key", Key: k, OrgID: k.OrgID, Via: "api_key"}
	p.TeamID = util.Deref(k.TeamID)
	p.ProjectID = util.Deref(k.ProjectID)
	p.AgentID = util.Deref(k.AgentID)
	if kind == "pat" {
		if e.user == nil {
			return nil, util.Err(401, "authentication_error", "personal access token has no owner")
		}
		p.Kind, p.User, p.Memberships, p.Via = "user", e.user, e.mem, "pat"
	}
	r.touch(k.ID)
	return p, nil
}

func (r *Resolver) touch(id string) {
	r.touchMu.Lock()
	r.touched[id] = time.Now()
	r.touchMu.Unlock()
}

func (r *Resolver) flushTouches() {
	for range time.Tick(30 * time.Second) {
		r.touchMu.Lock()
		t := r.touched
		r.touched = map[string]time.Time{}
		r.touchMu.Unlock()
		for id, ts := range t {
			_, _ = r.St.Pool.Exec(context.Background(), `UPDATE api_keys SET last_used_at=$2 WHERE id=$1`, id, ts)
		}
	}
}

// FromSession authenticates a browser session cookie.
func (r *Resolver) FromSession(ctx context.Context, req *http.Request) (*Principal, error) {
	c, err := req.Cookie(SessionCookie)
	if err != nil {
		return nil, util.ErrUnauth
	}
	var s Session
	if err := Verify(r.SessionKey, c.Value, &s); err != nil || !s.Valid() {
		return nil, util.ErrUnauth
	}
	return r.UserPrincipal(ctx, s.UserID, "session")
}

func (r *Resolver) UserPrincipal(ctx context.Context, userID, via string) (*Principal, error) {
	u, err := r.St.UserByID(ctx, userID)
	if err != nil {
		return nil, util.ErrUnauth
	}
	if u.Status != "active" {
		return nil, util.Err(401, "authentication_error", "account suspended")
	}
	mem, err := r.St.MembershipsOf(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	return &Principal{Kind: "user", User: u, Memberships: mem, Via: via}, nil
}

// FromIdPToken validates a bearer JWT issued by a configured IdP (e.g. an Entra
// access token fetched by an apiKeyHelper) and maps it to a user.
func (r *Resolver) FromIdPToken(ctx context.Context, token string, idps []*store.IdP) (*Principal, error) {
	uc, err := UnverifiedClaims(token)
	if err != nil {
		return nil, util.Err(401, "authentication_error", "invalid token")
	}
	iss := strings.TrimRight(uc.Str("iss"), "/")
	for _, idp := range idps {
		if strings.TrimRight(idp.Issuer, "/") != iss {
			continue
		}
		aud := []string{idp.ClientID}
		if idp.Audience != "" {
			aud = append(aud, strings.Split(idp.Audience, ",")...)
		}
		claims, err := r.OIDC.VerifyJWT(ctx, token, idp.Issuer, aud)
		if err != nil {
			return nil, util.Err(401, "authentication_error", "token rejected: "+err.Error())
		}
		u, err := r.Provision(ctx, idp, claims)
		if err != nil {
			return nil, err
		}
		return r.UserPrincipal(ctx, u.ID, "oidc_token")
	}
	return nil, util.Err(401, "authentication_error", "token issuer is not trusted")
}

// Provision finds or JIT-creates the user for verified claims and applies
// group → role mappings.
func (r *Resolver) Provision(ctx context.Context, idp *store.IdP, c Claims) (*store.User, error) {
	email := c.Email()
	if email == "" {
		return nil, util.Err(403, "permission_error", "identity token has no email claim")
	}
	if len(idp.EmailDomains) > 0 {
		ok := false
		for _, d := range idp.EmailDomains {
			if strings.HasSuffix(email, "@"+strings.ToLower(strings.TrimPrefix(d, "@"))) {
				ok = true
			}
		}
		if !ok {
			return nil, util.Err(403, "permission_error", "email domain not allowed for this identity provider")
		}
	}
	name := c.Str("name")
	u, err := r.St.UserByEmail(ctx, email)
	if err == util.ErrNotFound {
		if !idp.JIT {
			return nil, util.Err(403, "permission_error", "user is not provisioned; contact your administrator")
		}
		u = &store.User{ID: util.NewID("usr"), Email: email, Name: name, Status: "active", IdpID: &idp.ID}
		sub := c.Str("sub")
		u.ExternalID = &sub
		if err := r.St.Insert(ctx, r.St.Pool, "users", u); err != nil {
			return nil, err
		}
		slog.Info("jit provisioned user", "email", email, "idp", idp.Name)
	} else if err != nil {
		return nil, err
	}
	if u.Status != "active" {
		return nil, util.Err(403, "permission_error", "account suspended")
	}
	_, _ = r.St.Pool.Exec(ctx, `UPDATE users SET last_login_at=now(), name=CASE WHEN name='' THEN $2 ELSE name END WHERE id=$1`, u.ID, name)

	// Default org membership plus group mappings (source='idp' rows are managed here).
	var maps []store.RoleMapping
	_ = json.Unmarshal(idp.RoleMappings, &maps)
	groups := map[string]bool{}
	for _, g := range c.Strings(idp.GroupsClaim) {
		groups[g] = true
	}
	for _, g := range c.Strings("roles") {
		groups[g] = true
	}
	want := map[[2]string]string{}
	if idp.DefaultRole != "" && idp.DefaultRole != "none" {
		want[[2]string{"org", idp.OrgID}] = idp.DefaultRole
	}
	for _, m := range maps {
		if groups[m.Group] && ValidRole(m.Role) {
			k := [2]string{m.ScopeType, m.ScopeID}
			if RoleRank(m.Role) > RoleRank(want[k]) {
				want[k] = m.Role
			}
		}
	}
	existing, _ := r.St.MembershipsOf(ctx, u.ID)
	for _, m := range existing {
		if m.Source != "idp" || m.OrgID != idp.OrgID {
			continue
		}
		k := [2]string{m.ScopeType, m.ScopeID}
		if role, ok := want[k]; !ok {
			_, _ = r.St.Pool.Exec(ctx, `DELETE FROM memberships WHERE id=$1`, m.ID)
		} else if role != m.Role {
			_, _ = r.St.Pool.Exec(ctx, `UPDATE memberships SET role=$2 WHERE id=$1`, m.ID, role)
		}
	}
	for k, role := range want {
		_, _ = r.St.Pool.Exec(ctx, `INSERT INTO memberships (id, user_id, org_id, scope_type, scope_id, role, source) VALUES ($1,$2,$3,$4,$5,$6,'idp')
			ON CONFLICT (user_id, scope_type, scope_id) DO UPDATE SET role = CASE WHEN memberships.source='idp' THEN EXCLUDED.role ELSE memberships.role END`,
			util.NewID("mem"), u.ID, idp.OrgID, k[0], k[1], role)
	}
	return u, nil
}
