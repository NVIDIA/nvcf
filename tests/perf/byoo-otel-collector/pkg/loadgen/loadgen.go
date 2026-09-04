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

// Package loadgen builds telemetrygen Jobs that drive synthetic OTLP load into
// the BYOO collector under test. Each enabled signal (logs, metrics) runs as a
// single-shot Kubernetes Job that sends at a fixed rate for a fixed duration
// and then completes, so a run applies a controlled, repeatable load.
package loadgen

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/labels"
)

// DefaultImage is the upstream telemetrygen image. The tag can be overridden
// per run; it defaults to a build that matches the collector-contrib tag used
// elsewhere in the repo.
const DefaultImage = "ghcr.io/open-telemetry/opentelemetry-collector-contrib/telemetrygen:v0.129.0"

// argMaxBytes bounds the payload carried by a single telemetrygen CLI argument.
// Linux caps one argument string at MAX_ARG_STRLEN (128 KiB); a large payload is
// split across several attributes so no single argument approaches that limit.
const argMaxBytes = 96 * 1024

// MaxLogPayloadBytes bounds the synthetic per-record payload attribute size.
// The generator materializes the payload in memory and passes it as process
// arguments, so an unbounded value could exhaust local memory or overflow the
// argument vector. 1 MiB comfortably exceeds the largest observed real record
// (~330 KiB) while staying well within argv limits. Callers must reject larger
// values before building Jobs.
const MaxLogPayloadBytes = 1 << 20

// MaxLogBodyBytes bounds the synthetic log body size. The body is a single
// telemetrygen argument (--body), so it must stay under one argument's limit;
// an unbounded value would allocate the whole string up front and could produce
// a --body argument the container process cannot start with.
const MaxLogBodyBytes = argMaxBytes

// Signal is an OTLP signal telemetrygen can generate.
type Signal string

const (
	SignalLogs    Signal = "logs"
	SignalMetrics Signal = "metrics"
)

// Options controls the generated load.
type Options struct {
	// Image is the telemetrygen image.
	Image string
	// Endpoint is the collector OTLP gRPC endpoint (host:port) load is sent to.
	Endpoint string
	// Insecure sends over plaintext gRPC (--otlp-insecure), which is what the
	// in-cluster harness Service exposes.
	Insecure bool
	// Duration is how long each generator runs.
	Duration time.Duration
	// LogsPerSec / MetricsPerSec are the per-second generation rates. A rate of
	// zero disables that signal's Job.
	LogsPerSec    int
	MetricsPerSec int
	// Workers is telemetrygen's --workers (concurrent generators per Job). Zero
	// leaves telemetrygen's default (1).
	Workers int
	// LogBodyBytes pads each synthetic log body to approximately this size. Zero
	// keeps telemetrygen's default tiny body.
	LogBodyBytes int
	// LogPayloadBytes attaches a synthetic string attribute of approximately
	// this many bytes to a log record, split across several attributes so no CLI
	// argument exceeds argMaxBytes. This is what pushes a record past the
	// collector's log-chunking threshold. Zero adds no payload attribute.
	LogPayloadBytes int
	// LargeRecordFraction (0..1) is the fraction of the logs rate emitted as
	// large (payload-bearing) records; the remainder are small body-only
	// records, reproducing a bimodal record-size mix. A value <= 0 applies
	// LogPayloadBytes uniformly to every record; a value >= 1 does the same.
	LargeRecordFraction float64
}

// Job builds the telemetrygen Job for a single signal at the given rate. For
// logs it applies the body/payload shaping in opts; metrics ignore shaping.
func Job(namespace, instance string, signal Signal, rate int, opts Options) *batchv1.Job {
	body, payload := 0, 0
	if signal == SignalLogs {
		body, payload = opts.LogBodyBytes, opts.LogPayloadBytes
	}
	name := fmt.Sprintf("%s-loadgen-%s", instance, signal)
	return jobShaped(namespace, instance, name, signal, float64(rate), body, payload, opts)
}

