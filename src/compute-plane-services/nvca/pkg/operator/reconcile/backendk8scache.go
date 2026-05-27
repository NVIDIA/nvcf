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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	cmnotel "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/otel"
	cmnsecret "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/secret"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/version"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/imdario/mergo"
	"github.com/sirupsen/logrus"
	oteltrace "go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned/scheme"
	nvcabeinformers "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/informers/externalversions"
	nvcabelister "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/listers/nvcf/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/cleanup"
	nvcaoperatorerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/errors"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/kubeclients"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/metrics"
	nvcaopotel "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/otel"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
)

const (
	AgentName                         = "ClusterAgent"
	ResyncInterval                    = 30 * time.Minute
	defaultNVCAImageRepo              = "nvcr.io/nvidia/nvcf-byoc/nvca"
	nvcfBackendCountResourceQuotaName = "nvcfbackend-count"
	defaultOTelCollectorImageRepo     = "nvcr.io/nvidia/nvcf-byoc/nvcf-otel-collector"
	defaultOTelCollectorImageTag      = "0.143.2"

	// Maintenance mode feature flags
	cordonMaintenanceFeatureFlag         = "CordonMaintenance"
	cordonAndDrainMaintenanceFeatureFlag = "CordonAndDrainMaintenance"
)

// These are mocked in tests.
var (
	newDynamicClient = func(_ *runtime.Scheme, config *rest.Config) (dynamic.Interface, error) {
		rc, err := rest.RESTClientFor(config)
		if err != nil {
			return nil, err
		}
		return dynamic.New(rc), nil
	}
	newDiscoverClient = func(_ kubernetes.Interface, config *rest.Config) (discovery.DiscoveryInterface, error) {
		discClient, err := discovery.NewDiscoveryClientForConfig(config)
		return discClient, err
	}
)

// BackendK8sCache encapsulates the IO interactions from NVCA Operator to
// to work on the backend k8s
type BackendK8sCache struct {
	resyncPeriod         time.Duration
	clients              *kubeclients.KubeClients
	httpClient           *http.Client
	operatorNamespace    string
	systemNamespace      string
	k8sVersionOverride   string
	ngcServiceKeyFetcher cmnsecret.TokenFetcher
	nvcaRunAsUserID      int64
	nvcaRunAsGroupID     int64

	nvcfBackendLister nvcabelister.NVCFBackendLister
	syncedFuncs       []cache.InformerSynced
	eventRecorder     record.EventRecorder
	eventBroadcaster  record.EventBroadcaster
	tracer            oteltrace.Tracer

	nvcaImageRepo        string
	gxCacheNamespace     string
	crowdstrikeNamespace string
	helmRepositoryPrefix string

	deploymentDeleteTimeout time.Duration

	enableGXCache   bool
	ddcsIPAllowList []string

	// NVCF worker config
	nvcfWorkerConfig    nvidiaiov1.NVCFWorkerConfig
	workloadTolerations []corev1.Toleration

	// K8s Cluster Network CIDRs string slice
	k8sClusterNetworkCIDRs []string

	// Either stage or prod
	envType nvidiaiov1.EnvType

	// Function deployment stages service URL
	functionDeploymentStagesServiceURL string

	now func() time.Time

	nvcaOperatorVersion string

	dispatchReconcileClusterFunc func(ctx context.Context)
	// container resource configs
	agentResources         corev1.ResourceRequirements
	webhookResources       corev1.ResourceRequirements
	otelCollectorResources corev1.ResourceRequirements
	deploymentConfig       nvidiaiov1.DeploymentConfig
	// generateImagePullSecret is passed in from the operator to the agent, if true,
	// the agent will generate an image pull secret for nvca Pods
	// using the NGC service key.
	generateImagePullSecret bool
	// additionalImagePullSecrets is a list of pre-existing imagePullSecret names
	// in the operator namespace that will be copied to the nvca-system namespace
	// and used by nvca Pods.
	additionalImagePullSecrets []corev1.LocalObjectReference

	// OTel collector image configuration
	otelCollectorImageRepo string
	otelCollectorImageTag  string

	// OTel collector enabled flag
	otelCollectorEnabled bool

	// NVCA tracing configuration propagated from the operator.
	nvcaOTELConfig *nvidiaiov1.OTELConfig

	// Environment variable overrides for workloads (base64-encoded JSON maps)
	functionEnvOverridesB64 string
	taskEnvOverridesB64     string

	// identitySource controls the identity mechanism: auto, psat, or spire.
	identitySource string

	// gracefulShutdown is set to true when the operator is shutting down gracefully.
	// When true, the reconciliation loop will skip cleanup of NVCFBackend resources
	// and let the shutdown handler manage the cleanup instead.
	gracefulShutdown atomic.Bool
}

// BackendK8sCacheBuilder builds Backendk8sCache and start related K8s
// informers, monitored K8s events are sent to a event channel that is
// returned by Start()
type BackendK8sCacheBuilder struct {
	*BackendK8sCache
	tracer oteltrace.Tracer
}

func NewBackendK8sCacheBuilder() *BackendK8sCacheBuilder {
	return &BackendK8sCacheBuilder{
		BackendK8sCache: &BackendK8sCache{
			resyncPeriod:                 5 * time.Second,
			deploymentDeleteTimeout:      60 * time.Second,
			nvcaImageRepo:                defaultNVCAImageRepo,
			now:                          time.Now,
			nvcaOperatorVersion:          version.ReleaseString(),
			dispatchReconcileClusterFunc: func(_ context.Context) {},
			agentResources:               corev1.ResourceRequirements{},
			webhookResources:             corev1.ResourceRequirements{},
			generateImagePullSecret:      true,
		},
		tracer: nvcaopotel.NewTracer(),
	}
}

// WithContainerResources sets default resource requirements for agent and webhook containers.
func (b *BackendK8sCacheBuilder) WithAgentConfig(agentConfig nvidiaiov1.AgentConfig) *BackendK8sCacheBuilder {
	b.BackendK8sCache.deploymentConfig = agentConfig.DeploymentConfig
	b.BackendK8sCache.nvcfWorkerConfig = agentConfig.NVCFWorkerConfig
	if agentConfig.AgentResources != nil {
		b.BackendK8sCache.agentResources = *agentConfig.AgentResources
	}
	if agentConfig.WebhookResources != nil {
		b.BackendK8sCache.webhookResources = *agentConfig.WebhookResources
	}
	return b
}

// WithContainerResources sets default resource requirements for agent and webhook containers.
func (b *BackendK8sCacheBuilder) WithContainerResources(agent, webhook, otelCollector corev1.ResourceRequirements) *BackendK8sCacheBuilder {
	b.BackendK8sCache.agentResources = agent
	b.BackendK8sCache.webhookResources = webhook
	b.BackendK8sCache.otelCollectorResources = otelCollector
	return b
}

func (b *BackendK8sCacheBuilder) WithClients(clients *kubeclients.KubeClients) *BackendK8sCacheBuilder {
	next := *b
	next.clients = clients
	return &next
}

func (b *BackendK8sCacheBuilder) WithSystemNamespace(systemNamespace string) *BackendK8sCacheBuilder {
	next := *b
	next.operatorNamespace = systemNamespace
	return &next
}

func (b *BackendK8sCacheBuilder) WithK8sVersionOverride(k8sVersionOverride string) *BackendK8sCacheBuilder {
	next := *b
	next.k8sVersionOverride = k8sVersionOverride
	return &next
}

