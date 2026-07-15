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
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/imagecredential"
	cmnotel "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/otel"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	otelattr "go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
	"gopkg.in/yaml.v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/discovery"
	k8sinformers "k8s.io/client-go/informers"
	clientv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	listersv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/icms"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/logging"
	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/workloadtypes"
	nvcaotel "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/otel"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/envutil"
	helmutil "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/helm"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned/scheme"
	nvcainformers "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/informers/externalversions"
	nvcav2beta1listers "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/listers/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures/sharedcluster"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce/kaischeduler"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/fnds"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/profiling"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	SystemNamespace   = "nvca-system"
	RequestsNamespace = "nvcf-backend"

	// #nosec G101
	RegistrationInfoSecretName  = "icms-registration-info"
	BartServiceAPIKeySecretName = "bart-service-api-key-secret"
	ResyncInterval              = 30 * time.Minute
	SQSMessageIDKey             = "SQSMessageID"
	K8sNameLabelKey             = "kubernetes.io/metadata.name"
	NVCAFinalizer               = "nvca.finalizers.nvidia.io"
	NVCADeploymentName          = "nvca"
	SecretMirroredFromLabelKey  = "nvca.nvcf.nvidia.io/mirrored-from-namespace"
)

var (
	errICMSRequestFinalizerRetained = errors.New("ICMS request finalizer retained")
	useUUIDForRequestObjName        atomic.Bool
)

func init() {
	useUUIDForRequestObjName.Store(true)
}

// GetUseUUIDForRequestObjName returns whether request object names use UUID (true) or RequestID (false).
func GetUseUUIDForRequestObjName() bool {
	return useUUIDForRequestObjName.Load()
}

// SetUseUUIDForRequestObjName sets the naming convention for request objects. Used by tests.
func SetUseUUIDForRequestObjName(v bool) {
	useUUIDForRequestObjName.Store(v)
}

const (
	MaxFailedPodLogLines = int64(20)
	MaxBytesForPodLogs   = int64(1024)
)

type CacheAccessObj struct {
	ICMSRequestName, CacheName string
	MinutesSinceInstancePurge  float64
}

// BackendK8sCache encapsulates the IO interactions from BART to backend
// K8s clusters, it contains both query and mutating API
// calls. BackendK8sCache is immutable after creation, therefore it is
// thread-safe.
type BackendK8sCache struct {
	cfg nvcaconfig.Config

	// Namespace for all BART app resources.
	// No ICMS request or request-derived resources will be created here.
	systemNamespace string
	// Namespace for all ICMS request and request-derived resource creation.
	requestsNamespace string
	// Namespace for all raw Pod and related resource creation.
	podInstanceNamespace string
	// Label selectors for namespaces created by BackendK8sCache.
	namespaceLabels labels.Set

	k8sTimeConfig *k8sutil.TimeConfig

	resyncPeriod             time.Duration
	cachingSupportEnabled    bool
	nvmeshEncryptionEnabled  bool
	autoPurgeDegradedWorkers bool
	// POC: LowLatencyStreaming needs to be enabled in cluster
	lowLatencyStreamingEnabled bool
	clients                    *kubeclients.KubeClients
	modelCacheMtx              sync.Mutex
	logPostingEnabled          bool
	computeBackend             BackendType
	clusterRegion              string
	clusterName                string
	cloudProvider              string

	// Helm config flags
	helmRBACEnforcementEnabled           bool
	helmResourceConstraintsEnabled       bool
	helmSharedStorageEnabled             bool
	helmInternalPersistentStorageEnabled bool
	featureFlagFetcher                   featureflag.Fetcher
	// nsightProfilingAllowlist tracks which functions should have NVIDIA Nsight GPU
	// profiling enabled. Populated from an optional ConfigMap and refreshed periodically.
	nsightProfilingAllowlist *profiling.Allowlist
	// If true, nvca will request once for PVC rebind for model-cache setup
	pvcRebindEnabled bool
	// For credential updater jobs.
	imageCredentialHelperImage string

	sharedClusterOn *atomic.Bool

	// Helm restrictions path
	helmRepositoryPrefix string

	// Use Function Deployment Stages to log state transition events.
	fndsClient fnds.Client

	icmsRequestWQ workqueue.RateLimitingInterface
	nodeUpdateWQ  workqueue.RateLimitingInterface

	podLister                            listersv1.PodLister
	podSpecLister                        listersv1.PodNamespaceLister
	nodeLister                           listersv1.NodeLister
	nodeInformer                         cache.SharedIndexInformer
	instanceNamespaceLister              listersv1.NamespaceLister
	icmsRequestLister                    nvcav2beta1listers.ICMSRequestLister
	storageRequestLister                 nvcav2beta1listers.StorageRequestLister
	icmsRequestHelper                    ICMSRequestHelper
	k8sArtifactHelper                    K8sArtifactHelper
	syncedFuncs                          []cache.InformerSynced
	eventRecorder                        record.EventRecorder
	eventBroadcaster                     record.EventBroadcaster
	tracer                               oteltrace.Tracer
	enablePeriodicInstanceStatusUpdate   bool
	periodicInstanceStatusUpdateInterval time.Duration
	csiVolumeMountOptions                []string

	// Node features client, for retrieving instance type data.
	nfClient nodefeatures.Client
	// Registration instance type cache, since computing may be expensive.
	regITCache icms.RegistrationInstanceTypeCache

	infraOverheadGetter enforce.InfraOverheadGetter
	// Secret mirroring configuration
	secretMirrorSourceNamespace string
	secretMirrorLabelSelector   string
	secretLister                listersv1.SecretLister
	secretNamespaceLister       listersv1.SecretNamespaceLister

	// Custom annotations cache (populated by informer)
	customAnnotations *sync.Map

	// Environment variable overrides for workloads
	functionEnvOverrides map[string]string
	taskEnvOverrides     map[string]string
}

// BackendK8sCacheBuilder builds Backendk8sCache and start related egde K8s
// informers, monitored K8s events are sent to a event channel that is
// returned by Start()
type BackendK8sCacheBuilder struct {
	*BackendK8sCache

	// Static GPU configuration
	staticGPUCapacity uint64

	// Dynamic nodefeatures client configuration
	buildDynamicNFClient    bool
	baseDynamicNFClientOpts nodefeatures.DynamicClientOptions

	// OTEL configuration
	tracer oteltrace.Tracer

	// How many ICMS requests should be processed at any given time.
	icmsRequestSyncConcurrency int

	// Cluster attributes
	enabledAttrs featureflag.Attributes

	// Secret mirroring configuration
	secretMirrorSourceNamespace string
	secretMirrorLabelSelector   string

	// Environment variable overrides for workloads
	functionEnvOverrides map[string]string
	taskEnvOverrides     map[string]string

	// Mocked in tests
	addSharedClusterNodePublisher func(context.Context, cache.SharedIndexInformer) (*atomic.Bool, cache.InformerSynced, error)
}

type BootstrapTokenString struct {
	ID     string
	Secret string
}

func NewBackendk8sCacheBuilder() *BackendK8sCacheBuilder {
	return &BackendK8sCacheBuilder{
		BackendK8sCache: &BackendK8sCache{
			resyncPeriod:                         ResyncInterval,
			computeBackend:                       BackendTypeK8s,
			featureFlagFetcher:                   featureflag.DefaultFetcher,
			helmInternalPersistentStorageEnabled: featureflag.HelmInternalPersistentStorage.Enabled(),
			fndsClient:                           fnds.NewFakeClient("fake-ncaId-string"),
			k8sTimeConfig:                        (&k8sutil.TimeConfig{}).Complete(),
			infraOverheadGetter:                  enforce.NoOpInfraOverheadGetter,
		},
		tracer:                        nvcaotel.NewTracer(),
		icmsRequestSyncConcurrency:    20,
		enabledAttrs:                  featureflag.GetEnabledAttributes(),
		addSharedClusterNodePublisher: sharedcluster.AddNodePublisher,
	}
}

func (b *BackendK8sCacheBuilder) WithConfig(cfg nvcaconfig.Config) *BackendK8sCacheBuilder {
	next := *b
	next.cfg = cfg
	return &next
}

func (b *BackendK8sCacheBuilder) WithSystemNamespace(systemNamespace string) *BackendK8sCacheBuilder {
	next := *b
	next.systemNamespace = systemNamespace
	return &next
}

func (b *BackendK8sCacheBuilder) WithRequestsNamespace(requestsNamespace string) *BackendK8sCacheBuilder {
	next := *b
	next.requestsNamespace = requestsNamespace
	return &next
}

func (b *BackendK8sCacheBuilder) WithNamespaceLabels(nsl labels.Set) *BackendK8sCacheBuilder {
	next := *b
	next.namespaceLabels = nsl
	return &next
}

func (b *BackendK8sCacheBuilder) WithClients(clients *kubeclients.KubeClients) *BackendK8sCacheBuilder {
	next := *b
	next.clients = clients
	return &next
}

func (b *BackendK8sCacheBuilder) WithComputeBackend(be BackendType) *BackendK8sCacheBuilder {
	next := *b
	next.computeBackend = be
	return &next
}

func (b *BackendK8sCacheBuilder) WithLogPosting(enable bool) *BackendK8sCacheBuilder {
	next := *b
	next.logPostingEnabled = enable
	return &next
}

func (b *BackendK8sCacheBuilder) WithLowLatencyStreaming(enable bool) *BackendK8sCacheBuilder {
	next := *b
	next.lowLatencyStreamingEnabled = enable
	return &next
}

func (b *BackendK8sCacheBuilder) WithFeatureFlagFetcher(fff featureflag.Fetcher) *BackendK8sCacheBuilder {
	next := *b
	next.featureFlagFetcher = fff
	return &next
}

func (b *BackendK8sCacheBuilder) WithDynamicNodeFeatureClient(
	isDynClient bool,
	opts nodefeatures.DynamicClientOptions,
) *BackendK8sCacheBuilder {
	next := *b
	next.buildDynamicNFClient = isDynClient
	next.baseDynamicNFClientOpts = opts
	return &next
}

func (b *BackendK8sCacheBuilder) WithClusterProvider(cloudProvider string) *BackendK8sCacheBuilder {
	next := *b
	next.cloudProvider = cloudProvider
	return &next
}

func (b *BackendK8sCacheBuilder) WithClusterRegion(clusterRegion string) *BackendK8sCacheBuilder {
	next := *b
	next.clusterRegion = clusterRegion
	return &next
}

func (b *BackendK8sCacheBuilder) WithClusterName(clusterName string) *BackendK8sCacheBuilder {
	next := *b
	next.clusterName = clusterName
	return &next
}

func (b *BackendK8sCacheBuilder) WithStaticGPUCapacity(gpuCap uint64) *BackendK8sCacheBuilder {
	next := *b
	next.staticGPUCapacity = gpuCap
	return &next
}

func (b *BackendK8sCacheBuilder) WithInfraOverheadGetter(infraOverheadGetter enforce.InfraOverheadGetter) *BackendK8sCacheBuilder {
	next := *b
	next.infraOverheadGetter = infraOverheadGetter
	return &next
}

func (b *BackendK8sCacheBuilder) WithCachingSupport(enable, encryption bool) *BackendK8sCacheBuilder {
	next := *b
	next.cachingSupportEnabled = enable
	next.nvmeshEncryptionEnabled = encryption
	return &next
}

func (b *BackendK8sCacheBuilder) WithHelmRBACEnforcement(enable bool) *BackendK8sCacheBuilder {
	next := *b
	next.helmRBACEnforcementEnabled = enable
	return &next
}

func (b *BackendK8sCacheBuilder) WithHelmResourceConstraints(enable bool) *BackendK8sCacheBuilder {
	next := *b
	next.helmResourceConstraintsEnabled = enable
	return &next
}

func (b *BackendK8sCacheBuilder) WithTimeConfig(cfg *k8sutil.TimeConfig) *BackendK8sCacheBuilder {
	next := *b
	next.k8sTimeConfig = cfg
	return &next
}

func (b *BackendK8sCacheBuilder) WithWorkerDegradationHandler(enable bool) *BackendK8sCacheBuilder {
	next := *b
	next.autoPurgeDegradedWorkers = enable
	return &next
}

func (b *BackendK8sCacheBuilder) WithOTelTracer(tracer oteltrace.Tracer) *BackendK8sCacheBuilder {
	next := *b
	next.tracer = tracer
	return &next
}

func (b *BackendK8sCacheBuilder) WithPeriodicInstanceStatusUpdate(enable bool, period time.Duration) *BackendK8sCacheBuilder {
	next := *b
	next.enablePeriodicInstanceStatusUpdate = enable
	next.periodicInstanceStatusUpdateInterval = period
	return &next
}

func (b *BackendK8sCacheBuilder) WithICMSRequestSyncConcurrency(c int) *BackendK8sCacheBuilder {
	next := *b
	if c > 0 {
		next.icmsRequestSyncConcurrency = c
	}
	return &next
}

func (b *BackendK8sCacheBuilder) WithHelmInternalPersistentStorage(enable bool) *BackendK8sCacheBuilder {
	next := *b
	next.helmInternalPersistentStorageEnabled = enable
	return &next
}

func (b *BackendK8sCacheBuilder) WithHelmSharedStorage(enable bool) *BackendK8sCacheBuilder {
	next := *b
	next.helmSharedStorageEnabled = enable
	return &next
}

func (b *BackendK8sCacheBuilder) WithHelmRepositoryPrefix(hrepo string) *BackendK8sCacheBuilder {
	next := *b
	next.helmRepositoryPrefix = hrepo
	return &next
}

func (b *BackendK8sCacheBuilder) WithFNDSClient(client fnds.Client) *BackendK8sCacheBuilder {
	next := *b
	next.fndsClient = client
	return &next
}

func (b *BackendK8sCacheBuilder) WithPVCRebind(on bool) *BackendK8sCacheBuilder {
	next := *b
	next.pvcRebindEnabled = on
	return &next
}

func (b *BackendK8sCacheBuilder) WithCSIVolumeMountOptions(mntOptions []string) *BackendK8sCacheBuilder {
	next := *b
	next.csiVolumeMountOptions = mntOptions
	return &next
}

func (b *BackendK8sCacheBuilder) WithImageCredentialHelperImage(image string) *BackendK8sCacheBuilder {
	next := *b
	next.imageCredentialHelperImage = image
	return &next
}

// WithSecretMirrorSourceNamespace sets the namespace to source secrets from for mirroring
func (b *BackendK8sCacheBuilder) WithSecretMirrorConfig(namespace, selector string) *BackendK8sCacheBuilder {
	next := *b
	next.secretMirrorSourceNamespace = namespace
	next.secretMirrorLabelSelector = selector
	return &next
}

// WithEnvOverrides sets the environment variable overrides for function and task workloads
func (b *BackendK8sCacheBuilder) WithEnvOverrides(functionOverrides, taskOverrides map[string]string) *BackendK8sCacheBuilder {
	next := *b
	next.functionEnvOverrides = envutil.NormalizeEnvOverrides(functionOverrides)
	next.taskEnvOverrides = envutil.NormalizeEnvOverrides(taskOverrides)
	return &next
}

