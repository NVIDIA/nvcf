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

package clustermgmt

import (
	"encoding/json"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
)

type Capability string

const (
	DynamicGPUDiscovery     Capability = "DynamicGPUDiscovery"
	CachingSupport          Capability = "CachingSupport"
	MiniServiceRestrictions Capability = "MiniServiceRestrictions"
	HelmMiniService         Capability = "HelmMiniService"
	MultipleGPUTypesAllowed Capability = "MultipleGPUTypesAllowed"
	UniformInstanceLabels   Capability = "UniformInstanceLabels"
)

// ClusterConfigurationKeyType represents the type for cluster configuration keys
type ClusterConfigurationKeyType string

// ClusterConfiguration keys for key-value pair configuration
const (
	// AgentNodeSelectorLabelKeyConfig specifies the label key used for node selector to schedule NVCA agents on specific nodes
	AgentNodeSelectorLabelKeyConfig ClusterConfigurationKeyType = "AgentNodeSelectorLabelKey"

	// AgentNodeSelectorLabelValueConfig specifies the label value used for node selector to schedule NVCA agents on specific nodes
	AgentNodeSelectorLabelValueConfig ClusterConfigurationKeyType = "AgentNodeSelectorLabelValue"

	// AgentPriorityClassNameConfig specifies the priority class name for NVCA agent pods to control scheduling priority
	AgentPriorityClassNameConfig ClusterConfigurationKeyType = "AgentPriorityClassName"

	// ModelCacheVolumeMountOptionEnabledConfig enables or disables custom mount options for model cache volumes
	ModelCacheVolumeMountOptionEnabledConfig ClusterConfigurationKeyType = "ModelCacheVolumeMountOptionEnabled"

	// ModelCacheVolumeMountOptionsConfig specifies the mount options (e.g., "vers=3.0,dir_mode=0777") for model cache volumes
	ModelCacheVolumeMountOptionsConfig ClusterConfigurationKeyType = "ModelCacheVolumeMountOptions"

	// ClusterNetworkCIDRAllowedRangeConfig defines the allowed CIDR ranges for cluster network access
	ClusterNetworkCIDRAllowedRangeConfig ClusterConfigurationKeyType = "ClusterNetworkCIDRAllowedRange"

	// NVCFWorkerDegradationPeriodMinutesConfig specifies the time period in minutes before a worker is considered degraded
	NVCFWorkerDegradationPeriodMinutesConfig ClusterConfigurationKeyType = "NVCFWorkerDegradationPeriodMinutes"

	// NVCASecretMirrorSourceNamespaceConfig specifies the source namespace from which secrets should be mirrored to function namespaces
	NVCASecretMirrorSourceNamespaceConfig ClusterConfigurationKeyType = "NVCASecretMirrorSourceNamespace"

	// NVCASecretMirrorLabelSelectorConfig specifies the label selector to identify secrets that should be mirrored to function namespaces
	NVCASecretMirrorLabelSelectorConfig ClusterConfigurationKeyType = "NVCASecretMirrorLabelSelector"
)

// CaseInsensitiveClusterConfigMap provides case-insensitive key access to cluster configurations
type CaseInsensitiveClusterConfigMap map[ClusterConfigurationKeyType]string