func (b *BackendK8sCacheBuilder) WithNGCServiceKeyFetcher(ngcServiceKeyFetcher cmnsecret.TokenFetcher) *BackendK8sCacheBuilder {
	next := *b
	next.ngcServiceKeyFetcher = ngcServiceKeyFetcher
	return &next
}

func (b *BackendK8sCacheBuilder) WithDDCSIPAllowList(ddcsIPAllowList []string) *BackendK8sCacheBuilder {
	next := *b
	next.ddcsIPAllowList = ddcsIPAllowList
	return &next
}

func (b *BackendK8sCacheBuilder) WithOTelTracer(tracer oteltrace.Tracer) *BackendK8sCacheBuilder {
	next := *b
	next.tracer = tracer
	return &next
}

func (b *BackendK8sCacheBuilder) WithNVCAImageRepo(nvcaImageRepo string) *BackendK8sCacheBuilder {
	next := *b
	next.nvcaImageRepo = nvcaImageRepo
	return &next
}

func (b *BackendK8sCacheBuilder) WithUIDGIDOverride(uid, gid int64) *BackendK8sCacheBuilder {
	next := *b
	next.nvcaRunAsUserID = uid
	next.nvcaRunAsGroupID = gid
	return &next
}

func (b *BackendK8sCacheBuilder) WithGXCache(ns string, on bool) *BackendK8sCacheBuilder {
	next := *b
	next.gxCacheNamespace = ns
	next.enableGXCache = on
	return &next
}

func (b *BackendK8sCacheBuilder) WithCrowdstrike(ns string) *BackendK8sCacheBuilder {
	next := *b
	next.crowdstrikeNamespace = ns
	return &next
}

func (b *BackendK8sCacheBuilder) WithHelmRepositoryPrefix(hrepo string) *BackendK8sCacheBuilder {
	next := *b
	next.helmRepositoryPrefix = hrepo
	return &next
}

func (b *BackendK8sCacheBuilder) WithNVCFWorkerConfig(
	cacheMountOptionsEnabled bool,
	cacheMountOptions string,
	workerDegradationPeriod time.Duration,
) *BackendK8sCacheBuilder {
	next := *b
	next.nvcfWorkerConfig = nvidiaiov1.NVCFWorkerConfig{
		CacheMountOptionsEnabled: cacheMountOptionsEnabled,
		CacheMountOptions:        cacheMountOptions,
		WorkerDegradationPeriod:  workerDegradationPeriod,
	}
	return &next
}

func (b *BackendK8sCacheBuilder) WithWorkloadTolerations(tolerations []corev1.Toleration) *BackendK8sCacheBuilder {
	next := *b
	next.workloadTolerations = append([]corev1.Toleration(nil), tolerations...)
	return &next
}

func (b *BackendK8sCacheBuilder) WithK8sClusterNetworkCIDRs(cidrs []string) *BackendK8sCacheBuilder {
	next := *b
	next.k8sClusterNetworkCIDRs = cidrs
	return &next
}

func (b *BackendK8sCacheBuilder) WithEnvType(envType nvidiaiov1.EnvType) *BackendK8sCacheBuilder {
	next := *b
	next.envType = envType
	return &next
}

func (b *BackendK8sCacheBuilder) WithFunctionDeploymentStagesServiceURL(url string) *BackendK8sCacheBuilder {
	next := *b
	next.functionDeploymentStagesServiceURL = url
	return &next
}

func (b *BackendK8sCacheBuilder) WithDispatchReconcileClusterFunc(dispatchReconcileClusterFunc func(ctx context.Context)) *BackendK8sCacheBuilder {
	next := *b
	next.dispatchReconcileClusterFunc = dispatchReconcileClusterFunc
	return &next
}

func (b *BackendK8sCacheBuilder) WithGenerateImagePullSecret(generateImagePullSecret bool) *BackendK8sCacheBuilder {
	next := *b
	next.generateImagePullSecret = generateImagePullSecret
	return &next
}

func (b *BackendK8sCacheBuilder) WithAdditionalImagePullSecrets(secrets []corev1.LocalObjectReference) *BackendK8sCacheBuilder {
	next := *b
	next.additionalImagePullSecrets = secrets
	return &next
}

func (b *BackendK8sCacheBuilder) WithOTelCollectorConfig(repo, tag string, enabled bool) *BackendK8sCacheBuilder {
	next := *b
	next.otelCollectorImageRepo = repo
	next.otelCollectorImageTag = tag
	next.otelCollectorEnabled = enabled
	return &next
}

func (b *BackendK8sCacheBuilder) WithNVCAOTELConfig(otelConfig *nvidiaiov1.OTELConfig) *BackendK8sCacheBuilder {
	next := *b
	if otelConfig != nil {
		next.nvcaOTELConfig = otelConfig.DeepCopy()
	} else {
		next.nvcaOTELConfig = nil
	}
	return &next
}

// WithEnvOverrides sets the environment variable overrides for function and task workloads.
func (b *BackendK8sCacheBuilder) WithEnvOverrides(functionEnvOverridesB64, taskEnvOverridesB64 string) *BackendK8sCacheBuilder {
	next := *b
	next.functionEnvOverridesB64 = functionEnvOverridesB64
	next.taskEnvOverridesB64 = taskEnvOverridesB64
	return &next
}

func (b *BackendK8sCacheBuilder) WithIdentitySource(identitySource string) *BackendK8sCacheBuilder {
	next := *b
	next.identitySource = identitySource
	return &next
}

