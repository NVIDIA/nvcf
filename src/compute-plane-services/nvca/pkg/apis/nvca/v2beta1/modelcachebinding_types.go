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

package v2beta1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// ModelCacheBindingFinalizer protects provider resources while a binding is in use or retiring.
	ModelCacheBindingFinalizer = "nvca.nvcf.nvidia.io/model-cache-binding-finalizer"
)

// ModelCacheWorkflow identifies the model-cache workflow sharing a binding.
type ModelCacheWorkflow string

const (
	// ModelCacheWorkflowRegular identifies the regular container model-cache workflow.
	ModelCacheWorkflowRegular ModelCacheWorkflow = "regularModelCache"
	// ModelCacheWorkflowHelm identifies the Helm model-cache workflow.
	ModelCacheWorkflowHelm ModelCacheWorkflow = "helmModelCache"
)

// ModelCacheBindingPhase is the lifecycle state of a cache binding.
type ModelCacheBindingPhase string

const (
	// ModelCacheBindingPhaseActive accepts new request references.
	ModelCacheBindingPhaseActive ModelCacheBindingPhase = "Active"
	// ModelCacheBindingPhaseRetiring rejects new references while resources are released.
	ModelCacheBindingPhaseRetiring ModelCacheBindingPhase = "Retiring"
)

// ModelCachePopulationState is the state of the shared cache data identity.
type ModelCachePopulationState string

const (
	// ModelCachePopulationPending means population has not started.
	ModelCachePopulationPending ModelCachePopulationState = "Pending"
	// ModelCachePopulationPopulating means a writer is populating the cache.
	ModelCachePopulationPopulating ModelCachePopulationState = "Populating"
	// ModelCachePopulationReady means the cache is ready for readers.
	ModelCachePopulationReady ModelCachePopulationState = "Ready"
	// ModelCachePopulationFailed means the latest population attempt failed.
	ModelCachePopulationFailed ModelCachePopulationState = "Failed"
)

// ModelCacheBinding records one immutable provider decision for a shared model-cache key.
// +genclient
// +k8s:openapi-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ModelCacheBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ModelCacheBindingSpec   `json:"spec"`
	Status ModelCacheBindingStatus `json:"status,omitempty"`
}

// ModelCacheBindingSpec contains the immutable identity, provider decision, and resource intent.
// +k8s:openapi-gen=true
type ModelCacheBindingSpec struct {
	Identity     ModelCacheBindingIdentity       `json:"identity"`
	Decision     ModelCacheBindingDecision       `json:"decision"`
	StorageClass ModelCacheStorageClassSnapshot  `json:"storageClass"`
	Resources    ModelCacheBindingResourceIntent `json:"resources"`
}

// ModelCacheBindingIdentity is the stable cache key represented by a binding.
// +k8s:openapi-gen=true
type ModelCacheBindingIdentity struct {
	Version             string             `json:"version"`
	Workflow            ModelCacheWorkflow `json:"workflow"`
	SharingDomainDigest string             `json:"sharingDomainDigest"`
	CacheHandleDigest   string             `json:"cacheHandleDigest"`
}

// ModelCacheBindingDecision snapshots the selected provider transition.
// +k8s:openapi-gen=true
type ModelCacheBindingDecision struct {
	Provider    string `json:"provider"`
	Provisioner string `json:"provisioner"`
	Transition  string `json:"transition"`
	// +listType=set
	RequiredAccessModes []corev1.PersistentVolumeAccessMode `json:"requiredAccessModes"`
	// +listType=atomic
	RequiredMountOptions []string `json:"requiredMountOptions,omitempty"`
	// ProfileDigest covers only the qualified driver profile this decision
	// selected: provisioner, provider, workflow transition, qualified access
	// modes, and reader mount options. It deliberately excludes unrelated
	// catalog entries, comments, key ordering, and whitespace so that editing
	// the catalog cannot invalidate bindings it does not describe.
	ProfileDigest string `json:"profileDigest"`
	// CatalogRevision records which catalog payload produced this decision.
	// It is audit metadata only and is never compared when matching a binding
	// to a request intent.
	CatalogRevision    string `json:"catalogRevision,omitempty"`
	EncryptionRequired bool   `json:"encryptionRequired"`
}

// ModelCacheStorageClassSnapshot identifies the exact retained StorageClass selected by the binding.
// +k8s:openapi-gen=true
type ModelCacheStorageClassSnapshot struct {
	Name                string                               `json:"name"`
	UID                 types.UID                            `json:"uid"`
	ReclaimPolicy       corev1.PersistentVolumeReclaimPolicy `json:"reclaimPolicy"`
	ConfigurationDigest string                               `json:"configurationDigest"`
}

// ModelCacheBindingResourceIntent records deterministic names for shared resources.
// +k8s:openapi-gen=true
type ModelCacheBindingResourceIntent struct {
	WriterNamespace string `json:"writerNamespace"`
	// +listType=set
	PersistentVolumeClaimNames []string `json:"persistentVolumeClaimNames,omitempty"`
	// +listType=set
	PersistentVolumeNames []string `json:"persistentVolumeNames,omitempty"`
	// +listType=set
	JobNames []string `json:"jobNames,omitempty"`
	// +listType=set
	StorageClassNames []string `json:"storageClassNames,omitempty"`
	// +listType=set
	SecretNames []string `json:"secretNames,omitempty"`
	LeaseName   string   `json:"leaseName,omitempty"`
}

// ModelCacheBindingStatus contains mutable lifecycle and realized-resource state.
// +k8s:openapi-gen=true
type ModelCacheBindingStatus struct {
	Phase                   ModelCacheBindingPhase `json:"phase,omitempty"`
	LastPhaseTransitionTime *metav1.Time           `json:"lastPhaseTransitionTime,omitempty"`
	// +listType=map
	// +listMapKey=uid
	RequestReferences []ModelCacheBindingRequestReference `json:"requestReferences,omitempty"`
	Realized          *ModelCacheBindingRealizedState     `json:"realized,omitempty"`
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ModelCacheBindingRequestReference identifies a request currently using a binding.
// +k8s:openapi-gen=true
type ModelCacheBindingRequestReference struct {
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	UID       types.UID `json:"uid"`
}

// ModelCacheBindingRealizedState records provider resources only after they exist.
// +k8s:openapi-gen=true
type ModelCacheBindingRealizedState struct {
	BoundPersistentVolumeName string                    `json:"boundPersistentVolumeName,omitempty"`
	ProviderDataIdentity      string                    `json:"providerDataIdentity,omitempty"`
	PopulationState           ModelCachePopulationState `json:"populationState,omitempty"`
}

// ModelCacheBindingList is a list of ModelCacheBinding objects.
// +k8s:openapi-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ModelCacheBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ModelCacheBinding `json:"items"`
}
