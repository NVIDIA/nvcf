// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/retry"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
)

// pollRound reads the status of every job with a saved id.
func (u *Uploader) pollRound(ctx context.Context) error {
	now := time.Now()
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
		// The read failed, so the job's state is unknown: keep polling.
		fault := backend.Classify(u.client, err)
		u.countAuth(fault)
		u.policy.Failed(now, u.caps, retry.Failure{Key: b.ID, Stage: retry.StageStatus, Fault: fault, JobID: true, Attempts: b.Attempts + 1})
		slog.Warn("read job status", "batch", shortID(b.ID), "fault", faultText(fault))
		return nil
	}
	switch status {
	case backend.StatusSuccess:
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
		if b.State == journal.StateSubmitted && !b.SubmittedAt.IsZero() && now.Sub(b.SubmittedAt) > u.cfg.MaxJobAge {
			u.metrics.Add(telemetry.JobsStranded, 1, nil)
			slog.Warn("job unfinished past the max job age; it is kept and polled less often", "batch", shortID(b.ID))
			return u.store.MarkBatchStranded(b.ID)
		}
	case backend.StatusAmbiguous:
		if b.State == journal.StateSubmitted {
			return u.store.MarkBatchAmbiguous(b.ID, "job_ambiguous")
		}
	case backend.StatusGone:
		if b.State == journal.StateSubmitted {
			u.metrics.Add(telemetry.JobsStranded, 1, nil)
			slog.Warn("the backend no longer knows the job; it is kept for an operator", "batch", shortID(b.ID))
			return u.store.MarkBatchStranded(b.ID)
		}
	case backend.StatusFailure:
		u.metrics.Add(telemetry.Uploads, 1, telemetry.Labels{"status": "failure"})
		if b.State == journal.StateStranded {
			return nil
		}
		fault := backend.Classify(u.client, err)
		u.countAuth(fault)
		attempts := max(b.Attempts, u.countFailure(b.Members))
		decision := u.policy.Failed(now, u.caps, retry.Failure{Key: failureKey(b.Members), Stage: retry.StageJob, Fault: fault, JobID: true, Attempts: attempts})
		slog.Warn("job failed", "batch", shortID(b.ID), "fault", faultText(fault), "action", decision.Action)
		if decision.Action == retry.ActionQuarantine {
			return u.split(b, errorClass(fault))
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
	slog.Info("request trace batch confirmed", "batch", shortID(b.ID), "segments", len(b.Members))
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
