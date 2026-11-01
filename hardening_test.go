// SPDX-License-Identifier: Apache-2.0

package sdo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Logging a client, its job API or anything holding them must not print the
// token, whatever the verb.
func TestClientsDoNotPrintTheToken(t *testing.T) {
	cleanEnv(t)
	c, err := New(secretToken)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	jobs := c.Jobs()
	holder := struct {
		C    *Client
		J    *AsyncJobs
		Copy Client
		hid  Client // unexported: fmt cannot call its methods
		hidJ AsyncJobs
	}{c, jobs, *c, *c, *jobs}
	for _, v := range []any{c, *c, jobs, *jobs, holder, &holder} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			if out := fmt.Sprintf(verb, v); strings.Contains(out, secretToken) {
				t.Errorf("%s of %T leaks the token: %s", verb, v, out)
			}
		}
	}
	if got := c.String(); !strings.Contains(got, "https://api.scrape.do") || !strings.Contains(got, "***") {
		t.Errorf("String() = %q", got)
	}
	var nilClient *Client
	if got := fmt.Sprintf("%v %#v", nilClient, nilClient); got != "<nil> <nil>" {
		t.Errorf("a nil *Client prints %q", got)
	}
	if got := jobs.GoString(); !strings.Contains(got, "q.scrape.do") || strings.Contains(got, secretToken) {
		t.Errorf("AsyncJobs.GoString() = %q", got)
	}
}

func TestClientRedact(t *testing.T) {
	c, _ := New(secretToken)
	in := "echo " + secretToken + " and ?api_key=zzz and X-Token: abc"
	if got := c.Redact(in); got != "echo *** and ?api_key=*** and X-Token: ***" {
		t.Errorf("Redact = %q", got)
	}
}

// The job API sees the token in X-Token; if its error echoes the request, the
// message and retained body are scrubbed.
func TestTokenNeverInJobsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprintf(w, `{"Error":"bad key %s","Code":401,"Echo":"X-Token: %s"}`, r.Header.Get("X-Token"), r.Header.Get("X-Token"))
	}))
	defer srv.Close()
	c, _ := New(secretToken, WithAsyncBaseURL(srv.URL), WithRetries(0))
	_, err := c.Jobs().Status(context.Background(), "job-1")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !apiErr.IsAuth() {
		t.Fatalf("want an auth APIError, got %v", err)
	}
	if strings.Contains(apiErr.Body+apiErr.Message+errSurface(err), secretToken) {
		t.Errorf("token leaked: message=%q body=%q", apiErr.Message, apiErr.Body)
	}
}

// A caller's http.Client follows redirects by default. The SDK must not: on the
// job API that would forward X-Token to wherever the Location points.
func TestRedirectsNotFollowedEvenWithACallerClient(t *testing.T) {
	var elsewhere atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
	}))
	defer evil.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	follows := &http.Client{} // default policy: follow up to 10 redirects
	c, _ := New(secretToken, WithHTTPClient(follows), WithBaseURL(redirector.URL),
		WithAsyncBaseURL(redirector.URL), WithRetries(0))

	var apiErr *APIError
	if _, err := c.Jobs().Me(context.Background()); !errors.As(err, &apiErr) || apiErr.StatusCode != 307 {
		t.Errorf("job API 3xx: want an APIError(307), got %v", err)
	}
	res, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	if err != nil || res.StatusCode != 307 {
		t.Errorf("sync 3xx: want a 307 result, got %v %v", res, err)
	}
	if elsewhere.Load() != 0 {
		t.Errorf("a redirect was followed %d times", elsewhere.Load())
	}
	if follows.CheckRedirect != nil {
		t.Error("the caller's client was mutated")
	}
}

