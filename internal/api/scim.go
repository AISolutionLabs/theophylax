package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"theophylax/internal/audit"
	"theophylax/internal/auth"
	"theophylax/internal/store"
	"theophylax/internal/util"
)

// SCIM 2.0 provisioning (Entra ID, Okta, OneLogin, JumpCloud...). Users map to
// Theophylax users with an org membership; Groups map to Teams.

const (
	scimUser  = "urn:ietf:params:scim:schemas:core:2.0:User"
	scimGroup = "urn:ietf:params:scim:schemas:core:2.0:Group"
	scimList  = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	scimErr   = "urn:ietf:params:scim:api:messages:2.0:Error"
	scimPatch = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
)

type scimCtx struct {
	*Ctx
	idp *store.IdP
}

func (a *API) scimRoutes(mux *http.ServeMux) {
	h := func(fn func(*scimCtx) (any, int, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			c := &Ctx{Context: r.Context(), W: w, R: r, a: a}
			tok := auth.BearerToken(r)
			idp, err := store.Get[store.IdP](r.Context(), a.St.Pool, `SELECT * FROM idp_providers WHERE scim_token_hash=$1 AND enabled`, auth.HashKey(tok))
			if tok == "" || err != nil {
				scimError(w, 401, "invalid SCIM token")
				return
			}
			c.P = &auth.Principal{Kind: "scim"}
			v, status, err := fn(&scimCtx{Ctx: c, idp: idp})
			if err != nil {
				code := 500
				if he, ok := err.(*util.HTTPError); ok {
					code = he.Status
				}
				scimError(w, code, err.Error())
				return
			}
			if v == nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.Header().Set("Content-Type", "application/scim+json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(v)
		}
	}
	mux.HandleFunc("GET /scim/v2/ServiceProviderConfig", h(func(c *scimCtx) (any, int, error) {
		return map[string]any{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"},
			"patch": map[string]bool{"supported": true}, "bulk": map[string]any{"supported": false}, "filter": map[string]any{"supported": true, "maxResults": 200},
			"changePassword": map[string]bool{"supported": false}, "sort": map[string]bool{"supported": false}, "etag": map[string]bool{"supported": false},
			"authenticationSchemes": []map[string]string{{"type": "oauthbearertoken", "name": "Bearer token", "description": "Theophylax SCIM token"}}}, 200, nil
	}))
	mux.HandleFunc("GET /scim/v2/Users", h(a.scimListUsers))
	mux.HandleFunc("POST /scim/v2/Users", h(a.scimCreateUser))
	mux.HandleFunc("GET /scim/v2/Users/{id}", h(a.scimGetUser))
	mux.HandleFunc("PUT /scim/v2/Users/{id}", h(a.scimPutUser))
	mux.HandleFunc("PATCH /scim/v2/Users/{id}", h(a.scimPatchUser))
	mux.HandleFunc("DELETE /scim/v2/Users/{id}", h(a.scimDeleteUser))
	mux.HandleFunc("GET /scim/v2/Groups", h(a.scimListGroups))
	mux.HandleFunc("POST /scim/v2/Groups", h(a.scimCreateGroup))
	mux.HandleFunc("GET /scim/v2/Groups/{id}", h(a.scimGetGroup))
	mux.HandleFunc("PATCH /scim/v2/Groups/{id}", h(a.scimPatchGroup))
	mux.HandleFunc("PUT /scim/v2/Groups/{id}", h(a.scimPutGroup))
	mux.HandleFunc("DELETE /scim/v2/Groups/{id}", h(a.scimDeleteGroup))
}

func scimError(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"schemas": []string{scimErr}, "status": strconv.Itoa(status), "detail": detail})
}

