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
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	controllerfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const splitTransitionCatalog = `apiVersion: storage.nvcf.nvidia.com/v1alpha1
kind: StorageCapabilityCatalog
drivers:
  nvmesh-csi.excelero.com:
    provider: nvmesh
    accessModes: [ReadWriteOnce, ReadOnlyMany]
    readerMountOptions: [ro, norecovery, nouuid]
    transitions:
      regularModelCache: roxReadOnly
      helmModelCache: disabled
`

func testModelCacheStorageClass() *storagev1.StorageClass {
	retain := corev1.PersistentVolumeReclaimRetain
	wait := storagev1.VolumeBindingWaitForFirstConsumer
	expand := true
	return &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:        DefaultModelCacheStorageClassName,
			UID:         types.UID("nvcf-sc-uid"),
			Labels:      map[string]string{"test-metadata": "excluded-from-digest"},
			Annotations: map[string]string{"test-metadata": "excluded-from-digest"},
		},
		Provisioner: NVMeshStorageClassProvisioner,
		Parameters: map[string]string{
			"z-option": "last",
			"a-option": "first",
		},
		ReclaimPolicy:        &retain,
		MountOptions:         []string{"nouuid", "noatime"},
		AllowVolumeExpansion: &expand,
		VolumeBindingMode:    &wait,
		AllowedTopologies: []corev1.TopologySelectorTerm{{
			MatchLabelExpressions: []corev1.TopologySelectorLabelRequirement{{
				Key:    "topology.kubernetes.io/zone",
				Values: []string{"zone-b", "zone-a"},
			}},
		}},
	}
}

func storageResolutionClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, storagev1.AddToScheme(scheme))
	return controllerfake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func TestResolveModelCacheStorageSelectsWorkflowTransition(t *testing.T) {
	sc := testModelCacheStorageClass()
	cm := capabilityCatalogConfigMap(splitTransitionCatalog)
	c := storageResolutionClient(t, sc, cm)
	sum := sha256.Sum256([]byte(splitTransitionCatalog))
	wantCatalogRevision := fmt.Sprintf("sha256:%x", sum)

	tests := []struct {
		name           string
		workflow       ModelCacheWorkflow
		wantTransition string
	}{
		{name: "regular NVMesh transition", workflow: ModelCacheWorkflowRegular, wantTransition: ModelCacheTransitionROXReadOnly},
		{name: "disabled Helm transition", workflow: ModelCacheWorkflowHelm, wantTransition: ModelCacheTransitionDisabled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selection, err := ResolveModelCacheStorage(t.Context(), c, testCatalogNamespace, tt.workflow)
			require.NoError(t, err)
			assert.Equal(t, DefaultModelCacheStorageClassName, selection.StorageClassName)
			assert.Equal(t, sc.UID, selection.StorageClassUID)
			assert.Equal(t, digestStorageClass(sc), selection.StorageClassDigest)
			assert.Equal(t, wantCatalogRevision, selection.CatalogRevision,
				"the exact catalog payload is recorded as audit metadata")
			assert.Regexp(t, `^sha256:[a-f0-9]{64}$`, selection.ProfileDigest)
			assert.Equal(t, "nvmesh", selection.Provider)
			assert.Equal(t, NVMeshStorageClassProvisioner, selection.Provisioner)
			assert.Equal(t, tt.wantTransition, selection.Transition)
			if tt.wantTransition == ModelCacheTransitionROXReadOnly {
				assert.Equal(t, []string{"ro", "norecovery", "nouuid"}, selection.RequiredMountOptions)
			} else {
				assert.Empty(t, selection.RequiredMountOptions)
			}
		})
	}
}