// Job responses carry target content, so they are capped like scrapes.
func TestJobsResponsesAreCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"TaskID":"t","Content":"%s"}`, strings.Repeat("x", 4096))
	}))
	defer srv.Close()
	c, _ := New("t", WithAsyncBaseURL(srv.URL), WithMaxResponseBytes(1024), WithRetries(0))
	var tooLarge *ResponseTooLargeError
	if _, err := c.Jobs().Result(context.Background(), "j", "t"); !errors.As(err, &tooLarge) {
		t.Errorf("want ResponseTooLargeError, got %v", err)
	}
}

// Job calls share the scrape retry policy: GET retries a transient status,
// POST retries only a 429.
func TestJobsRetryPolicy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		call   func(*AsyncJobs) error
		want   int32
	}{
		{"GET 503", 503, func(j *AsyncJobs) error { _, err := j.Status(context.Background(), "j"); return err }, 3},
		{"POST 503", 503, func(j *AsyncJobs) error {
			_, err := j.Create(context.Background(), []string{"https://e.com"}, nil)
			return err
		}, 1},
		{"POST 429", 429, func(j *AsyncJobs) error {
			_, err := j.Create(context.Background(), []string{"https://e.com"}, nil)
			return err
		}, 3},
	} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(tc.status)
		}))
		c, _ := New("t", WithAsyncBaseURL(srv.URL), WithRetries(2), WithBackoff(0))
		if err := tc.call(c.Jobs()); err == nil {
			t.Errorf("%s: want an error", tc.name)
		}
		if calls.Load() != tc.want {
			t.Errorf("%s: %d attempts, want %d", tc.name, calls.Load(), tc.want)
		}
		srv.Close()
	}
}

func TestTaskContentBytesRejectsBadBase64(t *testing.T) {
	tr := &TaskResult{Base64EncodedContent: true, Content: "not base64!"}
	var te *TransportError
	if _, err := tr.ContentBytes(); !errors.As(err, &te) {
		t.Errorf("want TransportError, got %v", err)
	}
}

func TestWithUserAgent(t *testing.T) {
	var got string
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) { got = r.Header.Get("User-Agent") },
		WithUserAgent("sdo-cli/1.2.3-rc.1"), nil)
	if _, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"}); err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if got != "sdo-cli/1.2.3-rc.1" {
		t.Errorf("User-Agent = %q", got)
	}
	for _, ua := range []string{"", "curl/8.0", "sdo-cli/1.2", "sdo-sdk-go/1.2.3 (linux)", "sdo-cli/1.2.3\n"} {
		var ce *ConfigError
		if _, err := New("t", WithUserAgent(ua)); !errors.As(err, &ce) {
			t.Errorf("WithUserAgent(%q): want ConfigError, got %v", ua, err)
		}
	}
}

// WithTimeout wins; otherwise a caller client's own Timeout is kept; otherwise
// SDO_TIMEOUT or the default applies.
func TestTimeoutPrecedence(t *testing.T) {
	cleanEnv(t)
	own := &http.Client{Timeout: 7 * time.Second}
	cases := []struct {
		name string
		opts []Option
		env  string
		want time.Duration
	}{
		// The gateway's 60s default timeout plus the 10s margin.
		{"default", nil, "", 70 * time.Second},
		{"env", nil, "2.5", 2500 * time.Millisecond},
		{"explicit beats env", []Option{WithTimeout(time.Second)}, "9", time.Second},
		{"caller client kept", []Option{WithHTTPClient(own)}, "9", 7 * time.Second},
		{"explicit beats caller client", []Option{WithHTTPClient(own), WithTimeout(time.Second)}, "", time.Second},
		{"huge env saturates", nil, "99999999999999999999", time.Duration(1<<63 - 1)},
		{"env rounds to the nanosecond", nil, "0.0015", 1500 * time.Microsecond},
	}
	for _, c := range cases {
		t.Setenv("SDO_TIMEOUT", c.env)
		client, err := New("t", c.opts...)
		if err != nil {
			t.Errorf("%s: New: %v", c.name, err)
			continue
		}
		if client.timeout != c.want {
			t.Errorf("%s: timeout = %v; want %v", c.name, client.timeout, c.want)
		}
	}
}

// A body that stalls past the client deadline is a timeout, not a generic
// read failure.
func TestBodyReadDeadlineIsMarkedTimeout(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		time.Sleep(300 * time.Millisecond)
	}, WithRetries(0), WithTimeout(80*time.Millisecond))
	_, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	var te *TransportError
	if !errors.As(err, &te) || !te.Timeout {
		t.Errorf("want TransportError{Timeout: true}, got %#v", err)
	}
}

func TestRenderModeRules(t *testing.T) {
	var ce *ConfigError
	if _, err := (&ScrapeConfig{URL: "https://e.com", Render: true, RenderMode: "FALSE"}).toParams(); !errors.As(err, &ce) {
		t.Errorf("Render + RenderMode false: want ConfigError, got %v", err)
	}
	pairs, err := (&ScrapeConfig{URL: "https://e.com", Render: false, RenderMode: "TRUE"}).toParams()
	if err != nil || len(pairs) != 2 || pairs[1] != (param{"render", "true"}) {
		t.Errorf("RenderMode wins over Render: %v %v", pairs, err)
	}
	pairs, err = (&ScrapeConfig{URL: "https://e.com", RenderMode: "false"}).toParams()
	if err != nil || len(pairs) != 1 {
		t.Errorf(`RenderMode "false" is not sent: %v %v`, pairs, err)
	}
	// A range error names the unit.
	_, err = (&ScrapeConfig{URL: "https://e.com", Timeout: 30}).toParams()
	if err == nil || !strings.Contains(err.Error(), "milliseconds") {
		t.Errorf("range error should name the unit: %v", err)
	}
}

func TestValidTargetURL(t *testing.T) {
	for u, ok := range map[string]bool{
		"https://user:pw@e.com/x": true,
		"https://[::1]:8080/":     true,
		"http://e.com:80":         true,
		"https://[::1":            false,
		"https://:80":             false,
		"https:// /x":             false,
		"https:e.com":             false,
		"mailto:x@e.com":          false,
		"":                        false,
	} {
		if validTargetURL(u) != ok {
			t.Errorf("validTargetURL(%q) = %v, want %v", u, !ok, ok)
		}
	}
}

type colour string

func TestPluginParamValues(t *testing.T) {
	var nilMap map[string]int
	got, err := pluginParams(map[string]any{
		"named": colour("red"), "when": time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		"empty": nilMap, "obj": map[string]any{"b": 1, "a": true}, "raw": []byte("hi"),
	})
	if err != nil {
		t.Fatalf("pluginParams: %v", err)
	}
	want := []param{{"named", "red"}, {"obj", `{"a":true,"b":1}`}, {"raw", "aGk="}, {"when", "2026-01-02T00:00:00Z"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("pluginParams = %v, want %v", got, want)
	}
	for _, bad := range []map[string]any{{"f": func() {}}, {"l": []any{make(chan int)}}} {
		var ce *ConfigError
		if _, err := pluginParams(bad); !errors.As(err, &ce) {
			t.Errorf("%v: want ConfigError, got %v", bad, err)
		}
	}
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("reached the wire") })
	var ce *ConfigError
	if _, err := c.Plugin(context.Background(), "google/search", map[string]any{"f": func() {}}); !errors.As(err, &ce) {
		t.Errorf("Plugin with an unserializable param: want ConfigError, got %v", err)
	}
}

func TestRetryAfterEdgeCases(t *testing.T) {
	if secs, ok := parseRetryAfter("99999999999999999999999"); !ok || secs != 30 {
		t.Errorf("overflowing Retry-After = %d, %v; want the 30s cap", secs, ok)
	}
	// Retry-After: 0 is honoured (no wait), not replaced by the backoff.
	var calls atomic.Int32
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(503)
		}
	}, WithRetries(1), WithBackoff(time.Hour))
	start := time.Now()
	if _, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"}); err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("Retry-After: 0 waited for the backoff")
	}
	// A plain backoff adds jitter and still returns.
	(&transport{backoff: time.Millisecond}).sleep(context.Background(), 1, 0, false)
}

// Response bodies stay intact on the Result, repeated headers included.
func TestResultExposesRepeatedHeaders(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "a=1")
		w.Header().Add("Set-Cookie", "b=2")
		io.Copy(w, bytes.NewReader([]byte("ok")))
	})
	res, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	if err != nil || len(res.Header.Values("Set-Cookie")) != 2 {
		t.Errorf("Set-Cookie values = %v, %v", res.Header.Values("Set-Cookie"), err)
	}
}
