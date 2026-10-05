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

package storage

import (
	"context"
	stderrors "errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func TestPrimaryPVSelector(t *testing.T) {
	// primaryPVSel must carry the primary-PV exists requirement so cleanup
	// lists only model-cache primary PVs. An empty selector would match every
	// PV in the cluster.
	assert.False(t, primaryPVSel.Matches(labels.Set{}),
		"empty label set must not match; selector should require the primary-PV label")
	assert.True(t, primaryPVSel.Matches(labels.Set{primaryPVLabelKey: "true"}),
		"a PV carrying the primary-PV label must match")
}

func TestCleanupModelCaches(t *testing.T) {
	// create the object
	ctx := context.Background()

	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pv",
			Labels: map[string]string{
				types.NCAIDKey:             "random-ncaid",
				types.FunctionIDKey:        "random-fn-id",
				types.FunctionVersionIDKey: "random-fn-versionid",
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              "nvcf-sc",
		},
	}

	// create fake kubernetes client
	k8sClient := fake.NewClientBuilder().
		WithScheme(mgrScheme).
		WithObjects(pv).
		// WithIndex(
		// 	&nvcav1new.StorageRequest{},
		// 	modelCacheHandleFieldPath,
		// 	modelCacheHandleExtractValues,
		// ).
		WithIndex(
			&nvcav1new.StorageRequest{},
			objectNameFieldPath,
			objectNameExtractValues,
		).
		Build()

	// call the reconciler manually as the setup is a mock
	r := &Reconciler{
		Client:       k8sClient,
		nowFunc:      time.Now,
		initStatuses: newInitStatusCache(k8sClient),
		metrics:      newTestMetrics(),
	}

	err := r.cleanupIdleModelCaches(ctx)
	require.NoError(t, err)

	pvCopy := &corev1.PersistentVolume{}

	err = k8sClient.Get(ctx, client.ObjectKeyFromObject(pv), pvCopy)
	require.NoError(t, err)
	assert.Equal(t, pvCopy.Name, "test-pv")

	lastRefTime := time.Now().Add(-2 * time.Hour).Format(primaryPVLastReferencedTimeFormat)
	pv = &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pv",
			Labels: map[string]string{
				types.NCAIDKey:                       "random-ncaid",
				types.FunctionIDKey:                  "random-fn-id",
				types.FunctionVersionIDKey:           "random-fn-versionid",
				primaryPVLabelKey:                    primaryPVLabelValue,
				primaryPVLastReferencedAnnotationKey: lastRefTime,
			},
			Annotations: map[string]string{
				primaryPVLastReferencedAnnotationKey: lastRefTime,
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              "nvcf-sc",
		},
		Status: corev1.PersistentVolumeStatus{
			Phase: corev1.VolumeFailed,
		},
	}

	// create fake kubernetes client
	k8sClient = fake.NewClientBuilder().
		WithScheme(mgrScheme).
		WithObjects(pv).
		// WithIndex(
		// 	&nvcav1new.StorageRequest{},
		// 	modelCacheHandleFieldPath,
		// 	modelCacheHandleExtractValues,
		// ).
		WithIndex(
			&nvcav1new.StorageRequest{},
			objectNameFieldPath,
			objectNameExtractValues,
		).
		Build()

	// call the reconciler manually as the setup is a mock
	r = &Reconciler{
		Client:       k8sClient,
		nowFunc:      time.Now,
		initStatuses: newInitStatusCache(k8sClient),
	}

	err = r.cleanupIdleModelCaches(ctx)
	require.NoError(t, err)

	pvCopy = &corev1.PersistentVolume{}

	err = k8sClient.Get(ctx, client.ObjectKeyFromObject(pv), pvCopy)
	require.True(t, errors.IsNotFound(err))

	pv = &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pv",
			Labels: map[string]string{
				types.NCAIDKey:             "random-ncaid",
				types.FunctionIDKey:        "random-fn-id",
				types.FunctionVersionIDKey: "random-fn-versionid",
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              "nvcf-sc",
		},
		Status: corev1.PersistentVolumeStatus{
			Phase: corev1.VolumeAvailable,
		},
	}

	// create fake kubernetes client
	k8sClient = fake.NewClientBuilder().
		WithScheme(mgrScheme).
		WithObjects(pv).
		// WithIndex(
		// 	&nvcav1new.StorageRequest{},
		// 	modelCacheHandleFieldPath,
		// 	modelCacheHandleExtractValues,
		// ).
		WithIndex(
			&nvcav1new.StorageRequest{},
			objectNameFieldPath,
			objectNameExtractValues,
		).
		Build()

	// call the reconciler manually as the setup is a mock
	r = &Reconciler{
		Client:       k8sClient,
		nowFunc:      time.Now,
		initStatuses: newInitStatusCache(k8sClient),
	}

	err = r.cleanupIdleModelCaches(ctx)
	require.NoError(t, err)

	pvCopy = &corev1.PersistentVolume{}

	err = k8sClient.Get(ctx, client.ObjectKeyFromObject(pv), pvCopy)
	require.NoError(t, err)
	assert.Equal(t, pvCopy.Name, "test-pv")
}

