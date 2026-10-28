// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
)

func TestCatalogNamesAreUniqueAndLabelsBounded(t *testing.T) {
	seen := map[string]bool{}
	for _, instrument := range Instruments() {
		if instrument.Name == "" {
			t.Fatal("instrument with an empty name")
		}
		if seen[instrument.Name] {
			t.Fatalf("instrument %q defined twice", instrument.Name)
		}
		seen[instrument.Name] = true
		if instrument.Kind != Counter && instrument.Kind != Gauge && instrument.Kind != Histogram {
			t.Fatalf("instrument %q has kind %v", instrument.Name, instrument.Kind)
		}
		for _, key := range instrument.Labels {
			if !allowedLabelKeys[key] {
				t.Fatalf("instrument %q uses label %q outside the bounded set", instrument.Name, key)
			}
			if key == BackendLabel {
				t.Fatalf("instrument %q lists %q; every series carries it already", instrument.Name, BackendLabel)
			}
		}
		for key := range instrument.Values {
			if !contains(instrument.Labels, key) {
				t.Fatalf("instrument %q enumerates values for unlisted label %q", instrument.Name, key)
			}
		}
	}
}

func TestCatalogNamesAreBackendNeutral(t *testing.T) {
	for _, instrument := range Instruments() {
		backends := []string{"s3"}
		for _, backend := range []config.Backend{config.BackendKratos, config.BackendObjectStore, config.BackendDebug} {
			backends = append(backends, string(backend))
		}
		for _, backend := range backends {
			if strings.Contains(instrument.Name, backend) {
				t.Fatalf("instrument %q names a backend; use the %q label instead", instrument.Name, BackendLabel)
			}
		}
	}
}

func TestCatalogCoversRequiredInstruments(t *testing.T) {
	required := []string{
		Uploads, UploadDuration, SourceBytes, TransportBytes, ConfirmLatency,
		RecordsDropped, Deletions, PendingSegments, QuarantinedSegments,
		QuarantineEvicted, LastSuccessTimestamp, Uptime,
		RecordsReceived, RecordsUnreadable, SubmitsAmbiguous, AuthFailures,
		JobsStranded, SegmentsEvicted, QuarantineTripped,
		SpoolUsedBytes, SpoolFreeBytes, PendingBytes, OldestPendingAge,
		BatchSegments, BatchBytes, BatchSplits,
		LeftoversRemoved, LeftoversKept, SegmentsMissed,
	}
	for _, name := range required {
		if _, ok := Lookup(name); !ok {
			t.Errorf("instrument %q is not in the catalog", name)
		}
	}
}

func TestNewMemoryPresetsCountersToZero(t *testing.T) {
	m := newMemory(t)
	for _, instrument := range Instruments() {
		if instrument.Kind != Counter {
			continue
		}
		for _, labels := range presetLabels(instrument) {
			got, ok := m.Value(instrument.Name, labels)
			if !ok || got != 0 {
				t.Errorf("counter %q %v = %v, %v; want 0, true", instrument.Name, labels, got, ok)
			}
		}
	}
	for _, stage := range []string{"token", "api"} {
		if _, ok := m.Value(AuthFailures, Labels{"stage": stage}); !ok {
			t.Errorf("auth_failures{stage=%q} not preset", stage)
		}
	}
	if _, ok := m.Value(SubmitsAmbiguous, nil); !ok {
		t.Error("submits_ambiguous not preset")
	}
}

func TestNewMemoryRejectsInvalidBackend(t *testing.T) {
	for _, backend := range []string{"", "Some Backend", "/tmp/x", strings.Repeat("a", 65)} {
		if _, err := NewMemory(backend); err == nil {
			t.Errorf("NewMemory(%q) error = nil, want error", backend)
		}
	}
}

func TestMemoryRecordsEachKind(t *testing.T) {
	m := newMemory(t)

	m.Add(Uploads, 2, Labels{"status": "success"})
	m.Add(Uploads, 1, Labels{"status": "success"})
	m.Set(PendingSegments, 7, nil)
	m.Set(PendingSegments, 4, nil)
	m.Observe(UploadDuration, 1.5, nil)
	m.Observe(UploadDuration, 0.5, nil)

	if got, _ := m.Value(Uploads, Labels{"status": "success"}); got != 3 {
		t.Errorf("uploads{success} = %v, want 3", got)
	}
	if got, _ := m.Value(PendingSegments, nil); got != 4 {
		t.Errorf("pending_segments = %v, want 4", got)
	}
	count, sum, ok := m.Histogram(UploadDuration, nil)
	if !ok || count != 2 || sum != 2 {
		t.Errorf("upload.duration = count %d sum %v ok %v, want 2, 2, true", count, sum, ok)
	}
	if m.Dropped() != 0 {
		t.Errorf("Dropped() = %d, want 0", m.Dropped())
	}
}

