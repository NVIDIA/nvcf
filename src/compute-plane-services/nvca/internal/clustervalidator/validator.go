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

// Role values for VALIDATOR_ROLE.
const (
	RoleComputePlane = "compute-plane"
	RoleControlPlane = "control-plane"
)

// ValidationState captures the results of every validation check.
type ValidationState struct {
	Log  *logrus.Entry
	// Role is "control-plane" or "compute-plane" (empty = compute-plane default).
	// printSummary uses it to include only the checks relevant to the role.
	Role                string
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
	ContainerRuntime         string
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

	// Control-plane-specific check outcomes. Nil means the check was not run
	// (compute-plane role). Non-nil means the check ran and the bool holds
	// the pass/fail result.
	DefaultStorageClassOK *bool
	GatewayAPICRDsOK      *bool
	EnvoyGatewayOK        *bool
	GatewayRoutesOK       *bool
	ExternalLBOK          *bool
	// NodeToNodeOK is nil when the check was skipped (single-node cluster or
	// compute-plane role). true = overlay verified, false = failed.
	NodeToNodeOK *bool
	// Tier1DeploymentsOK is nil when the check did not run (compute-plane role)
	// or when a Deployment list call fails. Pre-install (no Deployments found)
	// sets this to true, not nil.
	Tier1DeploymentsOK *bool
	// Tier2StatefulSetsOK is nil when the check did not run (compute-plane role)
	// or when a StatefulSet list call fails. No quorum StatefulSets found
	// (pre-install or non-HA install) sets this to true, not nil.
	Tier2StatefulSetsOK *bool

	// EndpointResults captures per-endpoint reachability outcomes for the
	// summary ConfigMap / metrics pipeline. Keyed by the user-supplied
	// endpoint name (the same string Prometheus will use as the label
	// value). Populated by checkConfigurableReachability when a network
	// check config is loaded; empty otherwise.
	EndpointResults map[string]EndpointResult
	// NetpolPairResults captures per-pair NetworkPolicy-coverage outcomes
	// for the summary ConfigMap / metrics pipeline. Keyed by the
	// user-supplied pair name. Populated by checkConfigurableNetworkPolicies.
	NetpolPairResults map[string]NetpolPairResult
}

// EndpointResult is one row of ValidationState.EndpointResults — the
// per-endpoint outcome the agent will surface as a Prometheus gauge.
type EndpointResult struct {
	Reachable bool
	Critical  bool
}

// NetpolPairResult is one row of ValidationState.NetpolPairResults. The
// fields mirror clustervalidator.PairStatus so buildSummary can convert
// directly. Directions holds the per-direction, per-policy-side breakdown
// keyed by NetpolDirectionAToB / NetpolDirectionBToA.
type NetpolPairResult struct {
	Passed     bool
	Critical   bool
	Directions map[string]DirectionStatus
}

