// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package nvcaconfig

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/sirupsen/logrus"
	"go.yaml.in/yaml/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

type Config struct {
	Environment Environment       `yaml:",omitempty"`
	Cluster     NVCFClusterConfig `yaml:",omitempty"`
	Agent       AgentConfig       `yaml:",omitempty"`
	Webhook     WebhookConfig     `yaml:",omitempty"`
	Workload    WorkloadConfig    `yaml:",omitempty"`
	Authz       AuthzConfig       `yaml:",omitempty"`
	Tracing     TracingConfig     `yaml:",omitempty"`
}

type Environment string

const (
	EnvironmentStaging    Environment = "stg"
	EnvironmentProduction Environment = "prod"
)

type ResourceRequirements struct {
	Limits   ResourceList           `yaml:",omitempty"`
	Requests ResourceList           `yaml:",omitempty"`
	Claims   []corev1.ResourceClaim `yaml:",omitempty"`
}

const (
	// BYOOLogChunkMaxBodyBytesEnv is the BYOO collector env var that enables log chunking.
	BYOOLogChunkMaxBodyBytesEnv = "BYOO_LOG_CHUNK_MAX_BODY_BYTES"
	// BYOOLogChunkDryRunEnv is the BYOO collector env var that records chunking metrics without mutating logs.
	BYOOLogChunkDryRunEnv = "BYOO_LOG_CHUNK_DRY_RUN"
	// BYOOLogExporterBatchMaxSizeBytesEnv is the BYOO collector env var for exporterhelper byte batch splitting.
	BYOOLogExporterBatchMaxSizeBytesEnv = "BYOO_LOG_EXPORTER_BATCH_MAX_SIZE_BYTES"
	// BYOOMetricSubsetEnabledEnv is the BYOO collector env var that enables the metric subset pipeline.
	BYOOMetricSubsetEnabledEnv = "BYOO_METRIC_SUBSET_ENABLED"
	// BYOOMetricSubsetFilterConfigEnv is the BYOO collector env var for metric subset filter config YAML.
	BYOOMetricSubsetFilterConfigEnv = "BYOO_METRIC_SUBSET_FILTER_CONFIG"
	// BYOOWorkloadMetricsDropLabelsEnv is the BYOO collector env var for workload metrics resource labels to drop.
	BYOOWorkloadMetricsDropLabelsEnv = "BYOO_WORKLOAD_METRICS_DROP_LABELS"

	// DefaultBYOOLogExporterBatchMaxSizeBytes keeps serialized exporter batches near the backend limit.
	DefaultBYOOLogExporterBatchMaxSizeBytes int64 = 1000000
)

type BYOOLogChunkingConfig struct {
	MaxBodyBytes              int64  `yaml:"maxBodyBytes,omitempty"`
	DryRun                    bool   `yaml:"dryRun,omitempty"`
	ExporterBatchMaxSizeBytes *int64 `yaml:"exporterBatchMaxSizeBytes,omitempty"`
}

func (c BYOOLogChunkingConfig) IsZero() bool {
	return c.MaxBodyBytes == 0 && !c.DryRun && c.ExporterBatchMaxSizeBytes == nil
}

func (c BYOOLogChunkingConfig) Complete() BYOOLogChunkingConfig {
	if c.ExporterBatchMaxSizeBytes == nil {
		defaultValue := DefaultBYOOLogExporterBatchMaxSizeBytes
		c.ExporterBatchMaxSizeBytes = &defaultValue
	}
	return c
}

// EnvVars returns BYOO collector env vars for the supplied config.
func (c BYOOLogChunkingConfig) EnvVars() []corev1.EnvVar {
	envs := []corev1.EnvVar{}
	if c.MaxBodyBytes > 0 {
		envs = append(envs, corev1.EnvVar{
			Name:  BYOOLogChunkMaxBodyBytesEnv,
			Value: strconv.FormatInt(c.MaxBodyBytes, 10),
		})
	}
	if c.DryRun {
		envs = append(envs, corev1.EnvVar{
			Name:  BYOOLogChunkDryRunEnv,
			Value: strconv.FormatBool(c.DryRun),
		})
	}
	if c.ExporterBatchMaxSizeBytes != nil && *c.ExporterBatchMaxSizeBytes > 0 {
		envs = append(envs, corev1.EnvVar{
			Name:  BYOOLogExporterBatchMaxSizeBytesEnv,
			Value: strconv.FormatInt(*c.ExporterBatchMaxSizeBytes, 10),
		})
	}
	return envs
}

