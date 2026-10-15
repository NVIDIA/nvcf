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

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func sample(name string, value float64) Sample {
	return Sample{Name: name, Labels: map[string]string{}, Value: value}
}

// window builds a 30-second window with the given collector/sink counters at
// start and end.
func window() Window {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return Window{
		Start: Snapshot{
			At: start,
			Collector: Samples{
				sample("otelcol_receiver_accepted_log_records_total", 1000),
				sample("otelcol_receiver_accepted_metric_points_total", 2000),
				sample("otelcol_receiver_refused_log_records_total", 0),
				sample("otelcol_receiver_refused_metric_points_total", 0),
				sample("otelcol_exporter_sent_log_records_total", 900),
				sample("otelcol_exporter_sent_metric_points_total", 1800),
				sample("otelcol_exporter_send_failed_log_records_total", 0),
				sample("otelcol_exporter_send_failed_metric_points_total", 0),
				sample("otelcol_process_cpu_seconds_total", 1.0),
				sample("otelcol_process_memory_rss", 1000),
			},
			Sink: Samples{
				sample("otelcol_receiver_accepted_log_records_total", 500),
				sample("otelcol_receiver_accepted_metric_points_total", 800),
			},
		},
		End: Snapshot{
			At: start.Add(30 * time.Second),
			Collector: Samples{
				sample("otelcol_receiver_accepted_log_records_total", 4000),
				sample("otelcol_receiver_accepted_metric_points_total", 5000),
				sample("otelcol_receiver_refused_log_records_total", 0),
				sample("otelcol_receiver_refused_metric_points_total", 0),
				sample("otelcol_exporter_sent_log_records_total", 3600),
				sample("otelcol_exporter_sent_metric_points_total", 4500),
				sample("otelcol_exporter_send_failed_log_records_total", 10),
				sample("otelcol_exporter_send_failed_metric_points_total", 0),
				sample("otelcol_process_cpu_seconds_total", 4.0),
				sample("otelcol_process_memory_rss", 5242880),
			},
			Sink: Samples{
				sample("otelcol_receiver_accepted_log_records_total", 3200),
				sample("otelcol_receiver_accepted_metric_points_total", 4400),
			},
		},
	}
}

func TestBuildComputesDeltasAndDerived(t *testing.T) {
	r := Build(Inputs{
		Shape:         "container",
		Profile:       "dev",
		LogsPerSec:    100,
		MetricsPerSec: 120,
		Window:        window(),
		Health:        PodHealth{Phase: "Running", Restarts: 0},
	})

	if r.WindowSeconds != 30 {
		t.Fatalf("window seconds = %v, want 30", r.WindowSeconds)
	}
	if r.Logs.CollectorAccepted != 3000 {
		t.Errorf("collector accepted logs = %v, want 3000", r.Logs.CollectorAccepted)
	}
	if r.Logs.GeneratedExpected != 3000 {
		t.Errorf("expected generated logs = %v, want 3000 (100*30)", r.Logs.GeneratedExpected)
	}
	if r.Logs.SinkAccepted != 2700 {
		t.Errorf("sink accepted logs = %v, want 2700", r.Logs.SinkAccepted)
	}
	if r.Logs.ThroughputPerSec != 90 {
		t.Errorf("logs throughput = %v, want 90 (2700/30)", r.Logs.ThroughputPerSec)
	}
	if d := r.Logs.DeliveryRatio; d < 0.899 || d > 0.901 {
		t.Errorf("logs delivery ratio = %v, want ~0.9", d)
	}
	if r.Logs.ExporterFailed != 10 {
		t.Errorf("logs exporter failed = %v, want 10", r.Logs.ExporterFailed)
	}
	if r.Metrics.CollectorAccepted != 3000 {
		t.Errorf("collector accepted metric points = %v, want 3000", r.Metrics.CollectorAccepted)
	}
	if c := r.Resources.CPUCoresAvg; c < 0.099 || c > 0.101 {
		t.Errorf("cpu cores avg = %v, want ~0.1 ((4-1)/30)", c)
	}
	if r.Resources.MemRSSBytes != 5242880 {
		t.Errorf("mem rss = %v, want 5242880", r.Resources.MemRSSBytes)
	}
	if len(r.Notes) != 0 {
		t.Errorf("expected no missing-metric notes, got %v", r.Notes)
	}
}

