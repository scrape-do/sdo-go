// SPDX-License-Identifier: Apache-2.0

package sdo

// The cross-language conformance harness. conformance.json is generated from
// the spec and shared byte-for-byte by every Scrape.do SDK; each section below
// is consumed here exactly as the corpus defines it. A case whose "skip" lists
// "go" cannot be expressed in Go's type system and is skipped — nothing else is.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

type corpusCase struct {
	Name    string          `json:"name"`
	Go      map[string]any  `json:"go"`
	Py      json.RawMessage `json:"py"`
	JS      json.RawMessage `json:"js"`
	Expect  [][]string      `json:"expect"`
	Ordered bool            `json:"ordered"`
	Error   string          `json:"error"`
	Skip    []string        `json:"skip"`
}

// corpusFile models every key of conformance.json. It is decoded with
// DisallowUnknownFields, so a section or key this harness does not know fails
// the run instead of being silently ignored.
type corpusFile struct {
	Version  string `json:"version"`
	URL      string `json:"url"`
	Contract struct {
		Serialization struct {
			OmitFalseBooleans   bool     `json:"omit_false_booleans"`
			OmitZeroValues      bool     `json:"omit_zero_values"`
			JSONObjectKeys      string   `json:"json_object_keys"`
			JSONKeyOrder        string   `json:"json_key_order"`
			JSONHTMLEscape      bool     `json:"json_html_escape"`
			JSONLineSeparators  string   `json:"json_line_separators"`
			Numbers             string   `json:"numbers"`
			QueryOrder          string   `json:"query_order"`
			ExtraParamsOrder    string   `json:"extra_params_order"`
			ExtraParamsReserved []string `json:"extra_params_reserved"`
			Enums               string   `json:"enums"`
		} `json:"serialization"`
		Decoding struct {
			Algorithm                 string   `json:"algorithm"`
			BOMOverridesLabel         bool     `json:"bom_overrides_label"`
			StripBOM                  bool     `json:"strip_bom"`
			Labels                    string   `json:"labels"`
			MetaSniffBytes            int      `json:"meta_sniff_bytes"`
			UnknownOrReplacementLabel string   `json:"unknown_or_replacement_label"`
			MetaSniffTypes            []string `json:"meta_sniff_types"`
			InvalidBytes              string   `json:"invalid_bytes"`
			JSONStripsBOM             bool     `json:"json_strips_bom"`
			MaxDecompressedBytes      int64    `json:"max_decompressed_bytes"`
			MaxResponseBytes          int64    `json:"max_response_bytes"`
		} `json:"decoding"`
		Transport struct {
			ClientTimeout               string   `json:"client_timeout"`
			DefaultTimeoutSeconds       float64  `json:"default_timeout_seconds"`
			EffectiveTimeout            string   `json:"effective_timeout"`
			GatewayTimeoutMarginSeconds float64  `json:"gateway_timeout_margin_seconds"`
			FollowRedirects             bool     `json:"follow_redirects"`
			HTTPSOnly                   bool     `json:"https_only"`
			InsecureHTTPHosts           []string `json:"insecure_http_hosts"`
			BaseURLParts                []string `json:"base_url_parts"`
			EmptyTokenIsUnset           bool     `json:"empty_token_is_unset"`
			TokenTrim                   string   `json:"token_trim"`
			HeaderNames                 string   `json:"header_names"`
			HeaderValues                string   `json:"header_values"`
			ForbiddenHeaders            []string `json:"forbidden_headers"`
			ContentCodings              []string `json:"content_codings"`
			Retry                       struct {
				DefaultRetries             int      `json:"default_retries"`
				RetryableStatuses          []int    `json:"retryable_statuses"`
				NonIdempotentMethods       []string `json:"non_idempotent_methods"`
				NonIdempotentRetryStatuses []int    `json:"non_idempotent_retry_statuses"`
				RetryAfter                 string   `json:"retry_after"`
				MaxBackoffSeconds          float64  `json:"max_backoff_seconds"`
				NegativeRetries            string   `json:"negative_retries"`
				RetryAfterSend             bool     `json:"retry_after_send"`
			} `json:"retry"`
		} `json:"transport"`
		Environment map[string]string `json:"environment"`
		UserAgent   struct {
			SDK     string `json:"sdk"`
			CLI     string `json:"cli"`
			Pattern string `json:"pattern"`
		} `json:"user_agent"`
		PluginRoutePattern string `json:"plugin_route_pattern"`
		Redaction          struct {
			Mask             string   `json:"mask"`
			LiteralToken     bool     `json:"literal_token"`
			LiteralMinLength int      `json:"literal_min_length"`
			Shapes           []string `json:"shapes"`
		} `json:"redaction"`
		Errors struct {
			DefaultMessages    map[string]string `json:"default_messages"`
			ErrorBodyReadBytes int               `json:"error_body_read_bytes"`
			AsyncBilled        bool              `json:"async_billed"`
			BilledStatuses     []int             `json:"billed_statuses"`
			TargetOrigin       string            `json:"target_origin"`
			Billed             string            `json:"billed"`
			RequestCost        struct {
				Values int    `json:"values"`
				Trim   string `json:"trim"`
				Digits string `json:"digits"`
				Max    int64  `json:"max"`
			} `json:"request_cost"`
			EnvelopeKeys []string `json:"envelope_keys"`
		} `json:"errors"`
		JSONBodies struct {
			InvalidUTF8      string `json:"invalid_utf8"`
			NonFiniteNumbers string `json:"non_finite_numbers"`
		} `json:"json_bodies"`
	} `json:"contract"`
	Serialization []corpusCase `json:"serialization"`
	Edge          []corpusCase `json:"edge"`
	Wire          struct {
		Scrape []struct {
			Name  string          `json:"name"`
			Go    map[string]any  `json:"go"`
			Py    json.RawMessage `json:"py"`
			JS    json.RawMessage `json:"js"`
			Token string          `json:"token"`
			Path  string          `json:"path"`
			Query string          `json:"query"`
			Skip  []string        `json:"skip"`
		} `json:"scrape"`
		Plugin []struct {
			Name   string         `json:"name"`
			Route  string         `json:"route"`
			Params map[string]any `json:"params"`
			Token  string         `json:"token"`
			Path   string         `json:"path"`
			Query  string         `json:"query"`
			Skip   []string       `json:"skip"`
		} `json:"plugin"`
		PluginErrors []struct {
			Name   string         `json:"name"`
			Route  string         `json:"route"`
			Params map[string]any `json:"params"`
			Skip   []string       `json:"skip"`
		} `json:"plugin_errors"`
		Prefixed []struct {
			Name         string `json:"name"`
			BaseURL      string `json:"base_url"`
			AsyncBaseURL string `json:"async_base_url"`
			ScrapePath   string `json:"scrape_path"`
			PluginPath   string `json:"plugin_path"`
			JobsPath     string `json:"jobs_path"`
		} `json:"prefixed"`
		Routes []struct {
			Route string `json:"route"`
			Valid bool   `json:"valid"`
		} `json:"routes"`
	} `json:"wire"`
	Requests []struct {
		Name              string              `json:"name"`
		Method            *string             `json:"method"`
		JSON              json.RawMessage     `json:"json"`
		Data              *string             `json:"data"`
		Headers           map[string]string   `json:"headers"`
		ExpectMethod      string              `json:"expect_method"`
		Body              *string             `json:"body"`
		ContentType       *string             `json:"content_type"`
		ExpectHeaders     map[string][]string `json:"expect_headers"`
		ExpectHeaderNames []string            `json:"expect_header_names"`
		IgnoreHeaderNames []string            `json:"ignore_header_names"`
		Error             string              `json:"error"`
		Skip              []string            `json:"skip"`
	} `json:"requests"`
	Retry struct {
		Attempts []struct {
			Name        string            `json:"name"`
			Method      string            `json:"method"`
			Status      int               `json:"status"`
			Transparent bool              `json:"transparent"`
			Headers     map[string]string `json:"headers"`
			Retries     int               `json:"retries"`
			Attempts    int               `json:"attempts"`
			Skip        []string          `json:"skip"`
		} `json:"attempts"`
		RetryAfter []struct {
			Value   string `json:"value"`
			Seconds *int   `json:"seconds"`
		} `json:"retry_after"`
	} `json:"retry"`
	Errors []struct {
		Name              string            `json:"name"`
		Status            int               `json:"status"`
		Transparent       bool              `json:"transparent"`
		Headers           map[string]string `json:"headers"`
		Body              string            `json:"body"`
		Kind              *string           `json:"kind"`
		Billed            bool              `json:"billed"`
		Retryable         bool              `json:"retryable"`
		Message           *string           `json:"message"`
		RetainedBodyChars *int              `json:"retained_body_chars"`
		Cost              json.RawMessage   `json:"cost"` // absent: not checked; null: no readable cost
		Skip              []string          `json:"skip"`
	} `json:"errors"`
	Decoding []struct {
		Name        string   `json:"name"`
		BodyHex     string   `json:"body_hex"`
		ContentType string   `json:"content_type"`
		Text        string   `json:"text"`
		Skip        []string `json:"skip"`
	} `json:"decoding"`
	Decompression []struct {
		Name            string   `json:"name"`
		ContentEncoding string   `json:"content_encoding"`
		BodyHex         string   `json:"body_hex"`
		Text            *string  `json:"text"`
		Error           string   `json:"error"`
		Attempts        int      `json:"attempts"`
		Skip            []string `json:"skip"`
	} `json:"decompression"`
	JSONBodies []struct {
		Name    string          `json:"name"`
		BodyHex string          `json:"body_hex"`
		Value   json.RawMessage `json:"value"`
		Error   string          `json:"error"`
		Skip    []string        `json:"skip"`
	} `json:"json_bodies"`
	Redaction []struct {
		Name   string   `json:"name"`
		Token  string   `json:"token"`
		Input  string   `json:"input"`
		Output string   `json:"output"`
		Skip   []string `json:"skip"`
	} `json:"redaction"`
	Jobs struct {
		AuthHeader  string `json:"auth_header"`
		IDPattern   string `json:"id_pattern"`
		TaskResults []struct {
			Name              string          `json:"name"`
			JSON              json.RawMessage `json:"json"`
			Expect            map[string]any  `json:"expect"`
			Absent            []string        `json:"absent"`
			ContentBytesError string          `json:"content_bytes_error"`
		} `json:"task_results"`
		IDs []struct {
			ID    string `json:"id"`
			Valid bool   `json:"valid"`
		} `json:"ids"`
		Errors []struct {
			Name    string  `json:"name"`
			Status  int     `json:"status"`
			Body    string  `json:"body"`
			Kind    string  `json:"kind"`
			Message *string `json:"message"`
			Billed  *bool   `json:"billed"`
		} `json:"errors"`
		Create []struct {
			Name    string         `json:"name"`
			Targets []string       `json:"targets"`
			Options map[string]any `json:"options"`
			Body    string         `json:"body"`
			Error   string         `json:"error"`
		} `json:"create"`
		Account []struct {
			Name   string          `json:"name"`
			JSON   json.RawMessage `json:"json"`
			Expect map[string]int  `json:"expect"`
		} `json:"account"`
		CancelBodies []string `json:"cancel_bodies"`
	} `json:"jobs"`
	Client struct {
		BaseURLs []struct {
			URL string `json:"url"`
			OK  bool   `json:"ok"`
		} `json:"base_urls"`
		InsecureOptIn struct {
			URL string `json:"url"`
			OK  bool   `json:"ok"`
		} `json:"insecure_opt_in"`
		Timeouts []struct {
			Value   string   `json:"value"`
			Seconds *float64 `json:"seconds"`
		} `json:"timeouts"`
		EffectiveTimeouts []struct {
			ClientSeconds   float64 `json:"client_seconds"`
			ConfigTimeoutMs int     `json:"config_timeout_ms"`
			Seconds         float64 `json:"seconds"`
		} `json:"effective_timeouts"`
		Tokens []struct {
			Value string  `json:"value"`
			Token *string `json:"token"`
		} `json:"tokens"`
		EmptyTokenFallsBackToEnv bool `json:"empty_token_falls_back_to_env"`
		TargetURLs               []struct {
			URL string `json:"url"`
			OK  bool   `json:"ok"`
		} `json:"target_urls"`
	} `json:"client"`
}

