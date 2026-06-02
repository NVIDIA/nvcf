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

package clustervalidator

import (
	"context"
	"fmt"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
)

// ValidationState captures the results of every validation check.
type ValidationState struct {
	Log                 *logrus.Entry
	ControlPlaneHealthy bool
	// NodesAllReady tracks whether all worker nodes are Ready. False means at
	// least one NotReady node. Warning only — does not flip cluster readiness.
	NodesAllReady bool
	// NotReadyNodes is the count of NotReady nodes; populated when
	// NodesAllReady is false. Used by printSummary to surface the count.
	NotReadyNodes            int
	WebhooksSupported        bool
	NetworkPoliciesSupported bool
	SMBCSIDriverOK           bool
	GPUAvailable             bool
	GPUOperatorInstalled     bool
	K8sVersion               string
	TotalNodes               string
	Recommendations          []string
	Warnings                 []string

	// ReachabilityOK is nil when no reachability config was loaded,
	// non-nil when the check ran.
	ReachabilityOK *bool
	// ReachabilityCriticalOK tracks whether all endpoints marked
	// critical: true passed. Nil when no critical endpoints exist.
	ReachabilityCriticalOK *bool
	// ConfigurableNetPolOK is nil when no network-policy config was loaded,
	// non-nil when the check ran.
	ConfigurableNetPolOK *bool
	// ConfigurableNetPolCriticalOK tracks whether all pairs marked
	// critical: true passed. Nil when no critical pairs exist.
	ConfigurableNetPolCriticalOK *bool
	// EnforcementOK is nil when enforcement testing was not configured,
	// non-nil when the active enforcement check ran.
	EnforcementOK *bool
	// EnforcementCritical is true when the enforcement config has
	// critical: true, meaning enforcement failure blocks readiness.
	EnforcementCritical bool
}

// Run executes all cluster validation checks and prints a summary.
// It returns a non-nil error if the cluster is not ready, which the caller
// should use to set the process exit code.
//
// configNamespace and configName identify an optional ConfigMap that holds
// user-defined reachability and network-policy checks. When the ConfigMap
// does not exist the configurable checks are silently skipped.
func Run(ctx context.Context, client kubernetes.Interface, configNamespace, configName string) error {
	log := core.GetLogger(ctx)
	log.Info("Starting NVCF cluster validation")
	log.Info("")
	log.Infof("%s╔═══════════════════════════════════════════════════════════╗%s", colorBlue, colorReset)
	log.Infof("%s║     NVIDIA Cloud BYOC Cluster Readiness Check             ║%s", colorBlue, colorReset)
	log.Infof("%s║         Kubernetes Cluster Validation                     ║%s", colorBlue, colorReset)
	log.Infof("%s╚═══════════════════════════════════════════════════════════╝%s", colorBlue, colorReset)

	state := &ValidationState{
		Log:                 log,
		ControlPlaneHealthy: true,
		NodesAllReady:       true,
	}

	if err := checkPrerequisites(ctx, client, state); err != nil {
		return err
	}

	// Reclaim orphan netpol-validation-* namespaces left behind by previous
	// runs whose pod was SIGKILLed / OOMed / force-deleted (the deferred
	// cleanup in checkNetworkPolicyEnforcement only fires on normal control
	// flow). Runs unconditionally so orphans get reclaimed even if enforcement
	// is currently disabled.
	sweepOrphanTestNamespaces(ctx, log, client, orphanNamespaceTTL)

	checkControlPlaneHealth(ctx, client, state)
	checkWebhookSupport(ctx, client, state)
	checkNetworkPolicies(ctx, client, state)
	checkSMBCSIDriver(ctx, client, state)

	var netCfg *NetworkCheckConfig
	if configNamespace != "" && configName != "" {
		cfg, err := LoadNetworkCheckConfig(ctx, client, configNamespace, configName)
		if err != nil {
			log.WithError(err).Warn("Failed to load network check ConfigMap; skipping configurable checks")
		} else {
			netCfg = cfg
		}
	}

	if netCfg != nil && netCfg.Reachability != nil && len(netCfg.Reachability.Endpoints) > 0 {
		checkConfigurableReachability(state, netCfg.Reachability)
	}

	checkGPUResources(ctx, client, state)
	checkGPUOperator(ctx, client, state)

	if netCfg != nil {
		if netCfg.NetworkPolicies != nil && len(netCfg.NetworkPolicies.Pairs) > 0 {
			checkConfigurableNetworkPolicies(ctx, client, state, netCfg.NetworkPolicies)
		}
		if netCfg.Enforcement != nil && netCfg.Enforcement.Enabled {
			checkNetworkPolicyEnforcement(ctx, client, state, netCfg.Enforcement)
		}
	}

	return printSummary(state)
}

