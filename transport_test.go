// SPDX-License-Identifier: Apache-2.0

package sdo

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A caller that cancels its context must be able to recognise the cancellation
// with errors.Is. Redacting the token must not cost the error chain.
func TestCancelledContextIsDetectableAndRedacted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()
	c, _ := New(secretToken, WithBaseURL(srv.URL), WithRetries(0))

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	_, err := c.Scrape(ctx, &ScrapeConfig{URL: "https://e.com"})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false; err=%v", err)
	}
	if strings.Contains(errSurface(err), secretToken) {
		t.Errorf("token leaked: %s", errSurface(err))
	}
}

func TestDeadlineIsDetectableAndMarkedTimeout(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}, WithRetries(0))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := c.Scrape(ctx, &ScrapeConfig{URL: "https://e.com"})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("errors.Is(err, context.DeadlineExceeded) = false; err=%v", err)
	}
	var te *TransportError
	if !errors.As(err, &te) || !te.Timeout {
		t.Errorf("want TransportError{Timeout: true}, got %#v", err)
	}
}

// http.Client.Timeout surfaces as a *url.Error whose Timeout() is true, not as
// context.DeadlineExceeded — it must still be reported as a timeout.
func TestClientTimeoutIsMarkedTimeout(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}, WithRetries(0), WithTimeout(30*time.Millisecond))
	_, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	var te *TransportError
	if !errors.As(err, &te) || !te.Timeout || !strings.HasPrefix(te.Message, "request timed out after 30ms") {
		t.Errorf("want a timeout TransportError naming the 30ms deadline, got %#v", err)
	}
}

// A custom RoundTripper can fail with an error that is not a *url.Error and
// still embeds the URL. There is no URL-free inner error to keep, so only a
// redacted message survives.
type leakyTransport struct{}

func (leakyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("proxy refused %s", r.URL.String())
}

func TestRoundTripperErrorIsRedactedButStillMatchable(t *testing.T) {
	c, _ := New(secretToken, WithBaseURL("https://api.scrape.do"), WithRetries(0),
		WithHTTPClient(&http.Client{Transport: leakyTransport{}}))
	_, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	if err == nil {
		t.Fatal("expected an error")
	}
	// The RoundTripper's own message embeds the URL; no printable link of the
	// chain may carry it.
	if strings.Contains(errSurface(err), secretToken) {
		t.Errorf("token leaked through a custom RoundTripper error: %s", errSurface(err))
	}
	// A non-url error that reaches transportError directly is scrubbed too,
	// by shape and as the literal token.
	tr := &transport{cred: &credential{secretToken}}
	direct := tr.transportError(context.Background(), errors.New("proxy refused https://h/?token="+secretToken+" for "+secretToken), 0)
	if strings.Contains(errSurface(direct), secretToken) {
		t.Errorf("token leaked through a bare error: %s", errSurface(direct))
	}
	// errors.As still reaches the original typed cause when a caller asks for it.
	var opErr *net.OpError
	_, dialErr := (&http.Client{}).Get("http://127.0.0.1:1/")
	wrapped := tr.transportError(context.Background(), dialErr, 0)
	if !errors.As(wrapped, &opErr) {
		t.Errorf("errors.As lost the *net.OpError behind a dial failure: %v", wrapped)
	}
}

func TestNegativeRetriesStillSendsOnce(t *testing.T) {
	var calls atomic.Int32
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(503)
	}, WithRetries(-1))
	_, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	if calls.Load() != 1 {
		t.Errorf("negative retries sent %d requests, want exactly 1", calls.Load())
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 503 {
		t.Errorf("want the 503 surfaced, got %v", err)
	}
}

