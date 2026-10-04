package providers

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Custom connects to any OpenAI- or Anthropic-compatible API with credentials
// managed by the gateway. The credential name is required: every catalog model
// routed through it must be named "<model>@<name>" (see ModelSuffix).
//
//	custom://api.acme.com/v1?name=acme&auth=basic&username=..&password=..
//	    HTTP Basic on every request.
//	custom://api.acme.com/v1?name=acme&auth=basic&username=<client id>&password=<secret>&token_url=https://idp/oauth/token
//	    Basic client credentials exchanged for an access + refresh token
//	    (grant_type=client_credentials, or grant_type=password to send
//	    username/password as resource-owner credentials).
//	custom://api.acme.com/v1?name=acme&auth=bearer&token=..&refresh_token=..&token_url=..&client_id=..&client_secret=..
//	    Bearer token, renewed with the refresh token when it expires or is rejected.
//	custom://api.acme.com/v1?name=acme&auth=oauth&token_url=..&client_id=..&client_secret=..[&grant_type=authorization_code&authorize_url=..]
//	    OAuth 2.0: client credentials (default), or authorization code where an
//	    admin connects the provider once in the console (see OAuthApp).
//
// format=openai (default; POST {url}/chat/completions) or format=anthropic
// (POST {url}/v1/messages). Optional: scope, audience, base_url (for http://).
func newCustom(b baseProvider, c *Conn, opt Options) (Provider, error) {
	name := c.Get("name")
	if !credName.MatchString(name) {
		return nil, fmt.Errorf("custom: name is required (lowercase letters, digits, '.', '_' or '-'); models through this credential are named <model>%s<name>", ModelSuffixSep)
	}
	base := strings.TrimRight(c.Get("base_url"), "/")
	if base == "" {
		if c.Host == "" {
			return nil, fmt.Errorf("custom: API URL required, e.g. custom://api.example.com/v1")
		}
		base = "https://" + c.Host
	}
	src, err := newCredSource(c, b.client, opt)
	if err != nil {
		return nil, err
	}
	b.client = &http.Client{Timeout: b.client.Timeout, Transport: &credTransport{base: b.client.Transport, src: src}}
	switch f := strings.ToLower(c.Get("format")); f {
	case "", "openai":
		return &OpenAI{baseProvider: b, base: base}, nil
	case "anthropic":
		return &Anthropic{baseProvider: b, base: base, external: true}, nil
	default:
		return nil, fmt.Errorf("custom: format must be openai or anthropic, got %q", f)
	}
}

// newOAuthProvider builds an Anthropic or OpenAI-compatible provider whose
// requests carry OAuth access tokens managed by the gateway.
func newOAuthProvider(b baseProvider, c *Conn, opt Options) (Provider, error) {
	src, err := newCredSource(c, b.client, opt)
	if err != nil {
		return nil, err
	}
	b.client = &http.Client{Timeout: b.client.Timeout, Transport: &credTransport{base: b.client.Transport, src: src}}
	base := strings.TrimRight(c.Get("base_url"), "/")
	if c.Kind == KindAnthropic {
		if base == "" {
			host := c.Host
			if host == "" {
				host = "api.anthropic.com"
			}
			base = "https://" + host
		}
		return &Anthropic{baseProvider: b, base: base, version: c.Get("anthropic_version"), external: true}, nil
	}
	if base == "" {
		host := c.Host
		if host == "" {
			host = "api.openai.com"
		}
		base = "https://" + host + "/v1"
	}
	return &OpenAI{baseProvider: b, base: base}, nil
}

// ModelSuffixSep separates a model from its credential name: "gpt-4o@acme".
const ModelSuffixSep = "@"

var credName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// ModelSuffix returns the required public-name suffix for models routed
// through p ("@acme"), or "" when p is not a custom credential provider.
func ModelSuffix(p Provider) string {
	if bp, ok := p.(interface{ credentialName() string }); ok {
		if n := bp.credentialName(); n != "" {
			return ModelSuffixSep + n
		}
	}
	return ""
}

// ModelSuffixFor is ModelSuffix from a connection string, for callers that
// have not built the provider.
func ModelSuffixFor(conn string) string {
	c, err := ParseConn(conn)
	if err != nil || c.Kind != KindCustom {
		return ""
	}
	return ModelSuffixSep + c.Get("name")
}

