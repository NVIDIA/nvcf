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
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLedgerRestoresExistingFileBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env.yaml")
	original := "before: true\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ledger := NewLedger(filepath.Join(dir, "snaps"))
	if err := ledger.Snapshot(path); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if err := os.WriteFile(path, []byte("after: true\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := ledger.RestoreAll(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Fatalf("body = %q, want %q", got, original)
	}
}

func TestLedgerDeletesFileThatDidNotExist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env.yaml")

	ledger := NewLedger(filepath.Join(dir, "snaps"))
	if err := ledger.Snapshot(path); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if err := os.WriteFile(path, []byte("created: true\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := ledger.RestoreAll(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file still exists: %v", err)
	}
}

func TestLedgerSnapshotIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env.yaml")
	original := "v1\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ledger := NewLedger(filepath.Join(dir, "snaps"))
	if err := ledger.Snapshot(path); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if err := os.WriteFile(path, []byte("v2\n"), 0o644); err != nil {
		t.Fatalf("write between: %v", err)
	}
	if err := ledger.Snapshot(path); err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	if err := ledger.RestoreAll(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Fatalf("body = %q, want pre-suite original %q", got, original)
	}
}

func TestLedgerRestorePreservesMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.yaml")
	if err := os.WriteFile(path, []byte("x\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ledger := NewLedger(filepath.Join(dir, "snaps"))
	if err := ledger.Snapshot(path); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if err := os.WriteFile(path, []byte("y\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := ledger.RestoreAll(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
}
