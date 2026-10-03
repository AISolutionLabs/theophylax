package api

import (
	"net/http"
	"time"

	"theophylax/internal/gateway"
	"theophylax/internal/store"
	"theophylax/internal/util"
)

func (a *API) rolloutRoutes(mux *http.ServeMux) {
	h := a.handle
	mux.HandleFunc("GET /api/rollouts", h(a.listRollouts))
	mux.HandleFunc("POST /api/rollouts", h(a.createRollout))
	mux.HandleFunc("GET /api/rollouts/{id}", h(a.getRollout))
	mux.HandleFunc("PATCH /api/rollouts/{id}", h(a.updateRollout))
	mux.HandleFunc("POST /api/rollouts/{id}/{action}", h(a.rolloutAction))
}

type rolloutView struct {
	gateway.Rollout
	ModelName     string `json:"model_name" db:"model_name"`
	ProviderName  string `json:"provider_name" db:"provider_name"`
	IncumbentDesc string `json:"incumbent" db:"incumbent"`
}

const rolloutSelect = `SELECT r.*, m.name AS model_name, p.name AS provider_name,
	COALESCE((SELECT string_agg(pp.name || ' · ' || d.upstream_model, ', ' ORDER BY d.priority) FROM deployments d JOIN providers pp ON pp.id=d.provider_id WHERE d.model_id=r.model_id AND d.enabled), '') AS incumbent
	FROM model_rollouts r JOIN models m ON m.id=r.model_id JOIN providers p ON p.id=r.candidate_provider_id`

func (a *API) listRollouts(c *Ctx) (any, error) {
	return store.List[rolloutView](c, a.St.Pool, rolloutSelect+` ORDER BY CASE WHEN r.stage IN ('shadow','canary','paused') THEN 0 ELSE 1 END, r.created_at DESC LIMIT 200`)
}

func (a *API) getRollout(c *Ctx) (any, error) {
	r, err := store.Get[rolloutView](c, a.St.Pool, rolloutSelect+` WHERE r.id=$1`, c.Param("id"))
	if err != nil {
		return nil, err
	}
	type sample struct {
		ID            string    `json:"id" db:"id"`
		TS            time.Time `json:"ts" db:"ts"`
		RequestID     string    `json:"request_id" db:"request_id"`
		Verdict       string    `json:"verdict" db:"verdict"`
		Reason        string    `json:"reason" db:"reason"`
		CandLatencyMS int       `json:"cand_latency_ms" db:"cand_latency_ms"`
		IncLatencyMS  int       `json:"inc_latency_ms" db:"inc_latency_ms"`
		CandCost      float64   `json:"cand_cost" db:"cand_cost"`
		IncCost       float64   `json:"inc_cost" db:"inc_cost"`
	}
	samples, err := store.List[sample](c, a.St.Pool, `SELECT id, ts, request_id, verdict, reason, cand_latency_ms, inc_latency_ms, cand_cost, inc_cost FROM rollout_samples WHERE rollout_id=$1 ORDER BY ts DESC LIMIT 100`, r.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"rollout": r, "samples": samples}, nil
}

type rolloutIn struct {
	ModelID             *string  `json:"model_id"`
	Name                *string  `json:"name"`
	CandidateProviderID *string  `json:"candidate_provider_id"`
	CandidateUpstream   *string  `json:"candidate_upstream"`
	JudgeModel          *string  `json:"judge_model"`
	ShadowPct           *int     `json:"shadow_pct"`
	CanaryPct           *int     `json:"canary_pct"`
	MinSamples          *int     `json:"min_samples"`
	WinThreshold        *float64 `json:"win_threshold"`
	MaxErrorRate        *float64 `json:"max_error_rate"`
	MaxLatencyRatio     *float64 `json:"max_latency_ratio"`
	AutoPromote         *bool    `json:"auto_promote"`
	AutoRollback        *bool    `json:"auto_rollback"`
	SkipShadow          bool     `json:"skip_shadow"`
}

func applyRolloutIn(r *gateway.Rollout, in *rolloutIn) {
	if in.Name != nil {
		r.Name = *in.Name
	}
	if in.JudgeModel != nil {
		r.JudgeModel = *in.JudgeModel
	}
	if in.ShadowPct != nil {
		r.ShadowPct = min(max(*in.ShadowPct, 0), 100)
	}
	if in.CanaryPct != nil {
		r.CanaryPct = min(max(*in.CanaryPct, 0), 100)
	}
	if in.MinSamples != nil {
		r.MinSamples = max(*in.MinSamples, 1)
	}
	if in.WinThreshold != nil {
		r.WinThreshold = *in.WinThreshold
	}
	if in.MaxErrorRate != nil {
		r.MaxErrorRate = *in.MaxErrorRate
	}
	if in.MaxLatencyRatio != nil {
		r.MaxLatencyRatio = *in.MaxLatencyRatio
	}
	if in.AutoPromote != nil {
		r.AutoPromote = *in.AutoPromote
	}
	if in.AutoRollback != nil {
		r.AutoRollback = *in.AutoRollback
	}
}

