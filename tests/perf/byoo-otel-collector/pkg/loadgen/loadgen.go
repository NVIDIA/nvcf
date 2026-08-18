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
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/labels"
)

// DefaultImage is the upstream telemetrygen image. The tag can be overridden
// per run; it defaults to a build that matches the collector-contrib tag used
// elsewhere in the repo.
const DefaultImage = "ghcr.io/open-telemetry/opentelemetry-collector-contrib/telemetrygen:0.129.1"

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
}

// Job builds the telemetrygen Job for a single signal at the given rate.
func Job(namespace, instance string, signal Signal, rate int, opts Options) *batchv1.Job {
	image := opts.Image
	if image == "" {
		image = DefaultImage
	}
	name := fmt.Sprintf("%s-loadgen-%s", instance, signal)

	l := labels.Base()
	l[labels.Instance] = instance
	l[labels.Component] = labels.ComponentLoadgen

	args := []string{
		string(signal),
		"--otlp-endpoint", opts.Endpoint,
		"--duration", opts.Duration.String(),
		"--rate", strconv.Itoa(rate),
	}
	if opts.Insecure {
		args = append(args, "--otlp-insecure")
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

// Jobs builds one telemetrygen Job per enabled signal (rate > 0).
func Jobs(namespace, instance string, opts Options) []*batchv1.Job {
	var jobs []*batchv1.Job
	if opts.LogsPerSec > 0 {
		jobs = append(jobs, Job(namespace, instance, SignalLogs, opts.LogsPerSec, opts))
	}
	if opts.MetricsPerSec > 0 {
		jobs = append(jobs, Job(namespace, instance, SignalMetrics, opts.MetricsPerSec, opts))
	}
	return jobs
}
