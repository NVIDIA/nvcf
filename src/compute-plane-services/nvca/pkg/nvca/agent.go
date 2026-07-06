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

package nvca

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	nvcffndsclient "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/fnds/client"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/tracing"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/version"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"github.com/sourcegraph/conc/pool"
	"go.opentelemetry.io/otel"
	otelattr "go.opentelemetry.io/otel/attribute"
	otelpropagation "go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/workqueue"

	nvcaauth "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/auth"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	nvcalogging "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/logging"
	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	mscontroller "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/miniservice"
	nvcaotel "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/otel"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcainternaltranslate "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/translate"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned/scheme"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce/hostisolation"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce/kaischeduler"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce/kata"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/fnds"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/health"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue"
	natsqueue "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue/nats"
	queuesqs "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue/sqs"
	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	UnusedResourceCleanupDuration = 30 * time.Minute

	ICMSRequestAckMaxGoroutines                   = 20
	ICMSInstanceRequestStatusUpdatesMaxGoroutines = 20
)

// Controller tick types.
const (
	// #nosec G101
	EventTickRenewICMSCredentials              string = "TICK_RENEW_ICMS_CREDENTIALS"
	EventTickUpdateHeartbeat                   string = "TICK_UPDATE_HEARTBEAT"
	EventTickSyncSQSQueue                      string = "TICK_SYNC_SQS_QUEUE"
	EventTickSyncICMSRequestStatus             string = "TICK_SYNC_ICMS_REQUEST_STATUS"
	EventTickSyncICMSRequests                  string = "TICK_SYNC_ICMS_REQUESTS"
	EventTickAcknowledgeRequest                string = "TICK_ACKNOWLEDGE_REQUEST"
	EventTickSyncCleanupUnusedResources        string = "TICK_SYNC_CLEANUP_UNUSED_RESOURCES"
	EventTickSyncPeriodicInstanceStatusUpdates string = "TICK_SYNC_PERIODIC_INSTANCE_STATUS_UPDATES"
	EventTickUpdateICMSRegistration            string = "TICK_SYNC_UPDATE_ICMS_REGISTRATION"
	EventTickSyncNetworkPolicies               string = "TICK_SYNC_NETWORK_POLICIES"
	// metric events
	EventModelCachingFailed     string = "EVENT_MODEL_CACHING_FAILED"
	EventModelCachingSuccess    string = "EVENT_MODEL_CACHING_SUCCESS"
	EventPVCModelCachingError   string = "EVENT_PVC_MODEL_CACHING_ERROR"
	EventTranslateFunctionError string = "EVENT_TRANSLATE_FUNCTION_ERROR"
	EventTranslateTaskError     string = "EVENT_TRANSLATE_TASK_ERROR"
	EventWorkloadPaused         string = "EVENT_WORKLOAD_PAUSED"
)

// getAgentEvents are all the possibly event names for the agent
func getAgentEvents() []string {
	// with no constants to avoid this being accidentally modified
	// we put this in a function
	events := []string{
		EventTickRenewICMSCredentials,
		EventTickUpdateHeartbeat,
		EventTickSyncSQSQueue,
		EventTickSyncICMSRequestStatus,
		EventTickSyncICMSRequests,
		EventTickAcknowledgeRequest,
		EventTickSyncCleanupUnusedResources,
		EventTickUpdateICMSRegistration,
		EventTickSyncPeriodicInstanceStatusUpdates,
		EventTickSyncNetworkPolicies,
	}

	return events
}

func getNVCAMetricEvents() []string {
	metricEvents := []string{
		EventModelCachingFailed,
		EventModelCachingSuccess,
		EventPVCModelCachingError,
		EventTranslateFunctionError,
		EventTranslateTaskError,
		EventWorkloadPaused,
	}
	return metricEvents
}

var SkippedEventsInSelfDestructMode = map[string]bool{
	EventTickRenewICMSCredentials:              true,
	EventTickUpdateHeartbeat:                   true,
	EventTickSyncSQSQueue:                      true,
	EventTickSyncICMSRequestStatus:             true,
	EventTickAcknowledgeRequest:                true,
	EventTickUpdateICMSRegistration:            true,
	EventTickSyncPeriodicInstanceStatusUpdates: true,
}

type BackendType string

const (
	BackendTypeAce BackendType = "bcp"
	BackendTypeK8s BackendType = "k8s"
)

type AgentOptions struct {
	nvcaconfig.Config
	nvcaauth.TokenFetcherOptions

	FeatureFlagFetcher featureflag.Fetcher

	NCAId string

	ClusterName        string
	ClusterID          string
	ClusterDescription string
	ClusterGroupName   string
	ClusterGroupID     string
	ClusterRegion      string
	ClusterAttributes  featureflag.Attributes

	SharedStorageServerImage string

	// ICMSURL is the ICMS service URL.
	ICMSURL                        string
	ICMSHostHeaderOverride         string
	CloudProvider                  string
	KubeConfigPath                 string
	NVCASvcAddress                 string
	NVCAAdminAddr                  string
	NVCADebugAddr                  string
	SystemNamespace                string
	RequestsNamespace              string
	NamespaceLabels                labels.Set
	ComputeBackend                 string
	CredRenewInterval              time.Duration
	HeartbeatInterval              time.Duration
	SyncQueueInterval              time.Duration
	SyncRequestStatusInterval      time.Duration
	SyncAcknowledgeRequestInterval time.Duration
	PeriodicInstanceStatusInterval time.Duration
	// ICMSRequestACKInterval is the interval for ICMS request acknowledgements.
	ICMSRequestACKInterval time.Duration
	GPUCapacity            uint64
	K8sVersion             string
	// ImageCredentialHelperImage is the image tag for "nvcf-image-credential-helper",
	// for third party registry cred updates.
	ImageCredentialHelperImage string

	// K8sTimeConfig configures intervals, timeouts, thresholds for various K8s occurances.
	K8sTimeConfig *k8sutil.TimeConfig

	// MinHealthcheckRefreshWait forces the NVCA internal healthchecker
	// to wait at least this long between refresh calls,
	// in case the healthchecker is too chatty in specific instances.
	MinHealthcheckRefreshWait time.Duration

	// Feature flags
	LogPostingEnabled                   bool
	CachingSupportEnabled               bool
	NVMeshEncryptionEnabled             bool
	PeriodicInstanceStatusUpdateEnabled bool
	HelmRBACEnforcementEnabled          bool
	HelmResourceConstraintsEnabled      bool
	DynamicGPUDiscoveryEnabled          bool
	MultipleGPUTypesAllowed             bool
	UniformInstanceLabelsEnabled        bool
	AutoPurgeDegradedWorkers            bool
	ClusterTargetingEnabled             bool
	HelmSharedStorageEnabled            bool
	GXCacheEnabled                      bool
	LowLatencyStreamingEnabled          bool
	PVCRebindEnabled                    bool
	MultiNodeWorkloadsEnabled           bool

	// MaintenanceMode indicates the operational mode of NVCA
	MaintenanceMode types.MaintenanceMode

	// Self-destruct control flags
	SkipSelfDestruct  bool // Skip self-destruct even if ICMS sends SELF_DESTRUCT
	ForceSelfDestruct bool // Force self-destruct mode for testing

	// HelmRepository restriction
	HelmRepositoryPrefix string

	// QueueManager options
	EndpointURL      string
	NATSURL          string
	NATSHostOverride string

	// ReVal service config
	HelmReValServiceURL                     string
	HelmReValServiceHostHeaderOverride      string
	HelmReValStageOAuthTokenURL             string
	HelmReValStageOAuthPublicKeysetEndpoint string
	HelmReValProdOAuthTokenURL              string
	HelmReValProdOAuthPublicKeysetEndpoint  string

	// CSIVolumeMountOptions for PVC provisioning
	CSIVolumeMountOptions []string

	// Function Deployment Stages service config
	FunctionDeploymentStagesServiceURL                     string
	FunctionDeploymentStagesStageOAuthTokenURL             string
	FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint string
	FunctionDeploymentStagesProdOAuthTokenURL              string
	FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint  string

	// ICMSRequestAckRetryTimeout is the timeout for retrying ICMS request acknowledgements.
	ICMSRequestAckRetryTimeout time.Duration

	// NVCA Operator version
	NVCAOperatorVersion string

	// NVCA Agent version
	NVCAAgentVersion string

	// Secret Mirror
	SecretMirrorSourceNamespace string
	SecretMirrorLabelSelector   string

	// Environment variable overrides for workloads
	// These are maps of env var key-value pairs applied before translation
	FunctionEnvOverrides map[string]string
	TaskEnvOverrides     map[string]string

	// GPU Monitor configuration (used when GracefulNoGPU feature flag is enabled)
	// GPUPollInterval is the interval between GPU availability checks
	GPUPollInterval time.Duration
	// GPUDebounceTime is the time a GPU state must be stable before triggering a change
	GPUDebounceTime time.Duration

	// MetricsRegisterer allows tests to use a custom prometheus registry
	MetricsRegisterer prometheus.Registerer

	StartControllerManager func(context.Context, *kubeclients.KubeClients) error
}

// EffectiveICMSURL returns the ICMS service URL.
func (o *AgentOptions) EffectiveICMSURL() string {
	return o.ICMSURL
}

func withServiceOAuthEndpoints(
	opts nvcaauth.TokenFetcherOptions,
	serviceURL, stageTokenURL, stageJWKSURL, prodTokenURL, prodJWKSURL string,
) nvcaauth.TokenFetcherOptions {
	tokenURL, jwksURL := prodTokenURL, prodJWKSURL
	if strings.Contains(serviceURL, ".stg.") || strings.Contains(serviceURL, "://stg.") {
		tokenURL, jwksURL = stageTokenURL, stageJWKSURL
	}
	if tokenURL != "" {
		opts.TokenURL = tokenURL
	}
	if jwksURL != "" {
		opts.OAuthPublicKeysetEndpoint = jwksURL
	}
	return opts
}

func (o *AgentOptions) helmReValTokenFetcherOptions() nvcaauth.TokenFetcherOptions {
	return withServiceOAuthEndpoints(
		o.TokenFetcherOptions,
		o.HelmReValServiceURL,
		o.HelmReValStageOAuthTokenURL,
		o.HelmReValStageOAuthPublicKeysetEndpoint,
		o.HelmReValProdOAuthTokenURL,
		o.HelmReValProdOAuthPublicKeysetEndpoint,
	)
}

func (o *AgentOptions) functionDeploymentStagesTokenFetcherOptions() nvcaauth.TokenFetcherOptions {
	return withServiceOAuthEndpoints(
		o.TokenFetcherOptions,
		o.FunctionDeploymentStagesServiceURL,
		o.FunctionDeploymentStagesStageOAuthTokenURL,
		o.FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint,
		o.FunctionDeploymentStagesProdOAuthTokenURL,
		o.FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint,
	)
}

// EffectiveICMSRequestACKInterval returns the configured ICMS request acknowledgement interval.
func (o *AgentOptions) EffectiveICMSRequestACKInterval() time.Duration {
	return o.ICMSRequestACKInterval
}

// EffectiveICMSRequestAckRetryTimeout returns the configured ICMS request acknowledgement retry timeout.
func (o *AgentOptions) EffectiveICMSRequestAckRetryTimeout() time.Duration {
	return o.ICMSRequestAckRetryTimeout
}

// ICMSClientInterface defines the interface for ICMS client operations
type ICMSClientInterface interface {
	PutHealthStatus(ctx context.Context, req *types.HealthStatusRequest) (*types.HealthStatusResponse, error)
	Register(ctx context.Context, req *types.ICMSRegistrationRequest) (*types.ICMSRegistrationResponse, error)
	PostInstanceStatusUpdate(ctx context.Context, requestID, instanceID string, payload *types.ICMSInstanceStatusUpdateRequest) error
	GetICMSServerInstanceStatuses(ctx context.Context) (types.ICMSInstanceStatusResponse, error)
	PutRequestAcknowledgement(ctx context.Context, icmsReqID, messageBatchID string, instanceCount uint64, srTraceCtxCfg nvcav2beta1.ICMSRequestTraceContextConfig) error
	GetCreds(ctx context.Context) (*types.ICMSCredentialResponse, error)
	Endpoint() string
}

