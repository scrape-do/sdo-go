<!-- SPDX-License-Identifier: Apache-2.0 -->
# sdo — Go SDK for Scrape.do

The official Go SDK for the [Scrape.do](https://scrape.do) web-scraping API.
Token-only auth, transparent compression, a typed result, and a generic plugin
core — no telemetry, no UI.

```bash
go get github.com/scrape-do/sdo-go
```

## Quickstart

```go
package main

import (
	"context"
	"fmt"

	sdo "github.com/scrape-do/sdo-go"
)

func main() {
	// Token precedence: New("TOKEN") > SDO_TOKEN env var.
	client, err := sdo.New("YOUR_TOKEN")
	if err != nil {
		panic(err)
	}

	res, err := client.Scrape(context.Background(), &sdo.ScrapeConfig{
		URL:     "https://example.com",
		Render:  true,
		GeoCode: "us",
	})
	if err != nil {
		panic(err)
	}
	cost, _ := res.Cost()
	fmt.Println(res.StatusCode, cost)
	text := res.Text()
	fmt.Println(text[:min(200, len(text))]) // the first 200 bytes, or fewer
}
```

Go 1.26 or newer is required.

## Method, body and headers

`Scrape(ctx, cfg)` is a GET. `Do` sends any method through the gateway:

```go
res, err := client.Do(ctx, &sdo.ScrapeConfig{URL: "https://example.com/api"}, &sdo.Request{
	Method:  "PUT",                       // default GET, or POST when a body is given
	JSON:    map[string]any{"name": "x"}, // or Data: []byte("a=1&b=2")
	Headers: map[string]string{"Accept": "application/json"},
})
```

A `JSON` body is sent as compact JSON with sorted keys and `Content-Type:
application/json` (unless you set one). `GET`/`HEAD` with a body, or `JSON` and
`Data` together, are a `*sdo.ConfigError`.

Header names are case-insensitive, and your value replaces the SDK's own
(`User-Agent`, `Accept-Encoding`). A name must be an RFC 9110 token and a value
printable ASCII (space and tab allowed); `Host`, `Content-Length`,
`Transfer-Encoding` and `Connection` cannot be set. Anything else is a
`*sdo.ConfigError` before the request is sent.

## Retries and timeouts

A retryable status (429, 502, 503, 504, 520–524) is retried with
exponential backoff (or `Retry-After`, capped at 30 s); POST, PUT, PATCH and
DELETE only on 429, so a request with side effects is never repeated after the
target saw it. A failure before anything was sent (DNS, connect, TLS) is
retried for GET, HEAD and OPTIONS. A failure after the request was sent — a read
timeout, a dropped connection, a body that cannot be decompressed — is never
retried: the gateway may still be running (and billing) it. A 510 is the
gateway's record of a request whose HTTP client cancelled it before it finished;
it is not an upstream failure and is not retried.

The client timeout covers one whole attempt. Its default, 70 s, is the
gateway's own default timeout (60 s) plus 10 s, so the gateway's answer always
arrives first. When a `ScrapeConfig` sets the gateway `Timeout` (or a plugin
call passes a `timeout` param), that request's attempts wait at least the
gateway timeout plus 10 s, so the client never gives up on a request the
gateway was allowed to take longer for. A timeout error names the deadline that
expired.

## Errors

Successful scrapes — including target 404/410 pages, any status the gateway
billed and 3xx responses — return a `*Result` (check `res.StatusCode`;
`res.Header` holds every header, repeated ones included). Only gateway-origin
failures return an error.

`Scrape.do-Request-Cost` is the authoritative billing figure. A response with
status 400 or above that carries a positive cost is the target's own answer
(its 400, 401, 403, 422, ...): a `*Result`, never retried. Without one, the
status keeps its gateway meaning: the gateway's own 400 (a parameter it
rejected, such as `render=mert`) is `KindValidation`, its 401 (a refused token,
no credits) `KindAuth`, and so on. `APIError.Billed` follows the cost header
when it is present (0 is not billed) and the documented status table
otherwise; `APIError.Cost()` and `Result.Cost()` return the header:

```go
var apiErr *sdo.APIError
if errors.As(err, &apiErr) {
	switch apiErr.Kind {
	case sdo.KindConcurrency: // 429, retryable
	case sdo.KindAuth:        // 401/403 — a bad token or no credits left
	}
}
```

