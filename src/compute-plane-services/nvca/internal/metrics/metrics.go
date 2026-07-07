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

package metrics

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	promdto "github.com/prometheus/client_model/go"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"

	translatecommon "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"

	ictx "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/context"
	modelcachetypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/modelcachetypes"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/workloadtypes"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	InstanceTypeAllocatableMetricName          = "nvca_instance_type_allocatable"
	InstanceTypeCapacityMetricName             = "nvca_instance_type_capacity"
	GPUNodeUnclassifiedCountMetricName         = "nvca_gpu_node_unclassified_count"
	GPUNodeTotalCountMetricName                = "nvca_gpu_node_total_count"
	ContainerCrashTotalMetricName              = "nvca_container_crash_total"
	ContainerRestartTotalMetricName            = "nvca_container_restart_total"
	EventErrorTotalMetricName                  = "nvca_event_error_total"
	EventQueueLengthMetricName                 = "nvca_event_queue_length"
	EventQueueProcessLatencyMetricName         = "nvca_event_process_latency"
	MessageQueueProcessedTotalMetricName       = "nvca_queue_message_processed_total"
	MessageQueueDequeuedTotalMetricName        = "nvca_queue_message_dequeued_total"
	MessageQueueDequeueBatchSizeMetricName     = "nvca_queue_dequeue_batch_size"
	InstanceTypeUnschedulableMetricName        = "nvca_instance_type_unschedulable"
	ImagePullIssueTotalMetricName              = "nvca_image_pull_issue_total"
	K8sAPISuccessTotalMetricName               = "nvca_k8s_api_success_total"
	K8sAPIFailureTotalMetricName               = "nvca_k8s_api_failure_total"
	StorageRequestDurationMetricName           = "nvca_storage_controller_request_duration"
	MiniServiceReconcilePhaseTotalMetricName   = "nvca_miniservice_controller_reconcile_phase_total"
	MiniServicePhaseTransitionsTotalMetricName = "nvca_miniservice_controller_phase_transitions_total"
	MiniServiceFailuresTotalMetricName         = "nvca_miniservice_controller_failures_total"
	MiniServiceReadyStatusMetricName           = "nvca_miniservice_controller_miniservice_ready_status"
	MiniServiceReValRequestTotalMetricName     = "nvca_miniservice_controller_reval_request_total"
	MiniServiceEventErrorTotalMetricName       = "nvca_miniservice_controller_event_error_total"
	NVLinkAllocationCreatedTotalMetricName     = "nvca_nvlinkopt_allocation_created"
	NVLinkAllocationSuccessTotalMetricName     = "nvca_nvlinkopt_allocation_success"
	NVLinkAllocationFailureTotalMetricName     = "nvca_nvlinkopt_allocation_failure"

	// GC metrics
	OrphanedResourceCleanupTotalMetricName = "nvca_gc_orphaned_resource_cleanup_total"
	GCCleanerRunTotalMetricName            = "nvca_gc_cleaner_run_total"

	// Model cache metrics
	ModelCacheResultTotalMetricName = "nvca_model_cache_result_total"

	// Cluster attribute metrics
	KataRuntimeIsolationEnabledMetricName = "nvca_kata_runtime_isolation_enabled"
	MaintenanceModeStateMetricName        = "nvca_maintenance_mode_state"

	// Cluster-validator metrics — populated by a SharedInformer that
	// watches the cluster-validator-summary ConfigMap. The dynamic-cardinality
	// metrics (per endpoint / per netpol pair) are prune-on-update so only
	// the latest run's labels are exposed at any moment.
	ClusterValidatorReadyMetricName             = "nvca_cluster_validator_ready"
	ClusterValidatorCheckStatusMetricName       = "nvca_cluster_validator_check_status"
	ClusterValidatorEndpointReachableMetricName = "nvca_cluster_validator_endpoint_reachable"
	ClusterValidatorNetpolPairPassedMetricName  = "nvca_cluster_validator_netpol_pair_passed" //nolint:gosec // metric name literal, not a credential
	ClusterValidatorLastRunTimestampMetricName  = "nvca_cluster_validator_last_run_timestamp_seconds"
	ClusterValidatorLastRunDurationMetricName   = "nvca_cluster_validator_last_run_duration_seconds"

	// Cluster-validator label keys
	ClusterValidatorCheckLabel      = "check"
	ClusterValidatorEndpointLabel   = "endpoint"
	ClusterValidatorNetpolPairLabel = "pair"
	ClusterValidatorDirectionLabel  = "direction"
	ClusterValidatorPolicySideLabel = "policy_side"
	ClusterValidatorCriticalLabel   = "critical"

	// Cluster-validator netpol policy-side label values. The direction
	// label values ("a_to_b"/"b_to_a") originate from the summary wire
	// format and flow through as map keys.
	clusterValidatorPolicySideEgress  = "egress"
	clusterValidatorPolicySideIngress = "ingress"

	// Workload result metrics
	WorkloadResultTotalMetricName = "nvca_workload_result_total"

	// Upstream (ICMS) request metrics
	UpstreamRequestTotalMetricName = "nvca_upstream_request_total"

	// Scheduler workload count metrics
	SchedulerWorkloadCountMetricName = "nvca_scheduler_workload_count"

	// Scheduler name constants
	SchedulerNameDefault = "default-scheduler"
	SchedulerNameKAI     = "kai-scheduler"

	// Label keys
	ClusterGroupLabel        = "nvca_cluster_group"
	ClusterNameLabel         = "nvca_cluster_name"
	ContainerLabel           = "container"
	EventNameLabel           = "nvca_event_name"
	InstanceTypeLabel        = "instance_type"
	MessageActionLabel       = "message_action"
	NCAIDLabel               = "nvca_nca_id"
	VersionLabel             = "nvca_version"
	ImageRegLabel            = "image_registry"
	K8sResourceLabel         = "resource"
	QueueTypeLabel           = "queue_type"
	GPUNameLabel             = "gpu_name"
	GPUFamilyLabel           = "gpu_family"
	GPUMachineLabel          = "gpu_machine"
	MiniServicePhaseLabel    = "miniservice_phase"
	FromPhaseLabel           = "from_phase"
	ToPhaseLabel             = "to_phase"
	FunctionIDLabel          = "function_id"
	FunctionVersionIDLabel   = "function_version_id"
	TaskIDLabel              = "task_id"
	EndpointLabel            = "endpoint"
	HTTPCodeLabel            = "http_code"
	StorageRequestPhaseLabel = "storage_request_phase"
	EventKindLabel           = "event_kind"
	// GC labels
	ResourceTypeLabel = "resource_type"
	StatusLabel       = "status"
	CleanerNameLabel  = "cleaner_name"

	// Model cache labels
	ResultLabel        = "result"
	FailureReasonLabel = "failure_reason"

	// Workload result labels
	WorkloadTypeLabel    = "workload_type"
	WorkloadKindLabel    = "workload_kind"
	WorkloadStatusLabel  = "workload_status"
	FailureCategoryLabel = "failure_category"

	// Upstream (ICMS) request labels
	OperationLabel  = "operation"
	HTTPStatusLabel = "http_status"

	// Scheduler workload count labels
	SchedulerNameLabel = "scheduler_name"

	// Maintenance-mode label
	MaintenanceModeLabel = "mode"

	// UpstreamOperation values for use with RecordUpstreamRequest.
	UpstreamOperationHeartbeat   = "heartbeat"
	UpstreamOperationRegister    = "register"
	UpstreamOperationCredentials = "credentials"
	UpstreamOperationJWKSPush    = "jwks-push"
)

// AllUpstreamOperations is the complete set of upstream operation label values.
var AllUpstreamOperations = []string{
	UpstreamOperationHeartbeat,
	UpstreamOperationRegister,
	UpstreamOperationCredentials,
	UpstreamOperationJWKSPush,
}

func getDefaultLabels() []string {
	return []string{
		NCAIDLabel,
		ClusterNameLabel,
		ClusterGroupLabel,
		VersionLabel,
	}
}

// getStorageLabels returns labels for storage metrics (backwards compatibility - no NCAID)
func getStorageLabels() []string {
	return []string{
		ClusterNameLabel,
		ClusterGroupLabel,
		VersionLabel,
	}
}

// getMiniServiceLabels returns labels for miniservice metrics (backwards compatibility - no NCAID in default labels)
func getMiniServiceLabels(additionalLabels ...string) []string {
	labels := []string{
		ClusterNameLabel,
		ClusterGroupLabel,
		VersionLabel,
	}
	return append(labels, additionalLabels...)
}

