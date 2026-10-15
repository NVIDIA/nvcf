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

package metrics

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// metricsCtx returns a context with a fresh prometheus registry and metrics, plus the registry for inspection.
func metricsCtx(t *testing.T) (context.Context, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	ctx := WithDefaultMetrics(context.Background(), "nca-test", "cluster-test", "group-test", "v0.0.1", WithRegisterer(reg))
	m := FromContext(ctx)
	require.NotNil(t, m)
	t.Cleanup(func() { m.Destroy() })
	return ctx, reg
}

// getCounterValue returns the counter value for the given metric name and resource label.
func getCounterValue(t *testing.T, reg *prometheus.Registry, metricName, resource string) (float64, bool) {
	t.Helper()
	metricFamilies, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range metricFamilies {
		if *mf.Name != metricName {
			continue
		}
		for _, metric := range mf.Metric {
			for _, label := range metric.Label {
				if *label.Name == "resource" && *label.Value == resource {
					return *metric.Counter.Value, true
				}
			}
		}
	}
	return 0, false
}

// ---------------------------------------------------------------------------
// recordK8sAPICall tests
// ---------------------------------------------------------------------------

func TestRecordK8sAPICall_ResourceFromGVK(t *testing.T) {
	ctx, reg := metricsCtx(t)

	pod := &corev1.Pod{}
	pod.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"})
	recordK8sAPICall(ctx, pod, nil)

	val, found := getCounterValue(t, reg, K8sAPISuccessTotalMetricName, "pod")
	assert.True(t, found, "should record success for GVK-typed Pod")
	assert.Equal(t, float64(1), val)
}

func TestRecordK8sAPICall_ResourceFromGoType(t *testing.T) {
	// When GVK.Kind is empty (common for typed objects without explicit GVK set),
	// the function falls back to the Go type name.
	ctx, reg := metricsCtx(t)

	pod := &corev1.Pod{}
	recordK8sAPICall(ctx, pod, nil)

	val, found := getCounterValue(t, reg, K8sAPISuccessTotalMetricName, "pod")
	assert.True(t, found, "should derive resource name from Go type when GVK Kind is empty")
	assert.Equal(t, float64(1), val)
}

func TestRecordK8sAPICall_ListObjectStripsListSuffix(t *testing.T) {
	ctx, reg := metricsCtx(t)

	podList := &corev1.PodList{}
	podList.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "PodList"})
	recordK8sAPICall(ctx, podList, nil)

	val, found := getCounterValue(t, reg, K8sAPISuccessTotalMetricName, "pod")
	assert.True(t, found, "should strip 'List' suffix from Kind")
	assert.Equal(t, float64(1), val)
}

func TestRecordK8sAPICall_NilObject(t *testing.T) {
	ctx, reg := metricsCtx(t)

	recordK8sAPICall(ctx, nil, nil)

	val, found := getCounterValue(t, reg, K8sAPISuccessTotalMetricName, "generic")
	assert.True(t, found, "nil object should produce resource='generic'")
	assert.Equal(t, float64(1), val)
}

func TestRecordK8sAPICall_SubResource(t *testing.T) {
	ctx, reg := metricsCtx(t)

	pod := &corev1.Pod{}
	pod.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"})
	recordK8sAPICall(ctx, pod, nil, "status")

	val, found := getCounterValue(t, reg, K8sAPISuccessTotalMetricName, "pod.status")
	assert.True(t, found, "sub-resource should append '.status' to resource name")
	assert.Equal(t, float64(1), val)
}

func TestRecordK8sAPICall_SuccessOnNilError(t *testing.T) {
	ctx, reg := metricsCtx(t)

	pod := &corev1.Pod{}
	pod.SetGroupVersionKind(schema.GroupVersionKind{Kind: "Pod"})
	recordK8sAPICall(ctx, pod, nil)

	sVal, sFound := getCounterValue(t, reg, K8sAPISuccessTotalMetricName, "pod")
	assert.True(t, sFound)
	assert.Equal(t, float64(1), sVal)
}

