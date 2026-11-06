// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package jsonvalue

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type golden struct {
	Roundtrip []struct {
		In     string  `json:"in"`
		Out    *string `json:"out"`
		Sorted *string `json:"sorted"`
	} `json:"roundtrip"`
	Strings []struct {
		JSON   string `json:"json"`
		Len    int    `json:"len"`
		SHA256 string `json:"sha256"`
	} `json:"strings"`
}

// loadGolden reads expected values produced by Python's json module:
// json.loads, then json.dumps(value, separators=(",", ":")), with and
// without sort_keys; and len() and sha256 of str.encode("utf-8",
// "surrogatepass") for strings.
func loadGolden(t *testing.T) golden {
	t.Helper()
	data, err := os.ReadFile("testdata/python.json")
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

// TestDecodeMatchesPython checks that Decode accepts what Python's json
// module accepts and reads the same values, except NaN, Infinity and
// -Infinity, which are not JSON and are rejected. CanonicalSorted must match
// Python's sorted output byte for byte: redaction markers hash it.
func TestDecodeMatchesPython(t *testing.T) {
	for _, tc := range loadGolden(t).Roundtrip {
		t.Run(tc.In, func(t *testing.T) {
			value, err := Decode([]byte(tc.In))
			if tc.Out == nil || notJSON(tc.In) {
				if err == nil {
					t.Fatalf("Decode() = %s, want an error", Encode(value))
				}
				return
			}
			if err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			// Python writes a float that overflowed as Infinity, which is not
			// JSON, so only the canonical form can be compared for it.
			if !notJSON(*tc.Out) {
				want, err := Decode([]byte(*tc.Out))
				if err != nil {
					t.Fatal(err)
				}
				if !Equal(value, want) {
					t.Errorf("Decode() = %s, want the value of %s", Encode(value), *tc.Out)
				}
			}
			if got := string(CanonicalSorted(value)); got != *tc.Sorted {
				t.Errorf("CanonicalSorted() = %s\nwant               %s", got, *tc.Sorted)
			}
		})
	}
}

// notJSON reports text with the NaN or Infinity tokens that Python's json
// module reads and writes but JSON does not have.
func notJSON(text string) bool {
	return strings.Contains(text, "NaN") || strings.Contains(text, "Infinity")
}

func TestEncodeWritesPlainJSON(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "compact", in: ` { "a" : [ 1 , true , null ] } `, want: `{"a":[1,true,null]}`},
		{name: "non-ASCII stays UTF-8", in: `"caf\u00e9 \u2603 \ud83d\ude00"`, want: "\"caf\u00e9 \u2603 \U0001F600\""},
		{name: "raw non-ASCII stays UTF-8", in: "\"caf\u00e9\"", want: "\"caf\u00e9\""},
		{name: "quote and backslash", in: `"a\"b\\c\/d"`, want: `"a\"b\\c/d"`},
		{name: "control characters", in: `"\n\r\t\b\f\u0001\u001f"`, want: `"\n\r\t\b\f\u0001\u001f"`},
		{name: "delete stays as is", in: `"\u007f"`, want: "\"\x7f\""},
		{name: "lone surrogate stays escaped", in: `"a\ud800b"`, want: `"a\ud800b"`},
		{name: "numbers keep their source text", in: `[1.50,1e5,1E+2,-0,0.000001,12345678901234567890123]`, want: `[1.50,1e5,1E+2,-0,0.000001,12345678901234567890123]`},
		{name: "duplicate key keeps first position and last value", in: `{"k":1,"z":2,"k":3}`, want: `{"k":3,"z":2}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, err := Decode([]byte(tt.in))
			if err != nil {
				t.Fatal(err)
			}
			if got := string(Encode(value)); got != tt.want {
				t.Fatalf("Encode() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDecodeRejectsNonStandardNumbers(t *testing.T) {
	for _, in := range []string{`NaN`, `Infinity`, `-Infinity`, `[1,NaN]`, `{"a":Infinity}`} {
		if _, err := Decode([]byte(in)); err == nil {
			t.Errorf("Decode(%s) accepted a value that is not JSON", in)
		}
	}
}

func TestText(t *testing.T) {
	tests := map[string]string{
		`"plain"`:         "plain",
		`" spaced "`:      " spaced ",
		`"caf\u00e9"`:     "caf\u00e9",
		`123`:             "123",
		`1.50`:            "1.50",
		`1e5`:             "1e5",
		`true`:            "true",
		`false`:           "false",
		`null`:            "",
		`[1,"a",null]`:    `[1,"a",null]`,
		`{"a":{"b":1.0}}`: `{"a":{"b":1.0}}`,
	}
	for in, want := range tests {
		value, err := Decode([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		if got := Text(value); got != want {
			t.Errorf("Text(%s) = %q, want %q", in, got, want)
		}
	}
	if got := Text(nil); got != "" {
		t.Errorf("Text(nil) = %q, want empty", got)
	}
}

func TestEqual(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{a: `{"a":1,"b":[true,null]}`, b: `{"a":1,"b":[true,null]}`, want: true},
		{a: `{"a":1,"b":2}`, b: `{"b":2,"a":1}`, want: false},
		{a: `1.5e3`, b: `1500.0`, want: true},
		{a: `-0`, b: `0`, want: true},
		{a: `1`, b: `1.0`, want: false},
		{a: `"a"`, b: `"b"`, want: false},
		{a: `[1]`, b: `[1,2]`, want: false},
		{a: `{"a":1}`, b: `{"a":1,"b":2}`, want: false},
	}
	for _, tt := range tests {
		a, err := Decode([]byte(tt.a))
		if err != nil {
			t.Fatal(err)
		}
		b, err := Decode([]byte(tt.b))
		if err != nil {
			t.Fatal(err)
		}
		if got := Equal(a, b); got != tt.want {
			t.Errorf("Equal(%s, %s) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestStringLengthAndHashMatchPython(t *testing.T) {
	for _, tc := range loadGolden(t).Strings {
		t.Run(tc.JSON, func(t *testing.T) {
			value, err := Decode([]byte(tc.JSON))
			if err != nil {
				t.Fatal(err)
			}
			s, ok := value.String()
			if !ok {
				t.Fatalf("value kind = %v, want string", value.Kind())
			}
			if got := CodePoints(s); got != tc.Len {
				t.Errorf("CodePoints() = %d, want %d", got, tc.Len)
			}
			sum := sha256.Sum256([]byte(s))
			if got := hex.EncodeToString(sum[:]); got != tc.SHA256 {
				t.Errorf("sha256 = %s, want %s", got, tc.SHA256)
			}
		})
	}
}

func TestDecodeRejectsInvalidUTF8(t *testing.T) {
	if _, err := Decode([]byte("{\"a\":\"\xff\"}")); err == nil {
		t.Fatal("Decode() accepted invalid UTF-8")
	}
}

func TestDecodeBoundsNesting(t *testing.T) {
	deep := strings.Repeat("[", MaxDepth+1) + strings.Repeat("]", MaxDepth+1)
	if _, err := Decode([]byte(deep)); err == nil {
		t.Fatal("Decode() accepted nesting past MaxDepth")
	}
	ok := strings.Repeat("[", MaxDepth) + strings.Repeat("]", MaxDepth)
	if _, err := Decode([]byte(ok)); err != nil {
		t.Fatalf("Decode() at MaxDepth error = %v", err)
	}
}

func TestSetKeepsPositionOfExistingKey(t *testing.T) {
	value, err := Decode([]byte(`{"a":1,"b":2,"c":3}`))
	if err != nil {
		t.Fatal(err)
	}
	value.Set("b", NewString("x"))
	value.Set("d", NewBool(true))
	if got := string(Encode(value)); got != `{"a":1,"b":"x","c":3,"d":true}` {
		t.Fatalf("Encode() = %s", got)
	}
	value.Clear()
	value.Set("type", NewString("t"))
	if got := string(Encode(value)); got != `{"type":"t"}` {
		t.Fatalf("Encode() after Clear = %s", got)
	}
}

func TestCloneIsDeep(t *testing.T) {
	value, err := Decode([]byte(`{"a":{"b":[1,{"c":"d"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	clone := value.Clone()
	value.Get("a").Get("b").Items()[1].Set("c", NewString("changed"))
	value.Set("e", NewNull())
	if got := string(Encode(clone)); got != `{"a":{"b":[1,{"c":"d"}]}}` {
		t.Fatalf("clone = %s, want it unchanged by edits to the original", got)
	}
}
