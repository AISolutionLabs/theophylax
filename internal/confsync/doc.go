// Package confsync implements configuration as code: it exports a gateway's
// configuration as a YAML document and applies such a document back through
// the gateway's own admin API.
//
// Working through the public API (with a personal access token) rather than
// the database means every change goes through the same validation,
// permission checks, secret encryption and audit log as a change made in the
// console, and the tool works against any gateway the token can reach.
package confsync

import (
	"bytes"
	"encoding/json"
	"fmt"

	"theophylax/internal/migrate"
)

// Version is the document format version written by Export.
const Version = 1

// Doc is a configuration document. A list that is left out (nil) is not
// managed: Apply leaves whatever the gateway has there untouched, while an
// empty list ([]) means "none". Within an item, an empty string or a missing
// number, boolean or object also means "leave as is".
type Doc struct {
	Version       int        `json:"version"`
	Organizations []Org      `json:"organizations"`
	Providers     []Provider `json:"providers"`
	Models        []Model    `json:"models"`
}

type Org struct {
	Slug       string          `json:"slug"`
	Name       string          `json:"name,omitempty"`
	Settings   json.RawMessage `json:"settings,omitempty"`
	Teams      []Team          `json:"teams"`
	Guardrails []Guardrail     `json:"guardrails"`
	Budgets    []Budget        `json:"budgets"`
}

type Team struct {
	Slug        string          `json:"slug"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Settings    json.RawMessage `json:"settings,omitempty"`
	Projects    []Project       `json:"projects"`
}

type Project struct {
	Slug        string          `json:"slug"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Settings    json.RawMessage `json:"settings,omitempty"`
}

// Provider is an upstream connection. ConnectionString is exported in its
// masked form (secret values shown as ****1234; ${env:…} and ${file:…}
// references in full). Applying the masked form unchanged keeps the stored
// secret, so an exported document can be applied as is. Prefer references
// over literal secrets in documents kept in version control.
type Provider struct {
	Name             string          `json:"name"`
	Organization     string          `json:"organization,omitempty"`
	ConnectionString string          `json:"connection_string,omitempty"`
	Config           json.RawMessage `json:"config,omitempty"`
	Enabled          *bool           `json:"enabled,omitempty"`
}

// ref is how deployments name a provider: its name, or "org/name" for a
// provider owned by an organization.
func (p Provider) ref() string {
	if p.Organization != "" {
		return p.Organization + "/" + p.Name
	}
	return p.Name
}

type Model struct {
	Name          string       `json:"name"`
	DisplayName   string       `json:"display_name,omitempty"`
	Family        string       `json:"family,omitempty"`
	Aliases       []string     `json:"aliases"`
	ContextWindow *int         `json:"context_window,omitempty"`
	MaxOutput     *int         `json:"max_output,omitempty"`
	Capabilities  []string     `json:"capabilities"`
	Status        string       `json:"status,omitempty"`
	Strategy      string       `json:"strategy,omitempty"`
	Deployments   []Deployment `json:"deployments"`
}

type Deployment struct {
	Provider      string `json:"provider"`
	UpstreamModel string `json:"upstream_model"`
	Weight        *int   `json:"weight,omitempty"`
	Priority      *int   `json:"priority,omitempty"`
	Enabled       *bool  `json:"enabled,omitempty"`
}

// Guardrail is identified by its name within the organization.
type Guardrail struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Scope       string          `json:"scope,omitempty"`
	Mode        string          `json:"mode,omitempty"`
	Enabled     *bool           `json:"enabled,omitempty"`
	Rules       json.RawMessage `json:"rules,omitempty"`
}

// Budget is identified by (scope, period, per_user). Its limits are taken as
// written: a limit left out is removed.
type Budget struct {
	Scope       string   `json:"scope"`
	Period      string   `json:"period,omitempty"`
	PerUser     bool     `json:"per_user,omitempty"`
	LimitUSD    *float64 `json:"limit_usd,omitempty"`
	LimitTokens *int64   `json:"limit_tokens,omitempty"`
	AlertPct    *int     `json:"alert_pct,omitempty"`
	Action      string   `json:"action,omitempty"`
}

// Parse reads a document from YAML (or JSON, which is accepted as is).
// Unknown keys are errors, so typos do not silently do nothing.
func Parse(src []byte) (*Doc, error) {
	var raw []byte
	if t := bytes.TrimSpace(src); len(t) > 0 && t[0] == '{' {
		raw = t
	} else {
		v, err := migrate.ParseYAML(string(src))
		if err != nil {
			return nil, err
		}
		if raw, err = json.Marshal(v); err != nil {
			return nil, err
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var d Doc
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("invalid configuration document: %w", err)
	}
	if d.Version != Version {
		return nil, fmt.Errorf("unsupported document version %d (this build reads version %d)", d.Version, Version)
	}
	return &d, nil
}