type Agent struct {
	*AgentOptions

	metricsName    string
	newKubeClients func(ctx context.Context, path string) (*kubeclients.KubeClients, error)

	icmsClient         ICMSClientInterface
	fndsClient         fnds.Client
	queueManager       *QueueManager
	natsSecretsFetcher nvcaauth.NATSSecretsFetcher
	backendk8scache    *BackendK8sCache
	backendHealthCache health.StatusCache

	// gpuMonitor monitors GPU availability and controls queue processing
	// when GracefulNoGPU feature flag is enabled.
	gpuMonitor *GPUMonitor

	startControllerManager func(context.Context, *kubeclients.KubeClients) error

	// Get liveness/readiness checkers for HTTP endpoints in Start().
	livenessCheckGetter  health.LazyLivenessCheckGetter
	readinessCheckGetter health.LazyReadinessCheckGetter

	syncICMSRegistration func(context.Context) error

	// OTel tracer initialized at agent startup
	tracer oteltrace.Tracer

	// Metrics for the agent
	metrics *nvcametrics.Metrics

	// event queue processing
	resourceEventWorkerQueues map[string]workqueue.Interface

	ackThreadPool        *pool.Pool
	instStatusThreadPool *pool.Pool

	// selfDestruct tracks if self-destruct has been triggered by ICMS.
	// When true, the agent stops all ICMS communication.
	selfDestruct *atomic.Bool
}

func (o *AgentOptions) sanitizedString() string {
	var sanitized AgentOptions
	sanitized.NCAId = o.NCAId
	sanitized.ClusterName = o.ClusterName
	sanitized.ClusterID = o.ClusterID
	sanitized.ClusterGroupID = o.ClusterGroupID
	sanitized.ClusterDescription = o.ClusterDescription
	sanitized.KubeConfigPath = o.KubeConfigPath
	sanitized.ICMSURL = o.EffectiveICMSURL()
	sanitized.ICMSHostHeaderOverride = o.ICMSHostHeaderOverride
	sanitized.CloudProvider = o.CloudProvider
	sanitized.SystemNamespace = o.SystemNamespace
	sanitized.RequestsNamespace = o.RequestsNamespace
	sanitized.NamespaceLabels = o.NamespaceLabels
	sanitized.NVCASvcAddress = o.NVCASvcAddress
	sanitized.NVCAAdminAddr = o.NVCAAdminAddr
	sanitized.NVCADebugAddr = o.NVCADebugAddr
	sanitized.CredRenewInterval = o.CredRenewInterval
	sanitized.HeartbeatInterval = o.HeartbeatInterval
	sanitized.SyncQueueInterval = o.SyncQueueInterval
	sanitized.SyncRequestStatusInterval = o.SyncRequestStatusInterval
	sanitized.MinHealthcheckRefreshWait = o.MinHealthcheckRefreshWait
	sanitized.TokenURL = o.TokenURL
	sanitized.OAuthClientID = o.OAuthClientID
	// Do not want to print this, but can at least state it is non-empty
	if o.OAuthClientSecretKey != "" {
		sanitized.OAuthClientSecretKey = "<REDACTED>"
	}
	// do not want to print this, but can at least show it is non-empty
	if o.NGCServiceAPIKey != "" {
		sanitized.NGCServiceAPIKey = "<REDACTED>"
	}
	sanitized.OAuthTokenScope = o.OAuthTokenScope
	sanitized.OAuthClientSecretsEnvFile = o.OAuthClientSecretsEnvFile
	sanitized.PSATTokenFilePath = o.PSATTokenFilePath
	sanitized.EndpointURL = o.EndpointURL
	sanitized.NATSURL = o.NATSURL
	sanitized.NATSHostOverride = o.NATSHostOverride
	sanitized.K8sVersion = o.K8sVersion
	sanitized.GPUCapacity = o.GPUCapacity
	sanitized.ComputeBackend = o.ComputeBackend
	sanitized.LogPostingEnabled = o.LogPostingEnabled
	sanitized.CachingSupportEnabled = o.CachingSupportEnabled
	sanitized.NVMeshEncryptionEnabled = o.NVMeshEncryptionEnabled
	sanitized.HelmRBACEnforcementEnabled = o.HelmRBACEnforcementEnabled
	sanitized.DynamicGPUDiscoveryEnabled = o.DynamicGPUDiscoveryEnabled
	sanitized.UniformInstanceLabelsEnabled = o.UniformInstanceLabelsEnabled
	sanitized.MultipleGPUTypesAllowed = o.MultipleGPUTypesAllowed
	sanitized.Config.Tracing.Exporter = o.Config.Tracing.Exporter
	sanitized.Config.Tracing.LightstepServiceName = o.Config.Tracing.LightstepServiceName
	sanitized.K8sTimeConfig = o.K8sTimeConfig
	sanitized.AutoPurgeDegradedWorkers = o.AutoPurgeDegradedWorkers
	sanitized.ClusterTargetingEnabled = o.ClusterTargetingEnabled
	sanitized.HelmSharedStorageEnabled = o.HelmSharedStorageEnabled
	sanitized.GXCacheEnabled = o.GXCacheEnabled
	sanitized.LowLatencyStreamingEnabled = o.LowLatencyStreamingEnabled
	sanitized.HelmRepositoryPrefix = o.HelmRepositoryPrefix
	sanitized.HelmReValServiceURL = o.HelmReValServiceURL
	sanitized.HelmReValServiceHostHeaderOverride = o.HelmReValServiceHostHeaderOverride
	sanitized.HelmReValStageOAuthTokenURL = o.HelmReValStageOAuthTokenURL
	sanitized.HelmReValStageOAuthPublicKeysetEndpoint = o.HelmReValStageOAuthPublicKeysetEndpoint
	sanitized.HelmReValProdOAuthTokenURL = o.HelmReValProdOAuthTokenURL
	sanitized.HelmReValProdOAuthPublicKeysetEndpoint = o.HelmReValProdOAuthPublicKeysetEndpoint
	sanitized.FunctionDeploymentStagesServiceURL = o.FunctionDeploymentStagesServiceURL
	sanitized.FunctionDeploymentStagesStageOAuthTokenURL = o.FunctionDeploymentStagesStageOAuthTokenURL
	sanitized.FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint = o.FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint
	sanitized.FunctionDeploymentStagesProdOAuthTokenURL = o.FunctionDeploymentStagesProdOAuthTokenURL
	sanitized.FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint = o.FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint
	sanitized.PVCRebindEnabled = o.PVCRebindEnabled
	sanitized.MultiNodeWorkloadsEnabled = o.MultiNodeWorkloadsEnabled
	sanitized.CSIVolumeMountOptions = o.CSIVolumeMountOptions
	sanitized.ICMSRequestACKInterval = o.ICMSRequestACKInterval
	sanitized.ICMSRequestAckRetryTimeout = o.ICMSRequestAckRetryTimeout
	sanitized.NVCAOperatorVersion = o.NVCAOperatorVersion
	sanitized.NVCAAgentVersion = o.NVCAAgentVersion
	sanitized.ImageCredentialHelperImage = o.ImageCredentialHelperImage
	sanitized.SecretMirrorSourceNamespace = o.SecretMirrorSourceNamespace
	sanitized.SecretMirrorLabelSelector = o.SecretMirrorLabelSelector
	return fmt.Sprintf("%#v", sanitized)
}

func (o *AgentOptions) GetOTelAttributes() []otelattr.KeyValue {
	return []otelattr.KeyValue{
		otelattr.String(nvcaotel.NCAIDAttributeKey, o.NCAId),
		otelattr.String(nvcaotel.ClusterNameAttributeKey, o.ClusterName),
		otelattr.String(nvcaotel.ClusterGroupAttributeKey, o.ClusterGroupName),
		otelattr.String(nvcaotel.VersionAttributeKey, o.NVCAAgentVersion),
	}
}

func NewAgent(ctx context.Context, opts *AgentOptions) (*Agent, error) {
	log := core.GetLogger(ctx)
	if opts.EffectiveICMSURL() == "" {
		return nil, errors.New("ICMSURL required for Agent")
	}

	// Create the token fetcher
	tokenFetcher, tokenFetcherHealthCheck, err := newTokenFetcher(ctx, "icms", opts.TokenFetcherOptions)
	if err != nil {
		log.WithError(err).Error("Failed to initialize token fetcher")
		return nil, err
	}

	// Set default OTel attributes
	nvcaotel.SetDefaultAttributes(opts.GetOTelAttributes())

	// Initialize the default metrics
	metricsOpts := []nvcametrics.DefaultMetricsOption{
		nvcametrics.WithEventErrorTotalDefaultEvents(append(getAgentEvents(), getNVCAMetricEvents()...)),
		nvcametrics.WithContainerCrashAndRestartTotalDefaultContainerNames(GetDefaultWorkloadContainerNamesToWatch()),
		nvcametrics.WithKataRuntimeIsolationEnabled(opts.FeatureFlagFetcher.IsAttributeEnabled(featureflag.AttrKataRuntimeIsolation)),
	}
	if opts.MetricsRegisterer != nil {
		metricsOpts = append(metricsOpts, nvcametrics.WithRegisterer(opts.MetricsRegisterer))
	}
	metrics := nvcametrics.NewDefaultMetrics(
		opts.NCAId, opts.ClusterName, opts.ClusterGroupName, opts.NVCAAgentVersion,
		metricsOpts...)

	a := &Agent{
		AgentOptions: opts,
		metricsName:  "nvca",
		metrics:      metrics,
		tracer:       nvcaotel.NewTracer(),
		// Lazy readiness check getter to initialize throughout Start().
		readinessCheckGetter: health.NewLazyReadinessCheckGetter(),
		// Lazy liveness check getter to initialize throughout Start().
		livenessCheckGetter: health.NewLazyLivenessCheckGetter(tokenFetcherHealthCheck),
	}

	a.newKubeClients = defaultNewKubeClients
	a.icmsClient = NewICMSClientWithHostHeaderOverride(ctx, opts.ClusterID, opts.EffectiveICMSURL(), opts.ICMSHostHeaderOverride, tokenFetcher, a.tracer)
	a.instStatusThreadPool = pool.New().WithMaxGoroutines(ICMSInstanceRequestStatusUpdatesMaxGoroutines)
	a.ackThreadPool = pool.New().WithMaxGoroutines(ICMSRequestAckMaxGoroutines)
	// initialize selfDestruct to false
	a.selfDestruct = &atomic.Bool{}
	a.selfDestruct.Store(false)

	// Initialize the Function Deployment Stages client
	if opts.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.UseFunctionDeploymentStages) {
		core.GetLogger(ctx).Infof("Initializing Function Deployment Stages client with URL %s", opts.FunctionDeploymentStagesServiceURL)
		fndsTokenFetcher, fndsTokenFetcherHealthCheck, err := fnds.NewTokenFetcher(ctx,
			opts.functionDeploymentStagesTokenFetcherOptions(),
			opts.FunctionDeploymentStagesServiceURL)
		if err != nil {
			log.WithError(err).Error("Failed to initialize Function Deployment Stages token fetcher")
			return nil, err
		}

		a.fndsClient = nvcffndsclient.NewFndsClient(opts.FunctionDeploymentStagesServiceURL, opts.NCAId, fndsTokenFetcher,
			nvcffndsclient.WithHTTPClient(fnds.NewHTTPClient()),
		)
		a.livenessCheckGetter.AddChecker(fndsTokenFetcherHealthCheck)
	}

	// Initialize NATS secrets fetcher if SelfHosted feature flag is enabled
	// unless PSAT identity is in use — in that case the NATS client reads
	// the projected SA token directly (see natsqueue.NewClientWithTokenFile
	// below) and no vault-backed secrets fetcher is required.
	if opts.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.SelfHosted) && opts.TokenFetcherOptions.PSATTokenFilePath == "" {
		log.Info("SelfHosted feature flag enabled, initializing NATS secrets fetcher")
		// Check existing token fetcher to see if it is a nats secrets fetcher
		// if so reuse it, otherwise initialize a new one
		if natsSecretsFetcher, ok := tokenFetcher.(nvcaauth.NATSSecretsFetcher); ok {
			a.natsSecretsFetcher = natsSecretsFetcher
		} else {
			// Initialize NATS secrets fetcher
			secretsPath := opts.TokenFetcherOptions.SelfHostedVaultSecretsJSONPath
			if secretsPath == "" {
				secretsPath = nvcaauth.DefaultSelfHostedVaultSecretsJSONPath
			}
			secretsFetcher, _, fetchErr := nvcaauth.NewSelfManagedSecretsFetcher(ctx, "nats", secretsPath)
			if fetchErr != nil {
				log.WithError(fetchErr).Error("Failed to initialize NATS secrets fetcher")
				return nil, fmt.Errorf("initialize NATS secrets fetcher: %w", fetchErr)
			}
			a.natsSecretsFetcher = secretsFetcher
		}
	}

	// Initialize the Helm ReVal client tokenFetcher
	tf, revalTokenFetcherHealthCheck, err := mscontroller.NewTokenFetcher(ctx, opts.helmReValTokenFetcherOptions(), opts.HelmReValServiceURL)
	if err != nil {
		log.WithError(err).Error("Failed to initialize Helm ReVal token fetcher")
		return nil, err
	}
	a.livenessCheckGetter.AddChecker(revalTokenFetcherHealthCheck)
	revalTokenFetcher := tf

	if a.startControllerManager = opts.StartControllerManager; a.startControllerManager == nil {
		a.startControllerManager = func(ctx context.Context, clients *kubeclients.KubeClients) error {
			return startControllerManagerForAgent(ctx, a, revalTokenFetcher, clients, a.metrics)
		}
	}

	// create and initialize queues before start goroutines
	// to avoid a data race
	a.resourceEventWorkerQueues = map[string]workqueue.Interface{}
	for _, eventName := range getAgentEvents() {
		a.resourceEventWorkerQueues[eventName] = workqueue.New()
	}

	return a, nil
}

