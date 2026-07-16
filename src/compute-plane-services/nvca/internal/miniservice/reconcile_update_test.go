/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package mscontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/miniservice/chartcache"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/otel"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
)

const (
	updateTestNamespace = "ms-update-ns"
	updateICMSNamespace = "nvcf-backend"
	updateMSName        = "miniservice-update"
	updateICMSName      = "icms-request-update"
)

type countingReValClient struct {
	calls int
	err   error
}

func (c *countingReValClient) Render(_ context.Context, _ HelmReValRenderInput) (HelmReValRenderOutput, error) {
	c.calls++
	if c.err != nil {
		return HelmReValRenderOutput{}, c.err
	}
	return HelmReValRenderOutput{}, nil
}

func newUpdateRenderedData(t *testing.T, name, value string) []byte {
	t.Helper()

	obj := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: corev1.SchemeGroupVersion.String(),
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Data: map[string]string{"key": value},
	}
	rawObj, err := json.Marshal(obj)
	require.NoError(t, err)

	out, err := json.Marshal([]json.RawMessage{rawObj})
	require.NoError(t, err)
	return out
}

func newUpdateMiniService(values string) *v1alpha1.MiniService {
	return &v1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{
			Name:       updateMSName,
			Generation: 2,
			UID:        "ms-update-uid",
		},
		Spec: v1alpha1.MiniServiceSpec{
			Namespace:       updateTestNamespace,
			ICMSRequestName: updateICMSName,
			HelmChartConfig: common.HelmConfig{
				URL:    "https://helm.ngc.nvidia.com/myorg/charts/foo-1.0.0.tgz",
				Values: []byte(values),
			},
		},
		Status: v1alpha1.MiniServiceStatus{
			Phase:              v1alpha1.MiniServiceInstalling,
			Revision:           2,
			ObservedGeneration: 2,
		},
	}
}

func newUpdateICMSRequest(withTaskSpec bool) *nvcav2beta1.ICMSRequest {
	req := &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      updateICMSName,
			Namespace: updateICMSNamespace,
		},
		Spec: nvcav2beta1.ICMSRequestSpec{
			RequestID:      "request-1",
			MessageBatchID: "batch-1",
			NCAId:          "ncaid-1",
			Action:         common.TaskCreationAction,
			TaskDetails: task.Details{
				TaskID: "task-1",
			},
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
					Action:            common.TaskCreationAction,
					RequestID:         "request-1",
					MessageBatchID:    "batch-1",
					NCAID:             "ncaid-1",
					GPUType:           "L40",
					RequestedGPUCount: 1,
					InstanceCount:     1,
					InstanceTypeValue: "ON-PREM.GPU.L40",
				},
			},
		},
	}
	if withTaskSpec {
		req.Spec.CreationMsgInfo.TaskLaunchSpecification = &task.LaunchSpecification{
			EnvironmentB64: encodeEnvsForLaunchSpec([]corev1.EnvVar{
				{Name: common.TaskNameEncodedEnvKey, Value: "my-task"},
			}),
			ICMSEnvironment: "prod",
		}
	}
	return req
}

func newReadyUtilsPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      common.UtilsPodName,
			Namespace: updateTestNamespace,
		},
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{
				{Name: common.InitContainerName},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
				{Type: corev1.PodInitialized, Status: corev1.ConditionTrue},
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
			InitContainerStatuses: []corev1.ContainerStatus{
				{
					Name: common.InitContainerName,
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{ExitCode: 0},
					},
				},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: common.UtilsContainerName,
					State: corev1.ContainerState{
						Running: &corev1.ContainerStateRunning{},
					},
				},
			},
		},
	}
}

