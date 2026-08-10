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
	"os"
	"strings"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	requeableStorageErrorToken = "RequeableStorageError"
)

func NewModelCacheStorageRequest(req *nvcav2beta1.ICMSRequest, fff featureflag.Fetcher) (*nvcav2beta1.StorageRequest, error) {
	var cacheLaunchSpec *common.CacheLaunchSpecification
	if req.Spec.Action == common.RequestICMSInstances || req.Spec.Action == common.FunctionCreationAction {
		cacheLaunchSpec = req.Spec.CreationMsgInfo.FunctionLaunchSpecification.CacheLaunchSpecification
	} else {
		cacheLaunchSpec = req.Spec.CreationMsgInfo.TaskLaunchSpecification.CacheLaunchSpecification
	}
	if cacheLaunchSpec == nil {
		return nil, fmt.Errorf("cache launch specification is not set")
	}
	if cacheLaunchSpec.CacheHandle == "" {
		return nil, fmt.Errorf("cache handle is not set")
	}

	st := newStorageRequest(nvcav2beta1.ModelCacheRequest, req, fff)
	st.Spec.ModelCache = &nvcav2beta1.ModelCacheSpec{
		CacheHandle: cacheLaunchSpec.CacheHandle,
	}
	// Stamp the cache-handle label at creation. The model-cache controller's
	// fan-out maps writer-job / PV(C) events back to the StorageRequest by
	// listing on this label (getModelCacheFanOutEventHandlerMapFunc); the
	// reconcile loop persists only status, so a label set during reconcile is
	// not durable. Without it the SR never re-reconciles when the writer job
	// completes, and the cache stays stuck in Pending (all backends).
	if st.Labels == nil {
		st.Labels = map[string]string{}
	}
	st.Labels[modelCacheHandleLabelKey] = cacheLaunchSpec.CacheHandle
	if fff.IsFeatureFlagEnabled(featureflag.NVMeshEncryption) {
		st.Spec.ModelCache.Encryption = &nvcav2beta1.ModelCacheEncryption{
			Required: true,
		}
	}
	return st, nil
}

func NewSharedStorageRequest(req *nvcav2beta1.ICMSRequest,
	fff featureflag.Fetcher,
	cfg nvcaconfig.Config,
	workerPullSecrets []*corev1.Secret,
) *nvcav2beta1.StorageRequest {
	// Continue to set secretName for backwards-compatibility.
	var secretName string
	var secretNames []string
	for i, secret := range workerPullSecrets {
		if i == 0 {
			secretName = secret.Name
		}
		secretNames = append(secretNames, secret.Name)
	}
	st := newStorageRequest(nvcav2beta1.SharedStorageRequest, req, fff)
	st.Spec.SharedStorage = &nvcav2beta1.SharedStorageSpec{
		// This is fixed for now to Mi
		Size:                  *resource.NewQuantity(1<<20, resource.BinarySI),
		SMBContainerImage:     cfg.Agent.SharedStorage.Server.Image,
		WorkerPullSecretName:  secretName,
		WorkerPullSecretNames: secretNames,
	}
	if instanceTypeValue := req.Spec.CreationMsgInfo.GetInstanceTypeLabelSelValue(); instanceTypeValue != "" {
		st.Spec.SharedStorage.Server = &nvcav2beta1.SharedStorageServerSpec{
			SMBServerPodNodeAffinity: &corev1.Affinity{
				NodeAffinity: &corev1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{
							{
								MatchExpressions: []corev1.NodeSelectorRequirement{
									{
										Key:      nodefeatures.UniformInstanceTypeLabelKey,
										Operator: corev1.NodeSelectorOpIn,
										Values:   []string{instanceTypeValue},
									},
								},
							},
						},
					},
				},
			},
		}
	}
	if len(cfg.Workload.Tolerations) > 0 {
		if st.Spec.SharedStorage.Server == nil {
			st.Spec.SharedStorage.Server = &nvcav2beta1.SharedStorageServerSpec{}
		}
		st.Spec.SharedStorage.Server.SMBServerPodTolerations = append(
			st.Spec.SharedStorage.Server.SMBServerPodTolerations,
			cfg.Workload.Tolerations...,
		)
	}
	// If this is the a helm task request add Task Data spec
	if req.Spec.Action == common.RequestICMSInstancesForTask || req.Spec.Action == common.TaskCreationAction {
		st.Spec.SharedStorage.TaskData = &nvcav2beta1.SharedStorageTaskDataSpec{
			StorageClassName: cfg.Agent.SharedStorage.TaskData.StorageClassName,
			PVMountOptions:   cfg.Agent.SharedStorage.TaskData.PVMountOptions,
			Size:             cfg.Agent.SharedStorage.TaskData.StorageCapacity.DeepCopy(),
		}
	}
	// When GXCache is enabled, add annotation to skip gxcache webhook injection
	// on SMB server pods - they don't need shader cache
	if fff.IsFeatureFlagEnabled(featureflag.GXCache) {
		if st.Annotations == nil {
			st.Annotations = map[string]string{}
		}
		st.Annotations[nvcatypes.GXCacheSkipInjectionAnnotationKey] = nvcatypes.GXCacheSkipInjectionAnnotationValue
	}
	return st
}

