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

	"github.com/sirupsen/logrus"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// recheckDelay is the pause before the recheck, so a blip that failed a check
// has a moment to clear. A var so tests need not wait it out.
var recheckDelay = 10 * time.Second

// check is one check of the suite. run records the check's rows on the state
// it is given, with their warnings and recommendations, and adopt copies those
// rows to another state. critical reports how the check's critical rows came
// out; it is nil for a check with no critical row. configured marks a check
// the network-checks ConfigMap drives. reads names the earlier check whose
// result run reads.
type check struct {
	name       string
	run        func(ctx context.Context, s *ValidationState)
	adopt      func(dst, src *ValidationState)
	critical   func(s *ValidationState) outcome
	configured bool
	reads      string
}

// outcome is how the critical rows of a check came out: all passed (or did
// not apply), one failed on what the check observed, or one was not observed.
type outcome int

const (
	passed outcome = iota
	failed
	unobserved
)

// blocks reports whether a critical row of c failed or was not observed in r.
func (c check) blocks(r *ValidationState) bool {
	return c.critical != nil && c.critical(r) != passed
}

// rowOutcome is the outcome of a tri-state row.
func rowOutcome(ok *bool) outcome {
	switch {
	case ok == nil:
		return unobserved
	case !*ok:
		return failed
	}
	return passed
}

