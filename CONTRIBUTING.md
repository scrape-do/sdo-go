<!-- SPDX-License-Identifier: Apache-2.0 -->
# Contributing to sdo-go

Thanks for your interest in improving `sdo-go`, the official Go SDK for Scrape.do (`github.com/scrape-do/sdo-go`). This document is the contract for how changes land here. Read it before opening a pull request.

`sdo-go` is one of several language SDKs (`sdo-sdk-python`, `sdo-sdk-js`, `sdo-go`) plus a CLI (`sdo-cli`, which ships the `sdo` binary and bundles the MCP server via `sdo mcp serve`). The request parameters, defaults and observable behaviour are shared by all sdo SDKs and pinned by the conformance corpus (`conformance.json`), which Scrape.do maintains against https://scrape.do/documentation. Propose a change in an issue first; a change to a parameter lands in every SDK together.

## Ground rules

- **License.** This project is Apache-2.0. By contributing, you agree your contribution is licensed under Apache-2.0.
- **No telemetry, ever.** `sdo-go` does not phone home, collect usage data, emit analytics, or ship a "crash reporter." Do not add any. PRs that introduce network calls to anything other than the Scrape.do API (or a user-configured endpoint) will be rejected.
- **No UI.** The surface is CLI + SDKs + MCP only. There is no web dashboard, no embedded server, no browser anything in this repo.
- **Token-only auth.** Authentication is a single token read from the `SDO_TOKEN` environment variable (or passed explicitly to the client constructor). Do not add OAuth flows, key files, config-file credential loading, or any alternate auth path.
- **Governance is Solo+AI.** The founder is the sole approver and final reviewer of every merge. There is no committee, no CLA, and no second maintainer to ping. Keep PRs small and self-explanatory so a single reviewer can evaluate them quickly.

## Prerequisites

- **Go 1.26.9 or newer** (the `go` directive in `go.mod`; CI tests 1.26 and 1.27, the two supported releases). Check with `go version`; do not use features that raise the minimum.
- A POSIX-ish shell for the commands below. Everything works from the repo root.

Clone and verify a clean baseline before you change anything:

```sh
git clone https://github.com/scrape-do/sdo-go
cd sdo-go
go test ./...
go vet ./...
```

If the baseline is red on a fresh clone, stop and open an issue — do not build on top of a broken tree.

## Development workflow

We work **test-first (TDD)**. The loop is non-negotiable for behavior changes:

1. **Write a failing test** that expresses the behavior you want. Run it, watch it fail for the right reason.
2. **Write the minimum code** to make it pass.
3. **Refactor** with the test green.

Practically, the inner loop is:

```sh
go test ./...        # all tests must pass
go vet ./...         # static analysis must be clean
gofmt -l .           # must print nothing — every file is gofmt-clean
```

To auto-format before committing:

```sh
gofmt -w .
```

A change is not done until `go test -race ./...`, `go vet ./...`, and `gofmt -l .` are all clean. CI also enforces a 99% statement-coverage floor and checks `api-surface.txt` (regenerate it with `go run tools/apisurface/main.go > api-surface.txt` when the exported API changes).

### Running a focused test

```sh
go test ./... -run TestRetry -v
```

### Race detector

For anything touching concurrency (goroutines, the HTTP transport, shared client state), run:

```sh
go test ./... -race
```

## The conformance corpus

The public behaviour of every SDK is validated against a shared, cross-language conformance corpus. In this repo that corpus lives in **`conformance.json`**.

`conformance.json` is maintained by Scrape.do and shared by the Go, JavaScript and Python SDKs; its sha256 sidecar is checked by the tests. **Do not edit it in a contribution; describe the change in an issue.**

- If a conformance test fails, the correct response is almost always to fix `sdo-go` so it matches the corpus — not to change the corpus.
- If you believe the corpus itself is wrong (a missing case, an incorrect expectation), open an issue describing the discrepancy. Scrape.do updates the corpus against https://scrape.do/documentation, and a change to a parameter lands in every SDK together.
- The conformance tests load `conformance.json` and assert request shaping, parameter encoding, and error mapping against it. Treat them as the definition of correct behavior for the public API.
- The harness checks `conformance.json` against its `conformance.json.sha256` sidecar and fails on any section or key it does not consume, so an updated corpus with new cases cannot pass silently.
- The test suite is hermetic: `TestMain` removes every `SDO_*` variable for the run, so your shell's `SDO_TIMEOUT` or `SDO_BASE_URL` cannot change a result.

