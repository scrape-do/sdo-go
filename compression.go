// SPDX-License-Identifier: Apache-2.0

package sdo

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"
	"strings"

	"github.com/andybalholm/brotli"
)

// Content-Encoding handling. Go's net/http only auto-decompresses gzip when it
// set Accept-Encoding itself; the SDK sets Accept-Encoding explicitly, so it
// decodes here, for every body it reads (scrape, plugin, jobs, error bodies):
//
//   - the header is a comma list, trimmed and matched case-insensitively, and
//     decoded in reverse order (the last coding applied is undone first);
//   - gzip and x-gzip (every member; data after the final member is ignored),
//     deflate (zlib-wrapped or raw) and br are decoded, identity is skipped;
//   - any other coding stops decoding: the bytes are returned as they are at
//     that point;
//   - an empty body is an empty body, whatever the coding;
//   - a truncated or corrupt stream is an error (a *TransportError for the
//     caller, never retried), and so is a chain of more than
//     maxContentCodings codings to decode, whatever the body.
//
// Inflation is bounded: the scraped target controls the body, so a small
// compressed payload could otherwise inflate without bound. Every stage is
// streamed through a cap that fails as soon as it is crossed, so an oversized
// buffer is never materialized.

// contentCodings returns the codings to undo, in the order to undo them.
func contentCodings(header string) []string {
	parts := strings.Split(header, ",")
	out := make([]string, 0, len(parts))
	for i := len(parts) - 1; i >= 0; i-- {
		c := lowerASCII(strings.Trim(parts[i], " \t"))
		if c == "" || c == "identity" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// maxContentCodings is the most codings one body is decoded through (urllib3's
// limit too). Each stage holds its own decoder state and buffers, and the
// target controls the header, so a chain of thousands would cost memory and
// time before the size cap could stop anything.
const maxContentCodings = 5

// errTooManyCodings fails a body whose chain is longer than maxContentCodings.
var errTooManyCodings = fmt.Errorf("more than %d content codings", maxContentCodings)

// decodingReader streams src through the decoders its Content-Encoding names.
// Each stage's output is capped at limit bytes.
func decodingReader(src io.Reader, encoding string, limit int64) io.Reader {
	var opens []func(*bufio.Reader) (io.Reader, error)
codings:
	for _, c := range contentCodings(encoding) {
		var open func(*bufio.Reader) (io.Reader, error)
		switch c {
		case "gzip", "x-gzip":
			open = openGzip
		case "deflate":
			open = openDeflate
		case "br":
			open = func(b *bufio.Reader) (io.Reader, error) { return brotliStream{brotli.NewReader(b)}, nil }
		default:
			break codings // an unsupported coding: the bytes stay as they are
		}
		if len(opens) == maxContentCodings {
			return failingReader{errTooManyCodings}
		}
		opens = append(opens, open)
	}
	r := src
	for _, open := range opens {
		r = &capReader{r: &stage{src: bufio.NewReader(r), open: open}, limit: limit}
	}
	return r
}

// failingReader fails every read with err.
type failingReader struct{ err error }

func (f failingReader) Read([]byte) (int, error) { return 0, f.err }

// decompress decodes an in-memory body; the result is at most limit bytes.
func decompress(body []byte, encoding string, limit int64) ([]byte, error) {
	return readCapped(decodingReader(bytes.NewReader(body), encoding, limit), limit)
}

// stage is one decoder, opened on first read. A stage whose input is empty
// yields an empty output instead of a missing-header error.
type stage struct {
	src  *bufio.Reader
	open func(*bufio.Reader) (io.Reader, error)
	dec  io.Reader
	err  error
}

func (s *stage) Read(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	if s.dec == nil {
		if _, err := s.src.Peek(1); err != nil {
			s.err = err // io.EOF: an empty input is an empty output
			return 0, err
		}
		if s.dec, s.err = s.open(s.src); s.err != nil {
			return 0, s.err
		}
	}
	return s.dec.Read(p)
}

// openDeflate picks zlib when the stream starts with a valid zlib header and
// raw deflate otherwise ("deflate" is used for both in the wild).
func openDeflate(b *bufio.Reader) (io.Reader, error) {
	if h, _ := b.Peek(2); len(h) == 2 && h[0]&0x0f == 8 && h[0]>>4 <= 7 && (uint16(h[0])<<8|uint16(h[1]))%31 == 0 {
		return zlib.NewReader(b)
	}
	return flate.NewReader(b), nil
}

// brotliStream ignores data after the end of a complete brotli stream, as
// gzipMembers does after the last member. The brotli package reports such
// data only through its unexported "excessive input" error, which it returns
// once the stream has finished; a truncated stream is io.ErrUnexpectedEOF.
type brotliStream struct{ r *brotli.Reader }

func (b brotliStream) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err != nil && err.Error() == "brotli: excessive input" {
		return n, io.EOF
	}
	return n, err
}

// gzipMembers reads every gzip member in turn and ignores anything after the
// last one that does not start another member.
type gzipMembers struct {
	src *bufio.Reader
	z   *gzip.Reader
}

func openGzip(b *bufio.Reader) (io.Reader, error) {
	z, err := gzip.NewReader(b)
	if err != nil {
		return nil, err
	}
	z.Multistream(false)
	return &gzipMembers{src: b, z: z}, nil
}

func (g *gzipMembers) Read(p []byte) (int, error) {
	for {
		n, err := g.z.Read(p)
		if err != io.EOF {
			return n, err
		}
		if n > 0 {
			return n, nil // the member's end; the next call looks for another
		}
		if next, _ := g.src.Peek(2); len(next) < 2 || next[0] != 0x1f || next[1] != 0x8b {
			return 0, io.EOF
		}
		if err := g.z.Reset(g.src); err != nil {
			return 0, err
		}
		g.z.Multistream(false)
	}
}

// capReader fails with *ResponseTooLargeError once more than limit bytes have
// passed through it.
type capReader struct {
	r     io.Reader
	limit int64
	n     int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.n > c.limit {
		return 0, &ResponseTooLargeError{Limit: c.limit}
	}
	if room := c.limit - c.n + 1; int64(len(p)) > room {
		p = p[:room]
	}
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.n > c.limit {
		return n, &ResponseTooLargeError{Limit: c.limit}
	}
	return n, err
}

// readCapped reads at most limit bytes, reporting ResponseTooLargeError as soon
// as the source has more. It reads limit+1 so "exactly at the limit" succeeds.
func readCapped(r io.Reader, limit int64) ([]byte, error) {
	buf, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) > limit {
		return nil, &ResponseTooLargeError{Limit: limit}
	}
	return buf, nil
}

// lowerASCII lowercases A-Z only. Unicode case folding would let "MOBİLE" or a
// Kelvin sign match an ASCII keyword.
func lowerASCII(s string) string {
	for i := 0; i < len(s); i++ {
		if 'A' <= s[i] && s[i] <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if 'A' <= b[j] && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}