func TestResolveModelCacheStorageErrors(t *testing.T) {
	deletePolicy := corev1.PersistentVolumeReclaimDelete
	tests := []struct {
		name      string
		namespace string
		workflow  ModelCacheWorkflow
		objects   func() []client.Object
		want      string
		notFound  bool
	}{
		{
			name:      "missing exact StorageClass",
			namespace: testCatalogNamespace,
			workflow:  ModelCacheWorkflowRegular,
			objects: func() []client.Object {
				return []client.Object{capabilityCatalogConfigMap(validCatalog)}
			},
			want:     "model cache StorageClass not found",
			notFound: true,
		},
		{
			name:      "different StorageClass name is not selected",
			namespace: testCatalogNamespace,
			workflow:  ModelCacheWorkflowRegular,
			objects: func() []client.Object {
				sc := testModelCacheStorageClass()
				sc.Name = "another-class"
				return []client.Object{sc, capabilityCatalogConfigMap(validCatalog)}
			},
			want:     "model cache StorageClass not found",
			notFound: true,
		},
		{
			name:      "missing catalog ConfigMap",
			namespace: testCatalogNamespace,
			workflow:  ModelCacheWorkflowRegular,
			objects:   func() []client.Object { return []client.Object{testModelCacheStorageClass()} },
			want:      "get storage capability ConfigMap",
		},
		{
			name:      "catalog ConfigMap missing data key",
			namespace: testCatalogNamespace,
			workflow:  ModelCacheWorkflowRegular,
			objects: func() []client.Object {
				return []client.Object{
					testModelCacheStorageClass(),
					capabilityCatalogConfigMap(""),
				}
			},
			want: "has no",
		},
		{
			name:      "empty catalog namespace",
			namespace: "",
			workflow:  ModelCacheWorkflowRegular,
			objects: func() []client.Object {
				return []client.Object{testModelCacheStorageClass(), capabilityCatalogConfigMap(validCatalog)}
			},
			want: "namespace is empty",
		},
		{
			name:      "Delete reclaim policy",
			namespace: testCatalogNamespace,
			workflow:  ModelCacheWorkflowRegular,
			objects: func() []client.Object {
				sc := testModelCacheStorageClass()
				sc.ReclaimPolicy = &deletePolicy
				return []client.Object{sc, capabilityCatalogConfigMap(validCatalog)}
			},
			want: "must use reclaimPolicy Retain",
		},
		{
			name:      "missing reclaim policy",
			namespace: testCatalogNamespace,
			workflow:  ModelCacheWorkflowRegular,
			objects: func() []client.Object {
				sc := testModelCacheStorageClass()
				sc.ReclaimPolicy = nil
				return []client.Object{sc, capabilityCatalogConfigMap(validCatalog)}
			},
			want: "must use reclaimPolicy Retain",
		},
		{
			name:      "empty provisioner",
			namespace: testCatalogNamespace,
			workflow:  ModelCacheWorkflowRegular,
			objects: func() []client.Object {
				sc := testModelCacheStorageClass()
				sc.Provisioner = " \t"
				return []client.Object{sc, capabilityCatalogConfigMap(validCatalog)}
			},
			want: "has an empty provisioner",
		},
		{
			name:      "unknown provisioner",
			namespace: testCatalogNamespace,
			workflow:  ModelCacheWorkflowRegular,
			objects: func() []client.Object {
				sc := testModelCacheStorageClass()
				sc.Provisioner = "unknown.csi.example.com"
				return []client.Object{sc, capabilityCatalogConfigMap(validCatalog)}
			},
			want: "with no catalog entry",
		},
		{
			name:      "unknown workflow",
			namespace: testCatalogNamespace,
			workflow:  "containerCache",
			objects: func() []client.Object {
				return []client.Object{testModelCacheStorageClass(), capabilityCatalogConfigMap(validCatalog)}
			},
			want: "unknown model cache workflow",
		},
		{
			name:      "malformed catalog",
			namespace: testCatalogNamespace,
			workflow:  ModelCacheWorkflowRegular,
			objects: func() []client.Object {
				return []client.Object{testModelCacheStorageClass(), capabilityCatalogConfigMap("drivers: [")}
			},
			want: "parse storage capability catalog",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := storageResolutionClient(t, tt.objects()...)
			selection, err := ResolveModelCacheStorage(t.Context(), c, tt.namespace, tt.workflow)
			require.ErrorContains(t, err, tt.want)
			assert.Nil(t, selection)
			assert.Equal(t, tt.notFound, errors.Is(err, ErrModelCacheStorageClassNotFound))
		})
	}
}