// getGCLabels returns labels for GC metrics (backwards compatibility - no NCAID)
func getGCLabels(additionalLabels ...string) []string {
	labels := []string{
		ClusterNameLabel,
		ClusterGroupLabel,
		VersionLabel,
	}
	return append(labels, additionalLabels...)
}

func withDefaultLabels(additionalLabels ...string) []string {
	return append(getDefaultLabels(), additionalLabels...)
}

func withStorageLabels(additionalLabels ...string) []string {
	return append(getStorageLabels(), additionalLabels...)
}

// Metrics is a struct contains the set of nvca metrics pointers,
// reference:
// https://docs.google.com/document/d/11dJ7yKX7IOGWZLp9EgLfU25YqfYCW_6Fytqx2kvQoo0/edit#heading=h.cqbpr1nozi13
type Metrics struct {
	EventErrorTotal            *prometheus.CounterVec
	EventQueueLength           *prometheus.GaugeVec
	EventProcessLatency        *prometheus.SummaryVec
	ContainerCrashTotal        *prometheus.CounterVec
	ContainerRestartTotal      *prometheus.CounterVec
	QueueMessageProcessedTotal *prometheus.CounterVec
	QueueMessageDequeuedTotal  *prometheus.CounterVec
	QueueDequeueBatchSize      *prometheus.HistogramVec
	ImagePullIssueTotal        *prometheus.CounterVec

	// K8s API server interaction metrics
	K8sAPISuccessTotal *prometheus.CounterVec
	K8sAPIFailureTotal *prometheus.CounterVec

	// instance type metrics
	InstanceTypeAllocatable   *prometheus.GaugeVec // node must be schedulable to be allocatable
	InstanceTypeCapacity      *prometheus.GaugeVec
	InstanceTypeUnschedulable *prometheus.GaugeVec // amount where node is schedule=false according to NVCA
	GPUNodeUnclassifiedCount  *prometheus.GaugeVec // count of GPU-bearing nodes with no recognized instance-type label
	GPUNodeTotalCount         *prometheus.GaugeVec // total count of GPU-bearing nodes seen, classified and unclassified

	// Storage controller metrics
	StorageRequestDuration *prometheus.SummaryVec

	// MiniService controller metrics
	MiniServiceReconcilePhaseTotal   *prometheus.CounterVec
	MiniServicePhaseTransitionsTotal *prometheus.CounterVec
	MiniServiceFailuresTotal         *prometheus.CounterVec
	MiniServiceReadyStatus           *prometheus.GaugeVec
	MiniServiceReValRequestTotal     *prometheus.CounterVec
	MiniServiceEventErrorTotal       *prometheus.CounterVec

	// NVLink-optimized metrics
	NVLinkAllocationCreatedCount *prometheus.GaugeVec
	NVLinkAllocationSuccessCount *prometheus.GaugeVec
	NVLinkAllocationFailureCount *prometheus.GaugeVec

	// GC metrics
	OrphanedResourceCleanupTotal *prometheus.CounterVec
	GCCleanerRunTotal            *prometheus.CounterVec

	// Model cache metrics
	ModelCacheResultTotal *prometheus.CounterVec

	// Cluster attribute metrics
	KataRuntimeIsolationEnabled *prometheus.GaugeVec

	// MaintenanceModeState reports the active maintenance mode: the series
	// whose mode label matches the active mode is set to 1 and every other
	// mode series is 0 (one-hot encoding). See SetMaintenanceModeState() for
	// the update protocol.
	MaintenanceModeState *prometheus.GaugeVec

	// Cluster-validator metrics — see SetClusterValidatorSummary() for the
	// update protocol. The dynamic-cardinality vectors (Endpoint, NetpolPair)
	// are prune-on-update so only the current run's label values are
	// exposed; Prometheus's TSDB retains historical points via normal scrape
	// history.
	ClusterValidatorReady             *prometheus.GaugeVec
	ClusterValidatorCheckStatus       *prometheus.GaugeVec
	ClusterValidatorEndpointReachable *prometheus.GaugeVec
	ClusterValidatorNetpolPairPassed  *prometheus.GaugeVec
	ClusterValidatorLastRunTimestamp  *prometheus.GaugeVec
	ClusterValidatorLastRunDuration   *prometheus.GaugeVec
	// clusterValidatorLastEmitted tracks label tuples emitted by the last
	// reconcile so the next update can prune stale series. Guarded by
	// clusterValidatorMu.
	clusterValidatorLastEmitted *clusterValidatorEmittedSet
	clusterValidatorMu          sync.Mutex

	// Workload result metrics
	WorkloadResultTotal *prometheus.CounterVec

	// Upstream (ICMS) request metrics
	UpstreamRequestTotal *prometheus.CounterVec

	// Scheduler workload count metrics
	SchedulerWorkloadCount *prometheus.GaugeVec

	// label values
	defaultLabelValues []string
	// storage label values (for backwards compatibility - excludes NCAID)
	storageLabelValues []string
	// miniservice label values (for backwards compatibility - excludes NCAID, added per-call)
	miniServiceLabelValues []string
	// gc label values (for backwards compatibility - excludes NCAID)
	gcLabelValues []string
	// Custom registerer
	registerer prometheus.Registerer
}

func (m *Metrics) Destroy() {
	prometheus.Unregister(m.EventQueueLength)
	prometheus.Unregister(m.EventProcessLatency)
	prometheus.Unregister(m.EventErrorTotal)
	prometheus.Unregister(m.ContainerCrashTotal)
	prometheus.Unregister(m.ContainerRestartTotal)
	prometheus.Unregister(m.QueueMessageProcessedTotal)
	prometheus.Unregister(m.QueueMessageDequeuedTotal)
	prometheus.Unregister(m.QueueDequeueBatchSize)
	prometheus.Unregister(m.ImagePullIssueTotal)
	prometheus.Unregister(m.K8sAPISuccessTotal)
	prometheus.Unregister(m.K8sAPIFailureTotal)
	prometheus.Unregister(m.InstanceTypeCapacity)
	prometheus.Unregister(m.InstanceTypeAllocatable)
	prometheus.Unregister(m.InstanceTypeUnschedulable)
	prometheus.Unregister(m.GPUNodeUnclassifiedCount)
	prometheus.Unregister(m.GPUNodeTotalCount)
	prometheus.Unregister(m.StorageRequestDuration)
	prometheus.Unregister(m.MiniServiceReconcilePhaseTotal)
	prometheus.Unregister(m.MiniServicePhaseTransitionsTotal)
	prometheus.Unregister(m.MiniServiceFailuresTotal)
	prometheus.Unregister(m.MiniServiceReadyStatus)
	prometheus.Unregister(m.MiniServiceReValRequestTotal)
	prometheus.Unregister(m.MiniServiceEventErrorTotal)
	prometheus.Unregister(m.NVLinkAllocationCreatedCount)
	prometheus.Unregister(m.NVLinkAllocationSuccessCount)
	prometheus.Unregister(m.NVLinkAllocationFailureCount)
	prometheus.Unregister(m.OrphanedResourceCleanupTotal)
	prometheus.Unregister(m.GCCleanerRunTotal)
	prometheus.Unregister(m.ModelCacheResultTotal)
	prometheus.Unregister(m.KataRuntimeIsolationEnabled)
	prometheus.Unregister(m.MaintenanceModeState)
	prometheus.Unregister(m.ClusterValidatorReady)
	prometheus.Unregister(m.ClusterValidatorCheckStatus)
	prometheus.Unregister(m.ClusterValidatorEndpointReachable)
	prometheus.Unregister(m.ClusterValidatorNetpolPairPassed)
	prometheus.Unregister(m.ClusterValidatorLastRunTimestamp)
	prometheus.Unregister(m.ClusterValidatorLastRunDuration)
	prometheus.Unregister(m.WorkloadResultTotal)
	prometheus.Unregister(m.UpstreamRequestTotal)
	prometheus.Unregister(m.SchedulerWorkloadCount)
}

type DefaultMetricsOption func(*Metrics)

func WithRegisterer(registerer prometheus.Registerer) DefaultMetricsOption {
	return func(m *Metrics) {
		m.registerer = registerer
	}
}

func WithEventErrorTotalDefaultEvents(eventNames []string) DefaultMetricsOption {
	return func(m *Metrics) {
		// nvca_event_error_total metrics should be initialized to zero
		for _, evt := range eventNames {
			if m.EventErrorTotal != nil {
				m.EventErrorTotal.WithLabelValues(m.WithDefaultLabelValues(evt)...)
			}
		}
	}
}

