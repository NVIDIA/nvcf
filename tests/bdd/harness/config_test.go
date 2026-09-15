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

package harness

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveConfigPopulatesEveryPath(t *testing.T) {
	cfg, err := ResolveConfig()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.RepoRoot == "" {
		t.Fatal("repo root empty")
	}
	if !filepath.IsAbs(cfg.RepoRoot) {
		t.Fatalf("repo root not absolute: %s", cfg.RepoRoot)
	}
	for name, path := range map[string]string{
		"CLIPath":        cfg.CLIPath,
		"OutDir":         cfg.OutDir,
		"LedgerDir":      cfg.LedgerDir,
		"CommandLogDir":  cfg.CommandLogDir,
		"DiagnosticsDir": cfg.DiagnosticsDir,
	} {
		if path == "" {
			t.Fatalf("%s empty", name)
		}
		if !strings.HasPrefix(path, cfg.RepoRoot) {
			t.Fatalf("%s not under repo root: %s", name, path)
		}
	}
}