func TestResolveModelCacheStorageWithClientset(t *testing.T) {
	sc := testModelCacheStorageClass()
	cm := capabilityCatalogConfigMap(splitTransitionCatalog)
	k8sClient := kubernetesfake.NewSimpleClientset(sc, cm)

	for _, tt := range []struct {
		workflow       ModelCacheWorkflow
		wantTransition string
	}{
		{workflow: ModelCacheWorkflowRegular, wantTransition: ModelCacheTransitionROXReadOnly},
		{workflow: ModelCacheWorkflowHelm, wantTransition: ModelCacheTransitionDisabled},
	} {
		selection, err := ResolveModelCacheStorageWithClientset(
			t.Context(), k8sClient, testCatalogNamespace, tt.workflow)
		require.NoError(t, err)
		assert.Equal(t, sc.UID, selection.StorageClassUID)
		assert.Equal(t, digestStorageClass(sc), selection.StorageClassDigest)
		assert.Equal(t, digestCatalogPayload(splitTransitionCatalog), selection.CatalogRevision)
		assert.Equal(t, tt.wantTransition, selection.Transition)
	}
}

func TestResolveModelCacheStorageWithClientsetErrors(t *testing.T) {
	deletePolicy := corev1.PersistentVolumeReclaimDelete
	tests := []struct {
		name      string
		namespace string
		workflow  ModelCacheWorkflow
		objects   func() []runtime.Object
		want      string
		notFound  bool
	}{
		{
			name:      "missing exact StorageClass",
			namespace: testCatalogNamespace,
			workflow:  ModelCacheWorkflowRegular,
			objects: func() []runtime.Object {
				return []runtime.Object{capabilityCatalogConfigMap(validCatalog)}
			},
			want:     "model cache StorageClass not found",
			notFound: true,
		},
		{
			name:      "different StorageClass name is not selected",
			namespace: testCatalogNamespace,
			workflow:  ModelCacheWorkflowRegular,
			objects: func() []runtime.Object {
				sc := testModelCacheStorageClass()
				sc.Name = "another-class"
				return []runtime.Object{sc, capabilityCatalogConfigMap(validCatalog)}
			},
			want:     "model cache StorageClass not found",
			notFound: true,
		},
		{
			name:      "missing catalog ConfigMap",
			namespace: testCatalogNamespace,
			workflow:  ModelCacheWorkflowRegular,
			objects:   func() []runtime.Object { return []runtime.Object{testModelCacheStorageClass()} },
			want:      "get storage capability ConfigMap",
		},
		{
			name:      "empty catalog namespace",
			namespace: "",
			workflow:  ModelCacheWorkflowRegular,
			objects: func() []runtime.Object {
				return []runtime.Object{testModelCacheStorageClass(), capabilityCatalogConfigMap(validCatalog)}
			},
			want: "namespace is empty",
		},
		{
			name:      "Delete reclaim policy",
			namespace: testCatalogNamespace,
			workflow:  ModelCacheWorkflowRegular,
			objects: func() []runtime.Object {
				sc := testModelCacheStorageClass()
				sc.ReclaimPolicy = &deletePolicy
				return []runtime.Object{sc, capabilityCatalogConfigMap(validCatalog)}
			},
			want: "must use reclaimPolicy Retain",
		},
		{
			name:      "unknown provisioner",
			namespace: testCatalogNamespace,
			workflow:  ModelCacheWorkflowRegular,
			objects: func() []runtime.Object {
				sc := testModelCacheStorageClass()
				sc.Provisioner = "unknown.csi.example.com"
				return []runtime.Object{sc, capabilityCatalogConfigMap(validCatalog)}
			},
			want: "with no catalog entry",
		},
		{
			name:      "unknown workflow",
			namespace: testCatalogNamespace,
			workflow:  "containerCache",
			objects: func() []runtime.Object {
				return []runtime.Object{testModelCacheStorageClass(), capabilityCatalogConfigMap(validCatalog)}
			},
			want: "unknown model cache workflow",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k8sClient := kubernetesfake.NewSimpleClientset(tt.objects()...)
			selection, err := ResolveModelCacheStorageWithClientset(
				t.Context(), k8sClient, tt.namespace, tt.workflow)
			require.ErrorContains(t, err, tt.want)
			assert.Nil(t, selection)
			assert.Equal(t, tt.notFound, errors.Is(err, ErrModelCacheStorageClassNotFound))
		})
	}
}

