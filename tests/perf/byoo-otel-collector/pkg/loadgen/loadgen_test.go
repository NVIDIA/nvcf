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

package loadgen

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"

	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/labels"
)

func findBySuffix(jobs []*batchv1.Job, suffix string) *batchv1.Job {
	for _, j := range jobs {
		if strings.HasSuffix(j.Name, suffix) {
			return j
		}
	}
	return nil
}

func jobNames(jobs []*batchv1.Job) []string {
	var out []string
	for _, j := range jobs {
		out = append(out, j.Name)
	}
	return out
}

func baseOpts() Options {
	return Options{
		Endpoint:      "collector.byoo-perf.svc.cluster.local:14357",
		Insecure:      true,
		Duration:      30 * time.Second,
		LogsPerSec:    1000,
		MetricsPerSec: 2000,
	}
}

func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func hasArg(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func TestJobArgsAndMetadata(t *testing.T) {
	job := Job("byoo-perf", "perf-collector", SignalLogs, 1500, baseOpts())

	if job.Name != "perf-collector-loadgen-logs" {
		t.Errorf("name = %q", job.Name)
	}
	if job.Labels[labels.Component] != labels.ComponentLoadgen {
		t.Errorf("component label = %q, want %q", job.Labels[labels.Component], labels.ComponentLoadgen)
	}
	if job.Labels[labels.PartOf] != labels.PartOfValue {
		t.Errorf("part-of label missing")
	}
	if bl := job.Spec.BackoffLimit; bl == nil || *bl != 0 {
		t.Errorf("backoffLimit must be 0 so load is never replayed, got %v", bl)
	}

	c := job.Spec.Template.Spec.Containers[0]
	if c.Image != DefaultImage {
		t.Errorf("image = %q, want default %q", c.Image, DefaultImage)
	}
	if c.Args[0] != string(SignalLogs) {
		t.Errorf("first arg = %q, want %q", c.Args[0], SignalLogs)
	}
	if got := argValue(c.Args, "--rate"); got != "1500" {
		t.Errorf("--rate = %q, want 1500", got)
	}
	if got := argValue(c.Args, "--otlp-endpoint"); got != baseOpts().Endpoint {
		t.Errorf("--otlp-endpoint = %q", got)
	}
	if got := argValue(c.Args, "--duration"); got != "30s" {
		t.Errorf("--duration = %q, want 30s", got)
	}
	if !hasArg(c.Args, "--otlp-insecure") {
		t.Errorf("insecure endpoint should pass --otlp-insecure")
	}
	if job.Spec.Template.Spec.RestartPolicy != "Never" {
		t.Errorf("restart policy = %q, want Never", job.Spec.Template.Spec.RestartPolicy)
	}
}

func TestJobSecureOmitsInsecureFlag(t *testing.T) {
	o := baseOpts()
	o.Insecure = false
	job := Job("byoo-perf", "perf-collector", SignalMetrics, 100, o)
	if hasArg(job.Spec.Template.Spec.Containers[0].Args, "--otlp-insecure") {
		t.Errorf("secure endpoint must not pass --otlp-insecure")
	}
}

func TestJobsSkipsDisabledSignals(t *testing.T) {
	o := baseOpts()
	o.MetricsPerSec = 0
	jobs := Jobs("byoo-perf", "perf-collector", o)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job (logs only), got %d", len(jobs))
	}
	if !strings.HasSuffix(jobs[0].Name, "logs") {
		t.Errorf("expected logs job, got %q", jobs[0].Name)
	}

	o.LogsPerSec = 0
	o.MetricsPerSec = 0
	if jobs := Jobs("byoo-perf", "perf-collector", o); len(jobs) != 0 {
		t.Errorf("expected no jobs when all rates are zero, got %d", len(jobs))
	}
}

func TestJobImageOverride(t *testing.T) {
	o := baseOpts()
	o.Image = "example.invalid/telemetrygen:testtag"
	job := Job("byoo-perf", "perf-collector", SignalLogs, 10, o)
	if got := job.Spec.Template.Spec.Containers[0].Image; got != o.Image {
		t.Errorf("image override not applied: %q", got)
	}
}

