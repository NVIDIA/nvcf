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

package nvca

import (
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// AnnotatedICMSEventf emits a Kubernetes Event on the ICMSRequest with FnDs ledger
// annotations. Pass update=nil for request-level events; pass an update with
// InstanceID (and status payload fields when applicable) for instance-level events.
func (c *BackendK8sCache) AnnotatedICMSEventf(
	req *nvcav2beta1.ICMSRequest,
	eventType, reason, msgFmt string,
	update *types.ICMSRequestUpdateInfo,
	args ...any,
) {
	if c == nil || c.eventRecorder == nil || req == nil {
		return
	}
	annotations := types.LedgerEventAnnotations(req, c.clusterName, c.clusterRegion, update)
	c.eventRecorder.AnnotatedEventf(req, annotations, eventType, reason, msgFmt, args...)
}

// AnnotatedICMSEvent is AnnotatedICMSEventf without formatting args.
func (c *BackendK8sCache) AnnotatedICMSEvent(
	req *nvcav2beta1.ICMSRequest,
	eventType, reason, message string,
	update *types.ICMSRequestUpdateInfo,
) {
	c.AnnotatedICMSEventf(req, eventType, reason, "%s", update, message)
}

// instanceUpdate is a convenience for instance-level Events that only need instance-id.
func instanceUpdate(instanceID string) *types.ICMSRequestUpdateInfo {
	if instanceID == "" {
		return nil
	}
	return &types.ICMSRequestUpdateInfo{InstanceID: instanceID}
}