func TestInvalidBaseURLIsAConfigError(t *testing.T) {
	for _, u := range []string{"https://bad host", "https://", "ftp://h", "h.example"} {
		_, err := New("t", WithBaseURL(u))
		var ce *ConfigError
		if !errors.As(err, &ce) {
			t.Errorf("%q: want ConfigError, got %T %v", u, err, err)
		}
	}
	// The base URL is checked where it comes from, credentials masked.
	_, err := New("t", WithBaseURL("http://user:pw@gateway.example"))
	if err == nil || strings.Contains(err.Error(), "pw") || !strings.Contains(err.Error(), "WithBaseURL") {
		t.Errorf("error should name the option and mask credentials: %v", err)
	}
}

// A connection that breaks after the request went out is not retried:
// the gateway may still run (and bill) it.
func TestFailureAfterSendIsNotRetried(t *testing.T) {
	// A listener that accepts and immediately closes: the request is written,
	// then the connection drops before any response.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			conn.Close()
		}
	}()
	defer ln.Close()

	c, _ := New("t", WithBaseURL("http://"+ln.Addr().String()), WithRetries(2), WithBackoff(0))
	_, err = c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	var te *TransportError
	if !errors.As(err, &te) {
		t.Fatalf("want TransportError, got %v", err)
	}
	if got := accepted.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1 (no retry after send)", got)
	}
}

// A failure before anything was sent (here: every dial refused) is retried
// for an idempotent method, and not for POST.
func TestFailureBeforeSendIsRetriedForIdempotentMethods(t *testing.T) {
	var dials atomic.Int32
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			return nil, &net.OpError{Op: "dial", Net: network, Err: errors.New("connection refused")}
		},
	}}
	c, _ := New("t", WithBaseURL("http://127.0.0.1:9"), WithHTTPClient(hc), WithRetries(2), WithBackoff(0))
	_, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	var te *TransportError
	if !errors.As(err, &te) || !strings.HasPrefix(te.Message, "connection failed") {
		t.Fatalf("want a connection TransportError, got %v", err)
	}
	if got := dials.Load(); got != 3 {
		t.Errorf("GET dials = %d, want 3 (1 + 2 retries)", got)
	}
	dials.Store(0)
	_, _ = c.Do(context.Background(), &ScrapeConfig{URL: "https://e.com"}, &Request{Data: []byte("x")})
	if got := dials.Load(); got != 1 {
		t.Errorf("POST dials = %d, want 1 (never retried)", got)
	}
}

// A RoundTripper that reports no trace is judged by its error: only a DNS or
// dial error counts as unsent.
type errTransport struct {
	err   error
	calls atomic.Int32
}

func (e *errTransport) RoundTrip(*http.Request) (*http.Response, error) {
	e.calls.Add(1)
	return nil, e.err
}

func TestUntracedFailuresAreJudgedByTheirError(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int32
	}{
		{&net.DNSError{Err: "no such host", Name: "h"}, 3},
		{&net.OpError{Op: "dial", Err: errors.New("refused")}, 3},
		{&net.OpError{Op: "read", Err: errors.New("reset")}, 1},
		{errors.New("something else"), 1},
	} {
		rt := &errTransport{err: tc.err}
		c, _ := New("t", WithBaseURL("https://api.scrape.do"), WithHTTPClient(&http.Client{Transport: rt}),
			WithRetries(2), WithBackoff(0))
		_, _ = c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
		if got := rt.calls.Load(); got != tc.want {
			t.Errorf("%v: attempts = %d, want %d", tc.err, got, tc.want)
		}
	}
}

// A body that breaks off mid-read is a transport error, not retried.
func TestTruncatedBodyIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// Promise 100 bytes, send 5, then drop the connection.
		w.Header().Set("Content-Length", "100")
		w.Write([]byte("short"))
		hj, _ := w.(http.Hijacker)
		conn, _, _ := hj.Hijack()
		conn.Close()
	}, WithRetries(2), WithBackoff(0))
	_, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	var te *TransportError
	if !errors.As(err, &te) || te.Message != "failed to read response body" || te.Timeout {
		t.Fatalf("want a read TransportError, got %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
}

// The raw read is capped too, not only the inflated size: an uncompressed body
// larger than the ceiling is refused, and not retried.
func TestOversizedPlainBodyIsRefusedWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write(bytes.Repeat([]byte("x"), 4096))
	}, WithRetries(2), WithMaxResponseBytes(1024))
	_, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	var tooLarge *ResponseTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("want ResponseTooLargeError, got %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("an oversized body was retried: %d calls", calls.Load())
	}
	if !strings.Contains(tooLarge.Error(), "1024-byte limit") {
		t.Errorf("message should name the limit: %q", tooLarge.Error())
	}
}