With `TransparentResponse: true`, a response the target produced itself
(marked by `Scrape.do-Initial-Status-Code`) is always a `*Result`, whatever its
status, and is never retried. Invalid options are a `*sdo.ConfigError` before
any request: unknown enum values (matched case-insensitively), out-of-range
millisecond values (`Timeout` 5000–120000, `RetryTimeout` 5000–55000,
`CustomWait` 0–35000), render-only fields (`Height` and `Width` included)
without rendering, and a target URL that is not absolute `http(s)` (its
`user:password@` is masked in the message).

The documented parameter dependencies are checked too, each one the gateway
rejects (or documents as invalid), so a mistake costs nothing:

| Rule | Why |
|---|---|
| `ShowFrames`, `ShowWebsocketRequests` and every screenshot field need `ReturnJSON` (and rendering) | their output is part of the JSON response |
| at most one of `Screenshot`, `FullScreenshot`, `ParticularScreenshot` | the gateway takes one |
| `ParticularScreenshot` cannot be combined with `PlayWithBrowser` | use a `ScreenShot` action instead |
| `RegionalGeoCode` needs `SuperProxy` and excludes `GeoCode` | the gateway rejects both |
| `ZipCode` needs `SuperProxy` and `GeoCode` | without super it is silently ignored |
| `SessionID` is an integer 0–1000000 (decimal digits) | the documented range |
| `RetryTimeout` cannot be combined with rendering | the gateway rejects it |
| at most one of `CustomHeaders`, `ExtraHeaders`, `ForwardHeaders` | the gateway rejects any two |

`ReturnJSON` on its own is allowed: without rendering the gateway wraps the plain
response in the same JSON envelope.

## Configuration

| Option | Environment | Default |
|---|---|---|
| `New(token)` | `SDO_TOKEN` | — (required) |
| `WithBaseURL` | `SDO_BASE_URL` | `https://api.scrape.do` |
| `WithAsyncBaseURL` | `SDO_ASYNC_BASE_URL` | `https://q.scrape.do` |
| `WithTimeout` | `SDO_TIMEOUT` (seconds) | 70 s, total per attempt |

An option wins over the environment; an empty value means "not given". A token
is trimmed of surrounding whitespace (a secret file's trailing newline is
fine) and must otherwise be visible ASCII. `SDO_TIMEOUT` is a decimal number of
seconds, at least `0.001` (`30`, `1.5`). Base URLs must be `https` — the token
travels with every request — except for `localhost`, `127.0.0.1` and `::1`;
`sdo.WithAllowInsecureHTTP()` lifts that for other hosts. A base URL may carry
a path prefix (`https://gw.example/pfx`), but no query, fragment or
credentials. Invalid values are a `*sdo.ConfigError` from `New`. Redirects are
never followed, even with your own `WithHTTPClient`.

Error messages, retained error bodies and wrapped causes have the token and any
credential-shaped value masked, and printing a `Client` never shows the token.
`errors.As` through an SDK error hands back a `*url.Error` as a redacted copy and
never returns a cause whose text carries a credential. `client.Redact(text)`
applies the same masking to your own output.

## Compression

Every request sends `Accept-Encoding: gzip, deflate, br` and the SDK decompresses
the response transparently (`res.Bytes()` is decompressed): `gzip`/`x-gzip`,
`deflate` and `br`, stacked codings included; any other coding is returned as
it is. Decompression is capped (32 MiB by default, `WithMaxResponseBytes`). Use
`sdo.WithPreferBrotli()` to advertise `br` alone.

## Text, JSON and HTML

`res.Text()` decodes the body as a browser would (WHATWG Encoding Standard:
BOM, `charset`, `<meta>`, then UTF-8). `res.JSON(&v)` parses it as UTF-8 and
returns a `*sdo.TransportError` for anything that is not JSON. For CSS
selection, the `html` subpackage returns a goquery document (a separate
package, so programs that do not parse HTML do not link goquery):

```go
import sdohtml "github.com/scrape-do/sdo-go/html"

doc, err := sdohtml.Document(res)
title := doc.Find("h1").First().Text()
```

## Plugins

```go
// Google plugins take Google's own locale params: gl (country), hl (language).
data, err := client.Plugin(ctx, "google/shopping", map[string]any{"q": "laptop", "gl": "us"})

// PluginResult returns the same response with its status and headers.
res, err := client.PluginResult(ctx, "google/search", map[string]any{"q": "laptop", "gl": "us"})
cost, _ := res.Cost()
var serp map[string]any
err = res.JSON(&serp)

// The Amazon raw-HTML plugin lives at /plugin/amazon/; the route "amazon"
// (or "amazon/") calls it.
html, err := client.Plugin(ctx, "amazon", map[string]any{
	"url": "https://www.amazon.com/dp/B0C7BKZ883", "geocode": "us", "zipcode": "10001",
})
```

