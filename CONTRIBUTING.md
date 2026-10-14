<!-- SPDX-License-Identifier: Apache-2.0 -->
# Contributing

Thanks for your interest in OpenTidemark.

## Licence of contributions (inbound = outbound)

OpenTidemark is licensed under the [Apache License, Version 2.0](LICENSE). Any contribution you
submit is licensed under the same terms, as described in section 5 of the licence. There is no
separate contributor licence agreement and no relicensing grant.

## Developer Certificate of Origin

Every commit must carry a `Signed-off-by` line certifying the
[Developer Certificate of Origin 1.1](https://developercertificate.org/): you wrote the change or
otherwise have the right to submit it under the project's licence. Add the line with
`git commit -s`:

```
Signed-off-by: Jane Doe <jane@example.com>
```

Use your real name and an e-mail address you can be reached at. Pull requests with unsigned
commits cannot be accepted.

## Workflow

1. Open an issue first for larger changes, so the design can be agreed before you write code.
2. Keep pull requests small and focused. Add or update tests with every change; for bug fixes,
   add the failing regression test first.
3. The design specification is [SPEC.md](SPEC.md) (code comments refer to it as `SPEC §n`). Update
   it in the same pull request when you change behaviour, APIs, configuration, the query language
   or the UI, and update the user documentation in `docs/`, which is derived from it (tests keep
   them consistent). Add user-visible changes under `## [Unreleased]` in
   [CHANGELOG.md](CHANGELOG.md); maintainers release by setting `VERSION` and moving those entries
   into a dated section (SPEC §17.6).
4. Run `make check` (Linux/macOS) or `.\build.ps1 check` (Windows) and make sure it is green. Run
   `make test-integration` when you touch storage, ingestion, queries, retention or alerting.
5. Every source file starts with `SPDX-License-Identifier: Apache-2.0` in a comment (enforced by
   `go test ./internal/repotest`).
6. New dependencies must use a permissive, Apache-2.0-compatible licence (MIT, BSD, Apache-2.0,
   ISC; MPL-2.0 unmodified) and be pinned; `make licences` checks the Go dependencies.

## How changes land

This repository is published from the maintainers' development repository; each publication is
one commit named `Sync from private <sha>`. Accepted pull requests are applied there by a maintainer, keeping
your authorship and sign-off, and reach `main` with the next sync; the pull request is then
closed with a link to that commit.

## Reporting security issues

Do **not** open public issues for vulnerabilities. See [SECURITY.md](SECURITY.md).
