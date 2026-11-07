// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package mscontroller

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1 "github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce"
	nvcatypes "github.com/dsx-ai-factory/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// deploymentWithEnv returns a Deployment whose only container has the given env.
func deploymentWithEnv(env ...corev1.EnvVar) *appsv1.Deployment {
	return &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: "worker"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main", Env: env}},
		}}},
	}
}

// dynamoGraphDeployment mimics the shape of a DynamoGraphDeployment, a CRD NVCA does not
// know. The model cache path is set in a nested service env list.
func dynamoGraphDeployment(env ...any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "nvidia.com/v1alpha1",
		"kind":       "DynamoGraphDeployment",
		"metadata":   map[string]any{"name": "myllm"},
		"spec": map[string]any{
			"pvcs": []any{map[string]any{"name": "model-cache", "create": true}},
			"services": map[string]any{
				"VllmPrefillWorker": map[string]any{
					"envs": env,
					"volumeMounts": []any{
						map[string]any{"name": "model-cache", "mountPoint": "/opt/models"},
					},
				},
			},
		},
	}}
}

// TestWorkloadSetsModelCachePath checks which rendered workloads count as choosing their own model cache path.
func TestWorkloadSetsModelCachePath(t *testing.T) {
	tests := []struct {
		name string
		objs []client.Object
		want bool
	}{
		{name: "no objects", want: false},
		{
			name: "HF_HOME on a typed workload",
			objs: []client.Object{deploymentWithEnv(corev1.EnvVar{Name: "HF_HOME", Value: "/opt/models"})},
			want: true,
		},
		{
			name: "NIM_CACHE_PATH on a typed workload",
			objs: []client.Object{deploymentWithEnv(corev1.EnvVar{Name: "NIM_CACHE_PATH", Value: "/opt/models"})},
			want: true,
		},
		{
			name: "HF_HOME on an unknown CRD",
			objs: []client.Object{dynamoGraphDeployment(map[string]any{"name": "HF_HOME", "value": "/opt/models"})},
			want: true,
		},
		{
			name: "unrelated env",
			objs: []client.Object{deploymentWithEnv(corev1.EnvVar{Name: "LOG_LEVEL", Value: "debug"})},
			want: false,
		},
		{
			name: "empty value does not count",
			objs: []client.Object{deploymentWithEnv(corev1.EnvVar{Name: "HF_HOME"})},
			want: false,
		},
		{
			name: "value from a reference counts, the workload still chose where the model lives",
			objs: []client.Object{deploymentWithEnv(corev1.EnvVar{
				Name: "HF_HOME",
				ValueFrom: &corev1.EnvVarSource{
					ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "cm"},
						Key:                  "hf",
					},
				},
			})},
			want: true,
		},
		{
			name: "config map data keys are not env entries",
			objs: []client.Object{&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "cm"},
				Data:       map[string]string{"HF_HOME": "/opt/models"},
			}},
			want: false,
		},
		{
			name: "config map data with keys named name and value is not an env entry",
			objs: []client.Object{&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "cm"},
				Data:       map[string]string{"name": "HF_HOME", "value": "/opt/models"},
			}},
			want: false,
		},
		{
			name: "name and value pair outside an env list is ignored",
			objs: []client.Object{&unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "example.com/v1",
				"kind":       "Thing",
				"metadata":   map[string]any{"name": "t"},
				"spec": map[string]any{"settings": []any{
					map[string]any{"name": "HF_HOME", "value": "/opt/models"},
				}},
			}}},
			want: false,
		},
		{
			name: "env list of a container nested in an operator CRD",
			objs: []client.Object{&unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "nvidia.com/v1alpha1",
				"kind":       "DynamoGraphDeployment",
				"metadata":   map[string]any{"name": "myllm"},
				"spec": map[string]any{"services": map[string]any{"Worker": map[string]any{
					"extraPodSpec": map[string]any{"mainContainer": map[string]any{
						"env": []any{map[string]any{"name": "NIM_CACHE_PATH", "value": "/opt/models"}},
					}},
				}}},
			}}},
			want: true,
		},
		{
			name: "any object in the list is enough",
			objs: []client.Object{
				deploymentWithEnv(corev1.EnvVar{Name: "LOG_LEVEL", Value: "debug"}),
				dynamoGraphDeployment(map[string]any{"name": "HF_HOME", "value": "/opt/models"}),
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, workloadSetsModelCachePath(tt.objs))
		})
	}
}

