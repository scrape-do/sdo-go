// SPDX-License-Identifier: Apache-2.0

package sdo

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/andybalholm/brotli"
)

func TestDecompress(t *testing.T) {
	body := []byte("hello world payload")

	var gz bytes.Buffer
	gw := gzip.NewWriter(&gz)
	gw.Write(body)
	gw.Close()

	var zl bytes.Buffer
	zw := zlib.NewWriter(&zl)
	zw.Write(body)
	zw.Close()

	var br bytes.Buffer
	bw := brotli.NewWriter(&br)
	bw.Write(body)
	bw.Close()

	cases := []struct {
		enc string
		in  []byte
	}{
		{"gzip", gz.Bytes()},
		{"deflate", zl.Bytes()},
		{"br", br.Bytes()},
		{"", body},
		{"identity", body},
		{"unknown-codec", body},
	}
	for _, c := range cases {
		out, err := decompress(c.in, c.enc, defaultMaxResponseBytes)
		if err != nil {
			t.Fatalf("decompress %q: %v", c.enc, err)
		}
		if !bytes.Equal(out, body) {
			t.Errorf("decompress %q = %q, want %q", c.enc, out, body)
		}
	}
}

// A body is decoded through at most five codings: a longer chain is a
// TransportError (never retried), whatever the body; an error status keeps its
// error and only loses the body's detail.
func TestTooManyContentCodings(t *testing.T) {
	var calls atomic.Int32
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		n := len(r.URL.Query().Get("url")) - len("https://e.com/")
		body := []byte("hello")
		for range min(n, 6) {
			var gz bytes.Buffer
			gw := gzip.NewWriter(&gz)
			gw.Write(body)
			gw.Close()
			body = gz.Bytes()
		}
		w.Header().Set("Content-Encoding", strings.TrimSuffix(strings.Repeat("gzip, ", n), ", "))
		if r.URL.Query().Get("render") == "true" {
			w.WriteHeader(http.StatusBadGateway)
		}
		w.Write(body)
	}, WithRetries(2))
	scrape := func(n int, fail bool) (*Result, error) {
		return c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com/" + strings.Repeat("x", n), Render: fail})
	}
	if res, err := scrape(5, false); err != nil || string(res.Bytes()) != "hello" {
		t.Fatalf("five codings: %v", err)
	}
	for _, n := range []int{6, 2000} {
		calls.Store(0)
		_, err := scrape(n, false)
		var te *TransportError
		if !errors.As(err, &te) || te.Message != "failed to decompress response" ||
			!strings.Contains(errors.Unwrap(te).Error(), "more than 5 content codings") || calls.Load() != 1 {
			t.Errorf("%d codings: %v (%d calls)", n, err, calls.Load())
		}
	}
	if _, err := decompress(nil, strings.Repeat("br,", 6), 1); !errors.Is(err, errTooManyCodings) {
		t.Errorf("an empty body with six codings: %v", err)
	}
	if out, err := decompress([]byte("raw"), "gzip,gzip,gzip,gzip,gzip,gzip,zstd", 1<<10); err != nil || string(out) != "raw" {
		t.Errorf("codings after an unsupported one are not counted: %q, %v", out, err)
	}
	var ae *APIError
	if _, err := scrape(6, true); !errors.As(err, &ae) {
		t.Errorf("an error status with six codings: %v", err)
	}
}

func TestPluginRawBytes(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte{0x89, 0x50, 0x4e, 0x47})
	})
	out, err := c.Plugin(context.Background(), "amazon/product", map[string]any{"flag": true, "ids": []any{1, 2}, "skip": nil})
	if err != nil {
		t.Fatalf("Plugin: %v", err)
	}
	if b, ok := out.([]byte); !ok || len(b) != 4 {
		t.Errorf("expected raw bytes, got %T %v", out, out)
	}
}
