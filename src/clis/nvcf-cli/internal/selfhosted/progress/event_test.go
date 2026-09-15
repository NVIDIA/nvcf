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

package progress

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestEvent_KindIsStable(t *testing.T) {
	cases := []struct {
		name string
		ev   Event
		kind string
	}{
		{"phase-started", PhaseStarted{Num: 1, Name: "preflight"}, "phase_started"},
		{"phase-progress", PhaseProgress{Num: 4, Name: "apply-cp", Resource: "deployments", Done: 9, Total: 14}, "phase_progress"},
		{"phase-completed", PhaseCompleted{Num: 1, Duration: 8 * time.Second}, "phase_completed"},
		{"phase-failed", PhaseFailed{Num: 4, ErrCategory: "helm_apply", ErrMessage: "boom", RetryClass: "backoff", RetryAfterSec: 60}, "phase_failed"},
		{"phase-cancelled", PhaseCancelled{Num: 4, Name: "apply-cp", Reason: "sigint"}, "phase_cancelled"},
		{"waiting", Waiting{Num: 4, Reason: "cassandra-0 PodInitializing"}, "waiting"},
		{"last-progress", LastProgress{Num: 4, Detail: "nvcf-api became available"}, "last_progress"},
		{"final", Final{ClusterID: "abc"}, "final"},
		// M+8.1: status + check event types
		{"snapshot", Snapshot{Cluster: "ncp-local", Verdict: "healthy", ReconcileAgeSec: 30}, "snapshot"},
		{"component-health", ComponentHealth{Name: "sis", Ready: 2, Total: 2, UptimeSec: 1209600, Healthy: true}, "component_health"},
		{"cluster-row", ClusterRow{Name: "ncp-local", GPU: "A100", GPUCount: 4, ActiveDeployments: 1, LastSeenAgeSec: 60, Healthy: true}, "cluster_row"},
		{"recent-event", RecentEvent{AgeSec: 120, Kind: "function-deploy", Status: "ACTIVE", Name: "my-fn"}, "recent_event"},
		{"check-started", CheckStarted{Category: "local-host-tools", ID: "kubectl-on-path"}, "check_started"},
		{"check-completed", CheckCompleted{Category: "local-host-tools", ID: "kubectl-on-path", Passed: true, Severity: "info", Message: "kubectl 1.30.2 on PATH"}, "check_completed"},
		{"category-completed", CategoryCompleted{Category: "local-host-tools", PassedCount: 3, FailedCount: 0, DurationSec: 0.4}, "category_completed"},
		// M+11: down direction events
		{"drain-progress", DrainProgress{Num: 1, Deployment: "my-fn-v3", State: "REMOVING"}, "drain_progress"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, eventKind(tc.kind), tc.ev.kind())
		})
	}
}