func TestEverySeriesCarriesTheFixedBackendLabel(t *testing.T) {
	m := newMemory(t)
	m.Add(SubmitsAmbiguous, 1, nil)

	for _, series := range m.Series(SubmitsAmbiguous) {
		if series[BackendLabel] != "fake" {
			t.Fatalf("series %v, want %s=fake", series, BackendLabel)
		}
	}
	m.Add(SubmitsAmbiguous, 1, Labels{BackendLabel: "other"})
	if got, _ := m.Value(SubmitsAmbiguous, nil); got != 1 {
		t.Fatalf("submits_ambiguous = %v after a caller set %s, want 1", got, BackendLabel)
	}
	if m.Dropped() != 1 {
		t.Fatalf("Dropped() = %d, want 1 for a caller-set %s label", m.Dropped(), BackendLabel)
	}
}

func TestInvalidObservationsAreDroppedNotRecorded(t *testing.T) {
	tests := []struct {
		name   string
		record func(*Memory)
	}{
		{"unknown instrument", func(m *Memory) { m.Add("made_up", 1, nil) }},
		{"set on a counter", func(m *Memory) { m.Set(Uploads, 1, Labels{"status": "success"}) }},
		{"add on a gauge", func(m *Memory) { m.Add(PendingSegments, 1, nil) }},
		{"observe on a counter", func(m *Memory) { m.Observe(SubmitsAmbiguous, 1, nil) }},
		{"unlisted label key", func(m *Memory) { m.Add(SubmitsAmbiguous, 1, Labels{"request_id": "abc"}) }},
		{"missing label", func(m *Memory) { m.Add(Uploads, 1, nil) }},
		{"value outside enumeration", func(m *Memory) { m.Add(Uploads, 1, Labels{"status": "maybe"}) }},
		{"path as value", func(m *Memory) {
			m.Add(RecordsUnreadable, 1, Labels{"reason": "/var/spool/request-trace.000001.jsonl.gz"})
		}},
		{"error text as value", func(m *Memory) {
			m.Add(RecordsUnreadable, 1, Labels{"reason": "unexpected EOF reading member"})
		}},
		{"negative counter delta", func(m *Memory) { m.Add(SubmitsAmbiguous, -1, nil) }},
		{"NaN", func(m *Memory) { m.Set(PendingSegments, math.NaN(), nil) }},
		{"infinity", func(m *Memory) { m.Observe(UploadDuration, math.Inf(1), nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMemory(t)
			before := snapshot(m)
			tt.record(m)
			if m.Dropped() != 1 {
				t.Fatalf("Dropped() = %d, want 1", m.Dropped())
			}
			if after := snapshot(m); after != before {
				t.Fatalf("recorded state changed:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}

func TestSeriesPerInstrumentAreCapped(t *testing.T) {
	m := newMemory(t)
	for i := 0; i < MaxSeriesPerInstrument+5; i++ {
		m.Add(RecordsUnreadable, 1, Labels{"reason": fmt.Sprintf("reason_%03d", i)})
	}
	if got := len(m.Series(RecordsUnreadable)); got != MaxSeriesPerInstrument {
		t.Fatalf("series = %d, want the cap %d", got, MaxSeriesPerInstrument)
	}
	if m.Dropped() != 5 {
		t.Fatalf("Dropped() = %d, want 5 observations over the cap", m.Dropped())
	}
	m.Add(RecordsUnreadable, 1, Labels{"reason": "reason_000"})
	if got, _ := m.Value(RecordsUnreadable, Labels{"reason": "reason_000"}); got != 2 {
		t.Fatalf("existing series = %v after the cap, want it still counting to 2", got)
	}
}

func newMemory(t *testing.T) *Memory {
	t.Helper()
	m, err := NewMemory("fake")
	if err != nil {
		t.Fatalf("NewMemory() error = %v", err)
	}
	return m
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func snapshot(m *Memory) string {
	lines := []string{}
	for _, instrument := range Instruments() {
		for _, series := range m.Series(instrument.Name) {
			labels := withoutBackend(series)
			value, _ := m.Value(instrument.Name, labels)
			count, sum, _ := m.Histogram(instrument.Name, labels)
			lines = append(lines, fmt.Sprintf("%s%v=%v,%d,%v", instrument.Name, series, value, count, sum))
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, ";")
}

func withoutBackend(labels Labels) Labels {
	out := Labels{}
	for k, v := range labels {
		if k != BackendLabel {
			out[k] = v
		}
	}
	return out
}
