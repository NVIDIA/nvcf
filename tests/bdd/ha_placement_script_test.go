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
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func haPlacementPod(node, phase, ready string) string {
	return fmt.Sprintf(
		`{"spec":{"nodeName":%q},"status":{"phase":%q,"conditions":[{"type":"Ready","status":%q}]}}`,
		node, phase, ready,
	)
}

func runHAPlacement(t *testing.T, pods ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", "scripts/assert-ha-placement.sh", "2")
	cmd.Stdin = strings.NewReader(`{"items":[` + strings.Join(pods, ",") + `]}`)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func TestHAPlacementAcceptsReadyReplicasOnDistinctNodes(t *testing.T) {
	output, err := runHAPlacement(t,
		haPlacementPod("agent-0", "Running", "True"),
		haPlacementPod("agent-1", "Running", "True"),
	)
	if err != nil {
		t.Fatalf("distinct-node placement rejected: %v\n%s", err, output)
	}
	if got, want := strings.TrimSpace(output), "ha-placement=ok replicas=2 nodes=2"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestHAPlacementRejectsUnsafePlacement(t *testing.T) {
	for name, pods := range map[string][]string{
		"same node": {
			haPlacementPod("agent-0", "Running", "True"),
			haPlacementPod("agent-0", "Running", "True"),
		},
		"single replica": {
			haPlacementPod("agent-0", "Running", "True"),
		},
		"replica not ready": {
			haPlacementPod("agent-0", "Running", "True"),
			haPlacementPod("agent-1", "Running", "False"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			output, err := runHAPlacement(t, pods...)
			if err == nil {
				t.Fatalf("unsafe placement was accepted: %s", output)
			}
			if strings.Contains(output, "ha-placement=ok") {
				t.Fatalf("unsafe placement emitted success marker: %s", output)
			}
		})
	}
}
