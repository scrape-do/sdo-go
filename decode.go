// SPDX-License-Identifier: Apache-2.0

package sdo

import (
	"bytes"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

// This file is the WHATWG Encoding Standard "decode" algorithm applied to a
// response body, so .Text() yields the same string a browser (and the Python
// and JS SDKs) would:
//
//  1. a BOM decides the encoding and is consumed, overriding any label;
//  2. the Content-Type charset label, via the WHATWG label table;
//  3. */json and *+json are UTF-8;
//  4. for HTML only, a <meta> charset in the first 1024 bytes (UTF-16 → UTF-8);
//  5. UTF-8.
//
// A label that is unknown or maps to the "replacement" encoding is ignored, so
// a hostile page cannot pick an arbitrary decoder.

const metaSniffBytes = 1024

// metaSniffTypes are the media types whose <meta> is read ("" = no
// Content-Type). Other types are never sniffed: a text/plain body that merely
// contains a meta tag is not HTML.
var metaSniffTypes = []string{"", "text/html", "application/xhtml+xml"}

var (
	bomUTF8    = []byte{0xEF, 0xBB, 0xBF}
	bomUTF16BE = []byte{0xFE, 0xFF}
	bomUTF16LE = []byte{0xFF, 0xFE}
)

// decodeBody returns the text of body per the ladder above.
func decodeBody(body []byte, contentType string) string {
	switch {
	case bytes.HasPrefix(body, bomUTF8):
		return decodeUTF8(body[3:])
	case bytes.HasPrefix(body, bomUTF16BE):
		return decodeUTF16(body[2:], true)
	case bytes.HasPrefix(body, bomUTF16LE):
		return decodeUTF16(body[2:], false)
	}
	mediaType, charset := parseContentType(contentType)
	if name := encodingName(charset); name != "" {
		return decodeAs(name, body)
	}
	if strings.HasSuffix(mediaType, "/json") || strings.HasSuffix(mediaType, "+json") {
		return decodeUTF8(body)
	}
	if slices.Contains(metaSniffTypes, mediaType) {
		head := body
		if len(head) > metaSniffBytes {
			head = head[:metaSniffBytes]
		}
		if name := prescanMeta(head); name != "" {
			return decodeAs(name, body)
		}
	}
	return decodeUTF8(body)
}

// parseContentType returns the lowercased media type and the first charset
// parameter (unquoted and trimmed). It is deliberately lenient: a malformed
// header must not stop the charset from being read.
func parseContentType(ct string) (mediaType, charset string) {
	parts := strings.Split(ct, ";")
	mediaType = strings.ToLower(strings.TrimSpace(parts[0]))
	for _, p := range parts[1:] {
		name, value, ok := strings.Cut(p, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "charset") {
			continue
		}
		// Only double quotes quote a MIME parameter value: a single-quoted
		// value keeps its quotes and is then no label (WHATWG). An
		// unterminated double quote runs to the end.
		value = strings.TrimSpace(value)
		if rest, ok := strings.CutPrefix(value, `"`); ok {
			value, _, _ = strings.Cut(rest, `"`)
		}
		return mediaType, strings.TrimSpace(value)
	}
	return mediaType, ""
}

// encodingName resolves a label through the WHATWG table and returns the
// canonical encoding name, or "" when the label is unknown or maps to the
// "replacement" encoding (both are ignored and the ladder continues).
func encodingName(label string) string {
	if label == "" {
		return ""
	}
	for i := 0; i < len(label); i++ {
		if label[i] >= utf8.RuneSelf {
			return "" // labels match ASCII case-insensitively only (no Kelvin sign for k)
		}
	}
	enc, err := htmlindex.Get(label)
	if err != nil {
		return ""
	}
	name, err := htmlindex.Name(enc)
	if err != nil || name == "replacement" {
		return ""
	}
	return name
}

// decodeAs decodes body with a canonical WHATWG encoding name. UTF-8, UTF-16
// and the single-byte encodings are decoded here because x/text departs from
// the standard for them (replacement granularity, and unassigned C1 bytes).
func decodeAs(name string, body []byte) string {
	switch name {
	case "utf-8":
		return decodeUTF8(body)
	case "utf-16le":
		return decodeUTF16(body, false)
	case "utf-16be":
		return decodeUTF16(body, true)
	case "gbk", "gb18030":
		// WHATWG decodes GBK with the gb18030 decoder.
		return decodeGB18030(body)
	case "big5":
		return decodeBig5(body)
	case "shift_jis":
		return decodeShiftJIS(body)
	case "iso-8859-8-i":
		// Same index as iso-8859-8; x/text wraps it, hiding the table.
		name = "iso-8859-8"
	}
	enc, err := htmlindex.Get(name)
	if err != nil {
		return decodeUTF8(body)
	}
	if cm, ok := enc.(*charmap.Charmap); ok {
		return decodeSingleByte(cm, body)
	}
	out, err := enc.NewDecoder().Bytes(body)
	if err != nil {
		return decodeUTF8(body)
	}
	return decodeUTF8(out)
}

// decodeUTF8 is the WHATWG UTF-8 decoder: every invalid sequence becomes one
// U+FFFD per "maximal subpart". A truncated but otherwise valid prefix (F0 9F 98)
// is one replacement; an overlong or surrogate encoding is one per byte.
// strings.ToValidUTF8 (one per run) and a plain utf8.DecodeRune loop (one per
// byte, always) both get this wrong.
func decodeUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var out strings.Builder
	out.Grow(len(b) + 8)
	var (
		needed, seen int
		cp           rune
		lower, upper byte = 0x80, 0xBF
	)
	for i := 0; i < len(b); {
		c := b[i]
		if needed == 0 {
			i++
			switch {
			case c <= 0x7F:
				out.WriteByte(c)
			case c >= 0xC2 && c <= 0xDF:
				needed, cp = 1, rune(c&0x1F)
			case c >= 0xE0 && c <= 0xEF:
				if c == 0xE0 {
					lower = 0xA0
				} else if c == 0xED {
					upper = 0x9F
				}
				needed, cp = 2, rune(c&0x0F)
			case c >= 0xF0 && c <= 0xF4:
				if c == 0xF0 {
					lower = 0x90
				} else if c == 0xF4 {
					upper = 0x8F
				}
				needed, cp = 3, rune(c&0x07)
			default:
				out.WriteRune(utf8.RuneError)
			}
			continue
		}
		if c < lower || c > upper {
			// The sequence so far is one maximal subpart; c is reprocessed.
			needed, seen, cp, lower, upper = 0, 0, 0, 0x80, 0xBF
			out.WriteRune(utf8.RuneError)
			continue
		}
		i++
		lower, upper = 0x80, 0xBF
		cp = cp<<6 | rune(c&0x3F)
		seen++
		if seen == needed {
			out.WriteRune(cp)
			needed, seen, cp = 0, 0, 0
		}
	}
	if needed != 0 {
		out.WriteRune(utf8.RuneError)
	}
	return out.String()
}