func defaultNewQueueClient(endpoint string) queue.Client {
	return queuesqs.NewClient(endpoint, "")
}

func defaultNewKubeClients(ctx context.Context, path string) (*kubeclients.KubeClients, error) {
	log := core.GetLogger(ctx)

	log.Infof("Configuring Edge K8s kube clients from kubeconfig path %q ...", path)

	configurator := core.NewPathKubeConfigurator().WithPath(path)
	configCh := configurator.Start(ctx)

	coreKubeClientsCh := core.NewKubeClientsStream().WithConfigCh(configCh).Start(ctx)

	log.Info("Wait for kubeclients for clientsCh for backend K8s ...")

	coreClients, ok := <-coreKubeClientsCh
	if !ok {
		log.Error("Failed to configure core K8s clients")
		return nil, fmt.Errorf("failed to configure k8s client")
	}

	err := setKubernetesDefaults(coreClients.Config)
	if err != nil {
		log.WithError(err).Error("Failed to configure backend K8s REST config")
		return nil, err
	}

	backendK8sClients, err := kubeclients.NewFromCore(coreClients)
	if err != nil {
		log.WithError(err).Error("Failed to configure backend K8s clients")
		return nil, err
	}

	return backendK8sClients, nil
}

// Taken from https://github.com/kubernetes/kubectl/blob/82a943479841e06efdbb8543d28cfcd0c028c8b6/pkg/cmd/util/kubectl_match_version.go#L112-L129
// See https://github.com/kubernetes/client-go/issues/657
// setKubernetesDefaults sets default values on the provided client config for accessing the
// Kubernetes API or returns an error if any of the defaults are impossible or invalid.
// TODO this isn't what we want.  Each clientset should be setting defaults as it sees fit.
func setKubernetesDefaults(config *rest.Config) error {
	// TODO remove this hack.  This is allowing the GetOptions to be serialized.
	config.GroupVersion = &schema.GroupVersion{Group: "", Version: "v1"}

	if config.APIPath == "" {
		config.APIPath = "/api"
	}
	if config.NegotiatedSerializer == nil {
		// This codec factory ensures the resources are not converted. Therefore, resources
		// will not be round-tripped through internal versions. Defaulting does not happen
		// on the client.
		config.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
	}
	return rest.SetKubernetesDefaults(config)
}

func (a *Agent) getTickerEvents(ctx context.Context) <-chan *core.Event {
	log := core.GetLogger(ctx)

	log.Infof("Configuring ticker event streams ...")
	ts := core.NewTickerStream().WithRandomOffset(true)
	tickerStreams := []*core.TickerStream{
		ts.WithKind(EventTickSyncICMSRequests).WithInterval(syncICMSRequestInterval),
		ts.WithKind(EventTickRenewICMSCredentials).WithInterval(a.CredRenewInterval),
		ts.WithKind(EventTickUpdateHeartbeat).WithInterval(a.HeartbeatInterval),
		ts.WithKind(EventTickSyncSQSQueue).WithInterval(a.SyncQueueInterval),
		ts.WithKind(EventTickSyncICMSRequestStatus).WithInterval(a.SyncRequestStatusInterval),
		ts.WithKind(EventTickAcknowledgeRequest).WithInterval(a.SyncAcknowledgeRequestInterval),
		ts.WithKind(EventTickSyncCleanupUnusedResources).WithInterval(UnusedResourceCleanupDuration),
		ts.WithKind(EventTickUpdateICMSRegistration).WithInterval(syncICMSRegistrationInterval).WithImmediate(false),
		ts.WithKind(EventTickSyncPeriodicInstanceStatusUpdates).WithInterval(a.PeriodicInstanceStatusInterval),
		ts.WithKind(EventTickSyncNetworkPolicies).WithInterval(syncICMSRequestInterval),
	}

	merger := core.NewEventStreamMerger().WithBufferSize(100)
	eventChs := []<-chan *core.Event{}
	for _, s := range tickerStreams {
		ch := s.Start(ctx)
		eventChs = append(eventChs, ch)
		log.Debugf("Added ticker event stream: %+v", s)
	}
	return merger.Merge(ctx, eventChs...)
}

func (a *Agent) startK8sEventBroadcaster(ctx context.Context) {
	// start event broadcaster for backendk8s
	if a.backendk8scache != nil {
		a.backendk8scache.eventBroadcaster.StartStructuredLogging(0)
		a.backendk8scache.eventBroadcaster.StartRecordingToSink(&v1core.EventSinkImpl{Interface: a.backendk8scache.clients.K8s.CoreV1().Events("")})
		go func() {
			defer a.backendk8scache.eventBroadcaster.Shutdown()
			<-ctx.Done()
		}()
	}
}

