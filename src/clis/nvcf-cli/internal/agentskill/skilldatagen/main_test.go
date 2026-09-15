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

package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"testing"
)

func TestBuildSkillZipProducesSortedManifestAndDataFiles(t *testing.T) {
	files := []skillFile{
		{Path: "nvcf-self-managed-installation/SKILL.md", Body: []byte("install skill\n")},
		{Path: "nvcf-self-managed-cli/SKILL.md", Body: []byte("cli skill\n")},
	}

	zipBytes, manifest, err := buildSkillZip(files)
	if err != nil {
		t.Fatalf("buildSkillZip: %v", err)
	}
	repeatZipBytes, _, err := buildSkillZip([]skillFile{files[1], files[0]})
	if err != nil {
		t.Fatalf("repeat buildSkillZip: %v", err)
	}
	if !bytes.Equal(zipBytes, repeatZipBytes) {
		t.Fatal("buildSkillZip output changed when input order changed")
	}

	if manifest.GeneratedAt != deterministicGeneratedAt {
		t.Fatalf("GeneratedAt = %q, want %q", manifest.GeneratedAt, deterministicGeneratedAt)
	}
	if manifest.TotalFiles != 2 {
		t.Fatalf("TotalFiles = %d, want 2", manifest.TotalFiles)
	}
	if len(manifest.Files) != 2 {
		t.Fatalf("Files length = %d, want 2", len(manifest.Files))
	}
	if manifest.Files[0].Path != "nvcf-self-managed-cli/SKILL.md" {
		t.Fatalf("Files[0].Path = %q, want sorted CLI skill first", manifest.Files[0].Path)
	}

	wantSum := sha256.Sum256([]byte("cli skill\n"))
	if manifest.Files[0].SHA256 != hex.EncodeToString(wantSum[:]) {
		t.Fatalf("Files[0].SHA256 = %q, want %q", manifest.Files[0].SHA256, hex.EncodeToString(wantSum[:]))
	}
	if manifest.Files[0].Size != len("cli skill\n") {
		t.Fatalf("Files[0].Size = %d, want %d", manifest.Files[0].Size, len("cli skill\n"))
	}

	reader, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	manifestBody, err := fs.ReadFile(reader, "data/manifest.json")
	if err != nil {
		t.Fatalf("read manifest from zip: %v", err)
	}
	var fromZip manifestJSON
	if err := json.Unmarshal(manifestBody, &fromZip); err != nil {
		t.Fatalf("unmarshal zipped manifest: %v", err)
	}
	if fromZip.TotalBytes != len("cli skill\n")+len("install skill\n") {
		t.Fatalf("TotalBytes = %d, want source byte total", fromZip.TotalBytes)
	}
	for name, want := range map[string]string{
		"data/nvcf-self-managed-cli/SKILL.md":          "cli skill\n",
		"data/nvcf-self-managed-installation/SKILL.md": "install skill\n",
	} {
		body, err := fs.ReadFile(reader, name)
		if err != nil {
			t.Fatalf("read %s from zip: %v", name, err)
		}
		if string(body) != want {
			t.Fatalf("%s body = %q, want %q", name, string(body), want)
		}
	}
}

func TestValidateSkillPathRejectsUnsafeAndUnexpectedPaths(t *testing.T) {
	cases := []string{
		"../SKILL.md",
		"/nvcf-self-managed-cli/SKILL.md",
		"nvcf-self-managed-cli/../SKILL.md",
		"nvcf-self-managed-cli/.secret",
		"nvcf-self-managed-installation/_draft.md",
		"other-skill/SKILL.md",
	}

	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			if err := validateSkillPath(path); err == nil {
				t.Fatalf("validateSkillPath(%q) = nil, want error", path)
			}
		})
	}
}