// decodeUTF16 is the WHATWG UTF-16 decoder: a lone surrogate or a trailing odd
// byte is U+FFFD.
func decodeUTF16(b []byte, bigEndian bool) string {
	var out strings.Builder
	out.Grow(len(b))
	var lead rune = -1 // pending lead surrogate
	for i := 0; i+1 < len(b); i += 2 {
		var u rune
		if bigEndian {
			u = rune(b[i])<<8 | rune(b[i+1])
		} else {
			u = rune(b[i+1])<<8 | rune(b[i])
		}
		if lead >= 0 {
			if u >= 0xDC00 && u <= 0xDFFF {
				out.WriteRune(0x10000 + (lead-0xD800)<<10 + (u - 0xDC00))
				lead = -1
				continue
			}
			out.WriteRune(utf8.RuneError)
			lead = -1
		}
		switch {
		case u >= 0xD800 && u <= 0xDBFF:
			lead = u
		case u >= 0xDC00 && u <= 0xDFFF:
			out.WriteRune(utf8.RuneError)
		default:
			out.WriteRune(u)
		}
	}
	if lead >= 0 || len(b)%2 == 1 {
		out.WriteRune(utf8.RuneError)
	}
	return out.String()
}

// decodeSingleByte decodes a single-byte encoding through its 256-entry table.
// Every WHATWG single-byte index maps an unassigned byte in 0x80–0x9F to the
// C1 code point of the same value (windows-1252's 0x81 is U+0081), where x/text
// yields U+FFFD; unassigned bytes from 0xA0 up stay U+FFFD.
func decodeSingleByte(cm *charmap.Charmap, b []byte) string {
	var table [256]rune
	for i := range table {
		r := cm.DecodeByte(byte(i))
		if r == utf8.RuneError && i >= 0x80 && i <= 0x9F {
			r = rune(i)
		}
		table[i] = r
	}
	var out strings.Builder
	out.Grow(len(b) + len(b)/2)
	for _, c := range b {
		out.WriteRune(table[c])
	}
	return out.String()
}

