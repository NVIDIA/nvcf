// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package otlp

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

// limiter lets one log line per key through each interval and counts the
// events in between, so a repeating failure gives a periodic count instead of
// a line per event.
type limiter struct {
	interval time.Duration
	now      func() time.Time

	mu     sync.Mutex
	last   map[string]time.Time
	counts map[string]int
}

func newLimiter(interval time.Duration, now func() time.Time) *limiter {
	return &limiter{interval: interval, now: now, last: map[string]time.Time{}, counts: map[string]int{}}
}

// admit records one event for key. It reports whether to log now and, if
// so, how many events happened since the last line for key.
func (l *limiter) admit(key string) (int, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.counts[key]++
	now := l.now()
	if last, ok := l.last[key]; ok && now.Sub(last) < l.interval {
		return 0, false
	}
	count := l.counts[key]
	l.counts[key] = 0
	l.last[key] = now
	return count, true
}

// errorHandler receives the errors the OpenTelemetry SDK reports outside a
// call, such as a failed periodic export. It logs the reason and how many
// failures happened since its last line, never the error text, and at most
// once per interval.
type errorHandler struct{ limit *limiter }

func newErrorHandler(interval time.Duration, now func() time.Time) *errorHandler {
	return &errorHandler{limit: newLimiter(interval, now)}
}

// Handle implements otel.ErrorHandler.
func (h *errorHandler) Handle(err error) {
	if failures, ok := h.limit.admit(""); ok {
		slog.Warn("telemetry error, details withheld", "reason", failureReason(err), "failures", failures)
	}
}
