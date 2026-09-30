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

import (
	"fmt"
	"os"
	"strings"
)

// RequireEnvironmentVariables fails on the first named variable whose value
// is empty. Names are trimmed so table formatting whitespace is not semantic.
func RequireEnvironmentVariables(names []string) error {
	if len(names) == 0 {
		return fmt.Errorf("at least one environment variable name is required")
	}
	for _, rawName := range names {
		name := strings.TrimSpace(rawName)
		if name == "" {
			return fmt.Errorf("environment variable name must not be empty")
		}
		if os.Getenv(name) == "" {
			return fmt.Errorf("environment variable %q is not set", name)
		}
	}
	return nil
}
