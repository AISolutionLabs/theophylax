package api

import (
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"

	"theophylax/internal/auth"
	"theophylax/internal/store"
	"theophylax/internal/util"
)

type hierarchyIn struct {
	Name        *string          `json:"name"`
	Slug        *string          `json:"slug"`
	Description *string          `json:"description"`
	Settings    *json.RawMessage `json:"settings"`
}

func validSettings(raw *json.RawMessage) error {
	if raw == nil {
		return nil
	}
	var s store.ScopeSettings
	dec := json.NewDecoder(strings.NewReader(string(*raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return badReq("invalid settings: " + err.Error())
	}
	if s.CacheTTL < -1 || s.CacheTTL > 7*24*3600 {
		return badReq("cache_ttl_seconds must be between 1 and 604800, 0 to inherit, or -1 to turn caching off")
	}
	return nil
}

func (in *hierarchyIn) slug() (string, error) {
	s := ""
	if in.Slug != nil && *in.Slug != "" {
		s = *in.Slug
	} else if in.Name != nil {
		s = util.Slugify(*in.Name)
	}
	if !util.ValidSlug(s) {
		return "", badReq("a valid name/slug (lowercase letters, digits, dashes) is required")
	}
	return s, nil
}

// --- Orgs

func (a *API) listOrgs(c *Ctx) (any, error) {
	if c.P.IsPlatformAdmin() {
		return store.List[store.Org](c, a.St.Pool, `SELECT * FROM orgs ORDER BY name`)
	}
	return store.List[store.Org](c, a.St.Pool, `SELECT * FROM orgs WHERE id IN (SELECT org_id FROM memberships WHERE user_id=$1) ORDER BY name`, c.P.User.ID)
}

func (a *API) createOrg(c *Ctx) (any, error) {
	if err := c.RequirePlatformAdmin(); err != nil {
		return nil, err
	}
	var in hierarchyIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	slug, err := in.slug()
	if err != nil {
		return nil, err
	}
	if err := validSettings(in.Settings); err != nil {
		return nil, err
	}
	o := &store.Org{ID: util.NewID("org"), Slug: slug, Name: util.Deref(in.Name)}
	if in.Settings != nil {
		o.Settings = *in.Settings
	}
	err = a.St.Tx(c, func(tx pgx.Tx) error {
		if err := a.St.Insert(c, tx, "orgs", o); err != nil {
			return err
		}
		return a.St.Insert(c, tx, "memberships", &store.Membership{ID: util.NewID("mem"), UserID: c.P.User.ID, OrgID: o.ID, ScopeType: "org", ScopeID: o.ID, Role: "owner", Source: "manual"})
	})
	if err != nil {
		return nil, err
	}
	c.Audit("org.create", o.ID, "org", o.ID, o)
	a.changed(c)
	return created{o}, nil
}

func (a *API) getOrg(c *Ctx) (any, error) {
	o, s, err := a.orgCtx(c, "")
	if err != nil {
		return nil, err
	}
	return map[string]any{"org": o, "role": c.P.RoleAt(s)}, nil
}

func (a *API) updateOrg(c *Ctx) (any, error) {
	o, _, err := a.orgCtx(c, "admin")
	if err != nil {
		return nil, err
	}
	var in hierarchyIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if err := validSettings(in.Settings); err != nil {
		return nil, err
	}
	before := *o
	if in.Name != nil {
		o.Name = *in.Name
	}
	if in.Settings != nil {
		o.Settings = *in.Settings
	}
	if err := a.St.Update(c, a.St.Pool, "orgs", o); err != nil {
		return nil, err
	}
	c.Audit("org.update", o.ID, "org", o.ID, map[string]any{"before": before, "after": o})
	a.changed(c)
	return o, nil
}

func (a *API) deleteOrg(c *Ctx) (any, error) {
	o, _, err := a.orgCtx(c, "owner")
	if err != nil {
		return nil, err
	}
	if err := c.RequirePlatformAdmin(); err != nil {
		return nil, err
	}
	if err := a.St.Delete(c, a.St.Pool, "orgs", o.ID); err != nil {
		return nil, err
	}
	c.Audit("org.delete", o.ID, "org", o.ID, o)
	a.changed(c)
	return nil, nil
}

// --- Teams

type teamView struct {
	store.Team
	Projects int64 `json:"project_count" db:"project_count"`
	Members  int64 `json:"member_count" db:"member_count"`
}

func (a *API) listTeams(c *Ctx) (any, error) {
	o, s, err := a.orgCtx(c, "")
	if err != nil {
		return nil, err
	}
	q := `SELECT t.*, (SELECT count(*) FROM projects p WHERE p.team_id=t.id) AS project_count,
		(SELECT count(*) FROM memberships m WHERE m.scope_type='team' AND m.scope_id=t.id) AS member_count
		FROM teams t WHERE t.org_id=$1`
	if c.P.Can(s, "viewer") {
		return store.List[teamView](c, a.St.Pool, q+` ORDER BY t.name`, o.ID)
	}
	return store.List[teamView](c, a.St.Pool, q+` AND (t.id IN (SELECT scope_id FROM memberships WHERE user_id=$2 AND scope_type='team')
		OR t.id IN (SELECT p.team_id FROM projects p JOIN memberships m ON m.scope_id=p.id AND m.scope_type='project' WHERE m.user_id=$2)) ORDER BY t.name`, o.ID, c.P.UserID())
}

func (a *API) createTeam(c *Ctx) (any, error) {
	o, _, err := a.orgCtx(c, "admin")
	if err != nil {
		return nil, err
	}
	var in hierarchyIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	slug, err := in.slug()
	if err != nil {
		return nil, err
	}
	if err := validSettings(in.Settings); err != nil {
		return nil, err
	}
	t := &store.Team{ID: util.NewID("team"), OrgID: o.ID, Slug: slug, Name: util.Deref(in.Name), Description: util.Deref(in.Description)}
	if in.Settings != nil {
		t.Settings = *in.Settings
	}
	if err := a.St.Insert(c, a.St.Pool, "teams", t); err != nil {
		return nil, err
	}
	c.Audit("team.create", o.ID, "team", t.ID, t)
	a.changed(c)
	return created{t}, nil
}

func (a *API) teamCtx(c *Ctx, role string) (*store.Team, auth.Scope, error) {
	t, err := store.GetByID[store.Team](c, a.St.Pool, "teams", c.Param("id"))
	if err != nil {
		return nil, auth.Scope{}, err
	}
	s := auth.Scope{Type: "team", ID: t.ID, OrgID: t.OrgID, TeamID: t.ID}
	if !c.P.Can(s, role) {
		return nil, s, util.ErrForbidden
	}
	return t, s, nil
}

func (a *API) getTeam(c *Ctx) (any, error) {
	t, s, err := a.teamCtx(c, "viewer")
	if err != nil {
		// members of a project in the team can still see the team
		return nil, err
	}
	projects, err := store.List[store.Project](c, a.St.Pool, `SELECT * FROM projects WHERE team_id=$1 ORDER BY name`, t.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"team": t, "projects": projects, "role": c.P.RoleAt(s)}, nil
}

func (a *API) updateTeam(c *Ctx) (any, error) {
	t, _, err := a.teamCtx(c, "admin")
	if err != nil {
		return nil, err
	}
	var in hierarchyIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if err := validSettings(in.Settings); err != nil {
		return nil, err
	}
	before := *t
	if in.Name != nil {
		t.Name = *in.Name
	}
	if in.Description != nil {
		t.Description = *in.Description
	}
	if in.Settings != nil {
		t.Settings = *in.Settings
	}
	if err := a.St.Update(c, a.St.Pool, "teams", t); err != nil {
		return nil, err
	}
	c.Audit("team.update", t.OrgID, "team", t.ID, map[string]any{"before": before, "after": t})
	a.changed(c)
	return t, nil
}

func (a *API) deleteTeam(c *Ctx) (any, error) {
	t, _, err := a.teamCtx(c, "owner")
	if err != nil {
		return nil, err
	}
	if err := a.St.Delete(c, a.St.Pool, "teams", t.ID); err != nil {
		return nil, err
	}
	_, _ = a.St.Pool.Exec(c, `DELETE FROM memberships WHERE (scope_type='team' AND scope_id=$1) OR (scope_type='project' AND scope_id NOT IN (SELECT id FROM projects))`, t.ID)
	c.Audit("team.delete", t.OrgID, "team", t.ID, t)
	a.changed(c)
	return nil, nil
}

// --- Projects

type projectView struct {
	store.Project
	TeamName string `json:"team_name" db:"team_name"`
	Keys     int64  `json:"key_count" db:"key_count"`
}

func (a *API) listProjects(c *Ctx) (any, error) {
	o, s, err := a.orgCtx(c, "")
	if err != nil {
		return nil, err
	}
	q := `SELECT p.*, t.name AS team_name, (SELECT count(*) FROM api_keys k WHERE k.project_id=p.id AND k.revoked_at IS NULL) AS key_count
		FROM projects p JOIN teams t ON t.id=p.team_id WHERE p.org_id=$1`
	args := []any{o.ID}
	if tid := c.Query("team_id"); tid != "" {
		q += ` AND p.team_id=$2`
		args = append(args, tid)
	}
	list, err := store.List[projectView](c, a.St.Pool, q+` ORDER BY t.name, p.name`, args...)
	if err != nil || c.P.Can(s, "viewer") {
		return list, err
	}
	out := []projectView{}
	for _, p := range list {
		if c.P.Can(auth.Scope{Type: "project", ID: p.ID, OrgID: p.OrgID, TeamID: p.TeamID}, "viewer") {
			out = append(out, p)
		}
	}
	return out, nil
}

func (a *API) createProject(c *Ctx) (any, error) {
	t, _, err := a.teamCtx(c, "admin")
	if err != nil {
		return nil, err
	}
	var in hierarchyIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	slug, err := in.slug()
	if err != nil {
		return nil, err
	}
	if err := validSettings(in.Settings); err != nil {
		return nil, err
	}
	p := &store.Project{ID: util.NewID("prj"), OrgID: t.OrgID, TeamID: t.ID, Slug: slug, Name: util.Deref(in.Name), Description: util.Deref(in.Description)}
	if in.Settings != nil {
		p.Settings = *in.Settings
	}
	if err := a.St.Insert(c, a.St.Pool, "projects", p); err != nil {
		return nil, err
	}
	c.Audit("project.create", t.OrgID, "project", p.ID, p)
	a.changed(c)
	return created{p}, nil
}

func (a *API) projectCtx(c *Ctx, role string) (*store.Project, auth.Scope, error) {
	p, err := store.GetByID[store.Project](c, a.St.Pool, "projects", c.Param("id"))
	if err != nil {
		return nil, auth.Scope{}, err
	}
	s := auth.Scope{Type: "project", ID: p.ID, OrgID: p.OrgID, TeamID: p.TeamID}
	if !c.P.Can(s, role) {
		return nil, s, util.ErrForbidden
	}
	return p, s, nil
}

func (a *API) getProject(c *Ctx) (any, error) {
	p, s, err := a.projectCtx(c, "viewer")
	if err != nil {
		return nil, err
	}
	return map[string]any{"project": p, "role": c.P.RoleAt(s)}, nil
}

func (a *API) updateProject(c *Ctx) (any, error) {
	p, _, err := a.projectCtx(c, "admin")
	if err != nil {
		return nil, err
	}
	var in hierarchyIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if err := validSettings(in.Settings); err != nil {
		return nil, err
	}
	before := *p
	if in.Name != nil {
		p.Name = *in.Name
	}
	if in.Description != nil {
		p.Description = *in.Description
	}
	if in.Settings != nil {
		p.Settings = *in.Settings
	}
	if err := a.St.Update(c, a.St.Pool, "projects", p); err != nil {
		return nil, err
	}
	c.Audit("project.update", p.OrgID, "project", p.ID, map[string]any{"before": before, "after": p})
	a.changed(c)
	return p, nil
}

func (a *API) deleteProject(c *Ctx) (any, error) {
	p, _, err := a.projectCtx(c, "admin")
	if err != nil {
		return nil, err
	}
	if err := a.St.Delete(c, a.St.Pool, "projects", p.ID); err != nil {
		return nil, err
	}
	_, _ = a.St.Pool.Exec(c, `DELETE FROM memberships WHERE scope_type='project' AND scope_id=$1`, p.ID)
	c.Audit("project.delete", p.OrgID, "project", p.ID, p)
	a.changed(c)
	return nil, nil
}

// --- Memberships (owners/admins at org and team level, members at project level)

type memberView struct {
	store.Membership
	Email string `json:"email" db:"email"`
	Name  string `json:"name" db:"name"`
}

func (a *API) listMembers(c *Ctx) (any, error) {
	s, err := a.scopeOf(c, c.Query("scope_type"), c.Query("scope_id"))
	if err != nil {
		return nil, err
	}
	if err := c.Require(s, "viewer"); err != nil {
		return nil, err
	}
	return store.List[memberView](c, a.St.Pool, `SELECT m.*, u.email, u.name FROM memberships m JOIN users u ON u.id=m.user_id
		WHERE m.scope_type=$1 AND m.scope_id=$2 ORDER BY CASE m.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 WHEN 'member' THEN 2 ELSE 3 END, u.email`, s.Type, s.ID)
}

// canGrant: admins grant up to admin; only owners grant/revoke owner.
func canGrant(c *Ctx, s auth.Scope, role string) error {
	need := "admin"
	if role == "owner" {
		need = "owner"
	}
	return c.Require(s, need)
}

func (a *API) addMember(c *Ctx) (any, error) {
	var in struct {
		Email     string `json:"email"`
		Name      string `json:"name"`
		ScopeType string `json:"scope_type"`
		ScopeID   string `json:"scope_id"`
		Role      string `json:"role"`
	}
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if !auth.ValidRole(in.Role) || !strings.Contains(in.Email, "@") {
		return nil, badReq("email and a valid role (owner, admin, member, viewer) are required")
	}
	s, err := a.scopeOf(c, in.ScopeType, in.ScopeID)
	if err != nil {
		return nil, err
	}
	if err := canGrant(c, s, in.Role); err != nil {
		return nil, err
	}
	u, err := a.St.UserByEmail(c, in.Email)
	if err == util.ErrNotFound {
		// Pre-provision: the user can sign in via SSO (or have a password set by an admin).
		u = &store.User{ID: util.NewID("usr"), Email: strings.ToLower(strings.TrimSpace(in.Email)), Name: in.Name, Status: "active"}
		if err := a.St.Insert(c, a.St.Pool, "users", u); err != nil {
			return nil, err
		}
		c.Audit("user.create", s.OrgID, "user", u.ID, map[string]string{"email": u.Email, "via": "invite"})
	} else if err != nil {
		return nil, err
	}
	m := &store.Membership{ID: util.NewID("mem"), UserID: u.ID, OrgID: s.OrgID, ScopeType: s.Type, ScopeID: s.ID, Role: in.Role, Source: "manual"}
	if err := a.St.Insert(c, a.St.Pool, "memberships", m); err != nil {
		return nil, err
	}
	a.Res.Invalidate()
	c.Audit("member.add", s.OrgID, s.Type, s.ID, map[string]string{"email": u.Email, "role": in.Role})
	return created{memberView{Membership: *m, Email: u.Email, Name: u.Name}}, nil
}

func (a *API) memberCtx(c *Ctx) (*store.Membership, auth.Scope, error) {
	m, err := store.GetByID[store.Membership](c, a.St.Pool, "memberships", c.Param("id"))
	if err != nil {
		return nil, auth.Scope{}, err
	}
	s, err := a.scopeOf(c, m.ScopeType, m.ScopeID)
	return m, s, err
}

func (a *API) updateMember(c *Ctx) (any, error) {
	m, s, err := a.memberCtx(c)
	if err != nil {
		return nil, err
	}
	var in struct{ Role string }
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if !auth.ValidRole(in.Role) {
		return nil, badReq("invalid role")
	}
	if err := canGrant(c, s, in.Role); err != nil {
		return nil, err
	}
	if err := canGrant(c, s, m.Role); err != nil {
		return nil, err
	}
	if m.Role == "owner" && in.Role != "owner" {
		if err := a.ensureAnotherOwner(c, m); err != nil {
			return nil, err
		}
	}
	old := m.Role
	m.Role = in.Role
	if err := a.St.Update(c, a.St.Pool, "memberships", m); err != nil {
		return nil, err
	}
	a.Res.Invalidate()
	c.Audit("member.update", s.OrgID, s.Type, s.ID, map[string]string{"user_id": m.UserID, "from": old, "to": in.Role})
	return m, nil
}

func (a *API) ensureAnotherOwner(c *Ctx, m *store.Membership) error {
	if m.ScopeType != "org" {
		return nil
	}
	var n int
	_ = a.St.Pool.QueryRow(c, `SELECT count(*) FROM memberships WHERE scope_type='org' AND scope_id=$1 AND role='owner' AND id<>$2`, m.ScopeID, m.ID).Scan(&n)
	if n == 0 {
		return badReq("an organization must keep at least one owner")
	}
	return nil
}

func (a *API) removeMember(c *Ctx) (any, error) {
	m, s, err := a.memberCtx(c)
	if err != nil {
		return nil, err
	}
	if m.UserID != c.P.UserID() { // anyone may leave
		if err := canGrant(c, s, m.Role); err != nil {
			return nil, err
		}
	}
	if m.Role == "owner" {
		if err := a.ensureAnotherOwner(c, m); err != nil {
			return nil, err
		}
	}
	if err := a.St.Delete(c, a.St.Pool, "memberships", m.ID); err != nil {
		return nil, err
	}
	a.Res.Invalidate()
	c.Audit("member.remove", s.OrgID, s.Type, s.ID, map[string]string{"user_id": m.UserID, "role": m.Role})
	return nil, nil
}

// --- Users

type userView struct {
	store.User
	OrgRole string `json:"org_role" db:"org_role"`
	Teams   int64  `json:"team_count" db:"team_count"`
}

func (a *API) orgUsers(c *Ctx) (any, error) {
	o, _, err := a.orgCtx(c, "viewer")
	if err != nil {
		return nil, err
	}
	return store.List[userView](c, a.St.Pool, `SELECT u.*, COALESCE((SELECT role FROM memberships WHERE user_id=u.id AND scope_type='org' AND scope_id=$1),'') AS org_role,
		(SELECT count(*) FROM memberships WHERE user_id=u.id AND scope_type='team' AND org_id=$1) AS team_count
		FROM users u WHERE u.id IN (SELECT user_id FROM memberships WHERE org_id=$1) ORDER BY u.email`, o.ID)
}

func (a *API) listUsers(c *Ctx) (any, error) {
	if err := c.RequirePlatformAdmin(); err != nil {
		return nil, err
	}
	return store.List[store.User](c, a.St.Pool, `SELECT * FROM users ORDER BY email LIMIT 5000`)
}

func (a *API) createUser(c *Ctx) (any, error) {
	if err := c.RequirePlatformAdmin(); err != nil {
		return nil, err
	}
	var in struct {
		Email, Name, Password string
		PlatformAdmin         bool `json:"is_platform_admin"`
	}
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if !strings.Contains(in.Email, "@") {
		return nil, badReq("valid email required")
	}
	u := &store.User{ID: util.NewID("usr"), Email: strings.ToLower(strings.TrimSpace(in.Email)), Name: in.Name, Status: "active", IsPlatformAdmin: in.PlatformAdmin}
	if in.Password != "" {
		if len(in.Password) < 12 {
			return nil, badReq("password must be at least 12 characters")
		}
		h, err := auth.HashPassword(in.Password)
		if err != nil {
			return nil, err
		}
		u.PasswordHash = &h
	}
	if err := a.St.Insert(c, a.St.Pool, "users", u); err != nil {
		return nil, err
	}
	c.Audit("user.create", "", "user", u.ID, map[string]any{"email": u.Email, "platform_admin": u.IsPlatformAdmin})
	return created{u}, nil
}

func (a *API) updateUser(c *Ctx) (any, error) {
	if err := c.RequirePlatformAdmin(); err != nil {
		return nil, err
	}
	u, err := a.St.UserByID(c, c.Param("id"))
	if err != nil {
		return nil, err
	}
	var in struct {
		Name          *string `json:"name"`
		Status        *string `json:"status"`
		PlatformAdmin *bool   `json:"is_platform_admin"`
		Password      *string `json:"password"`
		// IdpID binds the account to an identity provider so it can sign in
		// through it ("" unbinds). Identity providers never link accounts that
		// are platform admins or belong to other organizations on their own, so
		// this is the explicit, platform-admin-approved way to do it.
		IdpID *string `json:"idp_id"`
	}
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if u.ID == c.P.User.ID && ((in.Status != nil && *in.Status != "active") || (in.PlatformAdmin != nil && !*in.PlatformAdmin)) {
		return nil, badReq("you cannot suspend or demote yourself")
	}
	if in.Name != nil {
		u.Name = *in.Name
	}
	if in.Status != nil {
		u.Status = *in.Status
	}
	if in.PlatformAdmin != nil {
		u.IsPlatformAdmin = *in.PlatformAdmin
	}
	if in.IdpID != nil {
		if *in.IdpID == "" {
			u.IdpID, u.ExternalID = nil, nil
		} else {
			if _, err := store.GetByID[store.IdP](c, a.St.Pool, "idp_providers", *in.IdpID); err != nil {
				return nil, badReq("idp_id: identity provider not found")
			}
			if u.IdpID == nil || *u.IdpID != *in.IdpID {
				u.ExternalID = nil // re-learnt from the IdP subject at next sign-in
			}
			u.IdpID = in.IdpID
		}
	}
	if in.Password != nil {
		if len(*in.Password) < 12 {
			return nil, badReq("password must be at least 12 characters")
		}
		h, err := auth.HashPassword(*in.Password)
		if err != nil {
			return nil, err
		}
		u.PasswordHash = &h
	}
	if err := a.St.Update(c, a.St.Pool, "users", u); err != nil {
		return nil, err
	}
	a.Res.Invalidate()
	c.Audit("user.update", "", "user", u.ID, map[string]any{"status": u.Status, "platform_admin": u.IsPlatformAdmin, "password_reset": in.Password != nil, "idp_id": util.Deref(u.IdpID)})
	return u, nil
}

// purgeCache clears the response cache for an organization, or one project
// with ?project_id=. Org admins (or project admins for their project).
func (a *API) purgeCache(c *Ctx) (any, error) {
	o, os, err := a.orgCtx(c, "")
	if err != nil {
		return nil, err
	}
	pid := c.Query("project_id")
	if pid == "" {
		if err := c.Require(os, "admin"); err != nil {
			return nil, err
		}
	} else {
		s, err := a.scopeOf(c, "project", pid)
		if err != nil || s.OrgID != o.ID {
			return nil, badReq("project not found in this organization")
		}
		if err := c.Require(s, "admin"); err != nil {
			return nil, err
		}
	}
	if a.Cache == nil {
		return map[string]any{"deleted": 0}, nil
	}
	n, err := a.Cache.Purge(c, o.ID, pid)
	if err != nil {
		return nil, err
	}
	c.Audit("cache.purge", o.ID, "org", o.ID, map[string]any{"project_id": pid, "deleted": n})
	return map[string]any{"deleted": n}, nil
}