func (a *Agent) startEventProcessDispatchers(ctx context.Context, events <-chan *core.Event) {
	log := core.GetLogger(ctx)
	metrics := nvcametrics.FromContext(ctx)

	go func() {
		for {
			select {
			// Pull from the ticker queue and push to the event-specific queue.
			case ev := <-events:
				if queue, ok := a.resourceEventWorkerQueues[ev.Kind]; ok {
					if ev.ObjectMetaKey != "" {
						queue.Add(ev.ObjectMetaKey)
					} else {
						// simply add by event kind and don't worry about the
						// other information this is for tickers
						queue.Add(ev.Kind)
					}
					metrics.EventQueueLength.WithLabelValues(metrics.WithDefaultLabelValues(ev.Kind)...).Set(float64(queue.Len()))
				} else {
					log.Errorf("No worker queue found for event type %s %v", ev.Kind, *ev)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (a *Agent) startEventProcessingWorkers(ctx context.Context) error {
	log := core.GetLogger(ctx)

	// Start the worker threads now that resourceEventWorkerQueues has been initialized
	for evtName, eventWorkQueue := range a.resourceEventWorkerQueues {
		log.Infof("Created workqueue and worker for event %s", evtName)
		go func(ctx context.Context, q workqueue.Interface, eventName string) {
			log.Infof("Started worker for event %s", eventName)
			metrics := nvcametrics.FromContext(ctx)
			for {
				// Obtain event and process
				obj, shutdown := q.Get()
				if shutdown {
					log.Infof("Workqueue shutdown, stop worker for %s", eventName)
					return
				}

				func() {
					// Apply then complete the event regardless of outcome,
					// since tickers will create a new event on their next interval.
					defer q.Done(obj)

					var ev *core.Event
					if objectMetaKey, ok := obj.(string); ok {
						ev = &core.Event{
							Kind:          eventName,
							ObjectMetaKey: objectMetaKey,
						}
					} else {
						ev = obj.(*core.Event)
					}
					metrics.EventQueueLength.WithLabelValues(metrics.WithDefaultLabelValues(eventName)...).Set(float64(q.Len()))
					a.apply(ctx, ev)
				}()
			}
		}(ctx, eventWorkQueue, evtName)
	}

	// goroutine to ensure the event processing workers are properly stopped
	go func(ctx context.Context) {
		<-ctx.Done()
		a.stopEventProcessingWorkers(ctx)
		if a.ackThreadPool != nil {
			a.ackThreadPool.Wait()
		}
		if a.instStatusThreadPool != nil {
			a.instStatusThreadPool.Wait()
		}
	}(ctx)

	log.Infof("eventWorkQueue initialized")
	return nil
}

func (a *Agent) stopEventProcessingWorkers(ctx context.Context) {
	log := core.GetLogger(ctx)
	for evtName, eventWorkQueue := range a.resourceEventWorkerQueues {
		log.Infof("Shutting down workqueue and worker for event %s", evtName)
		eventWorkQueue.ShutDown()
	}
}

// RegisterWithICMS is re-entrant from the ICMS perspective.
// BART Agent will Register unconditionally whenever it restarts
// And store the updated the Credentials in the local Secret
func (a *Agent) RegisterWithICMS(ctx context.Context) (*types.ICMSRegistrationResponse, error) {
	log := core.GetLogger(ctx)

	if a.backendk8scache == nil {
		return nil, fmt.Errorf("cannot register with ICMS, backendk8scache is not initialized")
	}
	if a.icmsClient == nil {
		return nil, fmt.Errorf("cannot register with ICMS, icmsClient is not initialized")
	}

	// Wait for caches to populate so cluster metadata gatherers
	// return the proper state.
	a.backendk8scache.ForceSync(ctx)

	backendGPUs, err := a.backendk8scache.GetAllBackendGPUs(ctx)
	if err != nil {
		log.WithError(err).Error("Get K8s Cluster GPUs failed")
		return nil, fmt.Errorf("failed to get K8s cluster GPUs, error: %w", err)
	}

	log.Debugf("Kubernetes Cluster GPUs: %+v", backendGPUs)

	regBackendGPUs, err := a.backendk8scache.GetRegisteredBackendGPUs(ctx,
		backendGPUs,
		a.MultiNodeWorkloadsEnabled)
	if err != nil {
		log.WithError(err).Error("Get registered backend GPUs")
		return nil, fmt.Errorf("get registered backend GPUs for ICMS registration request: %v", err)
	}

	res, err := a.registerWithICMS(ctx, regBackendGPUs)
	if err != nil {
		return nil, err
	}

	log.WithFields(logrus.Fields{
		"clusterID":      res.ClusterID,
		"clusterGroupID": res.ClusterGroupID,
	}).Info("Successfully registered with ICMS")

	return res, nil
}

func (a *Agent) registerWithICMS(ctx context.Context, regBackendGPUs []types.RegistrationGPU) (*types.ICMSRegistrationResponse, error) {
	log := core.GetLogger(ctx)
	k8sVersion := a.K8sVersion

	// Cache registration GPUs here, since all callers determine that
	// an update is necessary before calling this method.
	a.backendk8scache.regITCache.Put(regBackendGPUs)

	// Build registration request
	rreq := a.buildICMSRegistrationRequest(k8sVersion, regBackendGPUs)

	// Fetch the k8s version if not provided
	if rreq.K8sVersion == "" {
		v, err := getCurrentK8sVersion(ctx, a.backendk8scache.clients.DiscoveryClient)
		if err != nil {
			return nil, err
		}
		rreq.K8sVersion = v
	}

	// Invoke the registration request to ICMS.
	res, err := a.icmsClient.Register(ctx, &rreq)
	nvcametrics.FromContext(ctx).RecordUpstreamRequest(nvcametrics.UpstreamOperationRegister, err)
	if err != nil {
		log.WithError(err).WithField("req", rreq).Error("Register with ICMS")
		return nil, fmt.Errorf("register with ICMS: %v", err)
	}

	if err := a.backendk8scache.StoreICMSRegistrationResponse(ctx, res); err != nil {
		log.WithError(err).Error("Store ICMS registration")
		return nil, err
	}

	return res, nil
}

// buildICMSRegistrationRequest constructs the registration request payload without side effects
func (a *Agent) buildICMSRegistrationRequest(k8sVersion string, regBackendGPUs []types.RegistrationGPU) types.ICMSRegistrationRequest {
	clusterStatus := "READY"
	switch a.MaintenanceMode {
	case types.MaintenanceModeCordon:
		clusterStatus = "CORDON"
	case types.MaintenanceModeCordonAndDrain:
		clusterStatus = "CORDON_AND_DRAIN"
	}

	return types.ICMSRegistrationRequest{
		ClusterStatus:    clusterStatus,
		K8sVersion:       k8sVersion,
		ClusterTargeting: a.ClusterTargetingEnabled,
		BackendGPUs:      regBackendGPUs,
		// Always request task queues. This field is unset in prior NVCA versions
		// as a backwards compatibility mechanism only.
		TaskClusterCreationQueues: true,
		// Set NVCA version
		NVCAVersion: a.NVCAAgentVersion,
	}
}

// setupTracing initializes the OpenTelemetry tracer provider and propagators from
// opts.Config.Tracing. When Exporter is NoExporter a noop provider is installed
// and no background work is started. Otherwise the real OTLP/Lightstep exporter
// is configured via tracing.SetupOTELTracer and a goroutine is spawned that
// blocks on ctx.Done() and then flushes/shuts down the exporter so in-flight
// spans are not lost.
func setupTracing(ctx context.Context, opts AgentOptions) {
	otel.SetTextMapPropagator(otelpropagation.NewCompositeTextMapPropagator(
		otelpropagation.Baggage{},
		otelpropagation.TraceContext{},
	))
	if opts.Config.Tracing.Exporter == nvcaconfig.NoExporter {
		otel.SetTracerProvider(noop.NewTracerProvider())
		return
	}

	endpoint := os.Getenv("OTEL_ENDPOINT")
	if endpoint == "" {
		logrus.Error("OTEL tracing is enabled but OTEL_ENDPOINT is not set. " +
			"Set featureGate.otelConfig.endpoint in the NVCFBackend CR. Falling back to noop tracer.")
		otel.SetTracerProvider(noop.NewTracerProvider())
		return
	}

	if _, err := tracing.SetupOTELTracer(&tracing.OTELConfig{
		Enabled:     true,
		Endpoint:    endpoint,
		AccessToken: opts.Config.Tracing.LightstepAccessToken,
		Attributes: tracing.Attributes{
			ServiceName:    opts.Config.Tracing.LightstepServiceName,
			ServiceVersion: version.ReleaseString(),
		},
	}); err != nil {
		logrus.WithError(err).Error("Failed to set up OTEL tracer, falling back to noop")
		otel.SetTracerProvider(noop.NewTracerProvider())
		return
	}

	go func() {
		<-ctx.Done()
		tracing.Shutdown()
	}()
}

func (a *Agent) Start(ctx context.Context) error {
	log := core.GetLogger(ctx)

	if !strings.EqualFold(a.ComputeBackend, string(BackendTypeK8s)) {
		return fmt.Errorf("unsupported compute backend: %v", a.ComputeBackend)
	}

	// Re-inject the metrics into the context
	ctx = nvcametrics.WithMetrics(ctx, a.metrics)

	// Initialize tracing; a background goroutine handles shutdown on ctx cancellation.
	setupTracing(ctx, *a.AgentOptions)

	log.Infof("Starting NVCF Cluster Agent version %s with options: %+v",
		a.NVCAAgentVersion, a.AgentOptions.sanitizedString())

	// The main health/metrics server must be started first so k8s probes work as expected.
	// Start() may take a long time but certain endpoints should be present before that work completes.
	server := core.NewHTTPService(a.NVCASvcAddress)
	middlewareOpts := []core.HTTPMiddlewareOption{
		core.WithRequestMetrics(a.metricsName),
		core.WithHandlerTimeout(5 * time.Second),
	}
	if a.MetricsRegisterer != nil {
		middlewareOpts = append(middlewareOpts, core.WithPrometheusRegisterer(a.MetricsRegisterer))
	}
	server.Use(core.NewHTTPMiddleware(ctx, middlewareOpts...)...)

	// Provides /healthz
	health.HTTPAddReadinessRoute(server.Router, a.readinessCheckGetter)
	// Provides /version
	server.AddVersionRoute(ctx)
	// Provides /metrics
	nvcametrics.AddMetricsRoute(server.Router, log, nvcametrics.FromContext(ctx).GetDefaultLabelPairs(), "nvca")
	// Provides /livez
	health.HTTPAddLivenessRoute(server.Router, a.livenessCheckGetter)

	log.Info("Starting health server")
	if _, err := server.Start(ctx); err != nil {
		log.WithError(err).Error("Failed to start main server")
		return err
	}

	// Add profiling endpoints. These are for admin use only.
	if a.NVCADebugAddr != "" {
		log.Info("Starting debug server")
		if err := a.startDebugServer(ctx); err != nil {
			log.WithError(err).Error("Failed to start debug server")
			return err
		}
	} else {
		log.Info("Debug server address not configured, skipping")
	}

	// Provides /admin
	log.Info("Starting admin server")
	adminServer := core.NewHTTPService(a.NVCAAdminAddr)
	adminMiddlewareOpts := []core.HTTPMiddlewareOption{}
	if a.MetricsRegisterer != nil {
		adminMiddlewareOpts = append(adminMiddlewareOpts, core.WithPrometheusRegisterer(a.MetricsRegisterer))
	}
	adminServer.Use(core.NewHTTPMiddleware(ctx, adminMiddlewareOpts...)...)
	adminServer.AddAdminRoute(ctx)
	if _, err := adminServer.Start(ctx); err != nil {
		log.WithError(err).Error("Failed to start admin server")
		return err
	}

	log.Info("Creating kubeclient")
	k8sclients, err := a.newKubeClients(ctx, a.KubeConfigPath)
	if err != nil {
		return err
	}

	// Start the cluster-validator summary reconciler. This watches the
	// well-known summary ConfigMap in the operator/validator namespace
	// and republishes its content as Prometheus metrics on the agent's
	// /metrics endpoint. Failures here are non-fatal — metrics are an
	// SLI, not a gate.
	if a.metrics != nil {
		validatorNS := resolveValidatorSummaryNamespace()
		validatorReconciler := NewValidatorSummaryReconciler(k8sclients.K8s, validatorNS, a.metrics)
		if startErr := validatorReconciler.Start(ctx); startErr != nil {
			log.WithError(startErr).Warn("cluster-validator summary reconciler failed to start; metrics will be unavailable")
		}
	}

	infraOverheadGetter := enforce.NewInfraOverheadGetter(a.FeatureFlagFetcher, a.Config, enforce.GetRuntimeClassK8sClient(k8sclients.K8s))

	log.Info("Configuring backendk8scache")
	backendk8scache, _, err := NewBackendk8sCacheBuilder().
		WithConfig(a.Config).
		WithClusterProvider(a.CloudProvider).
		WithClusterRegion(a.ClusterRegion).
		WithClusterName(a.ClusterName).
		WithSystemNamespace(a.SystemNamespace).
		WithRequestsNamespace(a.RequestsNamespace).
		WithNamespaceLabels(a.NamespaceLabels).
		WithClients(k8sclients).
		WithTimeConfig(a.K8sTimeConfig).
		WithComputeBackend(BackendType(a.ComputeBackend)).
		WithLogPosting(a.LogPostingEnabled).
		WithCachingSupport(a.CachingSupportEnabled, a.NVMeshEncryptionEnabled).
		WithHelmRBACEnforcement(a.HelmRBACEnforcementEnabled).
		WithHelmResourceConstraints(a.HelmResourceConstraintsEnabled).
		WithHelmSharedStorage(a.HelmSharedStorageEnabled).
		WithStaticGPUCapacity(a.GPUCapacity).
		WithDynamicNodeFeatureClient(a.DynamicGPUDiscoveryEnabled, nodefeatures.DynamicClientOptions{
			MultipleGPUTypesAllowed: a.MultipleGPUTypesAllowed,
			UniformInstanceLabels:   a.UniformInstanceLabelsEnabled,
		}).
		WithPeriodicInstanceStatusUpdate(a.PeriodicInstanceStatusUpdateEnabled,
			a.PeriodicInstanceStatusInterval).
		WithWorkerDegradationHandler(a.AutoPurgeDegradedWorkers).
		WithLowLatencyStreaming(a.LowLatencyStreamingEnabled).
		WithHelmRepositoryPrefix(a.HelmRepositoryPrefix).
		WithPVCRebind(a.PVCRebindEnabled).
		WithFNDSClient(a.fndsClient).
		WithFeatureFlagFetcher(a.FeatureFlagFetcher).
		WithCSIVolumeMountOptions(a.CSIVolumeMountOptions).
		WithImageCredentialHelperImage(a.ImageCredentialHelperImage).
		WithInfraOverheadGetter(infraOverheadGetter).
		WithSecretMirrorConfig(a.SecretMirrorSourceNamespace, a.SecretMirrorLabelSelector).
		WithEnvOverrides(a.FunctionEnvOverrides, a.TaskEnvOverrides).
		Start(ctx)
	if err != nil {
		log.WithError(err).Error("Failed to configure the Backend K8s Cache")
		return err
	}

	if a.backendk8scache == nil {
		a.backendk8scache = backendk8scache
	}

	// Initialize GPU monitor if GracefulNoGPU is enabled
	gracefulNoGPUEnabled := a.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.GracefulNoGPU)
	if gracefulNoGPUEnabled {
		log.Info("GracefulNoGPU enabled, initializing GPU monitor")
		nfClient := a.backendk8scache.GetNodeFeaturesClient()
		if nfClient != nil {
			// Build GPU monitor options from agent options
			var gpuMonitorOpts []GPUMonitorOption
			if a.GPUPollInterval > 0 {
				gpuMonitorOpts = append(gpuMonitorOpts, WithGPUPollInterval(a.GPUPollInterval))
			}
			if a.GPUDebounceTime > 0 {
				gpuMonitorOpts = append(gpuMonitorOpts, WithGPUDebounceTime(a.GPUDebounceTime))
			}
			// Add node informer for event-driven GPU monitoring
			if nodeInformer := a.backendk8scache.GetNodeInformer(); nodeInformer != nil {
				gpuMonitorOpts = append(gpuMonitorOpts, WithNodeInformer(nodeInformer))
			}
			a.gpuMonitor = NewGPUMonitor(nfClient, gpuMonitorOpts...)
			// Check initial GPU availability
			gpus, gpuErr := nfClient.GetAllBackendGPUs(ctx)
			hasGPUs := gpuErr == nil && len(gpus) > 0
			a.gpuMonitor.SetHasGPUs(hasGPUs)
			if hasGPUs {
				log.Info("GPUs found during startup, proceeding normally")
			} else {
				log.Warn("No GPUs found during startup, NVCA will wait for GPUs to become available")
			}
		}
	}

	// Initialize the backendK8sCache's health status cache
	statusUpdaters := []health.ComponentStatusGetter{a.backendk8scache}

	// Add GPU monitor to status updaters for readiness checks when GracefulNoGPU is enabled
	if a.gpuMonitor != nil {
		statusUpdaters = append(statusUpdaters, a.gpuMonitor)
	}
	if a.FeatureFlagFetcher.IsAttributeEnabled(featureflag.AttrHostIsolation) {
		statusUpdaters = append(statusUpdaters, hostisolation.NewStatusGetter(
			a.backendk8scache.nodeLister,
			a.backendk8scache.podLister,
		))
	}
	if a.FeatureFlagFetcher.IsAttributeEnabled(featureflag.AttrKataRuntimeIsolation) {
		statusUpdaters = append(statusUpdaters, kata.NewStatusGetter(k8sclients.K8s))
	}
	if a.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.KAIScheduler) {
		statusUpdaters = append(statusUpdaters,
			kaischeduler.NewRunAIQueueHealthCheck(k8sclients.HelmV2))
	}
	if a.FeatureFlagFetcher.IsFeatureFlagEnabled(&featureflag.HelmSharedStorage.FeatureFlag) {
		statusUpdaters = append(statusUpdaters, nvcastorage.NewSMBCSIDriverHealthCheck(k8sclients.K8s.StorageV1().CSIDrivers()))
	}
	a.backendHealthCache = health.NewBackendStatusCache(a.MinHealthcheckRefreshWait, statusUpdaters...)

	// Pause until NVCA is initially healthy
	const (
		healthInterval = 2 * time.Second
		healthTimeout  = 1 * time.Minute
	)

	// Skip waiting for healthy status when GracefulNoGPU is enabled with no GPUs.
	// In this case, readiness will report not-ready until GPUs appear, but liveness will pass.
	skipHealthWait := a.gpuMonitor != nil && !a.gpuMonitor.HasGPUs()

	if skipHealthWait {
		log.Warn("GracefulNoGPU enabled with no GPUs - skipping health wait, readiness will report not-ready")
	} else {
		log.WithFields(logrus.Fields{
			"interval": healthInterval,
			"timeout":  healthTimeout,
		}).Info("Waiting for initial NVCA health")
		if err := health.WaitForHealthyStatus(ctx, healthInterval, healthTimeout, a.backendHealthCache); err != nil {
			log.WithError(err).Error("NVCA did not achieve healthy status")
			return err
		}
	}

	// Check if we should defer ICMS registration (no GPUs with GracefulNoGPU enabled).
	skipInitialRegistration := a.gpuMonitor != nil && !a.gpuMonitor.HasGPUs()
	var res *types.ICMSRegistrationResponse
	if skipInitialRegistration {
		log.Warn("No GPUs available - deferring ICMS registration until GPUs are detected")
		// Create an empty registration response with minimal credentials
		// Queue manager will be created but paused until GPUs appear
		res = &types.ICMSRegistrationResponse{
			ClusterID:      a.ClusterID,
			ClusterGroupID: a.ClusterGroupID,
			Credentials: types.QueueCredentials{
				CreationQueues: types.CreationQueueInfoSet{},
			},
		}
	} else {
		// Register with ICMS after health.
		log.Info("Registering with ICMS")
		var regErr error
		res, regErr = a.RegisterWithICMS(ctx)
		if regErr != nil {
			log.WithError(regErr).Error("Failed to register with ICMS")
			return regErr
		}
	}

	// Start auxiliary controllers.
	log.Info("Starting controller manager")
	if err := a.startControllerManager(ctx, k8sclients); err != nil {
		log.WithError(err).Error("failed to start controller manager")
		return err
	}

	// instantiate queue manager
	log.WithField("maintenance_mode", a.MaintenanceMode).Infof("Initializing queue manager")
	metrics := nvcametrics.FromContext(ctx)

	var queueClient queue.Client
	if a.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.SelfHosted) {
		// PSAT/SPIRE paths share the same projected-token file at
		// PSATTokenFilePath; both feed the same NATS PSAT token fetcher.
		// IdentitySource discriminates source-specific behavior below.
		if psatPath := a.AgentOptions.TokenFetcherOptions.PSATTokenFilePath; psatPath != "" {
			source := a.AgentOptions.Config.Authz.ClusterIssuedTokenSource
			if source == "" {
				source = nvcaconfig.ClusterIssuedTokenSourcePSAT
			}
			log.Infof("SelfHosted + %s identity, initializing NATS queue client with projected-token fetcher", source)
			psatFetcher, ferr := nvcaauth.NewPSATTokenFetcher(psatPath)
			if ferr != nil {
				log.WithError(ferr).Error("Failed to construct projected-token fetcher for NATS")
				return fmt.Errorf("construct projected-token fetcher: %w", ferr)
			}
			queueClient, err = natsqueue.NewClientWithTokenFetcherURLAndHostOverride(ctx, a.NATSURL, a.NATSHostOverride, a.ClusterID, psatFetcher)
			if err != nil {
				log.WithError(err).Error("Failed to create NATS queue client")
				return fmt.Errorf("create NATS queue client: %w", err)
			}

			// JWKS updater is PSAT-only: it discovers the projected token issuer's
			// public JWKS endpoint and pushes rotations to ICMS so ICMS can keep
			// verifying PSATs through a key rotation. SPIRE-mode clusters run a
			// different trust pipeline: ICMS already has the SPIRE trust bundle
			// out-of-band, and pushing K8s API JWKS would clobber it and break SVID
			// verification on the next introspect.
			//
			// Launched as a plain goroutine today (matches other non-controller
			// background loops in this package). The Start signature also
			// satisfies controller-runtime's manager.Runnable, so a future
			// multi-replica NVCA can switch to manager-registered leader-
			// elected startup without touching the updater itself.
			if source == nvcaconfig.ClusterIssuedTokenSourcePSAT {
				jwksUpdater, jerr := NewJWKSUpdater(JWKSUpdaterOptions{
					ICMSURL:                a.EffectiveICMSURL(),
					ICMSHostHeaderOverride: a.ICMSHostHeaderOverride,
					ClusterID:              a.ClusterID,
					TokenPath:              psatPath,
				})
				if jerr != nil {
					log.WithError(jerr).Error("Failed to construct JWKS updater")
					return fmt.Errorf("construct JWKS updater: %w", jerr)
				}
				go func() {
					if runErr := jwksUpdater.Start(ctx); runErr != nil {
						log.WithError(runErr).Error("JWKS updater exited with error")
					}
				}()
			} else {
				log.Infof("Identity source %q: skipping K8s API JWKS updater (not applicable)", source)
			}
		} else {
			log.Info("SelfHosted feature flag enabled, initializing NATS queue client")
			if a.natsSecretsFetcher == nil {
				return fmt.Errorf("nats secrets fetcher not available")
			}
			queueClient, err = natsqueue.NewClientWithURLAndHostOverride(ctx, a.NATSURL, a.NATSHostOverride, a.ClusterID, a.natsSecretsFetcher)
			if err != nil {
				log.WithError(err).Error("Failed to create NATS queue client")
				return fmt.Errorf("create NATS queue client: %w", err)
			}
		}
	} else {
		queueClient = newQueueClient(a.AgentOptions.EndpointURL)
	}

	a.queueManager = NewQueueManager(
		backendk8scache,
		a.backendHealthCache,
		queueClient,
		a.postProcessQueueCredentials(ctx, res.Credentials),
		featureflag.DefaultFetcher,
		a.MaintenanceMode,
		metrics,
	)
	a.livenessCheckGetter.AddChecker(a.queueManager)

	// If GracefulNoGPU is enabled and we started without GPUs, pause the queue manager
	// and set up callbacks to handle GPU availability changes
	if a.gpuMonitor != nil {
		if !a.gpuMonitor.HasGPUs() {
			log.Warn("Starting with queue manager paused due to no GPUs")
			a.queueManager.Pause()
		}

		// Set up GPU state change callback
		a.gpuMonitor.SetOnGPUStateChange(func(callbackCtx context.Context, hasGPUs bool) {
			callbackLog := core.GetLogger(callbackCtx)
			if hasGPUs {
				callbackLog.Info("GPUs detected - resuming queue manager and registering with ICMS")
				// Resume queue processing
				a.queueManager.Resume()
				// Register/re-register with ICMS to update GPU inventory.
				if _, regErr := a.RegisterWithICMS(callbackCtx); regErr != nil {
					callbackLog.WithError(regErr).Error("Failed to register with ICMS after GPUs became available")
				} else {
					callbackLog.Info("Successfully registered with ICMS after GPUs became available")
				}
			} else {
				callbackLog.Warn("GPUs no longer available - pausing queue manager")
				// Pause queue processing (allows termination messages, blocks creation)
				a.queueManager.Pause()
			}
		})

		// Start the GPU monitor polling loop
		log.Info("Starting GPU monitor")
		a.gpuMonitor.Start(ctx)
	}

	// Evict all workloads once during startup if in CordonAndDrainMaintenance mode
	if a.MaintenanceMode == types.MaintenanceModeCordonAndDrain {
		log.Infof("NVCA in %s maintenance mode - initiating one-time eviction of all workloads at startup", a.MaintenanceMode)
		if err := a.evictAllWorkloads(ctx); err != nil {
			log.WithError(err).Error("Failed to evict all workloads during startup in maintenance mode")
			return err
		}
		log.Infof("Successfully completed startup eviction of all workloads in %s maintenance mode", a.MaintenanceMode)
	}

	// Init node update syncer before starting workers, which call the syncer.
	log.Info("Initializing ICMS registration syncer")
	if err := a.initICMSRegistrationSyncer(ctx); err != nil {
		log.WithError(err).Error("Init Node update syncer failed")
		return err
	}

	// Init per event queue and workers
	log.Info("Starting event processing workers")
	if err := a.startEventProcessingWorkers(ctx); err != nil {
		log.WithError(err).Error("Failed to start event processing workers")
		return err
	}

	// Start event broadcaster
	log.Info("Starting kubernetes event broadcaster")
	a.startK8sEventBroadcaster(ctx)

	// Start ticker event dispatchers
	log.Info("Starting event dispatchers")
	a.startEventProcessDispatchers(ctx, a.getTickerEvents(ctx))

	// Set readiness check only after all critical initialization (including ICMS registration)
	// has succeeded. This ensures the readiness probe stays not-ready on error returns,
	// preventing a rolling update from proceeding when registration fails (e.g. 409 Conflict
	// due to instance type renames with active functions).
	a.readinessCheckGetter.SetCheck(a.backendHealthCache)

	log.Info("NVCA startup complete")

	return nil
}

// apply executes for each event in the queue
func (a *Agent) apply(ctx context.Context, ev *core.Event) {
	log := core.GetLogger(ctx)
	log.Tracef("starting event apply: %s", ev.String())
	timeStart := time.Now()

	// Skip ICMS-related events if self-destruct has been triggered.
	if a.selfDestruct != nil && a.selfDestruct.Load() {
		if SkippedEventsInSelfDestructMode[ev.Kind] {
			return
		}
	}

	// Add timer for this event to track the event latency
	metrics := nvcametrics.FromContext(ctx)
	timer := prometheus.NewTimer(metrics.EventProcessLatency.WithLabelValues(metrics.WithDefaultLabelValues(ev.Kind)...))
	defer timer.ObserveDuration()
	if err := func(ctx context.Context) (err error) {
		switch ev.Kind {
		case EventTickRenewICMSCredentials:
			err = a.RenewICMSQueueCreds(ctx)
		case EventTickUpdateHeartbeat:
			err = a.PutNVCAStatusUpdate(ctx)
		case EventTickSyncSQSQueue:
			err = a.queueManager.SyncQueues(ctx)
		case EventTickSyncICMSRequestStatus:
			err = a.PostICMSInstanceRequestStatusUpdates(ctx)
		case EventTickSyncICMSRequests:
			err = a.backendk8scache.SyncAllICMSRequests(ctx)
		case EventTickAcknowledgeRequest:
			err = a.PutICMSRequestAcknowledgement(ctx)
		case EventTickSyncCleanupUnusedResources:
			err = a.backendk8scache.CleanupUnusedResources(ctx)
		case EventTickUpdateICMSRegistration:
			err = a.syncICMSRegistration(ctx)
		case EventTickSyncPeriodicInstanceStatusUpdates:
			err = a.SyncPeriodicInstanceStatuses(ctx)
		case EventTickSyncNetworkPolicies:
			err = a.backendk8scache.SyncNetworkPolicies(ctx)
		default:
			err = fmt.Errorf("unknown event %q", ev.Kind)
		}
		eventDuration := time.Since(timeStart)
		if err != nil {
			log.WithError(err).Tracef("failed to apply the event %v, after %v", ev.Kind, eventDuration)
		} else {
			log.Tracef("successfully applied event %s, after %v", ev, eventDuration)
		}
		return err
	}(ctx); err != nil {
		// increment the counter for error totals per event kind
		metrics.EventErrorTotal.WithLabelValues(metrics.WithDefaultLabelValues(ev.Kind)...).Inc()
	}
}

func (a *Agent) PutNVCAStatusUpdate(ctx context.Context) error {
	a.backendk8scache.UpdateSchedulerWorkloadMetrics(ctx)

	agentHealth, err := a.backendHealthCache.RefreshStatus(ctx)
	if err != nil {
		return err
	}
	req := &types.HealthStatusRequest{
		UpgradeStatus:       a.backendk8scache.getNVCAUpgradeStatus(ctx),
		Status:              agentHealth.Status,
		GPUUsage:            agentHealth.GPUUsage,
		ClusterOwnerNCAID:   a.NCAId,
		NVCAAgentVersion:    a.NVCAAgentVersion,
		NVCAOperatorVersion: a.NVCAOperatorVersion,
		ClusterName:         a.ClusterName,
		MaintenanceMode:     a.MaintenanceMode,
	}

	// Send health status to ICMS and get response with action instructions.
	response, err := a.icmsClient.PutHealthStatus(ctx, req)
	nvcametrics.FromContext(ctx).RecordUpstreamRequest(nvcametrics.UpstreamOperationHeartbeat, err)
	if err != nil {
		return err
	}

	// Handle response action from ICMS with CLI flag overrides.
	log := core.GetLogger(ctx).WithFields(logrus.Fields{
		"rpc":    "Agent.PutNVCAStatusUpdate",
		"action": response.Action,
	})

	shouldTriggerSelfDestruct := false

	// Check force self-destruct flag first (highest priority)
	if a.ForceSelfDestruct {
		shouldTriggerSelfDestruct = true
		log.WithField("source", "forced").Warn("Initiating self-destruct sequence from feature flag")
	} else if response != nil && response.Action == types.HealthActionSelfDestruct {
		// Check if we should skip self-destruct
		if a.SkipSelfDestruct {
			log.Warn("Received SELF_DESTRUCT action from ICMS, but skipping due to skip-self-destruct flag")
		} else {
			shouldTriggerSelfDestruct = true
			log.WithField("source", "remote").Warn("Initiating self-destruct sequence from ICMS action")
		}
	}

	if shouldTriggerSelfDestruct {
		// Trigger self-destruct in a separate goroutine to avoid blocking the heartbeat
		go func() {
			// Use a new context to avoid cancellation issues
			selfDestructCtx := context.Background()
			selfDestructCtx = core.WithLogger(selfDestructCtx, log)

			if err := a.handleSelfDestruct(selfDestructCtx); err != nil {
				log.WithError(err).Error("Failed to complete self-destruct sequence")
			}
		}()
	}

	return nil
}

func (a *Agent) SyncPeriodicInstanceStatuses(ctx context.Context) error {
	log := core.GetLogger(ctx)
	isRes, err := a.icmsClient.GetICMSServerInstanceStatuses(ctx)

	if err != nil {
		return fmt.Errorf("failed to GetICMSServerInstanceStatuses, err: %v", err)
	}

	for _, is := range isRes.Instances {
		childLog := log.WithFields(logrus.Fields{
			"instanceID": is.InstanceID,
			"icms-state": is.InstanceState,
		})
		req, reqIS, ra, reqFound := a.backendk8scache.ReconcileInstanceStatus(ctx, is)
		if !reqFound {
			if ra == ICMSInstanceReconcileTerminateAndUpdate {
				childLog.Warn("Periodic not-found instance status corrective action")
				if err := a.handleNotFoundInstanceStatusSyncAction(ctx, is.RequestID, is.InstanceID, ra); err != nil {
					childLog.WithError(err).Error("Failed to handle not-found instance status sync")
				}
			}
			continue
		}
		childLog = childLog.WithFields(logrus.Fields{
			"local-state": reqIS.LastReportedStatus,
			"action":      string(ra),
		})
		childCtx := core.WithLogger(ctx, childLog)
		childLog.Debug("Periodic instance status sync")
		switch ra {
		case ICMSInstanceReconcileNoAction:
		case ICMSInstanceReconcileUpdateOnly, ICMSInstanceReconcileTerminateAndUpdate:
			childLog.Warn("Periodic instance status corrective action")
			if ra == ICMSInstanceReconcileTerminateAndUpdate {
				// following call only terminates the instance
				err := a.backendk8scache.icmsRequestHelper.HandleInstanceStatusPreconditionFailure(childCtx, req, reqIS.ID)
				if err != nil {
					childLog.WithError(err).Error("failed to terminate instance due to sync action")
				}
			}
			if err := a.handleInstanceStatusSyncAction(childCtx, req, reqIS, ra); err != nil {
				childLog.WithError(err).Error("Failed to handle instance status sync")
			}
		}
	}
	return nil
}

func (a *Agent) IsRequestFromClusterQueue(_ context.Context, req *nvcav2beta1.ICMSRequest) bool {
	return strings.Contains(req.Spec.CreationMsgInfo.QueueURL, a.ClusterID)
}

func (a *Agent) PutICMSRequestAcknowledgement(ctx context.Context) error {
	allReqs, err := a.backendk8scache.icmsRequestLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("failed PutICMSRequestAcknowledgement, err: %v", err)
	}

	if a.ackThreadPool == nil {
		a.ackThreadPool = pool.New().WithMaxGoroutines(ICMSRequestAckMaxGoroutines)
	}

	ackTaskAfterScheduled := a.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.AckTaskRequestAfterPodsScheduled)

	ackSR := func(ctx context.Context, req *nvcav2beta1.ICMSRequest) bool {
		log := core.GetLogger(ctx)
		log.Info("Acknowledging ICMSRequest")
		err := a.icmsClient.PutRequestAcknowledgement(ctx,
			req.Spec.RequestID,
			req.Spec.MessageBatchID,
			req.Spec.CreationMsgInfo.InstanceCount,
			req.Spec.GetTraceContext())
		if err != nil {
			a.backendk8scache.eventRecorder.Eventf(req, v1.EventTypeWarning,
				string(types.EventCategoryInstanceStatusUpdate), "Acknowledgement failed: %v", err)
			log.WithError(err).Error("Failed to acknowledge request")

			// If it has only been five minutes since the request was created, and a 404 is return, retry
			if isICMSRequestAcknowledgementErrorRetryable(err, req, a.EffectiveICMSRequestAckRetryTimeout()) {
				log.WithError(err).Info("Retrying ICMSRequest acknowledgement error")
			} else if err := a.handleStatusPreconditionFailed(ctx, req); err != nil {
				log.WithError(err).Error("Failed to update ICMSRequest status on precondition failure error")
			}
		}
		return err == nil
	}

	for _, req := range allReqs {
		// Cannot mutate the object since it is in the cache, we must first perform a deep copy on it
		req = req.DeepCopy()
		a.ackThreadPool.Go(func() {
			// inject the logger with spotrequest logger fields
			// intentionally do not override parent context and logger
			// we want new ones per scope
			ctx := nvcalogging.WithICMSRequestFieldLogger(ctx, req)
			log := core.GetLogger(ctx)

			switch {
			case req.Spec.Action == common.TerminationAction:
				// Termination requests only need status updated to Pending here.
				if req.Status.RequestStatus == "" {
					modify := func(ctx context.Context, sr *nvcav2beta1.ICMSRequest) {
						sr.Status.RequestStatus = nvcav2beta1.ICMSRequestStatusPending
						sr.Status.LastStatusUpdated = &metav1.Time{Time: core.GetCurrentTime(ctx)}
					}
					if !a.backendk8scache.applyICMSRequestStatusChange(ctx, req, modify) {
						log.Error("Failed to update ICMSRequest status to Pending")
					}
				}
				return
			case (req.Spec.Action == common.RequestICMSInstancesForTask || req.Spec.Action == common.TaskCreationAction) && ackTaskAfterScheduled:
				// Handle tasks with the ack-after-schedule feature flag enabled separately.
				a.putTaskICMSRequestAcknowledgementAfterScheduled(ctx, req, ackSR)
				return
			}

			// Function or tasks without ack-after-schedule.
			switch req.Status.RequestStatus {
			case nvcav2beta1.ICMSRequestStatusPending,
				nvcav2beta1.ICMSRequestStatusInProgress,
				nvcav2beta1.ICMSRequestStatusInstancesInProgress,
				nvcav2beta1.ICMSRequestStatusCompleted,
				nvcav2beta1.ICMSRequestStatusFailed,
				nvcav2beta1.ICMSRequestStatusCompletionAcknowledged,
				nvcav2beta1.ICMSRequestStatusFailureAcknowledged:
				// No action needed at this point. If the request has failed,
				// its message will become visible again after its timeout.
				// Tasks should only be ack'd after caching + pods scheduled when ff is enabled.
			case nvcav2beta1.ICMSRequestStatusCachingInProgress:
				// Send ACK again only if it is less than ICMS request ACK interval
				if req.Status.LastACKTimestamp != nil && time.Since(req.Status.LastACKTimestamp.Time) < a.EffectiveICMSRequestACKInterval() {
					break
				}
				fallthrough
			default:
				if !ackSR(ctx, req) {
					return
				}
				a.backendk8scache.eventRecorder.Event(req, v1.EventTypeNormal, string(types.EventCategoryInstanceStatusUpdate),
					"Request accepted for processing")

				// If ACK is successful, purge the message now
				err = a.queueManager.DeleteCreationMessageV2(ctx, req.Spec.MessageReceipt, req.Spec.CreationMsgInfo.QueueURL)
				if err != nil && !a.queueManager.Client.IsMessageNotFoundError(err) {
					log.WithError(err).
						WithFields(logrus.Fields{
							"receipt": req.Spec.MessageReceipt,
							"id":      req.Labels[SQSMessageIDKey],
						}).Warn("Failed to delete the request message")
				} else if err == nil {
					log.WithFields(logrus.Fields{
						"receipt": req.Spec.MessageReceipt,
						"id":      req.Labels[SQSMessageIDKey],
					}).Debug("Deleted message from creation queue")
				}

				modify := func(ctx context.Context, sr *nvcav2beta1.ICMSRequest) {
					now := core.GetCurrentTime(ctx)
					if req.Status.RequestStatus == "" {
						sr.Status.RequestStatus = nvcav2beta1.ICMSRequestStatusPending
						sr.Status.LastStatusUpdated = &metav1.Time{Time: now}
					}
					sr.Status.LastACKTimestamp = &metav1.Time{Time: now}
				}
				if !a.backendk8scache.applyICMSRequestStatusChange(ctx, req, modify) {
					log.Error("Failed to update ICMSRequest status to Pending/CachingInProgress")
				}
			}
		})
	}

	return nil
}

