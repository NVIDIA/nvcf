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

package cacheprobe

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func newFakeClient(t *testing.T, objs ...runtime.Object) *fake.ClientBuilder {
	t.Helper()
	sch := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(sch))
	b := fake.NewClientBuilder().WithScheme(sch)
	for _, o := range objs {
		b = b.WithRuntimeObjects(o)
	}
	return b
}

const probeNS = "model-cache-system"

func TestStateStore_SaveLoadGetStrategy(t *testing.T) {
	c := newFakeClient(t).Build()
	store := NewStateStore(c, probeNS)
	sc := "nvcf-miniservice-sc"

	// No ConfigMap yet -> fallback.
	got, err := store.GetStrategy(t.Context(), sc)
	require.NoError(t, err)
	assert.Equal(t, StrategyFallback, got)

	// Persist ROX supported -> GetStrategy returns ROX, round-trips via Load.
	results := map[string]Result{
		ResultKey(sc, StrategyROX): {State: StateSupported, CheckedAt: time.Now(), TTL: 3600},
		ResultKey(sc, StrategyRWX): {State: StateSupported, CheckedAt: time.Now(), TTL: 3600},
	}
	require.NoError(t, store.Save(t.Context(), results))

	loaded, err := store.Load(t.Context())
	require.NoError(t, err)
	assert.Equal(t, StateSupported, loaded[ResultKey(sc, StrategyROX)].State)

	got, err = store.GetStrategy(t.Context(), sc)
	require.NoError(t, err)
	assert.Equal(t, StrategyROX, got, "ROX preferred when both supported")

	// Only RWX supported -> RWX.
	require.NoError(t, store.Save(t.Context(), map[string]Result{
		ResultKey(sc, StrategyROX): {State: StateUnsupported, CheckedAt: time.Now(), TTL: 3600},
		ResultKey(sc, StrategyRWX): {State: StateSupported, CheckedAt: time.Now(), TTL: 3600},
	}))
	got, err = store.GetStrategy(t.Context(), sc)
	require.NoError(t, err)
	assert.Equal(t, StrategyRWX, got)

	// Expired ROX -> fallback.
	require.NoError(t, store.Save(t.Context(), map[string]Result{
		ResultKey(sc, StrategyROX): {State: StateSupported, CheckedAt: time.Now().Add(-2 * time.Hour), TTL: 60},
	}))
	got, err = store.GetStrategy(t.Context(), sc)
	require.NoError(t, err)
	assert.Equal(t, StrategyFallback, got)
}

func TestIsExpired(t *testing.T) {
	assert.False(t, isExpired(Result{TTL: 0, CheckedAt: time.Now().Add(-24 * time.Hour)}), "TTL<=0 never expires")
	assert.False(t, isExpired(Result{TTL: 3600, CheckedAt: time.Now()}))
	assert.True(t, isExpired(Result{TTL: 60, CheckedAt: time.Now().Add(-2 * time.Minute)}))
}

func TestProber_createProbePVC(t *testing.T) {
	c := newFakeClient(t).Build()
	p := NewProber(c, probeNS, "nvcf-miniservice-sc", 3600)

	require.NoError(t, p.createProbePVC(t.Context(), "probe-pvc", corev1.ReadOnlyMany))

	pvc := &corev1.PersistentVolumeClaim{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Name: "probe-pvc", Namespace: probeNS}, pvc))
	assert.Equal(t, []corev1.PersistentVolumeAccessMode{corev1.ReadOnlyMany}, pvc.Spec.AccessModes)
	require.NotNil(t, pvc.Spec.StorageClassName)
	assert.Equal(t, "nvcf-miniservice-sc", *pvc.Spec.StorageClassName)

	// Re-create errors with AlreadyExists: leftovers are deleted before each
	// probe run, so an existing PVC must not be silently reused.
	assert.True(t, apierrors.IsAlreadyExists(p.createProbePVC(t.Context(), "probe-pvc", corev1.ReadOnlyMany)))
}

