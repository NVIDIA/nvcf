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

// Package report turns collector and sink metric scrapes taken across a
// measurement window into a performance baseline: per-signal throughput, drops,
// end-to-end delivery, collector resource usage, and pod health. It emits both a
// human-readable summary and structured JSON. There are no pass/fail thresholds
// yet; the goal is a reproducible baseline.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Candidate metric names in priority order. Suffixes (notably "_total") and
// exact spellings vary across collector-contrib versions, so each concept lists
// the plausible names and the parser sums whichever matches.
var (
	acceptedLogs    = []string{"otelcol_receiver_accepted_log_records_total", "otelcol_receiver_accepted_log_records"}
	acceptedMetrics = []string{"otelcol_receiver_accepted_metric_points_total", "otelcol_receiver_accepted_metric_points"}
	refusedLogs     = []string{"otelcol_receiver_refused_log_records_total", "otelcol_receiver_refused_log_records"}
	refusedMetrics  = []string{"otelcol_receiver_refused_metric_points_total", "otelcol_receiver_refused_metric_points"}
	sentLogs        = []string{"otelcol_exporter_sent_log_records_total", "otelcol_exporter_sent_log_records"}
	sentMetrics     = []string{"otelcol_exporter_sent_metric_points_total", "otelcol_exporter_sent_metric_points"}
	failedLogs      = []string{"otelcol_exporter_send_failed_log_records_total", "otelcol_exporter_send_failed_log_records"}
	failedMetrics   = []string{"otelcol_exporter_send_failed_metric_points_total", "otelcol_exporter_send_failed_metric_points"}
	queueSize       = []string{"otelcol_exporter_queue_size"}
	queueCapacity   = []string{"otelcol_exporter_queue_capacity"}
	cpuSeconds      = []string{"otelcol_process_cpu_seconds_total", "otelcol_process_cpu_seconds"}
	memRSS          = []string{"otelcol_process_memory_rss_bytes", "otelcol_process_memory_rss"}
)

// PodHealth is the collector pod's restart/OOM state at the end of the run.
type PodHealth struct {
	Phase     string `json:"phase"`
	Restarts  int32  `json:"restarts"`
	OOMKilled bool   `json:"oom_killed"`
}

// Snapshot is a set of metric scrapes taken at one instant.
type Snapshot struct {
	At        time.Time
	Collector Samples
	Sink      Samples
}

// Window is the start and end snapshots bracketing the measurement window.
type Window struct {
	Start Snapshot
	End   Snapshot
}

// Seconds is the wall-clock duration of the window.
func (w Window) Seconds() float64 {
	d := w.End.At.Sub(w.Start.At).Seconds()
	if d <= 0 {
		return 0
	}
	return d
}

// SignalStat is the per-signal (logs or metrics) baseline over the window.
type SignalStat struct {
	GeneratedExpected float64 `json:"generated_expected"`
	CollectorAccepted float64 `json:"collector_accepted"`
	CollectorRefused  float64 `json:"collector_refused"`
	ExporterSent      float64 `json:"exporter_sent"`
	ExporterFailed    float64 `json:"exporter_failed"`
	SinkAccepted      float64 `json:"sink_accepted"`
	ThroughputPerSec  float64 `json:"throughput_per_sec"`
	DeliveryRatio     float64 `json:"delivery_ratio"`
}

// ResourceStat is the collector's resource usage over the window.
type ResourceStat struct {
	CPUCoresAvg float64 `json:"cpu_cores_avg"`
	MemRSSBytes float64 `json:"mem_rss_bytes"`
}

// ShapeReport is the full baseline for one workload shape.
type ShapeReport struct {
	Shape         string       `json:"shape"`
	Profile       string       `json:"profile"`
	WindowSeconds float64      `json:"window_seconds"`
	Logs          SignalStat   `json:"logs"`
	Metrics       SignalStat   `json:"metrics"`
	Resources     ResourceStat `json:"resources"`
	Health        PodHealth    `json:"health"`
	Notes         []string     `json:"notes,omitempty"`
}

// Inputs are the raw materials for a report.
type Inputs struct {
	Shape         string
	Profile       string
	LogsPerSec    int
	MetricsPerSec int
	Window        Window
	Health        PodHealth
}

