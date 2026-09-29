/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package nvca

import (
	"context"

	"github.com/sirupsen/logrus"
	"k8s.io/client-go/dynamic"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/nvsnap"
	nvsnapcontroller "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/nvsnap/controller"
	nvsnapreconciler "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/nvsnap/reconciler"
)

// startNvSnapController boots Hook B (the checkpoint-after-warm
// reconciler + Pod informer + workqueue). Returns nil immediately
// when featureflag.NvSnapCheckpointRestore is disabled, so the call
// from agent.Start is a safe no-op in the default config.
//
// Runs in its own goroutine; lifecycle bound to ctx. Failure inside
// the controller (e.g., cache sync) logs but doesn't kill the agent
// — NvSnap is an optimization, never a gate on pod creation.
//
// Wiring:
//   - dynamic.Interface for NvSnapFunctionState reads/writes.
//   - nvsnap HTTP client (in-cluster Service URL by default).
//   - Pod informer scoped to a.RequestsNamespace (where tenant pods
//     land). Empty namespace = cluster-wide; production NVCA uses
//     nvcf-backend.
func (a *Agent) startNvSnapController(ctx context.Context, k8sclients *kubeclients.KubeClients, log *logrus.Entry) error {
	if !featureflag.NvSnapCheckpointRestore.Enabled() {
		log.Info("nvsnap: NvSnapCheckpointRestore feature flag is off; controller not started")
		return nil
	}

	dynClient, err := dynamic.NewForConfig(k8sclients.Config)
	if err != nil {
		log.WithError(err).Warn("nvsnap: dynamic client build failed; controller not started")
		return nil // fail-open
	}

	// nvsnap.NewClient with no options points at the in-cluster
	// Service URL (nvsnap-server.nvsnap-system.svc.cluster.local:8080).
	// Per-cluster ServerURL override will land in PR-2's
	// ClusterConfig.NvSnap wiring once that propagates to the agent
	// runtime config (PR-8 territory).
	nvsnapClient := nvsnap.NewClient()

	r := &nvsnapreconciler.Reconciler{
		KubeClient:   k8sclients.K8s,
		DynClient:    dynClient,
		NvSnapClient: nvsnapClient,
		Log:          log.WithField("subcomponent", "nvsnap-reconciler"),
	}
	ctrl := nvsnapcontroller.NewController(k8sclients.K8s, r)
	ctrl.Namespace = a.RequestsNamespace // empty falls back to cluster-wide
	ctrl.Log = log.WithField("subcomponent", "nvsnap-controller")

	go func() {
		log.WithField("namespace", ctrl.Namespace).Info("nvsnap controller starting")
		if err := ctrl.Run(ctx); err != nil && err != context.Canceled {
			log.WithError(err).Warn("nvsnap controller exited with error")
		}
	}()
	return nil
}
