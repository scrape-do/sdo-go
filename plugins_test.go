// SPDX-License-Identifier: Apache-2.0

package sdo

import (
	"context"
	"errors"
	"math"
	"net/http"
	"testing"
	"time"
)

// Plugin params keep an explicit false — unlike ScrapeConfig, a plugin
// param map is forwarded as given — and non-string scalars use their %v form.
func TestPluginParamSerialization(t *testing.T) {
	var got map[string][]string
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}, WithRetries(0))
	_, err := c.Plugin(context.Background(), "google/shopping", map[string]any{
		"page": 2, "safe": false, "price": 9.5, "tags": []any{"a", "b"}, "skip": nil,
	})
	if err != nil {
		t.Fatalf("plugin: %v", err)
	}
	want := map[string][]string{"page": {"2"}, "safe": {"false"}, "price": {"9.5"}, "tags": {"a", "b"}}
	for k, v := range want {
		if len(got[k]) != len(v) || got[k][0] != v[0] {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if _, present := got["skip"]; present {
		t.Error("a nil param was sent")
	}
}

func TestPluginSurfacesAPIErrorsAndBadJSON(t *testing.T) {
	failing, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}, WithRetries(0))
	var apiErr *APIError
	if _, err := failing.Plugin(context.Background(), "google/shopping", nil); !errors.As(err, &apiErr) || !apiErr.IsAuth() {
		t.Errorf("want an auth APIError, got %v", err)
	}

	lying, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("<html>not json</html>"))
	}, WithRetries(0))
	if _, err := lying.Plugin(context.Background(), "google/shopping", nil); err == nil {
		t.Error("a JSON content-type with a non-JSON body decoded without error")
	}
}

// A plugin call follows the scrape status rules: a target 404/410, and a 400
// the gateway billed, are results; the gateway's own 400 is an unbilled
// validation error whose message is the plugin envelope's, read in any case.
func TestPluginStatusRules(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		header  map[string]string
		body    string
		wantErr string // "" = a result
	}{
		{"target 404", 404, nil, "gone", ""},
		{"target 410", 410, nil, "gone", ""},
		{"billed target 400", 400, map[string]string{"Scrape.do-Request-Cost": "25"}, "bad", ""},
		{"plugin parameter error", 400, map[string]string{"Scrape.do-Plugin": "google/search"},
			`{"error":"invalid_zipcode","message":"ZIP code format doesn't match country requirements"}`,
			"ZIP code format doesn't match country requirements"},
		{"plugin error code only", 400, nil, `{"error":"timeout is invalid"}`, "timeout is invalid"},
		{"unbilled 400 with a zero cost", 400, map[string]string{"Scrape.do-Request-Cost": "0"}, `{"Message":"nope"}`, "nope"},
	}
	for _, tc := range cases {
		c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			for k, v := range tc.header {
				w.Header().Set(k, v)
			}
			w.WriteHeader(tc.status)
			w.Write([]byte(tc.body))
		}, WithRetries(0))
		out, err := c.Plugin(context.Background(), "google/search", map[string]any{"q": "x"})
		if tc.wantErr == "" {
			if b, ok := out.([]byte); err != nil || !ok || string(b) != tc.body {
				t.Errorf("%s: want the body as a result, got %v, %v", tc.name, out, err)
			}
			continue
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) || !apiErr.IsValidation() || apiErr.Billed || apiErr.Message != tc.wantErr {
			t.Errorf("%s: want an unbilled validation error %q, got %v", tc.name, tc.wantErr, err)
		}
	}
}

// A positive integer "timeout" plugin param raises the attempt deadline to the
// timeout plus the gateway margin, as ScrapeConfig.Timeout does.
func TestPluginTimeoutParamGetsTheMargin(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.Write([]byte("ok"))
	}, WithRetries(0), WithTimeout(50*time.Millisecond))
	if _, err := c.Plugin(context.Background(), "walmart/search", map[string]any{"q": "x", "timeout": 100}); err != nil {
		t.Fatalf("timeout param did not extend the deadline: %v", err)
	}
	var te *TransportError
	if _, err := c.Plugin(context.Background(), "walmart/search", map[string]any{"q": "x"}); !errors.As(err, &te) || !te.Timeout {
		t.Fatalf("without a timeout param the client deadline applies, got %v", err)
	}
}

func TestPluginTimeoutMs(t *testing.T) {
	cases := []struct {
		params []param
		want   int
	}{
		{nil, 0},
		{[]param{{"q", "x"}}, 0},
		{[]param{{"timeout", "30000"}}, 30000},
		{[]param{{"timeout", "0"}}, 0},
		{[]param{{"timeout", "1.5"}}, 0},
		{[]param{{"timeout", "-5"}}, 0},
		{[]param{{"timeout", "abc"}}, 0},
		{[]param{{"timeout", "99999999999999999999999"}}, 0},
		{[]param{{"timeout", "1000"}, {"timeout", "2000"}}, 0},
		{[]param{{"Timeout", "1000"}}, 0},
	}
	for _, c := range cases {
		if got := pluginTimeoutMs(c.params); got != c.want {
			t.Errorf("pluginTimeoutMs(%v) = %d, want %d", c.params, got, c.want)
		}
	}
	if got := effectiveTimeout(time.Second, math.MaxInt); got != time.Duration(math.MaxInt64) {
		t.Errorf("a huge plugin timeout must saturate, got %v", got)
	}
}