func (c *scimCtx) audit(action, targetType, targetID string, d any) {
	c.a.Audit.Record(c, audit.Event{Actor: audit.Actor{Type: "scim", ID: c.idp.ID, Email: c.idp.Name, IP: util.ClientIP(c.R, c.a.Cfg.TrustedProxies)}, OrgID: c.idp.OrgID,
		Action: action, TargetType: targetType, TargetID: targetID, Details: d})
}

func (a *API) scimUserResource(c *scimCtx, u *store.User) map[string]any {
	var role string
	_ = a.St.Pool.QueryRow(c, `SELECT role FROM memberships WHERE user_id=$1 AND scope_type='org' AND scope_id=$2`, u.ID, c.idp.OrgID).Scan(&role)
	given, family, _ := strings.Cut(u.Name, " ")
	return map[string]any{"schemas": []string{scimUser}, "id": u.ID, "externalId": util.Deref(u.ExternalID), "userName": u.Email,
		"name": map[string]string{"givenName": given, "familyName": family, "formatted": u.Name}, "displayName": u.Name,
		"emails": []map[string]any{{"value": u.Email, "primary": true, "type": "work"}}, "active": u.Status == "active" && role != "",
		"meta": map[string]any{"resourceType": "User", "created": u.CreatedAt, "location": a.Cfg.PublicURL + "/scim/v2/Users/" + u.ID}}
}

var scimFilterRe = regexp.MustCompile(`^\s*(\w+(?:\.\w+)?)\s+eq\s+"([^"]*)"\s*$`)

func paging(c *scimCtx) (int, int) {
	start, _ := strconv.Atoi(c.Query("startIndex"))
	count, _ := strconv.Atoi(c.Query("count"))
	if start < 1 {
		start = 1
	}
	if count <= 0 || count > 200 {
		count = 100
	}
	return start, count
}

func listResp(res []map[string]any, total, start int) map[string]any {
	return map[string]any{"schemas": []string{scimList}, "totalResults": total, "startIndex": start, "itemsPerPage": len(res), "Resources": res}
}

func (a *API) scimListUsers(c *scimCtx) (any, int, error) {
	start, count := paging(c)
	where := `(id IN (SELECT user_id FROM memberships WHERE org_id=$1) OR idp_id=$2)`
	args := []any{c.idp.OrgID, c.idp.ID}
	if f := c.Query("filter"); f != "" {
		m := scimFilterRe.FindStringSubmatch(f)
		if m == nil {
			return nil, 0, util.Err(400, "invalidFilter", "unsupported filter")
		}
		switch strings.ToLower(m[1]) {
		case "username", "emails.value":
			where += ` AND lower(email)=lower($3)`
		case "externalid":
			where += ` AND external_id=$3`
		default:
			return nil, 0, util.Err(400, "invalidFilter", "unsupported filter attribute")
		}
		args = append(args, m[2])
	}
	var total int
	_ = a.St.Pool.QueryRow(c, `SELECT count(*) FROM users WHERE `+where, args...).Scan(&total)
	users, err := store.List[store.User](c, a.St.Pool, fmt.Sprintf(`SELECT * FROM users WHERE %s ORDER BY created_at OFFSET %d LIMIT %d`, where, start-1, count), args...)
	if err != nil {
		return nil, 0, err
	}
	res := make([]map[string]any, 0, len(users))
	for i := range users {
		res = append(res, a.scimUserResource(c, &users[i]))
	}
	return listResp(res, total, start), 200, nil
}

type scimUserIn struct {
	UserName    string `json:"userName"`
	ExternalID  string `json:"externalId"`
	DisplayName string `json:"displayName"`
	Active      *bool  `json:"active"`
	Name        struct {
		GivenName  string `json:"givenName"`
		FamilyName string `json:"familyName"`
		Formatted  string `json:"formatted"`
	} `json:"name"`
	Emails []struct {
		Value   string `json:"value"`
		Primary bool   `json:"primary"`
	} `json:"emails"`
}