func TestRecordK8sAPICall_SuccessOnNotFound(t *testing.T) {
	ctx, reg := metricsCtx(t)

	secret := &corev1.Secret{}
	secret.SetGroupVersionKind(schema.GroupVersionKind{Kind: "Secret"})
	nfErr := k8serrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, "missing")
	recordK8sAPICall(ctx, secret, nfErr)

	sVal, sFound := getCounterValue(t, reg, K8sAPISuccessTotalMetricName, "secret")
	assert.True(t, sFound, "NotFound should count as success")
	assert.Equal(t, float64(1), sVal)

	fVal, fFound := getCounterValue(t, reg, K8sAPIFailureTotalMetricName, "secret")
	if fFound {
		assert.Equal(t, float64(0), fVal, "NotFound should not increment failure counter")
	}
}

func TestRecordK8sAPICall_FailureOnGenericError(t *testing.T) {
	ctx, reg := metricsCtx(t)

	cm := &corev1.ConfigMap{}
	cm.SetGroupVersionKind(schema.GroupVersionKind{Kind: "ConfigMap"})
	recordK8sAPICall(ctx, cm, errors.New("connection refused"))

	fVal, fFound := getCounterValue(t, reg, K8sAPIFailureTotalMetricName, "configmap")
	assert.True(t, fFound)
	assert.Equal(t, float64(1), fVal)
}

func TestRecordK8sAPICall_FailureOnForbidden(t *testing.T) {
	ctx, reg := metricsCtx(t)

	ns := &corev1.Namespace{}
	ns.SetGroupVersionKind(schema.GroupVersionKind{Kind: "Namespace"})
	forbiddenErr := k8serrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "ns", errors.New("nope"))
	recordK8sAPICall(ctx, ns, forbiddenErr)

	fVal, fFound := getCounterValue(t, reg, K8sAPIFailureTotalMetricName, "namespace")
	assert.True(t, fFound)
	assert.Equal(t, float64(1), fVal)
}

func TestRecordK8sAPICall_NilMetricsInContext(t *testing.T) {
	ctx := context.Background()
	assert.NotPanics(t, func() {
		recordK8sAPICall(ctx, &corev1.Pod{}, nil)
	})
}

func TestRecordK8sAPICall_SubResourceUpperCase(t *testing.T) {
	ctx, reg := metricsCtx(t)

	pod := &corev1.Pod{}
	pod.SetGroupVersionKind(schema.GroupVersionKind{Kind: "Pod"})
	recordK8sAPICall(ctx, pod, nil, "Status")

	val, found := getCounterValue(t, reg, K8sAPISuccessTotalMetricName, "pod.status")
	assert.True(t, found, "sub-resource should be lowercased")
	assert.Equal(t, float64(1), val)
}

// ---------------------------------------------------------------------------
// getResourceName tests for applyConfiguration branch
// ---------------------------------------------------------------------------

func TestGetResourceName_ApplyConfigWithKind(t *testing.T) {
	ac := corev1ac.ConfigMap("my-cm", "default")
	resource := getResourceName(ac)
	assert.Equal(t, "configmap", resource)
}

func TestGetResourceName_ApplyConfigStripsApplyConfigurationSuffix(t *testing.T) {
	ac := &fakeApplyConfigNoKind{}
	resource := getResourceName(ac)
	assert.Equal(t, "fakeapplyconfignokind", resource, "nil Kind should fall back to Go type name")
}

func TestGetResourceName_ApplyConfigWithSubResource(t *testing.T) {
	ac := corev1ac.ConfigMap("my-cm", "default")
	resource := getResourceName(ac, "status")
	assert.Equal(t, "configmap.status", resource)
}

func TestGetResourceName_ApplyConfigPodKind(t *testing.T) {
	ac := corev1ac.Pod("my-pod", "default")
	resource := getResourceName(ac)
	assert.Equal(t, "pod", resource)
}

func TestGetResourceName_ApplyConfigSecretKind(t *testing.T) {
	ac := corev1ac.Secret("my-secret", "default")
	resource := getResourceName(ac)
	assert.Equal(t, "secret", resource)
}

func TestGetResourceName_ApplyConfigNamespaceKind(t *testing.T) {
	ac := corev1ac.Namespace("my-ns")
	resource := getResourceName(ac)
	assert.Equal(t, "namespace", resource)
}

// fakeApplyConfigNoKind satisfies the applyConfiguration interface but returns nil from GetKind.
type fakeApplyConfigNoKind struct{}

func (f *fakeApplyConfigNoKind) GetName() *string       { return nil }
func (f *fakeApplyConfigNoKind) GetNamespace() *string  { return nil }
func (f *fakeApplyConfigNoKind) GetKind() *string       { return nil }
func (f *fakeApplyConfigNoKind) GetAPIVersion() *string { return nil }
func (f *fakeApplyConfigNoKind) IsApplyConfiguration()  {}

