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
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/labels"
)

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
