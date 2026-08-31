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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testCatalogNamespace = "nvca-system"

func capabilityCatalogConfigMap(raw string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: StorageCapabilityConfigMapName, Namespace: testCatalogNamespace},
		Data:       map[string]string{StorageCapabilityConfigMapKey: raw},
	}
}

func capabilityClient(t *testing.T, cm *corev1.ConfigMap) *fake.ClientBuilder {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm)
}

const validCatalog = `apiVersion: storage.nvcf.nvidia.com/v1alpha1
kind: StorageCapabilityCatalog
drivers:
  nvmesh-csi.excelero.com:
    provider: nvmesh
    accessModes: [ReadWriteOnce, ReadOnlyMany]
    readerMountOptions: [ro, norecovery, nouuid]
`

func accessModes(modes ...string) *[]string {
	return &modes
}

func readerMountOptions(options ...string) *[]string {
	return &options
}

func validStorageCapabilityCatalog() *storageCapabilityCatalog {
	return &storageCapabilityCatalog{
		APIVersion: storageCapabilityCatalogAPIVersion,
		Kind:       storageCapabilityCatalogKind,
		Drivers: map[string]storageDriverSpec{
			NVMeshStorageClassProvisioner: {
				Provider:           "nvmesh",
				AccessModes:        accessModes("ReadWriteOnce", "ReadOnlyMany"),
				ReaderMountOptions: readerMountOptions("ro", "norecovery", "nouuid"),
			},
		},
	}
}

func TestLoadStorageCapabilityCatalogStrict(t *testing.T) {
	c := capabilityClient(t, capabilityCatalogConfigMap(validCatalog)).Build()

	catalog, err := loadStorageCapabilityCatalog(t.Context(), c, testCatalogNamespace)
	require.NoError(t, err)
	nvmesh := catalog.Drivers[NVMeshStorageClassProvisioner]
	assert.Equal(t, ModelCacheTransitionROXReadOnly,
		transitionForWorkflow(nvmesh, ModelCacheWorkflowRegular))
	require.NotNil(t, nvmesh.AccessModes)
	assert.ElementsMatch(t, []string{"ReadWriteOnce", "ReadOnlyMany"}, *nvmesh.AccessModes)
	require.NotNil(t, nvmesh.ReaderMountOptions)
	assert.Equal(t, []string{"ro", "norecovery", "nouuid"}, *nvmesh.ReaderMountOptions)
}

