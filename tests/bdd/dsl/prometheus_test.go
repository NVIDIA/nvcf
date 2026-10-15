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

package dsl

import (
	"path/filepath"
	"strings"
	"testing"
)

const routerSelector = "app.kubernetes.io/name=llm-request-router,app.kubernetes.io/instance=llm-request-router"

func TestPrometheusTotalsCommandKeepsTargetsAndExpectationsExplicit(t *testing.T) {
	t.Setenv("BDD_FUNCTION_ID", "function-1")
	got, err := PrometheusTotalsCommand("9090", routerSelector, "nvcf", "k3d-ncp-local-cp", "2m", []PrometheusTotalExpectation{
		{Metric: "stargate_routing_session_selections_total", Labels: " routing_key=${BDD_FUNCTION_ID} , session_state=new ", Comparison: "exactly", Total: 2},
		{Metric: "llm_api_gateway_last_cluster_lookups_total", Labels: "", Comparison: "at least", Total: 6},
	})
	if err != nil {
		t.Fatalf("build Prometheus totals command: %v", err)
	}
	want := "bash tests/bdd/scripts/wait-prometheus-totals.sh 9090 " + routerSelector + " nvcf k3d-ncp-local-cp 2m" +
		" stargate_routing_session_selections_total routing_key=function-1,session_state=new exactly 2" +
		" llm_api_gateway_last_cluster_lookups_total '' 'at least' 6"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestPrometheusTotalsCommandRejectsInvalidInputs(t *testing.T) {
	valid := PrometheusTotalExpectation{Metric: "metric_total", Labels: "a=b", Comparison: "exactly", Total: 1}
	tests := []struct {
		name         string
		port         string
		expectations []PrometheusTotalExpectation
	}{
		{name: "port not a number", port: "metrics", expectations: []PrometheusTotalExpectation{valid}},
		{name: "port out of range", port: "70000", expectations: []PrometheusTotalExpectation{valid}},
		{name: "empty", port: "9090", expectations: nil},
		{name: "invalid metric", port: "9090", expectations: []PrometheusTotalExpectation{{Metric: "metric name", Comparison: "exactly"}}},
		{name: "label without value", port: "9090", expectations: []PrometheusTotalExpectation{{Metric: "m", Labels: "a", Comparison: "exactly"}}},
		{name: "empty label value", port: "9090", expectations: []PrometheusTotalExpectation{{Metric: "m", Labels: "a=", Comparison: "exactly"}}},
		{name: "quoted label value", port: "9090", expectations: []PrometheusTotalExpectation{{Metric: "m", Labels: `a="b"`, Comparison: "exactly"}}},
		{name: "invalid label name", port: "9090", expectations: []PrometheusTotalExpectation{{Metric: "m", Labels: "a-b=c", Comparison: "exactly"}}},
		{name: "repeated label", port: "9090", expectations: []PrometheusTotalExpectation{{Metric: "m", Labels: "a=b,a=c", Comparison: "exactly"}}},
		{name: "invalid comparison", port: "9090", expectations: []PrometheusTotalExpectation{{Metric: "m", Comparison: "more than"}}},
		{name: "negative total", port: "9090", expectations: []PrometheusTotalExpectation{{Metric: "m", Comparison: "exactly", Total: -1}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := PrometheusTotalsCommand(test.port, routerSelector, "nvcf", "context", "2m", test.expectations); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	if _, err := PrometheusTotalsCommand("9090", " ", "nvcf", "context", "2m", []PrometheusTotalExpectation{valid}); err == nil {
		t.Fatal("expected empty selector error")
	}
}

// routerMetricsKubectl serves two running router pods and one terminating
// pod. The terminating pod's samples must not count.
const routerMetricsKubectl = `#!/bin/sh
case "$*" in
  *"get pods --selector"*)
    printf '%s\n' '{"items":['
    printf '%s\n' '{"metadata":{"name":"router-0"},"status":{"phase":"Running"}},'
    printf '%s\n' '{"metadata":{"name":"router-1"},"status":{"phase":"Running"}},'
    printf '%s\n' '{"metadata":{"name":"router-old","deletionTimestamp":"2026-01-01T00:00:00Z"},"status":{"phase":"Running"}}'
    printf '%s\n' ']}'
    ;;
  *router-0:9090*)
    printf '# HELP stargate_routing_session_selections_total Session selections.\n'
    printf '# TYPE stargate_routing_session_selections_total counter\n'
    printf 'stargate_routing_session_selections_total{algorithm="wait-and-widen",model="m",routing_key="f1",selection="primary",session_state="returning"} 3\n'
    printf 'stargate_routing_session_selections_total{algorithm="wait-and-widen",model="m",routing_key="f1",selection="fallback",session_state="returning"} 0\n'
    printf 'stargate_routing_session_selections_total{algorithm="wait-and-widen",model="m",routing_key="f1",selection="primary",session_state="new"} 1 1712345678\n'
    printf 'stargate_routing_session_selections_total{algorithm="wait-and-widen",model="m",routing_key="f2",selection="primary",session_state="returning"} 7\n'
    printf 'stargate_routing_session_selections_total_created{routing_key="f1",selection="primary",session_state="returning"} 1712345678\n'
    printf 'stargate_active_inference_servers{model="m",routing_key="f1"} 2\n'
    ;;
  *router-1:9090*)
    printf 'stargate_routing_session_selections_total{algorithm="wait-and-widen",model="m",routing_key="f1",selection="primary",session_state="returning"} 2\n'
    printf 'stargate_routing_session_selections_total{algorithm="wait-and-widen",model="m",routing_key="f1",selection="primary",session_state="new"} 1\n'
    printf 'stargate_active_inference_servers{model="m",routing_key="f1"} 2\n'
    ;;
  *router-old*)
    printf 'stargate_routing_session_selections_total{algorithm="wait-and-widen",model="m",routing_key="f1",selection="primary",session_state="returning"} 100\n'
    ;;
  *) printf 'unexpected kubectl call: %s\n' "$*" >&2; exit 1 ;;
esac
`

func TestWaitPrometheusTotalsScriptSumsMatchingSamplesAcrossRunningPods(t *testing.T) {
	fakeBin := t.TempDir()
	writeExecutable(t, filepath.Join(fakeBin, "kubectl"), routerMetricsKubectl)

	output, err := runRegistrationScript(
		t,
		fakeBin,
		"wait-prometheus-totals.sh",
		"9090", routerSelector, "nvcf", "k3d-ncp-local-cp", "1s",
		"stargate_routing_session_selections_total", "routing_key=f1,session_state=returning,selection=primary", "exactly", "5",
		"stargate_routing_session_selections_total", "routing_key=f1,session_state=returning,selection=fallback", "exactly", "0",
		"stargate_routing_session_selections_total", "routing_key=f1,session_state=new", "exactly", "2",
		"stargate_routing_session_selections_total", "session_state=stale", "exactly", "0",
		"stargate_routing_session_selections_total", "", "at least", "14",
		"stargate_active_inference_servers", "routing_key=f1", "exactly", "4",
	)
	if err != nil {
		t.Fatalf("wait for Prometheus totals: %v\n%s", err, output)
	}
	for _, want := range []string{
		"stargate_routing_session_selections_total{routing_key=f1,session_state=returning,selection=primary} total=5 across 2 pods, want exactly 5",
		"stargate_routing_session_selections_total{routing_key=f1,session_state=new} total=2 across 2 pods",
		"stargate_routing_session_selections_total{} total=14 across 2 pods, want at least 14",
		"stargate_active_inference_servers{routing_key=f1} total=4 across 2 pods",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output = %q, want %q", output, want)
		}
	}
}

func TestWaitPrometheusTotalsScriptReportsTheObservedTotalOnTimeout(t *testing.T) {
	fakeBin := t.TempDir()
	writeExecutable(t, filepath.Join(fakeBin, "kubectl"), routerMetricsKubectl)

	output, err := runRegistrationScript(
		t,
		fakeBin,
		"wait-prometheus-totals.sh",
		"9090", routerSelector, "nvcf", "k3d-ncp-local-cp", "1s",
		"stargate_routing_session_selections_total", "routing_key=f1,session_state=returning,selection=fallback", "exactly", "1",
	)
	if err == nil {
		t.Fatalf("expected a total mismatch\n%s", output)
	}
	if !strings.Contains(output, "selection=fallback} total=0 across 2 pods, want exactly 1") {
		t.Fatalf("output = %q, want the observed total", output)
	}
}

func TestWaitPrometheusTotalsScriptDoesNotTreatScrapeFailureAsZero(t *testing.T) {
	fakeBin := t.TempDir()
	writeExecutable(t, filepath.Join(fakeBin, "kubectl"), `#!/bin/sh
case "$*" in
  *"get pods --selector"*)
    printf '%s\n' '{"items":[{"metadata":{"name":"router-0"},"status":{"phase":"Running"}}]}'
    ;;
  *) printf 'metrics endpoint unavailable\n' >&2; exit 1 ;;
esac
`)

	output, err := runRegistrationScript(
		t,
		fakeBin,
		"wait-prometheus-totals.sh",
		"9090", routerSelector, "nvcf", "k3d-ncp-local-cp", "1s",
		"stargate_routing_session_selections_total", "session_state=stale", "exactly", "0",
	)
	if err == nil {
		t.Fatal("expected metrics scrape failure")
	}
	if !strings.Contains(output, "nvcf/router-0 metrics scrape failed: metrics endpoint unavailable") {
		t.Fatalf("output = %q, want preserved scrape failure", output)
	}
}

func TestWaitPrometheusTotalsScriptWaitsForARunningPod(t *testing.T) {
	fakeBin := t.TempDir()
	writeExecutable(t, filepath.Join(fakeBin, "kubectl"), "#!/bin/sh\nprintf '{\"items\":[]}\\n'\n")

	output, err := runRegistrationScript(
		t,
		fakeBin,
		"wait-prometheus-totals.sh",
		"9090", routerSelector, "nvcf", "k3d-ncp-local-cp", "1s",
		"stargate_routing_session_selections_total", "", "exactly", "0",
	)
	if err == nil {
		t.Fatal("expected no running pod failure")
	}
	if !strings.Contains(output, "no running pod matches "+routerSelector+" in namespace nvcf") {
		t.Fatalf("output = %q, want missing pod summary", output)
	}
}
