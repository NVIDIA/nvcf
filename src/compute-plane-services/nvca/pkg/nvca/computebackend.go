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
	"context"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	v1 "k8s.io/api/core/v1"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// ICMSRequestHelper is an interface for managing ICMS requests in compute backends.
type ICMSRequestHelper interface {
	// ApplyCreationMessage fulfills workload request defined in the ICMS request in the compute backend, also record updated status
	ApplyCreationMessage(ctx context.Context, req *nvcav2beta1.ICMSRequest) error

	// ApplyTerminationMessage terminates the instances in the request, also record updated status in ICMS request
	ApplyTerminationMessage(ctx context.Context, req *nvcav2beta1.ICMSRequest) error

	// AggregateInstanceStatuses aggregates the statuses of all Instances in req.
	AggregateInstanceStatuses(ctx context.Context, req *nvcav2beta1.ICMSRequest) AggregatedInstanceStatus

	// GetICMSRequestStatusUpdatesForRequest returns the consolidate payload of List<ICMSRequestUpdateInfo> to be posted to ICMS for InstanceStatusUpdate
	GetICMSRequestStatusUpdatesForRequest(ctx context.Context, req *nvcav2beta1.ICMSRequest) ([]types.ICMSRequestUpdateInfo, error)

	// GetICMSRequestUpdatesForTerminationRequest returns payload only for all Termination requests to be posted to ICMS
	GetICMSRequestUpdatesForTerminationRequest(ctx context.Context, req *nvcav2beta1.ICMSRequest) []types.ICMSRequestUpdateInfo

	// GetICMSRequestUpdatesForCreateRequest returns payload only for all Creation requests to be posted to ICMS
	GetICMSRequestUpdatesForCreateRequest(ctx context.Context, req *nvcav2beta1.ICMSRequest) []types.ICMSRequestUpdateInfo

	// ComputeCleanupCacheReferences cleanUp underlying cache references
	ComputeCleanupCacheReferences(ctx context.Context, references []string) error

	// AllInstancesTerminatedAndReported returns if all NVCF worker instances for backend are terminated and reported to ICMS
	AllInstancesTerminatedAndReported(ctx context.Context, req *nvcav2beta1.ICMSRequest) bool

	// HandleInstanceStatusPreconditionFailure purges the underlying instance and updates the status of ICMSRequest
	HandleInstanceStatusPreconditionFailure(ctx context.Context, req *nvcav2beta1.ICMSRequest, instID string) error

	// PurgeInstanceID purges a specific instance ID and updates the terminated instances map
	PurgeInstanceID(ctx context.Context, req *nvcav2beta1.ICMSRequest, terminatedInstances map[string]nvcav2beta1.InstanceStatus, instanceID string) bool
}

type K8sArtifactHelper interface {
	// AggregatePodInstanceStatus returns an aggregated status reflecting if the podID is Scheduled on a Node
	AggregatePodInstanceStatus(ctx context.Context, req *nvcav2beta1.ICMSRequest, podID string) AggregatedInstanceStatus

	// GetICMSRequestUpdatesForCreatePodRequest returns payload only for all Pod Creation requests to be posted to ICMS
	GetICMSRequestUpdatesForCreatePodRequest(ctx context.Context,
		st nvcav2beta1.InstanceStatus, req *nvcav2beta1.ICMSRequest) (types.ICMSRequestUpdateInfo, error)

	// CreatePodArtifact creates a PodArtifact as specified by the inputs podArt
	CreatePodArtifact(ctx context.Context, podArt function.LaunchArtifact, mf mutateFunc) error

	// GetErroredPodLogs returns the pod logs of the failed instance container or the static error message for Utils / Init if one exists
	GetErroredPodLogs(ctx context.Context, pod *v1.Pod, prepend string, writeMaxBytes int64) (string, int64, error)

	// CreateSecretArtifact creates the k8s Secret as specified by the inputs
	CreateSecretArtifact(ctx context.Context, a function.LaunchArtifact, mf mutateFunc) error

	// CreateConfigMapArtifact creates the k8s ConfigMap as specified by the inputs
	CreateConfigMapArtifact(ctx context.Context, a function.LaunchArtifact, mf mutateFunc) error

	// CreatePodArtifactInstances creates the standalone PodInstances
	CreatePodArtifactInstances(ctx context.Context, pod *v1.Pod,
		req *nvcav2beta1.ICMSRequest, mf mutateFunc) ([]nvcav2beta1.InstanceStatus, error)
}

// TODO: aparthasarat
// Not All Backend Need the Two, split the interface composition in
// BackendK8sCache to accommodate that
type ComputeBackend interface {
	// core ICMS request applier
	ICMSRequestHelper

	// auxiliary K8sArtifactHelper
	K8sArtifactHelper
}

type AggregatedInstanceStatus int

const (
	AggregatedInstanceStatusUnknown AggregatedInstanceStatus = iota
	AggregatedInstanceStatusModelCachingInProgress
	AggregatedInstanceStatusScheduling
	AggregatedInstanceStatusPending
	AggregatedInstanceStatusSucceeded
	AggregatedInstanceStatusFailed
)

func (s AggregatedInstanceStatus) String() string {
	switch s {
	case AggregatedInstanceStatusModelCachingInProgress:
		return "model-caching-in-progress"
	case AggregatedInstanceStatusScheduling:
		return "scheduling"
	case AggregatedInstanceStatusPending:
		return "pending"
	case AggregatedInstanceStatusSucceeded:
		return "succeeded"
	case AggregatedInstanceStatusFailed:
		return "failed"
	}
	return "unknown"
}