func (b *BackendK8sCacheBuilder) Start(ctx context.Context) (*BackendK8sCache, <-chan *core.Event, error) {
	log := core.GetLogger(ctx)
	resyncPeriod := b.resyncPeriod

	eventBroadcaster := record.NewBroadcaster()

	c := &BackendK8sCache{
		resyncPeriod:         resyncPeriod,
		clients:              b.clients,
		eventBroadcaster:     eventBroadcaster,
		eventRecorder:        eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "nvca-operator"}),
		systemNamespace:      b.systemNamespace,
		ngcServiceKeyFetcher: b.ngcServiceKeyFetcher,
		k8sVersionOverride:   b.k8sVersionOverride,
		tracer:               b.tracer,
		nvcaImageRepo:        b.nvcaImageRepo,
		nvcaRunAsUserID:      b.nvcaRunAsUserID,
		nvcaRunAsGroupID:     b.nvcaRunAsGroupID,
		httpClient: &http.Client{
			Timeout: 1 * time.Second,
		},
		gxCacheNamespace:                   b.gxCacheNamespace,
		helmRepositoryPrefix:               b.helmRepositoryPrefix,
		enableGXCache:                      b.enableGXCache,
		ddcsIPAllowList:                    b.ddcsIPAllowList,
		k8sClusterNetworkCIDRs:             b.k8sClusterNetworkCIDRs,
		envType:                            b.envType,
		functionDeploymentStagesServiceURL: b.functionDeploymentStagesServiceURL,
		now:                                b.now,
		nvcaOperatorVersion:                b.nvcaOperatorVersion,
		dispatchReconcileClusterFunc:       b.dispatchReconcileClusterFunc,
		agentResources:                     b.agentResources,
		webhookResources:                   b.webhookResources,
		otelCollectorResources:             b.otelCollectorResources,
		nvcfWorkerConfig:                   b.nvcfWorkerConfig,
		workloadTolerations:                append([]corev1.Toleration(nil), b.workloadTolerations...),
		deploymentConfig:                   b.deploymentConfig,
		generateImagePullSecret:            b.generateImagePullSecret,
		additionalImagePullSecrets:         b.additionalImagePullSecrets,
		otelCollectorImageRepo:             b.otelCollectorImageRepo,
		otelCollectorImageTag:              b.otelCollectorImageTag,
		otelCollectorEnabled:               b.otelCollectorEnabled,
		nvcaOTELConfig:                     b.nvcaOTELConfig,
		functionEnvOverridesB64:            b.functionEnvOverridesB64,
		taskEnvOverridesB64:                b.taskEnvOverridesB64,
		identitySource:                     b.identitySource,
	}

	if c.operatorNamespace == "" {
		c.operatorNamespace = NVCAOperatorNamespace
	}

	out := make(chan *core.Event)

	// Watch for NVCFBackend Resources
	{
		e := nvcabeinformers.NewSharedInformerFactoryWithOptions(
			b.clients.NVCAOP,
			ResyncInterval,
			nvcabeinformers.WithNamespace(c.operatorNamespace))

		i1 := e.Nvcf().V1().NVCFBackends()
		c.nvcfBackendLister = i1.Lister()
		c.syncedFuncs = append(c.syncedFuncs, i1.Informer().HasSynced)
		_, err := i1.Informer().AddEventHandler(&cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				_ = cmnotel.InvokeWithSpan(ctx, b.tracer, "nvca-operator.BackendK8sCache.NVCFBackend.AddHandler",
					func(ctx context.Context) error {
						req, ok := obj.(*nvidiaiov1.NVCFBackend)
						if !ok {
							err := fmt.Errorf("wrong object in NVCFBackend informer: %v", obj)
							log.Warn(err.Error())
							return err
						}
						err := c.SyncNVCFBackend(ctx, req, false)
						if err != nil {
							// TODO we should have all of these flow back to the event worker queue so changes can be deduped
							if nvcaoperatorerrors.IsFatal(err) {
								nvcaoperatorerrors.ExitReason(ctx, err)
								log.WithError(err).
									Fatalf("failed to sync NVCFBackend %v/%v, will not be requeued, NVCA operator will exit",
										req.Namespace, req.Name)
							}
							log.WithError(err).Errorf("failed to sync NVCFBackend %v/%v", req.Namespace, req.Name)
							return err
						}
						return nil
					}, oteltrace.WithSpanKind(oteltrace.SpanKindConsumer))
			},
			DeleteFunc: func(obj any) {
				_ = cmnotel.InvokeWithSpan(ctx, b.tracer, "nvca-operator.BackendK8sCache.NVCFBackend.DeleteHandler",
					func(ctx context.Context) error {
						nb, ok := obj.(*nvidiaiov1.NVCFBackend)
						if !ok {
							err := fmt.Errorf("wrong object in NVCFBackend informer: %v", obj)
							log.Warn(err.Error())
							return err
						}
						return c.cleanupResources(ctx, nb)
					}, oteltrace.WithSpanKind(oteltrace.SpanKindConsumer))
			},
			UpdateFunc: func(oldObj, newObj any) {
				_ = cmnotel.InvokeWithSpan(ctx, b.tracer, "nvca-operator.BackendK8sCache.NVCFBackend.UpdateHandler",
					func(ctx context.Context) error {
						oldNB, ok := oldObj.(*nvidiaiov1.NVCFBackend)
						if !ok {
							err := fmt.Errorf("wrong old object in NVCFBackend informer: %v", oldObj)
							log.Warn(err.Error())
							return err
						}
						newNB, ok := newObj.(*nvidiaiov1.NVCFBackend)
						if !ok {
							err := fmt.Errorf("wrong new object in NVCFBackend informer: %v", newObj)
							log.Warn(err.Error())
							return err
						}

						// Any updates to spec or overrides should trigger a reconcile.
						oldNBTmp := oldNB.DeepCopy()
						if err := mergeOverrides(oldNBTmp); err != nil {
							log.WithError(err).Error("Failed to merge old backend overrides")
							return err
						}
						newNBTmp := newNB.DeepCopy()
						if err := mergeOverrides(newNBTmp); err != nil {
							log.WithError(err).Error("Failed to merge updated backend overrides")
							return err
						}

						forceRollout := false
						if hasNVCFBackendRolloutAnnotationChanged(ctx, oldNBTmp.Annotations, newNBTmp.Annotations) {
							log.Infof("NVCFBackend %s has force rollout annotation, forcing rollout of NVCA", newNB.Name)
							forceRollout = true
						} else if objectsEqual(oldNBTmp.Spec, newNBTmp.Spec) {
							log.Debug("Updated NVCFBackend has no spec changes, skipping sync")
							return nil
						}

						log.Infof("Syncing updated NVCFBackend %s spec", newNB.Name)

						err := c.SyncNVCFBackend(ctx, newNB, forceRollout)
						if err != nil {
							// TODO we should have all of these flow back to the event worker queue so changes can be deduped
							if nvcaoperatorerrors.IsFatal(err) {
								nvcaoperatorerrors.ExitReason(ctx, err)
								log.WithError(err).
									Fatalf("failed to sync NVCFBackend %v/%v, will not be requeued, NVCA operator will exit",
										newNB.Namespace, newNB.Name)
							}
							log.WithError(err).Errorf("failed to sync NVCFBackend %v/%v", newNB.Namespace, newNB.Name)
							return err
						}
						return nil
					}, oteltrace.WithSpanKind(oteltrace.SpanKindConsumer))
			},
		})
		if err != nil {
			log.WithError(err).Error("failed to add event handler for NVCFBackends")
			return nil, nil, err
		}
		// add configMapInformers for Network Policy for k8s backend only
		if err := addConfigMapInformers(ctx, c); err != nil {
			return nil, nil, err
		}
		e.Start(ctx.Done())
	}

	// Wait for informer caches to sync before processing events.
	// This ensures the lister sees all existing objects and watch events are processed.
	log.Info("Waiting for informer caches to sync")
	if !cache.WaitForCacheSync(ctx.Done(), c.syncedFuncs...) {
		return nil, nil, fmt.Errorf("failed to sync informer caches")
	}

	log.Info("Starting NVCA webhook TLS cert rotator")
	go c.runTLSCertRotate(ctx, defaultCertRefreshPeriod)

	return c, out, nil
}

