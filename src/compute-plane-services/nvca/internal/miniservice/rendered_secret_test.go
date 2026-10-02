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
	"crypto/rand"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
)

// outputReValClient returns a fixed ReVal output and counts calls.
type outputReValClient struct {
	calls  int
	output HelmReValRenderOutput
	err    error
}

func (c *outputReValClient) Render(_ context.Context, _ HelmReValRenderInput) (HelmReValRenderOutput, error) {
	c.calls++
	return c.output, c.err
}

func getRenderedSecret(t *testing.T, c client.Client, ns string) *corev1.Secret {
	t.Helper()
	secret := &corev1.Secret{}
	err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: RenderedSecretName}, secret)
	require.NoError(t, err)
	return secret
}

func TestRenderedSecret_PersistAndLoadAcrossReconcilers(t *testing.T) {
	ctx := newTestContext()
	c, _ := newFakeClient(mgrScheme, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: updateTestNamespace}})

	r := newUpdateTestReconciler(t, c, mgrScheme)
	ms := newUpdateMiniService(`{"key":"value"}`)
	ms.Status.Revision = 0
	rendered := newUpdateRenderedData(t, "workload-cm", "v1")

	r.saveRenderedData(ctx, ms, rendered)
	require.NotNil(t, ms.Status.RenderDetails)
	require.NoError(t, r.persistRenderedData(ctx, ms, rendered))

	secret := getRenderedSecret(t, c, updateTestNamespace)
	assert.Equal(t, renderedSecretType, secret.Type)
	assert.Equal(t, managedByValue, secret.Labels[managedByLabel])
	assert.Equal(t, ms.Name, secret.Labels[miniserviceNameLabel])
	assert.Equal(t, "0", secret.Labels[revisionLabel])
	assert.Equal(t, ms.Status.RenderDetails.Hash, secret.Annotations[renderedSecretOutputHashAnnotation])
	assert.Equal(t, renderInputHash(ms), secret.Annotations[renderedSecretInputHashAnnotation])
	assert.Equal(t, ms.Spec.HelmChartConfig.URL, secret.Annotations[renderedSecretChartURLAnnotation])
	require.Len(t, secret.OwnerReferences, 1)
	assert.Equal(t, miniServiceKind, secret.OwnerReferences[0].Kind)
	assert.Equal(t, ms.UID, secret.OwnerReferences[0].UID)
	data, err := gunzipBytes(secret.Data[renderedSecretDataKey])
	require.NoError(t, err)
	assert.JSONEq(t, string(rendered), string(data))

	// Persisting again is a no-op that does not error.
	rv := secret.ResourceVersion
	require.NoError(t, r.persistRenderedData(ctx, ms, rendered))
	assert.Equal(t, rv, getRenderedSecret(t, c, updateTestNamespace).ResourceVersion)

	// A fresh reconciler (simulating an agent restart) loads the render from the Secret.
	r2 := newUpdateTestReconciler(t, c, mgrScheme)
	got, found, err := r2.getRenderedData(ctx, ms)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, rendered, got)
	// And the second read is served from memory.
	entry, ok := r2.loadRenderedEntry(ms)
	require.True(t, ok)
	assert.True(t, entry.synced)
}

