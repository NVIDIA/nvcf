// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package compat

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

func TestRoundTripMatchesPython(t *testing.T) {
	for _, tc := range loadGolden(t).Roundtrip {
		t.Run(tc.In, func(t *testing.T) {
			value, err := Decode([]byte(tc.In))
			if tc.Out == nil {
				if err == nil {
					t.Fatalf("Decode() = %s, want an error as Python raises", Encode(value))
				}
				return
			}
			if err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			if got := string(Encode(value)); got != *tc.Out {
				t.Errorf("Encode() = %s\nwant       %s", got, *tc.Out)
			}
			if got := string(EncodeSorted(value)); got != *tc.Sorted {
				t.Errorf("EncodeSorted() = %s\nwant             %s", got, *tc.Sorted)
			}
		})
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

type pythonExtras struct {
	PyStr []struct {
		In  string `json:"in"`
		Out string `json:"out"`
	} `json:"pystr"`
	Truthy []struct {
		In  string `json:"in"`
		Out bool   `json:"out"`
	} `json:"truthy"`
	Strip []struct {
		In  string `json:"in"`
		Out string `json:"out"`
	} `json:"strip"`
}

func loadExtras(t *testing.T) pythonExtras {
	t.Helper()
	data, err := os.ReadFile("testdata/python.json")
	if err != nil {
		t.Fatal(err)
	}
	var e pythonExtras
	if err := json.Unmarshal(data, &e); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestPyStrMatchesPython(t *testing.T) {
	for _, tc := range loadExtras(t).PyStr {
		t.Run(tc.In, func(t *testing.T) {
			value, err := Decode([]byte(tc.In))
			if err != nil {
				t.Fatal(err)
			}
			if got := PyStr(value); got != tc.Out {
				t.Errorf("PyStr() = %q, want %q", got, tc.Out)
			}
		})
	}
}

func TestTruthyMatchesPython(t *testing.T) {
	for _, tc := range loadExtras(t).Truthy {
		value, err := Decode([]byte(tc.In))
		if err != nil {
			t.Fatal(err)
		}
		if got := Truthy(value); got != tc.Out {
			t.Errorf("Truthy(%s) = %v, want %v", tc.In, got, tc.Out)
		}
	}
}

func TestPyStripMatchesPython(t *testing.T) {
	for _, tc := range loadExtras(t).Strip {
		if got := PyStrip(tc.In); got != tc.Out {
			t.Errorf("PyStrip(%q) = %q, want %q", tc.In, got, tc.Out)
		}
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
