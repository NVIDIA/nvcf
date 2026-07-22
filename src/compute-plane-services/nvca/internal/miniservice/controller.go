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

package mscontroller

import (
	"context"
	"fmt"
	"sync"
	"time"

	nvresourcev1beta1 "github.com/NVIDIA/k8s-dra-driver-gpu/api/nvidia.com/resource/v1beta1"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	kartav1alpha1 "github.com/run-ai/karta/pkg/api/runai/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	nodev1 "k8s.io/api/node/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	resourcev1 "k8s.io/api/resource/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/miniservice/chartcache"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/otel"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	nvcav1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/miniservice"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/profiling"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

var (
	SchemeBuilder = runtime.NewSchemeBuilder(
		nvcav2beta1.AddToScheme,
		nvcav1.AddToScheme,
		nvcav2beta1.AddToScheme,
		nvcav1alpha1.AddToScheme,
		corev1.AddToScheme,
		appsv1.AddToScheme,
		rbacv1.AddToScheme,
		netv1.AddToScheme,
		storagev1.AddToScheme,
		batchv1.AddToScheme,
		nodev1.AddToScheme,
		resourcev1.AddToScheme,
		nvresourcev1beta1.AddToScheme,
		authorizationv1.AddToScheme,
		// Hack to fix karta's scheme builder.
		getKartaAddToScheme(),
	)
)

func getKartaAddToScheme() func(*runtime.Scheme) error {
	sb := runtime.NewSchemeBuilder(
		func(s *runtime.Scheme) error {
			s.AddKnownTypes(kartav1alpha1.SchemeGroupVersion,
				&kartav1alpha1.Karta{},
				&kartav1alpha1.KartaList{},
			)
			metav1.AddToGroupVersion(s, kartav1alpha1.SchemeGroupVersion)
			return nil
		},
	)
	return sb.AddToScheme
}

type ControllerOptions struct {
	SystemNamespace         string
	ICMSRequestNamespace    string
	K8sVersion              string
	K8sTimeConfig           *k8sutil.TimeConfig
	FeatureFlagFetcher      featureflag.Fetcher
	InstanceNamespaceLabels labels.Set
	Metrics                 *metrics.Metrics
	ClusterName             string
	ClusterRegion           string
	// ImageCredentialHelperImage is the image tag for "nvcf-image-credential-helper",
	// for third party registry cred updates.
	ImageCredentialHelperImage string
	// For adding overhead to instance types. Optional.
	OverheadGetter enforce.InfraOverheadGetter
	// CustomAnnotations is the cached custom annotations from BackendK8sCache
	CustomAnnotations *sync.Map
	// Kartas are live Karta objects from the cluster, to include in status checkers.
	Kartas []*kartav1alpha1.Karta
	// NsightProfilingAllowlist tracks which functions should have NVIDIA Nsight GPU
	// profiling enabled. Shared with BackendK8sCache.
	NsightProfilingAllowlist *profiling.Allowlist

	// Internal use.
	cacheDir string
}

// Only Get is needed here.
type registrationInstanceTypeCache interface {
	Get(string) (types.RegistrationInstanceType, bool)
}

const (
	defaultCacheDir = "/var/run/nvca/reval-rendered-helmcharts"
)

func BuildController(ctx context.Context,
	cfg nvcaconfig.Config,
	mgr ctrl.Manager,
	rvClient ReValClient,
	nflient nodefeatures.Client,
	regITCache registrationInstanceTypeCache,
	enabledAttrs featureflag.Attributes,
	opts ControllerOptions,
) error {
	if opts.cacheDir == "" {
		opts.cacheDir = defaultCacheDir
	}

	gvkc := newGVKCache(mgr.GetScheme())
	gvkc.PrePopulate(&corev1.Pod{}, podGVK)
	gvkc.PrePopulate(&appsv1.Deployment{}, deploymentGVK)
	gvkc.PrePopulate(&appsv1.ReplicaSet{}, replicaSetGVK)
	gvkc.PrePopulate(&appsv1.StatefulSet{}, statefulSetGVK)
	gvkc.PrePopulate(&batchv1.Job{}, jobGVK)
	gvkc.PrePopulate(&batchv1.CronJob{}, cronJobGVK)
	gvkc.PrePopulate(&corev1.Secret{}, secretGVK)
	gvkc.PrePopulate(&corev1.Service{}, serviceGVK)
	gvkc.PrePopulate(&corev1.ConfigMap{}, configMapGVK)
	gvkc.PrePopulate(&corev1.PersistentVolumeClaim{}, pvcGVK)

	// Extra types are needed for clients/watches/decoders.
	var extraGVKs []schema.GroupVersionKind
	if cfg.Cluster.ValidationPolicy != nil {
		for _, ekt := range cfg.Cluster.ValidationPolicy.AllowedExtraKubernetesTypes {
			extraGVKs = append(extraGVKs, schema.GroupVersionKind{
				Group:   ekt.Group,
				Version: ekt.Version,
				Kind:    ekt.Kind,
			})
		}
	}

	r := &Reconciler{
		ControllerOptions: opts,
		cfg:               cfg,
		Client:            metrics.NewInstrumentedCRClient(mgr.GetClient()),
		RESTConfig:        mgr.GetConfig(),
		ReValClient:       rvClient,
		tracer:            otel.NewTracer(),
		Decoder:           newFlexibleDecoder(mgr.GetScheme(), extraGVKs...),
		NFClient:          nflient,
		eventRecorder:     mgr.GetEventRecorderFor("miniservice-controller"),
		chartCache:        chartcache.New(opts.cacheDir),
		regITCache:        regITCache,
		enabledAttrs:      enabledAttrs,
		gvkCache:          gvkc,
		now:               time.Now,
		newImpersonatingClient: func(namespace string) (client.Client, error) {
			username := miniservice.InstanceServiceAccountUsername(namespace)
			cfg := rest.CopyConfig(mgr.GetConfig())
			cfg.Impersonate = rest.ImpersonationConfig{UserName: username}
			c, err := client.New(cfg, client.Options{
				Scheme: mgr.GetScheme(),
			})
			if err == nil {
				c = metrics.NewInstrumentedCRClient(c)
			}
			return c, err
		},
		newPermissionsChecker:             newSelfSubjectAccessReviewPermissionsChecker,
		failedWorkloadUpdateRevisionCache: map[string]error{},
	}

	if r.regITCache == nil {
		return fmt.Errorf("registration instance type cache is a required option")
	}

	if r.FeatureFlagFetcher == nil {
		r.FeatureFlagFetcher = featureflag.DefaultFetcher
	}
	if r.InstanceNamespaceLabels == nil {
		r.InstanceNamespaceLabels = labels.Set{}
	}
	if r.OverheadGetter == nil {
		getRuntimeClass := func(ctx context.Context, name string) (*nodev1.RuntimeClass, error) {
			rtc := &nodev1.RuntimeClass{}
			err := r.Client.Get(ctx, client.ObjectKey{Name: name}, rtc)
			return rtc, err
		}
		r.OverheadGetter = enforce.NewInfraOverheadGetter(r.FeatureFlagFetcher, r.cfg, getRuntimeClass)
	}
	if r.statusCheckers == nil {
		r.statusCheckers = r.makeStatusCheckers()
	}

	// Add Karta status checkers from embedded and/or live Karta definitions.
	// These are used to check first class operator objects generically.
	kartas, err := loadKartaDefinitions(ctx, r.Kartas)
	if err != nil {
		return fmt.Errorf("load karta definitions: %w", err)
	}
	for _, karta := range kartas {
		checker, err := r.newKartaDefinedObjectStatusChecker(karta)
		if err != nil {
			return fmt.Errorf("new karta defined object status checker: %w", err)
		}
		r.statusCheckers[kartaGVKToSchemaGVK(*karta.Spec.StructureDefinition.RootComponent.Kind)] = checker
	}

	if err := mgr.Add(r.chartCache); err != nil {
		return fmt.Errorf("add local chart cache: %v", err)
	}

	if r.FeatureFlagFetcher.IsAttributeEnabled(featureflag.AttrNVLinkOptimized) {
		if err := mgr.Add(r.newNVLinkOptMetricsRunnable(mgr)); err != nil {
			return fmt.Errorf("add NVLink optimized metrics runnable: %v", err)
		}
	}

	if err := setStatusEventIndices(ctx, mgr.GetFieldIndexer()); err != nil {
		return fmt.Errorf("failed to set status event indices on manager: %w", err)
	}

	b := builder.
		ControllerManagedBy(mgr).
		Named("miniservice_controller").
		For(&nvcav1alpha1.MiniService{}).
		// Labels will be set on these objects and their children
		// in order to capture events by un-owned objects by the same type.
		Watches(&nvcav1.StorageRequest{}, miniserviceLabelEventHandler).
		Watches(&batchv1.Job{}, miniserviceLabelEventHandler).
		Watches(&batchv1.CronJob{}, miniserviceLabelEventHandler).
		Watches(&corev1.Namespace{}, miniserviceLabelEventHandler).
		Watches(&corev1.Pod{}, miniserviceLabelEventHandler).
		Watches(&corev1.Secret{}, miniserviceLabelEventHandler).
		Watches(&corev1.ConfigMap{}, miniserviceLabelEventHandler).
		Watches(&corev1.Service{}, miniserviceLabelEventHandler).
		Watches(&corev1.PersistentVolumeClaim{}, miniserviceLabelEventHandler).
		Watches(&appsv1.Deployment{}, miniserviceLabelEventHandler).
		Watches(&appsv1.StatefulSet{}, miniserviceLabelEventHandler).
		Watches(&appsv1.ReplicaSet{}, miniserviceLabelEventHandler)

	// Watch extra validation policy types so events are registered.
	for _, gvk := range extraGVKs {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		b.Watches(u, miniserviceLabelEventHandler)
	}

	if err := b.Complete(r); err != nil {
		return fmt.Errorf("create controller: %v", err)
	}

	return nil
}

func getMiniServiceNameFromLabel(obj client.Object) string {
	labels := obj.GetLabels()
	if labels == nil {
		return ""
	}
	return labels[miniserviceNameLabel]
}

var miniserviceLabelEventHandler = handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
	msName := getMiniServiceNameFromLabel(obj)
	if msName == "" {
		return nil
	}
	req := reconcile.Request{}
	req.Name = msName
	return []reconcile.Request{req}
})