func TestRenderedSecret_IgnoredWhenInputsOrHashDiffer(t *testing.T) {
	ctx := newTestContext()
	rendered := newUpdateRenderedData(t, "workload-cm", "v1")

	persist := func(t *testing.T) (client.Client, *v1alpha1.MiniService) {
		t.Helper()
		c, _ := newFakeClient(mgrScheme, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: updateTestNamespace}})
		r := newUpdateTestReconciler(t, c, mgrScheme)
		ms := newUpdateMiniService(`{"key":"value"}`)
		r.saveRenderedData(ctx, ms, rendered)
		require.NoError(t, r.persistRenderedData(ctx, ms, rendered))
		return c, ms
	}

	t.Run("helm values changed", func(t *testing.T) {
		c, ms := persist(t)
		ms.Spec.HelmChartConfig.Values = []byte(`{"key":"changed"}`)
		r := newUpdateTestReconciler(t, c, mgrScheme)
		_, found, err := r.getRenderedData(ctx, ms)
		require.NoError(t, err)
		assert.False(t, found)
	})

	t.Run("namespace changed", func(t *testing.T) {
		c, ms := persist(t)
		require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "other-ns"}}))
		secret := getRenderedSecret(t, c, updateTestNamespace)
		secret.ResourceVersion = ""
		secret.Namespace = "other-ns"
		require.NoError(t, c.Create(ctx, secret))
		ms.Spec.Namespace = "other-ns"
		r := newUpdateTestReconciler(t, c, mgrScheme)
		_, found, err := r.getRenderedData(ctx, ms)
		require.NoError(t, err)
		assert.False(t, found, "a render for another namespace must not be reused")
	})

	t.Run("status hash differs", func(t *testing.T) {
		c, ms := persist(t)
		ms.Status.RenderDetails.Hash = "sha256:0000"
		r := newUpdateTestReconciler(t, c, mgrScheme)
		_, found, err := r.getRenderedData(ctx, ms)
		require.NoError(t, err)
		assert.False(t, found)
	})

	t.Run("content corrupted", func(t *testing.T) {
		c, ms := persist(t)
		secret := getRenderedSecret(t, c, updateTestNamespace)
		corrupted, err := gzipBytes([]byte(`[{"kind":"ConfigMap"}]`))
		require.NoError(t, err)
		secret.Data[renderedSecretDataKey] = corrupted
		require.NoError(t, c.Update(ctx, secret))
		r := newUpdateTestReconciler(t, c, mgrScheme)
		_, found, err := r.getRenderedData(ctx, ms)
		require.NoError(t, err)
		assert.False(t, found, "content not matching its hash must not be trusted")
	})

	t.Run("in-memory entry for stale inputs is discarded", func(t *testing.T) {
		c, _ := newFakeClient(mgrScheme, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: updateTestNamespace}})
		r := newUpdateTestReconciler(t, c, mgrScheme)
		ms := newUpdateMiniService(`{"key":"value"}`)
		r.saveRenderedData(ctx, ms, rendered)
		ms.Spec.HelmChartConfig.Values = []byte(`{"key":"changed"}`)
		_, found, err := r.getRenderedData(ctx, ms)
		require.NoError(t, err)
		assert.False(t, found)
		_, ok := r.loadRenderedEntry(ms)
		assert.False(t, ok)
	})

	t.Run("no render details reuses matching secret and restores hash", func(t *testing.T) {
		c, ms := persist(t)
		storedHash := ms.Status.RenderDetails.Hash
		ms.Status.RenderDetails = nil
		r := newUpdateTestReconciler(t, c, mgrScheme)
		got, found, err := r.getRenderedData(ctx, ms)
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, rendered, got)
		require.NotNil(t, ms.Status.RenderDetails)
		assert.Equal(t, storedHash, ms.Status.RenderDetails.Hash)
	})

	t.Run("no render details and no secret", func(t *testing.T) {
		c, _ := newFakeClient(mgrScheme, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: updateTestNamespace}})
		ms := newUpdateMiniService(`{"key":"value"}`)
		r := newUpdateTestReconciler(t, c, mgrScheme)
		_, found, err := r.getRenderedData(ctx, ms)
		require.NoError(t, err)
		assert.False(t, found)
		assert.Nil(t, ms.Status.RenderDetails)
	})
}

func TestRenderedSecret_UpdateOverwritesPreviousRevision(t *testing.T) {
	ctx := newTestContext()
	c, _ := newFakeClient(mgrScheme, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: updateTestNamespace}})
	r := newUpdateTestReconciler(t, c, mgrScheme)

	ms := newUpdateMiniService(`{"key":"v1"}`)
	ms.Status.Revision = 0
	v1Data := newUpdateRenderedData(t, "workload-cm", "v1")
	r.saveRenderedData(ctx, ms, v1Data)
	require.NoError(t, r.persistRenderedData(ctx, ms, v1Data))
	v1Hash := ms.Status.RenderDetails.Hash

	// Simulate prepareUpdateIfNeeded followed by a new render.
	r.forgetRenderedData(ms)
	ms.Status.RenderDetails = nil
	ms.Status.Revision = 1
	ms.Spec.HelmChartConfig.Values = []byte(`{"key":"v2"}`)
	_, found, err := r.getRenderedData(ctx, ms)
	require.NoError(t, err)
	require.False(t, found)

	v2Data := newUpdateRenderedData(t, "workload-cm", "v2")
	r.saveRenderedData(ctx, ms, v2Data)
	require.NoError(t, r.persistRenderedData(ctx, ms, v2Data))
	require.NotEqual(t, v1Hash, ms.Status.RenderDetails.Hash)

	secrets := &corev1.SecretList{}
	require.NoError(t, c.List(ctx, secrets, client.InNamespace(updateTestNamespace)))
	require.Len(t, secrets.Items, 1, "the rendered Secret is overwritten, not duplicated")
	secret := secrets.Items[0]
	assert.Equal(t, "1", secret.Labels[revisionLabel])
	assert.Equal(t, ms.Status.RenderDetails.Hash, secret.Annotations[renderedSecretOutputHashAnnotation])
	data, err := gunzipBytes(secret.Data[renderedSecretDataKey])
	require.NoError(t, err)
	assert.JSONEq(t, string(v2Data), string(data))
}

