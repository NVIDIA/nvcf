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
	"sort"

	"go.starlark.net/syntax"
)

// Starlark analysis of MODULE.bazel files, using a real parser.
//
// Earlier versions scanned text: first with regular expressions, then with a
// hand-written balanced-parenthesis scanner. Review found a new syntax case
// each round that the scanner mishandled, always in the same direction, by
// treating an input it could not interpret as one that was not there: an
// indented closing parenthesis, single-quoted attributes, whitespace before the
// parenthesis, a concatenation reported as its first operand. Each fix was
// correct and the next case appeared elsewhere, because a scanner approximates
// a grammar and the gap between them is unbounded.
//
// This uses go.starlark.net/syntax, the parser for the language these files are
// actually written in. Escapes and triple-quoted strings are decoded by the
// parser rather than by us, and extension aliases are resolved from the file's
// own assignments instead of assuming a conventional receiver name.

// Call is one resolved extension method call, such as oci.pull.
type Call struct {
	// Attrs holds keyword arguments whose values are string literals, already
	// decoded by the parser.
	Attrs map[string]string
	// NonLiteral holds keyword arguments that are present but are not string
	// literals, for example `image = BASE_IMAGE` or a concatenation. These are
	// recorded rather than dropped, so callers can fail on them instead of
	// mistaking them for absent attributes.
	NonLiteral map[string]bool
	// Line is the 1-indexed line of the call, for error messages.
	Line int32
}

// Attr returns a string-literal attribute.
//
// literal is false when the attribute is present but is not a string literal;
// found is false when it is absent. Callers must distinguish the two, because
// treating an uninterpretable value as an absent one is exactly the defect this
// file exists to prevent.
func (c Call) Attr(key string) (value string, literal bool, found bool) {
	if v, ok := c.Attrs[key]; ok {
		return v, true, true
	}
	if c.NonLiteral[key] {
		return "", false, true
	}
	return "", false, false
}

// ParseModule parses a MODULE.bazel file into an AST.
func ParseModule(path, src string) (*syntax.File, error) {
	f, err := syntax.Parse(path, src, 0)
	if err != nil {
		return nil, fmt.Errorf("cannot parse %s: %w", path, err)
	}
	return f, nil
}

// ExtensionAliases maps local variable names to the extension they are bound to
// by use_extension.
//
// MODULE.bazel conventionally writes `oci = use_extension(..., "oci")`, but the
// local name is arbitrary: `images = use_extension(..., "oci")` makes
// `images.pull(...)` the same call. Hard-coding the conventional receiver meant
// such files reported zero declarations.
//
// The extension is identified by the second positional argument to
// use_extension, which names the extension within the module file.
func ExtensionAliases(f *syntax.File) map[string]string {
	aliases := map[string]string{}
	for _, stmt := range f.Stmts {
		assign, ok := stmt.(*syntax.AssignStmt)
		if !ok || assign.Op != syntax.EQ {
			continue
		}
		lhs, ok := assign.LHS.(*syntax.Ident)
		if !ok {
			continue
		}
		call, ok := assign.RHS.(*syntax.CallExpr)
		if !ok {
			continue
		}
		fn, ok := call.Fn.(*syntax.Ident)
		if !ok || fn.Name != "use_extension" {
			continue
		}
		var positional []string
		for _, arg := range call.Args {
			if binary, isKeyword := arg.(*syntax.BinaryExpr); isKeyword && binary.Op == syntax.EQ {
				continue
			}
			lit, ok := arg.(*syntax.Literal)
			if !ok {
				continue
			}
			if s, ok := lit.Value.(string); ok {
				positional = append(positional, s)
			}
		}
		if len(positional) >= 2 {
			aliases[lhs.Name] = positional[1]
		}
	}
	return aliases
}

// FindCalls returns every call to <extension>.<method> in the file, resolving
// local aliases established by use_extension.
//
// extension is the name as it appears in use_extension, for example "oci" or
// "go_sdk". A call written directly against that name is also matched, so files
// that do not alias still work.
func FindCalls(f *syntax.File, extension, method string) []Call {
	receivers := map[string]bool{extension: true}
	for local, ext := range ExtensionAliases(f) {
		if ext == extension {
			receivers[local] = true
		}
	}

	var calls []Call
	syntax.Walk(f, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		dot, ok := call.Fn.(*syntax.DotExpr)
		if !ok || dot.Name.Name != method {
			return true
		}
		recv, ok := dot.X.(*syntax.Ident)
		if !ok || !receivers[recv.Name] {
			return true
		}
		calls = append(calls, newCall(call))
		return true
	})
	sort.Slice(calls, func(i, j int) bool { return calls[i].Line < calls[j].Line })
	return calls
}

func newCall(expr *syntax.CallExpr) Call {
	c := Call{
		Attrs:      map[string]string{},
		NonLiteral: map[string]bool{},
		Line:       expr.Lparen.Line,
	}
	for _, arg := range expr.Args {
		binary, ok := arg.(*syntax.BinaryExpr)
		if !ok || binary.Op != syntax.EQ {
			continue
		}
		key, ok := binary.X.(*syntax.Ident)
		if !ok {
			continue
		}
		// The parser has already decoded escapes and triple-quoted strings, so
		// this is the actual string value rather than its source text.
		if lit, ok := binary.Y.(*syntax.Literal); ok {
			if s, ok := lit.Value.(string); ok {
				c.Attrs[key.Name] = s
				continue
			}
		}
		c.NonLiteral[key.Name] = true
	}
	return c
}