func TestStorageSnapshotDigests(t *testing.T) {
	base := testModelCacheStorageClass()
	baseDigest := digestStorageClass(base)
	assert.True(t, strings.HasPrefix(baseDigest, "v1:sha256:"))
	assert.Len(t, strings.TrimPrefix(baseDigest, "v1:sha256:"), sha256.Size*2)

	parameterOrder := base.DeepCopy()
	parameterOrder.Parameters = map[string]string{
		"a-option": "first",
		"z-option": "last",
	}
	assert.Equal(t, baseDigest, digestStorageClass(parameterOrder), "parameter map order must not affect the digest")

	metadataAndExpansion := base.DeepCopy()
	metadataAndExpansion.ResourceVersion = "new-resource-version"
	metadataAndExpansion.Labels = map[string]string{"changed": "metadata"}
	metadataAndExpansion.Annotations = map[string]string{"changed": "metadata"}
	expand := false
	metadataAndExpansion.AllowVolumeExpansion = &expand
	assert.Equal(t, baseDigest, digestStorageClass(metadataAndExpansion))

	deletePolicy := corev1.PersistentVolumeReclaimDelete
	immediate := storagev1.VolumeBindingImmediate
	mutations := []struct {
		name   string
		mutate func(*storagev1.StorageClass)
	}{
		{name: "provisioner", mutate: func(sc *storagev1.StorageClass) { sc.Provisioner = "other.csi.example.com" }},
		{name: "parameter value", mutate: func(sc *storagev1.StorageClass) { sc.Parameters["a-option"] = "changed" }},
		{name: "parameter name", mutate: func(sc *storagev1.StorageClass) {
			delete(sc.Parameters, "a-option")
			sc.Parameters["b-option"] = "first"
		}},
		{name: "reclaim policy", mutate: func(sc *storagev1.StorageClass) { sc.ReclaimPolicy = &deletePolicy }},
		{name: "binding mode", mutate: func(sc *storagev1.StorageClass) { sc.VolumeBindingMode = &immediate }},
		{name: "mount option", mutate: func(sc *storagev1.StorageClass) { sc.MountOptions[0] = "ro" }},
		{name: "mount option order", mutate: func(sc *storagev1.StorageClass) {
			sc.MountOptions[0], sc.MountOptions[1] = sc.MountOptions[1], sc.MountOptions[0]
		}},
		{name: "topology key", mutate: func(sc *storagev1.StorageClass) {
			sc.AllowedTopologies[0].MatchLabelExpressions[0].Key = "topology.example.com/rack"
		}},
		{name: "topology value order", mutate: func(sc *storagev1.StorageClass) {
			values := sc.AllowedTopologies[0].MatchLabelExpressions[0].Values
			values[0], values[1] = values[1], values[0]
		}},
	}
	for _, tt := range mutations {
		t.Run(tt.name, func(t *testing.T) {
			changed := base.DeepCopy()
			tt.mutate(changed)
			assert.NotEqual(t, baseDigest, digestStorageClass(changed))
		})
	}

	catalogDigest := digestCatalogPayload(validCatalog)
	assert.True(t, strings.HasPrefix(catalogDigest, "sha256:"))
	assert.NotEqual(t, catalogDigest, digestCatalogPayload(validCatalog+"\n"),
		"the catalog digest must cover the exact payload")
}

