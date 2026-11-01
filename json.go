// SPDX-License-Identifier: Apache-2.0

package sdo

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// canonicalJSON is the one JSON encoding the SDK puts on the wire: JSON-valued
// query params, JSON request bodies and the async job body. Every Scrape.do SDK
// produces these exact bytes, so the same input is the same request in every
// language.
//
// encoding/json is close but not it: it escapes U+2028/U+2029 even with
// SetEscapeHTML(false), and it prints 1e-07 and 1e+20 where ECMAScript prints
// 1e-7 and 100000000000000000000. Rules: compact; object keys sorted by code
// point; no HTML escaping; U+2028/U+2029 literal; control characters escaped as
// JSON.stringify does; numbers in ECMAScript Number::toString form.
func canonicalJSON(v any) (string, error) {
	var b bytes.Buffer
	if err := writeCanonical(&b, v, 0); err != nil {
		return "", err
	}
	return b.String(), nil
}

// maxJSONDepth stops a self-referencing map from recursing forever.
const maxJSONDepth = 1000

var errJSONTooDeep = errors.New("value nests too deeply (a cycle?)")

func writeCanonical(b *bytes.Buffer, v any, depth int) error {
	if depth > maxJSONDepth {
		return errJSONTooDeep
	}
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writeJSONString(b, x)
	case json.Number:
		s, err := numberLiteral(x)
		if err != nil {
			return err
		}
		b.WriteString(s)
	case float64:
		s, err := esNumber(x, 64)
		if err != nil {
			return err
		}
		b.WriteString(s)
	case float32:
		s, err := esNumber(float64(x), 32)
		if err != nil {
			return err
		}
		b.WriteString(s)
	case int:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case int8:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case int16:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case int32:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case uint:
		b.WriteString(strconv.FormatUint(uint64(x), 10))
	case uint8:
		b.WriteString(strconv.FormatUint(uint64(x), 10))
	case uint16:
		b.WriteString(strconv.FormatUint(uint64(x), 10))
	case uint32:
		b.WriteString(strconv.FormatUint(uint64(x), 10))
	case uint64:
		b.WriteString(strconv.FormatUint(x, 10))
	case map[string]any:
		b.WriteByte('{')
		for i, k := range sortedKeys(x) {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSONString(b, k)
			b.WriteByte(':')
			if err := writeCanonical(b, x[k], depth+1); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	case map[string]string:
		b.WriteByte('{')
		for i, k := range sortedKeys(x) {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSONString(b, k)
			b.WriteByte(':')
			writeJSONString(b, x[k])
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeCanonical(b, e, depth+1); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case []string:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSONString(b, e)
		}
		b.WriteByte(']')
	case []map[string]any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeCanonical(b, e, depth+1); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	default:
		// Structs, typed maps and slices, json.Marshaler values: let
		// encoding/json decide what the value means (tags, omitempty, custom
		// marshalers), then re-emit that meaning canonically. UseNumber keeps
		// integers exact.
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var generic any
		if err := dec.Decode(&generic); err != nil {
			return err
		}
		return writeCanonical(b, generic, depth+1)
	}
	return nil
}

// writeJSONString escapes exactly what JSON.stringify escapes: the quote, the
// backslash and C0 controls (\b \f \n \r \t short, the rest \u00xx). Everything
// else — including <, >, &, DEL and U+2028/U+2029 — is written literally.
func writeJSONString(b *bytes.Buffer, s string) {
	const hexDigits = "0123456789abcdef"
	b.WriteByte('"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch c {
			case '"':
				b.WriteString(`\"`)
			case '\\':
				b.WriteString(`\\`)
			case '\b':
				b.WriteString(`\b`)
			case '\f':
				b.WriteString(`\f`)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				if c < 0x20 {
					b.WriteString(`\u00`)
					b.WriteByte(hexDigits[c>>4])
					b.WriteByte(hexDigits[c&0xF])
				} else {
					b.WriteByte(c)
				}
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// A Go string may hold invalid UTF-8; JSON text cannot.
			b.WriteString("�")
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	b.WriteByte('"')
}

// esNumber formats a finite number exactly as ECMAScript Number::toString
// (what JSON.stringify prints): integral values without a fraction, plain
// decimal notation from 1e-6 up to (not including) 1e21, exponent notation
// outside it, and -0 as 0.
func esNumber(f float64, bitSize int) (string, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", fmt.Errorf("%v is not representable in JSON", f)
	}
	if f == 0 {
		return "0", nil
	}
	sign := ""
	if f < 0 {
		sign, f = "-", -f
	}
	// The shortest round-tripping digits, as ECMAScript requires.
	mant, expPart, _ := strings.Cut(strconv.FormatFloat(f, 'e', -1, bitSize), "e")
	digits := strings.TrimRight(strings.Replace(mant, ".", "", 1), "0")
	exp, _ := strconv.Atoi(expPart)
	k := len(digits)
	n := exp + 1 // value = 0.digits × 10^n
	var out string
	switch {
	case k <= n && n <= 21:
		out = digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		out = digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		out = "0." + strings.Repeat("0", -n) + digits
	default:
		e := n - 1
		m := digits
		if k > 1 {
			m = digits[:1] + "." + digits[1:]
		}
		esign := "+"
		if e < 0 {
			esign, e = "-", -e
		}
		out = m + "e" + esign + strconv.Itoa(e)
	}
	return sign + out, nil
}

