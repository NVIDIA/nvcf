// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/backend/backendtest"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/record"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/service"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/uploadertest"
)

func newMetrics(t *testing.T) *telemetry.Memory {
	t.Helper()
	metrics, err := telemetry.NewMemory("fake")
	if err != nil {
		t.Fatal(err)
	}
	return metrics
}

func samples(metrics *telemetry.Memory, name string, labels telemetry.Labels) (int, float64) {
	count, sum, _ := metrics.Histogram(name, labels)
	return count, sum
}

func TestEachStageOfTheExportPathIsTimed(t *testing.T) {
	spool, _ := spoolWith(t,
		[]string{uploadertest.RequestEnd("a", 1)},
		[]string{uploadertest.RequestEnd("b", 2)},
	)
	fake := backendtest.NewFake()
	fake.SetCapabilities(backend.Capabilities{
		AcceptedFormats: []backend.Format{backend.FormatGzipJSONL},
		Exports:         true,
		MaxBatchFiles:   16,
	})
	metrics := newMetrics(t)
	uploader := newUploader(t, spool, fake, service.Options{Metrics: metrics}, nil)

	if err := uploader.Pass(context.Background()); err != nil {
		t.Fatalf("Pass() error = %v", err)
	}
	if len(fake.Jobs()) != 1 {
		t.Fatalf("jobs = %d, want the two segments in one job", len(fake.Jobs()))
	}

	if count, _ := samples(metrics, telemetry.PrepareDuration, nil); count != 2 {
		t.Errorf("prepare_duration samples = %d, want one per segment", count)
	}
	if count, _ := samples(metrics, telemetry.UploadDuration, telemetry.Labels{"status": "success"}); count != 1 {
		t.Errorf("upload.duration{success} samples = %d, want one per submit", count)
	}
	if count, _ := samples(metrics, telemetry.UploadDuration, telemetry.Labels{"status": "failure"}); count != 0 {
		t.Errorf("upload.duration{failure} samples = %d, want 0", count)
	}
	if count, _ := samples(metrics, telemetry.ConfirmLatency, nil); count != 1 {
		t.Errorf("confirm_latency samples = %d, want one per job", count)
	}
	count, sum := samples(metrics, telemetry.ExportLatency, nil)
	if count != 2 || sum < 0 {
		t.Errorf("export_latency = count %d sum %v, want one non-negative sample per segment", count, sum)
	}

	// A later round finds nothing new to confirm, so nothing is timed twice.
	if err := uploader.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count, _ := samples(metrics, telemetry.ExportLatency, nil); count != 2 {
		t.Errorf("export_latency samples = %d after another poll, want still 2", count)
	}
	if metrics.Dropped() != 0 {
		t.Errorf("Dropped() = %d, want every observation valid", metrics.Dropped())
	}
}

func TestARefusedSubmitIsTimedAsAFailure(t *testing.T) {
	spool, _ := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	fake := backendtest.NewFake()
	fake.Script(backendtest.SubmitStep{Outcome: backendtest.Refuse})
	metrics := newMetrics(t)
	uploader := newUploader(t, spool, fake, service.Options{Metrics: metrics}, nil)

	if err := uploader.Pass(context.Background()); err != nil {
		t.Fatalf("Pass() error = %v", err)
	}

	if count, _ := samples(metrics, telemetry.UploadDuration, telemetry.Labels{"status": "failure"}); count != 1 {
		t.Errorf("upload.duration{failure} samples = %d, want 1", count)
	}
	if count, _ := samples(metrics, telemetry.UploadDuration, telemetry.Labels{"status": "success"}); count != 0 {
		t.Errorf("upload.duration{success} samples = %d, want 0", count)
	}
	if count, _ := samples(metrics, telemetry.ExportLatency, nil); count != 0 {
		t.Errorf("export_latency samples = %d, want none before a confirmation", count)
	}
}

func TestReceivedRecordsAreCountedByRecordType(t *testing.T) {
	newType := strings.Replace(uploadertest.RequestEnd("e", 5), `"request_end"`, `"model_swap"`, 1)
	spool, _ := spoolWith(t,
		[]string{
			uploadertest.RequestEnd("a", 1),
			uploadertest.RequestPayload("b", 2, nil),
			uploadertest.Tool(record.EventToolStart, "s", "c", 3),
			uploadertest.Tool(record.EventToolEnd, "s", "c", 4),
		},
		[]string{
			uploadertest.Tool(record.EventToolError, "s", "d", 5),
			newType,
			uploadertest.LegacyAuditRecord("f"),
			uploadertest.RequestEnd("g", 6),
		},
	)
	metrics := newMetrics(t)
	uploader := newUploader(t, spool, backendtest.NewFake(), service.Options{Metrics: metrics}, nil)

	if err := uploader.Pass(context.Background()); err != nil {
		t.Fatalf("Pass() error = %v", err)
	}

	want := map[string]float64{
		"request_end": 2, "request_payload": 1, "tool_start": 1, "tool_end": 1, "tool_error": 1, "unknown": 2,
	}
	for recordType, n := range want {
		if got, _ := metrics.Value(telemetry.RecordsReceived, telemetry.Labels{"record_type": recordType}); got != n {
			t.Errorf("records_received{record_type=%q} = %v, want %v", recordType, got, n)
		}
	}
	if metrics.Dropped() != 0 {
		t.Errorf("Dropped() = %d, want every record type label in the bounded set", metrics.Dropped())
	}
}