// loadCorpus reads conformance.json, checks it against its sha256 sidecar
// (a hand edit fails here) and decodes it strictly.
func loadCorpus(t *testing.T) corpusFile {
	t.Helper()
	data, err := os.ReadFile("conformance.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	sidecar, err := os.ReadFile("conformance.json.sha256")
	if err != nil {
		t.Fatalf("read corpus sidecar: %v", err)
	}
	fields := strings.Fields(string(sidecar))
	sum := sha256.Sum256(data)
	if len(fields) == 0 || !strings.EqualFold(fields[0], hex.EncodeToString(sum[:])) {
		t.Fatalf("conformance.json does not match conformance.json.sha256: regenerate it, never edit it")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var c corpusFile
	if err := dec.Decode(&c); err != nil {
		t.Fatalf("parse corpus (a section or key this harness does not consume?): %v", err)
	}
	return c
}

// skipGo skips a case the corpus marks as inexpressible in Go.
func skipGo(t *testing.T, skip []string) {
	t.Helper()
	if slices.Contains(skip, "go") {
		t.Skip("corpus: input shape cannot exist in Go")
	}
}

// cleanEnv isolates a test from SDO_* values an earlier step of the same
// test set (TestMain already removed the caller's).
func cleanEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"SDO_TOKEN", "SDO_BASE_URL", "SDO_ASYNC_BASE_URL", "SDO_TIMEOUT"} {
		t.Setenv(k, "")
	}
}

