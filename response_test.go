// SPDX-License-Identifier: Apache-2.0

package sdo

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func result(content []byte, header http.Header) *Result {
	if header == nil {
		header = http.Header{}
	}
	return &Result{content: content, StatusCode: 200, Header: header, URL: "https://e.com"}
}

func TestTriModalBody(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	r := result([]byte(`{"a":1}`), h)
	if string(r.Bytes()) != `{"a":1}` {
		t.Errorf("Bytes: %s", r.Bytes())
	}
	if r.Text() != `{"a":1}` {
		t.Errorf("Text: %s", r.Text())
	}
	var v struct{ A int }
	if err := r.JSON(&v); err != nil || v.A != 1 {
		t.Errorf("JSON: %v err=%v", v, err)
	}
}

func TestHeaderMetadata(t *testing.T) {
	h := http.Header{}
	h.Set("Scrape.do-Request-Cost", "10")
	h.Set("Scrape.do-Remaining-Credits", "999")
	r := result([]byte("ok"), h)
	if c, ok := r.Cost(); !ok || c != 10 {
		t.Errorf("Cost: %d %v", c, ok)
	}
	if rc, ok := r.RemainingCredits(); !ok || rc != 999 {
		t.Errorf("RemainingCredits: %d %v", rc, ok)
	}
}

// TestScrapeResultMetadata feeds the gateway's scrape.do-* enrichment headers and
// asserts each read-only accessor parses them, including the nullable/absent path.
func TestScrapeResultMetadata(t *testing.T) {
	h := http.Header{}
	h.Set("scrape.do-initial-status-code", "403")
	h.Set("scrape.do-target-url", "https://target.example/page")
	h.Set("scrape.do-resolved-url", "https://target.example/page?ok=1")
	h.Set("scrape.do-cookies", "sid=abc; path=/")
	r := result([]byte("ok"), h)

	if code, ok := r.InitialStatusCode(); !ok || code != 403 {
		t.Errorf("InitialStatusCode = %d %v, want 403 true", code, ok)
	}
	if r.TargetURL() != "https://target.example/page" {
		t.Errorf("TargetURL = %q", r.TargetURL())
	}
	if r.ResolvedURL() != "https://target.example/page?ok=1" {
		t.Errorf("ResolvedURL = %q", r.ResolvedURL())
	}
	if r.Cookies() != "sid=abc; path=/" {
		t.Errorf("Cookies = %q", r.Cookies())
	}

	// Absent headers: every accessor must report the nullable/empty path.
	empty := result([]byte("ok"), http.Header{})
	if _, ok := empty.InitialStatusCode(); ok {
		t.Error("InitialStatusCode should be absent")
	}
	if empty.TargetURL() != "" || empty.ResolvedURL() != "" || empty.Cookies() != "" {
		t.Error("string metadata accessors should be empty when headers absent")
	}
}

func TestTextCharsetLadder(t *testing.T) {
	cases := []struct {
		body []byte
		ct   string
		want string
	}{
		{[]byte("caf\xe9"), "text/html; charset=iso-8859-1", "café"},
		{[]byte("{\"k\":\"\xc3\xa9\"}"), "application/json", `{"k":"é"}`},
		{[]byte("<meta charset='windows-1254'>\xfd"), "text/html", "<meta charset='windows-1254'>ı"},
		{[]byte("plain \xc3\xa9"), "text/html", "plain é"},
		// Not a meta tag: the prescan walks tags, it does not grep for charset=.
		{[]byte("<p>charset=iso-8859-1</p>\xc3\xa9"), "text/html", "<p>charset=iso-8859-1</p>é"},
		// A comment hides a meta; a later real one still counts.
		{[]byte("<!-- <meta charset=koi8-r> --><meta charset=iso-8859-1>\xe9"), "", "<!-- <meta charset=koi8-r> --><meta charset=iso-8859-1>é"},
		// http-equiv content without the pragma is ignored.
		{[]byte(`<meta content="text/html; charset=iso-8859-1">` + "\xc3\xa9"), "", `<meta content="text/html; charset=iso-8859-1">é`},
		// x-user-defined via meta means windows-1252.
		{[]byte("<meta charset=x-user-defined>\x80"), "", "<meta charset=x-user-defined>€"},
		// Other multi-byte encodings go through x/text.
		{[]byte("\x81\x30\x81\x30"), "text/plain; charset=gb18030", "\u0080"},
		{[]byte("\xa4\xa4"), "text/plain; charset=big5", "中"},
	}
	for _, c := range cases {
		if got := result(c.body, http.Header{"Content-Type": {c.ct}}).Text(); got != c.want {
			t.Errorf("Text(%q, %q) = %q, want %q", c.body, c.ct, got, c.want)
		}
	}
}

func TestTextDecodesCharset(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Type", "text/plain; charset=latin1")
	r := result([]byte{0x63, 0x61, 0x66, 0xe9}, h) // café in latin-1
	if r.Text() != "café" {
		t.Errorf("Text = %q, want café", r.Text())
	}
}

// TestUTF16BOMEndianness: a byte-order mark picks the endianness and is
// consumed. FE FF => big-endian, FF FE => little.
func TestUTF16BOMEndianness(t *testing.T) {
	for name, body := range map[string][]byte{
		"big-endian":    {0xFE, 0xFF, 0x00, 0x68, 0x00, 0x69},
		"little-endian": {0xFF, 0xFE, 0x68, 0x00, 0x69, 0x00},
	} {
		if got := result(body, http.Header{"Content-Type": {"text/html"}}).Text(); got != "hi" {
			t.Errorf("%s: Text = %q, want %q", name, got, "hi")
		}
	}
}

