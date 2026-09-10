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
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	nvcainformers "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/informers/externalversions"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/health"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue"
	mockqueue "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue/mock"
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

// TestQueueReadinessUnhealthyWithNoCreationQueues covers the self-hosted route
// into nvcf#1590. postProcessQueueCredentials synthesises creation queues from
// the GPU-usage snapshot, so an empty snapshot leaves all three creation sets
// empty. SyncQueues then polls only the termination queue, finds no creation
// errors, and records a successful poll — the backend looks ready while having
// no creation consumer at all.
func TestQueueReadinessUnhealthyWithNoCreationQueues(t *testing.T) {
	qm := newReadinessTestQueueManager(t)

	// A poll completes successfully, because the termination queue is fine.
	qm.setPollResult(true)
	require.Equal(t, types.HealthStatusHealthy, queueComponent(t, qm).Status)

	// Credentials arrive with no creation queues, as in self-hosted mode with an
	// empty GPU-usage snapshot. Non-nil maps, matching the real payload.
	qm.updateQueues(types.QueueCredentials{
		CreationQueues:            types.CreationQueueInfoSet{},
		ClusterCreationQueues:     types.CreationQueueInfoSet{},
		TaskClusterCreationQueues: types.CreationQueueInfoSet{},
		TerminationQueue:          queue.MessageQueueInfo{QueueURL: "queue://termination"},
	})
	qm.setPollResult(true)

	ch := queueComponent(t, qm)
	assert.Equal(t, types.HealthStatusUnhealthy, ch.Status,
		"a backend with no creation queue cannot consume work and must not be ready")
	assert.Equal(t, types.StatusLevelError, ch.StatusLevel)
	assert.NotEmpty(t, ch.Errors)
}

// TestQueueReadinessHealthyWhenCreationQueuesConfiguredButSkipped is the guard
// against overcorrecting the case above. mustSkipQueueForGPU skips a GPU
// whenever creationMessagesFetchable is false, and that is false when
// Capacity <= Allocated — the normal state of a busy cluster. Those cycles poll
// no creation queue and must still be ready, or every saturated backend in the
// fleet would go unready and the operator would write agentStatus Unknown.
func TestQueueReadinessHealthyWhenCreationQueuesConfiguredButSkipped(t *testing.T) {
	ctx := newTestContext()

	clients := mockKubeClients()
	bk8s := &BackendK8sCache{
		featureFlagFetcher: featureflag.DefaultFetcher,
		clients:            clients,
		requestsNamespace:  RequestsNamespace,
	}
	bk8s.icmsRequestLister = mustICMSRequestLister(t, nvcainformers.NewSharedInformerFactoryWithOptions(
		clients.BART,
		ResyncInterval,
		nvcainformers.WithNamespace(bk8s.requestsNamespace)))

	// Capacity <= Allocated is what makes creationMessagesFetchable false, and
	// therefore what makes mustSkipQueueForGPU skip every creation queue. This
	// is a saturated cluster, which is a normal healthy state.
	statusGetter := health.NewBackendStatusCache(0, &mockBackendStatusGetter{
		hs: types.AgentHealth{
			Status: types.HealthStatusHealthy,
			GPUUsage: map[types.GPUName]types.GPUResource{
				"H100": {Capacity: 8, Allocated: 8},
			},
		},
	})

	qc := &recordingQueueClient{Client: &mockqueue.Client{Use10MillisForWaits: true}}

	qm := NewQueueManager(bk8s, statusGetter, qc,
		types.QueueCredentials{
			CreationQueues: types.CreationQueueInfoSet{
				"H100": queue.MessageQueueInfo{
					GPU:       "H100",
					QueueURL:  "create-h100",
					QueueType: queue.CreationQueue,
				},
			},
			TerminationQueue: queue.MessageQueueInfo{
				QueueURL:  "term",
				QueueType: queue.TerminationQueue,
			},
		},
		featureflag.DefaultFetcher,
		types.MaintenanceModeNone,
		nvcametrics.FromContext(ctx),
	)

	require.NoError(t, qm.SyncQueues(ctx))

	polled := qc.polledURLs()
	require.Contains(t, polled, "term",
		"the termination queue is always polled, so the cycle did run")
	require.NotContains(t, polled, "create-h100",
		"at capacity, the creation queue must be skipped — if it was polled this "+
			"test is no longer exercising the case it exists for")

	ch := queueComponent(t, qm)
	assert.Equal(t, types.HealthStatusHealthy, ch.Status,
		"a saturated cluster skips its creation queues and is still healthy; "+
			"reporting unready here would take every busy backend in the fleet "+
			"out of service")
	assert.Empty(t, ch.Errors)
}

// recordingQueueClient notes which queues were actually polled, so a test can
// assert that a creation queue was skipped rather than merely that health came
// out healthy.
type recordingQueueClient struct {
	*mockqueue.Client
	mu     sync.Mutex
	polled []string
}

func (c *recordingQueueClient) ReceiveMessage(
	ctx context.Context, in queue.ReceiveMessageInput,
) ([]queue.ReceiveMessageOutput, error) {
	c.mu.Lock()
	c.polled = append(c.polled, in.QueueInfo.QueueURL)
	c.mu.Unlock()
	return c.Client.ReceiveMessage(ctx, in)
}

func (c *recordingQueueClient) polledURLs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.polled)
}