const (
	eventInvObjPrefix = "involvedObject."

	eventInvObjNameFieldPath       = eventInvObjPrefix + "name"
	eventInvObjKindFieldPath       = eventInvObjPrefix + "kind"
	eventInvObjAPIVersionFieldPath = eventInvObjPrefix + "apiVersion"
	eventTypeFieldPath             = "type"
)

var (
	eventExtractValues = func(f func(*corev1.Event) string) func(o client.Object) (vs []string) {
		return func(o client.Object) (vs []string) {
			if s := f(o.(*corev1.Event)); s != "" {
				vs = append(vs, s)
			}
			return vs
		}
	}
	eventIndexSet = map[string]client.IndexerFunc{
		eventInvObjNameFieldPath:       eventExtractValues(func(e *corev1.Event) string { return e.InvolvedObject.Name }),
		eventInvObjKindFieldPath:       eventExtractValues(func(e *corev1.Event) string { return e.InvolvedObject.Kind }),
		eventInvObjAPIVersionFieldPath: eventExtractValues(func(e *corev1.Event) string { return e.InvolvedObject.APIVersion }),
		eventTypeFieldPath:             eventExtractValues(func(e *corev1.Event) string { return e.Type }),
	}
)

func setStatusEventIndices(ctx context.Context, fi client.FieldIndexer) error {
	for fieldPath, extractValues := range eventIndexSet {
		if err := fi.IndexField(ctx, &corev1.Event{}, fieldPath, extractValues); err != nil {
			return err
		}
	}
	return nil
}