func telemetryAttrs(args []string) []string {
	var out []string
	for i, a := range args {
		if a == "--telemetry-attributes" && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

func TestPayloadAttrsSplitUnderArgLimit(t *testing.T) {
	total := 320 * 1024
	attrs := payloadAttrs(total)
	if len(attrs) < 2 {
		t.Fatalf("expected payload split across multiple attributes, got %d", len(attrs))
	}
	sum := 0
	for i, kv := range attrs {
		if len(kv) > argMaxBytes {
			t.Errorf("attribute %d is %d bytes, exceeds argMaxBytes %d", i, len(kv), argMaxBytes)
		}
		prefix := fmt.Sprintf("payload%d=\"", i)
		if !strings.HasPrefix(kv, prefix) || !strings.HasSuffix(kv, "\"") {
			t.Fatalf("attribute %d not in key=\"value\" form", i)
		}
		sum += len(kv) - len(prefix) - 1
	}
	if sum != total {
		t.Errorf("payload value bytes = %d, want %d", sum, total)
	}
}

func TestPayloadAttrsClampsToMax(t *testing.T) {
	// At the documented maximum the full payload is materialized.
	if got := payloadValueBytes(payloadAttrs(MaxLogPayloadBytes)); got != MaxLogPayloadBytes {
		t.Errorf("at max: payload value bytes = %d, want %d", got, MaxLogPayloadBytes)
	}
	// Above the maximum the helper clamps rather than allocating unbounded.
	if got := payloadValueBytes(payloadAttrs(MaxLogPayloadBytes * 4)); got != MaxLogPayloadBytes {
		t.Errorf("above max: payload value bytes = %d, want clamp to %d", got, MaxLogPayloadBytes)
	}
	// Every attribute still respects the per-argument limit.
	for i, kv := range payloadAttrs(MaxLogPayloadBytes * 4) {
		if len(kv) > argMaxBytes {
			t.Errorf("attribute %d is %d bytes, exceeds argMaxBytes %d", i, len(kv), argMaxBytes)
		}
	}
}

func payloadValueBytes(attrs []string) int {
	sum := 0
	for i, kv := range attrs {
		prefix := fmt.Sprintf("payload%d=\"", i)
		sum += len(kv) - len(prefix) - 1
	}
	return sum
}

func TestLogsBimodalSplit(t *testing.T) {
	o := baseOpts()
	o.MetricsPerSec = 0
	o.LogsPerSec = 6
	o.LogBodyBytes = 8 * 1024
	o.LogPayloadBytes = 320 * 1024
	o.LargeRecordFraction = 0.5

	jobs := Jobs("byoo-perf", "perf-collector", o)
	if len(jobs) != 2 {
		t.Fatalf("expected small+large log jobs, got %d", len(jobs))
	}
	smallJob, largeJob := findBySuffix(jobs, "logs-small"), findBySuffix(jobs, "logs-large")
	if smallJob == nil || largeJob == nil {
		t.Fatalf("expected logs-small and logs-large jobs, got %v", jobNames(jobs))
	}
	if got := argValue(smallJob.Spec.Template.Spec.Containers[0].Args, "--rate"); got != "3" {
		t.Errorf("small --rate = %q, want 3", got)
	}
	if got := argValue(largeJob.Spec.Template.Spec.Containers[0].Args, "--rate"); got != "3" {
		t.Errorf("large --rate = %q, want 3", got)
	}
	if attrs := telemetryAttrs(smallJob.Spec.Template.Spec.Containers[0].Args); len(attrs) != 0 {
		t.Errorf("small job must carry no payload attributes, got %d", len(attrs))
	}
	if attrs := telemetryAttrs(largeJob.Spec.Template.Spec.Containers[0].Args); len(attrs) == 0 {
		t.Errorf("large job must carry payload attributes")
	}
}

func TestLogsUniformPayloadNoSplit(t *testing.T) {
	o := baseOpts()
	o.MetricsPerSec = 0
	o.LogsPerSec = 4
	o.LogPayloadBytes = 300 * 1024
	// No LargeRecordFraction -> every record large, single job.
	jobs := Jobs("byoo-perf", "perf-collector", o)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 uniform log job, got %d (%v)", len(jobs), jobNames(jobs))
	}
	if !strings.HasSuffix(jobs[0].Name, "-loadgen-logs") {
		t.Errorf("unexpected job name %q", jobs[0].Name)
	}
	if attrs := telemetryAttrs(jobs[0].Spec.Template.Spec.Containers[0].Args); len(attrs) == 0 {
		t.Errorf("uniform payload job must carry payload attributes")
	}
}

func TestLogBodyBytesClampedToMax(t *testing.T) {
	o := baseOpts()
	o.MetricsPerSec = 0
	o.LogsPerSec = 1
	o.LogBodyBytes = MaxLogBodyBytes * 4 // oversized

	jobs := Jobs("byoo-perf", "perf-collector", o)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 log job, got %d (%v)", len(jobs), jobNames(jobs))
	}
	body := argValue(jobs[0].Spec.Template.Spec.Containers[0].Args, "--body")
	if len(body) != MaxLogBodyBytes {
		t.Errorf("body length = %d, want clamp to %d", len(body), MaxLogBodyBytes)
	}
	if len(body) > argMaxBytes {
		t.Errorf("body length %d exceeds argMaxBytes %d", len(body), argMaxBytes)
	}
}