// Run executes all cluster validation checks and returns a non-nil error when
// the cluster is not ready. role selects the check set; configNamespace/configName
// identify the optional ConfigMap; emitMetrics gates the summary write.
func Run(
	ctx context.Context,
	client kubernetes.Interface,
	configNamespace, configName, summaryNamespace string,
	emitMetrics bool,
	role string,
) error {
	startedAt := time.Now()
	log := core.GetLogger(ctx)
	log.Info("Starting NVCF cluster validation")
	log.Info("")
	log.Infof("%s╔═══════════════════════════════════════════════════════════╗%s", colorBlue, colorReset)
	log.Infof("%s║     NVIDIA Cloud BYOC Cluster Readiness Check             ║%s", colorBlue, colorReset)
	log.Infof("%s║         Kubernetes Cluster Validation                     ║%s", colorBlue, colorReset)
	log.Infof("%s╚═══════════════════════════════════════════════════════════╝%s", colorBlue, colorReset)

	state := &ValidationState{
		Log:                 log,
		Role:                role,
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

	if role == RoleControlPlane {
		// Control-plane cluster: check gateway infrastructure, storage, and
		// inter-node overlay connectivity. GPU operator and SMB CSI are
		// compute-plane concerns and are skipped.
		checkStorageClass(ctx, client, state)
		checkGatewayAPICRDs(ctx, client, state)
		checkEnvoyGateway(ctx, client, state)
		checkGatewayRoutes(ctx, client, state)
		checkExternalLoadBalancer(ctx, client, state)
		// CLI RBAC bootstrap (Req 3) grants DaemonSet create/delete and
		// pod-create before Job submission; no emitMetrics gate needed.
		checkNodeToNode(ctx, client, state)
		checkTier1Deployments(ctx, client, state)
		checkTier2StatefulSets(ctx, client, state)
	} else {
		// Compute-plane cluster (default): GPU operator, SMB CSI driver.
		checkSMBCSIDriver(ctx, client, state)
		checkGPUResources(ctx, client, state)
		checkGPUOperator(ctx, client, state)
	}

	if netCfg != nil {
		if netCfg.NetworkPolicies != nil && len(netCfg.NetworkPolicies.Pairs) > 0 {
			checkConfigurableNetworkPolicies(ctx, client, state, netCfg.NetworkPolicies)
		}
		if netCfg.Enforcement != nil && netCfg.Enforcement.Enabled {
			checkNetworkPolicyEnforcement(ctx, client, state, netCfg.Enforcement)
		}
	}

	summaryErr := printSummary(state)

	// Persist the summary (to summaryNamespace, the agent's watch namespace)
	// for the agent to publish as metrics. Gated on emitMetrics so preflight
	// skips it. Best-effort: failures are logged, never block the verdict.
	verdict := "NVCF-Ready"
	if summaryErr != nil {
		verdict = "NVCF-Not-Ready"
	}
	if emitMetrics && summaryNamespace != "" {
		writeSummaryConfigMap(ctx, log, client, summaryNamespace,
			buildSummary(state, startedAt, summaryErr == nil, verdict))
	}

	return summaryErr
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

	if state.Role == RoleControlPlane {
		// Control-plane checks: gateway infrastructure and storage. GPU and
		// SMB checks are compute-plane concerns and are excluded here.
		if state.DefaultStorageClassOK != nil {
			checks = append(checks, check{*state.DefaultStorageClassOK,
				"Default StorageClass: Present", "Default StorageClass: Not Found", true})
		}
		if state.GatewayAPICRDsOK != nil {
			checks = append(checks, check{*state.GatewayAPICRDsOK,
				"Gateway API CRDs: Installed", "Gateway API CRDs: Not Installed", true})
		}
		if state.EnvoyGatewayOK != nil {
			// Non-critical: Envoy Gateway is installed by nvcf-cli up, so it is
			// expected to be absent on a fresh cluster before the first install.
			// A missing Envoy is informative (tells the operator the stack is not
			// yet deployed) but must not block a pre-install readiness check.
			checks = append(checks, check{*state.EnvoyGatewayOK,
				"Envoy Gateway: Installed and Running", "Envoy Gateway: Not Found or Not Running", false})
		}
		if state.GatewayRoutesOK != nil {
			checks = append(checks, check{*state.GatewayRoutesOK,
				"Gateway Routes: Present", "Gateway Routes: None Found", false})
		}
		if state.ExternalLBOK != nil {
			checks = append(checks, check{*state.ExternalLBOK,
				"External Load Balancer: IP Assigned", "External Load Balancer: No IP Assigned", false})
		}
		if state.NodeToNodeOK != nil {
			checks = append(checks, check{*state.NodeToNodeOK,
				"Node-to-Node Communication: Verified", "Node-to-Node Communication: Failed", true})
		}
		if state.Tier1DeploymentsOK != nil {
			checks = append(checks, check{*state.Tier1DeploymentsOK,
				"Tier-1 Deployments: All Ready", "Tier-1 Deployments: Under-replicated", true})
		}
		if state.Tier2StatefulSetsOK != nil {
			checks = append(checks, check{*state.Tier2StatefulSetsOK,
				"Tier-2 StatefulSets: Quorum and Placement OK", "Tier-2 StatefulSets: Quorum or Placement Failed", true})
		}
	} else {
		// Compute-plane checks: GPU resources, GPU operator, SMB CSI driver.
		// SMB CSI Driver missing is non-blocking: it is required only when
		// the HelmSharedStorage feature flag is enabled (NVCA model-cache).
		checks = append(checks,
			check{state.SMBCSIDriverOK, "SMB CSI Driver: v1.16.0+ Installed", "SMB CSI Driver: Not Installed or Below v1.16.0", false},
			check{state.GPUAvailable, "GPU Resources: Available", "GPU Resources: Not Available", true},
			// GPU Operator missing is non-blocking: clusters registered with
			// Manual Instance Configuration expose GPUs via an alternative
			// mechanism (pre-labeled nodes, DaemonSet, etc.) and do not require
			// GPU Operator. GPU Resources above is the load-bearing signal.
			check{state.GPUOperatorInstalled, "GPU Operator: Installed", "GPU Operator: Not Installed", false},
		)
	}

	if state.ConfigurableNetPolOK != nil {
		isCritical := state.ConfigurableNetPolCriticalOK != nil &&
			!*state.ConfigurableNetPolCriticalOK
		checks = append(checks, check{
			*state.ConfigurableNetPolOK,
			"Configurable Network Policies: All Checks Passed",
			"Configurable Network Policies: One or more checks failed",
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
		if state.ContainerRuntime != "" {
			printInfo(log, fmt.Sprintf("  Container Runtime: %s", state.ContainerRuntime))
		}
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