//nolint:gocyclo
func (b *BackendK8sCacheBuilder) Start(ctx context.Context) (*BackendK8sCache, <-chan *core.Event, error) {
	log := core.GetLogger(ctx)
	resyncPeriod := b.resyncPeriod
	k8sClient := b.clients.K8s

	if b.buildDynamicNFClient && b.addSharedClusterNodePublisher == nil {
		return nil, nil, fmt.Errorf("addSharedClusterNodePublisher is required")
	}

	eventBroadcaster := record.NewBroadcaster()
	// Certain features must be turned on for security in OVC environments.
	ovcSecEnforcementsEnabled := b.enabledAttrs.Enabled(featureflag.AttrOVCSecurityEnforcements)

	c := &BackendK8sCache{
		cfg:                                  b.cfg,
		systemNamespace:                      b.systemNamespace,
		requestsNamespace:                    b.requestsNamespace,
		namespaceLabels:                      b.namespaceLabels,
		resyncPeriod:                         resyncPeriod,
		clients:                              b.clients,
		eventBroadcaster:                     eventBroadcaster,
		eventRecorder:                        eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "nvca"}),
		computeBackend:                       b.computeBackend,
		clusterRegion:                        b.clusterRegion,
		clusterName:                          b.clusterName,
		cloudProvider:                        b.cloudProvider,
		imageCredentialHelperImage:           b.imageCredentialHelperImage,
		k8sTimeConfig:                        b.k8sTimeConfig,
		logPostingEnabled:                    b.logPostingEnabled,
		cachingSupportEnabled:                b.cachingSupportEnabled || ovcSecEnforcementsEnabled,
		nvmeshEncryptionEnabled:              b.nvmeshEncryptionEnabled || ovcSecEnforcementsEnabled,
		helmRBACEnforcementEnabled:           b.helmRBACEnforcementEnabled,
		helmResourceConstraintsEnabled:       b.helmResourceConstraintsEnabled || ovcSecEnforcementsEnabled,
		helmSharedStorageEnabled:             b.helmSharedStorageEnabled || ovcSecEnforcementsEnabled,
		helmInternalPersistentStorageEnabled: b.helmInternalPersistentStorageEnabled || ovcSecEnforcementsEnabled,
		tracer:                               b.tracer,
		enablePeriodicInstanceStatusUpdate:   b.enablePeriodicInstanceStatusUpdate,
		periodicInstanceStatusUpdateInterval: b.periodicInstanceStatusUpdateInterval,
		autoPurgeDegradedWorkers:             b.autoPurgeDegradedWorkers,
		icmsRequestWQ:                        workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter()),
		nodeUpdateWQ:                         workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter()),
		featureFlagFetcher:                   b.featureFlagFetcher,
		nsightProfilingAllowlist:             profiling.New(),
		lowLatencyStreamingEnabled:           b.lowLatencyStreamingEnabled,
		helmRepositoryPrefix:                 b.helmRepositoryPrefix,
		pvcRebindEnabled:                     b.pvcRebindEnabled,
		fndsClient:                           b.fndsClient,
		csiVolumeMountOptions:                b.csiVolumeMountOptions,
		regITCache:                           icms.NewRegistrationInstanceTypeCache(),
		infraOverheadGetter:                  b.infraOverheadGetter,
		secretMirrorSourceNamespace:          b.secretMirrorSourceNamespace,
		secretMirrorLabelSelector:            b.secretMirrorLabelSelector,
		functionEnvOverrides:                 b.functionEnvOverrides,
		taskEnvOverrides:                     b.taskEnvOverrides,
	}

	go func() {
		<-ctx.Done()
		c.icmsRequestWQ.ShutDown()
	}()
	go func() {
		<-ctx.Done()
		c.nodeUpdateWQ.ShutDown()
	}()

	if c.systemNamespace == "" {
		c.systemNamespace = SystemNamespace
	}
	if c.requestsNamespace == "" {
		c.requestsNamespace = RequestsNamespace
	}
	// NB(estroczynski): Pod instance namespace is set to the requests namespace always,
	// but in the future this could be something different to separate ICMS requests
	// from inference pods.
	if c.podInstanceNamespace == "" {
		c.podInstanceNamespace = c.requestsNamespace
	}

	// Load the optional Nsight profiling allowlist and keep it refreshed so operators
	// can change which functions are profiled without restarting NVCA.
	c.startNsightProfilingAllowlistRefresh(ctx)

	if c.namespaceLabels == nil {
		return nil, nil, fmt.Errorf("namespace labels are required")
	}

	if c.featureFlagFetcher == nil {
		c.featureFlagFetcher = featureflag.DefaultFetcher
	}

	out := make(chan *core.Event)

	// Initialize ICMSRequest and StorageRequest informers but do not start.
	// Must be started after dependencies are initialized and started.
	nvcaInformerFactory := nvcainformers.NewSharedInformerFactoryWithOptions(
		b.clients.BART,
		ResyncInterval,
	)
	icmsReqGenInf, err := nvcaInformerFactory.ForResource(nvcav2beta1new.SchemeGroupVersion.WithResource("icmsrequests"))
	if err != nil {
		return nil, nil, fmt.Errorf("ICMSRequest informer: %w", err)
	}
	c.icmsRequestLister = nvcav2beta1listers.NewICMSRequestLister(icmsReqGenInf.Informer().GetIndexer())
	c.syncedFuncs = append(c.syncedFuncs, icmsReqGenInf.Informer().HasSynced)
	storageInformer := nvcaInformerFactory.Nvca().V2beta1().StorageRequests()
	c.storageRequestLister = storageInformer.Lister()
	c.syncedFuncs = append(c.syncedFuncs, storageInformer.Informer().HasSynced)

	// Node informers for dynamic GPU discovery and FNDS events.
	nodeInfFactory := k8sinformers.NewSharedInformerFactoryWithOptions(
		c.clients.K8s,
		c.resyncPeriod,
		nodefeatures.NewNodeInformerOptions(c.featureFlagFetcher)...,
	)
	nodeInf := nodeInfFactory.Core().V1().Nodes()
	sharedNodeIInf := nodeInf.Informer()
	c.nodeLister = nodeInf.Lister()
	c.nodeInformer = sharedNodeIInf
	c.syncedFuncs = append(c.syncedFuncs, sharedNodeIInf.HasSynced)

	// Secret mirroring informer
	if c.secretMirrorLabelSelector != "" {
		if err := c.startSecretMirroringInformer(ctx); err != nil {
			return nil, nil, err
		}
	}

	// Custom annotations cache (populated by ConfigMap informer)
	c.initCustomAnnotationsCache()

	var nodeHandlersHasSynced []cache.InformerSynced
	if c.computeBackend == BackendTypeK8s {
		srHelper, k8sArtHelper := NewK8sComputeBackend(c.clients, c)
		if c.icmsRequestHelper == nil {
			c.icmsRequestHelper = srHelper
		}
		if c.k8sArtifactHelper == nil {
			c.k8sArtifactHelper = k8sArtHelper
		}

		// Lister for all pods in the cluster
		{
			onNVCFPodUpdateFunc := func(ctx context.Context, oldPod, newPod *corev1.Pod) {
				if c.podInstanceNamespace == newPod.Namespace {
					if err := c.onNVCFPodUpdate(ctx, oldPod, newPod); err != nil {
						log.WithError(err).Errorf("onNVCFPodUpdate handler failed for pod %s/%s", newPod.Namespace, newPod.Name)
					}
				}
			}
			podInfFactory := k8sinformers.NewSharedInformerFactoryWithOptions(
				k8sClient,
				resyncPeriod,
			)
			podInf := podInfFactory.Core().V1().Pods()
			_, err := podInf.Informer().AddEventHandler(&cache.ResourceEventHandlerFuncs{
				UpdateFunc: func(oldObj, newObj any) {
					oldPod, ok := oldObj.(*corev1.Pod)
					if !ok {
						log.Errorf("wrong oldObj object in Pod informer: %v", newObj)
						return
					}
					newPod, ok := newObj.(*corev1.Pod)
					if !ok {
						log.Errorf("wrong updated object in Pod informer: %v", newObj)
						return
					}
					// Iterate through the pod workload watchers
					for _, ww := range []workloadWatcher{watchForPodCrashesRestarts, onNVCFPodUpdateFunc} {
						ww(ctx, oldPod, newPod)
					}
				},
			})
			if err != nil {
				log.WithError(err).Errorf("failed to add event handler for all Pods")
				return nil, nil, err
			}
			c.podLister = podInf.Lister()
			c.podSpecLister = podInf.Lister().Pods(c.podInstanceNamespace)
			c.syncedFuncs = append(c.syncedFuncs, podInf.Informer().HasSynced)
			podInfFactory.Start(ctx.Done())
		}

		if err := c.initInstanceNamespaceInformer(ctx); err != nil {
			return nil, nil, err
		}

		// Lister for all events in the cluster
		if c.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.UseFunctionDeploymentStages) {
			fndsTracer := nvcaotel.NewTracer()
			onNVCFWorkloadPodEventFunc := func(ctx context.Context, event *corev1.Event) {
				// TODO: enable for Helm functions.
				if event.InvolvedObject.Namespace == c.podInstanceNamespace &&
					event.InvolvedObject.Kind == "Pod" && event.InvolvedObject.APIVersion == "v1" {
					if err := fnds.ProcessFnDSStageTransitionEvent(ctx, event, c.podLister, c.nodeLister.Get, c.icmsRequestLister, c.fndsClient, fndsTracer); err != nil {
						log.WithError(err).WithFields(logrus.Fields{
							"pod": fmt.Sprintf("%s/%s", event.InvolvedObject.Namespace, event.InvolvedObject.Name),
						}).Debug("Stage transition event handler failed")
					}
				}
			}
			// TODO: tweak list options to watch events only in namespaces of functions/tasks by labels.
			eventInfFactory := k8sinformers.NewSharedInformerFactoryWithOptions(
				k8sClient,
				resyncPeriod,
			)
			eventInf := eventInfFactory.Core().V1().Events()
			_, err := eventInf.Informer().AddEventHandler(&cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj any) {
					event, ok := obj.(*corev1.Event)
					if !ok {
						log.Errorf("wrong object type in Event informer: %T", obj)
						return
					}
					for _, ew := range []eventWatcher{onNVCFWorkloadPodEventFunc} {
						ew(ctx, event)
					}
				},
			})
			if err != nil {
				log.WithError(err).Errorf("failed to add event handler for all Events")
				return nil, nil, err
			}
			c.syncedFuncs = append(c.syncedFuncs, eventInf.Informer().HasSynced)
			eventInfFactory.Start(ctx.Done())
		}

		// Labels must be added for podInstanceNamespace if gxcache is enabled
		if c.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.GXCache) {
			if err := ensureGXCacheNamespaceLabels(ctx, c.clients.K8s.CoreV1().Namespaces(), c.podInstanceNamespace); err != nil {
				log.WithError(err).Error("failed to apply label to nvcf-backend namespace for GXCache enablement")
				return nil, nil, err
			}
		}

		// Helm model cache initialization namespace may not exist on startup.
		mcInitNamespace := storage.NewModelCacheInitNamespace()
		_, err := c.clients.K8s.CoreV1().Namespaces().Create(ctx, mcInitNamespace, metav1.CreateOptions{})

		if err != nil && !k8serrors.IsAlreadyExists(err) {
			return nil, nil, fmt.Errorf("failed to create model cache init namespace: %w", err)
		}

		// Network policies must exist in all workload namespaces;
		// the Helm handler methods will do this for each new namespace.
		for _, namespace := range []string{c.podInstanceNamespace, mcInitNamespace.Name} {
			err := k8sArtHelper.(K8sComputeBackend).ensureNetworkPolicies(ctx, namespace)
			if err != nil {
				return nil, nil, fmt.Errorf("create NetworkPolicies in namespace %s: %v",
					namespace, err)
			}
		}

		// add configMapInformers for Network Policy for k8s backend only
		if err := addConfigMapInformers(ctx, c); err != nil {
			return nil, nil, err
		}

		ag := newGPUAllocationGetter(c.icmsRequestLister, c.icmsRequestHelper)
		if b.buildDynamicNFClient {
			opts := b.baseDynamicNFClientOpts
			opts.AttributeFetcher = c.featureFlagFetcher

			sharedClusterOn, npHasSynced, err := b.addSharedClusterNodePublisher(ctx, sharedNodeIInf)
			if err != nil {
				return nil, nil, fmt.Errorf("start shared cluster node publisher: %v", err)
			}
			opts.SharedClusterOn, c.sharedClusterOn = sharedClusterOn, sharedClusterOn
			nodeHandlersHasSynced = append(nodeHandlersHasSynced, npHasSynced)

			normalizedClusterProvider, err := types.NormalizeClusterProvider(b.cloudProvider)
			if err != nil {
				return nil, nil, fmt.Errorf("normalize cluster provider: %v", err)
			}

			c.nfClient = nodefeatures.NewDynamicClient(ag, c.nodeLister, normalizedClusterProvider, opts)

			nehHasSynced, err := addNodeEventHandler(ctx, c, sharedNodeIInf)
			if err != nil {
				return nil, nil, err
			}
			nodeHandlersHasSynced = append(nodeHandlersHasSynced, nehHasSynced)

			nodeClient := c.clients.K8s.CoreV1().Nodes()
			nodeUpdater := nodefeatures.NewNodeUpdater(nodeClient, normalizedClusterProvider)

			nodeSyncConcurrency := 3
			log.Infof("Starting %d Node sync workers", nodeSyncConcurrency)
			for i := 0; i < nodeSyncConcurrency; i++ {
				go func() {
					for c.processNodeWork(ctx, nodeClient, nodeUpdater) {
					}
				}()
			}
		} else {
			c.nfClient = nodefeatures.NewStaticClient(c.clients, ag, c.systemNamespace, b.staticGPUCapacity)
		}

		nodeInfFactory.Start(ctx.Done())
	}

	// Handle ICMS request events
	{
		_, err := icmsReqGenInf.Informer().AddEventHandler(&cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				req, ok := obj.(*nvcav2beta1new.ICMSRequest)
				if !ok {
					log.Errorf("Wrong object type in ICMS request add event handler: %T", obj)
					return
				}
				c.icmsRequestWQ.Add(makeNamespacedName(req))
			},
			UpdateFunc: func(_, newObj interface{}) {
				req, ok := newObj.(*nvcav2beta1new.ICMSRequest)
				if !ok {
					log.Errorf("Wrong object type in ICMS request update event handler: %T", newObj)
					return
				}
				c.icmsRequestWQ.Add(makeNamespacedName(req))
			},
		})
		if err != nil {
			log.WithError(err).Error("failed to add event handler for ICMS requests")
			return nil, nil, err
		}
	}

	// Enqueue ICMSRequest reconciles for StorageRequest updates.
	{
		_, err := storageInformer.Informer().AddEventHandler(&cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				st := obj.(*nvcav2beta1new.StorageRequest)
				if st.Spec.RequestName == "" {
					return
				}
				key := apitypes.NamespacedName{Namespace: c.requestsNamespace, Name: st.Spec.RequestName}
				c.icmsRequestWQ.Add(key)
			},
			UpdateFunc: func(_, obj interface{}) {
				st := obj.(*nvcav2beta1new.StorageRequest)
				if st.Spec.RequestName == "" {
					return
				}
				key := apitypes.NamespacedName{Namespace: c.requestsNamespace, Name: st.Spec.RequestName}
				c.icmsRequestWQ.Add(key)
			},
		})
		if err != nil {
			log.WithError(err).Error("failed to add event handler for ICMS requests")
			return nil, nil, err
		}
	}

	// Processing ICMS requests requires instance type lookup, so
	// populate the cache before starting workers.
	if c.nfClient != nil {
		// Only wait until nodes have synced.
		cache.WaitForCacheSync(ctx.Done(), nodeHandlersHasSynced...)
		bGPUs, err := c.nfClient.GetAllBackendGPUs(ctx)
		if err != nil {
			// Check if GracefulNoGPU is enabled and this is a "no GPUs" error
			if nvcaerrors.IsNotExist(err) && c.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.GracefulNoGPU) {
				log.Warn("No GPUs found but GracefulNoGPU enabled, starting with empty GPU cache")
				bGPUs = []types.BackendGPU{}
			} else {
				log.WithError(err).Error("Failed to get initial backend GPUs")
				return nil, nil, err
			}
		}
		allowMultiNodeWorkloads := c.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.MultiNodeWorkloads)

		regBackendGPUs, err := c.GetRegisteredBackendGPUs(ctx, bGPUs, allowMultiNodeWorkloads)
		if err != nil {
			log.WithError(err).Error("Failed to get registered backend GPUs")
			return nil, nil, err
		}

		c.regITCache.Put(regBackendGPUs)
	}

	if err := c.ensureImageCredentialUpdaterCronJob(ctx); err != nil {
		return nil, nil, err
	}

	// Start NVCA object informers after all setup is complete so dependencies
	// can start first.
	nvcaInformerFactory.Start(ctx.Done())
	cache.WaitForCacheSync(ctx.Done(), c.syncedFuncs...)

	log.Infof("Starting %d ICMS request sync workers", b.icmsRequestSyncConcurrency)
	for i := 0; i < b.icmsRequestSyncConcurrency; i++ {
		go func() {
			for c.processICMSRequestWork(ctx) {
			}
		}()
	}

	return c, out, nil
}

