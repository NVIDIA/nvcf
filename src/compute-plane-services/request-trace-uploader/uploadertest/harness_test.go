// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package uploadertest_test

import (
	"context"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend/backendtest"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/record"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/segment"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/uploadertest"
)

// TestHarnessCarriesOneSegmentThroughFakeBackend proves the harness pieces fit
// the module's public interface: a spooled segment is discovered, read, and
// submitted to the fake backend until it reports success.
func TestHarnessCarriesOneSegmentThroughFakeBackend(t *testing.T) {
	spool := uploadertest.NewSpool(t, "request-trace")
	closed := spool.Roll(uploadertest.NewSegment().
		Member(
			uploadertest.Wrap(uploadertest.RequestEnd("req-1", eventTime), 1),
			uploadertest.Wrap(uploadertest.RequestPayload("req-1", eventTime, nil), 2),
		).
		Member(uploadertest.RequestEnd("req-2", eventTime)).
		Bytes())
	spool.Roll(uploadertest.NewSegment().Member(uploadertest.Wrap(uploadertest.RequestEnd("req-3", eventTime), 3)).Bytes())

	segments, err := segment.Discover(spool.Dir, spool.Prefix)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(segments) != 1 || segments[0].Path != closed {
		t.Fatalf("discovered %+v, want only the closed segment %q", segments, closed)
	}

	reader, err := record.Open(closed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var ids []string
	for reader.Next() {
		id, _ := reader.Record().RequestID()
		ids = append(ids, id)
	}
	if err := reader.Err(); err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if want := []string{"req-1", "req-1", "req-2"}; len(ids) != len(want) || ids[0] != want[0] || ids[1] != want[1] || ids[2] != want[2] {
		t.Fatalf("request ids = %v, want %v", ids, want)
	}

	fake := backendtest.NewFake()
	fake.Script(backendtest.SubmitStep{Statuses: []backendtest.StatusStep{
		{Status: backend.StatusPending},
		{Status: backend.StatusSuccess},
	}})
	ctx := context.Background()
	id, err := fake.Submit(ctx, backend.SubmitRequest{Segment: segments[0], Path: segments[0].Path})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	for _, want := range []backend.Status{backend.StatusPending, backend.StatusSuccess} {
		got, err := fake.Status(ctx, id)
		if err != nil || got != want {
			t.Fatalf("status = %q, %v; want %q", got, err, want)
		}
	}
	if jobs := fake.Jobs(); len(jobs) != 1 || !jobs[0].Landed {
		t.Errorf("jobs = %+v, want one landed job", jobs)
	}
}
