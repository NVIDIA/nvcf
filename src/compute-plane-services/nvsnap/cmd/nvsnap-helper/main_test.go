// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommand_FirstOnPath(t *testing.T) {
	argv, err := command([]string{"nvsnap-no-such-tool", "true"}, []string{"download", "x"})
	if err != nil || filepath.Base(argv[0]) != "true" || argv[1] != "download" {
		t.Fatalf("command = %v, %v; want the first program found", argv, err)
	}
	if _, err := command([]string{"nvsnap-no-such-tool"}, nil); err == nil {
		t.Error("no program on PATH must be an error")
	}
	if argv, _ := command(nil, nil); argv != nil {
		t.Error("nothing to run must be nil")
	}
}

func TestWait(t *testing.T) {
	dir := t.TempDir()
	marker, failed := filepath.Join(dir, ".done"), filepath.Join(dir, ".failed")
	if err := os.WriteFile(marker, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, err := waitCmd([]string{"--marker", marker}); code != 0 || err != nil {
		t.Errorf("complete model: code %d, %v", code, err)
	}
	_ = os.Remove(marker)
	if err := os.WriteFile(failed, []byte("disk full"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Read-only landing: the pod must be recreated on its own download.
	if code, _ := waitCmd([]string{"--marker", marker, "--failed", failed}); code != 1 {
		t.Errorf("read-only landing after a failure: code %d, want 1", code)
	}
	// Writable landing: the fallback download runs here.
	out := filepath.Join(dir, "fallback-ran")
	if code, err := waitCmd([]string{"--marker", marker, "--failed", failed, "--in-place", "--", "touch", out}); code != 0 || err != nil || !exists(out) {
		t.Errorf("in-place fallback: code %d, %v, ran=%v", code, err, exists(out))
	}
	// Past the deadline with no marker and no failure: fall back too.
	_ = os.Remove(failed)
	_ = os.Remove(out)
	if code, err := waitCmd([]string{"--marker", marker, "--deadline", "0", "--in-place", "--", "touch", out}); code != 0 || err != nil || !exists(out) {
		t.Errorf("deadline fallback: code %d, %v, ran=%v", code, err, exists(out))
	}
	// Nothing to fall back to: the engine downloads.
	if code, _ := waitCmd([]string{"--marker", marker, "--deadline", "0", "--in-place", "--try", "nvsnap-no-such-tool"}); code != 0 {
		t.Errorf("no downloader in the image: code %d, want 0", code)
	}
}

func TestWrite(t *testing.T) {
	dir := t.TempDir()
	marker, failed := filepath.Join(dir, ".done"), filepath.Join(dir, ".failed")
	_ = os.WriteFile(failed, []byte("x"), 0o644)
	payload := filepath.Join(dir, "weights")
	code, err := writeCmd([]string{"--marker", marker, "--failed", failed, "--dir", dir, "--", "sh", "-c", "printf 0123456789 > " + payload})
	if code != 0 || err != nil {
		t.Fatalf("write: code %d, %v", code, err)
	}
	b, _ := os.ReadFile(marker)
	if string(b) != "10" || exists(failed) {
		t.Errorf("marker = %q (want the 10 bytes written), failed marker left = %v", b, exists(failed))
	}
	// A complete model is not downloaded again.
	ran := filepath.Join(dir, "ran")
	if code, err := writeCmd([]string{"--marker", marker, "--dir", dir, "--", "touch", ran}); code != 0 || err != nil || exists(ran) {
		t.Errorf("complete model: code %d, %v, download ran=%v", code, err, exists(ran))
	}
	// A failing download leaves no marker.
	_ = os.Remove(marker)
	if code, _ := writeCmd([]string{"--marker", marker, "--dir", dir, "--", "false"}); code == 0 || exists(marker) {
		t.Errorf("failed download: code %d, marker=%v", code, exists(marker))
	}
}

func TestSeed(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "cache")
	_ = os.MkdirAll(filepath.Join(src, "a"), 0o700)
	_ = os.WriteFile(filepath.Join(src, "a", "k.bin"), []byte("kernel"), 0o600)
	_ = os.Symlink("a/k.bin", filepath.Join(src, "link"))
	seedCmd([]string{"--from", src, "--to", dst, "--label", "rank 0", "--index", ".nvsnap-seed-index"})
	fi, err := os.Stat(filepath.Join(dst, "a", "k.bin"))
	if err != nil || fi.Mode().Perm()&0o666 != 0o666 {
		t.Fatalf("seeded file must be readable and writable by all: %v %v", fi, err)
	}
	if l, err := os.Readlink(filepath.Join(dst, "link")); err != nil || l != "a/k.bin" {
		t.Errorf("symlink not kept: %q %v", l, err)
	}
	idx, _ := os.ReadFile(filepath.Join(dst, ".nvsnap-seed-index"))
	if strings.TrimSpace(string(idx)) != "a/k.bin" {
		t.Errorf("index = %q", idx)
	}
	// A missing source leaves an empty, writable cache and does not fail.
	empty := filepath.Join(t.TempDir(), "c")
	seedCmd([]string{"--from", "/nonexistent", "--to", empty})
	if fi, err := os.Stat(empty); err != nil || !fi.IsDir() {
		t.Error("missing source must still create the cache directory")
	}
}

func TestPrewarm(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "snap", "original"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "snap", "w.safetensors"), make([]byte, 3<<20), 0o644)
	_ = os.Symlink(root, filepath.Join(root, "snap", "loop")) // must not recurse forever
	prewarmCmd([]string{"--root", root, "--workers", "2", "--chunk", "1048576"})
	t.Setenv("NVSNAP_PREWARM", "0")
	prewarmCmd([]string{"--root", root})
}