func TestLoadStorageCapabilityCatalogErrors(t *testing.T) {
	missingConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: testCatalogNamespace},
	}
	missingData := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: StorageCapabilityConfigMapName, Namespace: testCatalogNamespace},
	}
	missingAccessModes := strings.Replace(
		validCatalog, "    accessModes: [ReadWriteOnce, ReadOnlyMany]\n", "", 1)
	nullAccessModes := strings.Replace(
		validCatalog, "    accessModes: [ReadWriteOnce, ReadOnlyMany]", "    accessModes: null", 1)
	missingReaderMountOptions := strings.Replace(
		validCatalog, "    readerMountOptions: [ro, norecovery, nouuid]\n", "", 1)
	nullReaderMountOptions := strings.Replace(
		validCatalog, "    readerMountOptions: [ro, norecovery, nouuid]", "    readerMountOptions: null", 1)

	tests := []struct {
		name      string
		namespace string
		configMap *corev1.ConfigMap
		want      string
	}{
		{
			name: "empty namespace", namespace: "",
			configMap: capabilityCatalogConfigMap(validCatalog), want: "namespace is empty",
		},
		{
			name: "missing ConfigMap", namespace: testCatalogNamespace,
			configMap: missingConfigMap, want: "get storage capability ConfigMap",
		},
		{
			name: "missing data key", namespace: testCatalogNamespace,
			configMap: missingData, want: "has no",
		},
		{
			name: "empty data", namespace: testCatalogNamespace,
			configMap: capabilityCatalogConfigMap(""), want: "has no",
		},
		{
			name: "malformed YAML", namespace: testCatalogNamespace,
			configMap: capabilityCatalogConfigMap("drivers: ["), want: "parse storage capability catalog",
		},
		{
			name: "missing accessModes", namespace: testCatalogNamespace,
			configMap: capabilityCatalogConfigMap(missingAccessModes), want: "has no accessModes",
		},
		{
			name: "null accessModes", namespace: testCatalogNamespace,
			configMap: capabilityCatalogConfigMap(nullAccessModes), want: "has no accessModes",
		},
		{
			name: "missing readerMountOptions", namespace: testCatalogNamespace,
			configMap: capabilityCatalogConfigMap(missingReaderMountOptions), want: "has no readerMountOptions",
		},
		{
			name: "null readerMountOptions", namespace: testCatalogNamespace,
			configMap: capabilityCatalogConfigMap(nullReaderMountOptions), want: "has no readerMountOptions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := capabilityClient(t, tt.configMap).Build()
			_, err := loadStorageCapabilityCatalog(t.Context(), c, tt.namespace)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestLoadStorageCapabilityCatalogRejectsUnknownFields(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "driver field",
			raw:  strings.Replace(validCatalog, "    provider: nvmesh", "    provider: nvmesh\n    surprise: true", 1),
			want: "unknown field \"surprise\"",
		},
		{
			// A declared transition is exactly what the catalog no longer
			// accepts: the flow is derived, never stated.
			name: "declared transition",
			raw: strings.Replace(validCatalog, "    provider: nvmesh",
				"    provider: nvmesh\n    transitions:\n      helmModelCache: roxReadOnly", 1),
			want: "unknown field \"transitions\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := capabilityClient(t, capabilityCatalogConfigMap(tt.raw)).Build()
			_, err := loadStorageCapabilityCatalog(t.Context(), c, testCatalogNamespace)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestValidateStorageCapabilityCatalog(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*storageCapabilityCatalog)
		want   string
	}{
		{name: "bad apiVersion", mutate: func(c *storageCapabilityCatalog) { c.APIVersion = "v1" }, want: "apiVersion"},
		{name: "bad kind", mutate: func(c *storageCapabilityCatalog) { c.Kind = "ConfigMap" }, want: "kind"},
		{name: "empty drivers", mutate: func(c *storageCapabilityCatalog) { c.Drivers = nil }, want: "no drivers"},
		{name: "empty provider", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			d.Provider = ""
			c.Drivers[NVMeshStorageClassProvisioner] = d
		}, want: "empty provisioner or provider"},
		{name: "whitespace provider", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			d.Provider = " \t"
			c.Drivers[NVMeshStorageClassProvisioner] = d
		}, want: "empty provisioner or provider"},
		{name: "whitespace provisioner", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			delete(c.Drivers, NVMeshStorageClassProvisioner)
			c.Drivers[" \t"] = d
		}, want: "empty provisioner or provider"},
		{name: "missing access modes", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			d.AccessModes = nil
			c.Drivers[NVMeshStorageClassProvisioner] = d
		}, want: "has no accessModes"},
		{name: "bad access mode", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			d.AccessModes = accessModes(append(*d.AccessModes, "ReadWriteEverywhere")...)
			c.Drivers[NVMeshStorageClassProvisioner] = d
		}, want: "invalid accessMode"},
		{name: "duplicate access mode", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			d.AccessModes = accessModes(append(*d.AccessModes, "ReadWriteOnce")...)
			c.Drivers[NVMeshStorageClassProvisioner] = d
		}, want: "duplicate accessMode"},
		{name: "missing reader mount options", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			d.ReaderMountOptions = nil
			c.Drivers[NVMeshStorageClassProvisioner] = d
		}, want: "has no readerMountOptions"},
		{name: "blank reader mount option", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			d.ReaderMountOptions = readerMountOptions(append(*d.ReaderMountOptions, " \t")...)
			c.Drivers[NVMeshStorageClassProvisioner] = d
		}, want: "blank readerMountOption"},
		{name: "reader mount option with surrounding whitespace", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			d.ReaderMountOptions = readerMountOptions("ro", " norecovery", "nouuid")
			c.Drivers[NVMeshStorageClassProvisioner] = d
		}, want: "surrounding whitespace"},
		{name: "duplicate reader mount option", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			d.ReaderMountOptions = readerMountOptions(append(*d.ReaderMountOptions, "ro")...)
			c.Drivers[NVMeshStorageClassProvisioner] = d
		}, want: "duplicate readerMountOption"},
		{name: "conflicting reader mount options", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			d.ReaderMountOptions = readerMountOptions("ro", "norecovery", "nouuid", "rw")
			c.Drivers[NVMeshStorageClassProvisioner] = d
		}, want: `readerMountOptions "ro" and "rw" conflict`},
		{name: "conflicting recovery reader mount options", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			d.ReaderMountOptions = readerMountOptions("ro", "recovery", "norecovery", "nouuid")
			c.Drivers[NVMeshStorageClassProvisioner] = d
		}, want: `readerMountOptions "recovery" and "norecovery" conflict`},
		{name: "conflicting UUID reader mount options", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			d.ReaderMountOptions = readerMountOptions("ro", "norecovery", "uuid", "nouuid")
			c.Drivers[NVMeshStorageClassProvisioner] = d
		}, want: `readerMountOptions "uuid" and "nouuid" conflict`},
		{name: "NVMesh transition lacks ro reader mount option", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			d.ReaderMountOptions = readerMountOptions("norecovery", "nouuid")
			c.Drivers[NVMeshStorageClassProvisioner] = d
		}, want: `must list readerMountOption "ro"`},
		{name: "roxReadOnly requires a read-only reader mount", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			d.ReaderMountOptions = readerMountOptions("norecovery", "nouuid")
			c.Drivers[NVMeshStorageClassProvisioner] = d
		}, want: `must list readerMountOption "ro"`},
		// roxReadOnly is gated on proven access modes, not on the vendor, so
		// moving the same qualified entry to another driver stays valid.
		{name: "roxReadOnly is not provisioner-specific", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			delete(c.Drivers, NVMeshStorageClassProvisioner)
			c.Drivers["example.csi.test"] = d
		}},
		{name: "roxReadOnly is not provider-specific", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			d.Provider = "weka"
			c.Drivers[NVMeshStorageClassProvisioner] = d
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalog := validStorageCapabilityCatalog()
			tt.mutate(catalog)
			err := validateStorageCapabilityCatalog(catalog)
			// An empty want means the mutation must stay valid: the catalog,
			// not the code, decides which drivers may run a transition.
			if tt.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.want)
		})
	}
}

