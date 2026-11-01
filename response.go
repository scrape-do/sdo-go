// SPDX-License-Identifier: Apache-2.0

package sdo

import (
	"net/http"
)

// Result is a successfully delivered scrape (or plugin) response. The body is
// already transparently decompressed. StatusCode is the *target's* status (a
// scrape of a 404 page is a successful Result with StatusCode == 404).
type Result struct {
	content    []byte
	StatusCode int
	// Header holds every response header, repeated ones included
	// (Header.Values), as the gateway sent them.
	Header http.Header
	// URL is the scraped target (ScrapeConfig.URL); "" for a plugin result.
	URL string
}

// Bytes returns the raw, decompressed payload.
func (r *Result) Bytes() []byte { return r.content }

// Cost returns the credits this request cost (Scrape.do-Request-Cost, the
// authoritative figure) and whether the header was present as a single
// non-negative integer of at most 2^53-1.
func (r *Result) Cost() (int, bool) { return requestCost(r.Header) }

// RemainingCredits returns the remaining credits and whether the header was
// present as a single integer.
func (r *Result) RemainingCredits() (int, bool) {
	return headerInt(r.Header, "Scrape.do-Remaining-Credits", true)
}

// ContentType returns the response Content-Type.
func (r *Result) ContentType() string { return r.Header.Get("Content-Type") }

// InitialStatusCode returns the target's first-hop status (before any gateway
// retry/redirect handling) and whether the header was present.
func (r *Result) InitialStatusCode() (int, bool) {
	return headerInt(r.Header, "scrape.do-initial-status-code", false)
}

// TargetURL returns the originally requested target URL, or "" if absent.
func (r *Result) TargetURL() string { return r.Header.Get("scrape.do-target-url") }

// TargetRedirectedLocation returns the Location the target answered with when
// redirects were not followed (ScrapeConfig.DisableRedirection), from the
// Scrape.do-Target-Redirected-Location header, or "" if absent.
func (r *Result) TargetRedirectedLocation() string {
	return r.Header.Get("Scrape.do-Target-Redirected-Location")
}

// ResolvedURL returns the final URL the gateway resolved to, or "" if absent.
func (r *Result) ResolvedURL() string { return r.Header.Get("scrape.do-resolved-url") }

// Cookies returns the raw session cookies the gateway reported, or "" if absent.
func (r *Result) Cookies() string { return r.Header.Get("scrape.do-cookies") }

// Text decodes the body per the WHATWG Encoding Standard: a BOM wins and is
// consumed; then the Content-Type charset (resolved through the WHATWG label
// table — an unknown label, or one that maps to "replacement", is ignored);
// JSON media types are UTF-8; HTML (or an unlabelled body) may name its charset
// in a <meta> within the first 1024 bytes; otherwise UTF-8. Invalid sequences
// become U+FFFD, one per maximal subpart, so the result is always valid UTF-8
// and identical to what the Python and JS SDKs return.
func (r *Result) Text() string { return decodeBody(r.content, r.ContentType()) }

// JSON unmarshals the body into v. The body is UTF-8 (a leading BOM ignored,
// invalid bytes read as U+FFFD); NaN, Infinity and numbers beyond the range of
// a float64 are rejected. Any failure is a *TransportError, like every other
// SDK failure (and like the Python and JS SDKs); the decoder's error stays
// reachable through errors.As.
func (r *Result) JSON(v any) error {
	if err := unmarshalBody(r.content, v); err != nil {
		return &TransportError{Message: "the response body could not be decoded as JSON: " + err.Error(), Err: err}
	}
	return nil
}