## Error handling and the `errors.As` taxonomy

`sdo-go` exposes a typed error taxonomy. Callers are expected to discriminate errors with `errors.As`, not by string-matching messages or comparing against sentinel values where a type is more appropriate.

When you add or change error behavior:

- Return concrete, exported error types (or wrap them) so that `errors.As` can recover them:

  ```go
  var apiErr *sdo.APIError
  if errors.As(err, &apiErr) && apiErr.IsAuth() {
      // handle an invalid token or an exhausted credit balance
  }
  ```

- Always wrap with `%w` (`fmt.Errorf("…: %w", err)`) when adding context, so the chain stays walkable by `errors.As` / `errors.Unwrap`.
- Do not collapse distinct failure categories (auth, rate-limit, validation, transport, API error) into a single opaque error. The taxonomy is part of the public surface and is covered by the conformance corpus.
- Do not break existing error types or rename their fields: that is a breaking API change. Propose it in an issue first.

Add a test that uses `errors.As` to assert the caller can recover the type you intend to expose.

## Public API stability

The exported surface (`sdo.*` types, function signatures, error types, struct fields) is recorded in `api-surface.txt`; its request parameters, defaults and observable behaviour are shared by all sdo SDKs and pinned by the conformance corpus. Propose a change to it in an issue first: a change to a parameter lands in every SDK together, and removing or changing an exported name is a breaking change (see [MIGRATING.md](MIGRATING.md)). Internal refactors that leave the public surface and the conformance corpus untouched are fine and welcome.

## Commit and sign-off requirements

### Conventional Commits

Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/). Use a type, an optional scope, and an imperative subject:

```
feat(client): add per-request timeout option
fix(retry): respect Retry-After header on 429
docs(contributing): clarify conformance corpus rules
test(errors): cover errors.As recovery of APIError
chore(deps): bump golang.org/x/text
```

Use `feat!:` / `fix!:` (or a `BREAKING CHANGE:` footer) for anything that alters the public surface. Common types: `feat`, `fix`, `docs`, `test`, `refactor`, `chore`.

### DCO sign-off

This project uses the **Developer Certificate of Origin** — not a CLA. Every commit must be signed off, certifying you have the right to submit the work under Apache-2.0. Add the sign-off trailer with `-s`:

```sh
git commit -s -m "fix(retry): respect Retry-After header on 429"
```

This appends:

```
Signed-off-by: Your Name <you@example.com>
```

Make sure `git config user.name` and `git config user.email` are your real identity. The `commits` CI job fails the pull request when a commit lacks a `Signed-off-by` trailer or a Conventional Commit subject, or when the pull request title, which becomes the squash commit's subject, is not a Conventional Commit subject. To sign off a commit you already made:

```sh
git commit --amend -s --no-edit
```

For a multi-commit branch, rebase with `git rebase --signoff <base>`.

## Pull request checklist

Before you open a PR, confirm:

- [ ] You started from a failing test (TDD) for any behavior change.
- [ ] `go test ./...` passes.
- [ ] `go test ./... -race` passes if you touched concurrency.
- [ ] `go vet ./...` is clean.
- [ ] `gofmt -l .` prints nothing.
- [ ] `conformance.json` is **not** edited.
- [ ] If the public surface changed, `api-surface.txt` was regenerated and the change is noted in CHANGELOG.md.
- [ ] New/changed errors are recoverable via `errors.As` and tested.
- [ ] No telemetry, no UI, no new auth path, no new third-party network calls.
- [ ] The minimum Go version is unchanged.
- [ ] Every commit is a Conventional Commit and is DCO-signed (`-s`).
- [ ] The diff is scoped to the change described — no drive-by refactors.

Keep the PR description focused: what changed, why, and how you verified it. Since the founder is the sole reviewer, a tight, well-tested diff is the fastest path to merge.

## Reporting bugs and proposing changes

- **Bugs:** open an issue with a minimal reproduction — ideally a failing Go test. Include `go version`, OS, and the exact `sdo-go` version/commit.
- **Corpus discrepancies:** open an issue describing the mismatch between `sdo-go` behaviour and https://scrape.do/documentation. Do not patch `conformance.json` to work around it.
- **Security:** do not open a public issue for vulnerabilities. Follow [SECURITY.md](SECURITY.md).

