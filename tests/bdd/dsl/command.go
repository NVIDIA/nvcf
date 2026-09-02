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

package dsl

import "strings"

// BuildCommand quotes each argument for the runner's POSIX shlex parser and
// joins them without interpreting their values.
func BuildCommand(args ...string) string {
	quoted := make([]string, len(args))
	for index, arg := range args {
		quoted[index] = quoteCommandArg(arg)
	}
	return strings.Join(quoted, " ")
}

func quoteCommandArg(value string) string {
	if isCommandArgSafe(value) {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func isCommandArgSafe(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' ||
			char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' ||
			strings.ContainsRune("_./:@%+=,-", char) {
			continue
		}
		return false
	}
	return true
}
