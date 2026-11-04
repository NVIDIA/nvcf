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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestWatchStargatesCommandKeepsObservationInputsExplicit(t *testing.T) {
	t.Setenv("BDD_WATCH_CONTEXT", "k3d-ncp-local-cp")
	got, err := WatchStargatesCommand(
		"127.0.0.1:50071",
		"llm-request-router.nvcf.svc.cluster.local",
		"stargate-quic-tls",
		"nvcf",
		"${BDD_WATCH_CONTEXT}",
		"3",
	)
	if err != nil {
		t.Fatalf("build WatchStargates command: %v", err)
	}
	want := "bash tests/bdd/scripts/observe-watch-stargates.sh 127.0.0.1:50071 llm-request-router.nvcf.svc.cluster.local stargate-quic-tls nvcf k3d-ncp-local-cp 3"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestWatchStargatesCommandRejectsMissingOrInvalidInputs(t *testing.T) {
	tests := []struct {
		name      string
		endpoint  string
		authority string
		duration  string
	}{
		{name: "empty endpoint", endpoint: "", authority: "router.nvcf.svc", duration: "3"},
		{name: "empty authority", endpoint: "127.0.0.1:50071", authority: "", duration: "3"},
		{name: "invalid duration", endpoint: "127.0.0.1:50071", authority: "router.nvcf.svc", duration: "3s"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := WatchStargatesCommand(test.endpoint, test.authority, "tls", "nvcf", "context", test.duration); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestPylonMetricsCommandKeepsExpectationsExplicit(t *testing.T) {
	got, err := PylonMetricsCommand("bdd-registration-tls", "llm-worker", "k3d-ncp-local-compute-1", "10m", []PylonMetricExpectation{
		{Metric: "pylon_registration_stream_connected", Comparison: "exactly", Count: 5},
		{Metric: "pylon_reverse_tunnel_connected", Comparison: "at least", Count: 3},
	})
	if err != nil {
		t.Fatalf("build Pylon metrics command: %v", err)
	}
	want := "bash tests/bdd/scripts/wait-pylon-metrics.sh bdd-registration-tls llm-worker k3d-ncp-local-compute-1 10m pylon_registration_stream_connected exactly 5 pylon_reverse_tunnel_connected 'at least' 3"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestPylonMetricsCommandRejectsInvalidExpectations(t *testing.T) {
	tests := []struct {
		name         string
		expectations []PylonMetricExpectation
	}{
		{name: "empty", expectations: nil},
		{name: "invalid metric", expectations: []PylonMetricExpectation{{Metric: "metric name", Comparison: "exactly", Count: 1}}},
		{name: "invalid comparison", expectations: []PylonMetricExpectation{{Metric: "metric_name", Comparison: "more than", Count: 1}}},
		{name: "negative count", expectations: []PylonMetricExpectation{{Metric: "metric_name", Comparison: "exactly", Count: -1}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := PylonMetricsCommand("function", "llm-worker", "context", "10m", test.expectations); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestObserveWatchStargatesScriptAcceptsSnapshotThenDeadline(t *testing.T) {
	fakeBin := t.TempDir()
	writeExecutable(t, filepath.Join(fakeBin, "kubectl"), "#!/bin/sh\nprintf 'dGVzdC1jYQ=='\n")
	writeExecutable(t, filepath.Join(fakeBin, "grpcurl"), `#!/bin/sh
printf '{\n  "stargates": []\n}\n'
sleep 1
printf 'ERROR:\n  Code: DeadlineExceeded\n  Message: context deadline exceeded\n' >&2
exit 1
`)

	output, err := runRegistrationScript(t, fakeBin, "observe-watch-stargates.sh", "127.0.0.1:50071", "router.nvcf.svc", "tls", "nvcf", "context", "1")
	if err != nil {
		t.Fatalf("observe WatchStargates: %v\n%s", err, output)
	}
	for _, want := range []string{`"stargates"`, "DeadlineExceeded"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output = %q, want %q", output, want)
		}
	}
}

func TestObserveWatchStargatesScriptSendsW3CTraceContext(t *testing.T) {
	fakeBin := t.TempDir()
	writeExecutable(t, filepath.Join(fakeBin, "kubectl"), "#!/bin/sh\nprintf 'dGVzdC1jYQ=='\n")
	writeExecutable(t, filepath.Join(fakeBin, "grpcurl"), `#!/bin/sh
printf '{\n  "stargates": []\n}\n'
for arg in "$@"; do
  case "$arg" in
    traceparent:*) printf 'observed %s\n' "$arg" >&2 ;;
  esac
done
sleep 1
printf 'ERROR:\n  Code: DeadlineExceeded\n  Message: context deadline exceeded\n' >&2
exit 1
`)

	output, err := runRegistrationScript(t, fakeBin, "observe-watch-stargates.sh", "127.0.0.1:50071", "router.nvcf.svc", "tls", "nvcf", "context", "1")
	if err != nil {
		t.Fatalf("observe WatchStargates: %v\n%s", err, output)
	}
	traceparent := regexp.MustCompile(`observed traceparent: (\S+)`).FindStringSubmatch(output)
	if traceparent == nil {
		t.Fatalf("output = %q, want an observed traceparent header", output)
	}
	if !regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`).MatchString(traceparent[1]) {
		t.Fatalf("traceparent = %q, want a valid W3C trace context value", traceparent[1])
	}
}

func TestObserveWatchStargatesScriptRejectsImmediateProductDeadline(t *testing.T) {
	fakeBin := t.TempDir()
	writeExecutable(t, filepath.Join(fakeBin, "kubectl"), "#!/bin/sh\nprintf 'dGVzdC1jYQ=='\n")
	writeExecutable(t, filepath.Join(fakeBin, "grpcurl"), `#!/bin/sh
printf '{\n  "stargates": []\n}\n'
printf 'ERROR:\n  Code: DeadlineExceeded\n  Message: product returned deadline exceeded\n' >&2
exit 1
`)

	output, err := runRegistrationScript(t, fakeBin, "observe-watch-stargates.sh", "127.0.0.1:50071", "router.nvcf.svc", "tls", "nvcf", "context", "3")
	if err == nil {
		t.Fatal("expected immediate product deadline failure")
	}
	if !strings.Contains(output, "before the 3s observation deadline") {
		t.Fatalf("output = %q, want early deadline diagnostic", output)
	}
}

func TestObserveWatchStargatesScriptRejectsDeadlineWithoutSnapshot(t *testing.T) {
	fakeBin := t.TempDir()
	writeExecutable(t, filepath.Join(fakeBin, "kubectl"), "#!/bin/sh\nprintf 'dGVzdC1jYQ=='\n")
	writeExecutable(t, filepath.Join(fakeBin, "grpcurl"), "#!/bin/sh\nprintf 'context deadline exceeded\\n' >&2\nexit 1\n")

	output, err := runRegistrationScript(t, fakeBin, "observe-watch-stargates.sh", "127.0.0.1:50071", "router.nvcf.svc", "tls", "nvcf", "context", "3")
	if err == nil {
		t.Fatal("expected missing snapshot failure")
	}
	if !strings.Contains(output, "did not return a streamed snapshot") {
		t.Fatalf("output = %q, want missing snapshot diagnostic", output)
	}
}

func TestWaitPylonMetricsScriptChecksEverySelectedPodAndCountsTimestampedSeries(t *testing.T) {
	fakeBin := t.TempDir()
	writeExecutable(t, filepath.Join(fakeBin, "kubectl"), `#!/bin/sh
case "$*" in
  *"get pods -A -o json"*)
    printf '%s\n' '{"items":['
    printf '%s\n' '{"metadata":{"namespace":"functions","name":"worker-0","annotations":{"function-name":"bdd-registration-tls"}},"status":{"phase":"Running"},"spec":{"containers":[{"name":"llm-worker"}]}},'
    printf '%s\n' '{"metadata":{"namespace":"functions","name":"worker-1","annotations":{"function-name":"bdd-registration-tls"}},"status":{"phase":"Running"},"spec":{"containers":[{"name":"llm-worker"}]}},'
    printf '%s\n' '{"metadata":{"namespace":"other","name":"worker-other","annotations":{"function-name":"another-function"}},"status":{"phase":"Running"},"spec":{"containers":[{"name":"llm-worker"}]}}'
    printf '%s\n' ']}'
    ;;
  *)
    printf 'pylon_registration_stream_connected{router="a"} 1 1712345678\n'
    printf 'pylon_registration_stream_connected{router="b"} 1\n'
    printf 'pylon_registration_stream_connected{router="c"} 1\n'
    printf 'pylon_reverse_tunnel_connected{router="a"} 1\n'
    printf 'pylon_reverse_tunnel_connected{router="b"} 1\n'
    printf 'pylon_reverse_tunnel_connected{router="c"} 1\n'
    ;;
esac
`)

	output, err := runRegistrationScript(
		t,
		fakeBin,
		"wait-pylon-metrics.sh",
		"bdd-registration-tls",
		"llm-worker",
		"k3d-ncp-local-compute-1",
		"1s",
		"pylon_registration_stream_connected",
		"exactly",
		"3",
		"pylon_reverse_tunnel_connected",
		"at least",
		"3",
	)
	if err != nil {
		t.Fatalf("wait for Pylon metrics: %v\n%s", err, output)
	}
	for _, want := range []string{
		"functions/worker-0 pylon_registration_stream_connected=3",
		"functions/worker-0 pylon_reverse_tunnel_connected=3",
		"functions/worker-1 pylon_registration_stream_connected=3",
		"functions/worker-1 pylon_reverse_tunnel_connected=3",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output = %q, want %q", output, want)
		}
	}
	if strings.Contains(output, "worker-other") {
		t.Fatalf("output = %q, did not want metrics from another function", output)
	}
}

func TestWaitPylonMetricsScriptDoesNotTreatScrapeFailureAsZero(t *testing.T) {
	fakeBin := t.TempDir()
	writeExecutable(t, filepath.Join(fakeBin, "kubectl"), `#!/bin/sh
case "$*" in
  *"get pods -A -o json"*)
    printf '%s\n' '{"items":[{"metadata":{"namespace":"functions","name":"worker-0","annotations":{"function-name":"bdd-registration-tls"}},"status":{"phase":"Running"},"spec":{"containers":[{"name":"llm-worker"}]}}]}'
    ;;
  *) printf 'metrics endpoint unavailable\n' >&2; exit 1 ;;
esac
`)

	output, err := runRegistrationScript(
		t,
		fakeBin,
		"wait-pylon-metrics.sh",
		"bdd-registration-tls",
		"llm-worker",
		"k3d-ncp-local-compute-1",
		"1s",
		"pylon_registration_stream_connected",
		"exactly",
		"0",
	)
	if err == nil {
		t.Fatal("expected metrics scrape failure")
	}
	if !strings.Contains(output, "metrics scrape failed: metrics endpoint unavailable") {
		t.Fatalf("output = %q, want preserved scrape failure", output)
	}
}

func TestWaitPylonMetricsScriptPreservesPodDiscoveryFailure(t *testing.T) {
	fakeBin := t.TempDir()
	writeExecutable(t, filepath.Join(fakeBin, "kubectl"), "#!/bin/sh\nprintf 'API server unavailable\\n' >&2\nexit 1\n")

	output, err := runRegistrationScript(
		t,
		fakeBin,
		"wait-pylon-metrics.sh",
		"bdd-registration-tls",
		"llm-worker",
		"k3d-ncp-local-compute-1",
		"1s",
		"pylon_registration_stream_connected",
		"exactly",
		"0",
	)
	if err == nil {
		t.Fatal("expected pod discovery failure")
	}
	if !strings.Contains(output, "pod discovery failed: API server unavailable") {
		t.Fatalf("output = %q, want preserved discovery failure", output)
	}
}

func runRegistrationScript(t *testing.T, fakeBin, scriptName string, args ...string) (string, error) {
	t.Helper()
	script := filepath.Join("..", "scripts", scriptName)
	command := exec.Command("bash", append([]string{script}, args...)...)
	command.Env = append(os.Environ(), "PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := command.CombinedOutput()
	return string(output), err
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}