type eventWatcher func(ctx context.Context, event *corev1.Event)

// startSecretMirroringInformer sets up the informer to watch and mirror secrets
func (c *BackendK8sCache) startSecretMirroringInformer(ctx context.Context) error {
	log := core.GetLogger(ctx)

	labelSelector, err := labels.Parse(c.secretMirrorLabelSelector)
	if err != nil {
		return fmt.Errorf("invalid secret mirror label selector: %w", err)
	}

	secretInfFactory := k8sinformers.NewSharedInformerFactoryWithOptions(
		c.clients.K8s,
		c.resyncPeriod,
		k8sinformers.WithNamespace(c.secretMirrorSourceNamespace),
		k8sinformers.WithTweakListOptions(func(lo *metav1.ListOptions) {
			lo.LabelSelector = labelSelector.String()
		}),
	)
	secretInf := secretInfFactory.Core().V1().Secrets()
	c.secretLister = secretInf.Lister()
	c.secretNamespaceLister = secretInf.Lister().Secrets(c.secretMirrorSourceNamespace)
	c.syncedFuncs = append(c.syncedFuncs, secretInf.Informer().HasSynced)

	// Add event handler for secret changes - only handle Add/Update
	_, err = secretInf.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			secret := obj.(*corev1.Secret)
			if err := c.mirrorSecret(ctx, secret); err != nil {
				log.WithError(err).Errorf("failed to mirror secret %s/%s", secret.Namespace, secret.Name)
			}
		},
		UpdateFunc: func(_, newObj interface{}) {
			secret := newObj.(*corev1.Secret)
			if err := c.mirrorSecret(ctx, secret); err != nil {
				log.WithError(err).Errorf("failed to mirror secret %s/%s", secret.Namespace, secret.Name)
			}
		},
	})
	if err != nil {
		return fmt.Errorf("failed to add secret informer event handler: %w", err)
	}

	secretInfFactory.Start(ctx.Done())
	return nil
}

// initCustomAnnotationsCache initializes the custom annotations cache
// The cache is populated by the ConfigMap informer in addConfigMapInformers
func (c *BackendK8sCache) initCustomAnnotationsCache() {
	c.customAnnotations = &sync.Map{}
	c.customAnnotations.Store("annotations", map[string]string{}) // Initialize with empty map
}

// mirrorSecret creates or updates the given secret in all NVCF function namespaces
func (c *BackendK8sCache) mirrorSecret(ctx context.Context, sourceSecret *corev1.Secret) error {
	log := core.GetLogger(ctx)
	log.Debugf("attempt secret mirror of %v/%v", sourceSecret.Namespace, sourceSecret.Name)

	// Check if the secret matches the label selector
	if c.secretMirrorLabelSelector != "" {
		selector, err := labels.Parse(c.secretMirrorLabelSelector)
		if err != nil {
			return fmt.Errorf("invalid secret mirror label selector: %w", err)
		}
		if !selector.Matches(labels.Set(sourceSecret.Labels)) {
			return nil
		}
	}

	if c.instanceNamespaceLister == nil {
		log.Debug("instanceNamespaceLister not initialized to mirror secrets")
		return nil
	}

	// Get all function namespaces
	namespaces, err := c.instanceNamespaceLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("failed to list function namespaces: %w", err)
	}

	// Create a new secret object for each namespace
	for _, ns := range namespaces {
		// Skip if source namespace is the same as target
		if ns.Name == sourceSecret.Namespace {
			continue
		}

		// Create a copy of the secret for this namespace
		newSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:        sourceSecret.Name,
				Namespace:   ns.Name,
				Labels:      maps.Clone(sourceSecret.Labels),
				Annotations: sourceSecret.Annotations,
			},
			Type: sourceSecret.Type,
			Data: make(map[string][]byte),
		}

		if newSecret.Labels == nil {
			newSecret.Labels = map[string]string{}
		}
		// add mirrored from label
		newSecret.Labels[SecretMirroredFromLabelKey] = sourceSecret.Namespace

		// Copy the secret data
		maps.Copy(newSecret.Data, sourceSecret.Data)

		// Create or update the secret
		_, err = c.clients.K8s.CoreV1().Secrets(ns.Name).Create(ctx, newSecret, metav1.CreateOptions{})
		if err != nil {
			if k8serrors.IsAlreadyExists(err) {
				// Update existing secret
				_, err = c.clients.K8s.CoreV1().Secrets(ns.Name).Update(ctx, newSecret, metav1.UpdateOptions{})
				if err != nil {
					log.WithError(err).Errorf("failed to update mirrored secret %s in namespace %s", newSecret.Name, ns.Name)
					continue
				}
				log.Debugf("Updated mirrored secret %s in namespace %s", newSecret.Name, ns.Name)
			} else {
				log.WithError(err).Errorf("failed to create mirrored secret %s in namespace %s", newSecret.Name, ns.Name)
				continue
			}
		} else {
			log.Debugf("Created mirrored secret %s in namespace %s", newSecret.Name, ns.Name)
		}
	}

	return nil
}

func (c *BackendK8sCache) processICMSRequestWork(ctx context.Context) bool {
	log := core.GetLogger(ctx)

	obj, shutdown := c.icmsRequestWQ.Get()
	if shutdown {
		log.Info("Worker shutting down")
		return false
	}

	defer c.icmsRequestWQ.Done(obj)

	func() {
		nn, ok := obj.(apitypes.NamespacedName)
		if !ok {
			c.icmsRequestWQ.Forget(obj)
			log.Errorf("Unexpected dequeued object type: %T", obj)
			return
		}

		log = log.WithFields(logrus.Fields{
			"namespace": nn.Namespace,
			"name":      nn.Name,
		})
		if rerr := c.SyncICMSRequest(ctx, nn); rerr != nil {
			switch {
			case nvcaerrors.IsTerminal(rerr), k8serrors.IsInvalid(rerr):
				log.WithError(rerr).Error("Terminal error encountered while syncing ICMS request, marking as failed")
				req, err := c.clients.BART.NvcaV2beta1().ICMSRequests(nn.Namespace).Get(ctx, nn.Name, metav1.GetOptions{})

				// Track K8s API call metrics
				if metrics := nvcametrics.FromContext(ctx); metrics != nil {
					metrics.TrackK8sAPICall("icmsrequest", err)
				}

				if err != nil {
					// If the ICMS request is not found, we can forget it and stop processing it.
					// additionally delete it's associated MiniService if it exists.
					if k8serrors.IsNotFound(err) {
						c.icmsRequestWQ.Forget(obj)
						miniserviceName := getMiniServiceInstanceID(nn.Name)
						if err := c.clients.HelmV2.Get(ctx, client.ObjectKey{Name: miniserviceName}, &v1alpha1.MiniService{}); err == nil {
							err = c.clients.HelmV2.Delete(ctx, &v1alpha1.MiniService{ObjectMeta: metav1.ObjectMeta{Name: miniserviceName}})
							if !k8serrors.IsNotFound(err) {
								log.WithError(err).Error("Failed to delete MiniService workload, requeuing to try again")
								c.icmsRequestWQ.AddRateLimited(obj)
								return
							} else if err == nil {
								log.Infof("Successfully deleted MiniService workload %s", miniserviceName)
							}
						}
						return
					}
					log.WithError(err).Error("Failed to get ICMS request to mark as failed, requeuing to try again")
					c.icmsRequestWQ.AddRateLimited(obj)
					return
				}
				// This change will requeue the ICMS request via an update event
				// so sync can clean up resources and report failures to ICMS.
				lastUpdated := &metav1.Time{Time: time.Now()}
				if !c.applyICMSRequestStatusChange(ctx, req, func(_ context.Context, sr *nvcav2beta1new.ICMSRequest) {
					// Before marking as failed, check if the ICMS request has any instances
					// attached to it. If not, we need to add placeholder instance(s) so they can be
					// updated later on with a final failed status, ensuring ICMS is explicitly notified.
					// This is critical for failures that occur after ACK but before instance creation.
					if len(sr.Status.Instances) == 0 {
						if helmutil.IsMiniServiceCreateRequest(sr) {
							// Helm chart request - create single placeholder instance
							instanceID := getMiniServiceInstanceID(sr.Name)
							sr.Status.Instances = map[string]nvcav2beta1new.InstanceStatus{
								instanceID: {
									ID:                    instanceID,
									Type:                  nvcav2beta1new.InstanceTypeMiniService,
									Status:                string(types.ICMSInstanceStarted),
									LastReportedStatus:    string(types.ICMSInstanceStateNoStatus),
									LastReportedTimestamp: nil,
								},
							}
						} else if sr.Spec.Action == common.FunctionCreationAction || sr.Spec.Action == common.TaskCreationAction {
							// Container function/task request - create placeholder pod instance(s)
							// Use the expected instance count from the creation message
							instanceCount := uint64(1)
							if sr.Spec.CreationMsgInfo.InstanceCount > 0 {
								instanceCount = sr.Spec.CreationMsgInfo.InstanceCount
							}

							// Create placeholder instance(s) matching what was ACK'd to ICMS
							sr.Status.Instances = make(map[string]nvcav2beta1new.InstanceStatus)
							for i := uint64(0); i < instanceCount; i++ {
								instanceID := getPodName(fmt.Sprintf("%d-%s", i, sr.Name))
								sr.Status.Instances[instanceID] = nvcav2beta1new.InstanceStatus{
									ID:                    instanceID,
									Type:                  nvcav2beta1new.InstanceTypePod,
									Status:                string(types.ICMSInstanceStarted),
									LastReportedStatus:    string(types.ICMSInstanceStateNoStatus),
									LastReportedTimestamp: nil,
								}
							}
						}
					}
					sr.Status.RequestStatus = nvcav2beta1new.ICMSRequestStatusFailed
					sr.Status.ReconcileErrors = req.Status.ReconcileErrors + 1
					sr.Status.LastReconcileError = rerr.Error()
					sr.Status.LastStatusUpdated = lastUpdated
				}) {
					log.Error("Failed to update ICMS request as failed, requeuing to try again")
					c.icmsRequestWQ.AddRateLimited(obj)
					return
				}
				// Forget the ICMS request so it does not stay enqueued.
				c.icmsRequestWQ.Forget(obj)
				return
			case errors.Is(rerr, errICMSRequestFinalizerRetained):
				log.WithError(rerr).Info("ICMS request is deleting but not ready for finalizer removal")
				c.icmsRequestWQ.Forget(obj)
				return
			case storage.IsRequeableStorageError(rerr):
				// Do nothing and requeue.
			default:
				log.WithError(rerr).Error("Failed to sync ICMS request, requeuing")
			}
			// The ICMS request will be requeued by the ticker on the next tick,
			// but reconciliation should still backoff.
			c.icmsRequestWQ.AddRateLimited(obj)
			return
		}

		c.icmsRequestWQ.Forget(obj)
		log.Debug("Successfully synced ICMS request")
	}()

	return true
}

func (c *BackendK8sCache) onNVCFPodUpdate(ctx context.Context, oldPod, newPod *corev1.Pod) error {
	log := core.GetLogger(ctx)

	icmsReqID := newPod.Labels[nvcatypes.ICMSRequestIDKey]
	messageBatchID := newPod.Labels[nvcatypes.MessageBatchIDKey]

	// Get ICMS request ID from pod
	if icmsReqID != "" && messageBatchID != "" {
		// Only if the status has changed do we want to sync
		if !reflect.DeepEqual(oldPod.Status, newPod.Status) {
			err := c.SyncICMSRequestByID(ctx, icmsReqID, messageBatchID)
			if err != nil {
				log.WithError(err).Errorf("failed to sync Pod %v/%v with %s=%s and %s=%s", newPod.Namespace, newPod.Name,
					nvcatypes.ICMSRequestIDKey, icmsReqID,
					nvcatypes.MessageBatchIDKey, messageBatchID)
				return err
			}
		}
	} else {
		log.Debugf("pod %v/%v does not have both labels %s=%s and %s=%s no update will occur",
			newPod.Namespace,
			newPod.Name,
			nvcatypes.ICMSRequestIDKey, icmsReqID,
			nvcatypes.MessageBatchIDKey, messageBatchID)
	}

	return nil
}

func makeNamespacedName(obj metav1.Object) apitypes.NamespacedName {
	return apitypes.NamespacedName{
		Namespace: obj.GetNamespace(),
		Name:      obj.GetName(),
	}
}

// nsightProfilingRefreshInterval is how often the Nsight profiling allowlist ConfigMap
// is re-read from the API server.
const nsightProfilingRefreshInterval = time.Minute