// TestCatalogEnablesNonNVMeshROXProvider is the property this feature exists
// for: enabling a qualified backend must be a catalog edit, not a code change.
// A second driver that proved ReadWriteOnce and ReadOnlyMany resolves to the
// same roxReadOnly transition as NVMesh, and carries its own reader mount
// options rather than NVMesh's XFS flags.
func TestCatalogEnablesNonNVMeshROXProvider(t *testing.T) {
	const wekaROXCatalog = `apiVersion: storage.nvcf.nvidia.com/v1alpha1
kind: StorageCapabilityCatalog
drivers:
  csi.weka.io:
    provider: weka
    accessModes: [ReadWriteOnce, ReadOnlyMany]
    readerMountOptions: [ro]
    transitions:
      regularModelCache: roxReadOnly
      helmModelCache: disabled
`
	_, err := parseStorageCapabilityCatalog(wekaROXCatalog)
	require.NoError(t, err, "a non-NVMesh driver must be able to declare roxReadOnly")

	sc := testModelCacheStorageClass()
	sc.Provisioner = "csi.weka.io"
	c := storageResolutionClient(t, sc, capabilityCatalogConfigMap(wekaROXCatalog))

	selection, err := ResolveModelCacheStorage(
		t.Context(), c, testCatalogNamespace, ModelCacheWorkflowRegular)
	require.NoError(t, err)
	assert.Equal(t, "weka", selection.Provider)
	assert.Equal(t, "csi.weka.io", selection.Provisioner)
	assert.Equal(t, ModelCacheTransitionROXReadOnly, selection.Transition)
	assert.Equal(t, []string{"ro"}, selection.RequiredMountOptions,
		"the driver's own reader options are used, not NVMesh's XFS flags")
	assert.Equal(t,
		[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce, corev1.ReadOnlyMany},
		selection.RequiredAccessModes)

	// The persisted selection must survive its own validation too, otherwise
	// the decision could be made but never committed.
	persisted, err := NewPersistedModelCacheStorageSelection(
		ModelCacheWorkflowRegular, ModelCacheSelectionDurable, selection)
	require.NoError(t, err)
	assert.Equal(t, "weka", persisted.Provider)

	// A driver that never proved ReadOnlyMany still cannot claim the transition.
	unqualified := strings.Replace(wekaROXCatalog,
		"accessModes: [ReadWriteOnce, ReadOnlyMany]", "accessModes: [ReadWriteMany]", 1)
	_, err = parseStorageCapabilityCatalog(unqualified)
	require.ErrorContains(t, err, "requires ReadWriteOnce and ReadOnlyMany access modes")
}

// TestHelmROXRequiresCrossNamespaceCapability pins the split the design calls
// for: regular caching keeps its reader in the same namespace, so proven
// access modes are enough for any driver. Helm caching must reach another
// namespace, which NVCA does by deriving a reader PV from the writer's CSI
// volume handle. Only a driver that declares that capability may run
// roxReadOnly for Helm; everything else must use a ReadWriteMany claim.
func TestHelmROXRequiresCrossNamespaceCapability(t *testing.T) {
	const wekaHelmROX = `apiVersion: storage.nvcf.nvidia.com/v1alpha1
kind: StorageCapabilityCatalog
drivers:
  csi.weka.io:
    provider: weka
    accessModes: [ReadWriteOnce, ReadOnlyMany]
    readerMountOptions: [ro]
    transitions:
      regularModelCache: roxReadOnly
      helmModelCache: roxReadOnly
`
	_, err := parseStorageCapabilityCatalog(wekaHelmROX)
	require.ErrorContains(t, err, "requires capability crossNamespaceVolumeSharing")

	// Regular-only is fine for the same driver: no namespace is crossed.
	regularOnly := strings.Replace(wekaHelmROX, "helmModelCache: roxReadOnly", "helmModelCache: disabled", 1)
	_, err = parseStorageCapabilityCatalog(regularOnly)
	require.NoError(t, err, "a driver without the capability may still cache for regular functions")

	// Declaring the capability makes the Helm transition legal again, and it
	// is a catalog edit rather than a code change.
	withCapability := strings.Replace(wekaHelmROX,
		"    transitions:",
		"    capabilities:\n      crossNamespaceVolumeSharing: true\n    transitions:", 1)
	_, err = parseStorageCapabilityCatalog(withCapability)
	require.NoError(t, err)
}