// nearVisTimeoutSeconds is close to vis timeout seconds for a task queue message.
// Since updates do not occur exactly on this threshold, invisibility should be extended earlier.
// It should be a function of the actual visibility timeout to scale with changes to this value.
const nearVisTimeoutSeconds = float64(creationQueueVisibilityTimeoutSeconds) * 0.7

func (a *Agent) putTaskICMSRequestAcknowledgementAfterScheduled(
	ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
	ackSR func(context.Context, *nvcav2beta1.ICMSRequest) bool,
) {
	log := core.GetLogger(ctx)

	switch req.Status.RequestStatus {
	case nvcav2beta1.ICMSRequestStatusCompleted,
		nvcav2beta1.ICMSRequestStatusFailed,
		nvcav2beta1.ICMSRequestStatusCompletionAcknowledged,
		nvcav2beta1.ICMSRequestStatusFailureAcknowledged:
		// No action needed at this point. If the request has failed,
		// its message will become visible again after its timeout.
	case nvcav2beta1.ICMSRequestStatusInstancesInProgress:
		// Tasks should only be ack'd after caching + pods scheduled.
		if req.Status.LastACKTimestamp == nil || time.Since(req.Status.LastACKTimestamp.Time) < a.EffectiveICMSRequestACKInterval() {
			if !ackSR(ctx, req) {
				return
			}
			a.backendk8scache.eventRecorder.Event(req, v1.EventTypeNormal, string(types.EventCategoryInstanceStatusUpdate),
				"Request accepted for processing")

			modify := func(ctx context.Context, sr *nvcav2beta1.ICMSRequest) {
				sr.Status.LastACKTimestamp = &metav1.Time{Time: core.GetCurrentTime(ctx)}
			}
			if !a.backendk8scache.applyICMSRequestStatusChange(ctx, req, modify) {
				log.Error("Failed to update ICMSRequest status last ack time on task acknowledgement")
			}
		}

		// If ACK is successful, purge the message now
		err := a.queueManager.DeleteCreationMessageV2(ctx, req.Spec.MessageReceipt, req.Spec.CreationMsgInfo.QueueURL)
		if err != nil && !a.queueManager.Client.IsMessageNotFoundError(err) {
			log.WithError(err).
				WithFields(logrus.Fields{
					"receipt": req.Spec.MessageReceipt,
					"id":      req.Labels[SQSMessageIDKey],
				}).Warn("Failed to delete the request message")
		} else if err == nil {
			log.WithFields(logrus.Fields{
				"receipt": req.Spec.MessageReceipt,
				"id":      req.Labels[SQSMessageIDKey],
			}).Debug("Deleted message from creation queue")
		}
	case nvcav2beta1.ICMSRequestStatusCachingInProgress,
		nvcav2beta1.ICMSRequestStatusPending,
		nvcav2beta1.ICMSRequestStatusInProgress:
		// Extend message invisibility until task is scheduled or a timeout occurs.
		isCaching := req.Status.RequestStatus == nvcav2beta1.ICMSRequestStatusCachingInProgress
		mrd, err := nvcainternaltranslate.ParseMaxRuntimeDuration(req.Spec.CreationMsgInfo.TaskLaunchSpecification.MaxRuntimeDuration)
		if err != nil {
			log.WithField("maxRuntimeDurationStr", req.Spec.CreationMsgInfo.TaskLaunchSpecification.MaxRuntimeDuration).
				WithError(err).
				Error("Failed to parse max runtime duration, using default max runtime duration")
			mrd = a.K8sTimeConfig.MaxRunningTimeout
		}
		lsuTime := req.Status.LastStatusUpdated
		if lsuTime != nil && time.Since(lsuTime.Time).Seconds() >= nearVisTimeoutSeconds &&
			// Caching is not affected by the max runtime, so only time out for other statuses.
			(isCaching || time.Since(lsuTime.Time) < mrd) {
			err := a.queueManager.ExtendCreationMessableVisibilityTimeoutV2(ctx, req.Spec.MessageReceipt, req.Spec.CreationMsgInfo.QueueURL)
			if err != nil {
				if !a.queueManager.Client.IsMessageNotFoundError(err) {
					log.WithError(err).WithField("message_receipt", req.Spec.MessageReceipt).
						Warn("Failed to extend message visibility timeout")
				}
				return
			}
			a.backendk8scache.eventRecorder.Event(req, v1.EventTypeNormal, string(types.EventCategoryInstanceCreation),
				"Message visibility extended")

			modify := func(ctx context.Context, sr *nvcav2beta1.ICMSRequest) {
				sr.Status.LastStatusUpdated = &metav1.Time{Time: core.GetCurrentTime(ctx)}
			}
			if !a.backendk8scache.applyICMSRequestStatusChange(ctx, req, modify) {
				log.Error("Failed to update ICMSRequest status last updated time on message visibility extension")
			}
		}
		// Send ACK again only if it is less than ICMS request ACK interval
		if isCaching && (req.Status.LastACKTimestamp == nil || time.Since(req.Status.LastACKTimestamp.Time) < a.EffectiveICMSRequestACKInterval()) {
			if !ackSR(ctx, req) {
				return
			}
			modify := func(ctx context.Context, sr *nvcav2beta1.ICMSRequest) {
				sr.Status.LastACKTimestamp = &metav1.Time{Time: core.GetCurrentTime(ctx)}
			}
			if !a.backendk8scache.applyICMSRequestStatusChange(ctx, req, modify) {
				log.Error("Failed to update ICMSRequest status last ack time on caching task")
			}
		}
	default:
		// Initial creation case will have no status.
		modify := func(ctx context.Context, sr *nvcav2beta1.ICMSRequest) {
			now := core.GetCurrentTime(ctx)
			sr.Status.RequestStatus = nvcav2beta1.ICMSRequestStatusPending
			sr.Status.LastStatusUpdated = &metav1.Time{Time: now}
		}
		if !a.backendk8scache.applyICMSRequestStatusChange(ctx, req, modify) {
			log.Error("Failed to update ICMSRequest status to Pending")
		}
	}
}