// setField applies a JSON value to an exported ScrapeConfig field via reflection.
//
// One shape adapter: the spec's single `render` param is two fields in Go
// (Render bool + RenderMode string), because Go has no bool|string union. A
// string value therefore lands on RenderMode.
func setField(t *testing.T, cfg *ScrapeConfig, name string, value any) {
	t.Helper()
	if name == "Render" {
		if s, ok := value.(string); ok {
			cfg.RenderMode = s
			return
		}
	}
	f := reflect.ValueOf(cfg).Elem().FieldByName(name)
	if !f.IsValid() || !f.CanSet() {
		t.Fatalf("unknown/unsettable field %q", name)
	}
	switch f.Kind() {
	case reflect.Bool:
		f.SetBool(value.(bool))
	case reflect.Ptr: // tri-state *bool (BlockResources): JSON bool -> &v, absent -> nil
		p := reflect.New(f.Type().Elem())
		p.Elem().SetBool(value.(bool))
		f.Set(p)
	case reflect.String:
		f.SetString(value.(string))
	case reflect.Int:
		f.SetInt(int64(value.(float64)))
	case reflect.Slice:
		arr := value.([]any)
		if f.Type().Elem().Kind() == reflect.String { // JSExecute []string
			out := make([]string, len(arr))
			for i, v := range arr {
				out[i] = v.(string)
			}
			f.Set(reflect.ValueOf(out))
		} else { // PlayWithBrowser []map[string]any
			out := make([]map[string]any, len(arr))
			for i, v := range arr {
				out[i] = v.(map[string]any)
			}
			f.Set(reflect.ValueOf(out))
		}
	case reflect.Map:
		src := value.(map[string]any)
		if f.Type().Elem().Kind() == reflect.String { // ExtraParams map[string]string
			out := make(map[string]string, len(src))
			for k, v := range src {
				out[k] = v.(string)
			}
			f.Set(reflect.ValueOf(out))
			return
		}
		f.Set(reflect.ValueOf(src)) // Parameters map[string]any
	default:
		t.Fatalf("unsupported kind for %q: %v", name, f.Kind())
	}
}

func configFrom(t *testing.T, url string, projection map[string]any) *ScrapeConfig {
	t.Helper()
	cfg := &ScrapeConfig{URL: url}
	for name, value := range projection {
		setField(t, cfg, name, value)
	}
	return cfg
}

func flat(pairs [][]string) []string {
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p[0] + "=" + p[1]
	}
	return out
}

func norm(pairs [][]string) []string {
	out := flat(pairs)
	sort.Strings(out)
	return out
}

// runCases serializes each case and asserts the expected outcome. `ordered`
// cases compare positionally, which pins query-string order too.
func runCases(t *testing.T, corpus corpusFile, cases []corpusCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			skipGo(t, c.Skip)
			pairs, err := configFrom(t, corpus.URL, c.Go).toParams()
			if c.Error == "config" {
				var cfgErr *ConfigError
				if !errors.As(err, &cfgErr) {
					t.Fatalf("expected a ConfigError, got err=%v params=%v", err, pairs)
				}
				return
			}
			if err != nil {
				t.Fatalf("toParams: %v", err)
			}
			got := make([][]string, len(pairs))
			for i, p := range pairs {
				got[i] = []string{p.Key, p.Value}
			}
			gotN, wantN := norm(got), norm(c.Expect)
			if c.Ordered {
				gotN, wantN = flat(got), flat(c.Expect)
			}
			if strings.Join(gotN, "&") != strings.Join(wantN, "&") {
				t.Errorf("\n got: %q\nwant: %q", gotN, wantN)
			}
		})
	}
}

func TestSerializationConformance(t *testing.T) {
	corpus := loadCorpus(t)
	runCases(t, corpus, corpus.Serialization)
}

// TestEdgeConformance covers the invariants a per-parameter sweep cannot reach:
// unset values, the render gate, enums, ranges, canonical JSON, ExtraParams.
func TestEdgeConformance(t *testing.T) {
	corpus := loadCorpus(t)
	runCases(t, corpus, corpus.Edge)
}

// captured is one request as the mock gateway saw it.
type captured struct {
	method, path, rawQuery, contentType string
	header                              http.Header
	body                                []byte
}

