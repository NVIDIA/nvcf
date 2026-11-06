// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package journalfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// fenceSegment moves an unreadable journal file, its source and its
// transports to unreadable/<name>.<id>/, so the source is never discovered
// and submitted again as new work.
//
// The fence directory is linked durably before anything moves into it, and
// the journal file moves last: a crash part way leaves the journal file in
// place, and the next start finds the source already in the fence and
// finishes the move. A source already in the fence is never replaced; a
// file under the same name in the spool is then a new segment and stays for
// upload.
//
// The file name always identifies the source here; loadSegments stops the
// spool with ErrUnreadableState when it does not. A source that is gone left
// nothing to upload again, so the journal file and transports are fenced
// alone.
func (s *Store) fenceSegment(file, name, id string) error {
	fence := filepath.Join(s.layout.unreadable(), name+"."+id)
	source := filepath.Join(s.cfg.SpoolDir, name)
	sourceInSpool, err := present(source)
	if err != nil {
		return err
	}
	sourceFenced, err := present(filepath.Join(fence, name))
	if err != nil {
		return err
	}
	if err := mkdirDurable(s.hooks, fence); err != nil {
		return err
	}
	moves, err := filepath.Glob(transportGlob(s.layout.transport(), id))
	if err != nil {
		return fmt.Errorf("list transports of unreadable journal entry: %w", err)
	}
	if sourceInSpool && !sourceFenced {
		moves = append(moves, source)
	}
	moves = append(moves, filepath.Join(s.layout.segments(), file))
	for _, path := range moves {
		if err := s.moveInto(path, fence); err != nil {
			return err
		}
	}
	for _, dir := range []string{fence, s.layout.transport(), s.cfg.SpoolDir, s.layout.segments()} {
		if err := syncDirWith(s.hooks, dir); err != nil {
			return err
		}
	}
	s.counter.Add(CounterUnreadable, 1)
	s.logger.Warn("fenced an unreadable journal entry",
		"segment_id", shortID(id),
		"source_present", sourceInSpool || sourceFenced)
	return nil
}

// fenceBatch moves an unreadable batch file aside. Its members are batched
// again by Recover, because the batch file is the only record of whether it
// was submitted.
func (s *Store) fenceBatch(file string) error {
	dir := filepath.Join(s.layout.unreadable(), "batches")
	if err := mkdirDurable(s.hooks, dir); err != nil {
		return err
	}
	if err := s.moveInto(filepath.Join(s.layout.batches(), file), dir); err != nil {
		return err
	}
	if err := errors.Join(syncDirWith(s.hooks, dir), syncDirWith(s.hooks, s.layout.batches())); err != nil {
		return err
	}
	s.counter.Add(CounterUnreadable, 1)
	s.logger.Warn("fenced an unreadable batch entry")
	return nil
}

// moveInto renames path into dir without replacing anything there: a name
// already taken gets a numbered suffix. A path already gone is skipped, so an
// interrupted fence can be repeated.
func (s *Store) moveInto(path, dir string) error {
	if found, err := present(path); err != nil {
		return err
	} else if !found {
		return nil
	}
	target := filepath.Join(dir, filepath.Base(path))
	for n := 1; ; n++ {
		taken, err := present(target)
		if err != nil {
			return err
		}
		if !taken {
			break
		}
		target = filepath.Join(dir, filepath.Base(path)+"."+strconv.Itoa(n))
	}
	if err := os.Rename(path, target); err != nil {
		return fmt.Errorf("fence unreadable journal entry: %w", err)
	}
	s.hooks.emit("rename", target)
	return nil
}

func present(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("stat for fencing: %w", err)
}
