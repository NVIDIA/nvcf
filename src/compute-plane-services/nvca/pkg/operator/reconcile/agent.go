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

package operator

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	cmnotel "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/otel"
	cmnsecret "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/secret"
	nvversion "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/version"
	"github.com/prometheus/client_golang/prometheus"
	otelattr "go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/workqueue"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned/scheme"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/cleanup"

	nvcaoperatorerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/errors"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/kubeclients"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/metrics"
	nvcaoptel "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/otel"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/reconcile/clustermgmt"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
)

var (
	SyncNVCFBackendInterval = 15 * time.Second
	SyncNVCACRDInterval     = 30 * time.Minute
)

// Controller tick types.
const (
	EventTickSyncNVCFBackend  string = "TICK_SYNC_NVCF_BACKEND"
	EventTickFetchNVCACluster string = "TICK_FETCH_NVCA_CLUSTER"
	EventTickSyncNVCACRDS     string = "TICK_SYNC_NVCA_CRDS"
)

// getAgentEvents are all the possibly event names for the agent
func getAgentEvents() []string {
	// with no constants to avoid this being accidentally modified
	// we put this in a function
	return []string{
		EventTickSyncNVCFBackend,
		EventTickFetchNVCACluster,
		EventTickSyncNVCACRDS,
	}
}

type AgentOptions struct {
	NCAID              string
	KubeConfigPath     string
	SvcAddress         string
	AdminAddr          string
	ShutdownAddr       string
	PodName            string
	PodNamespace       string
	DeploymentName     string
	SystemNamespace    string
	K8sVersionOverride string
	PriorityClassName  string
	ClusterName        string
	ClusterSource      nvcaoptypes.ClusterSource
	NodeSelectorKey    string
	NodeSelectorValue  string

	// NVCFClusterID when specified enables the cluster management
	NVCFClusterID                 string
	NVCAClusterManagementAPIURL   string
	NVCAClusterAPIRefreshInterval time.Duration
	NVCAImageRepo                 string

	// uid & gid to override
	NVCARunAsUserID  int64
	NVCARunAsGroupID int64

	// gxcache Namespace
	GXCacheNamespace     string
	HelmRepositoryPrefix string

	// crowdstrike namespace
	CrowdstrikeNamespace string

	// If true, set up NVCA to support GXCache
	EnableGXCache bool

	// DDCS IP AllowList string slice
	DDCSIPAllowList []string

	// K8s Cluster Network CIDRs string slice
	K8sClusterNetworkCIDRs []string

	// Resource requirements for the agent and webhook containers
	AgentResources         corev1.ResourceRequirements
	WebhookResources       corev1.ResourceRequirements
	OTelCollectorResources corev1.ResourceRequirements

	// NVCA cache mount options configuration
	NVCACacheMountOptionsEnabled bool
	NVCACacheMountOptions        string
	NVCAWorkerDegradationPeriod  time.Duration
	NVCAWorkloadTolerations      []corev1.Toleration
	// NVCAAgentTolerations is the helm-supplied fallback for NVCA agent pod
	// tolerations, used when the NVCFBackend CR does not surface them
	// (notably ngc-managed clusters whose CR is sourced from the NGC API).
	NVCAAgentTolerations []corev1.Toleration

	// NVCA secret mirroring configuration
	NVCASecretMirrorSourceNamespace string
	NVCASecretMirrorLabelSelector   string

	// TokenFetcher is passed in from the operator to the agent
	TokenFetcher cmnsecret.TokenFetcher

	// GenerateImagePullSecret is passed in from the operator to the agent, if true,
	// the agent will generate an image pull secret for nvca Pods
	// using the NGC service key.
	GenerateImagePullSecret bool

	// AdditionalImagePullSecrets is a list of pre-existing imagePullSecret names
	// in the operator namespace that will be copied to the nvca-system namespace
	// and used by nvca Pods.
	AdditionalImagePullSecrets []corev1.LocalObjectReference

	// OTel collector configuration
	OTelCollectorImageRepo string
	OTelCollectorImageTag  string
	OTelCollectorEnabled   bool

	// NVCA tracing configuration propagated into the managed NVCA deployment.
	NVCAOTELConfig *nvidiaiov1.OTELConfig

	// If true, generate an NVCA config file instead of using CLI args and env variables
	CreateAgentConfig bool

	// Environment variable overrides for workloads
	// These are base64-encoded JSON maps of env var key-value pairs
	FunctionEnvOverridesB64 string
	TaskEnvOverridesB64     string

	// AgentOverrideEnvVars is a map of environment variables to override
	// on the NVCA agent container. These take precedence over default values.
	AgentOverrideEnvVars map[string]string

	// IdentitySource controls the identity mechanism for self-managed clusters.
	// Allowed values: "auto" (default), "psat", "spire".
	IdentitySource string

	// VaultOAuthClientMountPathTemplate is the template for constructing the
	// Vault OAuth client mount path. Use %s as placeholder for clientID.
	VaultOAuthClientMountPathTemplate string
}

