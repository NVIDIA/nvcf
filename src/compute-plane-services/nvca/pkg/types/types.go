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
	"encoding/json"
	"fmt"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue"
)

const (
	// MaxUpgradeSuccessNotificationSeconds is the maximum amount of time we
	// will notify success of upgrade.
	MaxUpgradeSuccessNotificationSeconds = 3600
	AppName                              = "nvca"
	HeaderNVClusterID                    = "X-Nv-Cluster-Id"
	DefaultICMSRequestNamespace          = "nvcf-backend"
)

const (
	LaunchArtifactTypeBYOOOTelCollectorService = function.LaunchArtifactType("BYOO_OTEL_COLLECTOR_SERVICE")
)

type EventCategory string

const (
	EventCategoryModelCaching         EventCategory = "ModelCaching"
	EventCategoryInstanceCreation     EventCategory = "InstanceCreation"
	EventCategoryInstanceStatusUpdate EventCategory = "InstanceStatusUpdate"
	EventCategoryInstanceTermination  EventCategory = "InstanceTermination"
)

type NVCAUpgradeStatus string

const (
	NVCAUpgradeNoStatus      NVCAUpgradeStatus = ""
	NVCAUpgradeInProgress    NVCAUpgradeStatus = "IN_PROGRESS"
	NVCAUpgradeStatusFailed  NVCAUpgradeStatus = "FAILED"
	NVCAUpgradeStatusSuccess NVCAUpgradeStatus = "SUCCESS"
)

type HelmChartArtifactSpec struct {
	HelmChartURL  string `json:"helmChart,omitempty"`
	RepositoryURL string `json:"repository"`
	ChartName     string `json:"chartname"`
	Version       string `json:"version"`
	ValuesJSON    []byte `json:"values"`

	HelmChartServiceName string `json:"helmChartServiceName,omitempty"`
	HelmChartServicePort *int32 `json:"helmChartServicePort,omitempty"`
}

type HelmCredsArtifactSpec struct {
	APIKey string `json:"apiKey"`
}

type QueueCredentials struct {
	ClusterCreationQueues     CreationQueueInfoSet   `json:"clusterCreationQueue,omitempty"`
	TaskClusterCreationQueues CreationQueueInfoSet   `json:"clusterCreationQueueForTasks,omitempty"`
	CreationQueues            CreationQueueInfoSet   `json:"creationQueue,omitempty"`
	TerminationQueue          queue.MessageQueueInfo `json:"terminationQueue,omitempty"`
}

type CreationQueueInfoSet map[GPUName]queue.MessageQueueInfo

var _ json.Unmarshaler = (*CreationQueueInfoSet)(nil)

func (qs *CreationQueueInfoSet) UnmarshalJSON(qsRaw []byte) error {
	var t map[GPUName]queue.MessageQueueInfo
	if err := json.Unmarshal(qsRaw, &t); err != nil {
		return fmt.Errorf("decode queue creds: %v", err)
	}

	*qs = t
	for gpu, cq := range *qs {
		if gpu == "" {
			continue
		}
		newCQ := cq
		newCQ.GPU = string(gpu)
		(*qs)[gpu] = newCQ
	}

	return nil
}

type ICMSRegistrationRequest struct {
	ClusterStatus string            `json:"status,omitempty"`
	K8sVersion    string            `json:"k8sVersion,omitempty"`
	BackendGPUs   []RegistrationGPU `json:"gpus,omitempty"`
	// ClusterTargeting enables the ICMS feature to return
	// targeted cluster queues.
	ClusterTargeting bool `json:"allowClusterTargeting,omitempty"`
	// TaskClusterCreationQueues enables the ICMS feature to return
	// targeted task-specific cluster queues.
	TaskClusterCreationQueues bool   `json:"allowTaskClusterCreationQueues,omitempty"`
	NVCAVersion               string `json:"nvcaVersion,omitempty"`
}

type RegistrationGPU struct {
	Name          string                     `json:"name,omitempty"`
	Capacity      uint64                     `json:"capacity,omitempty"`
	InstanceTypes []RegistrationInstanceType `json:"instanceTypes,omitempty"`
}