func (b *baseProvider) credentialName() string {
	if b.kind != KindCustom || b.conn == nil {
		return ""
	}
	return b.conn.Get("name")
}

// ---------------------------------------------------------------------------
// Tokens

// Tokens is an access/refresh token pair. Seed identifies the configured
// credentials, so stored tokens are discarded when an admin changes them.
type Tokens struct {
	Access  string
	Refresh string
	Expiry  time.Time // zero: unknown, used until rejected
	Seed    string
}

// TokenStore shares tokens across replicas and restarts. Update runs fn under
// a lock spanning all replicas, with the currently stored tokens (nil if
// none), and stores what fn returns.
type TokenStore interface {
	Load(ctx context.Context, providerID string) (*Tokens, error)
	Update(ctx context.Context, providerID string, fn func(cur *Tokens) (*Tokens, error)) (*Tokens, error)
}

type credSource struct {
	client *http.Client
	store  TokenStore
	id     string

	auth                     string // basic | bearer
	username, password       string
	tokenURL, grantType      string
	clientID, clientSecret   string
	scope, audience          string
	authorizeURL, clientAuth string
	staticToken, seedRefresh string
	seed                     string

	mu     sync.Mutex
	tok    Tokens
	loaded bool
}

func newCredSource(c *Conn, client *http.Client, opt Options) (*credSource, error) {
	s := &credSource{client: client, store: opt.Tokens, id: opt.ID,
		auth: c.Get("auth"), username: c.Get("username"), password: c.Get("password"),
		tokenURL: c.Get("token_url"), grantType: c.Get("grant_type"), clientID: c.Get("client_id"), clientSecret: c.Get("client_secret"),
		scope: c.Get("scope"), audience: c.Get("audience"), staticToken: c.Get("token"), seedRefresh: c.Get("refresh_token"),
		authorizeURL: c.Get("authorize_url"), clientAuth: strings.ToLower(c.Get("client_auth"))}
	switch s.auth {
	case "basic":
		if s.username == "" || s.password == "" {
			return nil, fmt.Errorf("custom: basic auth needs username and password")
		}
		if s.grantType == "" {
			s.grantType = "client_credentials"
		}
		if s.grantType != "client_credentials" && s.grantType != "password" {
			return nil, fmt.Errorf("custom: grant_type must be client_credentials or password")
		}
	case "bearer":
		if s.staticToken == "" && s.seedRefresh == "" {
			return nil, fmt.Errorf("custom: bearer auth needs a token and/or a refresh_token")
		}
		if s.seedRefresh != "" && s.tokenURL == "" {
			return nil, fmt.Errorf("custom: refresh_token needs token_url")
		}
	case "oauth":
		if s.tokenURL == "" || s.clientID == "" {
			return nil, fmt.Errorf("oauth: token_url and client_id are required")
		}
		if s.grantType == "" {
			s.grantType = "client_credentials"
		}
		switch s.grantType {
		case "client_credentials":
			if s.clientSecret == "" {
				return nil, fmt.Errorf("oauth: client credentials need client_secret")
			}
		case "authorization_code":
			if s.authorizeURL == "" {
				return nil, fmt.Errorf("oauth: authorization_code needs authorize_url")
			}
		default:
			return nil, fmt.Errorf("oauth: grant_type must be client_credentials or authorization_code")
		}
		if s.clientAuth == "" {
			s.clientAuth = "basic"
		}
		if s.clientAuth != "basic" && s.clientAuth != "post" {
			return nil, fmt.Errorf("oauth: client_auth must be basic or post")
		}
	default:
		return nil, fmt.Errorf("custom: auth must be basic, bearer or oauth")
	}
	for name, v := range map[string]string{"token_url": s.tokenURL, "authorize_url": s.authorizeURL} {
		if v == "" {
			continue
		}
		if u, err := url.Parse(v); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, fmt.Errorf("%s must be an http(s) URL", name)
		}
	}
	h := sha256.Sum256([]byte(strings.Join([]string{s.auth, s.tokenURL, s.authorizeURL, s.grantType, s.username, s.password, s.clientID, s.clientSecret, s.clientAuth, s.staticToken, s.seedRefresh, s.scope, s.audience}, "\x00")))
	s.seed = hex.EncodeToString(h[:8])
	s.tok = Tokens{Access: s.staticToken, Refresh: s.seedRefresh, Seed: s.seed}
	return s, nil
}

