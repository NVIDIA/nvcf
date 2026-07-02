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
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/miniservice/chartcache"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
)

func testHelmConfig(url, values string) common.HelmConfig {
	return common.HelmConfig{
		URL:    url,
		Values: json.RawMessage(values),
	}
}

func TestPrepareUpgradeIfNeeded(t *testing.T) {
	testScheme := mgrScheme
	ctx := newTestContext()

	fixedTime := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	nsName := "test-ns"
	msName := "test-ms"

	tests := []struct {
		name               string
		generation         int64
		observedGeneration int64
		revision           int64
		specValues         string
		phase              v1alpha1.MiniServicePhase
		existingCMs        []*corev1.ConfigMap
		wantUpgrade        bool
		wantRevision       int64
		wantPhase          v1alpha1.MiniServicePhase
	}{
		{
			name:               "initial install, observedGeneration is 0",
			generation:         1,
			observedGeneration: 0,
			revision:           0,
			specValues:         `{"key": "initial"}`,
			phase:              v1alpha1.MiniServiceInstalling,
			wantUpgrade:        false,
			wantRevision:       0,
			wantPhase:          v1alpha1.MiniServiceInstalling,
		},
		{
			name:               "same generation, no upgrade",
			generation:         2,
			observedGeneration: 2,
			revision:           1,
			specValues:         `{"key": "same"}`,
			phase:              v1alpha1.MiniServiceRunning,
			wantUpgrade:        false,
			wantRevision:       1,
			wantPhase:          v1alpha1.MiniServiceRunning,
		},
		{
			name:               "generation mismatch, values changed",
			generation:         3,
			observedGeneration: 2,
			revision:           1,
			specValues:         `{"key": "new-value"}`,
			phase:              v1alpha1.MiniServiceRunning,
			existingCMs: []*corev1.ConfigMap{
				revisionCMHelper(nsName, msName, 1, `{"key": "old-value"}`),
			},
			wantUpgrade:  true,
			wantRevision: 2,
			wantPhase:    v1alpha1.MiniServiceInstalling,
		},
		{
			name:               "generation mismatch, values unchanged -- skip upgrade",
			generation:         3,
			observedGeneration: 2,
			revision:           1,
			specValues:         `{"key": "same-value"}`,
			phase:              v1alpha1.MiniServiceRunning,
			existingCMs: []*corev1.ConfigMap{
				revisionCMHelper(nsName, msName, 1, `{"key": "same-value"}`),
			},
			wantUpgrade:  false,
			wantRevision: 1,
			wantPhase:    v1alpha1.MiniServiceRunning,
		},
		{
			name:               "generation mismatch, no prior revision CM -- assume changed",
			generation:         2,
			observedGeneration: 1,
			revision:           0,
			specValues:         `{"key": "value"}`,
			phase:              v1alpha1.MiniServiceRunning,
			wantUpgrade:        true,
			wantRevision:       1,
			wantPhase:          v1alpha1.MiniServiceInstalling,
		},
		{
			name:               "picks latest revision for comparison",
			generation:         4,
			observedGeneration: 3,
			revision:           2,
			specValues:         `{"key": "v2-value"}`,
			phase:              v1alpha1.MiniServiceRunning,
			existingCMs: []*corev1.ConfigMap{
				revisionCMHelper(nsName, msName, 0, `{"key": "v0-value"}`),
				revisionCMHelper(nsName, msName, 1, `{"key": "v1-value"}`),
				revisionCMHelper(nsName, msName, 2, `{"key": "v2-value"}`),
			},
			wantUpgrade:  false,
			wantRevision: 2,
			wantPhase:    v1alpha1.MiniServiceRunning,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: nsName},
			}
			ms := &v1alpha1.MiniService{
				ObjectMeta: metav1.ObjectMeta{
					Name:       msName,
					Generation: tt.generation,
					UID:        types.UID("test-uid-123"),
				},
				Spec: v1alpha1.MiniServiceSpec{
					Namespace:       nsName,
					HelmChartConfig: testHelmConfig("https://helm.example.com/chart.tgz", tt.specValues),
				},
				Status: v1alpha1.MiniServiceStatus{
					Phase:              tt.phase,
					ObservedGeneration: tt.observedGeneration,
					Revision:           tt.revision,
					RenderDetails: &v1alpha1.RenderDetailsStatus{
						Hash: "abc123",
					},
				},
			}

			objs := []client.Object{ms, ns}
			for _, cm := range tt.existingCMs {
				objs = append(objs, cm)
			}
			c, _ := newFakeClient(testScheme, objs...)

			cc := chartcache.New(t.TempDir())
			require.NoError(t, cc.Start(ctx))

			r := &Reconciler{
				ControllerOptions: ControllerOptions{
					FeatureFlagFetcher: &featureflagmock.Fetcher{
						EnabledFFs: []*featureflag.FeatureFlag{
							featureflag.MiniServiceRevisionHistory,
						},
					},
				},
				Client:     c,
				chartCache: cc,
				now:        func() time.Time { return fixedTime },
			}

			err := r.prepareUpdateIfNeeded(ctx, ms)
			require.NoError(t, err)

			assert.Equal(t, tt.wantRevision, ms.Status.Revision)
			assert.Equal(t, tt.wantPhase, ms.Status.Phase)
			if tt.wantUpgrade {
				assert.Nil(t, ms.Status.RenderDetails)
			}
		})
	}
}

