// SPDX-License-Identifier: Apache-2.0

package sdo

import (
	"encoding/hex"
	"testing"
)

// The <meta> prescan follows the HTML standard's tokenization closely enough
// that markup around a charset declaration cannot fake or hide one.
func TestPrescanMeta(t *testing.T) {
	cases := map[string]string{
		`<meta charset="iso-8859-1">`:                                          "windows-1252",
		`<META CHARSET=Shift_JIS>`:                                             "shift_jis",
		`<meta/charset='koi8-r'/>`:                                             "koi8-r",
		`<meta charset = "koi8-r" >`:                                           "koi8-r",
		`<meta charset=utf-16be>`:                                              "utf-8",
		`<meta charset="bogus"><meta charset="koi8-r">`:                        "koi8-r",
		`<meta charset="iso-2022-kr"><meta charset="koi8-r">`:                  "koi8-r",
		`<meta http-equiv="content-type" content="text/html;charset=koi8-r">`:  "koi8-r",
		`<meta content='text/html; charset="koi8-r"' http-equiv=Content-Type>`: "koi8-r",
		`<meta http-equiv=content-type content="charset = koi8-r;x">`:          "koi8-r",
		`<meta http-equiv=content-type content="charsetx charset=koi8-r">`:     "koi8-r",
		`<meta http-equiv=content-type content="no charset here">`:             "",
		`<meta http-equiv=content-type content="charset=">`:                    "",
		`<meta http-equiv=content-type content="charset='koi8-r">`:             "",
		`<meta http-equiv=content-type content="charset">`:                     "",
		`<meta charset="koi8-r" charset="utf-8">`:                              "koi8-r",
		`<meta name=x>`:            "",
		`<meta ch>`:                "",
		`<meta =x charset=koi8-r>`: "koi8-r",
		`<meta charset`:            "",
		`<meta charset="koi8-r`:    "",
		`<a title='<meta charset=koi8-r>'><meta charset=iso-8859-1>`: "windows-1252",
		`<br/><meta charset=koi8-r>`:                                 "koi8-r",
		`</p><meta charset=koi8-r>`:                                  "koi8-r",
		`<!doctype html><?xml x?><meta charset=koi8-r>`:              "koi8-r",
		`<!-- <meta charset=koi8-r> -->`:                             "",
		`<!-- unterminated <meta charset=koi8-r>`:                    "",
		`<!doctype html`:                                             "",
		`<metadata charset=koi8-r>`:                                  "",
		`< meta charset=koi8-r>`:                                     "",
		`<p class=a`:                                                 "",
	}
	for in, want := range cases {
		if got := prescanMeta([]byte(in)); got != want {
			t.Errorf("prescanMeta(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseContentType(t *testing.T) {
	cases := []struct{ in, mt, cs string }{
		{"", "", ""},
		{"Text/HTML", "text/html", ""},
		{`text/html; foo; charset = "koi8-r" `, "text/html", "koi8-r"},
		{"text/html; charset='x'", "text/html", "'x'"}, // single quotes do not quote
		{`text/html; charset="`, "text/html", ""},
		{`text/html; charset="koi8-r`, "text/html", "koi8-r"}, // runs to the end
		{`text/html; charset=a; charset=b`, "text/html", "a"}, // the first wins
		{"text/html; name=v", "text/html", ""},
	}
	for _, c := range cases {
		if mt, cs := parseContentType(c.in); mt != c.mt || cs != c.cs {
			t.Errorf("parseContentType(%q) = %q, %q; want %q, %q", c.in, mt, cs, c.mt, c.cs)
		}
	}
}

// One U+FFFD per maximal subpart, across every lead-byte class.
func TestDecodeUTF8MaximalSubparts(t *testing.T) {
	cases := map[string]string{
		"\xc2":             "�",
		"\xc2A":            "�A",
		"\xc0\x80":         "��",
		"\xf4\x90\x80\x80": "����",
		"\xf4\x8f\xbf\xbf": "\U0010FFFF",
		"\xf5":             "�",
		"\xe2\x82\xac":     "€",
		"\xe2\x28\xa1":     "�(�",
		"\x80\x80":         "��",
	}
	for in, want := range cases {
		if got := decodeUTF8([]byte(in)); got != want {
			t.Errorf("decodeUTF8(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeAsFallbacks(t *testing.T) {
	// A name htmlindex does not know (not reachable from a label) decodes as
	// UTF-8 rather than failing.
	if got := decodeAs("no-such-encoding", []byte("h\xc3\xa9")); got != "hé" {
		t.Errorf("unknown name: %q", got)
	}
	// windows-1252 keeps the five bytes x/text leaves unmapped.
	if got := decodeBody([]byte{0x80, 0x81, 0x8d, 0x8f, 0x90, 0x9d, 0xe9}, "text/plain; charset=windows-1252"); got != "€\u0081\u008d\u008f\u0090\u009dé" {
		t.Errorf("windows-1252: %q", got)
	}
	// GBK is decoded with the gb18030 decoder, so four-byte sequences work.
	if got := decodeBody([]byte{0x81, 0x30, 0x81, 0x30}, "text/plain; charset=gbk"); got != "\u0080" {
		t.Errorf("gbk four-byte: %q", got)
	}
}

func TestLabelledUTF16AndMixedUTF8(t *testing.T) {
	if got := decodeBody([]byte{0x00, 'h', 0x00, 'i'}, "text/plain; charset=utf-16be"); got != "hi" {
		t.Errorf("utf-16be label: %q", got)
	}
	// A valid multi-byte character after an invalid byte decodes normally.
	if got := decodeUTF8([]byte("\xff\xe2\x82\xac\xf0\x9f\x98\x80")); got != "�€😀" {
		t.Errorf("mixed: %q", got)
	}
}

// GBK and gb18030 decode per the WHATWG index, private-use entries included,
// with WHATWG error recovery.
func TestGB18030IsWHATWG(t *testing.T) {
	for _, tc := range []struct{ hex, want string }{
		{"ade6", "\ue15f"},             // private-use pointer x/text lacks
		{"a6d9", "\ufe10"},             // GB18030-2022 vertical form
		{"fefe", "\ue4c5"},             // last two-byte pointer
		{"80", "\u20ac"},               // single-byte euro
		{"ff", "\ufffd"},               // never a lead
		{"8135f437", "\ue7c7"},         // four-byte pointer 7457
		{"81308130", "\u0080"},         // first four-byte pointer
		{"84318730", "\ufe70"},         // last BMP range
		{"8431a530", "\ufffd"},         // beyond the BMP ranges
		{"fe39fe39", "\ufffd"},         // beyond every range
		{"9030813061", "\U00010000a"},  // supplementary plane
		{"e3329a35", "\U0010ffff"},     // last pointer
		{"e3329a36", "\ufffd"},         // past it
		{"6bf93321", "k\ufffd3!"},      // invalid fourth byte: only the lead is dropped
		{"8130813a", "\ufffd0\ufffd:"}, // a fourth byte that is no digit: only the lead is dropped
		{"8130ff", "\ufffd0\ufffd"},    // third byte out of range
		{"81308131ff", "\u0081\ufffd"},
		{"813081", "\ufffd"},         // cut off at the end: one U+FFFD
		{"8130", "\ufffd"},           // cut off after the digit
		{"81", "\ufffd"},             // lone lead at the end
		{"8140c4e3", "\u4e02\u4f60"}, // ordinary two-byte
		{"817f41", "\ufffd\u007fA"},  // 0x7F is no trail: re-read as ASCII
		{"81ff41", "\ufffdA"},        // a non-ASCII bad trail is consumed
		{"a1a1", "\u3000"},
		{"d2bb", "\u4e00"},
	} {
		// Expectations computed from the WHATWG index files (2024-09-18).
		body, _ := hex.DecodeString(tc.hex)
		for _, label := range []string{"gbk", "gb18030", "GB2312"} {
			if got := decodeBody(body, "text/plain; charset="+label); got != tc.want {
				t.Errorf("%s %s = %+q, want %+q", label, tc.hex, got, tc.want)
			}
		}
	}
}

// A label matches ASCII case-insensitively only: a Kelvin sign is not a k.
func TestNonASCIILabelIsIgnored(t *testing.T) {
	if got := decodeBody([]byte{0xe9}, "text/html; charset=Koi8-r"); got != "�" {
		t.Errorf("Kelvin-sign label decoded as %q, want UTF-8 (U+FFFD)", got)
	}
	if got := decodeBody([]byte("<meta charset=\"Koi8-r\">\xe9"), ""); got[len(got)-3:] != "�" {
		t.Errorf("Kelvin-sign meta label decoded as %q", got)
	}
}

// Big5 and Shift_JIS decode per WHATWG: the user-defined area, the two-code-
// point Big5 pointers, and an ASCII trail byte after an unmapped pair decoded
// again. Expectations are from the WHATWG index files.
func TestBig5AndShiftJISAreWHATWG(t *testing.T) {
	for _, tc := range []struct{ label, hex, want string }{
		{"big5", "8141", "�A"},
		{"big5", "81a1", "�"},
		{"big5", "8862", "Ê̄"},
		{"big5", "8864", "Ê̌"},
		{"big5", "88a3", "ê̄"},
		{"big5", "88a5", "ê̌"},
		{"big5", "a440", "一"},
		{"big5", "80ff", "��"},
		{"big5", "61a4", "a�"},
		{"big5", "a47f", "�\u007f"},
		{"shift_jis", "8540", "�@"},
		{"shift_jis", "f040", ""},
		{"shift_jis", "f9fc", ""},
		{"shift_jis", "82a0", "あ"},
		{"shift_jis", "80a1df", "\u0080｡ﾟ"},
		{"shift_jis", "a0fdfeff", "����"},
		{"shift_jis", "611a62", "a\u001ab"},
		{"shift_jis", "82", "�"},
		{"shift_jis", "82fd", "�"},
	} {
		body, _ := hex.DecodeString(tc.hex)
		if got := decodeBody(body, "text/plain; charset="+tc.label); got != tc.want {
			t.Errorf("%s %s = %+q, want %+q", tc.label, tc.hex, got, tc.want)
		}
	}
}