// UnmarshalJSON implements custom unmarshaling to normalize keys to lowercase for case-insensitive matching
func (m *CaseInsensitiveClusterConfigMap) UnmarshalJSON(data []byte) error {
	// Unmarshal into a temporary map with string keys
	temp := make(map[string]string)
	if err := json.Unmarshal(data, &temp); err != nil {
		return err
	}

	// Create the normalized map
	normalized := make(map[ClusterConfigurationKeyType]string)

	// Build a lookup map of lowercase known keys to their canonical forms
	knownKeys := map[string]ClusterConfigurationKeyType{
		strings.ToLower(string(AgentNodeSelectorLabelKeyConfig)):          AgentNodeSelectorLabelKeyConfig,
		strings.ToLower(string(AgentNodeSelectorLabelValueConfig)):        AgentNodeSelectorLabelValueConfig,
		strings.ToLower(string(AgentPriorityClassNameConfig)):             AgentPriorityClassNameConfig,
		strings.ToLower(string(ModelCacheVolumeMountOptionEnabledConfig)): ModelCacheVolumeMountOptionEnabledConfig,
		strings.ToLower(string(ModelCacheVolumeMountOptionsConfig)):       ModelCacheVolumeMountOptionsConfig,
		strings.ToLower(string(ClusterNetworkCIDRAllowedRangeConfig)):     ClusterNetworkCIDRAllowedRangeConfig,
		strings.ToLower(string(NVCFWorkerDegradationPeriodMinutesConfig)): NVCFWorkerDegradationPeriodMinutesConfig,
		strings.ToLower(string(NVCASecretMirrorSourceNamespaceConfig)):    NVCASecretMirrorSourceNamespaceConfig,
		strings.ToLower(string(NVCASecretMirrorLabelSelectorConfig)):      NVCASecretMirrorLabelSelectorConfig,
	}

	// Normalize keys by matching them case-insensitively to known keys
	for key, value := range temp {
		lowerKey := strings.ToLower(key)
		if canonicalKey, found := knownKeys[lowerKey]; found {
			normalized[canonicalKey] = value
		} else {
			// If key is not recognized, store it as-is (for forward compatibility)
			normalized[ClusterConfigurationKeyType(key)] = value
		}
	}

	*m = CaseInsensitiveClusterConfigMap(normalized)
	return nil
}

// MarshalJSON implements custom marshaling
func (m CaseInsensitiveClusterConfigMap) MarshalJSON() ([]byte, error) {
	// Convert to a regular map for marshaling
	temp := make(map[string]string, len(m))
	for k, v := range m {
		temp[string(k)] = v
	}
	return json.Marshal(temp)
}

type agentDTO struct {
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	NATSURL     string              `json:"natsURL,omitempty"`

	HelmReValStageOAuthTokenURL             string `json:"helmReValStageOAuthTokenURL,omitempty"`
	HelmReValStageOAuthPublicKeysetEndpoint string `json:"helmReValStageOAuthPublicKeysetEndpoint,omitempty"`
	HelmReValProdOAuthTokenURL              string `json:"helmReValProdOAuthTokenURL,omitempty"`
	HelmReValProdOAuthPublicKeysetEndpoint  string `json:"helmReValProdOAuthPublicKeysetEndpoint,omitempty"`

	RolloverServiceStageOAuthTokenURL             string `json:"rolloverServiceStageOAuthTokenURL,omitempty"`
	RolloverServiceStageOAuthPublicKeysetEndpoint string `json:"rolloverServiceStageOAuthPublicKeysetEndpoint,omitempty"`
	RolloverServiceProdOAuthTokenURL              string `json:"rolloverServiceProdOAuthTokenURL,omitempty"`
	RolloverServiceProdOAuthPublicKeysetEndpoint  string `json:"rolloverServiceProdOAuthPublicKeysetEndpoint,omitempty"`

	FunctionDeploymentStagesStageOAuthTokenURL             string `json:"functionDeploymentStagesStageOAuthTokenURL,omitempty"`
	FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint string `json:"functionDeploymentStagesStageOAuthPublicKeysetEndpoint,omitempty"`
	FunctionDeploymentStagesProdOAuthTokenURL              string `json:"functionDeploymentStagesProdOAuthTokenURL,omitempty"`
	FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint  string `json:"functionDeploymentStagesProdOAuthPublicKeysetEndpoint,omitempty"`
}

