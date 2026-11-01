// SPDX-License-Identifier: Apache-2.0

package sdo

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// Each dependency rule names the fields involved, so the fix is obvious.
func TestDependencyErrorsNameTheRule(t *testing.T) {
	for _, c := range []struct {
		cfg  ScrapeConfig
		want string
	}{
		{ScrapeConfig{SessionID: "abc"}, "SessionID must be an integer from 0 to 1000000"},
		{ScrapeConfig{RegionalGeoCode: "europe"}, "RegionalGeoCode requires SuperProxy"},
		{ScrapeConfig{SuperProxy: true, GeoCode: "us", RegionalGeoCode: "europe"}, "RegionalGeoCode and GeoCode cannot be combined"},
		{ScrapeConfig{GeoCode: "us", ZipCode: "90210"}, "ZipCode requires SuperProxy and a GeoCode"},
		{ScrapeConfig{CustomHeaders: true, ForwardHeaders: true}, "cannot be combined"},
		{ScrapeConfig{Render: true, RetryTimeout: 5000}, "RetryTimeout cannot be combined with rendering"},
		{ScrapeConfig{Render: true, Screenshot: true}, "Screenshot requires ReturnJSON"},
		{ScrapeConfig{Render: true, ShowFrames: true}, "ShowFrames requires ReturnJSON"},
		{ScrapeConfig{Render: true, ReturnJSON: true, Screenshot: true, FullScreenshot: true}, "cannot be combined"},
		{ScrapeConfig{Render: true, ReturnJSON: true, ParticularScreenshot: "h1",
			PlayWithBrowser: []map[string]any{{"Action": "Click"}}}, "ParticularScreenshot cannot be combined with PlayWithBrowser"},
		{ScrapeConfig{Width: 1920}, "Width only applies to rendered requests"},
	} {
		c.cfg.URL = "https://e.com"
		_, err := c.cfg.toParams()
		var ce *ConfigError
		if !errors.As(err, &ce) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: err = %v, want a ConfigError containing %q", c.cfg, err, c.want)
		}
	}
}

// The gateway wraps a non-rendered response in its JSON envelope, so
// ReturnJSON alone is a valid request; and the documented screenshot request
// (render + returnJSON) goes through.
func TestReturnJSONAndScreenshotAccepted(t *testing.T) {
	for _, cfg := range []ScrapeConfig{
		{URL: "https://e.com", ReturnJSON: true},
		{URL: "https://e.com", Render: true, ReturnJSON: true, ParticularScreenshot: "#price"},
		{URL: "https://e.com", SuperProxy: true, GeoCode: "us", ZipCode: "90210", SessionID: "0"},
	} {
		if _, err := cfg.toParams(); err != nil {
			t.Errorf("%+v: %v", cfg, err)
		}
	}
}

// A target URL in an error message never shows its userinfo or a token.
func TestTargetURLErrorMasksCredentials(t *testing.T) {
	_, err := (&ScrapeConfig{URL: "https://user:hunter2@e.com:99999/?token=abcdef123"}).toParams()
	if err == nil || strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "user:") ||
		strings.Contains(err.Error(), "abcdef123") || !strings.Contains(err.Error(), "https://***@e.com") {
		t.Errorf("err = %v", err)
	}
}

// 510 is the client's own cancellation: one attempt, labelled as such.
func TestStatus510IsNotRetried(t *testing.T) {
	var calls atomic.Int32
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(510)
	})
	_, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.IsRetryable || calls.Load() != 1 ||
		apiErr.Message != "The request was canceled by the HTTP client (HTTP 510)." {
		t.Errorf("err = %v, calls = %d", err, calls.Load())
	}
}

func TestTargetRedirectedLocation(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Scrape.do-Target-Redirected-Location", "https://e.com/next")
		w.WriteHeader(http.StatusFound)
	})
	res, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com", DisableRedirection: true})
	if err != nil || res.TargetRedirectedLocation() != "https://e.com/next" || res.StatusCode != http.StatusFound {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if (&Result{Header: http.Header{}}).TargetRedirectedLocation() != "" {
		t.Error("absent header should be empty")
	}
}

// PluginResult keeps the status and headers Plugin drops; both read the same
// response the same way.
func TestPluginResultKeepsStatusAndCost(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Scrape.do-Request-Cost", "10")
		w.Header().Set("Scrape.do-Remaining-Credits", "990")
		io.WriteString(w, `{"organic":[1]}`)
	})
	res, err := c.PluginResult(context.Background(), "google/search", map[string]any{"q": "x", "gl": "us"})
	if err != nil {
		t.Fatal(err)
	}
	cost, ok := res.Cost()
	credits, _ := res.RemainingCredits()
	var body map[string]any
	if !ok || cost != 10 || credits != 990 || res.StatusCode != 200 || res.URL != "" || res.JSON(&body) != nil {
		t.Errorf("res = %+v cost=%d,%v body=%v", res, cost, ok, body)
	}
	out, err := c.Plugin(context.Background(), "google/search", map[string]any{"q": "x"})
	if m, _ := out.(map[string]any); err != nil || m["organic"] == nil {
		t.Errorf("Plugin = %v, %v", out, err)
	}
}

