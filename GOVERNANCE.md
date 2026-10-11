# Governance

Theophylax is a **founder-led open-source project**. The founder maintains it today and makes final decisions, and the processes below are written so that contributors can see how decisions are made and how to take on more responsibility. Governance will become more shared as the contributor base grows; changes to this document follow the process at the end.

## Maintainers

| Maintainer | Areas |
|---|---|
| Anirudh Koutha ([@anirudhkoutha](https://github.com/anirudhkoutha)) — founder | All |

Maintainers review and merge pull requests, triage issues, cut releases, handle security reports and uphold the [code of conduct](CODE_OF_CONDUCT.md).

## How decisions are made

- **Everyday changes** — bug fixes, features within the existing design, docs — are decided in pull requests. A change needs approval from a maintainer of the affected area and a green CI run. Disagreements are resolved in the discussion; if they can't be, the founder decides.
- **Significant changes** — new public APIs, changes to the receipt format, security model, data model or dependencies, or anything hard to reverse — start as a GitHub issue describing the problem and the proposal, and stay open for comment for at least a week before implementation is merged.
- **Security fixes** are developed privately and disclosed as described in [SECURITY.md](SECURITY.md).
- **Roadmap** priorities are published in [ROADMAP.md](ROADMAP.md) and reviewed in the open.

## Becoming a maintainer

Contributors who make sustained, high-quality contributions to an area — code, reviews, docs, triage or security work — and who work well with others may be invited to maintain that area. An existing maintainer nominates them in a pull request that adds them to the table above, and it is merged after a week without objection from other maintainers. Maintainers who are no longer active for six months may be moved to an emeritus list, and are welcome back.

## Contributions and licensing

Theophylax is licensed under Apache 2.0, and contributions are accepted under the same licence with a [Developer Certificate of Origin](https://developercertificate.org/) sign-off on each commit (see [CONTRIBUTING.md](CONTRIBUTING.md)). There is no contributor licence agreement.

## Changing this document

Changes to governance are proposed as a pull request to this file, kept open for at least two weeks for comment, and merged by the founder.