func NewInternalPersistentStorageRequest(req *nvcav2beta1.ICMSRequest,
	intSpec featureflag.InternalPersistentStorageSpec,
	fff featureflag.Fetcher,
) *nvcav2beta1.StorageRequest {
	st := newStorageRequest(nvcav2beta1.InternalPersistentStorageRequest, req, fff)
	st.Spec.InternalPersistentStorage = &nvcav2beta1.InternalPersistentStorageSpec{
		StorageClassName: intSpec.StorageClassName,
		ResourceQuota:    nvcav2beta1.InternalPersistentStorageResourceQuotaSpec{Hard: intSpec.ResourceQuota.Hard},
	}
	return st
}

func newStorageRequest(
	sType nvcav2beta1.StorageRequestType,
	req *nvcav2beta1.ICMSRequest,
	fff featureflag.Fetcher,
) *nvcav2beta1.StorageRequest {
	st := &nvcav2beta1.StorageRequest{}
	st.Name = sType.Name()
	st.Spec.Type = sType
	st.Spec.RequestName = req.Name
	st.Spec.RequestNamespace = req.Namespace
	st.Labels = nvcatypes.GetLabelsForRequest(req, fff)
	st.Annotations = nvcatypes.GetAnnotationsForRequest(req)
	return st
}

func GetSharedStorageServerImage(icmsServiceURL string, env nvcaconfig.Environment) string {
	// TODO: this needs to come from ICMS instead of being hard-coded here.
	smbServerImage := "nvcr.io/qtfpt1h0bieu/nvcf-core/samba:1.0.5"
	if v := os.Getenv("NVCA_SHARED_STORAGE_IMAGE"); v != "" {
		smbServerImage = v
	} else if strings.Contains(icmsServiceURL, "stg.") || env == nvcaconfig.EnvironmentStaging {
		smbServerImage = "stg.nvcr.io/nv-cf/nvcf-core/samba:1.0.5"
	}
	return smbServerImage
}

// IsRequeableStorageError and NewRequeueableStorageError are a temporary
// hack to suppress messages from storage not being ready
// going forward this will be handled properly by the controller and should reduce
// the noise in the meantime
func IsRequeableStorageError(err error) bool {
	return err != nil && strings.Contains(err.Error(), requeableStorageErrorToken)
}

func NewRequeueableStorageError(message string) error {
	return fmt.Errorf("%s - %s", message, requeableStorageErrorToken)
}

func GetStorageRequestErrorLog(st *nvcav2beta1.StorageRequest) string {
	// Check for shared storage failure condition
	if st.Status.Phase == nvcav2beta1.StorageFailed || st.Status.Phase == nvcav2beta1.StorageRuntimeError {
		for _, condition := range st.Status.Conditions {
			if strings.EqualFold(condition.Type, ConditionTypeSMBCSIDriverInstalled) &&
				condition.Status == metav1.ConditionFalse {
				return fmt.Sprintf("%s driver must be installed when %s feature flag is enabled. "+
					"Please contact your cluster administrator to install the driver.",
					SMBCSIDriverName, featureflag.HelmSharedStorage.Key)
			}
		}
		return fmt.Sprintf("storage request %s is in %s phase", st.Name, st.Status.Phase)
	}

	return ""
}
