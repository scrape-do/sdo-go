// SPDX-License-Identifier: Apache-2.0

package sdo

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDecompressRawFlate exercises the "deflate" fallback: a raw (headerless)
// flate stream fails zlib.NewReader and must be decoded by the raw flate reader.
func TestDecompressRawFlate(t *testing.T) {
	body := []byte("raw flate payload without zlib wrapper")
	var buf bytes.Buffer
	fw, err := flate.NewWriter(&buf, flate.DefaultCompression)
	if err != nil {
		t.Fatalf("flate.NewWriter: %v", err)
	}
	fw.Write(body)
	fw.Close()

	out, err := decompress(buf.Bytes(), "deflate", defaultMaxResponseBytes)
	if err != nil {
		t.Fatalf("decompress raw flate: %v", err)
	}
	if !bytes.Equal(out, body) {
		t.Errorf("raw flate = %q, want %q", out, body)
	}
}

// TestDecompressGzipError covers the gzip.NewReader error branch: bytes that are
// not a valid gzip stream must surface an error rather than garbage.
func TestDecompressGzipError(t *testing.T) {
	_, err := decompress([]byte("this is not gzip"), "gzip", defaultMaxResponseBytes)
	if err == nil {
		t.Fatal("expected error decompressing non-gzip body as gzip")
	}
}

// TestDecodingErrorsAreSticky: a decoding stream that failed keeps failing
// with the same error on every later read (a corrupt stream, a body over the
// cap), instead of resuming mid-stream.
func TestDecodingErrorsAreSticky(t *testing.T) {
	var big bytes.Buffer
	zw := gzip.NewWriter(&big)
	zw.Write(bytes.Repeat([]byte("a"), 100))
	zw.Close()
	cases := map[string]struct {
		r    io.Reader
		want func(error) bool
	}{
		"corrupt": {decodingReader(strings.NewReader("this is not gzip"), "gzip", 1<<10),
			func(err error) bool { return errors.Is(err, gzip.ErrHeader) }},
		"over the cap": {decodingReader(bytes.NewReader(big.Bytes()), "gzip", 10),
			func(err error) bool { return errors.As(err, new(*ResponseTooLargeError)) }},
	}
	for name, c := range cases {
		buf := make([]byte, 64)
		for i := range 2 {
			if n, err := c.r.Read(buf); !c.want(err) {
				t.Errorf("%s: read %d = %d, %v", name, i, n, err)
			}
		}
	}
}

// TestGzipCorruptMemberAfterTheLast: data after the last gzip member is
// ignored only when it does not start another member; a second member with a
// broken header is a corrupt body, not a silently shortened one.
func TestGzipCorruptMemberAfterTheLast(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write([]byte("first"))
	zw.Close()
	if out, err := decompress(append(buf.Bytes(), "trailing junk"...), "gzip", 1<<10); err != nil || string(out) != "first" {
		t.Errorf("junk after the last member = %q, %v; want it ignored", out, err)
	}
	broken := append(buf.Bytes(), 0x1f, 0x8b, 0x00, 0, 0, 0, 0, 0, 0, 0) // gzip magic, unknown method
	if _, err := decompress(broken, "gzip", 1<<10); !errors.Is(err, gzip.ErrHeader) {
		t.Errorf("a broken second member = %v, want gzip.ErrHeader", err)
	}
}

// TestPluginParamsLists covers list-valued plugin params: each element becomes
// its own repeated param under the same key, for any slice type, and nil
// elements are skipped.
func TestPluginParamsLists(t *testing.T) {
	got, err := pluginParams(map[string]any{"tag": []string{"a", "b"}, "n": []int{1, 2}, "x": []any{"c", nil, 1.5}})
	if err != nil {
		t.Fatalf("pluginParams: %v", err)
	}
	want := []param{{"n", "1"}, {"n", "2"}, {"tag", "a"}, {"tag", "b"}, {"x", "c"}, {"x", "1.5"}}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("param[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestReadBodyError covers the io.ReadAll error path in transport.do: the server
// promises more bytes than it delivers, then drops the connection, so reading
// the body fails and a TransportError is returned.
func TestReadBodyError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("ResponseWriter is not a Hijacker")
		}
		conn, bufrw, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		// Declare a body longer than we actually send, then close abruptly.
		bufrw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n")
		bufrw.WriteString("short")
		bufrw.Flush()
		conn.(*net.TCPConn).SetLinger(0)
		conn.Close()
	}))
	t.Cleanup(srv.Close)

	c, _ := New("t", WithBaseURL(srv.URL), WithRetries(0), WithBackoff(0))
	_, err := c.Scrape(context.Background(), &ScrapeConfig{URL: "https://e.com"})
	var te *TransportError
	if !errors.As(err, &te) || te.Timeout {
		t.Fatalf("expected a non-timeout TransportError from truncated body, got %v", err)
	}
}