// TestEnsureInstanceNamespace_WorkloadLabels checks how workload labels reach a new or existing instance namespace.
func TestEnsureInstanceNamespace_WorkloadLabels(t *testing.T) {
	ms := &v1alpha1.MiniService{Spec: v1alpha1.MiniServiceSpec{Namespace: "sr-optout"}}
	icmsReq := &nvcav2beta1.ICMSRequest{}
	optOut := map[string]string{NvSnapInjectLabel: nvsnapInjectOptOut}

	t.Run("new namespace gets the label", func(t *testing.T) {
		ctx := newTestContext()
		c, _ := newFakeClient(mgrScheme)
		r := &Reconciler{Client: c}

		require.NoError(t, r.ensureInstanceNamespace(ctx, ms, icmsReq, optOut))

		got := &corev1.Namespace{}
		require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "sr-optout"}, got))
		assert.Equal(t, "false", got.Labels[NvSnapInjectLabel])
	})

	t.Run("no workload labels leaves the label unset", func(t *testing.T) {
		ctx := newTestContext()
		c, _ := newFakeClient(mgrScheme)
		r := &Reconciler{Client: c}

		require.NoError(t, r.ensureInstanceNamespace(ctx, ms, icmsReq, nil))

		got := &corev1.Namespace{}
		require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "sr-optout"}, got))
		assert.NotContains(t, got.Labels, NvSnapInjectLabel)
	})

	t.Run("existing namespace without the label is updated", func(t *testing.T) {
		ctx := newTestContext()
		existing := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "sr-optout"}}
		c, _ := newFakeClient(mgrScheme, existing)
		r := &Reconciler{Client: c}

		require.NoError(t, r.ensureInstanceNamespace(ctx, ms, icmsReq, optOut))

		got := &corev1.Namespace{}
		require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "sr-optout"}, got))
		assert.Equal(t, "false", got.Labels[NvSnapInjectLabel])
	})

	t.Run("update keeps labels and annotations added by other controllers", func(t *testing.T) {
		ctx := newTestContext()
		existing := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name:        "sr-optout",
			Labels:      map[string]string{"other.example.com/owner": "team-a"},
			Annotations: map[string]string{"other.example.com/note": "keep"},
		}}
		c, _ := newFakeClient(mgrScheme, existing)
		r := &Reconciler{Client: c}

		require.NoError(t, r.ensureInstanceNamespace(ctx, ms, icmsReq, optOut))

		got := &corev1.Namespace{}
		require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "sr-optout"}, got))
		assert.Equal(t, "false", got.Labels[NvSnapInjectLabel])
		assert.Equal(t, "team-a", got.Labels["other.example.com/owner"])
		assert.Equal(t, "keep", got.Annotations["other.example.com/note"])
	})

	t.Run("cluster configured namespace labels win", func(t *testing.T) {
		ctx := newTestContext()
		c, _ := newFakeClient(mgrScheme)
		r := &Reconciler{Client: c}
		r.InstanceNamespaceLabels = map[string]string{NvSnapInjectLabel: "true"}

		require.NoError(t, r.ensureInstanceNamespace(ctx, ms, icmsReq, optOut))

		got := &corev1.Namespace{}
		require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "sr-optout"}, got))
		assert.Equal(t, "true", got.Labels[NvSnapInjectLabel])
	})
}

// newUpdateWorkloadRenderedData renders a one Deployment workload revision with the given env.
func newUpdateWorkloadRenderedData(t *testing.T, env ...corev1.EnvVar) []byte {
	t.Helper()

	raw, err := json.Marshal([]client.Object{deploymentWithEnv(env...)})
	require.NoError(t, err)
	return raw
}

// TestDoUpdateWorkload_ReconcilesNvSnapNamespaceLabel checks that an update which adds a cache path opts the namespace out.
func TestDoUpdateWorkload_ReconcilesNvSnapNamespaceLabel(t *testing.T) {
	tests := []struct {
		name      string
		env       []corev1.EnvVar
		wantLabel string
	}{
		{
			name:      "update that adds HF_HOME opts the namespace out",
			env:       []corev1.EnvVar{{Name: "HF_HOME", Value: "/opt/models"}},
			wantLabel: nvsnapInjectOptOut,
		},
		{
			name:      "update without a model cache path leaves the namespace alone",
			env:       []corev1.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}},
			wantLabel: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext()
			// an update patches a workload that is already running
			existingWorkload := deploymentWithEnv()
			existingWorkload.Namespace = updateTestNamespace
			c, _ := newFakeClient(mgrScheme,
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: updateTestNamespace}},
				newReadyUtilsPod(),
				existingWorkload,
			)
			r := newUpdateTestReconciler(t, c, mgrScheme)
			ms := newUpdateMiniService(`{"key":"value-v2"}`)
			icmsReq := newUpdateICMSRequest(true)
			require.NoError(t, r.saveRenderedData(ctx, ms, newUpdateWorkloadRenderedData(t, tt.env...)))

			_, err := r.doUpdateWorkload(ctx, ms, icmsReq)
			require.NoError(t, err)

			got := &corev1.Namespace{}
			require.NoError(t, c.Get(ctx, client.ObjectKey{Name: updateTestNamespace}, got))
			assert.Equal(t, tt.wantLabel, got.Labels[NvSnapInjectLabel])
		})
	}
}

