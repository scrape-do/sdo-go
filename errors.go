// SPDX-License-Identifier: Apache-2.0

package sdo

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrorKind classifies a gateway-origin API error. It maps to the shared
// cross-language taxonomy (Auth/Validation/Concurrency/Gateway).
//
// There is no quota kind: the gateway has no 402 tier and returns 401 for an
// exhausted credit balance too, so that case is KindAuth and is told apart by
// the message rather than the status code.
type ErrorKind int

const (
	KindOther ErrorKind = iota
	KindAuth
	KindValidation
	KindConcurrency
	KindGateway
)

func (k ErrorKind) String() string {
	switch k {
	case KindAuth:
		return "auth"
	case KindValidation:
		return "validation"
	case KindConcurrency:
		return "concurrency"
	case KindGateway:
		return "gateway"
	default:
		return "other"
	}
}

// redactMask replaces every credential the SDK scrubs.
const redactMask = "***"

// literalTokenMinLen keeps a short (test) token from shredding ordinary text
// when it is matched literally.
const literalTokenMinLen = 8

// ci spells an ASCII word as a case-insensitive character class sequence.
// (?i) is not used: its Unicode folding would also match a Kelvin sign for k.
func ci(word string) string {
	var b strings.Builder
	for _, c := range word {
		if lower := strings.ToLower(string(c)); lower != strings.ToUpper(string(c)) {
			b.WriteString("[" + strings.ToUpper(string(c)) + lower + "]")
		} else {
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return b.String()
}

// credentialNames is token|api[-_]?key|access[-_]?token, ASCII case-insensitive.
var credentialNames = ci("token") + "|" + ci("api") + "[-_]?" + ci("key") + "|" + ci("access") + "[-_]?" + ci("token")

// asciiSpace is Python's ASCII \s. Go's \s lacks \v.
const asciiSpace = `\t\n\v\f\r `

// secretShapes match credential-shaped values from any source, in order: a
// query or prose "token=VALUE", a JSON "token": "VALUE" field, and an X-Token
// header echo. \b is an ASCII word boundary, so "mytoken=" is not a match.
var secretShapes = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`\b((?:` + credentialNames + `)=)[^&` + asciiSpace + `"']+`), "${1}" + redactMask},
	{regexp.MustCompile(`("(?:` + credentialNames + `)"[` + asciiSpace + `]*:[` + asciiSpace + `]*")[^"]*(")`), "${1}" + redactMask + "${2}"},
	{regexp.MustCompile(`\b(` + ci("x-token") + `[` + asciiSpace + `]*:[` + asciiSpace + `]*)[^` + asciiSpace + `,;]+`), "${1}" + redactMask},
}

// RedactSecrets masks credential-shaped values in text: token=, api_key= and
// access_token= values (in a URL or in prose), the same names as JSON string
// fields, and an X-Token header value.
//
// It works by shape, so it also covers URLs the SDK never built (echoed by a
// proxy, or embedded in a *url.Error). Client.Redact additionally masks the
// client's own token wherever it appears.
func RedactSecrets(text string) string {
	for _, s := range secretShapes {
		text = s.re.ReplaceAllString(text, s.repl)
	}
	return text
}

// redact masks the literal token (and its percent-encoded wire form, with
// upper- or lowercase hex) when it is long enough to match safely, then every
// credential shape. It is applied to every error message, retained error body
// and wrapped cause.
func redact(text, token string) string {
	if utf8.RuneCountInString(token) >= literalTokenMinLen {
		text = strings.ReplaceAll(text, token, redactMask)
		if enc := pctEncode(token); enc != token {
			text = strings.ReplaceAll(text, enc, redactMask)
			// A proxy or server may echo the escapes in lowercase.
			if lower := lowerPctHex(enc); lower != enc {
				text = strings.ReplaceAll(text, lower, redactMask)
			}
		}
	}
	return RedactSecrets(text)
}

// lowerPctHex lowercases the hex digits of every %XX escape in a pctEncode
// result, leaving the literal characters as they are.
func lowerPctHex(enc string) string {
	b := []byte(enc)
	for i := 0; i+2 < len(b); i++ {
		if b[i] == '%' {
			b[i+1] = toLowerASCII(b[i+1])
			b[i+2] = toLowerASCII(b[i+2])
			i += 2
		}
	}
	return string(b)
}

