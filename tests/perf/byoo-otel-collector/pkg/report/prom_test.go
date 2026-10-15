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

package report

import "testing"

const scrape = `# HELP otelcol_receiver_accepted_log_records_total Number of log records accepted.
# TYPE otelcol_receiver_accepted_log_records_total counter
otelcol_receiver_accepted_log_records_total{receiver="otlp",transport="grpc"} 1200 1700000000000
otelcol_receiver_accepted_log_records_total{receiver="otlp",transport="http"} 300
otelcol_process_memory_rss{} 5.24288e+06
otelcol_process_cpu_seconds_total 2.5
garbage line without value
otelcol_exporter_queue_size{exporter="otlp_http/x"} 7
`

func TestParseSkipsCommentsAndGarbage(t *testing.T) {
	s := Parse(scrape)
	// 5 valid samples: 2 accepted logs, memory, cpu, queue size.
	if len(s) != 5 {
		t.Fatalf("expected 5 samples, got %d: %+v", len(s), s)
	}
}

func TestParseLabelsAndTimestamp(t *testing.T) {
	s := Parse(scrape)
	var found bool
	for _, smp := range s {
		if smp.Name == "otelcol_receiver_accepted_log_records_total" && smp.Labels["transport"] == "grpc" {
			found = true
			if smp.Value != 1200 {
				t.Errorf("value = %v, want 1200 (timestamp must be ignored)", smp.Value)
			}
		}
	}
	if !found {
		t.Error("did not parse the grpc-labelled accepted-logs sample")
	}
}

func TestSumAcrossLabelSets(t *testing.T) {
	s := Parse(scrape)
	v, ok := s.Sum("otelcol_receiver_accepted_log_records_total")
	if !ok {
		t.Fatal("expected accepted logs to be found")
	}
	if v != 1500 {
		t.Errorf("sum = %v, want 1500 (1200+300)", v)
	}
}

func TestSumUsesFirstMatchingCandidate(t *testing.T) {
	s := Parse(scrape)
	// The first candidate is absent; the second matches and must be used
	// without falling through to sum both.
	v, ok := s.Sum("otelcol_receiver_accepted_log_records", "otelcol_receiver_accepted_log_records_total")
	if !ok || v != 1500 {
		t.Errorf("Sum with fallback = %v (ok=%t), want 1500", v, ok)
	}
}

func TestSumMissing(t *testing.T) {
	s := Parse(scrape)
	if _, ok := s.Sum("does_not_exist"); ok {
		t.Error("expected missing metric to report not found")
	}
}

func TestLatestGauge(t *testing.T) {
	s := Parse(scrape)
	v, ok := s.Latest("otelcol_process_memory_rss")
	if !ok || v != 5.24288e+06 {
		t.Errorf("Latest memory = %v (ok=%t), want 5242880", v, ok)
	}
}
