# Security policy

Theophylax sits between your users and your model providers and holds provider
credentials, so we treat security reports as the highest priority.

## Reporting a vulnerability

Please **do not open a public issue**. Report privately through GitHub's
[private vulnerability reporting](../../security/advisories/new) for this
repository. Include:

- the version (`theophylax version`) and how it is deployed,
- steps to reproduce, and what an attacker gains,
- whether the issue is already public.

You'll get an acknowledgement within 3 working days and a first assessment
within 10. We coordinate a fix and disclosure date with you, credit you in the
advisory unless you'd rather we didn't, and publish a GitHub security
advisory with the fixed version.

## Scope

In scope: the gateway, admin API, console, Helm chart and container image in
this repository. Especially interesting:

- authentication or authorisation bypass (keys, SSO, SCIM, RBAC scopes),
- guardrail bypasses that defeat Secret Shield redaction (a credential
  reaching a model) or that make restore-on-return leak a secret to a
  different user,
- forging or altering receipts or the audit chain without detection,
- leaking provider credentials through the API, logs or exports.

Prompt-injection detection is heuristic: a phrasing the detector misses is a
welcome improvement report (open an issue or PR with the sample), not a
vulnerability, unless it bypasses a guarantee above.

## Supported versions

Security fixes go into the latest minor release.

## Verifying releases

Container images and release checksums are signed with Sigstore cosign
(keyless, GitHub OIDC) and recorded in the Rekor transparency log:

```bash
cosign verify ghcr.io/theophylax/theophylax:<version> \
  --certificate-identity-regexp 'https://github.com/Theophylax/phylax/.github/workflows/release.yml@.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp 'https://github.com/Theophylax/phylax/.github/workflows/release.yml@.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Each release also ships SPDX SBOMs for the image and the source tree.