// A primary PV is idle only when no storage request still serves from it.
// Requests record their secondary's volume handle, which is the primary's
// handle with the namespace segment rewritten, so the GC must compare the
// volume identity; on 2026-10-03 a verbatim comparison let it delete the
// primary under a running function.
func TestCleanupIdleModelCaches_KeepsPrimaryBehindActiveSecondary(t *testing.T) {
	const cacheHandle = "797a73eb7557f74f2c3d1d1e7e53e449"
	const primaryHandle = "single-zone-cluster:csi-b0e049fc-bfc0-459c:c55f93e0-bf45-11f1-8ed5-2fd5ee26c3aa:nvca-modelcache-init"
	now := time.Date(2026, 10, 3, 17, 41, 52, 0, time.UTC)
	stale := now.Add(-2 * time.Hour).Format(primaryPVLastReferencedTimeFormat)

	primary := func() *corev1.PersistentVolume {
		return &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "pvc-primary",
				Labels:      map[string]string{primaryPVLabelKey: primaryPVLabelValue, modelCacheHandleLabelKey: cacheHandle},
				Annotations: map[string]string{primaryPVLastReferencedAnnotationKey: stale},
			},
			Spec: corev1.PersistentVolumeSpec{
				PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
				StorageClassName:              "nvcf-sc",
				PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{
					Driver: "nvmesh-csi.excelero.com", VolumeHandle: primaryHandle,
				}},
			},
			Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased},
		}
	}
	request := func(ns, statusHandle, specHandle string) *nvcav1new.StorageRequest {
		st := &nvcav1new.StorageRequest{
			ObjectMeta: metav1.ObjectMeta{Name: nvcav1new.ModelCacheRequest.Name(), Namespace: ns},
			Spec:       nvcav1new.StorageRequestSpec{Type: nvcav1new.ModelCacheRequest, ICMSRequestName: ns},
			Status:     nvcav1new.StorageRequestStatus{Phase: nvcav1new.StorageReady},
		}
		if specHandle != "" {
			st.Spec.ModelCache = &nvcav1new.ModelCacheSpec{CacheHandle: specHandle}
		}
		if statusHandle != "" {
			st.Status.ModelCache = &nvcav1new.ModelCacheStatus{ROPVCName: "ro-pvc-" + cacheHandle, VolumeHandle: statusHandle}
		}
		return st
	}
	secondaryHandle, err := updateSecondaryPVVolumeHandle(primaryHandle, "sr-de993f31")
	require.NoError(t, err)
	require.NotEqual(t, primaryHandle, secondaryHandle, "the secondary's handle carries the function namespace")

	tests := []struct {
		name     string
		requests []client.Object
		wantKept bool
	}{
		{
			name:     "request serving from the secondary keeps the primary",
			requests: []client.Object{request("sr-de993f31", secondaryHandle, "other-handle")},
			wantKept: true,
		},
		{
			name:     "request for the cache handle without a status yet keeps the primary",
			requests: []client.Object{request("sr-new", "", cacheHandle)},
			wantKept: true,
		},
		{
			name: "a request being deleted does not keep it",
			requests: []client.Object{func() *nvcav1new.StorageRequest {
				st := request("sr-gone", secondaryHandle, cacheHandle)
				st.Finalizers = []string{"test/hold"}
				ts := metav1.NewTime(now)
				st.DeletionTimestamp = &ts
				return st
			}()},
			wantKept: false,
		},
		{
			name:     "no request: the idle primary is reclaimed",
			requests: nil,
			wantKept: false,
		},
		{
			name: "a failed request with only the spec handle and no reader does not keep it",
			requests: []client.Object{func() *nvcav1new.StorageRequest {
				st := request("sr-failed", "", cacheHandle)
				st.Status.Phase = nvcav1new.StorageFailed
				return st
			}()},
			wantKept: false,
		},
		{
			name: "a failed request whose reader claim is still bound keeps it",
			requests: []client.Object{
				func() *nvcav1new.StorageRequest {
					st := request("sr-failed-bound", secondaryHandle, cacheHandle)
					st.Status.Phase = nvcav1new.StorageRuntimeError
					return st
				}(),
				&corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{Name: "ro-pvc-" + cacheHandle, Namespace: "sr-failed-bound"},
					Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
				},
			},
			wantKept: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			objs := append([]client.Object{primary()}, tt.requests...)
			c := fake.NewClientBuilder().WithScheme(mgrScheme).WithObjects(objs...).
				WithIndex(&nvcav1new.StorageRequest{}, objectNameFieldPath, objectNameExtractValues).
				Build()
			r := &Reconciler{
				Client:        c,
				nowFunc:       func() time.Time { return now },
				k8sTimeConfig: (&k8sutil.TimeConfig{}).Complete(),
				initStatuses:  newInitStatusCache(c),
				metrics:       newTestMetrics(),
			}
			require.NoError(t, r.cleanupIdleModelCaches(ctx))
			pv := &corev1.PersistentVolume{}
			err := c.Get(ctx, client.ObjectKey{Name: "pvc-primary"}, pv)
			if tt.wantKept {
				require.NoError(t, err, "the primary must survive while a request serves from it")
				assert.Equal(t, corev1.PersistentVolumeReclaimRetain, pv.Spec.PersistentVolumeReclaimPolicy,
					"a kept primary keeps its Retain policy")
				return
			}
			assert.True(t, errors.IsNotFound(err), "an idle primary with no request is reclaimed, got %v", err)
		})
	}
}

