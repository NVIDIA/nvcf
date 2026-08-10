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

package agentskill

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestLoadManifest_OK(t *testing.T) {
	m, err := LoadManifest(FS())
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", m.SchemaVersion)
	}
	if m.TotalFiles < 1 {
		t.Errorf("TotalFiles = %d, want >= 1", m.TotalFiles)
	}
	if len(m.Files) == 0 {
		t.Error("Files is empty")
	}
	// Verify every entry has a non-empty path and SHA256.
	for i, mf := range m.Files {
		if mf.Path == "" {
			t.Errorf("Files[%d].Path is empty", i)
		}
		if mf.SHA256 == "" {
			t.Errorf("Files[%d].SHA256 is empty (path=%q)", i, mf.Path)
		}
	}
}

func TestVerify_OK(t *testing.T) {
	if err := Verify(FS()); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

// buildMapFSFromEmbedded builds a fstest.MapFS that mirrors the real embedded
// data/ tree so tests can mutate individual entries without touching the real FS.
func buildMapFSFromEmbedded(t *testing.T) (fstest.MapFS, *Manifest) {
	t.Helper()
	realFS := FS()
	m, err := LoadManifest(realFS)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}

	mapFS := make(fstest.MapFS)

	// Copy manifest.json.
	manifestData, err := fs.ReadFile(realFS, "data/manifest.json")
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}
	mapFS["data/manifest.json"] = &fstest.MapFile{Data: manifestData}

	// Copy every file listed in the manifest.
	for _, mf := range m.Files {
		body, err := fs.ReadFile(realFS, "data/"+mf.Path)
		if err != nil {
			t.Fatalf("read %s: %v", mf.Path, err)
		}
		mapFS["data/"+mf.Path] = &fstest.MapFile{Data: body}
	}

	return mapFS, m
}

func TestVerify_DetectsCorruption(t *testing.T) {
	mapFS, m := buildMapFSFromEmbedded(t)

	// Flip one byte in the first file's content.
	target := "data/" + m.Files[0].Path
	original := mapFS[target].Data
	corrupted := make([]byte, len(original))
	copy(corrupted, original)
	corrupted[0] ^= 0xFF
	mapFS[target] = &fstest.MapFile{Data: corrupted}

	err := Verify(mapFS)
	if err == nil {
		t.Fatal("Verify: expected error for corrupted file, got nil")
	}
	if !strings.Contains(err.Error(), m.Files[0].Path) {
		t.Errorf("error %q should mention %q", err.Error(), m.Files[0].Path)
	}
}

func TestVerify_DetectsMissingFile(t *testing.T) {
	mapFS, m := buildMapFSFromEmbedded(t)

	// Remove the first file from the MapFS (but keep the manifest referencing it).
	target := "data/" + m.Files[0].Path
	delete(mapFS, target)

	err := Verify(mapFS)
	if err == nil {
		t.Fatal("Verify: expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "verify "+m.Files[0].Path) {
		t.Errorf("error %q should contain %q", err.Error(), "verify "+m.Files[0].Path)
	}
}

func TestVerify_RejectsBadSchemaVersion(t *testing.T) {
	mapFS, m := buildMapFSFromEmbedded(t)

	// Re-marshal the manifest with schemaVersion 2.
	badManifest := *m
	badManifest.SchemaVersion = 2
	data, err := json.Marshal(badManifest)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	mapFS["data/manifest.json"] = &fstest.MapFile{Data: data}

	err = Verify(mapFS)
	if err == nil {
		t.Fatal("Verify: expected error for bad schemaVersion, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported manifest schemaVersion") {
		t.Errorf("error %q should mention 'unsupported manifest schemaVersion'", err.Error())
	}
}

// TestLoadManifest_RejectsTotalFilesMismatch covers the cross-check that
// catches a manifest whose totalFiles count doesn't match the listed entries
// — the failure mode where a partial bundle ships and Verify silently passes
// because it only walks listed files.
func TestLoadManifest_RejectsTotalFilesMismatch(t *testing.T) {
	mapFS, m := buildMapFSFromEmbedded(t)

	// Lie about totalFiles: declare 999 but keep the real entries unchanged.
	bad := *m
	bad.TotalFiles = 999
	data, err := json.Marshal(bad)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	mapFS["data/manifest.json"] = &fstest.MapFile{Data: data}

	_, err = LoadManifest(mapFS)
	if err == nil {
		t.Fatal("LoadManifest: expected error for totalFiles mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "totalFiles=999") {
		t.Errorf("error %q should mention 'totalFiles=999'", err.Error())
	}
}