// TestReleaseNvSnapOptOut checks which opt-out labels are removed once the workload stops needing them.
func TestReleaseNvSnapOptOut(t *testing.T) {
	ms := &v1alpha1.MiniService{Spec: v1alpha1.MiniServiceSpec{Namespace: "sr-optout"}}
	icmsReq := &nvcav2beta1.ICMSRequest{}
	optOut := map[string]string{NvSnapInjectLabel: nvsnapInjectOptOut}

	t.Run("removed when the workload no longer needs it, other metadata kept", func(t *testing.T) {
		ctx := newTestContext()
		c, _ := newFakeClient(mgrScheme)
		r := &Reconciler{Client: c}
		require.NoError(t, r.ensureInstanceNamespace(ctx, ms, icmsReq, optOut))

		got := &corev1.Namespace{}
		require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "sr-optout"}, got))
		require.Equal(t, "false", got.Labels[NvSnapInjectLabel])
		require.Equal(t, "true", got.Annotations[NvSnapInjectManagedAnnotation])
		got.Labels["other.example.com/owner"] = "team-a"
		require.NoError(t, c.Update(ctx, got))

		require.NoError(t, r.releaseNvSnapOptOut(ctx, ms, nil))

		require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "sr-optout"}, got))
		assert.NotContains(t, got.Labels, NvSnapInjectLabel)
		assert.NotContains(t, got.Annotations, NvSnapInjectManagedAnnotation)
		assert.Equal(t, "team-a", got.Labels["other.example.com/owner"])
	})

	t.Run("ensuring the namespace never removes it", func(t *testing.T) {
		ctx := newTestContext()
		c, _ := newFakeClient(mgrScheme)
		r := &Reconciler{Client: c}
		require.NoError(t, r.ensureInstanceNamespace(ctx, ms, icmsReq, optOut))
		require.NoError(t, r.ensureInstanceNamespace(ctx, ms, icmsReq, nil))

		got := &corev1.Namespace{}
		require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "sr-optout"}, got))
		assert.Equal(t, "false", got.Labels[NvSnapInjectLabel])
	})

	t.Run("kept while the workload still needs it", func(t *testing.T) {
		ctx := newTestContext()
		c, _ := newFakeClient(mgrScheme)
		r := &Reconciler{Client: c}
		require.NoError(t, r.ensureInstanceNamespace(ctx, ms, icmsReq, optOut))
		require.NoError(t, r.releaseNvSnapOptOut(ctx, ms, optOut))

		got := &corev1.Namespace{}
		require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "sr-optout"}, got))
		assert.Equal(t, "false", got.Labels[NvSnapInjectLabel])
	})

	t.Run("label from cluster configuration is kept and not marked as managed", func(t *testing.T) {
		ctx := newTestContext()
		c, _ := newFakeClient(mgrScheme)
		r := &Reconciler{Client: c}
		r.InstanceNamespaceLabels = map[string]string{NvSnapInjectLabel: nvsnapInjectOptOut}
		require.NoError(t, r.ensureInstanceNamespace(ctx, ms, icmsReq, optOut))
		require.NoError(t, r.releaseNvSnapOptOut(ctx, ms, nil))

		got := &corev1.Namespace{}
		require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "sr-optout"}, got))
		assert.Equal(t, "false", got.Labels[NvSnapInjectLabel])
		assert.NotContains(t, got.Annotations, NvSnapInjectManagedAnnotation)
	})

	t.Run("label set by someone else is kept", func(t *testing.T) {
		ctx := newTestContext()
		existing := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name:   "sr-optout",
			Labels: map[string]string{NvSnapInjectLabel: nvsnapInjectOptOut},
		}}
		c, _ := newFakeClient(mgrScheme, existing)
		r := &Reconciler{Client: c}
		require.NoError(t, r.releaseNvSnapOptOut(ctx, ms, nil))

		got := &corev1.Namespace{}
		require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "sr-optout"}, got))
		assert.Equal(t, "false", got.Labels[NvSnapInjectLabel])
	})

	t.Run("a missing namespace is not an error", func(t *testing.T) {
		c, _ := newFakeClient(mgrScheme)
		r := &Reconciler{Client: c}
		require.NoError(t, r.releaseNvSnapOptOut(newTestContext(), ms, nil))
	})
}

