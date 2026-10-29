// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package redact

import (
	"encoding/base64"
	"strings"
	"unicode"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/transform/jsonvalue"
)

// The scanners below match exactly what the earlier uploader's Python
// regular expressions match. Go's regexp has no lookaround and folds case and
// white space differently, so each one is written by hand. Offsets are byte
// offsets; the Python patterns' character classes are ASCII except where
// noted, so byte and code point boundaries agree.

// span is one media substring found in a string.
type span struct {
	start, end int
	kind       string
	mime       string
	hasMIME    bool
}

func (s span) mediaType() mimeType { return mimeType{value: s.mime, set: s.hasMIME} }

// mediaSpans finds every media data URL, and, in strings long enough, every
// base64 run outside them, in order.
func mediaSpans(text string) []span {
	var spans []span
	if strings.Contains(text, ":") {
		scanner := newHeaderScanner(text)
		position := 0
		for {
			h, ok := scanner.search(position)
			if !ok {
				break
			}
			var end int
			var matched bool
			if h.base64 {
				end, matched = matchBase64Body(text, h.end)
			} else {
				end, matched = scanner.urlBody(h.end)
			}
			if matched && isMediaDataURL(h.mime, h.hasMIME, h.base64) {
				s := span{start: h.start, end: end, kind: "data_url"}
				if h.hasMIME {
					s.mime, s.hasMIME = pyLower(h.mime), true
				}
				spans = append(spans, s)
				position = end
			} else {
				position = h.start + len("data:")
			}
		}
	}
	if len(text) < base64RunMinChars || jsonvalue.CodePoints(text) < base64RunMinChars {
		return spans
	}
	runs := make([]span, 0)
	previous := 0
	for i := 0; i <= len(spans); i++ {
		gapEnd := len(text)
		if i < len(spans) {
			gapEnd = spans[i].start
		}
		runs = append(runs, base64Runs(text, previous, gapEnd)...)
		if i < len(spans) {
			previous = spans[i].end
		}
	}
	if len(runs) == 0 {
		return spans
	}
	return mergeSpans(spans, runs)
}

func mergeSpans(a, b []span) []span {
	out := make([]span, 0, len(a)+len(b))
	for len(a) > 0 && len(b) > 0 {
		if a[0].start < b[0].start {
			out, a = append(out, a[0]), a[1:]
		} else {
			out, b = append(out, b[0]), b[1:]
		}
	}
	out = append(out, a...)
	return append(out, b...)
}

func isRunChar(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '_' || c == '-'
}

func isStdBase64Char(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/'
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// base64Runs finds runs of at least base64RunMinChars base64 characters in
// text[start:end], with up to two "=" of padding, that pass
// hasBase64Classes. A run must not follow a base64 character, even one
// before start, and stops at end. Gaps shorter than the minimum are skipped.
func base64Runs(text string, start, end int) []span {
	if end-start < base64RunMinChars || jsonvalue.CodePoints(text[start:end]) < base64RunMinChars {
		return nil
	}
	var runs []span
	i := start
	for i < end {
		if !isRunChar(text[i]) {
			i++
			continue
		}
		runStart := i
		for i < end && isRunChar(text[i]) {
			i++
		}
		if runStart > 0 && isRunChar(text[runStart-1]) {
			continue
		}
		if i-runStart < base64RunMinChars {
			continue
		}
		runEnd := i
		for pad := 0; pad < 2 && runEnd < end && text[runEnd] == '='; pad++ {
			runEnd++
		}
		if hasBase64Classes(text, runStart, runEnd) {
			runs = append(runs, span{start: runStart, end: runEnd, kind: "inline"})
		}
		i = runEnd
	}
	return runs
}

// hasBase64Classes reports whether text[start:end] mixes upper case, lower
// case and digits, and has few enough "/", "-" and "_" for base64 of media.
func hasBase64Classes(text string, start, end int) bool {
	separators := 0
	var upper, lower, digit bool
	for i := start; i < end; i++ {
		switch c := text[i]; {
		case c == '/' || c == '-' || c == '_':
			separators++
		case c >= 'A' && c <= 'Z':
			upper = true
		case c >= 'a' && c <= 'z':
			lower = true
		case c >= '0' && c <= '9':
			digit = true
		}
	}
	return separators*16 <= jsonvalue.CodePoints(text[start:end]) && upper && lower && digit
}

// isBase64String reports a full match of
// [ \t\r\n]*[A-Za-z0-9+/_-]+(?:\r?\n[A-Za-z0-9+/_-]+)*={0,2}[ \t\r\n]*.
func isBase64String(text string) bool {
	i := skipLineSpace(text, 0)
	start := i
	for i < len(text) && isRunChar(text[i]) {
		i++
	}
	if i == start {
		return false
	}
	for {
		next := i
		if next < len(text) && text[next] == '\r' {
			next++
		}
		if next >= len(text) || text[next] != '\n' || next+1 >= len(text) || !isRunChar(text[next+1]) {
			break
		}
		i = next + 1
		for i < len(text) && isRunChar(text[i]) {
			i++
		}
	}
	for pad := 0; pad < 2 && i < len(text) && text[i] == '='; pad++ {
		i++
	}
	return skipLineSpace(text, i) == len(text)
}

func skipLineSpace(text string, i int) int {
	for i < len(text) && (text[i] == ' ' || text[i] == '\t' || text[i] == '\r' || text[i] == '\n') {
		i++
	}
	return i
}

func skipSpaceTab(text string, i int) int {
	for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
		i++
	}
	return i
}