// profileDigestFor computes the qualified-profile digest the resolver would
// produce for one driver and workflow in the given catalog payload.
func profileDigestFor(
	t *testing.T, rawCatalog, provisioner string, workflow ModelCacheWorkflow,
) string {
	t.Helper()
	catalog, err := parseStorageCapabilityCatalog(rawCatalog)
	require.NoError(t, err)
	driver, ok := catalog.Drivers[provisioner]
	require.True(t, ok, "provisioner %q is absent from the catalog", provisioner)
	transition := driver.Transitions.RegularModelCache
	if workflow == ModelCacheWorkflowHelm {
		transition = driver.Transitions.HelmModelCache
	}
	return digestDriverProfile(provisioner, driver, workflow, transition)
}

// TestProfileDigestIgnoresUnrelatedCatalogEdits pins the property that makes an
// existing binding reusable: the compared digest covers only the driver and
// workflow a decision selected. Before this, the whole raw payload was hashed
// into the immutable binding spec, so adding a comment or an unrelated
// disabled provider invalidated every warm cache in the cluster.
func TestProfileDigestIgnoresUnrelatedCatalogEdits(t *testing.T) {
	base := profileDigestFor(t, validCatalog, NVMeshStorageClassProvisioner, ModelCacheWorkflowRegular)

	t.Run("trailing whitespace does not change the profile", func(t *testing.T) {
		edited := validCatalog + "\n"
		assert.NotEqual(t, digestCatalogPayload(validCatalog), digestCatalogPayload(edited),
			"the audit revision must still track the exact payload")
		assert.Equal(t, base,
			profileDigestFor(t, edited, NVMeshStorageClassProvisioner, ModelCacheWorkflowRegular))
	})

	t.Run("adding an unrelated disabled driver does not change the profile", func(t *testing.T) {
		edited := validCatalog + `
  csi.unrelated.example.com:
    provider: unrelated
    accessModes: []
    readerMountOptions: []
    transitions:
      regularModelCache: disabled
      helmModelCache: disabled
`
		assert.Equal(t, base,
			profileDigestFor(t, edited, NVMeshStorageClassProvisioner, ModelCacheWorkflowRegular))
	})

	t.Run("the selected driver's own capabilities still change the profile", func(t *testing.T) {
		catalog, err := parseStorageCapabilityCatalog(validCatalog)
		require.NoError(t, err)
		driver := catalog.Drivers[NVMeshStorageClassProvisioner]
		narrowed := []string{string(corev1.ReadWriteOnce)}
		driver.AccessModes = &narrowed
		assert.NotEqual(t, base, digestDriverProfile(
			NVMeshStorageClassProvisioner, driver,
			ModelCacheWorkflowRegular, ModelCacheTransitionROXReadOnly))
	})

	t.Run("workflow is part of the profile", func(t *testing.T) {
		assert.NotEqual(t, base,
			profileDigestFor(t, validCatalog, NVMeshStorageClassProvisioner, ModelCacheWorkflowHelm))
	})
}

