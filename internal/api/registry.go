package api

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"theophylax/internal/auth"
	"theophylax/internal/store"
	"theophylax/internal/util"
)

var semverRe = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$`)

type registryView struct {
	store.RegistryItem
	OwnerTeam    *string `json:"owner_team" db:"owner_team"`
	CreatorEmail *string `json:"creator_email" db:"creator_email"`
	Installed    int64   `json:"installed_in_scopes" db:"installed_in_scopes"`
}

const registrySelect = `SELECT i.*, t.name AS owner_team, u.email AS creator_email,
	(SELECT count(*) FROM installations x WHERE x.item_id=i.id) AS installed_in_scopes
	FROM registry_items i LEFT JOIN teams t ON t.id=i.owner_team_id LEFT JOIN users u ON u.id=i.created_by`

// listRegistry serves both the marketplace (approved items) and the
// publisher view (drafts and pending reviews the caller can see).
func (a *API) listRegistry(c *Ctx) (any, error) {
	o, os, err := a.orgCtx(c, "")
	if err != nil {
		return nil, err
	}
	q := registrySelect + ` WHERE i.org_id=$1`
	args := []any{o.ID}
	if k := c.Query("kind"); k != "" {
		args = append(args, k)
		q += fmt.Sprintf(` AND i.kind=$%d`, len(args))
	}
	if s := c.Query("q"); s != "" {
		args = append(args, "%"+strings.ToLower(s)+"%")
		q += fmt.Sprintf(` AND (lower(i.name) LIKE $%d OR lower(i.description) LIKE $%d OR lower(array_to_string(i.tags,' ')) LIKE $%d)`, len(args), len(args), len(args))
	}
	switch c.Query("view") {
	case "marketplace":
		q += ` AND i.approved_version<>'' AND i.status<>'deprecated' ORDER BY i.installs DESC, i.rating_sum DESC, i.name`
	case "review":
		if err := c.Require(os, "admin"); err != nil {
			return nil, err
		}
		q += ` AND i.status='pending_review' ORDER BY i.updated_at`
	default:
		q += ` ORDER BY i.updated_at DESC`
	}
	list, err := store.List[registryView](c, a.St.Pool, q, args...)
	if err != nil {
		return nil, err
	}
	out := []registryView{}
	for _, it := range list {
		if a.canSeeItem(c, &it.RegistryItem) {
			out = append(out, it)
		}
	}
	return out, nil
}

func (a *API) canSeeItem(c *Ctx, it *store.RegistryItem) bool {
	org := auth.Scope{Type: "org", ID: it.OrgID, OrgID: it.OrgID}
	if c.P.Can(org, "admin") || util.Deref(it.CreatedBy) == c.P.UserID() {
		return true
	}
	if it.OwnerTeamID != nil && c.P.Can(auth.Scope{Type: "team", ID: *it.OwnerTeamID, OrgID: it.OrgID, TeamID: *it.OwnerTeamID}, "member") {
		return true
	}
	if it.ApprovedVersion == "" {
		return false
	}
	if it.Visibility == "team" {
		return false
	}
	return c.P.InOrg(it.OrgID)
}

func (a *API) canPublish(c *Ctx, it *store.RegistryItem) bool {
	if util.Deref(it.CreatedBy) == c.P.UserID() {
		return true
	}
	if it.OwnerTeamID != nil && c.P.Can(auth.Scope{Type: "team", ID: *it.OwnerTeamID, OrgID: it.OrgID, TeamID: *it.OwnerTeamID}, "admin") {
		return true
	}
	return c.P.Can(auth.Scope{Type: "org", ID: it.OrgID, OrgID: it.OrgID}, "admin")
}

// normaliseContent validates per-kind content and seals secrets.
func (a *API) normaliseContent(kind string, raw json.RawMessage) (json.RawMessage, error) {
	var c map[string]any
	if err := json.Unmarshal(raw, &c); err != nil || c == nil {
		return nil, badReq("content must be a JSON object")
	}
	switch kind {
	case "skill":
		md, _ := c["skill_md"].(string)
		if strings.TrimSpace(md) == "" {
			return nil, badReq("skill content requires skill_md (the SKILL.md body)")
		}
		if files, ok := c["files"].(map[string]any); ok {
			for p := range files {
				clean := path.Clean(p)
				if strings.HasPrefix(clean, "..") || strings.HasPrefix(clean, "/") {
					return nil, badReq("invalid file path " + p)
				}
			}
		}
	case "mcp_server":
		u, _ := c["url"].(string)
		cmd, _ := c["command"].(string)
		if u == "" && cmd == "" {
			return nil, badReq("mcp_server content requires url (remote) or command (stdio)")
		}
		if tp, ok := c["tool_policy"]; ok {
			b, _ := json.Marshal(tp)
			var pol struct {
				Allow []string `json:"allow"`
				Deny  []string `json:"deny"`
			}
			dec := json.NewDecoder(bytes.NewReader(b))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&pol); err != nil {
				return nil, badReq("tool_policy must be {\"allow\": [tool names], \"deny\": [tool names]}; a trailing * matches a prefix")
			}
		}
		if tok, _ := c["authorization_token"].(string); tok != "" {
			c["authorization_token_enc"] = base64.StdEncoding.EncodeToString(a.Box.SealString(tok))
			delete(c, "authorization_token")
			c["has_authorization_token"] = true
		}
	case "agent":
		if m, _ := c["model"].(string); m == "" {
			return nil, badReq("agent template requires model")
		}
	case "prompt":
		if t, _ := c["template"].(string); t == "" {
			return nil, badReq("prompt requires template")
		}
	}
	out, _ := json.Marshal(c)
	return out, nil
}

func redactContent(raw json.RawMessage) json.RawMessage {
	if !strings.Contains(string(raw), "authorization_token_enc") {
		return raw
	}
	var c map[string]any
	_ = json.Unmarshal(raw, &c)
	delete(c, "authorization_token_enc")
	out, _ := json.Marshal(c)
	return out
}

func (a *API) createRegistryItem(c *Ctx) (any, error) {
	o, os, err := a.orgCtx(c, "")
	if err != nil {
		return nil, err
	}
	var in struct {
		Kind        string          `json:"kind"`
		Name        string          `json:"name"`
		Slug        string          `json:"slug"`
		Description string          `json:"description"`
		Tags        []string        `json:"tags"`
		OwnerTeamID string          `json:"owner_team_id"`
		Visibility  string          `json:"visibility"`
		Version     string          `json:"version"`
		Changelog   string          `json:"changelog"`
		Content     json.RawMessage `json:"content"`
		Submit      bool            `json:"submit"`
	}
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if err := c.Require(os, "member"); err != nil {
		return nil, err
	}
	slug := in.Slug
	if slug == "" {
		slug = util.Slugify(in.Name)
	}
	if !util.ValidSlug(slug) || in.Name == "" {
		return nil, badReq("name is required")
	}
	if in.Version == "" {
		in.Version = "1.0.0"
	}
	if !semverRe.MatchString(in.Version) {
		return nil, badReq("version must be semver (e.g. 1.0.0)")
	}
	content, err := a.normaliseContent(in.Kind, in.Content)
	if err != nil {
		return nil, err
	}
	uid := c.P.UserID()
	it := &store.RegistryItem{ID: util.NewID("reg"), OrgID: o.ID, Kind: in.Kind, Slug: slug, Name: in.Name, Description: in.Description, Tags: in.Tags,
		Visibility: "org", Status: "draft", LatestVersion: in.Version, CreatedBy: &uid, Publisher: c.P.Email()}
	if it.Tags == nil {
		it.Tags = []string{}
	}
	if in.Visibility == "team" {
		it.Visibility = "team"
	}
	if in.OwnerTeamID != "" {
		ts, err := a.scopeOf(c, "team", in.OwnerTeamID)
		if err != nil || ts.OrgID != o.ID {
			return nil, badReq("owner team not found")
		}
		if err := c.Require(ts, "member"); err != nil {
			return nil, err
		}
		it.OwnerTeamID = &ts.ID
		if snap := a.Loader.Snapshot(); snap.Teams[ts.ID] != nil {
			it.Publisher = snap.Teams[ts.ID].Name
		}
	}
	// Org admins publishing directly are trusted reviewers.
	autoApprove := in.Submit && c.P.Can(os, "admin")
	if in.Submit && !autoApprove {
		it.Status = "pending_review"
	}
	if autoApprove {
		it.Status, it.ApprovedVersion, it.ReviewedBy = "approved", in.Version, &uid
	}
	sum := sha256.Sum256(content)
	rv := &store.RegistryVersion{ID: util.NewID("rv"), ItemID: it.ID, Version: in.Version, Content: content, Digest: "sha256:" + hex.EncodeToString(sum[:]), Changelog: in.Changelog, CreatedBy: &uid}
	if autoApprove {
		now := time.Now()
		rv.ApprovedAt, rv.ApprovedBy = &now, &uid
	}
	err = a.St.Tx(c, func(tx pgx.Tx) error {
		if err := a.St.Insert(c, tx, "registry_items", it); err != nil {
			return err
		}
		return a.St.Insert(c, tx, "registry_versions", rv)
	})
	if err != nil {
		return nil, err
	}
	c.Audit("registry.create", o.ID, "registry_item", it.ID, map[string]any{"kind": it.Kind, "name": it.Name, "version": in.Version, "status": it.Status})
	if it.Status == "approved" {
		a.changed(c)
	}
	return created{it}, nil
}

func (a *API) itemCtx(c *Ctx) (*store.RegistryItem, error) {
	it, err := store.GetByID[store.RegistryItem](c, a.St.Pool, "registry_items", c.Param("id"))
	if err != nil {
		return nil, err
	}
	if !a.canSeeItem(c, it) {
		return nil, util.ErrNotFound
	}
	return it, nil
}

type installView struct {
	store.Installation
	ScopeName string `json:"scope_name"`
}

func (a *API) getRegistryItem(c *Ctx) (any, error) {
	it, err := a.itemCtx(c)
	if err != nil {
		return nil, err
	}
	vers, err := store.List[store.RegistryVersion](c, a.St.Pool, `SELECT * FROM registry_versions WHERE item_id=$1 ORDER BY created_at DESC`, it.ID)
	if err != nil {
		return nil, err
	}
	for i := range vers {
		vers[i].Content = redactContent(vers[i].Content)
	}
	inst, err := store.List[store.Installation](c, a.St.Pool, `SELECT * FROM installations WHERE item_id=$1 ORDER BY created_at DESC`, it.ID)
	if err != nil {
		return nil, err
	}
	snap := a.Loader.Snapshot()
	iv := make([]installView, 0, len(inst))
	for _, x := range inst {
		iv = append(iv, installView{Installation: x, ScopeName: a.scopeName(snap, x.ScopeType, x.ScopeID)})
	}
	var myRating *int
	var r int
	if a.St.Pool.QueryRow(c, `SELECT rating FROM registry_ratings WHERE item_id=$1 AND user_id=$2`, it.ID, c.P.UserID()).Scan(&r) == nil {
		myRating = &r
	}
	org := auth.Scope{Type: "org", ID: it.OrgID, OrgID: it.OrgID}
	return map[string]any{"item": it, "versions": vers, "installations": iv, "my_rating": myRating,
		"can_publish": a.canPublish(c, it), "can_review": c.P.Can(org, "admin")}, nil
}

func (a *API) updateRegistryItem(c *Ctx) (any, error) {
	it, err := a.itemCtx(c)
	if err != nil {
		return nil, err
	}
	if !a.canPublish(c, it) {
		return nil, util.ErrForbidden
	}
	var in struct {
		Name        *string   `json:"name"`
		Description *string   `json:"description"`
		Tags        *[]string `json:"tags"`
		Visibility  *string   `json:"visibility"`
		Deprecated  *bool     `json:"deprecated"`
	}
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if in.Name != nil {
		it.Name = *in.Name
	}
	if in.Description != nil {
		it.Description = *in.Description
	}
	if in.Tags != nil {
		it.Tags = *in.Tags
	}
	if in.Visibility != nil {
		it.Visibility = *in.Visibility
	}
	if in.Deprecated != nil {
		if *in.Deprecated {
			it.Status = "deprecated"
		} else if it.ApprovedVersion != "" {
			it.Status = "approved"
		} else {
			it.Status = "draft"
		}
	}
	if err := a.St.Update(c, a.St.Pool, "registry_items", it); err != nil {
		return nil, err
	}
	c.Audit("registry.update", it.OrgID, "registry_item", it.ID, map[string]any{"status": it.Status, "visibility": it.Visibility})
	a.changed(c)
	return it, nil
}

func (a *API) deleteRegistryItem(c *Ctx) (any, error) {
	it, err := a.itemCtx(c)
	if err != nil {
		return nil, err
	}
	if !a.canPublish(c, it) {
		return nil, util.ErrForbidden
	}
	if err := a.St.Delete(c, a.St.Pool, "registry_items", it.ID); err != nil {
		return nil, err
	}
	c.Audit("registry.delete", it.OrgID, "registry_item", it.ID, map[string]string{"name": it.Name})
	a.changed(c)
	return nil, nil
}

func (a *API) publishVersion(c *Ctx) (any, error) {
	it, err := a.itemCtx(c)
	if err != nil {
		return nil, err
	}
	if !a.canPublish(c, it) {
		return nil, util.ErrForbidden
	}
	var in struct {
		Version   string          `json:"version"`
		Changelog string          `json:"changelog"`
		Content   json.RawMessage `json:"content"`
		Submit    bool            `json:"submit"`
	}
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if !semverRe.MatchString(in.Version) {
		return nil, badReq("version must be semver (e.g. 1.2.0)")
	}
	content, err := a.normaliseContent(it.Kind, in.Content)
	if err != nil {
		return nil, err
	}
	uid := c.P.UserID()
	sum := sha256.Sum256(content)
	v := &store.RegistryVersion{ID: util.NewID("rv"), ItemID: it.ID, Version: in.Version, Content: content, Digest: "sha256:" + hex.EncodeToString(sum[:]), Changelog: in.Changelog, CreatedBy: &uid}
	org := auth.Scope{Type: "org", ID: it.OrgID, OrgID: it.OrgID}
	approve := in.Submit && c.P.Can(org, "admin")
	if approve {
		now := time.Now()
		v.ApprovedAt, v.ApprovedBy = &now, &uid
	}
	err = a.St.Tx(c, func(tx pgx.Tx) error {
		if err := a.St.Insert(c, tx, "registry_versions", v); err != nil {
			return err
		}
		it.LatestVersion = in.Version
		switch {
		case approve:
			it.Status, it.ApprovedVersion, it.ReviewedBy = "approved", in.Version, &uid
		case in.Submit:
			it.Status = "pending_review"
		}
		return a.St.Update(c, tx, "registry_items", it)
	})
	if err != nil {
		return nil, err
	}
	c.Audit("registry.publish", it.OrgID, "registry_item", it.ID, map[string]any{"version": in.Version, "digest": v.Digest, "status": it.Status})
	a.changed(c)
	return created{v}, nil
}

func (a *API) submitForReview(c *Ctx) (any, error) {
	it, err := a.itemCtx(c)
	if err != nil {
		return nil, err
	}
	if !a.canPublish(c, it) {
		return nil, util.ErrForbidden
	}
	if it.ApprovedVersion == it.LatestVersion {
		return nil, badReq("the latest version is already approved")
	}
	it.Status = "pending_review"
	if err := a.St.Update(c, a.St.Pool, "registry_items", it); err != nil {
		return nil, err
	}
	c.Audit("registry.submit", it.OrgID, "registry_item", it.ID, map[string]string{"version": it.LatestVersion})
	return it, nil
}

func (a *API) reviewItem(c *Ctx) (any, error) {
	it, err := a.itemCtx(c)
	if err != nil {
		return nil, err
	}
	if err := c.Require(auth.Scope{Type: "org", ID: it.OrgID, OrgID: it.OrgID}, "admin"); err != nil {
		return nil, err
	}
	var in struct {
		Decision string `json:"decision"`
		Note     string `json:"note"`
	}
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	uid := c.P.UserID()
	switch in.Decision {
	case "approve":
		it.Status, it.ApprovedVersion = "approved", it.LatestVersion
	case "reject":
		it.Status = "rejected"
		if it.ApprovedVersion != "" {
			it.Status = "approved" // previous approved version stays live
		}
	default:
		return nil, badReq("decision must be approve or reject")
	}
	it.ReviewedBy, it.ReviewNote = &uid, in.Note
	err = a.St.Tx(c, func(tx pgx.Tx) error {
		if err := a.St.Update(c, tx, "registry_items", it); err != nil {
			return err
		}
		if in.Decision != "approve" {
			return nil
		}
		_, err := tx.Exec(c, `UPDATE registry_versions SET approved_at=now(), approved_by=$3 WHERE item_id=$1 AND version=$2 AND approved_at IS NULL`, it.ID, it.ApprovedVersion, uid)
		return err
	})
	if err != nil {
		return nil, err
	}
	c.Audit("registry.review", it.OrgID, "registry_item", it.ID, map[string]string{"decision": in.Decision, "version": it.LatestVersion, "note": in.Note})
	a.changed(c)
	return it, nil
}

func (a *API) installItem(c *Ctx) (any, error) {
	it, err := a.itemCtx(c)
	if err != nil {
		return nil, err
	}
	if it.ApprovedVersion == "" {
		return nil, badReq("only approved items can be installed")
	}
	var in struct {
		ScopeType string `json:"scope_type"`
		ScopeID   string `json:"scope_id"`
		Version   string `json:"version"`
	}
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	s, err := a.scopeOf(c, in.ScopeType, in.ScopeID)
	if err != nil || s.OrgID != it.OrgID {
		return nil, badReq("invalid scope")
	}
	if err := c.Require(s, "admin"); err != nil {
		return nil, err
	}
	pinned := in.Version != ""
	if !pinned {
		in.Version = it.ApprovedVersion
	} else if !a.versionApproved(c, it, in.Version) {
		return nil, badReq("only an approved version can be pinned")
	}
	uid := c.P.UserID()
	inst := &store.Installation{ID: util.NewID("ins"), ItemID: it.ID, OrgID: it.OrgID, Version: in.Version, Pinned: pinned, ScopeType: s.Type, ScopeID: s.ID, InstalledBy: &uid}
	err = a.St.Tx(c, func(tx pgx.Tx) error {
		if err := a.St.Insert(c, tx, "installations", inst); err != nil {
			return err
		}
		_, err := tx.Exec(c, `UPDATE registry_items SET installs=installs+1 WHERE id=$1`, it.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	// Installing an agent template materialises a managed agent in the team.
	var agentID string
	if it.Kind == "agent" && s.Type != "org" {
		var content struct {
			Model        string          `json:"model"`
			SystemPrompt string          `json:"system_prompt"`
			Skills       []string        `json:"skills"`
			Params       json.RawMessage `json:"params"`
		}
		var raw json.RawMessage
		_ = a.St.Pool.QueryRow(c, `SELECT content FROM registry_versions WHERE item_id=$1 AND version=$2`, it.ID, in.Version).Scan(&raw)
		_ = json.Unmarshal(raw, &content)
		ag := &store.Agent{ID: util.NewID("agt"), OrgID: it.OrgID, TeamID: s.TeamID, Slug: it.Slug + "-" + strings.ToLower(util.RandomToken(4)), Name: it.Name, Description: it.Description,
			Model: content.Model, SystemPrompt: content.SystemPrompt, Skills: content.Skills, MCPServers: []string{}, Params: jsonOr(content.Params, "{}"), Tools: json.RawMessage("[]"), AllowClientSystem: true, OwnerUserID: &uid, Status: "active"}
		if ag.Skills == nil {
			ag.Skills = []string{}
		}
		if s.Type == "project" {
			ag.ProjectID = &s.ID
		}
		if err := a.St.Insert(c, a.St.Pool, "agents", ag); err == nil {
			agentID = ag.ID
		}
	}
	c.Audit("registry.install", it.OrgID, s.Type, s.ID, map[string]any{"item": it.Name, "version": in.Version, "pinned": pinned, "agent_id": agentID})
	a.changed(c)
	return created{map[string]any{"installation": inst, "agent_id": agentID}}, nil
}

// versionApproved reports whether ver of the item is, or once was, approved.
func (a *API) versionApproved(c *Ctx, it *store.RegistryItem, ver string) bool {
	if ver == it.ApprovedVersion {
		return true
	}
	var ok bool
	_ = a.St.Pool.QueryRow(c, `SELECT EXISTS(SELECT 1 FROM registry_versions WHERE item_id=$1 AND version=$2 AND approved_at IS NOT NULL)`, it.ID, ver).Scan(&ok)
	return ok
}

// updateInstallation pins an installation to an approved version, or with an
// empty version makes it follow the item's approved version again. Pinning
// the org-wide installation and pinning a team or project to a newer version
// stages a rollout; pinning back to an earlier version rolls it back.
func (a *API) updateInstallation(c *Ctx) (any, error) {
	inst, err := store.GetByID[store.Installation](c, a.St.Pool, "installations", c.Param("id"))
	if err != nil {
		return nil, err
	}
	s, err := a.scopeOf(c, inst.ScopeType, inst.ScopeID)
	if err != nil {
		return nil, err
	}
	if err := c.Require(s, "admin"); err != nil {
		return nil, err
	}
	it, err := store.GetByID[store.RegistryItem](c, a.St.Pool, "registry_items", inst.ItemID)
	if err != nil {
		return nil, err
	}
	var in struct {
		Version *string `json:"version"`
	}
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if in.Version == nil {
		return nil, badReq(`version is required: an approved version to pin, or "" to follow the approved version`)
	}
	if *in.Version == "" {
		inst.Pinned, inst.Version = false, it.ApprovedVersion
	} else {
		if !a.versionApproved(c, it, *in.Version) {
			return nil, badReq("only an approved version can be pinned")
		}
		inst.Pinned, inst.Version = true, *in.Version
	}
	if err := a.St.Update(c, a.St.Pool, "installations", inst); err != nil {
		return nil, err
	}
	c.Audit("registry.pin", inst.OrgID, s.Type, s.ID, map[string]any{"item_id": it.ID, "version": inst.Version, "pinned": inst.Pinned})
	return inst, nil
}

func (a *API) uninstall(c *Ctx) (any, error) {
	inst, err := store.GetByID[store.Installation](c, a.St.Pool, "installations", c.Param("id"))
	if err != nil {
		return nil, err
	}
	s, err := a.scopeOf(c, inst.ScopeType, inst.ScopeID)
	if err != nil {
		return nil, err
	}
	if err := c.Require(s, "admin"); err != nil {
		return nil, err
	}
	if err := a.St.Delete(c, a.St.Pool, "installations", inst.ID); err != nil {
		return nil, err
	}
	_, _ = a.St.Pool.Exec(c, `UPDATE registry_items SET installs=GREATEST(installs-1,0) WHERE id=$1`, inst.ItemID)
	c.Audit("registry.uninstall", inst.OrgID, s.Type, s.ID, map[string]string{"item_id": inst.ItemID})
	return nil, nil
}

func (a *API) rateItem(c *Ctx) (any, error) {
	it, err := a.itemCtx(c)
	if err != nil {
		return nil, err
	}
	var in struct {
		Rating  int    `json:"rating"`
		Comment string `json:"comment"`
	}
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if in.Rating < 1 || in.Rating > 5 {
		return nil, badReq("rating must be 1-5")
	}
	err = a.St.Tx(c, func(tx pgx.Tx) error {
		if _, err := tx.Exec(c, `INSERT INTO registry_ratings (item_id,user_id,rating,comment) VALUES ($1,$2,$3,$4)
			ON CONFLICT (item_id,user_id) DO UPDATE SET rating=EXCLUDED.rating, comment=EXCLUDED.comment, created_at=now()`, it.ID, c.P.UserID(), in.Rating, in.Comment); err != nil {
			return err
		}
		_, err := tx.Exec(c, `UPDATE registry_items SET rating_sum=(SELECT COALESCE(sum(rating),0) FROM registry_ratings WHERE item_id=$1),
			rating_count=(SELECT count(*) FROM registry_ratings WHERE item_id=$1) WHERE id=$1`, it.ID)
		return err
	})
	return map[string]any{"ok": err == nil}, err
}

// downloadItem packages a skill as a zip ready to unpack into ~/.claude/skills.
func (a *API) downloadItem(w http.ResponseWriter, r *http.Request) {
	a.wrap(true, func(c *Ctx) (any, error) {
		it, err := a.itemCtx(c)
		if err != nil {
			return nil, err
		}
		ver := c.Query("version")
		if ver == "" {
			ver = it.ApprovedVersion
		}
		if ver == "" {
			ver = it.LatestVersion
		}
		if ver != it.ApprovedVersion && !a.canPublish(c, it) {
			return nil, util.ErrForbidden
		}
		if err := writeSkillZip(c, a, it, ver, w); err != nil {
			return nil, err
		}
		return rawResponse{}, nil
	})(w, r)
}

func writeSkillZip(c *Ctx, a *API, it *store.RegistryItem, ver string, w http.ResponseWriter) error {
	var raw json.RawMessage
	if err := a.St.Pool.QueryRow(c, `SELECT content FROM registry_versions WHERE item_id=$1 AND version=$2`, it.ID, ver).Scan(&raw); err != nil {
		return util.ErrNotFound
	}
	var content map[string]any
	_ = json.Unmarshal(redactContent(raw), &content)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.zip"`, it.Slug, ver))
	zw := zip.NewWriter(w)
	add := func(name, body string) {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: it.Slug + "/" + name, Method: zip.Deflate, Modified: it.UpdatedAt})
		if err == nil {
			_, _ = f.Write([]byte(body))
		}
	}
	switch it.Kind {
	case "skill":
		add("SKILL.md", fmt.Sprint(content["skill_md"]))
		if files, ok := content["files"].(map[string]any); ok {
			for p, body := range files {
				add(path.Clean(p), fmt.Sprint(body))
			}
		}
	default:
		b, _ := json.MarshalIndent(content, "", "  ")
		add(it.Kind+".json", string(b))
	}
	return zw.Close()
}