func (a *API) createRollout(c *Ctx) (any, error) {
	if err := c.RequirePlatformAdmin(); err != nil {
		return nil, err
	}
	var in rolloutIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	if util.Deref(in.ModelID) == "" || util.Deref(in.CandidateProviderID) == "" || util.Deref(in.CandidateUpstream) == "" {
		return nil, badReq("model_id, candidate_provider_id and candidate_upstream are required")
	}
	uid := c.P.UserID()
	r := &gateway.Rollout{ID: util.NewID("ro"), ModelID: *in.ModelID, CandidateProviderID: *in.CandidateProviderID, CandidateUpstream: *in.CandidateUpstream,
		Stage: "shadow", ShadowPct: 10, CanaryPct: 10, MinSamples: 50, WinThreshold: 0.5, MaxErrorRate: 0.05, MaxLatencyRatio: 1.5, AutoPromote: true, AutoRollback: true, CreatedBy: &uid}
	applyRolloutIn(r, &in)
	if in.SkipShadow {
		r.Stage = "canary"
	} else if r.JudgeModel == "" {
		return nil, badReq("a judge model is required for the shadow stage (or choose to skip straight to canary)")
	}
	if err := a.St.Insert(c, a.St.Pool, "model_rollouts", r); err != nil {
		return nil, err
	}
	c.Audit("rollout.create", "", "rollout", r.ID, r)
	a.changed(c)
	return created{r}, nil
}

func (a *API) updateRollout(c *Ctx) (any, error) {
	if err := c.RequirePlatformAdmin(); err != nil {
		return nil, err
	}
	r, err := store.GetByID[gateway.Rollout](c, a.St.Pool, "model_rollouts", c.Param("id"))
	if err != nil {
		return nil, err
	}
	var in rolloutIn
	if err := c.Decode(&in); err != nil {
		return nil, err
	}
	applyRolloutIn(r, &in)
	if err := a.St.Update(c, a.St.Pool, "model_rollouts", r); err != nil {
		return nil, err
	}
	c.Audit("rollout.update", "", "rollout", r.ID, r)
	a.changed(c)
	return r, nil
}

func (a *API) rolloutAction(c *Ctx) (any, error) {
	if err := c.RequirePlatformAdmin(); err != nil {
		return nil, err
	}
	id, action := c.Param("id"), c.Param("action")
	r, err := store.GetByID[gateway.Rollout](c, a.St.Pool, "model_rollouts", id)
	if err != nil {
		return nil, err
	}
	who := "by " + c.P.Email()
	set := func(stage, paused, note string) error {
		_, err := a.St.Pool.Exec(c, `UPDATE model_rollouts SET stage=$2, paused_stage=$3, decision_note=$4, decided_at=CASE WHEN $2 IN ('promoted','rolled_back') THEN now() ELSE decided_at END, updated_at=now() WHERE id=$1`, id, stage, paused, note)
		return err
	}
	switch action {
	case "pause":
		if r.Stage != "shadow" && r.Stage != "canary" {
			return nil, badReq("only an active rollout can be paused")
		}
		err = set("paused", r.Stage, "paused "+who)
	case "resume":
		if r.Stage != "paused" {
			return nil, badReq("rollout is not paused")
		}
		err = set(r.PausedStage, "", "resumed "+who)
	case "advance":
		if r.Stage != "shadow" {
			return nil, badReq("only a shadow rollout can advance to canary")
		}
		err = set("canary", "", "advanced to canary "+who)
	case "promote":
		err = gateway.PromoteRollout(c, a.St, a.Audit, id, c.actor(), "promoted manually "+who)
	case "rollback":
		if r.Stage == "promoted" || r.Stage == "rolled_back" {
			return nil, badReq("rollout already finished")
		}
		err = set("rolled_back", "", "rolled back manually "+who)
	default:
		return nil, util.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if action != "promote" {
		c.Audit("rollout."+action, "", "rollout", id, nil)
	}
	a.changed(c)
	return map[string]any{"ok": true}, nil
}
