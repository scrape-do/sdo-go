// SPDX-License-Identifier: Apache-2.0

package sdo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sync/atomic"
	"time"
)

// retryableStatus are the transient statuses. 510 is not one: the gateway
// answers 510 only when the HTTP client cancelled the request before the
// gateway finished it (the request's own context ended early), so retrying
// would re-send a request its caller abandoned.
var retryableStatus = map[int]bool{
	429: true, 502: true, 503: true, 504: true,
	520: true, 521: true, 522: true, 523: true, 524: true,
}

// billedStatus is the documented billing table, used only for a response
// without a readable Scrape.do-Request-Cost (the header is authoritative):
// 2xx, a target's 400, 404 and 410 are billed; the gateway's own 400, 401,
// 429, 502 and 510 are not. Of these, only 404 and 410 can be in an error
// position, and they are results; a target's 400 is told apart by its cost.
var billedStatus = map[int]bool{404: true, 410: true}

// nonIdempotent methods can have side effects on the target, so they are
// retried only on 429, where the gateway refused the request before contacting
// the target.
var nonIdempotent = map[string]bool{
	http.MethodPost: true, http.MethodPut: true, http.MethodPatch: true, http.MethodDelete: true,
}

const (
	// maxBackoff caps any single wait between attempts, Retry-After included.
	maxBackoff = 30 * time.Second
	// retainedBodyChars is how much of an error body an APIError keeps.
	retainedBodyChars = 2048
	// errorBodyReadBytes is how much of an error body is read (decoded) to
	// find the envelope. The status decides the error; the body only adds
	// detail, so a larger or unreadable body never replaces the status error.
	errorBodyReadBytes = 64 << 10
	// gatewayTimeoutMargin is added to ScrapeConfig.Timeout to get the least
	// time one attempt waits for the gateway.
	gatewayTimeoutMargin = 10 * time.Second
	// initialStatusHeader marks a response the target produced itself.
	initialStatusHeader = "Scrape.do-Initial-Status-Code"
	// requestCostHeader is the credits the gateway charged for the request.
	requestCostHeader = "Scrape.do-Request-Cost"
	// asyncAuthHeader carries the token to the job API.
	asyncAuthHeader = "X-Token"
)

// transport is the SDK's one HTTP stack. Scrape, plugin and async job calls all
// go through do, so each gets the same size cap, redirect refusal, retry
// policy, redaction and error taxonomy.
type transport struct {
	httpClient       *http.Client
	cred             *credential
	acceptEncoding   string
	retries          int
	backoff          time.Duration
	userAgent        string
	maxResponseBytes int64
	// wait sleeps between attempts; nil means a real timer (tests replace it).
	wait func(ctx context.Context, d time.Duration)
}

// credential keeps the token one pointer further from anything fmt can reach.
// A bad verb (%s on a struct) makes fmt print one level of pointee, so a Client
// embedded in a caller's struct would otherwise expose the transport's fields;
// with this indirection it shows an address.
type credential struct{ token string }

func (c *credential) redact(s string) string {
	if c == nil {
		return RedactSecrets(s)
	}
	return redact(s, c.token)
}

func (t *transport) token() string {
	if t.cred == nil {
		return ""
	}
	return t.cred.token
}

// outgoing is one logical request (retried as a unit).
type outgoing struct {
	method string
	url    string
	header http.Header
	body   []byte
	// jobAPI selects the job API's contract: the token rides in X-Token, every
	// non-2xx status is an error, and the error envelope key is "Error".
	jobAPI bool
	// transparent is ScrapeConfig.TransparentResponse: a response the target
	// produced itself is then a result, whatever its status.
	transparent bool
	// gatewayTimeoutMs is ScrapeConfig.Timeout (0 = unset).
	gatewayTimeoutMs int
	// noStatusRetry turns off retrying a retryable status (a failure before
	// anything was sent is still retried). For /info, whose 429 is a
	// per-minute rate limit that a retry within seconds only extends.
	noStatusRetry bool
}

// incoming is a delivered, decompressed response.
type incoming struct {
	status int
	header http.Header
	body   []byte
}

// defaultMaxResponseBytes is the ceiling for a response body, raw and
// decompressed (spec: decoding.max_response_bytes / max_decompressed_bytes).
const defaultMaxResponseBytes int64 = 32 << 20

func (t *transport) limit() int64 {
	if t.maxResponseBytes > 0 {
		return t.maxResponseBytes
	}
	return defaultMaxResponseBytes
}

func (t *transport) redact(s string) string { return t.cred.redact(s) }

// cause keeps err matchable with errors.Is (and, for credential-free errors,
// errors.As) while its printable text is redacted.
func (t *transport) cause(err error) error {
	return &redactedError{msg: t.redact(err.Error()), inner: err, cred: t.cred}
}