// ConfigError is returned for client-side misconfiguration (no HTTP call made),
// e.g. a missing token or mutually exclusive options.
type ConfigError struct{ Message string }

func (e *ConfigError) Error() string { return e.Message }

// TransportError is returned when no usable HTTP response arrived: DNS, TCP,
// TLS, timeouts, a body that broke off or could not be decompressed, or a
// success body that was not the JSON the API promised. Timeout reports whether
// the cause was a deadline (the client timeout or the context's).
//
// Err holds the cause with credentials already redacted: the raw *url.Error Go
// produces embeds the request URL — and therefore the token — so wrapping it
// unchanged would leak through errors.Unwrap and %+v as well as Error().
type TransportError struct {
	Message string
	Timeout bool
	Err     error
}

func (e *TransportError) Error() string { return e.Message }
func (e *TransportError) Unwrap() error { return e.Err }

// redactedError carries a failure's cause with credentials stripped from its
// text, while still answering errors.Is about the original.
//
// It deliberately has no Unwrap: walking the chain and printing each link is an
// ordinary thing for a caller to do, and the original error (or anything below
// it) may embed the request URL — a custom RoundTripper's message, for one.
// Is delegates. As delegates only when what it would hand back carries no
// credential in its text (or in anything it wraps); a *url.Error is handed back
// as a redacted copy; anything else that carries one is not handed back.
type redactedError struct {
	msg   string
	inner error
	cred  *credential
}

func (e *redactedError) Error() string { return e.msg }

// Is reports whether the original cause matches target.
func (e *redactedError) Is(target error) bool { return e.inner != nil && errors.Is(e.inner, target) }

// As finds the first error in the original cause's chain that matches target,
// and sets target only to a value whose text is credential-free.
func (e *redactedError) As(target any) bool {
	if e.inner == nil || !errors.As(e.inner, target) {
		return false
	}
	slot := reflect.ValueOf(target).Elem()
	found, ok := slot.Interface().(error)
	if !ok || found == nil || e.clean(found) {
		return true
	}
	if u, isURL := found.(*url.Error); isURL {
		safe := reflect.ValueOf(e.redactURLError(u))
		if safe.Type().AssignableTo(slot.Type()) {
			slot.Set(safe)
			return true
		}
	}
	slot.Set(reflect.Zero(slot.Type()))
	return false
}

// clean reports whether err and everything it wraps print without a
// credential.
func (e *redactedError) clean(err error) bool {
	if err == nil {
		return true
	}
	if _, ours := err.(*redactedError); ours {
		return true
	}
	if text := err.Error(); e.cred.redact(text) != text {
		return false
	}
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		return e.clean(u.Unwrap())
	case interface{ Unwrap() []error }:
		for _, inner := range u.Unwrap() {
			if !e.clean(inner) {
				return false
			}
		}
	}
	return true
}

func (e *redactedError) redactURLError(u *url.Error) *url.Error {
	safe := &url.Error{Op: u.Op, URL: e.cred.redact(u.URL)}
	if u.Err != nil {
		safe.Err = &redactedError{msg: e.cred.redact(u.Err.Error()), inner: u.Err, cred: e.cred}
	}
	return safe
}

// ResponseTooLargeError is returned when a response exceeds the client's size
// ceiling.
//
// Transparent decompression is an attack surface: the scraped target controls
// the body, so a small compressed payload can inflate without bound. Reading and
// inflating are both capped and abort as soon as the ceiling is crossed, before
// the full buffer is materialized.
type ResponseTooLargeError struct {
	Limit int64
}

func (e *ResponseTooLargeError) Error() string {
	return fmt.Sprintf(
		"response exceeded the %d-byte limit; aborted before decompressing it fully "+
			"(raise WithMaxResponseBytes if you expect payloads this large)", e.Limit)
}

