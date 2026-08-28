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

package webhook

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/version"
	"github.com/bombsimon/logrusr/v4"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/cmdutil"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures/sharedcluster"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce/kata"
	whmetrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/webhook/metrics"
)

const (
	resync = 24 * time.Hour
)

func NewCommand() *cobra.Command {
	var configFile string
	cmd := &cobra.Command{
		Use:     "webhook-server",
		Short:   "NVIDIA Cluster Agent webhook server",
		Version: version.ReleaseString(),
		RunE: func(cmd *cobra.Command, _ []string) (err error) {
			ctx := cmd.Context()

			if configFile == "" {
				return fmt.Errorf("config file is required")
			}

			cfg, err := nvcaconfig.Init(configFile)
			if err != nil {
				return err
			}

			cfg = setDefaults(cfg.Complete())

			// The system namespace defaults to the pod's namespace so the TLS
			// secret informer watches the namespace the webhook runs in.
			if cfg.Agent.SystemNamespace == "" {
				cfg.Agent.SystemNamespace = os.Getenv("POD_NAMESPACE")
			}

			log := core.GetLogger(ctx)
			if log.Logger.Level, err = logrus.ParseLevel(cfg.Agent.LogLevel); err != nil {
				return err
			}

			// Move logs from all client-go logs into
			// the default logrus logger
			k8sLogger := logrusr.New(log, logrusr.WithReportCaller())
			ctrllog.SetLogger(k8sLogger)
			klog.SetLogger(k8sLogger)
			ctx = ctrllog.IntoContext(ctx, k8sLogger)

			// Feature flag shim
			if err := (&featureflag.CLIFlag{}).Set(strings.Join(cfg.Agent.FeatureFlags, ",")); err != nil {
				return fmt.Errorf("set featureflag CLI flag for config: %v", err)
			}
			// Cluster attributes shim
			if err := (&featureflag.AttrCLIFlag{}).Set(strings.Join(cfg.Cluster.Attributes, ",")); err != nil {
				return fmt.Errorf("set attribute CLI flag for config: %v", err)
			}

			// Inject metrics into context.
			ctx = nvcametrics.WithDefaultMetrics(ctx,
				cfg.Cluster.NCAID, cfg.Cluster.Name, cfg.Cluster.GroupName, version.ReleaseString(),
			)
			ctx = whmetrics.WithDefaultMetrics(ctx)

			// check if map is nil
			if cfg.Webhook.DCGMAnnotations == nil {
				cfg.Webhook.DCGMAnnotations = make(map[string]string)
			}
			dcgmMetricsCfg, err := DCGMMetricsConfigFromAnnotations(cfg.Webhook.DCGMAnnotations)
			if err != nil {
				return err
			}

			k8sClient, err := newK8sClient(ctx, cfg.Agent.KubeconfigPath)
			if err != nil {
				log.WithError(err).Error("Failed to create k8s client")
				return err
			}

			if err := k8sutil.SetConfigDefaultResources(&cfg); err != nil {
				return err
			}

			cw, err := newCertWatcher(ctx, cfg, k8sClient)
			if err != nil {
				return err
			}

			m := &webhookManager{
				cfg:              cfg,
				k8sClient:        k8sClient,
				dcgmMetrics:      dcgmMetricsCfg,
				readTimeout:      5 * time.Second,
				writeTimeout:     10 * time.Second,
				attrFetcher:      featureflag.DefaultFetcher,
				addNodePublisher: sharedcluster.AddNodePublisher,
				cw:               cw,
			}

			// Start shared cluster only once since the pod affinity webhook is a subscriber
			// to the returned atomic boolean.
			if err := m.startSharedClusterPubSub(ctx, resync); err != nil {
				return err
			}

			// Detect non-GPU Kata RuntimeClass existence.
			m.startKataRuntimeClassHandler(ctx)

			if err := m.run(ctx); err != nil {
				log.WithError(err).Error("failed to run webhook manager")
				return err
			}
			return nil
		},
	}

	cmd.PersistentFlags().StringVar(&configFile, "config", "", "Config file path")

	return cmd
}

func setDefaults(cfg nvcaconfig.Config) nvcaconfig.Config {
	cmdutil.SetEmptyValue(&cfg.Webhook.SvcAddress, "127.0.0.1:8443")
	return cfg
}