// recorder is a loopback gateway that records every request and answers with
// respond (200 and an empty body when nil).
func recorder(t *testing.T, respond http.HandlerFunc) (*httptest.Server, *[]captured) {
	t.Helper()
	var reqs []captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		reqs = append(reqs, captured{
			method: r.Method, path: r.URL.Path, rawQuery: r.URL.RawQuery,
			contentType: r.Header.Get("Content-Type"), header: r.Header.Clone(), body: body,
		})
		if respond != nil {
			respond(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

// TestWireConformance pins the exact bytes on the wire: the query string as
// sent (not re-parsed), percent-encoding included, and the path.
func TestWireConformance(t *testing.T) {
	cleanEnv(t)
	corpus := loadCorpus(t)
	for _, c := range corpus.Wire.Scrape {
		t.Run("scrape/"+c.Name, func(t *testing.T) {
			skipGo(t, c.Skip)
			srv, reqs := recorder(t, nil)
			client, err := New(c.Token, WithBaseURL(srv.URL), WithRetries(0))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if _, err := client.Scrape(context.Background(), configFrom(t, corpus.URL, c.Go)); err != nil {
				t.Fatalf("Scrape: %v", err)
			}
			if len(*reqs) != 1 {
				t.Fatalf("requests = %d, want 1", len(*reqs))
			}
			got := (*reqs)[0]
			if got.path != c.Path || got.rawQuery != c.Query {
				t.Errorf("\n got: %s?%s\nwant: %s?%s", got.path, got.rawQuery, c.Path, c.Query)
			}
		})
	}
	for _, c := range corpus.Wire.Plugin {
		t.Run("plugin/"+c.Name, func(t *testing.T) {
			skipGo(t, c.Skip)
			srv, reqs := recorder(t, nil)
			client, err := New(c.Token, WithBaseURL(srv.URL), WithRetries(0))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if _, err := client.Plugin(context.Background(), c.Route, c.Params); err != nil {
				t.Fatalf("Plugin: %v", err)
			}
			got := (*reqs)[0]
			if got.path != c.Path || got.rawQuery != c.Query {
				t.Errorf("\n got: %s?%s\nwant: %s?%s", got.path, got.rawQuery, c.Path, c.Query)
			}
		})
	}
	for _, c := range corpus.Wire.PluginErrors {
		t.Run("plugin_error/"+c.Name, func(t *testing.T) {
			skipGo(t, c.Skip)
			srv, reqs := recorder(t, nil)
			client, _ := New("TOK", WithBaseURL(srv.URL), WithRetries(0))
			_, err := client.Plugin(context.Background(), c.Route, c.Params)
			var ce *ConfigError
			if !errors.As(err, &ce) || len(*reqs) != 0 {
				t.Fatalf("want a ConfigError before any request, got err=%v requests=%d", err, len(*reqs))
			}
		})
	}
	for _, c := range corpus.Wire.Prefixed {
		t.Run("prefixed/"+c.Name, func(t *testing.T) {
			// The base URLs are not reachable hosts: a RoundTripper answers
			// in-process and records what would have been sent.
			var paths []string
			rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				paths = append(paths, r.URL.Host+r.URL.Path)
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
			})
			client, err := New("TOK", WithBaseURL(c.BaseURL), WithAsyncBaseURL(c.AsyncBaseURL),
				WithHTTPClient(&http.Client{Transport: rt}), WithRetries(0))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			ctx := context.Background()
			_, err1 := client.Scrape(ctx, &ScrapeConfig{URL: corpus.URL})
			_, err2 := client.Plugin(ctx, "google/search", nil)
			_, err3 := client.Jobs().Status(ctx, "j-1")
			if err1 != nil || err2 != nil || err3 != nil {
				t.Fatalf("calls failed: %v / %v / %v", err1, err2, err3)
			}
			host := func(u string) string { h, _ := url.Parse(u); return h.Host }
			want := []string{host(c.BaseURL) + c.ScrapePath, host(c.BaseURL) + c.PluginPath, host(c.AsyncBaseURL) + c.JobsPath}
			if !reflect.DeepEqual(paths, want) {
				t.Errorf("paths = %q, want %q", paths, want)
			}
		})
	}
	for _, c := range corpus.Wire.Routes {
		t.Run("route/"+c.Route, func(t *testing.T) {
			srv, reqs := recorder(t, nil)
			client, _ := New("TOK", WithBaseURL(srv.URL), WithRetries(0))
			_, err := client.Plugin(context.Background(), c.Route, nil)
			if c.Valid {
				if err != nil || len(*reqs) != 1 || (*reqs)[0].path != "/plugin/"+c.Route {
					t.Fatalf("valid route: err=%v requests=%v", err, *reqs)
				}
				return
			}
			var ce *ConfigError
			if !errors.As(err, &ce) || len(*reqs) != 0 {
				t.Fatalf("invalid route: want a ConfigError before any request, got err=%v requests=%d", err, len(*reqs))
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestRequestsConformance pins the HTTP method, body bytes and Content-Type of
// a scrape made with Do.
func TestRequestsConformance(t *testing.T) {
	cleanEnv(t)
	corpus := loadCorpus(t)
	for _, c := range corpus.Requests {
		t.Run(c.Name, func(t *testing.T) {
			skipGo(t, c.Skip)
			req := &Request{Headers: c.Headers}
			if c.Method != nil {
				req.Method = *c.Method
			}
			if len(c.JSON) > 0 {
				if err := json.Unmarshal(c.JSON, &req.JSON); err != nil {
					t.Fatalf("corpus json: %v", err)
				}
			}
			if c.Data != nil {
				req.Data = []byte(*c.Data)
			}
			srv, reqs := recorder(t, nil)
			client, _ := New("TOK", WithBaseURL(srv.URL), WithRetries(0))
			_, err := client.Do(context.Background(), &ScrapeConfig{URL: corpus.URL}, req)
			if c.Error == "config" {
				var ce *ConfigError
				if !errors.As(err, &ce) || len(*reqs) != 0 {
					t.Fatalf("want a ConfigError before any request, got err=%v requests=%d", err, len(*reqs))
				}
				return
			}
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			got := (*reqs)[0]
			if got.method != c.ExpectMethod {
				t.Errorf("method = %q, want %q", got.method, c.ExpectMethod)
			}
			wantBody := ""
			if c.Body != nil {
				wantBody = *c.Body
			}
			if string(got.body) != wantBody {
				t.Errorf("body = %q, want %q", got.body, wantBody)
			}
			wantCT := ""
			if c.ContentType != nil {
				wantCT = *c.ContentType
			}
			if got.contentType != wantCT {
				t.Errorf("Content-Type = %q, want %q", got.contentType, wantCT)
			}
			for name, want := range c.ExpectHeaders {
				if values := got.header.Values(name); !reflect.DeepEqual(values, want) {
					t.Errorf("header %s = %q, want exactly %q", name, values, want)
				}
			}
			if c.ExpectHeaderNames != nil {
				// Headers the HTTP stack itself manages are excluded, as the
				// corpus lists them (the server also moves Host out of the
				// header map).
				var names []string
				for name := range got.header {
					if n := strings.ToLower(name); !slices.Contains(c.IgnoreHeaderNames, n) {
						names = append(names, n)
					}
				}
				if !reflect.DeepEqual(sorted(names), sorted(c.ExpectHeaderNames)) {
					t.Errorf("header names = %q, want %q", sorted(names), sorted(c.ExpectHeaderNames))
				}
			}
		})
	}
}

// TestRetryConformance counts attempts per method and status, and pins the
// Retry-After grammar.
func TestRetryConformance(t *testing.T) {
	cleanEnv(t)
	corpus := loadCorpus(t)
	for _, c := range corpus.Retry.Attempts {
		t.Run(c.Name, func(t *testing.T) {
			skipGo(t, c.Skip)
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				for k, v := range c.Headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(c.Status)
			}))
			defer srv.Close()
			client, _ := New("TOK", WithBaseURL(srv.URL), WithRetries(c.Retries), WithBackoff(0))
			req := &Request{Method: c.Method}
			if c.Method != http.MethodGet {
				req.Data = []byte("x")
			}
			_, _ = client.Do(context.Background(), &ScrapeConfig{URL: corpus.URL, TransparentResponse: c.Transparent}, req)
			if got := int(calls.Load()); got != c.Attempts {
				t.Errorf("attempts = %d, want %d", got, c.Attempts)
			}
		})
	}
	for _, c := range corpus.Retry.RetryAfter {
		t.Run("retry_after/"+c.Value, func(t *testing.T) {
			secs, ok := parseRetryAfter(c.Value)
			switch {
			case c.Seconds == nil && ok:
				t.Errorf("parseRetryAfter(%q) = %d, want ignored", c.Value, secs)
			case c.Seconds != nil && (!ok || secs != *c.Seconds):
				t.Errorf("parseRetryAfter(%q) = %d,%v, want %d", c.Value, secs, ok, *c.Seconds)
			}
		})
	}
}

// corpusKinds maps the corpus error vocabulary onto ErrorKind; "api" is the
// base API error, which in Go is an *APIError of KindOther.
var corpusKinds = map[string]ErrorKind{
	"validation": KindValidation, "auth": KindAuth, "concurrency": KindConcurrency,
	"gateway": KindGateway, "api": KindOther,
}

func TestErrorConformance(t *testing.T) {
	cleanEnv(t)
	corpus := loadCorpus(t)
	for _, c := range corpus.Errors {
		t.Run(c.Name, func(t *testing.T) {
			skipGo(t, c.Skip)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range c.Headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(c.Status)
				io.WriteString(w, c.Body)
			}))
			defer srv.Close()
			client, _ := New("TOK", WithBaseURL(srv.URL), WithRetries(0), WithBackoff(0))
			res, err := client.Scrape(context.Background(), &ScrapeConfig{URL: corpus.URL, TransparentResponse: c.Transparent})
			// A plugin call shares the status rules (it has no transparent
			// mode): the same response is the same result or the same error.
			var pluginErr error
			if !c.Transparent {
				_, pluginErr = client.Plugin(context.Background(), "google/search", map[string]any{"q": "x"})
			}

			if c.Kind == nil {
				if err != nil || res.StatusCode != c.Status {
					t.Fatalf("status %d should be a result: err=%v", c.Status, err)
				}
				if pluginErr != nil {
					t.Fatalf("status %d should be a plugin result: err=%v", c.Status, pluginErr)
				}
				return
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("status %d: expected APIError, got %v", c.Status, err)
			}
			if !c.Transparent {
				var pluginAPIErr *APIError
				if !errors.As(pluginErr, &pluginAPIErr) || pluginAPIErr.Kind != apiErr.Kind ||
					pluginAPIErr.Billed != apiErr.Billed || pluginAPIErr.Message != apiErr.Message {
					t.Errorf("plugin error = %v, want the scrape error %v", pluginErr, apiErr)
				}
			}
			if want := corpusKinds[*c.Kind]; apiErr.Kind != want {
				t.Errorf("kind = %s, want %s", apiErr.Kind, want)
			}
			if apiErr.Billed != c.Billed || apiErr.IsRetryable != c.Retryable {
				t.Errorf("billed=%v retryable=%v, want billed=%v retryable=%v",
					apiErr.Billed, apiErr.IsRetryable, c.Billed, c.Retryable)
			}
			// Every error case pins its message, defaults included.
			if c.Message == nil || apiErr.Message != *c.Message {
				t.Errorf("message = %q, want %v", apiErr.Message, c.Message)
			}
			if len(c.Cost) > 0 {
				cost, ok := apiErr.Cost()
				want := string(c.Cost)
				if (want == "null" && ok) || (want != "null" && (!ok || fmt.Sprint(cost) != want)) {
					t.Errorf("cost = %d,%v, want %s", cost, ok, want)
				}
			}
			if c.RetainedBodyChars != nil {
				// The corpus counts the retained length before redaction, so
				// when redaction shortens the body, pin the rule instead:
				// the retained body is the redacted body, truncated.
				want := *c.RetainedBodyChars
				if redacted := redact(c.Body, "TOK"); redacted != c.Body {
					want = min(utf8.RuneCountInString(redacted), retainedBodyChars)
					if apiErr.Body != truncateRunes(redacted, retainedBodyChars) {
						t.Errorf("retained body = %q, want the redacted body", apiErr.Body)
					}
				}
				if got := utf8.RuneCountInString(apiErr.Body); got != want {
					t.Errorf("retained body chars = %d, want %d", got, want)
				}
			}
		})
	}
}