func TestHelmValuesChanged(t *testing.T) {
	testScheme := mgrScheme
	ctx := newTestContext()
	nsName := "val-ns"
	msName := "val-ms"

	tests := []struct {
		name        string
		specValues  string
		existingCMs []*corev1.ConfigMap
		wantChanged bool
	}{
		{
			name:        "no revision CMs, assume changed",
			specValues:  `{"a": 1}`,
			wantChanged: true,
		},
		{
			name:       "values match latest revision",
			specValues: `{"a": 1}`,
			existingCMs: []*corev1.ConfigMap{
				revisionCMHelper(nsName, msName, 0, `{"a": 1}`),
			},
			wantChanged: false,
		},
		{
			name:       "values differ from latest revision",
			specValues: `{"a": 2}`,
			existingCMs: []*corev1.ConfigMap{
				revisionCMHelper(nsName, msName, 0, `{"a": 1}`),
			},
			wantChanged: true,
		},
		{
			name:       "compares against highest revision, not first",
			specValues: `{"a": "old"}`,
			existingCMs: []*corev1.ConfigMap{
				revisionCMHelper(nsName, msName, 0, `{"a": "old"}`),
				revisionCMHelper(nsName, msName, 1, `{"a": "new"}`),
			},
			wantChanged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: nsName},
			}
			ms := &v1alpha1.MiniService{
				ObjectMeta: metav1.ObjectMeta{Name: msName},
				Spec: v1alpha1.MiniServiceSpec{
					Namespace:       nsName,
					HelmChartConfig: testHelmConfig("https://example.com/chart.tgz", tt.specValues),
				},
			}

			objs := []client.Object{ms, ns}
			for _, cm := range tt.existingCMs {
				objs = append(objs, cm)
			}
			c, _ := newFakeClient(testScheme, objs...)
			r := &Reconciler{Client: c}

			changed, err := r.helmValuesChanged(ctx, ms)
			require.NoError(t, err)
			assert.Equal(t, tt.wantChanged, changed)
		})
	}
}

