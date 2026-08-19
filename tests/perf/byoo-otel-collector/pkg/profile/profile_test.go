/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package profile

import (
	"testing"
	"time"
)

func TestDevDefaults(t *testing.T) {
	p := Dev()
	if p.Name != "dev" {
		t.Errorf("Name = %q, want dev", p.Name)
	}
	if p.Warmup != 10*time.Second {
		t.Errorf("Warmup = %s, want 10s", p.Warmup)
	}
	if p.MeasurementWindow != 30*time.Second {
		t.Errorf("MeasurementWindow = %s, want 30s", p.MeasurementWindow)
	}
	if p.Repetitions != 1 {
		t.Errorf("Repetitions = %d, want 1", p.Repetitions)
	}
	if p.LogRecordsPerSec != 1000 || p.MetricDataPointsPerSec != 1000 {
		t.Errorf("rates = (%d,%d), want (1000,1000)", p.LogRecordsPerSec, p.MetricDataPointsPerSec)
	}
}

func TestBaselineDefaults(t *testing.T) {
	p := Baseline()
	if p.Name != "baseline" {
		t.Errorf("Name = %q, want baseline", p.Name)
	}
	if p.Warmup != 60*time.Second {
		t.Errorf("Warmup = %s, want 60s", p.Warmup)
	}
	if p.MeasurementWindow != 5*time.Minute {
		t.Errorf("MeasurementWindow = %s, want 5m", p.MeasurementWindow)
	}
	if p.Repetitions != 3 {
		t.Errorf("Repetitions = %d, want 3", p.Repetitions)
	}
	if p.LogRecordsPerSec != 10000 || p.MetricDataPointsPerSec != 10000 {
		t.Errorf("rates = (%d,%d), want (10000,10000)", p.LogRecordsPerSec, p.MetricDataPointsPerSec)
	}
}

func TestNemotronDefaults(t *testing.T) {
	p := Nemotron()
	if p.Name != "nemotron" {
		t.Errorf("Name = %q, want nemotron", p.Name)
	}
	// Rate is the modest 40 logs/s the profile targets.
	if p.LogRecordsPerSec != 40 {
		t.Errorf("LogRecordsPerSec = %d, want 40", p.LogRecordsPerSec)
	}
	// The large-record shape lives in a payload attribute over the 256 KB chunk
	// threshold, on a minority of records.
	if p.LogPayloadBytes <= 256*1024 {
		t.Errorf("LogPayloadBytes = %d, want > 256KiB to exceed the chunk threshold", p.LogPayloadBytes)
	}
	if p.LargeRecordFraction <= 0 || p.LargeRecordFraction >= 1 {
		t.Errorf("LargeRecordFraction = %v, want a bimodal fraction in (0,1)", p.LargeRecordFraction)
	}
}

func TestLookup(t *testing.T) {
	tests := []struct {
		name     string
		wantName string
		wantErr  bool
	}{
		{"dev", "dev", false},
		{"baseline", "baseline", false},
		{"nemotron", "nemotron", false},
		{"unknown", "", true},
	}
	for _, tt := range tests {
		got, err := Lookup(tt.name)
		if tt.wantErr {
			if err == nil {
				t.Errorf("Lookup(%q): expected error, got nil", tt.name)
			}
			if got != (Profile{}) {
				t.Errorf("Lookup(%q): expected zero Profile on error, got %+v", tt.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Lookup(%q): unexpected error: %v", tt.name, err)
			continue
		}
		if got.Name != tt.wantName {
			t.Errorf("Lookup(%q).Name = %q, want %q", tt.name, got.Name, tt.wantName)
		}
	}
}
