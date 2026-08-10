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

package clusteragent

// The eight granular ICMSRequest statuses tracked by NVCA. Defined upstream in
// nvca/pkg/apis/nvca/v2beta1 as the RequestStatus enum.
const (
	statusPending                = "RequestPending"
	statusInProgress             = "RequestInProgress"
	statusCachingInProgress      = "RequestCachingInProgress"
	statusInstancesInProgress    = "RequestInstancesInProgress"
	statusCompleted              = "RequestCompleted"
	statusCompletionAcknowledged = "RequestCompletionAcknowledged"
	statusFailed                 = "RequestFailed"
	statusFailureAcknowledged    = "RequestFailureAcknowledged"

	// actionTermination is the ICMSRequest spec.action value for a teardown.
	// Matches common.TerminationAction in the NVCA API (pkg icms-translate).
	actionTermination = "TerminateInstances"
)

// DerivePhase folds a granular ICMSRequest status into a user-facing Phase.
//
// Precedence matters:
//   - FAILED first, so failed requests are never miscategorised as DRAINING/ACTIVE.
//   - DRAINING next: a cluster in CordonAndDrain maintenance, a termination
//     request, or instances mid-termination all mean the function is winding
//     down regardless of its raw status.
//   - Then the normal DEPLOYING/ACTIVE split.
//
// An unrecognised status falls back to DEPLOYING rather than being dropped, so
// a future NVCA status value still surfaces (as in-progress) instead of
// vanishing from the listing.
func DerivePhase(requestStatus, action string, clusterInMaintenanceDrain, instancesTerminating bool) Phase {
	switch requestStatus {
	case statusFailed, statusFailureAcknowledged:
		return PhaseFailed
	}

	if clusterInMaintenanceDrain || action == actionTermination || instancesTerminating {
		return PhaseDraining
	}

	switch requestStatus {
	case statusPending, statusInProgress, statusCachingInProgress, statusInstancesInProgress:
		return PhaseDeploying
	case statusCompleted, statusCompletionAcknowledged:
		return PhaseActive
	default:
		return PhaseDeploying
	}
}
