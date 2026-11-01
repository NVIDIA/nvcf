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

package featureflag

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
)

func TestNewHelmSharedStorageFeatureFlag_DefaultValues(t *testing.T) {
	key := "test-key"
	defaultValue := true

	flag := newHelmSharedStorageFeatureFlag(key, defaultValue)

	assert.Equal(t, key, flag.Key, "Key should match input")
	assert.Equal(t, defaultValue, *flag.defaultValue, "defaultValue should match input")
	assert.Equal(t, defaultValue, *flag.enabled, "enabled should be true (always enabled by default)")
	assert.True(t, flag.Enabled(), "Enabled() should return true")

	require.NotNil(t, flag.ServerSpec.Server, "ServerSpec.Server should be non-nil")

	expectedCPURequest := resource.MustParse("20m")
	assert.Equal(t, expectedCPURequest, flag.ServerSpec.Server.SMBServerContainerResources.Requests[corev1.ResourceCPU],
		"CPU request should match expected value")

	expectedMemoryRequest := resource.MustParse("150Mi")
	assert.Equal(t, expectedMemoryRequest, flag.ServerSpec.Server.SMBServerContainerResources.Requests[corev1.ResourceMemory],
		"Memory request should match expected value")

	expectedCPULimit := resource.MustParse("500m")
	assert.Equal(t, expectedCPULimit, flag.ServerSpec.Server.SMBServerContainerResources.Limits[corev1.ResourceCPU],
		"CPU limit should match expected value")

	expectedMemoryLimit := resource.MustParse("2Gi")
	assert.Equal(t, expectedMemoryLimit, flag.ServerSpec.Server.SMBServerContainerResources.Limits[corev1.ResourceMemory],
		"Memory limit should match expected value")

	expectedSize := resource.MustParse("100Gi")
	assert.Equal(t, 0, flag.TaskDataSpec.Size.Cmp(expectedSize),
		"Size should match expected value")
}

func TestNewHelmSharedStorageFeatureFlag_ServerValuesWithEnvVar(t *testing.T) {
	key := "test-key"
	defaultValue := true

	// Set up environment variable
	originalEnvVar := os.Getenv(nvcaSharedStorageonfigJSONBase64Key)
	defer os.Setenv(nvcaSharedStorageonfigJSONBase64Key, originalEnvVar)

	cfg := SharedStorageSpec{
		Server: &nvcav1new.SharedStorageServerSpec{
			SMBServerContainerResources: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("30m"),
					corev1.ResourceMemory: resource.MustParse("200Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("600m"),
					corev1.ResourceMemory: resource.MustParse("3Gi"),
				},
			},
		},
	}
	b, err := json.Marshal(cfg)
	require.NoError(t, err, "JSON marshaling should not fail")
	encodedCfg := base64.StdEncoding.EncodeToString(b)
	os.Setenv(nvcaSharedStorageonfigJSONBase64Key, encodedCfg)

	flag := newHelmSharedStorageFeatureFlag(key, defaultValue)

	// Test resource requests and limits from environment variables
	expectedCPURequest := resource.MustParse("30m")
	assert.Equal(t, expectedCPURequest, flag.ServerSpec.Server.SMBServerContainerResources.Requests[corev1.ResourceCPU],
		"CPU request should match value from environment")

	expectedMemoryRequest := resource.MustParse("200Mi")
	assert.Equal(t, expectedMemoryRequest, flag.ServerSpec.Server.SMBServerContainerResources.Requests[corev1.ResourceMemory],
		"Memory request should match value from environment")

	expectedCPULimit := resource.MustParse("600m")
	assert.Equal(t, expectedCPULimit, flag.ServerSpec.Server.SMBServerContainerResources.Limits[corev1.ResourceCPU],
		"CPU limit should match value from environment")

	expectedMemoryLimit := resource.MustParse("3Gi")
	assert.Equal(t, expectedMemoryLimit, flag.ServerSpec.Server.SMBServerContainerResources.Limits[corev1.ResourceMemory],
		"Memory limit should match value from environment")
}

func TestNewHelmSharedStorageFeatureFlag_TaskDataValuesWithEnv(t *testing.T) {
	// Set up the environment variable
	cfg := SharedStorageSpec{
		Enabled: newBool(false), // Try to disable with env var
		TaskData: &nvcav1new.SharedStorageTaskDataSpec{
			Size: resource.MustParse("5Ti"),
		},
	}
	b, err := json.Marshal(cfg)
	require.NoError(t, err, "JSON marshaling should not fail")

	os.Setenv(nvcaSharedStorageonfigJSONBase64Key, base64.StdEncoding.EncodeToString(b))
	defer os.Unsetenv(nvcaSharedStorageonfigJSONBase64Key)

	flag := newHelmSharedStorageFeatureFlag("HelmSharedStorage", false)

	assert.Equal(t, false, *flag.defaultValue, "defaultValue should match input")
	assert.Equal(t, false, *flag.enabled, "enabled should be false when disabled in env var")
	assert.False(t, flag.Enabled(), "Enabled() should return false")

	expectedSize := resource.MustParse("5Ti")
	assert.Equal(t, 0, flag.TaskDataSpec.Size.Cmp(expectedSize),
		"Size should match value from environment")
}

func TestNewHelmSharedStorageFeatureFlag_InvalidBase64(t *testing.T) {
	// Set up the environment variable with invalid base64
	os.Setenv(nvcaSharedStorageonfigJSONBase64Key, "invalid_base64")
	defer os.Unsetenv(nvcaSharedStorageonfigJSONBase64Key)

	flag := newHelmSharedStorageFeatureFlag("HelmSharedStorage", false)

	assert.Equal(t, false, *flag.defaultValue, "defaultValue should match input")
	assert.Equal(t, false, *flag.enabled, "enabled should be false with invalid env var")

	// Should fall back to default size
	expectedSize := resource.MustParse("100Gi")
	assert.Equal(t, 0, flag.TaskDataSpec.Size.Cmp(expectedSize),
		"Size should fall back to default value")
}

func TestNewHelmSharedStorageFeatureFlag_InvalidJSON(t *testing.T) {
	// Set up the environment variable with valid base64 but invalid JSON
	os.Setenv(nvcaSharedStorageonfigJSONBase64Key, base64.StdEncoding.EncodeToString([]byte("invalid_json")))
	t.Cleanup(func() {
		os.Unsetenv(nvcaSharedStorageonfigJSONBase64Key)
	})

	flag := newHelmSharedStorageFeatureFlag("HelmSharedStorage", false)

	assert.Equal(t, false, *flag.defaultValue, "defaultValue should match input")
	assert.Equal(t, false, *flag.enabled, "enabled should be false with invalid JSON")

	// Should fall back to default size
	expectedSize := resource.MustParse("100Gi")
	assert.Equal(t, 0, flag.TaskDataSpec.Size.Cmp(expectedSize),
		"Size should fall back to default value")
}
