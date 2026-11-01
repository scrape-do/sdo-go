// SPDX-License-Identifier: Apache-2.0

package sdo

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL      = "https://api.scrape.do"
	defaultAsyncBaseURL = "https://q.scrape.do"
	// defaultTimeout outlasts the gateway's own default timeout (60s) by
	// gatewayTimeoutMargin, so the gateway's answer to a request that sets no
	// Timeout always arrives before the client gives up.
	defaultTimeout        = defaultGatewayTimeout + gatewayTimeoutMargin
	defaultGatewayTimeout = 60 * time.Second
	defaultAcceptEncoding = "gzip, deflate, br"
	defaultRetries        = 2
	defaultBackoff        = 500 * time.Millisecond
)

// userAgentRe is the only User-Agent shape a Scrape.do client may send.
var userAgentRe = regexp.MustCompile(`^sdo-(sdk-(python|js|go)|cli)/[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+|(a|b|rc)[0-9]+)?$`)

type clientConfig struct {
	baseURL           string
	asyncBaseURL      string
	acceptEncoding    string
	timeout           time.Duration
	timeoutSet        bool
	backoff           time.Duration
	retries           int
	httpClient        *http.Client
	preferBrotli      bool
	maxResponseBytes  int64
	allowInsecureHTTP bool
	userAgent         string
	err               error
}

// Option configures a Client.
type Option func(*clientConfig)

// WithBaseURL sets the sync API base (default https://api.scrape.do, or the
// SDO_BASE_URL environment variable). It must be https, unless the host is
// localhost, 127.0.0.1 or ::1, or WithAllowInsecureHTTP is given. "" means
// not given.
func WithBaseURL(u string) Option { return func(c *clientConfig) { c.baseURL = u } }

// WithAsyncBaseURL sets the async job API base (default https://q.scrape.do, or
// SDO_ASYNC_BASE_URL). The same https rule as WithBaseURL applies.
func WithAsyncBaseURL(u string) Option { return func(c *clientConfig) { c.asyncBaseURL = u } }

// WithTimeout sets the total deadline of one HTTP attempt: connect, headers
// and the whole body. The default is 70s (the gateway's 60s default timeout
// plus 10s), or SDO_TIMEOUT seconds. 0 disables it. When a ScrapeConfig sets
// the gateway Timeout (or a plugin call the "timeout" param), that request's
// attempts wait at least the gateway timeout plus 10s.
func WithTimeout(d time.Duration) Option {
	return func(c *clientConfig) { c.timeout, c.timeoutSet = d, true }
}

// WithRetries sets how many times a transient failure is retried (default 2).
// A negative count is treated as 0: the request is still sent once.
func WithRetries(n int) Option { return func(c *clientConfig) { c.retries = n } }

// WithBackoff sets the base of the exponential backoff between retries
// (default 500ms). A Retry-After header takes precedence; waits are capped at
// 30s.
func WithBackoff(d time.Duration) Option { return func(c *clientConfig) { c.backoff = d } }

// WithHTTPClient supplies the http.Client to send through (for proxies, mTLS
// or tests). The SDK uses a copy that never follows redirects, so a 3xx
// cannot carry the token elsewhere. Its Timeout is kept unless WithTimeout is
// given; when it is 0 the SDK's timeout applies.
func WithHTTPClient(h *http.Client) Option { return func(c *clientConfig) { c.httpClient = h } }

// WithAcceptEncoding overrides the advertised Accept-Encoding
// (default "gzip, deflate, br"). The value must be printable ASCII.
func WithAcceptEncoding(s string) Option {
	return func(c *clientConfig) {
		if err := checkHeader("Accept-Encoding", s); err != nil {
			c.err = err
			return
		}
		c.acceptEncoding = s
	}
}

// WithPreferBrotli advertises "br" alone so the gateway actually returns brotli.
func WithPreferBrotli() Option { return func(c *clientConfig) { c.preferBrotli = true } }

// WithMaxResponseBytes sets the ceiling for a decompressed response body
// (default 32 MiB). Exceeding it returns *ResponseTooLargeError instead of
// inflating the whole payload.
func WithMaxResponseBytes(n int64) Option {
	return func(c *clientConfig) { c.maxResponseBytes = n }
}

