// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func confinedTestDir(t *testing.T) (d *confinedDir, dir, outside string) {
	t.Helper()
	dir, outside = t.TempDir(), t.TempDir()
	var err error
	d, err = openConfinedDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d, dir, outside
}

// Reads are of regular files inside the directory, bounded, and never
// block on a FIFO.
func TestConfinedDirReadFile(t *testing.T) {
	d, dir, outside := confinedTestDir(t)
	write := func(p, content string) {
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "q"), "0 ok\n")
	write(filepath.Join(outside, "secret"), "x")
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "out")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dir, "big"), strings.Repeat("x", 100))

	if b, err := d.readFile("q", 64); err != nil || string(b) != "0 ok\n" {
		t.Errorf("q = %q, %v", b, err)
	}
	if _, err := d.readFile("out", 64); err == nil {
		t.Error("read a file outside the directory through a symlink")
	}
	done := make(chan error, 1)
	go func() { _, err := d.readFile("fifo", 64); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("read a FIFO as a file")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO blocked")
	}
	if _, err := d.readFile("sub", 64); err == nil {
		t.Error("read a directory as a file")
	}
	if _, err := d.readFile("big", 64); err == nil {
		t.Error("read a file over the limit")
	}
	if _, err := d.readFile("../"+filepath.Base(outside)+"/secret", 64); err == nil {
		t.Error("read outside the directory through ..")
	}
}

// Writes land inside the directory by rename, with their mode, replacing a
// symlink there rather than writing through it, and leave no temporary
// file behind.
func TestConfinedDirWriteFileAtomic(t *testing.T) {
	d, dir, outside := confinedTestDir(t)
	target := filepath.Join(outside, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "in")); err != nil {
		t.Fatal(err)
	}
	if err := d.writeFileAtomic("in", []byte("handles\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Errorf("wrote through the symlink: target now %q", b)
	}
	fi, err := os.Lstat(filepath.Join(dir, "in"))
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o644 {
		t.Fatalf("in: %v %v, want a regular file 0644", fi, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "in")); string(b) != "handles\n" {
		t.Errorf("in = %q", b)
	}
	if err := os.Mkdir(filepath.Join(dir, "s"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.writeFileAtomic("s/u", []byte("unlock\n")); err != nil {
		t.Fatal(err)
	}
	if !d.exists("s/u") || d.exists("s/v") {
		t.Error("exists")
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temporary file left: %s", e.Name())
		}
	}
	if err := d.writeFileAtomic("../"+filepath.Base(outside)+"/x", []byte("x")); err == nil {
		t.Error("wrote outside the directory through ..")
	}
}
