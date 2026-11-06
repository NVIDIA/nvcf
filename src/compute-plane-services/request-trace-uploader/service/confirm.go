// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/internal/retry"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/journal"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
)

// pollRound reads the status of every job with a saved id.
func (u *Uploader) pollRound(ctx context.Context) error {
	now := u.now()
	u.saveUnsavedReceipts(now)
	// A confirmation whose member records failed is finished here, so its
	// segments reach cleanup without a restart.
	if err := u.store.SettleBatches(); err != nil {
		slog.Warn("finish recorded confirmations", "error", safeError(err))
	}
	for _, b := range u.store.Batches() {
		if b.Receipt == nil || b.Receipt.Backend != u.name {
			continue
		}
		switch b.State {
		case journal.StateSubmitted, journal.StateAmbiguous:
		case journal.StateStranded:
			if now.Sub(u.polledAt(b.ID)) < strandedPollInterval {
				continue
			}
		default:
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		u.stateMu.Lock()
		u.lastPolled[b.ID] = now
		u.stateMu.Unlock()
		status, err := u.client.Status(ctx, b.Receipt.ID)
		if err := u.applyStatus(b, status, err, now); err != nil {
			slog.Error("record job status", "batch", shortID(b.ID), "error", safeError(err))
		}
	}
	return nil
}

func (u *Uploader) applyStatus(b journal.Batch, status backend.Status, err error, now time.Time) error {
	if status == "" {
		// The read failed, so the job's state is unknown: keep polling,
		// and keep the job for an operator once it is past the max job age.
		fault := backend.Classify(u.client, err)
		u.countAuth(fault)
		u.policy.Failed(now, u.caps, retry.Failure{Key: b.ID, Stage: retry.StageStatus, Fault: fault, JobID: true, Attempts: b.Attempts + 1})
		slog.Warn("read job status", "batch", shortID(b.ID), "fault", faultText(fault))
		if u.pastMaxAge(b, now) {
			return u.strand(b, "job status unreadable past the max job age; it is kept and polled less often")
		}
		return nil
	}
	switch status {
	case backend.StatusSucceeded:
		if err := u.store.ConfirmBatch(b.ID); err != nil {
			if stored, ok := u.store.Batch(b.ID); ok && stored.State == journal.StateConfirmed {
				// The batch is confirmed; only member records failed, and
				// the next round finishes them.
				u.confirmed(b, now)
			}
			return err
		}
		u.point(PointConfirmed)
		u.confirmed(b, now)
	case backend.StatusPending:
		if u.pastMaxAge(b, now) {
			return u.strand(b, "job unfinished past the max job age; it is kept and polled less often")
		}
	case backend.StatusNotFound:
		if unresolved(b) {
			return u.strand(b, "the backend has no record of the job; it is kept for an operator")
		}
	case backend.StatusFailed:
		u.metrics.Add(telemetry.Uploads, 1, telemetry.Labels{"status": "failure"})
		if b.State == journal.StateStranded {
			return nil
		}
		fault := backend.Classify(u.client, err)
		u.countAuth(fault)
		attempts := max(b.Attempts, u.countFailure(b.Members))
		decision := u.policy.Failed(now, u.caps, retry.Failure{Key: failureKey(b.Members), Stage: retry.StageJob, Fault: fault, JobID: true, Attempts: attempts})
		slog.Warn("job failed", "batch", shortID(b.ID), "fault", faultText(fault), "action", decision.Action)
		switch decision.Action {
		case retry.ActionQuarantine:
			return u.split(b, errorClass(fault))
		case retry.ActionResendOnce:
			return u.resendOnce(b)
		}
		// The job finished without landing its data, so its members are
		// batched and submitted again.
		return u.store.ReleaseBatch(b.ID)
	default:
		u.metrics.Add(telemetry.UnknownStatus, 1, nil)
		slog.Warn("unknown job status; the job is polled again", "batch", shortID(b.ID))
	}
	return nil
}

// unresolved reports whether a batch is a job with a saved id that has not
// reached an outcome: submitted, or ambiguous from an older release.
func unresolved(b journal.Batch) bool {
	return b.State == journal.StateSubmitted || b.State == journal.StateAmbiguous
}

func (u *Uploader) pastMaxAge(b journal.Batch, now time.Time) bool {
	return unresolved(b) && !b.SubmittedAt.IsZero() && now.Sub(b.SubmittedAt) > u.cfg.MaxJobAge
}

// strand keeps a job for an operator: it is polled less often and never
// sent again automatically.
func (u *Uploader) strand(b journal.Batch, message string) error {
	u.metrics.Add(telemetry.JobsStranded, 1, nil)
	slog.Warn(message, "batch", shortID(b.ID))
	return u.store.MarkBatchStranded(b.ID)
}

// resendOnce sends again, as a new job, the work of a job that failed in a
// way that may still have landed. It does so once per member, counted as a
// possible duplicate; a job whose members were already sent again this way
// is kept for an operator.
func (u *Uploader) resendOnce(b journal.Batch) error {
	for _, m := range b.Members {
		seg, ok := u.store.Segment(m.SegmentID)
		if !ok {
			continue
		}
		if seg.UnconfirmedResends > 0 {
			return u.strand(b, "job failed again in a way that may have landed; it is kept for an operator")
		}
	}
	err := u.store.ReleaseForResend(b.ID)
	if stored, ok := u.store.Batch(b.ID); ok && stored.State == journal.StateReleased {
		// The release is recorded, so the work is sent again even when a
		// member record failed: the next round settles it.
		u.metrics.Add(telemetry.SubmitsAmbiguous, 1, nil)
		slog.Warn("job failed in a way that may have landed; its work is sent again once as a new job", "batch", shortID(b.ID))
	}
	return err
}

func (u *Uploader) confirmed(b journal.Batch, now time.Time) {
	var bytes int64
	u.stateMu.Lock()
	for _, m := range b.Members {
		bytes += m.Bytes
		delete(u.solo, m.SegmentID)
		delete(u.failures, m.SegmentID)
	}
	delete(u.lastPolled, b.ID)
	u.stateMu.Unlock()
	u.policy.Breaker().Succeeded()
	u.policy.Succeeded(now, failureKey(b.Members))
	u.metrics.Add(telemetry.Uploads, 1, telemetry.Labels{"status": "success"})
	u.metrics.Add(telemetry.TransportBytes, float64(bytes), nil)
	u.metrics.Set(telemetry.LastSuccessTimestamp, float64(now.Unix()), nil)
	if !b.SubmittedAt.IsZero() {
		u.metrics.Observe(telemetry.ConfirmLatency, now.Sub(b.SubmittedAt).Seconds(), nil)
	}
	u.observeExportLatency(b)
	slog.Info("request trace batch confirmed", "batch", shortID(b.ID), "segments", len(b.Members))
}

// observeExportLatency records, for each member this batch finished, the
// time from its discovery to its confirmation. A segment with several
// destinations is confirmed by the last of its batches, so it is observed
// once. Both times come from the journal's clock.
func (u *Uploader) observeExportLatency(b journal.Batch) {
	for _, m := range b.Members {
		seg, ok := u.store.Segment(m.SegmentID)
		if !ok || seg.DiscoveredAt.IsZero() || seg.ConfirmedAt.IsZero() {
			continue
		}
		if latency := seg.ConfirmedAt.Sub(seg.DiscoveredAt); latency >= 0 {
			u.metrics.Observe(telemetry.ExportLatency, latency.Seconds(), nil)
		}
	}
}

func (u *Uploader) polledAt(batchID string) time.Time {
	u.stateMu.Lock()
	defer u.stateMu.Unlock()
	return u.lastPolled[batchID]
}

// saveUnsavedReceipts records job ids the journal could not take when they
// came back. Until then their batches are neither polled nor sent again.
func (u *Uploader) saveUnsavedReceipts(now time.Time) {
	u.stateMu.Lock()
	pending := make(map[string]string, len(u.unsaved))
	for id, receipt := range u.unsaved {
		pending[id] = receipt
	}
	u.stateMu.Unlock()
	for id, receipt := range pending {
		if err := u.store.SetReceipt(id, journal.Receipt{Backend: u.name, ID: receipt, At: now, Exported: u.caps.Exports}); err != nil {
			slog.Warn("save a job id", "batch", shortID(id), "error", safeError(err))
			continue
		}
		u.stateMu.Lock()
		delete(u.unsaved, id)
		u.stateMu.Unlock()
	}
}