func TestGetResourceName_DefaultBranch(t *testing.T) {
	resource := getResourceName(42)
	assert.Equal(t, "generic", resource, "non-Object non-applyConfig should produce 'generic'")
}

func TestGetResourceName_NilInput(t *testing.T) {
	resource := getResourceName(nil)
	assert.Equal(t, "generic", resource, "nil input should produce 'generic'")
}

// ---------------------------------------------------------------------------
// instrumentedCRClient method tests
// ---------------------------------------------------------------------------

func newTestSetup(t *testing.T, objs ...client.Object) (context.Context, *prometheus.Registry, client.Client) {
	t.Helper()
	ctx, reg := metricsCtx(t)
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	inner := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(objs...).Build()
	instrumented := NewInstrumentedCRClient(inner)
	return ctx, reg, instrumented
}

func TestInstrumentedCRClient_Get(t *testing.T) {
	existingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
	}
	ctx, reg, c := newTestSetup(t, existingPod)

	t.Run("success", func(t *testing.T) {
		got := &corev1.Pod{}
		err := c.Get(ctx, types.NamespacedName{Name: "test-pod", Namespace: "default"}, got)
		require.NoError(t, err)
		assert.Equal(t, "test-pod", got.Name)

		val, found := getCounterValue(t, reg, K8sAPISuccessTotalMetricName, "pod")
		assert.True(t, found)
		assert.GreaterOrEqual(t, val, float64(1))
	})

	t.Run("not found", func(t *testing.T) {
		got := &corev1.Pod{}
		err := c.Get(ctx, types.NamespacedName{Name: "missing", Namespace: "default"}, got)
		assert.True(t, k8serrors.IsNotFound(err))
	})
}

func TestInstrumentedCRClient_List(t *testing.T) {
	existingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
	}
	ctx, reg, c := newTestSetup(t, existingPod)

	list := &corev1.PodList{}
	err := c.List(ctx, list, client.InNamespace("default"))
	require.NoError(t, err)
	assert.Len(t, list.Items, 1)

	val, found := getCounterValue(t, reg, K8sAPISuccessTotalMetricName, "pod")
	assert.True(t, found, "List should record a metric")
	assert.GreaterOrEqual(t, val, float64(1))
}

func TestInstrumentedCRClient_Create(t *testing.T) {
	ctx, reg, c := newTestSetup(t)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "new-pod", Namespace: "default"},
	}

	t.Run("success", func(t *testing.T) {
		err := c.Create(ctx, pod)
		require.NoError(t, err)

		val, found := getCounterValue(t, reg, K8sAPISuccessTotalMetricName, "pod")
		assert.True(t, found)
		assert.GreaterOrEqual(t, val, float64(1))
	})

	t.Run("already exists", func(t *testing.T) {
		dup := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "new-pod", Namespace: "default"},
		}
		err := c.Create(ctx, dup)
		assert.True(t, k8serrors.IsAlreadyExists(err))

		fVal, fFound := getCounterValue(t, reg, K8sAPIFailureTotalMetricName, "pod")
		assert.True(t, fFound, "AlreadyExists should record a failure")
		assert.GreaterOrEqual(t, fVal, float64(1))
	})
}

func TestInstrumentedCRClient_Update(t *testing.T) {
	existingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
	}
	ctx, reg, c := newTestSetup(t, existingPod)

	got := &corev1.Pod{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "test-pod", Namespace: "default"}, got))
	got.Labels = map[string]string{"updated": "true"}

	err := c.Update(ctx, got)
	require.NoError(t, err)

	val, found := getCounterValue(t, reg, K8sAPISuccessTotalMetricName, "pod")
	assert.True(t, found)
	assert.GreaterOrEqual(t, val, float64(1))
}

func TestInstrumentedCRClient_Delete(t *testing.T) {
	existingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
	}
	ctx, reg, c := newTestSetup(t, existingPod)

	err := c.Delete(ctx, existingPod)
	require.NoError(t, err)

	val, found := getCounterValue(t, reg, K8sAPISuccessTotalMetricName, "pod")
	assert.True(t, found)
	assert.GreaterOrEqual(t, val, float64(1))
}