// WithKataRuntimeIsolationEnabled sets the initial value of the Kata runtime isolation gauge.
// This should be called once at agent startup based on the KataRuntimeIsolation cluster attribute.
func WithKataRuntimeIsolationEnabled(enabled bool) DefaultMetricsOption {
	return func(m *Metrics) {
		if m.KataRuntimeIsolationEnabled != nil {
			val := 0.0
			if enabled {
				val = 1.0
			}
			m.KataRuntimeIsolationEnabled.WithLabelValues(m.WithDefaultLabelValues()...).Set(val)
		}
	}
}

// WithMaintenanceMode sets the initial value of the maintenance-mode gauge.
// This should be called once at agent startup with the configured mode.
func WithMaintenanceMode(mode types.MaintenanceMode) DefaultMetricsOption {
	return func(m *Metrics) {
		m.SetMaintenanceModeState(mode)
	}
}

func WithContainerCrashAndRestartTotalDefaultContainerNames(containerNames []string) DefaultMetricsOption {
	return func(m *Metrics) {
		// nvca_container_crash_total metrics should be initialized to zero
		for _, container := range containerNames {
			if m.ContainerCrashTotal != nil {
				m.ContainerCrashTotal.WithLabelValues(m.WithDefaultLabelValues(container)...)
			}
			if m.ContainerRestartTotal != nil {
				m.ContainerRestartTotal.WithLabelValues(m.WithDefaultLabelValues(container)...)
			}
		}
	}
}

