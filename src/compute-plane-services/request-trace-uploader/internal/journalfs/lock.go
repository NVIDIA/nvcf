// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package journalfs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
)

// lockPoll is how often a waiting uploader retries the lock.
var lockPoll = 200 * time.Millisecond

// lockOwner is written into the lock file by the holder. flock is the
// guard; the owner names who holds it, because on some network file systems
// flock is weak and the name is the only evidence of a second uploader.
type lockOwner struct {
	Holder string    `json:"holder"`
	PID    int       `json:"pid"`
	Since  time.Time `json:"since"`
}

type spoolLock struct {
	file     *os.File
	previous lockOwner
}

// acquireLock takes an exclusive flock on path, waiting up to wait for
// another holder to release it.
func acquireLock(ctx context.Context, path, holder string, wait time.Duration, now func() time.Time) (*spoolLock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o640)
	if err != nil {
		return nil, fmt.Errorf("open spool lock: %w", err)
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf("take spool lock: %w", err)
		}
		if !time.Now().Before(deadline) {
			owner := readOwner(f)
			_ = f.Close()
			return nil, fmt.Errorf("%w after waiting %s: holder %q pid %d since %s",
				journal.ErrLocked, wait, owner.Holder, owner.PID, owner.Since.Format(time.RFC3339))
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, fmt.Errorf("wait for spool lock: %w", ctx.Err())
		case <-time.After(min(lockPoll, time.Until(deadline))):
		}
	}
	lock := &spoolLock{file: f, previous: readOwner(f)}
	if err := lock.record(lockOwner{Holder: holder, PID: os.Getpid(), Since: now()}); err != nil {
		_ = lock.release()
		return nil, err
	}
	return lock, nil
}

// record replaces the holder record. Only the lock holder writes it.
func (l *spoolLock) record(owner lockOwner) error {
	data, err := json.Marshal(owner)
	if err != nil {
		return fmt.Errorf("encode spool lock holder: %w", err)
	}
	if err := l.file.Truncate(0); err != nil {
		return fmt.Errorf("write spool lock holder: %w", err)
	}
	if _, err := l.file.WriteAt(data, 0); err != nil {
		return fmt.Errorf("write spool lock holder: %w", err)
	}
	if err := l.file.Sync(); err != nil {
		return fmt.Errorf("sync spool lock holder: %w", err)
	}
	return nil
}

// readOwner reads the holder record. An empty or unreadable record gives a
// zero owner; it only feeds a message.
func readOwner(f *os.File) lockOwner {
	var owner lockOwner
	buf := make([]byte, 4096)
	n, _ := f.ReadAt(buf, 0)
	_ = json.Unmarshal(buf[:n], &owner)
	return owner
}

func (l *spoolLock) release() error {
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return fmt.Errorf("release spool lock: %w", unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close spool lock: %w", closeErr)
	}
	return nil
}