// flagOutcome is the outcome of a row held as a flag and an unobserved key.
func flagOutcome(ok, unknown bool) outcome {
	switch {
	case unknown:
		return unobserved
	case !ok:
		return failed
	}
	return passed
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
	// n2nFailed is the path a node-to-node checker dialled when the row
	// failed, which a recheck dials again.
	n2nFailed *n2nPath
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

// run runs the role's checks, each on a state of its own that carries the
// results of the checks before it, and records their results on state, which
// holds none until then. With recheck set, each check whose critical rows did
// not pass first runs once more, and its second result is the one recorded: a
// published verdict stands until the next run, so one transient error must not
// decide it. A preflight run is graded by its launcher, which can run it again.
func (su *suite) run(ctx context.Context, state *ValidationState, recheck bool) {
	su.runChecks(ctx, state, su.checks(state.Role), recheck)
}

// runChecks is run for checks. A critical check that fails its first run and
// passes its second is recorded with a warning saying what the first run saw,
// so a flapping check shows on a Ready verdict too.
func (su *suite) runChecks(ctx context.Context, state *ValidationState, checks []check, recheck bool) {
	results := make([]*ValidationState, len(checks))
	saw := make([][]string, len(checks))
	facts := state.fork()
	for i, c := range checks {
		results[i] = facts.fork()
		saw[i] = runRecordingErrors(ctx, c, results[i])
		c.adopt(facts, results[i])
	}
	if recheck {
		su.recheck(ctx, state, checks, results, saw)
	}
	for i, c := range checks {
		state.record(c, results[i])
	}
}

// errorLines records the error lines logged through it.
type errorLines struct{ lines []string }

func (e *errorLines) Levels() []logrus.Level { return []logrus.Level{logrus.ErrorLevel} }

func (e *errorLines) Fire(entry *logrus.Entry) error {
	if line := strings.TrimSpace(strings.TrimPrefix(entry.Message, iconCross)); line != "" {
		e.lines = append(e.lines, line)
	}
	return nil
}

// runRecordingErrors runs c on s and returns the error lines it logged. The
// lines go to s's logger as ever, through a copy of it that also records them.
func runRecordingErrors(ctx context.Context, c check, s *ValidationState) []string {
	log := s.Log
	recorder := &errorLines{}
	hooks := logrus.LevelHooks{}
	for level, levelHooks := range log.Logger.Hooks {
		hooks[level] = slices.Clone(levelHooks)
	}
	hooks.Add(recorder)
	logger := &logrus.Logger{
		Out: log.Logger.Out, Hooks: hooks, Formatter: log.Logger.Formatter, ReportCaller: log.Logger.ReportCaller,
		Level: log.Logger.GetLevel(), ExitFunc: log.Logger.ExitFunc, BufferPool: log.Logger.BufferPool,
	}
	s.Log = logger.WithFields(log.Data).WithContext(log.Context)
	c.run(ctx, s)
	s.Log = log
	return recorder.lines
}

// recheck runs again each check in checks whose result in results has a
// critical row that did not pass, and replaces that result, along with the
// result of a check that reads a replaced one. A recheck that could not observe
// a check its first pass saw fail keeps the failure. One that passes a check
// whose first run failed adds a warning naming what that run saw, from its
// error lines in saw. A check cut short by the run's deadline would report the
// deadline rather than the cluster, so it, and every check after it, keeps its
// first result.
func (su *suite) recheck(
	ctx context.Context, state *ValidationState, checks []check, results []*ValidationState, saw [][]string,
) {
	var names []string
	for i, c := range checks {
		if c.blocks(results[i]) {
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
		recheckCutShort(state, checks, results, 0)
		return
	}
	hadConfig := su.netCfg != nil
	su.gateways = nil
	facts := state.fork()
	replaced := map[string]bool{}
	for i, c := range checks {
		again := c.blocks(results[i]) || (c.reads != "" && replaced[c.reads])
		// A check the ConfigMap drives did not run when only the recheck could
		// load it.
		if c.configured && !hadConfig && su.netCfg != nil {
			again = true
		}
		if again {
			r := facts.fork()
			c.run(ctx, r)
			if ctx.Err() != nil {
				printWarning(log, fmt.Sprintf("The run ended during the recheck of %s; it and the checks after it "+
					"keep their first results", c.name))
				recheckCutShort(state, checks, results, i)
				return
			}
			switch {
			case c.critical != nil && c.critical(results[i]) == failed && c.critical(r) == unobserved:
				printWarning(log, fmt.Sprintf("The recheck could not observe %s; the failure its first run "+
					"observed stands", c.name))
			default:
				if c.critical != nil && c.critical(results[i]) == failed && c.critical(r) == passed {
					r.Warnings = append(r.Warnings, clearedWarning(c.name, saw[i]))
				}
				results[i] = r
				replaced[c.name] = true
			}
		}
		c.adopt(facts, results[i])
	}
}

// clearedWarning is the warning for critical check name that failed its first
// run and passed its second, with what the first run saw: the verdict follows
// the second, and this keeps the failure from vanishing with the first.
func clearedWarning(name string, saw []string) string {
	w := name + ": failed its first run and passed when run again, so it may fail intermittently"
	if len(saw) > 0 {
		w += "; the first run saw: " + shortMessage(strings.Join(saw[:min(len(saw), 3)], "; "))
	}
	return w
}

// recheckCutShort records on state that the run's time ran out before the
// checks from checks[from] on could run again: each of them that blocks keeps
// its first result, with a warning that it is unconfirmed. When no check the
// recheck did run still blocks, the Not-Ready verdict rests on those first
// results alone and is unconfirmed.
func recheckCutShort(state *ValidationState, checks []check, results []*ValidationState, from int) {
	var names []string
	for j := from; j < len(checks); j++ {
		if checks[j].blocks(results[j]) {
			names = append(names, checks[j].name)
		}
	}
	if len(names) == 0 {
		return
	}
	state.Warnings = append(state.Warnings, fmt.Sprintf("Recheck: the run's time ran out before %s could run "+
		"again, so what the first pass saw stands unconfirmed", strings.Join(names, ", ")))
	state.NotReadyUnconfirmed = true
	for j := range from {
		if checks[j].blocks(results[j]) {
			state.NotReadyUnconfirmed = false
		}
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
	c.adopt(s, r)
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

// ranOutcome is the outcome of a row that may not apply: nil did not run.
func ranOutcome(ok *bool) outcome {
	if ok == nil {
		return passed
	}
	return rowOutcome(ok)
}

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
			critical: func(s *ValidationState) outcome {
				return flagOutcome(s.ControlPlaneHealthy, s.Unobserved[CheckKeyControlPlane])
			},
		},
		{
			name: "Admission Webhooks",
			run:  func(ctx context.Context, s *ValidationState) { checkWebhookSupport(ctx, su.client, s) },
			adopt: func(dst, src *ValidationState) {
				dst.WebhooksSupported = src.WebhooksSupported
				adoptUnobserved(dst, src, CheckKeyWebhooks)
			},
			critical: func(s *ValidationState) outcome {
				return flagOutcome(s.WebhooksSupported, s.Unobserved[CheckKeyWebhooks])
			},
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
			name:  "Network Checks",
			run:   su.loadNetworkChecks,
			adopt: func(dst, src *ValidationState) { dst.NetworkChecksErr = src.NetworkChecksErr },
			critical: func(s *ValidationState) outcome {
				return flagOutcome(true, s.NetworkChecksErr != "")
			},
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
			critical: func(s *ValidationState) outcome { return ranOutcome(s.ReachabilityCriticalOK) },
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
			critical: func(s *ValidationState) outcome { return ranOutcome(s.ConfigurableNetPolCriticalOK) },
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
			critical: func(s *ValidationState) outcome {
				if !s.EnforcementCritical {
					return passed
				}
				return rowOutcome(s.EnforcementOK)
			},
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
			critical: func(s *ValidationState) outcome { return rowOutcome(s.DefaultStorageClassOK) },
		},
		{
			name: "Gateway API CRDs",
			run: func(ctx context.Context, s *ValidationState) {
				gw := su.gatewayView(ctx)
				checkGatewayAPICRDsIn(s, gw.surface, gw.err)
			},
			adopt:    func(dst, src *ValidationState) { dst.GatewayAPICRDsOK = src.GatewayAPICRDsOK },
			critical: func(s *ValidationState) outcome { return rowOutcome(s.GatewayAPICRDsOK) },
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
				path := probeNodeToNode(ctx, su.client, s, nodeToNodeProbeImage(su.netCfg), su.n2nFailed)
				if s.NodeToNodeOK != nil && !*s.NodeToNodeOK {
					su.n2nFailed = path
				}
			},
			adopt: func(dst, src *ValidationState) {
				dst.NodeToNodeOK, dst.NodeToNodeNotApplicable = src.NodeToNodeOK, src.NodeToNodeNotApplicable
			},
			critical: func(s *ValidationState) outcome {
				if s.NodeToNodeNotApplicable != "" {
					return passed
				}
				return rowOutcome(s.NodeToNodeOK)
			},
		},
		{
			name: "Tier-1 Deployments",
			run: func(ctx context.Context, s *ValidationState) {
				checkTier1DeploymentsFor(ctx, su.client, su.gatewayView(ctx).own, s)
			},
			adopt:    func(dst, src *ValidationState) { dst.Tier1DeploymentsOK = src.Tier1DeploymentsOK },
			critical: func(s *ValidationState) outcome { return rowOutcome(s.Tier1DeploymentsOK) },
		},
		{
			name: "Tier-2 StatefulSets",
			run:  func(ctx context.Context, s *ValidationState) { checkTier2StatefulSets(ctx, su.client, s) },
			adopt: func(dst, src *ValidationState) {
				dst.Tier2StatefulSetsOK, dst.Tier2PlacementNotAssessed =
					src.Tier2StatefulSetsOK, src.Tier2PlacementNotAssessed
			},
			critical: func(s *ValidationState) outcome { return rowOutcome(s.Tier2StatefulSetsOK) },
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
			critical: func(s *ValidationState) outcome {
				return flagOutcome(s.GPUAvailable, s.Unobserved[CheckKeyGPUResources])
			},
		},
		{
			// Whether GPUs are discoverable decides how a missing GPU Operator
			// is reported.
			name:  "GPU Operator",
			reads: GPUResourcesLabel,
			run:   func(ctx context.Context, s *ValidationState) { checkGPUOperator(ctx, su.client, s) },
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