func (in *scimUserIn) email() string {
	for _, e := range in.Emails {
		if e.Primary && e.Value != "" {
			return strings.ToLower(e.Value)
		}
	}
	if strings.Contains(in.UserName, "@") {
		return strings.ToLower(in.UserName)
	}
	if len(in.Emails) > 0 {
		return strings.ToLower(in.Emails[0].Value)
	}
	return ""
}

func (in *scimUserIn) name() string {
	if in.DisplayName != "" {
		return in.DisplayName
	}
	if in.Name.Formatted != "" {
		return in.Name.Formatted
	}
	return strings.TrimSpace(in.Name.GivenName + " " + in.Name.FamilyName)
}

func (a *API) setOrgActive(c *scimCtx, u *store.User, active bool) {
	if active {
		role := c.idp.DefaultRole
		if role == "" || role == "none" {
			role = "member"
		}
		_, _ = a.St.Pool.Exec(c, `INSERT INTO memberships (id,user_id,org_id,scope_type,scope_id,role,source) VALUES ($1,$2,$3,'org',$3,$4,'scim') ON CONFLICT DO NOTHING`, util.NewID("mem"), u.ID, c.idp.OrgID, role)
	} else {
		_, _ = a.St.Pool.Exec(c, `DELETE FROM memberships WHERE user_id=$1 AND org_id=$2`, u.ID, c.idp.OrgID)
		_, _ = a.St.Pool.Exec(c, `UPDATE api_keys SET revoked_at=now() WHERE user_id=$1 AND org_id=$2 AND revoked_at IS NULL`, u.ID, c.idp.OrgID)
	}
	a.Res.Invalidate()
}

func (a *API) scimCreateUser(c *scimCtx) (any, int, error) {
	var in scimUserIn
	if err := c.Decode(&in); err != nil {
		return nil, 0, err
	}
	email := in.email()
	if email == "" {
		return nil, 0, util.Err(400, "invalidValue", "userName or emails must contain an email address")
	}
	u, err := a.St.UserByEmail(c, email)
	if err == util.ErrNotFound {
		u = &store.User{ID: util.NewID("usr"), Email: email, Name: in.name(), Status: "active", IdpID: &c.idp.ID}
		if in.ExternalID != "" {
			u.ExternalID = &in.ExternalID
		}
		if err := a.St.Insert(c, a.St.Pool, "users", u); err != nil {
			return nil, 0, err
		}
	} else if err != nil {
		return nil, 0, err
	} else {
		if in.ExternalID != "" {
			u.ExternalID = &in.ExternalID
		}
		if u.IdpID == nil {
			u.IdpID = &c.idp.ID
		}
		_ = a.St.Update(c, a.St.Pool, "users", u)
	}
	a.setOrgActive(c, u, in.Active == nil || *in.Active)
	c.audit("scim.user_create", "user", u.ID, map[string]string{"email": email})
	return a.scimUserResource(c, u), 201, nil
}

func (a *API) scimUserCtx(c *scimCtx) (*store.User, error) {
	u, err := store.Get[store.User](c, a.St.Pool, `SELECT * FROM users WHERE id=$1 AND (idp_id=$2 OR id IN (SELECT user_id FROM memberships WHERE org_id=$3))`, c.Param("id"), c.idp.ID, c.idp.OrgID)
	if err != nil {
		return nil, util.Err(404, "notFound", "user not found")
	}
	return u, nil
}

func (a *API) scimGetUser(c *scimCtx) (any, int, error) {
	u, err := a.scimUserCtx(c)
	if err != nil {
		return nil, 0, err
	}
	return a.scimUserResource(c, u), 200, nil
}