// isDataURL reports whether text starts with "data:", ignoring case, after
// optional spaces, tabs and line breaks.
func isDataURL(text string) bool {
	_, ok := dataURLStart(text)
	return ok
}

func dataURLStart(text string) (int, bool) {
	i := skipLineSpace(text, 0)
	if hasDataPrefix(text, i) {
		return i, true
	}
	return 0, false
}

func hasDataPrefix(text string, i int) bool {
	return len(text)-i >= 5 && strings.EqualFold(text[i:i+4], "data") && text[i+4] == ':'
}

// dataURLMIME returns the lower-cased MIME type of a data URL string.
func dataURLMIME(text string) (string, bool) {
	start, ok := dataURLStart(text)
	if !ok {
		return "", false
	}
	h, ok := newHeaderScanner(text).match(start)
	if !ok || !h.hasMIME {
		return "", false
	}
	return pyLower(h.mime), true
}

type dataURLHeader struct {
	start, end int
	mime       string
	hasMIME    bool
	// base64 reports a bare ";base64" parameter.
	base64 bool
}

// headerScanner finds data URL headers in one string. A failed candidate
// would otherwise rescan the rest of a long parameter chain or URL body, and
// a string of repeated candidates would take quadratic time, so it remembers
// both. Remembering never changes a result: a parameter chain from a given
// ";" and a URL body from inside an already scanned body end the same way
// whichever candidate reaches them.
type headerScanner struct {
	text string
	// chains maps the position of a parameter's ";" to the end of the chain
	// that follows and whether that chain has a base64 flag.
	chains map[int]paramChain
	// urlStart and urlEnd bound the furthest URL body scanned so far.
	urlStart, urlEnd int
}

type paramChain struct {
	end    int
	base64 bool
}

func newHeaderScanner(text string) *headerScanner {
	return &headerScanner{text: text, chains: map[int]paramChain{}, urlStart: -1, urlEnd: -1}
}

// search finds the first data URL header at or after from.
func (s *headerScanner) search(from int) (dataURLHeader, bool) {
	text := s.text
	for i := from; i+5 <= len(text); {
		colon := strings.IndexByte(text[i+4:], ':')
		if colon < 0 {
			break
		}
		p := i + colon
		if hasDataPrefix(text, p) {
			if h, ok := s.match(p); ok {
				return h, true
			}
		}
		i = p + 1
	}
	return dataURLHeader{}, false
}

// match matches, ignoring case, at p:
//
//	data:[ \t]*(?P<mime>TOKEN/SUBTOKEN)?
//	(?P<params>(?:[ \t]*;[ \t]*TOKEN(?:=(?:"[^"\\]*"|[^;,\s"\\]*))?)*)[ \t]*,
//
// Every repetition is greedy and no backtracking can produce a longer or
// different match, so a single forward pass gives the same result.
func (s *headerScanner) match(p int) (dataURLHeader, bool) {
	text := s.text
	if !hasDataPrefix(text, p) {
		return dataURLHeader{}, false
	}
	h := dataURLHeader{start: p}
	i := skipSpaceTab(text, p+5)
	if end, ok := matchToken(text, i, false); ok && end < len(text) && text[end] == '/' {
		if subEnd, ok := matchToken(text, end+1, true); ok {
			h.mime, h.hasMIME = text[i:subEnd], true
			i = subEnd
		}
	}
	i, h.base64 = s.params(i)
	j := skipSpaceTab(text, i)
	if j >= len(text) || text[j] != ',' {
		return dataURLHeader{}, false
	}
	h.end = j + 1
	return h, true
}