// TestEnsureInstanceNamespace_ExistingOptOutLabelIsNotTakenOver checks that an opt-out someone else set is not claimed.
func TestEnsureInstanceNamespace_ExistingOptOutLabelIsNotTakenOver(t *testing.T) {
	ctx := newTestContext()
	ms := &v1alpha1.MiniService{Spec: v1alpha1.MiniServiceSpec{Namespace: "sr-optout"}}
	icmsReq := &nvcav2beta1.ICMSRequest{}
	existing := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "sr-optout",
		Labels: map[string]string{NvSnapInjectLabel: nvsnapInjectOptOut},
	}}
	c, _ := newFakeClient(mgrScheme, existing)
	r := &Reconciler{Client: c}

	// the workload asks for the same opt-out, then stops asking
	require.NoError(t, r.ensureInstanceNamespace(ctx, ms, icmsReq, map[string]string{NvSnapInjectLabel: nvsnapInjectOptOut}))
	got := &corev1.Namespace{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "sr-optout"}, got))
	assert.NotContains(t, got.Annotations, NvSnapInjectManagedAnnotation, "an existing label is not claimed")

	require.NoError(t, r.ensureInstanceNamespace(ctx, ms, icmsReq, nil))
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "sr-optout"}, got))
	assert.Equal(t, "false", got.Labels[NvSnapInjectLabel], "a label the workload did not introduce stays")
}

// TestDoUpdateWorkload_RemovesNvSnapOptOutWhenCachePathIsRemoved checks that a successful update releases the opt-out.
func TestDoUpdateWorkload_RemovesNvSnapOptOutWhenCachePathIsRemoved(t *testing.T) {
	ctx := newTestContext()
	existingWorkload := deploymentWithEnv(corev1.EnvVar{Name: "HF_HOME", Value: "/opt/models"})
	existingWorkload.Namespace = updateTestNamespace
	c, _ := newFakeClient(mgrScheme,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name:        updateTestNamespace,
			Labels:      map[string]string{NvSnapInjectLabel: nvsnapInjectOptOut},
			Annotations: map[string]string{NvSnapInjectManagedAnnotation: "true"},
		}},
		newReadyUtilsPod(),
		existingWorkload,
	)
	r := newUpdateTestReconciler(t, c, mgrScheme)
	ms := newUpdateMiniService(`{"key":"value-v3"}`)
	icmsReq := newUpdateICMSRequest(true)
	// the new revision no longer sets a model cache path
	require.NoError(t, r.saveRenderedData(ctx, ms, newUpdateWorkloadRenderedData(t,
		corev1.EnvVar{Name: "LOG_LEVEL", Value: "debug"})))

	_, err := r.doUpdateWorkload(ctx, ms, icmsReq)
	require.NoError(t, err)

	got := &corev1.Namespace{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: updateTestNamespace}, got))
	assert.NotContains(t, got.Labels, NvSnapInjectLabel)
	assert.NotContains(t, got.Annotations, NvSnapInjectManagedAnnotation)
}

// TestEnsureInstanceNamespace_ExistingExplicitInjectValueIsKept checks that an explicit nvsnap.io/inject value wins.
func TestEnsureInstanceNamespace_ExistingExplicitInjectValueIsKept(t *testing.T) {
	ctx := newTestContext()
	ms := &v1alpha1.MiniService{Spec: v1alpha1.MiniServiceSpec{Namespace: "sr-optout"}}
	existing := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "sr-optout",
		Labels: map[string]string{NvSnapInjectLabel: "true"},
	}}
	c, _ := newFakeClient(mgrScheme, existing)
	r := &Reconciler{Client: c}

	require.NoError(t, r.ensureInstanceNamespace(ctx, ms, &nvcav2beta1.ICMSRequest{},
		map[string]string{NvSnapInjectLabel: nvsnapInjectOptOut}))

	got := &corev1.Namespace{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "sr-optout"}, got))
	assert.Equal(t, "true", got.Labels[NvSnapInjectLabel], "a value someone set explicitly is not overwritten")
	assert.NotContains(t, got.Annotations, NvSnapInjectManagedAnnotation)
}

