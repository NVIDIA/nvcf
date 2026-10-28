// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package journalfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
)

// ErrorClassRestart is recorded on a batch that recovery found mid-submit.
const ErrorClassRestart = "restart"

// RecoverOptions names the backend this process runs and the other backends
// linked into the build.
type RecoverOptions struct {
	Backend string
	// Linked reports whether another backend is linked into this build, so
	// its receipts can still be polled after a switch. Nil means none.
	Linked func(name string) bool
}

// Recover repairs what a crash or a backend switch left and returns what to
// do next. Run it once after Open, before the pipeline starts.
//
//   - A batch file is the source of truth for membership: members it lists
//     are re-attached, and a confirmed or released batch is applied to every
//     member again.
//   - A member that points to a batch that no longer exists is returned to
//     Prepared and counted as ambiguous.
//   - A batch with no receipt that lost a member (fenced, evicted,
//     quarantined) is released, so the rest are batched again without it. A
//     batch with a receipt is still polled.
//   - Work started under a backend that is not linked any more goes back to
//     Discovered, so it is prepared and uploaded again under the running
//     backend. A receipt is never read by another backend.
//   - A segment found Preparing goes back to Discovered and its partial
//     transports are removed.
//   - A batch found Submitting with no receipt may have committed, so it
//     becomes Ambiguous, counted. The caller resubmits it or, when it
//     retains ambiguous submits, parks it for an operator. A batch with a
//     receipt is polled, never resubmitted.
func (s *Store) Recover(opts RecoverOptions) (journal.Plan, error) {
	if opts.Backend == "" {
		return journal.Plan{}, errors.New("recover journal: the running backend name is required")
	}
	pollable := func(b *journal.Batch) bool {
		return b.Receipt != nil && (b.Backend == opts.Backend || (opts.Linked != nil && opts.Linked(b.Backend)))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return journal.Plan{}, err
	}

	var plan journal.Plan
	var errs []error
	ambiguous := 0

	for _, b := range s.sortedBatchesLocked() {
		if terminalBatch(b.State) {
			errs = append(errs, s.settleMembersLocked(b))
			continue
		}
		for _, m := range b.Members {
			errs = append(errs, s.reattachLocked(m.SegmentID, b))
		}
	}

	orphans, err := s.detachOrphansLocked()
	errs = append(errs, err)
	ambiguous += orphans

	for _, b := range s.sortedBatchesLocked() {
		if terminalBatch(b.State) || b.Receipt != nil || !s.lostMemberLocked(b) {
			continue
		}
		if b.State == journal.StateSubmitting {
			ambiguous++
		}
		err := s.updateBatchLocked(b.ID, func(b *journal.Batch) error {
			return batchTransition(b, journal.StateReleased)
		})
		errs = append(errs, err)
		if err == nil {
			errs = append(errs, s.settleMembersLocked(s.batches[b.ID]))
		}
	}

	for _, b := range s.sortedBatchesLocked() {
		if terminalBatch(b.State) || b.Backend == opts.Backend || pollable(b) {
			continue
		}
		resets, err := s.resetBatchLocked(b, pollable)
		errs = append(errs, err)
		plan.BackendResets += resets
	}
	for _, seg := range s.sortedSegmentsLocked() {
		if !inFlight(seg.State) || seg.Backend == opts.Backend || s.heldByPollableBatchLocked(seg, pollable) {
			continue
		}
		errs = append(errs, s.resetSegmentLocked(seg.ID))
		plan.BackendResets++
	}

	for _, seg := range s.sortedSegmentsLocked() {
		switch seg.State {
		case journal.StatePreparing:
			err := s.updateSegmentLocked(seg.ID, func(seg *journal.Segment) error {
				seg.PrepareStartedAt = time.Time{}
				return transition(seg, journal.StateDiscovered)
			})
			errs = append(errs, err, s.removeTransportsLocked(seg.ID, nil))
			plan.PreparesReset++
		case journal.StateDiscovered:
			errs = append(errs, s.removeTransportsLocked(seg.ID, nil))
		}
	}

	for _, b := range s.sortedBatchesLocked() {
		action, counted, err := s.batchActionLocked(b, opts, pollable)
		errs = append(errs, err)
		if counted {
			ambiguous++
		}
		if action.Action != journal.ActionNone {
			plan.Batches = append(plan.Batches, action)
		}
	}
	for _, seg := range s.sortedSegmentsLocked() {
		action := journal.RestartAction(seg.State)
		if inFlight(seg.State) {
			action = journal.ActionNone
			if seg.Backend == opts.Backend && hasUnbatchedDestination(seg) {
				action = journal.ActionBatch
			}
		}
		if action != journal.ActionNone {
			plan.Segments = append(plan.Segments, journal.SegmentAction{ID: seg.ID, State: seg.State, Action: action})
		}
	}

	s.counter.Add(CounterBatchesAmbiguous, int64(ambiguous))
	s.counter.Add(CounterBackendResets, int64(plan.BackendResets))
	if err := errors.Join(errs...); err != nil {
		return plan, fmt.Errorf("recover journal: %w", err)
	}
	s.logger.Info("journal recovered",
		"segments", len(s.segments),
		"segment_actions", len(plan.Segments),
		"batch_actions", len(plan.Batches),
		"prepares_reset", plan.PreparesReset,
		"batches_ambiguous", ambiguous,
		"backend_resets", plan.BackendResets)
	return plan, nil
}