// TestDecodingConformance covers the WHATWG decode ladder: BOM, labels, JSON,
// meta sniffing, UTF-8 and invalid-byte replacement.
func TestDecodingConformance(t *testing.T) {
	corpus := loadCorpus(t)
	for _, c := range corpus.Decoding {
		t.Run(c.Name, func(t *testing.T) {
			skipGo(t, c.Skip)
			body, err := hex.DecodeString(c.BodyHex)
			if err != nil {
				t.Fatalf("bad body_hex: %v", err)
			}
			header := http.Header{}
			if c.ContentType != "" {
				header.Set("Content-Type", c.ContentType)
			}
			res := &Result{content: body, StatusCode: 200, Header: header}
			if got := res.Text(); got != c.Text {
				t.Errorf("Text() = %q, want %q", got, c.Text)
			}
		})
	}
}

func TestRedactionConformance(t *testing.T) {
	corpus := loadCorpus(t)
	for _, c := range corpus.Redaction {
		t.Run(c.Name, func(t *testing.T) {
			skipGo(t, c.Skip)
			if got := redact(c.Input, c.Token); got != c.Output {
				t.Errorf("redact(%q) = %q, want %q", c.Input, got, c.Output)
			}
		})
	}
}

// newCorpusJobs is a client whose async API is a loopback recorder.
func newCorpusJobs(t *testing.T, respond http.HandlerFunc) (*AsyncJobs, *[]captured) {
	t.Helper()
	srv, reqs := recorder(t, respond)
	client, err := New("TOK", WithAsyncBaseURL(srv.URL), WithRetries(0), WithBackoff(0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client.Jobs(), reqs
}

func TestJobsConformance(t *testing.T) {
	cleanEnv(t)
	corpus := loadCorpus(t)
	ctx := context.Background()

	for _, c := range corpus.Jobs.TaskResults {
		t.Run("task_result/"+c.Name, func(t *testing.T) {
			var tr TaskResult
			if err := json.Unmarshal(c.JSON, &tr); err != nil {
				t.Fatalf("decode: %v", err)
			}
			// The same JSON through the wire decodes the same way.
			jobs, _ := newCorpusJobs(t, func(w http.ResponseWriter, r *http.Request) { w.Write(c.JSON) })
			viaWire, err := jobs.Result(ctx, "j-1", "t-1")
			if err != nil || !reflect.DeepEqual(*viaWire, tr) {
				t.Errorf("Result() = %+v, %v; want %+v", viaWire, err, tr)
			}
			content, err := tr.ContentBytes()
			switch c.ContentBytesError {
			case "transport":
				var te *TransportError
				if !errors.As(err, &te) {
					t.Errorf("ContentBytes: want a TransportError, got %v", err)
				}
			case "":
				if err != nil {
					t.Fatalf("ContentBytes: %v", err)
				}
			default:
				t.Fatalf("unknown content_bytes_error %q", c.ContentBytesError)
			}
			stringMap := func(m map[string]string) any {
				out := map[string]any{}
				for k, v := range m {
					out[k] = v
				}
				return out
			}
			got := map[string]any{
				"task_id": tr.TaskID, "job_id": tr.JobID, "url": tr.URL, "status": tr.Status,
				"start_time": tr.StartTime, "end_time": tr.EndTime, "expires_at": tr.ExpiresAt,
				"update_time": tr.UpdateTime, "base64_encoded_content": tr.Base64EncodedContent,
				"status_code": float64(tr.StatusCode), "response_headers": stringMap(tr.ResponseHeaders),
				"scrape_do": stringMap(tr.ScrapeDo), "content": tr.Content, "error_message": tr.ErrorMessage,
				"content_bytes_hex": hex.EncodeToString(content),
			}
			zero := map[string]any{
				"task_id": "", "job_id": "", "url": "", "status": "", "start_time": "", "end_time": "",
				"expires_at": "", "update_time": "", "base64_encoded_content": false, "status_code": float64(0),
				"response_headers": map[string]any{}, "scrape_do": map[string]any{}, "content": "", "error_message": "",
			}
			for key, want := range c.Expect {
				g, known := got[key]
				if !known {
					t.Fatalf("corpus expects %q, which the harness does not map", key)
				}
				if !reflect.DeepEqual(g, want) {
					t.Errorf("%s = %#v, want %#v", key, g, want)
				}
			}
			for _, key := range c.Absent {
				if z, known := zero[key]; !known || !reflect.DeepEqual(got[key], z) {
					t.Errorf("%s = %#v, want absent (the zero value)", key, got[key])
				}
			}
		})
	}

	for _, c := range corpus.Jobs.Account {
		t.Run("account/"+c.Name, func(t *testing.T) {
			jobs, _ := newCorpusJobs(t, func(w http.ResponseWriter, r *http.Request) { w.Write(c.JSON) })
			acc, err := jobs.Me(ctx)
			if err != nil {
				t.Fatalf("Me: %v", err)
			}
			var direct Account
			if err := json.Unmarshal(c.JSON, &direct); err != nil || direct != *acc {
				t.Errorf("json.Unmarshal = %+v, %v; Me = %+v", direct, err, *acc)
			}
			got := map[string]int{
				"available_credits": acc.AvailableCredits, "total_concurrency": acc.TotalConcurrency,
				"free_concurrency": acc.FreeConcurrency, "active_jobs": acc.ActiveJobs,
			}
			for key, want := range c.Expect {
				g, known := got[key]
				if !known {
					t.Fatalf("corpus expects %q, which the harness does not map", key)
				}
				if g != want {
					t.Errorf("%s = %d, want %d", key, g, want)
				}
			}
		})
	}

	for i, body := range corpus.Jobs.CancelBodies {
		t.Run(fmt.Sprintf("cancel_body/%d", i), func(t *testing.T) {
			jobs, reqs := newCorpusJobs(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
			if err := jobs.Cancel(ctx, "j-1"); err != nil {
				t.Errorf("Cancel with a 200 body %q: %v", body, err)
			}
			if len(*reqs) != 1 || (*reqs)[0].method != http.MethodDelete || (*reqs)[0].path != "/api/v1/jobs/j-1" {
				t.Errorf("requests = %+v", *reqs)
			}
		})
	}

	for _, c := range corpus.Jobs.IDs {
		t.Run("id/"+c.ID, func(t *testing.T) {
			jobs, reqs := newCorpusJobs(t, func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, `{}`)
			})
			calls := []func() error{
				func() error { _, err := jobs.Status(ctx, c.ID); return err },
				func() error { _, err := jobs.Result(ctx, c.ID, "task-1"); return err },
				func() error { _, err := jobs.Result(ctx, "job-1", c.ID); return err },
				func() error { return jobs.Cancel(ctx, c.ID) },
			}
			for i, call := range calls {
				err := call()
				if c.Valid {
					if err != nil {
						t.Errorf("call %d: valid id rejected: %v", i, err)
					}
					continue
				}
				var ce *ConfigError
				if !errors.As(err, &ce) {
					t.Errorf("call %d: want a ConfigError, got %v", i, err)
				}
			}
			if c.Valid {
				if len(*reqs) != len(calls) || (*reqs)[0].path != "/api/v1/jobs/"+c.ID {
					t.Errorf("requests = %+v", *reqs)
				}
			} else if len(*reqs) != 0 {
				t.Errorf("an invalid id reached the wire: %+v", *reqs)
			}
		})
	}

	for _, c := range corpus.Jobs.Errors {
		t.Run("error/"+c.Name, func(t *testing.T) {
			jobs, reqs := newCorpusJobs(t, func(w http.ResponseWriter, r *http.Request) {
				if c.Status >= 300 && c.Status < 400 {
					w.Header().Set("Location", "https://elsewhere.invalid/steal")
				}
				w.WriteHeader(c.Status)
				io.WriteString(w, c.Body)
			})
			_, err := jobs.Status(ctx, "job-1")
			if len(*reqs) != 1 || (*reqs)[0].path != "/api/v1/jobs/job-1" {
				t.Fatalf("requests = %+v, want one GET /api/v1/jobs/job-1 (redirects are not followed)", *reqs)
			}
			if c.Kind == "transport" {
				var te *TransportError
				if !errors.As(err, &te) {
					t.Fatalf("want a TransportError, got %T %v", err, err)
				}
				return
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("want an APIError, got %T %v", err, err)
			}
			if want, ok := corpusKinds[c.Kind]; !ok || apiErr.Kind != want {
				t.Errorf("kind = %s, corpus kind %q", apiErr.Kind, c.Kind)
			}
			if c.Message == nil || apiErr.Message != *c.Message {
				t.Errorf("message = %q, want %v", apiErr.Message, c.Message)
			}
			if c.Billed == nil || apiErr.Billed != *c.Billed {
				t.Errorf("billed = %v, want %v", apiErr.Billed, c.Billed)
			}
		})
	}

	for _, c := range corpus.Jobs.Create {
		t.Run("create/"+c.Name, func(t *testing.T) {
			jobs, reqs := newCorpusJobs(t, func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, `{"JobID":"job-1"}`)
			})
			_, err := jobs.Create(ctx, c.Targets, c.Options)
			if c.Error == "config" {
				var ce *ConfigError
				if !errors.As(err, &ce) || len(*reqs) != 0 {
					t.Fatalf("want a ConfigError before any request, got err=%v requests=%d", err, len(*reqs))
				}
				return
			}
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			got := (*reqs)[0]
			if string(got.body) != c.Body {
				t.Errorf("body = %s\nwant   %s", got.body, c.Body)
			}
			if got.header.Get(corpus.Jobs.AuthHeader) != "TOK" || strings.Contains(got.rawQuery, "TOK") {
				t.Errorf("token must travel only in %s: header=%q query=%q",
					corpus.Jobs.AuthHeader, got.header.Get(corpus.Jobs.AuthHeader), got.rawQuery)
			}
		})
	}
}

