/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package api

// ErrorDomain is the google.rpc.ErrorInfo domain of every GDS error.
const ErrorDomain = "gds.nvidia.com"

// google.rpc.ErrorInfo reasons. common.proto documents the gRPC code of each.
const (
	ReasonStaleGeneration      = "STALE_GENERATION"
	ReasonLeaseEpochMismatch   = "LEASE_EPOCH_MISMATCH"
	ReasonClusterNotRegistered = "CLUSTER_NOT_REGISTERED"
	ReasonPoolNameTaken        = "POOL_NAME_TAKEN"
	ReasonTooManyEndpoints     = "TOO_MANY_ENDPOINTS"
	ReasonTooManyWatchers      = "TOO_MANY_WATCHERS"
	ReasonStoreUnavailable     = "STORE_UNAVAILABLE"
	ReasonShuttingDown         = "SHUTTING_DOWN"

	// ReasonInvalidArgument accompanies INVALID_ARGUMENT errors other than
	// TOO_MANY_ENDPOINTS; a google.rpc.BadRequest detail names the fields.
	ReasonInvalidArgument = "INVALID_ARGUMENT"
	// ReasonNotFound accompanies NOT_FOUND errors for objects other than
	// clusters, for example a pool or a ServiceImport.
	ReasonNotFound = "NOT_FOUND"
	// ReasonInternal accompanies INTERNAL errors.
	ReasonInternal = "INTERNAL"
)

// MetadataCurrentGeneration is the ErrorInfo metadata key that carries the
// stored generation, in decimal, of a STALE_GENERATION error.
const MetadataCurrentGeneration = "current_generation"

// ServiceExport condition types, as in sigs.k8s.io/mcs-api.
const (
	ConditionValid    = "Valid"
	ConditionReady    = "Ready"
	ConditionConflict = "Conflict"
)

// Condition reasons. The conflict reasons and Valid, Ready, Pending,
// NoService and InvalidServiceType are the sigs.k8s.io/mcs-api constants;
// IPFamilyConflict and ClusterLeaseExpired are GDS additions.
const (
	ConditionReasonValid              = "Valid"
	ConditionReasonReady              = "Ready"
	ConditionReasonPending            = "Pending"
	ConditionReasonNoService          = "NoService"
	ConditionReasonInvalidServiceType = "InvalidServiceType"

	ConditionReasonNoConflicts                   = "NoConflicts"
	ConditionReasonPortConflict                  = "PortConflict"
	ConditionReasonTypeConflict                  = "TypeConflict"
	ConditionReasonSessionAffinityConflict       = "SessionAffinityConflict"
	ConditionReasonSessionAffinityConfigConflict = "SessionAffinityConfigConflict"
	ConditionReasonAnnotationsConflict           = "AnnotationsConflict"
	ConditionReasonLabelsConflict                = "LabelsConflict"
	ConditionReasonInternalTrafficPolicyConflict = "InternalTrafficPolicyConflict"
	ConditionReasonTrafficDistributionConflict   = "TrafficDistributionConflict"
	ConditionReasonIPFamilyConflict              = "IPFamilyConflict"
	ConditionReasonClusterLeaseExpired           = "ClusterLeaseExpired"
)

// API limits. Requests over a limit fail with INVALID_ARGUMENT, except
// endpoint sets, which fail with reason TOO_MANY_ENDPOINTS.
const (
	MaxLabels             = 64
	MaxAnnotations        = 32
	MaxAnnotationBytes    = 16 << 10
	MaxPorts              = 64
	MaxEndpointsPerExport = 1000
	MaxEndpointAttributes = 16
	MaxModelsPerPool      = 256
	MaxMessageBytes       = 4 << 20
)