// prescanMeta is a compact form of the HTML standard's "prescan a byte stream
// to determine its encoding": it walks tags (skipping comments), and for each
// <meta> reads charset, or http-equiv="content-type" plus content. It returns a
// canonical encoding name, or "".
func prescanMeta(b []byte) string {
	for i := 0; i < len(b); {
		rest := b[i:]
		switch {
		case bytes.HasPrefix(rest, []byte("<!--")):
			end := bytes.Index(rest[4:], []byte("-->"))
			if end < 0 {
				return ""
			}
			i += 4 + end + 3
		case len(rest) >= 6 && strings.EqualFold(string(rest[:5]), "<meta") && isSpaceOrSlash(rest[5]):
			name, next := metaCharset(b, i+6)
			if name != "" {
				return name
			}
			i = next
		case len(rest) >= 2 && rest[0] == '<' && (isASCIILetter(rest[1]) ||
			(rest[1] == '/' && len(rest) >= 3 && isASCIILetter(rest[2]))):
			// Any other tag: step over its attributes so a quoted '>' or a
			// "<meta" inside an attribute value is not misread.
			j := i + 1
			for j < len(b) && !isSpaceOrSlash(b[j]) && b[j] != '>' {
				j++
			}
			for {
				_, _, next, ok := nextAttribute(b, j)
				j = next
				if !ok {
					break
				}
			}
			i = j + 1
		case len(rest) >= 2 && rest[0] == '<' && (rest[1] == '!' || rest[1] == '/' || rest[1] == '?'):
			end := bytes.IndexByte(rest, '>')
			if end < 0 {
				return ""
			}
			i += end + 1
		default:
			i++
		}
	}
	return ""
}

