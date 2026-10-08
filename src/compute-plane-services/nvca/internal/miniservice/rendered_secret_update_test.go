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

package mscontroller

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
)

const updateWorkloadObjectName = "updated-workload-cm"

// newValidReValClient returns a ReVal client that renders the given objects successfully.
func newValidReValClient(rendered []byte) *outputReValClient {
	return &outputReValClient{output: HelmReValRenderOutput{Valid: newBool(true), Output: rendered}}
}

// newRevisionConfigMap builds a revision history ConfigMap as saveRevisionHistory would.
func newRevisionConfigMap(revision int64, values string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s%d", revisionConfigMapPrefix, revision),
			Namespace: updateTestNamespace,
			Labels: map[string]string{
				managedByLabel:       managedByValue,
				miniserviceNameLabel: updateMSName,
				revisionLabel:        fmt.Sprint(revision),
			},
		},
		Data: map[string]string{
			revisionDataKeyValues:   values,
			revisionDataKeyChartURL: newUpdateMiniService("").Spec.HelmChartConfig.URL,
		},
	}
}

func updateTestObjects(values string) []client.Object {
	return []client.Object{
		newUpdateMiniService(values),
		newUpdateICMSRequest(true),
		newReadyUtilsPod(),
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: updateWorkloadObjectName, Namespace: updateTestNamespace},
			Data:       map[string]string{"key": "existing"},
		},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: updateTestNamespace}},
	}
}

func enableRevisionHistory(r *Reconciler) {
	r.FeatureFlagFetcher = &featureflagmock.Fetcher{
		EnabledFFs: []*featureflag.FeatureFlag{featureflag.MiniServiceRevisionHistory},
	}
}

func getWorkloadConfigMapValue(t *testing.T, c client.Client) string {
	t.Helper()
	cm := &corev1.ConfigMap{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: updateTestNamespace, Name: updateWorkloadObjectName}, cm))
	return cm.Data["key"]
}

// A Secret write failure after a successful render must not attempt the apply. Nothing is kept in
// memory, so the retry renders again, stores the Secret, and only then applies.
func TestReconcile_UpdateSecretWriteFailureIsRetried(t *testing.T) {
	ctx := newTestContext()

	failSecretCreate := true
	c, _ := newFakeClientWithInterceptors(mgrScheme,
		interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if s, ok := obj.(*corev1.Secret); ok && s.Name == RenderedSecretName && failSecretCreate {
					return apierrors.NewInternalError(errors.New("injected secret create failure"))
				}
				return c.Create(ctx, obj, opts...)
			},
		},
		updateTestObjects(`{"key":"value-v2"}`)...,
	)
	r := newUpdateTestReconciler(t, c, mgrScheme)
	enableRevisionHistory(r)
	rendered := newUpdateRenderedData(t, updateWorkloadObjectName, "v2")
	rv := newValidReValClient(rendered)
	r.ReValClient = rv

	req := reconcile.Request{NamespacedName: client.ObjectKey{Name: updateMSName}}
	_, err := r.Reconcile(ctx, req)
	require.Error(t, err)
	assert.ErrorContains(t, err, "injected secret create failure")
	assert.Equal(t, 1, rv.calls)

	ms := &v1alpha1.MiniService{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: updateMSName}, ms))
	assert.Equal(t, v1alpha1.MiniServiceInstalling, ms.Status.Phase)
	require.NotNil(t, ms.Status.RenderDetails, "render hash is recorded even though the Secret write failed")
	assert.Equal(t, "existing", getWorkloadConfigMapValue(t, c), "objects must not be applied before the render is stored")
	err = c.Get(ctx, client.ObjectKey{Namespace: updateTestNamespace, Name: RenderedSecretName}, &corev1.Secret{})
	assert.True(t, apierrors.IsNotFound(err))

	// Retry: the render is redone (no in-memory copy), the Secret write succeeds, then the apply proceeds.
	failSecretCreate = false
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, 2, rv.calls, "retry renders again because nothing is retained in memory")

	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: updateMSName}, ms))
	assert.Equal(t, v1alpha1.MiniServiceInstalled, ms.Status.Phase)
	assert.Equal(t, "v2", getWorkloadConfigMapValue(t, c))

	secret := getRenderedSecret(t, c, updateTestNamespace)
	assert.Equal(t, "2", secret.Labels[revisionLabel])
	assert.Equal(t, ms.Status.RenderDetails.Hash, secret.Annotations[renderedSecretOutputHashAnnotation])
	assert.Equal(t, renderInputHash(ms), secret.Annotations[renderedSecretInputHashAnnotation])

	revCM := &corev1.ConfigMap{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: updateTestNamespace, Name: revisionConfigMapPrefix + "2"}, revCM))
	assert.Equal(t, secret.Annotations[renderedSecretOutputHashAnnotation], revCM.Data[revisionDataKeyRenderHash])
	assert.JSONEq(t, `{"key":"value-v2"}`, revCM.Data[revisionDataKeyValues])
}