// newNVLinkOptMetricsRunnable returns a manager.Runnable that starts an informer to update metrics
// for DRA objects created/succeeded/failed in NVLink-opimized workloads.
func (r *Reconciler) newNVLinkOptMetricsRunnable(mgr ctrl.Manager) manager.Runnable {
	return manager.RunnableFunc(func(ctx context.Context) error {
		log := logf.FromContext(ctx)

		log.Info("Starting NVLink-optimized metrics runnable")

		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		inf, err := mgr.GetCache().GetInformer(cctx, &nvresourcev1beta1.ComputeDomain{}, cache.BlockUntilSynced(true))
		if err != nil {
			defer cancel()
			return fmt.Errorf("get ComputeDomain informer: %w", err)
		}
		cancel()

		updates := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedItemBasedRateLimiter[struct{}]())
		go func() {
			<-ctx.Done()
			updates.ShutDown()
		}()

		eh, err := inf.AddEventHandler(toolscache.FilteringResourceEventHandler{
			FilterFunc: func(obj any) bool {
				return types.IsInfraOwnedObject(obj.(*nvresourcev1beta1.ComputeDomain))
			},
			Handler: toolscache.ResourceEventHandlerDetailedFuncs{
				AddFunc: func(_ any, isInInitialList bool) {
					// Metrics are updated once for all existing DRA objects on startup
					// instead of rewriting metrics on each initial list element, for performance.
					if isInInitialList {
						return
					}
					updates.AddRateLimited(struct{}{})
				},
				UpdateFunc: func(_, _ any) {
					updates.AddRateLimited(struct{}{})
				},
				DeleteFunc: func(_ any) {
					updates.AddRateLimited(struct{}{})
				},
			},
		})
		if err != nil {
			return fmt.Errorf("add NVLink-optimized cache event handler: %w", err)
		}

		cctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		if !toolscache.WaitForCacheSync(cctx.Done(), eh.HasSynced) {
			defer cancel()
			return fmt.Errorf("wait for NVLink-optimized cache event handler to sync: %w", cctx.Err())
		}
		cancel()

		// Run once at startup.
		if err := r.updateNVLinkOptMetrics(ctx); err != nil {
			log.Error(err, "Failed to update NVLink-optimized metrics on startup")
		}
		for {
			item, shutdown := updates.Get()
			if shutdown {
				return nil
			}
			if err := r.updateNVLinkOptMetrics(ctx); err != nil {
				log.Error(err, "Failed to update NVLink-optimized metrics")
			}
			updates.Forget(item)
			updates.Done(item)
		}
	})
}

