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
	"slices"
	"strings"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// recheckDelay is the pause before the recheck, so a blip that failed a check
// has a moment to clear. A var so tests need not wait it out.
var recheckDelay = 10 * time.Second

// check is one check of the suite. run records the check's rows on the state
// it is given, with their warnings and recommendations, and adopt copies those
// rows to another state. blocks reports whether a critical row of the check
// failed or was not observed; it is nil for a check with no critical row.
// configured marks a check the network-checks ConfigMap drives.
type check struct {
	name       string
	run        func(ctx context.Context, s *ValidationState)
	adopt      func(dst, src *ValidationState)
	blocks     func(s *ValidationState) bool
	configured bool
}

// suite is the checks of one run and what they share.
type suite struct {
	client                      kubernetes.Interface
	routes                      dynamic.Interface
	configNamespace, configName string
	// netCfg is the network-checks ConfigMap, nil when none is configured or
	// it could not be loaded.
	netCfg *NetworkCheckConfig
	// gateways is the Gateway API surface and the NVCF Gateways, read by the
	// first Gateway check of a pass so the CRD, Envoy, route, LoadBalancer and
	// Tier-1 rows judge the same set.
	gateways *gatewayView
}

type gatewayView struct {
	surface gatewayAPISurface
	err     error
	own     *gatewayOwnership
}

func (su *suite) gatewayView(ctx context.Context) *gatewayView {
	if su.gateways == nil {
		surface, err := discoverGatewayAPIResources(ctx, su.client)
		su.gateways = &gatewayView{
			surface: surface, err: err,
			own: resolveGatewayOwnershipIn(ctx, su.client, surface, err, su.routes),
		}
	}
	return su.gateways
}

// run runs the role's checks, each on a state of its own, and records their
// results on state, which holds none until then. With recheck set, each check
// whose critical rows did not pass first runs once more, and its second result
// is the one recorded: a published verdict stands until the next run, so one
// transient error must not decide it. A preflight run is graded by its
// launcher, which can run it again.
func (su *suite) run(ctx context.Context, state *ValidationState, recheck bool) {
	checks := su.checks(state.Role)
	results := make([]*ValidationState, len(checks))
	for i, c := range checks {
		results[i] = state.fork()
		c.run(ctx, results[i])
	}
	if recheck {
		su.recheck(ctx, state, checks, results)
	}
	for i, c := range checks {
		state.record(c, results[i])
	}
}

// recheck runs again each check in checks whose result in results has a
// critical row that did not pass, and replaces that result. A check cut short
// by the run's deadline would report the deadline rather than the cluster, so
// it, and every check after it, keeps its first result.
func (su *suite) recheck(ctx context.Context, state *ValidationState, checks []check, results []*ValidationState) {
	var names []string
	for i, c := range checks {
		if c.blocks != nil && c.blocks(results[i]) {
			names = append(names, c.name)
		}
	}
	if len(names) == 0 {
		return
	}
	log := state.Log
	printHeader(log, "Recheck")
	printInfo(log, fmt.Sprintf("Critical checks that did not pass run once more before the verdict, so a "+
		"transient error does not decide it: %s", strings.Join(names, ", ")))
	if ctx.Err() != nil || !sleepCtx(ctx, recheckDelay) {
		printWarning(log, "The run has no time left to recheck them; their first results stand")
		return
	}
	hadConfig := su.netCfg != nil
	su.gateways = nil
	for i, c := range checks {
		again := c.blocks != nil && c.blocks(results[i])
		// A check the ConfigMap drives did not run when only the recheck could
		// load it.
		if c.configured && !hadConfig && su.netCfg != nil {
			again = true
		}
		if !again {
			continue
		}
		r := state.fork()
		c.run(ctx, r)
		if ctx.Err() != nil {
			printWarning(log, fmt.Sprintf("The run ended during the recheck of %s; it and the checks after it "+
				"keep their first results", c.name))
			return
		}
		results[i] = r
	}
}