// usesTokens reports whether requests carry an access token (vs. plain Basic).
func (s *credSource) usesTokens() bool { return s.auth == "bearer" || s.tokenURL != "" }

// canRenew reports whether a rejected token can be replaced.
func (s *credSource) canRenew() bool {
	return s.tokenURL != "" && (s.auth == "basic" || s.auth == "oauth" || s.seedRefresh != "")
}

func (s *credSource) shared() bool { return s.store != nil && s.id != "" && s.id != "validate" }

func fresh(t *Tokens) bool {
	return t.Access != "" && (t.Expiry.IsZero() || time.Until(t.Expiry) > time.Minute)
}

// header returns the Authorization header value for the next request.
func (s *credSource) header(ctx context.Context) (string, error) {
	if !s.usesTokens() {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(s.username+":"+s.password)), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded && s.shared() {
		s.loaded = true
		if t, err := s.store.Load(ctx, s.id); err == nil && t != nil && t.Seed == s.seed {
			s.tok = *t
		}
	}
	if fresh(&s.tok) || (s.tok.Access != "" && !s.canRenew()) {
		return "Bearer " + s.tok.Access, nil
	}
	if err := s.renewLocked(ctx, s.tok.Access); err != nil {
		return "", err
	}
	return "Bearer " + s.tok.Access, nil
}

// rejected renews after the upstream refused stale; it is a no-op if another
// request already replaced that token.
func (s *credSource) rejected(ctx context.Context, stale string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if "Bearer "+s.tok.Access != stale {
		return nil
	}
	return s.renewLocked(ctx, s.tok.Access)
}

func (s *credSource) renewLocked(ctx context.Context, stale string) error {
	if !s.shared() {
		t, err := s.obtain(ctx, s.tok.Refresh)
		if err != nil {
			return err
		}
		s.tok = *t
		return nil
	}
	t, err := s.store.Update(ctx, s.id, func(cur *Tokens) (*Tokens, error) {
		if cur != nil && cur.Seed == s.seed {
			if cur.Access != stale && fresh(cur) {
				return cur, nil // another replica already renewed
			}
			return s.obtain(ctx, cur.Refresh)
		}
		return s.obtain(ctx, s.tok.Refresh)
	})
	if err != nil {
		return err
	}
	s.tok = *t
	return nil
}

// obtain gets a new token: refresh grant first, then (for Basic) the
// configured primary grant.
func (s *credSource) obtain(ctx context.Context, refresh string) (*Tokens, error) {
	if s.tokenURL == "" {
		return nil, fmt.Errorf("custom: token rejected and no token_url to renew it")
	}
	var lastErr error
	if refresh != "" {
		v := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}
		t, err := s.tokenRequest(ctx, v)
		if err == nil {
			if t.Refresh == "" {
				t.Refresh = refresh
			}
			return t, nil
		}
		lastErr = err
	}
	if s.auth == "oauth" && s.grantType == "authorization_code" {
		if lastErr == nil {
			return nil, fmt.Errorf("oauth: not connected; an admin must connect this provider (Providers → Connect)")
		}
		return nil, fmt.Errorf("oauth: authorization expired or was revoked (%v); reconnect this provider (Providers → Connect)", lastErr)
	}
	if s.auth == "bearer" {
		if lastErr == nil {
			lastErr = fmt.Errorf("no refresh token")
		}
		return nil, fmt.Errorf("custom: refreshing access token: %w", lastErr)
	}
	v := url.Values{"grant_type": {s.grantType}}
	if s.grantType == "password" {
		v.Set("username", s.username)
		v.Set("password", s.password)
	}
	t, err := s.tokenRequest(ctx, v)
	if err != nil {
		return nil, fmt.Errorf("custom: obtaining access token: %w", err)
	}
	return t, nil
}