func TestProber_cleanupProbe(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "probe-pod", Namespace: probeNS}}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "probe-pvc", Namespace: probeNS},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "probe-pv"},
	}
	// Simulates a Retain-reclaim class: the PV outlives the PVC deletion and
	// must be deleted explicitly, or one Released PV accumulates per probe.
	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "probe-pv"}}
	c := newFakeClient(t, pod, pvc, pv).Build()
	p := NewProber(c, probeNS, "nvcf-miniservice-sc", 3600)

	p.cleanupProbe(t.Context(), "probe-pod", "probe-pvc")

	for name, obj := range map[string]client.Object{
		"pod": &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "probe-pod", Namespace: probeNS}},
		"pvc": &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "probe-pvc", Namespace: probeNS}},
		"pv":  &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "probe-pv"}},
	} {
		err := c.Get(t.Context(), client.ObjectKeyFromObject(obj), obj)
		assert.Truef(t, apierrors.IsNotFound(err), "%s must be deleted by cleanupProbe", name)
	}

	// Cleanup with nothing left is a no-op (all NotFound tolerated).
	p.cleanupProbe(t.Context(), "probe-pod", "probe-pvc")
}

func TestProber_waitForPodRunning(t *testing.T) {
	running := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "running", Namespace: probeNS},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	failed := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "failed", Namespace: probeNS},
		Status:     corev1.PodStatus{Phase: corev1.PodFailed, Message: "boom"},
	}
	c := newFakeClient(t, running, failed).Build()
	p := NewProber(c, probeNS, "nvcf-miniservice-sc", 3600)

	assert.NoError(t, p.waitForPodRunning(t.Context(), "running"))
	assert.Error(t, p.waitForPodRunning(t.Context(), "failed"))
}

func TestStateStore_HasFreshResult(t *testing.T) {
	c := newFakeClient(t).Build()
	store := NewStateStore(c, probeNS)
	sc := "nvcf-miniservice-sc"

	// No results yet -> not fresh (caller should probe).
	fresh, err := store.HasFreshResult(t.Context(), sc)
	require.NoError(t, err)
	assert.False(t, fresh)

	// A fresh negative (Unsupported) result is still "fresh": GetStrategy
	// returns Fallback, but the class should not be re-probed until TTL elapses.
	require.NoError(t, store.Save(t.Context(), map[string]Result{
		ResultKey(sc, StrategyROX): {State: StateUnsupported, CheckedAt: time.Now(), TTL: 3600},
		ResultKey(sc, StrategyRWX): {State: StateUnsupported, CheckedAt: time.Now(), TTL: 3600},
	}))
	got, err := store.GetStrategy(t.Context(), sc)
	require.NoError(t, err)
	assert.Equal(t, StrategyFallback, got)
	fresh, err = store.HasFreshResult(t.Context(), sc)
	require.NoError(t, err)
	assert.True(t, fresh, "fresh negative result must be honoured (no re-probe)")

	// Once the negative result expires, it is no longer fresh -> re-probe.
	require.NoError(t, store.Save(t.Context(), map[string]Result{
		ResultKey(sc, StrategyROX): {State: StateUnsupported, CheckedAt: time.Now().Add(-2 * time.Hour), TTL: 60},
		ResultKey(sc, StrategyRWX): {State: StateUnsupported, CheckedAt: time.Now().Add(-2 * time.Hour), TTL: 60},
	}))
	fresh, err = store.HasFreshResult(t.Context(), sc)
	require.NoError(t, err)
	assert.False(t, fresh, "expired result must not be considered fresh")
}

// TestProbeAccessMode_UnsupportedTTLCapped guards the negative-result TTL cap:
// a failed probe may be a transient environment problem, so it must not carry
// the full positive TTL and suppress re-probing (which would cascade terminal
// failures onto every storage request for that window).
func TestProbeAccessMode_UnsupportedTTLCapped(t *testing.T) {
	c := newFakeClient(t).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
			return errors.New("apiserver timeout")
		},
	}).Build()
	p := NewProber(c, probeNS, "some-sc", 3600)

	res := p.ProbeAccessMode(context.Background(), corev1.ReadOnlyMany)
	assert.Equal(t, StateUnsupported, res.State)
	assert.Equal(t, UnsupportedResultTTLSeconds, res.TTL,
		"negative results must carry the capped TTL, not the positive TTL")

	// A prober configured with a TTL below the cap keeps its own (shorter) TTL.
	pShort := NewProber(c, probeNS, "some-sc", 60)
	res = pShort.ProbeAccessMode(context.Background(), corev1.ReadOnlyMany)
	assert.Equal(t, 60, res.TTL)
}
