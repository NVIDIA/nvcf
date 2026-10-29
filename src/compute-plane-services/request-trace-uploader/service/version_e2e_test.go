// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend/backendtest"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/service"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/uploadertest"
)

// Records from a Dynamo release older than v1.4.0 carry no event type, so
// they are labeled unknown, as every type outside v1.4.0's set is.
var unsupportedVersion = telemetry.Labels{"record_type": "unknown", "reason": "unsupported_version"}

func TestOlderDynamoRecordsAreCountedAsDropped(t *testing.T) {
	spool, _ := spoolWith(t,
		[]string{uploadertest.LegacyAuditRecord("a"), uploadertest.RequestEnd("b", 1), uploadertest.LegacyAuditRecord("c")},
		[]string{uploadertest.LegacyAuditRecord("d")},
	)
	fake := backendtest.NewFake()
	exporter := newCapture(t, fake)
	metrics, err := telemetry.NewMemory("fake")
	if err != nil {
		t.Fatal(err)
	}
	uploader := newUploader(t, spool, exporter, service.Options{Metrics: metrics}, nil)

	if err := uploader.Pass(context.Background()); err != nil {
		t.Fatalf("Pass() error = %v", err)
	}

	if got, _ := metrics.Value(telemetry.RecordsDropped, unsupportedVersion); got != 3 {
		t.Errorf("records_dropped%v = %v, want 3", unsupportedVersion, got)
	}
	if got, _ := metrics.Value(telemetry.RecordsUnreadable, telemetry.Labels{"reason": "unparseable"}); got != 0 {
		t.Errorf("records_unreadable{unparseable} = %v, want 0: older records are dropped, not unreadable", got)
	}
	if got := len(exporter.exported()); got != 1 {
		t.Errorf("exported %d records, want the one v1.4.0 record", got)
	}
}

func TestUnsupportedVersionDropsArePresetToZero(t *testing.T) {
	spool, _ := spoolWith(t)
	metrics, err := telemetry.NewMemory("fake")
	if err != nil {
		t.Fatal(err)
	}
	newUploader(t, spool, backendtest.NewFake(), service.Options{Metrics: metrics}, nil)

	if got, ok := metrics.Value(telemetry.RecordsDropped, unsupportedVersion); !ok || got != 0 {
		t.Fatalf("records_dropped%v = %v, %v; want 0 before any traffic", unsupportedVersion, got, ok)
	}
}
