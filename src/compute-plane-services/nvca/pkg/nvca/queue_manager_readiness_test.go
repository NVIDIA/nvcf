/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package nvca

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// newReadinessTestQueueManager builds the smallest QueueManager that exercises
// the readiness component. Only CreationQueues has to be non-nil; the
// constructor panics otherwise.
func newReadinessTestQueueManager(t *testing.T) *QueueManager {
	t.Helper()
	ctx := newTestContext()
	return NewQueueManager(
		nil, nil, nil,
		types.QueueCredentials{
			CreationQueues: types.CreationQueueInfoSet{
				"H100": queue.MessageQueueInfo{QueueURL: "queue://creation/h100"},
			},
		},
		featureflag.DefaultFetcher,
		types.MaintenanceModeNone,
		nvcametrics.FromContext(ctx),
	)
}

func queueComponent(t *testing.T, qm *QueueManager) types.ComponentHealth {
	t.Helper()
	ah, err := qm.GetComponentStatus(newTestContext())
	require.NoError(t, err)
	ch, ok := ah.Components[queueComponentName]
	require.True(t, ok, "queue component missing from health payload")
	return ch
}

// TestQueueReadinessUnhealthyBeforeFirstPoll is the regression guard for
// nvcf#1590. The constructor marks the manager healthy for liveness before any
// poll has happened, so readiness must not be derived from that: a backend must
// not advertise itself as ready while nothing has consumed the creation queue.
func TestQueueReadinessUnhealthyBeforeFirstPoll(t *testing.T) {
	qm := newReadinessTestQueueManager(t)

	// The liveness signal is optimistic by construction; that is deliberate and
	// unchanged, and is exactly why it cannot double as a readiness signal.
	assert.True(t, qm.StatusOK(), "liveness should stay optimistic at construction")
	assert.False(t, qm.polled.Load(), "no poll has happened yet")

	ch := queueComponent(t, qm)
	assert.Equal(t, types.HealthStatusUnhealthy, ch.Status,
		"readiness must report unhealthy before the first poll")
	assert.Equal(t, types.StatusLevelError, ch.StatusLevel,
		"must be Error level or the readiness aggregate ignores it")
	assert.NotEmpty(t, ch.Errors, "should explain why it is not ready")
}

// TestQueueReadinessHealthyAfterSuccessfulPoll covers the normal path: once a
// poll has completed without a pull error, the queue is genuinely consumable.
func TestQueueReadinessHealthyAfterSuccessfulPoll(t *testing.T) {
	qm := newReadinessTestQueueManager(t)

	qm.setPollResult(true)

	assert.True(t, qm.polled.Load())
	ch := queueComponent(t, qm)
	assert.Equal(t, types.HealthStatusHealthy, ch.Status)
	assert.Empty(t, ch.Errors)
}

// TestQueueReadinessUnhealthyWhilePollsFail is the observed nvcf#1590 scenario:
// the stream or consumer cannot be reached, so every poll fails. Reported
// health must follow, rather than staying healthy indefinitely.
func TestQueueReadinessUnhealthyWhilePollsFail(t *testing.T) {
	qm := newReadinessTestQueueManager(t)

	// First poll succeeds, so the backend legitimately becomes ready.
	qm.setPollResult(true)
	require.Equal(t, types.HealthStatusHealthy, queueComponent(t, qm).Status)

	// Then polling starts failing, as when the creation stream is deleted.
	qm.setPollResult(false)

	ch := queueComponent(t, qm)
	assert.Equal(t, types.HealthStatusUnhealthy, ch.Status,
		"sustained poll failure must not keep reporting healthy")
	assert.Equal(t, types.StatusLevelError, ch.StatusLevel)
	assert.NotEmpty(t, ch.Errors)
}

// TestQueueReadinessTracksPollResultTransitions guards the lockstep between the
// liveness flag and the poll record, since readiness reads both.
func TestQueueReadinessTracksPollResultTransitions(t *testing.T) {
	qm := newReadinessTestQueueManager(t)

	for _, tc := range []struct {
		name string
		ok   bool
		want types.HealthStatus
	}{
		{"failed poll", false, types.HealthStatusUnhealthy},
		{"recovered", true, types.HealthStatusHealthy},
		{"failed again", false, types.HealthStatusUnhealthy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			qm.setPollResult(tc.ok)
			assert.True(t, qm.polled.Load(), "poll record must never revert to unpolled")
			assert.Equal(t, tc.ok, qm.StatusOK())
			assert.Equal(t, tc.want, queueComponent(t, qm).Status)
		})
	}
}