// startNsightProfilingAllowlistRefresh reads the optional Nsight profiling ConfigMap once
// and then re-reads it periodically until ctx is cancelled, keeping the in-memory allowlist
// current. A missing ConfigMap is treated as "profile nothing".
func (c *BackendK8sCache) startNsightProfilingAllowlistRefresh(ctx context.Context) {
	// lastSig logs at Info only on change (Debug otherwise) so the per-minute refresh does
	// not spam Info. Only accessed from refresh, which runs serially.
	var lastSig string

	refresh := func() {
		log := core.GetLogger(ctx)
		allowlist := c.nsightProfilingAllowlist
		cm, err := c.clients.K8s.CoreV1().ConfigMaps(c.systemNamespace).Get(
			ctx, profiling.ConfigMapName, metav1.GetOptions{})
		switch {
		case err != nil && k8serrors.IsNotFound(err):
			allowlist.LoadFromConfigMap(nil)
		case err != nil:
			log.WithError(err).Warnf("failed to read GPU profiling ConfigMap %s/%s",
				c.systemNamespace, profiling.ConfigMapName)
			return
		default:
			allowlist.LoadFromConfigMap(cm)
		}

		fields := logrus.Fields{
			"configMap":   fmt.Sprintf("%s/%s", c.systemNamespace, profiling.ConfigMapName),
			"wildcard":    allowlist.IsWildcard(),
			"functionIDs": allowlist.FunctionIDs(),
			"labelKey":    allowlist.LabelKey(),
			"labelValue":  allowlist.LabelValue(),
		}
		sig := fmt.Sprintf("wildcard=%t;ids=%s;label=%s=%s",
			allowlist.IsWildcard(), strings.Join(allowlist.FunctionIDs(), ","),
			allowlist.LabelKey(), allowlist.LabelValue())
		if sig != lastSig {
			lastSig = sig
			log.WithFields(fields).Info("GPU profiling allowlist updated")
		} else {
			log.WithFields(fields).Debug("GPU profiling allowlist unchanged")
		}
	}

	refresh()

	go func() {
		t := time.NewTicker(nsightProfilingRefreshInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				// select is non-deterministic when ctx.Done() and t.C are both ready;
				// re-check before refreshing to avoid a spurious warning on shutdown.
				if ctx.Err() != nil {
					return
				}
				refresh()
			}
		}
	}()
}

// parseCustomAnnotationsFromConfigMap extracts and parses custom annotations from a ConfigMap
func parseCustomAnnotationsFromConfigMap(cm *corev1.ConfigMap, log *logrus.Entry) map[string]string {
	// Get annotations from the "annotations" key
	annotationsYAML, ok := cm.Data[k8sutil.CustomAnnotationsMapKey]
	if !ok {
		return map[string]string{}
	}

	var annotations map[string]string
	if err := yaml.Unmarshal([]byte(annotationsYAML), &annotations); err != nil {
		log.WithError(err).Error("Failed to unmarshal annotations from ConfigMap")
		return map[string]string{}
	}

	return annotations
}

func configMapInformerHandler(ctx context.Context, c *BackendK8sCache) func(obj any) {
	log := core.GetLogger(ctx)
	return func(obj any) {
		log.Debug("Got ConfigMap update")

		cm, ok := obj.(*corev1.ConfigMap)
		if !ok {
			log.Errorf("Wrong new object in ConfigMap informer: %v", obj)
			return
		}

		// Handle custom annotations ConfigMap
		if cm.Name == k8sutil.CustomAnnotationsConfigMapName {
			if c.customAnnotations != nil {
				// Parse annotations directly from ConfigMap data
				annotations := parseCustomAnnotationsFromConfigMap(cm, log)
				log.WithField("count", len(annotations)).Info("Updating cached custom annotations")
				c.customAnnotations.Store("annotations", annotations)
			}
			return
		}

		// Ignore unknown CMs.
		switch cm.Name {
		case k8sutil.NetworkPoliciesConfigMapName:
		case helmChartInstanceRBACConfigMapName:
			if !c.helmRBACEnforcementEnabled {
				log.Infof("Helm RBAC enforcement is disabled, skipping ConfigMap %s updated", cm.Name)
				return
			}
		default:
			return
		}

		instanceNamespaces, err := c.instanceNamespaceLister.List(labels.Everything())
		if err != nil {
			log.WithError(err).Error("Get all Helm chart namespaces")
			return
		}
		// Remove terminated (not active) namespaces from the list of instance namespaces to update
		instanceNamespaces = slices.DeleteFunc(instanceNamespaces, func(ns *corev1.Namespace) bool {
			// return true if the namespace is not active which will remove it from the list
			return ns.Status.Phase != corev1.NamespaceActive
		})
		// Include the model cache init namespace since it has workload pods running (cache init jobs).
		instanceNamespaces = append(instanceNamespaces, storage.NewModelCacheInitNamespace())

		switch cm.Name {
		case k8sutil.NetworkPoliciesConfigMapName:
			kcb, ok := c.k8sArtifactHelper.(K8sComputeBackend)
			if !ok {
				log.Errorf("Code bug: expected K8sComputeBackend, got %T", c.k8sArtifactHelper)
				return
			}
			for _, namespace := range instanceNamespaces {
				if err := kcb.ensureNetworkPoliciesFromConfigMap(ctx, namespace.Name, cm); err != nil {
					log.WithError(err).Errorf("Ensure NetworkPolicies in namespace %s", namespace)
				}
			}
		case helmChartInstanceRBACConfigMapName:
			for _, namespace := range instanceNamespaces {
				if _, err := c.ensureHelmChartRBACFromConfigMap(ctx, namespace.Name, cm); err != nil {
					log.WithError(err).Errorf("Ensure Helm chart RBAC in namespace %s", namespace)
				}
			}
		}
	}
}

// addConfigMapInformers adds and starts an informer on ConfigMaps containing
// potentially dynamic data to ensure any changes are propagated to all necessary namespaces.
func addConfigMapInformers(ctx context.Context, c *BackendK8sCache) error {
	log := core.GetLogger(ctx)

	handle := configMapInformerHandler(ctx, c)

	f := k8sinformers.NewSharedInformerFactoryWithOptions(
		c.clients.K8s,
		c.resyncPeriod,
		k8sinformers.WithNamespace(c.systemNamespace))

	cmi := f.Core().V1().ConfigMaps()
	c.syncedFuncs = append(c.syncedFuncs, cmi.Informer().HasSynced)
	_, err := cmi.Informer().AddEventHandler(&cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			handle(obj)
		},
		UpdateFunc: func(_, obj any) {
			handle(obj)
		},
		DeleteFunc: func(obj any) {
			cm, ok := obj.(*corev1.ConfigMap)
			if !ok {
				log.Errorf("Wrong object in ConfigMap informer Delete handler: %v", obj)
				return
			}
			// Clear custom annotations cache when ConfigMap is deleted
			if cm.Name == k8sutil.CustomAnnotationsConfigMapName && c.customAnnotations != nil {
				log.Infof("Custom annotations ConfigMap %s deleted, clearing cache", cm.Name)
				c.customAnnotations.Store("annotations", map[string]string{})
			}
		},
	})
	if err != nil {
		log.WithError(err).Error("failed to add event handler for ConfigMaps")
		return err
	}

	f.Start(ctx.Done())

	return nil
}

func addNodeEventHandler(ctx context.Context,
	c *BackendK8sCache,
	inf cache.SharedIndexInformer,
) (cache.InformerSynced, error) {
	log := core.GetLogger(ctx)
	eh, err := inf.AddEventHandler(&cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			node, ok := obj.(*corev1.Node)
			if !ok {
				log.Errorf("expected *corev1.Node, got %T", obj)
				return
			}
			c.nodeUpdateWQ.Add(makeNamespacedName(node))
		},
		UpdateFunc: func(_, obj interface{}) {
			node, ok := obj.(*corev1.Node)
			if !ok {
				log.Errorf("expected *corev1.Node, got %T", obj)
				return
			}
			c.nodeUpdateWQ.Add(makeNamespacedName(node))
		},
	})
	if err != nil {
		return nil, err
	}

	c.syncedFuncs = append(c.syncedFuncs, eh.HasSynced)

	return eh.HasSynced, nil
}

func (c *BackendK8sCache) processNodeWork(ctx context.Context,
	nodeClient clientv1.NodeInterface,
	nodeUpdater *nodefeatures.NodeUpdater,
) bool {
	log := core.GetLogger(ctx)

	obj, shutdown := c.nodeUpdateWQ.Get()
	if shutdown {
		log.Info("Worker shutting down")
		return false
	}

	defer c.nodeUpdateWQ.Done(obj)

	func() {
		nn, ok := obj.(apitypes.NamespacedName)
		if !ok {
			c.nodeUpdateWQ.Forget(obj)
			log.Errorf("Unexpected dequeued object type: %T", obj)
			return
		}

		log = log.WithFields(logrus.Fields{
			"node": nn.Name,
		})

		node, err := nodeClient.Get(ctx, nn.Name, metav1.GetOptions{})

		// Track K8s API call metrics
		if metrics := nvcametrics.FromContext(ctx); metrics != nil {
			metrics.TrackK8sAPICall("node", err)
		}

		if err != nil {
			if k8serrors.IsNotFound(err) {
				c.nodeUpdateWQ.Forget(obj)
				return
			}
			c.nodeUpdateWQ.AddRateLimited(obj)
			log.WithError(err).Error("Get Node to update")
			return
		}

		if err := nodeUpdater.UpdateInstanceType(ctx, node); err != nil {
			c.nodeUpdateWQ.AddRateLimited(obj)
			log.WithError(err).Error("Update node instance type on add")
			return
		}

		c.nodeUpdateWQ.Forget(obj)
		log.Debug("Successfully synced Node")
	}()

	return true
}

func (c *BackendK8sCache) initInstanceNamespaceInformer(ctx context.Context) error {
	// Ensure only instance namespaces are selected
	namespaceLabelSel, err := labels.NewRequirement(nvcatypes.WorkloadInstanceTypeLabel,
		selection.Exists, nil)
	if err != nil {
		return fmt.Errorf("failed to create namespace label requirement: %w", err)
	}
	infFactory := k8sinformers.NewSharedInformerFactoryWithOptions(
		c.clients.K8s,
		c.resyncPeriod,
		k8sinformers.WithTweakListOptions(func(lo *metav1.ListOptions) {
			lo.LabelSelector = namespaceLabelSel.String()
		}),
	)
	namespaceInf := infFactory.Core().V1().Namespaces()
	c.instanceNamespaceLister = namespaceInf.Lister()
	c.syncedFuncs = append(c.syncedFuncs, namespaceInf.Informer().HasSynced)
	infFactory.Start(ctx.Done())
	return nil
}

func (c *BackendK8sCache) ForceSync(ctx context.Context) {
	log := core.GetLogger(ctx)

	log.Debugf("Waiting for informer caches to sync")
	if ok := cache.WaitForCacheSync(ctx.Done(), c.syncedFuncs...); !ok {
		log.Errorf("Failed to wait for caches to sync")
		return
	}
	log.Debugf("Informer cache synced !")
}

func (c *BackendK8sCache) GetGPUResource(ctx context.Context, gpuName types.GPUName) (types.GPUResource, error) {
	return c.nfClient.GetGPUResources(ctx, gpuName)
}

func (c *BackendK8sCache) GetAllBackendGPUs(ctx context.Context) ([]types.BackendGPU, error) {
	return c.nfClient.GetAllBackendGPUs(ctx)
}

// GetNodeFeaturesClient returns the node features client for GPU monitoring.
func (c *BackendK8sCache) GetNodeFeaturesClient() nodefeatures.Client {
	return c.nfClient
}

// GetNodeInformer returns the shared node informer for event-driven GPU monitoring.
func (c *BackendK8sCache) GetNodeInformer() cache.SharedIndexInformer {
	return c.nodeInformer
}

func (c *BackendK8sCache) GetComponentStatus(ctx context.Context) (hs types.AgentHealth, err error) {
	hs.GPUUsage, err = c.getGPUUsageStats(ctx)
	hs.Status = types.HealthStatusHealthy
	ch := types.ComponentHealth{
		Status:      types.HealthStatusHealthy,
		StatusLevel: types.StatusLevelError,
	}
	if err != nil {
		hs.Status = types.HealthStatusUnhealthy
		ch.Status = types.HealthStatusUnhealthy
		ch.Errors = append(ch.Errors, err.Error())
	}
	hs.Components = map[string]nvcatypes.ComponentHealth{
		"gpu": ch,
	}
	return hs, err
}

func getCurrentK8sVersion(ctx context.Context, serverVersionClient discovery.ServerVersionInterface) (string, error) {
	log := core.GetLogger(ctx)
	ver, err := serverVersionClient.ServerVersion()
	if err != nil {
		log.WithError(err).Errorf("failed to query the running k8s version")
		return "", fmt.Errorf("failed to query the running k8s version, error: %w", err)
	}
	return ver.GitVersion, nil
}

// TODO: Revisit to include this info in a new API from NVCA Operator to ICMS (via NGC API)
func (c *BackendK8sCache) getNVCAUpgradeStatus(ctx context.Context) types.NVCAUpgradeStatus {
	log := core.GetLogger(ctx)
	dep, err := c.clients.K8s.AppsV1().Deployments(c.systemNamespace).Get(ctx, NVCADeploymentName, metav1.GetOptions{})

	// Track K8s API call metrics
	if metrics := nvcametrics.FromContext(ctx); metrics != nil {
		metrics.TrackK8sAPICall("deployment", err)
	}

	if err != nil {
		log.Errorf("failed to get NVCA deployment, err: %v", err)
		return types.NVCAUpgradeNoStatus
	}

	if dep.Status.Replicas != dep.Status.ReadyReplicas {
		for _, c := range dep.Status.Conditions {
			if c.Type == appsv1.DeploymentProgressing {
				if strings.EqualFold(c.Reason, "ProgressDeadlineExceeded") {
					log.Debug("NVCA upgrade failed")
					return types.NVCAUpgradeStatusFailed
				}
				if strings.EqualFold(c.Reason, "ReplicaSetUpdated") {
					log.Debug("NVCA upgrade in_progress")
					return types.NVCAUpgradeInProgress
				}
			}
		}
		// We will wait for 10min for Upgrade to progress before marking it failed
		log.Debug("NVCA upgrade in progress")
		return types.NVCAUpgradeInProgress
	} else {
		for _, c := range dep.Status.Conditions {
			if c.Type == appsv1.DeploymentProgressing && strings.EqualFold(c.Reason, "NewReplicaSetAvailable") &&
				time.Since(c.LastUpdateTime.Time).Seconds() < types.MaxUpgradeSuccessNotificationSeconds {
				log.Debug("NVCA upgrade succeeded")
				return types.NVCAUpgradeStatusSuccess
			}
		}
	}

	return types.NVCAUpgradeNoStatus
}

