package api

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"theophylax/internal/auth"
	"theophylax/internal/providers"
	"theophylax/internal/store"
	"theophylax/internal/util"
)

// OAuth 2.0 authorization code flow for providers with auth=oauth and
// grant_type=authorization_code: an admin signs in at the provider once; the
// gateway stores the tokens (encrypted, shared by all replicas) and refreshes
// them from then on.

func (a *API) providerOAuthRedirectURI() string {
	return a.Cfg.PublicURL + "/auth/provider-oauth/callback"
}

type providerOAuthState struct {
	Provider string `json:"p"`
	Seed     string `json:"s"`
	User     string `json:"u"`
	Verifier string `json:"v"`
	Nonce    string `json:"n"`
	Exp      int64  `json:"e"`
}

// providerOAuthStatus is shown on the Providers page.
type providerOAuthStatus struct {
	Grant       string     `json:"grant"`
	Connected   bool       `json:"connected"`
	ConnectedBy string     `json:"connected_by,omitempty"`
	ConnectedAt *time.Time `json:"connected_at,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	RedirectURI string     `json:"redirect_uri"`
}

// oauthApp loads a provider and its OAuth configuration, checking the caller
// may manage it.
func (a *API) oauthApp(c *Ctx, id string) (*store.Provider, *providers.OAuthApp, error) {
	p, err := store.GetByID[store.Provider](c, a.St.Pool, "providers", id)
	if err != nil {
		return nil, nil, err
	}
	if err := a.providerPerm(c, p.OrgID); err != nil {
		return nil, nil, err
	}
	conn, err := a.Box.OpenString(p.ConnEnc)
	if err != nil {
		return nil, nil, util.Err(500, "api_error", "cannot decrypt the provider connection")
	}
	app, err := providers.OAuthAppFor(conn)
	if err != nil {
		return nil, nil, badReq(err.Error())
	}
	if app == nil {
		return nil, nil, badReq("this provider does not use OAuth authentication")
	}
	return p, app, nil
}

func (a *API) oauthStatus(ctx context.Context, p *store.Provider) *providerOAuthStatus {
	conn, err := a.Box.OpenString(p.ConnEnc)
	if err != nil {
		return nil
	}
	app, err := providers.OAuthAppFor(conn)
	if err != nil || app == nil {
		return nil
	}
	st := &providerOAuthStatus{Grant: app.Grant(), RedirectURI: a.providerOAuthRedirectURI()}
	var seed string
	var by *string
	if a.St.Pool.QueryRow(ctx, `SELECT seed, connected_by, connected_at, expires_at FROM provider_tokens WHERE provider_id=$1`, p.ID).Scan(&seed, &by, &st.ConnectedAt, &st.ExpiresAt) == nil && seed == app.Seed() {
		st.Connected, st.ConnectedBy = true, util.Deref(by)
	} else {
		st.ConnectedAt, st.ExpiresAt = nil, nil
	}
	return st
}

// providerOAuthStart sends the admin to the provider to sign in (browser navigation).
func (a *API) providerOAuthStart(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string) {
		http.Redirect(w, r, "/providers?oauth_error="+url.QueryEscape(msg), http.StatusFound)
	}
	pr, err := a.authenticate(r)
	if err != nil {
		http.Redirect(w, r, "/login?next="+url.QueryEscape("/providers"), http.StatusFound)
		return
	}
	c := &Ctx{Context: r.Context(), W: w, R: r, P: pr, a: a}
	p, app, err := a.oauthApp(c, r.PathValue("id"))
	if err != nil {
		fail(errMessage(err))
		return
	}
	if app.Grant() != "authorization_code" {
		fail("this provider uses OAuth client credentials; there is nothing to connect")
		return
	}
	verifier, challenge := auth.PKCE()
	st := providerOAuthState{Provider: p.ID, Seed: app.Seed(), User: pr.UserID(), Verifier: verifier, Nonce: util.RandomToken(24), Exp: time.Now().Add(10 * time.Minute).Unix()}
	http.SetCookie(w, &http.Cookie{Name: "tpx_prov_oauth", Value: auth.Sign(a.Box.SessionKey(), st), Path: "/auth/provider-oauth", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: 600, Secure: strings.HasPrefix(a.Cfg.PublicURL, "https")})
	http.Redirect(w, r, app.AuthCodeURL(st.Nonce, challenge, a.providerOAuthRedirectURI()), http.StatusFound)
}

// providerOAuthCallback receives the authorization code and stores the tokens.
func (a *API) providerOAuthCallback(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string) {
		http.Redirect(w, r, "/providers?oauth_error="+url.QueryEscape(msg), http.StatusFound)
	}
	ck, err := r.Cookie("tpx_prov_oauth")
	if err != nil {
		fail("the connection attempt expired; try again")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "tpx_prov_oauth", Value: "", Path: "/auth/provider-oauth", MaxAge: -1, HttpOnly: true})
	var st providerOAuthState
	if auth.Verify(a.Box.SessionKey(), ck.Value, &st) != nil || time.Now().Unix() > st.Exp || r.URL.Query().Get("state") != st.Nonce {
		fail("invalid OAuth state; try connecting again")
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		fail(strings.TrimSpace(e + ": " + r.URL.Query().Get("error_description")))
		return
	}
	pr, err := a.authenticate(r)
	if err != nil || pr.UserID() != st.User {
		fail("sign in to the console as the same user and try again")
		return
	}
	c := &Ctx{Context: r.Context(), W: w, R: r, P: pr, a: a}
	p, app, err := a.oauthApp(c, st.Provider)
	if err != nil {
		fail(errMessage(err))
		return
	}
	if app.Seed() != st.Seed {
		fail("the provider's OAuth settings changed while connecting; try again")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	tok, err := app.Exchange(ctx, r.URL.Query().Get("code"), st.Verifier, a.providerOAuthRedirectURI())
	if err != nil {
		fail("token exchange failed: " + err.Error())
		return
	}
	var ref []byte
	if tok.Refresh != "" {
		ref = a.Box.SealString(tok.Refresh)
	}
	var exp *time.Time
	if !tok.Expiry.IsZero() {
		exp = &tok.Expiry
	}
	if _, err := a.St.Pool.Exec(ctx, `INSERT INTO provider_tokens (provider_id, seed, access_enc, refresh_enc, expires_at, connected_by, connected_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6,now(),now())
		ON CONFLICT (provider_id) DO UPDATE SET seed=EXCLUDED.seed, access_enc=EXCLUDED.access_enc, refresh_enc=EXCLUDED.refresh_enc, expires_at=EXCLUDED.expires_at,
		connected_by=EXCLUDED.connected_by, connected_at=now(), updated_at=now()`, p.ID, app.Seed(), a.Box.SealString(tok.Access), ref, exp, pr.Email()); err != nil {
		fail("storing tokens: " + err.Error())
		return
	}
	_, _ = a.St.Pool.Exec(ctx, `UPDATE providers SET updated_at=now() WHERE id=$1`, p.ID) // rebuild cached clients on every replica
	c.Audit("provider.oauth_connect", util.Deref(p.OrgID), "provider", p.ID, map[string]any{"name": p.Name, "refresh_token": tok.Refresh != ""})
	a.changed(c)
	http.Redirect(w, r, "/providers?oauth=connected&provider="+url.QueryEscape(p.ID), http.StatusFound)
}

// providerOAuthDisconnect forgets the stored tokens.
func (a *API) providerOAuthDisconnect(c *Ctx) (any, error) {
	p, _, err := a.oauthApp(c, c.Param("id"))
	if err != nil {
		return nil, err
	}
	if _, err := a.St.Pool.Exec(c, `DELETE FROM provider_tokens WHERE provider_id=$1`, p.ID); err != nil {
		return nil, err
	}
	_, _ = a.St.Pool.Exec(c, `UPDATE providers SET updated_at=now() WHERE id=$1`, p.ID) // drop cached tokens on every replica
	c.Audit("provider.oauth_disconnect", util.Deref(p.OrgID), "provider", p.ID, map[string]string{"name": p.Name})
	a.changed(c)
	return nil, nil
}

func errMessage(err error) string {
	if he, ok := err.(*util.HTTPError); ok {
		return he.Msg
	}
	return "unexpected error"
}
