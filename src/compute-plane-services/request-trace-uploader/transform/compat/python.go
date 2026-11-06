// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package compat

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Clone returns a deep copy of v.
func (v *Value) Clone() *Value {
	if v == nil {
		return nil
	}
	out := *v
	out.index = nil
	if v.items != nil {
		out.items = make([]*Value, len(v.items))
		for i, item := range v.items {
			out.items[i] = item.Clone()
		}
	}
	if v.members != nil {
		out.members = make([]Member, len(v.members))
		for i, m := range v.members {
			out.members[i] = Member{Key: m.Key, Value: m.Value.Clone()}
		}
	}
	return &out
}

// Truthy reports Python truthiness: null, false, zero, an empty string and
// an empty container are false.
func Truthy(v *Value) bool {
	switch v.kind {
	case Null:
		return false
	case Bool:
		return v.boolean
	case Int:
		return v.digits != "0"
	case Float:
		return v.float != 0
	case String:
		return v.str != ""
	case Array:
		return len(v.items) > 0
	case Object:
		return len(v.members) > 0
	}
	return false
}

// PyStr returns Python's str() of the decoded value: a string as is, None,
// True and False, an integer's digits, repr for a float, and repr for a list
// or dict.
func PyStr(v *Value) string {
	if v.kind == String {
		return v.str
	}
	return string(appendRepr(nil, v))
}

func appendRepr(buf []byte, v *Value) []byte {
	switch v.kind {
	case Null:
		return append(buf, "None"...)
	case Bool:
		if v.boolean {
			return append(buf, "True"...)
		}
		return append(buf, "False"...)
	case Int:
		return append(buf, v.digits...)
	case Float:
		return append(buf, FloatRepr(v.float)...)
	case String:
		return appendStringRepr(buf, v.str)
	case Array:
		buf = append(buf, '[')
		for i, item := range v.items {
			if i > 0 {
				buf = append(buf, ", "...)
			}
			buf = appendRepr(buf, item)
		}
		return append(buf, ']')
	case Object:
		buf = append(buf, '{')
		for i, m := range v.members {
			if i > 0 {
				buf = append(buf, ", "...)
			}
			buf = appendStringRepr(buf, m.Key)
			buf = append(buf, ": "...)
			buf = appendRepr(buf, m.Value)
		}
		return append(buf, '}')
	}
	return buf
}

// appendStringRepr appends Python's repr() of a str: single quotes unless
// the text has a single quote and no double quote, and escapes for anything
// that is not printable.
func appendStringRepr(buf []byte, s string) []byte {
	quote := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		quote = '"'
	}
	buf = append(buf, quote)
	for i := 0; i < len(s); {
		r, size := DecodeRune(s[i:])
		i += size
		switch {
		case r == rune(quote) || r == '\\':
			buf = append(buf, '\\', byte(r))
		case r == '\t':
			buf = append(buf, '\\', 't')
		case r == '\n':
			buf = append(buf, '\\', 'n')
		case r == '\r':
			buf = append(buf, '\\', 'r')
		case r < 0x20 || r == 0x7f:
			buf = append(buf, '\\', 'x', hexDigits[r>>4], hexDigits[r&0xf])
		case r < 0x7f:
			buf = append(buf, byte(r))
		case r >= 0xd800 && r <= 0xdfff:
			buf = appendHexEscape(buf, 'u', r, 4)
		case unicode.IsPrint(r):
			buf = utf8.AppendRune(buf, r)
		case r <= 0xff:
			buf = append(buf, '\\', 'x', hexDigits[r>>4], hexDigits[r&0xf])
		case r <= 0xffff:
			buf = appendHexEscape(buf, 'u', r, 4)
		default:
			buf = appendHexEscape(buf, 'U', r, 8)
		}
	}
	return append(buf, quote)
}

func appendHexEscape(buf []byte, kind byte, r rune, width int) []byte {
	buf = append(buf, '\\', kind)
	for shift := (width - 1) * 4; shift >= 0; shift -= 4 {
		buf = append(buf, hexDigits[r>>shift&0xf])
	}
	return buf
}

// IsSpace reports whether Python's str.isspace() is true for r, which is
// also what Python's regular expression \s matches. It adds the four ASCII
// separator controls to Go's white space set.
func IsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// PyStrip removes leading and trailing white space as Python's str.strip()
// does with no argument.
func PyStrip(s string) string {
	return strings.TrimFunc(s, IsSpace)
}
