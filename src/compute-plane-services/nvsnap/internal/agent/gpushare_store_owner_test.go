// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The chunk store goes to the user the workload runs as (an engine that
// runs as uid 1000 cannot write a store the kubelet made as root).
func TestOwnGPUShareStore(t *testing.T) {
	proc, root := t.TempDir(), t.TempDir()
	uid, gid := os.Getuid(), os.Getgid() // the one owner a test may chown to
	if err := os.MkdirAll(filepath.Join(proc, "42"), 0o755); err != nil {
		t.Fatal(err)
	}
	status := fmt.Sprintf("Name:\tpython3\nUid:\t%d\t%d\t%d\t%d\nGid:\t%d\t%d\t%d\t%d\n", uid, uid, uid, uid, gid, gid, gid, gid)
	if err := os.WriteFile(filepath.Join(proc, "42", "status"), []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(root, strings.TrimPrefix(GPUShareStoreInContainer, "/"))
	if err := os.MkdirAll(filepath.Join(store, "fabric"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ownGPUShareStore(proc, 42, root, GPUShareStoreInContainer+"/fabric"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{store, filepath.Join(store, "fabric")} {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		st := fi.Sys().(*syscall.Stat_t)
		if int(st.Uid) != uid || int(st.Gid) != gid {
			t.Errorf("%s owned by %d:%d, want %d:%d", d, st.Uid, st.Gid, uid, gid)
		}
	}
	if _, _, err := procOwner(proc, 7); err == nil {
		t.Error("a missing process must be an error")
	}
}