// BYOOLogChunkingEnvVars returns BYOO collector env vars for the supplied config.
func BYOOLogChunkingEnvVars(config BYOOLogChunkingConfig) []corev1.EnvVar {
	return config.EnvVars()
}

type BYOOMetricSubsetConfig struct {
	Enabled      bool   `yaml:"enabled,omitempty"`
	FilterConfig string `yaml:"filterConfig,omitempty"`
}

func (c BYOOMetricSubsetConfig) IsZero() bool {
	return !c.Enabled && c.FilterConfig == ""
}

// EnvVars returns BYOO collector env vars for the supplied metric subset config.
func (c BYOOMetricSubsetConfig) EnvVars() []corev1.EnvVar {
	envs := []corev1.EnvVar{}
	if c.Enabled {
		envs = append(envs, corev1.EnvVar{
			Name:  BYOOMetricSubsetEnabledEnv,
			Value: strconv.FormatBool(c.Enabled),
		})
	}
	if c.FilterConfig != "" {
		envs = append(envs, corev1.EnvVar{
			Name:  BYOOMetricSubsetFilterConfigEnv,
			Value: c.FilterConfig,
		})
	}
	return envs
}

type BYOOWorkloadMetricsConfig struct {
	DropLabels []string `yaml:"dropLabels,omitempty"`
}

func (c BYOOWorkloadMetricsConfig) IsZero() bool {
	return len(c.DropLabels) == 0
}

func (c BYOOWorkloadMetricsConfig) EnvVars() []corev1.EnvVar {
	if len(c.DropLabels) == 0 {
		return nil
	}
	return []corev1.EnvVar{{
		Name:  BYOOWorkloadMetricsDropLabelsEnv,
		Value: strings.Join(c.DropLabels, ","),
	}}
}

func (r *ResourceRequirements) ToK8sResourceRequirements() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Limits:   corev1.ResourceList(r.Limits),
		Requests: corev1.ResourceList(r.Requests),
		Claims:   r.Claims,
	}
}

type ResourceList corev1.ResourceList

var (
	_ yaml.Marshaler           = (ResourceList)(nil)
	_ mapstructure.Unmarshaler = (*ResourceList)(nil)
)

func (l ResourceList) MarshalYAML() (any, error) {
	if l == nil {
		return nil, nil
	}
	m := make(map[string]string, len(l))
	for rn, rq := range l {
		m[rn.String()] = rq.String()
	}
	return m, nil
}

func (l *ResourceList) UnmarshalMapstructure(v any) (err error) {
	if l == nil {
		return nil
	}
	in, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("unexpected resource list type: %T", v)
	}
	*l = make(ResourceList, len(in))
	for k, rqv := range in {
		rqs, ok := rqv.(string)
		if !ok {
			return fmt.Errorf("expected string type for resource %q, got %T", k, rqv)
		}
		if (*l)[corev1.ResourceName(k)], err = resource.ParseQuantity(rqs); err != nil {
			return fmt.Errorf("decode resource %q: %w", k, err)
		}
	}
	return nil
}

func (c Config) Complete() Config {
	if c.Environment == "" {
		c.Environment = EnvironmentProduction
	}
	c.Agent = c.Agent.Complete(c.Environment)
	c.Workload = c.Workload.Complete()
	c.Authz = c.Authz.Complete()
	return c
}

type NVCFClusterConfig struct {
	Name          string   `yaml:",omitempty"`
	ID            string   `yaml:",omitempty"`
	GroupName     string   `yaml:",omitempty"`
	GroupID       string   `yaml:",omitempty"`
	NCAID         string   `yaml:",omitempty"`
	Region        string   `yaml:",omitempty"`
	Attributes    []string `yaml:",omitempty"`
	CloudProvider string   `yaml:",omitempty"`

	// ValidationPolicy defines Helm ReVal validation policy data passed to ReVal when rendering a Helm chart.
	ValidationPolicy *ValidationPolicyConfig `yaml:",omitempty"`
}

