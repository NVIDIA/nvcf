// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalFetch_InterruptedFetchIsNotComplete(t *testing.T) {
	root := t.TempDir()
	if err := beginLocalFetch(root, "c1"); err != nil {
		t.Fatal(err)
	}
	// The fetch got the images, then the agent stopped.
	if err := os.WriteFile(filepath.Join(root, "c1", "inventory.img"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if localCheckpointComplete(root, "c1") {
		t.Fatal("an interrupted fetch reads as complete")
	}
	if err := endLocalFetch(root, "c1", true); err != nil {
		t.Fatal(err)
	}
	if !localCheckpointComplete(root, "c1") {
		t.Error("a finished fetch reads as incomplete")
	}
}

func TestLocalFetch_CapturedCheckpointIsComplete(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "c1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "c1", "inventory.img"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !localCheckpointComplete(root, "c1") {
		t.Error("a checkpoint captured on this node reads as incomplete")
	}
}

// A placeholder may hold the checkpoint directory and its gpushare store
// bind-mounted: a retried fetch keeps both directories (same inode).
func TestLocalFetch_RetryKeepsTheDirectories(t *testing.T) {
	root := t.TempDir()
	store := filepath.Join(root, "c1", "gpushare")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(store)
	if err := beginLocalFetch(root, "c1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "chunk-0"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := endLocalFetch(root, "c1", false); err != nil {
		t.Fatal(err)
	}
	if err := beginLocalFetch(root, "c1"); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(store)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("the gpushare store directory was replaced")
	}
	if _, err := os.Stat(filepath.Join(store, "chunk-0")); !os.IsNotExist(err) {
		t.Error("a failed fetch's file survived the retry")
	}
}