func TestBuildNotesMissingMetrics(t *testing.T) {
	empty := Window{
		Start: Snapshot{At: time.Unix(0, 0)},
		End:   Snapshot{At: time.Unix(30, 0)},
	}
	r := Build(Inputs{Shape: "helm", Profile: "dev", LogsPerSec: 10, Window: empty})
	if len(r.Notes) == 0 {
		t.Fatal("expected notes about missing metrics when scrapes are empty")
	}
	// Missing metrics must not crash and must read as zero.
	if r.Logs.CollectorAccepted != 0 || r.Logs.ThroughputPerSec != 0 {
		t.Errorf("missing metrics should be zero, got %+v", r.Logs)
	}
}

func TestBuildStatusReflectsCompleteness(t *testing.T) {
	full := Build(Inputs{Shape: "container", Profile: "dev", LogsPerSec: 100, Window: window()})
	if full.Status != StatusOK {
		t.Errorf("complete window status = %q, want %q", full.Status, StatusOK)
	}
	empty := Build(Inputs{Shape: "helm", Profile: "dev", LogsPerSec: 10, Window: Window{
		Start: Snapshot{At: time.Unix(0, 0)},
		End:   Snapshot{At: time.Unix(30, 0)},
	}})
	if empty.Status != StatusPartial {
		t.Errorf("empty window status = %q, want %q", empty.Status, StatusPartial)
	}
}

func TestStartupHealthIsReportedAndSerialized(t *testing.T) {
	podStartedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	collectorStartedAt := podStartedAt.Add(2 * time.Second)
	healthyAt := collectorStartedAt.Add(15 * time.Second)
	startup := NewStartupHealth(podStartedAt, collectorStartedAt, healthyAt)

	r := Build(Inputs{
		Shape:         "container",
		Profile:       "dev",
		LogsPerSec:    100,
		MetricsPerSec: 120,
		Window:        window(),
		Health:        PodHealth{Phase: "Running"},
		StartupHealth: &startup,
	})
	if r.StartupHealth == nil {
		t.Fatal("startup health was not added to the report")
	}
	if r.StartupHealth.PodToHealthSeconds != 17 || r.StartupHealth.CollectorToHealthSeconds != 15 {
		t.Errorf("startup durations = %+v, want pod=17s collector=15s", r.StartupHealth)
	}

	data, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var decoded ShapeReport
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.StartupHealth == nil || !decoded.StartupHealth.HealthyAt.Equal(healthyAt) {
		t.Errorf("serialized startup health = %+v, want healthy at %s", decoded.StartupHealth, healthyAt)
	}

	var buf bytes.Buffer
	r.WriteSummary(&buf)
	if !bytes.Contains(buf.Bytes(), []byte("collector_to_health=15s")) {
		t.Errorf("summary missing startup duration:\n%s", buf.String())
	}
}