func (s *Store) batchActionLocked(b *journal.Batch, opts RecoverOptions, pollable func(*journal.Batch) bool) (journal.BatchAction, bool, error) {
	action := journal.BatchAction{ID: b.ID, State: b.State, Action: journal.ActionNone}
	switch {
	case terminalBatch(b.State):
	case pollable(b):
		receipt := *b.Receipt
		action.Action = journal.ActionPoll
		action.Receipt = &receipt
	case b.Backend != opts.Backend:
	case b.State == journal.StateSubmitting:
		err := s.updateBatchLocked(b.ID, func(b *journal.Batch) error {
			b.LastErrorClass = ErrorClassRestart
			return batchTransition(b, journal.StateAmbiguous)
		})
		if err != nil {
			return action, false, err
		}
		action.State = journal.StateAmbiguous
		action.Action = journal.ActionSubmit
		action.Ambiguous = true
		return action, true, nil
	case b.State == journal.StateAmbiguous:
		action.Action = journal.ActionSubmit
		action.Ambiguous = true
	case b.State == journal.StateRetryWait:
		action.Action = journal.ActionRetry
		action.NextRetryAt = b.NextRetryAt
	}
	return action, false, nil
}

// reattachLocked points a listed member at its batch when a crash stopped
// CreateBatch after the batch file was written. Members that moved on, such
// as a quarantined or reset segment, are left alone.
func (s *Store) reattachLocked(segmentID string, b *journal.Batch) error {
	seg, ok := s.segments[segmentID]
	if !ok || !inFlight(seg.State) || seg.Backend != b.Backend {
		return nil
	}
	dest := seg.Destinations[b.Destination]
	if dest == nil || dest.BatchID != "" || dest.State != journal.StatePrepared {
		return nil
	}
	return s.attachLocked(segmentID, b)
}

// lostMemberLocked reports whether a listed member can no longer be sent
// with the batch: its entry is gone or finished, or it left the batch.
func (s *Store) lostMemberLocked(b *journal.Batch) bool {
	for _, m := range b.Members {
		seg, ok := s.segments[m.SegmentID]
		if !ok || !inFlight(seg.State) {
			return true
		}
		if dest := seg.Destinations[b.Destination]; dest == nil || dest.BatchID != b.ID {
			return true
		}
	}
	return false
}

// detachOrphansLocked returns destinations whose batch file is gone to
// Prepared. It returns the number of distinct missing batches.
func (s *Store) detachOrphansLocked() (int, error) {
	missing := map[string]bool{}
	var errs []error
	for _, seg := range s.sortedSegmentsLocked() {
		var orphaned []string
		for name, dest := range seg.Destinations {
			if dest.BatchID != "" && s.batches[dest.BatchID] == nil {
				orphaned = append(orphaned, name)
				missing[dest.BatchID] = true
			}
		}
		if len(orphaned) == 0 {
			continue
		}
		errs = append(errs, s.updateSegmentLocked(seg.ID, func(seg *journal.Segment) error {
			for _, name := range orphaned {
				dest := seg.Destinations[name]
				dest.BatchID = ""
				if dest.State == journal.StateSubmitting {
					dest.State = journal.StatePrepared
				}
			}
			return s.settleInFlight(seg)
		}))
	}
	return len(missing), errors.Join(errs...)
}