type webhookManager struct {
	cfg nvcaconfig.Config

	readTimeout  time.Duration
	writeTimeout time.Duration
	dcgmMetrics  DCGMMetricsConfig

	attrFetcher featureflag.AttributeFetcher

	k8sClient kubernetes.Interface

	// sharedClusterOn is true when at least one node in the cluster has the "schedule" label,
	// and false in all other cases.
	//
	// NB(estroczynski): the edge case where the only node with the "schedule" label
	// transiently leaves the cluster, rendering all nodes available for scheduling,
	// has been acknowledged as tolerable.
	sharedClusterOn *atomic.Bool
	// kataNonGPURTClassExists is true when a RuntimeClass with name == kata.RuntimeClassNameNonGPU
	// is present in the cluster.
	kataNonGPURTClassExists *atomic.Bool
	// Mocked in tests.
	addNodePublisher func(ctx context.Context, inf cache.SharedIndexInformer) (*atomic.Bool, cache.InformerSynced, error)

	// Watch TLS certs for updates and update stored certificate when it changes.
	cw certWatcher
}

func (m *webhookManager) run(ctx context.Context) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	shutdownCompleted := make(chan struct{})
	if err := m.startWebhooks(ctx, cancel, shutdownCompleted); err != nil {
		return err
	}
	<-ctx.Done()
	<-shutdownCompleted
	// Surface a certificate-watcher failure (for example the TLS secret is missing
	// at startup) so the process exits non-zero and the container is restarted until
	// it recovers, instead of serving admission requests with no certificate.
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}
	return nil
}

type certWatcher interface {
	// Start starts the watch on the certificate and key data. It must block.
	Start(ctx context.Context) error
	// GetCertificate fetches the currently loaded certificate, which may be nil.
	GetCertificate(chi *tls.ClientHelloInfo) (*tls.Certificate, error)
}

func newCertWatcher(ctx context.Context, cfg nvcaconfig.Config, k8sClient kubernetes.Interface) (cw certWatcher, err error) {
	log := core.GetLogger(ctx)
	// If a TLS secret is configured, use the secret cert watcher's GetCertificate method
	// to get the TLS certificate without restarting the server.
	// Otherwise certificate files are watched by certwatcher and reloaded via GetCertificate.
	if cfg.Webhook.TLSSecretName != "" {
		if cfg.Agent.SystemNamespace == "" {
			return nil, fmt.Errorf("agent system namespace is required to watch TLS secret %s", cfg.Webhook.TLSSecretName)
		}
		log.WithField("secretName", cfg.Webhook.TLSSecretName).Info("Configuring Secret certificate watcher")
		cw = newSecretCertWatcher(cfg, k8sClient)
	} else if cfg.Webhook.TLSCertFile != "" || cfg.Webhook.TLSKeyFile != "" {
		log.WithFields(logrus.Fields{
			"certFile": cfg.Webhook.TLSCertFile,
			"keyFile":  cfg.Webhook.TLSKeyFile,
		}).Info("Configuring file certificate watcher")
		if cw, err = certwatcher.New(cfg.Webhook.TLSCertFile, cfg.Webhook.TLSKeyFile); err != nil {
			return nil, err
		}
	}
	return cw, nil
}

