// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: MPL-2.0

package renderer

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Run in a Linux container as UID/GID 1000 with only CHOWN and no-new-privileges.
func TestAtomicWriteDifferentOwner(t *testing.T) {
	if os.Getenv("ESS_TEST_CHOWN") != "1" {
		t.Skip("requires the non-root container ownership probe")
	}
	if os.Geteuid() != 1000 || os.Getegid() != 1000 {
		t.Fatal("ownership probe must run as UID/GID 1000")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"CapEff:\t0000000000000001", "NoNewPrivs:\t1"} {
		if !strings.Contains(string(status), field+"\n") {
			t.Fatalf("ownership probe requires %s", field)
		}
	}
	file := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(file, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(file, 0, 2345); err != nil {
		t.Fatal(err)
	}
	for _, contents := range []string{"first rotation", "second rotation"} {
		if err := AtomicWrite(file, false, []byte(contents), 0, false); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		owner := info.Sys().(*syscall.Stat_t)
		if owner.Uid != 0 || owner.Gid != 2345 || info.Mode().Perm() != 0o644 {
			t.Fatalf("owner/mode = %d:%d:%o, want 0:2345:644", owner.Uid, owner.Gid, info.Mode().Perm())
		}
		got, err := os.ReadFile(file)
		if err != nil || string(got) != contents {
			t.Fatalf("read rotated secret: contents=%q error=%v", got, err)
		}
	}
}
