package api

import (
	"encoding/json"
	"time"

	"theophylax/internal/auth"
	"theophylax/internal/store"
	"theophylax/internal/util"
)

type keyView struct {
	store.APIKey
	ProjectName *string `json:"project_name" db:"project_name"`
	TeamName    *string `json:"team_name" db:"team_name"`
	OwnerEmail  *string `json:"owner_email" db:"owner_email"`
	AgentName   *string `json:"agent_name" db:"agent_name"`
}

const keySelect = `SELECT k.*, p.name AS project_name, t.name AS team_name, u.email AS owner_email, ag.name AS agent_name
	FROM api_keys k LEFT JOIN projects p ON p.id=k.project_id LEFT JOIN teams t ON t.id=k.team_id
	LEFT JOIN users u ON u.id=k.user_id LEFT JOIN agents ag ON ag.id=k.agent_id`

func keyScope(k *store.APIKey) auth.Scope {
	if k.ProjectID != nil {
		return auth.Scope{Type: "project", ID: *k.ProjectID, OrgID: k.OrgID, TeamID: util.Deref(k.TeamID)}
	}
	return auth.Scope{Type: "org", ID: k.OrgID, OrgID: k.OrgID}
}

// canManageKey: the key's owner, or an admin of its project/team/org.
func canManageKey(c *Ctx, k *store.APIKey) bool {
	if k.UserID != nil && *k.UserID == c.P.UserID() {
		return true
	}
	return c.P.Can(keyScope(k), "admin")
}

