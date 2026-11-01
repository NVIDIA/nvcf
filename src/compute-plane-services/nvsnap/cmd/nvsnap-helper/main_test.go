// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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

// With --keep-owners the cachedir and every entry get the mode they have
// in the set, past the umask, and nothing is opened to every user: a
// restored engine reopens these files, and CRIU refuses one whose mode
// differs from the dump (a seeded 0666 Triton launcher of a 0644 one,
// dev2 2026-10-20).
func TestSeed_KeepOwners(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "cache")
	_ = os.MkdirAll(dst, 0o777) // the emptyDir subPath, as the kubelet makes it
	_ = os.Chmod(dst, 0o777)
	_ = os.MkdirAll(filepath.Join(src, "triton", "k"), 0o755)
	files := map[string]uint32{"triton/k/launcher.so": 0o644, "triton/k/rw.json": 0o666, "triton/k/x": 0o755, "key": 0o600}
	for rel := range files {
		_ = os.WriteFile(filepath.Join(src, rel), []byte(rel), 0o600)
	}
	modes := map[string]uint32{".": 0o2755, "triton": 0o750, "triton/k": 0o775}
	for rel, m := range files {
		modes[rel] = m
	}
	for rel, m := range modes {
		if err := syscall.Chmod(filepath.Join(src, rel), m); err != nil {
			t.Fatal(err)
		}
	}
	seedCmd([]string{"--from", src, "--to", dst, "--index", ".nvsnap-seed-index", "--keep-owners"})
	for rel, want := range modes {
		var st syscall.Stat_t
		if err := syscall.Stat(filepath.Join(dst, rel), &st); err != nil {
			t.Fatal(err)
		}
		if st.Mode&0o7777 != want {
			t.Errorf("%s: mode %o, want %o", rel, st.Mode&0o7777, want)
		}
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

func TestSkip(t *testing.T) {
	skipCmd([]string{"--reason", "nothing to do"}) // must return, not exit
}

// With --marker the seed waits for the source to be brought to the node:
// it copies once the marker is there, and past --deadline copies nothing
// and still leaves a writable cache.
func TestSeed_Marker(t *testing.T) {
	src := t.TempDir()
	_ = os.WriteFile(filepath.Join(src, "k.bin"), []byte("kernel"), 0o600)
	marker := filepath.Join(t.TempDir(), "ready")

	late := filepath.Join(t.TempDir(), "late")
	seedCmd([]string{"--from", src, "--to", late, "--marker", marker, "--deadline", "0"})
	if _, err := os.Stat(filepath.Join(late, "k.bin")); err == nil {
		t.Error("copied a source that was not ready")
	}
	if fi, err := os.Stat(late); err != nil || !fi.IsDir() {
		t.Error("no cache directory past the deadline")
	}

	_ = os.WriteFile(marker, nil, 0o644)
	ready := filepath.Join(t.TempDir(), "ready")
	seedCmd([]string{"--from", src, "--to", ready, "--marker", marker, "--deadline", "0"})
	if b, err := os.ReadFile(filepath.Join(ready, "k.bin")); err != nil || string(b) != "kernel" {
		t.Errorf("ready source not copied: %q %v", b, err)
	}
}

func TestAwaitMarker(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ready")
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = os.WriteFile(marker, nil, 0o644)
	}()
	if !awaitMarker(marker, 5*time.Second, 5*time.Millisecond) {
		t.Error("the marker written while waiting was missed")
	}
	if awaitMarker(filepath.Join(t.TempDir(), "never"), 0, time.Millisecond) {
		t.Error("a missing marker reported")
	}
}

// --links makes the links a capture recorded, under --to only.
func TestSeed_Links(t *testing.T) {
	src, dir := t.TempDir(), t.TempDir()
	_ = os.MkdirAll(filepath.Join(src, "model", "blobs"), 0o755)
	_ = os.WriteFile(filepath.Join(src, "model", "blobs", "b1"), []byte("weights"), 0o644)
	man := filepath.Join(dir, "nvsnap-cachedir.json")
	_ = os.WriteFile(man, []byte(`{"links":{"model/snapshots/w":"../blobs/b1"}}`), 0o644)
	to := filepath.Join(t.TempDir(), "c")
	seedCmd([]string{"--from", src, "--to", to, "--links", man})
	if b, err := os.ReadFile(filepath.Join(to, "model", "snapshots", "w")); err != nil || string(b) != "weights" {
		t.Errorf("link: %q %v", b, err)
	}
	_ = os.WriteFile(man, []byte(`{"links":{"../../out":"/etc"}}`), 0o644)
	if err := makeLinks(man, to); err == nil {
		t.Error("a link outside the cachedir was made")
	}
}
