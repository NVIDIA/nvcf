/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package main

import (
	"fmt"
	"strings"
)

const (
	ansiRed    = "\033[38;2;255;0;0m"
	ansiGreen  = "\033[38;2;0;255;0m"
	ansiBlue   = "\033[38;2;0;0;255m"
	ansiYellow = "\033[38;2;255;255;0m"
	ansiWhite  = "\033[38;2;255;255;255m"
	ansiReset  = "\033[0m"
)

// colorResult wraps a MetricsValidationResult in ANSI colour codes for terminal output.
func colorResult(result MetricsValidationResult) string {
	switch result {
	case ResultValid:
		return ansiGreen + string(result) + ansiReset
	case ResultValidWithWarnings:
		return ansiYellow + string(result) + ansiReset
	case ResultInvalid:
		return ansiRed + string(result) + ansiReset
	case ResultSkipped:
		return ansiWhite + string(result) + ansiReset
	default:
		return string(result)
	}
}

// printDiffColorized prints unified-diff-style lines with ANSI colour codes.
func printDiffColorized(diff []string) {
	fmt.Println("Printing diff with colorized output:")
	if len(diff) == 0 {
		fmt.Println("\tN/A")
		return
	}
	for _, line := range diff {
		switch {
		case strings.HasPrefix(line, "@@"):
			fmt.Println(ansiBlue + line + ansiWhite)
		case strings.HasPrefix(line, "- "):
			fmt.Println(ansiRed + line + ansiWhite)
		case strings.HasPrefix(line, "+ "):
			fmt.Println(ansiGreen + line + ansiWhite)
		case strings.HasPrefix(line, " "):
			fmt.Println(ansiWhite + line + ansiWhite)
		default:
			fmt.Println(line)
		}
	}
}
