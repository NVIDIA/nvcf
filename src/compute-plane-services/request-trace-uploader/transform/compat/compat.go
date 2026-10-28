// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package compat reads and writes the JSON of request trace records.
//
// Decode accepts standard JSON and keeps what a generic decoder loses: key
// order, the source text of every number, and a \u escape for a lone
// surrogate. Object keys keep their first position and take their last
// value. Encode writes compact JSON in UTF-8, with numbers as they were
// written, so a changed record differs from its source only where it was
// changed.
//
// Strings hold UTF-8, except that a lone surrogate is stored as its 3-byte
// generalized UTF-8 form, so it survives a round trip and hashes the way the
// earlier uploader hashed it.
package compat

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxDepth bounds nesting, so a hostile line cannot exhaust the stack.
const MaxDepth = 1000

// Kind is the JSON type of a Value.
type Kind uint8

const (
	Null Kind = iota
	Bool
	Int
	Float
	String
	Array
	Object
)

// Member is one key and value of an object, in insertion order.
type Member struct {
	Key   string
	Value *Value
}

// Value is one decoded JSON value. Containers are changed in place.
type Value struct {
	kind    Kind
	boolean bool
	// number holds a number's source text, for an integer or a float.
	number  string
	float   float64
	str     string
	items   []*Value
	members []Member
	index   map[string]int
}

// NewNull returns a JSON null.
func NewNull() *Value { return &Value{kind: Null} }

// NewBool returns a JSON boolean.
func NewBool(b bool) *Value { return &Value{kind: Bool, boolean: b} }

// NewInt returns a JSON integer.
func NewInt(i int64) *Value { return &Value{kind: Int, number: strconv.FormatInt(i, 10)} }

// NewString returns a JSON string.
func NewString(s string) *Value { return &Value{kind: String, str: s} }

// NewObject returns an empty JSON object.
func NewObject() *Value { return &Value{kind: Object} }

// Kind returns the value's JSON type.
func (v *Value) Kind() Kind { return v.kind }

// String returns the value of a JSON string.
func (v *Value) String() (string, bool) {
	if v == nil || v.kind != String {
		return "", false
	}
	return v.str, true
}

// Bool returns the value of a JSON boolean.
func (v *Value) Bool() (bool, bool) {
	if v == nil || v.kind != Bool {
		return false, false
	}
	return v.boolean, true
}

// IntDigits returns a JSON integer as it was written.
func (v *Value) IntDigits() (string, bool) {
	if v == nil || v.kind != Int {
		return "", false
	}
	return v.number, true
}

// Float returns the value of a JSON number written with a fraction or an
// exponent. A number too large for a float64 is an infinity.
func (v *Value) Float() (float64, bool) {
	if v == nil || v.kind != Float {
		return 0, false
	}
	return v.float, true
}

// Items returns the elements of an array. Changing an element changes the
// array.
func (v *Value) Items() []*Value {
	if v == nil || v.kind != Array {
		return nil
	}
	return v.items
}

// SetItem replaces element i of an array.
func (v *Value) SetItem(i int, item *Value) { v.items[i] = item }

// Members returns the members of an object in insertion order.
func (v *Value) Members() []Member {
	if v == nil || v.kind != Object {
		return nil
	}
	return v.members
}

// Get returns the value of key, or nil when v is not an object or has no
// such key.
func (v *Value) Get(key string) *Value {
	if i, ok := v.lookup(key); ok {
		return v.members[i].Value
	}
	return nil
}

// Has reports whether v is an object with key.
func (v *Value) Has(key string) bool {
	_, ok := v.lookup(key)
	return ok
}

// Set stores key in an object. An existing key keeps its position.
func (v *Value) Set(key string, value *Value) {
	if i, ok := v.lookup(key); ok {
		v.members[i].Value = value
		return
	}
	v.members = append(v.members, Member{Key: key, Value: value})
	if v.index != nil {
		v.index[key] = len(v.members) - 1
	}
}

// Clear removes every member of an object.
func (v *Value) Clear() {
	v.members = nil
	v.index = nil
}

// indexAbove is the member count past which lookups use a map.
const indexAbove = 8

func (v *Value) lookup(key string) (int, bool) {
	if v == nil || v.kind != Object {
		return 0, false
	}
	if len(v.members) > indexAbove {
		if v.index == nil {
			v.index = make(map[string]int, len(v.members))
			for i, m := range v.members {
				v.index[m.Key] = i
			}
		}
		i, ok := v.index[key]
		return i, ok
	}
	for i, m := range v.members {
		if m.Key == key {
			return i, true
		}
	}
	return 0, false
}

