/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package dsl carries the pure helpers behind the strict Gherkin DSL.
// Nothing in this package performs I/O coordination, captures step state,
// or speaks Godog. Each function is unit-testable in isolation.
package dsl

import (
	"os"
	"regexp"
)

// braced matches the only environment variable form the DSL recognizes:
// ${UPPERCASE_OR_DIGITS_UNDERSCORE}. A bare $word is left literal so a
// credential string like "$oauthtoken:${NGC_API_KEY}" only expands the
// API key half. This is the contract spelled out in PLAN.md; do not
// switch to os.ExpandEnv (which would also expand $oauthtoken).
var braced = regexp.MustCompile(`\$\{([A-Z0-9_]+)\}`)

// Interpolate replaces every ${VAR} occurrence in s with os.Getenv(VAR).
// Missing env vars expand to the empty string. Bare $word is preserved
// as a literal. Returns the resolved string.
func Interpolate(s string) string {
	return braced.ReplaceAllStringFunc(s, func(match string) string {
		name := match[2 : len(match)-1]
		return os.Getenv(name)
	})
}
