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

package workloadtypes

// WorkloadType is a typed string for the Kubernetes workload type.
type WorkloadType string

// Workload types for metric labeling.
const (
	WorkloadTypeContainer WorkloadType = "container"
	WorkloadTypeHelm      WorkloadType = "helm"
)

// WorkloadKind is a typed string for the NVCF request kind.
type WorkloadKind string

// Workload kind values for metric labeling (derived from req.Spec.Action).
const (
	WorkloadKindFunction WorkloadKind = "function"
	WorkloadKindTask     WorkloadKind = "task"
)

// WorkloadStatus is a typed string for the workload terminal outcome.
type WorkloadStatus string

// WorkloadStatus values for metric labeling.
const (
	WorkloadStatusSuccess WorkloadStatus = "success"
	WorkloadStatusFailure WorkloadStatus = "failure"
)

// FailureCategory is a typed string for workload failure categories.
type FailureCategory string

// Failure categories mapped from ICMSInstanceState.
const (
	FailureCategoryNone                  FailureCategory = ""
	FailureCategoryImagePull             FailureCategory = "image_pull"
	FailureCategoryInitStuck             FailureCategory = "init_stuck"
	FailureCategoryInitRestartLoop       FailureCategory = "init_restart_loop"
	FailureCategoryContainerRestart      FailureCategory = "container_restart_loop"
	FailureCategoryNoCapacity            FailureCategory = "no_capacity"
	FailureCategoryAdmissionError        FailureCategory = "admission_error"
	FailureCategorySharedStorage         FailureCategory = "shared_storage"
	FailureCategoryPersistentStorage     FailureCategory = "persistent_storage"
	FailureCategoryDegradedWorker        FailureCategory = "degraded_worker"
	FailureCategoryNotFound              FailureCategory = "not_found"
	FailureCategoryTerminalError         FailureCategory = "terminal_error"
	FailureCategorySyncAction            FailureCategory = "sync_action"
	FailureCategoryServiceMaintenance    FailureCategory = "service_maintenance"
	FailureCategoryPreconditionFail      FailureCategory = "precondition_failure"
	FailureConditionCreateContainerError FailureCategory = "create_container_error"
	FailureCategoryUnknown               FailureCategory = "unknown"
)

// AllWorkloadTypes for zero-initialization.
var AllWorkloadTypes = []WorkloadType{
	WorkloadTypeContainer,
	WorkloadTypeHelm,
}

// AllWorkloadKinds for zero-initialization.
var AllWorkloadKinds = []WorkloadKind{
	WorkloadKindFunction,
	WorkloadKindTask,
}

// AllFailureCategories for zero-initialization.
var AllFailureCategories = []FailureCategory{
	FailureCategoryImagePull,
	FailureCategoryInitStuck,
	FailureCategoryInitRestartLoop,
	FailureCategoryContainerRestart,
	FailureCategoryNoCapacity,
	FailureCategoryAdmissionError,
	FailureCategorySharedStorage,
	FailureCategoryPersistentStorage,
	FailureCategoryDegradedWorker,
	FailureCategoryNotFound,
	FailureCategoryTerminalError,
	FailureCategorySyncAction,
	FailureCategoryServiceMaintenance,
	FailureCategoryPreconditionFail,
	FailureCategoryUnknown,
}
