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

package nvcaenvtest

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

func NewEnvironment() (*envtest.Environment, error) {
	binAssetsDir := os.Getenv("KUBEBUILDER_ASSETS")
	if binAssetsDir == "" {
		return nil, fmt.Errorf("KUBEBUILDER_ASSETS not set")
	}
	_, callerPath, _, ok := runtime.Caller(0)
	if !ok {
		return nil, fmt.Errorf("code bug: bad Caller")
	}
	crdsDir := filepath.Join(filepath.Dir(callerPath), "crds")
	env := &envtest.Environment{
		BinaryAssetsDirectory:   binAssetsDir,
		CRDDirectoryPaths:       []string{crdsDir},
		ErrorIfCRDPathMissing:   true,
		ControlPlaneStopTimeout: 1 * time.Second,
	}

	return env, nil
}

func SetupEnvtest() (*rest.Config, *kubernetes.Clientset, func(), error) {
	logf.SetLogger(zap.New(zap.UseDevMode(true)))
	log := logf.Log
	var err error

	testenv, err := NewEnvironment()
	if err != nil {
		log.Error(err, "Failed to create new testenv environment")
		return nil, nil, nil, err
	}

	cfg, err := testenv.Start()
	if err != nil {
		log.Error(err, "Failed to start testenv")
		return nil, nil, nil, err
	}

	cleanup := func() {
		// Tear down envtest.
		// Loop fixes bug: https://github.com/kubernetes-sigs/controller-runtime/issues/1571#issuecomment-1437304502
		if err = func() (err error) {
			sleepTime := 1 * time.Millisecond
			for range 12 { // Exponentially sleep up to ~4s
				if err = testenv.Stop(); err == nil {
					return nil
				}
				sleepTime *= 2
				time.Sleep(sleepTime)
			}
			return err
		}(); err != nil {
			log.Error(err, "Failed to stop envtest, manual cleanup needed")
		}
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Error(err, "Failed to create new clientset")
		cleanup()
		return nil, nil, nil, err
	}

	return cfg, clientset, cleanup, nil
}

func StartManager(ctx context.Context, mgr manager.Manager) (mgrErrCh chan error, err error) {
	mgrErrCh = make(chan error)
	go func() {
		mgrErrCh <- mgr.Start(ctx)
	}()

	cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case <-mgr.Elected():
	case <-cctx.Done():
		err = fmt.Errorf("manager startup timed out")
	case err = <-mgrErrCh:
	}
	return mgrErrCh, err
}

// addrMutex prevents multiple tests from racing on lis.Close() and net.Listen().
var addrMutex sync.Mutex

func NewRandomAddress() (string, error) {
	addrMutex.Lock()
	defer addrMutex.Unlock()

	lis, err := net.Listen("tcp4", "127.0.0.1:")
	if err != nil {
		return "", err
	}
	addr := lis.Addr().String()
	if err := lis.Close(); err != nil {
		return "", err
	}
	return addr, nil
}

// Webhooks are handled external to the controller for now, but controller-runtime
// forces a webhook server with TLS. To avoid managing unused TLS certs,
// this fake webhook server implements the webhook.Server interface but do nothing.
func NewFakeWebhookServer() webhook.Server { return fakeWebhookServer{} }

type fakeWebhookServer struct{}

var _ webhook.Server = fakeWebhookServer{}

func (fakeWebhookServer) NeedLeaderElection() bool        { return false }
func (fakeWebhookServer) Register(string, http.Handler)   {}
func (fakeWebhookServer) Start(context.Context) error     { return nil }
func (fakeWebhookServer) StartedChecker() healthz.Checker { return healthz.Ping }
func (fakeWebhookServer) WebhookMux() *http.ServeMux      { return http.NewServeMux() }

// NewFakeMetricsOptions returns metrics server options configured to bind to a random port.
// This prevents port conflicts when running tests in parallel.
func NewFakeMetricsOptions() metricsserver.Options {
	return metricsserver.Options{
		BindAddress: "0",
	}
}