func (a *API) listKeys(c *Ctx) (any, error) {
	o, _, err := a.orgCtx(c, "")
	if err != nil {
		return nil, err
	}
	q := keySelect + ` WHERE k.org_id=$1`
	args := []any{o.ID}
	if pid := c.Query("project_id"); pid != "" {
		args = append(args, pid)
		q += ` AND k.project_id=$2`
	}
	if c.Query("include_revoked") != "true" {
		q += ` AND k.revoked_at IS NULL`
	}
	list, err := store.List[keyView](c, a.St.Pool, q+` ORDER BY k.created_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	mine := c.Query("mine") == "true"
	out := []keyView{}
	for _, k := range list {
		if k.Kind == "pat" && util.Deref(k.UserID) != c.P.UserID() {
			continue
		}
		if mine && util.Deref(k.UserID) != c.P.UserID() {
			continue
		}
		if canManageKey(c, &k.APIKey) || c.P.Can(keyScope(&k.APIKey), "viewer") && c.P.Can(auth.Scope{Type: "org", ID: o.ID, OrgID: o.ID}, "viewer") {
			out = append(out, k)
		}
	}
	return out, nil
}

type keyIn struct {
	Name          *string          `json:"name"`
	Kind          string           `json:"kind"`
	OrgID         string           `json:"org_id"`
	ProjectID     string           `json:"project_id"`
	AgentID       string           `json:"agent_id"`
	AllowedModels *[]string        `json:"allowed_models"`
	RPM           *int             `json:"rpm"`
	TPM           *int             `json:"tpm"`
	ExpiresInDays *int             `json:"expires_in_days"`
	Metadata      *json.RawMessage `json:"metadata"`
}

func (a *API) createKey(c *Ctx) (any, error) {
	var in keyIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if in.Kind == "" {
		in.Kind = "gateway"
	}
	if util.Deref(in.Name) == "" {
		return nil, badReq("name is required")
	}
	k := &store.APIKey{ID: util.NewID("key"), Kind: in.Kind, Name: *in.Name, AllowedModels: []string{}}
	uid := c.P.UserID()
	if uid != "" {
		k.UserID = &uid
		k.CreatedBy = &uid
	}
	prefix := auth.GatewayKeyPrefix
	switch in.Kind {
	case "gateway":
		s, err := a.scopeOf(c, "project", in.ProjectID)
		if err != nil {
			return nil, badReq("project_id is required for gateway keys")
		}
		if err := c.Require(s, "member"); err != nil {
			return nil, err
		}
		k.OrgID, k.TeamID, k.ProjectID = s.OrgID, &s.TeamID, &s.ID
		if in.AgentID != "" {
			ag, err := store.GetByID[store.Agent](c, a.St.Pool, "agents", in.AgentID)
			if err != nil || ag.OrgID != s.OrgID {
				return nil, badReq("agent not found in this organization")
			}
			if err := c.Require(auth.Scope{Type: "team", ID: ag.TeamID, OrgID: ag.OrgID, TeamID: ag.TeamID}, "admin"); err != nil {
				return nil, err
			}
			k.AgentID = &ag.ID
		}
	case "pat":
		if c.P.User == nil {
			return nil, util.ErrForbidden
		}
		o, err := a.orgByRef(c, in.OrgID)
		if err != nil || !c.P.InOrg(o.ID) {
			return nil, badReq("org_id must be an organization you belong to")
		}
		k.OrgID = o.ID
		prefix = auth.PATPrefix
	default:
		return nil, badReq("kind must be gateway or pat")
	}
	applyKeyIn(k, &in)
	plain, display, hash := auth.GenerateKey(prefix)
	k.Prefix, k.Hash = display, hash
	if err := a.St.Insert(c, a.St.Pool, "api_keys", k); err != nil {
		return nil, err
	}
	c.Audit("key.create", k.OrgID, "api_key", k.ID, map[string]any{"name": k.Name, "kind": k.Kind, "project_id": k.ProjectID, "agent_id": k.AgentID})
	return created{map[string]any{"key": k, "secret": plain}}, nil
}

func applyKeyIn(k *store.APIKey, in *keyIn) {
	if in.Name != nil && *in.Name != "" {
		k.Name = *in.Name
	}
	if in.AllowedModels != nil {
		k.AllowedModels = *in.AllowedModels
	}
	if in.RPM != nil {
		k.RPM = max(*in.RPM, 0)
	}
	if in.TPM != nil {
		k.TPM = max(*in.TPM, 0)
	}
	if in.ExpiresInDays != nil {
		if *in.ExpiresInDays > 0 {
			t := time.Now().AddDate(0, 0, *in.ExpiresInDays)
			k.ExpiresAt = &t
		} else {
			k.ExpiresAt = nil
		}
	}
	if in.Metadata != nil {
		k.Metadata = *in.Metadata
	}
}

func (a *API) keyCtx(c *Ctx) (*store.APIKey, error) {
	k, err := store.GetByID[store.APIKey](c, a.St.Pool, "api_keys", c.Param("id"))
	if err != nil {
		return nil, err
	}
	if !canManageKey(c, k) {
		return nil, util.ErrForbidden
	}
	return k, nil
}

func (a *API) updateKey(c *Ctx) (any, error) {
	k, err := a.keyCtx(c)
	if err != nil {
		return nil, err
	}
	var in keyIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	applyKeyIn(k, &in)
	if err := a.St.Update(c, a.St.Pool, "api_keys", k); err != nil {
		return nil, err
	}
	a.Res.Invalidate()
	c.Audit("key.update", k.OrgID, "api_key", k.ID, map[string]any{"allowed_models": k.AllowedModels, "rpm": k.RPM, "tpm": k.TPM, "expires_at": k.ExpiresAt})
	return k, nil
}

func (a *API) revokeKey(c *Ctx) (any, error) {
	k, err := a.keyCtx(c)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	k.RevokedAt = &now
	if err := a.St.Update(c, a.St.Pool, "api_keys", k); err != nil {
		return nil, err
	}
	a.Res.Invalidate()
	c.Audit("key.revoke", k.OrgID, "api_key", k.ID, map[string]string{"name": k.Name})
	return k, nil
}

func (a *API) rotateKey(c *Ctx) (any, error) {
	k, err := a.keyCtx(c)
	if err != nil {
		return nil, err
	}
	if k.RevokedAt != nil {
		return nil, badReq("cannot rotate a revoked key")
	}
	prefix := auth.GatewayKeyPrefix
	if k.Kind == "pat" {
		prefix = auth.PATPrefix
	}
	plain, display, hash := auth.GenerateKey(prefix)
	k.Prefix, k.Hash = display, hash
	if err := a.St.Update(c, a.St.Pool, "api_keys", k); err != nil {
		return nil, err
	}
	a.Res.Invalidate()
	c.Audit("key.rotate", k.OrgID, "api_key", k.ID, map[string]string{"name": k.Name})
	return map[string]any{"key": k, "secret": plain}, nil
}
