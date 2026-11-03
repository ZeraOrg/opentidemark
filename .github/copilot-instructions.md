<!-- SPDX-License-Identifier: Apache-2.0 -->
# Instructions for the Copilot coding agent

OpenTidemark is a Go 1.27 server with a React/TypeScript web UI in `web/`. `SPEC.md` is the
source of truth for behaviour, tooling and the pinned versions; read the sections that apply
before changing anything, and follow `CONTRIBUTING.md`.

## Dependabot tasks

Issues labelled `dependabot-fix` ask you to make one Dependabot pull request mergeable. You work
on the Dependabot branch and open a pull request into it. The maintainers apply the result to
`main` themselves, so a green ci run on your branch is what counts.

- **Keep the versions Dependabot proposes.** Do not update anything else, and never downgrade a
  version that `main` already has. If one update cannot be made to work, revert only that
  package and explain why in the pull request.
- **7-day rule (SPEC §15.9):** if you must re-resolve `web/package-lock.json`, use npm 11
  (Node 24) with `npm install --before=<date seven days ago>`, so transitive versions are at
  least 7 days old too.
- **Stale `THIRD_PARTY_NOTICES.md`** (the most common failure, ci job "licences and notices"):
  run `make notices` and commit the regenerated file. If the licence check fails, do not add a
  licence exception; stop and describe the package and its licence in the pull request.
- **Versions pinned in SPEC.md:** a changed Go release (`go.mod` `toolchain`, Dockerfile
  `golang:` image) updates SPEC §5 and §17.2; changed GitHub Actions or tool versions update the
  pinned list in SPEC §17.4; a major update of a library named in SPEC §5 updates that row.
  Every SPEC.md change bumps its version in the header and adds a row to the changelog (§21).
- **Code that needs adapting** to a new version: make the smallest change that keeps behaviour,
  with tests, and describe it in the pull request.

## Rules for every change

- Run `make check` (gofmt, go vet, golangci-lint, `go test -race ./...`, govulncheck,
  OSV-Scanner, and the web lint, typecheck, tests and build) before you finish. The ClickHouse
  integration tests run in ci.
- Never delete, skip or weaken a test, lint rule or check to get green.
- Every new source file starts with `SPDX-License-Identifier: Apache-2.0`.
- Go: `gofmt`, wrapped errors (`%w`), `context.Context` first for I/O. TypeScript: strict, no
  `any`, no `@ts-ignore`, ESLint with zero warnings, Prettier.
- Do not loosen security headers, the CSP, licence allow-lists or workflow guards.
- Commit messages: `area: imperative summary`, for example `deps: regenerate THIRD_PARTY_NOTICES.md`.
