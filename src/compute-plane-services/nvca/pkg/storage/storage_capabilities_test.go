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
	"encoding/json"
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
    transitions:
      regularModelCache: roxReadOnly
      helmModelCache: roxReadOnly
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
				Transitions: storageTransitions{
					RegularModelCache: ModelCacheTransitionROXReadOnly,
					HelmModelCache:    ModelCacheTransitionROXReadOnly,
				},
			},
		},
	}
}

func TestLoadStorageCapabilityCatalogStrict(t *testing.T) {
	c := capabilityClient(t, capabilityCatalogConfigMap(validCatalog)).Build()

	catalog, err := loadStorageCapabilityCatalog(t.Context(), c, testCatalogNamespace)
	require.NoError(t, err)
	nvmesh := catalog.Drivers[NVMeshStorageClassProvisioner]
	assert.Equal(t, ModelCacheTransitionROXReadOnly, nvmesh.Transitions.RegularModelCache)
	assert.Equal(t, ModelCacheTransitionROXReadOnly, nvmesh.Transitions.HelmModelCache)
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
			name: "container cache transition",
			raw: strings.Replace(validCatalog, "      helmModelCache: roxReadOnly",
				"      helmModelCache: roxReadOnly\n      containerCache: disabled", 1),
			want: "unknown field \"containerCache\"",
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
		}, want: `requires readerMountOption "ro"`},
		{name: "NVMesh transition lacks norecovery reader mount option", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			d.ReaderMountOptions = readerMountOptions("ro", "nouuid")
			c.Drivers[NVMeshStorageClassProvisioner] = d
		}, want: `requires readerMountOption "norecovery"`},
		{name: "NVMesh transition lacks nouuid reader mount option", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			d.ReaderMountOptions = readerMountOptions("ro", "norecovery")
			c.Drivers[NVMeshStorageClassProvisioner] = d
		}, want: `requires readerMountOption "nouuid"`},
		{name: "bad regular transition", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			d.Transitions.RegularModelCache = "shared-pvc-readonly-fanout"
			c.Drivers[NVMeshStorageClassProvisioner] = d
		}, want: "invalid strategy"},
		{name: "bad helm transition", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			d.Transitions.HelmModelCache = "samba"
			c.Drivers[NVMeshStorageClassProvisioner] = d
		}, want: "invalid strategy"},
		{name: "NVMesh transition is provisioner-specific", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			delete(c.Drivers, NVMeshStorageClassProvisioner)
			c.Drivers["example.csi.test"] = d
		}, want: "restricted to provisioner"},
		{name: "NVMesh transition is provider-specific", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			d.Provider = "weka"
			c.Drivers[NVMeshStorageClassProvisioner] = d
		}, want: "requires provider"},
		{name: "NVMesh transition lacks ReadWriteOnce", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			d.AccessModes = accessModes("ReadOnlyMany")
			c.Drivers[NVMeshStorageClassProvisioner] = d
		}, want: "requires ReadWriteOnce and ReadOnlyMany"},
		{name: "NVMesh transition lacks ReadOnlyMany", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			d.AccessModes = accessModes("ReadWriteOnce")
			c.Drivers[NVMeshStorageClassProvisioner] = d
		}, want: "requires ReadWriteOnce and ReadOnlyMany"},
		{name: "rwxReadOnly transition is rejected for Helm", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			d.AccessModes = accessModes("ReadWriteOnce", "ReadOnlyMany", "ReadWriteMany")
			d.Transitions.HelmModelCache = ModelCacheTransitionRWXReadOnly
			c.Drivers[NVMeshStorageClassProvisioner] = d
		}, want: "only supported for regularModelCache"},
		{name: "rwxReadOnly transition lacks ReadWriteMany", mutate: func(c *storageCapabilityCatalog) {
			d := c.Drivers[NVMeshStorageClassProvisioner]
			d.Transitions.RegularModelCache = ModelCacheTransitionRWXReadOnly
			d.Transitions.HelmModelCache = ModelCacheTransitionDisabled
			c.Drivers[NVMeshStorageClassProvisioner] = d
		}, want: "requires ReadWriteMany"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalog := validStorageCapabilityCatalog()
			tt.mutate(catalog)
			require.ErrorContains(t, validateStorageCapabilityCatalog(catalog), tt.want)
		})
	}
}

