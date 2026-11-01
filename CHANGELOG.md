<!-- SPDX-License-Identifier: Apache-2.0 -->
# Changelog

Format: [Keep a Changelog](https://keepachangelog.com/) · [SemVer](https://semver.org/).

## [Unreleased]

### Security

- Dependency: `golang.org/x/net` v0.62.0. Below it, the HTML parser's
  text-node coalescing was quadratic (GO-2026-6860), reachable through
  `html.Document`.

## [0.1.0] - 2026-11-01

The first release of the Go SDK (`github.com/scrape-do/sdo-go`, package `sdo`).
It requires Go 1.26 or newer.

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
- **Validation before any request** (`*ConfigError`): enum values matched
  case-insensitively (ASCII) and sent lowercase; millisecond ranges for
  `Timeout` (5000–120000), `RetryTimeout` (5000–55000) and `CustomWait`
  (0–35000); render-only fields require rendering; `Render`/`RenderMode`
  conflicts; `ExtraParams` may not set `token` or `url`, and its keys must match
  `^[A-Za-z][A-Za-z0-9_.-]*$` (the shape of every documented parameter); the
  target URL must be an absolute `http(s)` URL with a host and a valid port, with
  no whitespace, control character or backslash (it is sent exactly as given);
  request headers must be RFC 9110 tokens with printable-ASCII values, and
  `Host`, `Content-Length`, `Transfer-Encoding` and `Connection` cannot be set.
- **Wire format** identical to the other Scrape.do SDKs: RFC 3986 query
  encoding (space is `%20`), typed params in spec order then `ExtraParams`
  sorted, zero values omitted, and one canonical JSON encoding (sorted keys, no
  HTML escaping, ECMAScript numbers) for JSON params, JSON bodies and the job
  body. Header names compare case-insensitively and a caller's value replaces
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
  positive cost a status keeps its gateway meaning. `Billed` follows the cost
  header when present, else the documented table; `APIError.Cost()` returns
  the header, read like `Result.Cost()` (one value, ASCII digits, at most
  2^53-1). There is no 402/quota kind: an exhausted balance is a 401
  (`KindAuth`).
- **Retries**: a retryable status is retried with exponential backoff or
  `Retry-After` (whole seconds, capped at 30 s); POST, PUT, PATCH and DELETE
  only on 429. A failure before anything was sent (DNS, connect, TLS) is retried
  for idempotent methods; a failure after the request was sent (a read timeout,
  a dropped connection, a body that cannot be read or decoded, an oversized
  body) is never retried. A negative retry count is treated as 0.
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

### Changed

- `Do` refuses a rendered request (`Render`, or `RenderMode` "true")
  with any method other than GET with a `*ConfigError`, before anything is
  sent. The gateway answers such a request with a 400 ("JavaScript Render
  feature just works with 'GET' method type").

- **BREAKING.** `ScrapeConfig.BlockResources` is now `*bool` (was
  `bool`), so the field is tri-state. `nil` is unset and sends nothing, letting
  the gateway apply its default of `true` (resources blocked). `sdo.Bool(false)`
  now sends `blockResources=false` explicitly to *load* resources — this was
  impossible before, because a zero `false` was indistinguishable from unset and
  omitted, leaving resources wrongly blocked by the gateway default.
  `sdo.Bool(true)` sends `blockResources=true` and, being render-only, still
  requires rendering; `sdo.Bool(false)` does not. The new helper `sdo.Bool`
  takes the address of a bool literal for this field.
- **Behavior change.** `Scrape.do-Request-Cost` decides origin and billing.
  Any response of status 400 or above with a positive cost is the target's
  answer the gateway billed (its 400, 401, 403, 422, ...) and is now a
  `*Result`, like a target 404/410, and is never retried. The gateway's own
  errors carry no cost (or 0) and keep their kinds: its 400 (a parameter it
  rejected, such as `render=mert`) is `KindValidation`, its 401 `KindAuth`, both
  with `Billed` false — a 400 is no longer always billed. `Billed` follows the
  cost header when present and the documented status table otherwise. New
  `APIError.Cost()`. Code that caught every 4xx as an error should check
  `res.StatusCode` too.
- The error envelope's `Message` key is matched in any letter case, and an
  `error` key (any case) is the fallback, so plugin parameter errors
  (`{"error": "invalid_zipcode", "message": "..."}`) surface their message
  instead of the default.
- **Behavior change.** More documented parameter rules are checked before any
  request (`*ConfigError`), each one the gateway rejects with a 400 or documents
  as invalid: `ShowFrames`, `ShowWebsocketRequests` and the screenshot fields
  require `ReturnJSON`; at most one screenshot field; `ParticularScreenshot`
  excludes `PlayWithBrowser`; `RegionalGeoCode` requires `SuperProxy` and
  excludes `GeoCode`; `ZipCode` requires `SuperProxy` and `GeoCode`;
  `SessionID` must be an integer 0–1000000; `RetryTimeout` excludes rendering;
  at most one of `CustomHeaders`, `ExtraHeaders`, `ForwardHeaders`; `Height`
  and `Width` require rendering. `ReturnJSON` without rendering stays valid
  (the gateway wraps the plain response in JSON).
- **Behavior change.** A 510 is no longer retried and its default message is
  "The request was canceled by the HTTP client (HTTP 510)." The gateway answers
  510 only when the client cancelled the request before it finished; it is not
  an upstream failure.
- Field docs corrected: `PureCookies` returns the target's `Set-Cookie` header
  instead of `Scrape.do-Cookies` (the body is unchanged); `ReturnJSON` returns
  network requests and content as JSON; `RegionalGeoCode` does not
  override `GeoCode` (the two are mutually exclusive).
- **Behavior change.** The default client timeout is 70 s (was 60 s): the
  gateway's default `timeout` is 60 s, so a 60 s client deadline could give up
  just before the gateway's answer arrived. An explicit `WithTimeout` or
  `SDO_TIMEOUT` keeps its meaning. A plugin call whose `timeout` param is a
  positive integer now gets the same 10 s margin as `ScrapeConfig.Timeout`.

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
- CI: every job runs on GitHub-hosted runners, so code from a pull request
  never reaches the org's persistent self-hosted runners (only CodeQL runs
  there, for same-repository events; a fork's pull request is analysed once
  merged); every checkout drops its token
  (`persist-credentials: false`); and gitleaks scans with a config and ignore
  list from the workflow, so a pull request cannot allowlist its own secret with
  a `.gitleaks.toml` or `.gitleaksignore`.
- The User-Agent is exactly `sdo-sdk-go/<version>`; the SDK sends no telemetry.