// APIError is the single, normative gateway-origin error. Match it with
// errors.As(err, &apiErr) then inspect Kind. A successful scrape of a page that
// itself returned 404/410 is NOT an APIError — it is a *Result, and so is any
// response the gateway billed (a positive Scrape.do-Request-Cost: the target's
// 400, 401, 403, 422, ...) and any response the target itself produced under
// TransparentResponse. A 400 or 401 the gateway produced (a parameter it
// rejected, a refused token) carries no cost and is an APIError.
type APIError struct {
	// Message is the gateway's own explanation when its error body carries
	// one, otherwise a default for the status. Credentials are redacted.
	Message    string
	StatusCode int
	Kind       ErrorKind
	// Billed reports whether the gateway charged for the response: the
	// Scrape.do-Request-Cost header when it is readable (positive is billed,
	// 0 is not), else the documented table (the gateway's own 400, 401, 429,
	// 502 and 510 are not billed). Never for the async job API.
	Billed bool
	// IsRetryable reports whether the status is transient. The client retries
	// such statuses itself; POST, PUT, PATCH and DELETE only on 429.
	IsRetryable bool
	// Body is the start of the response body as text (up to 64 KiB is read),
	// redacted, truncated to 2048 characters.
	Body string

	retryAfter          time.Duration
	hasRetryAfter       bool
	remainingCredits    int
	hasRemainingCredits bool
	cost                int
	hasCost             bool
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s (status=%d, kind=%s)", e.Message, e.StatusCode, e.Kind)
}

// RetryAfter returns the Retry-After header as a duration (whole seconds) and
// whether it was present as a plain non-negative integer.
func (e *APIError) RetryAfter() (time.Duration, bool) { return e.retryAfter, e.hasRetryAfter }

// RemainingCredits returns the Scrape.do-Remaining-Credits header of a 429 and
// whether it was present.
func (e *APIError) RemainingCredits() (int, bool) { return e.remainingCredits, e.hasRemainingCredits }

// Cost returns the Scrape.do-Request-Cost header and whether it was present
// as a single non-negative integer, as Result.Cost reads it (never set for the
// async job API). An error's cost is 0: a positive cost makes the response a
// *Result.
func (e *APIError) Cost() (int, bool) { return e.cost, e.hasCost }

// Convenience predicates.
func (e *APIError) IsAuth() bool        { return e.Kind == KindAuth }
func (e *APIError) IsValidation() bool  { return e.Kind == KindValidation }
func (e *APIError) IsConcurrency() bool { return e.Kind == KindConcurrency }
func (e *APIError) IsGateway() bool     { return e.Kind == KindGateway }

// statusClientCanceled is the gateway's status for a request whose HTTP client
// cancelled it before the gateway finished (docs: "The request was canceled by
// the HTTP client").
const statusClientCanceled = 510

// Default messages, used when an error body carries no envelope.
const (
	msgValidation  = "The gateway rejected the request parameters."
	msgAuth        = "Authentication failed — check your token, plan permissions, and remaining credits."
	msgConcurrency = "Concurrent-request limit reached."
	msgGateway     = "Gateway-origin upstream failure (HTTP %d)."
	msgCanceled    = "The request was canceled by the HTTP client (HTTP %d)."
	msgRedirect    = "Unexpected redirect (HTTP %d); redirects are never followed."
	msgAPI         = "Request failed (HTTP %d)."
)