func (c *BackendK8sCache) ReconcileInstanceStatus(ctx context.Context, is types.ICMSServerInstanceState) (
	srToReturn *nvcav2beta1new.ICMSRequest,
	isToReturn nvcav2beta1new.InstanceStatus,
	state ICMSInstanceReconcileState,
	found bool,
) {
	log := core.GetLogger(ctx)
	lblICMSRequestIDReq, err := labels.NewRequirement(nvcatypes.ICMSRequestIDKey, selection.Equals, []string{is.RequestID})
	if err != nil {
		log.WithError(err).Errorf("failed to create selector requirement for ICMS request with ID %s", is.RequestID)
		return nil, nvcav2beta1new.InstanceStatus{}, ICMSInstanceReconcileNoAction, false
	}
	// Query the icmsrequests with the icms-request-id=${reqID} (label key is K8s API)
	icmsRequests, err := c.icmsRequestLister.List(labels.NewSelector().Add(*lblICMSRequestIDReq))
	if err != nil {
		log.WithError(err).Debugf("failed to retrieve the ICMS request %v resource", is.RequestID)
		return nil, nvcav2beta1new.InstanceStatus{}, ICMSInstanceReconcileNoAction, false
	}
	if len(icmsRequests) == 0 {
		log.Debug("ICMS request for request ID not found")
		return nil, nvcav2beta1new.InstanceStatus{}, ICMSInstanceReconcileNoAction, false
	}
	// sync all returned ICMS requests
	for _, sr := range icmsRequests {
		if sr.Status.Instances == nil {
			continue
		}
		if lis, ok := sr.Status.Instances[is.InstanceID]; ok {
			srToReturn = &nvcav2beta1new.ICMSRequest{}
			sr.DeepCopyInto(srToReturn)
			lis.DeepCopyInto(&isToReturn)
			found = true
			break
		}
	}

	if !found {
		log.Debug("Instance not found")
		return nil, nvcav2beta1new.InstanceStatus{}, ICMSInstanceReconcileTerminateAndUpdate, false
	}

	return srToReturn, isToReturn, instanceStatusReconcileAction(is.InstanceState, types.ICMSInstanceState(isToReturn.Status)), true
}

type ICMSInstanceReconcileState string

const (
	ICMSInstanceReconcileNoAction           ICMSInstanceReconcileState = "NoAction"
	ICMSInstanceReconcileUpdateOnly         ICMSInstanceReconcileState = "UpdateOnly"
	ICMSInstanceReconcileTerminateAndUpdate ICMSInstanceReconcileState = "TerminateAndUpdate"
)

// reconcileStateMap is map[ICMSInstanceState] -> map[LocalInstanceState]ReconcileAction
var reconcileStateMap = map[types.ICMSInstanceState]map[types.ICMSInstanceState]ICMSInstanceReconcileState{
	types.ICMSInstanceStarted: {
		types.ICMSInstanceStarted:    ICMSInstanceReconcileNoAction,
		types.ICMSInstanceRunning:    ICMSInstanceReconcileUpdateOnly,
		types.ICMSInstanceTerminated: ICMSInstanceReconcileUpdateOnly,
	},
	types.ICMSInstanceRunning: {
		types.ICMSInstanceStarted:    ICMSInstanceReconcileUpdateOnly,
		types.ICMSInstanceRunning:    ICMSInstanceReconcileNoAction,
		types.ICMSInstanceTerminated: ICMSInstanceReconcileUpdateOnly,
	},
	types.ICMSInstanceTerminated: {
		types.ICMSInstanceStarted:    ICMSInstanceReconcileUpdateOnly,
		types.ICMSInstanceRunning:    ICMSInstanceReconcileUpdateOnly,
		types.ICMSInstanceTerminated: ICMSInstanceReconcileNoAction,
	},
	types.ICMSInstanceShuttingDown: {
		types.ICMSInstanceStarted:    ICMSInstanceReconcileTerminateAndUpdate,
		types.ICMSInstanceRunning:    ICMSInstanceReconcileTerminateAndUpdate,
		types.ICMSInstanceTerminated: ICMSInstanceReconcileTerminateAndUpdate,
	},
}

// returns ICMSInstanceReconcileState
// ICMSInstanceReconcileNoAction - ICMS state and local state in sync
// ICMSInstanceReconcileUpdateOnly - ICMS state out of sync, update with local state
// ICMSInstanceReconcileTerminateAndUpdate - ICMS state out of sync, terminate locally
func instanceStatusReconcileAction(spotState, localState types.ICMSInstanceState) ICMSInstanceReconcileState {
	if sm, ok := reconcileStateMap[spotState]; ok {
		if rc, rcok := sm[localState]; rcok {
			return rc
		}
	}
	return ICMSInstanceReconcileNoAction
}

func (c *BackendK8sCache) getGPUUsageStats(ctx context.Context) (map[nvcatypes.GPUName]nvcatypes.GPUResource, error) {
	gpuUtil := make(map[nvcatypes.GPUName]nvcatypes.GPUResource)
	bg, err := c.GetAllBackendGPUs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get all backend GPUs: %w", err)
	}
	multiNodeWorkloadsEnabled := c.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.MultiNodeWorkloads)
	infraOverhead, err := c.infraOverheadGetter.GetInfraOverhead(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get infra overhead: %w", err)
	}
	regBackendGPUs := nvcatypes.BackendGPUs(bg).ToRegistration(multiNodeWorkloadsEnabled, infraOverhead)
	regBackendGPUs, _, err = nvcatypes.AddInstanceCapacity(ctx, regBackendGPUs, c.nodeLister, c.sharedClusterOn)
	if err != nil {
		return nil, fmt.Errorf("failed to add instance availability to backend GPUs: %w", err)
	}
	allocatedGPUs, err := c.getAllocatedInstanceGPUs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get allocated instance GPUs: %w", err)
	}
	skipStatic := map[string]struct{}{}
	for _, b := range bg {
		if b.Static {
			gres, err := c.GetGPUResource(ctx, b.Name)
			if err != nil {
				return nil, fmt.Errorf("failed to get static GPU resource: %w", err)
			}
			gpuUtil[b.Name] = gres
			skipStatic[string(b.Name)] = struct{}{}
		}
	}
	for _, regGPU := range regBackendGPUs {
		if _, ok := skipStatic[regGPU.Name]; ok {
			continue
		}
		for _, it := range regGPU.InstanceTypes {
			if strings.HasSuffix(it.Name, "_1x") {
				res := gpuUtil[nvcatypes.GPUName(regGPU.Name)]
				res.Capacity += it.MaxInstances * it.GPUCount
				res.Allocated += allocatedGPUs[it.Value]
				gpuUtil[nvcatypes.GPUName(regGPU.Name)] = res
				break
			}
		}
	}
	return gpuUtil, nil
}

func newGPUAllocationGetter(srLister nvcav2beta1listers.ICMSRequestLister, srHelper ICMSRequestHelper) nodefeatures.GPUAllocationGetter {
	return nodefeatures.GetGPUAllocationFunc(func(ctx context.Context, gpuName types.GPUName) (alloc uint64, err error) {
		log := core.GetLogger(ctx)

		reqs, err := srLister.List(labels.Everything())
		if err != nil {
			log.WithError(err).Error("Failed to list ICMS requests to calculate GPU resources")
			return 0, err
		}

		// Sum requested GPU counts for all active or terminated but unreported instances,
		// the latter being included to have parity with ICMS' instance type usage data.
		for _, req := range reqs {
			if string(gpuName) != req.Spec.CreationMsgInfo.GPUType {
				continue
			}
			if srHelper.AllInstancesTerminatedAndReported(ctx, req) {
				continue
			}
			alloc += req.Spec.CreationMsgInfo.InstanceCount * req.Spec.CreationMsgInfo.RequestedGPUCount
		}

		return alloc, nil
	})
}

func (c *BackendK8sCache) GetRegisteredBackendGPUs(ctx context.Context,
	backendGPUs []types.BackendGPU,
	multiNodeWorkloadsEnabled bool,
) ([]types.RegistrationGPU, error) {
	infraOverhead, err := c.infraOverheadGetter.GetInfraOverhead(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get infra overhead: %w", err)
	}
	var (
		regBackendGPUs = types.BackendGPUs(backendGPUs).ToRegistration(multiNodeWorkloadsEnabled, infraOverhead)
	)
	regBackendGPUs, gpuNodeClassifications, err := types.AddInstanceCapacity(ctx, regBackendGPUs, c.nodeLister, c.sharedClusterOn)
	if err != nil {
		return nil, err
	}
	err = c.UpdateInstanceTypeMetrics(ctx, regBackendGPUs, gpuNodeClassifications)
	if err != nil {
		return nil, err
	}
	return regBackendGPUs, nil
}

func (c *BackendK8sCache) UpdateInstanceTypeMetrics(
	ctx context.Context,
	gpus []types.RegistrationGPU,
	gpuNodeClassifications []types.GPUNodeClassification,
) error {
	log := core.GetLogger(ctx)
	metrics := nvcametrics.FromContext(ctx)
	// Reset before repopulating so a gpu_family/gpu_machine combination that disappears from the
	// cluster (e.g. a node pool decommissioned) doesn't leave a stale nonzero series behind.
	metrics.GPUNodeUnclassifiedCount.Reset()
	metrics.GPUNodeTotalCount.Reset()
	for _, classification := range gpuNodeClassifications {
		metrics.SetUnclassifiedGPUNodeCount(classification.GPUFamily, classification.GPUMachine, float64(classification.Unclassified))
		metrics.SetTotalGPUNodeCount(classification.GPUFamily, classification.GPUMachine, float64(classification.Total))
	}

	allocatedInstanceGPUs, err := c.getAllocatedInstanceGPUs(ctx)
	if err != nil {
		log.WithError(err).Error("failed to get allocated instances")
		return err
	}

	for _, gpu := range gpus {
		for _, it := range gpu.InstanceTypes {
			// Multinode instance types are multiples of full node instance types,
			// so should not be used as metric labels directly.
			if it.NodeType == nvcatypes.RegistrationInstanceTypeNodeTypeMulti {
				continue
			}
			allocGPUs := allocatedInstanceGPUs[string(types.InstanceName(it.Name).WithoutMultiplierMultiNode())]
			var allocatableGPUs uint64
			if allocGPUs > it.MaxInstances*it.GPUCount {
				log.WithFields(logrus.Fields{
					"instanceType": it.Name,
					"allocGPUs":    allocGPUs,
					"maxInstances": it.MaxInstances,
				}).Error("allocation exceeds max instances")
			} else {
				allocatableGPUs = it.MaxInstances*it.GPUCount - allocGPUs
			}
			allocatableInstances := it.MaxInstances
			if it.GPUCount > 0 {
				// TODO: Ideally we would calculate allocatableInstances based on
				// all the resources allocated (CPU, Memory, Storage). These would
				// be subtracted from the instance capacity, and then the number of
				// instance types that would fit would be calculated with
				// types.calculateAvailability().
				allocatableInstances = allocatableGPUs / it.GPUCount
			}
			metrics.SetInstanceTypeMetrics(it.Name, float64(it.MaxInstances), float64(allocatableInstances), float64(it.UnschedulableCapacity))
		}
	}

	return nil
}

// UpdateSchedulerWorkloadMetrics recomputes the scheduler workload gauge from live cluster state.
// It counts active (non-terminal) ICMSRequests grouped by workload kind (function/task)
// and the actual scheduler observed on their pods. This handles mixed clusters where some
// workloads were deployed before the KAIScheduler flag was enabled and others after.
// Because it scans actual pod state, the metric is correct even after NVCA restarts.
func (c *BackendK8sCache) UpdateSchedulerWorkloadMetrics(ctx context.Context) {
	log := core.GetLogger(ctx)
	m := nvcametrics.FromContext(ctx)
	if m == nil {
		return
	}

	reqs, err := c.icmsRequestLister.List(labels.Everything())
	if err != nil {
		log.WithError(err).Error("failed to list ICMS requests for scheduler workload metrics")
		return
	}

	// Build a map of requestID -> schedulerName by scanning actual pods.
	// Pods for both container workloads and MiniService workloads carry the
	// icms-request-id label, so a single list covers both paths.
	reqScheduler := map[string]string{}
	if c.podLister != nil {
		reqIDExists, err := labels.NewRequirement(nvcatypes.ICMSRequestIDKey, selection.Exists, nil)
		if err == nil {
			pods, listErr := c.podLister.List(labels.NewSelector().Add(*reqIDExists))
			if listErr != nil {
				log.WithError(listErr).Warn("failed to list pods for scheduler workload metrics, falling back to feature flag")
			} else {
				for _, pod := range pods {
					reqID := pod.Labels[nvcatypes.ICMSRequestIDKey]
					if _, seen := reqScheduler[reqID]; seen {
						continue
					}
					sn := pod.Spec.SchedulerName
					if sn == "" {
						sn = nvcametrics.SchedulerNameDefault
					}
					reqScheduler[reqID] = sn
				}
			}
		}
	}

	// Fallback scheduler for requests whose pods haven't been created yet.
	fallbackScheduler := nvcametrics.SchedulerNameDefault
	if c.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.KAIScheduler) {
		fallbackScheduler = nvcametrics.SchedulerNameKAI
	}

	type key struct {
		scheduler    string
		workloadKind string
	}
	counts := map[key]float64{
		{nvcametrics.SchedulerNameDefault, string(workloadtypes.WorkloadKindFunction)}: 0,
		{nvcametrics.SchedulerNameDefault, string(workloadtypes.WorkloadKindTask)}:     0,
		{nvcametrics.SchedulerNameKAI, string(workloadtypes.WorkloadKindFunction)}:     0,
		{nvcametrics.SchedulerNameKAI, string(workloadtypes.WorkloadKindTask)}:         0,
	}

	for _, req := range reqs {
		if c.icmsRequestHelper.AllInstancesTerminatedAndReported(ctx, req) {
			continue
		}
		wk := nvcametrics.ActionToWorkloadKind(req.Spec.Action)
		sched, ok := reqScheduler[req.Spec.RequestID]
		if !ok {
			sched = fallbackScheduler
		}
		counts[key{sched, string(wk)}]++
	}

	for k, v := range counts {
		m.SetSchedulerWorkloadCount(k.scheduler, workloadtypes.WorkloadKind(k.workloadKind), v)
	}
}

func (c *BackendK8sCache) getAllocatedInstanceGPUs(ctx context.Context) (map[string]uint64, error) {
	allocatedInstanceGPUs := map[string]uint64{}
	reqs, err := c.icmsRequestLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	// Sum requested instance counts for all active or terminated but unreported instances,
	// the latter being included to have parity with ICMS instance type usage data.
	for _, req := range reqs {
		if c.icmsRequestHelper.AllInstancesTerminatedAndReported(ctx, req) {
			continue
		}
		allocatedInstanceGPUs[req.Spec.CreationMsgInfo.InstanceTypeValue] += req.Spec.CreationMsgInfo.InstanceCount * req.Spec.CreationMsgInfo.RequestedGPUCount
	}

	return allocatedInstanceGPUs, nil
}

