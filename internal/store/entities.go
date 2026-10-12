package store

import (
	"encoding/json"
	"time"
)

type Org struct {
	ID        string          `db:"id" json:"id"`
	Slug      string          `db:"slug" json:"slug"`
	Name      string          `db:"name" json:"name"`
	Settings  json.RawMessage `db:"settings" json:"settings"`
	CreatedAt time.Time       `db:"created_at" json:"created_at"`
}

// OrgSettings is the typed view of orgs.settings / teams.settings / projects.settings.
type ScopeSettings struct {
	AllowedModels []string `json:"allowed_models,omitempty"`
	LogPayloads   *bool    `json:"log_payloads,omitempty"`
	RPM           int      `json:"rpm,omitempty"`
	TPM           int      `json:"tpm,omitempty"`
}

type User struct {
	ID              string     `db:"id" json:"id"`
	Email           string     `db:"email" json:"email"`
	Name            string     `db:"name" json:"name"`
	PasswordHash    *string    `db:"password_hash" json:"-"`
	IsPlatformAdmin bool       `db:"is_platform_admin" json:"is_platform_admin"`
	Status          string     `db:"status" json:"status"`
	IdpID           *string    `db:"idp_id" json:"idp_id"`
	ExternalID      *string    `db:"external_id" json:"external_id"`
	LastLoginAt     *time.Time `db:"last_login_at" json:"last_login_at"`
	CreatedAt       time.Time  `db:"created_at" json:"created_at"`
}

type Team struct {
	ID          string          `db:"id" json:"id"`
	OrgID       string          `db:"org_id" json:"org_id"`
	Slug        string          `db:"slug" json:"slug"`
	Name        string          `db:"name" json:"name"`
	Description string          `db:"description" json:"description"`
	Settings    json.RawMessage `db:"settings" json:"settings"`
	ExternalID  *string         `db:"external_id" json:"external_id"`
	CreatedAt   time.Time       `db:"created_at" json:"created_at"`
}

type Project struct {
	ID          string          `db:"id" json:"id"`
	OrgID       string          `db:"org_id" json:"org_id"`
	TeamID      string          `db:"team_id" json:"team_id"`
	Slug        string          `db:"slug" json:"slug"`
	Name        string          `db:"name" json:"name"`
	Description string          `db:"description" json:"description"`
	Settings    json.RawMessage `db:"settings" json:"settings"`
	CreatedAt   time.Time       `db:"created_at" json:"created_at"`
}