func NewDefaultMetrics(ncaID, clusterName, clusterGroup, version string, opts ...DefaultMetricsOption) *Metrics {
	// Build label values: [NCAID, ClusterName, ClusterGroup, Version]
	defaultLabelValues := []string{ncaID, clusterName, clusterGroup, version}
	// storageLabelValues should be: [ClusterName, ClusterGroup, Version] for backwards compatibility
	storageLabelValues := []string{clusterName, clusterGroup, version}
	// miniServiceLabelValues should be: [ClusterName, ClusterGroup, Version] for backwards compatibility
	miniServiceLabelValues := []string{clusterName, clusterGroup, version}
	// gcLabelValues should be: [ClusterName, ClusterGroup, Version] for backwards compatibility
	gcLabelValues := []string{clusterName, clusterGroup, version}

	m := &Metrics{
		defaultLabelValues:     defaultLabelValues,
		storageLabelValues:     storageLabelValues,
		miniServiceLabelValues: miniServiceLabelValues,
		gcLabelValues:          gcLabelValues,
		registerer:             prometheus.DefaultRegisterer,
	}

	// run options on metrics to set custom registerer
	for _, opt := range opts {
		opt(m)
	}

	promFactory := promauto.With(m.registerer)

	m.EventErrorTotal = promFactory.NewCounterVec(prometheus.CounterOpts{
		Name: EventErrorTotalMetricName,
		Help: "Total error count of NVCA event kind",
	}, withDefaultLabels(EventNameLabel))

	m.EventQueueLength = promFactory.NewGaugeVec(prometheus.GaugeOpts{
		Name: EventQueueLengthMetricName,
		Help: "Lengths of the NVCA event queues",
	}, withDefaultLabels(EventNameLabel))

	m.EventProcessLatency = promFactory.NewSummaryVec(prometheus.SummaryOpts{
		Name:       EventQueueProcessLatencyMetricName,
		Help:       "Latency of NVCA event processing",
		MaxAge:     1 * time.Hour,
		Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
	}, withDefaultLabels(EventNameLabel))

	m.ContainerCrashTotal = promFactory.NewCounterVec(prometheus.CounterOpts{
		Name: ContainerCrashTotalMetricName,
		Help: "Total number of container crashes of NVCA workload pods",
	}, withDefaultLabels(ContainerLabel))

	m.ContainerRestartTotal = promFactory.NewCounterVec(prometheus.CounterOpts{
		Name: ContainerRestartTotalMetricName,
		Help: "Total number of container restarts of NVCA workload pods",
	}, withDefaultLabels(ContainerLabel))

	// Queue message processed total metric and set default values to 0
	m.QueueMessageProcessedTotal = promFactory.NewCounterVec(prometheus.CounterOpts{
		Name: MessageQueueProcessedTotalMetricName,
		Help: "Total message count for this NVCA instance",
	}, withDefaultLabels(MessageActionLabel))
	for _, msgAction := range []string{
		string(translatecommon.FunctionCreationAction),
		string(translatecommon.TaskCreationAction),
		string(translatecommon.TerminationAction),
	} {
		m.QueueMessageProcessedTotal.WithLabelValues(m.WithDefaultLabelValues(msgAction)...)
	}

	// Queue message dequeued total metric to track dequeue rate per queue
	m.QueueMessageDequeuedTotal = promFactory.NewCounterVec(prometheus.CounterOpts{
		Name: MessageQueueDequeuedTotalMetricName,
		Help: "Total number of messages dequeued from SQS queues by queue type and GPU",
	}, withDefaultLabels(QueueTypeLabel, GPUNameLabel))

	// Queue dequeue batch size histogram to track distribution of messages per dequeue operation
	m.QueueDequeueBatchSize = promFactory.NewHistogramVec(prometheus.HistogramOpts{
		Name:    MessageQueueDequeueBatchSizeMetricName,
		Help:    "Distribution of batch sizes (number of messages) pulled per dequeue operation by queue type and GPU",
		Buckets: []float64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
	}, withDefaultLabels(QueueTypeLabel, GPUNameLabel))

	m.ImagePullIssueTotal = promFactory.NewCounterVec(prometheus.CounterOpts{
		Name: ImagePullIssueTotalMetricName,
		Help: "Total number of container image pull errors per registry host. Errors per registry host are counted once per NVCF instance",
	}, withDefaultLabels(ImageRegLabel))

	// K8s API server interaction metrics (only tracking Get operations)
	m.K8sAPISuccessTotal = promFactory.NewCounterVec(prometheus.CounterOpts{
		Name: K8sAPISuccessTotalMetricName,
		Help: "Total number of successful K8s API server Get operations",
	}, withDefaultLabels(K8sResourceLabel))

	m.K8sAPIFailureTotal = promFactory.NewCounterVec(prometheus.CounterOpts{
		Name: K8sAPIFailureTotalMetricName,
		Help: "Total number of failed K8s API server Get operations (excluding NotFound errors)",
	}, withDefaultLabels(K8sResourceLabel))
	// Initialize K8s API metrics to zero for known resource types
	for _, resource := range []string{
		"csidriver",
		"deployment",
		"namespace",
		"node",
		"pod",
		"runtimeclass",
		"secret",
		"serviceaccount",
		"icmsrequest",
		"storageclass",
		"storagerequests",
	} {
		m.K8sAPISuccessTotal.WithLabelValues(m.WithDefaultLabelValues(resource)...)
		m.K8sAPIFailureTotal.WithLabelValues(m.WithDefaultLabelValues(resource)...)
	}

	// instance type metric setup
	m.InstanceTypeCapacity = promFactory.NewGaugeVec(prometheus.GaugeOpts{
		Name: InstanceTypeCapacityMetricName,
		Help: "Count of instances that could be deployed on schedulable node resources by instance type",
	}, withDefaultLabels(InstanceTypeLabel))
	m.InstanceTypeAllocatable = promFactory.NewGaugeVec(prometheus.GaugeOpts{
		Name: InstanceTypeAllocatableMetricName,
		Help: "Count of instances that can be deployed on available schedulable node resources by instance type",
	}, withDefaultLabels(InstanceTypeLabel))
	m.InstanceTypeUnschedulable = promFactory.NewGaugeVec(prometheus.GaugeOpts{
		Name: InstanceTypeUnschedulableMetricName,
		Help: "Count of instances that could be deployed on unschedulable node resources by instance type",
	}, withDefaultLabels(InstanceTypeLabel))
	m.GPUNodeUnclassifiedCount = promFactory.NewGaugeVec(prometheus.GaugeOpts{
		Name: GPUNodeUnclassifiedCountMetricName,
		Help: "Count of nodes with GPU resources present but no recognized instance-type label, " +
			"indicating a GPU discovery or labeling gap, bucketed by GPU family and machine type",
	}, withDefaultLabels(GPUFamilyLabel, GPUMachineLabel))
	m.GPUNodeTotalCount = promFactory.NewGaugeVec(prometheus.GaugeOpts{
		Name: GPUNodeTotalCountMetricName,
		Help: "Total count of GPU-bearing nodes seen, classified and unclassified, bucketed by GPU family and machine type",
	}, withDefaultLabels(GPUFamilyLabel, GPUMachineLabel))

	// Storage controller metrics (uses storage labels for backwards compatibility)
	m.StorageRequestDuration = promFactory.NewSummaryVec(prometheus.SummaryOpts{
		Name: StorageRequestDurationMetricName,
		Help: "Duration of NVCA Storage Controller request to terminal state. " +
			"storage_request_phase is the terminal phase of the request.",
		MaxAge:     1 * time.Hour,
		Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
	}, withStorageLabels(StorageRequestPhaseLabel))
	// Pre-register all known storage phases so the series appear on the first Prometheus scrape
	// even before any StorageRequest reaches a terminal state.
	for _, phase := range []string{"Pending", "InitRunning", "Creating", "Ready", "Failed", "RuntimeError"} {
		m.StorageRequestDuration.WithLabelValues(m.withStorageLabelValues(phase)...)
	}

	// MiniService controller metrics (use getMiniServiceLabels for backwards compatibility)
	m.MiniServiceReconcilePhaseTotal = promFactory.NewCounterVec(prometheus.CounterOpts{
		Name: MiniServiceReconcilePhaseTotalMetricName,
		Help: "Total number of reconciliations per MiniService phase",
	}, getMiniServiceLabels(NCAIDLabel, MiniServicePhaseLabel))

	m.MiniServicePhaseTransitionsTotal = promFactory.NewCounterVec(prometheus.CounterOpts{
		Name: MiniServicePhaseTransitionsTotalMetricName,
		Help: "Total number of MiniService phase transitions",
	}, getMiniServiceLabels(NCAIDLabel, FromPhaseLabel, ToPhaseLabel))

	m.MiniServiceFailuresTotal = promFactory.NewCounterVec(prometheus.CounterOpts{
		Name: MiniServiceFailuresTotalMetricName,
		Help: "Total number of MiniService failures by reason",
	}, getMiniServiceLabels(NCAIDLabel, FailureReasonLabel))

	m.MiniServiceReadyStatus = promFactory.NewGaugeVec(prometheus.GaugeOpts{
		Name: MiniServiceReadyStatusMetricName,
		Help: "Success or failure of a MiniService function or task. " +
			"task_id will be set when the MiniService controls a task instance, " +
			"and function_id/function_version_id will be set when it controls a function",
	}, getMiniServiceLabels(NCAIDLabel, FunctionIDLabel, FunctionVersionIDLabel, TaskIDLabel))

	m.MiniServiceReValRequestTotal = promFactory.NewCounterVec(prometheus.CounterOpts{
		Name: MiniServiceReValRequestTotalMetricName,
		Help: "Total number of ReVal service requests per HTTP code",
	}, getMiniServiceLabels(NCAIDLabel, EndpointLabel, HTTPCodeLabel))

	m.MiniServiceEventErrorTotal = promFactory.NewCounterVec(prometheus.CounterOpts{
		Name: MiniServiceEventErrorTotalMetricName,
		Help: "Total error count of miniservice controller events",
	}, withDefaultLabels(EventKindLabel))

	// Initialize MiniService event error metrics to zero for known event kinds
	for _, eventKind := range []string{
		"EVENT_TRANSLATE_FUNCTION_ERROR",
		"EVENT_TRANSLATE_TASK_ERROR",
	} {
		m.MiniServiceEventErrorTotal.WithLabelValues(m.WithDefaultLabelValues(eventKind)...)
	}

	// NVLink-optimized metrics
	m.NVLinkAllocationCreatedCount = promFactory.NewGaugeVec(prometheus.GaugeOpts{
		Name: NVLinkAllocationCreatedTotalMetricName,
		Help: "Current number of DRA resources created for NVLink-optimized workloads",
	}, withDefaultLabels())
	m.NVLinkAllocationSuccessCount = promFactory.NewGaugeVec(prometheus.GaugeOpts{
		Name: NVLinkAllocationSuccessTotalMetricName,
		Help: "Current number of DRA resources succeeded for NVLink-optimized workloads (status == \"Ready\")",
	}, withDefaultLabels())
	m.NVLinkAllocationFailureCount = promFactory.NewGaugeVec(prometheus.GaugeOpts{
		Name: NVLinkAllocationFailureTotalMetricName,
		Help: "Current number of DRA resources failed for NVLink-optimized workloads",
	}, withDefaultLabels())

	// GC metrics (use getGCLabels for backwards compatibility)
	m.OrphanedResourceCleanupTotal = promFactory.NewCounterVec(prometheus.CounterOpts{
		Name: OrphanedResourceCleanupTotalMetricName,
		Help: "Total number of orphaned resources cleaned up by GC cleaners",
	}, getGCLabels(ResourceTypeLabel, StatusLabel))

	m.GCCleanerRunTotal = promFactory.NewCounterVec(prometheus.CounterOpts{
		Name: GCCleanerRunTotalMetricName,
		Help: "Total number of GC cleaner runs",
	}, getGCLabels(CleanerNameLabel, StatusLabel))

	// Initialize GC metrics to zero for known resource types, cleaners, and statuses
	gcResourceTypes := []string{
		"namespace",
		"persistent_volume",
		"persistent_volume_claim",
		"pod",
		"storage_class",
		"storage_request",
	}
	gcCleanerNames := []string{
		"NamespaceCleaner",
		"PersistentVolumeCleaner",
		"PodCleaner",
		"StorageClassCleaner",
	}
	gcStatuses := []string{"success", "failure"}

	for _, resourceType := range gcResourceTypes {
		for _, status := range gcStatuses {
			m.OrphanedResourceCleanupTotal.WithLabelValues(m.withGCLabelValues(resourceType, status)...)
		}
	}
	for _, cleanerName := range gcCleanerNames {
		for _, status := range gcStatuses {
			m.GCCleanerRunTotal.WithLabelValues(m.withGCLabelValues(cleanerName, status)...)
		}
	}

	// Model cache metrics (uses storage labels for backwards compatibility)
	m.ModelCacheResultTotal = promFactory.NewCounterVec(prometheus.CounterOpts{
		Name: ModelCacheResultTotalMetricName,
		Help: "Total number of model cache operations by result. " +
			"result is 'success' or 'failure', failure_reason is set only on failure.",
	}, withStorageLabels(ResultLabel, FailureReasonLabel))

	// Initialize model cache metrics to zero for known result/failure_reason combinations
	m.ModelCacheResultTotal.WithLabelValues(m.withStorageLabelValues(modelcachetypes.ResultSuccess, "")...)
	for _, reason := range modelcachetypes.AllFailureReasons {
		m.ModelCacheResultTotal.WithLabelValues(m.withStorageLabelValues(modelcachetypes.ResultFailure, reason)...)
	}

	// Cluster attribute metrics
	m.KataRuntimeIsolationEnabled = promFactory.NewGaugeVec(prometheus.GaugeOpts{
		Name: KataRuntimeIsolationEnabledMetricName,
		Help: "Whether Kata runtime isolation is enabled on this cluster (1=enabled, 0=disabled)",
	}, withDefaultLabels())
	// Initialize to 0 (disabled) so it appears on first Prometheus scrape
	m.KataRuntimeIsolationEnabled.WithLabelValues(m.WithDefaultLabelValues()...).Set(0)

	m.MaintenanceModeState = promFactory.NewGaugeVec(prometheus.GaugeOpts{
		Name: MaintenanceModeStateMetricName,
		Help: "Whether NVCA is in a maintenance mode on this cluster. The series whose " +
			"mode label matches the active mode is 1 and every other mode series is 0 " +
			"(one-hot encoding). mode is one of None, CordonOnly, CordonAndDrain.",
	}, withDefaultLabels(MaintenanceModeLabel))
	// Initialize every mode to 0 so all series appear on the first Prometheus scrape.
	for _, mode := range types.AllMaintenanceModes {
		m.MaintenanceModeState.WithLabelValues(m.WithDefaultLabelValues(mode.String())...).Set(0)
	}

	// Cluster-validator metrics. Fixed-cardinality vectors (Ready,
	// LastRun*, CheckStatus per known check) are initialized to zero so
	// they appear on the first Prometheus scrape — same pattern as
	// KataRuntimeIsolationEnabled above. Dynamic-cardinality vectors
	// (EndpointReachable, NetpolPairPassed) only appear after the first
	// reconcile fires from a real ConfigMap update.
	m.ClusterValidatorReady = promFactory.NewGaugeVec(prometheus.GaugeOpts{
		Name: ClusterValidatorReadyMetricName,
		Help: "Cluster-validator overall verdict (1=NVCF-Ready, 0=NVCF-Not-Ready). " +
			"Driven by the most recent run's verdictReady field.",
	}, withDefaultLabels())
	m.ClusterValidatorCheckStatus = promFactory.NewGaugeVec(prometheus.GaugeOpts{
		Name: ClusterValidatorCheckStatusMetricName,
		Help: "Per-check status from the latest cluster-validator run " +
			"(1=passed, 0=failed/skipped). The set of check names is fixed; see CheckKey* constants.",
	}, withDefaultLabels(ClusterValidatorCheckLabel))
	m.ClusterValidatorEndpointReachable = promFactory.NewGaugeVec(prometheus.GaugeOpts{
		Name: ClusterValidatorEndpointReachableMetricName,
		Help: "Reachability of each user-configured endpoint from the latest cluster-validator run " +
			"(1=reachable, 0=not reachable). Label value `endpoint` is the user-supplied name; " +
			"`critical=true` means the endpoint failure flips the cluster verdict. " +
			"Series are pruned when an endpoint is removed from config.",
	}, withDefaultLabels(ClusterValidatorEndpointLabel, ClusterValidatorCriticalLabel))
	m.ClusterValidatorNetpolPairPassed = promFactory.NewGaugeVec(prometheus.GaugeOpts{
		Name: ClusterValidatorNetpolPairPassedMetricName,
		Help: "Directional NetworkPolicy coverage from the latest cluster-validator run " +
			"(1=allowed, 0=blocked). Label `pair` is the user-supplied name; `direction` is " +
			"`a_to_b` or `b_to_a`; `policy_side` is `egress` (the source namespace's egress) or " +
			"`ingress` (the destination namespace's ingress); `critical=true` means the pair " +
			"failure flips the cluster verdict. Overall pair coverage is `min by (pair) (...)`. " +
			"Series are pruned when a pair is removed from config.",
	}, withDefaultLabels(
		ClusterValidatorNetpolPairLabel,
		ClusterValidatorCriticalLabel,
		ClusterValidatorDirectionLabel,
		ClusterValidatorPolicySideLabel,
	))
	m.ClusterValidatorLastRunTimestamp = promFactory.NewGaugeVec(prometheus.GaugeOpts{
		Name: ClusterValidatorLastRunTimestampMetricName,
		Help: "Unix timestamp (seconds) of the latest cluster-validator run. " +
			"Operators alert on staleness via `time() - <metric> > <threshold>`.",
	}, withDefaultLabels())
	m.ClusterValidatorLastRunDuration = promFactory.NewGaugeVec(prometheus.GaugeOpts{
		Name: ClusterValidatorLastRunDurationMetricName,
		Help: "Wall-clock duration (seconds) of the latest cluster-validator run.",
	}, withDefaultLabels())

	// Initialize the fixed-cardinality cluster-validator gauges to 0 so they
	// appear on the first Prometheus scrape — same "absent metric" pattern as
	// KataRuntimeIsolationEnabled. Each run updates these series in place.
	m.emitClusterValidatorBaseline()

	// Workload result metric (uses default labels)
	m.WorkloadResultTotal = promFactory.NewCounterVec(prometheus.CounterOpts{
		Name: WorkloadResultTotalMetricName,
		Help: "Total workload results by type, status, and failure category. " +
			"Tracks terminal states of pods and miniservices.",
	}, withDefaultLabels(WorkloadTypeLabel, WorkloadKindLabel, WorkloadStatusLabel, FailureCategoryLabel))

	// Initialize workload result metrics to zero for all known combinations (expose series so they appear on first scrape).
	for _, wt := range workloadtypes.AllWorkloadTypes {
		for _, wk := range workloadtypes.AllWorkloadKinds {
			// Success case (empty failure_category)
			m.WorkloadResultTotal.WithLabelValues(
				m.WithDefaultLabelValues(
					string(wt),
					string(wk),
					string(workloadtypes.WorkloadStatusSuccess),
					"",
				)...).Add(0)
			// Failure cases for each failure category
			for _, fc := range workloadtypes.AllFailureCategories {
				m.WorkloadResultTotal.WithLabelValues(
					m.WithDefaultLabelValues(
						string(wt),
						string(wk),
						string(workloadtypes.WorkloadStatusFailure),
						string(fc),
					)...).Add(0)
			}
		}
	}

	// Upstream (ICMS) request metrics, pre-initialized to zero for all operations and statuses.
	// http_status is "200" on success, or the numeric HTTP status code string on failure (e.g. "401", "503").
	m.UpstreamRequestTotal = promFactory.NewCounterVec(prometheus.CounterOpts{
		Name: UpstreamRequestTotalMetricName,
		Help: "Total number of upstream (ICMS) requests by operation and status. " +
			"operation is one of: UpstreamOperationHeartbeat, UpstreamOperationRegister, UpstreamOperationCredentials. " +
			"status is 'success' or 'failure'. " +
			"http_status is the HTTP status code string (e.g. '200', '401', '503').",
	}, withDefaultLabels(OperationLabel, StatusLabel, HTTPStatusLabel))
	// Pre-initialize to zero for all known operations and success status
	for _, op := range AllUpstreamOperations {
		m.UpstreamRequestTotal.WithLabelValues(m.WithDefaultLabelValues(op, "success", "200")...)
		m.UpstreamRequestTotal.WithLabelValues(m.WithDefaultLabelValues(op, "failure", "0")...)
	}

	// Scheduler workload count gauge — tracks the number of active functions and tasks per scheduler.
	// Recomputed from cluster state on each heartbeat so it survives NVCA restarts.
	m.SchedulerWorkloadCount = promFactory.NewGaugeVec(prometheus.GaugeOpts{
		Name: SchedulerWorkloadCountMetricName,
		Help: "Number of active functions and tasks by scheduler and workload kind",
	}, withDefaultLabels(SchedulerNameLabel, WorkloadKindLabel))
	// Pre-initialize all combinations to zero so they appear on the first Prometheus scrape
	for _, scheduler := range []string{SchedulerNameDefault, SchedulerNameKAI} {
		for _, wk := range workloadtypes.AllWorkloadKinds {
			m.SchedulerWorkloadCount.WithLabelValues(m.WithDefaultLabelValues(scheduler, string(wk))...).Set(0)
		}
	}

	// run options on metrics
	for _, opt := range opts {
		opt(m)
	}

	return m
}

