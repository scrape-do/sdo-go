// SPDX-License-Identifier: Apache-2.0

// Security invariants the public-flip audit gates on.
//
// These are fixtures, not smoke tests: each one pins a security guarantee
// that would otherwise only be checked by reading the code.

package sdo

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
)

const secretToken = "SUPERSECRETTOKEN"

// everything a caller could plausibly log or inspect from an error.
func errSurface(err error) string {
	if err == nil {
		return ""
	}
	parts := []string{err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err)}
	for inner := errors.Unwrap(err); inner != nil; inner = errors.Unwrap(inner) {
		parts = append(parts, inner.Error())
	}
	return strings.Join(parts, " ")
}

// TestTokenNeverInTransportError is the redaction fixture the auditor requires.
//
// The token rides in the query string, and Go's *url.Error embeds the whole
// request URL in its Error() string — so an unredacted wrap put the token into
// `log.Printf("%v", err)`, into errors.Unwrap, and (via the CLI) into the MCP
// server's reply to the LLM.
func TestTokenNeverInTransportError(t *testing.T) {
	// port 1 is unroutable, so this fails at dial time and produces *url.Error.
	c, err := New(secretToken, WithBaseURL("http://127.0.0.1:1"), WithRetries(0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, scrapeErr := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	if scrapeErr == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(errSurface(scrapeErr), secretToken) {
		t.Errorf("token leaked into the error surface: %s", errSurface(scrapeErr))
	}
}

func TestTokenNeverInAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A gateway that echoes the request URL back in its error body.
		w.WriteHeader(401)
		fmt.Fprintf(w, `{"Message":["unauthorized for %s%s"]}`, srv0(r), r.RequestURI)
	}))
	defer srv.Close()
	c, _ := New(secretToken, WithBaseURL(srv.URL), WithRetries(0))
	_, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	if err == nil {
		t.Fatal("expected an API error")
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && strings.Contains(apiErr.Body, secretToken) {
		t.Errorf("token leaked into APIError.Body: %s", apiErr.Body)
	}
	if strings.Contains(errSurface(err), secretToken) {
		t.Errorf("token leaked into the error surface: %s", errSurface(err))
	}
}

func srv0(r *http.Request) string { return "http://" + r.Host }

func TestRedactSecretsShape(t *testing.T) {
	// Redaction works by shape, not by comparing against the configured token,
	// so a URL echoed by a proxy or the runtime is covered too.
	cases := [][2]string{
		{`Get "http://h/?token=abc&url=x": refused`, `Get "http://h/?token=***&url=x": refused`},
		{"?api_key=zzz", "?api_key=***"},
		{"&Access-Token=zzz end", "&Access-Token=*** end"},
		{"nothing to redact", "nothing to redact"},
	}
	for _, c := range cases {
		if got := RedactSecrets(c[0]); got != c[1] {
			t.Errorf("RedactSecrets(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

// TestDecompressionBombRefused: the scraped target controls the body, so a
// small compressed payload must not be allowed to inflate without bound.
func TestDecompressionBombRefused(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(bytes.Repeat([]byte{0}, 8<<20)); err != nil { // 8 MiB of zeros
		t.Fatalf("write: %v", err)
	}
	zw.Close()
	if buf.Len() > 100_000 {
		t.Fatalf("fixture should be small compressed, got %d", buf.Len())
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(buf.Bytes())
	}))
	defer srv.Close()

	c, _ := New("t", WithBaseURL(srv.URL), WithRetries(0), WithMaxResponseBytes(1<<20))
	_, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	var tooLarge *ResponseTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("expected *ResponseTooLargeError, got %v", err)
	}
	if tooLarge.Limit != 1<<20 {
		t.Errorf("limit = %d, want %d", tooLarge.Limit, 1<<20)
	}
}

func TestBodyUnderTheCapStillDecompresses(t *testing.T) {
	payload := []byte("<html>" + strings.Repeat("x", 5000) + "</html>")
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(payload)
	zw.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(buf.Bytes())
	}))
	defer srv.Close()

	c, _ := New("t", WithBaseURL(srv.URL), WithRetries(0), WithMaxResponseBytes(1<<20))
	res, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	if !bytes.Equal(res.Bytes(), payload) {
		t.Errorf("body round-trip failed (%d bytes)", len(res.Bytes()))
	}
}

