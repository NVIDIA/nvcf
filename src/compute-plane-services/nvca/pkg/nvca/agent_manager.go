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
	"fmt"
	"net/http"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	cmnhttp "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/http"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	nvcaauth "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/auth"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/gc"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	mscontroller "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/miniservice"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

var (
	mgrScheme = runtime.NewScheme()
)

func init() {
	utilruntime.Must(storage.SchemeBuilder.AddToScheme(mgrScheme))
	utilruntime.Must(mscontroller.SchemeBuilder.AddToScheme(mgrScheme))
}

// startControllerManagerForAgent creates and starts a controller-runtime manager
// for auxiliary CRD controllers.
// storageControllerTypes returns the StorageRequest controller types to
// register with the agent's controller manager. The model-cache controller is
// included only when caching is enabled, since it backs all storage-class-
// selected cache backends (NVMesh / shared-FS / Samba). Callers pass the stable
// agent-level caching flag (a.CachingSupportEnabled), not a live feature-flag
// lookup, so the set is deterministic for the lifetime of the agent.
func storageControllerTypes(cachingEnabled bool) []nvcav1new.StorageRequestType {
	sts := []nvcav1new.StorageRequestType{
		nvcav1new.SharedStorageRequest,
		nvcav1new.InternalPersistentStorageRequest,
	}
	if cachingEnabled {
		sts = append(sts, nvcav1new.ModelCacheRequest)
	}
	return sts
}

func startControllerManagerForAgent(
	ctx context.Context,
	a *Agent,
	tf nvcaauth.TokenFetcher,
	clients *kubeclients.KubeClients,
	metrics *nvcametrics.Metrics,
) error {
	log := core.GetLogger(ctx)

	mgr, err := ctrl.NewManager(clients.Config, manager.Options{
		Scheme:        mgrScheme,
		WebhookServer: fakeWebhookServer{},
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				&corev1.Event{}: {
					// Exclude Kyverno events from the cache entirely.
					// These PolicyViolation events are not actionable by users and can
					// be very numerous, contributing to memory pressure. This filter
					// is applied server-side so events never come over the wire.
					// See DGXCINC-3086.
					Field: fields.AndSelectors(
						fields.OneTermNotEqualSelector("reportingComponent", "kyverno-admission"),
						fields.OneTermNotEqualSelector("reportingComponent", "kyverno-scan"),
						fields.OneTermNotEqualSelector("reportingComponent", "kyverno-generate"),
					),
				},
			},
		},
	})
	if err != nil {
		log.WithError(err).Error("Failed to create controller manager")
		return fmt.Errorf("create controller manager: %v", err)
	}

	// Create storage controllers for each type. The model-cache controller
	// backs every storage-class-selected cache backend (NVMesh / shared-FS /
	// Samba), so it is registered whenever caching is enabled.
	cachingEnabled := a.CachingSupportEnabled
	sts := storageControllerTypes(cachingEnabled)
	log.WithField("controllers", sts).
		WithField("caching_support_enabled", cachingEnabled).
		WithField("caching_support_flag", a.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.CachingSupport)).
		Info("Registering storage controllers")
	for _, st := range sts {
		if err := storage.BuildController(a.Config, st, mgr,
			a.ClusterName,
			a.ClusterRegion,
			a.K8sTimeConfig,
			storage.ControllerOptions{
				ICMSRequestNamespace:  a.RequestsNamespace,
				CSIVolumeMountOptions: a.CSIVolumeMountOptions,
				Metrics:               metrics,
				BARTClient:            clients.BART,
			}); err != nil {
			log.WithError(err).Errorf("Failed to create storage controller %s", st)
			return fmt.Errorf("create storage controller %s: %v", st, err)
		}
		log.WithField("type", st).Info("Registered storage controller")
	}

	log.Info("Starting MiniService controller")

	kartas, err := mscontroller.GetKartaObjects(ctx, a.backendk8scache.clients)
	if err != nil {
		log.Error(err, "Failed to detect Karta resource")
		return fmt.Errorf("detect karta resource: %w", err)
	}

	hrHTTPClient := cmnhttp.NewRetryableClient(ctx,
		cmnhttp.WithAppVersionUserAgent(types.AppName),
		cmnhttp.WithRequestHeader(types.HeaderNVClusterID, a.ClusterID),
	)
	rvClient := mscontroller.NewReValClient(
		a.HelmReValServiceURL,
		tf,
		hrHTTPClient,
		metrics,
		mscontroller.WithReValHostHeaderOverride(a.HelmReValServiceHostHeaderOverride),
	)
	if err := mscontroller.BuildController(ctx, a.Config, mgr, rvClient,
		a.backendk8scache.nfClient,
		a.backendk8scache.regITCache,
		a.ClusterAttributes,
		mscontroller.ControllerOptions{
			SystemNamespace:            a.SystemNamespace,
			ICMSRequestNamespace:       a.RequestsNamespace,
			K8sVersion:                 a.K8sVersion,
			FeatureFlagFetcher:         a.backendk8scache.featureFlagFetcher,
			K8sTimeConfig:              a.K8sTimeConfig,
			InstanceNamespaceLabels:    a.NamespaceLabels,
			Metrics:                    metrics,
			ClusterName:                a.ClusterName,
			ClusterRegion:              a.ClusterRegion,
			ImageCredentialHelperImage: a.ImageCredentialHelperImage,
			CustomAnnotations:          a.backendk8scache.customAnnotations,
			Kartas:                     kartas,
			NsightProfilingAllowlist:   a.backendk8scache.nsightProfilingAllowlist,
		},
	); err != nil {
		log.WithError(err).Error("Failed to create miniservice controller")
		return fmt.Errorf("create miniservice controller: %v", err)
	}

	// Clean up finished system jobs every 5 minutes.
	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		crclient := mgr.GetClient()
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-t.C:
				deleteFinalizedSystemJobs(ctx, crclient, a.SystemNamespace)
			}
		}
	})); err != nil {
		return fmt.Errorf("add system job garbage collector: %w", err)
	}

	// Add GC cleaners to the manager
	gcRunnable := gc.NewRunnable(clients, metrics, gc.DefaultInterval, a.RequestsNamespace)
	if err := mgr.Add(gcRunnable); err != nil {
		log.WithError(err).Error("Failed to add GC controller to controller manager")
		return fmt.Errorf("add GC controller to controller manager: %v", err)
	}
	log.Info("Added GC controller to controller manager")

	// Don't need a select statement since the context will cancel
	// the blocking manager start call
	go func() {
		if err := mgr.Start(ctx); err != nil {
			nvcaerrors.ExitReason(ctx, err)
			log.WithError(err).Fatal("Failed to start controller manager")
			return
		}
		log.Info("agent controller manager shutdown successful")
	}()

	return nil
}