// WithAllowInsecureHTTP permits an http:// base URL on any host. Without it,
// plain http is accepted only for localhost, 127.0.0.1 and ::1, because the
// token travels with every request.
func WithAllowInsecureHTTP() Option { return func(c *clientConfig) { c.allowInsecureHTTP = true } }

// WithUserAgent replaces the User-Agent, for tools built on this SDK (the sdo
// CLI sends "sdo-cli/<version>"). The value must match
// ^sdo-(sdk-(python|js|go)|cli)/<semver>$; anything else makes New return a
// *ConfigError. The default is "sdo-sdk-go/<Version>".
func WithUserAgent(ua string) Option {
	return func(c *clientConfig) {
		if !userAgentRe.MatchString(ua) {
			c.err = &ConfigError{Message: fmt.Sprintf(
				"WithUserAgent: %q is not a Scrape.do client identifier (sdo-sdk-<lang>/<version> or sdo-cli/<version>)", ua)}
			return
		}
		c.userAgent = ua
	}
}

// Client is the Scrape.do client. It is safe for concurrent use.
type Client struct {
	baseURL      string
	asyncBaseURL string
	timeout      time.Duration
	// t holds the token (behind a further pointer, see credential), so
	// printing a Client through reflection never reaches it.
	t *transport
}

// New constructs a Client.
//
// Token precedence: the explicit argument, then SDO_TOKEN. Either is trimmed of
// ASCII whitespace ("" then means not given) and must otherwise be visible
// ASCII. The environment also supplies SDO_BASE_URL, SDO_ASYNC_BASE_URL and
// SDO_TIMEOUT (a decimal number of seconds, at least 0.001) when the matching
// option is not given. Invalid settings, from options or the environment,
// return a *ConfigError.
func New(token string, opts ...Option) (*Client, error) {
	cfg := clientConfig{
		retries:        defaultRetries,
		backoff:        defaultBackoff,
		acceptEncoding: defaultAcceptEncoding,
		// The only client-identifying header, and deliberately the whole of
		// it: no OS and no Go version.
		userAgent: "sdo-sdk-go/" + Version,
	}
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	if cfg.err != nil {
		return nil, cfg.err
	}
	token, err := normalizeToken(token, "the token")
	if err != nil {
		return nil, err
	}
	if token == "" {
		if token, err = normalizeToken(os.Getenv("SDO_TOKEN"), "SDO_TOKEN"); err != nil {
			return nil, err
		}
	}
	if token == "" {
		return nil, &ConfigError{Message: "No token provided. Pass New(token) or set SDO_TOKEN."}
	}
	baseURL, err := resolveBaseURL("WithBaseURL", cfg.baseURL, "SDO_BASE_URL", defaultBaseURL, cfg.allowInsecureHTTP)
	if err != nil {
		return nil, err
	}
	asyncBaseURL, err := resolveBaseURL("WithAsyncBaseURL", cfg.asyncBaseURL, "SDO_ASYNC_BASE_URL",
		defaultAsyncBaseURL, cfg.allowInsecureHTTP)
	if err != nil {
		return nil, err
	}
	timeout := cfg.timeout
	if !cfg.timeoutSet {
		if timeout, err = timeoutFromEnv(); err != nil {
			return nil, err
		}
	}
	ae := cfg.acceptEncoding
	if cfg.preferBrotli {
		ae = "br"
	}
	hc := httpClientFor(cfg.httpClient, timeout, cfg.timeoutSet)
	return &Client{
		baseURL:      baseURL,
		asyncBaseURL: asyncBaseURL,
		timeout:      hc.Timeout,
		t: &transport{
			httpClient:       hc,
			cred:             &credential{token},
			acceptEncoding:   ae,
			retries:          max(cfg.retries, 0),
			backoff:          cfg.backoff,
			userAgent:        cfg.userAgent,
			maxResponseBytes: cfg.maxResponseBytes,
		},
	}, nil
}