// If the agent died after writing the Secret but before the status patch landed, status has no render
// hash. The stored render for identical inputs must be reused rather than rendered again.
func TestReconcile_UpdateReusesSecretWhenStatusHashIsMissing(t *testing.T) {
	ctx := newTestContext()
	c, _ := newFakeClient(mgrScheme, updateTestObjects(`{"key":"value-v2"}`)...)
	rendered := newUpdateRenderedData(t, updateWorkloadObjectName, "v2")

	// Previous process: rendered and stored, but the status patch never happened.
	previous := newUpdateTestReconciler(t, c, mgrScheme)
	scratch := newUpdateMiniService(`{"key":"value-v2"}`)
	previous.saveRenderedData(ctx, scratch, rendered)
	require.NoError(t, previous.persistRenderedData(ctx, scratch, rendered))
	storedHash := getRenderedSecret(t, c, updateTestNamespace).Annotations[renderedSecretOutputHashAnnotation]

	ms := &v1alpha1.MiniService{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: updateMSName}, ms))
	require.Nil(t, ms.Status.RenderDetails)

	// New process with a broken ReVal.
	r := newUpdateTestReconciler(t, c, mgrScheme)
	rv := &outputReValClient{err: errors.New("reval unavailable")}
	r.ReValClient = rv

	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKey{Name: updateMSName}})
	require.NoError(t, err)
	assert.Equal(t, 0, rv.calls, "stored render for identical inputs must be reused")

	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: updateMSName}, ms))
	assert.Equal(t, v1alpha1.MiniServiceInstalled, ms.Status.Phase)
	require.NotNil(t, ms.Status.RenderDetails)
	assert.Equal(t, storedHash, ms.Status.RenderDetails.Hash, "render hash is restored from the Secret")
	assert.Equal(t, "v2", getWorkloadConfigMapValue(t, c))
}

// After a render and Secret write, a failed apply leaves the Secret at the new revision without a
// revision ConfigMap. Reverting the values to the last recorded revision must re-render for the
// reverted values instead of reusing the stored render.
func TestReconcile_ApplyFailureThenValuesRevertRerenders(t *testing.T) {
	ctx := newTestContext()

	failWorkloadPatch := true
	objs := append(updateTestObjects(`{"key":"value-v2"}`), newRevisionConfigMap(1, `{"key":"value-v1"}`))
	c, _ := newFakeClientWithInterceptors(mgrScheme,
		interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == updateWorkloadObjectName && failWorkloadPatch {
					return apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, cm.Name, errors.New("forbidden by test"))
				}
				return c.Patch(ctx, obj, patch, opts...)
			},
		},
		objs...,
	)
	r := newUpdateTestReconciler(t, c, mgrScheme)
	enableRevisionHistory(r)
	r.ReValClient = newValidReValClient(newUpdateRenderedData(t, updateWorkloadObjectName, "v2"))

	req := reconcile.Request{NamespacedName: client.ObjectKey{Name: updateMSName}}
	_, err := r.Reconcile(ctx, req)
	require.Error(t, err)
	assert.ErrorContains(t, err, "forbidden")

	ms := &v1alpha1.MiniService{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: updateMSName}, ms))
	assert.Equal(t, v1alpha1.MiniServiceInstalling, ms.Status.Phase)
	assert.Equal(t, int64(2), ms.Status.Revision)
	v2InputHash := renderInputHash(ms)
	secret := getRenderedSecret(t, c, updateTestNamespace)
	assert.Equal(t, "2", secret.Labels[revisionLabel])
	assert.Equal(t, v2InputHash, secret.Annotations[renderedSecretInputHashAnnotation])
	err = c.Get(ctx, client.ObjectKey{Namespace: updateTestNamespace, Name: revisionConfigMapPrefix + "2"}, &corev1.ConfigMap{})
	assert.True(t, apierrors.IsNotFound(err), "revision history is only recorded after a successful apply")

	// User reverts the values to the last recorded revision.
	ms.Spec.HelmChartConfig.Values = []byte(`{"key":"value-v1"}`)
	ms.Generation = 3
	require.NoError(t, c.Update(ctx, ms))
	rv := newValidReValClient(newUpdateRenderedData(t, updateWorkloadObjectName, "v1"))
	r.ReValClient = rv
	failWorkloadPatch = false

	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, 1, rv.calls, "reverted values must be rendered, not served from the revision 2 render")

	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: updateMSName}, ms))
	assert.Equal(t, v1alpha1.MiniServiceInstalled, ms.Status.Phase)
	assert.Equal(t, int64(2), ms.Status.Revision, "values unchanged from history keeps the pending revision")
	assert.Equal(t, "v1", getWorkloadConfigMapValue(t, c))

	secret = getRenderedSecret(t, c, updateTestNamespace)
	assert.Equal(t, renderInputHash(ms), secret.Annotations[renderedSecretInputHashAnnotation])
	assert.NotEqual(t, v2InputHash, secret.Annotations[renderedSecretInputHashAnnotation])

	revCM := &corev1.ConfigMap{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: updateTestNamespace, Name: revisionConfigMapPrefix + "2"}, revCM))
	assert.JSONEq(t, `{"key":"value-v1"}`, revCM.Data[revisionDataKeyValues])
	assert.Equal(t, secret.Annotations[renderedSecretOutputHashAnnotation], revCM.Data[revisionDataKeyRenderHash])
}