func (m *Metrics) WithDefaultLabelValues(additionalLvs ...string) []string {
	// Return a copy of the original slice to avoid any sharing of
	// resources we're going to completely copy the slice by creating
	// a new one
	lblVals := make([]string, len(m.defaultLabelValues)+len(additionalLvs))
	copy(lblVals, m.defaultLabelValues)
	for i := len(m.defaultLabelValues); i < len(m.defaultLabelValues)+len(additionalLvs); i++ {
		lblVals[i] = additionalLvs[i-len(m.defaultLabelValues)]
	}
	return lblVals
}

func (m *Metrics) withStorageLabelValues(additionalLvs ...string) []string {
	// Storage labels exclude NCAID for backwards compatibility
	lblVals := make([]string, len(m.storageLabelValues)+len(additionalLvs))
	copy(lblVals, m.storageLabelValues)
	for i := len(m.storageLabelValues); i < len(m.storageLabelValues)+len(additionalLvs); i++ {
		lblVals[i] = additionalLvs[i-len(m.storageLabelValues)]
	}
	return lblVals
}

func (m *Metrics) withMiniServiceLabelValues(additionalLvs ...string) []string {
	// MiniService labels exclude NCAID from default labels (added per-call)
	lblVals := make([]string, len(m.miniServiceLabelValues)+len(additionalLvs))
	copy(lblVals, m.miniServiceLabelValues)
	for i := len(m.miniServiceLabelValues); i < len(m.miniServiceLabelValues)+len(additionalLvs); i++ {
		lblVals[i] = additionalLvs[i-len(m.miniServiceLabelValues)]
	}
	return lblVals
}

func (m *Metrics) withGCLabelValues(additionalLvs ...string) []string {
	// GC labels exclude NCAID for backwards compatibility
	lblVals := make([]string, len(m.gcLabelValues)+len(additionalLvs))
	copy(lblVals, m.gcLabelValues)
	for i := len(m.gcLabelValues); i < len(m.gcLabelValues)+len(additionalLvs); i++ {
		lblVals[i] = additionalLvs[i-len(m.gcLabelValues)]
	}
	return lblVals
}

