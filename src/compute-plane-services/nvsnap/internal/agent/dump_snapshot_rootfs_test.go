// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/containerd"
)

// snapshotOf returns the dump snapshot of a container with upperdir upper
// and its checkpoint directory. The container is this process, whose
// /dev/shm the dump is told it saves, so no replay mount is copied.
func snapshotOf(t *testing.T, upper string) (snap func() error, ckpt string) {
	t.Helper()
	t.Setenv("NVSNAP_REPLAY_MOUNTS", "/dev/shm")
	ckpt = t.TempDir()
	log := logrus.New()
	log.SetOutput(io.Discard)
	a := &Agent{log: log}
	ci := &containerd.ContainerInfo{PID: uint32(os.Getpid())} //nolint:gosec // a pid fits in 32 bits
	take := a.dumpSnapshot(context.Background(), ci, ckpt, upper, nil, &[]ReplayMount{}, logrus.NewEntry(log))
	return func() error { return take(map[string]bool{"/dev/shm": true}) }, ckpt
}

// archived returns the regular files of a tar archive and their contents.
func archived(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	out := map[string]string{}
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			b, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			out[h.Name] = string(b)
		}
	}
}

func writeUpper(t *testing.T) string {
	t.Helper()
	upper := t.TempDir()
	if err := os.MkdirAll(filepath.Join(upper, "root", ".cache", "jit"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(upper, "root", ".cache", "jit", "build.log"), []byte("built"), 0o644); err != nil {
		t.Fatal(err)
	}
	return upper
}

// The snapshot archives the upperdir: the archive is whole once the
// snapshot returns, whatever becomes of the upperdir after it (the runtime
// removes it once the dumped container is replaced).
func TestDumpSnapshot_ArchivesTheUpperdir(t *testing.T) {
	upper := writeUpper(t)
	snap, ckpt := snapshotOf(t, upper)
	if err := snap(); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if err := os.RemoveAll(upper); err != nil {
		t.Fatal(err)
	}
	got := archived(t, filepath.Join(ckpt, rootfsDiffArchive))
	if got["./root/.cache/jit/build.log"] != "built" {
		t.Fatalf("archive: %v, want the upperdir's file", got)
	}
}

// A checkpoint without its diff cannot restore: a failed archive fails
// the snapshot, so criu rolls the dump back, and leaves no archive behind.
func TestDumpSnapshot_ArchiveFailureFailsTheSnapshot(t *testing.T) {
	snap, ckpt := snapshotOf(t, filepath.Join(t.TempDir(), "gone"))
	if err := snap(); err == nil {
		t.Fatal("snapshot passed without its archive")
	}
	for _, p := range []string{rootfsDiffArchive, rootfsDiffArchive + ".partial"} {
		if _, err := os.Stat(filepath.Join(ckpt, p)); !os.IsNotExist(err) {
			t.Errorf("%s exists (%v)", p, err)
		}
	}
}

// A container without an overlay upperdir has no diff to archive.
func TestDumpSnapshot_NoUpperdir(t *testing.T) {
	snap, ckpt := snapshotOf(t, "")
	if err := snap(); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ckpt, rootfsDiffArchive)); !os.IsNotExist(err) {
		t.Errorf("archive exists (%v)", err)
	}
}

// criu kills the tree only after its post-dump action returns, and the
// action returns only once the archive is whole: an upperdir removed right
// after the action no longer reaches the archive.
func TestDumpActionScript_ArchivesBeforeTheActionReturns(t *testing.T) {
	upper := writeUpper(t)
	snap, ckpt := snapshotOf(t, upper)
	hook := t.TempDir()
	script := filepath.Join(t.TempDir(), "action.sh")
	if err := writeDumpActionScript(script, hook, ""); err != nil {
		t.Fatal(err)
	}
	stop := serveSnapshot(context.Background(), confinedFor(t, hook), snap)
	if err := runAction(t, script, "post-dump").Wait(); err != nil {
		t.Fatalf("script: %v, want exit 0", err)
	}
	if err := os.RemoveAll(upper); err != nil {
		t.Fatal(err)
	}
	if taken, err := stop(); !taken || err != nil {
		t.Fatalf("taken=%v err=%v", taken, err)
	}
	if got := archived(t, filepath.Join(ckpt, rootfsDiffArchive)); got["./root/.cache/jit/build.log"] != "built" {
		t.Fatalf("archive: %v, want the upperdir's file", got)
	}
}