type RegistrationInstanceType struct {
	Name          string `json:"name,omitempty"`
	Value         string `json:"value,omitempty"`
	Description   string `json:"description,omitempty"`
	Default       bool   `json:"default,omitempty"`
	CPU           string `json:"cpu"`
	SystemMemory  string `json:"systemMemory,omitempty"`
	GPUMemory     string `json:"gpuMemory,omitempty"`
	GPUCount      uint64 `json:"gpuCount"`
	CPUArch       string `json:"cpuArch,omitempty"`
	Storage       string `json:"storage,omitempty"`
	OS            string `json:"os,omitempty"`
	DriverVersion string `json:"driverVersion,omitempty"`
	// NodeType indicates whether the instance type targets one node or multiple nodes.
	NodeType RegistrationInstanceTypeNodeType `json:"nodeType"`
	// TODO: remove cpuCores once ICMS and UI accept both in prod.
	CPUCores uint64 `json:"cpuCores"`
	// MaxInstances are the maximum number of instances that can be deployed
	// given node resource availability.
	MaxInstances uint64 `json:"maxInstances"`
	// UnschedulableCapacity are the maximum number of instances that can be deployed
	// given unschedulable node resource availability.
	UnschedulableCapacity uint64 `json:"unschedulableCapacity"`
}

type RegistrationInstanceTypeNodeType string

const (
	RegistrationInstanceTypeNodeTypeSingle RegistrationInstanceTypeNodeType = "SINGLE"
	RegistrationInstanceTypeNodeTypeMulti  RegistrationInstanceTypeNodeType = "MULTI"
)

func (t RegistrationInstanceTypeNodeType) String() string {
	return string(t)
}

func (t *RegistrationInstanceType) String() string {
	return fmt.Sprintf("%s|cpuc=%s|sysm=%s|gpuc=%d|gpum=%s",
		t.Name, t.CPU, t.SystemMemory, t.GPUCount, t.GPUMemory)
}

type ICMSRegistrationResponse struct {
	ClusterID      string           `json:"clusterId,omitempty"`
	ClusterGroupID string           `json:"clusterGroupId,omitempty"`
	Credentials    QueueCredentials `json:"credentials,omitempty"`
}

type ICMSServerInstanceState struct {
	InstanceID    string            `json:"instanceId,omitempty"`
	RequestID     string            `json:"requestId,omitempty"`
	InstanceState ICMSInstanceState `json:"instanceState,omitempty"`
}

type DeploymentInfo struct {
	GPUType, RequestID, NCAID             string
	TaskID, FunctionID, FunctionVersionID string
	MessageID, MessageBatchID             string
}

type ICMSInstanceStatusResponse struct {
	Instances []ICMSServerInstanceState `json:"instances,omitempty"`
}

type ICMSRequestStatus string

const (
	// OTel track state changes of
	// RequestPending -> RequestCachingInProgress -> RequestInstancesInProgress -> RequestCompleted/RequestFailed

	ICMSRequestPending                      ICMSRequestStatus = "pending-fulfillment"
	ICMSRequestFulfilled                    ICMSRequestStatus = "fulfilled"
	ICMSRequestInstanceTerminatedNoCapacity ICMSRequestStatus = "instance-terminated-no-capacity"
	ICMSRequestInstanceTerminatedByUser     ICMSRequestStatus = "instance-terminated-by-user"
	ICMSRequestInstanceTerminatedByService  ICMSRequestStatus = "instance-terminated-by-service"
)

type ICMSAcknowledgeRequest struct {
	InstanceCount  uint64            `json:"instanceCount"`
	MessageBatchID string            `json:"messageBatchId,omitempty"`
	Status         ICMSRequestStatus `json:"status,omitempty"`
}

type ICMSInstanceRequestState string

const (
	ICMSInstanceRequestActive ICMSInstanceRequestState = "active"
	ICMSInstanceRequestClosed ICMSInstanceRequestState = "closed"
)

type ICMSInstanceState string