func isICMSRequestAcknowledgementErrorRetryable(err error, req *nvcav2beta1.ICMSRequest, retryTimeout time.Duration) bool {
	return err != nil &&
		req != nil &&
		nvcaerrors.GetHTTPStatusCode(err) == http.StatusNotFound &&
		time.Since(req.CreationTimestamp.Time) < retryTimeout
}

func (a *Agent) handleStatusPreconditionFailed(ctx context.Context, req *nvcav2beta1.ICMSRequest) error {
	log := core.GetLogger(ctx)
	var err error

	// for Tasks we hold off ACK until after instances are created
	// so attempt best-effort cleanup here
	if a.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.AckTaskRequestAfterPodsScheduled) {
		iIDs := req.Status.GetInstanceIDs()
		if len(iIDs) != 0 {
			for i := range iIDs {
				lerr := a.handleInstanceStatusPreconditionFailure(ctx, req, iIDs[i])
				if lerr != nil {
					log.WithError(lerr).Warnf("failed to cleanup instance %v for %v", iIDs[i], req.Name)
				}
			}
		}
	}

	modify := func(_ context.Context, sr *nvcav2beta1.ICMSRequest) {
		sr.Status.RequestStatus = nvcav2beta1.ICMSRequestStatusFailureAcknowledged
	}
	if !a.backendk8scache.applyICMSRequestStatusChange(ctx, req, modify) {
		return fmt.Errorf("error updating ICMSRequest '%s' status to Pending", req.Name)
	}
	// cleanup artifacts for cache-in-progress job & rw-pvc
	err = a.backendk8scache.k8sArtifactHelper.(K8sComputeBackend).CleanupModelCachingSetupArtifacts(ctx, req)
	if err != nil {
		log.WithError(err).Warnf("failed to cleanup setup artifacts for %v", req.Name)
	}

	if a.ClusterTargetingEnabled {
		err = a.queueManager.DeleteCreationMessageV2(ctx, req.Spec.MessageReceipt, req.Spec.CreationMsgInfo.QueueURL)
	} else {
		err = a.queueManager.DeleteCreationMessage(ctx, req.Spec.CreationMsgInfo.GPUName, req.Spec.MessageReceipt)
	}
	if err != nil && !a.queueManager.Client.IsMessageNotFoundError(err) {
		log.WithError(err).WithField("receipt", req.Spec.MessageReceipt).
			Warn("Failed to delete message on precondition failure")
	} else if err == nil {
		log.WithField("receipt", req.Spec.MessageReceipt).Debug("Purged message from creation queue")
	}
	return nil
}

