package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// LiteLLMData is a snapshot of a LiteLLM proxy database. Rows are read as
// JSON so the importer tolerates the column drift between LiteLLM versions.
type LiteLLMData struct {
	Orgs        []map[string]any
	Teams       []map[string]any
	Users       []map[string]any
	Keys        []map[string]any
	Budgets     map[string]map[string]any // by budget_id
	OrgMembers  []map[string]any
	ProxyModels []map[string]any // models stored in the DB (store_model_in_db)
}

// ReadLiteLLM reads everything the importer needs. Missing tables (older
// LiteLLM versions) are skipped.
func ReadLiteLLM(ctx context.Context, dsn string) (*LiteLLMData, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect to LiteLLM database: %w", err)
	}
	defer conn.Close(ctx)
	read := func(table string) ([]map[string]any, error) {
		var exists bool
		if err := conn.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, `"`+table+`"`).Scan(&exists); err != nil || !exists {
			return nil, err
		}
		rows, err := conn.Query(ctx, `SELECT to_jsonb(t)::text FROM "`+table+`" t`)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", table, err)
		}
		defer rows.Close()
		var out []map[string]any
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return nil, err
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(s), &m); err != nil {
				return nil, err
			}
			out = append(out, m)
		}
		return out, rows.Err()
	}
	d := &LiteLLMData{Budgets: map[string]map[string]any{}}
	for _, x := range []struct {
		table string
		dst   *[]map[string]any
	}{
		{"LiteLLM_OrganizationTable", &d.Orgs}, {"LiteLLM_TeamTable", &d.Teams}, {"LiteLLM_UserTable", &d.Users},
		{"LiteLLM_VerificationToken", &d.Keys}, {"LiteLLM_OrganizationMembership", &d.OrgMembers}, {"LiteLLM_ProxyModelTable", &d.ProxyModels},
	} {
		if *x.dst, err = read(x.table); err != nil {
			return nil, err
		}
	}
	budgets, err := read("LiteLLM_BudgetTable")
	if err != nil {
		return nil, err
	}
	for _, b := range budgets {
		d.Budgets[str(b["budget_id"])] = b
	}
	return d, nil
}

// AddDBModels maps LiteLLM_ProxyModelTable rows into the plan. LiteLLM
// encrypts every litellm_params value in its database; saltKey
// (LITELLM_SALT_KEY, or the LiteLLM master key if no salt key was set)
// decrypts them. Without it, DB-stored models cannot be read.
func (p *Plan) AddDBModels(rows []map[string]any, saltKey string) {
	if len(rows) == 0 {
		return
	}
	b := newBuilder(p)
	for i := range p.Providers {
		pp := p.Providers[i]
		b.prov[pp.Conn] = &pp
	}
	existing := map[string]bool{}
	for _, m := range p.Models {
		existing[m.Name] = true
	}
	skippedNoKey := 0
	for _, r := range rows {
		if r["blocked"] == true {
			continue
		}
		name := str(r["model_name"])
		if existing[name] {
			p.warn("%s is defined in both config.yaml and the database; the config.yaml definition is used", name)
			continue
		}
		lp := asMap(r["litellm_params"])
		if lp == nil {
			var m map[string]any
			_ = json.Unmarshal([]byte(str(r["litellm_params"])), &m)
			lp = m
		}
		if lp == nil {
			continue
		}
		ok := true
		for k, v := range lp {
			s, isStr := v.(string)
			if !isStr || s == "" || strings.HasPrefix(s, "os.environ/") || (k == "model" && strings.Contains(s, "/")) {
				continue
			}
			if saltKey == "" {
				ok = false
				break
			}
			if plain, err := DecryptLiteLLM(s, saltKey); err == nil {
				lp[k] = plain
			} else if k == "model" {
				ok = false
				break
			}
		}
		if !ok {
			skippedNoKey++
			continue
		}
		b.add(name, lp, asMap(r["model_info"]))
	}
	if skippedNoKey > 0 {
		p.warn("%d model(s) stored in the LiteLLM database were skipped: their settings are encrypted; pass -litellm-salt-key (LITELLM_SALT_KEY, or the LiteLLM master key if no salt key was set)", skippedNoKey)
	}
	b.finish("weighted", nil)
}