// GetAllPodsForRequest returns workload pods for the given logical request ID in the pod instance
// namespace. It matches the icms-request-id label.
func (c *BackendK8sCache) GetAllPodsForRequest(ctx context.Context, reqID string) ([]corev1.Pod, error) {
	ns := c.podInstanceNamespace
	selectors := []string{
		fmt.Sprintf("%s=%s", nvcatypes.ICMSRequestIDKey, reqID),
	}
	seen := make(map[string]struct{})
	out := make([]corev1.Pod, 0)
	for _, sel := range selectors {
		pL, err := c.clients.K8s.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: sel})
		if err != nil {
			return nil, fmt.Errorf("failed to list pods, err: %v", err)
		}
		for i := range pL.Items {
			p := pL.Items[i]
			// Prefer UID for deduplication; fake clients often omit UID so fall back to namespaced name.
			key := string(p.UID)
			if key == "" {
				key = p.Namespace + "/" + p.Name
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, p)
		}
	}
	return out, nil
}

// getICMSRequestObjectMeta produces ObjectMeta for ICMSRequest (and historically v1 ICMSRequest).
// The name uses the same "sr-{requestId}" or "sr-{uuid}" convention so ICMSRequest names match
// ICMS request names, preserving pod names derived from the owner.
func getICMSRequestObjectMeta(depInfo types.DeploymentInfo) metav1.ObjectMeta {
	var name string
	if GetUseUUIDForRequestObjName() {
		id := uuid.New()
		name = fmt.Sprintf("sr-%s", id.String())
	} else {
		name = fmt.Sprintf("sr-%s", depInfo.RequestID)
	}
	om := metav1.ObjectMeta{
		Name: name,
		Labels: map[string]string{
			SQSMessageIDKey:             depInfo.MessageID,
			nvcatypes.ICMSRequestIDKey:  depInfo.RequestID,
			nvcatypes.NCAIDKey:          types.MakeNCAIDLabelValue(depInfo.NCAID),
			nvcatypes.MessageBatchIDKey: depInfo.MessageBatchID,
		},
	}
	if depInfo.GPUType != "" {
		om.Labels[nvcatypes.GPUNameKey] = depInfo.GPUType
	}
	if depInfo.FunctionID != "" {
		om.Labels[nvcatypes.NVCAFunctionIDKey] = depInfo.FunctionID
	}
	if depInfo.TaskID != "" {
		om.Labels[nvcatypes.NVCATaskIDKey] = depInfo.TaskID
	}
	if depInfo.FunctionVersionID != "" {
		om.Labels[nvcatypes.NVCAFunctionVersionIDKey] = depInfo.FunctionVersionID
	}
	return om
}

func (c *BackendK8sCache) applyICMSRequestStatusChange(ctx context.Context,
	sr *nvcav2beta1new.ICMSRequest, modify func(context.Context, *nvcav2beta1new.ICMSRequest),
) bool {
	log := core.GetLogger(ctx)
	log.Debugf("UpdateStatus for ICMS request %s started", sr.Name)
	srClient := c.clients.BART.NvcaV2beta1().ICMSRequests(sr.Namespace)

	prevReqStatus := sr.Status.RequestStatus
	var newReqStatus nvcav2beta1new.RequestStatus
	var lastUpdatedTime metav1.Time
	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Retrieve the latest version of ICMS request before attempting update
		// RetryOnConflict uses exponential backoff to avoid exhausting the apiserver
		srLat, err := c.clients.BART.NvcaV2beta1().ICMSRequests(sr.Namespace).Get(ctx, sr.Name, metav1.GetOptions{})

		// Track K8s API call metrics
		if metrics := nvcametrics.FromContext(ctx); metrics != nil {
			metrics.TrackK8sAPICall("icmsrequest", err)
		}

		if err != nil {
			return err
		}

		// Update ICMS request
		modify(ctx, srLat)
		newReqStatus = srLat.Status.RequestStatus

		// set the lastUpdatedTimeStamp
		lastUpdatedTime = metav1.Time{Time: core.GetCurrentTime(ctx)}
		srLat.Status.LastStatusUpdated = &lastUpdatedTime
		_, updateErr := c.clients.BART.NvcaV2beta1().ICMSRequests(srLat.Namespace).UpdateStatus(ctx, srLat, metav1.UpdateOptions{})
		return updateErr
	})
	if retryErr != nil {
		log.WithError(retryErr).Errorf("failed to update status for %v/%v", sr.Namespace, sr.Name)
		return false
	}

	log.Debugf("UpdateStatus for ICMS request %s completed", sr.Name)
	return reconcileSpanRequestStatus(ctx, prevReqStatus, newReqStatus, lastUpdatedTime, sr, srClient, c.tracer)
}

type reconcileSpanRequestStatusClient interface {
	Get(ctx context.Context, name string, options metav1.GetOptions) (*nvcav2beta1new.ICMSRequest, error)
	UpdateStatus(ctx context.Context, sr *nvcav2beta1new.ICMSRequest, options metav1.UpdateOptions) (*nvcav2beta1new.ICMSRequest, error)
}

func reconcileSpanRequestStatus(
	ctx context.Context,
	prevReqStatus nvcav2beta1new.RequestStatus,
	newReqStatus nvcav2beta1new.RequestStatus,
	lastUpdatedTime metav1.Time,
	sr *nvcav2beta1new.ICMSRequest,
	srClient reconcileSpanRequestStatusClient,
	tracer oteltrace.Tracer,
) bool {
	log := core.GetLogger(ctx)

	// If the state changed we need to update spans accordingly
	if prevReqStatus != newReqStatus {
		// The initial status of an ICMS request is empty, so on transition to Pending
		// do not stop the old span because it does not exist.
		if prevReqStatus != "" && sr.Status.RequestStatusTraceContexts != nil {
			prevReqSpanCtxCfg, ok := sr.Status.RequestStatusTraceContexts[prevReqStatus]
			if !ok {
				log.WithFields(logrus.Fields{
					"prevRequestState": prevReqStatus,
					"newRequestState":  newReqStatus,
				}).Debug("cannot find span for state transition")
			} else {
				// Start and stop the previous span then start the next span.
				// The only way to stop a running span from another thread without the Span object
				// is to create a new one and overwrite the existing span data in ctx.
				_, prevReqSpan := tracer.Start(
					nvcaotel.ContextWithParentSpanFromICMS(ctx, sr.Spec.GetTraceContext()),
					prevReqSpanCtxCfg.SpanName,
					oteltrace.WithSpanKind(oteltrace.SpanKindConsumer),
					oteltrace.WithTimestamp(prevReqSpanCtxCfg.StartTime.Time),
					oteltrace.WithAttributes(nvcaotel.GetDefaultAttributes()...),
					oteltrace.WithAttributes(nvcaotel.GetOTelAttributesFromICMSRequest(sr)...),
					oteltrace.WithAttributes(cmnotel.GetSpanCodeAttributes(1)...))
				prevReqSpan.End(oteltrace.WithTimestamp(lastUpdatedTime.Time))
			}
		}

		newReqSpanCtx := nvcav2beta1new.ICMSRequestSpanContextConfig{
			SpanName:  fmt.Sprintf("nvca.ICMSRequest.%s", newReqStatus),
			StartTime: metav1.Now(),
		}
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			currReq, err := srClient.Get(ctx, sr.Name, metav1.GetOptions{})

			// Track K8s API call metrics
			if metrics := nvcametrics.FromContext(ctx); metrics != nil {
				metrics.TrackK8sAPICall("icmsrequest", err)
			}

			if err != nil {
				return err
			}
			if currReq.Status.RequestStatusTraceContexts == nil {
				currReq.Status.RequestStatusTraceContexts = map[nvcav2beta1new.RequestStatus]nvcav2beta1new.ICMSRequestSpanContextConfig{}
			}
			currReq.Status.RequestStatusTraceContexts[newReqStatus] = newReqSpanCtx
			_, uerr := srClient.UpdateStatus(ctx, currReq, metav1.UpdateOptions{})
			return uerr
		}); err != nil {
			log.WithError(err).Errorf("failed to update otel span status for %v/%v", sr.Namespace, sr.Name)
			return false
		}
	}

	return true
}

func getICMSLabelSelectorString(msgID string) string {
	return fmt.Sprintf("%s=%s", SQSMessageIDKey, msgID)
}

func (c *BackendK8sCache) checkICMSRequestExists(ctx context.Context, msgID string) (bool, error) {
	log := core.GetLogger(ctx)
	selectorStr := getICMSLabelSelectorString(msgID)
	icmsRequests, err := c.clients.BART.NvcaV2beta1().ICMSRequests(c.requestsNamespace).List(ctx, metav1.ListOptions{LabelSelector: selectorStr})
	if err != nil && !k8serrors.IsNotFound(err) {
		return true, fmt.Errorf("failed to list ICMS request in %v namespace: err: %v", c.requestsNamespace, err)
	}

	if icmsRequests != nil && len(icmsRequests.Items) > 0 {
		log.Debugf("ICMS request for %v already in progress, skipping", msgID)
		return true, nil
	}
	return false, nil
}

func (c *BackendK8sCache) CreateICMSCreationMessageRequest(ctx context.Context,
	msg translate.CreationQueueMessageMetadataGetter,
	msgReceipt, msgID, queueURL string,
) (*nvcav2beta1new.ICMSRequest, error) {
	log := core.GetLogger(ctx)

	exists, err := c.checkICMSRequestExists(ctx, msgID)
	if err != nil {
		return nil, fmt.Errorf("failed precheck %v, err: %v", msgID, err)
	}
	if exists {
		return nil, nil
	}

	msgMeta := msg.GetCreationQueueMessageMetadata()

	o := nvcav2beta1new.ICMSRequest{
		Spec: nvcav2beta1new.ICMSRequestSpec{
			RequestID:      msgMeta.RequestID,
			Action:         msgMeta.Action,
			MessageReceipt: msgReceipt,
			NCAId:          msgMeta.NCAID,
			MessageBatchID: msgMeta.MessageBatchID,
			CreationMsgInfo: nvcav2beta1new.ICMSCreationMessageInfo{
				CreationQueueMessageMetadata: msgMeta,
				GPUName:                      msgMeta.GPUType,
				QueueURL:                     queueURL,
			},
		},
		Status: nvcav2beta1new.ICMSRequestStatus{
			LastStatusUpdated: &metav1.Time{Time: core.GetCurrentTime(ctx)},
			Instances:         map[string]nvcav2beta1new.InstanceStatus{},
		},
	}

	// Add metrics for the creation message
	metrics := nvcametrics.FromContext(ctx)
	metrics.QueueMessageProcessedTotal.
		WithLabelValues(metrics.WithDefaultLabelValues(string(msg.GetCreationQueueMessageMetadata().Action))...).Inc()

	isBYOOEnabled := c.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.BYOObservability)

	switch mt := msg.(type) {
	case function.CreationQueueMessage:
		o.Spec.FunctionDetails = mt.Details
		// If function translation is turned off or the launch spec is not set,
		// default to using upstream-generated launch artifacts.
		if !c.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.UseFunctionTranslator) ||
			mt.LaunchSpecification == nil || mt.LaunchSpecification.EnvironmentB64 == "" {
			o.Spec.CreationMsgInfo.LaunchArtifacts = mt.LaunchArtifacts
		}
		if mt.LaunchSpecification != nil && mt.LaunchSpecification.Telemetries != nil &&
			!(isBYOOEnabled && c.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.UseFunctionTranslator)) {
			return nil, fmt.Errorf("telemetries is set but required features are disabled: %s, %s",
				featureflag.BYOObservability.Key, featureflag.UseFunctionTranslator.Key)
		}
		o.Spec.CreationMsgInfo.FunctionLaunchSpecification = mt.LaunchSpecification
		// Apply environment variable overrides if configured
		if len(c.functionEnvOverrides) > 0 && mt.LaunchSpecification != nil {
			modified, err := envutil.ApplyEnvOverrides(
				mt.LaunchSpecification.EnvironmentB64,
				c.functionEnvOverrides,
			)
			if err != nil {
				return nil, fmt.Errorf("apply function env overrides: %w", err)
			}
			o.Spec.CreationMsgInfo.FunctionLaunchSpecification.EnvironmentB64 = modified
		}
		// TODO: remove these fields when migration is in place.
		o.Spec.FunctionID = mt.Details.FunctionID
		o.Spec.FunctionVersionID = mt.Details.FunctionVersionID
		o.ObjectMeta = getICMSRequestObjectMeta(types.DeploymentInfo{
			RequestID:         msgMeta.RequestID,
			MessageID:         msgID,
			MessageBatchID:    msgMeta.MessageBatchID,
			NCAID:             msgMeta.NCAID,
			GPUType:           msgMeta.GPUType,
			FunctionID:        mt.Details.FunctionID,
			FunctionVersionID: mt.Details.FunctionVersionID,
		})
	case task.CreationQueueMessage:
		o.Spec.TaskDetails = mt.Details

		if mt.LaunchSpecification.Telemetries != nil && !isBYOOEnabled {
			return nil, fmt.Errorf("telemetries is set but required features are disabled: %s",
				featureflag.BYOObservability.Key)
		}

		o.Spec.CreationMsgInfo.TaskLaunchSpecification = &mt.LaunchSpecification
		// Apply environment variable overrides if configured
		if len(c.taskEnvOverrides) > 0 {
			modified, err := envutil.ApplyEnvOverrides(
				mt.LaunchSpecification.EnvironmentB64,
				c.taskEnvOverrides,
			)
			if err != nil {
				return nil, fmt.Errorf("apply task env overrides: %w", err)
			}
			o.Spec.CreationMsgInfo.TaskLaunchSpecification.EnvironmentB64 = modified
		}
		o.ObjectMeta = getICMSRequestObjectMeta(types.DeploymentInfo{
			RequestID:      msgMeta.RequestID,
			MessageID:      msgID,
			MessageBatchID: msgMeta.MessageBatchID,
			NCAID:          msgMeta.NCAID,
			GPUType:        msgMeta.GPUType,
			TaskID:         mt.Details.TaskID,
		})
	}
	obj, err := c.clients.BART.NvcaV2beta1().ICMSRequests(c.requestsNamespace).Create(ctx, &o, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to persist the ICMS request on the backend, err: %v", err)
	}

	log.Debugf("successfully created the ICMS request %v/%v for request %s for further action", obj.Namespace, obj.Name, msgMeta.RequestID)

	return obj, nil
}

