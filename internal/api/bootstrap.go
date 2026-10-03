package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"

	"theophylax/internal/auth"
	"theophylax/internal/store"
	"theophylax/internal/util"
)

// Bootstrap creates the first platform admin, a default org/team/project and,
// optionally, a demo provider + model so the console works out of the box.
func (a *API) Bootstrap(ctx context.Context) error {
	var users int
	if err := a.St.Pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&users); err != nil {
		return err
	}
	if users > 0 || a.Cfg.BootstrapEmail == "" {
		if users == 0 {
			slog.Warn("no users exist: set THEOPHYLAX_BOOTSTRAP_ADMIN_EMAIL and THEOPHYLAX_BOOTSTRAP_ADMIN_PASSWORD to create the first admin")
		}
		return nil
	}
	if len(a.Cfg.BootstrapPassword) < 12 {
		slog.Warn("THEOPHYLAX_BOOTSTRAP_ADMIN_PASSWORD must be at least 12 characters; skipping bootstrap")
		return nil
	}
	h, err := auth.HashPassword(a.Cfg.BootstrapPassword)
	if err != nil {
		return err
	}
	u := &store.User{ID: util.NewID("usr"), Email: strings.ToLower(a.Cfg.BootstrapEmail), Name: "Administrator", PasswordHash: &h, IsPlatformAdmin: true, Status: "active"}
	slug := util.Slugify(a.Cfg.BootstrapOrg)
	if slug == "" {
		slug = "default"
	}
	org := &store.Org{ID: util.NewID("org"), Slug: slug, Name: a.Cfg.BootstrapOrg}
	team := &store.Team{ID: util.NewID("team"), OrgID: org.ID, Slug: "platform", Name: "Platform", Description: "Default team"}
	proj := &store.Project{ID: util.NewID("prj"), OrgID: org.ID, TeamID: team.ID, Slug: "sandbox", Name: "Sandbox", Description: "Experiments and playground traffic"}
	demo := os.Getenv("THEOPHYLAX_SEED_DEMO") == "true"
	err = a.St.Tx(ctx, func(tx pgx.Tx) error {
		for _, x := range []struct {
			t string
			v any
		}{{"users", u}, {"orgs", org}, {"teams", team}, {"projects", proj}} {
			if err := a.St.Insert(ctx, tx, x.t, x.v); err != nil {
				return err
			}
		}
		for _, m := range []*store.Membership{
			{ID: util.NewID("mem"), UserID: u.ID, OrgID: org.ID, ScopeType: "org", ScopeID: org.ID, Role: "owner", Source: "manual"},
			{ID: util.NewID("mem"), UserID: u.ID, OrgID: org.ID, ScopeType: "team", ScopeID: team.ID, Role: "owner", Source: "manual"},
		} {
			if err := a.St.Insert(ctx, tx, "memberships", m); err != nil {
				return err
			}
		}
		if !demo {
			return nil
		}
		prov := &store.Provider{ID: util.NewID("prv"), Name: "Demo (built-in mock)", Kind: "mock", ConnEnc: a.Box.SealString("mock://local?latency_ms=60"), ConnRedacted: "mock://local?latency_ms=60", Config: json.RawMessage("{}"), Enabled: true}
		if err := a.St.Insert(ctx, tx, "providers", prov); err != nil {
			return err
		}
		in, out := 0.5, 1.5
		m := &store.Model{ID: util.NewID("mdl"), Name: "demo-echo", DisplayName: "Demo Echo", Family: "demo", Aliases: []string{"demo"}, InputPrice: &in, OutputPrice: &out,
			ContextWindow: 200000, MaxOutput: 8192, Capabilities: []string{"streaming", "tools"}, Status: "active", Strategy: "priority", Source: "seed"}
		if err := a.St.Insert(ctx, tx, "models", m); err != nil {
			return err
		}
		if err := a.St.Insert(ctx, tx, "deployments", &store.Deployment{ID: util.NewID("dep"), ModelID: m.ID, ProviderID: prov.ID, UpstreamModel: "mock-echo", Weight: 1, Enabled: true}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Secret Shield is on by default for every new installation.
	rules, _ := json.Marshal([]map[string]any{{"type": "secrets", "action": "redact", "restore": true, "apply_to": "input"}})
	pii, _ := json.Marshal([]map[string]any{{"type": "pii", "action": "redact", "apply_to": "input", "entities": []string{"email", "credit_card", "us_ssn", "uk_nino", "iban"}}, {"type": "prompt_injection", "action": "block", "threshold": 0.6, "canary": true, "exfil_guard": true, "tool_guard": true}})
	err = a.St.Tx(ctx, func(tx pgx.Tx) error {
		if err := a.St.Insert(ctx, tx, "guardrail_policies", &store.GuardrailPolicy{ID: util.NewID("grd"), OrgID: org.ID, ScopeType: "org", ScopeID: org.ID, Name: "Secret Shield",
			Description: "Credentials are replaced with placeholders before any model sees them, and restored in responses.", Enabled: true, Mode: "enforce", Rules: rules}); err != nil {
			return err
		}
		if !demo {
			return nil
		}
		return a.St.Insert(ctx, tx, "guardrail_policies", &store.GuardrailPolicy{ID: util.NewID("grd"), OrgID: org.ID, ScopeType: "org", ScopeID: org.ID, Name: "Personal data protection",
			Description: "Redact personal data before it leaves the network; log prompt-injection attempts.", Enabled: true, Mode: "enforce", Rules: pii})
	})
	if err != nil {
		return err
	}
	if demo {
		if err := a.seedMarketplace(ctx, org.ID, u.ID); err != nil {
			slog.Warn("demo marketplace seed failed", "err", err)
		}
	}
	slog.Info("bootstrapped platform admin", "email", u.Email, "org", org.Slug, "demo", demo)
	a.changed(ctx)
	return nil
}

func (a *API) seedMarketplace(ctx context.Context, orgID, userID string) error {
	items := []struct {
		kind, slug, name, desc string
		tags                   []string
		content                map[string]any
	}{
		{"skill", "conventional-commits", "Conventional commits", "Write commit messages and PR titles in the Conventional Commits format your release tooling expects.", []string{"git", "workflow"},
			map[string]any{"skill_md": "---\nname: conventional-commits\ndescription: Use when writing commit messages, PR titles or changelogs.\n---\n\n# Conventional commits\n\n## Format\n`<type>(<scope>): <summary>` in the imperative mood, under 72 characters.\n\n## Types\n- **feat**: a user-visible feature\n- **fix**: a bug fix\n- **docs**, **refactor**, **test**, **chore**, **ci**\n\n## Rules\n1. Breaking changes add `!` after the type and a `BREAKING CHANGE:` footer.\n2. Reference the ticket in the footer, e.g. `Refs: PLAT-123`.\n"}},
		{"skill", "terraform-plan-review", "Terraform plan review", "A checklist for reviewing terraform plan output: destructive changes, IAM widening, public exposure and drift.", []string{"terraform", "security", "review"},
			map[string]any{"skill_md": "---\nname: terraform-plan-review\ndescription: Use when asked to review terraform plan output or infrastructure diffs.\n---\n\n# Reviewing a terraform plan\n\n1. List every resource being **destroyed or replaced** first.\n2. Flag IAM policies that add `*` actions or resources.\n3. Flag security groups or buckets that become public.\n4. Call out changes to state backends, KMS keys or DNS.\n5. Finish with a one-line recommendation: approve, approve with changes, or block.\n"}},
		{"agent", "incident-summarizer", "Incident summarizer", "Turns a noisy incident channel export into a timeline, impact statement and follow-ups.", []string{"sre", "incidents"},
			map[string]any{"model": "demo-echo", "system_prompt": "You summarise incidents for an engineering audience. Produce: a timeline with UTC timestamps, customer impact, root cause (or best current hypothesis), and follow-up actions with owners. Never speculate beyond the evidence.", "params": map[string]any{"max_tokens": 2048}}},
		{"prompt", "pr-description", "Pull request description", "A template that produces consistent, reviewer-friendly PR descriptions.", []string{"git", "review"},
			map[string]any{"template": "Write a pull request description for the diff below.\n\n## What changed\n## Why\n## How to test\n## Risk and rollback\n\nDiff:\n{{diff}}"}},
	}
	for _, it := range items {
		raw, _ := json.Marshal(it.content)
		sum := sha256.Sum256(raw)
		item := &store.RegistryItem{ID: util.NewID("reg"), OrgID: orgID, Kind: it.kind, Slug: it.slug, Name: it.name, Description: it.desc, Publisher: "Platform", Tags: it.tags,
			Visibility: "org", Status: "approved", LatestVersion: "1.0.0", ApprovedVersion: "1.0.0", CreatedBy: &userID, ReviewedBy: &userID}
		if err := a.St.Insert(ctx, a.St.Pool, "registry_items", item); err != nil {
			return err
		}
		if err := a.St.Insert(ctx, a.St.Pool, "registry_versions", &store.RegistryVersion{ID: util.NewID("rv"), ItemID: item.ID, Version: "1.0.0", Content: raw,
			Digest: "sha256:" + hex.EncodeToString(sum[:]), Changelog: "Initial version", CreatedBy: &userID}); err != nil {
			return err
		}
	}
	return nil
}