// params matches the parameter chain at i and returns where it ends and
// whether it has a base64 flag.
func (s *headerScanner) params(i int) (int, bool) {
	text := s.text
	type step struct {
		semicolon int
		base64    bool
	}
	var steps []step
	end, tailBase64 := i, false
	for {
		j := skipSpaceTab(text, end)
		if j >= len(text) || text[j] != ';' {
			break
		}
		if known, ok := s.chains[j]; ok {
			end, tailBase64 = known.end, known.base64
			break
		}
		tokenEnd, ok := matchToken(text, skipSpaceTab(text, j+1), false)
		if !ok {
			break
		}
		if tokenEnd < len(text) && text[tokenEnd] == '=' {
			tokenEnd++
			if quoted, ok := matchQuoted(text, tokenEnd); ok {
				tokenEnd = quoted
			} else {
				tokenEnd = matchUnquoted(text, tokenEnd)
			}
		}
		// Python splits the whole parameter text on ";", including any ";"
		// inside a quoted value, so each parameter is checked the same way.
		steps = append(steps, step{semicolon: j, base64: hasBase64Flag(text[j+1 : tokenEnd])})
		end = tokenEnd
	}
	base64 := tailBase64
	for n := len(steps) - 1; n >= 0; n-- {
		base64 = base64 || steps[n].base64
		s.chains[steps[n].semicolon] = paramChain{end: end, base64: base64}
	}
	return end, base64
}

// matchToken matches one or more of [a-z0-9!#$&^_.+-] ignoring case, plus
// "*" in a MIME subtype. With Unicode case folding that class also matches
// U+0130, U+0131, U+017F and U+212A.
func matchToken(text string, i int, subtype bool) (int, bool) {
	start := i
	for i < len(text) {
		c := text[i]
		if c < 0x80 {
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("!#$&^_.+-", c) >= 0 || subtype && c == '*' {
				i++
				continue
			}
			break
		}
		r, size := jsonvalue.DecodeRune(text[i:])
		if r != 0x130 && r != 0x131 && r != 0x17f && r != 0x212a {
			break
		}
		i += size
	}
	return i, i > start
}

// matchQuoted matches "[^"\\]*".
func matchQuoted(text string, i int) (int, bool) {
	if i >= len(text) || text[i] != '"' {
		return i, false
	}
	for j := i + 1; j < len(text); j++ {
		switch text[j] {
		case '"':
			return j + 1, true
		case '\\':
			return i, false
		}
	}
	return i, false
}

// matchUnquoted matches [^;,\s"\\]*, where \s is Python's Unicode white space.
func matchUnquoted(text string, i int) int {
	for i < len(text) {
		c := text[i]
		if c == ';' || c == ',' || c == '"' || c == '\\' {
			break
		}
		r, size := jsonvalue.DecodeRune(text[i:])
		if isPySpace(r) {
			break
		}
		i += size
	}
	return i
}

// matchBase64Body matches one or more of: base64 or base64url characters
// with "=", the JSON escape "\/", a percent escape, or a line break (real or
// JSON-escaped) that is followed by 60 standard base64 characters.
func matchBase64Body(text string, i int) (int, bool) {
	start := i
	for i < len(text) {
		c := text[i]
		switch {
		case isRunChar(c) || c == '=':
			i++
		case c == '\\' && i+1 < len(text) && text[i+1] == '/':
			i += 2
		case c == '%' && i+2 < len(text) && isHex(text[i+1]) && isHex(text[i+2]):
			i += 3
		default:
			n := lineBreakLen(text, i)
			if n == 0 || !hasStdBase64(text, i+n, 60) {
				return i, i > start
			}
			i += n
		}
	}
	return i, i > start
}

func lineBreakLen(text string, i int) int {
	rest := text[i:]
	switch {
	case strings.HasPrefix(rest, "\r\n"):
		return 2
	case strings.HasPrefix(rest, "\n"):
		return 1
	case strings.HasPrefix(rest, `\r\n`):
		return 4
	case strings.HasPrefix(rest, `\n`):
		return 2
	}
	return 0
}

func hasStdBase64(text string, i, n int) bool {
	if len(text)-i < n {
		return false
	}
	for j := i; j < i+n; j++ {
		if !isStdBase64Char(text[j]) {
			return false
		}
	}
	return true
}

// urlBody matches one or more of: "\/", or any character except Python
// white space, quotes, angle, round, square and curly brackets, backslash
// and backtick. A body that runs into one already scanned ends where that
// one ends.
func (s *headerScanner) urlBody(i int) (int, bool) {
	text := s.text
	start := i
	for i < len(text) {
		if i >= s.urlStart && i < s.urlEnd {
			i = s.urlEnd
			break
		}
		c := text[i]
		if c == '\\' {
			if i+1 < len(text) && text[i+1] == '/' {
				i += 2
				continue
			}
			break
		}
		if c < 0x80 && strings.IndexByte("\"'<>()[]{}`", c) >= 0 {
			break
		}
		r, size := jsonvalue.DecodeRune(text[i:])
		if isPySpace(r) {
			break
		}
		i += size
	}
	if i > start && (s.urlEnd < 0 || i >= s.urlEnd) {
		s.urlStart, s.urlEnd = start, i
	}
	return i, i > start
}

