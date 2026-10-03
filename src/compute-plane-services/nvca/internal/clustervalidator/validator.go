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
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/sirupsen/logrus"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// RoleMarker prefixes the startup line naming the check set this run executes,
// for example "Validator role: control-plane". Launchers match on it, so the
// text is part of the interface and must not change.
const RoleMarker = "Validator role: "

// The strings below are the rest of what launchers grade a transcript on.
// Like RoleMarker they are part of the interface: nvcf-cli matches each one,
// so changing any of them changes how it reads every run.
const (
	// SummaryStart opens the summary rows.
	SummaryStart = "Check Results:"
	// VerdictLinePrefix starts each verdict banner, which ends the summary rows.
	VerdictLinePrefix = "Cluster is "
	// VerdictReady, VerdictReadyWithWarnings and VerdictNotReady follow
	// VerdictLinePrefix. VerdictReadyWithWarnings is a pass with warnings.
	VerdictReady             = "NVCF-Ready"
	VerdictReadyWithWarnings = VerdictReady + " (with warnings)"
	VerdictNotReady          = "NVCF-Not-Ready"
	// FailIcon starts a failed summary row.
	FailIcon = "\u2717"
	// GPUResourcesLabel names the GPU section and its summary row, which only
	// the compute-plane check set prints.
	GPUResourcesLabel = "GPU Resources"
	// The rollout markers appear in the Tier-1 and Tier-2 warnings for a
	// rollout in progress and nowhere else, so a launcher can wait one out.
	RolloutInProgressMarker = "rollout in progress"
	RollingUpdateMarker     = "rolling update in progress"
	MidRolloutMarker        = "mid-rollout"
)

// effectiveRole is the check set Run actually executes for role.
func effectiveRole(role Role) Role {
	if role == RoleControlPlane {
		return RoleControlPlane
	}
	return RoleComputePlane
}

// Role is the check set selected by VALIDATOR_ROLE.
type Role string

// Role values for VALIDATOR_ROLE.
const (
	RoleComputePlane Role = "compute-plane"
	RoleControlPlane Role = "control-plane"
)

// ValidationState captures the results of every validation check.
type ValidationState struct {
	Log *logrus.Entry
	// PostInstall is set when the launcher asserts the control plane is
	// already installed (VALIDATOR_POST_INSTALL), so an empty control plane is
	// a failure rather than a pre-install state. It is separate from
	// VALIDATOR_PREFLIGHT, which only suppresses the summary write and is set
	// by post-install CLI runs too.
	PostInstall bool
	// NodeToNodeNotApplicable holds the reason the overlay probe could not
	// apply (for example a single schedulable node). Non-empty means the check
	// is reported as Not Applicable rather than Verified or Unknown, and is
	// left out of the summary map so it is neither alerted on nor counted as
	// a pass.
	NodeToNodeNotApplicable string

	// Role is "control-plane" or "compute-plane" (empty = compute-plane default).
	// printSummary uses it to include only the checks relevant to the role.
	Role                Role
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
	// Unobserved holds the summary keys of the always-run checks above whose
	// reads failed. Their bool is then no result: the key is left out of the
	// summary and the row is shown as unknown.
	Unobserved map[string]bool
	// NetworkChecksErr is why the network-checks ConfigMap could not be
	// loaded. Any check it configures may be critical, so a load failure is a
	// critical unknown rather than an absent config.
	NetworkChecksErr string

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
	// EnvoyGatewayNotApplicable and ExternalLBNotApplicable hold the reason
	// those rows do not apply: every NVCF Gateway is run by an implementation
	// other than Envoy Gateway. Their pointers stay nil, as for the overlay.
	EnvoyGatewayNotApplicable string
	ExternalLBNotApplicable   string
	// NodeToNodeOK is nil when the overlay was not observed, when the check
	// does not apply (see NodeToNodeNotApplicable), or under the compute-plane
	// role. true = overlay verified, false = failed.
	NodeToNodeOK *bool
	// Tier1DeploymentsOK is nil when the check did not run (compute-plane role)
	// or something could not be observed. Finding no NVCF Deployment outside
	// the shared namespaces is false when PostInstall is set; finding nothing
	// is true otherwise.
	Tier1DeploymentsOK *bool
	// Tier2StatefulSetsOK is nil when the check did not run (compute-plane role),
	// when a StatefulSet list call fails, or when, after install, a stack quorum
	// component that is not declared external was not found. No quorum
	// StatefulSet found before install sets this to true, not nil.
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

// markUnobserved records that the always-run check behind key could not read
// what it checks, so its zero value is not reported as a failure.
func (s *ValidationState) markUnobserved(key string) {
	if s.Unobserved == nil {
		s.Unobserved = map[string]bool{}
	}
	s.Unobserved[key] = true
}

// annotationTrue is the string form of true in annotations and env values.
const annotationTrue = "true"

// PostInstallEnv tells the validator the control plane is already installed.
const PostInstallEnv = "VALIDATOR_POST_INSTALL"

// postInstallMode parses PostInstallEnv. Unset or unrecognized keeps the
// lenient pre-install reading.
func postInstallMode(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case annotationTrue, "1", "yes":
		return true
	default:
		return false
	}
}