// --- Agents

type agentView struct {
	store.Agent
	TeamName    string  `json:"team_name" db:"team_name"`
	OwnerEmail  *string `json:"owner_email" db:"owner_email"`
	Requests24h int64   `json:"requests_24h" db:"requests_24h"`
}

func (a *API) listAgents(c *Ctx) (any, error) {
	o, _, err := a.orgCtx(c, "")
	if err != nil {
		return nil, err
	}
	return store.List[agentView](c, a.St.Pool, `SELECT ag.*, t.name AS team_name, u.email AS owner_email,
		(SELECT count(*) FROM request_logs l WHERE l.agent_id=ag.id AND l.ts > now()-interval '24 hours') AS requests_24h
		FROM agents ag JOIN teams t ON t.id=ag.team_id LEFT JOIN users u ON u.id=ag.owner_user_id WHERE ag.org_id=$1 ORDER BY ag.name`, o.ID)
}

type agentIn struct {
	TeamID            *string          `json:"team_id"`
	ProjectID         *string          `json:"project_id"`
	Slug              *string          `json:"slug"`
	Name              *string          `json:"name"`
	Description       *string          `json:"description"`
	Model             *string          `json:"model"`
	SystemPrompt      *string          `json:"system_prompt"`
	Skills            *[]string        `json:"skills"`
	MCPServers        *[]string        `json:"mcp_servers"`
	Tools             *json.RawMessage `json:"tools"`
	Params            *json.RawMessage `json:"params"`
	AllowClientSystem *bool            `json:"allow_client_system"`
	Status            *string          `json:"status"`
}