Params are sent sorted; a list repeats its key, an object is sent as compact
JSON. An empty key, the key `token` (the client supplies it), a key that is
not an ASCII letter followed by letters, digits, `_`, `.` or `-`, or a list
inside a list is a `*sdo.ConfigError`. `ScrapeConfig.ExtraParams` keys follow
the same shape rule.

## Usage statistics

```go
u, err := client.Usage(ctx) // GET https://api.scrape.do/info; no credits
fmt.Println(u.IsActive, u.RemainingMonthlyRequest, u.RemainingConcurrentRequest)
```

`Usage` returns `IsActive`, `ConcurrentRequest`, `MaxMonthlyRequest`,
`RemainingConcurrentRequest` and `RemainingMonthlyRequest`. The endpoint allows
10 requests per minute per IP and answers 429 above that; the SDK does not retry
its error statuses (a retry within seconds would only spend more of the same
minute). An unknown token is answered with a 502 "authentication refused".

## Async jobs

Asynchronous scraping is the REST job API on `https://q.scrape.do`, reached via
`client.Jobs()`. Every request authenticates with the `X-Token` header (never a
`token=` query) and returns typed values. The `callback` option was removed —
poll a job's status and pull each task's result instead.

```go
jobs := client.Jobs()

// Create a job for one or more targets. The options map uses the job API's own
// PascalCase body keys (e.g. Super, GeoCode, Render, Output) verbatim.
job, err := jobs.Create(ctx, []string{"https://example.com"},
	map[string]any{"Super": true, "GeoCode": "us"})

status, err := jobs.Status(ctx, job.JobID)          // job + per-task state
task, err := jobs.Result(ctx, job.JobID, "TASK_ID") // one task's delivered content
body, err := task.ContentBytes()                     // base64-decoded for binary targets
all, err := jobs.List(ctx)                           // every visible job
err = jobs.Cancel(ctx, job.JobID)                    // cancel a job
acct, err := jobs.Me(ctx)                            // concurrency + credits snapshot
```

Job and task ids are validated (`^[A-Za-z0-9_-]{1,128}$`) before they reach the
URL. Job calls share the client's transport — timeout, size cap, retry policy,
redaction. Any non-2xx job response, a redirect included, is a
`*sdo.APIError` whose message comes from the API's `{"Error": ...}` body (job
API errors are never billed). Responses are decoded leniently: a missing or
mistyped field reads as its zero value instead of failing the call, and a 2xx
body that is not a JSON object is a `*sdo.TransportError`. `Cancel` ignores the
response body.

## CI/CD workflows

| Workflow | Runs on | What it does |
|----------|---------|--------------|
| `ci.yml` | every push to `main` and every pull request | `go vet` and the race-enabled test suite on Go 1.26 and 1.27 (Linux), plus macOS and Windows on 1.27, including the cross-language conformance suite and its integrity check; a `quality` job for `gofmt` and the 99% statement-coverage floor; `govulncheck`; the public-API snapshot (`api-surface.txt`) must be up to date; CodeQL code scanning. Everything runs on GitHub-hosted runners; CodeQL uploads its results from the org's self-hosted runners, and a pull request from a fork keeps its SARIF as a build artifact instead. |
| `security.yml` | push to `main`, pull requests, and weekly | secret scanning over the full git history (gitleaks), OSV-Scanner over `go.mod`/`go.sum`, and an SBOM uploaded as a build artifact. OpenSSF Scorecard runs on pushes to `main` and weekly, with results in code scanning. |
| `tag.yml` | a version tag only | A Go module is published by its tag (the module proxy fetches it), so there is no publish job. This workflow checks that the tag is `vX.Y.Z` or `vX.Y.Z-rc.N`, is on `main`, and matches `version.go`. A `-rc.N` tag is a Go pre-release, so `@latest` never selects it. |
| Dependabot | weekly | update pull requests for Go modules and the GitHub Actions, taken only after a 7-day cooldown; each one runs the checks above. |

Every action is pinned to a full commit SHA and every job declares least-privilege `permissions:`. Publishing never happens outside the `scrape-do` organisation, never from a commit that is not on `main`, and never without a maintainer's approval.

## Responsible use

You are responsible for complying with the terms of service, `robots.txt`, and
applicable laws of any site you scrape. Third-party names are trademarks of their
respective owners; Scrape.do is not affiliated with them.

## License

[Apache-2.0](LICENSE).
