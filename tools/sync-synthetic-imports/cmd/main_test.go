// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadManifestNormalizesNativeRoots(t *testing.T) {
	tmp := t.TempDir()
	manifestPath := filepath.Join(tmp, "imports.yaml")
	if err := os.WriteFile(manifestPath, []byte(`imports:
  - path: src/libraries/go/lib
    authoritative_source: native
  - path: src/services/demo
    repo: https://example.com/demo.git
    commit: deadbeef
`), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	entries, err := loadManifest(manifestPath)
	if err != nil {
		t.Fatalf("loadManifest failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("loadManifest returned %d entries, want 2", len(entries))
	}

	if entries[0].AuthoritativeSource != "native" {
		t.Fatalf("first entry authoritative source = %q, want native", entries[0].AuthoritativeSource)
	}
	if entries[0].Repo != "" {
		t.Fatalf("first entry repo = %q, want empty", entries[0].Repo)
	}

	if entries[1].AuthoritativeSource != "upstream" {
		t.Fatalf("second entry authoritative source = %q, want upstream", entries[1].AuthoritativeSource)
	}
}