// printSummary outputs the final validation results and returns an error if
// the cluster is not ready.
func printSummary(state *ValidationState) error {
	log := state.Log
	printHeader(log, "Validation Summary")

	isReady := true

	log.Info("Check Results:")

	type check struct {
		Passed   bool
		PassMsg  string
		FailMsg  string
		Critical bool
	}

	// Distinguish "we listed nodes and found N not-ready" (NotReadyNodes>0)
	// from "we couldn't list nodes at all" (NotReadyNodes==0 + !NodesAllReady).
	// Successful listing always yields either NodesAllReady=true (pass) or
	// NotReadyNodes>0 (genuine NotReady count); the zero case can only
	// happen when checkControlPlaneHealth's Nodes().List() returned an
	// error, so avoid the misleading "0 NotReady" summary row.
	nodesFailMsg := fmt.Sprintf("Worker Nodes: %d NotReady (non-blocking)", state.NotReadyNodes)
	if !state.NodesAllReady && state.NotReadyNodes == 0 {
		nodesFailMsg = "Worker Nodes: status unknown (node listing failed)"
	}

	checks := []check{
		{state.ControlPlaneHealthy, "Control Plane: Healthy", "Control Plane: Unhealthy", true},
		{state.NodesAllReady,
			"Worker Nodes: All Ready",
			nodesFailMsg,
			false},
		{state.WebhooksSupported, "Admission Webhooks: Mutating & Validating Supported", "Admission Webhooks: Not Supported", true},
		{state.NetworkPoliciesSupported, "Network Policies: Supported", "Network Policies: Not Confirmed", false},
		// SMB CSI Driver missing is non-blocking: it is required only when
		// the HelmSharedStorage feature flag is enabled (NVCA model-cache).
		// pkg/storage/smbcsidriver.go's runtime health check itself flags
		// this at StatusLevelWarn, not StatusLevelError — block install
		// only when the customer has explicitly opted in to a feature that
		// needs SMB CSI, not for every operator install.
		{state.SMBCSIDriverOK, "SMB CSI Driver: v1.16.0+ Installed", "SMB CSI Driver: Not Installed or Below v1.16.0", false},
	}

	if state.ReachabilityOK != nil {
		isCritical := state.ReachabilityCriticalOK != nil &&
			!*state.ReachabilityCriticalOK
		checks = append(checks, check{
			*state.ReachabilityOK,
			"Endpoint Reachability: All Endpoints Reachable",
			"Endpoint Reachability: One or more endpoints not reachable",
			isCritical,
		})
	}

	checks = append(checks,
		check{state.GPUAvailable, "GPU Resources: Available", "GPU Resources: Not Available", true},
		// GPU Operator missing is non-blocking: clusters registered with
		// Manual Instance Configuration expose GPUs via an alternative
		// mechanism (pre-labeled nodes, DaemonSet, etc.) and do not require
		// GPU Operator. GPU Resources above is the load-bearing signal —
		// if GPUs aren't usable that fails Critical separately.
		check{state.GPUOperatorInstalled, "GPU Operator: Installed", "GPU Operator: Not Installed", false},
	)

	if state.ConfigurableNetPolOK != nil {
		isCritical := state.ConfigurableNetPolCriticalOK != nil &&
			!*state.ConfigurableNetPolCriticalOK
		checks = append(checks, check{
			*state.ConfigurableNetPolOK,
			"Configurable Network Policies: All Checks Passed",
			"Configurable Network Policies: Some Checks Failed",
			isCritical,
		})
	}
	if state.EnforcementOK != nil {
		checks = append(checks, check{
			*state.EnforcementOK,
			"Network Policy Enforcement: Active Validation Passed",
			"Network Policy Enforcement: Active Validation Failed",
			state.EnforcementCritical,
		})
	}

	for _, c := range checks {
		if c.Passed {
			printSuccess(log, fmt.Sprintf("  %s", c.PassMsg))
		} else if c.Critical {
			printError(log, fmt.Sprintf("  %s", c.FailMsg))
			isReady = false
		} else {
			printWarning(log, fmt.Sprintf("  %s", c.FailMsg))
		}
	}

	log.Info("")
	log.Infof("%s%s%s", colorBlue, separator, colorReset)
	log.Info("")
	if isReady {
		hasWarnings := len(state.Warnings) > 0
		if hasWarnings {
			log.Infof("%s╔═══════════════════════════════════════════════════════════╗%s", colorYellow, colorReset)
			log.Infof("%s║        %s  Cluster is NVCF-Ready (with warnings)  %s        ║%s", colorYellow, iconWarn, iconWarn, colorReset)
			log.Infof("%s╚═══════════════════════════════════════════════════════════╝%s", colorYellow, colorReset)
			log.Info("")
			printWarning(log, "Your cluster meets all critical requirements; see warnings below for non-blocking issues.")
		} else {
			log.Infof("%s╔═══════════════════════════════════════════════════════════╗%s", colorGreen, colorReset)
			log.Infof("%s║                %s  Cluster is NVCF-Ready  %s                ║%s", colorGreen, iconCheck, iconCheck, colorReset)
			log.Infof("%s╚═══════════════════════════════════════════════════════════╝%s", colorGreen, colorReset)
			log.Info("")
			printSuccess(log, "Your cluster meets all requirements for NVCF workloads")
		}
		log.Info("")
		log.Info("Validated Cluster:")
		printInfo(log, fmt.Sprintf("  Kubernetes Version: %s", state.K8sVersion))
		printInfo(log, fmt.Sprintf("  Total Nodes: %s", state.TotalNodes))
	} else {
		log.Infof("%s╔═══════════════════════════════════════════════════════════╗%s", colorRed, colorReset)
		log.Infof("%s║              %s  Cluster is NVCF-Not-Ready  %s              ║%s", colorRed, iconCross, iconCross, colorReset)
		log.Infof("%s╚═══════════════════════════════════════════════════════════╝%s", colorRed, colorReset)
		log.Info("")
		printError(log, "Your cluster does not meet all requirements for NVCF workloads")
	}

	if len(state.Warnings) > 0 {
		log.Info("")
		log.Warn("Warnings (manual verification required):")
		for i, w := range state.Warnings {
			log.Warnf("  %d. %s %s", i+1, iconWarn, w)
		}
	}

	if len(state.Recommendations) > 0 {
		log.Info("")
		log.Info("Recommendations:")
		for i, r := range state.Recommendations {
			log.Infof("  %d. %s", i+1, r)
		}
	}
	log.Info("")
	log.Infof("Validation completed at %s", time.Now().UTC().Format("2006-01-02 15:04:05 UTC"))

	if !isReady {
		return fmt.Errorf("cluster is NVCF-Not-Ready")
	}
	return nil
}