func (c *BackendK8sCache) CreateICMSTerminationMessageRequest(ctx context.Context, tm types.ICMSTerminationMessage, msgReceipt, msgID string) error {
	log := core.GetLogger(ctx)

	exists, err := c.checkICMSRequestExists(ctx, msgID)
	if err != nil {
		return fmt.Errorf("failed precheck %v, err: %v", msgID, err)
	}
	if exists {
		return nil
	}

	o := nvcav2beta1new.ICMSRequest{
		ObjectMeta: getICMSRequestObjectMeta(
			types.DeploymentInfo{
				RequestID:         tm.RequestID,
				MessageID:         msgID,
				NCAID:             tm.NCAId,
				FunctionID:        tm.FunctionID,
				FunctionVersionID: tm.FunctionVersionID,
			}),
		Spec: nvcav2beta1new.ICMSRequestSpec{
			RequestID:         tm.RequestID,
			Action:            common.TerminationAction,
			MessageReceipt:    msgReceipt,
			NCAId:             tm.NCAId,
			FunctionID:        tm.FunctionID,
			FunctionVersionID: tm.FunctionVersionID,
			TerminationMsgInfo: nvcav2beta1new.ICMSTerminationMessageInfo{
				InstanceIds: tm.InstanceIds,
				ClusterName: tm.ClusterName,
			},
		},
		Status: nvcav2beta1new.ICMSRequestStatus{
			LastStatusUpdated: &metav1.Time{Time: core.GetCurrentTime(ctx)},
			Instances:         map[string]nvcav2beta1new.InstanceStatus{},
		},
	}

	obj, err := c.clients.BART.NvcaV2beta1().ICMSRequests(c.requestsNamespace).Create(ctx, &o, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to persist the ICMS request %+v on the backend, err: %v", tm, err)
	}

	log.Debugf("successfully created the ICMS request %v/%v for request %v for further action", obj.Namespace, obj.Name, tm.RequestID)

	return nil
}

func (c *BackendK8sCache) SyncAllICMSRequests(ctx context.Context) error {
	allReqs, err := c.icmsRequestLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("failed to list ICMS requests in the backend, err: %v", err)
	}
	for _, req := range allReqs {
		c.icmsRequestWQ.Add(makeNamespacedName(req))
	}
	return nil
}

// given the List of CacheAccessObject for a cacheRef
// returns the list of ICMS request names that can be purged
// returns bool is the cacheRef itself should be purged because all Accesses are over the modelCacheIdlePeriod
func (c *BackendK8sCache) isCacheReferencePurgeable(ctx context.Context, cN string, accessList []CacheAccessObj) ([]string, bool) {
	log := core.GetLogger(ctx)
	sToC := []string{}
	cacheToBeDeleted := true

	// return all ICMS requests once that are recent
	// or ones that don't meet the cleanup criteria
	for _, o := range accessList {
		if o.MinutesSinceInstancePurge <= c.k8sTimeConfig.ModelCacheIdlePeriod.Minutes() {
			cacheToBeDeleted = false
		} else {
			sToC = append(sToC, o.ICMSRequestName)
		}
	}
	log.WithFields(map[string]interface{}{
		"accessList":  accessList,
		"reqsToPurge": sToC,
	}).Debugf("AccessCheck for %v, cacheDeleted: %v", cN, cacheToBeDeleted)
	return sToC, cacheToBeDeleted
}

func (c *BackendK8sCache) CleanupUnusedResources(ctx context.Context) error {
	allReqs, e := c.icmsRequestLister.List(labels.Everything())
	if e != nil {
		return fmt.Errorf("failed to list ICMS requests in the backend, err: %v", e)
	}
	return c.CleanupCachedResources(ctx, allReqs)
}

func (c *BackendK8sCache) SyncNetworkPolicies(ctx context.Context) error {
	// Network policies must exist in all workload namespaces;
	// the Helm handler methods will do this for each new namespace.
	e := c.k8sArtifactHelper.(K8sComputeBackend).ensureNetworkPolicies(ctx, c.podInstanceNamespace)
	if e != nil {
		return fmt.Errorf("failed to sync network policies in the %s namespace, err: %v", c.podInstanceNamespace, e)
	}
	return nil
}

func (c *BackendK8sCache) CleanupCachedResources(ctx context.Context, allReqs []*nvcav2beta1new.ICMSRequest) error {
	log := core.GetLogger(ctx)
	var crn string
	cacheAccessMap := make(map[string][]CacheAccessObj)
	var cacheRefToCleanup, icmsRequestsToCleanup []string

	for _, req := range allReqs {
		tt := float64(0)
		// no cacheRef nothing to cleanup
		if req.Status.CacheReferenceName == "" {
			continue
		}
		crn = req.Status.CacheReferenceName
		if c.icmsRequestHelper.AllInstancesTerminatedAndReported(ctx, req) {
			// if all instances are terminated, the LastStatusUpdated
			// timestamp is when the last instance is terminated and reported
			if req.Status.LastStatusUpdated != nil {
				tt = time.Since(req.Status.LastStatusUpdated.Time).Minutes()
			}
		}
		newObj := CacheAccessObj{ICMSRequestName: req.Name, CacheName: req.Status.CacheReferenceName, MinutesSinceInstancePurge: tt}
		cacheAccessMap[crn] = append(cacheAccessMap[crn], newObj)
	}

	for k, a := range cacheAccessMap {
		sToC, oIP := c.isCacheReferencePurgeable(ctx, k, a)
		if oIP {
			cacheRefToCleanup = append(cacheRefToCleanup, k)
		}
		icmsRequestsToCleanup = append(icmsRequestsToCleanup, sToC...)
	}

	// cleanup ROPVC
	err := c.icmsRequestHelper.ComputeCleanupCacheReferences(ctx, cacheRefToCleanup)
	if err != nil {
		return fmt.Errorf("failed compute specific cleanup of cacheReferences (%+v) err: %v", cacheRefToCleanup, err)
	}

	// cleanup all ICMS requests that no longer need to be maintained
	for _, sr := range icmsRequestsToCleanup {
		req, err := c.clients.BART.NvcaV2beta1().ICMSRequests(c.requestsNamespace).Get(ctx, sr, metav1.GetOptions{})

		// Track K8s API call metrics
		if metrics := nvcametrics.FromContext(ctx); metrics != nil {
			metrics.TrackK8sAPICall("icmsrequest", err)
		}

		if err != nil {
			log.WithError(err).Errorf("failed to get ICMS request %v/%v", c.requestsNamespace, sr)
			continue
		}
		if c.icmsRequestHelper.AllInstancesTerminatedAndReported(ctx, req) {
			err = c.clients.BART.NvcaV2beta1().ICMSRequests(c.requestsNamespace).Delete(ctx, sr, metav1.DeleteOptions{})
			if err != nil && !k8serrors.IsNotFound(err) {
				log.WithError(err).Errorf("failed to delete request %v/%v", c.requestsNamespace, sr)
				continue
			}
			log.Infof("cleaned-up ICMS request (%v/%v) from backend as part of cacheRefCleanup", c.requestsNamespace, sr)
		} else {
			log.Debugf("skip cleanup %v/%v since instances are not terminated and reported", req.Namespace, sr)
		}
	}
	return nil
}

func (c *BackendK8sCache) CleanupCreationRequestResources(ctx context.Context, req *nvcav2beta1new.ICMSRequest) error {
	log := core.GetLogger(ctx)

	// TODO: Check if all the instance Status updates are posted to ICMS before purge
	if req.Status.CacheReferenceName == "" {
		log.Debugf("attempting ICMS request %v/%v resource cleanup", req.Namespace, req.Name)

		if c.icmsRequestHelper.AllInstancesTerminatedAndReported(ctx, req) {
			err := c.clients.BART.NvcaV2beta1().ICMSRequests(c.requestsNamespace).Delete(ctx, req.Name, metav1.DeleteOptions{})
			if err != nil && !k8serrors.IsNotFound(err) {
				return fmt.Errorf("failed to delete request in %v: %v/%v", req.Status.RequestStatus, req.Namespace, req.Name)
			}
			log.Infof("cleaned-up creation request (%v/%v) from backend as the instances are terminated", req.Namespace, req.Name)
		} else {
			log.Debugf("request (%v/%v) not cleaned-up as there are active instances", req.Namespace, req.Name)
		}
	}
	return nil
}

/*
// ICMS request state transition
"" (ACK to ICMS) ->

	"RequestStatusPending" ->
			"RequestStatusInProgress" -> "RequestCompleted" / "RequestFailed"
						-> "RequestCompletionACK / "RequestFailureACK"
*/
func (c *BackendK8sCache) SyncICMSRequest(ctx context.Context, nn apitypes.NamespacedName) error {
	req, err := c.icmsRequestLister.ICMSRequests(nn.Namespace).Get(nn.Name)
	if err != nil {
		// If the ICMS request no longer exists we need to consider it terminal
		if k8serrors.IsNotFound(err) {
			return nvcaerrors.TerminalError(err)
		}
		return err
	}
	// Deep copy the ICMS request to avoid data race condition
	// since the lister is pulling it from a cache
	req = req.DeepCopy()
	ctx = logging.WithICMSRequestFieldLogger(ctx, req)
	return nvcaotel.InvokeWithSpan(ctx, c.tracer, "nvca.BackendK8sCache.SyncICMSRequest",
		func(ctx context.Context) error {
			return c.syncICMSRequest(ctx, req)
		},
		oteltrace.WithSpanKind(oteltrace.SpanKindConsumer),
		oteltrace.WithAttributes(nvcaotel.GetOTelAttributesFromICMSRequest(req)...))
}

func getICMSRequestIDLabelSelector(ctx context.Context, icmsReqID, messageBatchID string) (labels.Selector, error) {
	log := core.GetLogger(ctx)
	lblICMSRequestIDReq, err := labels.NewRequirement(nvcatypes.ICMSRequestIDKey, selection.Equals, []string{icmsReqID})
	if err != nil {
		log.WithError(err).Errorf("failed to create selector requirement for ICMS request with ID %s", icmsReqID)
		return nil, err
	}
	lblBatchMsgIDReq, err := labels.NewRequirement(nvcatypes.MessageBatchIDKey, selection.Equals, []string{messageBatchID})
	if err != nil {
		log.WithError(err).Errorf("failed to create selector requirement for message batch ID %s", messageBatchID)
		return nil, err
	}
	return labels.NewSelector().Add(*lblICMSRequestIDReq).Add(*lblBatchMsgIDReq), nil
}

func (c *BackendK8sCache) SyncICMSRequestByID(ctx context.Context, icmsReqID, messageBatchID string) error {
	return nvcaotel.InvokeWithSpan(ctx, c.tracer, "nvca.BackendK8sCache.SyncICMSRequestByID",
		func(ctx context.Context) error {
			log := core.GetLogger(ctx)
			icmsRequestIDLblSel, err := getICMSRequestIDLabelSelector(ctx, icmsReqID, messageBatchID)
			if err != nil {
				return err
			}
			// Query the icmsrequests with the icms-request-id=${reqID} (label key is K8s API)
			icmsRequests, err := c.icmsRequestLister.ICMSRequests(c.requestsNamespace).List(icmsRequestIDLblSel)
			if err != nil {
				log.WithError(err).Debugf("failed to retrieve the ICMS request %v resource", icmsReqID)
				return fmt.Errorf("failed to sync ICMS request %v, will be requeued, err: %w", icmsReqID, err)
			}
			if len(icmsRequests) == 0 {
				err := fmt.Errorf("failed to retrieve an ICMS request with id=%s and messageBatchID=%s", icmsReqID, messageBatchID)
				log.WithError(err).Errorf("failed to sync ICMS request %v, will be requeued", icmsReqID)
				return err
			}
			// sync all returned ICMS requests
			for _, sr := range icmsRequests {
				c.icmsRequestWQ.Add(makeNamespacedName(sr))
			}
			return nil
		},
		oteltrace.WithSpanKind(oteltrace.SpanKindConsumer),
		oteltrace.WithAttributes(otelattr.String(nvcaotel.ICMSRequestIDAttributeKey, icmsReqID),
			otelattr.String(nvcaotel.MessageBatchIDAttributeKey, messageBatchID)))
}

// Helper functions to check and remove string from a slice of strings.
func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func removeString(slice []string, s string) (result []string) {
	for _, item := range slice {
		if item == s {
			continue
		}
		result = append(result, item)
	}
	return
}

func (c *BackendK8sCache) syncICMSRequest(ctx context.Context, req *nvcav2beta1new.ICMSRequest) error {
	log := core.GetLogger(ctx)
	log.Debug("Syncing ICMS request")

	if req.ObjectMeta.DeletionTimestamp.IsZero() {
		// The object is not being deleted, so if it does not have our finalizer,
		// then lets add the finalizer and update the object.
		if !containsString(req.ObjectMeta.Finalizers, NVCAFinalizer) {
			retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				// Retrieve the latest version of ICMS request before attempting update
				// RetryOnConflict uses exponential backoff to avoid exhausting the apiserver
				srLat, err := c.clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})

				// Track K8s API call metrics
				if metrics := nvcametrics.FromContext(ctx); metrics != nil {
					metrics.TrackK8sAPICall("icmsrequest", err)
				}

				if err != nil {
					return fmt.Errorf("failed to get latest version of ICMS request: %w", err)
				}

				// add our finalizer to the list and update it.
				srLat.ObjectMeta.Finalizers = append(srLat.ObjectMeta.Finalizers, NVCAFinalizer)

				_, updateErr := c.clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).Update(ctx, srLat, metav1.UpdateOptions{})
				return updateErr
			})
			if retryErr != nil && !k8serrors.IsNotFound(retryErr) {
				return fmt.Errorf("failed to add finalizer to ICMS request %s, err: %w", req.Name, retryErr)
			}
		}
	} else {
		// The object is being deleted
		if containsString(req.ObjectMeta.Finalizers, NVCAFinalizer) {
			// our finalizer is present, so lets handle our external dependency
			if !c.icmsRequestHelper.AllInstancesTerminatedAndReported(ctx, req) {
				return fmt.Errorf("%w: instances are not terminated and reported for %s", errICMSRequestFinalizerRetained, req.Name)
			}

			retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				// Retrieve the latest version of ICMS request before attempting update
				// RetryOnConflict uses exponential backoff to avoid exhausting the apiserver
				srLat, err := c.clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})

				// Track K8s API call metrics
				if metrics := nvcametrics.FromContext(ctx); metrics != nil {
					metrics.TrackK8sAPICall("icmsrequest", err)
				}

				if err != nil {
					return fmt.Errorf("failed to get latest version of ICMS request: %w", err)
				}

				// remove our finalizer from the list and update it.
				srLat.ObjectMeta.Finalizers = removeString(srLat.ObjectMeta.Finalizers, NVCAFinalizer)

				_, updateErr := c.clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).Update(ctx, srLat, metav1.UpdateOptions{})
				return updateErr
			})
			if retryErr != nil && !k8serrors.IsNotFound(retryErr) {
				return fmt.Errorf("failed to remove finalizer on ICMS request %s, err: %w", req.Name, retryErr)
			}
		}
		// Our finalizer has finished, so the reconciler can do nothing.
		return nil
	}

	var err error
	var purgeAttempted bool
	switch req.Status.RequestStatus {
	case nvcav2beta1new.ICMSRequestStatusPending:
		err, purgeAttempted = c.cleanupOldICMSRequest(ctx, req)
		if purgeAttempted {
			if err == nil {
				log.Infof("cleaned-up ICMS request")
			} else {
				log.WithError(err).Error("failed to clean up ICMS request")
				return err
			}
			return nil
		}
		fallthrough
	case nvcav2beta1new.ICMSRequestStatusInProgress, nvcav2beta1new.ICMSRequestStatusCachingInProgress:
		switch req.Spec.Action {
		case common.FunctionCreationAction, common.TaskCreationAction:
			if c.icmsRequestHelper.AllInstancesTerminatedAndReported(ctx, req) {
				// this occurs when the backend goes unhealthy
				// ICMS sends a termination for all instances
				// and nvca has not updated the status yet leaving requests
				// stuck InProgress
				err = c.CleanupCreationRequestResources(ctx, req)
			} else {
				err = c.icmsRequestHelper.ApplyCreationMessage(ctx, req)
			}
		case common.TerminationAction:
			err = c.icmsRequestHelper.ApplyTerminationMessage(ctx, req)
		}
	// cleanup lingering resources for creation request if all
	// instances are terminated and reported / instances are stuck for long
	case nvcav2beta1new.ICMSRequestStatusCompletionAcknowledged, nvcav2beta1new.ICMSRequestStatusInstancesInProgress:
		if req.Status.RequestStatus == nvcav2beta1new.ICMSRequestStatusInstancesInProgress {
			if err := c.ApplyICMSRequestStatusChange(ctx, req); err != nil {
				log.WithError(err).Debug("Failed to apply the ICMS request")
				return fmt.Errorf("failed to sync ICMS request %s, will be requeued, err: %w", req.Name, err)
			}
		}
		err = c.CleanupCreationRequestResources(ctx, req)
	// cleanup the requests after acknowledging failure
	case nvcav2beta1new.ICMSRequestStatusFailureAcknowledged:
		if len(req.Status.Instances) > 0 && !c.icmsRequestHelper.AllInstancesTerminatedAndReported(ctx, req) {
			// need to reconcile ICMS request status out of the transient failed state
			// due to miniservice reconciliation
			for _, is := range req.Status.Instances {
				if is.Status == string(types.ICMSInstanceRunning) && is.LastReportedStatus == is.Status {
					if err := c.ApplyICMSRequestStatusChange(ctx, req); err != nil {
						log.WithError(err).Debug("Failed to apply the ICMS request")
						return fmt.Errorf("failed to sync ICMS request %s, will be requeued, err: %w", req.Name, err)
					}
					log.Infof("Reconciled ICMS request status based on instance status")
					break
				}
			}
		} else {
			err, purgeAttempted = c.cleanupOldICMSRequest(ctx, req)
			if purgeAttempted {
				if err == nil {
					log.Infof("cleaned-up ICMS request")
				}
			}
		}
	}

	if err != nil {
		return fmt.Errorf("failed to sync ICMS request %s: %w", req.Name, err)
	}

	return nil
}

