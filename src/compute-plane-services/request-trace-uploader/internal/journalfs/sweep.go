// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package journalfs

import (
	"errors"
	"path/filepath"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
)

// Sweep removes Done entries older than the tombstone TTL, then finished
// batches older than the TTL that no unfinished segment still points to. It
// returns how many entries it removed.
//
// A tombstone outlives its source, so a crash between deleting the source
// and recording Done cannot lead to a second upload of the same content.
func (s *Store) Sweep() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return 0, err
	}
	cutoff := s.now().Add(-s.cfg.TombstoneTTL)
	removed := 0
	var errs []error
	for id, seg := range s.segments {
		if seg.State != journal.StateDone || seg.DoneAt.After(cutoff) {
			continue
		}
		if err := removeDurably(filepath.Join(s.layout.segments(), segmentFileName(seg.Name, seg.ID))); err != nil {
			errs = append(errs, err)
			continue
		}
		delete(s.segments, id)
		removed++
	}
	referenced := map[string]bool{}
	for _, seg := range s.segments {
		if seg.State == journal.StateDone {
			continue
		}
		for _, dest := range seg.Destinations {
			referenced[dest.BatchID] = true
		}
	}
	for id, b := range s.batches {
		if !terminalBatch(b.State) || referenced[id] || b.UpdatedAt.After(cutoff) {
			continue
		}
		if err := removeDurably(filepath.Join(s.layout.batches(), batchFileName(id))); err != nil {
			errs = append(errs, err)
			continue
		}
		delete(s.batches, id)
		removed++
	}
	return removed, errors.Join(errs...)
}
