package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OIDC provides discovery, JWKS caching and JWT verification for any
// standards-compliant IdP (Entra ID, Okta, Auth0, Ping, Keycloak, Google...).
type OIDC struct {
	client *http.Client
	mu     sync.Mutex
	disc   map[string]*discovery
	keys   map[string]*jwks
}

type discovery struct {
	Issuer        string `json:"issuer"`
	AuthURL       string `json:"authorization_endpoint"`
	TokenURL      string `json:"token_endpoint"`
	JWKSURL       string `json:"jwks_uri"`
	UserinfoURL   string `json:"userinfo_endpoint"`
	EndSessionURL string `json:"end_session_endpoint"`
	fetched       time.Time
}

type jwks struct {
	keys    map[string]crypto.PublicKey
	fetched time.Time
}

func NewOIDC() *OIDC {
	return &OIDC{client: &http.Client{Timeout: 10 * time.Second}, disc: map[string]*discovery{}, keys: map[string]*jwks{}}
}

func (o *OIDC) Discover(ctx context.Context, issuer string) (*discovery, error) {
	issuer = strings.TrimRight(issuer, "/")
	o.mu.Lock()
	d := o.disc[issuer]
	o.mu.Unlock()
	if d != nil && time.Since(d.fetched) < time.Hour {
		return d, nil
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, issuer+"/.well-known/openid-configuration", nil)
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("oidc discovery: status %d", resp.StatusCode)
	}
	d = &discovery{}
	if err := json.NewDecoder(resp.Body).Decode(d); err != nil {
		return nil, err
	}
	d.fetched = time.Now()
	o.mu.Lock()
	o.disc[issuer] = d
	o.mu.Unlock()
	return d, nil
}

