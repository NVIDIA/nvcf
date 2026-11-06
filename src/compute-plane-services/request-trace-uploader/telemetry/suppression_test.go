// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import "testing"

func TestSuppressionInstrumentsTakeRuleAndActionLabels(t *testing.T) {
	m, err := NewMemory("fake")
	if err != nil {
		t.Fatal(err)
	}
	m.Add(RecordsSuppressed, 1, Labels{"rule": "nca_id", "action": "strip_payload"})
	m.Add(SuppressionHeaderPresent, 1, Labels{"rule": "nca_id"})
	m.Add(RecordsEvaluated, 1, Labels{"record_type": "request_payload"})
	m.Add(RecordsIncomplete, 1, Labels{"record_type": "request_payload"})
	if m.Dropped() != 0 {
		t.Fatalf("Dropped() = %d, want every observation accepted", m.Dropped())
	}

	// An action outside the two the rules support is refused, so a typo
	// cannot open a new series.
	m.Add(RecordsSuppressed, 1, Labels{"rule": "nca_id", "action": "stripPayload"})
	if m.Dropped() != 1 {
		t.Fatalf("Dropped() = %d, want the unknown action refused", m.Dropped())
	}
}

func TestPayloadCountersArePresetToZero(t *testing.T) {
	m, err := NewMemory("fake")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{RecordsEvaluated, RecordsIncomplete} {
		if value, ok := m.Value(name, Labels{"record_type": "request_payload"}); !ok || value != 0 {
			t.Errorf("%s = %v, %v; want a zero series before any record", name, value, ok)
		}
	}
}