func (m *webhookManager) startWebhooks(ctx context.Context, cancel context.CancelCauseFunc, shutdownSignal chan struct{}) error {
	log := core.GetLogger(ctx)

	r := mux.NewRouter()

	nvcametrics.AddMetricsRoute(r, log, nil, "")

	// Use a max request size of 7MB like controller-runtime does
	// since full object(s) are embedded in webhook req/res.
	// https://github.com/kubernetes-sigs/controller-runtime/blob/961fc2c/pkg/webhook/admission/http.go#L55
	const maxRequestSize = int64(7 * 1024 * 1024)
	httpOpts := []core.HTTPMiddlewareOption{
		core.WithRequestBodyLimit(maxRequestSize),
	}
	r.Use(core.NewHTTPMiddleware(ctx, httpOpts...)...)

	if featureflag.AttrHostIsolation.Enabled() && featureflag.AttrAccountIsolation.Enabled() {
		log.Error("account and workload isolation are mutually exclusive")
		return fmt.Errorf("account and workload isolation are mutually exclusive")
	}

	valWH, err := NewHelmMiniServiceValidatingWebhook(ctx,
		"validate-helm-charts.nvca.nvcf.nvidia.io",
		featureflag.DefaultFetcher)
	if err != nil {
		log.WithError(err).Error("Error creating validating webhook")
		return err
	}
	handleWebhook(ctx, r, "/validate", valWH)

	genNodeAffValWH, err := newStandaloneWebhook(ctx,
		"validate-instance-type-nodeaffinity.nvca.nvcf.nvidia.io",
		newInstanceTypeNodeAffinityValWebhookHandler())
	if err != nil {
		log.WithError(err).Error("Error creating instance type node affinity validating webhook")
		return err
	}
	handleWebhook(ctx, r, "/validate-instance-type-nodeaffinity", genNodeAffValWH)

	podAffinityMuWH, err := NewPodAffinityMutatingWebhook(ctx,
		"mutate-pod-nodeaffinity.nvca.nvcf.nvidia.io",
		PodAffinityOptions{
			SharedClusterOn:       m.sharedClusterOn,
			UniformInstanceLabels: featureflag.UniformInstanceLabels.Enabled(),
			HostIsolation:         featureflag.AttrHostIsolation.Enabled(),
			AccountIsolation:      featureflag.AttrAccountIsolation.Enabled(),
		})
	if err != nil {
		log.WithError(err).Error("Error creating pod node affinity mutating webhook")
		return err
	}
	handleWebhook(ctx, r, "/mutate-pod-nodeaffinity", podAffinityMuWH)

	enfMuWH, err := NewPodEnforcementMutatingWebhook(ctx,
		"mutate-pod-enforcement.nvca.nvcf.nvidia.io",
		EnforcementOptions{
			AttributeFetcher:        m.attrFetcher,
			DCGMMetrics:             m.dcgmMetrics,
			KataNonGPURTClassExists: m.kataNonGPURTClassExists,
		})
	if err != nil {
		log.WithError(err).Error("Error creating pod enforcement mutating webhook")
		return err
	}
	handleWebhook(ctx, r, "/mutate-pod-enforcement", enfMuWH)

	// Note: the helm storage mutating webhook is now just a stub for backwards-compatibility.
	// The MiniService mutating webhook now handles all storage mutations.
	// This must be removed in a future release.
	helmStorageMuWebhook, err := newStandaloneWebhook(ctx, "mutate-helm-storage.nvca.nvcf.nvidia.io", newHelmStorageMutatingWebhook())
	if err != nil {
		log.WithError(err).Error("Error creating Helm storage mutating webhook")
		return err
	}
	handleWebhook(ctx, r, "/mutate-helm-storage", helmStorageMuWebhook)

	helmPersistentStorageMuWebhook, err := newStandaloneWebhook(ctx,
		"mutate-helm-storage.nvca.nvcf.nvidia.io",
		newHelmPersistentStorageWebhook(
			featureflag.HelmInternalPersistentStorage.Spec.StorageClassName,
			featureflag.HelmInternalPersistentStorage.Spec.Enabled))
	if err != nil {
		log.WithError(err).Error("Error creating Helm persistent storage mutating webhook")
		return err
	}
	handleWebhook(ctx, r, "/mutate-helm-persistent-storage", helmPersistentStorageMuWebhook)

	nvcaMutatingWebhook, err := newStandaloneWebhook(ctx,
		"nvca-mutating-webhook.nvca.nvcf.nvidia.io",
		newNVCAMutatingWebhook(featureflag.DefaultFetcher, v1.ResourceList(m.cfg.Agent.UtilsResources)))
	if err != nil {
		log.WithError(err).Error("Error creating NVCA mutating webhook")
		return err
	}
	handleWebhook(ctx, r, "/nvca-mutating-webhook", nvcaMutatingWebhook)

	miniserviceMuWH, err := NewMiniserviceMutatingWebhook(ctx,
		"mutate-miniservice",
		m.k8sClient)
	if err != nil {
		log.WithError(err).Error("Error creating miniservice mutating webhook")
		return err
	}
	handleWebhook(ctx, r, "/mutate-miniservice", miniserviceMuWH)

	server := &http.Server{
		Handler:      r,
		ReadTimeout:  m.readTimeout,
		WriteTimeout: m.writeTimeout,
		IdleTimeout:  120 * time.Second,
	}

	var listener net.Listener
	if m.cw != nil {
		go func() {
			if err := m.cw.Start(ctx); err != nil {
				core.GetLogger(ctx).WithError(err).Error("certificate watcher error")
				cancel(fmt.Errorf("certificate watcher failed: %w", err))
			}
		}()
		server.TLSConfig = &tls.Config{
			NextProtos:     []string{"h2"},
			MinVersion:     tls.VersionTLS12,
			GetCertificate: m.cw.GetCertificate,
		}
		listener, err = tls.Listen("tcp", m.cfg.Webhook.SvcAddress, server.TLSConfig)
	} else {
		listener, err = net.Listen("tcp", m.cfg.Webhook.SvcAddress)
	}
	if err != nil {
		return err
	}

	go func() {
		log.Infof("Serving webhooks at: %v", listener.Addr())
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error(err)
			cancel(fmt.Errorf("webhook server failed: %w", err))
		}
	}()

	go func(ctx context.Context) {
		<-ctx.Done()

		newCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()

		log.Infof("Shutting down HTTPService at %v", listener.Addr())
		err = server.Shutdown(newCtx)
		shutdownSignal <- struct{}{}
		if err != nil && !errors.Is(err, context.Canceled) {
			log.WithError(err).Errorf("Failed to shut down HTTPService at %s", listener.Addr())
			return
		}
	}(ctx)

	return nil
}

