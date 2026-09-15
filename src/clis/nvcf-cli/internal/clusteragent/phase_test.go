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

package clusteragent

import "testing"

func TestDerivePhase(t *testing.T) {
	tests := []struct {
		name        string
		status      string
		action      string
		drain       bool
		terminating bool
		want        Phase
	}{
		{name: "pending is deploying", status: statusPending, want: PhaseDeploying},
		{name: "in progress is deploying", status: statusInProgress, want: PhaseDeploying},
		{name: "caching is deploying", status: statusCachingInProgress, want: PhaseDeploying},
		{name: "instances in progress is deploying", status: statusInstancesInProgress, want: PhaseDeploying},
		{name: "completed is active", status: statusCompleted, want: PhaseActive},
		{name: "completion acknowledged is active", status: statusCompletionAcknowledged, want: PhaseActive},
		{name: "failed is failed", status: statusFailed, want: PhaseFailed},
		{name: "failure acknowledged is failed", status: statusFailureAcknowledged, want: PhaseFailed},

		{name: "active cluster draining", status: statusCompleted, drain: true, want: PhaseDraining},
		{name: "active termination action", status: statusCompleted, action: actionTermination, want: PhaseDraining},
		{name: "active instances terminating", status: statusCompleted, terminating: true, want: PhaseDraining},
		{name: "deploying while draining", status: statusPending, drain: true, want: PhaseDraining},

		{name: "failed takes precedence over drain", status: statusFailed, drain: true, want: PhaseFailed},
		{name: "failed takes precedence over termination", status: statusFailed, action: actionTermination, want: PhaseFailed},

		{name: "unknown status falls back to deploying", status: "RequestSomethingNew", want: PhaseDeploying},
		{name: "empty status falls back to deploying", status: "", want: PhaseDeploying},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DerivePhase(tt.status, tt.action, tt.drain, tt.terminating)
			if got != tt.want {
				t.Errorf("DerivePhase(%q, %q, drain=%v, terminating=%v) = %q, want %q",
					tt.status, tt.action, tt.drain, tt.terminating, got, tt.want)
			}
		})
	}
}