// TestDoUpdateWorkload_KeepsNvSnapOptOutWhenApplyFails checks that removing a cache path does not release the opt-out early.
func TestDoUpdateWorkload_KeepsNvSnapOptOutWhenApplyFails(t *testing.T) {
	ctx := newTestContext()
	existingWorkload := deploymentWithEnv(corev1.EnvVar{Name: "HF_HOME", Value: "/opt/models"})
	existingWorkload.Namespace = updateTestNamespace
	c, _ := newFakeClientWithInterceptors(mgrScheme,
		interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if _, ok := obj.(*appsv1.Deployment); ok {
					return apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"},
						obj.GetName(), fmt.Errorf("forbidden by test"))
				}
				return c.Patch(ctx, obj, patch, opts...)
			},
		},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name:        updateTestNamespace,
			Labels:      map[string]string{NvSnapInjectLabel: nvsnapInjectOptOut},
			Annotations: map[string]string{NvSnapInjectManagedAnnotation: "true"},
		}},
		newReadyUtilsPod(),
		existingWorkload,
	)
	r := newUpdateTestReconciler(t, c, mgrScheme)
	ms := newUpdateMiniService(`{"key":"value-v3"}`)
	require.NoError(t, r.saveRenderedData(ctx, ms, newUpdateWorkloadRenderedData(t,
		corev1.EnvVar{Name: "LOG_LEVEL", Value: "debug"})))

	_, err := r.doUpdateWorkload(ctx, ms, newUpdateICMSRequest(true))
	require.Error(t, err)

	got := &corev1.Namespace{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: updateTestNamespace}, got))
	assert.Equal(t, "false", got.Labels[NvSnapInjectLabel], "the old workload keeps its opt-out when the update does not apply")
	assert.Equal(t, "true", got.Annotations[NvSnapInjectManagedAnnotation])
}

// TestDoUpdateWorkload_FailedUpdateRestoresNvSnapOptOut checks what happens to a newly added opt-out when the apply fails.
func TestDoUpdateWorkload_FailedUpdateRestoresNvSnapOptOut(t *testing.T) {
	tests := []struct {
		name             string
		previousLabel    string
		partialApply     bool
		readFailure      bool
		readForbidden    bool
		transientFailure bool
		wantLabel        string
	}{
		{name: "rejected update releases newly added opt-out"},
		{name: "transient failure releases newly added opt-out", transientFailure: true},
		{name: "partial apply keeps opt-out", partialApply: true, wantLabel: nvsnapInjectOptOut},
		{name: "transient live read failure keeps opt-out", readFailure: true, wantLabel: nvsnapInjectOptOut},
		{name: "non-transient live read failure keeps opt-out and is reported", readFailure: true, readForbidden: true, wantLabel: nvsnapInjectOptOut},
		{name: "existing opt-out is kept", previousLabel: nvsnapInjectOptOut, wantLabel: nvsnapInjectOptOut},
		{name: "explicit opt-in is kept", previousLabel: "true", wantLabel: "true"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext()
			namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: updateTestNamespace}}
			if tt.previousLabel != "" {
				namespace.Labels = map[string]string{NvSnapInjectLabel: tt.previousLabel}
			}
			existing := deploymentWithEnv()
			existing.Namespace = updateTestNamespace
			failed := false
			applyErr := apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "worker", fmt.Errorf("rejected by test"))
			var patchErr error = applyErr
			if tt.transientFailure {
				patchErr = apierrors.NewServiceUnavailable("unavailable by test")
			}
			c, _ := newFakeClientWithInterceptors(mgrScheme, interceptor.Funcs{
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					deployment, ok := obj.(*appsv1.Deployment)
					if !ok {
						return c.Patch(ctx, obj, patch, opts...)
					}
					if tt.partialApply {
						// Simulate a write accepted by the server followed by a response failure.
						live := &appsv1.Deployment{}
						require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(obj), live))
						live.Spec = deployment.Spec
						require.NoError(t, c.Update(ctx, live))
					}
					failed = true
					return patchErr
				},
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if tt.readFailure && failed {
						if _, ok := obj.(*unstructured.Unstructured); ok {
							if tt.readForbidden {
								return apierrors.NewForbidden(schema.GroupResource{Resource: "deployments"}, "worker",
									fmt.Errorf("live read unavailable by test"))
							}
							return apierrors.NewServiceUnavailable("live read unavailable by test")
						}
					}
					return c.Get(ctx, key, obj, opts...)
				},
			}, namespace, newReadyUtilsPod(), existing)
			r := newUpdateTestReconciler(t, c, mgrScheme)
			ms := newUpdateMiniService(`{"key":"value-v2"}`)
			require.NoError(t, r.saveRenderedData(ctx, ms, newUpdateWorkloadRenderedData(t,
				corev1.EnvVar{Name: "HF_HOME", Value: "/opt/models"})))

			_, err := r.doUpdateWorkload(ctx, ms, newUpdateICMSRequest(true))
			require.ErrorIs(t, err, patchErr)
			require.True(t, failed, "the test must reach workload apply")
			if tt.readFailure && tt.readForbidden {
				assert.Contains(t, err.Error(), "live read unavailable by test", "a non-transient restore error is reported")
			}
			if tt.readFailure && !tt.readForbidden {
				assert.NotContains(t, err.Error(), "live read unavailable by test", "a transient restore error is only logged")
			}
			got := &corev1.Namespace{}
			require.NoError(t, c.Get(ctx, client.ObjectKey{Name: updateTestNamespace}, got))
			assert.Equal(t, tt.wantLabel, got.Labels[NvSnapInjectLabel])
			if tt.wantLabel == "" || tt.previousLabel != "" {
				assert.NotContains(t, got.Annotations, NvSnapInjectManagedAnnotation)
			} else {
				assert.Equal(t, "true", got.Annotations[NvSnapInjectManagedAnnotation])
			}
		})
	}
}

