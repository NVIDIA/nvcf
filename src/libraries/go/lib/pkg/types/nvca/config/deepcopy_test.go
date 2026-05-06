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

package nvcaconfig

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestDeepCopy_AgentTimeConfig(t *testing.T) {
	orig := &AgentTimeConfig{CredRenewInterval: 5 * time.Second}
	out := orig.DeepCopy()
	require.NotNil(t, out)
	assert.Equal(t, orig.CredRenewInterval, out.CredRenewInterval)
	assert.NotSame(t, orig, out)
}

func TestDeepCopy_AgentTimeConfig_Nil(t *testing.T) {
	var orig *AgentTimeConfig
	out := orig.DeepCopy()
	assert.Nil(t, out)
}

func TestDeepCopy_AgentConfig_Empty(t *testing.T) {
	orig := &AgentConfig{}
	out := orig.DeepCopy()
	require.NotNil(t, out)
	assert.Equal(t, orig.LogLevel, out.LogLevel)
}

func TestDeepCopy_AgentConfig_WithSlicesAndMaps(t *testing.T) {
	orig := &AgentConfig{
		LogLevel:        "debug",
		FeatureFlags:    []string{"flag1", "flag2"},
		NamespaceLabels: map[string]string{"env": "prod"},
	}
	out := orig.DeepCopy()
	require.NotNil(t, out)
	assert.Equal(t, orig.LogLevel, out.LogLevel)
	assert.Equal(t, orig.FeatureFlags, out.FeatureFlags)
	assert.Equal(t, orig.NamespaceLabels, out.NamespaceLabels)
	orig.FeatureFlags[0] = "modified"
	assert.Equal(t, "flag1", out.FeatureFlags[0])
}

func TestDeepCopy_AgentConfig_Nil(t *testing.T) {
	var orig *AgentConfig
	out := orig.DeepCopy()
	assert.Nil(t, out)
}

func TestDeepCopy_AllowedExtraKubernetesTypeConfig(t *testing.T) {
	orig := &AllowedExtraKubernetesTypeConfig{Group: "apps", Version: "v1", Kind: "Deployment", Resource: "deployments"}
	out := orig.DeepCopy()
	require.NotNil(t, out)
	assert.Equal(t, orig.Group, out.Group)
	assert.Equal(t, orig.Version, out.Version)
	assert.Equal(t, orig.Kind, out.Kind)
}

func TestDeepCopy_AllowedExtraKubernetesTypeConfig_Nil(t *testing.T) {
	var orig *AllowedExtraKubernetesTypeConfig
	out := orig.DeepCopy()
	assert.Nil(t, out)
}

func TestDeepCopy_AuthzConfig(t *testing.T) {
	orig := &AuthzConfig{TokenURL: "https://token.example.com", TokenScope: "openid"}
	out := orig.DeepCopy()
	require.NotNil(t, out)
	assert.Equal(t, orig.TokenURL, out.TokenURL)
	assert.Equal(t, orig.TokenScope, out.TokenScope)
}

func TestDeepCopy_AuthzConfig_Nil(t *testing.T) {
	var orig *AuthzConfig
	out := orig.DeepCopy()
	assert.Nil(t, out)
}

func TestDeepCopy_Config_Empty(t *testing.T) {
	orig := &Config{}
	out := orig.DeepCopy()
	require.NotNil(t, out)
	assert.Equal(t, orig.Environment, out.Environment)
}

func TestDeepCopy_Config_WithEnvironment(t *testing.T) {
	orig := &Config{Environment: EnvironmentProduction}
	out := orig.DeepCopy()
	require.NotNil(t, out)
	assert.Equal(t, EnvironmentProduction, out.Environment)
	assert.NotSame(t, orig, out)
}

func TestDeepCopy_Config_Nil(t *testing.T) {
	var orig *Config
	out := orig.DeepCopy()
	assert.Nil(t, out)
}

func TestDeepCopy_InternalPersistentStorageConfig(t *testing.T) {
	orig := &InternalPersistentStorageConfig{StorageClassName: "fast-ssd"}
	out := orig.DeepCopy()
	require.NotNil(t, out)
	assert.Equal(t, orig.StorageClassName, out.StorageClassName)
}

func TestDeepCopy_InternalPersistentStorageConfig_Nil(t *testing.T) {
	var orig *InternalPersistentStorageConfig
	out := orig.DeepCopy()
	assert.Nil(t, out)
}

func TestDeepCopy_NVCFClusterConfig_Empty(t *testing.T) {
	orig := &NVCFClusterConfig{}
	out := orig.DeepCopy()
	require.NotNil(t, out)
}

func TestDeepCopy_NVCFClusterConfig_WithData(t *testing.T) {
	orig := &NVCFClusterConfig{ID: "cluster-001", Region: "us-east-1", Name: "my-cluster"}
	out := orig.DeepCopy()
	require.NotNil(t, out)
	assert.Equal(t, orig.ID, out.ID)
	assert.Equal(t, orig.Region, out.Region)
}

func TestDeepCopy_NVCFClusterConfig_Nil(t *testing.T) {
	var orig *NVCFClusterConfig
	out := orig.DeepCopy()
	assert.Nil(t, out)
}

func TestDeepCopy_ResourceList_Nil(t *testing.T) {
	var orig ResourceList
	out := orig.DeepCopy()
	assert.Nil(t, out)
}

