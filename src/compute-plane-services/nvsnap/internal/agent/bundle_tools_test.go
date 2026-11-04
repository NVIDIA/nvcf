// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeWorkload is a /proc entry for pid 7 whose root holds rootDir, with
// the given mountinfo and effective capabilities.
func fakeWorkload(t *testing.T, rootDir, mountinfo, capEff string) string {
	t.Helper()
	proc := t.TempDir()
	base := filepath.Join(proc, "7")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rootDir, filepath.Join(base, "root")); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"mountinfo": mountinfo,
		"status":    "Name:\tvllm\nCapInh:\t0000000000000000\nCapEff:\t" + capEff + "\n",
	} {
		if err := os.WriteFile(filepath.Join(base, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return proc
}

const (
	bundleRO = "30 22 259:1 /versions/v1 /criu-bundle ro,relatime - ext4 /dev/x rw\n"
	bundleRW = "30 22 259:1 /versions/v1 /criu-bundle rw,relatime - ext4 /dev/x rw\n"
	// CAP_KILL, CAP_SYS_PTRACE and CAP_IPC_LOCK, as a Kimi-K3 worker has.
	capsWorker = "00000000000c4020"
	capsAdmin  = "00000000002c4020"
)

// A workload's bundle tools are trusted only when /criu-bundle is a
// read-only mount of one of the node's staged versions, in a workload that
// cannot change its mounts.
func TestBundleToolsOf(t *testing.T) {
	// The node's versions; the workload's /criu-bundle is version v1 (the
	// fake root is the versions directory itself, so the inode matches as
	// a bind mount's would).
	versions := t.TempDir()
	if err := os.MkdirAll(filepath.Join(versions, "criu-bundle"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A directory that is not a version, at the same path in another root.
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, "criu-bundle"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A /criu-bundle that is a symlink to the version.
	linked := t.TempDir()
	if err := os.Symlink(filepath.Join(versions, "criu-bundle"), filepath.Join(linked, "criu-bundle")); err != nil {
		t.Fatal(err)
	}

	tools, err := bundleToolsOf(fakeWorkload(t, versions, bundleRO, capsWorker), 7, versions)
	if err != nil || tools.version != "criu-bundle" || tools.pid != 7 {
		t.Fatalf("the node's version mounted read-only: %+v, %v", tools, err)
	}
	for _, c := range []struct {
		name, root, mountinfo, caps, want string
	}{
		{"not mounted", versions, "22 1 0:1 / / rw - overlay overlay rw\n", capsWorker, "no node bundle mounted"},
		{"mounted writable", versions, bundleRW, capsWorker, "not mounted read-only"},
		{"writable mount over the read-only one", versions, bundleRO + strings.Replace(bundleRW, "30 22", "31 30", 1), capsWorker, "not mounted read-only"},
		{"workload can change its mounts", versions, bundleRO, capsAdmin, "CAP_SYS_ADMIN"},
		{"not a staged version", other, bundleRO, capsWorker, "not one of the node's bundle versions"},
		{"a symlink", linked, bundleRO, capsWorker, "not a directory"},
	} {
		if _, err := bundleToolsOf(fakeWorkload(t, c.root, c.mountinfo, c.caps), 7, versions); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

func TestHasCapability(t *testing.T) {
	p := filepath.Join(t.TempDir(), "status")
	for caps, want := range map[string]bool{capsWorker: false, capsAdmin: true, "000001ffffffffff": true} {
		if err := os.WriteFile(p, []byte("CapEff:\t"+caps+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, err := hasCapability(p, capSysAdmin); err != nil || got != want {
			t.Errorf("CapEff %s: %v %v, want %v", caps, got, err, want)
		}
	}
	if err := os.WriteFile(p, []byte("Name:\tx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := hasCapability(p, capSysAdmin); err == nil {
		t.Error("a status without CapEff read as no capabilities")
	}
}

// The tools and the plugin directory are found in the mounted version.
func TestBundleToolsPaths(t *testing.T) {
	versions := t.TempDir()
	for _, f := range []string{"criu-bundle/criu", "criu-bundle/plugins/cuda/cuda_plugin.so"} {
		p := filepath.Join(versions, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	proc := fakeWorkload(t, versions, bundleRO, capsWorker)
	tools, err := bundleToolsOf(proc, 7, versions)
	if err != nil {
		t.Fatal(err)
	}
	if !tools.has(proc, "criu") || tools.has(proc, "tar") {
		t.Errorf("has: criu %v, tar %v", tools.has(proc, "criu"), tools.has(proc, "tar"))
	}
	if got := tools.path("criu"); got != "/criu-bundle/criu" {
		t.Errorf("path = %s", got)
	}
	if got := tools.pluginDir(proc); got != "/criu-bundle/plugins/cuda" {
		t.Errorf("pluginDir = %s", got)
	}
}
