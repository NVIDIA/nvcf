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

package v1alpha1

type MiniServicePhase string

const (
	MiniServiceCacheInProgress MiniServicePhase = "CachingInProgress"
	MiniServiceInstalling      MiniServicePhase = "Installing"
	MiniServiceInstalled       MiniServicePhase = "Installed"
	MiniServiceStarting        MiniServicePhase = "Starting"
	MiniServiceInstallFailed   MiniServicePhase = "InstallFailed"
	MiniServiceRunning         MiniServicePhase = "Running"
	MiniServiceFailed          MiniServicePhase = "Failed"
	// MiniServiceCompleted is used for tasks, which run to completion.
	MiniServiceCompleted MiniServicePhase = "Completed"
)

const (
	MiniServiceConditionCacheSuccessful   = "CacheSuccessful"
	MiniServiceConditionInstallSuccessful = "InstallSuccessful"
	MiniServiceConditionObjectsHealthy    = "ObjectsHealthy"
	MiniServiceConditionWorkersHealthy    = "WorkersHealthy"
	MiniServiceConditionCleanupSuccessful = "CleanupSuccessful"
)

const (
	// When caching fails.
	MiniServiceStatusReasonCachingFailed = "CachingFailed"
	// When caching succeeds.
	MiniServiceStatusReasonArtifactsCached = "ArtifactsCached"
	// When caching is still in progress.
	MiniServiceStatusReasonCachingInProgress = "CachingInProgress"
	// When the reval service finds the chart to be invalid.
	MiniServiceStatusReasonReValResultInvalid = "ReValResultInvalid"
	// When some objects failed to deploy.
	MiniServiceStatusReasonObjectsFailed = "ObjectsFailed"
	// When some objects failed to deploy with backoff.
	MiniServiceStatusReasonObjectsFailedWithinBackoffTimeout = "ObjectsFailedWithinBackoffTimeout"
	// When some workload pods are degraded.
	MiniServiceStatusReasonDegradedWorkload = "DegradedWorkload"
	// When some worker pods are degraded.
	MiniServiceStatusReasonDegradedWorker = "DegradedWorker"
	// When some objects were pending for too long.
	MiniServiceStatusReasonPendingTimeout = "ObjectsTimedOutPending"
	// Waiting on objects to be ready.
	MiniServiceStatusReasonWaitingObjectReadiness = "WaitingOnObjectReadiness"
	// When collection of object statuses encountered errors.
	MiniServiceStatusReasonObjectStatusErrors     = "ObjectStatusErrors"
	MiniServiceStatusReasonUnexpectedInstallError = "UnexpectedInstallError"
	MiniServiceStatusReasonUnexpectedRuntimeError = "UnexpectedRuntimeError"
)

var MiniServiceStatusBadReasons = map[string]bool{
	MiniServiceStatusReasonCachingFailed:          true,
	MiniServiceStatusReasonReValResultInvalid:     true,
	MiniServiceStatusReasonObjectsFailed:          true,
	MiniServiceStatusReasonDegradedWorkload:       true,
	MiniServiceStatusReasonDegradedWorker:         true,
	MiniServiceStatusReasonPendingTimeout:         true,
	MiniServiceStatusReasonObjectStatusErrors:     true,
	MiniServiceStatusReasonUnexpectedInstallError: true,
	MiniServiceStatusReasonUnexpectedRuntimeError: true,
}
