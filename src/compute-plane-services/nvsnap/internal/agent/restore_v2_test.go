// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func TestRestoreLogReportsFailure(t *testing.T) {
	if restoreLogReportsFailure("(00.1) Opening vma\n(00.2) Restoring 735 to 336 pgid\n") {
		t.Error("a log without the failure line is not a failure")
	}
	if !restoreLogReportsFailure("(00.160169) Error (criu/cr-restore.c:2382): Restoring FAILED.\n(00.160180) cuda_plugin: finished stage 2 err -1\n") {
		t.Error("the CRIU failure line must be recognised")
	}
}

// A criu that logs a failed restore and does not exit is cancelled after
// the grace period; one that is still restoring is left alone.
func TestCancelWhenRestoreFailed(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "restore.log")
	if err := os.WriteFile(logPath, []byte("(00.1) Restoring 735 to 336 pgid\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelled := make(chan struct{})
	go cancelWhenRestoreFailed(ctx, logPath, 50*time.Millisecond, func() { close(cancelled) }, logrus.NewEntry(logrus.New()))
	select {
	case <-cancelled:
		t.Fatal("a restore still in progress must not be cancelled")
	case <-time.After(300 * time.Millisecond):
	}
	if err := os.WriteFile(logPath, []byte("(00.2) Error (criu/cr-restore.c:2382): Restoring FAILED.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("criu left alive after a failed restore must be cancelled once the grace period passes")
	}
}