type Agent struct {
	*AgentOptions

	metricsName string

	ctx                            context.Context
	numDispatchers                 int
	getTickerEventsFunc            func(ctx context.Context) <-chan *core.Event
	getBackendK8sKubeClientsChFunc func(ctx context.Context) <-chan *core.KubeClients

	backendk8scache *BackendK8sCache

	// event queue processing
	resourceEventWorkerQueues map[string]workqueue.TypedRateLimitingInterface[any]

	// OTel tracer initialized at agent startup
	tracer oteltrace.Tracer

	// clusterMgmtClient is the client used for interacting with cluster management
	clusterMgmtClient clustermgmt.Client

	// cancelTickers is used to stop the ticker event producers during shutdown
	cancelTickers context.CancelFunc

	// eventProcessingStopped indicates that event processing has been stopped
	eventProcessingStopped *atomic.Bool
}

func (o *AgentOptions) sanitizedString() string {
	var sanitized AgentOptions
	sanitized.KubeConfigPath = o.KubeConfigPath
	sanitized.SvcAddress = o.SvcAddress
	sanitized.AdminAddr = o.AdminAddr
	sanitized.ShutdownAddr = o.ShutdownAddr
	sanitized.SystemNamespace = o.SystemNamespace
	sanitized.K8sVersionOverride = o.K8sVersionOverride
	sanitized.ClusterName = o.ClusterName
	sanitized.ClusterSource = o.ClusterSource
	sanitized.PriorityClassName = o.PriorityClassName
	sanitized.NodeSelectorKey = o.NodeSelectorKey
	sanitized.NodeSelectorValue = o.NodeSelectorValue
	sanitized.NCAID = o.NCAID
	sanitized.NVCFClusterID = o.NVCFClusterID
	sanitized.NVCAClusterManagementAPIURL = o.NVCAClusterManagementAPIURL
	sanitized.NVCAClusterAPIRefreshInterval = o.NVCAClusterAPIRefreshInterval
	sanitized.NVCAImageRepo = o.NVCAImageRepo
	sanitized.NVCARunAsUserID = o.NVCARunAsUserID
	sanitized.NVCARunAsGroupID = o.NVCARunAsGroupID
	sanitized.GXCacheNamespace = o.GXCacheNamespace
	sanitized.CrowdstrikeNamespace = o.CrowdstrikeNamespace
	sanitized.HelmRepositoryPrefix = o.HelmRepositoryPrefix
	sanitized.EnableGXCache = o.EnableGXCache
	sanitized.DDCSIPAllowList = o.DDCSIPAllowList
	sanitized.K8sClusterNetworkCIDRs = o.K8sClusterNetworkCIDRs
	sanitized.AgentResources = o.AgentResources
	sanitized.WebhookResources = o.WebhookResources
	sanitized.NVCACacheMountOptionsEnabled = o.NVCACacheMountOptionsEnabled
	sanitized.NVCACacheMountOptions = o.NVCACacheMountOptions
	sanitized.NVCAWorkerDegradationPeriod = o.NVCAWorkerDegradationPeriod
	sanitized.NVCAWorkloadTolerations = append([]corev1.Toleration(nil), o.NVCAWorkloadTolerations...)
	sanitized.NVCAAgentTolerations = append([]corev1.Toleration(nil), o.NVCAAgentTolerations...)
	sanitized.NVCASecretMirrorSourceNamespace = o.NVCASecretMirrorSourceNamespace
	sanitized.NVCASecretMirrorLabelSelector = o.NVCASecretMirrorLabelSelector
	sanitized.GenerateImagePullSecret = o.GenerateImagePullSecret
	sanitized.OTelCollectorImageRepo = o.OTelCollectorImageRepo
	sanitized.OTelCollectorImageTag = o.OTelCollectorImageTag
	sanitized.OTelCollectorEnabled = o.OTelCollectorEnabled
	sanitized.OTelCollectorResources = o.OTelCollectorResources
	if o.NVCAOTELConfig != nil {
		sanitized.NVCAOTELConfig = &nvidiaiov1.OTELConfig{
			Exporter:    o.NVCAOTELConfig.Exporter,
			ServiceName: o.NVCAOTELConfig.ServiceName,
		}
	}
	return fmt.Sprintf("%#v", sanitized)
}