// httpClientFor returns the client the SDK sends through: a copy of the
// caller's (or a fresh one) that never follows a redirect. A 3xx is a scrape
// result or a job API error, and following it would carry the token to
// wherever it points — a caller's CheckRedirect cannot change that.
func httpClientFor(user *http.Client, timeout time.Duration, explicit bool) *http.Client {
	var hc http.Client
	if user != nil {
		hc = *user
	}
	if explicit || hc.Timeout == 0 {
		hc.Timeout = timeout
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &hc
}

// resolveBaseURL picks the explicit value, then the environment, then the
// default, and checks whichever it picked: scheme, host, optional port and
// optional path prefix (kept); https, or http on a loopback host (or with the
// opt-in). A query, a fragment, userinfo or a trailing "?" would corrupt the
// request URL the SDK appends to it, so each is a *ConfigError.
func resolveBaseURL(option, explicit, envVar, def string, allowInsecure bool) (string, error) {
	raw, source := explicit, option
	if raw == "" {
		raw, source = os.Getenv(envVar), envVar
	}
	if raw == "" {
		return def, nil
	}
	bad := func(why string) error {
		return &ConfigError{Message: fmt.Sprintf("%s: %s", source, why)}
	}
	if !strings.Contains(raw, "://") {
		return "", bad("not an absolute https URL")
	}
	scheme, authority, rest, ok := splitHTTPURL(raw)
	if !ok {
		if s, _, _ := strings.Cut(raw, "://"); lowerASCII(s) != "http" && lowerASCII(s) != "https" {
			return "", bad(fmt.Sprintf("scheme must be https, got %q", RedactSecrets(s)))
		}
		return "", bad("not an absolute https URL")
	}
	if strings.Contains(authority, "@") {
		return "", bad("a base URL may not carry credentials (user:password@)")
	}
	if strings.ContainsAny(rest, "?#") {
		return "", bad("a base URL may not have a query or a fragment")
	}
	host, ok := splitHostPort(authority)
	if !ok {
		return "", bad("not an absolute https URL with a host")
	}
	if scheme == "http" && !allowInsecure && !isLoopbackHost(host) {
		return "", bad(fmt.Sprintf(
			"%s is plain http and would send the token in cleartext; use https "+
				"(http is allowed for localhost, 127.0.0.1 and ::1, or with WithAllowInsecureHTTP)", raw))
	}
	return strings.TrimRight(raw, "/"), nil
}

func isLoopbackHost(host string) bool {
	return lowerASCII(host) == "localhost" || host == "127.0.0.1" || host == "::1"
}

// timeoutRe is the SDO_TIMEOUT grammar: a plain decimal number of seconds.
var timeoutRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)

// minEnvTimeout is the smallest SDO_TIMEOUT: anything shorter would round to
// a zero (disabled) timeout.
const minEnvTimeout = time.Millisecond

// timeoutFromEnv reads SDO_TIMEOUT: a decimal number of seconds, at least
// 0.001. Unset or empty means the 70s default.
func timeoutFromEnv() (time.Duration, error) {
	raw := os.Getenv("SDO_TIMEOUT")
	if raw == "" {
		return defaultTimeout, nil
	}
	secs, err := strconv.ParseFloat(raw, 64)
	if !timeoutRe.MatchString(raw) || err != nil || secs < minEnvTimeout.Seconds() {
		return 0, &ConfigError{Message: fmt.Sprintf(
			"SDO_TIMEOUT must be a decimal number of seconds, at least 0.001 (for example 30 or 1.5), got %q", raw)}
	}
	if secs >= float64(math.MaxInt64)/float64(time.Second) {
		return time.Duration(math.MaxInt64), nil
	}
	return time.Duration(math.Round(secs * float64(time.Second))), nil
}

// normalizeToken trims ASCII whitespace; "" means not given. What remains
// must be visible ASCII: a stray newline from a secret file would otherwise go
// on the wire and fail every request. The value is never echoed.
func normalizeToken(raw, source string) (string, error) {
	tok := strings.Trim(raw, " \t\n\v\f\r")
	for i := 0; i < len(tok); i++ {
		if tok[i] < 0x21 || tok[i] > 0x7e {
			return "", &ConfigError{Message: source +
				" contains whitespace, a control character or non-ASCII; a token is visible ASCII only"}
		}
	}
	return tok, nil
}

