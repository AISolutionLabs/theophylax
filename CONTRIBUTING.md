# Contributing to Theophylax

Thanks for helping. Theophylax is Apache-2.0 licensed and contributions are
accepted under the same licence.

## Sign your commits (DCO)

We use the [Developer Certificate of Origin](https://developercertificate.org/)
rather than a CLA. Add a `Signed-off-by` line to every commit to certify you
wrote the change or have the right to contribute it:

```bash
git commit -s -m "fix: …"
```

CI rejects pull requests with unsigned commits.

## Development

Requirements: Go 1.26 or later (the `toolchain` line in `go.mod` selects 1.27.1), Node 22, Postgres 16.

```bash
make ui build                       # console + binary
export THEOPHYLAX_TEST_DATABASE_URL=postgres://postgres@localhost:5432/postgres?sslmode=disable
go test ./...                       # unit + end-to-end tests (throwaway databases)
```

- Keep the dependency footprint small: the gateway uses the standard library
  plus pgx. New dependencies need a strong reason.
- `gofmt` and `go vet` must be clean.
- Changes to request handling should keep the benchmark numbers in
  `bench/README.md` honest; run `bench/run.sh` for anything on the hot path.

## Guardrails

The prompt-injection detector is measured on a labelled corpus with a
development and a held-out test split (`internal/guardrails/testdata`). When
you improve it:

1. add the missed attack (or false positive) to `gen_corpus.py`,
2. tune using the **dev** split only,
3. report the **test** split numbers in your PR.

CI enforces recall and false-positive floors on both splits.

## Commit messages

Conventional style (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `ci:`),
imperative, under 72 characters for the subject.