func (s *credSource) tokenRequest(ctx context.Context, v url.Values) (*Tokens, error) {
	if s.scope != "" {
		v.Set("scope", s.scope)
	}
	if s.audience != "" {
		v.Set("audience", s.audience)
	}
	// Client authentication (RFC 6749 §2.3.1): the Basic credentials for
	// client_credentials, else an explicit client_id/secret.
	var user, pass string
	switch {
	case s.auth == "oauth" && s.clientAuth == "post":
		v.Set("client_id", s.clientID)
		if s.clientSecret != "" {
			v.Set("client_secret", s.clientSecret)
		}
	case s.auth == "oauth" && s.clientSecret == "":
		v.Set("client_id", s.clientID) // public client (PKCE)
	case s.auth == "basic" && s.grantType == "client_credentials":
		user, pass = s.username, s.password
	case s.clientID != "" && s.clientSecret != "":
		user, pass = s.clientID, s.clientSecret
	case s.clientID != "":
		v.Set("client_id", s.clientID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, strings.NewReader(v.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if user != "" {
		req.SetBasicAuth(url.QueryEscape(user), url.QueryEscape(pass))
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		AccessToken  string      `json:"access_token"`
		RefreshToken string      `json:"refresh_token"`
		ExpiresIn    json.Number `json:"expires_in"`
		Error        string      `json:"error"`
		Desc         string      `json:"error_description"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode/100 != 2 || out.AccessToken == "" {
		msg := strings.TrimSpace(out.Error + " " + out.Desc)
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
			if len(msg) > 200 {
				msg = msg[:200]
			}
		}
		return nil, fmt.Errorf("token endpoint %s: %s", resp.Status, msg)
	}
	t := &Tokens{Access: out.AccessToken, Refresh: out.RefreshToken, Seed: s.seed}
	if n, err := out.ExpiresIn.Int64(); err == nil && n > 0 {
		t.Expiry = time.Now().Add(time.Duration(n) * time.Second)
	}
	return t, nil
}

// credTransport adds credentials to every upstream call and, when the
// upstream rejects a token, renews it and retries the request once.
type credTransport struct {
	base http.RoundTripper
	src  *credSource
}

func (t *credTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	h, err := t.src.header(req.Context())
	if err != nil {
		return nil, err
	}
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", h)
	resp, err := t.base.RoundTrip(r)
	if err != nil || resp.StatusCode != http.StatusUnauthorized || !t.src.usesTokens() || !t.src.canRenew() || (req.Body != nil && req.GetBody == nil) {
		return resp, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if err := t.src.rejected(req.Context(), h); err != nil {
		return nil, err
	}
	if h, err = t.src.header(req.Context()); err != nil {
		return nil, err
	}
	r = req.Clone(req.Context())
	if req.GetBody != nil {
		if r.Body, err = req.GetBody(); err != nil {
			return nil, err
		}
	}
	r.Header.Set("Authorization", h)
	return t.base.RoundTrip(r)
}

// ---------------------------------------------------------------------------
// OAuth 2.0 authorization code: connecting a provider in the console

// OAuthApp is a provider's OAuth client configuration, used by the console's
// "Connect" flow (authorization code with PKCE).
type OAuthApp struct {
	src  *credSource
	Kind string
}

// OAuthAppFor returns the OAuth configuration of a connection string, or nil
// when it does not use auth=oauth.
func OAuthAppFor(conn string) (*OAuthApp, error) {
	c, err := ParseConn(conn)
	if err != nil {
		return nil, err
	}
	if c.Get("auth") != "oauth" {
		return nil, nil
	}
	client, err := c.HTTPClient(30 * time.Second)
	if err != nil {
		return nil, err
	}
	src, err := newCredSource(c, client, Options{})
	if err != nil {
		return nil, err
	}
	return &OAuthApp{src: src, Kind: c.Kind}, nil
}

// Grant is client_credentials or authorization_code.
func (o *OAuthApp) Grant() string { return o.src.grantType }

// Seed identifies this configuration; tokens stored under another seed are stale.
func (o *OAuthApp) Seed() string { return o.src.seed }

// AuthCodeURL is where the admin is sent to sign in and consent.
func (o *OAuthApp) AuthCodeURL(state, challenge, redirectURI string) string {
	q := url.Values{"response_type": {"code"}, "client_id": {o.src.clientID}, "redirect_uri": {redirectURI}, "state": {state},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	if o.src.scope != "" {
		q.Set("scope", o.src.scope)
	}
	if o.src.audience != "" {
		q.Set("audience", o.src.audience)
	}
	sep := "?"
	if strings.Contains(o.src.authorizeURL, "?") {
		sep = "&"
	}
	return o.src.authorizeURL + sep + q.Encode()
}

// Exchange trades an authorization code for tokens.
func (o *OAuthApp) Exchange(ctx context.Context, code, verifier, redirectURI string) (*Tokens, error) {
	scope := o.src.scope
	o.src.scope = "" // scope is not repeated on the code exchange
	defer func() { o.src.scope = scope }()
	return o.src.tokenRequest(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirectURI}})
}
