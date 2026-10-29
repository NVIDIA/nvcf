// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/batch"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/retry"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/segment"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
)

// retainedUntil parks a retained batch: it is never due, so only an
// operator moves it.
var retainedUntil = time.Date(9999, time.December, 31, 0, 0, 0, 0, time.UTC)

// quarantineSegment records a quarantine and then moves the files, so a crash
// in between is finished by the next pass. The count is taken once, when the
// quarantine is recorded.
func (u *Uploader) quarantineSegment(id string, index int, reason string, paths ...string) error {
	if err := u.store.Quarantine(id, reason); err != nil {
		return err
	}
	u.metrics.Add(telemetry.SegmentsQuarantined, 1, quarantineLabels(reason))
	if u.policy.Breaker().Quarantined() {
		u.metrics.Add(telemetry.QuarantineTripped, 1, nil)
	}
	slog.Warn("quarantined request trace segment", "segment", index, "reason", reason)
	return u.quarantine.Admit(id, paths...)
}

// quarantineLabels labels a quarantine by its reason. A backend may name
// reasons of its own; any reason outside the known set is counted as unknown,
// so the label stays bounded. The journal and the log keep the reason as
// recorded.
func quarantineLabels(reason string) telemetry.Labels {
	if !slices.Contains(telemetry.QuarantineReasons, reason) {
		reason = "unknown"
	}
	return telemetry.Labels{"reason": reason}
}

