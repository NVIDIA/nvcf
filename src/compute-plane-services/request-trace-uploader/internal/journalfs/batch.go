// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package journalfs

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
)

// CreateBatch writes the batch at Submitting before the backend is called,
// then points every member destination at it. The batch file is written
// first and is the source of truth for membership, so a crash between the
// two steps is repaired by Recover.
//
// Member transport fields are taken from the member's own entry; the path a
// caller passes must match it.
func (s *Store) CreateBatch(b journal.Batch) error {
	if !batchIDPattern.MatchString(b.ID) {
		return errors.New("create batch: id must be 1 to 128 letters, digits, '-' or '_'")
	}
	if b.Backend == "" || b.Destination == "" || len(b.Members) == 0 {
		return errors.New("create batch: a backend, a destination and at least one member are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return err
	}
	if _, exists := s.batches[b.ID]; exists {
		return fmt.Errorf("create batch: batch %s already exists", b.ID)
	}
	members := make([]journal.Member, 0, len(b.Members))
	seen := make(map[string]bool, len(b.Members))
	for _, m := range b.Members {
		if seen[m.SegmentID] {
			return fmt.Errorf("create batch: segment %s listed twice", shortID(m.SegmentID))
		}
		seen[m.SegmentID] = true
		dest, err := s.batchableLocked(m, b)
		if err != nil {
			return fmt.Errorf("create batch: %w", err)
		}
		members = append(members, journal.Member{
			SegmentID: m.SegmentID,
			Path:      dest.Transport.Path,
			SHA256:    dest.Transport.SHA256,
			Bytes:     dest.Transport.Bytes,
			Rows:      dest.Transport.Rows,
		})
	}
	now := s.now()
	record := &journal.Batch{
		V:           journal.SchemaVersion,
		ID:          b.ID,
		Backend:     b.Backend,
		Destination: b.Destination,
		State:       journal.StateSubmitting,
		Members:     members,
		Attempts:    1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.writeBatch(record); err != nil {
		return err
	}
	s.batches[record.ID] = record
	for _, m := range record.Members {
		if err := s.attachLocked(m.SegmentID, record); err != nil {
			return fmt.Errorf("attach batch member: %w", err)
		}
	}
	return nil
}

func (s *Store) batchableLocked(m journal.Member, b journal.Batch) (*journal.Destination, error) {
	seg, ok := s.segments[m.SegmentID]
	if !ok {
		return nil, fmt.Errorf("%w: segment %s", journal.ErrNotFound, shortID(m.SegmentID))
	}
	if seg.Backend != b.Backend {
		return nil, fmt.Errorf("segment %s was prepared for another backend", shortID(seg.ID))
	}
	dest := seg.Destinations[b.Destination]
	if dest == nil {
		return nil, fmt.Errorf("segment %s has no transport for this destination", shortID(seg.ID))
	}
	if (seg.State != journal.StatePrepared && seg.State != journal.StateSubmitting) ||
		dest.State != journal.StatePrepared || dest.BatchID != "" {
		return nil, fmt.Errorf("%w: segment %s is %s and its destination is %s", journal.ErrInvalidTransition, shortID(seg.ID), seg.State, dest.State)
	}
	if m.Path != dest.Transport.Path {
		return nil, fmt.Errorf("segment %s member path does not match its transport", shortID(seg.ID))
	}
	return dest, nil
}

// attachLocked points one member destination at its batch. It is a no-op
// when the destination already points there.
func (s *Store) attachLocked(segmentID string, b *journal.Batch) error {
	seg, ok := s.segments[segmentID]
	if !ok {
		return fmt.Errorf("%w: segment %s", journal.ErrNotFound, shortID(segmentID))
	}
	if dest := seg.Destinations[b.Destination]; dest != nil && dest.BatchID == b.ID {
		return nil
	}
	return s.updateSegmentLocked(segmentID, func(seg *journal.Segment) error {
		dest := seg.Destinations[b.Destination]
		if dest == nil || dest.State != journal.StatePrepared || dest.BatchID != "" {
			return fmt.Errorf("%w: segment %s cannot join batch %s", journal.ErrInvalidTransition, shortID(seg.ID), b.ID)
		}
		dest.BatchID = b.ID
		dest.State = journal.StateSubmitting
		return s.settleInFlight(seg)
	})
}

// Batch returns a copy of one batch entry.
func (s *Store) Batch(id string) (journal.Batch, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.batches[id]
	if !ok {
		return journal.Batch{}, false
	}
	return copyBatch(b), true
}

// Batches returns copies of every batch entry, oldest first.
func (s *Store) Batches() []journal.Batch {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]journal.Batch, 0, len(s.batches))
	for _, b := range s.batches {
		out = append(out, copyBatch(b))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// SetReceipt records what the backend returned for a batch, in one write to
// the batch file, so every member becomes Submitted together. It must be
// called as soon as a non-empty id comes back, even when the call also
// returned an error. The receipt is written once: the same id again is a
// no-op, and a different id is ErrReceiptExists.
func (s *Store) SetReceipt(batchID string, r journal.Receipt) error {
	if r.ID == "" || r.Backend == "" {
		return errors.New("set receipt: a backend and an id are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.batches[batchID]
	if !ok {
		return fmt.Errorf("%w: batch %s", journal.ErrNotFound, batchID)
	}
	if r.Backend != b.Backend {
		return fmt.Errorf("set receipt: batch %s belongs to another backend", batchID)
	}
	if b.Receipt != nil {
		if b.Receipt.ID == r.ID {
			return nil
		}
		return fmt.Errorf("%w: batch %s", journal.ErrReceiptExists, batchID)
	}
	return s.updateBatchLocked(batchID, func(b *journal.Batch) error {
		if err := batchTransition(b, journal.StateSubmitted); err != nil {
			return err
		}
		now := s.now()
		if r.At.IsZero() {
			r.At = now
		}
		b.Receipt = &r
		b.SubmittedAt = now
		return nil
	})
}

// RetryBatch records a retryable failure that created nothing, with the time
// of the next attempt, so the wait survives a restart. A batch with a
// receipt has a job and is polled instead.
func (s *Store) RetryBatch(batchID string, next time.Time, errorClass string) error {
	return s.updateBatch(batchID, func(b *journal.Batch) error {
		if b.Receipt != nil {
			return fmt.Errorf("%w: batch %s has a job to poll", journal.ErrReceiptExists, batchID)
		}
		if err := batchTransition(b, journal.StateRetryWait); err != nil {
			return err
		}
		b.NextRetryAt = next
		b.LastErrorClass = errorClass
		return nil
	})
}

// ResumeBatch moves a waiting or ambiguous batch back to Submitting and counts
// the attempt. A batch with a receipt is never submitted again; resubmitting
// its members means releasing it and creating a new batch.
func (s *Store) ResumeBatch(batchID string) error {
	return s.updateBatch(batchID, func(b *journal.Batch) error {
		if b.Receipt != nil {
			return fmt.Errorf("%w: batch %s cannot be submitted again", journal.ErrReceiptExists, batchID)
		}
		if err := batchTransition(b, journal.StateSubmitting); err != nil {
			return err
		}
		b.Attempts++
		b.NextRetryAt = time.Time{}
		return nil
	})
}

// MarkBatchAmbiguous records that a submit may or may not have committed.
func (s *Store) MarkBatchAmbiguous(batchID, errorClass string) error {
	return s.updateBatch(batchID, func(b *journal.Batch) error {
		if err := batchTransition(b, journal.StateAmbiguous); err != nil {
			return err
		}
		b.LastErrorClass = errorClass
		return nil
	})
}

// MarkBatchStranded records a job that stayed non-terminal past its maximum
// age. Its members are kept and it is never resubmitted automatically.
func (s *Store) MarkBatchStranded(batchID string) error {
	return s.updateBatch(batchID, func(b *journal.Batch) error {
		return batchTransition(b, journal.StateStranded)
	})
}

// ConfirmBatch records SUCCESS for a batch with a receipt, then for every member
// destination. A member whose destinations are all confirmed becomes
// Confirmed and is ready for cleanup. Recover finishes a fan-out that a crash
// interrupted.
func (s *Store) ConfirmBatch(batchID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.updateBatchLocked(batchID, func(b *journal.Batch) error {
		if b.Receipt == nil {
			return fmt.Errorf("%w: batch %s has no receipt to confirm", journal.ErrInvalidTransition, batchID)
		}
		if err := batchTransition(b, journal.StateConfirmed); err != nil {
			return err
		}
		b.ConfirmedAt = s.now()
		return nil
	})
	if err != nil {
		return err
	}
	return s.settleMembersLocked(s.batches[batchID])
}

// ReleaseBatch ends a batch and returns its members' destinations to
// Prepared, so they can be batched again: alone after a split, or under a new
// batch after an ambiguous receipt. Quarantined or finished members stay as
// they are.
func (s *Store) ReleaseBatch(batchID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.updateBatchLocked(batchID, func(b *journal.Batch) error {
		return batchTransition(b, journal.StateReleased)
	})
	if err != nil {
		return err
	}
	return s.settleMembersLocked(s.batches[batchID])
}

// SettleBatches applies every confirmed or released batch to the members
// it has not reached yet, such as after a member write failed during
// ConfirmBatch. It writes nothing when every member is settled, so a caller
// can run it on every round.
func (s *Store) SettleBatches() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return err
	}
	var errs []error
	for _, b := range s.sortedBatchesLocked() {
		if terminalBatch(b.State) {
			errs = append(errs, s.settleMembersLocked(b))
		}
	}
	return errors.Join(errs...)
}

// settleMembersLocked applies a terminal batch state to every member
// destination that still points at the batch. It is idempotent and skips a
// destination that is already settled.
func (s *Store) settleMembersLocked(b *journal.Batch) error {
	var errs []error
	for _, m := range b.Members {
		seg, ok := s.segments[m.SegmentID]
		if !ok {
			continue
		}
		dest := seg.Destinations[b.Destination]
		if dest == nil || dest.BatchID != b.ID {
			continue
		}
		if b.State == journal.StateConfirmed && dest.State == journal.StateConfirmed && inFlightTarget(seg) == seg.State {
			continue
		}
		err := s.updateSegmentLocked(seg.ID, func(seg *journal.Segment) error {
			dest := seg.Destinations[b.Destination]
			switch b.State {
			case journal.StateConfirmed:
				dest.State = journal.StateConfirmed
				dest.ConfirmedAt = b.ConfirmedAt
				dest.Exported = b.Receipt != nil && b.Receipt.Exported
			case journal.StateReleased:
				dest.BatchID = ""
				if dest.State == journal.StateSubmitting {
					dest.State = journal.StatePrepared
				}
			}
			return s.settleInFlight(seg)
		})
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// settleInFlight derives a segment's state from its destinations while it is
// in flight: Confirmed when every destination is, Submitting while any is in
// a batch, and Prepared otherwise.
func (s *Store) settleInFlight(seg *journal.Segment) error {
	target := inFlightTarget(seg)
	if target == seg.State {
		return nil
	}
	if err := transition(seg, target); err != nil {
		return err
	}
	if target == journal.StateConfirmed {
		seg.ConfirmedAt = s.now()
	}
	return nil
}

func (s *Store) updateBatch(id string, change func(*journal.Batch) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateBatchLocked(id, change)
}

func (s *Store) updateBatchLocked(id string, change func(*journal.Batch) error) error {
	current, ok := s.batches[id]
	if !ok {
		return fmt.Errorf("%w: batch %s", journal.ErrNotFound, id)
	}
	next := copyBatch(current)
	if err := change(&next); err != nil {
		return err
	}
	next.UpdatedAt = s.now()
	if err := s.writeBatch(&next); err != nil {
		return err
	}
	s.batches[id] = &next
	return nil
}

func batchTransition(b *journal.Batch, to journal.State) error {
	if !journal.CanTransitionBatch(b.State, to) {
		return fmt.Errorf("%w: batch %s from %s to %s", journal.ErrInvalidTransition, b.ID, b.State, to)
	}
	b.State = to
	if to == journal.StateAmbiguous {
		b.MayHaveLanded = true
	}
	return nil
}

// inFlightTarget returns the state an in-flight segment should hold given
// its destinations. A segment that is not in flight keeps its state.
func inFlightTarget(seg *journal.Segment) journal.State {
	if !inFlight(seg.State) {
		return seg.State
	}
	target := journal.StatePrepared
	confirmed := 0
	for _, dest := range seg.Destinations {
		switch dest.State {
		case journal.StateSubmitting:
			target = journal.StateSubmitting
		case journal.StateConfirmed:
			confirmed++
		}
	}
	if confirmed > 0 && confirmed == len(seg.Destinations) {
		target = journal.StateConfirmed
	}
	return target
}