// addConfigMapInformers adds and starts an informer on ConfigMaps containing
// potentially dynamic data to ensure any changes are propagated to all necessary namespaces.
func addConfigMapInformers(ctx context.Context, c *BackendK8sCache) error {
	log := core.GetLogger(ctx)

	f := informers.NewSharedInformerFactoryWithOptions(
		c.clients.K8s,
		30*time.Minute,
		informers.WithNamespace(c.operatorNamespace))

	cmi := f.Core().V1().ConfigMaps()
	c.syncedFuncs = append(c.syncedFuncs, cmi.Informer().HasSynced)
	_, err := cmi.Informer().AddEventHandler(&cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			_ = cmnotel.InvokeWithSpan(ctx, c.tracer, "nvca-operator.BackendK8sCache.ConfigMapInformer.AddHandler",
				func(_ context.Context) error {
					cm, ok := obj.(*corev1.ConfigMap)
					if !ok {
						log.Errorf("Wrong object in ConfigMap informer Add handler: %v", obj)
						return fmt.Errorf("invalid object received")
					}
					if cm.Name == cleanup.ShutdownSentinelConfigMapName {
						log.Debugf("found %s configmap update, skipping", cm.Name)
						return nil
					}
					return nil
				}, oteltrace.WithSpanKind(oteltrace.SpanKindConsumer))
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			_ = cmnotel.InvokeWithSpan(ctx, c.tracer, "nvca-operator.BackendK8sCache.ConfigMapInformer.UpdateHandler",
				func(_ context.Context) error {
					log.Debug("Got ConfigMap update")

					oldCM, ok := oldObj.(*corev1.ConfigMap)
					if !ok {
						log.Errorf("Wrong object in ConfigMap informer: %v", oldObj)
						return fmt.Errorf("invalid object received")
					}
					newCM, ok := newObj.(*corev1.ConfigMap)
					if !ok {
						log.Errorf("Wrong object in ConfigMap informer: %v", newObj)
						return fmt.Errorf("invalid object received")
					}

					log = log.WithFields(logrus.Fields{
						"configmapName": newCM.Name,
					})

					switch newCM.Name {
					case nvcfCustomNetworkPoliciesConfigMapName, nvcfCustomAnnotationsConfigMapName, nvcfBackendChartDefaultsConfigMapName:
						log.Debugf("found %s configmap update, syncing current NVCFBackend", newCM.Name)
						diff := cmp.Diff(oldCM.Data, newCM.Data, cmpopts.EquateEmpty())
						log.WithField("diff", diff).Debugf("configmap data diff")
						if diff != "" {
							log.Info("configmap data has changed, forcing rollout")
							if err := c.SyncNVCFCurrentBackend(ctx, true); err != nil {
								if nvcaoperatorerrors.IsFatal(err) {
									nvcaoperatorerrors.ExitReason(ctx, err)
									log.WithError(err).Fatalf("failed to sync current NVCFBackend, will not be requeued, NVCA operator will exit")
								}
								log.WithError(err).Error("failed to sync current NVCFBackend")
								return err
							}
						}
						log.Debug("successfully synced current NVCFBackend")
						return nil
					case nvcfBackendHelmManagedConfigMapName:
						log.Debugf("found %s configmap update, syncing current NVCFBackend", newCM.Name)
						diff := cmp.Diff(oldCM.Data, newCM.Data, cmpopts.EquateEmpty())
						log.WithField("diff", diff).Debugf("configmap data diff")
						if diff != "" {
							log.Infof("configmap %s data has changed, dispatch cluster reconcile event", newCM.Name)
							c.dispatchReconcileClusterFunc(ctx)
						}
						log.Debug("successfully dispatched cluster reconcile event")
						return nil
					case nvcfBackendSelfManagedConfigMapName:
						log.Debugf("found %s configmap update, syncing current NVCFBackend", newCM.Name)
						diff := cmp.Diff(oldCM.Data, newCM.Data, cmpopts.EquateEmpty())
						log.WithField("diff", diff).Debugf("configmap data diff")
						if diff != "" {
							log.Infof("configmap %s data has changed, dispatch cluster reconcile event", newCM.Name)
							c.dispatchReconcileClusterFunc(ctx)
						}
						log.Debug("successfully dispatched cluster reconcile event")
						return nil
					case cleanup.ShutdownSentinelConfigMapName:
						log.Debugf("found %s configmap update, skipping", newCM.Name)
						return nil
					}
					return nil
				}, oteltrace.WithSpanKind(oteltrace.SpanKindConsumer))
		},
	})
	if err != nil {
		log.WithError(err).Error("failed to add event handler for ConfigMaps")
		return err
	}
	f.Start(ctx.Done())
	log.Infof("added configmap informers")
	return nil
}

func (bc *BackendK8sCache) CreateOrUpdateNVCFBackend(ctx context.Context, deltaNB *nvidiaiov1.NVCFBackend) error {
	log := core.GetLogger(ctx)
	deltaNB = deltaNB.DeepCopy()
	log.Debugf("create or update NVCFBackend %s/%s", bc.operatorNamespace, deltaNB.Name)
	deltaNB.Namespace = bc.operatorNamespace
	if bc.nvcaOTELConfig != nil {
		otelCfg := bc.nvcaOTELConfig.DeepCopy()
		// Preserve endpoint from existing spec/overrides if not set by the operator config
		if otelCfg.Endpoint == "" && deltaNB.Spec.FeatureGate.OTELConfig != nil {
			otelCfg.Endpoint = deltaNB.Spec.FeatureGate.OTELConfig.Endpoint
		}
		deltaNB.Spec.FeatureGate.OTELConfig = otelCfg
	}

	// Wait for nvcf-backend resource quota to be ready with the count
	backendCountQuota, err := bc.clients.K8s.CoreV1().ResourceQuotas(bc.operatorNamespace).Get(ctx,
		nvcfBackendCountResourceQuotaName,
		metav1.GetOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		log.WithError(err).Errorf("failed to retrieve %s/%s", bc.operatorNamespace, nvcfBackendCountResourceQuotaName)
	} else if err == nil {
		// Verify the backend count is calculated
		if _, ok := backendCountQuota.Status.Used["count/nvcfbackends.nvcf.nvidia.io"]; !ok {
			log.Infof("waiting on resource quota %s to initialize before creating nvcfbackend %s/%s",
				nvcfBackendCountResourceQuotaName, bc.operatorNamespace, deltaNB.Name)
			return nil
		}
	}

	nbClient := bc.clients.NVCAOP.NvcfV1().NVCFBackends(bc.operatorNamespace)

	if _, err := nbClient.Get(ctx, deltaNB.Name, metav1.GetOptions{}); k8serrors.IsNotFound(err) {
		if _, err = nbClient.Create(ctx, deltaNB, metav1.CreateOptions{}); err != nil {
			log.WithError(err).Errorf("Failed to create nvcfbackend")
			return err
		}
		log.Infof("Successfully created nvcfbackend")
		return nil
	} else if err != nil {
		log.WithError(err).Errorf("Failed to get nvcfbackend")
		return fmt.Errorf("failed to get latest version of backend: %v", err)
	}

	// 2. Apply the delta
	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Retrieve the latest version of NVCFBackend before attempting update
		// RetryOnConflict uses exponential backoff to avoid exhausting the apiserver
		nbLat, err := nbClient.Get(ctx, deltaNB.Name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get latest version of backend: %v", err)
		}

		// Update on-cluster spec to the remote's, but preserve overrides.
		overrides := nbLat.Spec.Overrides
		deltaNB.Spec.DeepCopyInto(&nbLat.Spec)
		nbLat.Spec.Overrides = overrides

		_, updateErr := nbClient.Update(ctx, nbLat, metav1.UpdateOptions{})
		return updateErr
	})
	if retryErr != nil {
		return fmt.Errorf("failed to update NVCFBackend for %v/%v, err: %v", bc.operatorNamespace, deltaNB.Name, retryErr)
	}
	return nil
}

func (bc *BackendK8sCache) DeleteNVCFBackend(ctx context.Context, backend *nvidiaiov1.NVCFBackend) error {
	return cleanup.DeleteNVCFBackend(ctx, bc.clients.NVCAOP, backend)
}

