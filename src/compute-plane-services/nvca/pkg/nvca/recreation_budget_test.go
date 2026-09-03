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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
)

const (
	testRecreationBudgetNamespace = "nvca-system"
	testFunctionID                = "func-1"
	testFunctionVersionID         = "version-1"
)

func newTestRecreationBudgetBackend(client *fake.Clientset) K8sComputeBackend {
	return K8sComputeBackend{
		clients: &kubeclients.KubeClients{K8s: client},
		bk8s:    &BackendK8sCache{systemNamespace: testRecreationBudgetNamespace},
	}
}

func Test_tryReserveRecreationSlot_firstReservation(t *testing.T) {
	c := newTestRecreationBudgetBackend(fake.NewSimpleClientset())

	allowed, _, err := c.tryReserveRecreationSlot(context.Background(), testFunctionID, testFunctionVersionID)
	require.NoError(t, err)
	assert.True(t, allowed)

	cm, err := c.clients.K8s.CoreV1().ConfigMaps(testRecreationBudgetNamespace).
		Get(context.Background(), recreationBudgetConfigMapName(testFunctionID, testFunctionVersionID), metav1.GetOptions{})
	require.NoError(t, err)
	assert.Len(t, recentPurgeTimestamps(cm.Data[recreationBudgetTimestampsKey]), 1)
}

func Test_tryReserveRecreationSlot_maxPurgesEnforced(t *testing.T) {
	c := newTestRecreationBudgetBackend(fake.NewSimpleClientset())
	ctx := context.Background()

	for i := 0; i < recreationBudgetMaxPurges; i++ {
		allowed, _, err := c.tryReserveRecreationSlot(ctx, testFunctionID, testFunctionVersionID)
		require.NoError(t, err)
		assert.Truef(t, allowed, "purge %d should be allowed", i+1)
	}

	allowed, _, err := c.tryReserveRecreationSlot(ctx, testFunctionID, testFunctionVersionID)
	require.NoError(t, err)
	assert.False(t, allowed, "purge beyond the budget should be denied")
}

func Test_tryReserveRecreationSlot_windowExpiry(t *testing.T) {
	name := recreationBudgetConfigMapName(testFunctionID, testFunctionVersionID)
	expired := formatPurgeTimestamps([]time.Time{
		time.Now().Add(-recreationBudgetWindow - time.Minute),
		time.Now().Add(-recreationBudgetWindow - 2*time.Minute),
		time.Now().Add(-recreationBudgetWindow - 3*time.Minute),
	})
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testRecreationBudgetNamespace},
		Data:       map[string]string{recreationBudgetTimestampsKey: expired},
	}
	c := newTestRecreationBudgetBackend(fake.NewSimpleClientset(cm))

	allowed, _, err := c.tryReserveRecreationSlot(context.Background(), testFunctionID, testFunctionVersionID)
	require.NoError(t, err)
	assert.True(t, allowed, "purges outside the window must not count against the budget")
}

func Test_tryReserveRecreationSlot_retriesOnUpdateConflict(t *testing.T) {
	name := recreationBudgetConfigMapName(testFunctionID, testFunctionVersionID)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testRecreationBudgetNamespace},
	}
	client := fake.NewSimpleClientset(cm)

	conflictOnce := true
	client.PrependReactor("update", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if conflictOnce {
			conflictOnce = false
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, name, assert.AnError)
		}
		return false, nil, nil
	})

	c := newTestRecreationBudgetBackend(client)
	allowed, _, err := c.tryReserveRecreationSlot(context.Background(), testFunctionID, testFunctionVersionID)
	require.NoError(t, err)
	assert.True(t, allowed)
	assert.False(t, conflictOnce, "the reactor should have fired and been retried past")
}