func TestClientConformance(t *testing.T) {
	corpus := loadCorpus(t)
	isConfigErr := func(err error) bool {
		var ce *ConfigError
		return errors.As(err, &ce)
	}

	for _, c := range corpus.Client.BaseURLs {
		t.Run("base_url/"+c.URL, func(t *testing.T) {
			cleanEnv(t)
			attempts := map[string]func() (*Client, error){
				"WithBaseURL":      func() (*Client, error) { return New("TOK", WithBaseURL(c.URL)) },
				"WithAsyncBaseURL": func() (*Client, error) { return New("TOK", WithAsyncBaseURL(c.URL)) },
				"SDO_BASE_URL": func() (*Client, error) {
					t.Setenv("SDO_BASE_URL", c.URL)
					defer t.Setenv("SDO_BASE_URL", "")
					return New("TOK")
				},
				"SDO_ASYNC_BASE_URL": func() (*Client, error) {
					t.Setenv("SDO_ASYNC_BASE_URL", c.URL)
					defer t.Setenv("SDO_ASYNC_BASE_URL", "")
					return New("TOK")
				},
			}
			for how, build := range attempts {
				client, err := build()
				if c.OK {
					if err != nil {
						t.Errorf("%s(%q): %v", how, c.URL, err)
					} else if c.URL == "" && (client.baseURL != defaultBaseURL || client.asyncBaseURL != defaultAsyncBaseURL) {
						t.Errorf("%s(\"\") did not fall back to the defaults: %v", how, client)
					}
				} else if !isConfigErr(err) {
					t.Errorf("%s(%q): want a ConfigError, got %v", how, c.URL, err)
				}
			}
		})
	}

	t.Run("insecure_opt_in", func(t *testing.T) {
		cleanEnv(t)
		opt := corpus.Client.InsecureOptIn
		for _, o := range []Option{WithBaseURL(opt.URL), WithAsyncBaseURL(opt.URL)} {
			if _, err := New("TOK", WithAllowInsecureHTTP(), o); (err == nil) != opt.OK {
				t.Errorf("with the opt-in: err=%v, want ok=%v", err, opt.OK)
			}
			if _, err := New("TOK", o); !isConfigErr(err) {
				t.Errorf("without the opt-in %q must be refused, got %v", opt.URL, err)
			}
		}
	})

	for _, c := range corpus.Client.Timeouts {
		t.Run("timeout/"+c.Value, func(t *testing.T) {
			cleanEnv(t)
			t.Setenv("SDO_TIMEOUT", c.Value)
			client, err := New("TOK")
			if c.Seconds == nil {
				if !isConfigErr(err) {
					t.Fatalf("SDO_TIMEOUT=%q: want a ConfigError, got %v", c.Value, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("SDO_TIMEOUT=%q: %v", c.Value, err)
			}
			if want := time.Duration(*c.Seconds * float64(time.Second)); client.timeout != want {
				t.Errorf("timeout = %v, want %v", client.timeout, want)
			}
		})
	}

	for _, c := range corpus.Client.EffectiveTimeouts {
		t.Run(fmt.Sprintf("effective_timeout/%v_%d", c.ClientSeconds, c.ConfigTimeoutMs), func(t *testing.T) {
			client := time.Duration(c.ClientSeconds * float64(time.Second))
			want := time.Duration(c.Seconds * float64(time.Second))
			if got := effectiveTimeout(client, c.ConfigTimeoutMs); got != want {
				t.Errorf("effectiveTimeout(%v, %d ms) = %v, want %v", client, c.ConfigTimeoutMs, got, want)
			}
		})
	}

	for _, c := range corpus.Client.Tokens {
		t.Run(fmt.Sprintf("token/%q", c.Value), func(t *testing.T) {
			for _, viaEnv := range []bool{false, true} {
				if viaEnv && strings.ContainsRune(c.Value, 0) {
					continue // an environment variable cannot hold NUL
				}
				cleanEnv(t)
				const fallback = "env-fallback-token"
				arg := c.Value
				if viaEnv {
					arg = ""
					t.Setenv("SDO_TOKEN", c.Value)
				} else {
					t.Setenv("SDO_TOKEN", fallback)
				}
				client, err := New(arg)
				switch {
				case c.Token == nil:
					if !isConfigErr(err) {
						t.Errorf("env=%v: want a ConfigError, got %v", viaEnv, err)
					} else if strings.Contains(err.Error(), strings.TrimSpace(c.Value)) && strings.TrimSpace(c.Value) != "" {
						t.Errorf("the error echoes the token: %q", err)
					}
				case *c.Token == "" && viaEnv:
					if !isConfigErr(err) {
						t.Errorf("an SDO_TOKEN that trims to empty is no token: got %v", err)
					}
				case *c.Token == "":
					if err != nil || client.t.token() != fallback {
						t.Errorf("a token that trims to empty must fall back to SDO_TOKEN: err=%v", err)
					}
				default:
					if err != nil || client.t.token() != *c.Token {
						t.Errorf("env=%v: token = %q, %v; want %q", viaEnv, client.t.token(), err, *c.Token)
					}
				}
			}
		})
	}

	t.Run("empty_token_falls_back_to_env", func(t *testing.T) {
		cleanEnv(t)
		t.Setenv("SDO_TOKEN", "env-token-value")
		client, err := New("")
		if !corpus.Client.EmptyTokenFallsBackToEnv {
			t.Skip("corpus does not require the fallback")
		}
		if err != nil || client.t.token() != "env-token-value" {
			t.Fatalf("New(\"\") with SDO_TOKEN set: err=%v", err)
		}
	})

	for _, c := range corpus.Client.TargetURLs {
		t.Run("target_url/"+c.URL, func(t *testing.T) {
			cleanEnv(t)
			pairs, err := (&ScrapeConfig{URL: c.URL}).toParams()
			if !c.OK {
				if !isConfigErr(err) {
					t.Errorf("want a ConfigError, got %v", err)
				}
				srv, reqs := recorder(t, nil)
				client, _ := New("TOK", WithBaseURL(srv.URL))
				if _, err := client.Scrape(context.Background(), &ScrapeConfig{URL: c.URL}); !isConfigErr(err) || len(*reqs) != 0 {
					t.Errorf("Scrape: want a ConfigError before any request, got err=%v requests=%d", err, len(*reqs))
				}
				return
			}
			if err != nil || pairs[0] != (param{"url", c.URL}) {
				t.Errorf("want url sent exactly as given, got err=%v pairs=%v", err, pairs)
			}
		})
	}
}

// TestDecompressionConformance serves each body through a real HTTP exchange
// with its Content-Encoding, so the transport's own decoding path is tested.
// A failure is a TransportError and is never retried.
func TestDecompressionConformance(t *testing.T) {
	cleanEnv(t)
	corpus := loadCorpus(t)
	for _, c := range corpus.Decompression {
		t.Run(c.Name, func(t *testing.T) {
			skipGo(t, c.Skip)
			body, err := hex.DecodeString(c.BodyHex)
			if err != nil {
				t.Fatalf("bad body_hex: %v", err)
			}
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Encoding", c.ContentEncoding)
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				w.Write(body)
			}))
			defer srv.Close()
			client, _ := New("TOK", WithBaseURL(srv.URL), WithRetries(2), WithBackoff(0))
			res, err := client.Scrape(context.Background(), &ScrapeConfig{URL: corpus.URL})
			switch c.Error {
			case "":
				if err != nil || c.Text == nil {
					t.Fatalf("Scrape: %v", err)
				}
				if got := res.Text(); got != *c.Text {
					t.Errorf("Text() = %q, want %q", got, *c.Text)
				}
			case "transport":
				var te *TransportError
				if !errors.As(err, &te) {
					t.Fatalf("want a TransportError, got %T %v", err, err)
				}
				if got := int(calls.Load()); got != c.Attempts {
					t.Errorf("attempts = %d, want %d", got, c.Attempts)
				}
			default:
				t.Fatalf("unknown error kind %q", c.Error)
			}
		})
	}
}