// ValidationPolicyConfig defines the Helm ReVal validation policy for Helm chart workloads
// deployed on the cluster.
type ValidationPolicyConfig struct {
	// Name of the policy. This maps to values enumerated by ReVal.
	// Currently "Default" and "Unrestricted" are supported.
	Name string `json:"name"`
	// AllowedExtraKubernetesTypes are a list of Kubernetes group-version-kind + resource sets
	// that the cluster has resources defined for, and allows deploying objects of that type.
	AllowedExtraKubernetesTypes []AllowedExtraKubernetesTypeConfig `json:"allowedExtraKubernetesTypes,omitempty"`
}

// AllowedExtraKubernetesTypeConfig is a Kubernetes group-version-kind + resource set
// that the cluster has resources defined for, and allows deploying objects of that type.
// Typically these map to a CRD installed in the cluster, managed by a controller.
type AllowedExtraKubernetesTypeConfig struct {
	// Group of resource.
	// https://kubernetes.io/docs/reference/using-api/#api-groups
	Group string `json:"group"`
	// Version of resource.
	// https://kubernetes.io/docs/reference/using-api/#api-versioning
	Version string `json:"version"`
	// Kind of resource.
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#types-kinds
	Kind string `json:"kind"`
	// Resource name, used in API path.
	// https://kubernetes.io/docs/reference/using-api/api-concepts/#standard-api-terminology
	Resource string `json:"resource"`
}

// Validate fields of a ValidationPolicyConfig, if not nil.
func (c *ValidationPolicyConfig) Validate() (errs []error) {
	if c == nil {
		return nil
	}
	if c.Name == "" {
		errs = append(errs, fmt.Errorf("name must be set"))
	}
	for i, kt := range c.AllowedExtraKubernetesTypes {
		if kt.Group == "" {
			errs = append(errs, fmt.Errorf("allowed extra kubernetes type %d group must be set", i))
		}
		if kt.Version == "" {
			errs = append(errs, fmt.Errorf("allowed extra kubernetes type %d version must be set", i))
		}
		if kt.Kind == "" {
			errs = append(errs, fmt.Errorf("allowed extra kubernetes type %d kind must be set", i))
		}
		if kt.Resource == "" {
			errs = append(errs, fmt.Errorf("allowed extra kubernetes type %d resource must be set", i))
		}
	}
	return errs
}

