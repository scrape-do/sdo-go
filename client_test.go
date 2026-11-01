// SPDX-License-Identifier: Apache-2.0

package sdo

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
)

// TestMain makes the suite hermetic: every SDO_* variable of the caller's
// environment is removed for the run (and restored afterwards), so a
// developer's SDO_TIMEOUT or SDO_BASE_URL cannot change a result.
func TestMain(m *testing.M) {
	saved := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "SDO_") {
			saved[k] = v
			os.Unsetenv(k)
		}
	}
	code := m.Run()
	for k, v := range saved {
		os.Setenv(k, v)
	}
	os.Exit(code)
}

func newClient(t *testing.T, handler http.HandlerFunc, opts ...Option) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	all := append([]Option{WithBaseURL(srv.URL), WithBackoff(0)}, opts...)
	c, err := New("t", all...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, srv
}

func TestScrapeBuildsRequestAndResult(t *testing.T) {
	var gotEncoding, gotUA, gotToken, gotURL, gotRender, gotGeo string
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotEncoding = r.Header.Get("Accept-Encoding")
		gotUA = r.Header.Get("User-Agent")
		q := r.URL.Query()
		gotToken, gotURL, gotRender, gotGeo = q.Get("token"), q.Get("url"), q.Get("render"), q.Get("geoCode")
		w.Header().Set("Scrape.do-Request-Cost", "5")
		w.Write([]byte("<html>ok</html>"))
	})
	res, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://example.com", Render: true, GeoCode: "us"})
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if res.StatusCode != 200 || !bytes.Contains(res.Bytes(), []byte("ok")) {
		t.Errorf("result: %d %s", res.StatusCode, res.Bytes())
	}
	if cost, _ := res.Cost(); cost != 5 {
		t.Errorf("metadata: cost=%d", cost)
	}
	if gotEncoding != "gzip, deflate, br" {
		t.Errorf("Accept-Encoding = %q", gotEncoding)
	}
	if gotUA == "" || gotUA[:11] != "sdo-sdk-go/" {
		t.Errorf("User-Agent = %q", gotUA)
	}
	if gotToken != "t" || gotURL != "https://example.com" || gotRender != "true" || gotGeo != "us" {
		t.Errorf("params: token=%q url=%q render=%q geo=%q", gotToken, gotURL, gotRender, gotGeo)
	}
}

func TestTransparentGzip(t *testing.T) {
	body := []byte("<html>hello compressed</html>")
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	gw.Write(body)
	gw.Close()
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(buf.Bytes())
	})
	res, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if !bytes.Equal(res.Bytes(), body) {
		t.Errorf("gzip decompress: got %q", res.Bytes())
	}
}

func TestTransparentBrotli(t *testing.T) {
	body := []byte("<html>brotli body</html>")
	var buf bytes.Buffer
	bw := brotli.NewWriter(&buf)
	bw.Write(body)
	bw.Close()
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "br")
		w.Write(buf.Bytes())
	}, WithPreferBrotli())
	res, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if !bytes.Equal(res.Bytes(), body) {
		t.Errorf("brotli decompress: got %q", res.Bytes())
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		status int
		kind   ErrorKind
		billed bool
	}{
		// No 402 row: the gateway has no quota tier — an exhausted credit
		// balance also returns 401.
		{401, KindAuth, false},
		{400, KindValidation, false},
		{503, KindGateway, false},
	}
	for _, tc := range cases {
		c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		}, WithRetries(0))
		_, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("status %d: expected APIError, got %v", tc.status, err)
		}
		if apiErr.Kind != tc.kind || apiErr.Billed != tc.billed {
			t.Errorf("status %d: kind=%v billed=%v", tc.status, apiErr.Kind, apiErr.Billed)
		}
	}
}

func TestTarget404IsResult(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte("not found"))
	})
	res, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	if err != nil {
		t.Fatalf("404 should be a result, got error: %v", err)
	}
	if res.StatusCode != 404 {
		t.Errorf("status = %d", res.StatusCode)
	}
}

