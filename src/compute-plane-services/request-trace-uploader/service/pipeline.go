// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
)

const (
	// defaultLane names the journal destination of a backend that declares
	// none. The journal needs a non-empty name; the backend sees "".
	defaultLane = "default"
	// strandedPollInterval paces polls of a job past its max age.
	strandedPollInterval = 30 * time.Minute
	// Bounded reason codes recorded in the journal and on counters.
	reasonTooLarge      = "too_large"
	reasonRejected      = "rejected"
	reasonRetained      = "retained"
	reasonSourceMissing = "source_missing"
)

func laneOf(destination backend.Destination) string {
	if destination == "" {
		return defaultLane
	}
	return string(destination)
}

func (u *Uploader) destinationOf(lane string) backend.Destination {
	if lane == defaultLane && len(u.caps.Destinations) == 0 {
		return ""
	}
	return backend.Destination(lane)
}

// Pass runs one full round: it reads and prepares every new closed segment,
// batches and submits what is prepared, sends again what is due, polls every
// open job, and cleans up what is confirmed. The first call also runs the
// startup steps Run runs, so a test can drive the pipeline one pass at a
// time.
func (u *Uploader) Pass(ctx context.Context) error {
	return u.pass(ctx, true)
}

// scan is the pass Run repeats on the scan interval. It does not poll: Run
// polls on the status interval, so a short scan interval never shortens it.
func (u *Uploader) scan(ctx context.Context) error {
	return u.pass(ctx, false)
}

func (u *Uploader) pass(ctx context.Context, poll bool) error {
	if err := u.start(ctx); err != nil {
		return err
	}
	var errs []error
	if !u.draining.Load() {
		errs = append(errs, u.locked(&u.intakeMu, func() error { return u.intake(ctx, false) }))
	}
	errs = append(errs, u.locked(&u.submitMu, func() error { return errors.Join(u.submitPrepared(ctx), u.resend(ctx)) }))
	if poll {
		errs = append(errs, u.locked(&u.pollMu, func() error { return u.pollRound(ctx) }))
	}
	errs = append(errs, u.locked(&u.cleanupMu, func() error { return u.cleanupRound(ctx) }))
	u.housekeeping()
	return errors.Join(errs...)
}

func (u *Uploader) locked(mu *sync.Mutex, run func() error) error {
	mu.Lock()
	defer mu.Unlock()
	return run()
}

// Poll polls every open job once and cleans up what is confirmed. Run calls
// it on the status interval, apart from passes; it takes no lock a submit
// holds, so a slow submit or a hold never delays a confirmation.
func (u *Uploader) Poll(ctx context.Context) error {
	if err := u.start(ctx); err != nil {
		return err
	}
	return errors.Join(
		u.locked(&u.pollMu, func() error { return u.pollRound(ctx) }),
		u.locked(&u.cleanupMu, func() error { return u.cleanupRound(ctx) }),
	)
}

// drain scans the spool one last time, so a segment that closed after the
// last scan is not left behind, submits what is prepared, polls once more
// and cleans up. ctx carries the drain timeout; what it cuts short stays in
// the journal and on the spool for the next start.
func (u *Uploader) drain(ctx context.Context) error {
	return errors.Join(
		u.locked(&u.intakeMu, func() error { return u.intake(ctx, true) }),
		u.locked(&u.submitMu, func() error { return u.submitPrepared(ctx) }),
		u.locked(&u.pollMu, func() error { return u.pollRound(ctx) }),
		u.locked(&u.cleanupMu, func() error { return u.cleanupRound(ctx) }),
	)
}

// housekeeping drops old tombstones and refreshes the spool gauges.
func (u *Uploader) housekeeping() {
	if _, err := u.store.Sweep(); err != nil {
		slog.Warn("sweep the journal", "error", safeError(err))
	}
	pending, quarantined := 0, 0
	for _, seg := range u.store.Segments() {
		switch seg.State {
		case journal.StateDone:
		case journal.StateQuarantined:
			quarantined++
		default:
			pending++
		}
	}
	u.metrics.Set(telemetry.PendingSegments, float64(pending), nil)
	u.metrics.Set(telemetry.QuarantinedSegments, float64(quarantined), nil)
}

func (u *Uploader) countAuth(fault backend.Fault) {
	if fault.Reason == backend.ReasonAuth {
		u.metrics.Add(telemetry.AuthFailures, 1, telemetry.Labels{"stage": "api"})
	}
}

// errorClass is the bounded code the journal records for a fault.
func errorClass(fault backend.Fault) string {
	if fault.Reason != "" {
		return string(fault.Reason)
	}
	return reasonRejected
}

// faultText describes a fault without its cause, which can carry remote
// text, ids or paths.
func faultText(fault backend.Fault) string {
	return fmt.Sprintf("class=%s scope=%s reason=%s", fault.Class, fault.Scope, fault.Reason)
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// safeError describes err for a log line without the paths that file
// system errors carry.
func safeError(err error) string {
	if err == nil {
		return ""
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var parts []string
		for _, e := range joined.Unwrap() {
			if e != nil {
				parts = append(parts, safeError(e))
			}
		}
		return strings.Join(parts, "; ")
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Op + ": " + pathErr.Err.Error()
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return linkErr.Op + ": " + linkErr.Err.Error()
	}
	return err.Error()
}