// TestEnsureInstanceNamespace_LeftoverMarkDoesNotClaimAChangedLabel checks that a label someone changed to another
// value is kept even when the managed mark was left behind.
func TestEnsureInstanceNamespace_LeftoverMarkDoesNotClaimAChangedLabel(t *testing.T) {
	ctx := newTestContext()
	ms := &v1alpha1.MiniService{Spec: v1alpha1.MiniServiceSpec{Namespace: "sr-optout"}}
	existing := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:        "sr-optout",
		Labels:      map[string]string{NvSnapInjectLabel: "true"},
		Annotations: map[string]string{NvSnapInjectManagedAnnotation: "true"},
	}}
	c, _ := newFakeClient(mgrScheme, existing)
	r := &Reconciler{Client: c}

	require.NoError(t, r.ensureInstanceNamespace(ctx, ms, &nvcav2beta1.ICMSRequest{},
		map[string]string{NvSnapInjectLabel: nvsnapInjectOptOut}))

	got := &corev1.Namespace{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "sr-optout"}, got))
	assert.Equal(t, "true", got.Labels[NvSnapInjectLabel], "the changed value is not overwritten")
}

// TestDoUpdateWorkload_FailedUpdateReadsLiveStateThroughTheAPIReader checks that the decision after a failed apply
// uses the API reader, which sees a partial apply that the cached client does not.
func TestDoUpdateWorkload_FailedUpdateReadsLiveStateThroughTheAPIReader(t *testing.T) {
	ctx := newTestContext()
	applyErr := apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "worker",
		fmt.Errorf("rejected by test"))
	cached := deploymentWithEnv()
	cached.Namespace = updateTestNamespace
	c, _ := newFakeClientWithInterceptors(mgrScheme, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if _, ok := obj.(*appsv1.Deployment); ok {
				return applyErr
			}
			return c.Patch(ctx, obj, patch, opts...)
		},
	}, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: updateTestNamespace}}, newReadyUtilsPod(), cached)

	// the API reader already sees the partial apply, the cached client does not
	partial := deploymentWithEnv(corev1.EnvVar{Name: "HF_HOME", Value: "/opt/models"})
	partial.Namespace = updateTestNamespace
	live, _ := newFakeClient(mgrScheme, partial)
	r := newUpdateTestReconciler(t, c, mgrScheme)
	r.apiReader = live
	ms := newUpdateMiniService(`{"key":"value-v2"}`)
	require.NoError(t, r.saveRenderedData(ctx, ms, newUpdateWorkloadRenderedData(t,
		corev1.EnvVar{Name: "HF_HOME", Value: "/opt/models"})))

	_, err := r.doUpdateWorkload(ctx, ms, newUpdateICMSRequest(true))
	require.ErrorIs(t, err, applyErr)

	got := &corev1.Namespace{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: updateTestNamespace}, got))
	assert.Equal(t, nvsnapInjectOptOut, got.Labels[NvSnapInjectLabel], "the partial apply seen through the API reader keeps the opt-out")
}

// TestReleaseNvSnapOptOut_ReturnsUpdateConflict checks that a conflict while releasing is returned with context,
// so the reconcile is retried.
func TestReleaseNvSnapOptOut_ReturnsUpdateConflict(t *testing.T) {
	ctx := newTestContext()
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:        "sr-optout",
		Labels:      map[string]string{NvSnapInjectLabel: nvsnapInjectOptOut},
		Annotations: map[string]string{NvSnapInjectManagedAnnotation: "true"},
	}}
	c, _ := newFakeClientWithInterceptors(mgrScheme, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if _, ok := obj.(*corev1.Namespace); ok {
				return apierrors.NewConflict(schema.GroupResource{Resource: "namespaces"}, obj.GetName(),
					fmt.Errorf("modified by test"))
			}
			return c.Update(ctx, obj, opts...)
		},
	}, namespace)
	r := &Reconciler{Client: c}
	ms := &v1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{Name: "ms"},
		Spec:       v1alpha1.MiniServiceSpec{Namespace: "sr-optout"},
	}

	err := r.releaseNvSnapOptOut(ctx, ms, nil)
	require.Error(t, err)
	assert.True(t, apierrors.IsConflict(err))
	assert.Contains(t, err.Error(), "sr-optout")
}

