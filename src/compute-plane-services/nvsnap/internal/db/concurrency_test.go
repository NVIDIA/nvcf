// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// The agents of one instance's pods record their captures at once. Every
// pooled connection must wait for the write lock: a connection without the
// busy timeout failed at once with "database is locked (SQLITE_BUSY)", and
// a checkpoint whose L2 promote was refused that way could not be fetched
// by another node.
func TestConcurrentWritesWait(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	const writers, each = 16, 25
	var wg sync.WaitGroup
	errs := make(chan error, writers*each*2)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				id := fmt.Sprintf("ck-%d-%d", w, i)
				if err := d.UpsertCheckpoint(&Checkpoint{ID: id, CheckpointID: id, Hash: fmt.Sprintf("h%d", w), Status: "Completed"}); err != nil {
					errs <- fmt.Errorf("upsert %s: %w", id, err)
				}
				if _, err := d.UpdatePVCPromoteStateByHash(fmt.Sprintf("h%d", w), "pending", ""); err != nil {
					errs <- fmt.Errorf("promote state %s: %w", id, err)
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	n := 0
	for err := range errs {
		if n < 3 {
			t.Error(err)
		}
		n++
	}
	if n > 0 {
		t.Fatalf("%d of %d writes failed", n, writers*each*2)
	}
}

// Every connection of the pool has the busy timeout and WAL, not only the
// first one.
func TestEveryConnectionHasPragmas(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := t.Context()
	var conns []interface{ Close() error }
	for i := 0; i < 4; i++ {
		c, err := d.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		var timeout int
		var mode string
		if err := c.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil {
			t.Fatal(err)
		}
		if err := c.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if timeout != 5000 || mode != "wal" {
			t.Errorf("connection %d: busy_timeout %d, journal_mode %q", i, timeout, mode)
		}
	}
	for _, c := range conns {
		_ = c.Close()
	}
}
