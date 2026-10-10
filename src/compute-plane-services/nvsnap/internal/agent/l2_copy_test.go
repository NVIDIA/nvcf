// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyTreeDirect(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	want := map[string][]byte{}
	for rel, size := range map[string]int{"inventory.img": 3, "pages-1.img": 9<<20 + 7, "gpushare/c/chunk-0": 4096, "gpushare/c/chunk-1": 1<<20 + 1} {
		b := make([]byte, size)
		_, _ = rand.Read(b)
		want[rel] = b
		if err := os.MkdirAll(filepath.Join(src, filepath.Dir(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, rel), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(src, "lost+found"), 0o700); err != nil {
		t.Fatal(err)
	}
	// The store directory exists already (a placeholder holds it mounted),
	// with a longer stale file from an earlier try.
	if err := os.MkdirAll(filepath.Join(dst, "gpushare"), 0o755); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(filepath.Join(dst, "gpushare"))
	if err := os.WriteFile(filepath.Join(dst, "inventory.img"), bytes.Repeat([]byte("x"), 100), 0o644); err != nil {
		t.Fatal(err)
	}
	n, files, err := copyTreeDirect(context.Background(), src, dst, []string{"/lost+found"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for rel, b := range want {
		total += int64(len(b))
		got, err := os.ReadFile(filepath.Join(dst, rel))
		if err != nil || !bytes.Equal(got, b) {
			t.Errorf("%s differs after the copy (err %v)", rel, err)
		}
	}
	if n != total || files != len(want) {
		t.Errorf("copied %d bytes in %d files, want %d in %d", n, files, total, len(want))
	}
	after, _ := os.Stat(filepath.Join(dst, "gpushare"))
	if !os.SameFile(before, after) {
		t.Error("the existing store directory was replaced")
	}
	if _, err := os.Stat(filepath.Join(dst, "lost+found")); !os.IsNotExist(err) {
		t.Error("lost+found was copied")
	}
}
