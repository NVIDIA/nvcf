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

package main

import (
	"context"
	"os"
	"strings"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"

	internalutil "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/cmd/internal"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/clustervalidator"
)

const (
	defaultConfigMapName = "cluster-validator-network-checks"
	defaultNamespace     = "nvca-system"
	podNamespaceFile     = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
)

func main() {
	ctx := core.NewDefaultContext(context.Background())
	log := core.GetLogger(ctx)
	log.Logger.SetFormatter(&clustervalidator.CLIFormatter{})

	client, _, err := internalutil.NewK8sClient(ctx, "")
	if err != nil {
		log.WithError(err).Fatal("Failed to create Kubernetes client")
	}

	configNS := os.Getenv("VALIDATOR_CONFIG_NAMESPACE")
	if configNS == "" {
		configNS = podNamespace()
	}

	configName := os.Getenv("VALIDATOR_CONFIG_NAME")
	if configName == "" {
		configName = defaultConfigMapName
	}

	// Emit metrics by default; a preflight run (VALIDATOR_PREFLIGHT) skips the
	// summary write — no agent to read it, no RBAC to write it.
	emitMetrics := !preflightMode(os.Getenv("VALIDATOR_PREFLIGHT"))

	// Write the summary where the agent watches (VALIDATOR_SUMMARY_NAMESPACE),
	// resolved independently of the config namespace; defaults to this pod's
	// namespace.
	summaryNS := os.Getenv(clustervalidator.SummaryConfigMapNamespaceEnv)
	if summaryNS == "" {
		summaryNS = podNamespace()
	}
	if emitMetrics && summaryNS == "" {
		log.Warnf("metrics enabled but summary namespace is empty (%s unset and the "+
			"pod namespace file is unreadable); the summary ConfigMap write will be "+
			"skipped and cluster-validator metrics will not be populated",
			clustervalidator.SummaryConfigMapNamespaceEnv)
	}

	if err := clustervalidator.Run(ctx, client, configNS, configName, summaryNS, emitMetrics); err != nil {
		log.WithError(err).Fatal("Cluster validation failed")
	}
}

// preflightMode reports whether this is a one-shot preflight run (e.g. nvcf-cli,
// before NVCA is installed), which skips the summary write. Read from an env
// (not a flag) so an unknown value is ignored rather than crashing arg parsing.
// Defaults to false — in-cluster runs emit metrics.
func preflightMode(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes":
		return true
	default:
		return false
	}
}

func podNamespace() string {
	if data, err := os.ReadFile(podNamespaceFile); err == nil {
		if ns := string(data); ns != "" {
			return ns
		}
	}
	return defaultNamespace
}