// fork returns a state for one check to record its result on: s's settings
// and cluster facts, without warnings, recommendations or unobserved keys.
func (s *ValidationState) fork() *ValidationState {
	f := *s
	f.Warnings, f.Recommendations, f.Unobserved = nil, nil, nil
	return &f
}

// record copies the result r of check c onto s, with the warnings and
// recommendations of r that s does not hold yet: a check run again can repeat
// one that a check run once already gave.
func (s *ValidationState) record(c check, r *ValidationState) {
	if c.adopt != nil {
		c.adopt(s, r)
	}
	for _, w := range r.Warnings {
		if !slices.Contains(s.Warnings, w) {
			s.Warnings = append(s.Warnings, w)
		}
	}
	for _, rec := range r.Recommendations {
		if !slices.Contains(s.Recommendations, rec) {
			s.Recommendations = append(s.Recommendations, rec)
		}
	}
}

// adoptUnobserved copies whether each of keys was unobserved from src to dst.
func adoptUnobserved(dst, src *ValidationState, keys ...string) {
	for _, k := range keys {
		if src.Unobserved[k] {
			dst.markUnobserved(k)
		} else {
			delete(dst.Unobserved, k)
		}
	}
}

// notPassed reports whether a tri-state row failed or was not observed.
func notPassed(ok *bool) bool { return ok == nil || !*ok }

// ranAndFailed reports whether a row that may not apply ran and failed.
func ranAndFailed(ok *bool) bool { return ok != nil && !*ok }

// checks returns the role's checks in the order they run.
func (su *suite) checks(role Role) []check {
	checks := []check{
		{
			name: "Control Plane",
			run:  func(ctx context.Context, s *ValidationState) { checkControlPlaneHealth(ctx, su.client, s) },
			adopt: func(dst, src *ValidationState) {
				dst.ControlPlaneHealthy, dst.NodesAllReady, dst.NotReadyNodes =
					src.ControlPlaneHealthy, src.NodesAllReady, src.NotReadyNodes
				adoptUnobserved(dst, src, CheckKeyControlPlane, CheckKeyWorkerNodesAllReady)
			},
			blocks: func(s *ValidationState) bool { return !s.ControlPlaneHealthy || s.Unobserved[CheckKeyControlPlane] },
		},
		{
			name: "Admission Webhooks",
			run:  func(ctx context.Context, s *ValidationState) { checkWebhookSupport(ctx, su.client, s) },
			adopt: func(dst, src *ValidationState) {
				dst.WebhooksSupported = src.WebhooksSupported
				adoptUnobserved(dst, src, CheckKeyWebhooks)
			},
			blocks: func(s *ValidationState) bool { return !s.WebhooksSupported || s.Unobserved[CheckKeyWebhooks] },
		},
		{
			name: "Network Policies",
			run:  func(ctx context.Context, s *ValidationState) { checkNetworkPolicies(ctx, su.client, s) },
			adopt: func(dst, src *ValidationState) {
				dst.NetworkPoliciesSupported = src.NetworkPoliciesSupported
				adoptUnobserved(dst, src, CheckKeyNetworkPoliciesSupport)
			},
		},
		{
			name:   "Network Checks",
			run:    su.loadNetworkChecks,
			adopt:  func(dst, src *ValidationState) { dst.NetworkChecksErr = src.NetworkChecksErr },
			blocks: func(s *ValidationState) bool { return s.NetworkChecksErr != "" },
		},
		{
			name: "Endpoint Reachability", configured: true,
			run: func(ctx context.Context, s *ValidationState) {
				if cfg := su.netCfg; cfg != nil && cfg.Reachability != nil && len(cfg.Reachability.Endpoints) > 0 {
					checkConfigurableReachability(ctx, s, cfg.Reachability)
				}
			},
			adopt: func(dst, src *ValidationState) {
				dst.ReachabilityOK, dst.ReachabilityCriticalOK, dst.EndpointResults =
					src.ReachabilityOK, src.ReachabilityCriticalOK, src.EndpointResults
			},
			blocks: func(s *ValidationState) bool { return ranAndFailed(s.ReachabilityCriticalOK) },
		},
	}
	if role == RoleControlPlane {
		checks = append(checks, su.controlPlaneChecks()...)
	} else {
		checks = append(checks, su.computePlaneChecks()...)
	}
	return append(checks,
		check{
			name: "Configurable Network Policies", configured: true,
			run: func(ctx context.Context, s *ValidationState) {
				if cfg := su.netCfg; cfg != nil && cfg.NetworkPolicies != nil && len(cfg.NetworkPolicies.Pairs) > 0 {
					checkConfigurableNetworkPolicies(ctx, su.client, s, cfg.NetworkPolicies)
				}
			},
			adopt: func(dst, src *ValidationState) {
				dst.ConfigurableNetPolOK, dst.ConfigurableNetPolCriticalOK, dst.NetpolPairResults =
					src.ConfigurableNetPolOK, src.ConfigurableNetPolCriticalOK, src.NetpolPairResults
			},
			blocks: func(s *ValidationState) bool { return ranAndFailed(s.ConfigurableNetPolCriticalOK) },
		},
		check{
			name: "Network Policy Enforcement", configured: true,
			run: func(ctx context.Context, s *ValidationState) {
				if cfg := su.netCfg; cfg != nil && cfg.Enforcement != nil && cfg.Enforcement.Enabled {
					checkNetworkPolicyEnforcement(ctx, su.client, s, cfg.Enforcement)
				}
			},
			adopt: func(dst, src *ValidationState) {
				dst.EnforcementOK, dst.EnforcementCritical = src.EnforcementOK, src.EnforcementCritical
			},
			blocks: func(s *ValidationState) bool { return s.EnforcementCritical && notPassed(s.EnforcementOK) },
		},
	)
}