## Questions

If something here is unclear, open an issue rather than guessing. We'd rather answer a question than review a PR that took the wrong path.

## CI workflows

| Workflow | Runs on | What it does |
|----------|---------|--------------|
| `ci.yml` | every push to `main` and every pull request | `go vet` and the race-enabled test suite on Go 1.26 and 1.27 (Linux), plus macOS and Windows on 1.27, including the cross-language conformance suite and its integrity check; a `quality` job for `gofmt` and the 99% statement-coverage floor; the public-API snapshot (`api-surface.txt`) must be up to date; CodeQL code scanning. |
| `commits.yml` | every pull request: opened, updated, reopened, or its title or description edited | the `commits` job checks every commit's subject and sign-off and the pull request title, which becomes the squash commit's subject. A workflow of its own, so editing a pull request re-runs only this check. |
| `security.yml` | push to `main`, pull requests, and weekly | secret scanning over the full git history (gitleaks), OSV-Scanner over `go.mod`, `govulncheck`, and an SBOM uploaded as a build artifact. OpenSSF Scorecard runs on pushes to `main` and weekly, with results in code scanning. |
| `tag.yml` | a version tag only | A Go module is published by its tag (the module proxy fetches it), so there is no publish job. This workflow checks that the tag is `vX.Y.Z` or `vX.Y.Z-rc.N`, is on `main`, and matches `version.go`. A `-rc.N` tag is a Go pre-release, so `@latest` never selects it. Once the checks pass, the `github-release` job creates the tag's GitHub Release with the version's CHANGELOG.md section as its notes (a pre-release for `-rc.N`), and skips a tag that already has one. |
| Dependabot | weekly | update pull requests for Go modules and the GitHub Actions, taken only after a 7-day cooldown; each one runs the checks above. |

Every job runs on GitHub-hosted runners except CodeQL, Scorecard and `tag.yml`'s `github-release`, which run on the organisation's self-hosted runners: all three call the GitHub API (CodeQL to upload its results, Scorecard to read the repository, `github-release` to create the release), and the organisation's IP allow list only accepts those calls from there. CodeQL runs there for same-repository events only, so a pull request from a fork is analysed once merged; Scorecard never runs on a pull request, and `github-release` only for a version tag that passed the checks. Every action is pinned to a full commit SHA and every job declares least-privilege `permissions:`.

## Releasing

Development is trunk-based: every change lands on `main` through a pull request
with CI green. A Go module is published by its tag — the module proxy fetches it
on first request, and a published tag can never be moved or deleted (a bad one
is retracted in `go.mod` and superseded by a higher tag). So a release goes in
this order:

1. In one pull request, bump `Version` in `version.go`. For a final release,
   also move the `[Unreleased]` entries of CHANGELOG.md under a new
   `## [X.Y.Z] - YYYY-MM-DD` heading (the day you will push the tag), leave an
   empty `## [Unreleased]` above it, and update the link definitions at the end
   of the file: `[Unreleased]` compares `vX.Y.Z...HEAD`, and a new `[X.Y.Z]`
   line compares the previous tag with `vX.Y.Z`. A release candidate keeps its
   changes under `[Unreleased]`.
2. Once that pull request is merged, push the tag on its `main` commit:
   - `vX.Y.Z-rc.N` — a release candidate (`Version = "X.Y.Z-rc.N"`); Go treats it as a
     pre-release, so `go get …@latest` never selects it.
   - `vX.Y.Z` — a release (`Version = "X.Y.Z"`).
3. Watch the tag's `tag` workflow run: it checks the tag and creates the
   GitHub Release (below).

`.github/workflows/tag.yml` fails when a tag is malformed, not on `main`, or does
not match `version.go` — it cannot un-publish, but it tells you immediately.

When the tag passes, the workflow's `github-release` job creates its GitHub
Release, titled with the tag. The notes of a `vX.Y.Z` release are the
version's CHANGELOG.md section plus a link to the whole file, and the job fails
when the tagged commit's CHANGELOG.md has no such section; a `vX.Y.Z-rc.N` tag
becomes a pre-release with a fixed note that points at `[Unreleased]`. A re-run
for a tag that already has a GitHub Release leaves it as it is.