// metaCharset reads one <meta> tag's attributes starting at pos and returns
// the resolved encoding name (or "") and the position after the tag.
func metaCharset(b []byte, pos int) (string, int) {
	var (
		seen                  = map[string]bool{}
		gotPragma, hasCharset bool
		label                 string
		needPragma            = -1 // -1 unset, 0 false, 1 true
	)
	for {
		name, value, next, ok := nextAttribute(b, pos)
		pos = next
		if !ok {
			break
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		switch name {
		case "http-equiv":
			if value == "content-type" {
				gotPragma = true
			}
		case "content":
			if !hasCharset {
				if cs := charsetFromMetaContent(value); cs != "" {
					label, needPragma, hasCharset = cs, 1, true
				}
			}
		case "charset":
			label, needPragma, hasCharset = value, 0, true
		}
	}
	// A tag cut off before its '>' (at the end of the sniff window) is not a
	// declaration.
	if pos >= len(b) || needPragma == -1 || (needPragma == 1 && !gotPragma) {
		return "", pos + 1
	}
	enc := encodingName(label)
	switch enc {
	case "utf-16le", "utf-16be":
		enc = "utf-8" // a page cannot really be UTF-16 if ASCII meta bytes were readable
	case "x-user-defined":
		enc = "windows-1252"
	}
	return enc, pos + 1
}

// nextAttribute is the HTML standard's "get an attribute" over raw bytes.
// Names and values are ASCII-lowercased. ok is false at '>' or end of input,
// with next pointing at the '>'. Every successful call consumes input.
func nextAttribute(b []byte, pos int) (name, value string, next int, ok bool) {
	for pos < len(b) && isSpaceOrSlash(b[pos]) {
		pos++
	}
	if pos >= len(b) || b[pos] == '>' {
		return "", "", pos, false
	}
	var n []byte
	for pos < len(b) {
		c := b[pos]
		switch {
		case c == '=' && len(n) > 0:
			v, next := attributeValue(b, pos+1)
			return string(n), v, next, true
		case isHTMLSpace(c):
			for pos < len(b) && isHTMLSpace(b[pos]) {
				pos++
			}
			if pos < len(b) && b[pos] == '=' {
				v, next := attributeValue(b, pos+1)
				return string(n), v, next, true
			}
			return string(n), "", pos, true
		case c == '/' || c == '>':
			return string(n), "", pos, true
		}
		n = append(n, toLowerASCII(c))
		pos++
	}
	return string(n), "", pos, true
}

// attributeValue reads a quoted or unquoted attribute value starting at pos.
func attributeValue(b []byte, pos int) (string, int) {
	for pos < len(b) && isHTMLSpace(b[pos]) {
		pos++
	}
	var v []byte
	if pos < len(b) && (b[pos] == '"' || b[pos] == '\'') {
		q := b[pos]
		for pos++; pos < len(b) && b[pos] != q; pos++ {
			v = append(v, toLowerASCII(b[pos]))
		}
		if pos < len(b) {
			pos++ // the closing quote
		}
		return string(v), pos
	}
	for pos < len(b) && !isHTMLSpace(b[pos]) && b[pos] != '>' {
		v = append(v, toLowerASCII(b[pos]))
		pos++
	}
	return string(v), pos
}

// charsetFromMetaContent is the HTML standard's "extract a character encoding
// from a meta element" for a content attribute such as
// "text/html; charset=windows-1252".
func charsetFromMetaContent(s string) string {
	for {
		i := strings.Index(s, "charset")
		if i < 0 {
			return ""
		}
		s = s[i+len("charset"):]
		t := strings.TrimLeft(s, "\t\n\f\r ")
		if !strings.HasPrefix(t, "=") {
			continue
		}
		t = strings.TrimLeft(t[1:], "\t\n\f\r ")
		if t == "" {
			return ""
		}
		if q := t[0]; q == '"' || q == '\'' {
			end := strings.IndexByte(t[1:], q)
			if end < 0 {
				return ""
			}
			return t[1 : 1+end]
		}
		end := strings.IndexAny(t, "\t\n\f\r ;")
		if end < 0 {
			return t
		}
		return t[:end]
	}
}

func isHTMLSpace(c byte) bool {
	return c == '\t' || c == '\n' || c == '\f' || c == '\r' || c == ' '
}

func isSpaceOrSlash(c byte) bool { return isHTMLSpace(c) || c == '/' }

func isASCIILetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

func toLowerASCII(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

// decodeGB18030 is the WHATWG gb18030 decoder, which also decodes GBK. x/text's
// decoder departs from it: it maps the index's private-use pointers (0xADE6
// is U+E15F) to U+FFFD, four-byte pointer 7457 to U+1E3F instead of U+E7C7,
// and recovers from an invalid four-byte sequence byte by byte.
func decodeGB18030(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c < 0x80:
			sb.WriteByte(c)
			i++
		case c == 0x80:
			sb.WriteRune('€')
			i++
		case c == 0xFF:
			sb.WriteRune(utf8.RuneError)
			i++
		case i+1 == len(b):
			sb.WriteRune(utf8.RuneError) // a lead byte at the end
			i++
		case '0' <= b[i+1] && b[i+1] <= '9':
			r, n := gb18030FourByte(b[i:])
			sb.WriteRune(r)
			i += n
		default:
			r, n := gb18030TwoByte(c, b[i+1])
			sb.WriteRune(r)
			i += n
		}
	}
	return sb.String()
}