func (bc *BackendK8sCache) SyncAllNVCFBackends(ctx context.Context) error {
	log := core.GetLogger(ctx)
	log.Debugf("sync'ing all NVCFBackends in %v", bc.operatorNamespace)
	var err error

	allBEs, e := bc.nvcfBackendLister.List(labels.Everything())
	if e != nil {
		return fmt.Errorf("failed to list NVCFBackends in the compute, err: %v", e)
	}
	// If we have more than one NVCFBackend, we need to fail since there can only be one
	if len(allBEs) > 1 {
		return nvcaoperatorerrors.FatalError(fmt.Errorf("found more than one NVCFBackend in the compute, err: %v", e))
	}
	for _, be := range allBEs {
		err = bc.SyncNVCFBackend(ctx, be, false)
		if err != nil {
			if nvcaoperatorerrors.IsFatal(err) {
				nvcaoperatorerrors.ExitReason(ctx, err)
				log.WithError(err).Fatalf("failed to sync NVCFBackend %v/%v, will not be requeued, NVCA operator will exit", be.Namespace, be.Name)
			}
			log.WithError(err).Errorf("failed to sync NVCFBackend %v/%v", be.Namespace, be.Name)
			continue
		}
		err = cmnotel.InvokeWithSpan(ctx, bc.tracer, "BackendK8sCache.SyncNVCFBackendHealth", func(ctx context.Context) error {
			return bc.SyncNVCFBackendHealth(ctx, be)
		}, oteltrace.WithSpanKind(oteltrace.SpanKindConsumer), oteltrace.WithAttributes(nvcaopotel.GetOTelAttributesFromNVCFBackend(be)...))
		if err != nil {
			log.WithError(err).Errorf("failed to sync NVCFBackend health %v/%v", be.Namespace, be.Name)
			continue
		}
		log.Debugf("successfully sync'ed NVCFBackend %v/%v", be.Namespace, be.Name)
	}
	return nil
}

// SetGracefulShutdown sets the graceful shutdown flag.
// When true, the reconciliation loop will skip cleanup of NVCFBackend resources
// and let the shutdown handler manage the cleanup instead.
func (bc *BackendK8sCache) SetGracefulShutdown(shutdown bool) {
	bc.gracefulShutdown.Store(shutdown)
}

// IsGracefulShutdown returns true if the operator is in graceful shutdown mode.
func (bc *BackendK8sCache) IsGracefulShutdown() bool {
	return bc.gracefulShutdown.Load()
}

// SyncNVCFBackendByName syncs an NVCFBackend by name with forceRollout=true.
// This is useful for forcing reconciliation during shutdown to propagate configuration changes.
func (bc *BackendK8sCache) SyncNVCFBackendByName(ctx context.Context, name, namespace string) error {
	log := core.GetLogger(ctx)

	nb, err := bc.clients.NVCAOP.NvcfV1().NVCFBackends(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get NVCFBackend %s/%s: %w", namespace, name, err)
	}

	log.Infof("Force syncing NVCFBackend %s/%s", namespace, name)
	return bc.SyncNVCFBackend(ctx, nb, true)
}

type nvcfBackendHealthResponse struct {
	Status     nvidiaiov1.AgentStatus                        `json:"status,omitempty"`
	K8sVersion string                                        `json:"k8sVersion,omitempty"`
	GPUUsage   map[nvidiaiov1.GPUName]nvidiaiov1.GPUResource `json:"gpuUsage,omitempty"`
}

func (bc *BackendK8sCache) SyncNVCFBackendHealth(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
	log := core.GetLogger(ctx)
	var err error
	log.Debugf("sync'ing NVCFBackend health %v/%v", nb.Namespace, nb.Name)

	if !nb.ObjectMeta.DeletionTimestamp.IsZero() {
		bc.eventRecorder.Event(nb, corev1.EventTypeNormal,
			string(nvcaoptypes.EventCategoryHealth), "Skipping health check, backend purge in progress")
		return nil
	}

	localNVCAServiceURL, err := makeNVCAHealthzURL(nb)
	if err != nil {
		return fmt.Errorf("make NVCA /healthz URL: %v", err)
	}

	// Query the service to ensure /healthz endpoint.
	nvcaHealthResp := bc.getNVCAHealth(ctx, localNVCAServiceURL)

	// If the agent responded with a healthy status,
	// also check if the number of replicas is as expected.
	// Otherwise the agent's health is unhealthy or unknown.
	evType := corev1.EventTypeNormal
	if nvcaHealthResp.Status == nvidiaiov1.AgentStatusHealthy {
		dep, err := bc.clients.K8s.AppsV1().Deployments(getSystemNamespace(nb)).Get(ctx, nvcaoptypes.NVCAModuleName, metav1.GetOptions{})
		if err != nil {
			log.WithError(err).Error("Get NVCA deployment")
			nvcaHealthResp.Status = nvidiaiov1.AgentStatusUnknown
			evType = corev1.EventTypeWarning
		} else if dep.Status.ReadyReplicas == 0 {
			log.Warn("NVCA deployment has no ready replicas")
			nvcaHealthResp.Status = nvidiaiov1.AgentStatusUnhealthy
			evType = corev1.EventTypeWarning
		}
	} else {
		evType = corev1.EventTypeWarning
	}

	if shouldUpdateNVCFStatus(nb.Status, nvcaHealthResp) {
		bc.eventRecorder.Eventf(nb, evType,
			string(nvcaoptypes.EventCategoryHealth), "%v health changed from '%v' to '%v'",
			AgentName, nb.Status.AgentStatus, nvcaHealthResp.Status)

		retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Retrieve the latest version of NVCFBackend before attempting update
			// RetryOnConflict uses exponential backoff to avoid exhausting the apiserver
			nbLat, err := bc.clients.NVCAOP.NvcfV1().NVCFBackends(bc.operatorNamespace).Get(ctx, nb.Name, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("failed to get latest version of backend: %v", err)
			}

			// update the AgentStatus & TimeStamp
			nbLat.Status.AgentStatus = nvcaHealthResp.Status
			nbLat.Status.KubernetesVersion = nvcaHealthResp.K8sVersion
			nbLat.Status.GPUUsage = nvcaHealthResp.GPUUsage
			nbLat.Status.LastUpdatedAgentStatus = &metav1.Time{Time: core.GetCurrentTime(ctx)}

			_, updateErr := bc.clients.NVCAOP.NvcfV1().NVCFBackends(bc.operatorNamespace).UpdateStatus(ctx, nbLat, metav1.UpdateOptions{})
			return updateErr
		})
		if retryErr != nil {
			return fmt.Errorf("failed to update health status for %v/%v, err: %v", nb.Namespace, nb.Name, retryErr)
		}
	}
	return nil
}

var makeNVCAHealthzURL = func(nb *nvidiaiov1.NVCFBackend) (string, error) {
	port, err := getNVCAListenPortHTTP(nb)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("http://%s.%s:%d/healthz",
		nvcaoptypes.NVCAModuleName, getSystemNamespace(nb), port,
	), nil
}

