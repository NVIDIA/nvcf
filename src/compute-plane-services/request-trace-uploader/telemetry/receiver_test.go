// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import "testing"

type recordingBackend struct{ metrics Metrics }

func (r *recordingBackend) UseMetrics(m Metrics) { r.metrics = m }

func TestReceiverGetsTheSharedRecorder(t *testing.T) {
	m, err := NewMemory("fake")
	if err != nil {
		t.Fatal(err)
	}
	var receiver Receiver = &recordingBackend{}
	receiver.UseMetrics(m)
	receiver.(*recordingBackend).metrics.Add(UnknownStatus, 1, nil)
	if got, ok := m.Value(UnknownStatus, nil); !ok || got != 1 {
		t.Fatalf("unknown_status = %v, %v; want 1", got, ok)
	}
}

func TestUnknownStatusIsPresetToZero(t *testing.T) {
	m, err := NewMemory("fake")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := m.Value(UnknownStatus, nil); !ok || got != 0 {
		t.Fatalf("unknown_status = %v, %v; want a zero series", got, ok)
	}
}