func handleWebhook(ctx context.Context, r *mux.Router, path string, wh http.Handler) {
	wh = whmetrics.FromContext(ctx).InstrumentedHook(path, wh)
	r.Path(path).Handler(wh).Methods("POST")
}

type secretCertWatcher struct {
	cfg          nvcaconfig.Config
	k8sClient    kubernetes.Interface
	resyncPeriod time.Duration

	certMu            sync.RWMutex
	currentCert       *tls.Certificate
	cachedKeyPEMBlock []byte
}

func newSecretCertWatcher(cfg nvcaconfig.Config, k8sClient kubernetes.Interface) *secretCertWatcher {
	return &secretCertWatcher{
		cfg:          cfg,
		k8sClient:    k8sClient,
		resyncPeriod: resync,
	}
}

// GetCertificate fetches the currently loaded certificate, which may be nil.
func (w *secretCertWatcher) GetCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	w.certMu.RLock()
	defer w.certMu.RUnlock()
	return w.currentCert, nil
}

func (w *secretCertWatcher) Start(ctx context.Context) error {
	log := core.GetLogger(ctx)

	f := informers.NewSharedInformerFactoryWithOptions(
		w.k8sClient,
		w.resyncPeriod,
		informers.WithNamespace(w.cfg.Agent.SystemNamespace),
		informers.WithTweakListOptions(func(lo *metav1.ListOptions) {
			lo.FieldSelector = fields.OneTermEqualSelector(metav1.ObjectNameField, w.cfg.Webhook.TLSSecretName).String()
		}),
	)

	whmetrics := whmetrics.FromContext(ctx)

	handleSecret := func(sec *v1.Secret) error {
		for _, filePath := range []string{w.cfg.Webhook.TLSCertFile, w.cfg.Webhook.TLSKeyFile} {
			fileName := filepath.Base(filePath)

			fileData, ok := sec.Data[fileName]
			if !ok {
				whmetrics.ReadCertificateErrors.Inc()
				return fmt.Errorf("key %s not found", fileName)
			}
			if len(fileData) == 0 {
				whmetrics.ReadCertificateErrors.Inc()
				return fmt.Errorf("key %s has empty data", fileName)
			}
		}

		whmetrics.ReadCertificateTotal.Inc()
		certPEMBlock := sec.Data[filepath.Base(w.cfg.Webhook.TLSCertFile)]
		keyPEMBlock := sec.Data[filepath.Base(w.cfg.Webhook.TLSKeyFile)]

		cert, err := tls.X509KeyPair(certPEMBlock, keyPEMBlock)
		if err != nil {
			whmetrics.ReadCertificateErrors.Inc()
			return err
		}

		w.certMu.Lock()
		if w.currentCert != nil &&
			bytes.Equal(w.currentCert.Certificate[0], cert.Certificate[0]) &&
			bytes.Equal(w.cachedKeyPEMBlock, keyPEMBlock) {
			log.Debug("TLS certificate already cached")
			w.certMu.Unlock()
			return nil
		}
		w.currentCert = &cert
		w.cachedKeyPEMBlock = keyPEMBlock
		log.Info("Updated current TLS certificate")
		w.certMu.Unlock()

		return nil
	}

	inf := f.Core().V1().Secrets().Informer()
	_, err := inf.AddEventHandler(&cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			sec, ok := obj.(*v1.Secret)
			if !ok {
				log.Errorf("Wrong object type in Secret informer Add handler: %T", obj)
				return
			}

			log.Infof("Got new TLS Secret %s", sec.Name)

			if err := handleSecret(sec); err != nil {
				log.WithError(err).Error("Update TLS data")
			}
		},
		UpdateFunc: func(_, newObj any) {
			newSec, ok := newObj.(*v1.Secret)
			if !ok {
				log.Errorf("Wrong new object type in Secret informer Update handler: %T", newObj)
				return
			}

			log.Infof("Got TLS Secret %s update", newSec.Name)

			if err := handleSecret(newSec); err != nil {
				log.WithError(err).Error("Update TLS data")
			}
		},
	})
	if err != nil {
		log.WithError(err).Error("failed to add event handler for Secrets")
		return err
	}

	f.Start(ctx.Done())

	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if !cache.WaitForCacheSync(cctx.Done(), inf.HasSynced) {
		log.Error("Informer cache sync timed out")
		return fmt.Errorf("timeout while waiting for secret informer sync")
	}

	// After cache sync, the certificate must be available.
	// Returning an error will cause the webhook container to restart until the TLS secret is present.
	if currentCert, _ := w.GetCertificate(nil); currentCert == nil {
		return fmt.Errorf("TLS certificate not found")
	}

	log.Info("TLS Secret watcher initialized")

	<-ctx.Done()

	return nil
}

