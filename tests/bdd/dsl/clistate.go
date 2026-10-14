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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CLIStatePath returns the nvcf-cli state file for a --config path, mirroring
// the CLI: ~/.nvcf-cli.<config basename without extension>.state.
func CLIStatePath(home, configPath string) string {
	name := strings.TrimSuffix(filepath.Base(configPath), filepath.Ext(configPath))
	return filepath.Join(home, fmt.Sprintf(".nvcf-cli.%s.state", name))
}

// CLIStateToken reads the admin token that `nvcf-cli init` stored in the
// state file at path. It fails when the file or the token is missing.
func CLIStateToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read nvcf-cli state: %w", err)
	}
	var state struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return "", fmt.Errorf("parse nvcf-cli state %s: %w", path, err)
	}
	if state.Token == "" {
		return "", fmt.Errorf("nvcf-cli state %s has no admin token; run nvcf-cli init first", path)
	}
	return state.Token, nil
}