func TestRetryThenSucceed(t *testing.T) {
	var calls int32
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		}
		w.Write([]byte("ok"))
	})
	res, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if res.StatusCode != 200 || atomic.LoadInt32(&calls) != 2 {
		t.Errorf("calls=%d status=%d", calls, res.StatusCode)
	}
}

func TestConcurrencyExhausted(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(429)
	}, WithRetries(1))
	_, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !apiErr.IsConcurrency() || !apiErr.IsRetryable {
		t.Fatalf("expected retryable concurrency error, got %v", err)
	}
}

func TestConnectionError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // now unreachable
	c, _ := New("t", WithBaseURL(url), WithRetries(0), WithBackoff(0))
	_, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	var te *TransportError
	if !errors.As(err, &te) {
		t.Fatalf("expected TransportError, got %v", err)
	}
}

func TestTokenPrecedence(t *testing.T) {
	t.Setenv("SDO_TOKEN", "envtok")
	var got string
	handler := func(w http.ResponseWriter, r *http.Request) { got = r.URL.Query().Get("token"); w.Write([]byte("x")) }
	srv := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(srv.Close)

	c, _ := New("paramtok", WithBaseURL(srv.URL))
	c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	if got != "paramtok" {
		t.Errorf("explicit token should win, got %q", got)
	}

	c2, _ := New("", WithBaseURL(srv.URL))
	c2.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	if got != "envtok" {
		t.Errorf("env token should be used, got %q", got)
	}
}

func TestNoTokenErrors(t *testing.T) {
	t.Setenv("SDO_TOKEN", "")
	if _, err := New(""); err == nil {
		t.Fatal("expected ConfigError when no token")
	}
}

func TestPreferBrotliHeader(t *testing.T) {
	var ae string
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		ae = r.Header.Get("Accept-Encoding")
		w.Write([]byte("x"))
	}, WithPreferBrotli())
	c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	if ae != "br" {
		t.Errorf("Accept-Encoding = %q, want br", ae)
	}
}

