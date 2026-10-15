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

// Package v2beta1 provides conversion between nvca v1 and v2beta1 StorageRequest
// so that get/update can work with both versions; create uses v2beta1 only.

package v2beta1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvcav1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
)

// StorageRequestFromV1 converts a v1 StorageRequest to v2beta1 (e.g. when reading v1 from the API).
func StorageRequestFromV1(in *nvcav1.StorageRequest) *StorageRequest {
	if in == nil {
		return nil
	}
	out := &StorageRequest{
		TypeMeta:   in.TypeMeta,
		ObjectMeta: in.ObjectMeta,
		Spec:       storageRequestSpecFromV1(&in.Spec),
		Status:     storageRequestStatusFromV1(&in.Status),
	}
	out.APIVersion = SchemeGroupVersion.String()
	out.Kind = "StorageRequest"
	return out
}

// StorageRequestToV1 converts a v2beta1 StorageRequest to v1 (e.g. for reconciler that expects v1 or for updating v1 resources).
func StorageRequestToV1(in *StorageRequest) *nvcav1.StorageRequest {
	if in == nil {
		return nil
	}
	out := &nvcav1.StorageRequest{
		TypeMeta:   in.TypeMeta,
		ObjectMeta: in.ObjectMeta,
		Spec:       storageRequestSpecToV1(&in.Spec),
		Status:     storageRequestStatusToV1(&in.Status),
	}
	out.APIVersion = nvcav1.SchemeGroupVersion.String()
	out.Kind = "StorageRequest"
	return out
}

// ApplyV1SpecAndStatus sets dst's Spec and Status from the v1 forms (used when updating a v2beta1 StorageRequest from reconciler state).
func ApplyV1SpecAndStatus(dst *StorageRequest, spec *nvcav1.StorageRequestSpec, status *nvcav1.StorageRequestStatus) {
	if dst == nil {
		return
	}
	if spec != nil {
		dst.Spec = storageRequestSpecFromV1(spec)
	}
	if status != nil {
		dst.Status = storageRequestStatusFromV1(status)
	}
}

func storageRequestSpecFromV1(s *nvcav1.StorageRequestSpec) StorageRequestSpec {
	if s == nil {
		return StorageRequestSpec{}
	}
	out := StorageRequestSpec{
		Type:             StorageRequestType(s.Type),
		RequestName:      s.ICMSRequestName,
		RequestNamespace: s.ICMSRequestNamespace,
	}
	if s.ModelCache != nil {
		out.ModelCache = &ModelCacheSpec{
			CacheHandle: s.ModelCache.CacheHandle,
			Backend:     s.ModelCache.Backend,
			Encryption:  nil,
		}
		if s.ModelCache.Encryption != nil {
			out.ModelCache.Encryption = &ModelCacheEncryption{Required: s.ModelCache.Encryption.Required}
		}
	}
	if s.SharedStorage != nil {
		out.SharedStorage = sharedStorageSpecFromV1(s.SharedStorage)
	}
	if s.InternalPersistentStorage != nil {
		out.InternalPersistentStorage = internalPersistentStorageSpecFromV1(s.InternalPersistentStorage)
	}
	return out
}

func storageRequestSpecToV1(s *StorageRequestSpec) nvcav1.StorageRequestSpec {
	if s == nil {
		return nvcav1.StorageRequestSpec{}
	}
	out := nvcav1.StorageRequestSpec{
		Type:                 nvcav1.StorageRequestType(s.Type),
		ICMSRequestName:      s.RequestName,
		ICMSRequestNamespace: s.RequestNamespace,
	}
	if s.ModelCache != nil {
		out.ModelCache = &nvcav1.ModelCacheSpec{
			CacheHandle: s.ModelCache.CacheHandle,
			Backend:     s.ModelCache.Backend,
			Encryption:  nil,
		}
		if s.ModelCache.Encryption != nil {
			out.ModelCache.Encryption = &nvcav1.ModelCacheEncryption{Required: s.ModelCache.Encryption.Required}
		}
	}
	if s.SharedStorage != nil {
		out.SharedStorage = sharedStorageSpecToV1(s.SharedStorage)
	}
	if s.InternalPersistentStorage != nil {
		out.InternalPersistentStorage = internalPersistentStorageSpecToV1(s.InternalPersistentStorage)
	}
	return out
}