// UTF-16 surrogates: a valid pair joins; a lone lead or trail is U+FFFD.
func TestUTF16Surrogates(t *testing.T) {
	cases := map[string]string{
		"\xff\xfe\x3d\xd8\x00\xde":         "😀",
		"\xff\xfe\x3d\xd8\x41\x00":         "\ufffdA",
		"\xff\xfe\x00\xde\x41\x00":         "\ufffdA",
		"\xff\xfe\x3d\xd8":                 "\ufffd",
		"\xfe\xff\xd8\x3d\xde\x00\x00\x41": "😀A",
	}
	for body, want := range cases {
		if got := result([]byte(body), nil).Text(); got != want {
			t.Errorf("Text(%q) = %q, want %q", body, got, want)
		}
	}
}

func TestResponseMetadataBadValues(t *testing.T) {
	r := result([]byte("x"), http.Header{
		"Scrape.do-Request-Cost": {"not-a-number"},
	})
	if _, ok := r.Cost(); ok {
		t.Error("a non-numeric cost header parsed as present")
	}
}

// An unknown charset label must not panic or return raw invalid bytes.
func TestUnknownCharsetFallsBackToValidUTF8(t *testing.T) {
	r := result([]byte{'a', 0xff, 'b'}, http.Header{"Content-Type": {"text/html; charset=x-no-such-charset"}})
	if got := r.Text(); got != "a�b" {
		t.Errorf("Text() = %q, want replacement for the invalid byte", got)
	}
}

// Meta sniffing only looks at the first 1024 bytes, as in Python and JS.
func TestMetaCharsetBeyondSniffWindowIsIgnored(t *testing.T) {
	body := append(bytes.Repeat([]byte(" "), 1100), []byte(`<meta charset="iso-8859-1">`+"\xe9")...)
	if got := result(body, http.Header{"Content-Type": {"text/html"}}).Text(); !strings.HasSuffix(got, "\ufffd") {
		t.Errorf("Text = %q, want the meta past the sniff window ignored", got[len(got)-10:])
	}
}

// JSON ignores a leading UTF-8 BOM.
func TestJSONIgnoresBOM(t *testing.T) {
	var v map[string]int
	if err := result([]byte("\xef\xbb\xbf{\"a\":1}"), nil).JSON(&v); err != nil || v["a"] != 1 {
		t.Errorf("JSON with BOM: %v %v", v, err)
	}
}

func TestResultJSONErrorIsTransportError(t *testing.T) {
	var v map[string]any
	err := result([]byte("not json"), nil).JSON(&v)
	var te *TransportError
	if !errors.As(err, &te) {
		t.Fatalf("want *TransportError, got %T %v", err, err)
	}
	var syn *json.SyntaxError
	if !errors.As(err, &syn) {
		t.Fatalf("the decoder error should stay reachable, got %v", err)
	}
}

// Integer headers are one plain decimal: no sign (except a negative credit
// balance), no repeats, no overflow.
func TestIntegerHeadersAreStrict(t *testing.T) {
	for _, tc := range []struct {
		values []string
		cost   int
		ok     bool
	}{
		{[]string{"5"}, 5, true},
		{[]string{" 7\t"}, 7, true},
		{[]string{"+5"}, 0, false},
		{[]string{"-1"}, 0, false},
		{[]string{"1", "2"}, 0, false},
		{[]string{"99999999999999999999999"}, 0, false},
		{[]string{"١"}, 0, false},
		{[]string{""}, 0, false},
	} {
		h := http.Header{"Scrape.do-Request-Cost": tc.values, "Scrape.do-Remaining-Credits": tc.values}
		r := result(nil, h)
		if n, ok := r.Cost(); n != tc.cost || ok != tc.ok {
			t.Errorf("Cost(%q) = %d,%v; want %d,%v", tc.values, n, ok, tc.cost, tc.ok)
		}
	}
	r := result(nil, http.Header{"Scrape.do-Remaining-Credits": {"-3"}, "Scrape.do-Initial-Status-Code": {"-3"}})
	if n, ok := r.RemainingCredits(); n != -3 || !ok {
		t.Errorf("RemainingCredits(-3) = %d,%v", n, ok)
	}
	if _, ok := r.InitialStatusCode(); ok {
		t.Errorf("a negative status code was accepted")
	}
}

// JSON bodies reject numbers a float64 cannot hold, whatever the target type.
func TestJSONRejectsOverflowForEveryTarget(t *testing.T) {
	var n json.Number
	if err := result([]byte(`1e400`), http.Header{}).JSON(&n); err == nil {
		t.Errorf("1e400 into json.Number was accepted")
	}
	var raw json.RawMessage
	if err := result([]byte(`{"a":[1,`+strings.Repeat("9", 400)+`]}`), http.Header{}).JSON(&raw); err == nil {
		t.Errorf("a 400-digit integer was accepted")
	}
	var v any
	for _, ok := range []string{`[1e-400, 1.7976931348623157e308, -0, "1e400", "\"1e999"]`, `{"e":1E5}`} {
		if err := result([]byte(ok), http.Header{}).JSON(&v); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	err := result([]byte(`{"a":`), http.Header{}).JSON(&v)
	var te *TransportError
	if !errors.As(err, &te) || !strings.Contains(te.Message, "could not be decoded as JSON") {
		t.Errorf("want a TransportError, got %v", err)
	}
}