func (m *Metrics) GetDefaultLabelPairs() []*promdto.LabelPair {
	defaultLabels := getDefaultLabels()
	lblPairs := make([]*promdto.LabelPair, len(defaultLabels))
	for i := range defaultLabels {
		lblName := defaultLabels[i]
		lblV := m.defaultLabelValues[i]
		lblPairs[i] = &promdto.LabelPair{
			Name:  &lblName,
			Value: &lblV,
		}
	}
	return lblPairs
}

const ctxKey ictx.Key = "metrics"

func WithMetrics(parent context.Context, m *Metrics) context.Context {
	return context.WithValue(parent, ctxKey, m)
}

func WithDefaultMetrics(parent context.Context, ncaID, clusterName, clusterGroup, version string, opts ...DefaultMetricsOption) context.Context {
	return WithMetrics(parent, NewDefaultMetrics(ncaID, clusterName, clusterGroup, version, opts...))
}

func FromContext(ctx context.Context) *Metrics {
	if ctx == nil {
		return nil
	}
	if m, ok := ctx.Value(ctxKey).(*Metrics); ok {
		return m
	}
	return nil
}

func (m *Metrics) SetInstanceTypeMetrics(instanceType string, capacity, allocatable, unschedulable float64) {
	m.InstanceTypeCapacity.WithLabelValues(m.WithDefaultLabelValues(instanceType)...).Set(capacity)
	m.InstanceTypeAllocatable.WithLabelValues(m.WithDefaultLabelValues(instanceType)...).Set(allocatable)
	m.InstanceTypeUnschedulable.WithLabelValues(m.WithDefaultLabelValues(instanceType)...).Set(unschedulable)
}

// SetUnclassifiedGPUNodeCount sets the count of GPU-bearing nodes, for the given GPU family and
// machine type, that could not be attributed to any known instance type due to a missing or
// unrecognized instance-type label.
func (m *Metrics) SetUnclassifiedGPUNodeCount(gpuFamily, gpuMachine string, count float64) {
	m.GPUNodeUnclassifiedCount.WithLabelValues(m.WithDefaultLabelValues(gpuFamily, gpuMachine)...).Set(count)
}

// SetTotalGPUNodeCount sets the total count of GPU-bearing nodes seen, classified and
// unclassified, for the given GPU family and machine type.
func (m *Metrics) SetTotalGPUNodeCount(gpuFamily, gpuMachine string, count float64) {
	m.GPUNodeTotalCount.WithLabelValues(m.WithDefaultLabelValues(gpuFamily, gpuMachine)...).Set(count)
}

// RecordK8sAPISuccess increments the K8s API success counter
func (m *Metrics) RecordK8sAPISuccess(resource string) {
	if m == nil {
		return
	}
	m.K8sAPISuccessTotal.WithLabelValues(m.WithDefaultLabelValues(resource)...).Inc()
}

// RecordK8sAPIFailure increments the K8s API failure counter
func (m *Metrics) RecordK8sAPIFailure(resource string) {
	if m == nil {
		return
	}
	m.K8sAPIFailureTotal.WithLabelValues(m.WithDefaultLabelValues(resource)...).Inc()
}

// TrackK8sAPICall is a helper function to track K8s API Get calls
// It takes the result of a K8s API Get call and records success or failure metrics
// based on whether the error is nil or not (excluding NotFound errors from failures)
func (m *Metrics) TrackK8sAPICall(resource string, err error) {
	if m == nil {
		return
	}
	if err == nil {
		m.RecordK8sAPISuccess(resource)
	} else if !k8serrors.IsNotFound(err) {
		// Only count as failure if it's not a NotFound error
		m.RecordK8sAPIFailure(resource)
	} else {
		// NotFound errors are still considered "successful" API calls
		// since the API server responded correctly, just the resource doesn't exist
		m.RecordK8sAPISuccess(resource)
	}
}

// RecordQueueMessageDequeued increments the queue message dequeued counter
// This tracks the dequeue rate per queue type and GPU name
func (m *Metrics) RecordQueueMessageDequeued(queueType, gpuName string, count int) {
	if m == nil {
		return
	}
	m.QueueMessageDequeuedTotal.WithLabelValues(m.WithDefaultLabelValues(queueType, gpuName)...).Add(float64(count))
}

// RecordQueueDequeueBatchSize observes the batch size (number of messages) for a dequeue operation
// This tracks the distribution of batch sizes per queue type and GPU name
func (m *Metrics) RecordQueueDequeueBatchSize(queueType, gpuName string, batchSize int) {
	if m == nil {
		return
	}
	m.QueueDequeueBatchSize.WithLabelValues(m.WithDefaultLabelValues(queueType, gpuName)...).Observe(float64(batchSize))
}

// RecordStorageRequestDuration records the duration of a storage request to terminal state
// Uses storage-specific labels (ClusterName, ClusterGroup, Version) for backwards compatibility
func (m *Metrics) RecordStorageRequestDuration(phase string, durationSeconds float64) {
	if m == nil {
		return
	}
	m.StorageRequestDuration.WithLabelValues(m.withStorageLabelValues(phase)...).Observe(durationSeconds)
}

// RecordMiniServiceReconcilePhase increments the reconcile phase counter
func (m *Metrics) RecordMiniServiceReconcilePhase(ncaID, phase string) {
	if m == nil {
		return
	}
	m.MiniServiceReconcilePhaseTotal.WithLabelValues(m.withMiniServiceLabelValues(ncaID, phase)...).Inc()
}

// RecordMiniServicePhaseTransition increments the phase transition counter
func (m *Metrics) RecordMiniServicePhaseTransition(ncaID, fromPhase, toPhase string) {
	if m == nil {
		return
	}
	m.MiniServicePhaseTransitionsTotal.WithLabelValues(m.withMiniServiceLabelValues(ncaID, fromPhase, toPhase)...).Inc()
}

// RecordMiniServiceFailure increments the failure counter with a failure reason
func (m *Metrics) RecordMiniServiceFailure(ncaID, reason string) {
	if m == nil {
		return
	}
	m.MiniServiceFailuresTotal.WithLabelValues(m.withMiniServiceLabelValues(ncaID, reason)...).Inc()
}

// SetMiniServiceReadyStatus sets the ready status gauge for a MiniService.
// Deprecated: use RecordMiniServicePhaseTransition and RecordMiniServiceFailure instead.
func (m *Metrics) SetMiniServiceReadyStatus(ncaID string, value float64) {
	if m == nil {
		return
	}
	m.MiniServiceReadyStatus.WithLabelValues(m.withMiniServiceLabelValues(ncaID, "", "", "")...).Set(value)
}

// RecordMiniServiceReValRequest increments the ReVal request counter
func (m *Metrics) RecordMiniServiceReValRequest(ncaID, endpoint, httpCode string) {
	if m == nil {
		return
	}
	m.MiniServiceReValRequestTotal.WithLabelValues(m.withMiniServiceLabelValues(ncaID, endpoint, httpCode)...).Inc()
}

// RecordMiniServiceEventError increments the miniservice event error counter
func (m *Metrics) RecordMiniServiceEventError(eventKind string) {
	if m == nil {
		return
	}
	m.MiniServiceEventErrorTotal.WithLabelValues(m.WithDefaultLabelValues(eventKind)...).Inc()
}

// RecordOrphanedResourceCleanup increments the orphaned resource cleanup counter
func (m *Metrics) RecordOrphanedResourceCleanup(resourceType, status string) {
	if m == nil {
		return
	}
	m.OrphanedResourceCleanupTotal.WithLabelValues(m.withGCLabelValues(resourceType, status)...).Inc()
}

// RecordGCCleanerRun increments the GC cleaner run counter
func (m *Metrics) RecordGCCleanerRun(cleanerName, status string) {
	if m == nil {
		return
	}
	m.GCCleanerRunTotal.WithLabelValues(m.withGCLabelValues(cleanerName, status)...).Inc()
}

// RecordModelCacheResult records a model cache operation result.
// result should be "success" or "failure".
// failureReason should be empty string for success, or one of the defined failure reasons for failure.
func (m *Metrics) RecordModelCacheResult(result, failureReason string) {
	if m == nil {
		return
	}
	m.ModelCacheResultTotal.WithLabelValues(m.withStorageLabelValues(result, failureReason)...).Inc()
}

