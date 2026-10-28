// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package shadow

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/cleanup"
)

// Finisher takes the place of cleanup in shadow mode. It records Cleaning,
// removes the segment's transports, and records Done. It never looks at the
// source, and it refuses any path outside the shadow state directory.
type Finisher struct {
	journal  cleanup.Journal
	stateDir string
	counter  cleanup.Counter
}

// NewFinisher builds a finisher. counter may be nil. It counts finished
// segments under the cleanup counter names, so the same dashboards serve
// both modes.
func NewFinisher(cfg Config, journal cleanup.Journal, counter cleanup.Counter) (*Finisher, error) {
	cfg, err := cfg.resolved()
	if err != nil {
		return nil, err
	}
	if journal == nil {
		return nil, errors.New("shadow finisher needs a journal")
	}
	if counter == nil {
		counter = discardCounter{}
	}
	for _, name := range []string{cleanup.CounterSegmentsDone, cleanup.CounterSegmentsEmpty} {
		counter.Add(name, 0)
	}
	return &Finisher{journal: journal, stateDir: cfg.StateDir, counter: counter}, nil
}

// Clean finishes a segment every destination confirmed.
func (f *Finisher) Clean(ctx context.Context, item cleanup.Item) (cleanup.Result, error) {
	transports, err := f.check(ctx, item)
	if err != nil {
		return "", err
	}
	if err := f.journal.BeginCleaning(item.SegmentID); err != nil {
		return "", fmt.Errorf("record cleanup start: %w", err)
	}
	for _, path := range transports {
		if err := removeDurably(path); err != nil {
			return "", fmt.Errorf("delete transport: %w", err)
		}
	}
	if err := f.journal.FinishCleaning(item.SegmentID); err != nil {
		return "", fmt.Errorf("record cleanup done: %w", err)
	}
	f.counter.Add(cleanup.CounterSegmentsDone, 1)
	return cleanup.ResultDone, nil
}

// CleanEmpty finishes a segment that held no records.
func (f *Finisher) CleanEmpty(ctx context.Context, item cleanup.Item) (cleanup.Result, error) {
	if _, err := f.check(ctx, item); err != nil {
		return "", err
	}
	if err := f.journal.FinishEmpty(item.SegmentID); err != nil {
		return "", fmt.Errorf("record empty segment done: %w", err)
	}
	f.counter.Add(cleanup.CounterSegmentsEmpty, 1)
	return cleanup.ResultDone, nil
}

// check refuses the whole item before anything changes, so a wrong path in a
// journal entry, or a link on the way to it, can never remove a file the
// owner uses. It returns the transports to remove with links resolved, so
// the removal reaches exactly the file that was checked. A path must be
// clean: the kernel follows a link before "..", but filepath.Dir does not.
func (f *Finisher) check(ctx context.Context, item cleanup.Item) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if item.SegmentID == "" {
		return nil, errors.New("shadow finish: segment id is required")
	}
	var transports []string
	for _, path := range item.Transports {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, errors.New("shadow finish: transport path must be absolute and clean")
		}
		dir, err := filepath.EvalSymlinks(filepath.Dir(path))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("shadow finish: resolve transport directory: %w", err)
		}
		resolved := filepath.Join(dir, filepath.Base(path))
		if _, ok := below(f.stateDir, resolved); !ok {
			return nil, errors.New("shadow finish: transport is outside the shadow state directory")
		}
		transports = append(transports, resolved)
	}
	return transports, nil
}

// removeDurably removes path and syncs its directory, so a crash after the
// journal records Done cannot bring the file back. It syncs even when the
// file is already gone, because a retry may follow a sync that failed.
func removeDurably(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := syncDir(filepath.Dir(path)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

type discardCounter struct{}

func (discardCounter) Add(string, int64) {}