// TestJSONBodiesConformance decodes each body as Result.JSON and as a plugin's
// JSON response: the same value, or a TransportError, both ways.
func TestJSONBodiesConformance(t *testing.T) {
	cleanEnv(t)
	corpus := loadCorpus(t)
	for _, c := range corpus.JSONBodies {
		t.Run(c.Name, func(t *testing.T) {
			skipGo(t, c.Skip)
			body, err := hex.DecodeString(c.BodyHex)
			if err != nil {
				t.Fatalf("bad body_hex: %v", err)
			}
			var viaResult any
			resultErr := (&Result{content: body, Header: http.Header{}}).JSON(&viaResult)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write(body)
			}))
			defer srv.Close()
			client, _ := New("TOK", WithBaseURL(srv.URL), WithRetries(0))
			viaPlugin, pluginErr := client.Plugin(context.Background(), "google/search", nil)
			switch c.Error {
			case "":
				var want any
				if err := json.Unmarshal(c.Value, &want); err != nil {
					t.Fatalf("corpus value: %v", err)
				}
				if resultErr != nil || !reflect.DeepEqual(viaResult, want) {
					t.Errorf("Result.JSON = %#v, %v; want %#v", viaResult, resultErr, want)
				}
				if pluginErr != nil || !reflect.DeepEqual(viaPlugin, want) {
					t.Errorf("Plugin = %#v, %v; want %#v", viaPlugin, pluginErr, want)
				}
			case "transport":
				var te1, te2 *TransportError
				if !errors.As(resultErr, &te1) || !errors.As(pluginErr, &te2) {
					t.Errorf("want TransportErrors, got %v / %v", resultErr, pluginErr)
				}
			default:
				t.Fatalf("unknown error kind %q", c.Error)
			}
		})
	}
}