// newAPIError builds the normative error for a failed status. The message is
// the envelope's when the body carries one — parsed from the whole body read,
// before the retained copy is truncated — and both are redacted: the gateway
// may echo the request, token included. body is nil when it could not be read.
func (t *transport) newAPIError(status int, header http.Header, body []byte, jobAPI bool) *APIError {
	e := &APIError{
		StatusCode:  status,
		Billed:      !jobAPI && billedStatus[status],
		IsRetryable: retryableStatus[status],
		Body:        truncateRunes(t.redact(decodeUTF8(body)), retainedBodyChars),
	}
	if !jobAPI {
		// The header is authoritative; the table is the fallback.
		if e.cost, e.hasCost = requestCost(header); e.hasCost {
			e.Billed = e.cost > 0
		}
	}
	if secs, ok := parseRetryAfter(header.Get("Retry-After")); ok {
		e.retryAfter, e.hasRetryAfter = time.Duration(secs)*time.Second, true
	}
	switch {
	case status == 401 || status == 403:
		// The gateway returns 401 for a depleted credit balance too, so the
		// default message names both causes.
		e.Kind, e.Message = KindAuth, msgAuth
	case status == 429:
		e.Kind, e.Message = KindConcurrency, msgConcurrency
		e.remainingCredits, e.hasRemainingCredits = headerInt(header, "Scrape.do-Remaining-Credits", true)
	case status == 400:
		e.Kind, e.Message = KindValidation, msgValidation
	case status == statusClientCanceled:
		// The gateway's own record of a request the client hung up on; it is
		// not an upstream failure and is not retried.
		e.Kind, e.Message = KindGateway, fmt.Sprintf(msgCanceled, status)
	case status >= 500:
		e.Kind, e.Message = KindGateway, fmt.Sprintf(msgGateway, status)
	case status >= 300 && status < 400:
		e.Kind, e.Message = KindOther, fmt.Sprintf(msgRedirect, status)
	default:
		e.Kind, e.Message = KindOther, fmt.Sprintf(msgAPI, status)
	}
	parse := parseErrorEnvelope
	if jobAPI {
		parse = parseAsyncErrorEnvelope
	}
	if msg, ok := parse(body); ok {
		e.Message = t.redact(msg)
	}
	return e
}

// parseErrorEnvelope reads the sync gateway's error envelope: a JSON object
// with a "message" key, else an "error" key, each matched in any ASCII letter
// case — the gateway writes {"Message": [...]}, a plugin {"error": "code",
// "message": "text"}. The value is a string, or an array whose string elements
// are joined with "; " (other elements, and empty strings, are skipped). When
// several keys match one name, they are tried in byte order ("Message" before
// "message"); one that yields nothing falls through to the next, and then to
// "error". Nothing left means no envelope.
func parseErrorEnvelope(body []byte) (string, bool) {
	m, ok := jsonObject(body)
	if !ok {
		return "", false
	}
	keys := sortedKeys(m)
	for _, name := range []string{"message", "error"} {
		for _, k := range keys {
			if lowerASCII(k) != name {
				continue
			}
			if msg, ok := envelopeText(m[k]); ok {
				return msg, true
			}
		}
	}
	return "", false
}

// envelopeText reads one envelope value: a non-empty string, or the non-empty
// strings of an array joined with "; ".
func envelopeText(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, v != ""
	case []any:
		var parts []string
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "; "), len(parts) > 0
	}
	return "", false
}

// parseAsyncErrorEnvelope reads the job API's envelope,
// {"Error": "...", "Code": n}. Only "Error" is read.
func parseAsyncErrorEnvelope(body []byte) (string, bool) {
	m, ok := jsonObject(body)
	if !ok {
		return "", false
	}
	s, ok := m["Error"].(string)
	return s, ok && s != ""
}

func jsonObject(body []byte) (map[string]any, bool) {
	var m map[string]any
	if err := json.Unmarshal(bytes.TrimPrefix(body, bomUTF8), &m); err != nil {
		return nil, false
	}
	return m, m != nil
}

// truncateRunes keeps the first n characters of s.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for count := 0; count < n; count++ {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i]
}

// headerInt reads a header that must be a single integer: ASCII digits
// (with a leading '-' only when negative is allowed), surrounding spaces and
// tabs ignored. Anything else — a sign, repeated headers, an overflow — is
// absent.
func headerInt(h http.Header, name string, negative bool) (int, bool) {
	values := h.Values(name)
	if len(values) != 1 {
		return 0, false
	}
	s := strings.Trim(values[0], " \t")
	digits := s
	if negative {
		digits = strings.TrimPrefix(s, "-")
	}
	if !allDigits(digits) {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// parseRetryAfter reads Retry-After as RFC 9110 delay-seconds: ASCII digits
// only, surrounding spaces and tabs allowed. Anything else — an HTTP date, a
// sign, a fraction, NaN — is ignored and the exponential backoff applies.
func parseRetryAfter(raw string) (int, bool) {
	s := strings.Trim(raw, " \t")
	if !allDigits(s) {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil { // all digits, so only overflow: far beyond the cap anyway
		return int(maxBackoff / time.Second), true
	}
	return n, true
}