func newUpdateTestReconciler(t *testing.T, c client.Client, scheme *runtime.Scheme) *Reconciler {
	t.Helper()

	r := &Reconciler{
		ControllerOptions: ControllerOptions{
			ICMSRequestNamespace: updateICMSNamespace,
			FeatureFlagFetcher:   &featureflagmock.Fetcher{},
			K8sTimeConfig:        (&k8sutil.TimeConfig{}).Complete(),
			Metrics: metrics.NewDefaultMetrics(
				"test-nca-id", "test-cluster", "test-group", "test-version", metrics.WithRegisterer(prometheus.NewRegistry()),
			),
		},
		Client:                c,
		Decoder:               serializer.NewCodecFactory(scheme).UniversalDeserializer(),
		eventRecorder:         record.NewFakeRecorder(20),
		tracer:                otel.NewTracer(),
		chartCache:            chartcache.New(t.TempDir()),
		newPermissionsChecker: newFakePermissionsChecker,
		now:                   time.Now,
	}
	require.NoError(t, r.chartCache.Start(context.Background()))
	return r
}

func TestDoUpdateWorkload_UnwrapsTerminalPreparationError(t *testing.T) {
	ctx := newTestContext()
	testScheme := mgrScheme

	c, _ := newFakeClient(testScheme)
	r := newUpdateTestReconciler(t, c, testScheme)
	ms := newUpdateMiniService(`{"key":"value-v2"}`)
	icmsReq := newUpdateICMSRequest(false)

	gotRes, err := r.doUpdateWorkload(ctx, ms, icmsReq)
	require.Error(t, err)
	assert.Equal(t, reconcile.Result{}, gotRes)
	assert.ErrorContains(t, err, "both function and task launch specs are empty")
	assert.NotContains(t, err.Error(), "terminal error")
	assert.Equal(t, v1alpha1.MiniServiceInstalling, ms.Status.Phase)
}

func TestDoUpdateWorkload_UnwrapsTerminalApplyError(t *testing.T) {
	ctx := newTestContext()
	testScheme := mgrScheme
	const workloadObjectName = "updated-workload-cm"

	patchCalls := 0
	c, _ := newFakeClientWithInterceptors(testScheme,
		interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == workloadObjectName {
					patchCalls++
					return apierrors.NewForbidden(
						schema.GroupResource{Group: "", Resource: "configmaps"},
						cm.Name,
						fmt.Errorf("forbidden by test"),
					)
				}
				return c.Patch(ctx, obj, patch, opts...)
			},
		},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: updateTestNamespace}},
		newReadyUtilsPod(),
	)
	r := newUpdateTestReconciler(t, c, testScheme)
	ms := newUpdateMiniService(`{"key":"value-v2"}`)
	icmsReq := newUpdateICMSRequest(true)
	require.NoError(t, r.saveRenderedData(ctx, ms, newUpdateRenderedData(t, workloadObjectName, "v2")))

	gotRes, err := r.doUpdateWorkload(ctx, ms, icmsReq)
	require.Error(t, err)
	assert.Equal(t, reconcile.Result{}, gotRes)
	assert.ErrorContains(t, err, "is forbidden")
	assert.NotContains(t, err.Error(), "terminal error")
	assert.Equal(t, 1, patchCalls, "expected one SSA patch attempt for workload object")
	assert.Equal(t, v1alpha1.MiniServiceInstalling, ms.Status.Phase)
}

func TestDoUpdateWorkload_ApplyFailureRetainsRenderedCache(t *testing.T) {
	ctx := newTestContext()
	testScheme := mgrScheme
	const workloadObjectName = "updated-workload-cm"

	c, _ := newFakeClientWithInterceptors(testScheme,
		interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == workloadObjectName {
					return apierrors.NewForbidden(
						schema.GroupResource{Group: "", Resource: "configmaps"},
						cm.Name,
						fmt.Errorf("forbidden by test"),
					)
				}
				return c.Patch(ctx, obj, patch, opts...)
			},
		},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: updateTestNamespace}},
		newReadyUtilsPod(),
	)
	r := newUpdateTestReconciler(t, c, testScheme)
	ms := newUpdateMiniService(`{"key":"value-v2"}`)
	icmsReq := newUpdateICMSRequest(true)
	renderedData := newUpdateRenderedData(t, workloadObjectName, "v2")
	require.NoError(t, r.saveRenderedData(ctx, ms, renderedData))
	require.NotNil(t, ms.Status.RenderDetails)
	oldHash := ms.Status.RenderDetails.Hash

	_, err := r.doUpdateWorkload(ctx, ms, icmsReq)
	require.Error(t, err)
	assert.Equal(t, v1alpha1.MiniServiceInstalling, ms.Status.Phase)
	require.NotNil(t, ms.Status.RenderDetails)
	assert.Equal(t, oldHash, ms.Status.RenderDetails.Hash)

	cachedData, found, err := r.getRenderedData(ctx, ms)
	require.NoError(t, err)
	require.True(t, found, "rendered cache should be retained after failed workload apply")
	assert.Equal(t, renderedData, cachedData)
}