// validateCIDRs validates that all strings in the slice are valid CIDR notations
func validateCIDRs(cidrs []string) error {
	for _, cidr := range cidrs {
		_, _, err := net.ParseCIDR(cidr)
		if err != nil {
			return fmt.Errorf("invalid CIDR %q: %w", cidr, err)
		}
	}
	return nil
}

func NewAgent(ctx context.Context, opts *AgentOptions) (*Agent, error) {
	log := core.GetLogger(ctx)
	if opts.SystemNamespace == "" {
		log.Error("SystemNamespace required for Agent")
		return nil, errors.New("SystemNamespace required for Agent")
	}

	// Validate DDCSIPAllowList if provided
	if len(opts.DDCSIPAllowList) > 0 {
		if err := validateCIDRs(opts.DDCSIPAllowList); err != nil {
			log.WithError(err).Error("invalid DDCSIPAllowList")
			return nil, err
		}
	}

	// Validate K8sClusterNetworkCIDRs if provided
	if len(opts.K8sClusterNetworkCIDRs) > 0 {
		if err := validateCIDRs(opts.K8sClusterNetworkCIDRs); err != nil {
			log.WithError(err).Error("invalid K8sClusterNetworkCIDRs")
			return nil, err
		}
	}

	a := &Agent{
		ctx:                    ctx,
		metricsName:            "nvca_operator",
		AgentOptions:           opts,
		numDispatchers:         1,
		tracer:                 nvcaoptel.NewTracer(opts),
		eventProcessingStopped: &atomic.Bool{},
	}
	a.getTickerEventsFunc = a.getTickerEvents
	a.getBackendK8sKubeClientsChFunc = a.getBackendK8sKubeClientsCh
	return a, nil
}

func (o *AgentOptions) GetOTelAttributes() []otelattr.KeyValue {
	return []otelattr.KeyValue{
		otelattr.String(nvcaoptel.NCAIDAttributeKey, o.NCAID),
		otelattr.String(nvcaoptel.ClusterNameAttributeKey, o.ClusterName),
		otelattr.String(nvcaoptel.VersionAttributeKey, nvversion.Version),
	}
}

func (a *Agent) getBackendK8sKubeClientsCh(ctx context.Context) <-chan *core.KubeClients {
	log := core.GetLogger(ctx)
	configurator := core.NewPathKubeConfigurator().WithPath(a.KubeConfigPath)
	configCh := configurator.Start(ctx)
	log.Infof("Configuring Edge K8s kube clients from kubeconfig path %q ...", a.KubeConfigPath)
	return core.NewKubeClientsStream().WithConfigCh(configCh).Start(ctx)
}

