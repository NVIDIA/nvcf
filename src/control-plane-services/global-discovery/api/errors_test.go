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

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Reasons are part of the wire contract with the Go and Rust clients.
func TestErrorReasons(t *testing.T) {
	assert.Equal(t, "gds.nvidia.com", ErrorDomain)
	assert.Equal(t, "current_generation", MetadataCurrentGeneration)
	assert.Equal(t, []string{
		"STALE_GENERATION", "LEASE_EPOCH_MISMATCH", "CLUSTER_NOT_REGISTERED", "POOL_NAME_TAKEN",
		"TOO_MANY_ENDPOINTS", "TOO_MANY_WATCHERS", "STORE_UNAVAILABLE", "SHUTTING_DOWN",
	}, []string{
		ReasonStaleGeneration, ReasonLeaseEpochMismatch, ReasonClusterNotRegistered, ReasonPoolNameTaken,
		ReasonTooManyEndpoints, ReasonTooManyWatchers, ReasonStoreUnavailable, ReasonShuttingDown,
	})
	assert.Equal(t, []string{"INVALID_ARGUMENT", "NOT_FOUND", "INTERNAL"},
		[]string{ReasonInvalidArgument, ReasonNotFound, ReasonInternal})
}

// The conflict reasons must equal the sigs.k8s.io/mcs-api constants so that
// kubectl output matches other MCS implementations.
func TestConditionReasons(t *testing.T) {
	assert.Equal(t, []string{"Valid", "Ready", "Conflict"}, []string{ConditionValid, ConditionReady, ConditionConflict})
	assert.Equal(t, []string{
		"NoConflicts", "PortConflict", "TypeConflict", "SessionAffinityConflict",
		"SessionAffinityConfigConflict", "AnnotationsConflict", "LabelsConflict",
		"InternalTrafficPolicyConflict", "TrafficDistributionConflict", "IPFamilyConflict",
		"ClusterLeaseExpired",
	}, []string{
		ConditionReasonNoConflicts, ConditionReasonPortConflict, ConditionReasonTypeConflict,
		ConditionReasonSessionAffinityConflict, ConditionReasonSessionAffinityConfigConflict,
		ConditionReasonAnnotationsConflict, ConditionReasonLabelsConflict,
		ConditionReasonInternalTrafficPolicyConflict, ConditionReasonTrafficDistributionConflict,
		ConditionReasonIPFamilyConflict, ConditionReasonClusterLeaseExpired,
	})
	assert.Equal(t, []string{"Valid", "Ready", "Pending", "NoService", "InvalidServiceType"}, []string{
		ConditionReasonValid, ConditionReasonReady, ConditionReasonPending,
		ConditionReasonNoService, ConditionReasonInvalidServiceType,
	})
}

func TestLimits(t *testing.T) {
	assert.Equal(t, 64, MaxLabels)
	assert.Equal(t, 32, MaxAnnotations)
	assert.Equal(t, 16*1024, MaxAnnotationBytes)
	assert.Equal(t, 64, MaxPorts)
	assert.Equal(t, 1000, MaxEndpointsPerExport)
	assert.Equal(t, 16, MaxEndpointAttributes)
	assert.Equal(t, 256, MaxModelsPerPool)
	assert.Equal(t, 4*1024*1024, MaxMessageBytes)
}