// Webhooks are handled external to the controller for now, but controller-runtime
// forces a webhook server with TLS. To avoid managing unused TLS certs,
// this fake webhook server implements the webhook.Server interface but do nothing.
type fakeWebhookServer struct{}

var _ webhook.Server = fakeWebhookServer{}

func (fakeWebhookServer) NeedLeaderElection() bool        { return false }
func (fakeWebhookServer) Register(string, http.Handler)   {}
func (fakeWebhookServer) Start(context.Context) error     { return nil }
func (fakeWebhookServer) StartedChecker() healthz.Checker { return healthz.Ping }
func (fakeWebhookServer) WebhookMux() *http.ServeMux      { return http.NewServeMux() }

func deleteFinalizedSystemJobs(ctx context.Context, crclient client.Client, namespace string) {
	log := logf.FromContext(ctx)

	log.V(1).Info("Cleaning up system jobs")

	jobList := &batchv1.JobList{}
	if err := crclient.List(ctx, jobList, client.InNamespace(namespace)); err != nil {
		log.Error(err, "Failed to list system jobs")
		return
	}
	for _, job := range jobList.Items {
		if job.Annotations == nil || job.Annotations[k8sutil.ImageCredUpdaterInitJobCompletedAnnotationKey] == "" {
			continue
		}
		log.V(1).Info("Cleaning up system job", "job", job.Name)
		propPolicy := client.PropagationPolicy(metav1.DeletePropagationBackground)
		if err := crclient.Delete(ctx, &job, propPolicy); err != nil {
			log.Error(err, "Failed to delete system job")
		}
	}
}