func TestCorruptCompressedBodyIsATransportError(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Write([]byte("definitely not gzip"))
	}, WithRetries(0))
	_, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	var te *TransportError
	if !errors.As(err, &te) || te.Message != "failed to decompress response" {
		t.Errorf("want a decompression TransportError, got %v", err)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}, WithRetries(0))
	res, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	if res.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want the 302 itself", res.StatusCode)
	}
}

func TestBaseURLFromEnvironment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("from env base"))
	}))
	defer srv.Close()
	t.Setenv("SDO_BASE_URL", srv.URL)
	c, _ := New("t", WithRetries(0))
	res, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	if err != nil || string(res.Bytes()) != "from env base" {
		t.Fatalf("SDO_BASE_URL not honoured: res=%v err=%v", res, err)
	}
	// An explicit option still wins over the environment.
	c2, _ := New("t", WithBaseURL("https://explicit.invalid"))
	if c2.baseURL != "https://explicit.invalid" {
		t.Errorf("explicit base URL lost to env: %q", c2.baseURL)
	}
}

// Do merges config headers with request headers (request headers win) and
// sends the body.
func TestRequestMergesHeadersAndSendsBody(t *testing.T) {
	var gotHeader, gotOverride, gotBody, gotMethod, gotCT string
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotHeader = r.Header.Get("X-From-Config")
		gotOverride = r.Header.Get("X-Both")
		gotCT = r.Header.Get("Content-Type")
		b := new(bytes.Buffer)
		b.ReadFrom(r.Body)
		gotBody = b.String()
	}, WithRetries(0))
	cfg := &ScrapeConfig{URL: "https://e.com", Headers: map[string]string{"X-From-Config": "1", "X-Both": "config"}}
	_, err := c.Do(context.Background(), cfg, &Request{
		JSON:    map[string]any{"a": 1, "u": "\u2028<"},
		Headers: map[string]string{"X-Both": "call"},
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if gotMethod != "POST" || gotHeader != "1" || gotOverride != "call" || gotBody != "{\"a\":1,\"u\":\"\u2028<\"}" ||
		gotCT != "application/json" {
		t.Errorf("method=%q header=%q override=%q body=%q ct=%q", gotMethod, gotHeader, gotOverride, gotBody, gotCT)
	}
}

func TestDoRejectsBadRequests(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("an invalid request reached the wire")
	}, WithRetries(0))
	ctx := context.Background()
	cfg := &ScrapeConfig{URL: "https://e.com"}
	for name, req := range map[string]*Request{
		"unserializable JSON": {JSON: map[string]any{"f": func() {}}},
		"NaN":                 {JSON: []any{math.NaN()}},
	} {
		var ce *ConfigError
		if _, err := c.Do(ctx, cfg, req); !errors.As(err, &ce) {
			t.Errorf("%s: want ConfigError, got %v", name, err)
		}
	}
	var ce *ConfigError
	var nilCtx context.Context
	if _, err := c.Scrape(nilCtx, cfg); !errors.As(err, &ce) {
		t.Errorf("nil context: want ConfigError, got %v", err)
	}
	if _, err := c.Do(ctx, nil, nil); !errors.As(err, &ce) {
		t.Errorf("nil config: want ConfigError, got %v", err)
	}
	if _, err := c.Scrape(ctx, nil); !errors.As(err, &ce) {
		t.Errorf("Scrape(ctx, nil): want ConfigError, got %v", err)
	}
}

