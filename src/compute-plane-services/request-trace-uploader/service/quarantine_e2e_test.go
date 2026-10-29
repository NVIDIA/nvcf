// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend/backendtest"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/service"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/uploadertest"
)

// failedJob scripts a job that the backend reports failed for reason, a
// fault of that one segment, so the segment is quarantined at once.
func failedJob(reason backend.Reason) backendtest.SubmitStep {
	return backendtest.SubmitStep{Statuses: []backendtest.StatusStep{{
		Status: backend.StatusFailure,
		Err:    &backend.Fault{Class: backend.ClassTerminal, Scope: backend.ScopeSegment, Reason: reason},
	}}}
}

func quarantinedBy(metrics *telemetry.Memory, reason string) float64 {
	value, _ := metrics.Value(telemetry.SegmentsQuarantined, telemetry.Labels{"reason": reason})
	return value
}

func TestQuarantinedSegmentsAreCountedByReason(t *testing.T) {
	logs := captureLogs(t)
	metrics, err := telemetry.NewMemory("fake")
	if err != nil {
		t.Fatal(err)
	}
	spool, _ := spoolWith(t,
		// Only bad lines: quarantined while it is prepared.
		[]string{"not json", "still not json"},
		[]string{uploadertest.RequestEnd("a", 1)},
		[]string{uploadertest.RequestEnd("b", 2)},
	)
	fake := backendtest.NewFake()
	// A backend may name its own reasons; one the core does not know is
	// counted as unknown, so the label set stays bounded.
	fake.Script(failedJob(backend.ReasonRejected), failedJob(backend.Reason("schema_mismatch")))
	uploader := newUploader(t, spool, fake, service.Options{Metrics: metrics}, nil)

	for _, reason := range telemetry.QuarantineReasons {
		if _, preset := metrics.Value(telemetry.SegmentsQuarantined, telemetry.Labels{"reason": reason}); !preset {
			t.Errorf("reason %q not preset to zero before any traffic", reason)
		}
	}

	passUntil(t, uploader, func() bool {
		return quarantinedBy(metrics, "bad_records")+quarantinedBy(metrics, "rejected")+quarantinedBy(metrics, "unknown") >= 3
	})
	for range 2 {
		_ = uploader.Pass(context.Background())
	}

	want := map[string]float64{"bad_records": 1, "rejected": 1, "unknown": 1}
	for _, reason := range telemetry.QuarantineReasons {
		if got := quarantinedBy(metrics, reason); got != want[reason] {
			t.Errorf("%s{reason=%q} = %v, want %v", telemetry.SegmentsQuarantined, reason, got, want[reason])
		}
	}
	if _, ok := metrics.Value(telemetry.SegmentsQuarantined, telemetry.Labels{"reason": "schema_mismatch"}); ok {
		t.Error("a reason outside the known set became its own series")
	}
	if got := strings.Count(logs.String(), "quarantined request trace segment"); got != 3 {
		t.Fatalf("quarantine log lines = %d, want one per segment:\n%s", got, logs)
	}
}
