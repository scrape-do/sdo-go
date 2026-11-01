// SPDX-License-Identifier: Apache-2.0

package sdo

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

type jsonTag struct {
	B    int    `json:"b"`
	A    string `json:"a"`
	Skip string `json:"-"`
}

// Every Go shape a caller is likely to hand the SDK encodes to the same
// canonical bytes the other SDKs produce for the equivalent value.
func TestCanonicalJSONShapes(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"signed ints", []any{int8(-1), int16(2), int32(3), int64(-4), 5}, `[-1,2,3,-4,5]`},
		{"unsigned ints", []any{uint(1), uint8(2), uint16(3), uint32(4), uint64(18446744073709551615)},
			`[1,2,3,4,18446744073709551615]`},
		{"float32 uses its own shortest form", float32(0.1), `0.1`},
		{"json.Number integer stays exact", json.Number("123456789012345678901234567890"), `123456789012345678901234567890`},
		{"json.Number -0", json.Number("-0"), `0`},
		{"json.Number fraction", json.Number("1.50"), `1.5`},
		{"json.Number exponent", json.Number("1E-7"), `1e-7`},
		{"map[string]string", map[string]string{"b": "2", "a": "<1>"}, `{"a":"<1>","b":"2"}`},
		{"[]string", []string{"x", " "}, "[\"x\",\" \"]"},
		{"[]map[string]any", []map[string]any{{"z": nil, "y": true}, {}}, `[{"y":true,"z":null},{}]`},
		{"struct via encoding/json", jsonTag{B: 1, A: "&", Skip: "no"}, `{"a":"&","b":1}`},
		{"typed map and slice", map[string][]int{"k": {3, 1}}, `{"k":[3,1]}`},
		{"marshaler", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), `"2026-01-02T03:04:05Z"`},
		{"invalid UTF-8 is replaced", "a\xffb", "\"a�b\""},
		{"controls", "\x00\x1f\x7f", `"\u0000\u001f` + "\x7f" + `"`},
		{"false", false, `false`},
	}
	for _, c := range cases {
		got, err := canonicalJSON(c.in)
		if err != nil || got != c.want {
			t.Errorf("%s: canonicalJSON = %s, %v; want %s", c.name, got, err, c.want)
		}
	}
}

// deepNode nests through a pointer: encoding/json marshals it to any depth,
// but cannot decode JSON nested past its limit (10000).
type deepNode struct {
	Next *deepNode `json:"next,omitempty"`
}

func TestCanonicalJSONRejects(t *testing.T) {
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	var deep *deepNode
	for range 10001 {
		deep = &deepNode{Next: deep}
	}
	for name, v := range map[string]any{
		"NaN":               math.NaN(),
		"Inf float32":       float32(math.Inf(1)),
		"channel":           make(chan int),
		"bad json.Number":   json.Number("12abc"),
		"NaN in slice":      []any{1, math.Inf(-1)},
		"NaN in map":        map[string]any{"x": math.NaN()},
		"NaN in []map":      []map[string]any{{"x": math.NaN()}},
		"cycle":             cyclic,
		"NaN json.Number":   json.Number("NaN"),
		"unencodable leaf":  map[string]any{"f": func() {}},
		"too deep a struct": deep,
	} {
		if got, err := canonicalJSON(v); err == nil {
			t.Errorf("%s: want an error, got %s", name, got)
		}
	}
}

func TestESNumberForms(t *testing.T) {
	for in, want := range map[float64]string{
		5e-324: "5e-324", 1.7976931348623157e308: "1.7976931348623157e+308", 123e-20: "1.23e-18",
		-1e21: "-1e+21", 999999999999999900000: "999999999999999900000", 0.000001234: "0.000001234",
		-0.5: "-0.5", 25: "25",
	} {
		if got, err := esNumber(in, 64); err != nil || got != want {
			t.Errorf("esNumber(%v) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestPctEncode(t *testing.T) {
	if got := pctEncode("aZ09-._~ /+é*"); got != "aZ09-._~%20%2F%2B%C3%A9%2A" {
		t.Errorf("pctEncode = %q", got)
	}
}
