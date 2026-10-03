package main

import (
	"crypto/ed25519"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"theophylax/internal/receipts"
)

// verifyReceiptCLI verifies a Proof-of-Inference receipt offline.
//
//	theophylax verify-receipt --keys https://gw.example.com/.well-known/theophylax-receipt-keys.json \
//	    --request req.json --response resp.json <jws | @file>
func verifyReceiptCLI(args []string) int {
	fs := flag.NewFlagSet("verify-receipt", flag.ContinueOnError)
	keys := fs.String("keys", "", "JWKS URL or file with the gateway's receipt public keys")
	reqF := fs.String("request", "", "file containing the exact request body that was sent")
	respF := fs.String("response", "", "file containing the exact response body that was received")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 || *keys == "" {
		fmt.Fprintln(os.Stderr, "usage: theophylax verify-receipt --keys <jwks-url|file> [--request f] [--response f] <jws|@file>")
		return 2
	}
	jws := fs.Arg(0)
	if strings.HasPrefix(jws, "@") {
		b, err := os.ReadFile(jws[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		jws = strings.TrimSpace(string(b))
	}
	var jwks []byte
	var err error
	if strings.HasPrefix(*keys, "http") {
		c := &http.Client{Timeout: 10 * time.Second}
		resp, e := c.Get(*keys)
		if e != nil {
			fmt.Fprintln(os.Stderr, "fetch keys:", e)
			return 2
		}
		jwks, err = io.ReadAll(resp.Body)
		resp.Body.Close()
	} else {
		jwks, err = os.ReadFile(*keys)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	set, err := receipts.ParseJWKS(jwks)
	if err != nil {
		fmt.Fprintln(os.Stderr, "parse keys:", err)
		return 2
	}
	rc, err := receipts.Verify(jws, map[string]ed25519.PublicKey(set))
	if err != nil {
		fmt.Println("✗ INVALID:", err)
		return 1
	}
	ok := true
	fmt.Printf("✓ signature valid (issuer %s)\n  request  %s\n  model    %s → %s via %s (%d attempt(s))\n  status   %d   tokens in/out %d/%d   cost $%.6f\n  policies %d in force, %d action(s)\n",
		rc.Issuer, rc.ID, rc.Requested, rc.Route.UpstreamModel, rc.Route.ProviderKind, rc.Route.Attempts, rc.Status, rc.Usage.Input, rc.Usage.Output, rc.CostUSD, len(rc.Policies), len(rc.Actions))
	for name, pair := range map[string][2]string{"request": {*reqF, rc.ReqSHA256}, "response": {*respF, rc.RespSHA256}} {
		if pair[0] == "" {
			continue
		}
		b, err := os.ReadFile(pair[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		if receipts.SHA256Hex(b) == pair[1] {
			fmt.Printf("✓ %s body matches receipt digest\n", name)
		} else {
			fmt.Printf("✗ %s body does NOT match receipt digest\n", name)
			ok = false
		}
	}
	if !ok {
		return 1
	}
	return 0
}