func TestRenderedSecret_ReplacesSecretOfDifferentType(t *testing.T) {
	ctx := newTestContext()
	stale := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: RenderedSecretName, Namespace: updateTestNamespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"old": []byte("format")},
	}
	c, _ := newFakeClient(mgrScheme, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: updateTestNamespace}}, stale)
	r := newUpdateTestReconciler(t, c, mgrScheme)
	ms := newUpdateMiniService(`{"key":"value"}`)
	rendered := newUpdateRenderedData(t, "workload-cm", "v1")

	r.saveRenderedData(ctx, ms, rendered)
	require.NoError(t, r.persistRenderedData(ctx, ms, rendered))

	secret := getRenderedSecret(t, c, updateTestNamespace)
	assert.Equal(t, renderedSecretType, secret.Type)
	assert.NotContains(t, secret.Data, "old")
	data, err := gunzipBytes(secret.Data[renderedSecretDataKey])
	require.NoError(t, err)
	assert.JSONEq(t, string(rendered), string(data))
}

func TestRenderedSecret_TooLargeIsSkipped(t *testing.T) {
	ctx := newTestContext()
	c, _ := newFakeClient(mgrScheme, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: updateTestNamespace}})
	r := newUpdateTestReconciler(t, c, mgrScheme)
	ms := newUpdateMiniService(`{"key":"value"}`)

	// Random bytes do not compress, so this exceeds the Secret size guard.
	large := make([]byte, renderedSecretMaxCompressedBytes+64<<10)
	_, err := rand.Read(large)
	require.NoError(t, err)

	r.saveRenderedData(ctx, ms, large)
	require.NoError(t, r.persistRenderedData(ctx, ms, large), "oversize renders are skipped, not failed")

	secrets := &corev1.SecretList{}
	require.NoError(t, c.List(ctx, secrets, client.InNamespace(updateTestNamespace)))
	assert.Empty(t, secrets.Items)

	// Still served from memory in this process, but a fresh reconciler falls back to rendering.
	got, found, err := r.getRenderedData(ctx, ms)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, large, got)

	r2 := newUpdateTestReconciler(t, c, mgrScheme)
	_, found, err = r2.getRenderedData(ctx, ms)
	require.NoError(t, err)
	assert.False(t, found)
}

func TestRenderedSecret_PersistErrorsAreReturned(t *testing.T) {
	ctx := newTestContext()
	c, _ := newFakeClientWithInterceptors(mgrScheme, interceptor.Funcs{
		Create: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.CreateOption) error {
			return apierrors.NewInternalError(fmt.Errorf("injected create failure for %s", obj.GetName()))
		},
	}, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: updateTestNamespace}})
	r := newUpdateTestReconciler(t, c, mgrScheme)
	ms := newUpdateMiniService(`{"key":"value"}`)
	rendered := newUpdateRenderedData(t, "workload-cm", "v1")

	r.saveRenderedData(ctx, ms, rendered)
	err := r.persistRenderedData(ctx, ms, rendered)
	require.Error(t, err)
	entry, ok := r.loadRenderedEntry(ms)
	require.True(t, ok)
	assert.False(t, entry.synced, "a failed persist must be retried on the next reconcile")
}

func TestDoStatus_UsesPersistedRenderInsteadOfReVal(t *testing.T) {
	ctx := newTestContext()
	c, _ := newFakeClient(mgrScheme,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: updateTestNamespace}},
		newReadyUtilsPod(),
	)

	ms := newUpdateMiniService(`{"key":"value"}`)
	ms.Status.Phase = v1alpha1.MiniServiceRunning
	icmsReq := newUpdateICMSRequest(true)

	// First process renders and persists.
	r1 := newUpdateTestReconciler(t, c, mgrScheme)
	r1.saveRenderedData(ctx, ms, []byte("[]"))
	require.NoError(t, r1.persistRenderedData(ctx, ms, []byte("[]")))

	for _, workerReadiness := range []bool{false, true} {
		t.Run(map[bool]string{false: "aggressive status", true: "worker readiness status"}[workerReadiness], func(t *testing.T) {
			ms := ms.DeepCopy()
			if workerReadiness {
				ms.Spec.WorkloadConfig = &v1alpha1.WorkloadConfig{
					FeatureFlags: map[string]bool{featureflag.StatusByWorkerReadiness: true},
				}
			}
			// Second process (after an agent restart) has an empty memory cache and a broken ReVal.
			rv := &outputReValClient{err: errors.New("reval unavailable")}
			r2 := newUpdateTestReconciler(t, c, mgrScheme)
			r2.ReValClient = rv
			r2.statusCheckers = r2.makeStatusCheckers()

			_, err := r2.doStatus(ctx, ms, icmsReq)
			require.NoError(t, err)
			assert.Equal(t, 0, rv.calls, "status checks must not call ReVal when a persisted render exists")
			assert.Equal(t, v1alpha1.MiniServiceRunning, ms.Status.Phase)
		})
	}
}