// TestPluginRouteValidation pins the LLM trust boundary: the MCP server passes
// an LLM-chosen route straight through, so traversal, query injection and
// fragment truncation must be rejected before the request is built.
func TestPluginRouteValidation(t *testing.T) {
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c, _ := New(secretToken, WithBaseURL(srv.URL), WithRetries(0))

	for _, route := range []string{"../", "..%2f..", "x#", "x?token=evil", "a/../b", "Google/Shopping", "", "  "} {
		reached = false
		_, err := c.Plugin(context.Background(), route, map[string]any{"q": "1"})
		var cfgErr *ConfigError
		if !errors.As(err, &cfgErr) {
			t.Errorf("route %q: expected *ConfigError, got %v", route, err)
		}
		if reached {
			t.Errorf("route %q reached the wire", route)
		}
	}

	for _, route := range []string{"google/shopping", "amazon-product", "a1/b2/c3"} {
		var gotPath string
		ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"ok":true}`))
		}))
		c2, _ := New(secretToken, WithBaseURL(ok.URL), WithRetries(0))
		if _, err := c2.Plugin(context.Background(), route, map[string]any{"q": "1"}); err != nil {
			t.Errorf("route %q: %v", route, err)
		}
		if gotPath != "/plugin/"+route {
			t.Errorf("route %q -> path %q", route, gotPath)
		}
		ok.Close()
	}
}

// TestPluginParamsAreOrdered pins the deterministic query string: Go randomizes
// map iteration, so the same call must not serialize differently run to run.
func TestPluginParamsAreOrdered(t *testing.T) {
	params := map[string]any{"zulu": "1", "alpha": "2", "mike": "3", "bravo": "4", "yankee": "5"}
	seen := map[string]bool{}
	for range 8 {
		var raw string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw = r.URL.RawQuery
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{}`))
		}))
		c, _ := New("t", WithBaseURL(srv.URL), WithRetries(0))
		if _, err := c.Plugin(context.Background(), "google/shopping", params); err != nil {
			t.Fatalf("plugin: %v", err)
		}
		seen[raw] = true
		srv.Close()
	}
	if len(seen) != 1 {
		t.Errorf("query string is not deterministic across runs: %v", seen)
	}
}

func TestUserAgentHasNoSuffix(t *testing.T) {
	// No OS, no Go version: the spec fixes the UA and anything appended is
	// extra client fingerprinting.
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
	}))
	defer srv.Close()
	c, _ := New("t", WithBaseURL(srv.URL), WithRetries(0))
	if _, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"}); err != nil {
		t.Fatalf("scrape: %v", err)
	}
	if want := "sdo-sdk-go/" + Version; gotUA != want {
		t.Errorf("User-Agent = %q, want %q", gotUA, want)
	}
}

// compressWith encodes b with one Content-Encoding.
func compressWith(t *testing.T, coding string, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	var w io.WriteCloser
	switch coding {
	case "gzip":
		w = gzip.NewWriter(&buf)
	case "deflate":
		w, _ = flate.NewWriter(&buf, flate.BestCompression)
	case "deflate-zlib":
		w = zlib.NewWriter(&buf)
	case "br":
		w = brotli.NewWriter(&buf)
	default:
		t.Fatalf("unknown coding %q", coding)
	}
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

// Every codec, stacked codings and every body path are bounded: an 8 MiB
// inflation against a 1 MiB cap is refused, not materialized.
func TestDecompressionBombsPerCodecAndPath(t *testing.T) {
	zeros := bytes.Repeat([]byte{0}, 8<<20)
	bombs := map[string][]byte{
		"gzip":       compressWith(t, "gzip", zeros),
		"deflate":    compressWith(t, "deflate", zeros),
		"br":         compressWith(t, "br", zeros),
		"gzip, br":   compressWith(t, "br", compressWith(t, "gzip", zeros)),
		"br, x-gzip": compressWith(t, "gzip", compressWith(t, "br", zeros)),
	}
	bombs["deflate (zlib)"] = compressWith(t, "deflate-zlib", zeros)
	for label, body := range bombs {
		coding := strings.TrimSuffix(label, " (zlib)")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Encoding", coding)
			w.Header().Set("Content-Type", "application/json")
			w.Write(body)
		}))
		c, _ := New("t", WithBaseURL(srv.URL), WithAsyncBaseURL(srv.URL), WithRetries(0), WithMaxResponseBytes(1<<20))
		ctx := context.Background()
		_, errScrape := c.Scrape(ctx, &ScrapeConfig{URL: "https://e.com"})
		_, errPlugin := c.Plugin(ctx, "google/search", nil)
		_, errJobs := c.Jobs().Result(ctx, "j-1", "t-1")
		for path, err := range map[string]error{"scrape": errScrape, "plugin": errPlugin, "jobs": errJobs} {
			var tooLarge *ResponseTooLargeError
			if !errors.As(err, &tooLarge) {
				t.Errorf("%s bomb on the %s path: want ResponseTooLargeError, got %v", label, path, err)
			}
		}
		srv.Close()
	}
}

