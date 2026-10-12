// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package debug

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/segment"
)

// captureLogs redirects the default slog logger to a buffer for the duration
// of a test, so a test can assert on what was, or was not, logged.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	return &buf
}

func writeSegment(t *testing.T, lines ...string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	for _, line := range lines {
		if _, err := gz.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "request-trace.000000.jsonl.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRegisteredUnderDebugBackend(t *testing.T) {
	client, err := backend.New(config.Config{Backend: config.BackendDebug})
	if err != nil {
		t.Fatalf("New() error = %v, want the debug backend to be registered", err)
	}
	if client == nil {
		t.Fatal("New() returned a nil client")
	}
}

func TestSubmitReadsSegmentAndReportsSuccess(t *testing.T) {
	path := writeSegment(t,
		`{"schema":"dynamo.request.trace.v1","event_type":"request_end","event_time_unix_ms":1,"request":{"request_id":"req-1"}}`,
		`{"schema":"dynamo.request.trace.v1","event_type":"request_payload","event_time_unix_ms":2,"payload":{"request_id":"req-1","payload_complete":true,"http_request_headers":{"nvcf-ncaid":"acme"}}}`,
		`{not valid json`,
	)

	client := &Client{}
	id, err := client.Submit(context.Background(), backend.SubmitRequest{
		Segment: segment.Segment{Index: 7, Path: path},
		Path:    path,
	})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if id == "" {
		t.Fatal("Submit() returned an empty id")
	}

	status, err := client.Status(context.Background(), id)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status != backend.StatusSuccess {
		t.Errorf("Status() = %v, want success", status)
	}
}

func TestSubmitDoesNotDeleteTheSource(t *testing.T) {
	path := writeSegment(t,
		`{"schema":"dynamo.request.trace.v1","event_type":"request_end","event_time_unix_ms":1,"request":{"request_id":"req-1"}}`,
	)

	client := &Client{}
	if _, err := client.Submit(context.Background(), backend.SubmitRequest{
		Segment: segment.Segment{Index: 0, Path: path},
		Path:    path,
	}); err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("source segment was removed: %v", err)
	}
}

func TestSubmitFailsOnAMissingSegment(t *testing.T) {
	client := &Client{}
	_, err := client.Submit(context.Background(), backend.SubmitRequest{
		Segment: segment.Segment{Index: 0, Path: "/nonexistent/request-trace.000000.jsonl.gz"},
		Path:    "/nonexistent/request-trace.000000.jsonl.gz",
	})
	if err == nil {
		t.Fatal("Submit() error = nil, want an open failure")
	}
}

