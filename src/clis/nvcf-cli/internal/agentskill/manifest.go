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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
)

// Manifest mirrors agent-skill/manifest.json. Field names match the JSON
// schema produced by mcamp/docs's gen-skill-manifest.sh.
type Manifest struct {
	SchemaVersion int            `json:"schemaVersion"`
	GeneratedAt   string         `json:"generatedAt"`
	TotalFiles    int            `json:"totalFiles"`
	TotalBytes    int            `json:"totalBytes"`
	Files         []ManifestFile `json:"files"`
}

// ManifestFile describes one file in the manifest.
type ManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int    `json:"size"`
}

// LoadManifest parses data/manifest.json from the given filesystem.
func LoadManifest(fsys fs.FS) (*Manifest, error) {
	f, err := fsys.Open("data/manifest.json")
	if err != nil {
		return nil, fmt.Errorf("open manifest: %w", err)
	}
	defer f.Close()
	body, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("unmarshal manifest: %w", err)
	}
	if m.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported manifest schemaVersion %d (want 1)", m.SchemaVersion)
	}
	// Cross-check totalFiles against the list length. Catches the case where
	// a manifest entry is missing — without this guard, Verify would walk only
	// the listed files and silently let a partial bundle ship.
	if m.TotalFiles != len(m.Files) {
		return nil, fmt.Errorf("manifest totalFiles=%d but listed %d files", m.TotalFiles, len(m.Files))
	}
	return &m, nil
}

// Verify recomputes SHA256 over each file referenced in the manifest and
// compares it to the manifest entry. Returns the first mismatch as an error.
//
// Only the files listed in the manifest are checked — the manifest is the
// source of truth for "what's in the bundle." If data/ contains a file that
// the manifest doesn't reference, Verify ignores it. Conversely, a manifest
// entry whose target file is missing from data/ is reported as a mismatch.
func Verify(fsys fs.FS) error {
	m, err := LoadManifest(fsys)
	if err != nil {
		return err
	}
	for _, mf := range m.Files {
		body, err := fs.ReadFile(fsys, "data/"+mf.Path)
		if err != nil {
			return fmt.Errorf("verify %s: %w", mf.Path, err)
		}
		sum := sha256.Sum256(body)
		got := hex.EncodeToString(sum[:])
		if got != mf.SHA256 {
			return fmt.Errorf("verify %s: sha256 mismatch (got %s, manifest %s)", mf.Path, got, mf.SHA256)
		}
		if len(body) != mf.Size {
			return fmt.Errorf("verify %s: size mismatch (got %d, manifest %d)", mf.Path, len(body), mf.Size)
		}
	}
	return nil
}
