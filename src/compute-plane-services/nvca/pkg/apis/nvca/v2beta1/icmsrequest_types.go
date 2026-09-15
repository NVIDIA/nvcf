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

package v2beta1

import (
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:openapi-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// ICMSRequest represents Request Object as obtained from ICMS to be applied by NVCA and the progress of the request.
type ICMSRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ICMSRequestSpec   `json:"spec,omitempty"`
	Status            ICMSRequestStatus `json:"status,omitempty"`
}

// +k8s:openapi-gen=true
// ICMSRequestSpec defines the desired request to be handled by NVCA
type ICMSRequestSpec struct {
	RequestID          string                     `json:"requestId,omitempty"`
	MessageReceipt     string                     `json:"sqsMessageReceipt,omitempty"`
	NCAId              string                     `json:"ncaId,omitempty"`
	MessageBatchID     string                     `json:"messageBatchId,omitempty"`
	Action             common.MessageAction       `json:"action,omitempty"`
	CreationMsgInfo    ICMSCreationMessageInfo    `json:"creationMsgInfo"`
	TerminationMsgInfo ICMSTerminationMessageInfo `json:"terminationMsgInfo"`

	// FunctionDetails will be set if the message is for creating a function.
	FunctionDetails function.Details `json:"functionDetails,omitempty"`
	// TaskDetails will be set if the message is for creating a task.
	TaskDetails task.Details `json:"taskDetails,omitempty"`

	// Deprecated: use functionDetails.functionId
	FunctionID string `json:"functionId,omitempty"`
	// Deprecated: use functionDetails.functionVersionId
	FunctionVersionID string `json:"functionVersionId,omitempty"`
}

type RequestStatus string

const (
	// NVCA is waiting to create the instances for the request
	ICMSRequestStatusPending RequestStatus = "RequestPending"
	// NVCA is creating the requested number of Instances for the request
	ICMSRequestStatusInProgress RequestStatus = "RequestInProgress"
	// NVCA has ack'ed the request and attempting Caching setup as requested
	ICMSRequestStatusCachingInProgress RequestStatus = "RequestCachingInProgress"
	// NVCA has created requested number of instances
	ICMSRequestStatusInstancesInProgress RequestStatus = "RequestInstancesInProgress"
	// All requested instances are in Running State
	ICMSRequestStatusCompleted              RequestStatus = "RequestCompleted"
	ICMSRequestStatusCompletionAcknowledged RequestStatus = "RequestCompletionAcknowledged"
	ICMSRequestStatusFailed                 RequestStatus = "RequestFailed"
	ICMSRequestStatusFailureAcknowledged    RequestStatus = "RequestFailureAcknowledged"
)

type InstanceType string

const (
	InstanceTypePod         InstanceType = "Pod"
	InstanceTypeNGCJob      InstanceType = "NGCJob"
	InstanceTypeMiniService InstanceType = "MiniService"
)

type InstanceStatus struct {
	ID                    string            `json:"id,omitempty"`
	Type                  InstanceType      `json:"instanceType,omitempty"`
	Status                string            `json:"status,omitempty"`
	LastReportedStatus    string            `json:"lastReportedStatus,omitempty"`
	LastReportedTimestamp *metav1.Time      `json:"lastReportedTimestamp,omitempty"`
	Attributes            map[string]string `json:"attributes,omitempty"`
}

func (s ICMSRequestSpec) GetTraceContext() ICMSRequestTraceContextConfig {
	// This can be empty values since it gets handled properly downstream
	return ICMSRequestTraceContextConfig{
		TraceParent: s.CreationMsgInfo.TraceParent,
		TraceState:  s.CreationMsgInfo.TraceState,
	}
}

// ICMSRequestTraceContextConfig is the trace information sent from ICMS.
type ICMSRequestTraceContextConfig struct {
	TraceParent string            `json:"traceParent"`
	TraceState  map[string]string `json:"traceState"`
}

// ICMSRequestSpanContextConfig represents span context configuration for tracing.
type ICMSRequestSpanContextConfig struct {
	SpanName  string      `json:"spanName"`
	StartTime metav1.Time `json:"startTime"`
}

// +k8s:openapi-gen=true
// ICMSRequestStatus is the NVCA managed status of all requests in the backend.
type ICMSRequestStatus struct {
	RequestStatus                      RequestStatus             `json:"requestStatus,omitempty"`
	LastStatusUpdated                  *metav1.Time              `json:"lastStatusUpdated,omitempty"`
	LastObservedAllTerminatedInstances *metav1.Time              `json:"lastObservedAllTerminatedInstances,omitempty"`
	LastACKTimestamp                   *metav1.Time              `json:"lastACKTimestamp,omitempty"`
	CacheReferenceName                 string                    `json:"cacheReferenceName,omitempty"`
	Instances                          map[string]InstanceStatus `json:"instances,omitempty"`
	ReconcileErrors                    uint64                    `json:"reconcileErrors,omitempty"`
	LastReconcileError                 string                    `json:"lastReconcileError,omitempty"`
	// RequestStatusTraceContexts represents a of map of traces
	// to be closed when the state represented by the
	// the RequestStatus is reached.
	RequestStatusTraceContexts map[RequestStatus]ICMSRequestSpanContextConfig `json:"requestStatusTraceContexts,omitempty"`
}

// GetInstanceIDs returns the Function Instance IDs for the ICMSRequestStatus.
func (s ICMSRequestStatus) GetInstanceIDs() []string {
	if len(s.Instances) == 0 {
		return nil
	}

	instanceIDs := make([]string, len(s.Instances))
	i := 0
	for k := range s.Instances {
		instanceIDs[i] = k
		i++
	}
	return instanceIDs
}

// +k8s:openapi-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ICMSRequestList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ICMSRequest `json:"items"`
}

// +k8s:openapi-gen=true
type ICMSCreationMessageInfo struct {
	// Deprecated: this no longer will be set since cluster group is static
	ClusterGroup string `json:"clusterGroup,omitempty"`
	// Deprecated: use gpuType
	GPUName  string `json:"gpuName"`
	QueueURL string `json:"queueURL,omitempty"`

	common.CreationQueueMessageMetadata `json:",inline"`

	LaunchArtifacts             function.LaunchArtifacts      `json:"launchArtifacts,omitempty"`
	FunctionLaunchSpecification *function.LaunchSpecification `json:"functionLaunchSpecification,omitempty"`

	TaskLaunchSpecification *task.LaunchSpecification `json:"taskLaunchSpecification,omitempty"`
}

func (ci ICMSCreationMessageInfo) GetInstanceTypeLabelSelValue() string {
	if ci.InstanceTypeValue != "" {
		return ci.InstanceTypeValue
	}
	return ci.InstanceType //nolint:staticcheck
}

// +k8s:openapi-gen=true
type ICMSTerminationMessageInfo struct {
	ClusterName string `json:"availabilityZone,omitempty"`
	//nolint:revive
	InstanceIds []string `json:"instanceIds,omitempty"`
}

// GetICMSEnvironment returns the ICMSEnvironment from the ICMSRequestSpec.
func (s ICMSRequestSpec) GetICMSEnvironment() string {
	var icmsEnv string
	if s.CreationMsgInfo.FunctionLaunchSpecification != nil {
		icmsEnv = s.CreationMsgInfo.FunctionLaunchSpecification.ICMSEnvironment
	} else if s.CreationMsgInfo.TaskLaunchSpecification != nil {
		icmsEnv = s.CreationMsgInfo.TaskLaunchSpecification.ICMSEnvironment
	}
	return icmsEnv
}