// TestTransitionForWorkflow is the whole decision the catalog drives: proven
// access modes in, cache flow out, per workflow.
func TestTransitionForWorkflow(t *testing.T) {
	driver := func(provider string, modes ...string) storageDriverSpec {
		return storageDriverSpec{
			Provider:           provider,
			AccessModes:        accessModes(modes...),
			ReaderMountOptions: readerMountOptions(),
		}
	}

	tests := []struct {
		name        string
		driver      storageDriverSpec
		wantRegular string
		wantHelm    string
	}{
		{
			// A shared claim reaches other namespaces on its own, so RWX
			// serves both workflows on any vendor.
			name:        "ReadWriteMany serves both workflows",
			driver:      driver("weka", "ReadWriteMany"),
			wantRegular: ModelCacheTransitionRWXReadOnly,
			wantHelm:    ModelCacheTransitionRWXReadOnly,
		},
		{
			// Regular caching prefers the shared claim when a driver proved
			// both shapes: one volume, readers mounting it read-only.
			name:        "ReadWriteMany wins when a driver proved both shapes",
			driver:      driver("weka", "ReadWriteMany", "ReadWriteOnce", "ReadOnlyMany"),
			wantRegular: ModelCacheTransitionRWXReadOnly,
			wantHelm:    ModelCacheTransitionRWXReadOnly,
		},
		{
			// The ROX shape keeps its reader in the request namespace, so it
			// serves regular caching for anyone, and Helm caching for nobody.
			name:        "ReadWriteOnce plus ReadOnlyMany serves regular only",
			driver:      driver("weka", "ReadWriteOnce", "ReadOnlyMany"),
			wantRegular: ModelCacheTransitionROXReadOnly,
			wantHelm:    ModelCacheTransitionDisabled,
		},
		{
			// NVMesh volume handles encode the namespace, so NVCA can derive
			// a reader PV for another namespace. This is the one exception.
			name:        "NVMesh reaches other namespaces with the ROX shape",
			driver:      driver(ModelCacheProviderNVMesh, "ReadWriteOnce", "ReadOnlyMany"),
			wantRegular: ModelCacheTransitionROXReadOnly,
			wantHelm:    ModelCacheTransitionROXReadOnly,
		},
		{
			name:        "a single mode qualifies nothing",
			driver:      driver("ociFss", "ReadWriteOnce"),
			wantRegular: ModelCacheTransitionDisabled,
			wantHelm:    ModelCacheTransitionDisabled,
		},
		{
			name:        "ReadOnlyMany alone qualifies nothing",
			driver:      driver("weka", "ReadOnlyMany"),
			wantRegular: ModelCacheTransitionDisabled,
			wantHelm:    ModelCacheTransitionDisabled,
		},
		{
			// Nothing qualified yet is how an unqualified driver is recorded.
			name:        "no qualified mode disables both workflows",
			driver:      driver("ociLustre"),
			wantRegular: ModelCacheTransitionDisabled,
			wantHelm:    ModelCacheTransitionDisabled,
		},
		{
			// NVMesh gets no exemption from qualification.
			name:        "NVMesh with nothing qualified is still disabled",
			driver:      driver(ModelCacheProviderNVMesh),
			wantRegular: ModelCacheTransitionDisabled,
			wantHelm:    ModelCacheTransitionDisabled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantRegular,
				transitionForWorkflow(tt.driver, ModelCacheWorkflowRegular), "regular")
			assert.Equal(t, tt.wantHelm,
				transitionForWorkflow(tt.driver, ModelCacheWorkflowHelm), "helm")
		})
	}
}