func (m *webhookManager) startSharedClusterPubSub(ctx context.Context,
	resyncPeriod time.Duration,
) error {
	log := core.GetLogger(ctx)

	f := informers.NewSharedInformerFactoryWithOptions(
		m.k8sClient,
		resyncPeriod,
		nodefeatures.NewNodeInformerOptions(featureflag.DefaultFetcher)...,
	)

	inf := f.Core().V1().Nodes().Informer()
	var err error
	if m.sharedClusterOn, _, err = m.addNodePublisher(ctx, inf); err != nil {
		return err
	}

	f.Start(ctx.Done())

	log.Infof("Started shared cluster informer")

	return nil
}

// TODO: remove this once non-GPU kata rt class is available in all clusters.
func (m *webhookManager) startKataRuntimeClassHandler(ctx context.Context) {
	log := core.GetLogger(ctx).WithField("runtimeclass", kata.RuntimeClassNameNonGPU)

	m.kataNonGPURTClassExists = &atomic.Bool{}

	checkRTClass := func() bool {
		log.Debug("Checking RuntimeClass existence")
		_, err := m.k8sClient.NodeV1().RuntimeClasses().Get(ctx, kata.RuntimeClassNameNonGPU, metav1.GetOptions{})

		// Track K8s API call metrics
		nvcametrics.FromContext(ctx).TrackK8sAPICall("runtimeclass", err)

		if err == nil {
			m.kataNonGPURTClassExists.Store(true)
			log.Info("Found Kata RuntimeClass, exiting handler")
			return true
		} else if !apierrors.IsNotFound(err) {
			log.WithError(err).Error("Error checking if Kata RuntimeClass exists")
		}
		return false
	}

	// Initial check since ticker does not run immediately.
	if checkRTClass() {
		return
	}

	// Since the runtime class should only be created once and not removed,
	// a simple poll loop with a long interval can be used.
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		for {
			select {
			case <-ctx.Done():
				log.Info("Shutting down Kata RuntimeClass handler")
				return
			case <-ticker.C:
				if checkRTClass() {
					ticker.Stop()
					return
				}
			}
		}
	}()

	log.Infof("Started Kata RuntimeClass handler")
}

var newK8sClient = func(ctx context.Context, path string) (kubernetes.Interface, error) {
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

	return coreClients.K8s, nil
}