type AgentConfig struct {
	AgentTimeConfig `mapstructure:",squash" yaml:",inline"`

	LogLevel string `yaml:",omitempty"`

	FeatureFlags []string `yaml:",omitempty"`

	SharedStorage             SharedStorageConfig             `yaml:",omitempty"`
	InternalPersistentStorage InternalPersistentStorageConfig `yaml:",omitempty"`

	ICMSURL                string            `yaml:",omitempty"`
	ICMSHostHeaderOverride string            `yaml:"icmsHostHeaderOverride,omitempty"`
	KubeconfigPath         string            `yaml:",omitempty"`
	SvcAddress             string            `yaml:",omitempty"`
	AdminAddr              string            `yaml:",omitempty"`
	DebugAddr              string            `yaml:",omitempty"`
	SystemNamespace        string            `yaml:",omitempty"`
	RequestsNamespace      string            `yaml:",omitempty"`
	NamespaceLabels        map[string]string `yaml:",omitempty"`
	ComputeBackend         string            `yaml:",omitempty"`
	StaticGPUCapacity      uint64            `yaml:",omitempty"`
	// MinHealthcheckRefreshWait forces the NVCA internal healthchecker
	// to wait at least this long between refresh calls,
	// in case the healthchecker is too chatty in specific instances.
	MinHealthcheckRefreshWait time.Duration `yaml:",omitempty"`

	// MaintenanceMode indicates the operational mode of NVCA
	MaintenanceMode MaintenanceMode `yaml:",omitempty"`

	// HelmRepository restriction
	HelmRepositoryPrefix string `yaml:",omitempty"`

	// ReVal service config
	HelmReValServiceURL                string `yaml:",omitempty"`
	HelmReValServiceHostHeaderOverride string `yaml:"helmReValServiceHostHeaderOverride,omitempty"`
	// Helm ReVal OAuth endpoints are selected from HelmReValServiceURL.
	HelmReValStageOAuthTokenURL             string `yaml:",omitempty"`
	HelmReValStageOAuthPublicKeysetEndpoint string `yaml:",omitempty"`
	HelmReValProdOAuthTokenURL              string `yaml:",omitempty"`
	HelmReValProdOAuthPublicKeysetEndpoint  string `yaml:",omitempty"`

	// NATS service config
	NATSURL          string `yaml:"NATSURL,omitempty"`
	NATSHostOverride string `yaml:"NATSHostOverride,omitempty"`

	// CSIVolumeMountOptions for PVC provisioning
	CSIVolumeMountOptions []string `yaml:",omitempty"`

	// Function Deployment Stages service config
	FunctionDeploymentStagesServiceURL string `yaml:",omitempty"`
	// Function Deployment Stages OAuth endpoints are selected from FunctionDeploymentStagesServiceURL.
	FunctionDeploymentStagesStageOAuthTokenURL             string `yaml:",omitempty"`
	FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint string `yaml:",omitempty"`
	FunctionDeploymentStagesProdOAuthTokenURL              string `yaml:",omitempty"`
	FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint  string `yaml:",omitempty"`

	// NVCA Operater version
	OperatorVersion string `yaml:",omitempty"`
	// Kubernetes version override
	KubernetesVersionOverride string `yaml:",omitempty"`

	// ImageCredentialHelperImage is the image tag for "nvcf-image-credential-helper",
	// for third party registry cred updates.
	//
	// See the image-credential-helper service for related image handling.
	ImageCredentialHelperImage string `yaml:",omitempty"`

	// Skip self-destruct even if ICMS sends SELF_DESTRUCT
	SkipSelfDestruct bool `yaml:",omitempty"`
	// Force self-destruct mode for testing
	ForceSelfDestruct bool `yaml:",omitempty"`

	// Secret Mirror
	SecretMirrorSourceNamespace string `yaml:",omitempty"`
	SecretMirrorLabelSelector   string `yaml:",omitempty"`
	// Tolerations are applied to the NVCA agent pod.
	Tolerations []corev1.Toleration `yaml:",omitempty"`

	// UtilsResources contains resources required by the utils container.
	// UtilsResources is subtracted from instance type resources
	// when the agent calculates them for registration.
	// Quantities must be strings.
	UtilsResources ResourceList `yaml:",omitempty"`

	// AdditionalResourceOverhead contains the collective resource overhead from
	// cluster reserved capacity like DaemonSet containers.
	// AdditionalResourceOverhead is subtracted from instance type resources
	// when the agent calculates them for registration.
	// Quantities must be strings.
	AdditionalResourceOverhead ResourceList `yaml:",omitempty"`

	// BYOOResources contains resources required by the BYOO (Bring Your Own Observability)
	// OTel collector container. BYOOResources is subtracted from instance type resources
	// when the agent calculates them for registration.
	// Quantities must be strings.
	BYOOResources ResourceRequirements `yaml:",omitempty"`

	// BYOOFluentBitResources contains resources required by the BYOO (Bring Your Own Observability)
	// FluentBit container. BYOOFluentBitResources is subtracted from instance type resources
	// when the agent calculates them for registration.
	// Quantities must be strings.
	BYOOFluentBitResources ResourceRequirements `yaml:",omitempty"`

	// BYOOLogChunking contains BYOO OTel collector log chunking and exporter batch settings.
	BYOOLogChunking BYOOLogChunkingConfig `yaml:",omitempty"`

	// BYOOMetricSubset contains BYOO OTel collector metric subset pipeline settings.
	BYOOMetricSubset BYOOMetricSubsetConfig `yaml:"byooMetricSubset,omitempty"`

	// BYOOWorkloadMetrics contains settings for the generated workload metrics pipeline.
	BYOOWorkloadMetrics BYOOWorkloadMetricsConfig `yaml:"byooWorkloadMetrics,omitempty"`
}

func (t AgentConfig) Complete(env Environment) AgentConfig {
	if t.LogLevel == "" {
		t.LogLevel = logrus.InfoLevel.String()
	}

	t.AgentTimeConfig = t.AgentTimeConfig.Complete()
	t.BYOOLogChunking = t.BYOOLogChunking.Complete()
	return t
}