// An error body is read only up to 64 KiB, so a bomb in a 5xx body costs no
// more than that and the status error is still raised.
func TestErrorBodyBombKeepsTheStatusError(t *testing.T) {
	body := compressWith(t, "br", bytes.Repeat([]byte("x"), 8<<20))
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "br")
		w.WriteHeader(http.StatusBadGateway)
		w.Write(body)
	}, WithRetries(0), WithMaxResponseBytes(1<<20))
	_, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 502 || apiErr.Message != "Gateway-origin upstream failure (HTTP 502)." {
		t.Fatalf("want the 502 status error, got %v", err)
	}
	if len(apiErr.Body) != retainedBodyChars {
		t.Errorf("retained body = %d chars, want %d", len(apiErr.Body), retainedBodyChars)
	}
}

// urlErrRT fails the way wrapping RoundTrippers (retry, instrumentation,
// proxy layers) commonly do: with its own *url.Error carrying the request URL.
type urlErrRT struct{}

func (urlErrRT) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, &url.Error{Op: "Get", URL: r.URL.String(), Err: fmt.Errorf("upstream reset for %s: %w", r.URL, context.DeadlineExceeded)}
}

// customErr carries the URL in a field and its text.
type customErr struct{ URL string }

func (e *customErr) Error() string { return "custom failure for " + e.URL }

type customErrRT struct{}

func (customErrRT) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, &customErr{URL: r.URL.String()}
}

// errors.As through an SDK error never hands back an object that prints the
// token: a *url.Error comes back as a redacted copy, a credential-carrying
// custom error not at all, while errors.Is keeps working.
func TestErrorsAsNeverReturnsAnUnredactedCause(t *testing.T) {
	c, _ := New(secretToken, WithBaseURL("https://api.scrape.do"), WithRetries(0),
		WithHTTPClient(&http.Client{Transport: urlErrRT{}}))
	_, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		t.Fatalf("errors.As(*url.Error) = false for %v", err)
	}
	if out := fmt.Sprintf("%v %+v %s %#v", urlErr, urlErr, urlErr.URL, urlErr.Err); strings.Contains(out, secretToken) {
		t.Errorf("errors.As handed back the token: %s", out)
	}
	if !strings.Contains(urlErr.URL, "token=***") {
		t.Errorf("the redacted copy should keep the URL shape: %q", urlErr.URL)
	}
	var netErr interface{ Timeout() bool }
	if !errors.As(err, &netErr) || strings.Contains(fmt.Sprint(netErr), secretToken) {
		t.Errorf("interface target: %v", netErr)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("errors.Is(context.DeadlineExceeded) lost: %v", err)
	}

	c, _ = New(secretToken, WithBaseURL("https://api.scrape.do"), WithRetries(0),
		WithHTTPClient(&http.Client{Transport: customErrRT{}}))
	_, err = c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	var ce *customErr
	if errors.As(err, &ce) || ce != nil {
		t.Errorf("a credential-carrying custom error was handed back: %v", ce)
	}
	if strings.Contains(errSurface(err), secretToken) {
		t.Errorf("token leaked: %s", errSurface(err))
	}
	// A cause without a credential is handed back as it is.
	tr := &transport{cred: &credential{secretToken}}
	var opErr *net.OpError
	if wrapped := tr.cause(&net.OpError{Op: "dial", Err: errors.New("refused")}); !errors.As(wrapped, &opErr) {
		t.Errorf("a clean *net.OpError was not handed back")
	}
	multi := tr.cause(errors.Join(errors.New("a"), &customErr{URL: "https://h/?token=" + secretToken}))
	if errors.As(multi, &ce) {
		t.Errorf("a joined credential-carrying error was handed back")
	}
}

// wrapErr prints only its own message, whatever it wraps.
type wrapErr struct {
	msg string
	err error
}

func (e *wrapErr) Error() string { return e.msg }
func (e *wrapErr) Unwrap() error { return e.err }

// errors.As judges a found error by everything it wraps, not by its text
// alone: one that prints clean but wraps a credential-carrying error, also
// inside an errors.Join, is not handed back. One that wraps nothing, or wraps
// an error the SDK already redacted, is.
func TestErrorsAsChecksWhatAFoundErrorWraps(t *testing.T) {
	tr := &transport{cred: &credential{secretToken}}
	dirty := &customErr{URL: "https://h/?token=" + secretToken}

	var joined interface{ Unwrap() []error }
	if errors.As(tr.cause(errors.Join(errors.New("a"), &wrapErr{msg: "b", err: dirty})), &joined) || joined != nil {
		t.Errorf("a join hiding a credential-carrying error was handed back: %v", joined)
	}
	if !errors.As(tr.cause(errors.Join(errors.New("a"), &wrapErr{msg: "b"})), &joined) || joined == nil {
		t.Errorf("a clean join was not handed back")
	}

	var w *wrapErr
	if errors.As(tr.cause(&wrapErr{msg: "clean text", err: dirty}), &w) || w != nil {
		t.Errorf("a clean-printing wrapper of a credential was handed back: %v", w)
	}
	if !errors.As(tr.cause(&wrapErr{msg: "retrying", err: tr.cause(dirty)}), &w) || w == nil {
		t.Fatalf("a wrapper of an already-redacted error was not handed back")
	}
	if strings.Contains(errSurface(w), secretToken) {
		t.Errorf("token leaked: %s", errSurface(w))
	}

	// Without a cause there is nothing to match.
	empty := &redactedError{msg: "x", cred: tr.cred}
	if errors.As(empty, &w) || errors.Is(empty, context.Canceled) {
		t.Errorf("a redactedError without a cause matched")
	}
}