func (a *API) scimPutUser(c *scimCtx) (any, int, error) {
	u, err := a.scimUserCtx(c)
	if err != nil {
		return nil, 0, err
	}
	var in scimUserIn
	if err := c.Decode(&in); err != nil {
		return nil, 0, err
	}
	if n := in.name(); n != "" {
		u.Name = n
	}
	if e := in.email(); e != "" {
		u.Email = e
	}
	if in.ExternalID != "" {
		u.ExternalID = &in.ExternalID
	}
	if err := a.St.Update(c, a.St.Pool, "users", u); err != nil {
		return nil, 0, err
	}
	if in.Active != nil {
		a.setOrgActive(c, u, *in.Active)
	}
	c.audit("scim.user_update", "user", u.ID, nil)
	return a.scimUserResource(c, u), 200, nil
}

func (a *API) scimPatchUser(c *scimCtx) (any, int, error) {
	u, err := a.scimUserCtx(c)
	if err != nil {
		return nil, 0, err
	}
	var in struct {
		Operations []struct {
			Op    string          `json:"op"`
			Path  string          `json:"path"`
			Value json.RawMessage `json:"value"`
		} `json:"Operations"`
	}
	if err := c.Decode(&in); err != nil {
		return nil, 0, err
	}
	for _, op := range in.Operations {
		if !strings.EqualFold(op.Op, "replace") && !strings.EqualFold(op.Op, "add") {
			continue
		}
		vals := map[string]json.RawMessage{}
		if op.Path != "" {
			vals[op.Path] = op.Value
		} else {
			_ = json.Unmarshal(op.Value, &vals)
		}
		for k, v := range vals {
			switch strings.ToLower(k) {
			case "active":
				var b bool
				if json.Unmarshal(v, &b) != nil {
					var s string
					_ = json.Unmarshal(v, &s)
					b = strings.EqualFold(s, "true")
				}
				a.setOrgActive(c, u, b)
				c.audit("scim.user_active", "user", u.ID, map[string]bool{"active": b})
			case "displayname", "name.formatted":
				_ = json.Unmarshal(v, &u.Name)
			case "username", "emails[type eq \"work\"].value":
				var s string
				if json.Unmarshal(v, &s) == nil && strings.Contains(s, "@") {
					u.Email = strings.ToLower(s)
				}
			case "externalid":
				var s string
				_ = json.Unmarshal(v, &s)
				u.ExternalID = &s
			}
		}
	}
	if err := a.St.Update(c, a.St.Pool, "users", u); err != nil {
		return nil, 0, err
	}
	return a.scimUserResource(c, u), 200, nil
}

func (a *API) scimDeleteUser(c *scimCtx) (any, int, error) {
	u, err := a.scimUserCtx(c)
	if err != nil {
		return nil, 0, err
	}
	a.setOrgActive(c, u, false)
	c.audit("scim.user_deprovision", "user", u.ID, map[string]string{"email": u.Email})
	return nil, 204, nil
}

// --- Groups ↔ Teams

func (a *API) scimGroupResource(c *scimCtx, t *store.Team) map[string]any {
	members := []map[string]string{}
	rows, err := a.St.Pool.Query(c, `SELECT u.id, u.email FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.scope_type='team' AND m.scope_id=$1`, t.ID)
	if err == nil {
		for rows.Next() {
			var id, email string
			_ = rows.Scan(&id, &email)
			members = append(members, map[string]string{"value": id, "display": email})
		}
		rows.Close()
	}
	return map[string]any{"schemas": []string{scimGroup}, "id": t.ID, "externalId": util.Deref(t.ExternalID), "displayName": t.Name, "members": members,
		"meta": map[string]any{"resourceType": "Group", "created": t.CreatedAt, "location": a.Cfg.PublicURL + "/scim/v2/Groups/" + t.ID}}
}