// gb18030FourByte decodes a lead byte followed by a digit. n is how many bytes
// it consumed; after an invalid byte only the lead is, and the rest is decoded
// again. A sequence cut off by the end of the input is one U+FFFD.
func gb18030FourByte(b []byte) (r rune, n int) {
	if len(b) < 3 {
		return utf8.RuneError, len(b)
	}
	if b[2] < 0x81 || b[2] > 0xFE {
		return utf8.RuneError, 1
	}
	if len(b) < 4 {
		return utf8.RuneError, len(b)
	}
	if b[3] < '0' || b[3] > '9' {
		return utf8.RuneError, 1
	}
	p := (int(b[0])-0x81)*12600 + (int(b[1])-'0')*1260 + (int(b[2])-0x81)*10 + int(b[3]) - '0'
	return gb18030RangesCodePoint(p), 4
}

// gb18030RangesCodePoint is the WHATWG "index gb18030 ranges code point".
func gb18030RangesCodePoint(p int) rune {
	switch {
	case (p > 39419 && p < 189000) || p > 1237575:
		return utf8.RuneError
	case p == 7457:
		return 0xE7C7
	case p >= 189000:
		return rune(0x10000 + p - 189000)
	}
	k := sort.Search(len(gb18030Ranges), func(i int) bool { return int(gb18030Ranges[i].pointer) > p }) - 1
	return rune(int(gb18030Ranges[k].cp) + p - int(gb18030Ranges[k].pointer))
}

// gb18030TwoByte decodes a two-byte sequence. An invalid one is one U+FFFD; an
// ASCII trail byte is then decoded again.
func gb18030TwoByte(lead, trail byte) (rune, int) {
	if 0x40 <= trail && trail <= 0x7E || 0x80 <= trail && trail <= 0xFE {
		offset := 0x40
		if trail >= 0x7F {
			offset = 0x41
		}
		if r := gb18030Index((int(lead)-0x81)*190 + int(trail) - offset); r != 0 {
			return r, 2
		}
	}
	if trail < 0x80 {
		return utf8.RuneError, 1
	}
	return utf8.RuneError, 2
}

// pointerTable is a WHATWG index (pointer → code point, 0 for none), built on
// first use by decoding every two-byte sequence once through x/text, whose
// tables match the WHATWG indexes for mapped pairs; x/text's departures are in
// how it recovers from invalid input, which the decoders here do themselves,
// and in the ranges patched after building (gb18030PUA).
type pointerTable struct {
	once  sync.Once
	table []rune
	enc   encoding.Encoding
	size  int
	pair  func(pointer int) (lead, trail byte)
	patch func(t []rune)
}

func (pt *pointerTable) at(pointer int) rune {
	pt.once.Do(func() {
		t := make([]rune, pt.size)
		dec := pt.enc.NewDecoder()
		for p := range t {
			lead, trail := pt.pair(p)
			out, err := dec.Bytes([]byte{lead, trail})
			if r, size := utf8.DecodeRune(out); err == nil && size == len(out) && r != utf8.RuneError {
				t[p] = r
			}
		}
		if pt.patch != nil {
			pt.patch(t)
		}
		pt.table = t
	})
	if pointer < 0 || pointer >= len(pt.table) {
		return 0
	}
	return pt.table[pointer]
}

// gb18030Table is the WHATWG "index gb18030".
var gb18030Table = &pointerTable{
	enc:  simplifiedchinese.GB18030,
	size: 23940,
	pair: func(p int) (byte, byte) {
		trail := p%190 + 0x40
		if trail >= 0x7F {
			trail++
		}
		return byte(p/190 + 0x81), byte(trail)
	},
	patch: func(t []rune) {
		for _, run := range gb18030PUA {
			for k := range run.n {
				t[int(run.pointer)+k] = rune(run.cp) + rune(k)
			}
		}
	},
}

func gb18030Index(pointer int) rune { return gb18030Table.at(pointer) }

