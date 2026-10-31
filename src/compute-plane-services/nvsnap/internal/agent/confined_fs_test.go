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

// A directory inside a workload's root is opened within that root: a
// symlink there cannot lead outside it.
func TestOpenWorkloadDir(t *testing.T) {
	proc, rootfs, outside := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(proc, "7"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rootfs, filepath.Join(proc, "7", "root")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rootfs, "nvsnap-restore-work"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(rootfs, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	d, err := openWorkloadDir(proc, 7, "/nvsnap-restore-work")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.writeFileAtomic("restored", []byte("1\n")); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	if _, err := os.Stat(filepath.Join(rootfs, "nvsnap-restore-work", "restored")); err != nil {
		t.Errorf("not written in the workload's directory: %v", err)
	}
	if _, err := openWorkloadDir(proc, 7, "/elsewhere"); err == nil {
		t.Error("opened a directory outside the workload's root through a symlink")
	}
	if _, err := openWorkloadDir(proc, 7, "/missing"); err == nil {
		t.Error("opened a missing directory")
	}
}

// The tail of a log is its last bytes, from a regular file only.
func TestConfinedDirReadTail(t *testing.T) {
	d, dir, _ := confinedTestDir(t)
	if err := os.WriteFile(filepath.Join(dir, "restore.log"), []byte(strings.Repeat("a", 100)+"Restoring FAILED.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := d.readTail("restore.log", 18)
	if err != nil || string(b) != "Restoring FAILED.\n" {
		t.Errorf("tail = %q, %v", b, err)
	}
	if b, err := d.readTail("restore.log", 1<<20); err != nil || len(b) != 118 {
		t.Errorf("whole log: %d bytes, %v", len(b), err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := d.readTail("fifo", 64); err == nil {
		t.Error("read a FIFO as a log")
	}
}

// A harvest moves regular files and their directories into the agent's
// directory, and refuses anything else, leaving nothing of it behind.
func TestHarvestInto(t *testing.T) {
	src, dst, outside := t.TempDir(), t.TempDir(), t.TempDir()
	write := func(p, content string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(src, "pages-1.img"), "pages")
	write(filepath.Join(src, "chunks", "ab", "abcd"), "chunk")
	d, err := openConfinedDir(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := harvestInto(d, dst); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	for p, want := range map[string]string{"pages-1.img": "pages", "chunks/ab/abcd": "chunk"} {
		if b, err := os.ReadFile(filepath.Join(dst, p)); err != nil || string(b) != want {
			t.Errorf("%s = %q, %v", p, b, err)
		}
		if _, err := os.Lstat(filepath.Join(src, p)); !os.IsNotExist(err) {
			t.Errorf("%s left in the source: %v", p, err)
		}
	}

	for name, make := range map[string]func(p string) error{
		"symlink": func(p string) error { return os.Symlink(filepath.Join(outside, "x"), p) },
		"fifo":    func(p string) error { return syscall.Mkfifo(p, 0o644) },
	} {
		src, dst := t.TempDir(), t.TempDir()
		if err := os.MkdirAll(filepath.Join(src, "chunks"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := make(filepath.Join(src, "chunks", "gpus")); err != nil {
			t.Fatal(err)
		}
		d, err := openConfinedDir(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := harvestInto(d, dst); err == nil {
			t.Errorf("%s: harvested", name)
		}
		_ = d.Close()
		if _, err := os.Lstat(filepath.Join(dst, "chunks", "gpus")); !os.IsNotExist(err) {
			t.Errorf("%s: reached the agent's directory (%v)", name, err)
		}
	}
}

// Across filesystems a harvest copies, with the same checks.
func TestHarvestIntoAcrossFilesystems(t *testing.T) {
	shm, err := os.MkdirTemp("/dev/shm", "harvest-")
	if err != nil {
		t.Skip("no /dev/shm")
	}
	defer func() { _ = os.RemoveAll(shm) }()
	dst := t.TempDir()
	var a, b syscall.Stat_t
	if syscall.Stat(shm, &a) != nil || syscall.Stat(dst, &b) != nil || a.Dev == b.Dev {
		t.Skip("/dev/shm and the test directory share a filesystem")
	}
	if werr := os.WriteFile(filepath.Join(shm, "inventory.img"), []byte("inv"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	d, err := openConfinedDir(shm)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if herr := harvestInto(d, dst); herr != nil {
		t.Fatal(herr)
	}
	fi, err := os.Lstat(filepath.Join(dst, "inventory.img"))
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Errorf("copied file: %v %v", fi, err)
	}
	if _, err := os.Lstat(filepath.Join(shm, "inventory.img")); !os.IsNotExist(err) {
		t.Errorf("source left after the copy: %v", err)
	}
}

// gpuInstallFixture is a placeholder (pid 1) with a GPU identity file
// bind-mounted at /run/nvidia-container-devices/GPU-a, and a restored tree
// (pid 2) whose root is tree.
func gpuInstallFixture(t *testing.T, tree string) string {
	t.Helper()
	proc, placeholder := t.TempDir(), t.TempDir()
	for pid, root := range map[string]string{"1": placeholder, "2": tree} {
		if err := os.MkdirAll(filepath.Join(proc, pid), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(root, filepath.Join(proc, pid, "root")); err != nil {
			t.Fatal(err)
		}
	}
	dev := filepath.Join(placeholder, "run", "nvidia-container-devices", "GPU-a")
	if err := os.MkdirAll(filepath.Dir(dev), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dev, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	mi := "22 1 0:1 / / rw - overlay overlay rw\n" +
		"30 22 0:50 /GPU-a /run/nvidia-container-devices/GPU-a ro - tmpfs tmpfs rw\n"
	if err := os.WriteFile(filepath.Join(proc, "1", "mountinfo"), []byte(mi), 0o644); err != nil {
		t.Fatal(err)
	}
	return proc
}

// The GPU entries a placeholder has are created in the restored tree, under
// its root: a symlinked directory in the tree does not take a creation
// outside it.
func TestInstallPlaceholderGPUsStaysInTheTree(t *testing.T) {
	tree := t.TempDir()
	made, err := installPlaceholderGPUs(gpuInstallFixture(t, tree), 1, 2)
	if err != nil || len(made) != 1 {
		t.Fatalf("a plain tree: made %v, %v", made, err)
	}
	if _, lerr := os.Lstat(filepath.Join(tree, "run", "nvidia-container-devices", "GPU-a")); lerr != nil {
		t.Errorf("not created in the tree: %v", lerr)
	}

	tree, outside := t.TempDir(), t.TempDir()
	if serr := os.Symlink(outside, filepath.Join(tree, "run")); serr != nil {
		t.Fatal(serr)
	}
	made, err = installPlaceholderGPUs(gpuInstallFixture(t, tree), 1, 2)
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Errorf("created %v outside the restored tree (made %v, err %v)", ents, made, err)
	}
}

// Only device nodes are declared as GPU externals: a link in the
// workload's /dev to the agent's own /dev/null names nothing to declare.
func TestNvidiaDevExternalsSkipsLinks(t *testing.T) {
	dev := t.TempDir()
	if err := os.Symlink("/dev/null", filepath.Join(dev, "nvidia0")); err != nil {
		t.Fatal(err)
	}
	exts, err := nvidiaDevExternals(dev)
	if err != nil || len(exts) != 0 {
		t.Errorf("externals %v, %v; want none for a link", exts, err)
	}
}