func (bc *BackendK8sCache) getNVCAHealth(ctx context.Context, svcURL string) nvcfBackendHealthResponse {
	log := core.GetLogger(ctx)

	nvcaHealthResp := nvcfBackendHealthResponse{
		Status: nvidiaiov1.AgentStatusUnknown,
	}
	healthReq, err := http.NewRequest(http.MethodGet, svcURL, nil)
	if err != nil {
		log.WithError(err).Errorf("Failed to create request %s %s", http.MethodGet, svcURL)
		return nvcaHealthResp
	}

	healthReq.Header.Set("Accept", "application/json")
	resp, err := bc.httpClient.Do(healthReq)
	if err != nil {
		log.WithError(err).Errorf("Failed to do request %s %s", http.MethodGet, svcURL)
		return nvcaHealthResp
	}
	defer resp.Body.Close()

	// Read with 1MB max response, and ensure the response has a valid status field,
	// otherwise keep the default 'unknown' status.
	var body nvcfBackendHealthResponse
	if resp.StatusCode != http.StatusOK {
		log.WithError(fmt.Errorf("status: %s", resp.Status)).Errorf("unexpected status code for request %s %s", healthReq.Method, healthReq.URL)
		return nvcaHealthResp
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&body); err != nil {
		log.WithError(err).Errorf("Failed to decode request %s %s", http.MethodGet, svcURL)
	} else if body.Status == "" {
		log.Errorf("Unexpected response body: %#v", body)
	} else {
		nvcaHealthResp = body
	}

	return nvcaHealthResp
}

func shouldUpdateNVCFStatus(nbStatus nvidiaiov1.NVCFBackendStatus, nvcaStatus nvcfBackendHealthResponse) bool {
	return nbStatus.AgentStatus != nvcaStatus.Status ||
		nbStatus.KubernetesVersion != nvcaStatus.K8sVersion ||
		!objectsEqual(nbStatus.GPUUsage, nvcaStatus.GPUUsage)
}

func (bc *BackendK8sCache) SyncNVCFCurrentBackend(ctx context.Context, forceRollout bool) error {
	log := core.GetLogger(ctx)
	log.Debug("sync'ing current NVCFBackend")

	// Fetch the current backend from the cache
	backends, err := bc.nvcfBackendLister.List(labels.Everything())
	if err != nil {
		log.WithError(err).Error("failed to list NVCFBackends")
		return fmt.Errorf("failed to list NVCFBackends: %w", err)
	}
	if len(backends) == 0 {
		// No backends present skipping NVCF sync
		log.Debug("no NVCFBackends present, skipping sync")
		return nil
	} else if len(backends) > 1 {
		return nvcaoperatorerrors.FatalError(fmt.Errorf("expected 1 NVCFBackend, found %d", len(backends)))
	}

	return bc.SyncNVCFBackend(ctx, backends[0].DeepCopy(), forceRollout)
}

func (bc *BackendK8sCache) SyncNVCFBackend(ctx context.Context, nb *nvidiaiov1.NVCFBackend, forceRollout bool) error {
	return cmnotel.InvokeWithSpan(ctx, bc.tracer,
		"nvca-operator.BackendK8sCache.SyncNVCFBackend",
		func(ctx context.Context) error {
			return bc.syncNVCFBackend(ctx, nb, forceRollout)
		},
		oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
		oteltrace.WithAttributes(nvcaopotel.GetOTelAttributesFromNVCFBackend(nb)...))
}