// controlPlaneChecks check gateway infrastructure, storage, inter-node overlay
// connectivity and HA readiness. GPU operator and SMB CSI are compute-plane
// concerns and are skipped.
func (su *suite) controlPlaneChecks() []check {
	return []check{
		{
			name: "Default StorageClass",
			run:  func(ctx context.Context, s *ValidationState) { checkStorageClass(ctx, su.client, s) },
			adopt: func(dst, src *ValidationState) {
				dst.DefaultStorageClassOK, dst.StorageClassFailure = src.DefaultStorageClassOK, src.StorageClassFailure
			},
			blocks: func(s *ValidationState) bool { return notPassed(s.DefaultStorageClassOK) },
		},
		{
			name: "Gateway API CRDs",
			run: func(ctx context.Context, s *ValidationState) {
				gw := su.gatewayView(ctx)
				checkGatewayAPICRDsIn(s, gw.surface, gw.err)
			},
			adopt:  func(dst, src *ValidationState) { dst.GatewayAPICRDsOK = src.GatewayAPICRDsOK },
			blocks: func(s *ValidationState) bool { return notPassed(s.GatewayAPICRDsOK) },
		},
		{
			name: "Envoy Gateway",
			run: func(ctx context.Context, s *ValidationState) {
				checkEnvoyGatewayFor(ctx, su.client, su.gatewayView(ctx).own, s)
			},
			adopt: func(dst, src *ValidationState) { dst.EnvoyGatewayOK = src.EnvoyGatewayOK },
		},
		{
			name: "Optional Gateway Route CR Types",
			run: func(ctx context.Context, s *ValidationState) {
				gw := su.gatewayView(ctx)
				checkGatewayRoutesIn(s, gw.surface, gw.err)
			},
			adopt: func(dst, src *ValidationState) { dst.GatewayRoutesOK = src.GatewayRoutesOK },
		},
		{
			name: "External Load Balancer",
			run: func(ctx context.Context, s *ValidationState) {
				checkExternalLoadBalancerFor(ctx, su.client, su.gatewayView(ctx).own, s)
			},
			adopt: func(dst, src *ValidationState) { dst.ExternalLBOK = src.ExternalLBOK },
		},
		{
			name: "Node-to-Node Communication",
			run: func(ctx context.Context, s *ValidationState) {
				checkNodeToNode(ctx, su.client, s, nodeToNodeProbeImage(su.netCfg))
			},
			adopt: func(dst, src *ValidationState) {
				dst.NodeToNodeOK, dst.NodeToNodeNotApplicable = src.NodeToNodeOK, src.NodeToNodeNotApplicable
			},
			blocks: func(s *ValidationState) bool {
				return s.NodeToNodeNotApplicable == "" && notPassed(s.NodeToNodeOK)
			},
		},
		{
			name: "Tier-1 Deployments",
			run: func(ctx context.Context, s *ValidationState) {
				checkTier1DeploymentsFor(ctx, su.client, su.gatewayView(ctx).own, s)
			},
			adopt:  func(dst, src *ValidationState) { dst.Tier1DeploymentsOK = src.Tier1DeploymentsOK },
			blocks: func(s *ValidationState) bool { return notPassed(s.Tier1DeploymentsOK) },
		},
		{
			name: "Tier-2 StatefulSets",
			run:  func(ctx context.Context, s *ValidationState) { checkTier2StatefulSets(ctx, su.client, s) },
			adopt: func(dst, src *ValidationState) {
				dst.Tier2StatefulSetsOK, dst.Tier2PlacementNotAssessed =
					src.Tier2StatefulSetsOK, src.Tier2PlacementNotAssessed
			},
			blocks: func(s *ValidationState) bool { return notPassed(s.Tier2StatefulSetsOK) },
		},
	}
}