func TestLatestRevisionConfigMap(t *testing.T) {
	cms := []corev1.ConfigMap{
		{ObjectMeta: metav1.ObjectMeta{Name: "v0", Labels: map[string]string{revisionLabel: "0"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "v2", Labels: map[string]string{revisionLabel: "2"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "v1", Labels: map[string]string{revisionLabel: "1"}}},
	}
	latest := latestRevisionConfigMap(cms)
	require.NotNil(t, latest)
	assert.Equal(t, "v2", latest.Name)
}

func TestLatestRevisionConfigMap_Empty(t *testing.T) {
	latest := latestRevisionConfigMap(nil)
	assert.Nil(t, latest)
}

func TestSaveRevisionHistory(t *testing.T) {
	testScheme := mgrScheme
	ctx := newTestContext()
	fixedTime := time.Date(2025, 6, 1, 10, 30, 0, 0, time.UTC)

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "ms-namespace"},
	}
	ms := &v1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-service",
			UID:  types.UID("uid-456"),
		},
		Spec: v1alpha1.MiniServiceSpec{
			Namespace:       ns.Name,
			HelmChartConfig: testHelmConfig("https://charts.example.com/app.tgz", `{"replicas": 3}`),
		},
		Status: v1alpha1.MiniServiceStatus{
			Revision: 2,
			RenderDetails: &v1alpha1.RenderDetailsStatus{
				Hash: "hash-v2",
			},
		},
	}

	c, _ := newFakeClient(testScheme, ms, ns)
	r := &Reconciler{
		ControllerOptions: ControllerOptions{
			FeatureFlagFetcher: &featureflagmock.Fetcher{
				EnabledFFs: []*featureflag.FeatureFlag{
					featureflag.MiniServiceRevisionHistory,
				},
			},
		},
		Client: c,
		now:    func() time.Time { return fixedTime },
	}

	err := r.saveRevisionHistory(ctx, ms)
	require.NoError(t, err)

	cm := &corev1.ConfigMap{}
	err = c.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: "miniservice-revision-v2"}, cm)
	require.NoError(t, err)

	assert.Equal(t, `{"replicas": 3}`, cm.Data[revisionDataKeyValues])
	assert.Equal(t, "hash-v2", cm.Data[revisionDataKeyRenderHash])
	assert.Equal(t, "https://charts.example.com/app.tgz", cm.Data[revisionDataKeyChartURL])
	assert.Equal(t, "2025-06-01T10:30:00Z", cm.Data[revisionDataKeyTimestamp])

	assert.Equal(t, "my-service", cm.Labels[miniserviceNameLabel])
	assert.Equal(t, "2", cm.Labels[revisionLabel])
	assert.Equal(t, managedByValue, cm.Labels["app.kubernetes.io/managed-by"])

	require.Len(t, cm.OwnerReferences, 1)
	assert.Equal(t, "my-service", cm.OwnerReferences[0].Name)
	assert.Equal(t, types.UID("uid-456"), cm.OwnerReferences[0].UID)
}

func TestSaveRevisionHistory_Disabled(t *testing.T) {
	ctx := newTestContext()
	r := &Reconciler{
		ControllerOptions: ControllerOptions{
			FeatureFlagFetcher: &featureflagmock.Fetcher{
				EnabledFFs: []*featureflag.FeatureFlag{},
			},
		},
	}

	ms := &v1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{Name: "ignored"},
		Status: v1alpha1.MiniServiceStatus{
			Revision: 5,
		},
	}

	err := r.saveRevisionHistory(ctx, ms)
	require.NoError(t, err)
}