// A series present only in the end scrape cannot yield a window delta: it must
// be reported as missing (zero + note + partial status), not as a full
// process-lifetime counter.
func TestBuildEndOnlyCounterIsMissing(t *testing.T) {
	start := time.Unix(0, 0)
	w := Window{
		// Start snapshot lacks the accepted-logs series entirely.
		Start: Snapshot{At: start, Collector: Samples{sample("otelcol_process_cpu_seconds_total", 1.0)}},
		End: Snapshot{At: start.Add(30 * time.Second), Collector: Samples{
			sample("otelcol_receiver_accepted_log_records_total", 4000),
			sample("otelcol_process_cpu_seconds_total", 2.0),
		}},
	}
	r := Build(Inputs{Shape: "container", Profile: "dev", LogsPerSec: 100, Window: w})
	if r.Logs.CollectorAccepted != 0 {
		t.Errorf("end-only accepted logs = %v, want 0 (missing start scrape)", r.Logs.CollectorAccepted)
	}
	if r.Status != StatusPartial {
		t.Errorf("status = %q, want %q for a one-sided scrape", r.Status, StatusPartial)
	}
	var noted bool
	for _, n := range r.Notes {
		if n == "collector accepted logs" {
			noted = true
		}
	}
	if !noted {
		t.Errorf("expected a missing-metric note for the end-only series, got %v", r.Notes)
	}
}

// A report whose pod health could not be observed must not read as a clean
// baseline: it is marked partial with a note so a zero-value health block is
// not mistaken for an observed healthy pod.
func TestBuildHealthErrorMarksPartial(t *testing.T) {
	r := Build(Inputs{
		Shape:      "container",
		Profile:    "dev",
		LogsPerSec: 100,
		Window:     window(),
		HealthErr:  errors.New("boom"),
	})
	if r.Status != StatusPartial {
		t.Errorf("status = %q, want %q when health is unavailable", r.Status, StatusPartial)
	}
	var noted bool
	for _, n := range r.Notes {
		if n == "collector pod health" {
			noted = true
		}
	}
	if !noted {
		t.Errorf("expected a missing-metric note for pod health, got %v", r.Notes)
	}
}

func TestBuildCounterResetGuard(t *testing.T) {
	start := time.Unix(0, 0)
	w := Window{
		Start: Snapshot{At: start, Collector: Samples{sample("otelcol_receiver_accepted_log_records_total", 5000)}},
		// End lower than start (a restart reset the counter): delta must not go
		// negative; the post-reset value is used instead.
		End: Snapshot{At: start.Add(10 * time.Second), Collector: Samples{sample("otelcol_receiver_accepted_log_records_total", 200)}},
	}
	r := Build(Inputs{Shape: "container", Profile: "dev", Window: w})
	if r.Logs.CollectorAccepted != 200 {
		t.Errorf("after counter reset, accepted = %v, want 200", r.Logs.CollectorAccepted)
	}
}

func TestJSONRoundTrips(t *testing.T) {
	r := Build(Inputs{Shape: "container", Profile: "dev", LogsPerSec: 100, Window: window()})
	data, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var back ShapeReport
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Shape != "container" || back.Logs.SinkAccepted != r.Logs.SinkAccepted {
		t.Errorf("round-trip mismatch: %+v", back)
	}
}

func TestMarkInvalidSurfacesInSummary(t *testing.T) {
	r := Build(Inputs{Shape: "container", Profile: "baseline", LogsPerSec: 100, Window: window()})
	r.MarkInvalid("load generators did not complete cleanly: deadline exceeded")
	if r.Status != StatusInvalid {
		t.Fatalf("status = %q, want %q", r.Status, StatusInvalid)
	}
	var buf bytes.Buffer
	r.WriteSummary(&buf)
	out := buf.String()
	if !bytes.Contains([]byte(out), []byte("status="+StatusInvalid)) {
		t.Errorf("summary header missing invalid status:\n%s", out)
	}
	if !bytes.Contains([]byte(out), []byte("INVALID")) {
		t.Errorf("summary missing INVALID reason line:\n%s", out)
	}
}

func TestWriteSummaryContainsSignals(t *testing.T) {
	r := Build(Inputs{Shape: "container", Profile: "dev", LogsPerSec: 100, Window: window()})
	var buf bytes.Buffer
	r.WriteSummary(&buf)
	out := buf.String()
	for _, want := range []string{"container baseline", "logs", "metrics", "throughput", "resources", "health"} {
		if !bytes.Contains([]byte(out), []byte(want)) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
}
