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

package agent

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var osExecCommand = exec.Command

func TestSanitizeMountPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/dev/shm", "dev_shm"},
		{"/var/run/nvsnap", "var_run_nvsnap"},
		{"/tmp", "tmp"},
		{"/", ""},
		{"/a/b/c/d", "a_b_c_d"},
	}
	for _, tc := range cases {
		if got := SanitizeMountPath(tc.in); got != tc.want {
			t.Errorf("SanitizeMountPath(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// TestTarMount_RoundTrip exercises tarMount via the calling process's own
// procfs (/proc/self/root/...) and verifies the produced tar can be
// extracted into a target directory with content preserved.
//
// We can't unit-test untarIntoMntns directly — it shells out to nsenter
// which needs root + a target mntns — but the tar produced by tarMount
// is plain and standards-compliant, so a host-side `tar -x` round-trip
// is a sufficient smoke test for the snapshot half of the pipeline.
func TestTarMount_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Populate a few representative files.
	if err := os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(srcDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "sub", "b.bin"), []byte{0, 1, 2, 3, 4}, 0o600); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(tmp, "snapshot.tar")
	pid := os.Getpid()

	// tarMount expects mp to be reachable at /proc/<pid>/root/<mp>. We pass
	// the absolute path of srcDir; under /proc/self/root/<srcDir> resolves
	// to the same content via the procfs magic-link.
	if err := tarMount(pid, srcDir, out, nil, nil); err != nil {
		t.Fatalf("tarMount: %v", err)
	}

	st, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat tar: %v", err)
	}
	if st.Size() == 0 {
		t.Fatalf("tar is empty")
	}

	// Extract into a fresh dir and compare. Use the host's tar (same binary
	// tarMount called) for parity.
	extractDir := filepath.Join(tmp, "out")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runHostTar("-C", extractDir, "-xf", out); err != nil {
		t.Fatalf("extract tar: %v", err)
	}

	// Verify content round-tripped.
	if got, err := os.ReadFile(filepath.Join(extractDir, "a.txt")); err != nil {
		t.Fatalf("read a.txt: %v", err)
	} else if string(got) != "hello" {
		t.Errorf("a.txt content: got %q, want %q", got, "hello")
	}
	if got, err := os.ReadFile(filepath.Join(extractDir, "sub", "b.bin")); err != nil {
		t.Fatalf("read b.bin: %v", err)
	} else if want := []byte{0, 1, 2, 3, 4}; !bytes.Equal(got, want) {
		t.Errorf("b.bin content: got %v, want %v", got, want)
	}
}

func TestTarMount_MissingSource(t *testing.T) {
	// tarMount returns nil (no error) for missing source — captured-but-empty
	// is harmless on restore. Verifies we don't fail checkpoint over a
	// transient missing path.
	tmp := t.TempDir()
	out := filepath.Join(tmp, "should-not-exist.tar")
	if err := tarMount(os.Getpid(), filepath.Join(tmp, "nope"), out, nil, nil); err != nil {
		t.Errorf("tarMount missing source: got err %v, want nil", err)
	}
	if _, err := os.Stat(out); err == nil {
		t.Errorf("missing source produced output tar; should not exist")
	}
}

func TestUntarIntoMntns_MissingTar(t *testing.T) {
	// Missing tar → no-op, no error. Mirrors mirrorIntoMntns semantics for
	// missing source diff.
	tmp := t.TempDir()
	if err := untarIntoMntns(t.Context(), os.Getpid(), "/dev/shm", filepath.Join(tmp, "nope.tar"), bundleTools{}, nil); err != nil {
		t.Errorf("missing tar: got err %v, want nil", err)
	}
}

// runHostTar shells out to tar with the given args. Used by tests
// to extract artifacts produced by tarMount.
func runHostTar(args ...string) error {
	cmd := osExecCommand("tar", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return &tarErr{out: out, err: err}
	}
	return nil
}

type tarErr struct {
	out []byte
	err error
}

func (e *tarErr) Error() string { return e.err.Error() + ": " + string(e.out) }