func TestVolumeHandleKey(t *testing.T) {
	assert.Equal(t, "c:v:u", volumeHandleKey("c:v:u:nvca-modelcache-init"))
	assert.Equal(t, "c:v:u", volumeHandleKey("c:v:u:sr-de993f31"))
	assert.Equal(t, "plain", volumeHandleKey("plain"))
}

// A missing primary must not tear down a secondary a workload is bound to.
func TestSecondaryInUse(t *testing.T) {
	const cacheHandle = "797a73eb7557f74f2c3d1d1e7e53e449"
	st := &nvcav1new.StorageRequest{
		ObjectMeta: metav1.ObjectMeta{Name: nvcav1new.ModelCacheRequest.Name(), Namespace: "sr-fn"},
		Spec:       nvcav1new.StorageRequestSpec{ModelCache: &nvcav1new.ModelCacheSpec{CacheHandle: cacheHandle}},
	}
	pvc := func(phase corev1.PersistentVolumeClaimPhase, deleting bool) *corev1.PersistentVolumeClaim {
		p := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "ro-pvc-" + cacheHandle, Namespace: "sr-fn"},
			Status:     corev1.PersistentVolumeClaimStatus{Phase: phase},
		}
		if deleting {
			p.Finalizers = []string{"kubernetes.io/pvc-protection"}
			ts := metav1.Now()
			p.DeletionTimestamp = &ts
		}
		return p
	}
	tests := []struct {
		name string
		objs []client.Object
		want bool
	}{
		{name: "no claim", want: false},
		{name: "bound claim", objs: []client.Object{pvc(corev1.ClaimBound, false)}, want: true},
		{name: "pending claim", objs: []client.Object{pvc(corev1.ClaimPending, false)}, want: false},
		{name: "claim being deleted", objs: []client.Object{pvc(corev1.ClaimBound, true)}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(mgrScheme).WithObjects(tt.objs...).Build()
			r := &Reconciler{Client: c}
			got, err := r.secondaryInUse(context.Background(), st)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
	none := &Reconciler{Client: fake.NewClientBuilder().WithScheme(mgrScheme).Build()}
	got, err := none.secondaryInUse(context.Background(), &nvcav1new.StorageRequest{})
	require.NoError(t, err)
	assert.False(t, got, "a request without a cache handle has no secondary")
}

// A missing primary is only tolerated for a Ready request with a bound
// secondary. A Creating request has no reader status yet; preserving it
// would leave the phase at Creating forever, so it fails terminally and the
// caller handles the failure.
func TestPrimaryMissing(t *testing.T) {
	const cacheHandle = "797a73eb7557f74f2c3d1d1e7e53e449"
	request := func(phase nvcav1new.StoragePhase) *nvcav1new.StorageRequest {
		return &nvcav1new.StorageRequest{
			ObjectMeta: metav1.ObjectMeta{Name: nvcav1new.ModelCacheRequest.Name(), Namespace: "sr-fn"},
			Spec:       nvcav1new.StorageRequestSpec{ModelCache: &nvcav1new.ModelCacheSpec{CacheHandle: cacheHandle}},
			Status:     nvcav1new.StorageRequestStatus{Phase: phase},
		}
	}
	boundClaim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "ro-pvc-" + cacheHandle, Namespace: "sr-fn"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
	notFound := errors.NewNotFound(corev1.Resource("persistentvolumes"), "primary-pv")
	tests := []struct {
		name         string
		phase        nvcav1new.StoragePhase
		objs         []client.Object
		wantTerminal bool
	}{
		{name: "ready with a bound secondary is preserved", phase: nvcav1new.StorageReady, objs: []client.Object{boundClaim}, wantTerminal: false},
		{name: "ready without a reader fails", phase: nvcav1new.StorageReady, wantTerminal: true},
		{name: "creating with a bound secondary still fails", phase: nvcav1new.StorageCreating, objs: []client.Object{boundClaim}, wantTerminal: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(mgrScheme).WithObjects(tt.objs...).Build()
			r := &Reconciler{Client: c, eventRecorder: record.NewFakeRecorder(4), metrics: newTestMetrics()}
			st := request(tt.phase)
			err := r.primaryMissing(context.Background(), st, st.DeepCopy(), notFound)
			if tt.wantTerminal {
				require.Error(t, err)
				assert.True(t, stderrors.Is(err, reconcile.TerminalError(nil)), "expected a terminal error, got %v", err)
				return
			}
			require.NoError(t, err)
		})
	}
}
