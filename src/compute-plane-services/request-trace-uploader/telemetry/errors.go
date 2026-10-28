// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"
)

// errorLogInterval bounds how often SDK errors are logged. Exports run every
// minute, so a collector that stays down gives one line per interval with a
// count, not one line per export.
const errorLogInterval = 5 * time.Minute

// exportFailure reports an export failure by its bounded reason only. The
// cause can quote the collector URL or its response body, so Error never
// prints it; Unwrap keeps it for errors.Is.
type exportFailure struct {
	reason string
	cause  error
}

func (e *exportFailure) Error() string { return "metrics export failed: " + e.reason }
func (e *exportFailure) Unwrap() error { return e.cause }

func exportError(err error) error {
	if err == nil {
		return nil
	}
	return &exportFailure{reason: failureReason(err), cause: err}
}

// failureReason is "timeout" for a deadline or network timeout and "error"
// for anything else.
func failureReason(err error) string {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return "timeout"
	}
	return "error"
}

// errorHandler receives the errors the OpenTelemetry SDK reports outside a
// call, such as a failed periodic export. It logs the reason and how many
// failures happened since its last line, never the error text, and at most
// once per interval.
type errorHandler struct {
	interval time.Duration
	now      func() time.Time

	mu       sync.Mutex
	last     time.Time
	failures int
}

func newErrorHandler(interval time.Duration, now func() time.Time) *errorHandler {
	return &errorHandler{interval: interval, now: now}
}

// Handle implements otel.ErrorHandler.
func (h *errorHandler) Handle(err error) {
	h.mu.Lock()
	h.failures++
	now := h.now()
	if !h.last.IsZero() && now.Sub(h.last) < h.interval {
		h.mu.Unlock()
		return
	}
	failures := h.failures
	h.failures = 0
	h.last = now
	h.mu.Unlock()
	slog.Warn("telemetry error, details withheld", "reason", failureReason(err), "failures", failures)
}