func TestPluginReturnsJSON(t *testing.T) {
	var path, q string
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		q = r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[1,2]}`))
	})
	out, err := c.Plugin(context.Background(), "google/shopping", map[string]any{"q": "laptop", "gl": "us"})
	if err != nil {
		t.Fatalf("Plugin: %v", err)
	}
	m, ok := out.(map[string]any)
	if !ok || path != "/plugin/google/shopping" || q != "laptop" {
		t.Errorf("plugin: out=%v path=%q q=%q", m, path, q)
	}
}

// A token is trimmed and must be visible ASCII; the error never echoes it and
// names where it came from.
func TestTokenNormalization(t *testing.T) {
	t.Setenv("SDO_TOKEN", "")
	c, err := New(" tok_1234567890\n")
	if err != nil || c.t.token() != "tok_1234567890" {
		t.Fatalf("trim: %v", err)
	}
	var ce *ConfigError
	if _, err := New("tok 123"); !errors.As(err, &ce) || strings.Contains(err.Error(), "tok 123") || !strings.Contains(err.Error(), "the token") {
		t.Errorf("inner space: %v", err)
	}
	t.Setenv("SDO_TOKEN", "tök")
	if _, err := New(""); !errors.As(err, &ce) || !strings.Contains(err.Error(), "SDO_TOKEN") {
		t.Errorf("non-ASCII SDO_TOKEN: %v", err)
	}
	t.Setenv("SDO_TOKEN", "  \t")
	if _, err := New(" "); !errors.As(err, &ce) || !strings.Contains(err.Error(), "No token") {
		t.Errorf("blank everywhere: %v", err)
	}
}

// Base URLs keep a path prefix and refuse anything that would corrupt the
// request URL appended to them.
func TestBaseURLGrammar(t *testing.T) {
	for raw, want := range map[string]string{
		"https://gw.example/pfx/": "https://gw.example/pfx",
		"HTTPS://GW.EXAMPLE:8443": "HTTPS://GW.EXAMPLE:8443",
		"http://[::1]:9/a":        "http://[::1]:9/a",
	} {
		c, err := New("t", WithBaseURL(raw))
		if err != nil {
			t.Errorf("%q: %v", raw, err)
		} else if c.baseURL != want {
			t.Errorf("%q: base=%q, want %q", raw, c.baseURL, want)
		}
	}
	for _, raw := range []string{
		"https://gw.example/?", "https://gw.example?x=1", "https://gw.example#f", "https://u:p@gw.example",
		"https:///gw.example", "https://gw.example:0", "https://gw.example:99999", "https://gw example",
		"https:gw.example", "gw.example", "ftp://gw.example", "https://[::1", "https://[::1]x",
	} {
		var ce *ConfigError
		_, err := New("t", WithAsyncBaseURL(raw))
		if !errors.As(err, &ce) {
			t.Errorf("%q: want ConfigError, got %v", raw, err)
		} else if strings.Contains(err.Error(), ":p@") {
			t.Errorf("%q: credentials echoed: %v", raw, err)
		}
	}
}

// SDO_TIMEOUT is a plain decimal of at least 0.001 s: nothing rounds down to a
// disabled timeout, and no other number syntax is accepted.
func TestTimeoutEnvGrammar(t *testing.T) {
	for _, bad := range []string{"0.0009", "1e-12", "0x1p4", "1_0", "  ", "30 ", "٣", "+1", ".5", "Inf"} {
		t.Setenv("SDO_TIMEOUT", bad)
		var ce *ConfigError
		if _, err := New("t"); !errors.As(err, &ce) {
			t.Errorf("SDO_TIMEOUT=%q: want ConfigError, got %v", bad, err)
		}
	}
	t.Setenv("SDO_TIMEOUT", "0.001")
	if c, err := New("t"); err != nil {
		t.Errorf("0.001: %v", err)
	} else if c.timeout != time.Millisecond {
		t.Errorf("0.001: timeout = %v", c.timeout)
	}
}

// Target 404 and 410 pages are results, never errors, on the scrape path.
func TestTarget404And410AreResults(t *testing.T) {
	for _, status := range []int{404, 410} {
		c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			w.Write([]byte("page"))
		}, WithRetries(0))
		res, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
		if err != nil || res.StatusCode != status || string(res.Bytes()) != "page" {
			t.Errorf("status %d: res=%v err=%v", status, res, err)
		}
	}
}

// The gateway answers a parameter it cannot parse (render=mert) before it
// authorizes the request: a 400 with its JSON envelope and no
// Scrape.do-Request-Cost. That is an unbilled validation error carrying the
// gateway's message. A 400 the target answered is billed (a positive cost) and
// is the target's page.
func TestGateway400VersusTarget400(t *testing.T) {
	gw, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("render") != "mert" {
			t.Errorf("render = %q", r.URL.Query().Get("render"))
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(400)
		w.Write([]byte(`{"URL":"https://e.com","StatusCode":400,"Message":["'Render' parameter should boolean."],` +
			`"PossibleCauses":["Please check your request."],"Contact":"Please don't hesitate to contact with us via support@scrape.do."}`))
	}, WithRetries(2))
	_, err := gw.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com", ExtraParams: map[string]string{"render": "mert"}})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !apiErr.IsValidation() || apiErr.Billed || apiErr.IsRetryable ||
		apiErr.Message != "'Render' parameter should boolean." {
		t.Fatalf("want the gateway's unbilled validation error, got %v", err)
	}

	target, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Scrape.do-Request-Cost", "1")
		w.Header().Set("Scrape.do-Initial-Status-Code", "400")
		w.WriteHeader(400)
		w.Write([]byte("<html>Bad Request</html>"))
	})
	res, err := target.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	if err != nil || res.StatusCode != 400 || string(res.Bytes()) != "<html>Bad Request</html>" {
		t.Fatalf("want the target's 400 page as a result, got %v, %v", res, err)
	}
	if cost, ok := res.Cost(); !ok || cost != 1 {
		t.Errorf("cost = %d, %v", cost, ok)
	}
}

// Scrape.do-Request-Cost is authoritative: any status the gateway billed is the
// target's page, on the scrape and plugin paths alike, and is never retried;
// without a positive cost a status keeps its gateway meaning, and Billed
// follows the header when there is one.
func TestRequestCostDecidesOriginAndBilling(t *testing.T) {
	for _, status := range []int{401, 403, 405, 422, 429, 500} {
		var calls atomic.Int32
		c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Scrape.do-Request-Cost", "3")
			w.WriteHeader(status)
			w.Write([]byte("target page"))
		}, WithRetries(2))
		res, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
		if err != nil || res.StatusCode != status || string(res.Bytes()) != "target page" {
			t.Errorf("billed %d: res=%v err=%v", status, res, err)
		} else if cost, ok := res.Cost(); !ok || cost != 3 {
			t.Errorf("billed %d: cost = %d,%v", status, cost, ok)
		}
		if out, err := c.Plugin(context.Background(), "google/search", map[string]any{"q": "x"}); err != nil ||
			string(out.([]byte)) != "target page" {
			t.Errorf("billed %d plugin: %v, %v", status, out, err)
		}
		if n := calls.Load(); n != 2 {
			t.Errorf("billed %d: %d calls, want one per call (never retried)", status, n)
		}
	}

	cases := []struct {
		name     string
		status   int
		cost     string // "" = no header
		kind     ErrorKind
		billed   bool
		wantCost int
		hasCost  bool
	}{
		{"gateway 401 (refused token)", 401, "", KindAuth, false, 0, false},
		{"401 with cost 0", 401, "0", KindAuth, false, 0, true},
		{"gateway 400", 400, "", KindValidation, false, 0, false},
		{"unbilled 502", 502, "0", KindGateway, false, 0, true},
		{"cost over 2^53-1 is unreadable", 401, "9007199254740992", KindAuth, false, 0, false},
	}
	for _, tc := range cases {
		c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			if tc.cost != "" {
				w.Header().Set("Scrape.do-Request-Cost", tc.cost)
			}
			w.WriteHeader(tc.status)
		}, WithRetries(0))
		_, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Kind != tc.kind || apiErr.Billed != tc.billed {
			t.Errorf("%s: got %v", tc.name, err)
			continue
		}
		if cost, ok := apiErr.Cost(); cost != tc.wantCost || ok != tc.hasCost {
			t.Errorf("%s: Cost() = %d,%v", tc.name, cost, ok)
		}
	}
}

// Result.Cost and the origin rule read the header the same way: one value,
// ASCII digits after trimming spaces and tabs, at most 2^53-1.
func TestRequestCostParsing(t *testing.T) {
	cases := []struct {
		values []string
		cost   int
		ok     bool
	}{
		{nil, 0, false},
		{[]string{"7"}, 7, true},
		{[]string{" 7\t"}, 7, true},
		{[]string{"0"}, 0, true},
		{[]string{"+7"}, 0, false},
		{[]string{"-1"}, 0, false},
		{[]string{"1.5"}, 0, false},
		{[]string{"7", "7"}, 0, false},
		{[]string{"9007199254740991"}, 9007199254740991, true},
		{[]string{"9007199254740992"}, 0, false},
		{[]string{"99999999999999999999999"}, 0, false},
	}
	for _, c := range cases {
		h := http.Header{}
		for _, v := range c.values {
			h.Add("Scrape.do-Request-Cost", v)
		}
		cost, ok := requestCost(h)
		if cost != c.cost || ok != c.ok {
			t.Errorf("%q: requestCost = %d,%v", c.values, cost, ok)
		}
		if rc, rok := (&Result{Header: h}).Cost(); rc != cost || rok != ok {
			t.Errorf("%q: Result.Cost = %d,%v, requestCost %d,%v", c.values, rc, rok, cost, ok)
		}
		if targetAnswered(h) != (ok && cost > 0) {
			t.Errorf("%q: targetAnswered disagrees with requestCost", c.values)
		}
	}
}
