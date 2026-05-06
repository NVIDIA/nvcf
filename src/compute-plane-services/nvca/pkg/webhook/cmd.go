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
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/bombsimon/logrusr/v4"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures/sharedcluster"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce/kata"
)

const (
	resync = 24 * time.Hour
)

func logLevelsToStrs() []string {
	ss := make([]string, len(logrus.AllLevels))
	for i, l := range logrus.AllLevels {
		ss[i] = l.String()
	}
	return ss
}

func NewCommand() *cli.Command {
	return &cli.Command{
		Name:  "webhook-server",
		Usage: "NV Cluster Agent webhook server",
		Flags: []cli.Flag{
			&cli.GenericFlag{
				Name:  "feature-flags",
				Usage: "Enable or disable features through flags",
				Value: &featureflag.CLIFlag{},
			},
			&cli.StringFlag{
				Name:  "listen",
				Value: "127.0.0.1:8443",
				Usage: "Address and port for the webhooks server",
			},
			&cli.StringFlag{
				Name:  "tls-key-file",
				Usage: "TLS server private key file",
			},
			&cli.StringFlag{
				Name:  "tls-cert-file",
				Usage: "TLS server cert file",
			},
			&cli.StringFlag{
				Name:  "tls-secret-name",
				Usage: "Name of the TLS server cert Secret (in this namespace) to watch for updates",
			},
			&cli.StringFlag{
				Name:    "namespace",
				EnvVars: []string{"POD_NAMESPACE"}, // TODO: configure this env in the operator through dw api
				Value:   "nvca-system",
				Usage:   "The current namespace",
			},
			&cli.StringFlag{
				Name:    "kubeconfig",
				EnvVars: []string{"KUBECONFIG"},
				Usage:   "The KUBECONFIG path for backend K8s cluster",
			},
			&cli.StringFlag{
				Name:  "log-level",
				Value: "debug",
				Usage: fmt.Sprintf("Log level, one of: %q", logLevelsToStrs()),
			},
			&cli.GenericFlag{
				Name:    "cluster-attributes",
				Usage:   "Cluster attributes of the form \"KEY=VALUE\"",
				EnvVars: []string{"CLUSTER_ATTRIBUTES"},
				Value:   featureflag.AttrCLIFlag{},
			},
			&cli.StringFlag{
				Name:  "dcgm-annotations",
				Usage: "DCGM Annotations to be applied to Pods requesting GPU Resources",
				Value: dcgmDefaultAnnotations,
			},
		},
		Action: func(c *cli.Context) error {
			ctx := c.Context
			log := core.GetLogger(ctx)
			err := core.SetLevel(log, c.String("log-level"))
			if err != nil {
				log.WithError(err).Error("failed to set log level")
				return err
			}

			dcgmAnnotations, err := k8sutil.ParseAnnotations(c.String("dcgm-annotations"))
			if err != nil {
				return err
			}
			dcgmMetricsCfg, err := DCGMMetricsConfigFromAnnotations(dcgmAnnotations)
			if err != nil {
				return err
			}

			// Move logs from all client-go logs into
			// the default logrus logger
			k8sLogger := logrusr.New(log, logrusr.WithReportCaller())
			ctrllog.SetLogger(k8sLogger)
			klog.SetLogger(k8sLogger)
			ctx = ctrllog.IntoContext(ctx, k8sLogger)

			k8sClient, err := newK8sClient(ctx, c.String("kubeconfig"))
			if err != nil {
				log.WithError(err).Error("failed to create k8s client")
				return err
			}

			cfg := nvcaconfig.Config{
				Webhook: nvcaconfig.WebhookConfig{
					SvcAddress:    c.String("listen"),
					TLSCertFile:   c.String("tls-cert-file"),
					TLSKeyFile:    c.String("tls-key-file"),
					TLSSecretName: c.String("tls-secret-name"),
				},
			}

			if err := k8sutil.SetConfigDefaultResources(&cfg); err != nil {
				return err
			}

			m := &webhookManager{
				cfg:              cfg,
				namespace:        c.String("namespace"),
				k8sClient:        k8sClient,
				dcgmMetrics:      dcgmMetricsCfg,
				readTimeout:      5 * time.Second,
				writeTimeout:     10 * time.Second,
				attrFetcher:      featureflag.DefaultFetcher,
				metrics:          metrics.FromContext(ctx),
				addNodePublisher: sharedcluster.AddNodePublisher,
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
}

type webhookManager struct {
	cfg nvcaconfig.Config

	readTimeout  time.Duration
	writeTimeout time.Duration
	dcgmMetrics  DCGMMetricsConfig

	attrFetcher featureflag.AttributeFetcher
	metrics     *metrics.Metrics

	k8sClient kubernetes.Interface
	namespace string

	// certMu ensures only one goroutine updates a TLS cert file at a given time.
	certMu sync.Mutex

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
}

func (m *webhookManager) run(ctx context.Context) error {
	if m.cfg.Webhook.TLSSecretName != "" {
		return m.runWithReload(ctx)
	}
	shutdownCompleted := make(chan struct{})
	if err := m.startWebhooks(ctx, shutdownCompleted); err != nil {
		return err
	}
	<-ctx.Done()
	<-shutdownCompleted
	return nil
}

// runWithReload runs the webhook server, restarting it when TLS files are updated.
func (m *webhookManager) runWithReload(parentCtx context.Context) error {
	// reloadSignal will ony receive values if a secret is configured.
	// If not, the webhook server is only shut down when parentCtx is canceled/times out.
	reloadSignal := make(chan struct{})
	if err := m.startSecretInformer(parentCtx, resync, reloadSignal); err != nil {
		return err
	}

	// Consume the first reload signal from initial informer list.
	<-reloadSignal

	shutdownCompleted := make(chan struct{})
	ctx, cancel := context.WithCancel(parentCtx)

	if err := m.startWebhooks(ctx, shutdownCompleted); err != nil {
		cancel()
		return err
	}

	for {
		select {
		case <-parentCtx.Done():
			cancel()
			<-shutdownCompleted
			return nil
		case <-reloadSignal:
			cancel()
			<-shutdownCompleted
			// The port may not be released until a few seconds after shutdown completes.
			if err := wait.PollUntilContextTimeout(parentCtx,
				100*time.Millisecond, 10*time.Second, true,
				func(ctx context.Context) (bool, error) {
					return isPortFree(ctx, m.cfg.Webhook.SvcAddress), nil
				}); err != nil {
				return err
			}
			// Start the server with the new cert/key.
			ctx, cancel = context.WithCancel(parentCtx)
			if err := m.startWebhooks(ctx, shutdownCompleted); err != nil {
				cancel()
				return err
			}
		}
	}
}

func isPortFree(ctx context.Context, addr string) bool {
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", addr)
	if err == nil {
		_ = conn.Close()
		return false
	}
	return true
}

func (m *webhookManager) startWebhooks(ctx context.Context, shutdownSignal chan struct{}) error {
	log := core.GetLogger(ctx)

	m.certMu.Lock()
	defer m.certMu.Unlock()

	r := mux.NewRouter()

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
	handleWebhook(r, "/validate", valWH)

	genNodeAffValWH, err := newStandaloneWebhook(ctx,
		"validate-instance-type-nodeaffinity.nvca.nvcf.nvidia.io",
		newInstanceTypeNodeAffinityValWebhookHandler())
	if err != nil {
		log.WithError(err).Error("Error creating instance type node affinity validating webhook")
		return err
	}
	handleWebhook(r, "/validate-instance-type-nodeaffinity", genNodeAffValWH)

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
	handleWebhook(r, "/mutate-pod-nodeaffinity", podAffinityMuWH)

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
	handleWebhook(r, "/mutate-pod-enforcement", enfMuWH)

	// Note: the helm storage mutating webhook is now just a stub for backwards-compatibility.
	// The MiniService mutating webhook now handles all storage mutations.
	// This must be removed in a future release.
	helmStorageMuWebhook, err := newStandaloneWebhook(ctx, "mutate-helm-storage.nvca.nvcf.nvidia.io", newHelmStorageMutatingWebhook())
	if err != nil {
		log.WithError(err).Error("Error creating Helm storage mutating webhook")
		return err
	}
	handleWebhook(r, "/mutate-helm-storage", helmStorageMuWebhook)

	helmPersistentStorageMuWebhook, err := newStandaloneWebhook(ctx,
		"mutate-helm-storage.nvca.nvcf.nvidia.io",
		newHelmPersistentStorageWebhook(
			featureflag.HelmInternalPersistentStorage.Spec.StorageClassName,
			featureflag.HelmInternalPersistentStorage.Spec.Enabled))
	if err != nil {
		log.WithError(err).Error("Error creating Helm persistent storage mutating webhook")
		return err
	}
	handleWebhook(r, "/mutate-helm-persistent-storage", helmPersistentStorageMuWebhook)

	nvcaMutatingWebhook, err := newStandaloneWebhook(ctx,
		"nvca-mutating-webhook.nvca.nvcf.nvidia.io",
		newNVCAMutatingWebhook(featureflag.DefaultFetcher, v1.ResourceList(m.cfg.Agent.UtilsResources)))
	if err != nil {
		log.WithError(err).Error("Error creating NVCA mutating webhook")
		return err
	}
	handleWebhook(r, "/nvca-mutating-webhook", nvcaMutatingWebhook)

	miniserviceMuWH, err := NewMiniserviceMutatingWebhook(ctx,
		"mutate-miniservice",
		m.k8sClient)
	if err != nil {
		log.WithError(err).Error("Error creating miniservice mutating webhook")
		return err
	}
	handleWebhook(r, "/mutate-miniservice", miniserviceMuWH)

	server := &http.Server{
		Handler:      r,
		ReadTimeout:  m.readTimeout,
		WriteTimeout: m.writeTimeout,
		IdleTimeout:  120 * time.Second,
	}

	listener, err := net.Listen("tcp", m.cfg.Webhook.SvcAddress)
	if err != nil {
		return err
	}

	go func() {
		logErr := func(err error) {
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error(err)
			}
		}
		if m.cfg.Webhook.TLSCertFile != "" || m.cfg.Webhook.TLSKeyFile != "" {
			log.Infof("Serving HTTPS at: %v", listener.Addr())
			logErr(server.ServeTLS(listener, m.cfg.Webhook.TLSCertFile, m.cfg.Webhook.TLSKeyFile))
		} else {
			log.Infof("Serving HTTP at: %v", listener.Addr())
			logErr(server.Serve(listener))
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

func handleWebhook(r *mux.Router, path string, wh http.Handler) {
	r.Path(path).Handler(wh).Methods("POST")
}

func (m *webhookManager) startSecretInformer(ctx context.Context,
	resyncPeriod time.Duration,
	reloadSignal chan struct{},
) error {
	log := core.GetLogger(ctx)

	f := informers.NewSharedInformerFactoryWithOptions(
		m.k8sClient,
		resyncPeriod,
		informers.WithNamespace(m.namespace),
		informers.WithTweakListOptions(func(lo *metav1.ListOptions) {
			lo.FieldSelector = fields.OneTermEqualSelector(metav1.ObjectNameField, m.cfg.Webhook.TLSSecretName).String()
		}),
	)

	handleSecret := func(sec *v1.Secret) error {
		m.certMu.Lock()
		defer m.certMu.Unlock()

		for _, filePath := range []string{m.cfg.Webhook.TLSCertFile, m.cfg.Webhook.TLSKeyFile} {
			fileName := filepath.Base(filePath)

			log.WithField("file", fileName).Info("Updating TLS file")

			fileData, ok := sec.Data[fileName]
			if !ok {
				return fmt.Errorf("key %s not found", fileName)
			}
			if len(fileData) == 0 {
				return fmt.Errorf("key %s has empty data", fileName)
			}

			tmpFile, err := os.CreateTemp(filepath.Dir(filePath), "*."+fileName)
			if err != nil {
				return err
			}
			defer tmpFile.Close()

			if _, err := io.Copy(tmpFile, bytes.NewReader(fileData)); err != nil {
				return err
			}

			if err := os.Rename(tmpFile.Name(), filePath); err != nil {
				return err
			}
		}

		reloadSignal <- struct{}{}

		return nil
	}

	inf := f.Core().V1().Secrets().Informer()
	_, err := inf.AddEventHandler(&cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			sec, ok := obj.(*v1.Secret)
			if !ok {
				log.Errorf("Wrong object in Secret informer Add handler: %v", obj)
				return
			}

			log.Infof("Got new TLS Secret %s", sec.Name)

			if err := handleSecret(sec); err != nil {
				log.WithError(err).Error("Update TLS files")
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldSec, ok := oldObj.(*v1.Secret)
			if !ok {
				log.Errorf("Wrong old object in Secret informer Update handler: %v", oldObj)
				return
			}
			newSec, ok := newObj.(*v1.Secret)
			if !ok {
				log.Errorf("Wrong new object in Secret informer Update handler: %v", newObj)
				return
			}

			// Ignore non-data-related changes to the Secret.
			if cmp.Equal(oldSec.Data, newSec.Data, cmpopts.EquateEmpty()) {
				return
			}

			log.Infof("Got TLS Secret %s update", newSec.Name)

			if err := handleSecret(newSec); err != nil {
				log.WithError(err).Error("Update TLS files")
			}
		},
	})
	if err != nil {
		log.WithError(err).Error("failed to add event handler for Secrets")
		return err
	}

	log.Infof("Starting TLS Secret %s informer", m.cfg.Webhook.TLSSecretName)

	f.Start(ctx.Done())

	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if !cache.WaitForCacheSync(cctx.Done(), inf.HasSynced) {
		log.Error("Informer cache sync timed out")
		return fmt.Errorf("timeout while waiting for secret informer sync")
	}

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
		if metrics := m.metrics; metrics != nil {
			metrics.TrackK8sAPICall("runtimeclass", err)
		}

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
