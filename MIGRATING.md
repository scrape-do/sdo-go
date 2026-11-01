# Migrating

This document describes how to upgrade `sdo-go` (`github.com/scrape-do/sdo-go`)
across versions. Each release that requires action from you gets its own section,
ordered newest first. Read every section between your current version and your
target version — they are cumulative.

## How `sdo-go` versions work

`sdo-go` follows [Semantic Versioning](https://semver.org/) and the
[Go module versioning](https://go.dev/ref/mod#versions) rules. The two interact
in ways that matter when you upgrade:

- **Patch (`0.1.0` → `0.1.1`)** — backward-compatible bug fixes only. Safe to
  `go get -u` with no source changes.
- **Minor (`0.1.0` → `0.2.0`)** — backward-compatible additions. New functions,
  types, or fields. Existing call sites keep compiling. Deprecations may be
  introduced here (still working, documented, removed only in a later major).
- **Major (`1.x` → `2.0.0`)** — breaking changes. Under Go module semantics, any
  major version `v2` or higher **changes the module path**: `v2+` lives at a
  `/vN` suffix. The import path for the second major release is
  `github.com/scrape-do/sdo-go/v2`, the third is
  `github.com/scrape-do/sdo-go/v3`, and so on. `v0` and `v1` share the bare
  `github.com/scrape-do/sdo-go` path with no suffix.

### What a major upgrade looks like in Go

Because the module path changes, a major upgrade is an explicit, reviewable edit
rather than a silent `go get -u`. To move from `v1` to a hypothetical `v2`:

```bash
go get github.com/scrape-do/sdo-go/v2@v2.0.0
```

Then update every import in your code:

```go
// before
import "github.com/scrape-do/sdo-go"

// after
import sdo "github.com/scrape-do/sdo-go/v2"
```

The `/v2` suffix means `v1` and `v2` can coexist in the same build graph during a
gradual migration — they are, to the Go toolchain, two different modules. Migrate
package by package, then drop the old import once nothing references it.

## Stable across all versions

These contracts are intentional and will not change without a major version bump
and a migration section here:

- **Token-only auth.** Authentication is always the `SDO_TOKEN` environment
  variable (or the equivalent explicit option passed to the client constructor).
  There is no other credential mechanism.
- **No telemetry.** `sdo-go` never phones home, never emits usage analytics, and
  has no opt-out to manage. Upgrades will not introduce any.
- **No UI.** This is an SDK. There is no bundled dashboard or web surface to
  account for when upgrading.
- **Spec-driven surface.** The public API mirrors the hand-maintained `sdo`
  spec and is exercised against the cross-language conformance corpus. Breaking
  changes to that surface are gated behind a major version and documented below.

## Governance and how breaking changes are decided

`sdo-go` is licensed under **Apache-2.0** and developed under **Solo+AI**
governance: the founder is the sole approver, and contributions are accepted via
**DCO sign-off** (`git commit -s`) — there is no CLA. Breaking changes are not
made casually; when one ships, it arrives as a new major version with a dedicated
migration section in this file describing the new `/vN` import path and the
required source edits.

## Migrations

None yet: no version has been released, so the first release is the baseline.
Each later release that requires action from you gets its own section here.
