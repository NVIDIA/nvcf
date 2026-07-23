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

package v1alpha1

import (
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:openapi-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// MiniService represents a Helm Chart ICMS instance.
// Note: Legacy request resources remain supported for backwards compatibility.
type MiniService struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MiniServiceSpec   `json:"spec,omitempty"`
	Status MiniServiceStatus `json:"status,omitempty"`
}

// +k8s:openapi-gen=true
type MiniServiceSpec struct {
	// Namespace is set to the namespace created for and owned by this miniservice.
	Namespace string `json:"namespace"`
	// ICMSRequestName is the name of the ICMS request that requested this MiniService instance.
	ICMSRequestName string `json:"icmsRequestName"`

	HelmChartConfig common.HelmConfig `json:"helmChartConfig"`

	// WorkloadConfig holds workload-specific configuration decoded from the user-provided
	// workload config ConfigMap in the rendered Helm chart. It is populated by the controller
	// during installation and read on subsequent reconciles.
	// +optional
	WorkloadConfig *WorkloadConfig `json:"workloadConfig,omitempty"`
}

// WorkloadConfig is workload-specific configuration a chart author may supply through the
// workload config ConfigMap. It is intended to grow with additional workload-scoped settings
// beyond feature flags.
type WorkloadConfig struct {
	// FeatureFlags toggles per-workload behaviors, keyed by feature flag name.
	// +optional
	FeatureFlags map[string]bool `json:"featureFlags,omitempty"`
}

// IsFeatureFlagEnabled reports whether the named feature flag is enabled.
func (c *WorkloadConfig) IsFeatureFlagEnabled(key string) bool {
	if c == nil || c.FeatureFlags == nil {
		return false
	}
	return c.FeatureFlags[key]
}

type LocalObjectReference struct {
	Name string `json:"name"`
}

// +k8s:openapi-gen=true
type MiniServiceStatus struct {
	Phase                   MiniServicePhase     `json:"phase"`
	LastPhaseTransitionTime *metav1.Time         `json:"lastPhaseTransitionTime,omitempty"`
	RenderDetails           *RenderDetailsStatus `json:"renderedDetails,omitempty"`
	Conditions              []metav1.Condition   `json:"conditions,omitempty"`
	// ObservedGeneration is the most recent metadata.generation observed by the controller.
	// Used to detect spec changes (e.g., helm values updates) that require a re-render and re-apply.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Revision is incremented each time helm values are updated and successfully re-rendered.
	// The initial install is revision 0.
	Revision int64 `json:"revision,omitempty"`
}

type RenderDetailsStatus struct {
	Hash      string           `json:"hash,omitempty"`
	Resources []ResourceStatus `json:"resources,omitempty"`
}

// ResourceStatus represents an item of resources that are part of the MiniService.
// Inspired by ArgoCD's ResourceStatus type.
type ResourceStatus struct {
	// GVK represents the GroupVersionKind of the resource.
	GVK string `json:"gvk,omitempty"`
	// Names is the list of unique names of the resource within the namespace.
	Names []string `json:"-"`
	// Count is the number of resources with the same name.
	Count int `json:"count,omitempty"`
}

// +k8s:openapi-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type MiniServiceList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []MiniService `json:"items"`
}