// TestDoUpdateWorkload_ReleaseConflictRequeues checks that a conflict while releasing the opt-out is retried quietly
// instead of surfacing as a reconcile error.
func TestDoUpdateWorkload_ReleaseConflictRequeues(t *testing.T) {
	ctx := newTestContext()
	existingWorkload := deploymentWithEnv(corev1.EnvVar{Name: "HF_HOME", Value: "/opt/models"})
	existingWorkload.Namespace = updateTestNamespace
	c, _ := newFakeClientWithInterceptors(mgrScheme, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			// the release is the namespace update that drops the managed mark
			if ns, ok := obj.(*corev1.Namespace); ok && ns.Annotations[NvSnapInjectManagedAnnotation] == "" {
				return apierrors.NewConflict(schema.GroupResource{Resource: "namespaces"}, ns.Name,
					fmt.Errorf("modified by test"))
			}
			return c.Update(ctx, obj, opts...)
		},
	},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name:        updateTestNamespace,
			Labels:      map[string]string{NvSnapInjectLabel: nvsnapInjectOptOut},
			Annotations: map[string]string{NvSnapInjectManagedAnnotation: "true"},
		}},
		newReadyUtilsPod(),
		existingWorkload,
	)
	r := newUpdateTestReconciler(t, c, mgrScheme)
	ms := newUpdateMiniService(`{"key":"value-v3"}`)
	// the new revision no longer sets a model cache path
	require.NoError(t, r.saveRenderedData(ctx, ms, newUpdateWorkloadRenderedData(t,
		corev1.EnvVar{Name: "LOG_LEVEL", Value: "debug"})))

	res, err := r.doUpdateWorkload(ctx, ms, newUpdateICMSRequest(true))
	require.NoError(t, err, "a conflict is transient and must not be a reconcile error")
	assert.True(t, res.Requeue)

	got := &corev1.Namespace{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: updateTestNamespace}, got))
	assert.Equal(t, nvsnapInjectOptOut, got.Labels[NvSnapInjectLabel], "the opt-out stays until the release succeeds")
}

// TestEnsureInstanceNamespace_DropsStaleConditionalMetadata checks that keys NVCA sets only under some settings do
// not outlive those settings, while metadata from other controllers is kept.
func TestEnsureInstanceNamespace_DropsStaleConditionalMetadata(t *testing.T) {
	ctx := newTestContext()
	ms := &v1alpha1.MiniService{Spec: v1alpha1.MiniServiceSpec{Namespace: "sr-conditional"}}
	existing := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "sr-conditional",
		Labels: map[string]string{
			nvcatypes.ShaderCacheLabelKey: "true",
			enforce.NeedsEnforceLabelKey:  "true",
			"other.example.com/owner":     "team-a",
		},
		Annotations: map[string]string{
			enforce.EnforcementsAnnotationKey: "x=y",
			"other.example.com/note":          "keep",
		},
	}}
	c, _ := newFakeClient(mgrScheme, existing)
	r := &Reconciler{Client: c}
	r.FeatureFlagFetcher = &featureflagmock.Fetcher{} // GXCache off, no enforcement attributes

	require.NoError(t, r.ensureInstanceNamespace(ctx, ms, &nvcav2beta1.ICMSRequest{}, nil))

	got := &corev1.Namespace{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "sr-conditional"}, got))
	assert.NotContains(t, got.Labels, nvcatypes.ShaderCacheLabelKey)
	assert.NotContains(t, got.Labels, enforce.NeedsEnforceLabelKey)
	assert.NotContains(t, got.Annotations, enforce.EnforcementsAnnotationKey)
	assert.Equal(t, "team-a", got.Labels["other.example.com/owner"])
	assert.Equal(t, "keep", got.Annotations["other.example.com/note"])
}

// TestEnsureInstanceNamespace_KeepsConditionalMetadataWhileSet checks that the same keys are kept while the
// settings that produce them are still on.
func TestEnsureInstanceNamespace_KeepsConditionalMetadataWhileSet(t *testing.T) {
	ctx := newTestContext()
	ms := &v1alpha1.MiniService{Spec: v1alpha1.MiniServiceSpec{Namespace: "sr-conditional"}}
	existing := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:        "sr-conditional",
		Labels:      map[string]string{enforce.NeedsEnforceLabelKey: "true"},
		Annotations: map[string]string{enforce.EnforcementsAnnotationKey: "x=y"},
	}}
	c, _ := newFakeClient(mgrScheme, existing)
	r := &Reconciler{Client: c}
	r.FeatureFlagFetcher = &featureflagmock.Fetcher{}
	r.enabledAttrs = featureflag.NewAttributes(map[string]string{"x": "y"})

	require.NoError(t, r.ensureInstanceNamespace(ctx, ms, &nvcav2beta1.ICMSRequest{}, nil))

	got := &corev1.Namespace{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "sr-conditional"}, got))
	assert.Equal(t, "true", got.Labels[enforce.NeedsEnforceLabelKey])
	assert.Equal(t, "x=y", got.Annotations[enforce.EnforcementsAnnotationKey])
}