// A request that cannot even be built (here a URL with a control character)
// is a *ConfigError, returned before anything is sent, and its message — Go
// quotes the whole URL — does not carry the token.
func TestUnbuildableRequestIsARedactedConfigError(t *testing.T) {
	c, err := New(secretToken, WithBaseURL("https://api.scrape.do"), WithRetries(2),
		WithHTTPClient(&http.Client{Transport: customErrRT{}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.t.do(context.Background(), outgoing{method: http.MethodGet,
		url: "https://api.scrape.do/?token=" + secretToken + "&url=\x7f"})
	var ce *ConfigError
	if !errors.As(err, &ce) || !strings.Contains(ce.Message, "invalid control character") {
		t.Fatalf("want a *ConfigError, got %T %v", err, err)
	}
	if strings.Contains(errSurface(err), secretToken) || !strings.Contains(ce.Message, "token=***") {
		t.Errorf("token not redacted: %s", errSurface(err))
	}
}

// RedactSecrets matches credential names by ASCII case only and stops a value
// at any ASCII whitespace, \v included.
func TestRedactSecretsIsASCII(t *testing.T) {
	for in, want := range map[string]string{
		"to\u212Aen=abc":     "to\u212Aen=abc", // a Kelvin sign is not the ASCII k
		"TOKEN=abc\vrest":    "TOKEN=***\vrest",
		"x-token:\vabc rest": "x-token:\v*** rest",
		`"Api_Key" : "v"`:    `"Api_Key" : "***"`,
	} {
		if got := RedactSecrets(in); got != want {
			t.Errorf("RedactSecrets(%q) = %q, want %q", in, got, want)
		}
	}
}

// ExtraParams and plugin param keys are restricted to the documented shape, so
// a key cannot smuggle a second parameter, or a near-miss of "token"/"url",
// past the reserved-key check.
func TestParamKeysMustBeParameterNames(t *testing.T) {
	bad := []string{"token ", " url", "url[]", "a&token=x", "a=b", "1st", "_x", "-x", ".x", "tokKen", "a b", "a\x00"}
	for _, k := range bad {
		var ce *ConfigError
		if _, err := (&ScrapeConfig{URL: "https://e.com", ExtraParams: map[string]string{k: "v"}}).toParams(); !errors.As(err, &ce) || !strings.Contains(ce.Message, "not a valid parameter name") {
			t.Errorf("ExtraParams %q: want a key-shape ConfigError, got %v", k, err)
		}
		if _, err := pluginParams(map[string]any{k: "v"}); !errors.As(err, &ce) || !strings.Contains(ce.Message, "not a valid parameter name") {
			t.Errorf("plugin params %q: want a key-shape ConfigError, got %v", k, err)
		}
	}
	good := map[string]string{"include_airlines": "1", "hl": "en", "gl": "us", "x-y.z_9": "1", "Beta": "1"}
	if _, err := (&ScrapeConfig{URL: "https://e.com", ExtraParams: good}).toParams(); err != nil {
		t.Errorf("ExtraParams: documented key shapes refused: %v", err)
	}
	anyGood := map[string]any{}
	for k, v := range good {
		anyGood[k] = v
	}
	if _, err := pluginParams(anyGood); err != nil {
		t.Errorf("plugin params: documented key shapes refused: %v", err)
	}
}

// A server or proxy echoing the percent-encoded token with lowercase hex is
// masked as well as the uppercase wire form.
func TestRedactMasksLowercasePercentEncodedToken(t *testing.T) {
	tok := "abc/def+ghi=="
	for _, in := range []string{"x abc%2Fdef%2Bghi%3D%3D y", "x abc%2fdef%2bghi%3d%3d y", "x abc/def+ghi== y"} {
		if got := redact(in, tok); got != "x *** y" {
			t.Errorf("redact(%q) = %q", in, got)
		}
	}
	if got := lowerPctHex("A%2FB%"); got != "A%2fB%" {
		t.Errorf("lowerPctHex = %q", got)
	}
}
