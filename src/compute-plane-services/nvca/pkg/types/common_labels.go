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

package types //nolint:revive

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
)

const (
	// Logging keys
	NCAIDUpperKey             = "NCA_ID"
	FunctionIDUpperKey        = "FUNCTION_ID"
	FunctionVersionIDUpperKey = "FUNCTION_VERSION_ID"
	TaskIDUpperKey            = "TASK_ID"

	labelFQDNPrefix = "nvca.nvcf.nvidia.io"

	NCAIDKey                            = "nca-id"
	ICMSRequestIDKey                    = "icms-request-id"
	ClusterGroupKey                     = "icms-cluster-group"
	FunctionIDKey                       = "function-id"
	FunctionVersionIDKey                = "function-version-id"
	TaskIDKey                           = "task-id"
	InstanceCountKey                    = "instance-count"
	GPUNameKey                          = "gpu-name"
	MessageBatchIDKey                   = "nvcf.nvidia.io/message-batch-id"
	ShaderCacheLabelKey                 = labelFQDNPrefix + "/gxcache-client-inject"
	GXCacheSkipInjectionAnnotationKey   = "gxcache.nvcf.nvidia.com/injected-at"
	GXCacheSkipInjectionAnnotationValue = "skip"
	NVCARebindAttemptedAnnotationKey    = labelFQDNPrefix + "/pvc-rebind-attempted"
	PVCBindCompletedAnnotationKey       = "pv.kubernetes.io/bind-completed"
	NVCAFunctionIDKey                   = labelFQDNPrefix + "/function-id"
	NVCATaskIDKey                       = labelFQDNPrefix + "/task-id"
	NVCAFunctionVersionIDKey            = labelFQDNPrefix + "/function-version-id"
	// Infra objects should have this annotation to differentiate from user-owned objects.
	// This should be wiped from user-owned objects if set.
	InfraObjectAnnotationKey = labelFQDNPrefix + "/nvca-infra-object"

	// WorkloadInstanceTypeLabel denotes what instance type will exist in a given namespace.
	WorkloadInstanceTypeLabel = labelFQDNPrefix + "/workload-instance-type"

	WorkloadInstanceTypeValuePodSpec     = "pod_spec"
	WorkloadInstanceTypeValueMiniService = "miniservice"

	// NVCFInstIDEnvKey is the function's instance ID.(podName/miniservice-name)
	NVCFInstIDEnvKey = "NVCF_INSTANCE_ID"
	// NVCTInstIDEnvKey is the task's instance ID.(podName/miniservice-name)
	NVCTInstIDEnvKey = "NVCT_INSTANCE_ID"

	InstanceIDEnvKey            = "INSTANCE_ID"
	InferenceReadyTimeoutEnvKey = "INFERENCE_READY_TIMEOUT"
)

func GetFunctionVersionIDLabelVal(labels map[string]string) (string, bool) {
	if labels == nil {
		return "", false
	}
	v, ok := labels[FunctionVersionIDKey]
	return v, ok && v != ""
}

func GetNCAIDLabelVal(labels map[string]string) (string, bool) {
	if labels == nil || labels[NCAIDKey] == "" {
		return "", false
	}
	return strings.TrimPrefix(strings.TrimSuffix(labels[NCAIDKey], "-nca"), "nca-"), true
}

// Any NCA ID can start or end with non-alphanumeric characters,
// and special treatment of dns1123-valid ID's should be avoided for consistency.
func MakeNCAIDLabelValue(ncaID string) string {
	return "nca-" + ncaID + "-nca"
}

func GetLabelsForRequest(req *nvcav2beta1.ICMSRequest, fff featureflag.Fetcher) map[string]string {
	if fff == nil {
		fff = featureflag.DefaultFetcher
	}

	gpuName := req.Spec.CreationMsgInfo.GPUType
	if gpuName == "" {
		//nolint
		gpuName = req.Spec.CreationMsgInfo.GPUName
	}

	ncaIDLabelVal := MakeNCAIDLabelValue(req.Spec.NCAId)
	labelsForReq := map[string]string{
		ICMSRequestIDKey:  req.Spec.RequestID,
		NCAIDKey:          ncaIDLabelVal,
		NCAIDUpperKey:     ncaIDLabelVal,
		MessageBatchIDKey: req.Spec.MessageBatchID,
		GPUNameKey:        gpuName,
	}
	switch req.Spec.Action {
	case common.FunctionCreationAction:
		functionID := req.Spec.FunctionDetails.FunctionID
		if functionID == "" {
			//nolint
			functionID = req.Spec.FunctionID
		}
		functionVersionID := req.Spec.FunctionDetails.FunctionVersionID
		if functionVersionID == "" {
			//nolint
			functionVersionID = req.Spec.FunctionVersionID
		}
		labelsForReq[FunctionIDKey] = functionID
		labelsForReq[FunctionIDUpperKey] = functionID
		labelsForReq[FunctionVersionIDKey] = functionVersionID
		labelsForReq[FunctionVersionIDUpperKey] = functionVersionID
	case common.TaskCreationAction:
		labelsForReq[TaskIDKey] = req.Spec.TaskDetails.TaskID
		labelsForReq[TaskIDUpperKey] = req.Spec.TaskDetails.TaskID
	}

	if fff.IsFeatureFlagEnabled(featureflag.GXCache) || fff.IsAttributeEnabled(featureflag.AttrOVCSecurityEnforcements) {
		if _, ok := labelsForReq[ShaderCacheLabelKey]; !ok {
			labelsForReq[ShaderCacheLabelKey] = strconv.FormatBool(true)
		}
	} else {
		delete(labelsForReq, ShaderCacheLabelKey)
	}

	return labelsForReq
}

func GetAnnotationsForRequest(req *nvcav2beta1.ICMSRequest) map[string]string {
	return map[string]string{
		ICMSRequestIDKey: req.Spec.RequestID,
		NCAIDKey:         req.Spec.NCAId,
		ClusterGroupKey:  req.Spec.CreationMsgInfo.ClusterGroup,
		InstanceCountKey: fmt.Sprintf("%d", req.Spec.CreationMsgInfo.InstanceCount),
	}
}

// IsInfraOwnedObject returns true if obj has the InfraObjectAnnotationKey set to a non-empty value in its annotations.
func IsInfraOwnedObject(obj client.Object) bool {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		return false
	}
	return annotations[InfraObjectAnnotationKey] != ""
}