func (a *API) scimListGroups(c *scimCtx) (any, int, error) {
	start, count := paging(c)
	where, args := `org_id=$1`, []any{c.idp.OrgID}
	if f := c.Query("filter"); f != "" {
		m := scimFilterRe.FindStringSubmatch(f)
		if m == nil {
			return nil, 0, util.Err(400, "invalidFilter", "unsupported filter")
		}
		switch strings.ToLower(m[1]) {
		case "displayname":
			where += ` AND name=$2`
		case "externalid":
			where += ` AND external_id=$2`
		default:
			return nil, 0, util.Err(400, "invalidFilter", "unsupported filter attribute")
		}
		args = append(args, m[2])
	}
	teams, err := store.List[store.Team](c, a.St.Pool, fmt.Sprintf(`SELECT * FROM teams WHERE %s ORDER BY created_at OFFSET %d LIMIT %d`, where, start-1, count), args...)
	if err != nil {
		return nil, 0, err
	}
	var total int
	_ = a.St.Pool.QueryRow(c, `SELECT count(*) FROM teams WHERE `+where, args...).Scan(&total)
	res := make([]map[string]any, 0, len(teams))
	for i := range teams {
		res = append(res, a.scimGroupResource(c, &teams[i]))
	}
	return listResp(res, total, start), 200, nil
}

type scimGroupIn struct {
	DisplayName string `json:"displayName"`
	ExternalID  string `json:"externalId"`
	Members     []struct {
		Value string `json:"value"`
	} `json:"members"`
}

func (a *API) setTeamMember(c *scimCtx, teamID, userID string, add bool) {
	if add {
		_, _ = a.St.Pool.Exec(c, `INSERT INTO memberships (id,user_id,org_id,scope_type,scope_id,role,source) SELECT $1,$2,$3,'team',$4,'member','scim' WHERE EXISTS (SELECT 1 FROM users WHERE id=$2) ON CONFLICT DO NOTHING`,
			util.NewID("mem"), userID, c.idp.OrgID, teamID)
	} else {
		_, _ = a.St.Pool.Exec(c, `DELETE FROM memberships WHERE user_id=$1 AND scope_type='team' AND scope_id=$2 AND source='scim'`, userID, teamID)
	}
	a.Res.Invalidate()
}

func (a *API) scimCreateGroup(c *scimCtx) (any, int, error) {
	var in scimGroupIn
	if err := c.Decode(&in); err != nil {
		return nil, 0, err
	}
	if in.DisplayName == "" {
		return nil, 0, util.Err(400, "invalidValue", "displayName required")
	}
	t, err := store.Get[store.Team](c, a.St.Pool, `SELECT * FROM teams WHERE org_id=$1 AND (name=$2 OR ($3<>'' AND external_id=$3))`, c.idp.OrgID, in.DisplayName, in.ExternalID)
	if err == util.ErrNotFound {
		slug := util.Slugify(in.DisplayName)
		if slug == "" {
			slug = "team-" + strings.ToLower(util.RandomToken(6))
		}
		t = &store.Team{ID: util.NewID("team"), OrgID: c.idp.OrgID, Slug: slug, Name: in.DisplayName}
		if in.ExternalID != "" {
			t.ExternalID = &in.ExternalID
		}
		if err := a.St.Insert(c, a.St.Pool, "teams", t); err != nil {
			t.Slug = slug + "-" + strings.ToLower(util.RandomToken(4))
			if err := a.St.Insert(c, a.St.Pool, "teams", t); err != nil {
				return nil, 0, err
			}
		}
	} else if err != nil {
		return nil, 0, err
	}
	for _, m := range in.Members {
		a.setTeamMember(c, t.ID, m.Value, true)
	}
	c.audit("scim.group_create", "team", t.ID, map[string]string{"name": t.Name})
	a.changed(c)
	return a.scimGroupResource(c, t), 201, nil
}

func (a *API) scimTeam(c *scimCtx) (*store.Team, error) {
	t, err := store.Get[store.Team](c, a.St.Pool, `SELECT * FROM teams WHERE id=$1 AND org_id=$2`, c.Param("id"), c.idp.OrgID)
	if err != nil {
		return nil, util.Err(404, "notFound", "group not found")
	}
	return t, nil
}

