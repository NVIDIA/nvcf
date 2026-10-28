// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type timeoutError struct{}

func (timeoutError) Error() string   { return "dial tcp 10.0.0.1:4317: i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestFailureReason(t *testing.T) {
	tests := map[string]struct {
		err  error
		want string
	}{
		"deadline":         {context.DeadlineExceeded, "timeout"},
		"wrapped deadline": {fmt.Errorf("export: %w", context.DeadlineExceeded), "timeout"},
		"network timeout":  {fmt.Errorf("post: %w", timeoutError{}), "timeout"},
		"rejected":         {errors.New("failed to upload metrics: 400 Bad Request: body: nope"), "error"},
		"cancelled":        {context.Canceled, "error"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := failureReason(tt.err); got != tt.want {
				t.Fatalf("failureReason() = %q, want %q", got, tt.want)
			}
		})
	}
}

func captureDefaultLog(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	return buf
}

func TestErrorHandlerLogsAReasonAndNeverTheErrorText(t *testing.T) {
	logs := captureDefaultLog(t)
	h := newErrorHandler(time.Minute, time.Now)

	h.Handle(errors.New(`failed to upload metrics: Post "http://collector.example:4318/v1/metrics": 400 Bad Request: body: secret-body`))

	out := logs.String()
	if !strings.Contains(out, `"level":"WARN"`) || !strings.Contains(out, `"reason":"error"`) || !strings.Contains(out, `"failures":1`) {
		t.Fatalf("log = %s, want one warning with reason error and failures 1", out)
	}
	for _, leak := range []string{"collector.example", "4318", "/v1/metrics", "secret-body", "Bad Request"} {
		if strings.Contains(out, leak) {
			t.Fatalf("log = %s, contains %q", out, leak)
		}
	}
}

func TestErrorHandlerLogsAtMostOncePerInterval(t *testing.T) {
	logs := captureDefaultLog(t)
	now := time.Unix(1_800_000_000, 0)
	h := newErrorHandler(5*time.Minute, func() time.Time { return now })

	h.Handle(errors.New("first"))
	h.Handle(errors.New("second"))
	h.Handle(context.DeadlineExceeded)
	now = now.Add(time.Minute)
	h.Handle(errors.New("fourth"))
	if got := strings.Count(logs.String(), "\n"); got != 1 {
		t.Fatalf("lines = %d within the interval, want 1\n%s", got, logs.String())
	}

	now = now.Add(5 * time.Minute)
	h.Handle(context.DeadlineExceeded)
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d after the interval, want 2\n%s", len(lines), logs.String())
	}
	if !strings.Contains(lines[1], `"failures":4`) || !strings.Contains(lines[1], `"reason":"timeout"`) {
		t.Fatalf("second line = %s, want failures 4 since the first line and reason timeout", lines[1])
	}
}

// lockedBuffer is a bytes.Buffer safe for the exporter's background
// goroutine to write while a test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