func (a *Agent) newKubeClients(ctx context.Context, path string) (*kubeclients.KubeClients, error) {
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

	dynClient, err := newDynamicClient(nil, coreClients.Config)
	if err != nil {
		log.WithError(err).Error("Failed to configure dynamic k8s client")
		return nil, err
	}
	discClient, err := newDiscoverClient(nil, coreClients.Config)
	if err != nil {
		log.WithError(err).Error("Failed to configure dynamic k8s client")
		return nil, err
	}

	backendK8sClients, err := kubeclients.NewFromCore(coreClients, dynClient, discClient)
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

	// Create a child context for tickers so they can be cancelled independently
	tickerCtx, cancel := context.WithCancel(ctx)
	a.cancelTickers = cancel

	log.Infof("Configuring ticker event streams ...")
	ts := core.NewTickerStream().WithRandomOffset(true)
	tickerStreams := []*core.TickerStream{
		ts.WithKind(EventTickSyncNVCACRDS).WithInterval(SyncNVCACRDInterval),
		ts.WithKind(EventTickSyncNVCFBackend).WithInterval(SyncNVCFBackendInterval),
		ts.WithKind(EventTickFetchNVCACluster).WithInterval(a.NVCAClusterAPIRefreshInterval),
	}

	merger := core.NewEventStreamMerger().WithBufferSize(100)
	eventChs := []<-chan *core.Event{}
	for _, s := range tickerStreams {
		ch := s.Start(tickerCtx)
		eventChs = append(eventChs, ch)
		log.Debugf("Added ticker event stream: %+v", s)
	}
	return merger.Merge(tickerCtx, eventChs...)
}