// gb18030PUA lists the WHATWG index entries x/text lacks, as runs of
// consecutive pointers mapping to consecutive code points.
var gb18030PUA = []struct {
	pointer, cp uint16
	n           int
}{
	{6080, 0xE4C6, 96}, {6270, 0xE526, 96}, {6376, 0xE766, 6}, {6433, 0xE76D, 1}, {6444, 0xE76E, 2},
	{6458, 0xE770, 2}, {6460, 0xE586, 95}, {6650, 0xE5E6, 96}, {6829, 0xE772, 11}, {6840, 0xE646, 96},
	{7022, 0xE77D, 8}, {7030, 0xE6A6, 96}, {7150, 0xE785, 8}, {7182, 0xFE10, 1}, {7183, 0xFE12, 1},
	{7184, 0xFE11, 1}, {7185, 0xFE13, 4}, {7201, 0xFE17, 2}, {7208, 0xFE19, 1}, {7211, 0xE797, 9},
	{7220, 0xE706, 96}, {7349, 0xE7A0, 15}, {7397, 0xE7AF, 13}, {7495, 0xE7BC, 11}, {7533, 0x1E3F, 1},
	{7538, 0xE7C9, 4}, {7579, 0xE7CD, 21}, {7624, 0xE7E2, 1}, {7627, 0xE7E3, 1}, {7629, 0xE7E4, 3},
	{7686, 0xE7F4, 13}, {7775, 0xE801, 15}, {7886, 0xE000, 94}, {8076, 0xE05E, 94},
	{8266, 0xE0BC, 94}, {8456, 0xE11A, 94}, {8646, 0xE178, 94}, {8836, 0xE1D6, 94},
	{16525, 0xE810, 5}, {22706, 0xE234, 94}, {22896, 0xE292, 94}, {23086, 0xE2F0, 94},
	{23276, 0xE34E, 94}, {23466, 0xE3AC, 94}, {23656, 0xE40A, 94}, {23767, 0xE816, 3},
	{23775, 0x9FB4, 1}, {23783, 0x9FB5, 1}, {23788, 0x9FB6, 2}, {23794, 0xE831, 1},
	{23795, 0x9FB8, 1}, {23804, 0xE83B, 1}, {23812, 0x9FB9, 1}, {23829, 0x9FBA, 1},
	{23830, 0xE855, 1}, {23845, 0x9FBB, 1}, {23846, 0xE468, 94},
}

