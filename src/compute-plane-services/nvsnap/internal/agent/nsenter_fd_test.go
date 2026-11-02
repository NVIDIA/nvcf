// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// fakeNSProc lays out <base>/<pid>/{ns/*,root,cwd,status} with the namespaces
// linked to this process's own, the user namespace to user, and CapEff.
func fakeNSProc(t *testing.T, user, capEff string) string {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "7")
	if err := os.MkdirAll(filepath.Join(dir, "ns"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"mnt", "pid", "net", "ipc", "uts"} {
		if err := os.Symlink("/proc/self/ns/"+k, filepath.Join(dir, "ns", k)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(user, filepath.Join(dir, "ns", "user")); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"root", "cwd"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte("Name:\tx\nCapEff:\t"+capEff+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return base
}

// The namespaces are opened once and named by descriptor, numbered from
// first in the order of the files returned.
func TestNsenterByFDNamesDescriptors(t *testing.T) {
	base := fakeNSProc(t, "/proc/self/ns/user", "0000000000000000")
	args, files, err := nsenterByFD(base, 7, []string{"mnt", "net"}, true, 5)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFiles(files)
	want := []string{"--mount=/proc/self/fd/5", "--net=/proc/self/fd/6", "--root=/proc/self/fd/7", "--wd=/proc/self/fd/8"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Fatalf("args %q, want %q", args, want)
	}
	if len(files) != 4 {
		t.Fatalf("%d files", len(files))
	}
	var mine, got syscall.Stat_t
	if err := syscall.Stat("/proc/self/ns/mnt", &mine); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Fstat(int(files[0].Fd()), &got); err != nil {
		t.Fatal(err)
	}
	if mine.Ino != got.Ino {
		t.Error("the mount namespace passed is not the process's")
	}
}

// A process the agent must not enter is refused, and nothing is left open.
func TestNsenterByFDRefuses(t *testing.T) {
	other := filepath.Join(t.TempDir(), "user")
	if err := os.WriteFile(other, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		user, capEff string
		kinds        []string
		want         string
	}{
		"own user namespace": {other, "0", []string{"mnt"}, "user namespace of its own"},
		"CAP_SYS_ADMIN":      {"/proc/self/ns/user", "0000000000200000", []string{"mnt"}, "CAP_SYS_ADMIN"},
		"unknown namespace":  {"/proc/self/ns/user", "0", []string{"mnt", "cgroupx"}, "unknown namespace"},
	} {
		base := fakeNSProc(t, c.user, c.capEff)
		args, files, err := nsenterByFD(base, 7, c.kinds, true, 3)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err %v, want %q", name, err, c.want)
		}
		if args != nil || files != nil {
			t.Errorf("%s: returned args %q, %d files", name, args, len(files))
		}
	}
	if _, _, err := nsenterByFD(t.TempDir(), 7, nil, false, 3); err == nil {
		t.Error("a process that is gone was entered")
	}
}
