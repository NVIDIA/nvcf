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
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	internalutil "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/cmd/internal"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/clustervalidator"
)

const (
	defaultConfigMapName = "cluster-validator-network-checks"
	defaultNamespace     = "nvca-system"
	podNamespaceFile     = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

	// requestTimeout bounds every apiserver request, including the discovery
	// calls that take no context.
	requestTimeout = 30 * time.Second
	// runTimeoutEnv bounds the checks, as a Go duration or whole seconds. A
	// launcher sets it below its Job's activeDeadlineSeconds, so the run ends
	// with time left to publish its summary instead of being killed first.
	runTimeoutEnv = "VALIDATOR_TIMEOUT"
	// defaultRunTimeout leaves a minute of the chart Job's 600s
	// activeDeadlineSeconds for the summary write.
	defaultRunTimeout = 9 * time.Minute
)

func main() {
	ctx := core.NewDefaultContext(context.Background())
	log := core.GetLogger(ctx)
	log.Logger.SetFormatter(&clustervalidator.CLIFormatter{})

	_, restCfg, err := internalutil.NewK8sClient(ctx, "")
	if err != nil {
		log.WithError(err).Fatal("Failed to create Kubernetes client")
	}
	restCfg.Timeout = requestTimeout
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		log.WithError(err).Fatal("Failed to create Kubernetes client")
	}
	// Gateway API routes are CRDs, so listing them needs the dynamic client.
	routes, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		log.WithError(err).Fatal("Failed to create Kubernetes dynamic client")
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

	// VALIDATOR_ROLE selects which check set runs: "control-plane" runs the
	// gateway, StorageClass, overlay and HA checks and skips GPU/SMB; anything
	// else (including unset) runs the compute-plane check set (backward-compatible
	// default).
	roleEnv := os.Getenv("VALIDATOR_ROLE")
	role, roleKnown := parseRole(roleEnv)
	if roleEnv != "" && !roleKnown {
		log.Warnf("VALIDATOR_ROLE=%q is not recognized; defaulting to compute-plane", roleEnv)
	}

	timeout, ok := runTimeout(os.Getenv(runTimeoutEnv))
	if !ok {
		log.Warnf("%s=%q is not a positive duration; using %s", runTimeoutEnv, os.Getenv(runTimeoutEnv), timeout)
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	err = clustervalidator.Run(runCtx, client, routes, configNS, configName, summaryNS, emitMetrics, role)
	cancel()
	switch code := clustervalidator.ExitCode(err); {
	case code != 0:
		log.WithError(err).Error("Cluster validation failed")
		os.Exit(code)
	case err != nil:
		log.WithError(err).Warn("Cluster validation could not observe every critical check; " +
			"the operator starts, and the published verdict stays Not-Ready")
	}
}

// runTimeout parses runTimeoutEnv: a Go duration such as "5m", or whole
// seconds. Unset gives the default; an invalid value gives the default and
// false.
func runTimeout(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return defaultRunTimeout, true
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		secs, serr := strconv.Atoi(v)
		if serr != nil {
			return defaultRunTimeout, false
		}
		d = time.Duration(secs) * time.Second
	}
	if d <= 0 {
		return defaultRunTimeout, false
	}
	return d, true
}

// parseRole normalizes the VALIDATOR_ROLE env value. Returns the matching
// clustervalidator.Role constant and true for "control-plane" or
// "compute-plane"; returns the compute-plane default and false for any other
// value so unknown inputs are safe.
func parseRole(v string) (clustervalidator.Role, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case string(clustervalidator.RoleControlPlane):
		return clustervalidator.RoleControlPlane, true
	case string(clustervalidator.RoleComputePlane):
		return clustervalidator.RoleComputePlane, true
	default:
		return clustervalidator.RoleComputePlane, false
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