// updateNVLinkOptMetrics is a helper for the NVLink opt metrics runnable that actually updates metrics.
func (r *Reconciler) updateNVLinkOptMetrics(ctx context.Context) error {
	log := logf.FromContext(ctx)

	cdList := &nvresourcev1beta1.ComputeDomainList{}
	if err := r.Client.List(ctx, cdList); err != nil {
		return fmt.Errorf("list compute domains: %w", err)
	}
	var createdCount, readyCount, failedCount int64
	for _, cd := range cdList.Items {
		if !types.IsInfraOwnedObject(&cd) {
			continue
		}
		createdCount++
		switch cd.Status.Status {
		case nvresourcev1beta1.ComputeDomainStatusReady:
			readyCount++
		case nvresourcev1beta1.ComputeDomainStatusNotReady:
			now := r.now()
			if !k8sutil.IsOverTimeout(cd.CreationTimestamp, r.K8sTimeConfig.PodScheduledThreshold, now) {
				continue
			}
			// Only do lookup if base threshold has not been passed.
			msName := cd.Labels[miniserviceNameLabel]
			if msName == "" {
				log.V(1).Info("ComputeDomain's MiniService name label is empty", "computedomain", cd.Name, "namespace", cd.Namespace)
				continue
			}
			ms := &nvcav1alpha1.MiniService{}
			if err := r.Client.Get(ctx, client.ObjectKey{Name: msName}, ms); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				log.V(1).Error(err, "Failed to get MiniService for ComputeDomain", "miniservice", msName, "computedomain", cd.Name, "namespace", cd.Namespace)
				continue
			}
			icmsReq := &nvcav2beta1.ICMSRequest{}
			if err := r.Client.Get(ctx, client.ObjectKey{Name: ms.Spec.ICMSRequestName, Namespace: r.ICMSRequestNamespace}, icmsReq); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				log.V(1).Error(err, "Failed to get ICMSRequest for MiniService", "miniservice", msName, "computedomain", cd.Name, "namespace", cd.Namespace)
				continue
			}
			realSchedThreshold := getPodSchedulingTimeout(ctx, icmsReq, r.K8sTimeConfig)
			if k8sutil.IsOverTimeout(cd.CreationTimestamp, realSchedThreshold, now) {
				failedCount++
			}
		}
	}
	r.Metrics.NVLinkAllocationCreatedCount.WithLabelValues(r.Metrics.WithDefaultLabelValues()...).Set(float64(createdCount))
	r.Metrics.NVLinkAllocationSuccessCount.WithLabelValues(r.Metrics.WithDefaultLabelValues()...).Set(float64(readyCount))
	r.Metrics.NVLinkAllocationFailureCount.WithLabelValues(r.Metrics.WithDefaultLabelValues()...).Set(float64(failedCount))
	return nil
}