// envTrue reports whether the named env var holds a true value.
func envTrue(name string) bool { return postInstallMode(os.Getenv(name)) }

// RequireSummaryEnv marks a run whose output is its summary ConfigMap: the
// chart's validator Job. A run that could not write the summary returns
// ErrSummaryNotPublished, so the Job retries it instead of completing.
const RequireSummaryEnv = "VALIDATOR_REQUIRE_SUMMARY"

// StartupGateEnv marks a run that gates a workload's startup: the operator's
// init container. Only a critical check that ran and failed fails it. A
// critical check that could not run still makes the verdict and the summary
// Not-Ready, but must not keep the operator from starting.
const StartupGateEnv = "VALIDATOR_STARTUP_GATE"

// ExitNotReady is the exit code of a RequireSummaryEnv run that published a
// Not-Ready verdict. The chart's podFailurePolicy fails the Job on it rather
// than rerunning a suite whose result would not change. Other runs keep exit
// code 1 for Not-Ready, which launchers grade on.
const ExitNotReady = 3

// ErrNotReady is the error Run returns, as a *NotReadyError, when the cluster
// is NVCF-Not-Ready.
var ErrNotReady = errors.New("cluster is NVCF-Not-Ready")

// ErrSummaryNotPublished is the error Run returns when RequireSummaryEnv is
// set and the summary could not be written.
var ErrSummaryNotPublished = errors.New("the validation summary was not published")

// NotReadyError is an NVCF-Not-Ready verdict. Failed counts the critical
// checks that ran and failed; Unobserved counts those that could not run.
type NotReadyError struct {
	Failed, Unobserved int
}

func (e *NotReadyError) Error() string { return ErrNotReady.Error() }

// Is makes errors.Is(err, ErrNotReady) hold.
func (e *NotReadyError) Is(target error) bool { return target == ErrNotReady }

// ExitCode maps Run's result to the process exit code, per RequireSummaryEnv
// and StartupGateEnv.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var notReady *NotReadyError
	if !errors.As(err, &notReady) {
		return 1
	}
	switch {
	case envTrue(StartupGateEnv) && notReady.Failed == 0:
		return 0
	case envTrue(RequireSummaryEnv):
		return ExitNotReady
	default:
		return 1
	}
}