func durableSelectionForStorageClass(t *testing.T, sc *storagev1.StorageClass) *PersistedModelCacheStorageSelection {
	t.Helper()
	selection, err := NewPersistedModelCacheStorageSelection(
		ModelCacheWorkflowRegular,
		ModelCacheSelectionDurable,
		&ModelCacheStorageSelection{
			StorageClassName:   sc.Name,
			StorageClassUID:    sc.UID,
			StorageClassDigest: digestStorageClass(sc),
			ProfileDigest:      profileDigestFor(t, validCatalog, NVMeshStorageClassProvisioner, ModelCacheWorkflowRegular),
			Provider:           "nvmesh",
			Provisioner:        sc.Provisioner,
			Transition:         ModelCacheTransitionROXReadOnly,
			RequiredAccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
				corev1.ReadOnlyMany,
			},
			RequiredMountOptions: []string{"ro", "norecovery", "nouuid"},
		},
	)
	require.NoError(t, err)
	return selection
}

func TestValidateModelCacheStorageSelectionLive(t *testing.T) {
	base := testModelCacheStorageClass()
	selection := durableSelectionForStorageClass(t, base)
	deletePolicy := corev1.PersistentVolumeReclaimDelete
	tests := []struct {
		name   string
		object func() *storagev1.StorageClass
		want   string
	}{
		{name: "unchanged", object: func() *storagev1.StorageClass { return base.DeepCopy() }},
		{name: "metadata and expansion changes are ignored", object: func() *storagev1.StorageClass {
			sc := base.DeepCopy()
			sc.Labels = map[string]string{"new": "metadata"}
			expand := false
			sc.AllowVolumeExpansion = &expand
			return sc
		}},
		{name: "UID replacement", object: func() *storagev1.StorageClass {
			sc := base.DeepCopy()
			sc.UID = "replacement-uid"
			return sc
		}, want: "UID changed"},
		{name: "provisioner replacement", object: func() *storagev1.StorageClass {
			sc := base.DeepCopy()
			sc.Provisioner = "other.csi.example.com"
			return sc
		}, want: "provisioner changed"},
		{name: "reclaim policy drift", object: func() *storagev1.StorageClass {
			sc := base.DeepCopy()
			sc.ReclaimPolicy = &deletePolicy
			return sc
		}, want: "no longer uses reclaimPolicy Retain"},
		{name: "configuration drift", object: func() *storagev1.StorageClass {
			sc := base.DeepCopy()
			sc.Parameters["a-option"] = "changed"
			return sc
		}, want: "configuration digest changed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := storageResolutionClient(t, tt.object())
			err := ValidateModelCacheStorageSelectionLive(t.Context(), c, selection)
			if tt.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.want)
		})
	}

	t.Run("missing selected StorageClass", func(t *testing.T) {
		c := storageResolutionClient(t)
		err := ValidateModelCacheStorageSelectionLive(t.Context(), c, selection)
		require.ErrorContains(t, err, "get selected model cache StorageClass")
	})

	t.Run("invalid selection fails before API access", func(t *testing.T) {
		invalid := *selection
		invalid.Version = "v2"
		err := ValidateModelCacheStorageSelectionLive(t.Context(), nil, &invalid)
		require.ErrorContains(t, err, "unsupported model cache storage selection version")
	})
}

func TestValidateModelCacheStorageSelectionLiveWithClientset(t *testing.T) {
	base := testModelCacheStorageClass()
	selection := durableSelectionForStorageClass(t, base)

	k8sClient := kubernetesfake.NewSimpleClientset(base.DeepCopy())
	require.NoError(t, ValidateModelCacheStorageSelectionLiveWithClientset(t.Context(), k8sClient, selection))

	drifted := base.DeepCopy()
	drifted.MountOptions = append(drifted.MountOptions, "ro")
	k8sClient = kubernetesfake.NewSimpleClientset(drifted)
	err := ValidateModelCacheStorageSelectionLiveWithClientset(t.Context(), k8sClient, selection)
	require.ErrorContains(t, err, "configuration digest changed")

	k8sClient = kubernetesfake.NewSimpleClientset()
	err = ValidateModelCacheStorageSelectionLiveWithClientset(t.Context(), k8sClient, selection)
	require.ErrorContains(t, err, "get selected model cache StorageClass")
}