// BYOOOTelCollectorEnvVars returns env vars that must be set only on the BYOO OTel collector container.
func (t AgentConfig) BYOOOTelCollectorEnvVars() []corev1.EnvVar {
	envs := t.BYOOLogChunking.EnvVars()
	envs = append(envs, t.BYOOMetricSubset.EnvVars()...)
	return append(envs, t.BYOOWorkloadMetrics.EnvVars()...)
}

const (
	defaultCredRenewInterval              = 45 * time.Minute
	defaultHeartbeatInterval              = 5 * time.Minute
	defaultSyncQueueInterval              = 3 * time.Second
	defaultSyncRequestStatusInterval      = 15 * time.Second
	defaultPeriodicInstanceStatusInterval = 5 * time.Minute
	defaultICMSRequestAckInterval         = 15 * time.Minute
	defaultSyncAcknowledgeRequestInterval = 5 * time.Second
	defaultICMSRequestAckRetryTimeout     = 5 * time.Minute
)

type AgentTimeConfig struct {
	CredRenewInterval              time.Duration `yaml:",omitempty"`
	HeartbeatInterval              time.Duration `yaml:",omitempty"`
	SyncQueueInterval              time.Duration `yaml:",omitempty"`
	SyncRequestStatusInterval      time.Duration `yaml:",omitempty"`
	SyncAcknowledgeRequestInterval time.Duration `yaml:",omitempty"`
	PeriodicInstanceStatusInterval time.Duration `yaml:",omitempty"`
	ICMSRequestAckInterval         time.Duration `yaml:",omitempty"`
	ICMSRequestAckRetryTimeout     time.Duration `yaml:",omitempty"`
}

func (t AgentTimeConfig) Complete() AgentTimeConfig {
	if t.CredRenewInterval == 0 {
		t.CredRenewInterval = defaultCredRenewInterval
	}
	if t.HeartbeatInterval == 0 {
		t.HeartbeatInterval = defaultHeartbeatInterval
	}
	if t.SyncQueueInterval == 0 {
		t.SyncQueueInterval = defaultSyncQueueInterval
	}
	if t.SyncRequestStatusInterval == 0 {
		t.SyncRequestStatusInterval = defaultSyncRequestStatusInterval
	}
	if t.SyncAcknowledgeRequestInterval == 0 {
		t.SyncAcknowledgeRequestInterval = defaultSyncAcknowledgeRequestInterval
	}
	if t.PeriodicInstanceStatusInterval == 0 {
		t.PeriodicInstanceStatusInterval = defaultPeriodicInstanceStatusInterval
	}

	if t.ICMSRequestAckInterval == 0 {
		t.ICMSRequestAckInterval = defaultICMSRequestAckInterval
	}
	if t.ICMSRequestAckRetryTimeout == 0 {
		t.ICMSRequestAckRetryTimeout = defaultICMSRequestAckRetryTimeout
	}

	panicOnUnsetFields(t)

	return t
}

type SharedStorageConfig struct {
	Server   SharedStorageServerConfig   `yaml:",omitempty"`
	TaskData SharedStorageTaskDataConfig `yaml:",omitempty"`
}

type SharedStorageServerConfig struct {
	// Image for the shared storage pod's SMB container.
	Image string `yaml:",omitempty"`
	// ContainerResources are the requests/limits set for the shared storage pod's SMB container
	ContainerResources ResourceRequirements `yaml:",omitempty"`
}

type SharedStorageTaskDataConfig struct {
	// StorageClassName the storage class to use for the task data
	// if none specified ephemeral storage will be used
	StorageClassName *string `yaml:",omitempty"`
	// PVMountOptions represents the mount options for the PV
	PVMountOptions []string `yaml:",omitempty"`
	// StorageCapacity of the provisioned volume.
	StorageCapacity resource.Quantity `yaml:",omitempty"`
}

type InternalPersistentStorageConfig struct {
	StorageClassName string `yaml:",omitempty"`
	// The desired hard limit for storage in the IPS PVC.
	// More info: https://kubernetes.io/docs/concepts/policy/resource-quotas/
	HardResourceQuota ResourceList `yaml:",omitempty"`
}

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
func (m MaintenanceMode) String() string { return string(m) }

type WebhookConfig struct {
	SvcAddress      string            `yaml:",omitempty"`
	TLSKeyFile      string            `yaml:",omitempty"`
	TLSCertFile     string            `yaml:",omitempty"`
	TLSSecretName   string            `yaml:",omitempty"`
	DCGMAnnotations map[string]string `yaml:",omitempty"`
}

