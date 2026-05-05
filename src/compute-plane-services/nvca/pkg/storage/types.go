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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
)

var (
	//nolint:gosec
	WebhookModelCachePVCNameAnnotationKey = fmt.Sprintf("%s/storage-%s-pvc-name", nvcav1new.SchemeGroupVersion.Group, nvcav1new.ModelCacheRequest)
	//nolint:gosec
	HelmWebhookSharedStorageSecretsReadWritePVCNameAnnotationKey = fmt.Sprintf("%s/storage-%s-secrets-rw-pvc-name",
		nvcav1new.SchemeGroupVersion.Group,
		nvcav1new.SharedStorageRequest)
	//nolint:gosec
	HelmWebhookSharedStorageSecretsReadOnlyPVCNameAnnotationKey = fmt.Sprintf("%s/storage-%s-secrets-ro-pvc-name",
		nvcav1new.SchemeGroupVersion.Group,
		nvcav1new.SharedStorageRequest)
	//nolint:gosec
	HelmWebhookSharedStorageKNSReadWritePVCNameAnnotationKey = fmt.Sprintf("%s/storage-%s-kns-rw-pvc-name",
		nvcav1new.SchemeGroupVersion.Group,
		nvcav1new.SharedStorageRequest)
	//nolint:gosec
	HelmWebhookSharedStorageKNSReadOnlyPVCNameAnnotationKey = fmt.Sprintf("%s/storage-%s-kns-ro-pvc-name",
		nvcav1new.SchemeGroupVersion.Group,
		nvcav1new.SharedStorageRequest)
	HelmWebhookInternalPersistentStorageStorageClassNameAnnotationKey = fmt.Sprintf("%s/storage-%s-storage-class-name",
		nvcav1new.SchemeGroupVersion.Group,
		nvcav1new.InternalPersistentStorageRequest)

	HelmWebhookSharedStorageTaskDataReadWritePVCNameAnnotationKey = fmt.Sprintf("%s/storage-%s-task-data-rw-pvc-name",
		nvcav1new.SchemeGroupVersion.Group,
		nvcav1new.SharedStorageRequest)

	storageRequestGVK        = nvcav1new.SchemeGroupVersion.WithKind("StorageRequest")
	storageRequestV2Beta1GVK = nvcav2beta1.SchemeGroupVersion.WithKind("StorageRequest")
)

const (
	//nolint:gosec
	SharedStorageSecretsReadWritePVCName = "nvcf-secrets-data-rw"
	//nolint:gosec
	SharedStorageSecretsReadOnlyPVCName  = "nvcf-secrets-data-ro"
	SharedStorageSecretsVolumeName       = "secrets-data"
	SharedStorageVolumeSecretsMountPath  = "/var/secrets"
	SharedStorageVolumeSecretsSambaShare = "var-secrets"

	SharedStorageTaskDataReadWritePVCName = "nvcf-task-data-rw"
	SharedStorageTaskDataVolumeName       = "task-data"
	SharedStorageVolumeTaskDataMountPath  = "/var/task"
	SharedStorageVolumeTaskDataSambaShare = "var-task"
	SharedStorageTaskDataSMBServerPVCName = "nvcf-task-data-smb-server"

	SharedStorageKNSReadWritePVCName      = "nvcf-kns-data-rw"
	SharedStorageKNSReadOnlyPVCName       = "nvcf-kns-data-ro"
	SharedStorageVolumeKNSTokenVolumeName = "kns-data"
	SharedStorageVolumeKNSSambaShare      = "var-run-nvcf-info"
	//nolint:gosec
	SharedStorageVolumeKNSTokenMountPath = "/var/run/nvcf/info"

	// SharedStorageEnabledLabel namespace label
	SharedStorageEnabledLabel = "nvca.nvcf.nvidia.io/shared-storage-enabled"
)

func HasStorageAnnotation(annotations map[string]string) bool {
	for k := range annotations {
		switch k {
		case WebhookModelCachePVCNameAnnotationKey,
			HelmWebhookSharedStorageSecretsReadWritePVCNameAnnotationKey,
			HelmWebhookSharedStorageSecretsReadOnlyPVCNameAnnotationKey,
			HelmWebhookSharedStorageKNSReadWritePVCNameAnnotationKey,
			HelmWebhookSharedStorageKNSReadOnlyPVCNameAnnotationKey,
			HelmWebhookInternalPersistentStorageStorageClassNameAnnotationKey:
			return true
		}
	}
	return false
}

func IsSharedStoragePVC(pvcName string) bool {
	for _, v := range []string{
		SharedStorageSecretsReadOnlyPVCName,
		SharedStorageSecretsReadWritePVCName,
		SharedStorageKNSReadOnlyPVCName,
		SharedStorageKNSReadWritePVCName,
	} {
		if v == pvcName {
			return true
		}
	}
	return false
}

func IsSharedStorageVolumeName(volumeName string) bool {
	for _, v := range []string{
		SharedStorageSecretsVolumeName,
		SharedStorageVolumeKNSTokenVolumeName,
	} {
		if v == volumeName {
			return true
		}
	}
	return false
}

func IsSharedStorageVolumeMountPath(mountPath string) bool {
	for _, v := range []string{
		SharedStorageVolumeSecretsMountPath,
		SharedStorageVolumeKNSTokenMountPath,
	} {
		if v == mountPath {
			return true
		}
	}
	return false
}

func GetIngressNetworkPolicies() []*netv1.NetworkPolicy {
	smbPort := intstr.FromInt(445)
	smbMetricsPort := intstr.FromInt(9922)
	tcpProtocol := corev1.ProtocolTCP
	return []*netv1.NetworkPolicy{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "allow-ingress-sharedstorage",
			},
			Spec: netv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app.kubernetes.io/name": SMBServerPodName,
					},
				},
				PolicyTypes: []netv1.PolicyType{
					netv1.PolicyTypeIngress,
				},
				Ingress: []netv1.NetworkPolicyIngressRule{
					{
						Ports: []netv1.NetworkPolicyPort{
							{
								Port:     &smbPort,
								Protocol: &tcpProtocol,
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "allow-ingress-sharedstorage-monitoring",
			},
			Spec: netv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app.kubernetes.io/name": SMBServerPodName,
					},
				},
				PolicyTypes: []netv1.PolicyType{
					netv1.PolicyTypeIngress,
				},
				Ingress: []netv1.NetworkPolicyIngressRule{
					{
						From: []netv1.NetworkPolicyPeer{
							{
								NamespaceSelector: &metav1.LabelSelector{
									MatchLabels: map[string]string{
										"kubernetes.io/metadata.name": "monitoring",
									},
								},
							},
						},
						Ports: []netv1.NetworkPolicyPort{
							{
								Port:     &smbMetricsPort,
								Protocol: &tcpProtocol,
							},
						},
					},
				},
			},
		},
	}
}