// gb18030Ranges is the WHATWG "index gb18030 ranges" below pointer 189000
// (from there, code point = 0x10000 + pointer - 189000).
var gb18030Ranges = []struct{ pointer, cp uint16 }{
	{0, 0x0080}, {36, 0x00A5}, {38, 0x00A9}, {45, 0x00B2}, {50, 0x00B8}, {81, 0x00D8}, {89, 0x00E2},
	{95, 0x00EB}, {96, 0x00EE}, {100, 0x00F4}, {103, 0x00F8}, {104, 0x00FB}, {105, 0x00FD},
	{109, 0x0102}, {126, 0x0114}, {133, 0x011C}, {148, 0x012C}, {172, 0x0145}, {175, 0x0149},
	{179, 0x014E}, {208, 0x016C}, {306, 0x01CF}, {307, 0x01D1}, {308, 0x01D3}, {309, 0x01D5},
	{310, 0x01D7}, {311, 0x01D9}, {312, 0x01DB}, {313, 0x01DD}, {341, 0x01FA}, {428, 0x0252},
	{443, 0x0262}, {544, 0x02C8}, {545, 0x02CC}, {558, 0x02DA}, {741, 0x03A2}, {742, 0x03AA},
	{749, 0x03C2}, {750, 0x03CA}, {805, 0x0402}, {819, 0x0450}, {820, 0x0452}, {7922, 0x2011},
	{7924, 0x2017}, {7925, 0x201A}, {7927, 0x201E}, {7934, 0x2027}, {7943, 0x2031}, {7944, 0x2034},
	{7945, 0x2036}, {7950, 0x203C}, {8062, 0x20AD}, {8148, 0x2104}, {8149, 0x2106}, {8152, 0x210A},
	{8164, 0x2117}, {8174, 0x2122}, {8236, 0x216C}, {8240, 0x217A}, {8262, 0x2194}, {8264, 0x219A},
	{8374, 0x2209}, {8380, 0x2210}, {8381, 0x2212}, {8384, 0x2216}, {8388, 0x221B}, {8390, 0x2221},
	{8392, 0x2224}, {8393, 0x2226}, {8394, 0x222C}, {8396, 0x222F}, {8401, 0x2238}, {8406, 0x223E},
	{8416, 0x2249}, {8419, 0x224D}, {8424, 0x2253}, {8437, 0x2262}, {8439, 0x2268}, {8445, 0x2270},
	{8482, 0x2296}, {8485, 0x229A}, {8496, 0x22A6}, {8521, 0x22C0}, {8603, 0x2313}, {8936, 0x246A},
	{8946, 0x249C}, {9046, 0x254C}, {9050, 0x2574}, {9063, 0x2590}, {9066, 0x2596}, {9076, 0x25A2},
	{9092, 0x25B4}, {9100, 0x25BE}, {9108, 0x25C8}, {9111, 0x25CC}, {9113, 0x25D0}, {9131, 0x25E6},
	{9162, 0x2607}, {9164, 0x260A}, {9218, 0x2641}, {9219, 0x2643}, {11329, 0x2E82}, {11331, 0x2E85},
	{11334, 0x2E89}, {11336, 0x2E8D}, {11346, 0x2E98}, {11361, 0x2EA8}, {11363, 0x2EAB},
	{11366, 0x2EAF}, {11370, 0x2EB4}, {11372, 0x2EB8}, {11375, 0x2EBC}, {11389, 0x2ECB},
	{11682, 0x2FFC}, {11686, 0x3004}, {11687, 0x3018}, {11692, 0x301F}, {11694, 0x302A},
	{11714, 0x303F}, {11716, 0x3094}, {11723, 0x309F}, {11725, 0x30F7}, {11730, 0x30FF},
	{11736, 0x312A}, {11982, 0x322A}, {11989, 0x3232}, {12102, 0x32A4}, {12336, 0x3390},
	{12348, 0x339F}, {12350, 0x33A2}, {12384, 0x33C5}, {12393, 0x33CF}, {12395, 0x33D3},
	{12397, 0x33D6}, {12510, 0x3448}, {12553, 0x3474}, {12851, 0x359F}, {12962, 0x360F},
	{12973, 0x361B}, {13738, 0x3919}, {13823, 0x396F}, {13919, 0x39D1}, {13933, 0x39E0},
	{14080, 0x3A74}, {14298, 0x3B4F}, {14585, 0x3C6F}, {14698, 0x3CE1}, {15583, 0x4057},
	{15847, 0x4160}, {16318, 0x4338}, {16434, 0x43AD}, {16438, 0x43B2}, {16481, 0x43DE},
	{16729, 0x44D7}, {17102, 0x464D}, {17122, 0x4662}, {17315, 0x4724}, {17320, 0x472A},
	{17402, 0x477D}, {17418, 0x478E}, {17859, 0x4948}, {17909, 0x497B}, {17911, 0x497E},
	{17915, 0x4984}, {17916, 0x4987}, {17936, 0x499C}, {17939, 0x49A0}, {17961, 0x49B8},
	{18664, 0x4C78}, {18703, 0x4CA4}, {18814, 0x4D1A}, {18962, 0x4DAF}, {19043, 0x9FA6},
	{33469, 0xE76C}, {33470, 0xE7C8}, {33471, 0xE7E7}, {33484, 0xE815}, {33485, 0xE819},
	{33490, 0xE81F}, {33497, 0xE827}, {33501, 0xE82D}, {33505, 0xE833}, {33513, 0xE83C},
	{33520, 0xE844}, {33536, 0xE856}, {33550, 0xE865}, {37845, 0xF92D}, {37921, 0xF97A},
	{37948, 0xF996}, {38029, 0xF9E8}, {38038, 0xF9F2}, {38064, 0xFA10}, {38065, 0xFA12},
	{38066, 0xFA15}, {38069, 0xFA19}, {38075, 0xFA22}, {38076, 0xFA25}, {38078, 0xFA2A},
	{39108, 0xFE32}, {39109, 0xFE45}, {39113, 0xFE53}, {39114, 0xFE58}, {39115, 0xFE67},
	{39116, 0xFE6C}, {39265, 0xFF5F}, {39394, 0xFFE6},
}