// A mount nested in a replayed one (the model under nvsnap's cachedir) is the
// restore pod's own volume: its mountpoint must not be in the archive, or
// extracting it over the read-only mount fails the replay.
func TestTarMount_ExcludesNestedMounts(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	for _, d := range []string{"cache/.triton/C6", "hf/model", "cache/inductor/hf"} {
		if err := os.MkdirAll(filepath.Join(src, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"cache/.triton/C6/launcher.so", "cache/inductor/hf/k.py"} {
		if err := os.WriteFile(filepath.Join(src, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(tmp, "s.tar")
	if err := tarMount(os.Getpid(), src, out, []string{"hf"}, nil); err != nil {
		t.Fatalf("tarMount: %v", err)
	}
	names := tarNames(t, out)
	for _, n := range names {
		if n == "hf" || strings.HasPrefix(n, "hf/") {
			t.Errorf("nested mount %q is in the archive", n)
		}
	}
	for _, want := range []string{"cache/.triton/C6/launcher.so", "cache/inductor/hf/k.py"} {
		if !slices.Contains(names, want) {
			t.Errorf("archive lacks %s: %v", want, names)
		}
	}
}

// Extraction leaves out only the placeholder's nested mount at the top of
// the replayed mount, not every directory deeper down that shares its name
// (vLLM's inductor cache has an hf/ of its own).
func TestReplayExtractArgs_ExcludesOnlyTheNestedMount(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	files := map[string]string{
		"hf/model/w.bin":             "model",
		"cache/inductor/hf/k.py":     "kernel",
		"cache/inductor/hf/k.config": "config",
		"a/b/c":                      "nested",
		"x/a/b/c":                    "deep",
	}
	for f, body := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(src, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, f), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// An archive that still carries the nested mounts, as older captures do.
	out := filepath.Join(tmp, "s.tar")
	if err := tarMount(os.Getpid(), src, out, nil, nil); err != nil {
		t.Fatalf("tarMount: %v", err)
	}
	dst := filepath.Join(tmp, "dst")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	cmd := exec.CommandContext(t.Context(), "tar", replayExtractArgs(dst, []string{"hf", "a/b"})...) //nolint:gosec // test: the args under test
	cmd.Stdin = f
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tar: %v: %s", err, b)
	}
	for f := range files {
		_, err := os.Lstat(filepath.Join(dst, f))
		excluded := strings.HasPrefix(f, "hf/") || strings.HasPrefix(f, "a/b/")
		if excluded && err == nil {
			t.Errorf("%s was extracted over the nested mount", f)
		}
		if !excluded && err != nil {
			t.Errorf("%s was not extracted: %v", f, err)
		}
	}
}

func TestNestedMountPoints(t *testing.T) {
	got := nestedMountPoints([]string{"/", "/opt/nvsnap", "/opt/nvsnap/hf", "/opt/nvsnapx", "/opt/nvsnap/a/b", "/dev/shm"}, "/opt/nvsnap")
	if strings.Join(got, ",") != "hf,a/b" {
		t.Errorf("got %v", got)
	}
}

// The replay check names what an extraction left out, and ignores the
// placeholder's own nested mounts.
func TestVerifyReplayed(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	for _, d := range []string{"cache/.triton/C6", "hf"} {
		if err := os.MkdirAll(filepath.Join(src, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(src, "cache/.triton/C6/launcher.so"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(tmp, "s.tar")
	if err := tarMount(os.Getpid(), src, out, nil, nil); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(tmp, "dst")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runHostTar("-C", dst, "-xf", out); err != nil {
		t.Fatal(err)
	}
	if n, err := verifyReplayed(os.Getpid(), dst, out, nil); err != nil || n == 0 {
		t.Fatalf("complete replay: %d entries, %v", n, err)
	}
	if err := os.RemoveAll(filepath.Join(dst, "cache/.triton/C6")); err != nil {
		t.Fatal(err)
	}
	_, err := verifyReplayed(os.Getpid(), dst, out, nil)
	if err == nil || !strings.Contains(err.Error(), "cache/.triton/C6") {
		t.Fatalf("missing dir not reported: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(dst, "hf")); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyReplayed(os.Getpid(), dst, out, []string{"hf", "cache"}); err != nil {
		t.Errorf("skipped paths still checked: %v", err)
	}
}

func tarNames(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var names []string
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, filepath.Clean(strings.TrimPrefix(h.Name, "./")))
	}
}

// writeDiffTree builds a container upperdir as the etcd capture of
// 2026-10-21 had it: a data file only its owner may read in a directory
// only its owner may enter, a symlink, an empty directory, and nvsnap's own
// staging, which a capture leaves out.
func writeDiffTree(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	for _, d := range []struct {
		path string
		mode os.FileMode
	}{{"bitnami/etcd/data/member/snap", 0o700}, {"var/empty", 0o755}, {"criu-bundle", 0o755}} {
		if err := os.MkdirAll(filepath.Join(src, d.path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(src, d.path), d.mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"bitnami/etcd/data/member/snap/db", "criu-bundle/criu", "proc-mount/x"} {
		p := filepath.Join(src, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil && !os.IsExist(err) {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("member/snap/db", filepath.Join(src, "bitnami", "etcd", "data", "db-link")); err != nil {
		t.Fatal(err)
	}
	return src
}

// The archive is the checkpoint's copy of the diff: copied as plain bytes, as
// a peer agent's fetch copies a checkpoint's files, and extracted with the
// replay's arguments, it gives back what the source had. A tree did not: the
// fetch recreated the etcd data file 0644, CRIU refused to reopen it ("bad
// mode 0100644 (expect 0100600)"), and the instance's restore failed.
func TestArchiveOverlayDir_SurvivesAContentOnlyCopy(t *testing.T) {
	src := writeDiffTree(t)
	ckpt := t.TempDir()
	archive := filepath.Join(ckpt, rootfsDiffArchive)
	if err := archiveOverlayDir(src, archive, []string{"/proc-mount"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(archive + ".partial"); !os.IsNotExist(err) {
		t.Errorf("partial archive left behind: %v", err)
	}

	// What the peer fetch does: the file's bytes, a fresh default mode.
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	fetched := filepath.Join(t.TempDir(), rootfsDiffArchive)
	if err = os.WriteFile(fetched, data, 0o644); err != nil { //nolint:gosec // test temp dir

		t.Fatal(err)
	}

	dst := t.TempDir()
	f, err := os.Open(fetched)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	cmd := exec.Command("tar", replayDiffExtractArgs(dst, nil)...) //nolint:gosec,noctx // test temp dir
	cmd.Stdin = f
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("extract: %v: %s", err, out)
	}

	for _, c := range []struct {
		path string
		mode os.FileMode
	}{
		{"bitnami/etcd/data/member/snap/db", 0o600},
		{"bitnami/etcd/data/member/snap", os.ModeDir | 0o700},
		{"var/empty", os.ModeDir | 0o755},
	} {
		fi, err := os.Lstat(filepath.Join(dst, c.path))
		if err != nil {
			t.Errorf("%s: %v", c.path, err)
			continue
		}
		if got := fi.Mode() & (os.ModeDir | os.ModePerm); got != c.mode {
			t.Errorf("%s: mode %v, want %v", c.path, got, c.mode)
		}
	}
	if target, err := os.Readlink(filepath.Join(dst, "bitnami", "etcd", "data", "db-link")); err != nil || target != "member/snap/db" {
		t.Errorf("symlink: %q, %v; want member/snap/db", target, err)
	}
	for _, gone := range []string{"criu-bundle", "proc-mount"} {
		if _, err := os.Lstat(filepath.Join(dst, gone)); !os.IsNotExist(err) {
			t.Errorf("%s replayed (%v); a capture leaves it out", gone, err)
		}
	}
}

// The archive records owners by number: the restore, run as root, gives the
// workload's user (etcd runs as 1001) back its files.
func TestArchiveOverlayDir_RecordsNumericOwners(t *testing.T) {
	src := writeDiffTree(t)
	archive := filepath.Join(t.TempDir(), rootfsDiffArchive)
	if err := archiveOverlayDir(src, archive, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	tr := tar.NewReader(f)
	uid := os.Getuid()
	seen := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Name == "./bitnami/etcd/data/member/snap/db" {
			seen = true
			if h.Uid != uid || h.Uname != "" || h.Mode&0o777 != 0o600 {
				t.Errorf("db header uid %d uname %q mode %o; want uid %d, no name, 0600", h.Uid, h.Uname, h.Mode, uid)
			}
		}
	}
	if !seen {
		t.Error("db not in the archive")
	}
}

// A capture that cannot read its source leaves no archive for a restore to
// replay half of.
func TestArchiveOverlayDir_MissingSource(t *testing.T) {
	archive := filepath.Join(t.TempDir(), rootfsDiffArchive)
	if err := archiveOverlayDir(filepath.Join(t.TempDir(), "gone"), archive, nil, nil, nil); err == nil {
		t.Fatal("no error for a missing upperdir")
	}
	for _, p := range []string{archive, archive + ".partial"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s exists (%v)", p, err)
		}
	}
}

// The extract leaves out the placeholder's own mountpoints and the agent's
// directories, as the archiving of a diff tree did.
func TestReplayDiffExtractArgs_LeavesOutMountpointsAndAgentDirs(t *testing.T) {
	args := strings.Join(replayDiffExtractArgs("/", []string{"/usr/bin/nvidia-smi"}), " ")
	for _, want := range []string{"-C / -xf -", "--preserve-permissions", "--numeric-owner", "--overwrite",
		"--exclude=./usr/bin/nvidia-smi", "--exclude=./usr/bin/nvidia-smi/*",
		"--exclude=." + v2BinDirInContainer, "--exclude=." + v2ImagesDirInContainer} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
}