// computePlaneChecks check the GPU operator and SMB CSI driver.
func (su *suite) computePlaneChecks() []check {
	return []check{
		{
			name: "SMB CSI Driver",
			run:  func(ctx context.Context, s *ValidationState) { checkSMBCSIDriver(ctx, su.client, s) },
			adopt: func(dst, src *ValidationState) {
				dst.SMBCSIDriverOK = src.SMBCSIDriverOK
				adoptUnobserved(dst, src, CheckKeySMBCSI)
			},
		},
		{
			name: GPUResourcesLabel,
			run:  func(ctx context.Context, s *ValidationState) { checkGPUResources(ctx, su.client, s) },
			adopt: func(dst, src *ValidationState) {
				dst.GPUAvailable = src.GPUAvailable
				adoptUnobserved(dst, src, CheckKeyGPUResources)
			},
			blocks: func(s *ValidationState) bool { return !s.GPUAvailable || s.Unobserved[CheckKeyGPUResources] },
		},
		{
			name: "GPU Operator",
			run:  func(ctx context.Context, s *ValidationState) { checkGPUOperator(ctx, su.client, s) },
			adopt: func(dst, src *ValidationState) {
				dst.GPUOperatorInstalled = src.GPUOperatorInstalled
				adoptUnobserved(dst, src, CheckKeyGPUOperator)
			},
		},
	}
}

// loadNetworkChecks loads the network-checks ConfigMap into su.netCfg. The
// checks it configures may be critical, so a load failure cannot simply skip
// them: it says so, and leaves the verdict unknown.
func (su *suite) loadNetworkChecks(ctx context.Context, s *ValidationState) {
	su.netCfg = nil
	if su.configNamespace == "" || su.configName == "" {
		return
	}
	cfg, err := LoadNetworkCheckConfig(ctx, su.client, su.configNamespace, su.configName)
	if err != nil {
		s.NetworkChecksErr = fmt.Sprintf("could not load %s/%s: %v", su.configNamespace, su.configName, err)
		printWarning(s.Log, "Network checks: "+s.NetworkChecksErr)
		s.Warnings = append(s.Warnings, "Network checks: "+s.NetworkChecksErr+
			"; its endpoint, NetworkPolicy and enforcement checks did not run")
		return
	}
	su.netCfg = cfg
}
