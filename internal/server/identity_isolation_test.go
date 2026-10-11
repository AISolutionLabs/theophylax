package server

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// TestIdentityTenantIsolation checks that an identity provider configured in one
// organization cannot act on accounts outside that organization: not by
// asserting their email at sign-in, not through SCIM, and not through role
// mappings that point at another organization.
func TestIdentityTenantIsolation(t *testing.T) {
	ts, s := setup(t)
	ctx := context.Background()
	idp := newFakeIdP(t)
	root := newClient(t, ts.URL)
	root.must("POST", "/api/auth/login", map[string]string{"email": "root@example.com", "password": "root-password-123"}, 200)

	// Organization A (the bootstrap org) and organization B, each with a project.
	orgA := root.list("/api/orgs")[0]["id"].(string)
	teamA := root.must("POST", "/api/orgs/"+orgA+"/teams", map[string]any{"name": "A team"}, 201)["id"].(string)
	projA := root.must("POST", "/api/teams/"+teamA+"/projects", map[string]any{"name": "A proj"}, 201)["id"].(string)
	orgB := root.must("POST", "/api/orgs", map[string]any{"name": "Beta"}, 201)["id"].(string)
	teamB := root.must("POST", "/api/orgs/"+orgB+"/teams", map[string]any{"name": "B team"}, 201)["id"].(string)
	projB := root.must("POST", "/api/teams/"+teamB+"/projects", map[string]any{"name": "B proj"}, 201)["id"].(string)
	prov := root.must("POST", "/api/providers", map[string]any{"name": "mock", "connection_string": "mock://x"}, 201)["id"].(string)
	m := root.must("POST", "/api/models", map[string]any{"name": "m"}, 201)["id"].(string)
	root.must("POST", "/api/models/"+m+"/deployments", map[string]any{"provider_id": prov, "upstream_model": "x"}, 201)

	idpBody := func(mapping map[string]any) map[string]any {
		return map[string]any{"name": "B IdP", "issuer": idp.srv.URL, "client_id": "tpx", "gateway_tokens": true, "default_role": "member",
			"role_mappings": []any{mapping}}
	}

	// Role mappings must stay inside the IdP's organization.
	for _, bad := range []map[string]any{
		{"group": "g", "scope_type": "org", "scope_id": orgA, "role": "owner"},
		{"group": "g", "scope_type": "team", "scope_id": teamA, "role": "admin"},
		{"group": "g", "scope_type": "project", "scope_id": projA, "role": "admin"},
	} {
		if code, _, raw, _ := root.do("POST", "/api/orgs/"+orgB+"/idps", idpBody(bad)); code != 400 {
			t.Fatalf("cross-org role mapping %v accepted: %d %s", bad, code, raw)
		}
	}
	created := root.must("POST", "/api/orgs/"+orgB+"/idps", idpBody(map[string]any{"group": "g", "scope_type": "team", "scope_id": teamB, "role": "member"}), 201)
	idpB := created["id"].(string)
	waitReload(s)

	now := time.Now().Unix()
	tok := func(sub, email string, extra map[string]any) string {
		c := map[string]any{"iss": idp.srv.URL, "aud": "tpx", "sub": sub, "email": email, "groups": []string{"g"}, "iat": now, "exp": now + 300}
		for k, v := range extra {
			c[k] = v
		}
		return idp.token(c)
	}
	call := func(token, project string) int {
		code, _, _, _ := gw(t, ts.URL, token, "/v1/messages", msg("m", "hi"), "X-Theophylax-Project", project)
		return code
	}

	// 1. B's IdP cannot sign in as the platform admin by asserting their email.
	if code := call(tok("attacker-1", "root@example.com", nil), projB); code != 403 {
		t.Fatalf("org B IdP signed in as the platform admin: %d", code)
	}
	var rootIdP *string
	_ = s.Pool.QueryRow(ctx, `SELECT idp_id FROM users WHERE email='root@example.com'`).Scan(&rootIdP)
	if rootIdP != nil {
		t.Fatalf("platform admin was bound to org B's IdP")
	}

	// 2. Nor as a user who belongs to another organization.
	root.must("POST", "/api/members", map[string]any{"email": "carol@example.com", "scope_type": "org", "scope_id": orgA, "role": "member"}, 201)
	if code := call(tok("attacker-2", "carol@example.com", nil), projB); code != 403 {
		t.Fatalf("org B IdP signed in as an org A user: %d", code)
	}

	// 3. A user who lives only in org B can still be linked, and is then matched
	//    on the IdP subject even if the email changes.
	root.must("POST", "/api/members", map[string]any{"email": "dave@example.com", "scope_type": "org", "scope_id": orgB, "role": "member"}, 201)
	if code := call(tok("dave-sub", "dave@example.com", nil), projB); code != 200 {
		t.Fatalf("legitimate same-org link refused: %d", code)
	}
	var daveID, daveIdP string
	_ = s.Pool.QueryRow(ctx, `SELECT id, coalesce(idp_id,'') FROM users WHERE email='dave@example.com'`).Scan(&daveID, &daveIdP)
	if daveIdP != idpB {
		t.Fatalf("linked user not bound to the IdP: %q", daveIdP)
	}
	if code := call(tok("dave-sub", "dave.renamed@example.com", nil), projB); code != 200 {
		t.Fatalf("subject match after email change: %d", code)
	}
	var n int
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE email='dave.renamed@example.com'`).Scan(&n)
	if n != 0 {
		t.Fatalf("email change created a duplicate account")
	}

	// 4. An explicitly unverified email is refused.
	if code := call(tok("u-unverified", "newbie@example.com", map[string]any{"email_verified": false}), projB); code != 403 {
		t.Fatalf("unverified email accepted: %d", code)
	}

	// 5. A membership row naming another organization's scope confers nothing.
	_, _ = s.Pool.Exec(ctx, `INSERT INTO memberships (id, user_id, org_id, scope_type, scope_id, role, source) VALUES ('mem_forged', $1, $2, 'org', $3, 'owner', 'idp')`, daveID, orgB, orgA)
	s.Gateway.Resolver.Invalidate()
	if code := call(tok("dave-sub", "dave@example.com", nil), projA); code != 403 {
		t.Fatalf("cross-org membership row granted access to org A: %d", code)
	}

	// 6. SCIM cannot adopt or rewrite accounts managed outside org B.
	scim := root.must("POST", "/api/idps/"+idpB+"/scim-token", map[string]any{}, 200)["token"].(string)
	sc := &client{t: t, base: ts.URL, hc: http.DefaultClient, auth: scim}
	for _, email := range []string{"root@example.com", "carol@example.com"} {
		if code, _, raw, _ := sc.do("POST", "/scim/v2/Users", map[string]any{"userName": email, "active": true}); code != 409 {
			t.Fatalf("SCIM adopted %s: %d %s", email, code, raw)
		}
	}
	// Erin is in both organizations; B's SCIM may manage her B membership but not
	// her global profile.
	root.must("POST", "/api/members", map[string]any{"email": "erin@example.com", "scope_type": "org", "scope_id": orgA, "role": "member"}, 201)
	root.must("POST", "/api/members", map[string]any{"email": "erin@example.com", "scope_type": "org", "scope_id": orgB, "role": "member"}, 201)
	var erinID string
	_ = s.Pool.QueryRow(ctx, `SELECT id FROM users WHERE email='erin@example.com'`).Scan(&erinID)
	patch := func(value map[string]any) {
		sc.do("PATCH", "/scim/v2/Users/"+erinID, map[string]any{"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"},
			"Operations": []any{map[string]any{"op": "replace", "value": value}}})
	}
	patch(map[string]any{"userName": "attacker@evil.example", "displayName": "pwned"})
	var email, name string
	_ = s.Pool.QueryRow(ctx, `SELECT email, name FROM users WHERE id=$1`, erinID).Scan(&email, &name)
	if email != "erin@example.com" || name == "pwned" {
		t.Fatalf("org B SCIM rewrote a shared user's profile: %q %q", email, name)
	}
	patch(map[string]any{"active": false})
	var inA, inB int
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE org_id=$2), count(*) FILTER (WHERE org_id=$3) FROM memberships WHERE user_id=$1`, erinID, orgA, orgB).Scan(&inA, &inB)
	if inA != 1 || inB != 0 {
		t.Fatalf("deactivation should only remove the org B membership: A=%d B=%d", inA, inB)
	}

	// 7. A platform admin can still choose to sign in through an IdP: binding is
	//    an explicit platform-admin action, after which sign-in works.
	var rootID string
	_ = s.Pool.QueryRow(ctx, `SELECT id FROM users WHERE email='root@example.com'`).Scan(&rootID)
	if code, _, _, _ := root.do("PATCH", "/api/users/"+rootID, map[string]any{"idp_id": "idp_missing"}); code != 400 {
		t.Fatalf("binding to an unknown IdP accepted: %d", code)
	}
	root.must("PATCH", "/api/users/"+rootID, map[string]any{"idp_id": idpB}, 200)
	if code := call(tok("root-sub", "root@example.com", nil), projB); code != 200 {
		t.Fatalf("explicitly bound platform admin could not sign in: %d", code)
	}
	var ext string
	_ = s.Pool.QueryRow(ctx, `SELECT coalesce(external_id,'') FROM users WHERE id=$1`, rootID).Scan(&ext)
	if ext != "root-sub" {
		t.Fatalf("subject not learnt after explicit binding: %q", ext)
	}
}
