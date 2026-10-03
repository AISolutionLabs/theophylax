package api

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"theophylax/internal/audit"
	"theophylax/internal/auth"
	"theophylax/internal/store"
	"theophylax/internal/util"
)

const sessionTTL = 12 * time.Hour

func (a *API) setSession(w http.ResponseWriter, userID, idp string) {
	tok := auth.Sign(a.Box.SessionKey(), auth.Session{UserID: userID, Exp: time.Now().Add(sessionTTL).Unix(), IdP: idp})
	http.SetCookie(w, &http.Cookie{Name: auth.SessionCookie, Value: tok, Path: "/", HttpOnly: true, Secure: !a.Cfg.DevMode && strings.HasPrefix(a.Cfg.PublicURL, "https"),
		SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL.Seconds())})
}

func (a *API) authConfig(c *Ctx) (any, error) {
	type idpInfo struct {
		ID, Name, Org string
	}
	rows, err := a.St.Pool.Query(c, `SELECT i.id, i.name, o.name FROM idp_providers i JOIN orgs o ON o.id=i.org_id WHERE i.enabled ORDER BY o.name, i.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]string{}
	for rows.Next() {
		var i idpInfo
		if err := rows.Scan(&i.ID, &i.Name, &i.Org); err != nil {
			return nil, err
		}
		out = append(out, map[string]string{"id": i.ID, "name": i.Name, "org": i.Org})
	}
	return map[string]any{"local_login": a.Cfg.LocalLogin, "providers": out, "product": "Theophylax"}, nil
}

func (a *API) login(c *Ctx) (any, error) {
	if !a.Cfg.LocalLogin {
		return nil, util.Err(403, "permission_error", "password login is disabled; use single sign-on")
	}
	var in struct{ Email, Password string }
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	u, err := a.St.UserByEmail(c, in.Email)
	act := audit.Actor{Type: "user", Email: strings.ToLower(in.Email), IP: util.ClientIP(c.R, a.Cfg.TrustedProxies), UA: c.R.UserAgent()}
	if err != nil || u.PasswordHash == nil || !auth.CheckPassword(*u.PasswordHash, in.Password) || u.Status != "active" {
		a.Audit.Record(c, audit.Event{Actor: act, Action: "auth.login_failed"})
		time.Sleep(300 * time.Millisecond)
		return nil, util.Err(401, "authentication_error", "invalid email or password")
	}
	act.ID = u.ID
	_, _ = a.St.Pool.Exec(c, `UPDATE users SET last_login_at=now() WHERE id=$1`, u.ID)
	a.setSession(c.W, u.ID, "")
	a.Audit.Record(c, audit.Event{Actor: act, Action: "auth.login", Details: map[string]string{"method": "password"}})
	return map[string]any{"ok": true}, nil
}

func (a *API) logout(c *Ctx) (any, error) {
	http.SetCookie(c.W, &http.Cookie{Name: auth.SessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	return nil, nil
}

type oidcState struct {
	IdP      string `json:"i"`
	Verifier string `json:"v"`
	Nonce    string `json:"n"`
	Redirect string `json:"r"`
	Exp      int64  `json:"e"`
}

func (a *API) redirectURI() string { return a.Cfg.PublicURL + "/auth/oidc/callback" }

func (a *API) oidcStart(w http.ResponseWriter, r *http.Request) {
	idp, err := store.GetByID[store.IdP](r.Context(), a.St.Pool, "idp_providers", r.PathValue("idp"))
	if err != nil || !idp.Enabled {
		http.Error(w, "identity provider not found", 404)
		return
	}
	d, err := a.Res.OIDC.Discover(r.Context(), idp.Issuer)
	if err != nil {
		http.Error(w, "identity provider discovery failed: "+err.Error(), 502)
		return
	}
	verifier, challenge := auth.PKCE()
	redirect := r.URL.Query().Get("redirect")
	if !strings.HasPrefix(redirect, "/") || strings.HasPrefix(redirect, "//") {
		redirect = "/"
	}
	st := oidcState{IdP: idp.ID, Verifier: verifier, Nonce: util.RandomToken(24), Redirect: redirect, Exp: time.Now().Add(10 * time.Minute).Unix()}
	http.SetCookie(w, &http.Cookie{Name: "tpx_oidc", Value: auth.Sign(a.Box.SessionKey(), st), Path: "/auth/oidc", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 600,
		Secure: strings.HasPrefix(a.Cfg.PublicURL, "https")})
	q := url.Values{"response_type": {"code"}, "client_id": {idp.ClientID}, "redirect_uri": {a.redirectURI()}, "scope": {strings.Join(idp.Scopes, " ")},
		"state": {st.Nonce}, "nonce": {st.Nonce}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	http.Redirect(w, r, d.AuthURL+"?"+q.Encode(), http.StatusFound)
}

func (a *API) oidcCallback(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string) {
		http.Redirect(w, r, "/login?error="+url.QueryEscape(msg), http.StatusFound)
	}
	ck, err := r.Cookie("tpx_oidc")
	if err != nil {
		fail("login session expired, please try again")
		return
	}
	var st oidcState
	if auth.Verify(a.Box.SessionKey(), ck.Value, &st) != nil || time.Now().Unix() > st.Exp || r.URL.Query().Get("state") != st.Nonce {
		fail("invalid login state")
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		fail(e + ": " + r.URL.Query().Get("error_description"))
		return
	}
	idp, err := store.GetByID[store.IdP](r.Context(), a.St.Pool, "idp_providers", st.IdP)
	if err != nil {
		fail("identity provider not found")
		return
	}
	secret, _ := a.Box.OpenString(idp.SecretEnc)
	idTok, err := a.Res.OIDC.Exchange(r.Context(), idp.Issuer, idp.ClientID, secret, r.URL.Query().Get("code"), a.redirectURI(), st.Verifier)
	if err != nil {
		fail(err.Error())
		return
	}
	claims, err := a.Res.OIDC.VerifyJWT(r.Context(), idTok, idp.Issuer, []string{idp.ClientID})
	if err != nil {
		fail("id token rejected: " + err.Error())
		return
	}
	if claims.Str("nonce") != st.Nonce {
		fail("nonce mismatch")
		return
	}
	u, err := a.Res.Provision(r.Context(), idp, claims)
	act := audit.Actor{Type: "user", Email: claims.Email(), IP: util.ClientIP(r, a.Cfg.TrustedProxies), UA: r.UserAgent()}
	if err != nil {
		a.Audit.Record(r.Context(), audit.Event{Actor: act, OrgID: idp.OrgID, Action: "auth.login_failed", Details: map[string]string{"method": "oidc", "idp": idp.Name, "reason": err.Error()}})
		fail(err.Error())
		return
	}
	act.ID = u.ID
	a.Audit.Record(r.Context(), audit.Event{Actor: act, OrgID: idp.OrgID, Action: "auth.login", Details: map[string]string{"method": "oidc", "idp": idp.Name}})
	http.SetCookie(w, &http.Cookie{Name: "tpx_oidc", Value: "", Path: "/auth/oidc", MaxAge: -1})
	a.setSession(w, u.ID, idp.ID)
	http.Redirect(w, r, st.Redirect, http.StatusFound)
}

func (a *API) me(c *Ctx) (any, error) {
	type memView struct {
		store.Membership
		ScopeName string `json:"scope_name" db:"scope_name"`
		OrgName   string `json:"org_name" db:"org_name"`
		OrgSlug   string `json:"org_slug" db:"org_slug"`
	}
	mems, err := store.List[memView](c, a.St.Pool, `SELECT m.*, o.name AS org_name, o.slug AS org_slug,
		COALESCE(CASE m.scope_type WHEN 'org' THEN o.name WHEN 'team' THEN (SELECT name FROM teams WHERE id=m.scope_id) ELSE (SELECT name FROM projects WHERE id=m.scope_id) END, '') AS scope_name
		FROM memberships m JOIN orgs o ON o.id=m.org_id WHERE m.user_id=$1 ORDER BY o.name`, c.P.User.ID)
	if err != nil {
		return nil, err
	}
	var orgs []store.Org
	if c.P.IsPlatformAdmin() {
		orgs, err = store.List[store.Org](c, a.St.Pool, `SELECT * FROM orgs ORDER BY name`)
	} else {
		orgs, err = store.List[store.Org](c, a.St.Pool, `SELECT * FROM orgs WHERE id IN (SELECT org_id FROM memberships WHERE user_id=$1) ORDER BY name`, c.P.User.ID)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"user": c.P.User, "memberships": mems, "orgs": orgs, "is_platform_admin": c.P.IsPlatformAdmin(), "has_password": c.P.User.PasswordHash != nil, "via": c.P.Via}, nil
}

func (a *API) changePassword(c *Ctx) (any, error) {
	var in struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if len(in.New) < 12 {
		return nil, badReq("password must be at least 12 characters")
	}
	u := c.P.User
	if u.PasswordHash != nil && !auth.CheckPassword(*u.PasswordHash, in.Current) {
		return nil, util.Err(403, "permission_error", "current password is incorrect")
	}
	h, err := auth.HashPassword(in.New)
	if err != nil {
		return nil, err
	}
	if _, err := a.St.Pool.Exec(c, `UPDATE users SET password_hash=$2 WHERE id=$1`, u.ID, h); err != nil {
		return nil, err
	}
	c.Audit("user.password_change", "", "user", u.ID, nil)
	return nil, nil
}
