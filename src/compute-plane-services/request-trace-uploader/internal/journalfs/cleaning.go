// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package journalfs

import (
	"fmt"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
)

// BeginCleaning moves a Confirmed segment to Cleaning. Cleanup calls it
// before deleting anything, and calls it again after a restart, so it is a
// no-op on a segment already Cleaning.
func (s *Store) BeginCleaning(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seg, ok := s.segments[id]; ok && seg.State == journal.StateCleaning {
		return nil
	}
	return s.updateSegmentLocked(id, func(seg *journal.Segment) error {
		if err := transition(seg, journal.StateCleaning); err != nil {
			return err
		}
		seg.CleaningAt = s.now()
		return nil
	})
}

// FinishCleaning marks an uploaded segment Done once its transports and
// source are gone. The entry stays as a tombstone until Sweep removes it.
func (s *Store) FinishCleaning(id string) error {
	return s.finish(id, journal.StateCleaning, journal.ReasonUploaded)
}

// FinishEmpty marks an empty segment Done once its source is gone.
func (s *Store) FinishEmpty(id string) error {
	return s.finish(id, journal.StateEmpty, journal.ReasonEmpty)
}

func (s *Store) finish(id string, from journal.State, reason string) error {
	return s.updateSegment(id, func(seg *journal.Segment) error {
		if seg.State != from {
			return fmt.Errorf("%w: segment %s is %s, not %s", journal.ErrInvalidTransition, shortID(seg.ID), seg.State, from)
		}
		if err := transition(seg, journal.StateDone); err != nil {
			return err
		}
		seg.Reason = reason
		seg.DoneAt = s.now()
		return nil
	})
}

// Evict marks a segment Done after the disk guard deleted it to free space.
// This is the one transition outside the state machine table: it records a
// counted loss from any state. Evicting a Done segment is a no-op.
func (s *Store) Evict(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seg, ok := s.segments[id]; ok && seg.State == journal.StateDone {
		return nil
	}
	return s.updateSegmentLocked(id, func(seg *journal.Segment) error {
		seg.State = journal.StateDone
		seg.Reason = journal.ReasonEvicted
		seg.DoneAt = s.now()
		return nil
	})
}
