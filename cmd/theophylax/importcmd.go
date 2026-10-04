package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"theophylax/internal/config"
	"theophylax/internal/db"
	"theophylax/internal/migrate"
	"theophylax/internal/secrets"
	"theophylax/internal/store"
)

// importLiteLLMCLI migrates a LiteLLM proxy:
//
//	theophylax import-litellm -config litellm.yaml -dry-run
//	theophylax import-litellm -config litellm.yaml -litellm-db postgres://… -litellm-salt-key "$LITELLM_SALT_KEY"
//
// It writes into the gateway database named by THEOPHYLAX_DATABASE_URL using
// THEOPHYLAX_MASTER_KEY (the same environment the gateway runs with).
func importLiteLLMCLI(args []string) int {
	fs := flag.NewFlagSet("import-litellm", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "LiteLLM config.yaml (model_list, router_settings)")
	dsn := fs.String("litellm-db", "", "LiteLLM Postgres URL (orgs, teams, users, keys, budgets, DB-stored models)")
	salt := fs.String("litellm-salt-key", os.Getenv("LITELLM_SALT_KEY"), "LITELLM_SALT_KEY (or the LiteLLM master key) to read models stored in the LiteLLM database")
	org := fs.String("org", "", "organization slug for teams and keys outside a LiteLLM organization (default: the first organization)")
	dry := fs.Bool("dry-run", false, "show what would be imported without writing anything")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *cfgPath == "" && *dsn == "" {
		fmt.Fprintln(os.Stderr, "usage: theophylax import-litellm [-config litellm.yaml] [-litellm-db postgres://…] [-litellm-salt-key …] [-org slug] [-dry-run]")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var plan *migrate.Plan
	if *cfgPath != "" {
		b, err := os.ReadFile(*cfgPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if plan, err = migrate.ParseConfig(string(b)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	var data *migrate.LiteLLMData
	if *dsn != "" {
		var err error
		if data, err = migrate.ReadLiteLLM(ctx, *dsn); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if len(data.ProxyModels) > 0 {
			if plan == nil {
				plan = &migrate.Plan{}
			}
			plan.AddDBModels(data.ProxyModels, *salt)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	box, err := secrets.New(cfg.MasterKey)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	pool, err := db.Connect(ctx, cfg.DatabaseURL, 4)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gateway database:", err)
		return 1
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	rep, err := migrate.Apply(ctx, store.New(pool), box, plan, data, migrate.Options{OrgSlug: *org, DryRun: *dry})
	if err != nil {
		fmt.Fprintln(os.Stderr, "import failed (nothing was written):", err)
		return 1
	}
	if *asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(rep)
		return 0
	}
	verb := "Imported"
	if *dry {
		verb = "Would import"
	}
	kinds := map[string]bool{}
	for k := range rep.Created {
		kinds[k] = true
	}
	for k := range rep.Existing {
		kinds[k] = true
	}
	var ks []string
	for k := range kinds {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	fmt.Printf("%s from LiteLLM:\n", verb)
	for _, k := range ks {
		line := fmt.Sprintf("  %-12s %4d new", k, rep.Created[k])
		if n := rep.Existing[k]; n > 0 {
			line += fmt.Sprintf(", %d already present", n)
		}
		fmt.Println(line)
	}
	if plan != nil && len(plan.Providers) > 0 {
		fmt.Println("\nProviders:")
		for _, p := range plan.Providers {
			fmt.Printf("  %-32s %s\n", p.Name, redactConn(p.Conn))
		}
	}
	if len(rep.Warnings) > 0 {
		fmt.Println("\nNeeds attention:")
		for _, w := range rep.Warnings {
			fmt.Println("  - " + w)
		}
	}
	if rep.Created["keys"] > 0 && !*dry {
		fmt.Println("\nExisting LiteLLM keys (sk-…) now work against this gateway unchanged.")
	}
	return 0
}

func redactConn(c string) string {
	parts := strings.Split(c, "&")
	for i, p := range parts {
		for _, k := range []string{"api_key=", "secret_access_key=", "client_secret=", "session_token="} {
			if j := strings.Index(p, k); j >= 0 && !strings.HasPrefix(p[j+len(k):], "${") {
				parts[i] = p[:j+len(k)] + "****"
			}
		}
	}
	return strings.Join(parts, "&")
}
