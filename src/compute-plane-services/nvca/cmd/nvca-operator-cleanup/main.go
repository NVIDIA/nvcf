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

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/version"
	cli "github.com/urfave/cli/v2"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/cleanup"
)

func main() {
	ctx := core.NewDefaultContext(context.Background())
	log := core.GetLogger(ctx)

	if err := newCleanupApp().RunContext(ctx, os.Args); err != nil {
		log.Fatal(err)
	}
}

func newCleanupApp() *cli.App {
	return &cli.App{
		Name:    "nvca-operator-cleanup",
		Usage:   "Clean up NVCA resources before nvca-operator Helm uninstall",
		Version: version.String(),
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "namespace",
				Value: cleanup.NVCAOperatorName,
				Usage: "Namespace where the nvca-operator NVCFBackend and sentinel resources live",
			},
			&cli.StringFlag{
				Name:  "kubeconfig",
				Value: "",
				Usage: "Path to kubeconfig; empty uses in-cluster config",
			},
			&cli.StringFlag{
				Name:  "log-level",
				Value: "info",
				Usage: "Log level",
			},
			&cli.DurationFlag{
				Name:  "drain-timeout",
				Value: 6 * time.Minute,
				Usage: "Maximum time to wait for active workloads to drain",
			},
			&cli.DurationFlag{
				Name:  "rollout-timeout",
				Value: 2 * time.Minute,
				Usage: "Maximum time to wait for NVCA deployment rollout during drain",
			},
			&cli.StringFlag{
				Name:  "cluster-role-name",
				Value: cleanup.NVCAOperatorName,
				Usage: "Operator ClusterRole name to unblock after cleanup",
			},
			&cli.StringFlag{
				Name:  "cluster-role-binding-name",
				Value: cleanup.NVCAOperatorName,
				Usage: "Operator ClusterRoleBinding name to unblock after cleanup",
			},
			&cli.StringFlag{
				Name:  "service-account-name",
				Value: cleanup.NVCAOperatorName,
				Usage: "Operator ServiceAccount name to unblock after cleanup",
			},
		},
		Action: runCleanup,
	}
}

type cleanupDeps struct {
	kubernetesConfig   func(string) (*rest.Config, error)
	newK8sClient       func(*rest.Config) (kubernetes.Interface, error)
	newNVCAClient      func(*rest.Config) (versioned.Interface, error)
	newDynamicClient   func(*rest.Config) (dynamic.Interface, error)
	runShutdownCleanup func(context.Context, cleanup.ShutdownHandlerOptions) cleanup.ShutdownResponse
}

func defaultCleanupDeps() cleanupDeps {
	return cleanupDeps{
		kubernetesConfig: kubernetesConfig,
		newK8sClient: func(cfg *rest.Config) (kubernetes.Interface, error) {
			return kubernetes.NewForConfig(cfg)
		},
		newNVCAClient: func(cfg *rest.Config) (versioned.Interface, error) {
			return versioned.NewForConfig(cfg)
		},
		newDynamicClient: func(cfg *rest.Config) (dynamic.Interface, error) {
			return dynamic.NewForConfig(cfg)
		},
		runShutdownCleanup: cleanup.RunShutdownCleanup,
	}
}

func runCleanup(c *cli.Context) error {
	return runCleanupWithDeps(c, defaultCleanupDeps())
}

func runCleanupWithDeps(c *cli.Context, deps cleanupDeps) error {
	ctx := c.Context
	log := core.GetLogger(ctx)
	if err := core.SetLevel(log, c.String("log-level")); err != nil {
		return err
	}

	cfg, err := deps.kubernetesConfig(c.String("kubeconfig"))
	if err != nil {
		return err
	}

	k8sClient, err := deps.newK8sClient(cfg)
	if err != nil {
		return fmt.Errorf("create kubernetes client: %w", err)
	}
	nvcaClient, err := deps.newNVCAClient(cfg)
	if err != nil {
		return fmt.Errorf("create nvca client: %w", err)
	}
	dynamicClient, err := deps.newDynamicClient(cfg)
	if err != nil {
		return fmt.Errorf("create dynamic client: %w", err)
	}

	resp := deps.runShutdownCleanup(ctx, cleanup.ShutdownHandlerOptions{
		K8sClient:              k8sClient,
		NVCAClient:             nvcaClient,
		DynamicClient:          dynamicClient,
		Namespace:              c.String("namespace"),
		DrainTimeout:           c.Duration("drain-timeout"),
		RolloutTimeout:         c.Duration("rollout-timeout"),
		ClusterRoleName:        c.String("cluster-role-name"),
		ClusterRoleBindingName: c.String("cluster-role-binding-name"),
		ServiceAccountName:     c.String("service-account-name"),
	})
	if resp.Error != "" {
		return fmt.Errorf("%s: %s", resp.Message, resp.Error)
	}
	log.Infof("NVCA operator cleanup result: %s", resp.Message)
	return nil
}

func kubernetesConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	return rest.InClusterConfig()
}