// numberLiteral canonicalizes a json.Number: integers stay exact at any size,
// everything else goes through the ECMAScript formatter.
func numberLiteral(n json.Number) (string, error) {
	if i, ok := new(big.Int).SetString(string(n), 10); ok {
		return i.String(), nil // also turns -0 into 0
	}
	f, err := strconv.ParseFloat(string(n), 64)
	if err != nil {
		return "", fmt.Errorf("invalid number literal %q", string(n))
	}
	return esNumber(f, 64)
}

// sortedKeys returns a map's keys in code-point order. Go compares strings
// byte-wise, which for UTF-8 is exactly code-point order (unlike JavaScript's
// UTF-16 code-unit sort).
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// pctEncode is the one query-component encoding on the wire: RFC 3986
// unreserved characters (A-Z a-z 0-9 - . _ ~) literal, every other UTF-8 byte
// as %XX with uppercase hex, and space as %20. url.QueryEscape is not it: it
// writes space as '+'.
func pctEncode(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isUnreserved(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0xF])
	}
	return b.String()
}

func isUnreserved(c byte) bool {
	return 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' ||
		c == '-' || c == '.' || c == '_' || c == '~'
}

// encodeQuery joins key/value pairs in order, each side percent-encoded.
func encodeQuery(params []param) string {
	var b strings.Builder
	for i, p := range params {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(pctEncode(p.Key))
		b.WriteByte('=')
		b.WriteString(pctEncode(p.Value))
	}
	return b.String()
}

// errNonFinite rejects numbers a float64 cannot hold (JSON has no NaN or
// Infinity literal; 1e400 would silently become +Inf in some decoders).
var errNonFinite = errors.New("a number is out of the range of a float64")

// unmarshalBody decodes a JSON response body the one way the SDK does
// everywhere (Result.JSON, plugin and job responses): UTF-8 with invalid
// sequences read as U+FFFD (WHATWG), a leading BOM ignored, and numbers
// that overflow a float64 rejected.
func unmarshalBody(body []byte, v any) error {
	body = bytes.TrimPrefix(body, bomUTF8)
	if !utf8.Valid(body) {
		body = []byte(decodeUTF8(body))
	}
	if err := json.Unmarshal(body, v); err != nil {
		return err
	}
	if !numbersFinite(body) {
		return errNonFinite
	}
	return nil
}

// numbersFinite reports whether every number literal in valid JSON text fits
// a float64. Only a literal with an exponent or more than 300 characters can
// overflow, so only those are parsed.
func numbersFinite(data []byte) bool {
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case c == '"':
			for i++; i < len(data) && data[i] != '"'; i++ {
				if data[i] == '\\' {
					i++
				}
			}
		case c == '-' || '0' <= c && c <= '9':
			start, exp := i, false
			for ; i < len(data) && strings.IndexByte("0123456789+-.eE", data[i]) >= 0; i++ {
				exp = exp || data[i] == 'e' || data[i] == 'E'
			}
			if exp || i-start > 300 {
				// Underflow reads as 0, as in JSON.parse; overflow is ±Inf.
				if f, _ := strconv.ParseFloat(string(data[start:i]), 64); math.IsInf(f, 0) {
					return false
				}
			}
			i--
		}
	}
	return true
}
