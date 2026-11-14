<!-- SPDX-License-Identifier: Apache-2.0 -->
# Migrating

This document describes how to upgrade `sdo-go` (`github.com/scrape-do/sdo-go`)
across versions. Each release that requires action from you gets its own section,
ordered newest first. Read every section between your current version and your
target version — they are cumulative.

## How `sdo-go` versions work

`sdo-go` follows [Semantic Versioning](https://semver.org/) and the
[Go module versioning](https://go.dev/ref/mod#versions) rules. The two interact
in ways that matter when you upgrade:

- **Patch (`0.1.0` → `0.1.1`)** — backward-compatible bug fixes only. Patch
  versions never break: upgrade with no source changes.
- **Minor (`0.1.0` → `0.2.0`)** — new functions, types, or fields. Before 1.0,
  a minor version may contain breaking changes; each is marked **Breaking** in
  CHANGELOG.md and described in a section below. From 1.0 on, a minor is
  backward-compatible: existing call sites keep compiling, and a deprecation
  introduced in one keeps working until a later major removes it.
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

These contracts are intentional and will not change without a migration section
here (and, from 1.0 on, a major version bump):

- **Token-only auth.** Authentication is always the `SDO_TOKEN` environment
  variable (or the equivalent explicit option passed to the client constructor).
  There is no other credential mechanism.
- **No telemetry.** `sdo-go` never phones home, never emits usage analytics, and
  has no opt-out to manage. Upgrades will not introduce any.
- **No UI.** This is an SDK. There is no bundled dashboard or web surface to
  account for when upgrading.
- **Shared behaviour.** The request parameters, defaults and observable
  behaviour are shared by all sdo SDKs and pinned by the conformance corpus
  (`conformance.json`), which Scrape.do maintains against
  https://scrape.do/documentation. A breaking change to that surface is marked
  **Breaking** in CHANGELOG.md and documented below.

## Governance and how breaking changes are decided

`sdo-go` is licensed under **Apache-2.0** and developed under **Solo+AI**
governance: the founder is the sole approver, and contributions are accepted via
**DCO sign-off** (`git commit -s`) — there is no CLA. Breaking changes are not
made casually. Before 1.0 one may ship in a minor version; from 1.0 on it
arrives as a new major version with a new `/vN` import path. Either way it gets
a dedicated migration section in this file describing the required source edits.

## Migrations

### 0.1.x → 0.2.0

- **Breaking: `AsyncJobs.List` takes options and returns a page.**
  `List(ctx)` is now `List(ctx, opts *ListJobsOptions)` and returns a
  `*JobPage` instead of `[]JobStatus`. The endpoint is paginated (10 jobs a
  page by default), so the old call silently returned only the first page.
  Pass `nil` for the API's defaults and read `.Jobs`; to see more, set `Page`
  and `PageSize` (1-100) and walk pages up to `TotalPages`.

  ```go
  // 0.1.x
  list, err := client.Jobs().List(ctx)
  for _, j := range list { /* ... */ }

  // 0.2.0
  page, err := client.Jobs().List(ctx, nil)
  for _, j := range page.Jobs { /* ... */ }
  ```

- **Breaking: `Create` refuses a `Plugin` option.** An options map that sets
  `Plugin` (in any letter case) is now a `*ConfigError` before anything is
  sent. Submit a plugin job with `CreatePlugin(ctx, key, params, options)`
  instead: `key` names the plugin (e.g. `amazon/pdp`) and each `params` entry
  becomes one task.