// jobShaped builds a telemetrygen Job with an explicit (possibly fractional)
// rate and log body/payload sizes.
func jobShaped(namespace, instance, name string, signal Signal, rate float64, bodyBytes, payloadBytes int, opts Options) *batchv1.Job {
	image := opts.Image
	if image == "" {
		image = DefaultImage
	}

	l := labels.Base()
	l[labels.Instance] = instance
	l[labels.Component] = labels.ComponentLoadgen

	// telemetrygen's --rate is per worker (each worker emits at that rate), so
	// to hit the requested aggregate rate we divide it across the workers. An
	// unset/non-positive worker count means telemetrygen runs a single worker.
	workers := opts.Workers
	if workers <= 0 {
		workers = 1
	}
	perWorkerRate := rate / float64(workers)

	args := []string{
		string(signal),
		"--otlp-endpoint", opts.Endpoint,
		"--duration", opts.Duration.String(),
		"--rate", formatRate(perWorkerRate),
	}
	if opts.Insecure {
		args = append(args, "--otlp-insecure")
	}
	if opts.Workers > 0 {
		args = append(args, "--workers", strconv.Itoa(opts.Workers))
	}
	if bodyBytes > 0 {
		// Defensive cap so a misconfigured profile can never allocate an
		// unbounded body or produce an oversized single --body argument.
		if bodyBytes > MaxLogBodyBytes {
			bodyBytes = MaxLogBodyBytes
		}
		args = append(args, "--body", strings.Repeat("x", bodyBytes))
	}
	for _, kv := range payloadAttrs(payloadBytes) {
		args = append(args, "--telemetry-attributes", kv)
	}

	// A load generator must never be retried: a retry would replay the whole
	// load and corrupt the measurement window.
	backoffLimit := int32(0)

	return &batchv1.Job{
		TypeMeta: metav1.TypeMeta{Kind: "Job", APIVersion: "batch/v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    l,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: l},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:  "telemetrygen",
						Image: image,
						Args:  args,
					}},
				},
			},
		},
	}
}

// Jobs builds the telemetrygen Jobs for the enabled signals (rate > 0). When a
// bimodal log mix is requested (LogPayloadBytes and a LargeRecordFraction in the
// open interval (0,1)), logs are split into a small body-only Job and a large
// payload-bearing Job whose rates sum to LogsPerSec.
func Jobs(namespace, instance string, opts Options) []*batchv1.Job {
	var jobs []*batchv1.Job
	jobs = append(jobs, logJobs(namespace, instance, opts)...)
	if opts.MetricsPerSec > 0 {
		jobs = append(jobs, Job(namespace, instance, SignalMetrics, opts.MetricsPerSec, opts))
	}
	return jobs
}

// logJobs builds the log generator Job(s), splitting into small/large variants
// when a bimodal record-size mix is requested.
func logJobs(namespace, instance string, opts Options) []*batchv1.Job {
	if opts.LogsPerSec <= 0 {
		return nil
	}
	total := float64(opts.LogsPerSec)
	logsName := fmt.Sprintf("%s-loadgen-logs", instance)

	// One uniform Job unless a bimodal split is explicitly requested. Only a
	// finite fraction strictly inside (0,1) splits the load; anything else
	// (no payload, fraction <=0 or >=1, or a non-finite NaN/Inf) is treated as
	// uniform, which also keeps the computed --rate finite.
	frac := opts.LargeRecordFraction
	if opts.LogPayloadBytes <= 0 || !(frac > 0 && frac < 1) {
		return []*batchv1.Job{jobShaped(namespace, instance, logsName, SignalLogs, total, opts.LogBodyBytes, opts.LogPayloadBytes, opts)}
	}

	largeRate := total * frac
	smallRate := total - largeRate
	var jobs []*batchv1.Job
	if smallRate > 0 {
		jobs = append(jobs, jobShaped(namespace, instance, logsName+"-small", SignalLogs, smallRate, opts.LogBodyBytes, 0, opts))
	}
	jobs = append(jobs, jobShaped(namespace, instance, logsName+"-large", SignalLogs, largeRate, opts.LogBodyBytes, opts.LogPayloadBytes, opts))
	return jobs
}

// payloadAttrs returns telemetrygen --telemetry-attributes values (key="value")
// whose combined value bytes total approximately totalBytes, split so no single
// argument exceeds argMaxBytes. Each attribute is counted toward a log record's
// size by the collector's log-chunking processor.
func payloadAttrs(totalBytes int) []string {
	if totalBytes <= 0 {
		return nil
	}
	// Defensive cap: the CLI rejects oversized --payload-bytes, but clamp here
	// too so a misconfigured profile can never trigger an unbounded allocation.
	if totalBytes > MaxLogPayloadBytes {
		totalBytes = MaxLogPayloadBytes
	}
	// Reserve room for the key and the key="" wrapper within one argument.
	const perAttr = argMaxBytes - 32
	var attrs []string
	remaining := totalBytes
	for i := 0; remaining > 0; i++ {
		n := remaining
		if n > perAttr {
			n = perAttr
		}
		attrs = append(attrs, fmt.Sprintf("payload%d=%q", i, strings.Repeat("x", n)))
		remaining -= n
	}
	return attrs
}

// formatRate renders a telemetrygen --rate value, trimming trailing zeros so an
// integer rate stays integer-formatted.
func formatRate(rate float64) string {
	return strconv.FormatFloat(rate, 'f', -1, 64)
}
