// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"fmt"
	"strings"
)

// Minimal Starlark call scanning, sufficient for the MODULE.bazel declarations
// this tool measures.
//
// A regular expression is not sufficient here, and trying to use one produced
// real defects: a pattern anchored on a closing parenthesis at column zero
// missed calls with an indented ")", a pattern matching only double quotes
// missed single-quoted attributes, and scanning a call body for any version-like
// token picked up numbers from trailing comments. Balanced-parenthesis scanning
// that understands strings and comments avoids all three by construction.

// Call is one occurrence of name(...) in a source file.
type Call struct {
	// Body is the text between the outer parentheses.
	Body string
	// Line is the 1-indexed line where the call starts, for error messages.
	Line int
}

// FindCalls returns every top-level occurrence of name( ... ) in src.
//
// Parentheses inside string literals and comments do not affect nesting, so a
// call whose closing parenthesis is indented, or whose attributes contain
// parentheses, is still bounded correctly.
func FindCalls(src, name string) ([]Call, error) {
	var calls []Call
	for i := 0; i < len(src); {
		idx := strings.Index(src[i:], name)
		if idx < 0 {
			break
		}
		start := i + idx
		next := start + len(name)
		// Require a call rather than a longer identifier ending in name.
		if start > 0 && isIdentByte(src[start-1]) {
			i = next
			continue
		}
		// Starlark permits whitespace between the callee and the opening
		// parenthesis, so `oci.pull (` is the same call as `oci.pull(`.
		paren := next
		for paren < len(src) && (src[paren] == ' ' || src[paren] == '\t' ||
			src[paren] == '\n' || src[paren] == '\r') {
			paren++
		}
		if paren >= len(src) || src[paren] != '(' {
			i = next
			continue
		}
		if isIdentByte(src[next]) {
			i = next
			continue
		}
		if inCommentOrString(src[:start]) {
			i = next
			continue
		}
		open := paren + 1
		end, err := matchParen(src, open)
		if err != nil {
			return nil, fmt.Errorf("%s at line %d: %w", name, lineOf(src, start), err)
		}
		calls = append(calls, Call{Body: src[open:end], Line: lineOf(src, start)})
		i = end + 1
	}
	return calls, nil
}

func isIdentByte(b byte) bool {
	return b == '_' || b == '.' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func lineOf(src string, pos int) int {
	return strings.Count(src[:pos], "\n") + 1
}

// matchParen returns the index of the parenthesis closing the one just before
// open, ignoring parentheses inside strings and comments.
func matchParen(src string, open int) (int, error) {
	depth := 1
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '#':
			// Comment runs to end of line.
			nl := strings.IndexByte(src[i:], '\n')
			if nl < 0 {
				return 0, fmt.Errorf("unterminated call: comment reaches end of file")
			}
			i += nl
		case '"', '\'':
			end, err := skipString(src, i)
			if err != nil {
				return 0, err
			}
			i = end
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i, nil
			}
		}
	}
	return 0, fmt.Errorf("unterminated call: no matching closing parenthesis")
}

// skipString returns the index of the closing quote of the literal starting at
// start. Triple-quoted strings are handled because Starlark allows them.
func skipString(src string, start int) (int, error) {
	quote := src[start]
	triple := strings.HasPrefix(src[start:], strings.Repeat(string(quote), 3))
	if triple {
		closing := strings.Repeat(string(quote), 3)
		idx := strings.Index(src[start+3:], closing)
		if idx < 0 {
			return 0, fmt.Errorf("unterminated triple-quoted string")
		}
		return start + 3 + idx + 2, nil
	}
	for i := start + 1; i < len(src); i++ {
		switch src[i] {
		case '\\':
			i++
		case quote:
			return i, nil
		case '\n':
			return 0, fmt.Errorf("unterminated string literal")
		}
	}
	return 0, fmt.Errorf("unterminated string literal")
}

// inCommentOrString reports whether the end of prefix falls inside a comment or
// string literal, which would mean a following call token is not real code.
func inCommentOrString(prefix string) bool {
	for i := 0; i < len(prefix); i++ {
		switch prefix[i] {
		case '#':
			nl := strings.IndexByte(prefix[i:], '\n')
			if nl < 0 {
				return true
			}
			i += nl
		case '"', '\'':
			end, err := skipString(prefix, i)
			if err != nil {
				return true
			}
			i = end
		}
	}
	return false
}

// StringAttr returns the value of a `key = "value"` attribute in a call body.
//
// found is false when the attribute is absent. literal is false when the
// attribute is present but bound to something other than a string literal, for
// example `image = BASE_IMAGE`. Callers must treat a non-literal attribute as
// an input they cannot interpret rather than as an absent one; conflating the
// two is what previously let unparseable declarations be reported as zero.
func StringAttr(body, key string) (value string, literal bool, found bool) {
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '#':
			nl := strings.IndexByte(body[i:], '\n')
			if nl < 0 {
				return "", false, false
			}
			i += nl
			continue
		case '"', '\'':
			end, err := skipString(body, i)
			if err != nil {
				return "", false, false
			}
			i = end
			continue
		}
		if !strings.HasPrefix(body[i:], key) {
			continue
		}
		// The key must be a whole word.
		if i > 0 && isIdentByte(body[i-1]) {
			continue
		}
		j := i + len(key)
		if j < len(body) && isIdentByte(body[j]) {
			continue
		}
		for j < len(body) && (body[j] == ' ' || body[j] == '\t') {
			j++
		}
		if j >= len(body) || body[j] != '=' {
			continue
		}
		j++
		for j < len(body) && (body[j] == ' ' || body[j] == '\t' || body[j] == '\n') {
			j++
		}
		if j >= len(body) {
			return "", false, true
		}
		if body[j] != '"' && body[j] != '\'' {
			// Present, but not a string literal.
			return "", false, true
		}
		end, err := skipString(body, j)
		if err != nil {
			return "", false, true
		}
		// The string must be the whole value. `version = "1.25.0" + SUFFIX`
		// starts with a literal but is an expression, and reporting its first
		// operand as the value would be wrong rather than merely imprecise.
		k := end + 1
		for k < len(body) && (body[k] == ' ' || body[k] == '\t' ||
			body[k] == '\n' || body[k] == '\r') {
			k++
		}
		if k < len(body) && body[k] != ',' && body[k] != ')' {
			return "", false, true
		}
		return body[j+1 : end], true, true
	}
	return "", false, false
}
