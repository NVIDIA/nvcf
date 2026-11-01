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

package nv

import (
	"os"
	"regexp"
	"strings"
)

const (
	undetermined = "undetermined"
)

var (
	parseActionAndPathRegex = regexp.MustCompile(`^ess\.(list|pki|read|write)\(([^)]+)\)$`)
)

// ParseActionAndPath parses a string to extract the operation and path.
// Returns "undetermined" if operation or path cannot be extracted as is.
func ParseActionAndPath(input string) (string, string) {
	// default values
	operation := undetermined
	path := undetermined

	matches := parseActionAndPathRegex.FindStringSubmatch(strings.TrimSpace(input))
	if matches != nil {
		operation = matches[1]
		path = matches[2]
	}

	return strings.ToLower(operation), path
}

// IsInInitMode checks the env and decides if current run is as init or non-init.
func IsInInitMode() bool {
	return strings.EqualFold(os.Getenv(EnvESSAgentInit), "true")
}
