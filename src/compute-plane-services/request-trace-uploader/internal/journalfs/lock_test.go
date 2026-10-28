// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package journalfs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
)

const helperStateEnv = "RTU_JOURNALFS_HELPER_STATE_DIR"

func TestOpenRecordsLockHolder(t *testing.T) {
	h := newHarness(t)
	h.open()

	raw, err := os.ReadFile(filepath.Join(h.cfg.StateDir, "lock"))
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	var owner lockOwner
	if err := json.Unmarshal(raw, &owner); err != nil {
		t.Fatalf("lock file is not a holder record: %v", err)
	}
	if owner.Holder != "pod-a" || owner.PID != os.Getpid() {
		t.Fatalf("lock holder = %+v, want pod-a with pid %d", owner, os.Getpid())
	}
}

func TestLockHeldWaitsThenFails(t *testing.T) {
	h := newHarness(t)
	h.open()

	second := h.cfg
	second.Holder = "pod-b"
	started := time.Now()
	_, err := Open(t.Context(), second, Options{})
	waited := time.Since(started)

	if !errors.Is(err, journal.ErrLocked) {
		t.Fatalf("second Open error = %v, want ErrLocked", err)
	}
	if waited < h.cfg.LockWait {
		t.Fatalf("second Open gave up after %s, want at least the %s lock wait", waited, h.cfg.LockWait)
	}
	if !strings.Contains(err.Error(), "pod-a") || !strings.Contains(err.Error(), strconv.Itoa(os.Getpid())) {
		t.Fatalf("error %q does not name the holder pod-a and its pid", err)
	}
}

func TestLockReleasedDuringWaitIsAcquired(t *testing.T) {
	h := newHarness(t)
	first := h.open()

	second := h.cfg
	second.Holder = "pod-b"
	second.LockWait = 5 * time.Second
	done := make(chan error, 1)
	go func() {
		store, err := Open(context.Background(), second, Options{})
		if err == nil {
			err = store.Close()
		}
		done <- err
	}()

	time.Sleep(3 * lockPoll / 2)
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Open after release: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("Open did not take the released lock")
	}
}

func TestLockWaitStopsWhenContextIsCancelled(t *testing.T) {
	h := newHarness(t)
	h.open()

	second := h.cfg
	second.Holder = "pod-b"
	second.LockWait = time.Minute
	ctx, cancel := context.WithTimeout(t.Context(), 3*lockPoll/2)
	defer cancel()
	if _, err := Open(ctx, second, Options{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Open error = %v, want the context deadline", err)
	}
}

// TestLockHeldByAnotherProcess is F23 across processes: flock is held by a
// separate uploader process, as it would be by an old pod still draining.
func TestLockHeldByAnotherProcess(t *testing.T) {
	h := newHarness(t)
	if err := os.MkdirAll(h.cfg.StateDir, 0o750); err != nil {
		t.Fatalf("create state dir: %v", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcessHoldsLock$")
	cmd.Env = append(os.Environ(), helperStateEnv+"="+h.cfg.StateDir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "locked" {
		t.Fatalf("helper did not take the lock: %q, %v", line, err)
	}

	_, err = Open(t.Context(), h.cfg, Options{})
	if !errors.Is(err, journal.ErrLocked) || !strings.Contains(err.Error(), "pod-other") {
		t.Fatalf("Open while another process holds the lock = %v, want ErrLocked naming pod-other", err)
	}
}

func TestHelperProcessHoldsLock(t *testing.T) {
	stateDir := os.Getenv(helperStateEnv)
	if stateDir == "" {
		t.Skip("runs only as the helper process of TestLockHeldByAnotherProcess")
	}
	lock, err := acquireLock(context.Background(), filepath.Join(stateDir, "lock"), "pod-other", time.Second, time.Now)
	if err != nil {
		fmt.Println("lock failed")
		os.Exit(2)
	}
	fmt.Println("locked")
	_, _ = io.Copy(io.Discard, os.Stdin)
	_ = lock.release()
	os.Exit(0)
}