// Run executes all cluster validation checks and returns a non-nil error when
// the cluster is not ready. role selects the check set; configNamespace/configName
// identify the optional ConfigMap; emitMetrics gates the summary write. routes
// lists Gateway API routes to identify the NVCF Gateways; nil leaves that
// ownership undetermined.
func Run(
	ctx context.Context,
	client kubernetes.Interface,
	routes dynamic.Interface,
	configNamespace, configName, summaryNamespace string,
	emitMetrics bool,
	role Role,
) error {
	startedAt := time.Now()
	log := core.GetLogger(ctx)
	log.Info("Starting NVCF cluster validation")
	// A fixed, parseable line. Launchers that ask for a role look for it: an
	// image that predates role support never prints it, so they can report
	// "too old for this role" instead of failing on the checks it ran instead.
	log.Info(RoleMarker + string(effectiveRole(role)))
	log.Info("")
	log.Infof("%s╔═══════════════════════════════════════════════════════════╗%s", colorBlue, colorReset)
	log.Infof("%s║     NVIDIA Cloud BYOC Cluster Readiness Check             ║%s", colorBlue, colorReset)
	log.Infof("%s║         Kubernetes Cluster Validation                     ║%s", colorBlue, colorReset)
	log.Infof("%s╚═══════════════════════════════════════════════════════════╝%s", colorBlue, colorReset)

	state := &ValidationState{
		Log:                 log,
		Role:                role,
		ControlPlaneHealthy: true,
		PostInstall:         postInstallMode(os.Getenv(PostInstallEnv)),
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
	// Only the control-plane role runs the node-to-node probe, but either
	// role's validator may run on a cluster, so every run reclaims the probe
	// namespaces a killed probe left behind. Legacy probe DaemonSets were only
	// ever created by the control-plane check set, and only its role is
	// granted DaemonSets, so only it sweeps them.
	sweepOrphanN2NNamespaces(ctx, log, client, orphanN2NNamespaceTTL)
	if role == RoleControlPlane {
		sweepLegacyOrphanN2NDaemonSets(ctx, log, client, orphanN2NNamespaceTTL)
	}

	checkControlPlaneHealth(ctx, client, state)
	checkWebhookSupport(ctx, client, state)
	checkNetworkPolicies(ctx, client, state)

	var netCfg *NetworkCheckConfig
	if configNamespace != "" && configName != "" {
		cfg, err := LoadNetworkCheckConfig(ctx, client, configNamespace, configName)
		if err != nil {
			// The checks it would configure may be critical, so they cannot
			// simply be skipped: say so, and leave the verdict unknown.
			state.NetworkChecksErr = fmt.Sprintf("could not load %s/%s: %v", configNamespace, configName, err)
			printWarning(log, "Network checks: "+state.NetworkChecksErr)
			state.Warnings = append(state.Warnings, "Network checks: "+state.NetworkChecksErr+
				"; its endpoint, NetworkPolicy and enforcement checks did not run")
		} else {
			netCfg = cfg
		}
	}

	if netCfg != nil && netCfg.Reachability != nil && len(netCfg.Reachability.Endpoints) > 0 {
		checkConfigurableReachability(ctx, state, netCfg.Reachability)
	}

	if role == RoleControlPlane {
		// Control-plane cluster: check gateway infrastructure, storage, and
		// inter-node overlay connectivity. GPU operator and SMB CSI are
		// compute-plane concerns and are skipped.
		checkStorageClass(ctx, client, state)
		// Discover the Gateway API surface and decide which Gateways are
		// NVCF's once, so the Envoy, LoadBalancer and Tier-1 rows judge the
		// same set.
		surface, surfaceErr := discoverGatewayAPIResources(ctx, client)
		checkGatewayAPICRDsIn(state, surface, surfaceErr)
		ownership := resolveGatewayOwnershipIn(ctx, client, surface, surfaceErr, routes)
		checkEnvoyGatewayFor(ctx, client, ownership, state)
		checkGatewayRoutesIn(state, surface, surfaceErr)
		checkExternalLoadBalancerFor(ctx, client, ownership, state)
		checkNodeToNode(ctx, client, state, nodeToNodeProbeImage(netCfg))
		checkTier1DeploymentsFor(ctx, client, ownership, state)
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
	if err := publishSummary(ctx, client, state, startedAt, summaryErr, summaryNamespace, emitMetrics); err != nil {
		if envTrue(RequireSummaryEnv) {
			return fmt.Errorf("%w: %w", ErrSummaryNotPublished, err)
		}
		log.WithError(err).Warn("cluster-validator: the summary was not written; metrics will be stale")
	}
	return summaryErr
}

// publishSummary writes the summary to summaryNamespace, the agent's watch
// namespace, for the agent to publish as metrics. Preflight runs
// (emitMetrics=false) write nothing. The write gets its own budget: a run
// that spent its deadline on the checks must still publish what they found.
func publishSummary(
	ctx context.Context, client kubernetes.Interface, state *ValidationState, startedAt time.Time,
	summaryErr error, summaryNamespace string, emitMetrics bool,
) error {
	if !emitMetrics {
		return nil
	}
	if summaryNamespace == "" {
		return errors.New("no summary namespace")
	}
	verdict := VerdictReady
	if summaryErr != nil {
		verdict = VerdictNotReady
	}
	return writeSummaryConfigMap(context.WithoutCancel(ctx), client, summaryNamespace,
		buildSummary(state, startedAt, summaryErr == nil, verdict))
}

// printSummary outputs the final validation results and returns an error if
// the cluster is not ready.
func printSummary(state *ValidationState) error {
	log := state.Log
	printHeader(log, "Validation Summary")

	isReady := true

	log.Info(SummaryStart)

	type check struct {
		Passed   bool
		PassMsg  string
		FailMsg  string
		Critical bool
		// Unknown marks a check that did not run. Rendered as its own row so a
		// critical check cannot silently vanish from the verdict, which would
		// otherwise make a throttled API call look better than a clean run.
		Unknown    bool
		UnknownMsg string
		// NotApplicable marks a check this cluster's shape cannot exercise, as
		// distinct from one we failed to observe. Both are non-passes, but
		// only Unknown means something is hidden, so only Unknown blocks the
		// verdict. Reporting a not-applicable check as Passed would claim a
		// result the run never produced.
		NotApplicable bool
		NAMsg         string
	}

	// A failed node list marks the row unobserved rather than NotReady, so a
	// failed row always has a NotReady count.
	nodesFailMsg := fmt.Sprintf("Worker Nodes: %d NotReady (non-blocking)", state.NotReadyNodes)

	// unobservedRow renders an always-run check whose read failed as unknown,
	// and any other as it ran.
	unobservedRow := func(key, label string, c check) check {
		if state.Unobserved[key] {
			return check{Critical: c.Critical, Unknown: true, UnknownMsg: label + ": Status Unknown (not observed)"}
		}
		return c
	}

	checks := []check{
		unobservedRow(CheckKeyControlPlane, "Control Plane", check{Passed: state.ControlPlaneHealthy,
			PassMsg: "Control Plane: Healthy", FailMsg: "Control Plane: Unhealthy", Critical: true}),
		unobservedRow(CheckKeyWorkerNodesAllReady, "Worker Nodes", check{Passed: state.NodesAllReady,
			PassMsg: "Worker Nodes: All Ready", FailMsg: nodesFailMsg, Critical: false}),
		unobservedRow(CheckKeyWebhooks, "Admission Webhooks", check{Passed: state.WebhooksSupported,
			PassMsg: "Admission Webhooks: Mutating & Validating Supported",
			FailMsg: "Admission Webhooks: Not Supported", Critical: true}),
		unobservedRow(CheckKeyNetworkPoliciesSupport, "Network Policies", check{
			Passed: state.NetworkPoliciesSupported, PassMsg: "Network Policies: Supported",
			FailMsg: "Network Policies: Not Confirmed", Critical: false}),
	}
	if state.NetworkChecksErr != "" {
		checks = append(checks, check{Critical: true, Unknown: true,
			UnknownMsg: "Network Checks: Status Unknown (the network-checks ConfigMap could not be loaded)"})
	}

	if state.ReachabilityOK != nil {
		isCritical := state.ReachabilityCriticalOK != nil &&
			!*state.ReachabilityCriticalOK
		checks = append(checks, check{
			Passed:   *state.ReachabilityOK,
			PassMsg:  "Endpoint Reachability: All Endpoints Reachable",
			FailMsg:  "Endpoint Reachability: One or more endpoints not reachable",
			Critical: isCritical,
		})
	}

	if state.Role == RoleControlPlane {
		// Control-plane checks: gateway infrastructure and storage. GPU and
		// SMB checks are compute-plane concerns and are excluded here.
		//
		// addCP renders a nil pointer as an explicit UNKNOWN row for critical
		// checks, so an API error during the run cannot quietly drop a critical
		// row and leave a cleaner-looking summary than a successful run.
		addCP := func(ptr *bool, label, passDetail, failDetail string, critical bool) {
			if ptr != nil {
				checks = append(checks, check{
					Passed:   *ptr,
					PassMsg:  label + ": " + passDetail,
					FailMsg:  label + ": " + failDetail,
					Critical: critical,
				})
				return
			}
			if critical {
				checks = append(checks, check{
					Critical:   critical,
					Unknown:    true,
					UnknownMsg: label + ": Status Unknown (check did not run)",
				})
			}
		}

		// addOrNA renders a check this cluster's shape makes moot as Not
		// Applicable: non-blocking, but not a pass, since nothing was judged.
		addOrNA := func(na string, ptr *bool, label, passDetail, failDetail string, critical bool) {
			if na == "" {
				addCP(ptr, label, passDetail, failDetail, critical)
				return
			}
			checks = append(checks, check{NotApplicable: true, NAMsg: label + ": Not Applicable (" + na + ")"})
		}

		addCP(state.DefaultStorageClassOK, "Default StorageClass", "Present", "Not Found", true)
		addCP(state.GatewayAPICRDsOK, "Gateway API CRDs", "Installed", "Not Installed", true)
		// Non-critical: Envoy Gateway is installed by nvcf-cli up, so it is
		// expected to be absent on a fresh cluster before the first install.
		// A missing Envoy is informative (tells the operator the stack is not
		// yet deployed) but must not block a pre-install readiness check.
		addOrNA(state.EnvoyGatewayNotApplicable, state.EnvoyGatewayOK, "Envoy Gateway",
			"Installed and Running", "Not Found or Not Running", false)
		addCP(state.GatewayRoutesOK, "Gateway Route CR Types", "Registered", "Not Registered", false)
		addOrNA(state.ExternalLBNotApplicable, state.ExternalLBOK, "External Load Balancer",
			"IP Assigned", "No IP Assigned", false)
		// Non-blocking, but not "Verified": no cross-node packet was sent.
		addOrNA(state.NodeToNodeNotApplicable, state.NodeToNodeOK, "Node-to-Node Communication",
			"Verified", "Failed", true)
		addCP(state.Tier1DeploymentsOK, "Tier-1 Deployments", "All Ready", "Under-replicated", true)
		addCP(state.Tier2StatefulSetsOK, "Tier-2 StatefulSets",
			"Quorum and Placement OK", "Quorum or Placement Failed", true)
	} else {
		// Compute-plane checks: GPU resources, GPU operator, SMB CSI driver.
		// SMB CSI Driver missing is non-blocking: it is required only when
		// the HelmSharedStorage feature flag is enabled (NVCA model-cache).
		checks = append(checks,
			unobservedRow(CheckKeySMBCSI, "SMB CSI Driver", check{Passed: state.SMBCSIDriverOK,
				PassMsg: "SMB CSI Driver: v1.16.0+ Installed",
				FailMsg: "SMB CSI Driver: Not Installed or Below v1.16.0", Critical: false}),
			unobservedRow(CheckKeyGPUResources, GPUResourcesLabel, check{Passed: state.GPUAvailable,
				PassMsg: GPUResourcesLabel + ": Available", FailMsg: GPUResourcesLabel + ": Not Available",
				Critical: true}),
			// GPU Operator missing is non-blocking: clusters registered with
			// Manual Instance Configuration expose GPUs via an alternative
			// mechanism (pre-labeled nodes, DaemonSet, etc.) and do not require
			// GPU Operator. GPU Resources above is the load-bearing signal.
			unobservedRow(CheckKeyGPUOperator, "GPU Operator", check{Passed: state.GPUOperatorInstalled,
				PassMsg: "GPU Operator: Installed", FailMsg: "GPU Operator: Not Installed", Critical: false}),
		)
	}

	if state.ConfigurableNetPolOK != nil {
		isCritical := state.ConfigurableNetPolCriticalOK != nil &&
			!*state.ConfigurableNetPolCriticalOK
		checks = append(checks, check{
			Passed:   *state.ConfigurableNetPolOK,
			PassMsg:  "Configurable Network Policies: All Checks Passed",
			FailMsg:  "Configurable Network Policies: One or more checks failed",
			Critical: isCritical,
		})
	}
	switch {
	case state.EnforcementOK != nil:
		checks = append(checks, check{
			Passed:   *state.EnforcementOK,
			PassMsg:  "Network Policy Enforcement: Active Validation Passed",
			FailMsg:  "Network Policy Enforcement: Active Validation Failed",
			Critical: state.EnforcementCritical,
		})
	case state.EnforcementCritical:
		// The check was configured as critical but produced no result: its
		// setup failed, or an API error cut it short. Dropping the row would
		// certify a precondition nothing looked at.
		checks = append(checks, check{
			Critical:   true,
			Unknown:    true,
			UnknownMsg: "Network Policy Enforcement: Status Unknown (check did not complete)",
		})
	}

	var unknownCritical []string
	failedCritical := 0
	for _, c := range checks {
		switch {
		case c.NotApplicable:
			// Neither pass nor failure: the cluster shape made the check moot.
			printInfo(log, fmt.Sprintf("  %s", c.NAMsg))
		case c.Unknown:
			// A critical check we could not observe cannot be certified as
			// ready. Logging it while still publishing verdict=NVCF-Ready and
			// VerdictReady=true would export a perfect green SLI for a
			// precondition nothing looked at, and the check key is pruned from
			// the metric, so there is no series left to alert on either.
			printWarning(log, fmt.Sprintf("  %s", c.UnknownMsg))
			if c.Critical {
				unknownCritical = append(unknownCritical, c.UnknownMsg)
				isReady = false
			}
		case c.Passed:
			printSuccess(log, fmt.Sprintf("  %s", c.PassMsg))
		case c.Critical:
			printError(log, fmt.Sprintf("  %s", c.FailMsg))
			failedCritical++
			isReady = false
		default:
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
			log.Infof("%s\u2551        %s  %s%s  %s        \u2551%s",
				colorYellow, iconWarn, VerdictLinePrefix, VerdictReadyWithWarnings, iconWarn, colorReset)
			log.Infof("%s╚═══════════════════════════════════════════════════════════╝%s", colorYellow, colorReset)
			log.Info("")
			printWarning(log, "Your cluster meets all critical requirements; see warnings below for non-blocking issues.")
		} else {
			log.Infof("%s╔═══════════════════════════════════════════════════════════╗%s", colorGreen, colorReset)
			log.Infof("%s\u2551                %s  %s%s  %s                \u2551%s",
				colorGreen, iconCheck, VerdictLinePrefix, VerdictReady, iconCheck, colorReset)
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
		log.Infof("%s\u2551              %s  %s%s  %s              \u2551%s",
			colorRed, iconCross, VerdictLinePrefix, VerdictNotReady, iconCross, colorReset)
		log.Infof("%s╚═══════════════════════════════════════════════════════════╝%s", colorRed, colorReset)
		log.Info("")
		if len(unknownCritical) > 0 {
			// Distinguish "could not check" from "checked and broken": the
			// operator's next step is to fix access or re-run, not to go
			// looking for a fault that was never observed.
			printError(log, fmt.Sprintf(
				"%d critical check(s) could not be observed, so readiness cannot be confirmed", len(unknownCritical)))
			for _, m := range unknownCritical {
				printInfo(log, "  "+m)
			}
		}
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
		return &NotReadyError{Failed: failedCritical, Unobserved: len(unknownCritical)}
	}
	return nil
}