func TestReconcile_UpdateFailureStaysInstalling(t *testing.T) {
	ctx := newTestContext()
	testScheme := mgrScheme
	const workloadObjectName = "updated-workload-cm"

	c, _ := newFakeClientWithInterceptors(testScheme,
		interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == workloadObjectName {
					return apierrors.NewForbidden(
						schema.GroupResource{Group: "", Resource: "configmaps"},
						cm.Name,
						fmt.Errorf("forbidden by test"),
					)
				}
				return c.Patch(ctx, obj, patch, opts...)
			},
		},
		newUpdateMiniService(`{"key":"value-v2"}`),
		newUpdateICMSRequest(true),
		newReadyUtilsPod(),
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      workloadObjectName,
				Namespace: updateTestNamespace,
			},
			Data: map[string]string{"key": "existing"},
		},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: updateTestNamespace}},
	)
	r := newUpdateTestReconciler(t, c, testScheme)

	ms := &v1alpha1.MiniService{}
	require.NoError(t, r.Client.Get(ctx, client.ObjectKey{Name: updateMSName}, ms))
	require.NoError(t, r.saveRenderedData(ctx, ms, newUpdateRenderedData(t, workloadObjectName, "v2")))
	require.NoError(t, r.Client.Status().Update(ctx, ms))

	req := reconcile.Request{NamespacedName: client.ObjectKey{Name: updateMSName}}
	gotRes, err := r.Reconcile(ctx, req)
	require.Error(t, err)
	assert.Equal(t, reconcile.Result{}, gotRes)
	assert.ErrorContains(t, err, "is forbidden")
	assert.NotContains(t, err.Error(), "terminal error")

	require.NoError(t, r.Client.Get(ctx, client.ObjectKey{Name: updateMSName}, ms))
	assert.Equal(t, v1alpha1.MiniServiceInstalling, ms.Status.Phase)
	assert.NotEqual(t, v1alpha1.MiniServiceInstallFailed, ms.Status.Phase)
}