func (o *OIDC) keyFor(ctx context.Context, jwksURL, kid string) (crypto.PublicKey, error) {
	o.mu.Lock()
	ks := o.keys[jwksURL]
	o.mu.Unlock()
	if ks != nil {
		if k, ok := ks.keys[kid]; ok {
			return k, nil
		}
		if time.Since(ks.fetched) < time.Minute { // rate-limit refetch on unknown kid
			return nil, fmt.Errorf("unknown signing key %q", kid)
		}
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, nil)
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var raw struct {
		Keys []struct {
			Kty, Kid, N, E, Crv, X, Y, Use string
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	ks = &jwks{keys: map[string]crypto.PublicKey{}, fetched: time.Now()}
	b64 := base64.RawURLEncoding.DecodeString
	for _, k := range raw.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		switch k.Kty {
		case "RSA":
			n, err1 := b64(k.N)
			e, err2 := b64(k.E)
			if err1 != nil || err2 != nil {
				continue
			}
			ks.keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		case "EC":
			x, err1 := b64(k.X)
			y, err2 := b64(k.Y)
			if err1 != nil || err2 != nil {
				continue
			}
			var c elliptic.Curve
			switch k.Crv {
			case "P-256":
				c = elliptic.P256()
			case "P-384":
				c = elliptic.P384()
			case "P-521":
				c = elliptic.P521()
			default:
				continue
			}
			ks.keys[k.Kid] = &ecdsa.PublicKey{Curve: c, X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		case "OKP":
			if x, err := b64(k.X); err == nil && k.Crv == "Ed25519" {
				ks.keys[k.Kid] = ed25519.PublicKey(x)
			}
		}
	}
	o.mu.Lock()
	o.keys[jwksURL] = ks
	o.mu.Unlock()
	if k, ok := ks.keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("unknown signing key %q", kid)
}

// Claims is a decoded JWT payload.
type Claims map[string]any

func (c Claims) Str(k string) string { s, _ := c[k].(string); return s }

func (c Claims) Strings(k string) []string {
	switch v := c[k].(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// Email returns the best available email-like claim (Entra often uses preferred_username/upn).
func (c Claims) Email() string {
	for _, k := range []string{"email", "preferred_username", "upn", "unique_name"} {
		if s := c.Str(k); strings.Contains(s, "@") {
			return strings.ToLower(s)
		}
	}
	return ""
}

func UnverifiedClaims(tok string) (Claims, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return nil, errors.New("not a JWT")
	}
	p, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var c Claims
	return c, json.Unmarshal(p, &c)
}

// VerifyJWT validates signature, issuer, audience and time claims.
func (o *OIDC) VerifyJWT(ctx context.Context, tok, issuer string, audiences []string) (Claims, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed JWT")
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, err
	}
	var hdr struct{ Alg, Kid string }
	if err := json.Unmarshal(hb, &hdr); err != nil {
		return nil, err
	}
	d, err := o.Discover(ctx, issuer)
	if err != nil {
		return nil, err
	}
	key, err := o.keyFor(ctx, d.JWKSURL, hdr.Kid)
	if err != nil {
		return nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, err
	}
	signed := []byte(parts[0] + "." + parts[1])
	if err := verifySig(hdr.Alg, key, signed, sig); err != nil {
		return nil, err
	}
	c, err := UnverifiedClaims(tok)
	if err != nil {
		return nil, err
	}
	if strings.TrimRight(c.Str("iss"), "/") != strings.TrimRight(d.Issuer, "/") {
		return nil, fmt.Errorf("issuer mismatch: %s", c.Str("iss"))
	}
	if len(audiences) > 0 {
		ok := false
		for _, a := range c.Strings("aud") {
			for _, want := range audiences {
				if a == want {
					ok = true
				}
			}
		}
		if !ok {
			return nil, errors.New("audience mismatch")
		}
	}
	now := float64(time.Now().Unix())
	if exp, ok := c["exp"].(float64); !ok || now > exp+60 {
		return nil, errors.New("token expired")
	}
	if nbf, ok := c["nbf"].(float64); ok && now+60 < nbf {
		return nil, errors.New("token not yet valid")
	}
	return c, nil
}

func verifySig(alg string, key crypto.PublicKey, signed, sig []byte) error {
	hashFor := func(h crypto.Hash) []byte {
		switch h {
		case crypto.SHA256:
			s := sha256.Sum256(signed)
			return s[:]
		case crypto.SHA384:
			s := sha512.Sum384(signed)
			return s[:]
		default:
			s := sha512.Sum512(signed)
			return s[:]
		}
	}
	var h crypto.Hash
	switch alg[len(alg)-3:] {
	case "256":
		h = crypto.SHA256
	case "384":
		h = crypto.SHA384
	case "512":
		h = crypto.SHA512
	}
	switch {
	case strings.HasPrefix(alg, "RS"):
		k, ok := key.(*rsa.PublicKey)
		if !ok {
			return errors.New("key type mismatch")
		}
		return rsa.VerifyPKCS1v15(k, h, hashFor(h), sig)
	case strings.HasPrefix(alg, "PS"):
		k, ok := key.(*rsa.PublicKey)
		if !ok {
			return errors.New("key type mismatch")
		}
		return rsa.VerifyPSS(k, h, hashFor(h), sig, nil)
	case strings.HasPrefix(alg, "ES"):
		k, ok := key.(*ecdsa.PublicKey)
		if !ok {
			return errors.New("key type mismatch")
		}
		n := len(sig) / 2
		r, s := new(big.Int).SetBytes(sig[:n]), new(big.Int).SetBytes(sig[n:])
		if !ecdsa.Verify(k, hashFor(h), r, s) {
			return errors.New("invalid signature")
		}
		return nil
	case alg == "EdDSA":
		k, ok := key.(ed25519.PublicKey)
		if !ok || !ed25519.Verify(k, signed, sig) {
			return errors.New("invalid signature")
		}
		return nil
	}
	return fmt.Errorf("unsupported alg %q", alg)
}

// Exchange trades an authorization code for tokens (client_secret_post, PKCE).
func (o *OIDC) Exchange(ctx context.Context, issuer, clientID, secret, code, redirect, verifier string) (idToken string, err error) {
	d, err := o.Discover(ctx, issuer)
	if err != nil {
		return "", err
	}
	vals := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "client_id": {clientID}, "code_verifier": {verifier}}
	if secret != "" {
		vals.Set("client_secret", secret)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, d.TokenURL, strings.NewReader(vals.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
		Desc    string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.IDToken == "" {
		err := fmt.Errorf("token exchange failed: %s %s", out.Error, out.Desc)
		if strings.Contains(out.Desc, "AADSTS700025") {
			err = fmt.Errorf("%w (the Entra app registration is a public client: register the redirect URI under the \"Web\" platform and set \"Allow public client flows\" to No, or remove the client secret from this identity provider to use PKCE only)", err)
		}
		return "", err
	}
	return out.IDToken, nil
}

// PKCE returns a verifier and its S256 challenge.
func PKCE() (verifier, challenge string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	s := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(s[:])
}