func TestSubmitStopsOnCancellation(t *testing.T) {
	path := writeSegment(t,
		`{"schema":"dynamo.request.trace.v1","event_type":"request_end","event_time_unix_ms":1,"request":{"request_id":"a"}}`,
		`{"schema":"dynamo.request.trace.v1","event_type":"request_end","event_time_unix_ms":2,"request":{"request_id":"b"}}`,
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := &Client{}
	_, err := client.Submit(ctx, backend.SubmitRequest{
		Segment: segment.Segment{Index: 0, Path: path},
		Path:    path,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Submit() error = %v, want context.Canceled", err)
	}
}

func TestSubmitOnAnEmptySegmentSucceeds(t *testing.T) {
	path := writeSegment(t)

	client := &Client{}
	id, err := client.Submit(context.Background(), backend.SubmitRequest{
		Segment: segment.Segment{Index: 3, Path: path},
		Path:    path,
	})
	if err != nil {
		t.Fatalf("Submit() error = %v, want an empty segment to be accepted", err)
	}
	if id == "" {
		t.Fatal("Submit() returned an empty id")
	}
}

func TestNewDefaultsToBasicVerbosity(t *testing.T) {
	client, err := New(config.Config{Backend: config.BackendDebug})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	debugClient, ok := client.(*Client)
	if !ok {
		t.Fatalf("New() returned %T, want *Client", client)
	}
	if debugClient.verbosity == config.DebugVerbosityDetailed {
		t.Error("New() with an unset verbosity behaved as detailed, want basic")
	}
}

// sensitiveFixture carries a request identifier, a session identifier, and a
// header value in a spot each event type actually places one, plus
// non-identifying metrics that detailed verbosity is allowed to log.
func sensitiveFixture(t *testing.T) string {
	t.Helper()
	return writeSegment(t,
		`{"schema":"dynamo.request.trace.v1","event_type":"request_end","event_time_unix_ms":1,"request":{"request_id":"req-secret-id","model":"llama-70b","input_tokens":10,"output_tokens":20}}`,
		`{"schema":"dynamo.request.trace.v1","event_type":"tool_start","event_time_unix_ms":2,"agent_context":{"session_id":"session-secret-id"},"tool":{"tool_call_id":"tool-secret-id","tool_class":"search","status":"ok"}}`,
		`{"schema":"dynamo.request.trace.v1","event_type":"request_payload","event_time_unix_ms":3,"payload":{"request_id":"req-secret-id","payload_complete":true,"http_request_headers":{"nvcf-ncaid":"header-secret-value"}}}`,
	)
}

func TestSubmitBasicVerbosityLogsOnlyTheSummary(t *testing.T) {
	path := sensitiveFixture(t)
	logs := captureLogs(t)

	client := &Client{verbosity: config.DebugVerbosityBasic}
	if _, err := client.Submit(context.Background(), backend.SubmitRequest{
		Segment: segment.Segment{Index: 0, Path: path},
		Path:    path,
	}); err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	output := logs.String()
	if !strings.Contains(output, "debug backend read a segment") {
		t.Error("basic verbosity did not log the segment summary")
	}
	if strings.Contains(output, "debug backend read a record") {
		t.Error("basic verbosity logged a per-record line, want summary only")
	}
}

func TestSubmitDetailedVerbosityLogsPerRecord(t *testing.T) {
	path := sensitiveFixture(t)
	logs := captureLogs(t)

	client := &Client{verbosity: config.DebugVerbosityDetailed}
	if _, err := client.Submit(context.Background(), backend.SubmitRequest{
		Segment: segment.Segment{Index: 0, Path: path},
		Path:    path,
	}); err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	output := logs.String()
	if got := strings.Count(output, "debug backend read a record"); got != 3 {
		t.Errorf("detailed verbosity logged %d per-record lines, want 3", got)
	}
	if !strings.Contains(output, "debug backend read a segment") {
		t.Error("detailed verbosity did not also log the segment summary")
	}
	for _, want := range []string{"model=llama-70b", "input_tokens=10", "output_tokens=20", "tool_class=search", "status=ok"} {
		if !strings.Contains(output, want) {
			t.Errorf("detailed verbosity output missing %q:\n%s", want, output)
		}
	}
}

// TestSubmitDetailedVerbosityNeverLogsIdentifyingValues is the containment
// guarantee: detailed verbosity adds resolution, not exposure. A request
// identifier, a session identifier, and a header value must never appear in
// the log regardless of verbosity.
func TestSubmitDetailedVerbosityNeverLogsIdentifyingValues(t *testing.T) {
	path := sensitiveFixture(t)
	logs := captureLogs(t)

	client := &Client{verbosity: config.DebugVerbosityDetailed}
	if _, err := client.Submit(context.Background(), backend.SubmitRequest{
		Segment: segment.Segment{Index: 0, Path: path},
		Path:    path,
	}); err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	output := logs.String()
	for _, forbidden := range []string{"req-secret-id", "session-secret-id", "tool-secret-id", "header-secret-value"} {
		if strings.Contains(output, forbidden) {
			t.Errorf("detailed verbosity leaked an identifying value %q:\n%s", forbidden, output)
		}
	}
}

// TestSubmitDetailedVerbosityTruncatesOversizedFields guards against an
// unbounded log line: Model has no length validation upstream, so a
// malformed or adversarial record could otherwise put an arbitrarily large
// value there.
func TestSubmitDetailedVerbosityTruncatesOversizedFields(t *testing.T) {
	huge := strings.Repeat("a", 5000)
	path := writeSegment(t,
		`{"schema":"dynamo.request.trace.v1","event_type":"request_end","event_time_unix_ms":1,"request":{"request_id":"req-1","model":"`+huge+`"}}`,
	)
	logs := captureLogs(t)

	client := &Client{verbosity: config.DebugVerbosityDetailed}
	if _, err := client.Submit(context.Background(), backend.SubmitRequest{
		Segment: segment.Segment{Index: 0, Path: path},
		Path:    path,
	}); err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	output := logs.String()
	if strings.Contains(output, huge) {
		t.Error("detailed verbosity logged an oversized field without truncation")
	}
	if !strings.Contains(output, "...(truncated)") {
		t.Error("detailed verbosity did not mark the oversized field as truncated")
	}
}