func applyAgentIn(ag *store.Agent, in *agentIn) {
	if in.Name != nil {
		ag.Name = *in.Name
	}
	if in.Description != nil {
		ag.Description = *in.Description
	}
	if in.Model != nil {
		ag.Model = *in.Model
	}
	if in.SystemPrompt != nil {
		ag.SystemPrompt = *in.SystemPrompt
	}
	if in.Skills != nil {
		ag.Skills = *in.Skills
	}
	if in.MCPServers != nil {
		ag.MCPServers = *in.MCPServers
	}
	if in.Tools != nil {
		ag.Tools = *in.Tools
	}
	if in.Params != nil {
		ag.Params = *in.Params
	}
	if in.AllowClientSystem != nil {
		ag.AllowClientSystem = *in.AllowClientSystem
	}
	if in.Status != nil {
		ag.Status = *in.Status
	}
}

func (a *API) createAgent(c *Ctx) (any, error) {
	o, _, err := a.orgCtx(c, "")
	if err != nil {
		return nil, err
	}
	var in agentIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	ts, err := a.scopeOf(c, "team", util.Deref(in.TeamID))
	if err != nil || ts.OrgID != o.ID {
		return nil, badReq("team_id is required")
	}
	if err := c.Require(ts, "admin"); err != nil {
		return nil, err
	}
	uid := c.P.UserID()
	ag := &store.Agent{ID: util.NewID("agt"), OrgID: o.ID, TeamID: ts.ID, Skills: []string{}, MCPServers: []string{}, Tools: json.RawMessage("[]"), Params: json.RawMessage("{}"), AllowClientSystem: true, OwnerUserID: &uid, Status: "active"}
	applyAgentIn(ag, &in)
	ag.Slug = util.Deref(in.Slug)
	if ag.Slug == "" {
		ag.Slug = util.Slugify(ag.Name)
	}
	if !util.ValidSlug(ag.Slug) || ag.Name == "" || ag.Model == "" {
		return nil, badReq("name and model are required")
	}
	if pid := util.Deref(in.ProjectID); pid != "" {
		ag.ProjectID = &pid
	}
	if err := a.St.Insert(c, a.St.Pool, "agents", ag); err != nil {
		return nil, err
	}
	c.Audit("agent.create", o.ID, "agent", ag.ID, ag)
	a.changed(c)
	return created{ag}, nil
}