func (a *API) scimGetGroup(c *scimCtx) (any, int, error) {
	t, err := a.scimTeam(c)
	if err != nil {
		return nil, 0, err
	}
	return a.scimGroupResource(c, t), 200, nil
}

func (a *API) scimPutGroup(c *scimCtx) (any, int, error) {
	t, err := a.scimTeam(c)
	if err != nil {
		return nil, 0, err
	}
	var in scimGroupIn
	if err := c.Decode(&in); err != nil {
		return nil, 0, err
	}
	if in.DisplayName != "" {
		t.Name = in.DisplayName
		_ = a.St.Update(c, a.St.Pool, "teams", t)
	}
	want := map[string]bool{}
	for _, m := range in.Members {
		want[m.Value] = true
		a.setTeamMember(c, t.ID, m.Value, true)
	}
	rows, _ := a.St.Pool.Query(c, `SELECT user_id FROM memberships WHERE scope_type='team' AND scope_id=$1 AND source='scim'`, t.ID)
	var drop []string
	for rows != nil && rows.Next() {
		var id string
		_ = rows.Scan(&id)
		if !want[id] {
			drop = append(drop, id)
		}
	}
	if rows != nil {
		rows.Close()
	}
	for _, id := range drop {
		a.setTeamMember(c, t.ID, id, false)
	}
	c.audit("scim.group_replace", "team", t.ID, nil)
	return a.scimGroupResource(c, t), 200, nil
}

func (a *API) scimPatchGroup(c *scimCtx) (any, int, error) {
	t, err := a.scimTeam(c)
	if err != nil {
		return nil, 0, err
	}
	var in struct {
		Operations []struct {
			Op    string          `json:"op"`
			Path  string          `json:"path"`
			Value json.RawMessage `json:"value"`
		} `json:"Operations"`
	}
	if err := c.Decode(&in); err != nil {
		return nil, 0, err
	}
	memberPath := regexp.MustCompile(`^members\[value eq "([^"]+)"\]$`)
	for _, op := range in.Operations {
		var members []struct {
			Value string `json:"value"`
		}
		switch {
		case strings.EqualFold(op.Path, "members"):
			_ = json.Unmarshal(op.Value, &members)
			for _, m := range members {
				a.setTeamMember(c, t.ID, m.Value, !strings.EqualFold(op.Op, "remove"))
			}
		case memberPath.MatchString(op.Path):
			a.setTeamMember(c, t.ID, memberPath.FindStringSubmatch(op.Path)[1], !strings.EqualFold(op.Op, "remove"))
		case strings.EqualFold(op.Path, "displayName"):
			_ = json.Unmarshal(op.Value, &t.Name)
			_ = a.St.Update(c, a.St.Pool, "teams", t)
		case op.Path == "":
			var v struct {
				DisplayName string `json:"displayName"`
			}
			if json.Unmarshal(op.Value, &v) == nil && v.DisplayName != "" {
				t.Name = v.DisplayName
				_ = a.St.Update(c, a.St.Pool, "teams", t)
			}
		}
	}
	c.audit("scim.group_patch", "team", t.ID, nil)
	return a.scimGroupResource(c, t), 200, nil
}

func (a *API) scimDeleteGroup(c *scimCtx) (any, int, error) {
	t, err := a.scimTeam(c)
	if err != nil {
		return nil, 0, err
	}
	// Unlink rather than destroy: projects, keys and logs stay intact.
	_, _ = a.St.Pool.Exec(c, `DELETE FROM memberships WHERE scope_type='team' AND scope_id=$1 AND source='scim'`, t.ID)
	_, _ = a.St.Pool.Exec(c, `UPDATE teams SET external_id=NULL WHERE id=$1`, t.ID)
	a.Res.Invalidate()
	c.audit("scim.group_delete", "team", t.ID, map[string]string{"name": t.Name})
	return nil, 204, nil
}