func TestBimodalRateNeverNonFiniteForNaNFraction(t *testing.T) {
	o := baseOpts()
	o.MetricsPerSec = 0
	o.LogsPerSec = 40
	o.LogPayloadBytes = 300 * 1024
	o.LargeRecordFraction = math.NaN() // must not split into NaN-rate Jobs

	jobs := Jobs("byoo-perf", "perf-collector", o)
	if len(jobs) != 1 {
		t.Fatalf("NaN fraction should fall back to a single uniform job, got %d (%v)", len(jobs), jobNames(jobs))
	}
	for _, j := range jobs {
		rate := argValue(j.Spec.Template.Spec.Containers[0].Args, "--rate")
		f, err := strconv.ParseFloat(rate, 64)
		if err != nil {
			t.Fatalf("job %s --rate %q not parseable: %v", j.Name, rate, err)
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			t.Errorf("job %s has non-finite --rate %q", j.Name, rate)
		}
	}
}

func TestWorkersFlag(t *testing.T) {
	o := baseOpts()
	o.Workers = 4
	job := Job("byoo-perf", "perf-collector", SignalLogs, 10, o)
	args := job.Spec.Template.Spec.Containers[0].Args
	if got := argValue(args, "--workers"); got != "4" {
		t.Errorf("--workers = %q, want 4", got)
	}
	// telemetrygen's --rate is per worker, so the requested aggregate rate of 10
	// with 4 workers must be emitted as 2.5 per worker.
	if got := argValue(args, "--rate"); got != "2.5" {
		t.Errorf("--rate = %q, want 2.5 (10 requested / 4 workers)", got)
	}
}

func TestRatePerWorkerAppliesToMetricsAndBimodalLogs(t *testing.T) {
	o := baseOpts()
	o.Workers = 4
	o.LogsPerSec = 40
	o.MetricsPerSec = 20
	o.LogPayloadBytes = 300 * 1024
	o.LargeRecordFraction = 0.25 // 10 large + 30 small, each divided by 4 workers

	jobs := Jobs("byoo-perf", "perf-collector", o)
	rateByJob := map[string]string{}
	for _, j := range jobs {
		rateByJob[j.Name] = argValue(j.Spec.Template.Spec.Containers[0].Args, "--rate")
	}

	want := map[string]string{
		"perf-collector-loadgen-logs-small": "7.5", // 30 / 4
		"perf-collector-loadgen-logs-large": "2.5", // 10 / 4
		"perf-collector-loadgen-metrics":    "5",   // 20 / 4
	}
	for name, wantRate := range want {
		if got := rateByJob[name]; got != wantRate {
			t.Errorf("%s --rate = %q, want %q", name, got, wantRate)
		}
	}
}