// SetKataRuntimeIsolationEnabled sets the Kata runtime isolation gauge.
// Pass true if KataRuntimeIsolation attribute is enabled, false otherwise.
func (m *Metrics) SetKataRuntimeIsolationEnabled(enabled bool) {
	if m == nil {
		return
	}
	val := 0.0
	if enabled {
		val = 1.0
	}
	m.KataRuntimeIsolationEnabled.WithLabelValues(m.WithDefaultLabelValues()...).Set(val)
}

// SetMaintenanceModeState updates the maintenance-mode gauge: the series for
// the given mode is set to 1 and every other known mode is set to 0 (one-hot
// encoding). Safe to call repeatedly; unknown modes leave all series at 0.
func (m *Metrics) SetMaintenanceModeState(mode types.MaintenanceMode) {
	if m == nil || m.MaintenanceModeState == nil {
		return
	}
	for _, mm := range types.AllMaintenanceModes {
		val := 0.0
		if mm == mode {
			val = 1.0
		}
		m.MaintenanceModeState.WithLabelValues(m.WithDefaultLabelValues(mm.String())...).Set(val)
	}
}

// RecordWorkloadStatus records the terminal outcome of a workload.
func (m *Metrics) RecordWorkloadStatus(
	workloadType workloadtypes.WorkloadType,
	workloadKind workloadtypes.WorkloadKind,
	workloadStatus workloadtypes.WorkloadStatus,
	failureCategory workloadtypes.FailureCategory,
) {
	if m == nil {
		return
	}
	m.WorkloadResultTotal.WithLabelValues(
		m.WithDefaultLabelValues(string(workloadType),
			string(workloadKind),
			string(workloadStatus),
			string(failureCategory),
		)...).Inc()
}

// SetSchedulerWorkloadCount sets the gauge for the number of active workloads on a given scheduler.
// Callers should reset all label combinations to zero before setting observed values to avoid stale counts.
func (m *Metrics) SetSchedulerWorkloadCount(schedulerName string, workloadKind workloadtypes.WorkloadKind, count float64) {
	if m == nil {
		return
	}
	m.SchedulerWorkloadCount.WithLabelValues(m.WithDefaultLabelValues(schedulerName, string(workloadKind))...).Set(count)
}

// ActionToWorkloadKind maps an ICMS creation action to a workload kind label value.
func ActionToWorkloadKind(action translatecommon.MessageAction) workloadtypes.WorkloadKind {
	switch action {
	case translatecommon.TaskCreationAction:
		return workloadtypes.WorkloadKindTask
	default:
		return workloadtypes.WorkloadKindFunction
	}
}

// ICMSInstanceStateToFailureCategory maps a ICMSInstanceState to a failure category label value.
func ICMSInstanceStateToFailureCategory(state types.ICMSInstanceState) workloadtypes.FailureCategory {
	switch state {
	case types.ICMSInstanceFailedImagePullIssues:
		return workloadtypes.FailureCategoryImagePull
	case types.ICMSInstanceFailedInitContainerStuck:
		return workloadtypes.FailureCategoryInitStuck
	case types.ICMSInstanceFailedInitContainerRestartLoop:
		return workloadtypes.FailureCategoryInitRestartLoop
	case types.ICMSInstanceFailedContainerRestartLoop:
		return workloadtypes.FailureCategoryContainerRestart
	case types.ICMSInstanceKilledNoCapacity:
		return workloadtypes.FailureCategoryNoCapacity
	case types.ICMSInstanceKilledAdmissionError:
		return workloadtypes.FailureCategoryAdmissionError
	case types.ICMSInstanceSharedStorageFailure:
		return workloadtypes.FailureCategorySharedStorage
	case types.ICMSInstanceInternalPersistentStorageFailure:
		return workloadtypes.FailureCategoryPersistentStorage
	case types.ICMSInstanceDegradedWorker:
		return workloadtypes.FailureCategoryDegradedWorker
	case types.ICMSInstanceFailedNotFound:
		return workloadtypes.FailureCategoryNotFound
	case types.ICMSInstanceTerminatedTerminalError:
		return workloadtypes.FailureCategoryTerminalError
	case types.ICMSInstanceTerminatedDuetoSyncAction:
		return workloadtypes.FailureCategorySyncAction
	case types.ICMSInstanceTerminatedServiceMaintenance:
		return workloadtypes.FailureCategoryServiceMaintenance
	case types.ICMSInstanceTerminatedPreconditionFailure:
		return workloadtypes.FailureCategoryPreconditionFail
	case types.ICMSInstanceFailedCreateContainerError:
		return workloadtypes.FailureConditionCreateContainerError
	case types.ICMSInstanceFailed:
		return workloadtypes.FailureCategoryUnknown
	default:
		return workloadtypes.FailureCategoryNone
	}
}

// RecordUpstreamRequest records a single ICMS request outcome.
//
// operation must be one of the UpstreamOperation* constants:
// UpstreamOperationHeartbeat, UpstreamOperationRegister, UpstreamOperationCredentials.
// On success, call: RecordUpstreamRequest(UpstreamOperationHeartbeat, nil)
// On failure, call: RecordUpstreamRequest(UpstreamOperationHeartbeat, err)  where err may be an nvcaerrors.HTTPStatusError.
//
// The http_status label is set to "200" on success, the numeric string of the HTTP status code
// if the error wraps an nvcaerrors.HTTPStatusError (e.g. "401", "503"), or "0" for non-HTTP errors
// (network failures, context cancellations, etc.).
func (m *Metrics) RecordUpstreamRequest(operation string, err error) {
	if m == nil {
		return
	}
	if err == nil {
		m.UpstreamRequestTotal.WithLabelValues(m.WithDefaultLabelValues(operation, "success", "200")...).Inc()
		return
	}
	httpCode := "0"
	if code := nvcaerrors.GetHTTPStatusCode(err); code != 0 {
		httpCode = fmt.Sprintf("%d", code)
	}
	m.UpstreamRequestTotal.WithLabelValues(m.WithDefaultLabelValues(operation, "failure", httpCode)...).Inc()
}

// -----------------------------------------------------------------------------
// Cluster-validator metric helpers
// -----------------------------------------------------------------------------

// clusterValidatorCheckKeys returns the canonical list of CheckStatus
// label values for init-to-zero. MUST stay in sync with
// internal/clustervalidator/summary.go's AllCheckKeys. Drift is caught
// by TestClusterValidatorCheckKeysSync.
func clusterValidatorCheckKeys() []string {
	return []string{
		"control_plane",
		"worker_nodes_all_ready",
		"webhooks",
		"network_policies_supported",
		"smb_csi",
		"endpoint_reachability",
		"gpu_resources",
		"gpu_operator",
		"configurable_netpol",
		"netpol_enforcement",
	}
}

// ClusterValidatorSummary is the agent-facing view of one cluster-validator
// run. The reconciler in pkg/nvca builds this from the ConfigMap payload
// (via clustervalidator.ParseSummary) and hands it to
// Metrics.SetClusterValidatorSummary.
type ClusterValidatorSummary struct {
	RanAtUnixSec    int64   // epoch seconds; the LastRunTimestamp metric value
	DurationSeconds float64 // the LastRunDuration metric value
	VerdictReady    bool
	Checks          map[string]bool
	Endpoints       map[string]ClusterValidatorEndpoint
	NetpolPairs     map[string]ClusterValidatorNetpolPair
}

// ClusterValidatorEndpoint mirrors clustervalidator.EndpointStatus but
// kept here so the metrics package has no upward dependency on
// clustervalidator.
type ClusterValidatorEndpoint struct {
	Reachable bool
	Critical  bool
}

// ClusterValidatorNetpolPair mirrors clustervalidator.PairStatus. Directions
// is keyed by direction ("a_to_b"/"b_to_a"); each entry yields two metric
// series (egress and ingress policy sides).
type ClusterValidatorNetpolPair struct {
	Passed     bool
	Critical   bool
	Directions map[string]ClusterValidatorNetpolDirection
}

// ClusterValidatorNetpolDirection mirrors clustervalidator.DirectionStatus.
type ClusterValidatorNetpolDirection struct {
	EgressAllowed  bool
	IngressAllowed bool
}

// clusterValidatorEmittedSet records every label tuple the last reconcile
// emitted, so the next update can DeleteLabelValues() series that aren't
// present in the new summary. Bounded cardinality at /metrics is the
// whole point of this struct.
type clusterValidatorEmittedSet struct {
	checks      map[string]struct{}                  // set of check keys
	endpoints   map[string]clusterValidatorRow       // endpoint name → (critical) for prune
	netpolPairs map[string]clusterValidatorNetpolRow // pair name → (critical, emitted sides) for prune
}