// clusterDTO represents an NVCA clusterDTO definition stored in the NGC API
type clusterDTO struct {
	ID            string              `json:"clusterId"`
	Name          string              `json:"clusterName"`
	Description   string              `json:"clusterDescription"`
	GroupName     string              `json:"clusterGroupName"`
	GroupID       string              `json:"clusterGroupId"`
	Status        types.ClusterStatus `json:"status"`
	LastConnected metav1.Time         `json:"nvcaLastConnected"`

	NCAID string `json:"ncaID"`

	NVCAVersion            string   `json:"nvcaVersion"`
	OAuthClientID          string   `json:"oAuthClientId"`
	OAuthClientMountPath   string   `json:"oAuthClientMountPath,omitempty"`
	CloudProvider          string   `json:"cloudProvider"`
	Region                 string   `json:"region"`
	Attributes             []string `json:"attributes"`
	CustomAttributes       []string `json:"customAttributes"`
	Capabilities           []string `json:"capabilities"`
	K8sClusterNetworkCIDRs []string `json:"k8sClusterNetworkCIDRs,omitempty"`

	GPUs    []registrationGPU `json:"gpus"`
	GPUsB64 string            `json:"gpusB64"`

	ICMSConfig struct {
		PublicKeysetEndpoint string `json:"publicKeysetEndpoint"`
		TokenURL             string `json:"tokenURL"`
		ICMSServiceURL       string `json:"icmsServiceURL"`
	} `json:"icmsConfig"`
	VaultConfig struct {
		Address string `json:"address"`
	} `json:"vaultConfig"`
	// ClusterConfigurations contains key-value pairs for cluster-specific configuration
	ClusterConfigurations CaseInsensitiveClusterConfigMap `json:"clusterConfigurations,omitempty"`
	// ImageCredHelper configures the "nvcf-image-credential-helper" image in NVCA.
	ImageCredHelper *struct {
		ImageConfig struct {
			Repository string `json:"repository"`
			Tag        string `json:"tag"`
		} `json:"imageConfig,omitempty"`
	} `json:"imageCredentialHelper,omitempty"`
	// WebhookConfig configures the NVCA admission webhook image. Without this,
	// json.Unmarshal silently drops the chart's webhookConfig values and the
	// NVCFBackend Spec.WebhookConfig stays empty, so the webhook image resolver
	// falls back to `nvca/nvca:` (empty tag, InvalidImageName).
	WebhookConfig *struct {
		ImageConfig struct {
			Repository string `json:"repository"`
			Tag        string `json:"tag"`
		} `json:"imageConfig,omitempty"`
	} `json:"webhookConfig,omitempty"`
	// SharedStorage configures the shared storage image in NVCA.
	SharedStorage *struct {
		ImageRepository string `json:"imageRepository"`
		ImageTag        string `json:"imageTag"`
	} `json:"sharedStorage,omitempty"`
	// OTelCollector configures the OpenTelemetry Collector sidecar image in NVCA.
	OTelCollector *struct {
		Enabled     bool `json:"enabled"`
		ImageConfig struct {
			Repository string `json:"repository"`
			Tag        string `json:"tag"`
		} `json:"imageConfig,omitempty"`
	} `json:"otelCollector,omitempty"`
	// MiniService configures the MiniService (ReVal) settings.
	MiniService *struct {
		HelmReValServiceURL string `json:"helmReValServiceURL"`
	} `json:"miniService,omitempty"`
	// Agent configures NVCA agent-specific settings sourced from cluster DTO.
	Agent *agentDTO `json:"agent,omitempty"`
}

// getClientID returns the OAuth client ID.
func (c *clusterDTO) getClientID() string {
	return c.OAuthClientID
}

// VaultEnabled returns true when an OAuth client ID is specified.
func (c *clusterDTO) vaultEnabled() bool {
	return c.getClientID() != ""
}

func (c *clusterDTO) containsCapability(target Capability) bool {
	for _, c := range c.Capabilities {
		if Capability(c) == target {
			return true
		}
	}
	return false
}

type registrationGPU struct {
	Name          string                     `json:"name,omitempty"`
	Capacity      uint32                     `json:"capacity"`
	InstanceTypes []registrationInstanceType `json:"instanceTypes,omitempty"`
}

type registrationInstanceType struct {
	Name         string `json:"name,omitempty"`
	Value        string `json:"value,omitempty"`
	Description  string `json:"description,omitempty"`
	Default      bool   `json:"default,omitempty"`
	CPUCores     uint64 `json:"cpuCores,omitempty"`
	SystemMemory string `json:"systemMemory,omitempty"`
	GPUMemory    string `json:"gpuMemory,omitempty"`
	GPUCount     uint64 `json:"gpuCount,omitempty"`
}

type Cluster struct {
	NVCFBackend *nvidiaiov1.NVCFBackend
}
