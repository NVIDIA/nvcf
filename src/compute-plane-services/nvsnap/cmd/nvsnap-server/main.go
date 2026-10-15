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

// NVSNAP Server - GPU Checkpoint/Restore API Server
//
// Serves the NVSNAP REST API and React UI. Discovers GPU nodes and pods
// via K8s API and proxies checkpoint/restore to nvsnap-agent on each node.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/db"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/server"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/tracing"
	nvsnapui "github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/ui"
)

var version = "0.4.0"

func main() {
	rootCmd := &cobra.Command{
		Use:   "nvsnap-server",
		Short: "NVSNAP API Server - GPU Checkpoint/Restore",
		Long:  "Serves the NVSNAP REST API and UI. Proxies checkpoint/restore to node agents.",
		RunE:  run,
	}

	rootCmd.Flags().String("address", ":8080", "Server listen address")
	rootCmd.Flags().String("kubeconfig", "", "Path to kubeconfig (auto-detects in-cluster or ~/.kube/config)")
	rootCmd.Flags().Int("agent-port", 8081, "Agent HTTP port on nodes")
	rootCmd.Flags().String("blobstore-url", "", "NvSnap-blobstore base URL (default: http://nvsnap-blobstore.nvsnap-system.svc.cluster.local:9000)")
	rootCmd.Flags().String("log-level", "info", "Log level (debug, info, warn, error)")
	rootCmd.Flags().String("db-path", "./nvsnap.db", "Path to SQLite database file")

	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run:   func(cmd *cobra.Command, args []string) { logrus.Infof("NVSNAP Server v%s", version) },
	})

	if err := rootCmd.Execute(); err != nil {
		logrus.Fatal(err)
	}
}

func run(cmd *cobra.Command, args []string) error {
	level, _ := logrus.ParseLevel(cmd.Flag("log-level").Value.String())
	logrus.SetLevel(level)
	logrus.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})

	kubeconfig, _ := cmd.Flags().GetString("kubeconfig")
	k8sCfg, err := buildK8sConfig(kubeconfig)
	if err != nil {
		return err
	}

	kubeClient, err := kubernetes.NewForConfig(k8sCfg)
	if err != nil {
		return err
	}
	dynClient, err := dynamic.NewForConfig(k8sCfg)
	if err != nil {
		return err
	}

	agentPort, _ := cmd.Flags().GetInt("agent-port")
	address, _ := cmd.Flags().GetString("address")
	dbPath, _ := cmd.Flags().GetString("db-path")
	blobstoreURL, _ := cmd.Flags().GetString("blobstore-url")

	catalog, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = catalog.Close() }()
	logrus.WithField("path", dbPath).Info("Opened checkpoint catalog database")

	srv := server.New(server.Config{
		Address:      address,
		AgentPort:    agentPort,
		BlobstoreURL: blobstoreURL,
	}, kubeClient, dynClient, catalog)

	// Embed React UI — serves at / with SPA fallback
	srv.SetUI(nvsnapui.DistFS)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// OTel tracing — no-op unless OTEL_EXPORTER_OTLP_ENDPOINT is set
	// (the Helm chart wires it to the in-cluster Jaeger collector).
	tracingShutdown, terr := tracing.Init(ctx, "nvsnap-server")
	if terr != nil {
		logrus.WithError(terr).Warn("tracing init failed; continuing without traces")
	}
	defer func() {
		shutdownCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		if err := tracingShutdown(shutdownCtx); err != nil {
			logrus.WithError(err).Warn("tracing shutdown error")
		}
	}()

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		logrus.Info("Received shutdown signal")
		cancel()
	}()

	logrus.WithFields(logrus.Fields{
		"version": version,
		"address": address,
	}).Info("Starting NVSNAP Server")

	return srv.Run(ctx)
}

func buildK8sConfig(kubeconfig string) (*rest.Config, error) {
	// Explicit kubeconfig takes priority
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}

	// Try in-cluster (ServiceAccount)
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}

	// Fall back to default kubeconfig ($KUBECONFIG or ~/.kube/config)
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, nil).ClientConfig()
}