func (a *API) agentCtx(c *Ctx, role string) (*store.Agent, error) {
	ag, err := store.GetByID[store.Agent](c, a.St.Pool, "agents", c.Param("id"))
	if err != nil {
		return nil, err
	}
	if !c.P.InOrg(ag.OrgID) {
		return nil, util.ErrNotFound
	}
	if role != "" {
		if err := c.Require(auth.Scope{Type: "team", ID: ag.TeamID, OrgID: ag.OrgID, TeamID: ag.TeamID}, role); err != nil {
			return nil, err
		}
	}
	return ag, nil
}

func (a *API) getAgent(c *Ctx) (any, error) {
	ag, err := a.agentCtx(c, "")
	if err != nil {
		return nil, err
	}
	keys, _ := store.List[store.APIKey](c, a.St.Pool, `SELECT * FROM api_keys WHERE agent_id=$1 AND revoked_at IS NULL`, ag.ID)
	return map[string]any{"agent": ag, "keys": keys, "endpoint": a.Cfg.PublicURL + "/v1/agents/" + ag.Slug + "/messages",
		"can_edit": c.P.Can(auth.Scope{Type: "team", ID: ag.TeamID, OrgID: ag.OrgID, TeamID: ag.TeamID}, "admin")}, nil
}

func (a *API) updateAgent(c *Ctx) (any, error) {
	ag, err := a.agentCtx(c, "admin")
	if err != nil {
		return nil, err
	}
	before := *ag
	var in agentIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	applyAgentIn(ag, &in)
	if err := a.St.Update(c, a.St.Pool, "agents", ag); err != nil {
		return nil, err
	}
	c.Audit("agent.update", ag.OrgID, "agent", ag.ID, map[string]any{"before": before, "after": ag})
	a.changed(c)
	return ag, nil
}

func (a *API) deleteAgent(c *Ctx) (any, error) {
	ag, err := a.agentCtx(c, "admin")
	if err != nil {
		return nil, err
	}
	if err := a.St.Delete(c, a.St.Pool, "agents", ag.ID); err != nil {
		return nil, err
	}
	c.Audit("agent.delete", ag.OrgID, "agent", ag.ID, map[string]string{"name": ag.Name})
	a.changed(c)
	return nil, nil
}