// resetBatchLocked ends a batch of a backend that is no longer linked and
// sends its members back to Discovered. A Stranded batch is ended too: the
// state machine never resubmits it, but its backend can no longer read it,
// so uploading again under the running backend is the only way forward.
// A member that a pollable batch still holds, such as the other destination
// of a dual write, is only detached from this batch. It is reset after that
// poll settles, so a job that may land is never uploaded again.
func (s *Store) resetBatchLocked(b *journal.Batch, pollable func(*journal.Batch) bool) (int, error) {
	err := s.updateBatchLocked(b.ID, func(b *journal.Batch) error {
		b.State = journal.StateReleased
		return nil
	})
	if err != nil {
		return 0, err
	}
	resets := 0
	var errs []error
	for _, m := range b.Members {
		seg, ok := s.segments[m.SegmentID]
		if !ok || !inFlight(seg.State) || s.heldByPollableBatchLocked(seg, pollable) {
			continue
		}
		errs = append(errs, s.resetSegmentLocked(seg.ID))
		resets++
	}
	errs = append(errs, s.settleMembersLocked(s.batches[b.ID]))
	return resets, errors.Join(errs...)
}

// resetSegmentLocked sends an in-flight segment back to Discovered with no
// backend and no transports. The entry is written first, so no entry ever
// names a transport that was already removed.
func (s *Store) resetSegmentLocked(id string) error {
	paths := transportPaths(s.segments[id])
	err := s.updateSegmentLocked(id, func(seg *journal.Segment) error {
		if err := transition(seg, journal.StateDiscovered); err != nil {
			return err
		}
		seg.Backend = ""
		seg.Destinations = nil
		seg.PrepareStartedAt = time.Time{}
		seg.PreparedAt = time.Time{}
		return nil
	})
	if err != nil {
		return err
	}
	return s.removeTransportsLocked(id, paths)
}

// removeTransportsLocked removes the named transport files and every file in
// the transport directory that belongs to the segment.
func (s *Store) removeTransportsLocked(id string, paths []string) error {
	matches, err := filepath.Glob(transportGlob(s.layout.transport(), id))
	if err != nil {
		return fmt.Errorf("list transports: %w", err)
	}
	var errs []error
	for _, path := range append(paths, matches...) {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove transport: %w", err))
		}
	}
	return errors.Join(errs...)
}

func (s *Store) heldByPollableBatchLocked(seg *journal.Segment, pollable func(*journal.Batch) bool) bool {
	for _, dest := range seg.Destinations {
		if b := s.batches[dest.BatchID]; b != nil && !terminalBatch(b.State) && pollable(b) {
			return true
		}
	}
	return false
}

func (s *Store) sortedSegmentsLocked() []*journal.Segment {
	out := make([]*journal.Segment, 0, len(s.segments))
	for _, seg := range s.segments {
		out = append(out, seg)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Index != out[j].Index {
			return out[i].Index < out[j].Index
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (s *Store) sortedBatchesLocked() []*journal.Batch {
	out := make([]*journal.Batch, 0, len(s.batches))
	for _, b := range s.batches {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func transportPaths(seg *journal.Segment) []string {
	if seg == nil {
		return nil
	}
	paths := make([]string, 0, len(seg.Destinations))
	for _, dest := range seg.Destinations {
		if dest.Transport.Path != "" {
			paths = append(paths, dest.Transport.Path)
		}
	}
	return paths
}

func hasUnbatchedDestination(seg *journal.Segment) bool {
	for _, dest := range seg.Destinations {
		if dest.State == journal.StatePrepared && dest.BatchID == "" {
			return true
		}
	}
	return false
}

func inFlight(state journal.State) bool {
	return state == journal.StatePrepared || state == journal.StateSubmitting
}

func terminalBatch(state journal.State) bool {
	return state == journal.StateConfirmed || state == journal.StateReleased
}
