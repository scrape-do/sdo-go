<!-- SPDX-License-Identifier: Apache-2.0 -->
# Changelog

Format: [Keep a Changelog](https://keepachangelog.com/) · [SemVer](https://semver.org/).

Before 1.0, a minor version may contain breaking changes; each is marked **Breaking** and described in MIGRATING.md. Patch versions never break.

## [0.2.1] - 2026-11-15

### Added

- Each tag now gets a GitHub Release with its CHANGELOG section.

### Fixed

- The postal-code option is sent as the documented `zipcode` query parameter (it was sent as `zipCode`).

## [0.2.0] - 2026-11-03

### Added

- `AsyncJobs.CreatePlugin(ctx, key, params, options)`: plugin jobs on the
  async API (`{"Plugin": {"Key": ..., "Params": [...]}}`), one task per
  params entry, 1 to 1000 entries. A malformed key, an empty or oversized
  list, or options that set `Targets` or `Plugin` is a `*ConfigError`.
- `ListJobsOptions` (page, page size 1-100, status, start-time range, sort)
  and `JobPage` (`Jobs`, `TotalCount`, `PageSize`, `PageNumber`,
  `TotalPages`) for the paginated job list; `JobStatus*` and `JobSort*`
  constants for the documented values.
- `JobStatus.AcquiredConcurrency` and `JobStatus.LimitConcurrency`.

### Changed

- **Breaking:** `AsyncJobs.List(ctx)` is now `List(ctx, opts)` and returns a
  `*JobPage`. The endpoint is paginated (10 jobs a page by default), so the
  old signature silently returned only the first page while documenting
  "every visible job". Pass `nil` for the API's defaults and read `.Jobs`.
- **Breaking:** `Create` also refuses options that set `Plugin` (a
  `*ConfigError`); submit a plugin job with `CreatePlugin`.

### Security

- Dependency: `golang.org/x/net` v0.62.0. Below it, the HTML parser's
  text-node coalescing was quadratic (GO-2026-6860), reachable through
  `html.Document`.

## [0.1.0] - 2026-11-01

The first release of the Go SDK (`github.com/scrape-do/sdo-go`, package `sdo`).
It requires Go 1.26.9 or newer.

### Added

- **Client.** `New(token, ...Option)`; the token comes from the argument, else
  `SDO_TOKEN`, trimmed of surrounding whitespace and otherwise visible ASCII.
  Options: `WithBaseURL`, `WithAsyncBaseURL`, `WithTimeout`, `WithRetries`,
  `WithBackoff`, `WithHTTPClient`, `WithAcceptEncoding`, `WithPreferBrotli`,
  `WithMaxResponseBytes`, `WithAllowInsecureHTTP` and `WithUserAgent` (for tools
  built on the SDK; the value must be a Scrape.do client identifier).
  `SDO_BASE_URL`, `SDO_ASYNC_BASE_URL` and `SDO_TIMEOUT` (a decimal number of
  seconds, at least 0.001) apply when the matching option is not given. Invalid
  settings are a `*ConfigError` from `New`.
- **Scraping.** `Client.Scrape(ctx, *ScrapeConfig)` and
  `Client.Do(ctx, *ScrapeConfig, *Request)` for any method with a body
  (`Request.JSON` or `Request.Data`) and headers. `ScrapeConfig` covers the
  documented gateway parameters with an `ExtraParams` escape hatch.
  `ScrapeConfig.BlockResources` is a `*bool`: `nil` sends nothing, so the
  gateway applies its default of `true` (resources blocked); `sdo.Bool(false)`
  sends `blockResources=false` to load resources; `sdo.Bool(true)` sends
  `blockResources=true` and, being render-only, requires rendering. The helper
  `sdo.Bool` takes the address of a bool literal for this field. `PureCookies`
  returns the target's `Set-Cookie` header instead of `Scrape.do-Cookies`.
- **Validation before any request** (`*ConfigError`): enum values matched
  case-insensitively (ASCII) and sent lowercase; millisecond ranges for
  `Timeout` (5000–120000), `RetryTimeout` (5000–55000) and `CustomWait`
  (0–35000); render-only fields require rendering; `Render`/`RenderMode`
  conflicts; `ExtraParams` may not set `token` or `url`, and its keys must match
  `^[A-Za-z][A-Za-z0-9_.-]*$` (the shape of every documented parameter); the
  target URL must be an absolute `http(s)` URL with a host and a valid port, with
  no whitespace, control character or backslash (it is sent exactly as given);
  request headers must be RFC 9110 tokens with printable-ASCII values, and
  `Host`, `Content-Length`, `Transfer-Encoding` and `Connection` cannot be set;
  `Do` refuses a rendered request (`Render`, or `RenderMode` "true") with any
  method other than GET, which the gateway answers with a 400 ("JavaScript
  Render feature just works with 'GET' method type"). The documented parameter
  rules the gateway rejects with a 400 or documents as invalid are checked too:
  `ShowFrames`, `ShowWebsocketRequests` and the screenshot fields require
  `ReturnJSON`; at most one screenshot field; `ParticularScreenshot` excludes
  `PlayWithBrowser`; `RegionalGeoCode` requires `SuperProxy` and excludes
  `GeoCode`; `ZipCode` requires `SuperProxy` and `GeoCode`; `SessionID` must be
  an integer 0–1000000; `RetryTimeout` excludes rendering; at most one of
  `CustomHeaders`, `ExtraHeaders`, `ForwardHeaders`; `Height` and `Width`
  require rendering. `ReturnJSON` without rendering is valid (the gateway wraps
  the plain response in JSON).
- **Wire format** identical to the other Scrape.do SDKs: RFC 3986 query
  encoding (space is `%20`), typed params in a fixed order shared by every
  SDK, then `ExtraParams` sorted, zero values omitted, and one canonical JSON
  encoding (sorted keys, no HTML escaping, ECMAScript numbers) for JSON
  params, JSON bodies and the job body. Header names compare case-insensitively and a caller's value replaces
  the SDK's own `User-Agent` or `Accept-Encoding`.
- **`Result`**: `Bytes()`, `Text()` (the WHATWG Encoding Standard decode: BOM,
  `charset`, JSON as UTF-8, `<meta>` for HTML, then UTF-8; GBK and gb18030 per
  the WHATWG index), `JSON(v)` (UTF-8, BOM ignored, non-finite numbers rejected,
  any failure a `*TransportError`), `Header`, and accessors for the
  `Scrape.do-*` metadata headers (`Cost`, `RemainingCredits`,
  `InitialStatusCode`, `TargetURL`, `ResolvedURL`, `Cookies`,
  `TargetRedirectedLocation` — the target's `Location` under
  `DisableRedirection`).
- **`html` subpackage**: `html.Document(*sdo.Result)` returns a goquery document
  for CSS selection. It is separate so programs that do not parse HTML do not
  link goquery.
- **Transparent decompression** of `gzip`/`x-gzip` (every member), `deflate`
  (zlib or raw) and `br`, stacked codings decoded in reverse order; any other
  coding is returned as it is; an empty body is empty whatever its coding. Every
  stage is capped (`WithMaxResponseBytes`, 32 MiB by default); crossing it is a
  `*ResponseTooLargeError`, and a truncated or corrupt stream a
  `*TransportError`.
- **Errors**: `*APIError` (with `Kind`, `Billed`, `IsRetryable`, `Body`, and
  the `RetryAfter()` and `RemainingCredits()` accessors),
  `*TransportError`, `*ConfigError` and `*ResponseTooLargeError`. The message
  comes from the gateway's `{"Message": ...}` envelope (`message`, then
  `error`, either in any letter case, so a plugin's `{"error": ...,
  "message": ...}` reads too; the job API's `{"Error": ...}`) when there is
  one, else a fixed default per status. An error body is read up to 64 KiB; a
  larger, undecodable or interrupted one still yields the status error. Target
  404/410 pages, a target 400 the gateway billed (positive
  `Scrape.do-Request-Cost`), 3xx responses and, under `TransparentResponse`,
  any response the target produced itself are a `*Result`. So is any status
  of 400 or above the gateway billed (a positive `Scrape.do-Request-Cost`, the
  authoritative figure): the target's own 400, 401, 403, 422, .... Without a
  positive cost a status keeps its gateway meaning: the gateway's own 400 (a
  parameter it rejected, such as `render=maybe`) is `KindValidation` and its
  401 `KindAuth`, both with `Billed` false. `Billed` follows the cost
  header when present, else the documented table; `APIError.Cost()` returns
  the header, read like `Result.Cost()` (one value, ASCII digits, at most
  2^53-1). There is no 402/quota kind: an exhausted balance is a 401
  (`KindAuth`).
- **Retries**: a retryable status is retried with exponential backoff or
  `Retry-After` (whole seconds, capped at 30 s); POST, PUT, PATCH and DELETE
  only on 429. A failure before anything was sent (DNS, connect, TLS) is retried
  for idempotent methods; a failure after the request was sent (a read timeout,
  a dropped connection, a body that cannot be read or decoded, an oversized
  body) is never retried. A negative retry count is treated as 0. A 510 is not
  retried: the gateway answers it only when the client cancelled the request
  before it finished, so it is not an upstream failure; its default message is
  "The request was canceled by the HTTP client (HTTP 510)."
- **Timeouts**: the client timeout (70 s by default: the gateway's 60 s
  default plus 10 s) covers one whole attempt. When a `ScrapeConfig` sets the
  gateway `Timeout`, or a plugin call a `timeout` param, that request's
  attempts wait at least the gateway timeout plus 10 s. A timeout error names the deadline
  that expired; `errors.Is(err, context.Canceled)` and
  `context.DeadlineExceeded` work on transport errors.
- **Plugins**: `Client.Plugin(ctx, route, params)` returns parsed JSON or raw
  bytes; `Client.PluginResult(ctx, route, params)` returns the same response as
  a `*Result`, so its status and `Cost()` are not lost. The route `amazon` (or
  its documented spelling `amazon/`) calls the Amazon raw-HTML plugin at
  `/plugin/amazon/`, the path the gateway serves it under (it answers
  `/plugin/amazon` with a redirect the SDK never follows). Routes are validated; params are sent sorted, a list repeats its key,
  an object is canonical JSON; an empty key, `token` (any case), a key outside
  `^[A-Za-z][A-Za-z0-9_.-]*$` or a nested list is a `*ConfigError`.
- **Usage statistics**: `Client.Usage(ctx)` reads `GET /info` on the sync base
  URL into a `*Usage` (`IsActive`, `ConcurrentRequest`, `MaxMonthlyRequest`,
  `RemainingConcurrentRequest`, `RemainingMonthlyRequest`); a body without
  those fields is a `*TransportError`. The endpoint is rate-limited to 10
  requests per minute, so its error statuses (429 included) are not retried.
- **Async job API** (`Client.Jobs()`, `https://q.scrape.do`, `X-Token`
  authentication): `Create`, `Status`, `Result`, `List`, `Cancel` and `Me`,
  returning `*Job`, `*JobStatus`, `*TaskResult`, `[]JobStatus` and `*Account`.
  Ids are validated before they reach the URL. Responses decode leniently (a
  missing or mistyped field reads as its zero value; the types implement
  `json.Unmarshaler` accordingly); a 2xx body that is not a JSON object is a
  `*TransportError`; `Cancel` ignores the body; `Me` reads `AvailableCredits`
  and falls back to the API's `AvaliableCredits` spelling;
  `TaskResult.ContentBytes()` decodes base64 content. Job API errors are never
  billed.