const (
	ICMSInstanceStateNoStatus                    ICMSInstanceState = ""
	ICMSInstanceStarted                          ICMSInstanceState = "starting"
	ICMSInstanceRunning                          ICMSInstanceState = "running"
	ICMSInstanceShuttingDown                     ICMSInstanceState = "shutting-down"
	ICMSInstanceTerminated                       ICMSInstanceState = "terminated"
	ICMSInstanceFailed                           ICMSInstanceState = "failed"
	ICMSInstanceKilledNoCapacity                 ICMSInstanceState = "instance-terminated-no-capacity"
	ICMSInstanceKilledAdmissionError             ICMSInstanceState = "instance-terminated-admission-error"
	ICMSInstanceSucceeded                        ICMSInstanceState = "succeeded"
	ICMSInstanceFailedInitContainerStuck         ICMSInstanceState = "init-container-stuck"
	ICMSInstanceFailedImagePullIssues            ICMSInstanceState = "image-pull-issues"
	ICMSInstanceFailedInitContainerRestartLoop   ICMSInstanceState = "init-container-restart-loop"
	ICMSInstanceFailedContainerRestartLoop       ICMSInstanceState = "container-restart-loop"
	ICMSInstanceFailedCreateContainerError       ICMSInstanceState = "create-container-error"
	ICMSInstanceFailedNotFound                   ICMSInstanceState = "instance-not-found"
	ICMSInstanceTerminatedPreconditionFailure    ICMSInstanceState = "instance-terminated-precondition-failure"
	ICMSInstanceTerminatedTerminalError          ICMSInstanceState = "instance-terminated-terminal-error"
	ICMSInstanceDegradedWorker                   ICMSInstanceState = "degraded-worker"
	ICMSInstanceTerminatedDuetoSyncAction        ICMSInstanceState = "instance-terminated-sync-action"
	ICMSInstanceTerminatedServiceMaintenance     ICMSInstanceState = "instance-terminated-service-maintenance"
	ICMSInstanceSharedStorageFailure             ICMSInstanceState = "shared-storage-failure"
	ICMSInstanceInternalPersistentStorageFailure ICMSInstanceState = "internal-persistent-storage-failure"
	ICMSInstanceModelCacheFailure                ICMSInstanceState = "model-cache-failure"
	ICMSInstanceUtilsPodNotFound                 ICMSInstanceState = "utils-pod-not-found"
)

type HealthInfo struct {
	ErrorLog    string `json:"errorLog,omitempty"`
	ErrorSource string `json:"errorSource,omitempty"`
}

const (
	ErrorSourceK8s           = "k8s"
	ErrorSourceUtils         = "utils"
	ErrorSourceInit          = "init"
	ErrorSourceSharedStorage = "shared_storage"
	ErrorSourceTaskContainer = "task_container"
)

type ICMSInstanceStatusUpdateRequest struct {
	Status           ICMSRequestStatus        `json:"status,omitempty"`
	InstanceState    ICMSInstanceState        `json:"instanceState,omitempty"`
	RequestState     ICMSInstanceRequestState `json:"requestState,omitempty"`
	Action           common.MessageAction     `json:"action,omitempty"`
	TerminationCause ICMSInstanceState        `json:"terminationCause,omitempty"`
	HealthInfo       HealthInfo               `json:"healthInfo,omitempty"`
	SystemFailure    string                   `json:"systemFailure,omitempty"`
	MessageBatchID   string                   `json:"messageBatchId,omitempty"`
	InstanceIPs      []string                 `json:"instanceIps,omitempty"`
}

type ICMSRequestUpdateInfo struct {
	RequestID  string                          `json:"requestID,omitempty"`
	InstanceID string                          `json:"instanceID,string"`
	Payload    ICMSInstanceStatusUpdateRequest `json:"payload,omitempty"`
}

type HealthStatus string

const (
	HealthStatusHealthy   HealthStatus = "healthy"
	HealthStatusUnhealthy HealthStatus = "unhealthy"
)

// MaintenanceMode represents the operational mode of NVCA
type MaintenanceMode string