// TestContractConstants pins the normative contract the corpus carries against
// what this SDK actually implements.
func TestContractConstants(t *testing.T) {
	cleanEnv(t)
	corpus := loadCorpus(t)
	ct := corpus.Contract

	s := ct.Serialization
	if !s.OmitFalseBooleans || !s.OmitZeroValues || s.JSONObjectKeys != "sorted" || s.JSONKeyOrder != "code_point" ||
		s.JSONHTMLEscape || s.JSONLineSeparators != "literal" || s.Numbers != "ecmascript" ||
		s.QueryOrder != "spec_then_extra_sorted" || s.ExtraParamsOrder != "sorted" || s.Enums != "lowercase" ||
		!reflect.DeepEqual(s.ExtraParamsReserved, []string{"token", "url"}) {
		t.Errorf("serialization contract changed; update the SDK: %+v", s)
	}

	d := ct.Decoding
	if d.Algorithm != "whatwg" || !d.StripBOM || d.InvalidBytes != "replace" || d.MetaSniffBytes != metaSniffBytes ||
		!reflect.DeepEqual(d.MetaSniffTypes, metaSniffTypes) {
		t.Errorf("decoding contract changed; update the SDK: %+v", d)
	}
	if defaultMaxResponseBytes != d.MaxDecompressedBytes || defaultMaxResponseBytes != d.MaxResponseBytes {
		t.Errorf("max bytes = %d, corpus says %d / %d", defaultMaxResponseBytes, d.MaxDecompressedBytes, d.MaxResponseBytes)
	}

	if !d.BOMOverridesLabel || d.Labels != "whatwg" || d.UnknownOrReplacementLabel != "ignored" || !d.JSONStripsBOM {
		t.Errorf("decoding contract changed; update the SDK: %+v", d)
	}

	tr := ct.Transport
	if tr.DefaultTimeoutSeconds != defaultTimeout.Seconds() || tr.FollowRedirects || !tr.HTTPSOnly || !tr.EmptyTokenIsUnset {
		t.Errorf("transport contract changed; update the SDK: %+v", tr)
	}
	if tr.ClientTimeout != "total" || tr.GatewayTimeoutMarginSeconds != gatewayTimeoutMargin.Seconds() ||
		tr.EffectiveTimeout != "max(client_timeout, config.timeout_ms / 1000 + margin)" ||
		tr.TokenTrim != "ascii_whitespace" || tr.HeaderNames != "rfc9110_token" || tr.HeaderValues != "printable_ascii" ||
		!reflect.DeepEqual(tr.BaseURLParts, []string{"scheme", "host", "port", "path_prefix"}) {
		t.Errorf("transport contract changed; update the SDK: %+v", tr)
	}
	var forbidden []string
	for h := range forbiddenHeaders {
		forbidden = append(forbidden, h)
	}
	if !reflect.DeepEqual(sorted(forbidden), sorted(tr.ForbiddenHeaders)) {
		t.Errorf("forbidden headers = %v, corpus %v", sorted(forbidden), tr.ForbiddenHeaders)
	}
	for _, coding := range tr.ContentCodings {
		if got := decodingReader(strings.NewReader(""), coding, 1); reflect.TypeOf(got) != reflect.TypeOf(&capReader{}) {
			t.Errorf("content coding %q is not decoded", coding)
		}
	}
	if len(tr.ContentCodings) != 4 {
		t.Errorf("content codings changed: %v", tr.ContentCodings)
	}

	e := ct.Errors
	defaults := map[string]string{
		"validation": msgValidation, "auth": msgAuth, "concurrency": msgConcurrency,
		"gateway": strings.Replace(msgGateway, "%d", "{status}", 1), "api": strings.Replace(msgAPI, "%d", "{status}", 1),
		"redirect": strings.Replace(msgRedirect, "%d", "{status}", 1),
		"canceled": strings.Replace(msgCanceled, "%d", "{status}", 1),
	}
	if !reflect.DeepEqual(e.DefaultMessages, defaults) {
		t.Errorf("default messages = %v, corpus %v", defaults, e.DefaultMessages)
	}
	if e.ErrorBodyReadBytes != errorBodyReadBytes || e.AsyncBilled {
		t.Errorf("errors contract = %+v, SDK reads %d bytes and never bills async errors", e, errorBodyReadBytes)
	}
	var billed []int
	for s := range billedStatus {
		billed = append(billed, s)
	}
	slices.Sort(billed)
	// target_origin and billed are pinned by the errors cases; the names here
	// are the rules targetAnswered and newAPIError implement.
	rc := e.RequestCost
	if !reflect.DeepEqual(billed, e.BilledStatuses) || e.TargetOrigin != "request_cost_positive" ||
		e.Billed != "request_cost_else_billed_statuses" || rc.Values != 1 || rc.Trim != " \t" ||
		rc.Digits != "ascii" || rc.Max != maxCost || !reflect.DeepEqual(e.EnvelopeKeys, []string{"message", "error"}) {
		t.Errorf("errors contract = %+v, SDK bills %v", e, billed)
	}
	if jb := ct.JSONBodies; jb.InvalidUTF8 != "replace" || jb.NonFiniteNumbers != "reject" {
		t.Errorf("json_bodies contract changed: %+v", jb)
	}
	for _, h := range tr.InsecureHTTPHosts {
		if !isLoopbackHost(h) {
			t.Errorf("insecure host %q is not accepted", h)
		}
	}
	r := tr.Retry
	if r.RetryAfter != "seconds" || r.NegativeRetries != "clamp_to_zero" || r.RetryAfterSend {
		t.Errorf("retry contract changed; update the SDK: %+v", r)
	}
	if r.DefaultRetries != defaultRetries || r.MaxBackoffSeconds != maxBackoff.Seconds() {
		t.Errorf("retry defaults = %d/%v, corpus %d/%v", defaultRetries, maxBackoff, r.DefaultRetries, r.MaxBackoffSeconds)
	}
	var retryable []int
	for st := range retryableStatus {
		retryable = append(retryable, st)
	}
	sort.Ints(retryable)
	if !reflect.DeepEqual(retryable, r.RetryableStatuses) {
		t.Errorf("retryable statuses = %v, corpus %v", retryable, r.RetryableStatuses)
	}
	var methods []string
	for m := range nonIdempotent {
		methods = append(methods, m)
	}
	if sort.Strings(methods); !reflect.DeepEqual(methods, sorted(r.NonIdempotentMethods)) {
		t.Errorf("non-idempotent methods = %v, corpus %v", methods, r.NonIdempotentMethods)
	}
	if !reflect.DeepEqual(r.NonIdempotentRetryStatuses, []int{http.StatusTooManyRequests}) {
		t.Errorf("non-idempotent retry statuses changed: %v", r.NonIdempotentRetryStatuses)
	}

	var env []string
	for k := range ct.Environment {
		env = append(env, k)
	}
	if want := []string{"SDO_ASYNC_BASE_URL", "SDO_BASE_URL", "SDO_TIMEOUT", "SDO_TOKEN"}; !reflect.DeepEqual(sorted(env), want) {
		t.Errorf("environment contract = %v, SDK reads %v", sorted(env), want)
	}

	client, err := New("TOK")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	wantUA := strings.NewReplacer("<lang>", "go", "<version>", Version).Replace(ct.UserAgent.SDK)
	if client.t.userAgent != wantUA {
		t.Errorf("user agent = %q, want %q", client.t.userAgent, wantUA)
	}
	if userAgentRe.String() != ct.UserAgent.Pattern {
		t.Errorf("user-agent pattern = %q, corpus says %q", userAgentRe.String(), ct.UserAgent.Pattern)
	}
	if cli := strings.ReplaceAll(ct.UserAgent.CLI, "<version>", Version); !userAgentRe.MatchString(cli) || !userAgentRe.MatchString(wantUA) {
		t.Errorf("the documented user agents do not match the pattern: %q %q", cli, wantUA)
	}

	if pluginRouteRe.String() != ct.PluginRoutePattern {
		t.Errorf("route pattern = %q, corpus says %q", pluginRouteRe.String(), ct.PluginRoutePattern)
	}
	if ct.Redaction.Mask != redactMask || !ct.Redaction.LiteralToken || ct.Redaction.LiteralMinLength != literalTokenMinLen ||
		len(ct.Redaction.Shapes) != len(secretShapes) {
		t.Errorf("redaction contract changed; update the SDK: %+v", ct.Redaction)
	}
	if corpus.Jobs.AuthHeader != asyncAuthHeader || corpus.Jobs.IDPattern != jobIDRe.String() {
		t.Errorf("jobs contract = %q %q, SDK %q %q", corpus.Jobs.AuthHeader, corpus.Jobs.IDPattern, asyncAuthHeader, jobIDRe.String())
	}
}

func sorted(s []string) []string {
	out := slices.Clone(s)
	sort.Strings(out)
	return out
}
