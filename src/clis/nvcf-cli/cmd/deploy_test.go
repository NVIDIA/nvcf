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

package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"nvcf-cli/internal/state"
)

func TestApplySavedDeployContextFillsMissingFunctionIDs(t *testing.T) {
	config := &DeployConfig{}
	currentState := &state.State{
		FunctionID: "func-from-state",
		VersionID:  "ver-from-state",
	}

	applySavedDeployContext(config, currentState)

	if config.FunctionID != "func-from-state" {
		t.Fatalf("FunctionID = %q, want func-from-state", config.FunctionID)
	}
	if config.VersionID != "ver-from-state" {
		t.Fatalf("VersionID = %q, want ver-from-state", config.VersionID)
	}
}

func TestApplySavedDeployContextKeepsExplicitFunctionIDs(t *testing.T) {
	config := &DeployConfig{
		FunctionID: "func-explicit",
		VersionID:  "ver-explicit",
	}
	currentState := &state.State{
		FunctionID: "func-from-state",
		VersionID:  "ver-from-state",
	}

	applySavedDeployContext(config, currentState)

	if config.FunctionID != "func-explicit" {
		t.Fatalf("FunctionID = %q, want func-explicit", config.FunctionID)
	}
	if config.VersionID != "ver-explicit" {
		t.Fatalf("VersionID = %q, want ver-explicit", config.VersionID)
	}
}

func TestSaveStateForCurrentCommandPersistsFunctionContextForConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	previousCfgFile := cfgFile
	previousManager := configStateManager
	previousManagerKey := configStateManagerKey
	t.Cleanup(func() {
		cfgFile = previousCfgFile
		configStateManager = previousManager
		configStateManagerKey = previousManagerKey
	})

	configPath := filepath.Join(t.TempDir(), "nvcf-cli-local.yaml")
	if err := os.WriteFile(configPath, []byte("base_http_url: http://api.localhost:8080\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfgFile = configPath
	configStateManager = nil
	configStateManagerKey = ""

	if err := LoadStateForCurrentCommand(); err != nil {
		t.Fatalf("load state: %v", err)
	}

	SetCurrentFunction("func-from-create", "ver-from-create", "bdd-function")
	SetCurrentTokens("admin-token", "", time.Now().Add(time.Hour), time.Time{})
	if err := SaveStateForCurrentCommand(); err != nil {
		t.Fatalf("save state: %v", err)
	}

	sm := state.GetStateManagerForConfig(configPath)
	if err := sm.Load(); err != nil {
		t.Fatalf("reload state: %v", err)
	}
	saved := sm.GetState()
	if saved.ConfigFile != configPath {
		t.Fatalf("ConfigFile = %q, want %q", saved.ConfigFile, configPath)
	}
	if saved.FunctionID != "func-from-create" {
		t.Fatalf("FunctionID = %q, want func-from-create", saved.FunctionID)
	}
	if saved.VersionID != "ver-from-create" {
		t.Fatalf("VersionID = %q, want ver-from-create", saved.VersionID)
	}
	if saved.Token != "admin-token" {
		t.Fatalf("Token = %q, want admin-token", saved.Token)
	}
}