### Security

- The token only travels over https, except to `localhost`, `127.0.0.1` and
  `::1` or with `WithAllowInsecureHTTP()`. A base URL may carry a path prefix
  but no query, fragment or credentials.
- Redirects are never followed on any path, even with a caller-supplied
  `http.Client`.
- The token is masked, literally and percent-encoded (upper- or lowercase hex),
  in every error message, retained error body and wrapped cause, together with
  credential-shaped values (`token=`, `api_key=`, `access_token=`, the same
  JSON fields, `X-Token:`). `errors.As` through an SDK error never hands back a
  cause whose text carries a credential; a `*url.Error` comes back as a
  redacted copy. Printing a `Client` or `AsyncJobs` never shows the token.
  `Client.Redact` and `RedactSecrets` apply the same masking to your own text.
- A target URL in a `*ConfigError` has its `user:password@` masked and its
  credential-shaped query values redacted.
- A body is decoded through at most five `Content-Encoding` codings (urllib3's
  limit); a longer chain is a `*TransportError`, never retried, so a target
  cannot make the SDK stack a decoder per listed coding.
- CI: code from a fork's pull request never reaches the organisation's
  persistent self-hosted runners: every job runs on GitHub-hosted runners
  except CodeQL (same-repository events only; a fork's pull request is
  analysed once merged) and Scorecard (never on a pull request); every
  checkout drops its token
  (`persist-credentials: false`); and gitleaks scans with a config and ignore
  list from the workflow, so a pull request cannot allowlist its own secret with
  a `.gitleaks.toml` or `.gitleaksignore`.
- The User-Agent is exactly `sdo-sdk-go/<version>`; the SDK sends no telemetry.

[Unreleased]: https://github.com/scrape-do/sdo-go/compare/v0.2.1...HEAD
[0.2.1]: https://github.com/scrape-do/sdo-go/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/scrape-do/sdo-go/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/scrape-do/sdo-go/releases/tag/v0.1.0
