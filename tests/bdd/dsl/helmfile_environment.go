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
	"path/filepath"
	"regexp"
)

var helmfileEnvironmentName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

var helmfileStacks = map[string]struct{}{
	"nvcf-compute-plane": {},
	"observability":      {},
	"self-managed":       {},
}

// HelmfileEnvironmentPath validates a named stack environment and returns its
// destination beneath an absolute repository root. It never depends on the
// process working directory.
func HelmfileEnvironmentPath(repoRoot, stack, environment string) (string, error) {
	if !filepath.IsAbs(repoRoot) {
		return "", fmt.Errorf("repository root must be absolute")
	}
	if _, ok := helmfileStacks[stack]; !ok {
		return "", fmt.Errorf("unsupported Helmfile stack %q", stack)
	}
	if !helmfileEnvironmentName.MatchString(environment) {
		return "", fmt.Errorf("invalid Helmfile environment name %q", environment)
	}
	return filepath.Join(repoRoot, "deploy", "stacks", stack, "environments", environment+".yaml"), nil
}