func TestRequestRejectsInvalidConfig(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {}, WithRetries(0))
	_, err := c.Scrape(context.Background(), &ScrapeConfig{})
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Errorf("want ConfigError for a config with no URL, got %v", err)
	}
}

// The 30s cap applies to Retry-After and to the exponential backoff alike;
// the wait itself is observed, not slept.
func TestSleepCapsEveryWait(t *testing.T) {
	var waited []time.Duration
	tr := &transport{backoff: time.Minute, wait: func(_ context.Context, d time.Duration) { waited = append(waited, d) }}
	tr.sleep(context.Background(), 0, 40*time.Second, true) // Retry-After above the cap
	tr.sleep(context.Background(), 0, 3*time.Second, true)  // Retry-After below it
	tr.sleep(context.Background(), 3, 0, false)             // backoff 1m×8
	tr.sleep(context.Background(), 0, 0, true)              // Retry-After: 0
	want := []time.Duration{maxBackoff, 3 * time.Second, maxBackoff}
	if fmt.Sprint(waited) != fmt.Sprint(want) {
		t.Errorf("waits = %v, want %v", waited, want)
	}
	tr = &transport{backoff: 10 * time.Millisecond, wait: func(_ context.Context, d time.Duration) { waited = []time.Duration{d} }}
	tr.sleep(context.Background(), 2, 0, false)
	if d := waited[0]; d < 40*time.Millisecond || d >= 140*time.Millisecond {
		t.Errorf("backoff for attempt 2 = %v, want 40ms plus < 100ms jitter", d)
	}
}