func TestValidateStorageCapabilityCatalogAllowsRegularRWXReadOnly(t *testing.T) {
	const provisioner = "shared.csi.example.com"
	catalog := validStorageCapabilityCatalog()
	catalog.Drivers[provisioner] = storageDriverSpec{
		Provider:           "sharedFilesystem",
		AccessModes:        accessModes("ReadWriteMany", "ReadOnlyMany"),
		ReaderMountOptions: readerMountOptions(),
		Transitions: storageTransitions{
			RegularModelCache: ModelCacheTransitionRWXReadOnly,
			HelmModelCache:    ModelCacheTransitionDisabled,
		},
	}

	require.NoError(t, validateStorageCapabilityCatalog(catalog))
	assert.Equal(t,
		[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		requiredAccessModesForTransition(ModelCacheTransitionRWXReadOnly))

	sc := testModelCacheStorageClass()
	sc.Provisioner = provisioner
	selection, err := selectModelCacheStorageFromObjects(
		sc, catalog, "sha256:catalog", ModelCacheWorkflowRegular)
	require.NoError(t, err)
	assert.Equal(t, ModelCacheTransitionRWXReadOnly, selection.Transition)
	assert.Equal(t,
		[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		selection.RequiredAccessModes)
	assert.Empty(t, selection.RequiredMountOptions)

	driver := catalog.Drivers[provisioner]
	driver.ReaderMountOptions = readerMountOptions("ro")
	catalog.Drivers[provisioner] = driver
	require.ErrorContains(t, validateStorageCapabilityCatalog(catalog),
		"does not create a reader PV and requires empty readerMountOptions")
}

func TestValidateStorageCapabilityCatalogAllowsDisabledTransitionsWithEmptyModes(t *testing.T) {
	catalog := validStorageCapabilityCatalog()
	driver := catalog.Drivers[NVMeshStorageClassProvisioner]
	driver.AccessModes = accessModes()
	driver.ReaderMountOptions = readerMountOptions()
	driver.Transitions = storageTransitions{
		RegularModelCache: "disabled",
		HelmModelCache:    "disabled",
	}
	catalog.Drivers[NVMeshStorageClassProvisioner] = driver

	require.NoError(t, validateStorageCapabilityCatalog(catalog))
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
	assert.Equal(t, ModelCacheTransitionROXReadOnly, nvmesh.Transitions.RegularModelCache)
	assert.Equal(t, ModelCacheTransitionROXReadOnly, nvmesh.Transitions.HelmModelCache)
	require.NotNil(t, nvmesh.AccessModes)
	assert.ElementsMatch(t, []string{"ReadWriteOnce", "ReadOnlyMany"}, *nvmesh.AccessModes)
	require.NotNil(t, nvmesh.ReaderMountOptions)
	assert.Equal(t, []string{"ro", "norecovery", "nouuid"}, *nvmesh.ReaderMountOptions)

	for _, provisioner := range []string{"csi.weka.io", "fss.csi.oraclecloud.com", "lustre.csi.oraclecloud.com"} {
		driver, ok := catalog.Drivers[provisioner]
		require.True(t, ok, provisioner)
		assert.Equal(t, "disabled", driver.Transitions.RegularModelCache)
		assert.Equal(t, "disabled", driver.Transitions.HelmModelCache)
		require.NotNil(t, driver.ReaderMountOptions)
		assert.Empty(t, *driver.ReaderMountOptions)
	}
	weka := catalog.Drivers["csi.weka.io"]
	require.NotNil(t, weka.AccessModes)
	assert.ElementsMatch(t, []string{"ReadWriteMany", "ReadOnlyMany"}, *weka.AccessModes)
	fss := catalog.Drivers["fss.csi.oraclecloud.com"]
	require.NotNil(t, fss.AccessModes)
	assert.Equal(t, []string{"ReadWriteMany"}, *fss.AccessModes)
	lustre := catalog.Drivers["lustre.csi.oraclecloud.com"]
	require.NotNil(t, lustre.AccessModes)
	assert.Empty(t, *lustre.AccessModes)

	schemaRaw, err := os.ReadFile(filepath.Join(chartDir, "files", "nvcf-storage-capabilities-v1alpha1.schema.json"))
	require.NoError(t, err)
	assert.True(t, json.Valid(schemaRaw), "shipped JSON schema must be valid JSON")
	var schema map[string]any
	require.NoError(t, json.Unmarshal(schemaRaw, &schema))
	definitions, ok := schema["$defs"].(map[string]any)
	require.True(t, ok)
	regularStrategy, ok := definitions["regularTransitionStrategy"].(map[string]any)
	require.True(t, ok)
	helmStrategy, ok := definitions["helmTransitionStrategy"].(map[string]any)
	require.True(t, ok)
	assert.ElementsMatch(t,
		[]any{ModelCacheTransitionDisabled, ModelCacheTransitionROXReadOnly, ModelCacheTransitionRWXReadOnly},
		regularStrategy["enum"])
	assert.ElementsMatch(t,
		[]any{ModelCacheTransitionDisabled, ModelCacheTransitionROXReadOnly},
		helmStrategy["enum"])
}