// submitPrepared batches every prepared destination that is in no batch and
// submits the batches. Lanes are flushed at the end of each call, so a
// segment waits at most one scan interval, as with the earlier exporter.
func (u *Uploader) submitPrepared(ctx context.Context) error {
	now := time.Now()
	var batches []batch.Batch
	var errs []error
	for _, seg := range u.store.Segments() {
		if seg.State != journal.StatePrepared && seg.State != journal.StateSubmitting {
			continue
		}
		for lane, dest := range seg.Destinations {
			if dest.State != journal.StatePrepared || dest.BatchID != "" {
				continue
			}
			preparedAt := seg.PreparedAt
			if preparedAt.IsZero() {
				preparedAt = now
			}
			member := batch.Member{
				SegmentID:   seg.ID,
				Destination: lane,
				File:        batch.File{Path: dest.Transport.Path, SHA256: dest.Transport.SHA256, Bytes: dest.Transport.Bytes, Rows: dest.Transport.Rows},
				PreparedAt:  preparedAt,
			}
			if u.isSolo(seg.ID) {
				solo, err := u.batcher.Solo(member, now)
				if err != nil {
					errs = append(errs, err)
					continue
				}
				batches = append(batches, solo)
				continue
			}
			closed, err := u.batcher.Add(member, now)
			if errors.Is(err, batch.ErrMemberTooLarge) {
				errs = append(errs, u.quarantineSegment(seg.ID, seg.Index, reasonTooLarge, filepath.Join(u.cfg.SourceDir, seg.Name), dest.Transport.Path))
				continue
			}
			if err != nil {
				errs = append(errs, err)
				continue
			}
			batches = append(batches, closed...)
		}
	}
	batches = append(batches, u.batcher.Due(now.Add(batch.DefaultMaxWait))...)
	for _, b := range batches {
		if err := u.submit(ctx, b, false); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// resend submits again every batch whose retry is due, and every batch whose
// earlier submit may have committed without a job id; the second kind can
// land twice and is counted. When ambiguous submits are retained, such a
// batch is parked for an operator instead, as a failed submit would be.
func (u *Uploader) resend(ctx context.Context) error {
	now := time.Now()
	var errs []error
	for _, b := range u.store.Batches() {
		switch {
		case b.State == journal.StateRetryWait && !now.Before(b.NextRetryAt):
		case b.State == journal.StateAmbiguous && b.Receipt == nil && u.retainsAmbiguous():
			// Recovery counted it when it found the submit unfinished.
			slog.Warn("submit retained for an operator", "batch", shortID(b.ID))
			if err := u.store.RetryBatch(b.ID, retainedUntil, reasonRetained); err != nil {
				errs = append(errs, err)
			}
			continue
		case b.State == journal.StateAmbiguous && b.Receipt == nil:
			u.metrics.Add(telemetry.SubmitsAmbiguous, 1, nil)
		default:
			continue
		}
		if err := u.submit(ctx, toBatch(b), true); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// retainsAmbiguous reports whether a submit that may have landed without a
// job id waits for an operator instead of being sent again.
func (u *Uploader) retainsAmbiguous() bool {
	return u.faults.AmbiguousWithoutID == retry.AmbiguousRetain && !u.caps.ResubmitSafe
}

func toBatch(b journal.Batch) batch.Batch {
	out := batch.Batch{ID: b.ID, Destination: b.Destination, ClosedAt: b.CreatedAt}
	for _, m := range b.Members {
		out.Members = append(out.Members, batch.Member{
			SegmentID:   m.SegmentID,
			Destination: b.Destination,
			File:        batch.File{Path: m.Path, SHA256: m.SHA256, Bytes: m.Bytes, Rows: m.Rows},
			PreparedAt:  b.CreatedAt,
		})
		out.Bytes += m.Bytes
	}
	return out
}

// submit waits for the create budget and any hold, records the batch (a new
// one) or moves it back to Submitting (a resend), sends it, and saves the job
// id before it looks at the error.
func (u *Uploader) submit(ctx context.Context, b batch.Batch, resend bool) error {
	if err := u.policy.WaitSubmit(ctx); err != nil {
		return err
	}
	j := batchJournal{store: u.store, point: u.point, exports: u.caps.Exports}
	if resend {
		if err := u.store.ResumeBatch(b.ID); err != nil {
			return err
		}
	} else if err := j.OpenBatch(b, u.name); err != nil {
		// A batch recorded with only some members pointing at it must
		// never be sent: a member that does not point at it would be
		// batched and sent again. Release it; nothing was sent.
		if _, recorded := u.store.Batch(b.ID); recorded {
			err = errors.Join(err, u.store.ReleaseBatch(b.ID))
		}
		return fmt.Errorf("record batch before submit: %w", err)
	}
	receipt, err := batch.Send(ctx, j, u.name, b, u.send)
	if receipt != "" {
		if errors.Is(err, batch.ErrReceiptNotSaved) {
			// Keep the id, so a later round saves it instead of
			// submitting again.
			u.stateMu.Lock()
			u.unsaved[b.ID] = receipt
			u.stateMu.Unlock()
			return err
		}
		if err != nil {
			slog.Warn("create returned a job id and an error; the job is polled", "batch", shortID(b.ID), "fault", faultText(backend.Classify(u.client, err)))
		}
		slog.Info("submitted request trace batch", "batch", shortID(b.ID), "segments", len(b.Members), "bytes", b.Bytes)
		return nil
	}
	if err == nil {
		err = errors.New("backend returned no job id")
	}
	return u.submitFailed(b, err)
}

func (u *Uploader) send(ctx context.Context, b batch.Batch) (string, error) {
	request := backend.SubmitRequest{Destination: u.destinationOf(b.Destination)}
	for _, m := range b.Members {
		request.Files = append(request.Files, backend.File{Path: m.File.Path, Bytes: m.File.Bytes, Rows: m.File.Rows})
	}
	if len(b.Members) == 1 {
		request.Path = b.Members[0].File.Path
		if seg, ok := u.store.Segment(b.Members[0].SegmentID); ok {
			request.Segment = segment.Segment{
				Path:    filepath.Join(u.cfg.SourceDir, seg.Name),
				Prefix:  seg.Prefix,
				Index:   seg.Index,
				Size:    seg.Size,
				ModTime: seg.ModTime,
			}
		}
	}
	if err := u.caps.Check(request); err != nil {
		return "", err
	}
	id, err := u.client.Submit(ctx, request)
	u.point(PointSubmitted)
	return id, err
}

// submitFailed applies the fault policy to a submit that created no job.
func (u *Uploader) submitFailed(b batch.Batch, err error) error {
	now := time.Now()
	stored, _ := u.store.Batch(b.ID)
	fault := backend.Classify(u.client, err)
	u.countAuth(fault)
	// The journal counted this attempt when the batch was recorded or
	// resumed, so it is not added again.
	attempts := max(stored.Attempts, u.countFailure(stored.Members))
	decision := u.policy.Failed(now, u.caps, retry.Failure{Key: failureKey(stored.Members), Stage: retry.StageSubmit, Fault: fault, Attempts: attempts})
	class := errorClass(fault)
	slog.Warn("submit failed", "batch", shortID(b.ID), "segments", len(b.Members), "fault", faultText(fault), "action", decision.Action)
	switch decision.Action {
	case retry.ActionResubmit:
		return u.store.MarkBatchAmbiguous(b.ID, class)
	case retry.ActionQuarantine:
		return u.split(stored, class)
	case retry.ActionRetain:
		// The create may have landed and the backend cannot take it twice,
		// so the batch waits for an operator and is never sent again
		// automatically, also after a restart. It is recorded as ambiguous
		// first, so a restart never treats it as only refused.
		slog.Warn("submit retained for an operator", "batch", shortID(b.ID))
		if err := u.store.MarkBatchAmbiguous(b.ID, class); err != nil {
			return err
		}
		return u.store.RetryBatch(b.ID, retainedUntil, reasonRetained)
	}
	next := now.Add(u.retryDelay(stored.Attempts))
	if decision.Hold.Until.After(next) {
		next = decision.Hold.Until
	}
	return u.store.RetryBatch(b.ID, next, class)
}

func (u *Uploader) retryDelay(attempts int) time.Duration {
	delay := time.Duration(0)
	for range attempts + 1 {
		delay = u.faults.Transient.Doubling(delay)
	}
	return delay
}

// split ends a batch that a segment fault failed. A batch of one quarantines
// its segment; a larger batch is released and its members go alone, so the
// one at fault is found.
func (u *Uploader) split(b journal.Batch, reason string) error {
	if err := u.store.ReleaseBatch(b.ID); err != nil {
		return err
	}
	u.stateMu.Lock()
	defer u.stateMu.Unlock()
	if len(b.Members) > 1 {
		for _, m := range b.Members {
			u.solo[m.SegmentID] = true
		}
		u.metrics.Add(telemetry.BatchSplits, 1, nil)
		return nil
	}
	m := b.Members[0]
	seg, ok := u.store.Segment(m.SegmentID)
	if !ok {
		return nil
	}
	delete(u.solo, m.SegmentID)
	delete(u.failures, m.SegmentID)
	return u.quarantineSegment(seg.ID, seg.Index, reason, filepath.Join(u.cfg.SourceDir, seg.Name), m.Path)
}

func (u *Uploader) isSolo(segmentID string) bool {
	u.stateMu.Lock()
	defer u.stateMu.Unlock()
	return u.solo[segmentID]
}

// failureKey names a batch's work for fault correlation. A batch sent again
// gets a new id, so the key is the member set instead: one segment failing
// under a run of replacement batches counts as one item, never as many
// items failing, which would hold the whole backend.
func failureKey(members []journal.Member) string {
	if len(members) == 1 {
		return members[0].SegmentID
	}
	ids := make([]string, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.SegmentID)
	}
	slices.Sort(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
	return "batch-" + hex.EncodeToString(sum[:])
}

// countFailure records one more failure for every member and returns the
// highest count, so a segment's history survives the new batch id each
// retry gets.
func (u *Uploader) countFailure(members []journal.Member) int {
	u.stateMu.Lock()
	defer u.stateMu.Unlock()
	highest := 0
	for _, m := range members {
		u.failures[m.SegmentID]++
		highest = max(highest, u.failures[m.SegmentID])
	}
	return highest
}