func TestValidateModelCacheStorageSelectionInputsWithClientset(t *testing.T) {
	base := testModelCacheStorageClass()
	selection := durableSelectionForStorageClass(t, base)

	t.Run("exact inputs", func(t *testing.T) {
		k8sClient := kubernetesfake.NewSimpleClientset(
			base.DeepCopy(), capabilityCatalogConfigMap(validCatalog))
		require.NoError(t, ValidateModelCacheStorageSelectionInputsWithClientset(
			t.Context(), k8sClient, testCatalogNamespace, selection))
	})

	t.Run("catalog payload drift alone is not drift", func(t *testing.T) {
		// The selected driver's qualified profile is unchanged, so an edit
		// elsewhere in the payload must not invalidate this selection.
		k8sClient := kubernetesfake.NewSimpleClientset(
			base.DeepCopy(), capabilityCatalogConfigMap(validCatalog+"\n"))
		require.NoError(t, ValidateModelCacheStorageSelectionInputsWithClientset(
			t.Context(), k8sClient, testCatalogNamespace, selection))
	})

	t.Run("selected driver profile drift is drift", func(t *testing.T) {
		// Reordering the reader mount options keeps the catalog valid and
		// leaves provider and transition untouched, but it changes how the
		// reader is mounted, so the decision must be rejected. Only the
		// profile digest catches this.
		reordered := strings.Replace(validCatalog,
			"readerMountOptions: [ro, norecovery, nouuid]",
			"readerMountOptions: [ro, nouuid, norecovery]", 1)
		require.NotEqual(t, validCatalog, reordered)
		k8sClient := kubernetesfake.NewSimpleClientset(
			base.DeepCopy(), capabilityCatalogConfigMap(reordered))
		err := ValidateModelCacheStorageSelectionInputsWithClientset(
			t.Context(), k8sClient, testCatalogNamespace, selection)
		require.ErrorContains(t, err, "qualified profile digest changed")
		assert.ErrorIs(t, err, ErrModelCacheStorageSelectionDrift)
	})

	t.Run("matching digest cannot invent transition", func(t *testing.T) {
		disabled := strings.Replace(
			validCatalog, "regularModelCache: roxReadOnly", "regularModelCache: disabled", 1)
		forged := *selection
		forged.ProfileDigest = profileDigestFor(t, disabled, NVMeshStorageClassProvisioner, ModelCacheWorkflowRegular)
		k8sClient := kubernetesfake.NewSimpleClientset(
			base.DeepCopy(), capabilityCatalogConfigMap(disabled))
		err := ValidateModelCacheStorageSelectionInputsWithClientset(
			t.Context(), k8sClient, testCatalogNamespace, &forged)
		require.ErrorContains(t, err, "transition for regularModelCache changed")
		assert.ErrorIs(t, err, ErrModelCacheStorageSelectionDrift)
	})

	t.Run("missing catalog", func(t *testing.T) {
		k8sClient := kubernetesfake.NewSimpleClientset(base.DeepCopy())
		err := ValidateModelCacheStorageSelectionInputsWithClientset(
			t.Context(), k8sClient, testCatalogNamespace, selection)
		require.ErrorContains(t, err, "get storage capability ConfigMap")
		assert.True(t, apierrors.IsNotFound(err))
	})
}

func TestValidateNonDurableSelectionDoesNotReadKubernetes(t *testing.T) {
	for _, mode := range []ModelCacheSelectionMode{ModelCacheSelectionNone, ModelCacheSelectionEphemeral} {
		selection, err := NewPersistedModelCacheStorageSelection(ModelCacheWorkflowHelm, mode, nil)
		require.NoError(t, err)
		require.NoError(t, ValidateModelCacheStorageSelectionLive(t.Context(), nil, selection))
		require.NoError(t, ValidateModelCacheStorageSelectionLiveWithClientset(t.Context(), nil, selection))
		require.NoError(t, ValidateModelCacheStorageSelectionInputsWithClientset(
			t.Context(), nil, "", selection))
	}
}