// big5Table is the WHATWG "index Big5" (HKSCS included).
var big5Table = &pointerTable{
	enc:  traditionalchinese.Big5,
	size: 126 * 157,
	pair: func(p int) (byte, byte) {
		trail := p%157 + 0x40
		if trail > 0x7E {
			trail += 0x22 // 0xA1 onwards
		}
		return byte(p/157 + 0x81), byte(trail)
	},
}

// big5Pairs are the pointers that decode to two code points.
var big5Pairs = map[int]string{1133: "Ê̄", 1135: "Ê̌", 1164: "ê̄", 1166: "ê̌"}

// decodeBig5 is the WHATWG Big5 decoder. x/text's table is the WHATWG index,
// but after an unmapped pair it drops an ASCII trail byte that WHATWG decodes
// again.
func decodeBig5(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c < 0x80:
			sb.WriteByte(c)
			i++
		case c == 0x80 || c == 0xFF || i+1 == len(b):
			sb.WriteRune(utf8.RuneError) // not a lead, or a lead at the end
			i++
		default:
			trail, p := b[i+1], -1
			if 0x40 <= trail && trail <= 0x7E {
				p = (int(c)-0x81)*157 + int(trail) - 0x40
			} else if 0xA1 <= trail && trail <= 0xFE {
				p = (int(c)-0x81)*157 + int(trail) - 0x62
			}
			if pair, ok := big5Pairs[p]; ok {
				sb.WriteString(pair)
				i += 2
			} else if r := big5Table.at(p); r != 0 {
				sb.WriteRune(r)
				i += 2
			} else {
				sb.WriteRune(utf8.RuneError)
				i += trailStep(trail)
			}
		}
	}
	return sb.String()
}

// shiftJISTable is the WHATWG "index jis0208" by Shift_JIS pointer.
var shiftJISTable = &pointerTable{
	enc:  japanese.ShiftJIS,
	size: 60 * 188,
	pair: func(p int) (byte, byte) {
		lead := p/188 + 0x81
		if lead > 0x9F {
			lead += 0x40 // 0xE0 onwards
		}
		trail := p%188 + 0x40
		if trail >= 0x7F {
			trail++
		}
		return byte(lead), byte(trail)
	},
}

// decodeShiftJIS is the WHATWG Shift_JIS decoder. x/text's table is the
// WHATWG index, but it lacks the user-defined area (pointers 8836-10715 are
// U+E000 onwards) and drops an ASCII trail byte after an unmapped pair.
func decodeShiftJIS(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c <= 0x80:
			sb.WriteRune(rune(c))
			i++
		case 0xA1 <= c && c <= 0xDF:
			sb.WriteRune(0xFF61 - 0xA1 + rune(c))
			i++
		case !(c <= 0x9F || 0xE0 <= c && c <= 0xFC) || i+1 == len(b):
			sb.WriteRune(utf8.RuneError) // not a lead, or a lead at the end
			i++
		default:
			trail, p := b[i+1], -1
			leadOffset := 0x81
			if c >= 0xA0 {
				leadOffset = 0xC1
			}
			if 0x40 <= trail && trail <= 0x7E {
				p = (int(c)-leadOffset)*188 + int(trail) - 0x40
			} else if 0x80 <= trail && trail <= 0xFC {
				p = (int(c)-leadOffset)*188 + int(trail) - 0x41
			}
			if 8836 <= p && p <= 10715 {
				sb.WriteRune(0xE000 - 8836 + rune(p))
				i += 2
			} else if r := shiftJISTable.at(p); r != 0 {
				sb.WriteRune(r)
				i += 2
			} else {
				sb.WriteRune(utf8.RuneError)
				i += trailStep(trail)
			}
		}
	}
	return sb.String()
}

// trailStep is how many bytes an unmapped pair consumes: an ASCII trail byte
// is decoded again.
func trailStep(trail byte) int {
	if trail < 0x80 {
		return 1
	}
	return 2
}
