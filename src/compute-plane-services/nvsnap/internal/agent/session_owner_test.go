// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A restore's fabric session directory belongs to the user the restored
// processes run as: a non-root engine writes its handle list there.
func TestOwnSessionDir(t *testing.T) {
	proc, dir := t.TempDir(), t.TempDir()
	uid, gid := os.Getuid(), os.Getgid() // the only owner an unprivileged test may give
	if err := os.MkdirAll(filepath.Join(proc, "42"), 0o755); err != nil {
		t.Fatal(err)
	}
	status := fmt.Sprintf("Name:\tpython3\nUid:\t%d\t%d\t%d\t%d\nGid:\t%d\t%d\t%d\t%d\n", uid, uid, uid, uid, gid, gid, gid, gid)
	if err := os.WriteFile(filepath.Join(proc, "42", "status"), []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
	parent, err := openConfinedDir(filepath.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()
	sess := &fabricSessionDir{parent: parent, name: filepath.Base(dir)}
	if err := sess.own(proc, 42); err != nil {
		t.Fatal(err)
	}
	var st syscall.Stat_t
	if err := syscall.Stat(dir, &st); err != nil || int(st.Uid) != uid || int(st.Gid) != gid {
		t.Errorf("owner %d:%d (%v), want %d:%d", st.Uid, st.Gid, err, uid, gid)
	}
	if err := sess.own(proc, 7); err == nil {
		t.Error("a process that is gone must be an error, not a guess")
	}
}