// effectiveTimeout is one attempt's deadline: the client timeout, raised to the
// gateway timeout plus a margin when the config sets one, so the client never
// abandons a request the gateway was told it may keep working on. 0 (no
// client timeout) stays 0.
func effectiveTimeout(client time.Duration, gatewayTimeoutMs int) time.Duration {
	if client <= 0 || gatewayTimeoutMs <= 0 {
		return client
	}
	// A plugin's timeout param is not range-checked: saturate, never overflow.
	if int64(gatewayTimeoutMs) > int64((math.MaxInt64-gatewayTimeoutMargin)/time.Millisecond) {
		return time.Duration(math.MaxInt64)
	}
	return max(client, time.Duration(gatewayTimeoutMs)*time.Millisecond+gatewayTimeoutMargin)
}

// do runs one logical request through the retry loop and returns the
// delivered response, or the error it maps to.
//
// Only a failure before anything was sent (DNS, connect, TLS, a connect
// timeout) is retried, and only for idempotent methods. Once the request may
// have reached the gateway — a read timeout, a connection dropped
// mid-response, a body that cannot be read or decoded — the gateway may still
// run and bill it, so the error is returned. A retryable status is retried per
// the method's policy.
func (t *transport) do(ctx context.Context, o outgoing) (*incoming, error) {
	if ctx == nil {
		return nil, &ConfigError{Message: "a non-nil context.Context is required"}
	}
	idempotent := !nonIdempotent[o.method]
	hc := t.httpClient
	timeout := effectiveTimeout(hc.Timeout, o.gatewayTimeoutMs)
	if timeout != hc.Timeout {
		per := *hc
		per.Timeout = timeout
		hc = &per
	}
	for attempt := 0; ; attempt++ {
		canRetry := attempt < t.retries
		var sent sendState
		req, err := t.newRequest(httptrace.WithClientTrace(ctx, sent.trace()), o)
		if err != nil {
			return nil, err
		}
		resp, err := hc.Do(req)
		if err != nil {
			if canRetry && idempotent && ctx.Err() == nil && !sent.mayHaveSent(err) {
				t.sleep(ctx, attempt, 0, false)
				continue
			}
			return nil, t.transportError(ctx, err, timeout)
		}
		if !isErrorResponse(o, resp.StatusCode, resp.Header) {
			return t.readResult(ctx, resp, timeout)
		}
		apiErr := t.readError(resp, o.jobAPI)
		if apiErr.IsRetryable && canRetry && !o.noStatusRetry &&
			(idempotent || apiErr.StatusCode == http.StatusTooManyRequests) {
			t.sleep(ctx, attempt, apiErr.retryAfter, apiErr.hasRetryAfter)
			continue
		}
		return nil, apiErr
	}
}

func (t *transport) newRequest(ctx context.Context, o outgoing) (*http.Request, error) {
	var body io.Reader
	if o.body != nil {
		body = bytes.NewReader(o.body)
	}
	req, err := http.NewRequestWithContext(ctx, o.method, o.url, body)
	if err != nil {
		return nil, &ConfigError{Message: t.redact(err.Error())}
	}
	req.Header.Set("User-Agent", t.userAgent)
	req.Header.Set("Accept-Encoding", t.acceptEncoding)
	// Keys are canonical, so a caller's value replaces the SDK's own.
	for k, v := range o.header {
		req.Header[k] = v
	}
	if o.jobAPI {
		req.Header.Set(asyncAuthHeader, t.token())
	}
	return req, nil
}

// sendState tells a failure before the request left from one after. With the
// standard transport the trace decides: nothing is written before a connection
// is obtained. A custom RoundTripper may not report the trace; then only a
// DNS or dial error counts as unsent.
type sendState struct{ traced, connected atomic.Bool }

func (s *sendState) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GetConn: func(string) { s.traced.Store(true) },
		GotConn: func(httptrace.GotConnInfo) { s.connected.Store(true) },
	}
}

func (s *sendState) mayHaveSent(err error) bool {
	if s.traced.Load() {
		return s.connected.Load()
	}
	var dnsErr *net.DNSError
	var opErr *net.OpError
	return !errors.As(err, &dnsErr) && !(errors.As(err, &opErr) && opErr.Op == "dial")
}

// isErrorResponse decides from the status line and headers alone whether a
// response is an error.
//
// Sync: 1xx–3xx, 404 and 410 are the target's page, not a gateway failure,
// and so is any status the gateway billed (targetAnswered). Under
// TransparentResponse, a response carrying Scrape.do-Initial-Status-Code is
// the target's own answer whatever its status; either condition alone is not
// enough. Async: every non-2xx status is an error, redirects included — they
// are never followed.
func isErrorResponse(o outgoing, status int, header http.Header) bool {
	if o.jobAPI {
		return status < 200 || status >= 300
	}
	if o.transparent && len(header.Values(initialStatusHeader)) > 0 {
		return false
	}
	if targetAnswered(header) {
		return false
	}
	return status >= 400 && status != http.StatusNotFound && status != http.StatusGone
}

// targetAnswered reports whether the gateway billed the response, which makes
// it the target's answer whatever its status. The gateway charges the target
// statuses it counts as delivered (400, 401, 404, 405 and more) and states the
// charge in Scrape.do-Request-Cost; its own errors — a parameter it could not
// parse (render=mert), a refused token, a blocked URL — are written before any
// cost is known and carry no cost header, and a failed fetch carries a cost of
// 0. So a positive cost means the target answered, and such a response is
// never retried: it was billed. Without a readable positive cost the status
// keeps its gateway meaning, so a gateway message is never passed off as the
// target's page.
func targetAnswered(header http.Header) bool {
	cost, ok := requestCost(header)
	return ok && cost > 0
}

