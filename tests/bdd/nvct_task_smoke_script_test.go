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

package bdd_tmp

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// fakeTaskSmokeCurl answers the three request kinds the task smoke script
// sends: API key creation, task creation, and task status. It copies the task
// creation body to FAKE_TASK_REQUEST so the test can inspect it.
const fakeTaskSmokeCurl = `#!/usr/bin/env bash
set -euo pipefail
out=""
data=""
url=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    --data-binary) data="${2#@}"; shift 2 ;;
    -X|-H|-w) shift 2 ;;
    http*) url="$1"; shift ;;
    *) shift ;;
  esac
done
case "$url" in
  */v1/keys) printf '{"value":"nvapi-fake"}' >"$out" ;;
  */v1/nvct/tasks)
    cp "$data" "$FAKE_TASK_REQUEST"
    printf '{"task":{"id":"task-1"}}' >"$out"
    ;;
  */v1/nvct/tasks/task-1) printf '{"task":{"status":"COMPLETED"}}' >"$out" ;;
  *) echo "unexpected URL: $url" >&2; exit 2 ;;
esac
printf '200'
`

// runTaskSmokeWithFakeCurl runs the container-mode task smoke script against a
// fake curl and returns the task creation request it sent.
func runTaskSmokeWithFakeCurl(t *testing.T, extraEnv ...string) map[string]any {
	t.Helper()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "curl"), []byte(fakeTaskSmokeCurl), 0o755); err != nil {
		t.Fatalf("write fake curl: %v", err)
	}
	statePath := filepath.Join(binDir, "state.json")
	if err := os.WriteFile(statePath, []byte(`{"token":"admin-jwt"}`), 0o600); err != nil {
		t.Fatalf("write CLI state: %v", err)
	}
	requestPath := filepath.Join(binDir, "create-task.json")

	cmd := exec.Command("bash", "scripts/run-nvct-task-smoke.sh")
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"FAKE_TASK_REQUEST="+requestPath,
		"NVCT_BDD_STATE_PATH="+statePath,
		"NVCT_BDD_TASK_IMAGE=registry.example.com/samples/task-byoo-sample:test",
		"NVCT_BDD_TASK_INSTANCE_TYPE=NCP.GPU.H100_1x",
		"NVCT_BDD_TASK_POLL_SECONDS=1",
		"NVCT_BDD_TASK_LOGS_TELEMETRY_ID=",
		"NVCT_BDD_TASK_METRICS_TELEMETRY_ID=",
		"NVCT_BDD_TASK_TRACES_TELEMETRY_ID=",
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run task smoke script: %v\n%s", err, output)
	}

	body, err := os.ReadFile(requestPath)
	if err != nil {
		t.Fatalf("read task creation request: %v", err)
	}
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("parse task creation request: %v\n%s", err, body)
	}
	return request
}

func TestNVCTTaskSmokeAddsRequestedTelemetries(t *testing.T) {
	request := runTaskSmokeWithFakeCurl(t,
		"NVCT_BDD_TASK_LOGS_TELEMETRY_ID=logs-id",
		"NVCT_BDD_TASK_METRICS_TELEMETRY_ID=metrics-id",
		"NVCT_BDD_TASK_TRACES_TELEMETRY_ID=traces-id",
	)
	want := map[string]any{
		"logsTelemetryId":    "logs-id",
		"metricsTelemetryId": "metrics-id",
		"tracesTelemetryId":  "traces-id",
	}
	if got := request["telemetries"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("telemetries = %#v, want %#v", got, want)
	}
	if got := request["containerImage"]; got != "registry.example.com/samples/task-byoo-sample:test" {
		t.Fatalf("containerImage = %#v", got)
	}
}

func TestNVCTTaskSmokeAddsOnlyNonEmptyTelemetries(t *testing.T) {
	request := runTaskSmokeWithFakeCurl(t, "NVCT_BDD_TASK_TRACES_TELEMETRY_ID=traces-id")
	want := map[string]any{"tracesTelemetryId": "traces-id"}
	if got := request["telemetries"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("telemetries = %#v, want %#v", got, want)
	}
}

func TestNVCTTaskSmokeOmitsTelemetriesByDefault(t *testing.T) {
	request := runTaskSmokeWithFakeCurl(t)
	if telemetries, ok := request["telemetries"]; ok {
		t.Fatalf("request without telemetry IDs has telemetries %#v", telemetries)
	}
}
