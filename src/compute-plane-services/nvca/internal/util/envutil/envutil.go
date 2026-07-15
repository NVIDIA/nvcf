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

package envutil

import (
	"encoding/base64"
	"sort"
	"strings"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
)

// ApplyEnvOverrides decodes a base64-encoded environment string,
// applies the provided overrides, and returns a new base64-encoded string.
// The envB64 is expected to be a base64-encoded text with KEY=value lines.
// Overrides take precedence over existing values.
func ApplyEnvOverrides(envB64 string, overrides map[string]string) (string, error) {
	if len(overrides) == 0 {
		return envB64, nil
	}
	overrides = NormalizeEnvOverrides(overrides)

	// Decode the existing environment using the translate library's decoder.
	envs, err := common.DecodeEnvironmentB64(envB64, common.EnvDecoderText)
	if err != nil {
		return "", err
	}

	// Apply overrides
	for k, v := range overrides {
		envs[k] = v
	}

	// Re-encode to text format
	return EncodeEnvB64(envs), nil
}

// NormalizeEnvOverrides normalizes configured environment override names.
// NVCA config rendering lowercases map keys, while the launch specs expect
// conventional upper-case environment variable names.
func NormalizeEnvOverrides(overrides map[string]string) map[string]string {
	if len(overrides) == 0 {
		return overrides
	}

	normalized := make(map[string]string, len(overrides))
	for k, v := range overrides {
		normalized[NormalizeEnvOverrideName(k)] = v
	}
	return normalized
}

// NormalizeEnvOverrideName returns the canonical name for an environment override.
func NormalizeEnvOverrideName(name string) string {
	return strings.ToUpper(name)
}

// EncodeEnvB64 encodes a map of environment variables to a base64 text string.
// Format: KEY=value separated by newlines.
func EncodeEnvB64(envs map[string]string) string {
	if len(envs) == 0 {
		return ""
	}

	// Sort keys for deterministic output
	keys := make([]string, 0, len(envs))
	for k := range envs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteString("=")
		sb.WriteString(envs[k])
		sb.WriteString("\n")
	}

	return base64.StdEncoding.EncodeToString([]byte(sb.String()))
}
