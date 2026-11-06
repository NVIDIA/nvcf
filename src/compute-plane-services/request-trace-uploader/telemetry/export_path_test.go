// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import "testing"

func TestExportPathTimingsAreHistogramsInSeconds(t *testing.T) {
	for _, name := range []string{UploadDuration, PrepareDuration, ExportLatency, ConfirmLatency} {
		instrument, ok := Lookup(name)
		if !ok {
			t.Errorf("instrument %q is not in the catalog", name)
			continue
		}
		if instrument.Kind != Histogram || instrument.Unit != "s" {
			t.Errorf("%s is a %v in %q, want a histogram in s", name, instrument.Kind, instrument.Unit)
		}
	}
}

func TestPrepareDurationResolvesSubSecondSegments(t *testing.T) {
	instrument, ok := Lookup(PrepareDuration)
	if !ok {
		t.Fatalf("instrument %q is not in the catalog", PrepareDuration)
	}
	if first := instrument.Buckets[0]; first > 0.01 {
		t.Fatalf("first boundary = %v, want at most 10ms so a small segment is not lumped with a slow one", first)
	}
}

func TestUploadDurationIsLabeledByOutcome(t *testing.T) {
	m := newMemory(t)

	m.Observe(UploadDuration, 0.5, Labels{"status": "success"})
	m.Observe(UploadDuration, 2, Labels{"status": "failure"})
	m.Observe(UploadDuration, 1, nil)
	m.Observe(UploadDuration, 1, Labels{"status": "timeout"})

	for status, want := range map[string]float64{"success": 0.5, "failure": 2} {
		count, sum, ok := m.Histogram(UploadDuration, Labels{"status": status})
		if !ok || count != 1 || sum != want {
			t.Errorf("upload.duration{%s} = count %d sum %v ok %v, want 1, %v, true", status, count, sum, ok, want)
		}
	}
	if m.Dropped() != 2 {
		t.Errorf("Dropped() = %d, want 2 for a missing and an unknown status", m.Dropped())
	}
}

func TestRecordsReceivedIsPresetForEveryRecordTypeAndUnknown(t *testing.T) {
	m := newMemory(t)
	want := []string{"request_end", "request_payload", "tool_start", "tool_end", "tool_error", "unknown"}
	for _, recordType := range want {
		if got, ok := m.Value(RecordsReceived, Labels{"record_type": recordType}); !ok || got != 0 {
			t.Errorf("records_received{record_type=%q} = %v, %v; want 0, true", recordType, got, ok)
		}
	}
	if got := len(m.Series(RecordsReceived)); got != len(want) {
		t.Errorf("records_received has %d series, want %d", got, len(want))
	}
	m.Add(RecordsReceived, 1, Labels{"record_type": "model_swap"})
	if m.Dropped() != 1 {
		t.Errorf("Dropped() = %d, want 1 for a record type outside the set", m.Dropped())
	}
}