// The real timer honours cancellation and the requested duration.
func TestSleepTimer(t *testing.T) {
	tr := &transport{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	tr.sleep(ctx, 0, 20*time.Second, true)
	if time.Since(start) > time.Second {
		t.Errorf("sleep ignored a cancelled context")
	}
	start = time.Now()
	tr.sleep(context.Background(), 0, 5*time.Millisecond, true)
	if time.Since(start) < 5*time.Millisecond {
		t.Errorf("sleep returned before Retry-After elapsed")
	}
}

func TestAPIErrorShapes(t *testing.T) {
	cases := []struct {
		name   string
		status int
		header http.Header
		body   string
		check  func(t *testing.T, e *APIError)
	}{
		{"unknown 4xx is KindOther", 418, nil, "", func(t *testing.T, e *APIError) {
			if e.Kind != KindOther || e.Kind.String() != "other" || !strings.Contains(e.Message, "HTTP 418") {
				t.Errorf("got kind=%v msg=%q", e.Kind, e.Message)
			}
		}},
		{"429 carries the remaining credits", 429,
			http.Header{"Scrape.do-Remaining-Credits": {"42"}}, "",
			func(t *testing.T, e *APIError) {
				if rc, ok := e.RemainingCredits(); rc != 42 || !ok || !e.IsConcurrency() {
					t.Errorf("credits=%d,%v", rc, ok)
				}
			}},
		{"long bodies are truncated to 2 KiB", 400, nil, strings.Repeat("b", 5000),
			func(t *testing.T, e *APIError) {
				if len(e.Body) != 2048 {
					t.Errorf("body length = %d, want 2048", len(e.Body))
				}
			}},
		{"non-envelope JSON keeps the default message", 400, nil, `{"other":1}`,
			func(t *testing.T, e *APIError) {
				if e.Message != "The gateway rejected the request parameters." {
					t.Errorf("message = %q", e.Message)
				}
			}},
		{"an envelope message that is not text falls through to error", 400, nil, `{"message":5,"error":"bad key"}`,
			func(t *testing.T, e *APIError) {
				if e.Message != "bad key" {
					t.Errorf("message = %q", e.Message)
				}
			}},
		{"an unparseable Retry-After is ignored", 503, http.Header{"Retry-After": {"soon"}}, "",
			func(t *testing.T, e *APIError) {
				if d, ok := e.RetryAfter(); d != 0 || ok || !e.IsRetryable {
					t.Errorf("retryAfter=%v,%v retryable=%v", d, ok, e.IsRetryable)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.header
			if h == nil {
				h = http.Header{}
			}
			tc.check(t, (&transport{}).newAPIError(tc.status, h, []byte(tc.body), false))
		})
	}
}

func TestDecompressCapsUnencodedAndUnknownBodies(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 64)
	for _, enc := range []string{"", "identity", "zstd"} {
		_, err := decompress(big, enc, 16)
		var tooLarge *ResponseTooLargeError
		if !errors.As(err, &tooLarge) {
			t.Errorf("encoding %q: want ResponseTooLargeError, got %v", enc, err)
		}
		if out, err := decompress(big, enc, 1024); err != nil || !bytes.Equal(out, big) {
			t.Errorf("encoding %q under the cap: out=%d err=%v", enc, len(out), err)
		}
	}
	// A gzip stream that inflates past the cap is refused mid-read.
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(bytes.Repeat([]byte{0}, 4096))
	zw.Close()
	if _, err := decompress(buf.Bytes(), "gzip", 1024); err == nil {
		t.Error("gzip bomb under a 1 KiB cap was not refused")
	}
}

// Invalid headers are a ConfigError before anything is sent — never a retried
// connection failure — whether they come from the config or the request.
func TestInvalidHeadersAreConfigErrors(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("a request with an invalid header reached the wire")
	}, WithRetries(2))
	ctx := context.Background()
	for _, h := range []map[string]string{
		{"X-A": "a\r\nX-B: b"}, {"X-A": "café"}, {"X-A": "a\x00b"}, {"X-A": "a\x7fb"},
		{"Bad Name": "x"}, {"": "x"}, {"X:Y": "x"}, {"Host": "evil.example"}, {"content-length": "1"},
		{"Transfer-Encoding": "chunked"}, {"CONNECTION": "close"},
	} {
		var ce *ConfigError
		if _, err := c.Do(ctx, &ScrapeConfig{URL: "https://e.com", Headers: h}, nil); !errors.As(err, &ce) {
			t.Errorf("config headers %q: want ConfigError, got %v", h, err)
		}
		if _, err := c.Do(ctx, &ScrapeConfig{URL: "https://e.com"}, &Request{Headers: h}); !errors.As(err, &ce) {
			t.Errorf("request headers %q: want ConfigError, got %v", h, err)
		}
	}
	var ce *ConfigError
	if _, err := New("t", WithAcceptEncoding("gzip\r\nX: y")); !errors.As(err, &ce) {
		t.Errorf("WithAcceptEncoding with CRLF: want ConfigError, got %v", err)
	}
	// A secret-looking value is not echoed in the message.
	_, err := c.Do(ctx, &ScrapeConfig{URL: "https://e.com"}, &Request{Headers: map[string]string{"Authorization": "Bearer s3cr3t\n"}})
	if err == nil || strings.Contains(err.Error(), "s3cr3t") {
		t.Errorf("header value echoed: %v", err)
	}
}

// Header names compare case-insensitively: the request's value replaces the
// config's and the SDK's own, and is sent once.
func TestHeaderNamesAreCaseInsensitive(t *testing.T) {
	var got http.Header
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) { got = r.Header.Clone() }, WithRetries(0))
	cfg := &ScrapeConfig{URL: "https://e.com", Headers: map[string]string{"x-both": "config", "USER-AGENT": "cfg-ua"}}
	if _, err := c.Do(context.Background(), cfg, &Request{Headers: map[string]string{"X-BOTH": "call", "accept-encoding": "br"}}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	for name, want := range map[string][]string{"X-Both": {"call"}, "User-Agent": {"cfg-ua"}, "Accept-Encoding": {"br"}} {
		if v := got.Values(name); fmt.Sprint(v) != fmt.Sprint(want) {
			t.Errorf("%s = %q, want %q", name, v, want)
		}
	}
}