// CodePoints returns the number of code points in s, counting each lone
// surrogate as one. Redaction markers measure media this way.
func CodePoints(s string) int {
	n := 0
	for i := 0; i < len(s); {
		_, size := DecodeRune(s[i:])
		i += size
		n++
	}
	return n
}

// DecodeRune decodes the first code point of s, accepting the 3-byte form of
// a lone surrogate that Decode stores.
func DecodeRune(s string) (rune, int) {
	if len(s) >= 3 && s[0] == 0xed && s[1] >= 0xa0 && s[1] <= 0xbf && s[2]&0xc0 == 0x80 {
		return rune(s[0]&0x0f)<<12 | rune(s[1]&0x3f)<<6 | rune(s[2]&0x3f), 3
	}
	return utf8.DecodeRuneInString(s)
}

func appendSurrogate(buf []byte, r rune) []byte {
	return append(buf, 0xe0|byte(r>>12), 0x80|byte(r>>6)&0x3f, 0x80|byte(r)&0x3f)
}

// Decode parses one JSON text. NaN, Infinity and anything else that is not
// JSON is an error.
func Decode(data []byte) (*Value, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("json: invalid UTF-8")
	}
	d := decoder{data: data}
	d.skipSpace()
	value, err := d.value(0)
	if err != nil {
		return nil, err
	}
	d.skipSpace()
	if d.pos != len(d.data) {
		return nil, d.errorf("extra data")
	}
	return value, nil
}

type decoder struct {
	data []byte
	pos  int
}

func (d *decoder) errorf(format string, args ...any) error {
	return fmt.Errorf("json: %s at offset %d", fmt.Sprintf(format, args...), d.pos)
}

func (d *decoder) skipSpace() {
	for d.pos < len(d.data) {
		switch d.data[d.pos] {
		case ' ', '\t', '\n', '\r':
			d.pos++
		default:
			return
		}
	}
}

func (d *decoder) literal(word string) bool {
	if strings.HasPrefix(string(d.data[d.pos:min(len(d.data), d.pos+len(word))]), word) {
		d.pos += len(word)
		return true
	}
	return false
}

func (d *decoder) value(depth int) (*Value, error) {
	if d.pos >= len(d.data) {
		return nil, d.errorf("expecting value")
	}
	switch c := d.data[d.pos]; {
	case c == '{':
		return d.object(depth + 1)
	case c == '[':
		return d.array(depth + 1)
	case c == '"':
		s, err := d.string()
		if err != nil {
			return nil, err
		}
		return NewString(s), nil
	case c == 'n' && d.literal("null"):
		return NewNull(), nil
	case c == 't' && d.literal("true"):
		return NewBool(true), nil
	case c == 'f' && d.literal("false"):
		return NewBool(false), nil
	case c == '-' || (c >= '0' && c <= '9'):
		return d.number()
	}
	return nil, d.errorf("expecting value")
}

func (d *decoder) object(depth int) (*Value, error) {
	if depth > MaxDepth {
		return nil, d.errorf("nesting deeper than %d", MaxDepth)
	}
	d.pos++
	object := NewObject()
	d.skipSpace()
	if d.pos < len(d.data) && d.data[d.pos] == '}' {
		d.pos++
		return object, nil
	}
	for {
		if d.pos >= len(d.data) || d.data[d.pos] != '"' {
			return nil, d.errorf("expecting property name enclosed in double quotes")
		}
		key, err := d.string()
		if err != nil {
			return nil, err
		}
		d.skipSpace()
		if d.pos >= len(d.data) || d.data[d.pos] != ':' {
			return nil, d.errorf("expecting ':' delimiter")
		}
		d.pos++
		d.skipSpace()
		value, err := d.value(depth)
		if err != nil {
			return nil, err
		}
		object.Set(key, value)
		d.skipSpace()
		if d.pos >= len(d.data) {
			return nil, d.errorf("expecting ',' delimiter")
		}
		switch d.data[d.pos] {
		case '}':
			d.pos++
			return object, nil
		case ',':
			d.pos++
			d.skipSpace()
		default:
			return nil, d.errorf("expecting ',' delimiter")
		}
	}
}