func (bc *BackendK8sCache) syncNVCFBackend(ctx context.Context, nb *nvidiaiov1.NVCFBackend, forceRollout bool) error {
	log := core.GetLogger(ctx).WithFields(logrus.Fields{
		"backend":   nb.Name,
		"namespace": nb.Namespace,
	})

	// If the backend is being deleted, we need to cleanup the resources
	if !nb.ObjectMeta.DeletionTimestamp.IsZero() {
		// Give the preStop hook time to set the graceful shutdown flag.
		// This handles the race where NVCFBackend deletion is processed
		// before the preStop hook has a chance to fire and set the flag.
		const gracefulShutdownCheckRetries = 3
		gracefulShutdownActive := false
		for i := 0; i < gracefulShutdownCheckRetries; i++ {
			if bc.gracefulShutdown.Load() {
				gracefulShutdownActive = true
				break
			}
			if i < gracefulShutdownCheckRetries-1 {
				log.Debug("backend being deleted, waiting to check for graceful shutdown signal")
				time.Sleep(time.Second)
			}
		}

		if gracefulShutdownActive {
			// Graceful shutdown is in progress - skip cleanup but continue with reconciliation.
			// This allows ForceSync to propagate CordonAndDrain config to NVCA deployment.
			// The shutdown handler will handle actual cleanup after draining completes.
			log.Info("backend is being deleted but graceful shutdown in progress, skipping cleanup but continuing reconciliation")
		} else {
			// No graceful shutdown signal after retries - proceed with normal cleanup.
			// This handles direct kubectl delete of NVCFBackend without helm uninstall.
			log.Info("backend is being deleted, cleaning up resources")
			return bc.cleanupResources(ctx, nb)
		}
	}

	var err error
	log.Debugf("sync'ing NVCFBackend")

	// The object is not being deleted, so if it does not have our finalizer,
	// then lets add the finalizer and update the object.
	if !containsString(nb.ObjectMeta.Finalizers, cleanup.NVCAOperatorFinalizer) {
		retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Retrieve the latest version of NVCFBackend before attempting update
			// RetryOnConflict uses exponential backoff to avoid exhausting the apiserver
			nbLat, err := bc.clients.NVCAOP.NvcfV1().NVCFBackends(bc.operatorNamespace).Get(ctx, nb.Name, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("failed to get latest version of backend: %v", err)
			}

			// exit if finalizer already present
			if containsString(nbLat.ObjectMeta.Finalizers, cleanup.NVCAOperatorFinalizer) {
				log.Debugf("Finalizer %s present in NVCFBackend resource %s", cleanup.NVCAOperatorFinalizer, nbLat.Name)
				return nil
			}

			// add finalizer if the latest backend does not have it
			nbLat.ObjectMeta.Finalizers = append(nbLat.ObjectMeta.Finalizers, cleanup.NVCAOperatorFinalizer)
			_, updateErr := bc.clients.NVCAOP.NvcfV1().NVCFBackends(bc.operatorNamespace).Update(ctx, nbLat, metav1.UpdateOptions{})
			return updateErr
		})
		if retryErr != nil {
			return fmt.Errorf("failed to add finalizer for %v/%v, err: %v", nb.Namespace, nb.Name, retryErr)
		}
		bc.eventRecorder.Eventf(nb, corev1.EventTypeNormal,
			string(nvcaoptypes.EventCategoryInstall), "Finalizer added for %v", nb.Name)
	}

	// Get latest backend not from cache.
	nb, err = bc.clients.NVCAOP.NvcfV1().NVCFBackends(bc.operatorNamespace).Get(ctx, nb.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get latest version of backend: %v", err)
	}

	nbMerged := nb.DeepCopy()
	if err := mergeOverrides(nbMerged); err != nil {
		return err
	}

	// version cannot be empty
	if nbMerged.Spec.Version == "" {
		return fmt.Errorf("%v version cannot be empty", nbMerged.Name)
	}

	// add input verification if these fail we should not proceed with the sync
	if err := bc.validateNVCFBackend(ctx, nbMerged); err != nil {
		return nvcaoperatorerrors.FatalError(err)
	}

	// Ensure CRDs always exist, regardless of rollout status.
	// CRDs are foundational infrastructure and must be present before any rollout.
	err = bc.setupCRDs(ctx)
	if err != nil {
		return fmt.Errorf("failed to SetupCRDs for NVCFBackend %v/%v, err: %w", nbMerged.Namespace, nbMerged.Name, err)
	}

	// Get effective config by merging ICMS and local configs for comparison
	effectiveConfigForComparison, err := bc.getEffectiveAgentConfig(ctx, nbMerged)
	if err != nil {
		return err
	}

	// Get effective network CIDRs - use spec values (from NGC) if available, otherwise fall back to Helm values
	effectiveK8sNetworkCIDRs := bc.getEffectiveK8sNetworkCIDRs(nbMerged)

	// Build the list of nvcaRolloutChecks
	nvcaRolloutChecks := []func() bool{
		func() bool { return forceRollout },
		hasNVCFBackendChangedCheck(ctx, nbMerged),
		hasNetworkConfigChangedCheck(ctx, bc.ddcsIPAllowList, effectiveK8sNetworkCIDRs, nbMerged.Status),
		hasNVCAResourcesChangedCheck(ctx, bc.agentResources, bc.webhookResources, bc.otelCollectorResources, nbMerged.Status),
		hasOTelCollectorConfigChangedCheck(ctx, effectiveConfigForComparison.OTelCollectorConfig, nbMerged.Status),
		hasAgentDeploymentConfigChanged(ctx, effectiveConfigForComparison.DeploymentConfig, nbMerged.Status),
		hasAgentWorkerConfigOptionsChanged(ctx, effectiveConfigForComparison.NVCFWorkerConfig, nbMerged.Status),
		hasEnvOverridesChangedCheck(ctx, bc.functionEnvOverridesB64, bc.taskEnvOverridesB64, nbMerged.Status),
	}

	// Only check NGC service API key for NGC-managed clusters (or empty, which defaults to NGC-managed)
	if nbMerged.Spec.ClusterSource != nvcaoptypes.ClusterSourceSelfHosted {
		nvcaRolloutChecks = append(nvcaRolloutChecks, hasNGCServiceAPIKeyChangedCheck(ctx, bc.ngcServiceKeyFetcher,
			bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nbMerged)).Get))
	}

	// Only check if the image pull secret is changed if the generateImagePullSecret is enabled
	if bc.generateImagePullSecret {
		nvcaRolloutChecks = append(nvcaRolloutChecks, hasImagePullSecretChangedCheck(ctx, bc.ngcServiceKeyFetcher,
			bc.clients.K8s.CoreV1().Secrets(getSystemNamespace(nbMerged)).Get,
			bc.getImageRegistryServerFromRepo(nbMerged)))
	}

	// Always check if additional image pull secrets have changed (handles additions, removals, and changes)
	nvcaRolloutChecks = append(nvcaRolloutChecks, hasAdditionalImagePullSecretsChanged(ctx, bc.additionalImagePullSecrets, nbMerged.Status))

	cfgCheck, err := bc.newAgentConfigChangedCheck(ctx, nbMerged)
	if err != nil {
		log.WithError(err).Error("Failed to create new agent config check")
		return err
	}
	nvcaRolloutChecks = append(nvcaRolloutChecks, cfgCheck)

	// finalizer added proceed with sync
	if shouldNVCARollout(ctx, nvcaRolloutChecks...) {
		if nbMerged.Status.Version == "" {
			bc.eventRecorder.Eventf(nbMerged, corev1.EventTypeNormal,
				string(nvcaoptypes.EventCategoryInstall), "Starting %v %v installation", AgentName, nbMerged.Spec.Version)
		} else if nbMerged.Spec.Version != nbMerged.Status.Version {
			bc.eventRecorder.Eventf(nbMerged, corev1.EventTypeNormal,
				string(nvcaoptypes.EventCategoryUpgrade), "Upgrading %v from %v to %v", AgentName, nbMerged.Status.Version, nbMerged.Spec.Version)
		} else if !reflect.DeepEqual(nbMerged.Spec.AccountConfig, nbMerged.Status.AccountConfig) {
			log.Infof("Updating %v account configuration from %+v to %+v", AgentName, nbMerged.Spec.AccountConfig, nbMerged.Status.AccountConfig)
			bc.eventRecorder.Eventf(nbMerged, corev1.EventTypeNormal,
				string(nvcaoptypes.EventCategoryUpgrade), "Updating %v account configuration", AgentName)
		} else if !reflect.DeepEqual(nbMerged.Spec.FeatureGate, nbMerged.Status.FeatureGate) {
			log.Infof("Updating %v feature configuration from %+v to %+v", AgentName, nbMerged.Spec.FeatureGate, nbMerged.Status.FeatureGate)
			bc.eventRecorder.Eventf(nbMerged, corev1.EventTypeNormal,
				string(nvcaoptypes.EventCategoryUpgrade), "Updating %v feature configuration", AgentName)
		} else {
			bc.eventRecorder.Eventf(nbMerged, corev1.EventTypeNormal,
				string(nvcaoptypes.EventCategoryUpgrade), "%s periodic sync", AgentName)
		}

		err = bc.setupNVCAAgentInfra(ctx, nbMerged)
		if err != nil {
			return fmt.Errorf("failed to setup %v for NVCFBackend %v/%v, err: %w", AgentName, nbMerged.Namespace, nbMerged.Name, err)
		}

		// Get effective config by merging ICMS and local configs
		effectiveAgentConfig, err := bc.getEffectiveAgentConfig(ctx, nbMerged)
		if err != nil {
			return err
		}

		// updated status on successful sync completion
		retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Retrieve the latest version of NVCFBackend before attempting update
			// RetryOnConflict uses exponential backoff to avoid exhausting the apiserver
			nbLat, err := bc.clients.NVCAOP.NvcfV1().NVCFBackends(bc.operatorNamespace).Get(ctx, nbMerged.Name, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("failed to get latest version of backend: %v", err)
			}

			// update the status with the currently configured config.
			nbLat.Status.Version = nbMerged.Spec.Version
			nbMerged.Spec.NVCFBackendSpecT.DeepCopyInto(&nbLat.Status.NVCFBackendSpecT)
			nbLat.Status.LastUpdated = &metav1.Time{Time: core.GetCurrentTime(ctx)}

			// Store current network config values for next rollout comparison
			nbLat.Status.DDCSIPAllowList = make([]string, len(bc.ddcsIPAllowList))
			copy(nbLat.Status.DDCSIPAllowList, bc.ddcsIPAllowList)
			nbLat.Status.K8sClusterNetworkCIDRs = make([]string, len(effectiveK8sNetworkCIDRs))
			copy(nbLat.Status.K8sClusterNetworkCIDRs, effectiveK8sNetworkCIDRs)

			// Store current env overrides for next rollout comparison
			nbLat.Status.FunctionEnvOverridesB64 = bc.functionEnvOverridesB64
			nbLat.Status.TaskEnvOverridesB64 = bc.taskEnvOverridesB64

			// Store the effective agent config (merged from ICMS and local) that was actually applied
			nbLat.Status.AgentConfig = effectiveAgentConfig

			_, updateErr := bc.clients.NVCAOP.NvcfV1().NVCFBackends(bc.operatorNamespace).UpdateStatus(ctx, nbLat, metav1.UpdateOptions{})
			return updateErr
		})
		if retryErr != nil {
			return fmt.Errorf("failed to update status for %v/%v, err: %v", nbMerged.Namespace, nbMerged.Name, retryErr)
		}

		if nbMerged.Status.Version == "" {
			bc.eventRecorder.Eventf(nbMerged, corev1.EventTypeNormal,
				string(nvcaoptypes.EventCategoryInstall), "%v %v installation completed", AgentName, nbMerged.Spec.Version)
		} else {
			bc.eventRecorder.Eventf(nbMerged, corev1.EventTypeNormal,
				string(nvcaoptypes.EventCategoryUpgrade), "%v %v updated", AgentName, nbMerged.Spec.Version)
		}
	} else {
		log.Debugf("Backend already updated, skipping sync")
	}

	return nil
}