func TestDoStatus_RenderFailureDoesNotFailRunningInstance(t *testing.T) {
	ctx := newTestContext()
	icmsReq := newUpdateICMSRequest(true)

	tests := []struct {
		name      string
		newClient func() *outputReValClient
	}{
		{
			name: "reval returns invalid chart",
			newClient: func() *outputReValClient {
				return &outputReValClient{output: HelmReValRenderOutput{Valid: newBool(false), ValidationErrors: []string{"bad"}}}
			},
		},
		{
			name: "reval returns non-retryable error",
			newClient: func() *outputReValClient {
				return &outputReValClient{err: reconcile.TerminalError(errors.New("400 bad request"))}
			},
		},
		{
			name: "reval returns transient error",
			newClient: func() *outputReValClient {
				return &outputReValClient{err: errors.New("503 unavailable")}
			},
		},
	}
	for _, tt := range tests {
		for _, workerReadiness := range []bool{false, true} {
			name := tt.name + map[bool]string{false: " (aggressive status)", true: " (worker readiness status)"}[workerReadiness]
			t.Run(name, func(t *testing.T) {
				c, _ := newFakeClient(mgrScheme,
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: updateTestNamespace}},
					newReadyUtilsPod(),
				)
				ms := newUpdateMiniService(`{"key":"value"}`)
				ms.Status.Phase = v1alpha1.MiniServiceRunning
				// The instance was rendered in the past, but the persisted render is gone.
				ms.Status.RenderDetails = &v1alpha1.RenderDetailsStatus{Hash: "sha256:previous"}
				meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
					Type:   v1alpha1.MiniServiceConditionInstallSuccessful,
					Status: metav1.ConditionTrue,
					Reason: "Installed",
				})
				if workerReadiness {
					ms.Spec.WorkloadConfig = &v1alpha1.WorkloadConfig{
						FeatureFlags: map[string]bool{featureflag.StatusByWorkerReadiness: true},
					}
				}

				rv := tt.newClient()
				r := newUpdateTestReconciler(t, c, mgrScheme)
				r.ReValClient = rv
				r.statusCheckers = r.makeStatusCheckers()

				_, err := r.doStatus(ctx, ms, icmsReq)
				require.Error(t, err)
				assert.False(t, isTerminal(err), "re-render failures during status must be retryable: %v", err)
				assert.Equal(t, 1, rv.calls)
				assert.Equal(t, v1alpha1.MiniServiceRunning, ms.Status.Phase)
				cond := meta.FindStatusCondition(ms.Status.Conditions, v1alpha1.MiniServiceConditionInstallSuccessful)
				require.NotNil(t, cond)
				assert.Equal(t, metav1.ConditionTrue, cond.Status, "install condition must not be rewritten by a status re-render")
			})
		}
	}
}

func TestRenderInputHash(t *testing.T) {
	base := newUpdateMiniService(`{"a": 1}`)
	same := newUpdateMiniService(`{"a": 1}`)
	assert.Equal(t, renderInputHash(base), renderInputHash(same))

	for name, mutate := range map[string]func(*v1alpha1.MiniService){
		"values":       func(ms *v1alpha1.MiniService) { ms.Spec.HelmChartConfig.Values = []byte(`{"a": 2}`) },
		"chart url":    func(ms *v1alpha1.MiniService) { ms.Spec.HelmChartConfig.URL = "https://example.test/other.tgz" },
		"service name": func(ms *v1alpha1.MiniService) { ms.Spec.HelmChartConfig.ServiceName = "svc" },
		"service port": func(ms *v1alpha1.MiniService) { p := int32(8080); ms.Spec.HelmChartConfig.ServicePort = &p },
		"namespace":    func(ms *v1alpha1.MiniService) { ms.Spec.Namespace = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			ms := newUpdateMiniService(`{"a": 1}`)
			mutate(ms)
			assert.NotEqual(t, renderInputHash(base), renderInputHash(ms))
		})
	}

	// Unrelated spec fields do not affect the hash.
	other := newUpdateMiniService(`{"a": 1}`)
	other.Spec.ICMSRequestName = "other-request"
	assert.Equal(t, renderInputHash(base), renderInputHash(other))
}
