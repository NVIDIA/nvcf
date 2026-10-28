// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/cleanup"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
)

// cleanupRound finishes every confirmed or empty segment.
func (u *Uploader) cleanupRound(ctx context.Context) error {
	var errs []error
	for _, seg := range u.store.Segments() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		switch seg.State {
		case journal.StateConfirmed, journal.StateCleaning:
			err = u.clean(ctx, seg)
		case journal.StateEmpty:
			err = u.cleanEmpty(ctx, seg)
		}
		if err != nil {
			slog.Warn("clean up request trace segment", "segment", seg.Index, "error", safeError(err))
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func cleanupItem(sourceDir string, seg journal.Segment) cleanup.Item {
	item := cleanup.Item{
		SegmentID: seg.ID,
		Source:    filepath.Join(sourceDir, seg.Name),
		Prefix:    seg.Prefix,
		Index:     seg.Index,
		Size:      seg.Size,
		ModTime:   seg.ModTime,
		SHA256:    seg.SHA256,
	}
	for _, dest := range seg.Destinations {
		item.Transports = append(item.Transports, dest.Transport.Path)
	}
	return item
}

// clean deletes a confirmed segment's files. A segment whose confirmed
// submits did not all export (a diagnostic read, a dry run) never loses its
// source: only the transports go, and the entry is finished. The journal
// records this per submit, so a restart under a backend that exports does
// not change it.
func (u *Uploader) clean(ctx context.Context, seg journal.Segment) error {
	item := cleanupItem(u.cfg.SourceDir, seg)
	if exportedEverywhere(seg) {
		_, err := u.cleaner.Clean(ctx, item)
		return err
	}
	if err := u.store.BeginCleaning(seg.ID); err != nil {
		return err
	}
	for _, path := range item.Transports {
		if err := removeFile(path); err != nil {
			return err
		}
	}
	return u.store.FinishCleaning(seg.ID)
}

func exportedEverywhere(seg journal.Segment) bool {
	if len(seg.Destinations) == 0 {
		return false
	}
	for _, dest := range seg.Destinations {
		if !dest.Exported {
			return false
		}
	}
	return true
}

func (u *Uploader) cleanEmpty(ctx context.Context, seg journal.Segment) error {
	if u.caps.Exports {
		_, err := u.cleaner.CleanEmpty(ctx, cleanupItem(u.cfg.SourceDir, seg))
		return err
	}
	return u.store.FinishEmpty(seg.ID)
}