func sharedStorageSpecFromV1(s *nvcav1.SharedStorageSpec) *SharedStorageSpec {
	if s == nil {
		return nil
	}
	out := &SharedStorageSpec{
		SMBContainerImage:     s.SMBContainerImage,
		WorkerPullSecretName:  s.WorkerPullSecretName,
		WorkerPullSecretNames: append([]string(nil), s.WorkerPullSecretNames...),
		Size:                  s.Size,
	}
	if s.Server != nil {
		out.Server = &SharedStorageServerSpec{
			SMBServerContainerResources: s.Server.SMBServerContainerResources,
			SMBServerPodNodeAffinity:    s.Server.SMBServerPodNodeAffinity,
			SMBServerPodTolerations:     append([]corev1.Toleration(nil), s.Server.SMBServerPodTolerations...),
		}
	}
	if s.TaskData != nil {
		out.TaskData = &SharedStorageTaskDataSpec{
			StorageClassName: s.TaskData.StorageClassName,
			PVMountOptions:   append([]string(nil), s.TaskData.PVMountOptions...),
			Size:             s.TaskData.Size,
		}
	}
	return out
}

func sharedStorageSpecToV1(s *SharedStorageSpec) *nvcav1.SharedStorageSpec {
	if s == nil {
		return nil
	}
	out := &nvcav1.SharedStorageSpec{
		SMBContainerImage:     s.SMBContainerImage,
		WorkerPullSecretName:  s.WorkerPullSecretName,
		WorkerPullSecretNames: append([]string(nil), s.WorkerPullSecretNames...),
		Size:                  s.Size,
	}
	if s.Server != nil {
		out.Server = &nvcav1.SharedStorageServerSpec{
			SMBServerContainerResources: s.Server.SMBServerContainerResources,
			SMBServerPodNodeAffinity:    s.Server.SMBServerPodNodeAffinity,
			SMBServerPodTolerations:     append([]corev1.Toleration(nil), s.Server.SMBServerPodTolerations...),
		}
	}
	if s.TaskData != nil {
		out.TaskData = &nvcav1.SharedStorageTaskDataSpec{
			StorageClassName: s.TaskData.StorageClassName,
			PVMountOptions:   append([]string(nil), s.TaskData.PVMountOptions...),
			Size:             s.TaskData.Size,
		}
	}
	return out
}

func internalPersistentStorageSpecFromV1(s *nvcav1.InternalPersistentStorageSpec) *InternalPersistentStorageSpec {
	if s == nil {
		return nil
	}
	return &InternalPersistentStorageSpec{
		StorageClassName: s.StorageClassName,
		ResourceQuota:    InternalPersistentStorageResourceQuotaSpec{Hard: s.ResourceQuota.Hard},
	}
}

func internalPersistentStorageSpecToV1(s *InternalPersistentStorageSpec) *nvcav1.InternalPersistentStorageSpec {
	if s == nil {
		return nil
	}
	return &nvcav1.InternalPersistentStorageSpec{
		StorageClassName: s.StorageClassName,
		ResourceQuota:    nvcav1.InternalPersistentStorageResourceQuotaSpec{Hard: s.ResourceQuota.Hard},
	}
}