// Build computes the baseline from the window snapshots. It never fails on
// missing metrics: absent series are recorded as zero and noted, so a partial
// scrape still produces a usable report.
func Build(in Inputs) ShapeReport {
	r := ShapeReport{
		Shape:         in.Shape,
		Profile:       in.Profile,
		WindowSeconds: in.Window.Seconds(),
		Health:        in.Health,
	}
	win := r.WindowSeconds

	var missing []string
	note := func(concept string, ok bool) {
		if !ok {
			missing = append(missing, concept)
		}
	}

	start, end := in.Window.Start, in.Window.End

	// Logs.
	r.Logs.GeneratedExpected = float64(in.LogsPerSec) * win
	var ok bool
	r.Logs.CollectorAccepted, ok = counterDelta(start.Collector, end.Collector, acceptedLogs...)
	note("collector accepted logs", ok)
	r.Logs.CollectorRefused, _ = counterDelta(start.Collector, end.Collector, refusedLogs...)
	r.Logs.ExporterSent, _ = counterDelta(start.Collector, end.Collector, sentLogs...)
	r.Logs.ExporterFailed, _ = counterDelta(start.Collector, end.Collector, failedLogs...)
	r.Logs.SinkAccepted, ok = counterDelta(start.Sink, end.Sink, acceptedLogs...)
	note("sink accepted logs", ok)

	// Metrics.
	r.Metrics.GeneratedExpected = float64(in.MetricsPerSec) * win
	r.Metrics.CollectorAccepted, ok = counterDelta(start.Collector, end.Collector, acceptedMetrics...)
	note("collector accepted metric points", ok)
	r.Metrics.CollectorRefused, _ = counterDelta(start.Collector, end.Collector, refusedMetrics...)
	r.Metrics.ExporterSent, _ = counterDelta(start.Collector, end.Collector, sentMetrics...)
	r.Metrics.ExporterFailed, _ = counterDelta(start.Collector, end.Collector, failedMetrics...)
	r.Metrics.SinkAccepted, ok = counterDelta(start.Sink, end.Sink, acceptedMetrics...)
	note("sink accepted metric points", ok)

	finishSignal(&r.Logs, win)
	finishSignal(&r.Metrics, win)

	// Resources: CPU as average cores over the window, memory as the end RSS.
	if cpu, ok := counterDelta(start.Collector, end.Collector, cpuSeconds...); ok && win > 0 {
		r.Resources.CPUCoresAvg = cpu / win
	} else {
		note("collector process cpu", ok)
	}
	if mem, ok := end.Collector.Latest(memRSS...); ok {
		r.Resources.MemRSSBytes = mem
	} else {
		note("collector process memory", ok)
	}

	r.Notes = missing
	return r
}

// finishSignal computes the derived throughput and delivery-ratio fields.
func finishSignal(s *SignalStat, window float64) {
	if window > 0 {
		s.ThroughputPerSec = s.SinkAccepted / window
	}
	if s.CollectorAccepted > 0 {
		s.DeliveryRatio = s.SinkAccepted / s.CollectorAccepted
	}
}

// counterDelta returns end-minus-start for a monotonic counter, guarding
// against a negative result from a counter reset (restart).
func counterDelta(start, end Samples, candidates ...string) (float64, bool) {
	e, ok := end.Sum(candidates...)
	if !ok {
		return 0, false
	}
	s, _ := start.Sum(candidates...)
	d := e - s
	if d < 0 {
		d = e
	}
	return d, true
}

// JSON returns the indented JSON encoding of the report.
func (r ShapeReport) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// WriteSummary prints a human-readable summary of the report.
func (r ShapeReport) WriteSummary(w io.Writer) {
	fmt.Fprintf(w, "=== %s baseline (profile=%s, window=%.0fs) ===\n", r.Shape, r.Profile, r.WindowSeconds)
	writeSignal(w, "logs", r.Logs)
	writeSignal(w, "metrics", r.Metrics)
	fmt.Fprintf(w, "  resources     : cpu=%.3f cores (avg)  mem_rss=%s\n", r.Resources.CPUCoresAvg, humanBytes(r.Resources.MemRSSBytes))
	fmt.Fprintf(w, "  health        : phase=%s restarts=%d oom_killed=%t\n", r.Health.Phase, r.Health.Restarts, r.Health.OOMKilled)
	if len(r.Notes) > 0 {
		fmt.Fprintf(w, "  notes         : missing metrics: ")
		for i, n := range r.Notes {
			if i > 0 {
				fmt.Fprintf(w, ", ")
			}
			fmt.Fprintf(w, "%s", n)
		}
		fmt.Fprintln(w)
	}
}

func writeSignal(w io.Writer, name string, s SignalStat) {
	fmt.Fprintf(w, "  %-8s      : generated~%.0f  accepted=%.0f refused=%.0f  sent=%.0f failed=%.0f  sink=%.0f\n",
		name, s.GeneratedExpected, s.CollectorAccepted, s.CollectorRefused, s.ExporterSent, s.ExporterFailed, s.SinkAccepted)
	fmt.Fprintf(w, "                  throughput=%.0f/s delivery=%.1f%%\n", s.ThroughputPerSec, s.DeliveryRatio*100)
}

func humanBytes(b float64) string {
	const unit = 1024.0
	if b < unit {
		return fmt.Sprintf("%.0fB", b)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	val := b / unit
	i := 0
	for val >= unit && i < len(units)-1 {
		val /= unit
		i++
	}
	return fmt.Sprintf("%.1f%s", val, units[i])
}