func (a *Agent) Start(ctx context.Context) error {
	log := core.GetLogger(ctx)
	// agentStarted is used to ensure the agent handlers don't fire too early
	agentStarted := &atomic.Bool{}
	agentStarted.Store(false)
	var backendk8scache *BackendK8sCache

	log.Infof("Starting NVCAOperator %s with options: %+v", nvversion.Version, a.AgentOptions.sanitizedString()) //nolint:staticcheck

	// Initialize the default metrics in the context
	ctx = metrics.WithDefaultMetrics(ctx, a.metricsName,
		[]string{a.NCAID, a.ClusterName, nvversion.Version})

	merger := core.NewEventStreamMerger().WithBufferSize(100)

	// Init per event queue and workers
	if err := a.startEventProcessingWorkers(ctx); err != nil {
		log.WithError(err).Error("failed to start event processing workers")
		return err
	}

	log.Debug("Adding ticker events ...")
	tickerEventsCh := a.getTickerEventsFunc(ctx)

	log.Debug("Adding Backend K8s cache and events ...")
	backendK8sClients, err := a.newKubeClients(ctx, a.KubeConfigPath)
	if err != nil {
		return err
	}

	// TODO: source this directly from values and default URL's based on that.
	envType := nvidiaiov1.EnvTypeProd
	if strings.Contains(a.NVCAClusterManagementAPIURL, ".stg.") {
		envType = nvidiaiov1.EnvTypeStage
	}
	log.Debug("Received kubeclients for backend K8s, configuring backendk8scache")
	// Construct AgentConfig from parameters
	agentConfig := nvidiaiov1.AgentConfig{
		DeploymentConfig: nvidiaiov1.DeploymentConfig{
			PriorityClassName:           a.PriorityClassName,
			NodeSelectorKey:             a.NodeSelectorKey,
			NodeSelectorValue:           a.NodeSelectorValue,
			SecretMirrorSourceNamespace: a.NVCASecretMirrorSourceNamespace,
			SecretMirrorLabelSelector:   a.NVCASecretMirrorLabelSelector,
			GenerateImagePullSecret:     a.GenerateImagePullSecret,
			AdditionalImagePullSecrets:  a.AdditionalImagePullSecrets,
			OverrideEnvironmentVars:     a.AgentOverrideEnvVars, // nil if not specified
			Tolerations:                 append([]corev1.Toleration(nil), a.NVCAAgentTolerations...),
		},
		NVCFWorkerConfig: nvidiaiov1.NVCFWorkerConfig{
			CacheMountOptionsEnabled: a.NVCACacheMountOptionsEnabled,
			CacheMountOptions:        a.NVCACacheMountOptions,
			WorkerDegradationPeriod:  a.NVCAWorkerDegradationPeriod,
		},
		AgentResources:   &a.AgentResources,
		WebhookResources: &a.WebhookResources,
	}

	backendk8scache, _, err = NewBackendK8sCacheBuilder().
		WithClients(backendK8sClients).
		WithSystemNamespace(a.SystemNamespace).
		WithK8sVersionOverride(a.K8sVersionOverride).
		WithNGCServiceKeyFetcher(a.TokenFetcher).
		WithNVCAImageRepo(a.NVCAImageRepo).
		WithUIDGIDOverride(a.NVCARunAsUserID, a.NVCARunAsGroupID).
		WithGXCache(a.GXCacheNamespace, a.EnableGXCache).
		WithCrowdstrike(a.CrowdstrikeNamespace).
		WithHelmRepositoryPrefix(a.HelmRepositoryPrefix).
		WithDDCSIPAllowList(a.DDCSIPAllowList).
		WithK8sClusterNetworkCIDRs(a.K8sClusterNetworkCIDRs).
		WithAgentConfig(agentConfig).
		WithEnvType(envType).
		WithDispatchReconcileClusterFunc(a.dispatchReconcileCluster).
		WithContainerResources(a.AgentResources, a.WebhookResources, a.OTelCollectorResources).
		WithNVCFWorkerConfig(a.NVCACacheMountOptionsEnabled, a.NVCACacheMountOptions, a.NVCAWorkerDegradationPeriod).
		WithWorkloadTolerations(a.NVCAWorkloadTolerations).
		WithGenerateImagePullSecret(a.GenerateImagePullSecret).
		WithAdditionalImagePullSecrets(a.AdditionalImagePullSecrets).
		WithOTelCollectorConfig(a.OTelCollectorImageRepo, a.OTelCollectorImageTag, a.OTelCollectorEnabled).
		WithNVCAOTELConfig(a.NVCAOTELConfig).
		WithEnvOverrides(a.FunctionEnvOverridesB64, a.TaskEnvOverridesB64).
		WithIdentitySource(a.IdentitySource).
		WithClusterSource(a.ClusterSource).
		Start(ctx)

	if err != nil {
		log.WithError(err).Error("failed to configure the Backend K8s Cache")
		return err
	}

	// Initialize the cluster management client for fetching NVCA clusters if needed
	switch a.ClusterSource {
	case nvcaoptypes.ClusterSourceNGCManaged:
		clusterMgmtClient, err := clustermgmt.NewNGCManagedClient(ctx, a.TokenFetcher, AppName,
			envType,
			clustermgmt.WithRootNGCAPIURL(a.NVCAClusterManagementAPIURL),
			clustermgmt.WithVaultOAuthClientMountPathTemplate(a.VaultOAuthClientMountPathTemplate))
		if err != nil {
			log.WithError(err).Error("failed to create the cluster management client")
			return err
		}
		a.clusterMgmtClient = clusterMgmtClient
	case nvcaoptypes.ClusterSourceHelmManaged:
		a.clusterMgmtClient = clustermgmt.NewHelmManagedClient(
			envType,
			func(ctx context.Context) (*corev1.ConfigMap, error) {
				return backendK8sClients.K8s.CoreV1().ConfigMaps(a.SystemNamespace).
					Get(ctx, nvcfBackendHelmManagedConfigMapName, metav1.GetOptions{})
			},
			a.VaultOAuthClientMountPathTemplate,
		)
	case nvcaoptypes.ClusterSourceSelfHosted:
		a.clusterMgmtClient = clustermgmt.NewSelfManagedClient(
			envType,
			func(ctx context.Context) (*corev1.ConfigMap, error) {
				return backendK8sClients.K8s.CoreV1().ConfigMaps(a.SystemNamespace).
					Get(ctx, nvcfBackendSelfManagedConfigMapName, metav1.GetOptions{})
			},
			func(ctx context.Context) (*corev1.ConfigMap, error) {
				return backendK8sClients.K8s.CoreV1().ConfigMaps(a.SystemNamespace).
					Get(ctx, NVCAClusterRegistrationConfigMapName, metav1.GetOptions{})
			},
			a.VaultOAuthClientMountPathTemplate,
		)
	default:
		return fmt.Errorf("unsupported cluster-source: %s", a.ClusterSource)
	}

	log.Infof("Added backendk8scache and events!")
	if a.backendk8scache == nil {
		a.backendk8scache = backendk8scache
	}

	// Start the sentinel watcher for graceful shutdown
	sentinelWatcher := cleanup.NewSentinelWatcher(a.SystemNamespace, backendK8sClients.K8s, a.StopEventProcessing)
	go sentinelWatcher.Start(ctx)

	eventQueue := merger.Merge(ctx, tickerEventsCh)
	for i := 0; i < a.numDispatchers; i++ {
		go a.startDispatcher(ctx, eventQueue)
	}

	server := core.NewHTTPService(a.SvcAddress)
	server.Use(core.NewHTTPMiddleware(ctx,
		core.WithRequestMetrics(a.metricsName),
		core.WithHandlerTimeout(5*time.Second))...)

	// Provides /healthz, /version, /metrics
	server.AddHealthRoute(ctx)
	server.AddVersionRoute(ctx)
	server.AddMetricsRoute(ctx)

	_, err = server.Start(ctx)
	if err != nil {
		log.WithError(err).Errorf("failed to start server")
		return err
	}

	// Provides /admin
	adminServer := core.NewHTTPService(a.AdminAddr)
	adminServer.Use(core.NewHTTPMiddleware(ctx, core.WithHandlerTimeout(5*time.Second))...)
	adminServer.AddAdminRoute(ctx)
	_, err = adminServer.Start(ctx)
	if err != nil {
		log.WithError(err).Error("failed to start adminServer")
		return err
	}

	// Get deployment name for RBAC resource names (Helm uses nvcaop.fullname for all RBAC resources)
	deploymentName := a.DeploymentName
	if deploymentName == "" {
		// Fallback to discovery if not provided (for backwards compatibility)
		var err error
		deploymentName, err = discoverDeploymentName(ctx, backendK8sClients, a.PodName, a.PodNamespace)
		if err != nil {
			log.WithError(err).Error("Failed to discover deployment name")
			return fmt.Errorf("failed to discover deployment name: %w", err)
		}
		if deploymentName == "" && a.PodName != "" {
			log.Error("Could not discover deployment name from pod owner references")
			return errors.New("could not discover deployment name: pod has no deployment owner")
		}
	}

	// Provides /shutdown on a separate port accessible by kubelet preStop hook
	shutdownServer := core.NewHTTPService(a.ShutdownAddr)
	// Extend timeouts for shutdown endpoint. Must be less than terminationGracePeriodSeconds
	// to ensure the response is sent before K8s kills the pod.
	shutdownServer.WriteTimeout = 9 * time.Minute
	shutdownServer.Use(core.NewHTTPMiddleware(ctx, core.WithHandlerTimeout(9*time.Minute))...)
	shutdownHandler := cleanup.NewShutdownHandler(ctx, cleanup.ShutdownHandlerOptions{
		K8sClient:     backendK8sClients.K8s,
		NVCAClient:    backendK8sClients.NVCAOP,
		DynamicClient: backendK8sClients.DynamicClient,
		Namespace:     a.SystemNamespace,
		OnShutdown:    a.StopEventProcessing,
		// SetGracefulShutdown prevents the reconciliation loop from running cleanup
		// when it sees NVCFBackend has a deletion timestamp during graceful shutdown.
		SetGracefulShutdown: func(shutdown bool) {
			if a.backendk8scache != nil {
				a.backendk8scache.SetGracefulShutdown(shutdown)
			}
		},
		// RBAC resources use the same name as the deployment (set by Helm's nvcaop.fullname)
		ClusterRoleName:        deploymentName,
		ClusterRoleBindingName: deploymentName,
		ServiceAccountName:     deploymentName,
	})
	shutdownServer.HandleFunc("/shutdown", shutdownHandler).Methods("GET")
	_, err = shutdownServer.Start(ctx)
	if err != nil {
		log.WithError(err).Error("failed to start shutdownServer")
		return err
	}

	// Mark the agent as started
	agentStarted.Store(true)

	return nil
}