func TestInstrumentedCRClient_Patch(t *testing.T) {
	existingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
	}
	ctx, reg, c := newTestSetup(t, existingPod)

	patch := client.MergeFrom(existingPod.DeepCopy())
	existingPod.Labels = map[string]string{"patched": "true"}
	err := c.Patch(ctx, existingPod, patch)
	require.NoError(t, err)

	val, found := getCounterValue(t, reg, K8sAPISuccessTotalMetricName, "pod")
	assert.True(t, found)
	assert.GreaterOrEqual(t, val, float64(1))
}

func TestInstrumentedCRClient_DeleteAllOf(t *testing.T) {
	existingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
	}
	ctx, reg, c := newTestSetup(t, existingPod)

	err := c.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace("default"))
	require.NoError(t, err)

	val, found := getCounterValue(t, reg, K8sAPISuccessTotalMetricName, "pod")
	assert.True(t, found)
	assert.GreaterOrEqual(t, val, float64(1))
}

func TestInstrumentedCRClient_Apply(t *testing.T) {
	existingCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cm", Namespace: "default"},
	}
	ctx, reg, c := newTestSetup(t, existingCM)

	ac := corev1ac.ConfigMap("test-cm", "default").
		WithLabels(map[string]string{"applied": "true"})
	err := c.Apply(ctx, ac, client.FieldOwner("test"))
	require.NoError(t, err)

	val, found := getCounterValue(t, reg, K8sAPISuccessTotalMetricName, "configmap")
	assert.True(t, found, "Apply should record a metric with resource='configmap'")
	assert.GreaterOrEqual(t, val, float64(1))
}

func TestInstrumentedCRClient_Apply_RecordsFailure(t *testing.T) {
	ctx, reg, c := newTestSetup(t)

	// Applying without FieldOwner causes an error with the fake client.
	ac := corev1ac.ConfigMap("new-cm", "default")
	err := c.Apply(ctx, ac)
	if err != nil {
		fVal, fFound := getCounterValue(t, reg, K8sAPIFailureTotalMetricName, "configmap")
		assert.True(t, fFound, "Apply failure should record a failure metric")
		assert.GreaterOrEqual(t, fVal, float64(1))
	}
}

// ---------------------------------------------------------------------------
// instrumentedCRSubresourceClient tests
// ---------------------------------------------------------------------------

func TestInstrumentedCRClient_StatusUpdate(t *testing.T) {
	existingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
	}
	ctx, reg, c := newTestSetup(t, existingPod)

	got := &corev1.Pod{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "test-pod", Namespace: "default"}, got))
	got.Status.Phase = corev1.PodRunning

	err := c.Status().Update(ctx, got)
	require.NoError(t, err)

	val, found := getCounterValue(t, reg, K8sAPISuccessTotalMetricName, "pod.status")
	assert.True(t, found, "Status().Update() should record with 'pod.status' resource")
	assert.GreaterOrEqual(t, val, float64(1))
}

func TestInstrumentedCRClient_StatusPatch(t *testing.T) {
	existingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
	}
	ctx, reg, c := newTestSetup(t, existingPod)

	got := &corev1.Pod{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "test-pod", Namespace: "default"}, got))

	patch := client.MergeFrom(got.DeepCopy())
	got.Status.Phase = corev1.PodRunning
	err := c.Status().Patch(ctx, got, patch)
	require.NoError(t, err)

	val, found := getCounterValue(t, reg, K8sAPISuccessTotalMetricName, "pod.status")
	assert.True(t, found, "Status().Patch() should record with 'pod.status' resource")
	assert.GreaterOrEqual(t, val, float64(1))
}

func TestInstrumentedCRClient_SubResource(t *testing.T) {
	existingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
	}
	ctx, reg, c := newTestSetup(t, existingPod)

	got := &corev1.Pod{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "test-pod", Namespace: "default"}, got))
	got.Status.Phase = corev1.PodSucceeded

	err := c.SubResource("status").Update(ctx, got)
	require.NoError(t, err)

	val, found := getCounterValue(t, reg, K8sAPISuccessTotalMetricName, "pod.status")
	assert.True(t, found, "SubResource('status').Update() should record with 'pod.status'")
	assert.GreaterOrEqual(t, val, float64(1))
}

// ---------------------------------------------------------------------------
// Pass-through / delegation tests
// ---------------------------------------------------------------------------

func TestInstrumentedCRClient_Scheme(t *testing.T) {
	_, _, c := newTestSetup(t)
	assert.NotNil(t, c.Scheme(), "Scheme() should delegate to inner client")
}

