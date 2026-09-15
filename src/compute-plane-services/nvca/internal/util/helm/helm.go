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

package helm

import (
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
)

const (
	MiniServiceWorkloadNamespacePrefix = "sr-"
)

func IsMiniServiceCreateRequest(req *nvcav2beta1.ICMSRequest) bool {
	return (req.Spec.Action == common.RequestICMSInstances || req.Spec.Action == common.FunctionCreationAction ||
		req.Spec.Action == common.RequestICMSInstancesForTask || req.Spec.Action == common.TaskCreationAction) &&
		isMiniServiceType(req)
}

func isMiniServiceType(req *nvcav2beta1.ICMSRequest) bool {
	for _, a := range req.Spec.CreationMsgInfo.LaunchArtifacts {
		if a.Type == function.LaunchArtifactTypeHelmChart {
			return true
		}
	}
	// TODO(mcamp): move this detection to the translate library
	return (req.Spec.CreationMsgInfo.FunctionLaunchSpecification != nil &&
		req.Spec.CreationMsgInfo.FunctionLaunchSpecification.HelmChartLaunchSpecification != nil) ||
		(req.Spec.CreationMsgInfo.TaskLaunchSpecification != nil &&
			req.Spec.CreationMsgInfo.TaskLaunchSpecification.HelmChartLaunchSpecification != nil)
}