const (
	// MaintenanceModeNone indicates normal operation mode
	MaintenanceModeNone MaintenanceMode = "None"
	// MaintenanceModeCordon indicates maintenance mode where creation tasks/functions are cordoned (paused)
	MaintenanceModeCordon MaintenanceMode = "CordonOnly"
	// MaintenanceModeCordonAndDrain indicates maintenance mode where creation is cordoned and existing workloads are drained
	MaintenanceModeCordonAndDrain MaintenanceMode = "CordonAndDrain"
)

// String returns the string representation of the MaintenanceMode
func (m MaintenanceMode) String() string {
	return string(m)
}

// AllMaintenanceModes lists every MaintenanceMode value, used to
// zero-initialize the maintenance-mode metric so all series appear on the
// first Prometheus scrape.
var AllMaintenanceModes = []MaintenanceMode{
	MaintenanceModeNone,
	MaintenanceModeCordon,
	MaintenanceModeCordonAndDrain,
}

// HealthStatusRequest is the payload type for reporting NVCA health to ICMS.
type HealthStatusRequest struct {
	Status              HealthStatus            `json:"status,omitempty"`
	UpgradeStatus       NVCAUpgradeStatus       `json:"upgradeStatus,omitempty"`
	GPUUsage            map[GPUName]GPUResource `json:"gpuUsage,omitempty"`
	ClusterOwnerNCAID   string                  `json:"clusterOwnerNcaID,omitempty"`
	NVCAAgentVersion    string                  `json:"nvcaAgentVersion,omitempty"`
	NVCAOperatorVersion string                  `json:"nvcaOperatorVersion,omitempty"`
	ClusterName         string                  `json:"clusterName,omitempty"`
	MaintenanceMode     MaintenanceMode         `json:"maintenanceMode,omitempty"`
}

// HealthAction represents the action that ICMS can instruct NVCA to take
type HealthAction string

const (
	// HealthActionAccepted indicates normal operation should continue
	HealthActionAccepted HealthAction = "ACCEPTED"
	// HealthActionSelfDestruct indicates NVCA should terminate workloads and stop ICMS communication
	HealthActionSelfDestruct HealthAction = "SELF_DESTRUCT"
)

// String returns the string representation of the HealthAction
func (h HealthAction) String() string {
	return string(h)
}

// HealthStatusResponse is the payload type for ICMS responses to NVCA health reports.
// It contains instructions for NVCA on what action to take.
type HealthStatusResponse struct {
	Action HealthAction `json:"action"`
}

// AgentHealth is the internal representation of NVCA health,
// and all its cluster component dependencies.
type AgentHealth struct {
	// Status is an aggregate of all component health statuses,
	// plus core status.
	Status HealthStatus
	// GPUUsage is the resource usage and availability of all GPUs
	// available to NVCF workloads.
	GPUUsage map[GPUName]GPUResource
	// Components are the named set of cluster component dependencies, ex. Kata.
	Components map[string]ComponentHealth
}

// ComponentHealth contains information about a specific cluster component's health.
type ComponentHealth struct {
	// Status of the component.
	Status HealthStatus
	// Errors from the component itself, not from the healthcheck method.
	Errors []string
	// StatusLevel represents the severity level of a status
	StatusLevel StatusLevel `json:"-"`
}

// StatusLevel represents the severity level of a status
type StatusLevel int

const (
	// StatusLevelError indicates an error-level status that requires attention
	StatusLevelError StatusLevel = iota
	// StatusLevelWarn indicates a warning-level status that may require attention
	StatusLevelWarn
)

func (ch ComponentHealth) IsHealthy() bool {
	return ch.Status == HealthStatusHealthy && len(ch.Errors) == 0
}

type ICMSCredentialResponse struct {
	QueueCredentials
}

type ICMSTerminationMessage struct {
	RequestID         string               `json:"requestId,omitempty"`
	NCAId             string               `json:"ncaId,omitempty"`
	FunctionID        string               `json:"functionId,omitempty"`
	FunctionVersionID string               `json:"functionVersionId,omitempty"`
	ClusterName       string               `json:"availabilityZone,omitempty"`
	Action            common.MessageAction `json:"action,omitempty"`
	//nolint:revive
	InstanceIds []string `json:"instanceIds,omitempty"`
}
