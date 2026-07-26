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
// Earlier versions scanned text and were repeatedly found to mishandle a syntax
// case by treating an input they could not interpret as one that was not there.
// Parsing removed that class for surface syntax, but the same failure mode
// returns one level up if the analysis ignores forms it does not model: a call
// reached through a chained expression or a reassigned name, or declarations
// living in an included file, are all silent undercounts.
//
// The rule here is that anything not understood is an error, never an omission.
// Bindings are resolved where they can be, and where they cannot the caller
// fails with the location.

// maxResolveDepth bounds constant resolution so a cyclic binding cannot loop.
const maxResolveDepth = 16

// Module is a parsed MODULE.bazel plus its module-level bindings.
type Module struct {
	Path string
	File *syntax.File

	// bindings maps module-level names to their assigned expression, so a
	// constant used as an attribute value can be resolved to its literal.
	bindings map[string]syntax.Expr
}

// NewModule parses a MODULE.bazel file and records its top-level bindings.
func NewModule(path, src string) (*Module, error) {
	f, err := syntax.Parse(path, src, 0)
	if err != nil {
		return nil, fmt.Errorf("cannot parse %s: %w", path, err)
	}
	m := &Module{Path: path, File: f, bindings: map[string]syntax.Expr{}}
	for _, stmt := range f.Stmts {
		assign, ok := stmt.(*syntax.AssignStmt)
		if !ok || assign.Op != syntax.EQ {
			continue
		}
		if lhs, ok := assign.LHS.(*syntax.Ident); ok {
			m.bindings[lhs.Name] = assign.RHS
		}
	}
	return m, nil
}

// Includes returns the arguments of any top-level include() call.
//
// MODULE.bazel may split declarations across included files. This tool reads
// one file at a time, so declarations in an included file would simply not be
// counted. Callers must fail rather than report a total that silently omits
// them.
func (m *Module) Includes() []string {
	var included []string
	for _, stmt := range m.File.Stmts {
		expr, ok := stmt.(*syntax.ExprStmt)
		if !ok {
			continue
		}
		call, ok := expr.X.(*syntax.CallExpr)
		if !ok {
			continue
		}
		fn, ok := call.Fn.(*syntax.Ident)
		if !ok || fn.Name != "include" {
			continue
		}
		label := "unknown"
		if len(call.Args) > 0 {
			if v, ok := m.ResolveString(call.Args[0]); ok {
				label = v
			}
		}
		included = append(included, label)
	}
	return included
}

// ResolveString returns the string value of an expression when it can be
// determined statically, following module-level constant bindings.
//
// A literal is returned directly. A name is followed to its binding, so
// `IMG = "nvcr.io/x"` used as `image = IMG` resolves. Anything else, including
// concatenations and function calls, is not a static string.
func (m *Module) ResolveString(e syntax.Expr) (string, bool) {
	return m.resolveString(e, 0)
}

func (m *Module) resolveString(e syntax.Expr, depth int) (string, bool) {
	if depth > maxResolveDepth {
		return "", false
	}
	switch v := e.(type) {
	case *syntax.Literal:
		s, ok := v.Value.(string)
		return s, ok
	case *syntax.Ident:
		bound, ok := m.bindings[v.Name]
		if !ok {
			return "", false
		}
		return m.resolveString(bound, depth+1)
	}
	return "", false
}

// extensionOf reports which extension an expression denotes, if any.
//
// It handles a name bound by use_extension, a name bound to another such name,
// and a use_extension call used directly as a receiver. Each of these is valid
// and each was previously not matched at all.
func (m *Module) extensionOf(e syntax.Expr, depth int) (string, bool) {
	if depth > maxResolveDepth {
		return "", false
	}
	switch v := e.(type) {
	case *syntax.CallExpr:
		fn, ok := v.Fn.(*syntax.Ident)
		if !ok || fn.Name != "use_extension" {
			return "", false
		}
		var positional []syntax.Expr
		for _, arg := range v.Args {
			if binary, isKeyword := arg.(*syntax.BinaryExpr); isKeyword && binary.Op == syntax.EQ {
				continue
			}
			positional = append(positional, arg)
		}
		if len(positional) < 2 {
			return "", false
		}
		return m.resolveString(positional[1], depth+1)
	case *syntax.Ident:
		if bound, ok := m.bindings[v.Name]; ok {
			return m.extensionOf(bound, depth+1)
		}
		// A name that is never assigned is the extension itself, which is how
		// files that do not alias are written.
		return v.Name, true
	}
	return "", false
}

// Call is one resolved extension method call, such as oci.pull.
type Call struct {
	// Attrs holds keyword arguments resolvable to a string, whether written as
	// a literal or as a module-level constant.
	Attrs map[string]string
	// NonLiteral holds keyword arguments present but not resolvable to a
	// string, for example a concatenation or a function call. These are
	// recorded rather than dropped so callers fail instead of mistaking them
	// for absent attributes.
	NonLiteral map[string]bool
	// Line is the 1-indexed line of the call, for error messages.
	Line int32
}

// Attr returns a string attribute.
//
// literal is false when the attribute is present but not resolvable to a
// string; found is false when it is absent. Callers must distinguish the two.
func (c Call) Attr(key string) (value string, literal bool, found bool) {
	if v, ok := c.Attrs[key]; ok {
		return v, true, true
	}
	if c.NonLiteral[key] {
		return "", false, true
	}
	return "", false, false
}

// FindCalls returns every call to <extension>.<method>, resolving aliases,
// reassignments, and chained use_extension receivers.
func (m *Module) FindCalls(extension, method string) []Call {
	var calls []Call
	syntax.Walk(m.File, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		dot, ok := call.Fn.(*syntax.DotExpr)
		if !ok || dot.Name.Name != method {
			return true
		}
		if ext, ok := m.extensionOf(dot.X, 0); !ok || ext != extension {
			return true
		}
		calls = append(calls, m.newCall(call))
		return true
	})
	sort.Slice(calls, func(i, j int) bool { return calls[i].Line < calls[j].Line })
	return calls
}

func (m *Module) newCall(expr *syntax.CallExpr) Call {
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
		// The parser has already decoded escapes and triple-quoted strings, and
		// ResolveString follows constant bindings, so this is the effective
		// value rather than its source text.
		if v, ok := m.resolveString(binary.Y, 0); ok {
			c.Attrs[key.Name] = v
			continue
		}
		c.NonLiteral[key.Name] = true
	}
	return c
}
