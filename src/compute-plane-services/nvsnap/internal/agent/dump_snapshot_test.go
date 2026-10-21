// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runAction runs a dump action script as criu would, for action.
func runAction(t *testing.T, script, action string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), script)
	cmd.Env = append(os.Environ(), "CRTOOLS_SCRIPT_ACTION="+action)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func waitFile(path string, limit time.Duration) bool {
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

// At post-dump the script has the snapshot taken first, then waits at the
// group's barrier: the copy is made with the tree still stopped, before
// any member can be released.
func TestDumpActionScript_SnapshotThenBarrier(t *testing.T) {
	snap, barrier := t.TempDir(), t.TempDir()
	script := filepath.Join(t.TempDir(), "action.sh")
	if err := writeDumpActionScript(script, snap, barrier); err != nil {
		t.Fatal(err)
	}
	var takes int
	var reachedBeforeSnapshot bool
	stop := serveSnapshot(context.Background(), snap, func() error {
		takes++
		_, err := os.Stat(filepath.Join(barrier, "reached"))
		reachedBeforeSnapshot = err == nil
		return nil
	})
	cmd := runAction(t, script, "post-dump")
	if !waitFile(filepath.Join(barrier, "reached"), 5*time.Second) {
		t.Fatal("the script never reached the barrier")
	}
	if err := os.WriteFile(filepath.Join(barrier, "go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("script: %v, want exit 0", err)
	}
	taken, err := stop()
	if !taken || err != nil || takes != 1 {
		t.Fatalf("taken=%v err=%v takes=%d; want one snapshot", taken, err, takes)
	}
	if reachedBeforeSnapshot {
		t.Fatal("the barrier was reached before the snapshot was taken")
	}
}

// A snapshot that fails fails the script: criu then rolls the dump back
// rather than leave a checkpoint whose copies do not match it.
func TestDumpActionScript_SnapshotFailureFailsTheDump(t *testing.T) {
	snap := t.TempDir()
	script := filepath.Join(t.TempDir(), "action.sh")
	if err := writeDumpActionScript(script, snap, ""); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("tar failed")
	stop := serveSnapshot(context.Background(), snap, func() error { return boom })
	if err := runAction(t, script, "post-dump").Wait(); err == nil {
		t.Fatal("script passed after a failed snapshot, want a failure")
	}
	taken, err := stop()
	if taken || !errors.Is(err, boom) {
		t.Fatalf("taken=%v err=%v; want not taken, %v", taken, err, boom)
	}
	if got := snapshotNotTaken(err).Error(); !strings.Contains(got, "tar failed") {
		t.Fatalf("snapshotNotTaken = %q, want the cause", got)
	}
}

// Other actions pass without asking for a snapshot; a dump that never
// reaches post-dump reports none taken.
func TestDumpActionScript_OtherActionsPass(t *testing.T) {
	snap := t.TempDir()
	script := filepath.Join(t.TempDir(), "action.sh")
	if err := writeDumpActionScript(script, snap, ""); err != nil {
		t.Fatal(err)
	}
	stop := serveSnapshot(context.Background(), snap, func() error {
		t.Error("snapshot taken for a pre-dump action")
		return nil
	})
	for _, action := range []string{"pre-dump", "network-lock", "post-resume"} {
		if err := runAction(t, script, action).Wait(); err != nil {
			t.Errorf("%s: %v, want a pass", action, err)
		}
	}
	if taken, err := stop(); taken || err != nil {
		t.Fatalf("taken=%v err=%v; want none", taken, err)
	}
	if got := snapshotNotTaken(nil).Error(); !strings.Contains(got, "never asked") {
		t.Fatalf("snapshotNotTaken(nil) = %q", got)
	}
}

// The barrier-only script (no snapshot) is the group's post-dump barrier
// as before.
func TestDumpActionScript_BarrierOnly(t *testing.T) {
	barrier := t.TempDir()
	script := filepath.Join(t.TempDir(), "action.sh")
	if err := writeDumpActionScript(script, "", barrier); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "snapshot") {
		t.Fatalf("barrier-only script asks for a snapshot:\n%s", b)
	}
	cmd := runAction(t, script, "post-dump")
	if !waitFile(filepath.Join(barrier, "reached"), 5*time.Second) {
		t.Fatal("never reached the barrier")
	}
	_ = os.WriteFile(filepath.Join(barrier, "abort"), nil, 0o644)
	if err := cmd.Wait(); err == nil {
		t.Fatal("aborted barrier passed, want a failure")
	}
}