func storageRequestStatusFromV1(s *nvcav1.StorageRequestStatus) StorageRequestStatus {
	if s == nil {
		return StorageRequestStatus{}
	}
	out := StorageRequestStatus{
		LastPhaseTransitionTime:   s.LastPhaseTransitionTime,
		Phase:                     StoragePhase(s.Phase),
		Conditions:                append([]metav1.Condition(nil), s.Conditions...),
		ModelCache:                nil,
		SharedStorage:             nil,
		InternalPersistentStorage: nil,
	}
	if s.ModelCache != nil {
		out.ModelCache = &ModelCacheStatus{ROPVCName: s.ModelCache.ROPVCName, VolumeHandle: s.ModelCache.VolumeHandle}
	}
	if s.SharedStorage != nil {
		out.SharedStorage = &SharedStorageStatus{
			KNS:      sharedStorageTypeStatusFromV1(&s.SharedStorage.KNS),
			Secrets:  sharedStorageTypeStatusFromV1(&s.SharedStorage.Secrets),
			TaskData: nil,
		}
		if s.SharedStorage.TaskData != nil {
			out.SharedStorage.TaskData = ptr(sharedStorageTypeStatusFromV1(s.SharedStorage.TaskData))
		}
	}
	if s.InternalPersistentStorage != nil {
		out.InternalPersistentStorage = &InternalPersistentStorageStatus{StorageClassName: s.InternalPersistentStorage.StorageClassName}
	}
	return out
}

func storageRequestStatusToV1(s *StorageRequestStatus) nvcav1.StorageRequestStatus {
	if s == nil {
		return nvcav1.StorageRequestStatus{}
	}
	out := nvcav1.StorageRequestStatus{
		LastPhaseTransitionTime:   s.LastPhaseTransitionTime,
		Phase:                     nvcav1.StoragePhase(s.Phase),
		Conditions:                append([]metav1.Condition(nil), s.Conditions...),
		ModelCache:                nil,
		SharedStorage:             nil,
		InternalPersistentStorage: nil,
	}
	if s.ModelCache != nil {
		out.ModelCache = &nvcav1.ModelCacheStatus{ROPVCName: s.ModelCache.ROPVCName, VolumeHandle: s.ModelCache.VolumeHandle}
	}
	if s.SharedStorage != nil {
		out.SharedStorage = &nvcav1.SharedStorageStatus{
			KNS:      sharedStorageTypeStatusToV1(&s.SharedStorage.KNS),
			Secrets:  sharedStorageTypeStatusToV1(&s.SharedStorage.Secrets),
			TaskData: nil,
		}
		if s.SharedStorage.TaskData != nil {
			out.SharedStorage.TaskData = ptr(sharedStorageTypeStatusToV1(s.SharedStorage.TaskData))
		}
	}
	if s.InternalPersistentStorage != nil {
		out.InternalPersistentStorage = &nvcav1.InternalPersistentStorageStatus{StorageClassName: s.InternalPersistentStorage.StorageClassName}
	}
	return out
}

func sharedStorageTypeStatusFromV1(s *nvcav1.SharedStorageTypeStatus) SharedStorageTypeStatus {
	if s == nil {
		return SharedStorageTypeStatus{}
	}
	return SharedStorageTypeStatus{
		CreatePVCIfNotExists: s.CreatePVCIfNotExists,
		ReadWritePVName:      s.ReadWritePVName,
		ReadWritePVCName:     s.ReadWritePVCName,
		ReadWriteAccessMode:  s.ReadWriteAccessMode,
		ReadOnlyPVName:       s.ReadOnlyPVName,
		ReadOnlyPVCName:      s.ReadOnlyPVCName,
		ReadOnlyAccessMode:   s.ReadOnlyAccessMode,
		StorageClassName:     s.StorageClassName,
		StorageCapacity:      s.StorageCapacity,
	}
}

func sharedStorageTypeStatusToV1(s *SharedStorageTypeStatus) nvcav1.SharedStorageTypeStatus {
	if s == nil {
		return nvcav1.SharedStorageTypeStatus{}
	}
	return nvcav1.SharedStorageTypeStatus{
		CreatePVCIfNotExists: s.CreatePVCIfNotExists,
		ReadWritePVName:      s.ReadWritePVName,
		ReadWritePVCName:     s.ReadWritePVCName,
		ReadWriteAccessMode:  s.ReadWriteAccessMode,
		ReadOnlyPVName:       s.ReadOnlyPVName,
		ReadOnlyPVCName:      s.ReadOnlyPVCName,
		ReadOnlyAccessMode:   s.ReadOnlyAccessMode,
		StorageClassName:     s.StorageClassName,
		StorageCapacity:      s.StorageCapacity,
	}
}

func ptr[T any](v T) *T { return &v }