type WorkloadConfig struct {
	WorkloadTimeConfig `mapstructure:",squash" yaml:",inline"`

	// Tolerations are applied to translated workload pod specs and related infra pods.
	Tolerations []corev1.Toleration `yaml:",omitempty"`

	// FunctionEnvOverrides is a map of environment variable overrides
	// applied to function workloads before translation.
	// Example keys: INIT_CONTAINER, UTILS_CONTAINER, OTEL_CONTAINER, ESS_AGENT_CONTAINER
	FunctionEnvOverrides map[string]string `yaml:",omitempty"`

	// TaskEnvOverrides is a map of environment variable overrides
	// applied to task workloads before translation.
	// Example keys: INIT_CONTAINER, UTILS_CONTAINER, ESS_AGENT_CONTAINER
	TaskEnvOverrides map[string]string `yaml:",omitempty"`

	// Stargate configuration
	DefaultStargateAddress string `yaml:",omitempty"`
	StargateQUICInsecure   bool   `yaml:",omitempty"`

	// TransportTLS configures trust material used by workload transport clients.
	TransportTLS *TransportTLSConfig `yaml:",omitempty"`
}

// TrustMode controls how the NVCA agent's LLM worker pins the QUIC TLS
// certificate served by Stargate. "system" uses the host's standard CA
// pool; "bundle" requires the operator-supplied PEM and fingerprint and
// pins exclusively against that root.
type TrustMode string

const (
	TrustModeSystem TrustMode = "system"
	TrustModeBundle TrustMode = "bundle"
)

// TransportTLSConfig is the operator-supplied trust material the NVCA agent
// uses to verify Stargate's openbao-issued QUIC TLS certificate on
// self-managed clusters.
type TransportTLSConfig struct {
	TrustMode                TrustMode `yaml:"trustMode"`
	TrustBundleConfigMapName string    `yaml:"trustBundleConfigMapName"`
	TrustBundleKey           string    `yaml:"trustBundleKey"`
	TrustBundleFingerprint   string    `yaml:"trustBundleFingerprint"`
	TrustBundlePEM           string    `yaml:"trustBundlePem"`
	InstallerImage           string    `yaml:"installerImage"`
}

func (t WorkloadConfig) Complete() WorkloadConfig {
	t.WorkloadTimeConfig = t.WorkloadTimeConfig.Complete()
	return t
}

const (
	defaultMaxRunningTimeout                         = 180 * time.Minute
	defaultModelCacheIdlePeriod                      = 1 * time.Hour
	defaultModelCacheIdleCleanupPeriod               = 5 * time.Minute
	defaultModelCacheROPVCBindTimeGracePeriod        = 2 * time.Minute
	defaultModelCacheVolumeDetachmentTimeout         = 5 * time.Minute
	defaultWorkerDegradationTimeout                  = 30 * time.Minute
	defaultWorkerStartupTimeout                      = 2 * time.Hour
	defaultPodLaunchThresholdSecondsOnFailedRestarts = 10 * time.Minute
	defaultPodLaunchThresholdMinutesOnInitFailure    = 2 * time.Hour
	defaultPodScheduledThreshold                     = 10 * time.Minute
	defaultInitCacheJobFailureThreshold              = 2 * defaultMaxRunningTimeout
	defaultMaxImagePullErrorThreshold                = 1 * time.Minute
	defaultNamespaceStuckTimeout                     = 5 * time.Minute
	defaultFailingObjectsBackoffTimeout              = 90 * time.Second
	defaultFailingObjectsBackoffRequeueInterval      = 30 * time.Second
)