func TestSaveRevisionHistory_CreateFails(t *testing.T) {
	testScheme := mgrScheme
	ctx := newTestContext()
	fixedTime := time.Date(2025, 8, 20, 14, 0, 0, 0, time.UTC)

	nsName := "fail-ns"
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: nsName},
	}
	ms := &v1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{
			Name: "fail-svc",
			UID:  types.UID("uid-fail"),
		},
		Spec: v1alpha1.MiniServiceSpec{
			Namespace:       nsName,
			HelmChartConfig: testHelmConfig("https://charts.example.com/app.tgz", `{"replicas": 2}`),
		},
		Status: v1alpha1.MiniServiceStatus{
			Phase:    v1alpha1.MiniServiceInstalled,
			Revision: 0,
			RenderDetails: &v1alpha1.RenderDetailsStatus{
				Hash: "hash-v0",
			},
		},
	}

	injectedErr := fmt.Errorf("simulated API server error")
	c, _ := newFakeClientWithInterceptors(testScheme,
		interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*corev1.ConfigMap); ok {
					return injectedErr
				}
				return c.Create(ctx, obj, opts...)
			},
		},
		ms, ns,
	)
	r := &Reconciler{
		ControllerOptions: ControllerOptions{
			FeatureFlagFetcher: &featureflagmock.Fetcher{
				EnabledFFs: []*featureflag.FeatureFlag{
					featureflag.MiniServiceRevisionHistory,
				},
			},
		},
		Client: c,
		now:    func() time.Time { return fixedTime },
	}

	err := r.saveRevisionHistory(ctx, ms)
	require.Error(t, err)
	assert.ErrorIs(t, err, injectedErr)

	assert.Equal(t, v1alpha1.MiniServiceInstalled, ms.Status.Phase,
		"phase should remain unchanged when saveRevisionHistory fails")
	assert.Equal(t, int64(0), ms.Status.Revision,
		"revision should remain unchanged when saveRevisionHistory fails")
}

func TestRevisionUpgradeCycle(t *testing.T) {
	testScheme := mgrScheme
	ctx := newTestContext()
	fixedTime := time.Date(2025, 7, 10, 8, 0, 0, 0, time.UTC)

	nsName := "cycle-ns"
	msName := "cycle-ms"
	chartURL := "https://charts.example.com/myapp.tgz"

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: nsName},
	}
	ms := &v1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{
			Name:       msName,
			Generation: 1,
			UID:        types.UID("cycle-uid"),
		},
		Spec: v1alpha1.MiniServiceSpec{
			Namespace:       nsName,
			HelmChartConfig: testHelmConfig(chartURL, `{"replicas": 1}`),
		},
		Status: v1alpha1.MiniServiceStatus{
			Phase:    v1alpha1.MiniServiceInstalled,
			Revision: 0,
			RenderDetails: &v1alpha1.RenderDetailsStatus{
				Hash: "hash-v0",
			},
		},
	}

	c, _ := newFakeClient(testScheme, ms, ns)
	cc := chartcache.New(t.TempDir())
	require.NoError(t, cc.Start(ctx))

	r := &Reconciler{
		ControllerOptions: ControllerOptions{
			FeatureFlagFetcher: &featureflagmock.Fetcher{
				EnabledFFs: []*featureflag.FeatureFlag{
					featureflag.MiniServiceRevisionHistory,
				},
			},
		},
		Client:     c,
		chartCache: cc,
		now:        func() time.Time { return fixedTime },
	}

	// Step 1: save revision 0 after initial install.
	require.NoError(t, r.saveRevisionHistory(ctx, ms))

	v0CM := &corev1.ConfigMap{}
	err := c.Get(ctx, client.ObjectKey{Namespace: nsName, Name: "miniservice-revision-v0"}, v0CM)
	require.NoError(t, err, "revision 0 ConfigMap should exist")
	assert.Equal(t, `{"replicas": 1}`, v0CM.Data[revisionDataKeyValues])
	assert.Equal(t, "hash-v0", v0CM.Data[revisionDataKeyRenderHash])

	// Step 2: simulate a spec update — new helm values and bumped generation.
	ms.Spec.HelmChartConfig.Values = json.RawMessage(`{"replicas": 3}`)
	ms.Generation = 2
	ms.Status.ObservedGeneration = 1

	// Step 3: detect the upgrade.
	changed, err := r.helmValuesChanged(ctx, ms)
	require.NoError(t, err)
	assert.True(t, changed, "new values should differ from revision 0")

	require.NoError(t, r.prepareUpdateIfNeeded(ctx, ms))
	assert.Equal(t, int64(1), ms.Status.Revision)
	assert.Equal(t, v1alpha1.MiniServiceInstalling, ms.Status.Phase)
	assert.Nil(t, ms.Status.RenderDetails, "render details should be cleared for re-render")

	// Step 4: save revision 1 after successful re-apply.
	ms.Status.RenderDetails = &v1alpha1.RenderDetailsStatus{Hash: "hash-v1"}
	require.NoError(t, r.saveRevisionHistory(ctx, ms))

	v1CM := &corev1.ConfigMap{}
	err = c.Get(ctx, client.ObjectKey{Namespace: nsName, Name: "miniservice-revision-v1"}, v1CM)
	require.NoError(t, err, "revision 1 ConfigMap should exist")
	assert.Equal(t, `{"replicas": 3}`, v1CM.Data[revisionDataKeyValues])
	assert.Equal(t, "hash-v1", v1CM.Data[revisionDataKeyRenderHash])
	assert.Equal(t, chartURL, v1CM.Data[revisionDataKeyChartURL])
	assert.Equal(t, "1", v1CM.Labels[revisionLabel])
	assert.Equal(t, msName, v1CM.Labels[miniserviceNameLabel])
	require.Len(t, v1CM.OwnerReferences, 1)
	assert.Equal(t, types.UID("cycle-uid"), v1CM.OwnerReferences[0].UID)

	// Step 5: verify revision 0 is still intact.
	err = c.Get(ctx, client.ObjectKey{Namespace: nsName, Name: "miniservice-revision-v0"}, v0CM)
	require.NoError(t, err, "revision 0 ConfigMap should still exist")
	assert.Equal(t, `{"replicas": 1}`, v0CM.Data[revisionDataKeyValues])

	// Step 6: verify listing returns both revisions.
	cmList, err := r.listRevisionHistory(ctx, ms)
	require.NoError(t, err)
	assert.Len(t, cmList.Items, 2)

	// Step 7: with the same values a second time, no upgrade should trigger.
	ms.Generation = 3
	ms.Status.ObservedGeneration = 2
	ms.Status.Phase = v1alpha1.MiniServiceRunning

	changed, err = r.helmValuesChanged(ctx, ms)
	require.NoError(t, err)
	assert.False(t, changed, "values unchanged since revision 1")

	require.NoError(t, r.prepareUpdateIfNeeded(ctx, ms))
	assert.Equal(t, int64(1), ms.Status.Revision, "revision should not bump when values are unchanged")
	assert.Equal(t, v1alpha1.MiniServiceRunning, ms.Status.Phase, "phase should remain Running")
}