// An error status wins over its body: a body that is corrupt, too large or
// breaks off still yields the status error with the default message.
func TestErrorStatusSurvivesAnUnreadableBody(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"corrupt gzip": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			w.WriteHeader(400)
			w.Write([]byte("\x1f\x8b\x08\x00garbage"))
		},
		"over the raw cap": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(400)
			w.Write(bytes.Repeat([]byte("x"), 4096))
		},
		"breaks off": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(400)
			w.Write([]byte(`{"Message":`))
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
		},
	}
	for name, h := range cases {
		c, _ := newClient(t, h, WithRetries(0), WithMaxResponseBytes(1024))
		_, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 || apiErr.Message != msgValidation || apiErr.Billed {
			t.Errorf("%s: want the 400 validation error, got %T %v", name, err, err)
		}
	}
}

// An empty body is empty whatever its Content-Encoding: HEAD, 204, 304 and an
// empty 503 (retried, then answered).
func TestEmptyEncodedBodies(t *testing.T) {
	var calls atomic.Int32
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		switch r.URL.Query().Get("url") {
		case "https://e.com/204":
			w.WriteHeader(204)
		case "https://e.com/304":
			w.WriteHeader(304)
		case "https://e.com/503":
			if calls.Add(1) == 1 {
				w.WriteHeader(503)
				return
			}
			w.Header().Del("Content-Encoding")
			w.Write([]byte("ok"))
		default:
			w.Header().Set("Content-Length", "25")
			w.WriteHeader(200)
		}
	}, WithRetries(2))
	ctx := context.Background()
	for _, target := range []string{"https://e.com/204", "https://e.com/304"} {
		if res, err := c.Scrape(ctx, &ScrapeConfig{URL: target}); err != nil || len(res.Bytes()) != 0 {
			t.Errorf("%s: res=%v err=%v", target, res, err)
		}
	}
	if res, err := c.Do(ctx, &ScrapeConfig{URL: "https://e.com/head"}, &Request{Method: "HEAD"}); err != nil || len(res.Bytes()) != 0 {
		t.Errorf("HEAD: res=%v err=%v", res, err)
	}
	if res, err := c.Scrape(ctx, &ScrapeConfig{URL: "https://e.com/503"}); err != nil || string(res.Bytes()) != "ok" || calls.Load() != 2 {
		t.Errorf("empty 503 then 200: res=%v err=%v calls=%d", res, err, calls.Load())
	}
}

// With a gateway Timeout set, one attempt waits at least that long plus the
// margin, so a client timeout below it does not abandon the request.
func TestConfigTimeoutRaisesTheAttemptDeadline(t *testing.T) {
	var calls atomic.Int32
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(150 * time.Millisecond)
		w.Write([]byte("slow but fine"))
	}, WithRetries(2), WithTimeout(50*time.Millisecond))
	res, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com", Timeout: 5000})
	if err != nil || string(res.Bytes()) != "slow but fine" || calls.Load() != 1 {
		t.Fatalf("res=%v err=%v calls=%d", res, err, calls.Load())
	}
	// Without it, the 50ms client timeout applies, and it is not retried: the
	// request was sent.
	calls.Store(0)
	_, err = c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	var te *TransportError
	if !errors.As(err, &te) || !te.Timeout || !strings.Contains(te.Message, "50ms") || calls.Load() != 1 {
		t.Errorf("want one timed-out attempt naming 50ms, got %v (calls=%d)", err, calls.Load())
	}
}

// The context's own deadline and cancellation read as such.
func TestContextErrorsAreNamed(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(200 * time.Millisecond) }, WithRetries(0))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.Scrape(ctx, &ScrapeConfig{URL: "https://e.com"})
	var te *TransportError
	if !errors.As(err, &te) || !te.Timeout || !strings.Contains(te.Message, "context deadline") {
		t.Errorf("deadline: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	_, err = c.Scrape(ctx, &ScrapeConfig{URL: "https://e.com"})
	if !errors.As(err, &te) || te.Timeout || te.Message != "request canceled" || !errors.Is(err, context.Canceled) {
		t.Errorf("cancel: %#v", err)
	}
}
