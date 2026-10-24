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

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

type fakeConnState struct{ closed bool }

func (f *fakeConnState) ConnectionClosed() bool { return f.closed }

// TestQueueLivenessIgnoresPollFailuresWhileConnected is the point of splitting
// the liveness view from the readiness view. A queue outage with the client
// still reconnecting must not restart the pod: the restart discards an
// in-progress reconnect, and because every NVCA shares the queue it would
// restart the fleet at once. Readiness is what should react here.
func TestQueueLivenessIgnoresPollFailuresWhileConnected(t *testing.T) {
	qm := newReadinessTestQueueManager(t)
	conn := &fakeConnState{closed: false}
	live := NewQueueLiveness(qm, conn)

	qm.setPollResult(false)

	assert.False(t, qm.StatusOK(), "polling is failing")
	assert.Equal(t, types.HealthStatusUnhealthy, queueComponent(t, qm).Status,
		"readiness must react to the failing polls")
	assert.True(t, live.StatusOK(),
		"liveness must not restart the pod while the connection is still up")
}

// TestQueueLivenessFailsWhenConnectionClosed covers the one queue condition a
// restart actually fixes: nats.go never reopens a closed connection.
func TestQueueLivenessFailsWhenConnectionClosed(t *testing.T) {
	qm := newReadinessTestQueueManager(t)
	conn := &fakeConnState{closed: true}
	live := NewQueueLiveness(qm, conn)

	// Healthy polls, closed connection. Liveness must still fail, so the signal
	// is genuinely the connection and not a proxy for poll results.
	qm.setPollResult(true)

	assert.True(t, qm.StatusOK())
	assert.False(t, live.StatusOK(),
		"a permanently closed connection must fail liveness")
}

// TestQueueLivenessFallsBackWithoutConnectionState keeps SQS on its existing
// poll-based behaviour. SQS has no persistent connection, so scoping liveness
// to connection state would otherwise remove its restart path silently.
func TestQueueLivenessFallsBackWithoutConnectionState(t *testing.T) {
	qm := newReadinessTestQueueManager(t)
	live := NewQueueLiveness(qm, nil)

	qm.setPollResult(true)
	assert.True(t, live.StatusOK())

	qm.setPollResult(false)
	assert.False(t, live.StatusOK(),
		"without connection state, liveness keeps following poll results")
}

// TestQueueLivenessNameMatchesQueueManager keeps the probe's failure log
// pointing at the same component name as before the split.
func TestQueueLivenessNameMatchesQueueManager(t *testing.T) {
	qm := newReadinessTestQueueManager(t)
	assert.Equal(t, qm.Name(), NewQueueLiveness(qm, nil).Name())
}
