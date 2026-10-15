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

package modelcachetypes

// Result values for model cache metrics
const (
	ResultSuccess = "success"
	ResultFailure = "failure"
)

// Failure reason values for model cache metrics
const (
	ReasonCacheSpecInvalid   = "cache_spec_invalid"
	ReasonPVCSetupFailed     = "pvc_setup_failed"
	ReasonPVCBindFailed      = "pvc_bind_failed"
	ReasonRWPVCBindFailed    = "rw_pvc_bind_failed"
	ReasonJobNotFound        = "job_not_found"
	ReasonJobBackoffExceeded = "job_backoff_exceeded"
	ReasonJobTimeout         = "job_timeout"
	ReasonImagePull          = "image_pull"
	ReasonInitStuck          = "init_stuck"
	ReasonSchedulingTimeout  = "scheduling_timeout"
	ReasonAdmissionRejected  = "admission_rejected"
	ReasonInitJobFailed      = "init_job_failed"
)

// AllFailureReasons is the complete set of known failure reasons.
// Used for pre-initializing Prometheus counters to zero.
var AllFailureReasons = []string{
	ReasonCacheSpecInvalid,
	ReasonPVCSetupFailed,
	ReasonPVCBindFailed,
	ReasonRWPVCBindFailed,
	ReasonJobNotFound,
	ReasonJobBackoffExceeded,
	ReasonJobTimeout,
	ReasonImagePull,
	ReasonInitStuck,
	ReasonSchedulingTimeout,
	ReasonAdmissionRejected,
	ReasonInitJobFailed,
}

// Backend label values come from the HelmCacheBackend constants in pkg/types
// (shared with pkg/storage); callers pass string(backend) directly. See
// nvcatypes.AllSelectableHelmCacheBackends for the pre-initialization set.
