// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package diskguard

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestAdmitLinksNewDirectoriesBeforeMovingFiles(t *testing.T) {
	spool := t.TempDir()
	dir := filepath.Join(spool, ".rtu-quarantine")
	source := filepath.Join(spool, "request-trace.000003.jsonl.gz")
	if err := os.WriteFile(source, []byte("segment"), 0o640); err != nil {
		t.Fatalf("write source: %v", err)
	}
	var synced []string
	onSync = func(d string) { synced = append(synced, d) }
	t.Cleanup(func() { onSync = nil })

	if err := NewQuarantine(dir).Admit("seg-3", source); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	// The quarantine directory is linked in the spool, then the entry in the
	// quarantine, then the moved file, and only then the source removal.
	want := []string{spool, dir, filepath.Join(dir, "seg-3"), spool}
	if !slices.Equal(synced, want) {
		t.Fatalf("synced directories = %v, want %v", synced, want)
	}
}