// TestEnsureInstanceNamespace_RelinquishedOwnershipMarkIsRemoved checks the sequence where someone takes over a managed
// label: the stale mark must go, or a later external opt-out would be treated as NVCA's and removed.
func TestEnsureInstanceNamespace_RelinquishedOwnershipMarkIsRemoved(t *testing.T) {
	ctx := newTestContext()
	ms := &v1alpha1.MiniService{Spec: v1alpha1.MiniServiceSpec{Namespace: "sr-optout"}}
	icmsReq := &nvcav2beta1.ICMSRequest{}
	optOut := map[string]string{NvSnapInjectLabel: nvsnapInjectOptOut}
	c, _ := newFakeClient(mgrScheme)
	r := &Reconciler{Client: c}

	// NVCA sets the opt-out and marks it
	require.NoError(t, r.ensureInstanceNamespace(ctx, ms, icmsReq, optOut))
	got := &corev1.Namespace{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "sr-optout"}, got))
	require.Equal(t, "true", got.Annotations[NvSnapInjectManagedAnnotation])

	// an operator takes the label over and sets it to true, leaving the mark behind
	got.Labels[NvSnapInjectLabel] = "true"
	require.NoError(t, c.Update(ctx, got))
	require.NoError(t, r.ensureInstanceNamespace(ctx, ms, icmsReq, optOut))
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "sr-optout"}, got))
	assert.Equal(t, "true", got.Labels[NvSnapInjectLabel], "the operator's value is kept")
	assert.NotContains(t, got.Annotations, NvSnapInjectManagedAnnotation, "NVCA gave up the label, so the mark goes")

	// the operator later sets false itself, and the workload stops needing an opt-out
	got.Labels[NvSnapInjectLabel] = nvsnapInjectOptOut
	require.NoError(t, c.Update(ctx, got))
	require.NoError(t, r.releaseNvSnapOptOut(ctx, ms, nil))
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "sr-optout"}, got))
	assert.Equal(t, "false", got.Labels[NvSnapInjectLabel], "an external opt-out is not NVCA's to remove")
}

// TestDoUpdateWorkload_NamespaceErrorsAreClassified checks the two namespace calls made before the workload is applied:
// a transient error requeues quietly and any other error is wrapped with the namespace and MiniService.
func TestDoUpdateWorkload_NamespaceErrorsAreClassified(t *testing.T) {
	tests := []struct {
		name        string
		getErr      error
		updateErr   error
		wantRequeue bool
		wantErrText string
	}{
		{
			name:        "transient namespace read requeues",
			getErr:      apierrors.NewServiceUnavailable("unavailable by test"),
			wantRequeue: true,
		},
		{
			name:        "non-transient namespace read is wrapped",
			getErr:      apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, updateTestNamespace, fmt.Errorf("denied by test")),
			wantErrText: `get namespace "ms-update-ns" before the update of MiniService`,
		},
		{
			name: "namespace update conflict requeues",
			updateErr: apierrors.NewConflict(schema.GroupResource{Resource: "namespaces"}, updateTestNamespace,
				fmt.Errorf("modified by test")),
			wantRequeue: true,
		},
		{
			name: "non-transient namespace update is wrapped",
			updateErr: apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, updateTestNamespace,
				fmt.Errorf("denied by test")),
			wantErrText: `ensure instance namespace "ms-update-ns" for MiniService`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext()
			existingWorkload := deploymentWithEnv()
			existingWorkload.Namespace = updateTestNamespace
			c, _ := newFakeClientWithInterceptors(mgrScheme, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*corev1.Namespace); ok && tt.getErr != nil {
						return tt.getErr
					}
					return c.Get(ctx, key, obj, opts...)
				},
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					if _, ok := obj.(*corev1.Namespace); ok && tt.updateErr != nil {
						return tt.updateErr
					}
					return c.Update(ctx, obj, opts...)
				},
			}, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: updateTestNamespace}}, newReadyUtilsPod(), existingWorkload)
			r := newUpdateTestReconciler(t, c, mgrScheme)
			ms := newUpdateMiniService(`{"key":"value-v2"}`)
			// a workload that sets a cache path, so the namespace is read and ensured before the apply
			require.NoError(t, r.saveRenderedData(ctx, ms, newUpdateWorkloadRenderedData(t,
				corev1.EnvVar{Name: "HF_HOME", Value: "/opt/models"})))

			res, err := r.doUpdateWorkload(ctx, ms, newUpdateICMSRequest(true))
			if tt.wantRequeue {
				require.NoError(t, err, "a transient error must not be a reconcile error")
				assert.True(t, res.Requeue)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErrText)
			assert.False(t, res.Requeue)
		})
	}
}
