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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:openapi-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type StorageRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   StorageRequestSpec   `json:"spec,omitempty"`
	Status StorageRequestStatus `json:"status,omitempty"`
}

// +k8s:openapi-gen=true
type StorageRequestSpec struct {
	Type StorageRequestType `json:"type"`
	// RequestName is the name of the ICMS request that requested this storage.
	RequestName string `json:"requestName"`
	// RequestNamespace is the namespace of the ICMS request.
	RequestNamespace          string                         `json:"requestNamespace,omitempty"`
	ModelCache                *ModelCacheSpec                `json:"modelCache,omitempty"`
	SharedStorage             *SharedStorageSpec             `json:"sharedStorage,omitempty"`
	InternalPersistentStorage *InternalPersistentStorageSpec `json:"internalPersistentStorage,omitempty"`
}

type StorageRequestType string

const (
	ModelCacheRequest                StorageRequestType = "modelcache"
	SharedStorageRequest             StorageRequestType = "sharedstorage"
	InternalPersistentStorageRequest StorageRequestType = "internalpersistentstorage"
)

func (sType StorageRequestType) Name() string {
	switch sType {
	case SharedStorageRequest:
		return "shared-storage"
	case ModelCacheRequest:
		return "nvmesh-model-cache"
	case InternalPersistentStorageRequest:
		return "internal-persistent-storage"
	default:
		return ""
	}
}

type ModelCacheSpec struct {
	CacheHandle string                `json:"cacheHandle"`
	Encryption  *ModelCacheEncryption `json:"encryption,omitempty"`
}

type ModelCacheEncryption struct {
	Required bool `json:"required"`
}

type SharedStorageSpec struct {
	SMBContainerImage     string                     `json:"smbContainerImage"`
	WorkerPullSecretName  string                     `json:"workerPullSecretName"`
	WorkerPullSecretNames []string                   `json:"workerPullSecretNames"`
	Size                  resource.Quantity          `json:"size,omitempty"`
	Server                *SharedStorageServerSpec   `json:"server,omitempty"`
	TaskData              *SharedStorageTaskDataSpec `json:"taskData,omitempty"`
}

type SharedStorageServerSpec struct {
	SMBServerContainerResources *corev1.ResourceRequirements `json:"smbServerContainerResources,omitempty"`
	SMBServerPodNodeAffinity    *corev1.Affinity             `json:"smbServerPodNodeAffinity,omitempty"`
	SMBServerPodTolerations     []corev1.Toleration          `json:"smbServerPodTolerations,omitempty"`
}

type SharedStorageTaskDataSpec struct {
	StorageClassName *string           `json:"storageClassName,omitempty"`
	PVMountOptions   []string          `json:"pvMountOptions,omitempty"`
	Size             resource.Quantity `json:"size,omitempty"`
}

type InternalPersistentStorageSpec struct {
	StorageClassName string                                     `json:"storageClassName,omitempty"`
	ResourceQuota    InternalPersistentStorageResourceQuotaSpec `json:"resourceQuota,omitempty"`
}

type InternalPersistentStorageResourceQuotaSpec struct {
	Hard corev1.ResourceList `json:"hard,omitempty"`
}

// +k8s:openapi-gen=true
type StorageRequestStatus struct {
	LastPhaseTransitionTime   *metav1.Time                     `json:"lastPhaseTransitionTime,omitempty"`
	Phase                     StoragePhase                     `json:"phase,omitempty"`
	Conditions                []metav1.Condition               `json:"conditions,omitempty"`
	ModelCache                *ModelCacheStatus                `json:"modelCache,omitempty"`
	SharedStorage             *SharedStorageStatus             `json:"sharedStorage,omitempty"`
	InternalPersistentStorage *InternalPersistentStorageStatus `json:"internalPersistentStorage,omitempty"`
}

type ModelCacheStatus struct {
	ROPVCName    string `json:"readOnlyPVCName"`
	VolumeHandle string `json:"volumeHandle"`
}

type SharedStorageStatus struct {
	KNS      SharedStorageTypeStatus  `json:"kns,omitempty"`
	Secrets  SharedStorageTypeStatus  `json:"secrets,omitempty"`
	TaskData *SharedStorageTypeStatus `json:"taskData,omitempty"`
}

type SharedStorageTypeStatus struct {
	CreatePVCIfNotExists bool                              `json:"createPVCIfNotExists"`
	ReadWritePVName      string                            `json:"readWritePVName,omitempty"`
	ReadWritePVCName     string                            `json:"readWritePVCName,omitempty"`
	ReadWriteAccessMode  corev1.PersistentVolumeAccessMode `json:"readWriteAccessMode,omitempty"`
	ReadOnlyPVName       string                            `json:"readOnlyPVName,omitempty"`
	ReadOnlyPVCName      string                            `json:"readOnlyPVCName,omitempty"`
	ReadOnlyAccessMode   corev1.PersistentVolumeAccessMode `json:"readOnlyAccessMode,omitempty"`
	StorageClassName     string                            `json:"storageClassName,omitempty"`
	StorageCapacity      resource.Quantity                 `json:"storageCapacity,omitempty"`
}

type InternalPersistentStorageStatus struct {
	StorageClassName string `json:"storageClassName"`
}

type StoragePhase string

const (
	StorageUnknown      StoragePhase = ""
	StoragePending      StoragePhase = "Pending"
	StorageInitRunning  StoragePhase = "InitRunning"
	StorageCreating     StoragePhase = "Creating"
	StorageReady        StoragePhase = "Ready"
	StorageFailed       StoragePhase = "Failed"
	StorageRuntimeError StoragePhase = "RuntimeError"
)

func (p StoragePhase) String() string { return string(p) }

func (p StoragePhase) IsEndState() bool {
	return p == StorageFailed || p == StorageRuntimeError || p == StorageReady
}

// +k8s:openapi-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type StorageRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []StorageRequest `json:"items"`
}
