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

package criu

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStartDumpStreamer_MissingBinaries(t *testing.T) {
	tmpDir := t.TempDir()

	// Neither binary exists — should return nil cleanup (fallback)
	cleanup, err := startDumpStreamer(tmpDir, tmpDir)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if cleanup != nil {
		t.Fatal("expected nil cleanup when binaries are missing")
	}
}

func TestStartDumpStreamer_MissingLz4Only(t *testing.T) {
	tmpDir := t.TempDir()

	// Create fake streamer but no lz4
	if err := os.WriteFile(filepath.Join(tmpDir, "criu-image-streamer"), []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}

	cleanup, err := startDumpStreamer(tmpDir, tmpDir)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if cleanup != nil {
		t.Fatal("expected nil cleanup when lz4 is missing")
	}
}
