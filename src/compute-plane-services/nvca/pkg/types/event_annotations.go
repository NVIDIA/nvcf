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
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
)

const (
	ledgerAnnotationPrefix = "nvcf.nvidia.io/"

	// Ledger Event annotation keys stamped on ICMSRequest Kubernetes Events for FnDs.
	LedgerAnnotationICMSRequestID     = ledgerAnnotationPrefix + "icms-request-id"
	LedgerAnnotationFunctionVersionID = ledgerAnnotationPrefix + "function-version-id"
	LedgerAnnotationTaskID            = ledgerAnnotationPrefix + "task-id"
	LedgerAnnotationInstanceID        = ledgerAnnotationPrefix + "instance-id"
	LedgerAnnotationFunctionID        = ledgerAnnotationPrefix + "function-id"
	LedgerAnnotationNCAID             = ledgerAnnotationPrefix + "nca-id"
	LedgerAnnotationClusterID         = ledgerAnnotationPrefix + "cluster-id"
	LedgerAnnotationRegion            = ledgerAnnotationPrefix + "region"
	LedgerAnnotationInstanceState     = ledgerAnnotationPrefix + "instance-state"
	LedgerAnnotationStatus            = ledgerAnnotationPrefix + "status"
	LedgerAnnotationTerminationCause  = ledgerAnnotationPrefix + "termination-cause"
	LedgerAnnotationFailureCategory   = ledgerAnnotationPrefix + "failure-category"
)

// LedgerEventAnnotations builds FnDs ledger annotations for an ICMSRequest Event.
// Empty values are omitted. Instance-level fields come from update when non-nil.
// Status subset (instance-state, status, termination-cause, failure-category) is
// included only when the corresponding payload fields are set.
func LedgerEventAnnotations(
	req *nvcav2beta1.ICMSRequest,
	clusterID, region string,
	update *ICMSRequestUpdateInfo,
) map[string]string {
	if req == nil {
		return nil
	}

	annotations := make(map[string]string)
	set := func(key, value string) {
		if value != "" {
			annotations[key] = value
		}
	}

	set(LedgerAnnotationICMSRequestID, req.Spec.RequestID)
	set(LedgerAnnotationNCAID, req.Spec.NCAId)
	set(LedgerAnnotationClusterID, clusterID)
	set(LedgerAnnotationRegion, region)

	// FunctionDetails is authoritative; the flat Spec.Function*ID fields are
	// deprecated. Fall back to them only for pre-FunctionDetails CRs still in
	// flight during an upgrade (parity with common_labels.go).
	functionID := req.Spec.FunctionDetails.FunctionID
	if functionID == "" {
		functionID = req.Spec.FunctionID
	}
	set(LedgerAnnotationFunctionID, functionID)

	functionVersionID := req.Spec.FunctionDetails.FunctionVersionID
	if functionVersionID == "" {
		functionVersionID = req.Spec.FunctionVersionID
	}
	taskID := req.Spec.TaskDetails.TaskID
	// A request is either a function deployment or a task, never both, so TaskID
	// and FunctionVersionID are mutually exclusive by design. Emit task-id for
	// tasks; otherwise function-version-id.
	if taskID != "" {
		set(LedgerAnnotationTaskID, taskID)
	} else {
		set(LedgerAnnotationFunctionVersionID, functionVersionID)
	}

	if update != nil {
		set(LedgerAnnotationInstanceID, update.InstanceID)
		set(LedgerAnnotationInstanceState, string(update.Payload.InstanceState))
		set(LedgerAnnotationStatus, string(update.Payload.Status))
		set(LedgerAnnotationTerminationCause, string(update.Payload.TerminationCause))
		set(LedgerAnnotationFailureCategory, update.Payload.FailureCategory)
	}

	return annotations
}
