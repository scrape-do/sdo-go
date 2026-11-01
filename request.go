// SPDX-License-Identifier: Apache-2.0

package sdo

import (
	"fmt"
	"net/http"
	"net/textproto"
	"strings"
)

// Request is the HTTP side of a scrape: the method, body and headers the
// gateway sends to the target. A nil *Request is a plain GET.
type Request struct {
	// Method is case-insensitive and one of GET, POST, PUT, PATCH, DELETE,
	// HEAD or OPTIONS. Empty means GET, or POST when a body is given.
	Method string
	// JSON, when non-nil, is the body, in the SDK's canonical JSON encoding
	// (sorted keys, no HTML escaping), sent with Content-Type:
	// application/json unless Headers sets one.
	JSON any
	// Data, when non-nil, is the body, sent verbatim with no Content-Type.
	// JSON and Data are mutually exclusive.
	Data []byte
	// Headers are sent with the request and win over ScrapeConfig.Headers.
	// Names are case-insensitive; a value replaces the SDK's own header of
	// the same name (User-Agent, Accept-Encoding). See ScrapeConfig.Headers for
	// what is accepted.
	Headers map[string]string
}

var requestMethods = map[string]bool{
	http.MethodGet: true, http.MethodPost: true, http.MethodPut: true, http.MethodPatch: true,
	http.MethodDelete: true, http.MethodHead: true, http.MethodOptions: true,
}

// prepare validates the request and returns the method to send, the body
// (nil for none) and the Content-Type the SDK sets ("" for none).
func (r *Request) prepare() (method string, body []byte, contentType string, err error) {
	if r == nil {
		return http.MethodGet, nil, "", nil
	}
	if r.JSON != nil && r.Data != nil {
		return "", nil, "", &ConfigError{Message: "Request.JSON and Request.Data are mutually exclusive"}
	}
	switch {
	case r.JSON != nil:
		s, err := canonicalJSON(r.JSON)
		if err != nil {
			return "", nil, "", &ConfigError{Message: fmt.Sprintf("Request.JSON is not JSON-serializable: %v", err)}
		}
		body, contentType = []byte(s), "application/json"
	case r.Data != nil:
		body = r.Data
	}
	method = strings.ToUpper(r.Method)
	if method == "" {
		method = http.MethodGet
		if body != nil {
			method = http.MethodPost
		}
	}
	if !requestMethods[method] {
		return "", nil, "", &ConfigError{Message: fmt.Sprintf(
			"Request.Method must be one of GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS; got %q", r.Method)}
	}
	if body != nil && (method == http.MethodGet || method == http.MethodHead) {
		return "", nil, "", &ConfigError{Message: fmt.Sprintf("a %s request cannot carry a body", method)}
	}
	return method, body, contentType, nil
}

// forbiddenHeaders are set by the HTTP client itself and cannot be overridden.
var forbiddenHeaders = map[string]bool{"host": true, "content-length": true, "transfer-encoding": true, "connection": true}

// checkHeader validates one caller header: the name an RFC 9110 token, the
// value printable ASCII plus space and tab. The value is never echoed: it may
// be a credential.
func checkHeader(name, value string) error {
	if !isToken(name) {
		return &ConfigError{Message: fmt.Sprintf("header name %q is not a valid HTTP field name (an RFC 9110 token)", name)}
	}
	if forbiddenHeaders[lowerASCII(name)] {
		return &ConfigError{Message: fmt.Sprintf("header %q is set by the HTTP client and cannot be sent by the caller", name)}
	}
	for i := 0; i < len(value); i++ {
		if c := value[i]; c != '\t' && (c < 0x20 || c > 0x7e) {
			return &ConfigError{Message: fmt.Sprintf(
				"the value of header %q may hold only printable ASCII, space and tab (no CR, LF, NUL or non-ASCII)", name)}
		}
	}
	return nil
}

// isToken reports whether s is an RFC 9110 token (1*tchar).
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0) {
			return false
		}
	}
	return true
}

// mergeHeaders validates and combines header maps; a later map wins, and names
// are compared case-insensitively.
func mergeHeaders(layers ...map[string]string) (http.Header, error) {
	h := http.Header{}
	for _, m := range layers {
		for _, k := range sortedKeys(m) {
			if err := checkHeader(k, m[k]); err != nil {
				return nil, err
			}
			h[textproto.CanonicalMIMEHeaderKey(k)] = []string{m[k]}
		}
	}
	return h, nil
}
