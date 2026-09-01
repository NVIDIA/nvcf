// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package debug

import (
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/segment"
)

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
