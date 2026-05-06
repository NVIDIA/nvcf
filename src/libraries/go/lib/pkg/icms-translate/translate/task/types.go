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

package task

import (
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
)

const (
	taskContainerName = "task"

	// Task result handling envs
	maxRunTimeEnvKey          = "NVCT_MAX_RUN_TIME_DURATION"
	resultHandlingStratEnvKey = "NVCT_RESULT_HANDLING_STRATEGY"

	// Minimum 1 hour termination grace period for pods.
	minimumTermGracePeriodSeconds int64 = 60 * 60
)

type CreationQueueMessage struct {
	common.CreationQueueMessageMetadata

	Details Details `json:"taskDetails"`

	LaunchSpecification LaunchSpecification `json:"launchSpecification"`
}

func (m CreationQueueMessage) GetCreationQueueMessageMetadata() common.CreationQueueMessageMetadata {
	return m.CreationQueueMessageMetadata
}

// +k8s:deepcopy-gen=true
type Details struct {
	TaskID   string `json:"taskId"`
	TaskType string `json:"taskType"`
}

// +k8s:deepcopy-gen=true
type LaunchSpecification struct {
	ContainerImage                 string                        `json:"containerImage"`
	EnvironmentB64                 string                        `json:"environment"`
	CloudProvider                  string                        `json:"cloudProvider"`
	MaxRuntimeDuration             string                        `json:"maxRuntimeDuration"`
	MaxQueuedDuration              string                        `json:"maxQueuedDuration"`
	TerminationGracePeriodDuration string                        `json:"terminationGracePeriodDuration"`
	ResultHandlingStrategy         common.ResultHandlingStrategy `json:"resultHandlingStrategy"`
	ICMSEnvironment                string                        `json:"icmsEnvironment"`
	// Telemetry configuration metadata.
	Telemetries *common.TelemetriesLaunchSpecification `json:"telemetries,omitempty"`

	// Helm chart function components of the launch spec.
	*common.HelmChartLaunchSpecification `json:",inline"`
	// Cache object configuration metadata of the launch spec.
	*common.CacheLaunchSpecification `json:",inline"`
}