func TestListRevisionHistory(t *testing.T) {
	testScheme := mgrScheme
	ctx := newTestContext()

	nsName := "rev-ns"
	msName := "svc-a"

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: nsName},
	}
	ms := &v1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{Name: msName},
		Spec:       v1alpha1.MiniServiceSpec{Namespace: nsName},
	}

	unrelated := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "other-configmap",
			Namespace: nsName,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": managedByValue,
				miniserviceNameLabel:           "different-ms",
			},
		},
	}

	c, _ := newFakeClient(testScheme, ms, ns,
		revisionCMHelper(nsName, msName, 0, `{"v": 0}`),
		revisionCMHelper(nsName, msName, 1, `{"v": 1}`),
		unrelated,
	)
	r := &Reconciler{Client: c}

	cmList, err := r.listRevisionHistory(ctx, ms)
	require.NoError(t, err)
	assert.Len(t, cmList.Items, 2, "should only return ConfigMaps for svc-a")

	names := make([]string, len(cmList.Items))
	for i, cm := range cmList.Items {
		names[i] = cm.Name
	}
	assert.Contains(t, names, "miniservice-revision-v0")
	assert.Contains(t, names, "miniservice-revision-v1")
}

// revisionCMHelper builds a revision ConfigMap for test setup.
func revisionCMHelper(namespace, msName string, revision int64, values string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "miniservice-revision-v" + strconv.FormatInt(revision, 10),
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": managedByValue,
				miniserviceNameLabel:           msName,
				revisionLabel:                  strconv.FormatInt(revision, 10),
			},
		},
		Data: map[string]string{revisionDataKeyValues: values},
	}
}
