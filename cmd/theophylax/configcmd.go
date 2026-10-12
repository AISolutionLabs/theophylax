package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"theophylax/internal/confsync"
)

const configUsage = `usage:
  theophylax config export [-org slug] [-o file]
  theophylax config plan   -f file [-prune] [-exit-code]
  theophylax config apply  -f file [-prune] [-dry-run]

Connects to the gateway at -url (or THEOPHYLAX_URL) with a personal access
token from -token (or THEOPHYLAX_TOKEN). Changes go through the admin API, so
the token's permissions, validation and the audit log all apply.
`

// configCLI implements configuration as code over the admin API.
func configCLI(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(os.Stderr, configUsage)
		return 2
	}
	cmd := args[0]
	fs := flag.NewFlagSet("config "+cmd, flag.ContinueOnError)
	base := fs.String("url", os.Getenv("THEOPHYLAX_URL"), "gateway URL, e.g. https://ai.example.com")
	token := fs.String("token", os.Getenv("THEOPHYLAX_TOKEN"), "personal access token (tpxp_…)")
	file := fs.String("f", "", "configuration document (YAML or JSON); - reads standard input")
	out := fs.String("o", "", "export: write to this file instead of standard output")
	org := fs.String("org", "", "export: only this organization (providers and models are still included)")
	prune := fs.Bool("prune", false, "delete providers, models, deployments, guardrails and budgets the document does not list")
	dry := fs.Bool("dry-run", false, "apply: show the plan without changing anything")
	exitCode := fs.Bool("exit-code", false, "plan: exit with status 3 when there are changes (drift detection in CI)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *base == "" || *token == "" {
		fmt.Fprintln(os.Stderr, "set -url and -token (or THEOPHYLAX_URL and THEOPHYLAX_TOKEN)")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	c := confsync.NewClient(*base, *token)

	switch cmd {
	case "export":
		d, warn, err := confsync.Export(ctx, c, *org)
		if err != nil {
			return fail(err)
		}
		b, err := confsync.MarshalYAML(d, "Theophylax configuration, exported "+time.Now().UTC().Format(time.RFC3339)+
			"\nSecrets are masked; applying a masked value unchanged keeps the stored secret.\nApply with: theophylax config apply -f <file>")
		if err != nil {
			return fail(err)
		}
		for _, w := range warn {
			fmt.Fprintln(os.Stderr, "warning:", w)
		}
		if *out == "" {
			_, _ = os.Stdout.Write(b)
			return 0
		}
		if err := os.WriteFile(*out, b, 0o600); err != nil {
			return fail(err)
		}
		return 0
	case "plan", "apply":
		if *file == "" {
			fmt.Fprintln(os.Stderr, "-f is required")
			return 2
		}
		var src []byte
		var err error
		if *file == "-" {
			src, err = io.ReadAll(os.Stdin)
		} else {
			src, err = os.ReadFile(*file)
		}
		if err != nil {
			return fail(err)
		}
		d, err := confsync.Parse(src)
		if err != nil {
			return fail(err)
		}
		p, err := confsync.Compute(ctx, c, d, confsync.Options{Prune: *prune})
		if err != nil {
			return fail(err)
		}
		p.Write(os.Stdout)
		if cmd == "plan" || *dry || p.Empty() {
			if *exitCode && !p.Empty() {
				return 3
			}
			return 0
		}
		fmt.Println()
		n, err := p.Apply(ctx, c, os.Stdout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "applied %d of %d changes, then: %v\n", n, len(p.Changes), err)
			return 1
		}
		fmt.Printf("Applied %d changes.\n", n)
		return 0
	}
	fmt.Fprint(os.Stderr, configUsage)
	return 2
}

func fail(err error) int {
	var ae *confsync.APIError
	if errors.As(err, &ae) && ae.Status == 401 {
		fmt.Fprintln(os.Stderr, "the gateway rejected the token:", ae.Message)
		return 1
	}
	fmt.Fprintln(os.Stderr, err)
	return 1
}