func (bc *BackendK8sCache) validateNVCFBackend(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
	log := core.GetLogger(ctx)

	// add input verification if these fail we should not proceed with the sync
	nvcaValidationChecks := []func() error{
		func() error {
			_, err := bc.getCustomNetworkPoliciesData(ctx, bc.clients.K8s.CoreV1().ConfigMaps(nb.Namespace).Get)
			return err
		},
	}
	for _, check := range nvcaValidationChecks {
		if err := check(); err != nil {
			log.WithError(err).Errorf("failed to validate NVCFBackend %v/%v", nb.Namespace, nb.Name)
			return err
		}
	}
	return nil
}

func shouldNVCARollout(ctx context.Context, checks ...func() bool) bool {
	log := core.GetLogger(ctx)
	for i, check := range checks {
		checkResult := check()
		log.Debugf("shouldNVCARollout check %d returned %v", i, checkResult)
		if checkResult {
			return true
		}
	}
	return false
}

func objectsEqual(a, b any) bool { return cmp.Equal(a, b, cmpopts.EquateEmpty()) }

// mergeOverrides merges nb's spec and overrides fields into spec,
// so ensure nb is a copy of the source of truth.
func mergeOverrides(nb *nvidiaiov1.NVCFBackend) error {
	if nb.Spec.Overrides == nil {
		return nil
	}

	// Save the original maintenance mode feature gates before merge
	originalHasCordonMaintenance := false
	originalHasCordonAndDrainMaintenance := false
	for _, val := range nb.Spec.FeatureGate.Values {
		if val == cordonMaintenanceFeatureFlag {
			originalHasCordonMaintenance = true
		}
		if val == cordonAndDrainMaintenanceFeatureFlag {
			originalHasCordonAndDrainMaintenance = true
		}
	}

	tmp := &nvidiaiov1.NVCFBackend{Spec: nb.Spec}
	if err := mergo.Merge(
		tmp,
		&nvidiaiov1.NVCFBackend{
			Spec: nvidiaiov1.NVCFBackendSpec{NVCFBackendSpecT: *nb.Spec.Overrides},
		},
		mergo.WithOverride,
	); err != nil {
		return err
	}

	// Check override maintenance mode feature gates after merge
	overrideHasCordonMaintenance := false
	overrideHasCordonAndDrainMaintenance := false
	for _, val := range tmp.Spec.FeatureGate.Values {
		if val == cordonMaintenanceFeatureFlag {
			overrideHasCordonMaintenance = true
		}
		if val == cordonAndDrainMaintenanceFeatureFlag {
			overrideHasCordonAndDrainMaintenance = true
		}
	}

	// Apply additive merge logic ONLY for maintenance mode feature gates
	// If original had a maintenance mode but override doesn't, keep it (additive)
	needsCordonMaintenance := originalHasCordonMaintenance || overrideHasCordonMaintenance
	needsCordonAndDrainMaintenance := originalHasCordonAndDrainMaintenance || overrideHasCordonAndDrainMaintenance

	// Check for conflicting maintenance modes
	if needsCordonMaintenance && needsCordonAndDrainMaintenance {
		// Emit metric for conflicting maintenance modes
		if m := metrics.FromContext(context.Background()); m != nil {
			m.ConflictingMaintenanceModes.WithLabelValues(m.WithDefaultLabelValues()...).Inc()
		}

		// Prefer CordonMaintenance, remove CordonAndDrainMaintenance
		needsCordonAndDrainMaintenance = false
	}

	// Rebuild the feature gate values with the maintenance mode logic applied
	// Use a map for deduplication while preserving order from the original list
	mergedValues := make(map[string]bool)
	for _, val := range tmp.Spec.FeatureGate.Values {
		// Skip maintenance modes - we'll add them back based on our logic
		if val != cordonMaintenanceFeatureFlag && val != cordonAndDrainMaintenanceFeatureFlag {
			mergedValues[val] = true
		}
	}

	// Add back the maintenance modes based on additive merge + conflict resolution
	if needsCordonMaintenance {
		mergedValues[cordonMaintenanceFeatureFlag] = true
	}
	if needsCordonAndDrainMaintenance {
		mergedValues[cordonAndDrainMaintenanceFeatureFlag] = true
	}

	// Preserve order by iterating through the original list first
	var finalValues []string
	seen := make(map[string]bool)

	// First, add values from the merged list in their original order
	for _, val := range tmp.Spec.FeatureGate.Values {
		if mergedValues[val] && !seen[val] {
			finalValues = append(finalValues, val)
			seen[val] = true
		}
	}

	// Then add any remaining values (e.g., maintenance modes added via additive merge)
	// These are appended at the end to maintain consistency
	if needsCordonMaintenance && !seen[cordonMaintenanceFeatureFlag] {
		finalValues = append(finalValues, cordonMaintenanceFeatureFlag)
	}
	if needsCordonAndDrainMaintenance && !seen[cordonAndDrainMaintenanceFeatureFlag] {
		finalValues = append(finalValues, cordonAndDrainMaintenanceFeatureFlag)
	}

	tmp.Spec.FeatureGate.Values = finalValues
	nb.Spec.NVCFBackendSpecT = tmp.Spec.NVCFBackendSpecT

	return nil
}

func (bc *BackendK8sCache) cleanupResources(ctx context.Context, nb *nvidiaiov1.NVCFBackend) error {
	log := core.GetLogger(ctx)

	// Use shared cleanup (CRDs are cleaned up via owner references)
	if err := cleanup.CleanupBackendResources(
		ctx,
		bc.clients.K8s,
		bc.clients.DynamicClient,
		nb,
	); err != nil {
		return err
	}

	// Remove the operator finalizer so the API server can garbage-collect the
	// NVCFBackend CR. Without this, the reconciler sees the CR's deletionTimestamp
	// on every tick (the finalizer holds the resource alive), runs cleanup again,
	// logs "Successfully cleaned up resources", and loops forever — a `kubectl
	// delete nvcfbackend` outside the helm-uninstall flow never completes. The
	// graceful-shutdown branch (when /shutdown is driving teardown) is unaffected:
	// shutdown.go calls RemoveNVCFBackendFinalizer itself after cleanup. Removing
	// the finalizer here is safe because we only reach this code when the
	// graceful-shutdown signal is *not* set (gracefulShutdownActive == false at
	// the call site).
	if err := cleanup.RemoveNVCFBackendFinalizer(ctx, bc.clients.NVCAOP, nb); err != nil {
		return fmt.Errorf("remove finalizer from NVCFBackend %s/%s: %w", nb.Namespace, nb.Name, err)
	}
	log.Infof("Removed finalizer from NVCFBackend %s/%s; ready for GC", nb.Namespace, nb.Name)
	return nil
}