func (d *decoder) array(depth int) (*Value, error) {
	if depth > MaxDepth {
		return nil, d.errorf("nesting deeper than %d", MaxDepth)
	}
	d.pos++
	array := &Value{kind: Array}
	d.skipSpace()
	if d.pos < len(d.data) && d.data[d.pos] == ']' {
		d.pos++
		return array, nil
	}
	for {
		item, err := d.value(depth)
		if err != nil {
			return nil, err
		}
		array.items = append(array.items, item)
		d.skipSpace()
		if d.pos >= len(d.data) {
			return nil, d.errorf("expecting ',' delimiter")
		}
		switch d.data[d.pos] {
		case ']':
			d.pos++
			return array, nil
		case ',':
			d.pos++
			d.skipSpace()
		default:
			return nil, d.errorf("expecting ',' delimiter")
		}
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// number matches -?(0|[1-9]\d*)(\.\d+)?([eE][-+]?\d+)?. A fraction or
// exponent makes a float; anything else is an integer. Either keeps its
// source text.
func (d *decoder) number() (*Value, error) {
	start := d.pos
	if d.data[d.pos] == '-' {
		d.pos++
	}
	switch {
	case d.pos < len(d.data) && d.data[d.pos] == '0':
		d.pos++
	case d.pos < len(d.data) && isDigit(d.data[d.pos]):
		for d.pos < len(d.data) && isDigit(d.data[d.pos]) {
			d.pos++
		}
	default:
		d.pos = start
		return nil, d.errorf("expecting value")
	}
	isFloat := false
	if d.pos+1 < len(d.data) && d.data[d.pos] == '.' && isDigit(d.data[d.pos+1]) {
		isFloat = true
		d.pos++
		for d.pos < len(d.data) && isDigit(d.data[d.pos]) {
			d.pos++
		}
	}
	if d.pos < len(d.data) && (d.data[d.pos] == 'e' || d.data[d.pos] == 'E') {
		next := d.pos + 1
		if next < len(d.data) && (d.data[next] == '+' || d.data[next] == '-') {
			next++
		}
		if next < len(d.data) && isDigit(d.data[next]) {
			isFloat = true
			d.pos = next
			for d.pos < len(d.data) && isDigit(d.data[d.pos]) {
				d.pos++
			}
		}
	}
	text := string(d.data[start:d.pos])
	if !isFloat {
		return &Value{kind: Int, number: text}, nil
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return nil, d.errorf("invalid number")
	}
	return &Value{kind: Float, number: text, float: f}, nil
}

func hexValue(c byte) (rune, bool) {
	switch {
	case c >= '0' && c <= '9':
		return rune(c - '0'), true
	case c >= 'a' && c <= 'f':
		return rune(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return rune(c-'A') + 10, true
	}
	return 0, false
}

func (d *decoder) hex4(at int) (rune, bool) {
	if at+4 > len(d.data) {
		return 0, false
	}
	var r rune
	for _, c := range d.data[at : at+4] {
		v, ok := hexValue(c)
		if !ok {
			return 0, false
		}
		r = r<<4 | v
	}
	return r, true
}

// string decodes a JSON string starting at its opening quote.
func (d *decoder) string() (string, error) {
	d.pos++
	var out []byte
	for {
		start := d.pos
		for d.pos < len(d.data) {
			c := d.data[d.pos]
			if c == '"' || c == '\\' || c < 0x20 {
				break
			}
			d.pos++
		}
		out = append(out, d.data[start:d.pos]...)
		if d.pos >= len(d.data) {
			return "", d.errorf("unterminated string")
		}
		c := d.data[d.pos]
		if c == '"' {
			d.pos++
			return string(out), nil
		}
		if c < 0x20 {
			return "", d.errorf("invalid control character")
		}
		d.pos++
		if d.pos >= len(d.data) {
			return "", d.errorf("unterminated string")
		}
		esc := d.data[d.pos]
		d.pos++
		switch esc {
		case '"', '\\', '/':
			out = append(out, esc)
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'u':
			r, ok := d.hex4(d.pos)
			if !ok {
				return "", d.errorf("invalid \\uXXXX escape")
			}
			d.pos += 4
			if r >= 0xd800 && r <= 0xdbff && d.pos+6 <= len(d.data) && d.data[d.pos] == '\\' && d.data[d.pos+1] == 'u' {
				low, ok := d.hex4(d.pos + 2)
				if !ok {
					return "", d.errorf("invalid \\uXXXX escape")
				}
				if low >= 0xdc00 && low <= 0xdfff {
					d.pos += 6
					out = utf8.AppendRune(out, 0x10000+(r-0xd800)<<10+(low-0xdc00))
					continue
				}
			}
			if r >= 0xd800 && r <= 0xdfff {
				out = appendSurrogate(out, r)
			} else {
				out = utf8.AppendRune(out, r)
			}
		default:
			return "", d.errorf("invalid \\escape")
		}
	}
}

// Encode writes v as compact JSON in UTF-8. Numbers keep their source text.
// Only quotes, backslashes, control characters and lone surrogates are
// escaped.
func Encode(v *Value) []byte { return encodePlain(nil, v) }

func encodePlain(buf []byte, v *Value) []byte {
	switch v.kind {
	case Null:
		return append(buf, "null"...)
	case Bool:
		if v.boolean {
			return append(buf, "true"...)
		}
		return append(buf, "false"...)
	case Int, Float:
		return append(buf, v.number...)
	case String:
		return appendQuoted(buf, v.str)
	case Array:
		buf = append(buf, '[')
		for i, item := range v.items {
			if i > 0 {
				buf = append(buf, ',')
			}
			buf = encodePlain(buf, item)
		}
		return append(buf, ']')
	case Object:
		buf = append(buf, '{')
		for i, m := range v.members {
			if i > 0 {
				buf = append(buf, ',')
			}
			buf = appendQuoted(buf, m.Key)
			buf = append(buf, ':')
			buf = encodePlain(buf, m.Value)
		}
		return append(buf, '}')
	}
	return buf
}

func appendQuoted(buf []byte, s string) []byte {
	buf = append(buf, '"')
	for i := 0; i < len(s); {
		c := s[i]
		if c >= 0x20 && c != '"' && c != '\\' && c != 0xed {
			buf = append(buf, c)
			i++
			continue
		}
		switch c {
		case '"':
			buf = append(buf, '\\', '"')
		case '\\':
			buf = append(buf, '\\', '\\')
		case '\n':
			buf = append(buf, '\\', 'n')
		case '\r':
			buf = append(buf, '\\', 'r')
		case '\t':
			buf = append(buf, '\\', 't')
		case '\b':
			buf = append(buf, '\\', 'b')
		case '\f':
			buf = append(buf, '\\', 'f')
		case 0xed:
			// 0xed leads both a lone surrogate's stored form and ordinary
			// code points from U+D000 to U+D7FF.
			if r, size := DecodeRune(s[i:]); size == 3 && r >= 0xd800 && r <= 0xdfff {
				buf = appendEscape(buf, r)
				i += size
				continue
			}
			buf = append(buf, c)
		default:
			buf = appendEscape(buf, rune(c))
		}
		i++
	}
	return append(buf, '"')
}

// Text returns a value as plain text: a string as it is, a number as it was
// written, true or false, an empty string for null or a missing value, and
// compact JSON for an array or object.
func Text(v *Value) string {
	if v == nil {
		return ""
	}
	switch v.kind {
	case Null:
		return ""
	case String:
		return v.str
	case Int, Float:
		return v.number
	}
	return string(Encode(v))
}

// Equal reports whether a and b are the same JSON: the same keys in the same
// order, the same strings, and numbers of the same kind with the same value.
func Equal(a, b *Value) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.kind != b.kind {
		return false
	}
	switch a.kind {
	case Bool:
		return a.boolean == b.boolean
	case Int:
		return canonicalInt(a.number) == canonicalInt(b.number)
	case Float:
		return a.float == b.float
	case String:
		return a.str == b.str
	case Array:
		if len(a.items) != len(b.items) {
			return false
		}
		for i := range a.items {
			if !Equal(a.items[i], b.items[i]) {
				return false
			}
		}
		return true
	case Object:
		if len(a.members) != len(b.members) {
			return false
		}
		for i := range a.members {
			if a.members[i].Key != b.members[i].Key || !Equal(a.members[i].Value, b.members[i].Value) {
				return false
			}
		}
		return true
	}
	return true
}

func canonicalInt(text string) string {
	if text == "-0" {
		return "0"
	}
	return text
}

// CanonicalSorted writes v with sorted keys in the canonical form the
// redaction marker hashes for a value that is not a string: every character
// outside printable ASCII escaped, integers in plain digits, and floats in
// the shortest form that round-trips (1e-05, 1e+16, 100.0). It is the form
// the earlier uploader hashed, so marker hashes stay stable.
func CanonicalSorted(v *Value) []byte { return encodeCanonical(nil, v) }

func encodeCanonical(buf []byte, v *Value) []byte {
	switch v.kind {
	case Null:
		return append(buf, "null"...)
	case Bool:
		if v.boolean {
			return append(buf, "true"...)
		}
		return append(buf, "false"...)
	case Int:
		return append(buf, canonicalInt(v.number)...)
	case Float:
		switch {
		case math.IsInf(v.float, 1):
			return append(buf, "Infinity"...)
		case math.IsInf(v.float, -1):
			return append(buf, "-Infinity"...)
		}
		return append(buf, FloatRepr(v.float)...)
	case String:
		return appendASCIIQuoted(buf, v.str)
	case Array:
		buf = append(buf, '[')
		for i, item := range v.items {
			if i > 0 {
				buf = append(buf, ',')
			}
			buf = encodeCanonical(buf, item)
		}
		return append(buf, ']')
	case Object:
		members := append([]Member(nil), v.members...)
		// Byte order of UTF-8, and of the surrogate form, is code point
		// order.
		sort.Slice(members, func(i, j int) bool { return members[i].Key < members[j].Key })
		buf = append(buf, '{')
		for i, m := range members {
			if i > 0 {
				buf = append(buf, ',')
			}
			buf = appendASCIIQuoted(buf, m.Key)
			buf = append(buf, ':')
			buf = encodeCanonical(buf, m.Value)
		}
		return append(buf, '}')
	}
	return buf
}

const hexDigits = "0123456789abcdef"

func appendEscape(buf []byte, r rune) []byte {
	return append(buf, '\\', 'u', hexDigits[r>>12&0xf], hexDigits[r>>8&0xf], hexDigits[r>>4&0xf], hexDigits[r&0xf])
}

// appendASCIIQuoted appends s as a JSON string with every character outside
// printable ASCII escaped.
func appendASCIIQuoted(buf []byte, s string) []byte {
	buf = append(buf, '"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			i++
			switch {
			case c == '"':
				buf = append(buf, '\\', '"')
			case c == '\\':
				buf = append(buf, '\\', '\\')
			case c == '\n':
				buf = append(buf, '\\', 'n')
			case c == '\r':
				buf = append(buf, '\\', 'r')
			case c == '\t':
				buf = append(buf, '\\', 't')
			case c == '\b':
				buf = append(buf, '\\', 'b')
			case c == '\f':
				buf = append(buf, '\\', 'f')
			case c < 0x20 || c == 0x7f:
				buf = appendEscape(buf, rune(c))
			default:
				buf = append(buf, c)
			}
			continue
		}
		r, size := DecodeRune(s[i:])
		i += size
		if r >= 0x10000 {
			r -= 0x10000
			buf = appendEscape(buf, 0xd800+(r>>10))
			buf = appendEscape(buf, 0xdc00+(r&0x3ff))
			continue
		}
		buf = appendEscape(buf, r)
	}
	return append(buf, '"')
}

// FloatRepr formats f as Python's repr(float) does: the shortest digits that
// round-trip, fixed notation for exponents from -4 to 15 with at least one
// fractional digit, otherwise d.ddde+XX, and nan, inf and -inf.
func FloatRepr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case f == 0:
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	text := strconv.FormatFloat(f, 'e', -1, 64)
	sign := ""
	if text[0] == '-' {
		sign = "-"
		text = text[1:]
	}
	mantissa, exponent, _ := strings.Cut(text, "e")
	exp, _ := strconv.Atoi(exponent)
	digits := strings.Replace(mantissa, ".", "", 1)
	point := exp + 1
	switch {
	case point <= -4 || point > 16:
		out := digits[:1]
		if len(digits) > 1 {
			out += "." + digits[1:]
		}
		expSign := "+"
		if exp < 0 {
			expSign = "-"
			exp = -exp
		}
		return fmt.Sprintf("%s%se%s%02d", sign, out, expSign, exp)
	case point <= 0:
		return sign + "0." + strings.Repeat("0", -point) + digits
	case point >= len(digits):
		return sign + digits + strings.Repeat("0", point-len(digits)) + ".0"
	default:
		return sign + digits[:point] + "." + digits[point:]
	}
}
