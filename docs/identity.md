# Identity, SSO, SCIM and RBAC

## Roles

| Role | Org scope | Team scope | Project scope |
|---|---|---|---|
| **owner** | everything, including other owners, SSO and deleting teams | everything in the team, including team owners | — |
| **admin** | teams, projects, members (not owners), providers for the org, guardrails, budgets, marketplace review, audit | projects, members, keys, agents, policies in the team | members, keys, settings |
| **member** | see the org, use projects they belong to, publish to the registry | use the team's projects, create keys | use the project, create keys |
| **viewer** | read-only | read-only | read-only |

Roles inherit downwards. **Platform administrators** manage the model catalog, platform-wide providers, rollouts, organizations and users. An organization always keeps at least one owner.

## Single sign-on (OIDC)

Register `https://<public-url>/auth/oidc/callback` as the redirect URI. Login uses the authorization-code flow with PKCE, verifies the ID token signature (RS/PS/ES/EdDSA via JWKS), issuer, audience, expiry and nonce. Email comes from `email`, `preferred_username` or `upn`.

- **Allowed email domains** restrict who can sign in.
- **JIT provisioning** creates accounts on first sign-in; turn it off to require SCIM or invites.
- **Group mappings** (`groups` or `roles` claim) grant roles at org, team or project level and are re-applied at every sign-in; mapping-managed memberships are removed when the group disappears. Manually added memberships are never touched.

### SSO tokens as gateway credentials

With *Accept access tokens* on, a valid IdP JWT (audience = client ID or a configured audience) authenticates gateway calls as that person, with `X-Theophylax-Project` choosing the project they're a member of. Pair with Claude Code's `apiKeyHelper` to remove shared keys entirely.

## SCIM 2.0

Generate a SCIM token on the identity provider and point Entra ID / Okta provisioning at `https://<public-url>/scim/v2`.

- `Users`: create, get, list with `filter=userName eq "…"` / `externalId eq "…"`, replace, patch (`active`, names, emails), delete. Deactivating removes all memberships in the org and revokes the person's keys.
- `Groups` ↔ teams: create (or link by name / externalId), patch members, replace, delete (unlinks; projects and keys stay).

## Personal access tokens

`tpxp_…` tokens act with the creating user's permissions on the admin API (`Authorization: Bearer`), expire after 90 days by default and are auditable like any other actor — use them for Terraform/Crossplane/GitOps automation.