// isPySpace reports Python's str.isspace(), which is also what Python's
// regular expression \s matches: Go's white space plus the four ASCII
// separator controls. Media detection keeps the earlier uploader's
// boundaries, so markers and their hashes stay the same.
func isPySpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// pyStrip trims leading and trailing characters for which isPySpace is
// true, as Python's str.strip() does.
func pyStrip(s string) string {
	return strings.TrimFunc(s, isPySpace)
}

// hasBase64Flag reports a bare ";base64" parameter. A value such as
// name=base64.pdf is not the flag.
func hasBase64Flag(params string) bool {
	for _, param := range strings.Split(params, ";") {
		if asciiLower(pyStrip(param)) == "base64" {
			return true
		}
	}
	return false
}

// asciiLower lower-cases ASCII letters only. Python's lower() maps no other
// character to an ASCII letter that "base64" contains.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// isMediaDataURL decides whether a data URL holds media: always with the
// base64 flag, otherwise by its MIME type.
func isMediaDataURL(mime string, hasMIME, base64 bool) bool {
	if base64 {
		return true
	}
	if !hasMIME {
		return false
	}
	major, subtype, _ := strings.Cut(pyLower(mime), "/")
	if mediaMIMEMajors[major] {
		return true
	}
	if major != "application" {
		return false
	}
	return !(textualApplicationSubtypes[subtype] || strings.HasSuffix(subtype, "+json") || strings.HasSuffix(subtype, "+xml"))
}

// pyLower lower-cases like Python's str.lower(): U+0130 becomes "i" plus a
// combining dot, a capital sigma at the end of a word becomes a final sigma,
// and every other character takes its simple lower-case mapping.
func pyLower(s string) string {
	var runes []rune
	var widths []int
	for i := 0; i < len(s); {
		r, size := jsonvalue.DecodeRune(s[i:])
		runes = append(runes, r)
		widths = append(widths, size)
		i += size
	}
	var b strings.Builder
	offset := 0
	for i, r := range runes {
		switch {
		case r == 0x130:
			b.WriteString("i\u0307")
		case r == 0x3a3 && finalSigma(runes, i):
			b.WriteRune(0x3c2)
		case r >= 0xd800 && r <= 0xdfff:
			b.WriteString(s[offset : offset+widths[i]])
		default:
			b.WriteRune(unicode.ToLower(r))
		}
		offset += widths[i]
	}
	return b.String()
}

// finalSigma applies Unicode's Final_Sigma condition as Python does: a cased
// letter before the sigma and none after it, skipping case-ignorable
// characters on both sides.
func finalSigma(runes []rune, i int) bool {
	j := i - 1
	for j >= 0 && caseIgnorable(runes[j]) {
		j--
	}
	if j < 0 || !cased(runes[j]) {
		return false
	}
	j = i + 1
	for j < len(runes) && caseIgnorable(runes[j]) {
		j++
	}
	return j == len(runes) || !cased(runes[j])
}

func cased(r rune) bool {
	return unicode.In(r, unicode.Lu, unicode.Ll, unicode.Lt, unicode.Other_Lowercase, unicode.Other_Uppercase)
}

// wordBreakMid lists the MidLetter, MidNumLet and Single_Quote characters,
// which Unicode counts as case-ignorable.
var wordBreakMid = map[rune]bool{
	'\'': true, '.': true, ':': true, 0xb7: true, 0x387: true, 0x55f: true, 0x5f4: true,
	0x2018: true, 0x2019: true, 0x2024: true, 0x2027: true, 0xfe13: true, 0xfe52: true,
	0xfe55: true, 0xff07: true, 0xff0e: true, 0xff1a: true,
}

func caseIgnorable(r rune) bool {
	return wordBreakMid[r] || unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk)
}

// isAlnum reports Python's str.isalnum(): not empty, and every character a
// letter or a number.
func isAlnum(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); {
		r, size := jsonvalue.DecodeRune(s[i:])
		i += size
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) {
			return false
		}
	}
	return true
}

// decodeBase64Strict decodes like Python's base64.b64decode(validate=True):
// only [A-Za-z0-9+/]*={0,2} is accepted.
func decodeBase64Strict(text string) (string, bool) {
	body := strings.TrimRight(text, "=")
	if len(text)-len(body) > 2 {
		return "", false
	}
	for i := 0; i < len(body); i++ {
		if !isStdBase64Char(body[i]) {
			return "", false
		}
	}
	raw, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return "", false
	}
	return string(raw), true
}