func Test_tryReserveRecreationSlot_retriesOnCreateAlreadyExists(t *testing.T) {
	name := recreationBudgetConfigMapName(testFunctionID, testFunctionVersionID)
	client := fake.NewSimpleClientset()

	// Simulate a concurrent caller genuinely winning the create race: our
	// first Get sees NotFound, but by the time we Create, a competing
	// writer has already created the ConfigMap with its own data (already
	// at the purge cap). Our Create fails AlreadyExists; Tracker().Add
	// seeds the winner's object directly in the fake backing store,
	// bypassing reactors entirely so this isn't a recursive call back into
	// our own "create" reactor. The retry must re-Get and see that real
	// data -- proving the cap holds after losing the race, not just that
	// the call is retried.
	winner := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testRecreationBudgetNamespace},
		Data: map[string]string{
			recreationBudgetTimestampsKey: formatPurgeTimestamps([]time.Time{time.Now(), time.Now(), time.Now()}),
		},
	}
	raceOnce := true
	client.PrependReactor("create", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if raceOnce {
			raceOnce = false
			require.NoError(t, client.Tracker().Add(winner))
			return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "configmaps"}, name)
		}
		return false, nil, nil
	})

	c := newTestRecreationBudgetBackend(client)
	allowed, _, err := c.tryReserveRecreationSlot(context.Background(), testFunctionID, testFunctionVersionID)
	require.NoError(t, err)
	assert.False(t, allowed, "after losing the create race, the retry must see the winner's already-at-cap data and deny the reservation")
	assert.False(t, raceOnce, "the reactor should have fired")
}

func Test_tryReserveRecreationSlot_returnsErrorWhenBudgetStoreUnreachable(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("get", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInternalError(assert.AnError)
	})

	c := newTestRecreationBudgetBackend(client)
	allowed, _, err := c.tryReserveRecreationSlot(context.Background(), testFunctionID, testFunctionVersionID)
	assert.Error(t, err, "a persistent budget-store error must be returned, not swallowed")
	assert.False(t, allowed, "the caller must fail closed (not purge) when the budget cannot be checked")
}

func Test_releaseRecreationSlot_removesOnlyTheGivenTimestamp(t *testing.T) {
	client := fake.NewSimpleClientset()
	c := newTestRecreationBudgetBackend(client)
	ctx := context.Background()

	allowed1, reservedAt1, err := c.tryReserveRecreationSlot(ctx, testFunctionID, testFunctionVersionID)
	require.NoError(t, err)
	require.True(t, allowed1)
	allowed2, reservedAt2, err := c.tryReserveRecreationSlot(ctx, testFunctionID, testFunctionVersionID)
	require.NoError(t, err)
	require.True(t, allowed2)

	require.NoError(t, c.releaseRecreationSlot(ctx, testFunctionID, testFunctionVersionID, reservedAt1))

	cm, err := client.CoreV1().ConfigMaps(testRecreationBudgetNamespace).
		Get(ctx, recreationBudgetConfigMapName(testFunctionID, testFunctionVersionID), metav1.GetOptions{})
	require.NoError(t, err)
	remaining := recentPurgeTimestamps(cm.Data[recreationBudgetTimestampsKey])
	assert.Len(t, remaining, 1, "only the released timestamp should be removed")
	assert.True(t, remaining[0].Equal(reservedAt2), "the other reservation must be untouched")
}

func Test_releaseRecreationSlot_restoresBudgetForRetry(t *testing.T) {
	client := fake.NewSimpleClientset()
	c := newTestRecreationBudgetBackend(client)
	ctx := context.Background()

	var lastReservedAt time.Time
	for i := 0; i < recreationBudgetMaxPurges; i++ {
		allowed, reservedAt, err := c.tryReserveRecreationSlot(ctx, testFunctionID, testFunctionVersionID)
		require.NoError(t, err)
		require.True(t, allowed)
		lastReservedAt = reservedAt
	}
	allowed, _, err := c.tryReserveRecreationSlot(ctx, testFunctionID, testFunctionVersionID)
	require.NoError(t, err)
	require.False(t, allowed, "budget should be exhausted after max purges")

	// Simulate the last reservation's purge failing (e.g. HelmV2.Delete
	// erroring): release it, exactly like k8scomputebackend_miniservice.go
	// does on a non-NotFound delete failure.
	require.NoError(t, c.releaseRecreationSlot(ctx, testFunctionID, testFunctionVersionID, lastReservedAt))

	allowed, _, err = c.tryReserveRecreationSlot(ctx, testFunctionID, testFunctionVersionID)
	require.NoError(t, err)
	assert.True(t, allowed, "releasing a slot for a purge that never happened must free it for a real retry")
}

func Test_releaseRecreationSlot_noConfigMapIsANoop(t *testing.T) {
	client := fake.NewSimpleClientset()
	c := newTestRecreationBudgetBackend(client)
	assert.NoError(t, c.releaseRecreationSlot(context.Background(), testFunctionID, testFunctionVersionID, time.Now()))
}