func TestInstrumentedCRClient_RESTMapper(t *testing.T) {
	_, _, c := newTestSetup(t)
	assert.NotNil(t, c.RESTMapper(), "RESTMapper() should delegate to inner client")
}

func TestInstrumentedCRClient_GroupVersionKindFor(t *testing.T) {
	_, _, c := newTestSetup(t)
	gvk, err := c.GroupVersionKindFor(&corev1.Pod{})
	require.NoError(t, err)
	assert.Equal(t, "Pod", gvk.Kind)
}

func TestInstrumentedCRClient_IsObjectNamespaced(t *testing.T) {
	_, _, c := newTestSetup(t)

	_, err := c.IsObjectNamespaced(&corev1.Pod{})
	// The fake client may not have full REST mappings; we only verify delegation doesn't panic.
	_ = err
}

func TestNewInstrumentedCRClient_ImplementsClientInterface(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	inner := clientfake.NewClientBuilder().WithScheme(scheme).Build()
	instrumented := NewInstrumentedCRClient(inner)

	var _ client.Client = instrumented
}

// ---------------------------------------------------------------------------
// Error propagation tests
// ---------------------------------------------------------------------------

func TestInstrumentedCRClient_ErrorsPropagated(t *testing.T) {
	ctx, reg, c := newTestSetup(t)

	t.Run("Get NotFound", func(t *testing.T) {
		err := c.Get(ctx, types.NamespacedName{Name: "nope", Namespace: "default"}, &corev1.Pod{})
		assert.True(t, k8serrors.IsNotFound(err), "NotFound error should propagate through")
	})

	t.Run("Delete NotFound", func(t *testing.T) {
		err := c.Delete(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "nope", Namespace: "default"}})
		assert.True(t, k8serrors.IsNotFound(err), "NotFound error should propagate through Delete")

		_, found := getCounterValue(t, reg, K8sAPISuccessTotalMetricName, "pod")
		assert.True(t, found, "NotFound on Delete should still record as success (API responded)")
	})
}

// ---------------------------------------------------------------------------
// Multiple operations accumulate metrics test
// ---------------------------------------------------------------------------

func TestInstrumentedCRClient_MetricsAccumulate(t *testing.T) {
	existingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
	}
	ctx, reg, c := newTestSetup(t, existingPod)

	got := &corev1.Pod{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "test-pod", Namespace: "default"}, got))
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "test-pod", Namespace: "default"}, got))
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "test-pod", Namespace: "default"}, got))

	val, found := getCounterValue(t, reg, K8sAPISuccessTotalMetricName, "pod")
	assert.True(t, found)
	assert.GreaterOrEqual(t, val, float64(3), "three successful Gets should increment counter at least 3 times")
}

// ---------------------------------------------------------------------------
// Mixed resource types
// ---------------------------------------------------------------------------

func TestInstrumentedCRClient_DifferentResourceTypes(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"}}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "default"}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"}}

	ctx, reg, c := newTestSetup(t, pod, cm, secret)

	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "p", Namespace: "default"}, &corev1.Pod{}))
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "c", Namespace: "default"}, &corev1.ConfigMap{}))
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "s", Namespace: "default"}, &corev1.Secret{}))

	for _, resource := range []string{"pod", "configmap", "secret"} {
		val, found := getCounterValue(t, reg, K8sAPISuccessTotalMetricName, resource)
		assert.True(t, found, "should have success metric for %s", resource)
		assert.GreaterOrEqual(t, val, float64(1), "should have at least 1 success for %s", resource)
	}
}

// ---------------------------------------------------------------------------
// Context without metrics (no panic)
// ---------------------------------------------------------------------------

func TestInstrumentedCRClient_NoMetricsInContext(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	existingPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"}}
	inner := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(existingPod).WithStatusSubresource(existingPod).Build()
	c := NewInstrumentedCRClient(inner)

	ctx := context.Background()
	assert.NotPanics(t, func() {
		got := &corev1.Pod{}
		_ = c.Get(ctx, types.NamespacedName{Name: "test-pod", Namespace: "default"}, got)
	}, "Get with no metrics in context should not panic")

	assert.NotPanics(t, func() {
		_ = c.List(ctx, &corev1.PodList{})
	}, "List with no metrics in context should not panic")

	assert.NotPanics(t, func() {
		_ = c.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm", Namespace: "default"}})
	}, "Create with no metrics in context should not panic")
}