type Membership struct {
	ID        string    `db:"id" json:"id"`
	UserID    string    `db:"user_id" json:"user_id"`
	OrgID     string    `db:"org_id" json:"org_id"`
	ScopeType string    `db:"scope_type" json:"scope_type"`
	ScopeID   string    `db:"scope_id" json:"scope_id"`
	Role      string    `db:"role" json:"role"`
	Source    string    `db:"source" json:"source"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

type Agent struct {
	ID                string          `db:"id" json:"id"`
	OrgID             string          `db:"org_id" json:"org_id"`
	TeamID            string          `db:"team_id" json:"team_id"`
	ProjectID         *string         `db:"project_id" json:"project_id"`
	Slug              string          `db:"slug" json:"slug"`
	Name              string          `db:"name" json:"name"`
	Description       string          `db:"description" json:"description"`
	Model             string          `db:"model" json:"model"`
	SystemPrompt      string          `db:"system_prompt" json:"system_prompt"`
	Skills            []string        `db:"skills" json:"skills"`
	MCPServers        []string        `db:"mcp_servers" json:"mcp_servers"`
	Tools             json.RawMessage `db:"tools" json:"tools"`
	Params            json.RawMessage `db:"params" json:"params"`
	AllowClientSystem bool            `db:"allow_client_system" json:"allow_client_system"`
	OwnerUserID       *string         `db:"owner_user_id" json:"owner_user_id"`
	Status            string          `db:"status" json:"status"`
	CreatedAt         time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt         time.Time       `db:"updated_at" json:"updated_at"`
}

type APIKey struct {
	ID            string          `db:"id" json:"id"`
	Kind          string          `db:"kind" json:"kind"`
	OrgID         string          `db:"org_id" json:"org_id"`
	TeamID        *string         `db:"team_id" json:"team_id"`
	ProjectID     *string         `db:"project_id" json:"project_id"`
	UserID        *string         `db:"user_id" json:"user_id"`
	AgentID       *string         `db:"agent_id" json:"agent_id"`
	Name          string          `db:"name" json:"name"`
	Prefix        string          `db:"prefix" json:"prefix"`
	Hash          []byte          `db:"hash" json:"-"`
	AllowedModels []string        `db:"allowed_models" json:"allowed_models"`
	RPM           int             `db:"rpm" json:"rpm"`
	TPM           int             `db:"tpm" json:"tpm"`
	ExpiresAt     *time.Time      `db:"expires_at" json:"expires_at"`
	RevokedAt     *time.Time      `db:"revoked_at" json:"revoked_at"`
	LastUsedAt    *time.Time      `db:"last_used_at" json:"last_used_at"`
	CreatedBy     *string         `db:"created_by" json:"created_by"`
	Metadata      json.RawMessage `db:"metadata" json:"metadata"`
	CreatedAt     time.Time       `db:"created_at" json:"created_at"`
}

type Provider struct {
	ID           string          `db:"id" json:"id"`
	OrgID        *string         `db:"org_id" json:"org_id"`
	Name         string          `db:"name" json:"name"`
	Kind         string          `db:"kind" json:"kind"`
	ConnEnc      []byte          `db:"conn_enc" json:"-"`
	ConnRedacted string          `db:"conn_redacted" json:"connection_string"`
	Config       json.RawMessage `db:"config" json:"config"`
	Enabled      bool            `db:"enabled" json:"enabled"`
	CreatedAt    time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt    time.Time       `db:"updated_at" json:"updated_at"`
}

type Model struct {
	ID            string    `db:"id" json:"id"`
	Name          string    `db:"name" json:"name"`
	DisplayName   string    `db:"display_name" json:"display_name"`
	Family        string    `db:"family" json:"family"`
	Aliases       []string  `db:"aliases" json:"aliases"`
	ContextWindow int       `db:"context_window" json:"context_window"`
	MaxOutput     int       `db:"max_output" json:"max_output"`
	Capabilities  []string  `db:"capabilities" json:"capabilities"`
	Status        string    `db:"status" json:"status"`
	Strategy      string    `db:"strategy" json:"strategy"`
	Source        string    `db:"source" json:"source"`
	CreatedAt     time.Time `db:"created_at" json:"created_at"`
	UpdatedAt     time.Time `db:"updated_at" json:"updated_at"`
}

type Deployment struct {
	ID            string    `db:"id" json:"id"`
	ModelID       string    `db:"model_id" json:"model_id"`
	ProviderID    string    `db:"provider_id" json:"provider_id"`
	UpstreamModel string    `db:"upstream_model" json:"upstream_model"`
	Weight        int       `db:"weight" json:"weight"`
	Priority      int       `db:"priority" json:"priority"`
	Enabled       bool      `db:"enabled" json:"enabled"`
	CreatedAt     time.Time `db:"created_at" json:"created_at"`
}

type GuardrailPolicy struct {
	ID          string          `db:"id" json:"id"`
	OrgID       string          `db:"org_id" json:"org_id"`
	ScopeType   string          `db:"scope_type" json:"scope_type"`
	ScopeID     string          `db:"scope_id" json:"scope_id"`
	Name        string          `db:"name" json:"name"`
	Description string          `db:"description" json:"description"`
	Enabled     bool            `db:"enabled" json:"enabled"`
	Mode        string          `db:"mode" json:"mode"`
	Rules       json.RawMessage `db:"rules" json:"rules"`
	CreatedAt   time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt   time.Time       `db:"updated_at" json:"updated_at"`
}

type Budget struct {
	ID          string   `db:"id" json:"id"`
	OrgID       string   `db:"org_id" json:"org_id"`
	ScopeType   string   `db:"scope_type" json:"scope_type"`
	ScopeID     string   `db:"scope_id" json:"scope_id"`
	Period      string   `db:"period" json:"period"`
	LimitUSD    *float64 `db:"limit_usd" json:"limit_usd"`
	LimitTokens *int64   `db:"limit_tokens" json:"limit_tokens"`
	// PerUser makes an org or team budget a default limit for each person in
	// it rather than a pooled cap.
	PerUser   bool      `db:"per_user" json:"per_user"`
	AlertPct  int       `db:"alert_pct" json:"alert_pct"`
	Action    string    `db:"action" json:"action"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

type IdP struct {
	ID            string          `db:"id" json:"id"`
	OrgID         string          `db:"org_id" json:"org_id"`
	Name          string          `db:"name" json:"name"`
	Type          string          `db:"type" json:"type"`
	Issuer        string          `db:"issuer" json:"issuer"`
	ClientID      string          `db:"client_id" json:"client_id"`
	SecretEnc     []byte          `db:"secret_enc" json:"-"`
	Scopes        []string        `db:"scopes" json:"scopes"`
	GroupsClaim   string          `db:"groups_claim" json:"groups_claim"`
	EmailDomains  []string        `db:"email_domains" json:"email_domains"`
	JIT           bool            `db:"jit" json:"jit"`
	DefaultRole   string          `db:"default_role" json:"default_role"`
	RoleMappings  json.RawMessage `db:"role_mappings" json:"role_mappings"`
	GatewayTokens bool            `db:"gateway_tokens" json:"gateway_tokens"`
	Audience      string          `db:"audience" json:"audience"`
	SCIMTokenHash []byte          `db:"scim_token_hash" json:"-"`
	Enabled       bool            `db:"enabled" json:"enabled"`
	CreatedAt     time.Time       `db:"created_at" json:"created_at"`
}

// RoleMapping maps an IdP group (claim value) to a role at a scope.
type RoleMapping struct {
	Group     string `json:"group"`
	ScopeType string `json:"scope_type"`
	ScopeID   string `json:"scope_id"`
	Role      string `json:"role"`
}

type RegistryItem struct {
	ID              string    `db:"id" json:"id"`
	OrgID           string    `db:"org_id" json:"org_id"`
	Kind            string    `db:"kind" json:"kind"`
	Slug            string    `db:"slug" json:"slug"`
	Name            string    `db:"name" json:"name"`
	Description     string    `db:"description" json:"description"`
	Publisher       string    `db:"publisher" json:"publisher"`
	OwnerTeamID     *string   `db:"owner_team_id" json:"owner_team_id"`
	Tags            []string  `db:"tags" json:"tags"`
	Visibility      string    `db:"visibility" json:"visibility"`
	Status          string    `db:"status" json:"status"`
	LatestVersion   string    `db:"latest_version" json:"latest_version"`
	ApprovedVersion string    `db:"approved_version" json:"approved_version"`
	Installs        int       `db:"installs" json:"installs"`
	RatingSum       int       `db:"rating_sum" json:"rating_sum"`
	RatingCount     int       `db:"rating_count" json:"rating_count"`
	CreatedBy       *string   `db:"created_by" json:"created_by"`
	ReviewedBy      *string   `db:"reviewed_by" json:"reviewed_by"`
	ReviewNote      string    `db:"review_note" json:"review_note"`
	CreatedAt       time.Time `db:"created_at" json:"created_at"`
	UpdatedAt       time.Time `db:"updated_at" json:"updated_at"`
}

type RegistryVersion struct {
	ID        string          `db:"id" json:"id"`
	ItemID    string          `db:"item_id" json:"item_id"`
	Version   string          `db:"version" json:"version"`
	Content   json.RawMessage `db:"content" json:"content"`
	Digest    string          `db:"digest" json:"digest"`
	Changelog string          `db:"changelog" json:"changelog"`
	CreatedBy *string         `db:"created_by" json:"created_by"`
	CreatedAt time.Time       `db:"created_at" json:"created_at"`
	// ApprovedAt is set when this version is approved; only approved
	// versions can be pinned by an installation.
	ApprovedAt *time.Time `db:"approved_at" json:"approved_at"`
	ApprovedBy *string    `db:"approved_by" json:"approved_by"`
}

type Installation struct {
	ID      string `db:"id" json:"id"`
	ItemID  string `db:"item_id" json:"item_id"`
	OrgID   string `db:"org_id" json:"org_id"`
	Version string `db:"version" json:"version"`
	// Pinned installations serve Version; the others follow the item's
	// approved version (Version then records what was approved at install).
	Pinned      bool      `db:"pinned" json:"pinned"`
	ScopeType   string    `db:"scope_type" json:"scope_type"`
	ScopeID     string    `db:"scope_id" json:"scope_id"`
	InstalledBy *string   `db:"installed_by" json:"installed_by"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
}

type RequestLog struct {
	ID               string          `db:"id" json:"id"`
	TS               time.Time       `db:"ts" json:"ts"`
	OrgID            *string         `db:"org_id" json:"org_id"`
	TeamID           *string         `db:"team_id" json:"team_id"`
	ProjectID        *string         `db:"project_id" json:"project_id"`
	UserID           *string         `db:"user_id" json:"user_id"`
	KeyID            *string         `db:"key_id" json:"key_id"`
	AgentID          *string         `db:"agent_id" json:"agent_id"`
	Endpoint         string          `db:"endpoint" json:"endpoint"`
	Model            string          `db:"model" json:"model"`
	UpstreamModel    string          `db:"upstream_model" json:"upstream_model"`
	ProviderID       *string         `db:"provider_id" json:"provider_id"`
	ProviderKind     string          `db:"provider_kind" json:"provider_kind"`
	Status           int             `db:"status" json:"status"`
	Error            string          `db:"error" json:"error"`
	Stream           bool            `db:"stream" json:"stream"`
	LatencyMS        int             `db:"latency_ms" json:"latency_ms"`
	TTFBMS           int             `db:"ttfb_ms" json:"ttfb_ms"`
	InputTokens      int             `db:"input_tokens" json:"input_tokens"`
	OutputTokens     int             `db:"output_tokens" json:"output_tokens"`
	CacheReadTokens  int             `db:"cache_read_tokens" json:"cache_read_tokens"`
	CacheWriteTokens int             `db:"cache_write_tokens" json:"cache_write_tokens"`
	CostUSD          float64         `db:"cost_usd" json:"cost_usd"`
	PriceKey         string          `db:"price_key" json:"price_key"`
	Attempts         int             `db:"attempts" json:"attempts"`
	Guardrails       json.RawMessage `db:"guardrails" json:"guardrails"`
	ClientIP         string          `db:"client_ip" json:"client_ip"`
	UserAgent        string          `db:"user_agent" json:"user_agent"`
	Tags             json.RawMessage `db:"tags" json:"tags"`
}

type AuditEntry struct {
	Seq        int64           `db:"seq" json:"seq"`
	ID         string          `db:"id" json:"id"`
	TS         time.Time       `db:"ts" json:"ts"`
	ActorType  string          `db:"actor_type" json:"actor_type"`
	ActorID    string          `db:"actor_id" json:"actor_id"`
	ActorEmail string          `db:"actor_email" json:"actor_email"`
	OrgID      *string         `db:"org_id" json:"org_id"`
	Action     string          `db:"action" json:"action"`
	TargetType string          `db:"target_type" json:"target_type"`
	TargetID   string          `db:"target_id" json:"target_id"`
	Details    json.RawMessage `db:"details" json:"details"`
	IP         string          `db:"ip" json:"ip"`
	UserAgent  string          `db:"user_agent" json:"user_agent"`
	PrevHash   []byte          `db:"prev_hash" json:"-"`
	Hash       []byte          `db:"hash" json:"hash"`
}
