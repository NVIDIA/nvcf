/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

// Command pylon-operator reconciles InferenceEndpoint objects: it probes the
// backend Service, resolves the GPU type, runs a Pylon transport Deployment
// per endpoint and reports status.
package main

import (
	"context"
	"flag"
	"os"

	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	ctrl "sigs.k8s.io/controller-runtime"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/controller"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/gpu"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/prober"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/registration"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/transport"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/internal/version"
)

// eventSource is the component name on Events the operator emits.
const eventSource = "pylon-operator"

func main() {
	var cfg config.Config
	cfg.BindFlags(flag.CommandLine)
	zapOpts := zap.Options{}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	setupLog := ctrl.Log.WithName("setup")

	if err := cfg.Validate(); err != nil {
		setupLog.Error(err, "Invalid configuration")
		os.Exit(2)
	}
	setupLog.Info("Starting pylon-operator", append(version.KeysAndValues(),
		"clusterId", cfg.ClusterID,
		"operatorNamespace", cfg.OperatorNamespace,
		"watchNamespaces", cfg.WatchNamespaces,
		"pylonImagePullSecrets", cfg.PylonImagePullSecrets,
		"probeInterval", cfg.ProbeInterval.String(),
		"scrapeInterval", cfg.ScrapeInterval.String(),
		"leaderElect", cfg.LeaderElect)...)

	if err := run(ctrl.SetupSignalHandler(), cfg); err != nil {
		setupLog.Error(err, "pylon-operator stopped with an error")
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config.Config) error {
	scheme, err := controller.NewScheme()
	if err != nil {
		return err
	}
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), cfg.ManagerOptions(scheme))
	if err != nil {
		return err
	}

	m := metrics.New()
	if err := m.Register(ctrlmetrics.Registry); err != nil {
		return err
	}

	reconciler := controller.NewReconciler(controller.Options{
		Client:   mgr.GetClient(),
		Recorder: mgr.GetEventRecorderFor(eventSource),
		Config:   cfg,
		Metrics:  m,
		Steps: controller.DefaultSteps(
			mgr.GetClient(),
			prober.New(m),
			&gpu.Resolver{Reader: mgr.GetClient()},
			transport.New(mgr.GetClient(), cfg),
			registration.New(mgr.GetClient(), m),
		),
	})
	if err := reconciler.SetupWithManager(ctx, mgr); err != nil {
		return err
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}
	return mgr.Start(ctx)
}