func (a *Agent) dispatchReconcileCluster(ctx context.Context) {
	log := core.GetLogger(ctx)

	if a.ClusterSource != nvcaoptypes.ClusterSourceHelmManaged {
		log.Debug("cluster source is not helm managed, skipping reconcile")
		return
	}

	if a.backendk8scache == nil {
		log.Debug("backendk8scache is not initialized yet, skipping reconcile")
		return
	}

	a.dispatch(ctx, &core.Event{Kind: EventTickFetchNVCACluster})
}

func (a *Agent) startDispatcher(ctx context.Context, queue <-chan *core.Event) {
	// start event broadcaster for backendk8s
	if a.backendk8scache != nil {
		a.backendk8scache.eventBroadcaster.StartStructuredLogging(0)
		a.backendk8scache.eventBroadcaster.StartRecordingToSink(&v1core.EventSinkImpl{Interface: a.backendk8scache.clients.K8s.CoreV1().Events("")})
		defer a.backendk8scache.eventBroadcaster.Shutdown()
	}
	for {
		select {
		case ev, ok := <-queue:
			if !ok {
				// Channel closed, exit dispatcher
				return
			}
			if ev == nil {
				continue
			}
			a.dispatch(ctx, ev)
		case <-ctx.Done():
			return
		}
	}
}