// returns an error & bool indicating purged (true) or not-purged (false)
func (c *BackendK8sCache) cleanupOldICMSRequest(ctx context.Context, req *nvcav2beta1new.ICMSRequest) (error, bool) {
	// garbage collect the request if it has been more than 1 hour since Failed
	t := req.Status.LastStatusUpdated
	cw := FailedRequestCleanupWindow
	if t != nil && time.Since(t.Time) > cw {
		if err := c.CleanupCreationRequestResources(ctx, req); err != nil {
			return err, true
		} else {
			return nil, true
		}
	}
	return nil, false
}

func mergeMaps(m1, m2 map[string]string) map[string]string {
	merged := make(map[string]string)
	for k, v := range m1 {
		merged[k] = v
	}
	for key, value := range m2 {
		merged[key] = value
	}
	return merged
}

func getPodName(str string) string {
	return trimDNS1123Label(str, 0)
}

// trimDNS1123Label trims a string to meet the DNS 1123 label length requirement
// by removing characters from the beginning of label such that len(label) <= (63+extra).
func trimDNS1123Label(s string, extra int) string {
	if extra < 0 || extra >= validation.DNS1123LabelMaxLength {
		extra = 0
	}
	if len(s) > validation.DNS1123LabelMaxLength-extra {
		return strings.Trim(s[:validation.DNS1123LabelMaxLength-extra], "-")
	}
	return s
}

func (c *BackendK8sCache) GetBARTRegistrationResponseSecret(ctx context.Context) (*corev1.Secret, error) {
	secret, err := c.clients.K8s.CoreV1().Secrets(c.systemNamespace).Get(ctx, RegistrationInfoSecretName, metav1.GetOptions{})

	// Track K8s API call metrics
	if metrics := nvcametrics.FromContext(ctx); metrics != nil {
		metrics.TrackK8sAPICall("secret", err)
	}

	return secret, err
}

func (c *BackendK8sCache) StoreUpdatedCredentials(ctx context.Context, qCreds types.QueueCredentials) error {
	log := core.GetLogger(ctx)
	secret, err := c.GetBARTRegistrationResponseSecret(ctx)
	if err != nil {
		return fmt.Errorf("failed to get the RegistrationResponseSecret, err: %v", err)
	}

	resBytes := secret.Data[RegistrationInfoSecretName]
	curRes := types.ICMSRegistrationResponse{}

	err = json.Unmarshal(resBytes, &curRes)
	if err != nil {
		return fmt.Errorf("failed to unmarshal current response, err: %v", err)
	}

	curRes.Credentials = qCreds

	jsonUpdateBytes, err := json.Marshal(&curRes)
	if err != nil {
		return fmt.Errorf("failed to marshal the ICMS registration response")
	}

	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		sObj, err := c.GetBARTRegistrationResponseSecret(ctx)
		if err != nil {
			return fmt.Errorf("failed to create secret %v/%v for StoreUpdatedCredentials, err:%v",
				secret.Namespace, secret.Name, err)
		}

		// secret exists, update the data
		sObj.Data[RegistrationInfoSecretName] = jsonUpdateBytes
		_, err = c.clients.K8s.CoreV1().Secrets(c.systemNamespace).Update(ctx, sObj, metav1.UpdateOptions{})
		return err
	}); err != nil {
		return fmt.Errorf("failed to update secret %v/%v for StoreUpdatedCredentials, err:%v",
			secret.Namespace, secret.Name, err)
	}

	log.Debugf("successfully StoreUpdatedCredentials %v/%v", secret.Namespace, secret.Name)
	return nil
}

func (c *BackendK8sCache) StoreICMSRegistrationResponse(ctx context.Context, res *types.ICMSRegistrationResponse) error {
	log := core.GetLogger(ctx)
	secret := &corev1.Secret{}
	secret.Name = RegistrationInfoSecretName
	secret.Namespace = c.systemNamespace
	jsonResBytes, err := json.Marshal(res)
	if err != nil {
		return fmt.Errorf("failed to marshal the ICMS registration response")
	}

	secret.Data = map[string][]byte{
		RegistrationInfoSecretName: jsonResBytes,
	}

	if _, err := c.GetBARTRegistrationResponseSecret(ctx); err != nil {
		if k8serrors.IsNotFound(err) {
			_, err := c.clients.K8s.CoreV1().Secrets(c.systemNamespace).Create(ctx, secret, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to create secret %v/%v for ICMSRegistrationResponse, err:%v",
					secret.Namespace, secret.Name, err)
			}
			log.Debugf("created ICMSRegistrationResponse secret")
			return nil
		}
		return fmt.Errorf("failed to create secret %v/%v for ICMSRegistrationResponse, err:%v",
			secret.Namespace, secret.Name, err)
	}

	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		sObj, err := c.GetBARTRegistrationResponseSecret(ctx)
		if err != nil {
			return fmt.Errorf("failed to create secret %v/%v for ICMSRegistrationResponse, err:%v",
				secret.Namespace, secret.Name, err)
		}

		// secret exists, update the data
		sObj.Data = secret.Data
		_, err = c.clients.K8s.CoreV1().Secrets(c.systemNamespace).Update(ctx, sObj, metav1.UpdateOptions{})
		return err
	}); err != nil {
		return fmt.Errorf("failed to update secret %v/%v for ICMSRegistrationResponse, err:%v",
			secret.Namespace, secret.Name, err)
	}

	log.Debugf("successfully stored ICMSRegistrationResponse %v/%v", secret.Namespace, secret.Name)
	return nil
}

func (c *BackendK8sCache) FetchICMSRegistrationResponse(ctx context.Context) (*types.ICMSRegistrationResponse, error) {
	log := core.GetLogger(ctx)
	sObj, err := c.clients.K8s.CoreV1().Secrets(c.systemNamespace).Get(ctx, RegistrationInfoSecretName, metav1.GetOptions{})

	// Track K8s API call metrics
	if metrics := nvcametrics.FromContext(ctx); metrics != nil {
		metrics.TrackK8sAPICall("secret", err)
	}

	if err != nil {
		return nil, fmt.Errorf("unable to fetch the ICMSRegistrationResponse, err: %v", err)
	}

	rres := types.ICMSRegistrationResponse{}

	err = json.Unmarshal(sObj.Data[RegistrationInfoSecretName], &rres)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal to ICMSRegistrationResponse from secret, err: %v", err)
	}

	log.Debugf("successfully fetched the registration response")
	return &rres, nil
}

func (c *BackendK8sCache) shouldReportInstanceStatusHeartbeat(
	ctx context.Context,
	req *nvcav2beta1new.ICMSRequest,
	instanceID, currentStatus, lastReportedStatus string,
	lastReportedTimeStamp *metav1.Time,
) bool {
	log := core.GetLogger(ctx)

	log.Debugf("checking if status update required for %v", instanceID)
	if !strings.EqualFold(currentStatus, lastReportedStatus) ||
		lastReportedTimeStamp == nil {
		return true
	}

	if !c.enablePeriodicInstanceStatusUpdate {
		log.Debugf("skip status for %v, periodicReporting disabled", instanceID)
		return false
	}

	// If all the instances are Terminated and Reported, no need to further report
	if c.icmsRequestHelper.AllInstancesTerminatedAndReported(ctx, req) {
		log.Debugf("skip status update for %v, request status: %v, instance status: %v",
			instanceID, req.Status.RequestStatus, currentStatus)
		return false
	}

	if time.Since(lastReportedTimeStamp.Time) >= c.periodicInstanceStatusUpdateInterval {
		return true
	}

	log.Debugf("skip status for %v, currentStatus: %v, lastReportedStatus: %v, lastReportedTimeStamp: %v",
		instanceID, currentStatus, lastReportedStatus, lastReportedTimeStamp)
	return false
}

func (c *BackendK8sCache) ApplyICMSRequestStatusChange(ctx context.Context, req *nvcav2beta1new.ICMSRequest) error {
	log := core.GetLogger(ctx)

	var newReqStatus *nvcav2beta1new.RequestStatus

	switch c.icmsRequestHelper.AggregateInstanceStatuses(ctx, req) {
	case AggregatedInstanceStatusSucceeded:
		newReqStatus = reqStatusPtr(nvcav2beta1new.ICMSRequestStatusCompleted)
	case AggregatedInstanceStatusFailed:
		newReqStatus = reqStatusPtr(nvcav2beta1new.ICMSRequestStatusFailed)
	case AggregatedInstanceStatusModelCachingInProgress:
		// This status should have already been applied by the instance handler.
	case AggregatedInstanceStatusScheduling:
		if c.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.AckTaskRequestAfterPodsScheduled) {
			// The request status should not change while task pods are scheduling.
			break
		}
		// Treat scheduling like pending if the feature flag is disabled.
		fallthrough
	case AggregatedInstanceStatusPending:
		if t := req.CreationTimestamp.Time; !t.IsZero() &&
			req.Status.RequestStatus != nvcav2beta1new.ICMSRequestStatusCachingInProgress &&
			time.Since(t) >= c.k8sTimeConfig.MaxRunningTimeout {
			newReqStatus = reqStatusPtr(nvcav2beta1new.ICMSRequestStatusFailed)
		} else {
			// all requested instances are created
			newReqStatus = reqStatusPtr(nvcav2beta1new.ICMSRequestStatusInstancesInProgress)
		}
	}

	if newReqStatus != nil {
		if req.Status.RequestStatus != *newReqStatus {
			log.WithFields(logrus.Fields{
				"old_status": req.Status.RequestStatus,
				"new_status": *newReqStatus,
			}).Info("ICMS request aggregated status changing")
		}

		var ss nvcav2beta1new.ICMSRequestStatus
		ss.RequestStatus = *newReqStatus
		ss.LastStatusUpdated = &metav1.Time{Time: core.GetCurrentTime(ctx)}

		modify := func(_ context.Context, sr *nvcav2beta1new.ICMSRequest) {
			sr.Status.RequestStatus = ss.RequestStatus
			sr.Status.LastStatusUpdated = ss.LastStatusUpdated
		}
		if !c.applyICMSRequestStatusChange(ctx, req, modify) {
			return fmt.Errorf("error updating ICMS request '%s' status to %+v", req.Name, ss)
		}
	}
	return nil
}

func (c *BackendK8sCache) ensureImageCredentialUpdaterCronJob(ctx context.Context) error {
	log := core.GetLogger(ctx)

	if c.imageCredentialHelperImage == "" {
		log.Debug("Third party registry image credential helper image not configured, skipping image credential update setup")
		return nil
	}
	const cjName = "image-cred-updater"
	namespaceSelectorReq, err := labels.NewRequirement(nvcatypes.WorkloadInstanceTypeLabel, selection.Exists, nil)
	if err != nil {
		log.WithError(err).Error("Failed to parse ICMS instance type label requirement")
		return err
	}
	namespaceSelector := labels.NewSelector().Add(*namespaceSelectorReq)
	cj := imagecredential.NewUpdaterCronJob(cjName, c.imageCredentialHelperImage, namespaceSelector.String())
	// Use NVCA's service account to run the job for API access and image pull secrets.
	cj.Namespace = c.systemNamespace
	cj.Spec.JobTemplate.Spec.Template.Spec.ServiceAccountName = "nvca"

	// All Pods created by NVCA must use the same scheduler.
	if c.featureFlagFetcher.IsFeatureFlagEnabled(featureflag.KAIScheduler) {
		cj.Spec.JobTemplate.Spec.Template.Spec.SchedulerName = kaischeduler.SchedulerName
		labels := cj.Spec.JobTemplate.Spec.Template.GetLabels()
		if labels == nil {
			labels = make(map[string]string)
		}
		labels[kaischeduler.SchedulerQueueLabel] = kaischeduler.DefaultQueue
		cj.Spec.JobTemplate.Spec.Template.SetLabels(labels)
	}

	cjClient := c.clients.K8s.BatchV1().CronJobs(cj.Namespace)
	if _, err := cjClient.Get(ctx, cj.Name, metav1.GetOptions{}); err != nil {
		if !k8serrors.IsNotFound(err) {
			log.WithError(err).Error("Failed to get image credential updater CronJob")
			return err
		}
		if _, err := cjClient.Create(ctx, cj, metav1.CreateOptions{}); err != nil {
			log.WithError(err).Error("Failed to create image credential updater CronJob")
			return err
		}
	} else if _, err := cjClient.Update(ctx, cj, metav1.UpdateOptions{}); err != nil {
		log.WithError(err).Error("Failed to update image credential updater CronJob")
		return err
	}
	return nil
}