type WorkloadTimeConfig struct {
	// MaxRunningTimeout is the max duration an operation (ex. Helm install, cache Job run) can run for before being considered failed.
	MaxRunningTimeout time.Duration `yaml:",omitempty"`
	// ModelCacheIdlePeriod is the max duration a model cache PV can exist while not in use.
	ModelCacheIdlePeriod time.Duration `yaml:",omitempty"`
	// ModelCacheIdleCleanupPeriod is the period of the model cache cleanup runner.
	ModelCacheIdleCleanupPeriod time.Duration `yaml:",omitempty"`
	// ModelCacheROPVCBindTimeGracePeriod is the max duration an RO PVC can be unbound during cache init.
	ModelCacheROPVCBindTimeGracePeriod time.Duration `yaml:",omitempty"`
	// ModelCacheVolumeDetachmentTimeout is the max duration to wait for PV detachment on cache cleanup.
	ModelCacheVolumeDetachmentTimeout time.Duration `yaml:",omitempty"`
	// defaultWorkerDegradationTimeout is the duration after which a Pod is considered degraded if its status indicates so.
	WorkerDegradationTimeout time.Duration `yaml:",omitempty"`
	// WorkerStartupTimeout is the duration after which a non-".status.phase=(Ready|Succeeded)" Pod is marked failed.
	WorkerStartupTimeout time.Duration `yaml:",omitempty"`
	// PodLaunchThresholdSecondsOnFailedRestarts is the duration after which a Pod with failed and restarting containers is marked failed.
	PodLaunchThresholdSecondsOnFailedRestarts time.Duration `yaml:",omitempty"`
	// PodLaunchThresholdMinutesOnInitFailure is the duration after which a Pod with failed init containers is marked failed.
	PodLaunchThresholdMinutesOnInitFailure time.Duration `yaml:",omitempty"`
	// PodLaunchThresholdMinutesOnInitFailure is the duration after which an un-scheduled Pod is marked failed.
	PodScheduledThreshold time.Duration `yaml:",omitempty"`
	// PodLaunchThresholdMinutesOnInitFailure is the duration after which an init cache Job with unsuccessful Pods is marked failed.
	InitCacheJobFailureThreshold time.Duration `yaml:",omitempty"`
	// PodLaunchThresholdMinutesOnInitFailure is the duration after which a Pod with container pull issues is marked failed.
	MaxImagePullErrorThreshold time.Duration `yaml:",omitempty"`
	// NamespaceStuckTimeout is the duration after which a terminating Namespace is considered stuck.
	NamespaceStuckTimeout time.Duration `yaml:",omitempty"`
	// FailingObjectsBackoffTimeout is the duration to retry transient events (FailedMount, FailedAttachVolume) before marking as failed.
	FailingObjectsBackoffTimeout time.Duration `yaml:",omitempty"`
	// FailingObjectsBackoffRequeueInterval is the interval to requeue reconciliation when objects are failing within the backoff period.
	FailingObjectsBackoffRequeueInterval time.Duration `yaml:",omitempty"`
}

// Complete sets defaults on WorkloadTimeConfig. It panics if some field is not set or defaulted.
func (t WorkloadTimeConfig) Complete() WorkloadTimeConfig {
	if t.MaxRunningTimeout == 0 {
		t.MaxRunningTimeout = defaultMaxRunningTimeout
	}
	if t.ModelCacheIdlePeriod == 0 {
		t.ModelCacheIdlePeriod = defaultModelCacheIdlePeriod
	}
	if t.ModelCacheIdleCleanupPeriod == 0 {
		t.ModelCacheIdleCleanupPeriod = defaultModelCacheIdleCleanupPeriod
	}
	if t.ModelCacheROPVCBindTimeGracePeriod == 0 {
		t.ModelCacheROPVCBindTimeGracePeriod = defaultModelCacheROPVCBindTimeGracePeriod
	}
	if t.ModelCacheVolumeDetachmentTimeout == 0 {
		t.ModelCacheVolumeDetachmentTimeout = defaultModelCacheVolumeDetachmentTimeout
	}
	if t.WorkerDegradationTimeout == 0 {
		t.WorkerDegradationTimeout = defaultWorkerDegradationTimeout
	}
	if t.WorkerStartupTimeout == 0 {
		t.WorkerStartupTimeout = defaultWorkerStartupTimeout
	}
	if t.PodLaunchThresholdSecondsOnFailedRestarts == 0 {
		t.PodLaunchThresholdSecondsOnFailedRestarts = defaultPodLaunchThresholdSecondsOnFailedRestarts
	}
	if t.PodLaunchThresholdMinutesOnInitFailure == 0 {
		t.PodLaunchThresholdMinutesOnInitFailure = defaultPodLaunchThresholdMinutesOnInitFailure
	}
	if t.PodScheduledThreshold == 0 {
		t.PodScheduledThreshold = defaultPodScheduledThreshold
	}
	if t.InitCacheJobFailureThreshold == 0 {
		t.InitCacheJobFailureThreshold = defaultInitCacheJobFailureThreshold
	}
	if t.MaxImagePullErrorThreshold == 0 {
		t.MaxImagePullErrorThreshold = defaultMaxImagePullErrorThreshold
	}
	if t.NamespaceStuckTimeout == 0 {
		t.NamespaceStuckTimeout = defaultNamespaceStuckTimeout
	}
	if t.FailingObjectsBackoffTimeout == 0 {
		t.FailingObjectsBackoffTimeout = defaultFailingObjectsBackoffTimeout
	}
	if t.FailingObjectsBackoffRequeueInterval == 0 {
		t.FailingObjectsBackoffRequeueInterval = defaultFailingObjectsBackoffRequeueInterval
	}

	panicOnUnsetFields(t)

	return t
}