// TestSelectionFollowsQualifiedModes checks the derivation reaches a real
// selection, including for a driver NVCA has no special knowledge of.
func TestSelectionFollowsQualifiedModes(t *testing.T) {
	const provisioner = "shared.csi.example.com"
	catalog := validStorageCapabilityCatalog()
	catalog.Drivers[provisioner] = storageDriverSpec{
		Provider:           "someVendor",
		AccessModes:        accessModes("ReadWriteMany", "ReadOnlyMany"),
		ReaderMountOptions: readerMountOptions(),
	}
	require.NoError(t, validateStorageCapabilityCatalog(catalog))

	sc := testModelCacheStorageClass()
	sc.Provisioner = provisioner
	for _, workflow := range []ModelCacheWorkflow{ModelCacheWorkflowRegular, ModelCacheWorkflowHelm} {
		selection, err := selectModelCacheStorageFromObjects(sc, catalog, "sha256:catalog", workflow)
		require.NoError(t, err, workflow)
		assert.Equal(t, ModelCacheTransitionRWXReadOnly, selection.Transition, workflow)
		assert.Equal(t,
			[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			selection.RequiredAccessModes, workflow)
		assert.Empty(t, selection.RequiredMountOptions, workflow,
			"a shared claim is mounted read-only by the Pod, so NVCA sets no reader options")
	}
}

func TestValidateStorageCapabilityCatalogAllowsNothingQualified(t *testing.T) {
	catalog := validStorageCapabilityCatalog()
	driver := catalog.Drivers[NVMeshStorageClassProvisioner]
	driver.AccessModes = accessModes()
	driver.ReaderMountOptions = readerMountOptions()
	catalog.Drivers[NVMeshStorageClassProvisioner] = driver

	require.NoError(t, validateStorageCapabilityCatalog(catalog),
		"an unqualified driver is recorded with no access modes, not rejected")
}

func TestShippedStorageCapabilityCatalog(t *testing.T) {
	chartDir := filepath.Join("src", "compute-plane-services", "nvca", "deployments", "nvca-operator")
	if _, err := os.Stat(chartDir); os.IsNotExist(err) {
		chartDir = filepath.Join("..", "..", "deployments", "nvca-operator")
	}
	raw, err := os.ReadFile(filepath.Join(chartDir, "files", "nvcf-storage-capabilities-v1alpha1.yaml"))
	require.NoError(t, err)
	c := capabilityClient(t, capabilityCatalogConfigMap(string(raw))).Build()
	catalog, err := loadStorageCapabilityCatalog(t.Context(), c, testCatalogNamespace)
	require.NoError(t, err)

	nvmesh := catalog.Drivers[NVMeshStorageClassProvisioner]
	require.NotNil(t, nvmesh.AccessModes)
	assert.ElementsMatch(t, []string{"ReadWriteOnce", "ReadOnlyMany"}, *nvmesh.AccessModes)
	require.NotNil(t, nvmesh.ReaderMountOptions)
	assert.Equal(t, []string{"ro", "norecovery", "nouuid"}, *nvmesh.ReaderMountOptions)
	assert.Equal(t, ModelCacheTransitionROXReadOnly,
		transitionForWorkflow(nvmesh, ModelCacheWorkflowRegular))
	assert.Equal(t, ModelCacheTransitionROXReadOnly,
		transitionForWorkflow(nvmesh, ModelCacheWorkflowHelm))

	// Weka and FSS were qualified on 2026-08-31: a reader in another namespace,
	// on a PV derived from the writer volume, read the cache and was denied
	// writes. Both serve regular and Helm caching from a shared claim, and
	// neither needs reader mount options because the Pod mounts the claim
	// read-only. See docs/dev/storage-provider-qualification.md.
	for _, provisioner := range []string{"csi.weka.io", "fss.csi.oraclecloud.com"} {
		driver, ok := catalog.Drivers[provisioner]
		require.True(t, ok, provisioner)
		require.NotNil(t, driver.AccessModes, provisioner)
		assert.ElementsMatch(t,
			[]string{"ReadWriteMany", "ReadOnlyMany"}, *driver.AccessModes, provisioner)
		assert.Equal(t, ModelCacheTransitionRWXReadOnly,
			transitionForWorkflow(driver, ModelCacheWorkflowRegular), provisioner)
		assert.Equal(t, ModelCacheTransitionRWXReadOnly,
			transitionForWorkflow(driver, ModelCacheWorkflowHelm), provisioner)
		require.NotNil(t, driver.ReaderMountOptions, provisioner)
		assert.Empty(t, *driver.ReaderMountOptions, provisioner)
	}

	// Lustre has no qualification run, so both workflows stay off.
	lustre, ok := catalog.Drivers["lustre.csi.oraclecloud.com"]
	require.True(t, ok)
	require.NotNil(t, lustre.AccessModes)
	assert.Empty(t, *lustre.AccessModes)
	assert.Equal(t, ModelCacheTransitionDisabled,
		transitionForWorkflow(lustre, ModelCacheWorkflowRegular))
	assert.Equal(t, ModelCacheTransitionDisabled,
		transitionForWorkflow(lustre, ModelCacheWorkflowHelm))
}