func TestReconcile_UpdateFailureThenNewUpdateSucceeds(t *testing.T) {
	ctx := newTestContext()
	testScheme := mgrScheme
	const workloadObjectName = "updated-workload-cm"

	failWorkloadPatch := true
	c, _ := newFakeClientWithInterceptors(testScheme,
		interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == workloadObjectName && failWorkloadPatch {
					return apierrors.NewForbidden(
						schema.GroupResource{Group: "", Resource: "configmaps"},
						cm.Name,
						fmt.Errorf("forbidden by test"),
					)
				}
				return c.Patch(ctx, obj, patch, opts...)
			},
		},
		newUpdateMiniService(`{"key":"value-v2"}`),
		newUpdateICMSRequest(true),
		newReadyUtilsPod(),
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      workloadObjectName,
				Namespace: updateTestNamespace,
			},
			Data: map[string]string{"key": "existing"},
		},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: updateTestNamespace}},
	)
	r := newUpdateTestReconciler(t, c, testScheme)

	ms := &v1alpha1.MiniService{}
	require.NoError(t, r.Client.Get(ctx, client.ObjectKey{Name: updateMSName}, ms))
	oldRenderedData := newUpdateRenderedData(t, workloadObjectName, "v2")
	require.NoError(t, r.saveRenderedData(ctx, ms, oldRenderedData))
	require.NoError(t, r.Client.Status().Update(ctx, ms))

	// First reconcile: update fails on object apply, but should remain in update state.
	req := reconcile.Request{NamespacedName: client.ObjectKey{Name: updateMSName}}
	_, err := r.Reconcile(ctx, req)
	require.Error(t, err)
	assert.ErrorContains(t, err, "is forbidden")

	require.NoError(t, r.Client.Get(ctx, client.ObjectKey{Name: updateMSName}, ms))
	assert.Equal(t, v1alpha1.MiniServiceInstalling, ms.Status.Phase)
	assert.Equal(t, int64(2), ms.Status.Revision)
	assert.Equal(t, int64(2), ms.Status.ObservedGeneration)
	cachedData, found, err := r.getRenderedData(ctx, ms)
	require.NoError(t, err)
	require.True(t, found, "failed update should keep cached rendered data for retries")
	assert.Equal(t, oldRenderedData, cachedData)

	// Remediation: user updates values again (new generation). Reconcile should prepare and complete update.
	ms.Spec.HelmChartConfig.Values = []byte(`{"key":"value-v3-remediation"}`)
	ms.Generation = 3
	require.NoError(t, r.Client.Update(ctx, ms))

	newRenderedData := newUpdateRenderedData(t, workloadObjectName, "v3")
	wantErr := new(error)
	r.ReValClient = newFakeReValClient(t, newRenderedData, wantErr)

	failWorkloadPatch = false
	gotRes, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, gotRes)

	require.NoError(t, r.Client.Get(ctx, client.ObjectKey{Name: updateMSName}, ms))
	assert.Equal(t, v1alpha1.MiniServiceInstalled, ms.Status.Phase)
	assert.Equal(t, int64(3), ms.Status.Revision)
	assert.Equal(t, int64(3), ms.Status.ObservedGeneration)
	newCachedData, found, err := r.getRenderedData(ctx, ms)
	require.NoError(t, err)
	require.True(t, found, "remediation update should cache new rendered data")
	assert.Equal(t, newRenderedData, newCachedData)

	installCond := meta.FindStatusCondition(ms.Status.Conditions, v1alpha1.MiniServiceConditionInstallSuccessful)
	if assert.NotNil(t, installCond) {
		assert.Equal(t, metav1.ConditionTrue, installCond.Status)
		assert.Equal(t, "WorkloadObjectsUpdated", installCond.Reason)
		assert.Equal(t, "Workload update to revision 3 completed successfully", installCond.Message)
	}
}

func TestReconcile_FailedUpdateRetryReusesCache(t *testing.T) {
	ctx := newTestContext()
	testScheme := mgrScheme
	const workloadObjectName = "updated-workload-cm"

	failWorkloadPatch := true
	c, _ := newFakeClientWithInterceptors(testScheme,
		interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == workloadObjectName && failWorkloadPatch {
					return apierrors.NewForbidden(
						schema.GroupResource{Group: "", Resource: "configmaps"},
						cm.Name,
						fmt.Errorf("forbidden by test"),
					)
				}
				return c.Patch(ctx, obj, patch, opts...)
			},
		},
		newUpdateMiniService(`{"key":"value-v2"}`),
		newUpdateICMSRequest(true),
		newReadyUtilsPod(),
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      workloadObjectName,
				Namespace: updateTestNamespace,
			},
			Data: map[string]string{"key": "existing"},
		},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: updateTestNamespace}},
	)
	r := newUpdateTestReconciler(t, c, testScheme)

	ms := &v1alpha1.MiniService{}
	require.NoError(t, r.Client.Get(ctx, client.ObjectKey{Name: updateMSName}, ms))
	require.NoError(t, r.saveRenderedData(ctx, ms, newUpdateRenderedData(t, workloadObjectName, "v2")))
	require.NoError(t, r.Client.Status().Update(ctx, ms))

	req := reconcile.Request{NamespacedName: client.ObjectKey{Name: updateMSName}}
	_, err := r.Reconcile(ctx, req)
	require.Error(t, err)
	assert.ErrorContains(t, err, "is forbidden")

	// Retry same generation: should consume existing cache and not call ReVal.
	reval := &countingReValClient{err: fmt.Errorf("reval must not be called for cached retry")}
	r.ReValClient = reval
	failWorkloadPatch = false

	gotRes, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, gotRes)
	assert.Equal(t, 0, reval.calls, "expected retry to reuse cached render data without ReVal call")
}