// TestQueueReadinessUnhealthyWhenPollsStopHappening covers the paths where
// SyncQueues returns before it reaches any poll: a failed health refresh, or
// being paused with no credentials. Neither records a poll result, so without a
// staleness clock the component keeps serving its last successful poll while
// nothing is being consumed — nvcf#1590 again, by a different route.
func TestQueueReadinessUnhealthyWhenPollsStopHappening(t *testing.T) {
	qm := newReadinessTestQueueManager(t)

	qm.setPollResult(true)
	require.Equal(t, types.HealthStatusHealthy, queueComponent(t, qm).Status)

	// Still inside the tolerance: a blip must not flap readiness.
	for i := 0; i < maxCyclesWithoutPoll; i++ {
		qm.cyclesSincePoll.Add(1)
	}
	assert.Equal(t, types.HealthStatusHealthy, queueComponent(t, qm).Status,
		"a brief run of early returns must be tolerated")

	// Sustained: SyncQueues keeps running and keeps not polling.
	qm.cyclesSincePoll.Add(1)
	ch := queueComponent(t, qm)
	assert.Equal(t, types.HealthStatusUnhealthy, ch.Status,
		"polling having stopped must surface on readiness")
	assert.Equal(t, types.StatusLevelError, ch.StatusLevel)
	assert.NotEmpty(t, ch.Errors)

	// A poll completing clears it without needing a restart.
	qm.setPollResult(true)
	assert.Equal(t, types.HealthStatusHealthy, queueComponent(t, qm).Status,
		"a completed poll must clear the stall")
}

// TestQueueReadinessSurvivesAnyConfiguredSyncInterval guards the staleness
// tolerance against SyncQueueInterval, which is operator-configurable with no
// upper bound and defaults to 3s.
//
// The ordering matters and is what an earlier wall-clock version of this got
// wrong: readiness is refreshed at the top of SyncQueues, before that cycle's
// poll updates the record, so the value readiness sees is always at least one
// full interval old. Any fixed duration shorter than the configured interval
// therefore reads stale on every cycle and pins a healthy backend at 503
// forever. Counting cycles is correct at every interval, so this loop models the
// real order of operations and asserts it never goes unready.
func TestQueueReadinessSurvivesAnyConfiguredSyncInterval(t *testing.T) {
	qm := newReadinessTestQueueManager(t)

	// The first cycle's poll. Before it, unhealthy is correct and is the whole
	// point of the fix, so steady state starts here.
	qm.cyclesSincePoll.Add(1)
	qm.setPollResult(true)

	for cycle := 0; cycle < 3*maxCyclesWithoutPoll; cycle++ {
		// SyncQueues entry.
		qm.cyclesSincePoll.Add(1)
		// The health refresh at the top of SyncQueues, which publishes readiness.
		require.Equal(t, types.HealthStatusHealthy, queueComponent(t, qm).Status,
			"cycle %d: a backend polling successfully every cycle must stay ready, "+
				"however long the configured interval is", cycle)
		// The poll itself completes.
		qm.setPollResult(true)
	}
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

// erroringComponentGetter fails its gather, which makes
// BackendStatusCache.RefreshStatus return an error and so makes SyncQueues
// return before it reaches any poll.
type erroringComponentGetter struct{}

func (erroringComponentGetter) GetComponentStatus(context.Context) (types.AgentHealth, error) {
	return types.AgentHealth{}, errors.New("health gather failed")
}

// TestQueueReadinessStalenessIsWiredIntoRealSyncQueues drives the real
// SyncQueues rather than poking the counter, so the production increment is
// under test and not just the comparison.
//
// The other two staleness tests manipulate cyclesSincePoll directly, which means
// deleting the increment from SyncQueues leaves them green — the fix would be
// silently disabled with every readiness test passing. This is the one that
// fails in that case.
//
// The failed health refresh is a genuine early-return path: SyncQueues refreshes
// health before polling anything and returns the error, so nothing consumes the
// queue while the last successful poll keeps being served.
func TestQueueReadinessStalenessIsWiredIntoRealSyncQueues(t *testing.T) {
	ctx := newTestContext()

	qm := NewQueueManager(
		nil,
		health.NewBackendStatusCache(0, erroringComponentGetter{}),
		nil,
		types.QueueCredentials{
			CreationQueues: types.CreationQueueInfoSet{
				"H100": queue.MessageQueueInfo{QueueURL: "queue://creation/h100"},
			},
		},
		featureflag.DefaultFetcher,
		types.MaintenanceModeNone,
		nvcametrics.FromContext(ctx),
	)

	// The healthy baseline a running backend would have.
	qm.setPollResult(true)
	require.Equal(t, types.HealthStatusHealthy, queueComponent(t, qm).Status)

	// Inside the tolerance a run of early returns must not flap readiness.
	for cycle := 1; cycle <= maxCyclesWithoutPoll; cycle++ {
		require.Error(t, qm.SyncQueues(ctx),
			"cycle %d: the refresh must fail, or this is not the early-return path", cycle)
		require.Equal(t, types.HealthStatusHealthy, queueComponent(t, qm).Status,
			"cycle %d is still within the tolerance", cycle)
	}

	// Sustained: SyncQueues keeps running and keeps never polling.
	require.Error(t, qm.SyncQueues(ctx))

	ch := queueComponent(t, qm)
	assert.Equal(t, types.HealthStatusUnhealthy, ch.Status,
		"a queue nothing has polled for %d cycles must stop advertising ready",
		maxCyclesWithoutPoll)
	require.NotEmpty(t, ch.Errors)
	assert.Contains(t, ch.Errors[0], "sync cycles")

	// And it recovers once polling resumes, so this cannot latch a backend off.
	qm.setPollResult(true)
	assert.Equal(t, types.HealthStatusHealthy, queueComponent(t, qm).Status,
		"readiness must recover when polling resumes")
}
