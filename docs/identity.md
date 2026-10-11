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
- **Group mappings** (`groups` or `roles` claim) grant roles at org, team or project level and are re-applied at every sign-in; mapping-managed memberships are removed when the group disappears. Manually added memberships are never touched. Mappings may only target the identity provider's own organization and its teams and projects.
- **Account linking.** An identity provider belongs to one organization and is trusted only inside it. A sign-in matches the user already bound to the provider by its subject (`sub`), so a changed email keeps the same account. Otherwise an existing account with the same email is linked automatically only if it is not a platform admin and has no memberships in other organizations; anything else is refused. To let such an account sign in through a provider, a platform admin binds it explicitly with `PATCH /api/users/{id}` `{"idp_id": "idp_…"}` (`""` unbinds). Tokens with `email_verified: false` are refused.

### SSO tokens as gateway credentials

With *Accept access tokens* on, a valid IdP JWT (audience = client ID or a configured audience) authenticates gateway calls as that person, with `X-Theophylax-Project` choosing the project they're a member of. Pair with Claude Code's `apiKeyHelper` to remove shared keys entirely.

## SCIM 2.0

Generate a SCIM token on the identity provider and point Entra ID / Okta provisioning at `https://<public-url>/scim/v2`.

- `Users`: create, get, list with `filter=userName eq "…"` / `externalId eq "…"`, replace, patch (`active`, names, emails), delete. Deactivating removes all memberships in the org and revokes the person's keys. The same linking rule applies: creating a user whose email belongs to an account managed outside the organization returns `409`, and for people who also belong to other organizations SCIM manages their membership here (`active`) but not their name, email or externalId.
- `Groups` ↔ teams: create (or link by name / externalId), patch members, replace, delete (unlinks; projects and keys stay).

## Personal access tokens

`tpxp_…` tokens act with the creating user's permissions on the admin API (`Authorization: Bearer`), expire after 90 days by default and are auditable like any other actor — use them for Terraform/Crossplane/GitOps automation.
