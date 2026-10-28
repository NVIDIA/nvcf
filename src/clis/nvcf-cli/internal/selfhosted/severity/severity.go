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

// Package severity grades a check row. Both the check runner and the
// renderers import it, so every count, glyph and exit code follows one rule.
package severity

// Severity is how much a failed check matters.
type Severity string

const (
	Info    Severity = "info"
	Warning Severity = "warning"
	Error   Severity = "error"
)

// Grade is how a row counts in every tally.
type Grade int

const (
	Pass Grade = iota
	Warn
	Fail
)

// Of grades one row. A miss blocks the run unless its severity is exactly
// info or warning, so a misspelled or empty severity cannot downgrade a
// failure.
func Of(passed bool, s Severity) Grade {
	switch {
	case passed:
		return Pass
	case s == Info || s == Warning:
		return Warn
	default:
		return Fail
	}
}