func (a *Agent) handleInstanceStatusPreconditionFailure(ctx context.Context, req *nvcav2beta1.ICMSRequest, instID string) error {
	err := a.backendk8scache.icmsRequestHelper.HandleInstanceStatusPreconditionFailure(ctx, req, instID)
	if err != nil {
		return err
	}
	log := core.GetLogger(ctx)
	updateRequest := &types.ICMSInstanceStatusUpdateRequest{
		Status:           types.ICMSRequestInstanceTerminatedByService,
		InstanceState:    types.ICMSInstanceTerminated,
		Action:           common.TerminationAction,
		RequestState:     types.ICMSInstanceRequestClosed,
		TerminationCause: types.ICMSInstanceTerminatedPreconditionFailure,
	}
	err = a.icmsClient.PostInstanceStatusUpdate(ctx, req.Spec.RequestID, instID, updateRequest)
	if err != nil {
		log.WithError(err).Errorf("failed to update ICMS instance %s terminated", instID)
	}

	return nil
}

func (a *Agent) handleInstanceStatusSyncAction(ctx context.Context, req *nvcav2beta1.ICMSRequest,
	is nvcav2beta1.InstanceStatus, ra ICMSInstanceReconcileState) error {
	var updateRequest *types.ICMSInstanceStatusUpdateRequest
	iStatus := types.ICMSInstanceTerminated
	action := common.TerminationAction
	rs := types.ICMSInstanceRequestClosed
	tc := types.ICMSInstanceTerminatedDuetoSyncAction

	if ra == ICMSInstanceReconcileUpdateOnly {
		iStatus = types.ICMSInstanceState(is.Status)
		action = common.FunctionCreationAction
		rs = types.ICMSInstanceRequestActive
		tc = types.ICMSInstanceStateNoStatus
	}
	updateRequest = &types.ICMSInstanceStatusUpdateRequest{
		Status:           types.ICMSRequestInstanceTerminatedByService,
		InstanceState:    iStatus,
		Action:           action,
		RequestState:     rs,
		TerminationCause: tc,
	}
	err := a.icmsClient.PostInstanceStatusUpdate(ctx, req.Spec.RequestID, is.ID, updateRequest)
	if err != nil {
		return fmt.Errorf("failed to update ICMS instance %s: %w", is.ID, err)
	}
	return nil
}

func (a *Agent) handleNotFoundInstanceStatusSyncAction(ctx context.Context, reqID, instanceID string, ra ICMSInstanceReconcileState) error {
	updateRequest := &types.ICMSInstanceStatusUpdateRequest{
		Status:           types.ICMSRequestInstanceTerminatedByService,
		InstanceState:    types.ICMSInstanceTerminated,
		Action:           common.TerminationAction,
		RequestState:     types.ICMSInstanceRequestClosed,
		TerminationCause: types.ICMSInstanceTerminatedDuetoSyncAction,
	}
	err := a.icmsClient.PostInstanceStatusUpdate(ctx, reqID, instanceID, updateRequest)
	if err != nil {
		return fmt.Errorf("failed to update not-found ICMS instance %s: %w", instanceID, err)
	}
	return nil
}

func getPostedInstanceStatus(ctx context.Context, isu types.ICMSRequestUpdateInfo) nvcav2beta1.InstanceStatus {
	return nvcav2beta1.InstanceStatus{
		ID:                    isu.InstanceID,
		LastReportedStatus:    string(isu.Payload.InstanceState),
		LastReportedTimestamp: &metav1.Time{Time: core.GetCurrentTime(ctx)},
	}
}

func getUpdatedInstanceStatusMap(currentStatus, postedStatus map[string]nvcav2beta1.InstanceStatus) map[string]nvcav2beta1.InstanceStatus {
	updatedStatus := make(map[string]nvcav2beta1.InstanceStatus)
	for id, cis := range currentStatus {
		if _, ok := postedStatus[id]; ok {
			cis.Status = postedStatus[id].LastReportedStatus
			cis.LastReportedStatus = postedStatus[id].LastReportedStatus
			cis.LastReportedTimestamp = postedStatus[id].LastReportedTimestamp
			updatedStatus[id] = cis
		}
	}
	return updatedStatus
}

