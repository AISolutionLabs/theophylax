package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"theophylax/internal/audit"
	"theophylax/internal/auth"
	"theophylax/internal/db"
	"theophylax/internal/store"
	"theophylax/internal/util"
)

// resetPasswordCLI is break-glass account recovery. It talks to Postgres
// directly, so it works when nobody can sign in to the console:
//
//	theophylax reset-password -email admin@example.com              # generates a password
//	echo 'new-password-here' | theophylax reset-password -email admin@example.com -password-stdin
//
// It also reactivates a suspended account (unless -keep-status), and records
// the reset in the tamper-evident audit log. Run it wherever the gateway runs,
// e.g. kubectl exec deploy/theophylax -- theophylax reset-password -email ...
func resetPasswordCLI(args []string) int {
	fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	email := fs.String("email", "", "email of the local account to reset")
	fromStdin := fs.Bool("password-stdin", false, "read the new password from stdin instead of generating one")
	keepStatus := fs.Bool("keep-status", false, "do not reactivate the account if it is suspended")
	makeAdmin := fs.Bool("platform-admin", false, "also grant platform admin (use when the last admin is locked out)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *email == "" {
		fmt.Fprintln(os.Stderr, "usage: theophylax reset-password -email <email> [-password-stdin] [-keep-status] [-platform-admin]")
		return 2
	}

	pw, generated := "", false
	if *fromStdin {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintln(os.Stderr, "could not read a password from stdin")
			return 1
		}
		pw = strings.TrimRight(line, "\r\n")
	} else {
		pw, generated = util.RandomToken(18), true
	}
	if len(pw) < 12 {
		fmt.Fprintln(os.Stderr, "password must be at least 12 characters")
		return 1
	}

	dsn := os.Getenv("THEOPHYLAX_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "set THEOPHYLAX_DATABASE_URL to the gateway's database")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.Connect(ctx, dsn, 2)
	if err != nil {
		fmt.Fprintln(os.Stderr, "database:", err)
		return 1
	}
	defer pool.Close()

	hash, err := auth.HashPassword(pw)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var id, status string
	var admin bool
	err = pool.QueryRow(ctx, `
		UPDATE users SET password_hash = $2,
		       status = CASE WHEN $3 THEN status ELSE 'active' END,
		       is_platform_admin = is_platform_admin OR $4
		 WHERE lower(email) = lower($1)
		RETURNING id, status, is_platform_admin`, *email, hash, *keepStatus, *makeAdmin).Scan(&id, &status, &admin)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			fmt.Fprintf(os.Stderr, "no user with email %s\n", *email)
		} else {
			fmt.Fprintln(os.Stderr, "update:", err)
		}
		return 1
	}

	host, _ := os.Hostname()
	audit.New(store.New(pool)).Record(ctx, audit.Event{
		Actor:  audit.Actor{Type: "system", ID: "cli", Email: "reset-password@" + host},
		Action: "auth.password_reset", TargetType: "user", TargetID: id,
		Details: map[string]any{"email": *email, "via": "cli", "generated": generated, "reactivated": !*keepStatus, "granted_platform_admin": *makeAdmin},
	})

	fmt.Fprintf(os.Stderr, "password reset for %s (status %s, platform admin %v)\n", *email, status, admin)
	if generated {
		fmt.Println(pw)
	}
	return 0
}