func TestUsage(t *testing.T) {
	var path, query string
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		path, query = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		io.WriteString(w, `{"IsActive":true,"ConcurrentRequest":40,"MaxMonthlyRequest":3500000,`+
			`"RemainingConcurrentRequest":15,"RemainingMonthlyRequest":2565023}`)
	})
	u, err := c.Usage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Usage{IsActive: true, ConcurrentRequest: 40, MaxMonthlyRequest: 3500000,
		RemainingConcurrentRequest: 15, RemainingMonthlyRequest: 2565023}
	if *u != want || path != "/info" || query != "token=t" {
		t.Errorf("usage = %+v path=%q query=%q", u, path, query)
	}
}

// /info's 429 is a per-minute rate limit and its 502 an unknown token: neither
// is retried, and the error carries the gateway's message.
func TestUsageStatusesAreNotRetried(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		kind   ErrorKind
	}{
		{429, `{"message":"You have exceeded your request limit. You can send 10 requests per minute."}`, KindConcurrency},
		{502, `{"message":"authentication refused"}`, KindGateway},
	} {
		var calls atomic.Int32
		client, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(c.status)
			io.WriteString(w, c.body)
		}, WithRetries(3))
		_, err := client.Usage(context.Background())
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Kind != c.kind || calls.Load() != 1 || !strings.Contains(c.body, apiErr.Message) {
			t.Errorf("%d: err = %v, calls = %d", c.status, err, calls.Load())
		}
	}
}

func TestUsageRejectsUnexpectedBodies(t *testing.T) {
	for _, body := range []string{
		`[]`, `null`, `{"IsActive":true}`, `not json`,
		`{"IsActive":"yes","ConcurrentRequest":1,"MaxMonthlyRequest":1,"RemainingConcurrentRequest":1,"RemainingMonthlyRequest":1}`,
		`{"IsActive":true,"ConcurrentRequest":1.5,"MaxMonthlyRequest":1,"RemainingConcurrentRequest":1,"RemainingMonthlyRequest":1}`,
		`{"IsActive":null,"ConcurrentRequest":1,"MaxMonthlyRequest":1,"RemainingConcurrentRequest":1,"RemainingMonthlyRequest":1}`,
	} {
		c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
		_, err := c.Usage(context.Background())
		var te *TransportError
		if !errors.As(err, &te) {
			t.Errorf("%s: err = %v, want a TransportError", body, err)
		}
	}
}

// The usage call shares the transport: a redirect is not followed (the token
// stays home) and the body is size-capped.
func TestUsageSharesTransportSafeguards(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example/collect", http.StatusFound)
	})
	// Not followed: the 302's own body is read, and it is not usage JSON.
	var te *TransportError
	if _, err := c.Usage(context.Background()); !errors.As(err, &te) {
		t.Errorf("redirect: %v", err)
	}
	c, _ = newClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Repeat(" ", 2048)+"{}")
	}, WithMaxResponseBytes(1024))
	var tooLarge *ResponseTooLargeError
	if _, err := c.Usage(context.Background()); !errors.As(err, &tooLarge) {
		t.Errorf("size cap: %v", err)
	}
}

// The gateway renders GET requests only; any other method with render is
// refused before a request is sent.
func TestRenderRequiresGET(t *testing.T) {
	var calls atomic.Int32
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	ctx := context.Background()
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
		for _, cfg := range []ScrapeConfig{{Render: true}, {RenderMode: "true"}} {
			cfg.URL = "https://e.com"
			_, err := c.Do(ctx, &cfg, &Request{Method: m})
			var ce *ConfigError
			if !errors.As(err, &ce) || !strings.Contains(err.Error(), "render works with GET requests only; got "+m) {
				t.Errorf("%s %+v: err = %v", m, cfg, err)
			}
		}
	}
	if _, err := c.Do(ctx, &ScrapeConfig{URL: "https://e.com", RenderMode: "false"}, &Request{Method: "POST", Data: []byte("a=1")}); err != nil {
		t.Errorf("POST with RenderMode false: %v", err)
	}
	if _, err := c.Do(ctx, &ScrapeConfig{URL: "https://e.com", Render: true}, nil); err != nil {
		t.Errorf("rendered GET: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("gateway calls = %d, want 2", got)
	}
}
