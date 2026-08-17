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

// Run status values. A consumer must not treat "invalid" (and should treat
// "partial" with caution) as a clean baseline.
const (
	StatusOK      = "ok"
	StatusPartial = "partial"
	StatusInvalid = "invalid"
)

// ShapeReport is the full baseline for one workload shape.
type ShapeReport struct {
	Shape         string       `json:"shape"`
	Profile       string       `json:"profile"`
	Run           int          `json:"run,omitempty"`
	Repetitions   int          `json:"repetitions,omitempty"`
	Status        string       `json:"status"`
	FailureReason string       `json:"failure_reason,omitempty"`
	WindowSeconds float64      `json:"window_seconds"`
	Logs          SignalStat   `json:"logs"`
	Metrics       SignalStat   `json:"metrics"`
	Resources     ResourceStat `json:"resources"`
	Health        PodHealth    `json:"health"`
	Notes         []string     `json:"notes,omitempty"`
}

// MarkInvalid flags the report as an invalid measurement with a reason, so
// neither the summary nor the JSON can be mistaken for a clean baseline.
func (r *ShapeReport) MarkInvalid(reason string) {
	r.Status = StatusInvalid
	r.FailureReason = reason
}

// Inputs are the raw materials for a report.
type Inputs struct {
	Shape         string
	Profile       string
	LogsPerSec    int
	MetricsPerSec int
	Window        Window
	Health        PodHealth
	// HealthErr, when non-nil, means pod health could not be observed. The
	// zero-value Health is then recorded as missing (note + partial) so a
	// report cannot read as healthy when health was never collected.
	HealthErr error
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

	// Logs. Every reported counter is noted when absent, so a missing series is
	// never indistinguishable from an observed zero.
	r.Logs.GeneratedExpected = float64(in.LogsPerSec) * win
	var ok bool
	r.Logs.CollectorAccepted, ok = counterDelta(start.Collector, end.Collector, acceptedLogs...)
	note("collector accepted logs", ok)
	r.Logs.CollectorRefused, ok = counterDelta(start.Collector, end.Collector, refusedLogs...)
	note("collector refused logs", ok)
	r.Logs.ExporterSent, ok = counterDelta(start.Collector, end.Collector, sentLogs...)
	note("exporter sent logs", ok)
	r.Logs.ExporterFailed, ok = counterDelta(start.Collector, end.Collector, failedLogs...)
	note("exporter failed logs", ok)
	r.Logs.SinkAccepted, ok = counterDelta(start.Sink, end.Sink, acceptedLogs...)
	note("sink accepted logs", ok)

	// Metrics.
	r.Metrics.GeneratedExpected = float64(in.MetricsPerSec) * win
	r.Metrics.CollectorAccepted, ok = counterDelta(start.Collector, end.Collector, acceptedMetrics...)
	note("collector accepted metric points", ok)
	r.Metrics.CollectorRefused, ok = counterDelta(start.Collector, end.Collector, refusedMetrics...)
	note("collector refused metric points", ok)
	r.Metrics.ExporterSent, ok = counterDelta(start.Collector, end.Collector, sentMetrics...)
	note("exporter sent metric points", ok)
	r.Metrics.ExporterFailed, ok = counterDelta(start.Collector, end.Collector, failedMetrics...)
	note("exporter failed metric points", ok)
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

	note("collector pod health", in.HealthErr == nil)

	r.Notes = missing
	// A run with missing series is a partial baseline, not a clean one. The
	// caller may still override this to invalid on load-generation failure.
	if len(missing) > 0 {
		r.Status = StatusPartial
	} else {
		r.Status = StatusOK
	}
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
// against a negative result from a counter reset (restart). It requires the
// series in BOTH snapshots: a one-sided sample (e.g. the start scrape failed)
// cannot yield a valid window delta, so it is reported as missing rather than
// treated as a full process-lifetime counter.
func counterDelta(start, end Samples, candidates ...string) (float64, bool) {
	e, ok := end.Sum(candidates...)
	if !ok {
		return 0, false
	}
	s, ok := start.Sum(candidates...)
	if !ok {
		return 0, false
	}
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
	run := ""
	if r.Repetitions > 1 {
		run = fmt.Sprintf(", run %d/%d", r.Run, r.Repetitions)
	}
	fmt.Fprintf(w, "=== %s baseline (profile=%s%s, window=%.0fs, status=%s) ===\n", r.Shape, r.Profile, run, r.WindowSeconds, r.Status)
	if r.Status == StatusInvalid && r.FailureReason != "" {
		fmt.Fprintf(w, "  status        : INVALID (%s)\n", r.FailureReason)
	}
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