func panicOnUnsetFields(t any) {
	// Detect unset fields, which could cause downstream errors.
	var zeroFields []string
	v := reflect.ValueOf(t)
	for i := range v.Type().NumField() {
		if v.Field(i).IsZero() {
			zeroFields = append(zeroFields, v.Type().Field(i).Name)
		}
	}
	if len(zeroFields) != 0 {
		panic(fmt.Sprintf("code bug: %T fields not set: %+q", t, zeroFields))
	}
}

type ClusterIssuedTokenSource string

const (
	// ClusterIssuedTokenSourcePSAT configures the agent to use the projected service account token authz path.
	ClusterIssuedTokenSourcePSAT ClusterIssuedTokenSource = "psat"

	ClusterIssuedTokenSourceNone ClusterIssuedTokenSource = "none"
)

type AuthzConfig struct {
	// ClientID must come from a secret and set in-memory.
	// It will not be stored when the config is written.
	ClientID string `mapstructure:"-" yaml:"-"`
	// ClientSecretKey must come from a secret and set in-memory.
	// It will not be stored when the config is written.
	ClientSecretKey            string `mapstructure:"-" yaml:"-"`
	ClientSecretsEnvFile       string `yaml:",omitempty"`
	TokenFetchFailureThreshold uint64 `yaml:",omitempty"`
	TokenURL                   string `yaml:",omitempty"`
	TokenScope                 string `yaml:",omitempty"`
	PublicKeysetEndpoint       string `yaml:",omitempty"`
	NGCServiceAPIKeyFile       string `yaml:",omitempty"`
	// NGCServiceAPIKey must come from a secret and set in-memory.
	// It will not be stored when the config is written.
	NGCServiceAPIKey string `mapstructure:"-" yaml:"-"`
	// SelfManagedVaultSecretsJSONPath is the path to the secrets.json file created by Vault
	// for self-managed environments.
	SelfManagedVaultSecretsJSONPath string `yaml:",omitempty"`

	// Cluster-issued JWT authz configuration

	// ClusterIssuedTokenSource is the source of the cluster-issued JWT token.
	ClusterIssuedTokenSource ClusterIssuedTokenSource `yaml:",omitempty"`
	// ClusterIssuedTokenFilePath is the path to the cluster-issued token file.
	ClusterIssuedTokenFilePath string `yaml:",omitempty"`
}

func (t AuthzConfig) Complete() AuthzConfig {
	if t.TokenFetchFailureThreshold == 0 {
		t.TokenFetchFailureThreshold = 3
	}
	if t.ClusterIssuedTokenSource == "" {
		t.ClusterIssuedTokenSource = ClusterIssuedTokenSourcePSAT
	}
	return t
}

// OTELExporter is a enum for which type of OTEL exporter we are using
type OTELExporter string

const (
	// NoExporter is the default and specifies no exporter should be setup
	NoExporter OTELExporter = ""
	// LightstepExporter is the exporter enum for lighstep
	LightstepExporter = "lightstep"
)

// TracingConfig configures tracing
type TracingConfig struct {
	Exporter             OTELExporter `yaml:",omitempty"`
	LightstepServiceName string       `yaml:",omitempty"`
	// LightstepAccessToken must come from a secret and set in-memory.
	// It will not be stored when the config is written.
	LightstepAccessToken string `mapstructure:"-" yaml:"-"`
}