func TestDeepCopy_ResourceList_WithValues(t *testing.T) {
	orig := ResourceList{
		corev1.ResourceCPU:    resource.MustParse("100m"),
		corev1.ResourceMemory: resource.MustParse("256Mi"),
	}
	out := orig.DeepCopy()
	assert.Equal(t, len(orig), len(out))
	cpuOrig := orig[corev1.ResourceCPU]
	cpuOut := out[corev1.ResourceCPU]
	assert.Equal(t, cpuOrig.String(), cpuOut.String())
}

func TestDeepCopy_ResourceRequirements_Empty(t *testing.T) {
	orig := &ResourceRequirements{}
	out := orig.DeepCopy()
	require.NotNil(t, out)
}

func TestDeepCopy_ResourceRequirements_WithLimits(t *testing.T) {
	orig := &ResourceRequirements{
		Limits: ResourceList{corev1.ResourceCPU: resource.MustParse("500m")},
	}
	out := orig.DeepCopy()
	require.NotNil(t, out)
	cpuOrig := orig.Limits[corev1.ResourceCPU]
	cpuOut := out.Limits[corev1.ResourceCPU]
	assert.Equal(t, cpuOrig.String(), cpuOut.String())
}

func TestDeepCopy_ResourceRequirements_Nil(t *testing.T) {
	var orig *ResourceRequirements
	out := orig.DeepCopy()
	assert.Nil(t, out)
}

func TestDeepCopy_SharedStorageConfig(t *testing.T) {
	orig := &SharedStorageConfig{Server: SharedStorageServerConfig{Image: "nfs:latest"}}
	out := orig.DeepCopy()
	require.NotNil(t, out)
	assert.Equal(t, orig.Server.Image, out.Server.Image)
}

func TestDeepCopy_SharedStorageConfig_Nil(t *testing.T) {
	var orig *SharedStorageConfig
	out := orig.DeepCopy()
	assert.Nil(t, out)
}

func TestDeepCopy_SharedStorageServerConfig(t *testing.T) {
	orig := &SharedStorageServerConfig{Image: "smbserver:v1.2.3"}
	out := orig.DeepCopy()
	require.NotNil(t, out)
	assert.Equal(t, orig.Image, out.Image)
}

func TestDeepCopy_SharedStorageServerConfig_Nil(t *testing.T) {
	var orig *SharedStorageServerConfig
	out := orig.DeepCopy()
	assert.Nil(t, out)
}

func TestDeepCopy_SharedStorageTaskDataConfig_Empty(t *testing.T) {
	orig := &SharedStorageTaskDataConfig{}
	out := orig.DeepCopy()
	require.NotNil(t, out)
}

func TestDeepCopy_SharedStorageTaskDataConfig_Nil(t *testing.T) {
	var orig *SharedStorageTaskDataConfig
	out := orig.DeepCopy()
	assert.Nil(t, out)
}

func TestDeepCopy_TracingConfig(t *testing.T) {
	orig := &TracingConfig{LightstepServiceName: "my-service"}
	out := orig.DeepCopy()
	require.NotNil(t, out)
	assert.Equal(t, orig.LightstepServiceName, out.LightstepServiceName)
}

func TestDeepCopy_TracingConfig_Nil(t *testing.T) {
	var orig *TracingConfig
	out := orig.DeepCopy()
	assert.Nil(t, out)
}

func TestDeepCopy_ValidationPolicyConfig_Empty(t *testing.T) {
	orig := &ValidationPolicyConfig{}
	out := orig.DeepCopy()
	require.NotNil(t, out)
}

func TestDeepCopy_ValidationPolicyConfig_WithData(t *testing.T) {
	orig := &ValidationPolicyConfig{
		Name: "Default",
		AllowedExtraKubernetesTypes: []AllowedExtraKubernetesTypeConfig{
			{Group: "apps", Version: "v1", Kind: "Deployment"},
		},
	}
	out := orig.DeepCopy()
	require.NotNil(t, out)
	assert.Equal(t, orig.Name, out.Name)
	assert.Len(t, out.AllowedExtraKubernetesTypes, 1)
}

func TestDeepCopy_ValidationPolicyConfig_Nil(t *testing.T) {
	var orig *ValidationPolicyConfig
	out := orig.DeepCopy()
	assert.Nil(t, out)
}

func TestDeepCopy_WebhookConfig(t *testing.T) {
	orig := &WebhookConfig{SvcAddress: "webhook-svc:443", TLSCertFile: "/etc/tls/cert.pem"}
	out := orig.DeepCopy()
	require.NotNil(t, out)
	assert.Equal(t, orig.SvcAddress, out.SvcAddress)
	assert.Equal(t, orig.TLSCertFile, out.TLSCertFile)
}

func TestDeepCopy_WebhookConfig_Nil(t *testing.T) {
	var orig *WebhookConfig
	out := orig.DeepCopy()
	assert.Nil(t, out)
}

func TestDeepCopy_WorkloadConfig_Empty(t *testing.T) {
	orig := &WorkloadConfig{}
	out := orig.DeepCopy()
	require.NotNil(t, out)
}

func TestDeepCopy_WorkloadConfig_Nil(t *testing.T) {
	var orig *WorkloadConfig
	out := orig.DeepCopy()
	assert.Nil(t, out)
}

func TestDeepCopy_WorkloadTimeConfig(t *testing.T) {
	orig := &WorkloadTimeConfig{WorkerDegradationTimeout: 2 * time.Hour}
	out := orig.DeepCopy()
	require.NotNil(t, out)
	assert.Equal(t, orig.WorkerDegradationTimeout, out.WorkerDegradationTimeout)
}

func TestDeepCopy_WorkloadTimeConfig_Nil(t *testing.T) {
	var orig *WorkloadTimeConfig
	out := orig.DeepCopy()
	assert.Nil(t, out)
}