// String describes the client without its token, so logging a Client or a
// copy of one (with %v, %+v, %s or %q) cannot leak the credential.
func (c Client) String() string {
	return fmt.Sprintf("sdo.Client{BaseURL: %q, AsyncBaseURL: %q, Token: %q}", c.baseURL, c.asyncBaseURL, redactMask)
}

// GoString is String, for %#v.
func (c Client) GoString() string { return c.String() }

// Redact masks this client's token (literally and percent-encoded) and every
// credential shape RedactSecrets knows in text. Use it on anything derived from
// a response before it leaves your process (logs, an LLM's context).
func (c *Client) Redact(text string) string { return c.t.redact(text) }

// Scrape fetches a URL with a GET and returns a *Result. It is Do(ctx, config,
// nil).
func (c *Client) Scrape(ctx context.Context, config *ScrapeConfig) (*Result, error) {
	return c.Do(ctx, config, nil)
}

// Do scrapes config.URL with the method, body and headers in req (nil is a
// plain GET).
//
// Retries follow the method: GET, HEAD and OPTIONS retry a retryable status
// and a failure before anything was sent (DNS, connect, TLS); POST, PUT, PATCH
// and DELETE, which can have side effects on the target, retry only a 429 (the
// gateway refused before contacting the target). A failure after the request
// was sent (a read timeout, a dropped connection, a body that cannot be read
// or decoded) is never retried: the gateway may still run and bill it.
func (c *Client) Do(ctx context.Context, config *ScrapeConfig, req *Request) (*Result, error) {
	if config == nil {
		return nil, &ConfigError{Message: "a *ScrapeConfig is required, got nil"}
	}
	params, err := config.toParams()
	if err != nil {
		return nil, err
	}
	method, body, contentType, err := req.prepare()
	if err != nil {
		return nil, err
	}
	// The gateway renders GET requests only and answers any other method with
	// a 400 ("JavaScript Render feature just works with 'GET' method type").
	if render, _ := config.renderValue(); render != "" && render != "false" && method != http.MethodGet {
		return nil, &ConfigError{Message: fmt.Sprintf("render works with GET requests only; got %s", method)}
	}
	var callerHeaders map[string]string
	if req != nil {
		callerHeaders = req.Headers
	}
	header, err := mergeHeaders(config.Headers, callerHeaders)
	if err != nil {
		return nil, err
	}
	if contentType != "" && header.Get("Content-Type") == "" {
		header.Set("Content-Type", contentType)
	}
	query := encodeQuery(append([]param{{"token", c.t.token()}}, params...))
	in, err := c.t.do(ctx, outgoing{
		method:           method,
		url:              c.baseURL + "/?" + query,
		header:           header,
		body:             body,
		transparent:      config.TransparentResponse,
		gatewayTimeoutMs: config.Timeout,
	})
	if err != nil {
		return nil, err
	}
	return &Result{content: in.body, StatusCode: in.status, Header: in.header, URL: config.URL}, nil
}

// Plugin calls a documented plugin endpoint generically. Returns parsed JSON
// (as any) or raw bytes for non-JSON plugins. PluginResult returns the same
// response with its status and headers (cost included).
//
// route is the path under /plugin/, e.g. "google/shopping"; "amazon" (or
// "amazon/") is the Amazon raw-HTML plugin at /plugin/amazon/.
func (c *Client) Plugin(ctx context.Context, route string, params map[string]any) (any, error) {
	return callPlugin(ctx, c.t, c.baseURL, route, params)
}

// PluginResult calls a plugin like Plugin but returns the delivered response
// as a *Result: its status, its headers (Cost, RemainingCredits)
// and its body (Result.JSON parses it). Result.URL is "".
func (c *Client) PluginResult(ctx context.Context, route string, params map[string]any) (*Result, error) {
	return pluginResult(ctx, c.t, c.baseURL, route, params)
}

// Jobs returns the async (q.scrape.do) job API. It shares the client's
// transport: the same timeout, size cap, retry policy and redaction.
func (c *Client) Jobs() *AsyncJobs {
	return &AsyncJobs{t: c.t, baseURL: c.asyncBaseURL}
}