type clusterValidatorRow struct{ critical string }

// clusterValidatorNetpolRow records, per pair, the critical flag plus every
// (direction, policy_side) tuple emitted for it, so the next run can prune
// the exact series it created.
type clusterValidatorNetpolRow struct {
	critical string
	sides    []clusterValidatorNetpolSide
}

type clusterValidatorNetpolSide struct{ direction, policySide string }

func newClusterValidatorEmittedSet() *clusterValidatorEmittedSet {
	return &clusterValidatorEmittedSet{
		checks:      make(map[string]struct{}),
		endpoints:   make(map[string]clusterValidatorRow),
		netpolPairs: make(map[string]clusterValidatorNetpolRow),
	}
}

// SetClusterValidatorSummary atomically updates every cluster-validator
// metric from a single run's summary. The protocol:
//  1. Compute the new label tuples we're about to emit.
//  2. For every label tuple from the previous run that is NOT in the
//     new set, DeleteLabelValues() so stale series stop appearing at
//     /metrics. (Prometheus's TSDB retains historical scrapes — the
//     data isn't lost; it just stops being re-emitted.)
//  3. Set the new gauges with the new label values.
//  4. Remember the new tuples so the next reconcile can prune them.
//
// Nil-safe: if m is nil OR the summary is nil, this returns silently.
// Safe for concurrent use: it acquires m.clusterValidatorMu itself, so
// callers must NOT hold the lock when calling it.
func (m *Metrics) SetClusterValidatorSummary(s *ClusterValidatorSummary) {
	if m == nil || s == nil {
		return
	}
	m.clusterValidatorMu.Lock()
	defer m.clusterValidatorMu.Unlock()

	if m.clusterValidatorLastEmitted == nil {
		m.clusterValidatorLastEmitted = newClusterValidatorEmittedSet()
	}
	prior := m.clusterValidatorLastEmitted
	current := newClusterValidatorEmittedSet()

	// Cluster-level gauges carry only the default labels, so each run updates
	// the same series in place — no per-run series churn.
	m.ClusterValidatorReady.WithLabelValues(m.WithDefaultLabelValues()...).Set(boolToFloat(s.VerdictReady))
	m.ClusterValidatorLastRunTimestamp.WithLabelValues(m.WithDefaultLabelValues()...).Set(float64(s.RanAtUnixSec))
	m.ClusterValidatorLastRunDuration.WithLabelValues(m.WithDefaultLabelValues()...).Set(s.DurationSeconds)

	// CheckStatus: prune the prior run's checks, then emit the current set. A
	// conditional check that did not run this time is left absent rather than
	// reporting a stale value.
	for check := range prior.checks {
		m.ClusterValidatorCheckStatus.DeleteLabelValues(m.WithDefaultLabelValues(check)...)
	}
	for check, passed := range s.Checks {
		m.ClusterValidatorCheckStatus.WithLabelValues(m.WithDefaultLabelValues(check)...).Set(boolToFloat(passed))
		current.checks[check] = struct{}{}
	}

	// EndpointReachable: prune the prior endpoints, then emit the current set
	// so an endpoint removed from config stops appearing.
	for name, row := range prior.endpoints {
		m.ClusterValidatorEndpointReachable.DeleteLabelValues(
			m.WithDefaultLabelValues(name, row.critical)...)
	}
	for name, ep := range s.Endpoints {
		crit := strconv.FormatBool(ep.Critical)
		m.ClusterValidatorEndpointReachable.
			WithLabelValues(m.WithDefaultLabelValues(name, crit)...).
			Set(boolToFloat(ep.Reachable))
		current.endpoints[name] = clusterValidatorRow{critical: crit}
	}

	// NetpolPairPassed: directional — up to 4 series per pair
	// (direction × policy_side). Prune the prior run's tuples, then emit and
	// record the current ones so a pair removed from config stops appearing.
	for name, row := range prior.netpolPairs {
		for _, side := range row.sides {
			m.ClusterValidatorNetpolPairPassed.DeleteLabelValues(
				m.WithDefaultLabelValues(name, row.critical, side.direction, side.policySide)...)
		}
	}
	for name, pair := range s.NetpolPairs {
		crit := strconv.FormatBool(pair.Critical)
		sides := make([]clusterValidatorNetpolSide, 0, len(pair.Directions)*2)
		for direction, d := range pair.Directions {
			m.ClusterValidatorNetpolPairPassed.
				WithLabelValues(m.WithDefaultLabelValues(
					name, crit, direction, clusterValidatorPolicySideEgress)...).
				Set(boolToFloat(d.EgressAllowed))
			m.ClusterValidatorNetpolPairPassed.
				WithLabelValues(m.WithDefaultLabelValues(
					name, crit, direction, clusterValidatorPolicySideIngress)...).
				Set(boolToFloat(d.IngressAllowed))
			sides = append(sides,
				clusterValidatorNetpolSide{direction: direction, policySide: clusterValidatorPolicySideEgress},
				clusterValidatorNetpolSide{direction: direction, policySide: clusterValidatorPolicySideIngress})
		}
		current.netpolPairs[name] = clusterValidatorNetpolRow{critical: crit, sides: sides}
	}

	m.clusterValidatorLastEmitted = current
}

// ResetClusterValidatorMetrics drops every emitted cluster-validator
// series and resets the cluster-level gauges to zero. Called by the agent when
// it observes the explicit cluster-validator-metrics-reset ConfigMap — an
// operator-initiated one-shot signal to clear the metrics to baseline.
// Deleting the summary ConfigMap does NOT trigger this: last-known-good is
// preserved on delete.
func (m *Metrics) ResetClusterValidatorMetrics() {
	if m == nil {
		return
	}
	m.clusterValidatorMu.Lock()
	defer m.clusterValidatorMu.Unlock()

	m.pruneClusterValidatorEmitted()

	// Re-emit the init-to-zero baseline so /metrics doesn't go quiet on the
	// cluster-validator gauges.
	m.emitClusterValidatorBaseline()
}

// pruneClusterValidatorEmitted deletes every series recorded in
// clusterValidatorLastEmitted (the prior run, or the init-to-zero
// baseline). Caller must hold clusterValidatorMu.
func (m *Metrics) pruneClusterValidatorEmitted() {
	prior := m.clusterValidatorLastEmitted
	if prior == nil {
		return
	}
	m.ClusterValidatorReady.DeleteLabelValues(m.WithDefaultLabelValues()...)
	m.ClusterValidatorLastRunTimestamp.DeleteLabelValues(m.WithDefaultLabelValues()...)
	m.ClusterValidatorLastRunDuration.DeleteLabelValues(m.WithDefaultLabelValues()...)
	for check := range prior.checks {
		m.ClusterValidatorCheckStatus.DeleteLabelValues(m.WithDefaultLabelValues(check)...)
	}
	for name, row := range prior.endpoints {
		m.ClusterValidatorEndpointReachable.DeleteLabelValues(
			m.WithDefaultLabelValues(name, row.critical)...)
	}
	for name, row := range prior.netpolPairs {
		for _, side := range row.sides {
			m.ClusterValidatorNetpolPairPassed.DeleteLabelValues(
				m.WithDefaultLabelValues(name, row.critical, side.direction, side.policySide)...)
		}
	}
}

// emitClusterValidatorBaseline sets the fixed-cardinality cluster-validator
// gauges to 0 so they appear on the first Prometheus scrape (same "absent
// metric" pattern as the other init-to-zero gauges), and records the emitted
// check keys in clusterValidatorLastEmitted. Each real run updates these series
// in place. Caller must hold clusterValidatorMu (NewDefaultMetrics runs
// single-threaded at construction).
func (m *Metrics) emitClusterValidatorBaseline() {
	m.ClusterValidatorReady.WithLabelValues(m.WithDefaultLabelValues()...).Set(0)
	m.ClusterValidatorLastRunTimestamp.WithLabelValues(m.WithDefaultLabelValues()...).Set(0)
	m.ClusterValidatorLastRunDuration.WithLabelValues(m.WithDefaultLabelValues()...).Set(0)

	baseline := newClusterValidatorEmittedSet()
	for _, check := range clusterValidatorCheckKeys() {
		m.ClusterValidatorCheckStatus.WithLabelValues(m.WithDefaultLabelValues(check)...).Set(0)
		baseline.checks[check] = struct{}{}
	}
	m.clusterValidatorLastEmitted = baseline
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