// maxCost is the largest cost read: 2^53-1, the largest integer every SDK
// represents exactly, so all of them read the same header the same way.
const maxCost = 1<<53 - 1

// requestCost reads Scrape.do-Request-Cost: one value, ASCII digits after
// trimming spaces and tabs, at most maxCost. Anything else — a repeated
// header, a sign, a fraction — is no readable cost.
func requestCost(header http.Header) (int, bool) {
	cost, ok := headerInt(header, requestCostHeader, false)
	if !ok || cost > maxCost {
		return 0, false
	}
	return cost, true
}

// readResult drains and decodes a delivered body under the size cap. Nothing
// here is retried: the gateway has answered (and billed).
func (t *transport) readResult(ctx context.Context, resp *http.Response, timeout time.Duration) (*incoming, error) {
	defer resp.Body.Close()
	src := &errRecorder{r: resp.Body}
	raw := &capReader{r: src, limit: t.limit()}
	body, err := readCapped(decodingReader(raw, resp.Header.Get("Content-Encoding"), t.limit()), t.limit())
	if err == nil {
		return &incoming{status: resp.StatusCode, header: resp.Header, body: body}, nil
	}
	var tooLarge *ResponseTooLargeError
	switch {
	case errors.As(err, &tooLarge):
		return nil, tooLarge
	case src.err == nil:
		return nil, &TransportError{Message: "failed to decompress response", Err: t.cause(err)}
	case ctx.Err() != nil || isTimeout(src.err):
		return nil, t.timeoutError(ctx, src.err, timeout)
	default:
		return nil, &TransportError{Message: "failed to read response body", Err: t.cause(src.err)}
	}
}

// readError builds the APIError for an error status. Its body is read up to
// errorBodyReadBytes (decoded); a body that is larger, cannot be decoded or
// breaks off still yields the status error, with the default message.
func (t *transport) readError(resp *http.Response, jobAPI bool) *APIError {
	defer resp.Body.Close()
	raw := &capReader{r: resp.Body, limit: t.limit()}
	decoded := decodingReader(raw, resp.Header.Get("Content-Encoding"), t.limit())
	body, err := io.ReadAll(io.LimitReader(decoded, errorBodyReadBytes))
	if err != nil {
		body = nil
	}
	return t.newAPIError(resp.StatusCode, resp.Header, body, jobAPI)
}

// errRecorder remembers the last error the network read returned, so a decode
// failure can be told apart from a connection that broke off.
type errRecorder struct {
	r   io.Reader
	err error
}

func (e *errRecorder) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && err != io.EOF {
		e.err = err
	}
	return n, err
}

func (t *transport) sleep(ctx context.Context, attempt int, retryAfter time.Duration, useRetryAfter bool) {
	d := retryAfter
	if !useRetryAfter {
		d = time.Duration(math.Min(float64(t.backoff)*math.Pow(2, float64(attempt)), float64(maxBackoff)))
		if t.backoff > 0 {
			d += time.Duration(rand.Int63n(int64(100 * time.Millisecond)))
		}
	}
	d = min(d, maxBackoff)
	if d <= 0 {
		return
	}
	if t.wait != nil {
		t.wait(ctx, d)
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

// transportError turns a client-side failure into a TransportError with the
// token stripped.
//
// Go wraps request failures in *url.Error, whose Error() string embeds the full
// request URL — and the token rides in the query string. The cause is kept as a
// redactedError: its text is scrubbed and it cannot be unwrapped, but it still
// answers errors.Is (and errors.As for causes that carry no credential), so
// errors.Is(err, context.Canceled) and context.DeadlineExceeded keep working.
func (t *transport) transportError(ctx context.Context, err error, timeout time.Duration) *TransportError {
	if ctx.Err() != nil || isTimeout(err) {
		return t.timeoutError(ctx, err, timeout)
	}
	return &TransportError{Message: t.redact(fmt.Sprintf("connection failed: %v", err)), Err: t.cause(unwrapURLError(err))}
}

// timeoutError reports a deadline or a cancellation. A client timeout names
// the per-attempt deadline that expired.
func (t *transport) timeoutError(ctx context.Context, err error, timeout time.Duration) *TransportError {
	cause := t.cause(unwrapURLError(err))
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return &TransportError{Message: "request canceled", Err: cause}
	case ctx.Err() != nil:
		return &TransportError{Message: "request timed out: the context deadline passed", Timeout: true, Err: cause}
	}
	return &TransportError{
		Message: fmt.Sprintf("request timed out after %s (the client timeout for one attempt)", timeout),
		Timeout: true,
		Err:     cause,
	}
}

// unwrapURLError drops the *url.Error http.Client adds; its text is the
// request URL, token included, and it adds nothing a caller needs.
func unwrapURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err
	}
	return err
}

func isTimeout(err error) bool {
	var te interface{ Timeout() bool }
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &te) && te.Timeout())
}