// A transient ReVal failure during a values update must be retried on the next reconcile rather
// than cached until the values change again. Only terminal render results are cached.
func TestReconcile_UpdateTransientRenderErrorIsRetried(t *testing.T) {
	ctx := newTestContext()
	c, _ := newFakeClient(mgrScheme, updateTestObjects(`{"key":"value-v2"}`)...)
	r := newUpdateTestReconciler(t, c, mgrScheme)
	enableRevisionHistory(r)

	flaky := &outputReValClient{err: errors.New("503 reval unavailable")}
	r.ReValClient = flaky
	req := reconcile.Request{NamespacedName: client.ObjectKey{Name: updateMSName}}

	_, err := r.Reconcile(ctx, req)
	require.Error(t, err)
	assert.False(t, isTerminal(err))
	assert.Equal(t, 1, flaky.calls)

	ms := &v1alpha1.MiniService{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: updateMSName}, ms))
	assert.Equal(t, v1alpha1.MiniServiceInstalling, ms.Status.Phase)
	assert.Nil(t, ms.Status.RenderDetails)
	assert.Equal(t, "existing", getWorkloadConfigMapValue(t, c))

	// ReVal recovers; the retry renders again instead of returning a cached error.
	healthy := newValidReValClient(newUpdateRenderedData(t, updateWorkloadObjectName, "v2"))
	r.ReValClient = healthy
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, 1, healthy.calls, "transient render errors must not be cached")

	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: updateMSName}, ms))
	assert.Equal(t, v1alpha1.MiniServiceInstalled, ms.Status.Phase)
	assert.Equal(t, "v2", getWorkloadConfigMapValue(t, c))
	getRenderedSecret(t, c, updateTestNamespace)
}

// An invalid chart during a values update is cached per values hash so ReVal is not called again
// until the values change; the running workload and the stored render are untouched.
func TestReconcile_UpdateInvalidRenderIsCachedUntilValuesChange(t *testing.T) {
	ctx := newTestContext()
	c, _ := newFakeClient(mgrScheme, updateTestObjects(`{"key":"value-v2"}`)...)
	r := newUpdateTestReconciler(t, c, mgrScheme)
	enableRevisionHistory(r)

	// Revision 1 render stored for the previous values.
	previous := newUpdateMiniService(`{"key":"value-v1"}`)
	previous.Status.Revision = 1
	saveAndPersistRenderedData(t, ctx, r, previous, newUpdateRenderedData(t, updateWorkloadObjectName, "v1"))
	previousInputHash := renderInputHash(previous)

	invalid := &outputReValClient{output: HelmReValRenderOutput{Valid: newBool(false), ValidationErrors: []string{"bad"}}}
	r.ReValClient = invalid
	req := reconcile.Request{NamespacedName: client.ObjectKey{Name: updateMSName}}

	for i := 0; i < 3; i++ {
		_, err := r.Reconcile(ctx, req)
		require.Error(t, err)
		assert.False(t, isTerminal(err), "an update failure must not fail the running instance")
	}
	assert.Equal(t, 1, invalid.calls, "invalid results are cached per values hash")

	ms := &v1alpha1.MiniService{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: updateMSName}, ms))
	assert.Equal(t, v1alpha1.MiniServiceInstalling, ms.Status.Phase)
	cond := meta.FindStatusCondition(ms.Status.Conditions, v1alpha1.MiniServiceConditionInstallSuccessful)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, v1alpha1.MiniServiceStatusReasonReValResultInvalid, cond.Reason)
	assert.Equal(t, "existing", getWorkloadConfigMapValue(t, c))
	secret := getRenderedSecret(t, c, updateTestNamespace)
	assert.Equal(t, previousInputHash, secret.Annotations[renderedSecretInputHashAnnotation], "stored render is untouched")
	assert.Equal(t, "1", secret.Labels[revisionLabel])
}