func (a *Agent) dispatch(ctx context.Context, ev *core.Event) {
	log := core.GetLogger(ctx)
	metrics := metrics.FromContext(ctx)

	if queue, ok := a.resourceEventWorkerQueues[ev.Kind]; ok {
		if ev.ObjectMetaKey != "" {
			queue.Add(ev.ObjectMetaKey)
		} else {
			queue.Add(ev)
		}
		metrics.EventQueueLength.WithLabelValues(metrics.WithDefaultLabelValues(ev.Kind)...).Set(float64(queue.Len()))
	} else {
		log.Errorf("No worker queue found for event type %s %v", ev.Kind, *ev)
	}
}

func (a *Agent) startEventProcessingWorkers(ctx context.Context) error {
	log := core.GetLogger(ctx)

	// Guard to prevent this being called twice
	if a.resourceEventWorkerQueues != nil {
		return errors.New("eventQueue initialization cannot be called twice")
	}

	// create and initialize queues before start goroutines
	// to aovid a data race
	a.resourceEventWorkerQueues = make(map[string]workqueue.TypedRateLimitingInterface[any])
	for _, eventName := range getAgentEvents() {
		a.resourceEventWorkerQueues[eventName] =
			workqueue.NewTypedRateLimitingQueueWithConfig(
				workqueue.DefaultTypedItemBasedRateLimiter[any](),
				workqueue.TypedRateLimitingQueueConfig[any]{Name: eventName})
	}

	// Start the worker threads now that resourceEventWorkerQueues has been initialized
	for evtName, eventWorkQueue := range a.resourceEventWorkerQueues {
		log.Infof("Created workqueue and worker for event %s", evtName)
		go func(ctx context.Context, q workqueue.TypedRateLimitingInterface[any], eventName string) {
			log.Infof("Started worker for event %s", eventName)
			metrics := metrics.FromContext(ctx)
			for {
				// Obtain event and process
				obj, shutdown := q.Get()
				if shutdown {
					log.Infof("Workqueue shutdown, stop worker for %s", eventName)
					return
				}
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
				err := a.apply(ctx, ev)
				q.Done(obj)

				// requeue on error for per-object events
				if nvcaoperatorerrors.IsFatal(err) {
					nvcaoperatorerrors.ExitReason(ctx, err)
					log.WithError(err).Fatalf("%v failed, will not be requeued, NVCA operator will exit", ev.Kind)
				} else if err != nil && ev.ObjectMetaKey != "" {
					if k8serr.IsNotFound(err) {
						log.Infof("%v for ObjectMetaKey %v not found, skip requeue", ev.Kind, ev.ObjectMetaKey)
					} else {
						log.WithError(err).Errorf("%v failed for ObjectMetaKey %v, will be requeued", ev.Kind, ev.ObjectMetaKey)
						q.AddRateLimited(ev.ObjectMetaKey)
					}
				} else if err != nil {
					log.WithError(err).Errorf("%v failed, will not be requeued", ev.Kind)
				}
			}
		}(ctx, eventWorkQueue, evtName)
	}

	// goroutine to ensure the event processing workers are properly stopped
	go func(ctx context.Context) {
		<-ctx.Done()
		a.stopEventProcessingWorkers(ctx)
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

// StopEventProcessing stops both the ticker event producers and the event processing workers.
// This is called during graceful shutdown when the sentinel ConfigMap is being deleted.
func (a *Agent) StopEventProcessing(ctx context.Context) {
	log := core.GetLogger(ctx)

	// Guard against multiple calls
	if a.eventProcessingStopped.Swap(true) {
		log.Debug("Event processing already stopped")
		return
	}

	log.Info("Stopping event processing for graceful shutdown...")

	// Stop ticker event producers first
	if a.cancelTickers != nil {
		log.Info("Cancelling ticker event producers")
		a.cancelTickers()
	}

	// Then stop the event processing workers
	a.stopEventProcessingWorkers(ctx)

	log.Info("Event processing stopped")
}

// ShutdownResponse is the JSON response from the /shutdown endpoint
// apply executes for each event in the queue
func (a *Agent) apply(ctx context.Context, ev *core.Event) error {
	log := core.GetLogger(ctx)

	// Skip all reconciliation if event processing has been stopped
	if a.eventProcessingStopped.Load() {
		log.Debugf("Event processing stopped, skipping event: %v", ev.Kind)
		return nil
	}

	metrics := metrics.FromContext(ctx)
	timer := prometheus.NewTimer(metrics.EventProcessLatency.WithLabelValues(metrics.WithDefaultLabelValues(ev.Kind)...))
	defer timer.ObserveDuration()

	return cmnotel.InvokeWithSpan(ctx, a.tracer, fmt.Sprintf("nvca-operator.apply.%s", ev.Kind),
		func(ctx context.Context) error {
			var err error
			switch ev.Kind {
			case EventTickSyncNVCFBackend:
				err = a.backendk8scache.SyncAllNVCFBackends(ctx)
			case EventTickFetchNVCACluster:
				err = a.reconcileNVCACluster(ctx, a.NVCFClusterID)
			case EventTickSyncNVCACRDS:
				err = a.backendk8scache.setupCRDs(ctx)
			}
			if err != nil {
				log.WithError(err).Errorf("failed to apply the event %v", ev.Kind)
				return err
			}
			log.Debugf("successfully applied event: %v", ev.String())
			return nil
		}, oteltrace.WithSpanKind(oteltrace.SpanKindInternal))
}

func (a *Agent) reconcileNVCACluster(ctx context.Context, clusterID string) error {
	log := core.GetLogger(ctx)
	err := clustermgmt.ReconcileNVCACluster(ctx, a.backendk8scache, a.clusterMgmtClient, clusterID)
	if k8serr.IsNotFound(err) {
		// A 404 from ICMS/NGC means the cluster is not found in the backend.
		// We intentionally do NOT auto-delete local NVCFBackend resources in this case,
		// as this could lead to unintended data loss during transient API failures or
		// cluster registration issues. The operator should continue running and allow
		// for manual intervention or re-registration.
		log.Warnf("ClusterID=%s not found in backend API, continuing without auto-deletion", clusterID)
		return nil
	}

	return err
}

// discoverDeploymentName discovers the Deployment name that owns this pod by
// traversing the owner reference chain: Pod -> ReplicaSet -> Deployment.
// This is called once at startup to cache the deployment name for shutdown checks.
func discoverDeploymentName(ctx context.Context, clients *kubeclients.KubeClients, podName, podNamespace string) (string, error) {
	log := core.GetLogger(ctx)

	if podName == "" || podNamespace == "" {
		log.Warn("Pod name or namespace not provided, cannot discover deployment")
		return "", nil
	}

	// Get the pod
	pod, err := clients.K8s.CoreV1().Pods(podNamespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get pod %s/%s: %w", podNamespace, podName, err)
	}

	// Find the ReplicaSet owner
	var rsName string
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "ReplicaSet" {
			rsName = ref.Name
			break
		}
	}
	if rsName == "" {
		log.Info("Pod has no ReplicaSet owner, cannot discover deployment")
		return "", nil
	}

	// Get the ReplicaSet to find its Deployment owner
	rs, err := clients.K8s.AppsV1().ReplicaSets(podNamespace).Get(ctx, rsName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get replicaset %s/%s: %w", podNamespace, rsName, err)
	}

	// Find the Deployment owner
	for _, ref := range rs.OwnerReferences {
		if ref.Kind == "Deployment" {
			log.Infof("Discovered deployment name: %s", ref.Name)
			return ref.Name, nil
		}
	}

	log.Info("ReplicaSet has no Deployment owner")
	return "", nil
}