func (a *Agent) PostICMSInstanceRequestStatusUpdates(ctx context.Context) error {
	allReqs, err := a.backendk8scache.icmsRequestLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("failed PostICMSInstanceRequestStatusUpdates, err: %v", err)
	}

	if a.instStatusThreadPool == nil {
		a.instStatusThreadPool = pool.New().WithMaxGoroutines(ICMSInstanceRequestStatusUpdatesMaxGoroutines)
	}

	ackTaskAfterScheduled := a.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.AckTaskRequestAfterPodsScheduled)

	for _, req := range allReqs {
		// Cannot mutate the object since it is in the cache, we must first perform a deep copy on it
		req = req.DeepCopy()
		a.instStatusThreadPool.Go(func() {
			// inject the logger with spotrequest logger fields
			// intentionally do not override parent context and logger
			// we want new ones per scope
			ctx := nvcalogging.WithICMSRequestFieldLogger(ctx, req)
			log := core.GetLogger(ctx)

			if req.Spec.Action == common.RequestICMSInstances || req.Spec.Action == common.FunctionCreationAction ||
				((req.Spec.Action == common.RequestICMSInstancesForTask || req.Spec.Action == common.TaskCreationAction) && !ackTaskAfterScheduled) {
				// purge message for the following statuses
				// this logic is as the message is purged for new requests after ACK is successful
				// for both clusterQ and clusterGroupQ
				// deprecated and will be removed in a follow-up release
				switch req.Status.RequestStatus {
				case nvcav2beta1.ICMSRequestStatusFailed, nvcav2beta1.ICMSRequestStatusInstancesInProgress, nvcav2beta1.ICMSRequestStatusCachingInProgress:
					if a.ClusterTargetingEnabled {
						if !a.IsRequestFromClusterQueue(ctx, req) {
							err = a.queueManager.DeleteCreationMessageV2(ctx, req.Spec.MessageReceipt, req.Spec.CreationMsgInfo.QueueURL)
						}
						// else skip purge as the message for clusterQ is purged after ACK
					} else {
						err = a.queueManager.DeleteCreationMessage(ctx, req.Spec.CreationMsgInfo.GPUName, req.Spec.MessageReceipt)
					}
					if err != nil && !a.queueManager.Client.IsMessageNotFoundError(err) {
						log.WithError(err).WithField("receipt", req.Spec.MessageReceipt).
							Warn("Failed to delete message")
					}
				}
			}
			// If tasks should not be ack'd until after pods are scheduled,
			// then request fulfillment should be skipped until 1) ack and 2) pods are scheduled.
			reqNotAcked := false
			if (req.Spec.Action == common.RequestICMSInstancesForTask || req.Spec.Action == common.TaskCreationAction) && ackTaskAfterScheduled {
				switch req.Status.RequestStatus {
				case nvcav2beta1.ICMSRequestStatusCachingInProgress, nvcav2beta1.ICMSRequestStatusPending, nvcav2beta1.ICMSRequestStatusInProgress:
					// An ack prior to InstancesInProgress is insufficient for request fulfillment
					// since pods are not scheduled yet.
					reqNotAcked = true
				case nvcav2beta1.ICMSRequestStatusInstancesInProgress:
					// Now that the request is InstancesInProgress, all pods are scheduled.
					// Any ack is sufficient for proceeding to request fulfillment,
					// whether it was during caching or after pods were scheduled.
					reqNotAcked = req.Status.LastACKTimestamp == nil
				}
			}

			reqUpdates, err := a.backendk8scache.icmsRequestHelper.GetICMSRequestStatusUpdatesForRequest(ctx, req)
			if err != nil {
				log.WithError(err).Errorf("failed in GetICMSRequestStatusUpdatesForRequest for req %v", req.Spec.RequestID)
				return
			}
			postedInstanceStatus := map[string]nvcav2beta1.InstanceStatus{}
			for _, ru := range reqUpdates {
				if ru.Payload.Status == types.ICMSRequestFulfilled && reqNotAcked {
					log.WithField("instanceId", ru.InstanceID).
						Debug("Request has not been acknowledged with instances scheduled, skipping fulfilled update")
					continue
				}

				// Copy to avoid memory aliasing with the ru variable in this for loop
				ruPayload := ru.Payload
				err := a.icmsClient.PostInstanceStatusUpdate(ctx, ru.RequestID, ru.InstanceID, &ruPayload)
				if err != nil {
					log.WithError(err).Errorf("failed to PostInstanceStatusUpdate for req %+v", ru)
					if nvcaerrors.GetHTTPStatusCode(err) == http.StatusPreconditionFailed {
						if err := a.handleInstanceStatusPreconditionFailure(ctx, req, ru.InstanceID); err != nil {
							log.WithError(err).Error("Failed to update req Status on StatusPreconditionFailed")
						}
					}
					continue
				}
				a.backendk8scache.eventRecorder.Eventf(req, v1.EventTypeNormal,
					string(types.EventCategoryInstanceStatusUpdate), "%v is %v", ru.InstanceID, ruPayload.InstanceState)
				// successfully posted this update so this has to be updated to Status
				postedInstanceStatus[ru.InstanceID] = getPostedInstanceStatus(ctx, ru)
			}
			var instanceStatusMap map[string]nvcav2beta1.InstanceStatus
			var nrs nvcav2beta1.RequestStatus
			crs := req.Status.RequestStatus
			if len(postedInstanceStatus) != 0 {
				instanceStatusMap = getUpdatedInstanceStatusMap(req.Status.Instances, postedInstanceStatus)
			} else {
				instanceStatusMap = req.Status.Instances
			}
			switch crs {
			case nvcav2beta1.ICMSRequestStatusCompleted:
				nrs = nvcav2beta1.ICMSRequestStatusCompletionAcknowledged
			case nvcav2beta1.ICMSRequestStatusFailed:
				nrs = nvcav2beta1.ICMSRequestStatusFailureAcknowledged
			default:
				nrs = crs
			}
			if crs != nrs || len(postedInstanceStatus) != 0 {
				modify := func(_ context.Context, sr *nvcav2beta1.ICMSRequest) {
					sr.Status.Instances = instanceStatusMap
					sr.Status.RequestStatus = nrs
				}
				if !a.backendk8scache.applyICMSRequestStatusChange(ctx, req, modify) {
					log.Errorf("Error updating Status for ICMSRequest '%s' from %v to %v status", req.Name, crs, nrs)
				}
			}
		})
	}
	return nil
}

func (a *Agent) RenewICMSQueueCreds(ctx context.Context) error {
	log := core.GetLogger(ctx)

	// Skip if components are not initialized yet (during startup)
	if a.queueManager == nil {
		log.Debug("Queue manager not initialized yet, skipping credential renewal")
		return nil
	}

	if a.icmsClient == nil {
		log.Debug("ICMS client not initialized yet, skipping credential renewal")
		return nil
	}

	if a.backendk8scache == nil {
		log.Debug("Backend K8s cache not initialized yet, skipping credential renewal")
		return nil
	}

	credRes, err := a.icmsClient.GetCreds(ctx)
	nvcametrics.FromContext(ctx).RecordUpstreamRequest(nvcametrics.UpstreamOperationCredentials, err)
	if err != nil {
		return fmt.Errorf("failed to GetCreds from ICMS, err: %v", err)
	}

	// TODO: this is a hack remove this once ICMS properly sends back the queue credentials
	queueCreds := a.postProcessQueueCredentials(ctx, credRes.QueueCredentials)

	err = a.backendk8scache.StoreUpdatedCredentials(ctx, queueCreds)
	if err != nil {
		return fmt.Errorf("failed to store renewed Queue Credentials, err: %v", err)
	}

	a.queueManager.updateQueues(queueCreds)

	log.Debugf("refreshed queueManager with new Creds")
	return nil
}

// evictAllWorkloads directly purges all workload instances and sends termination status updates to ICMS.
func (a *Agent) evictAllWorkloads(ctx context.Context) error {
	log := core.GetLogger(ctx).WithFields(logrus.Fields{
		"rpc": "Agent.evictAllWorkloads",
	})

	// Get all ICMS requests that are currently running.
	allReqs, err := a.backendk8scache.icmsRequestLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("failed to list ICMS requests: %w", err)
	}

	var evictionErrors []error

	for _, req := range allReqs {
		// Skip requests that are failed / failed-acked
		if req.Status.RequestStatus == nvcav2beta1.ICMSRequestStatusFailed ||
			req.Status.RequestStatus == nvcav2beta1.ICMSRequestStatusFailureAcknowledged {
			continue
		}

		// Skip requests with no instances
		if len(req.Status.Instances) == 0 {
			continue
		}

		// Cannot mutate the object since it is in the cache, we must first perform a deep copy on it
		req = req.DeepCopy()

		log.WithFields(logrus.Fields{
			"requestID": req.Spec.RequestID,
			"instances": len(req.Status.Instances),
		}).Info("Evicting workload instances during maintenance mode")

		// Create instance IDs list for termination
		var instanceIDs []string
		for instanceID := range req.Status.Instances {
			instanceIDs = append(instanceIDs, instanceID)
		}

		// Initialize terminatedInstances map with existing instances
		terminatedInstances := make(map[string]nvcav2beta1.InstanceStatus)
		if len(req.Status.Instances) != 0 {
			terminatedInstances = req.Status.Instances
		}

		// Use PurgeInstanceID to directly terminate each instance
		var terminatedCount int
		for _, instanceID := range instanceIDs {
			if a.backendk8scache.icmsRequestHelper.PurgeInstanceID(ctx, req, terminatedInstances, instanceID) {
				terminatedCount++
			}
		}

		if terminatedCount > 0 {
			log.WithFields(logrus.Fields{
				"requestID":       req.Spec.RequestID,
				"terminatedCount": terminatedCount,
				"totalInstances":  len(instanceIDs),
			}).Info("Successfully terminated instances during maintenance eviction")

			// Update the request status with the terminated instances
			req.Status.Instances = terminatedInstances
			req.Status.RequestStatus = nvcav2beta1.ICMSRequestStatusInProgress
			req.Status.LastStatusUpdated = &metav1.Time{Time: core.GetCurrentTime(ctx)}

			modify := func(_ context.Context, sr *nvcav2beta1.ICMSRequest) {
				sr.Status.RequestStatus = req.Status.RequestStatus
				sr.Status.LastStatusUpdated = req.Status.LastStatusUpdated
				sr.Status.Instances = req.Status.Instances
			}
			if !a.backendk8scache.applyICMSRequestStatusChange(ctx, req, modify) {
				log.WithFields(logrus.Fields{
					"requestID": req.Spec.RequestID,
				}).Error("Failed to update ICMS request status after instance termination")
				evictionErrors = append(evictionErrors, fmt.Errorf("failed to update ICMS request status for %s", req.Spec.RequestID))
			}
		}

		// Send termination status updates to ICMS for each terminated instance.
		for _, instanceID := range instanceIDs {
			updateRequest := &types.ICMSInstanceStatusUpdateRequest{
				Status:           types.ICMSRequestInstanceTerminatedByService,
				InstanceState:    types.ICMSInstanceTerminated,
				Action:           common.TerminationAction,
				RequestState:     types.ICMSInstanceRequestClosed,
				TerminationCause: types.ICMSInstanceTerminatedServiceMaintenance,
			}

			if err := a.icmsClient.PostInstanceStatusUpdate(ctx, req.Spec.RequestID, instanceID, updateRequest); err != nil {
				log.WithError(err).WithFields(logrus.Fields{
					"requestID":  req.Spec.RequestID,
					"instanceID": instanceID,
				}).Error("Failed to send termination status update to ICMS during eviction")
				evictionErrors = append(evictionErrors, fmt.Errorf("failed to send termination status update for %s/%s: %w", req.Spec.RequestID, instanceID, err))
			} else {
				log.WithFields(logrus.Fields{
					"requestID":  req.Spec.RequestID,
					"instanceID": instanceID,
				}).Info("Successfully sent termination status update to ICMS during eviction")
			}
		}
	}

	if len(evictionErrors) > 0 {
		return fmt.Errorf("encountered %d errors during workload eviction: %v", len(evictionErrors), evictionErrors)
	}

	log.Info("Successfully completed eviction of all workloads during maintenance mode")
	return nil
}

// handleSelfDestruct implements the self-destruct sequence when instructed by ICMS.
// It evicts all workloads and stops ICMS communication (except termination updates during eviction).
func (a *Agent) handleSelfDestruct(ctx context.Context) error {
	log := core.GetLogger(ctx).WithFields(logrus.Fields{
		"rpc": "Agent.handleSelfDestruct",
	})

	log.Warn("Starting self-destruct sequence - evicting workloads and stopping ICMS communication")

	// Step 1: Evict all existing workloads (termination updates will still be sent to ICMS).
	log.Info("Evicting all existing workloads")
	if err := a.evictAllWorkloads(ctx); err != nil {
		log.WithError(err).Error("Failed to evict workloads during self-destruct")
		// Continue with self-destruct even if eviction fails partially
	} else {
		log.Info("Successfully evicted all workloads")
	}

	// Step 2: Mark agent as self-destructed (this will stop further ICMS communication).
	a.selfDestruct.Store(true)
	log.Debugf("Self-destruct mode active: skipping events %v", SkippedEventsInSelfDestructMode)
	log.Info("Self-destruct sequence completed - agent will no longer communicate with ICMS")
	return nil
}

// postProcessQueueCredentials post-processes the queue credentials based on the feature flag.
// If the feature flag is enabled, it will return the queue credentials for the NATS queue.
// Otherwise, it will return the queue credentials for the AWS queue.
func (a *Agent) postProcessQueueCredentials(ctx context.Context, creds types.QueueCredentials) types.QueueCredentials {
	// if the feature flag is enabled and the queue credentials are empty, then post-process the queue credentials
	// this allows ICMS to fix things on its end and we'll automatically pick up the new credentials
	if a.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.SelfHosted) &&
		cmp.Equal(creds, types.QueueCredentials{}, cmpopts.EquateEmpty()) {
		core.GetLogger(ctx).Debug("SelfHosted feature flag enabled, post-processing queue credentials")
		creds = types.QueueCredentials{}
		creds.CreationQueues = types.CreationQueueInfoSet{}
		creds.ClusterCreationQueues = types.CreationQueueInfoSet{}
		creds.TaskClusterCreationQueues = types.CreationQueueInfoSet{}
		creds.TerminationQueue = queue.MessageQueueInfo{
			QueueType: queue.TerminationQueue,
			QueueURL:  natsqueue.TermStreamName,
		}

		// Get a unique set of GPUs from the health status cache
		for gpuName := range a.backendHealthCache.GetStatus().GPUUsage {
			creds.ClusterCreationQueues[gpuName] = queue.MessageQueueInfo{
				GPU:       string(gpuName),
				QueueType: queue.CreationQueue,
				QueueURL:  natsqueue.CreateStreamName,
			}
		}
	}
	return creds
}
