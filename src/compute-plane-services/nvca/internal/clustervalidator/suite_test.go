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
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const recheckSummaryNS = "nvca-operator"

// runPublishing runs the validator as the chart's Job does: it publishes its
// summary and requires it. It returns the transcript and Run's error.
func runPublishing(
	ctx context.Context, t *testing.T, client kubernetes.Interface, configNS, configName string, role Role,
) (string, error) {
	t.Helper()
	t.Setenv(RequireSummaryEnv, "true")
	log, buf := bufferLog()
	err := Run(core.WithLogger(ctx, log), client, nil, configNS, configName, recheckSummaryNS, true, role)
	return buf.String(), err
}

// stubDNS replaces the DNS capability probe for one test.
func stubDNS(t *testing.T, probe func(context.Context) bool) {
	t.Helper()
	orig := probeDNSFn
	t.Cleanup(func() { probeDNSFn = orig })
	probeDNSFn = probe
}

// cpuComputeCluster passes every critical compute-plane check but GPU
// Resources: its one node has no GPU.
func cpuComputeCluster() *fake.Clientset {
	client := readyComputeCluster()
	_ = client.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("nodes"), "", "gpu-1")
	_ = client.Tracker().Add(makeNode("cpu-1", true, 0))
	return client
}

// One transient failure does not decide a published verdict: the critical
// check it failed runs once more, and the summary and exit code follow that
// second result. Before, one DNS blip published Not-Ready and failed the Job
// until the next scheduled run. The failure does not vanish with the first
// result: a warning says what the first run saw, so the verdict is Ready with
// warnings.
func TestRun_RecheckClearsATransientFailure(t *testing.T) {
	var lookups atomic.Int32
	stubDNS(t, func(context.Context) bool { return lookups.Add(1) > 1 })
	client := readyComputeCluster()

	out, err := runPublishing(context.Background(), t, client, "", "", RoleComputePlane)
	require.NoError(t, err, out)
	assert.Equal(t, 0, ExitCode(err))
	assert.Equal(t, int32(2), lookups.Load(), "the failed check ran twice")
	assert.Contains(t, out, "run once more before the verdict")
	assert.NotContains(t, out, "Fix control plane issues", "the first result's advice is replaced")

	s := readPublishedSummary(t, client, recheckSummaryNS)
	assert.True(t, s.VerdictReady)
	assert.True(t, s.Checks[CheckKeyControlPlane])
	assert.Contains(t, s.Warnings, "Control Plane: failed its first run and passed when run again, so it may "+
		"fail intermittently; the first run saw: DNS resolution: failed to resolve kubernetes.default.svc; "+
		"Some control plane components may need attention")
	assert.Contains(t, out, VerdictLinePrefix+VerdictReadyWithWarnings)
}

// A failure that holds on the recheck is published, and the Job fails on it
// without a retry. Only the critical check that failed runs twice.
func TestRun_ConfirmedNotReadyFailsTheJob(t *testing.T) {
	client := cpuComputeCluster()

	out, err := runPublishing(context.Background(), t, client, "", "", RoleComputePlane)
	var notReady *NotReadyError
	require.ErrorAs(t, err, &notReady)
	assert.Equal(t, NotReadyError{Failed: 1}, *notReady)
	assert.Equal(t, ExitNotReady, ExitCode(err))
	assert.Equal(t, 2, strings.Count(out, "  "+GPUResourcesLabel+"\x1b"), "GPU Resources ran twice")
	assert.Equal(t, 1, strings.Count(out, "  SMB CSI Driver\x1b"), "a non-critical check runs once")

	s := readPublishedSummary(t, client, recheckSummaryNS)
	assert.False(t, s.VerdictReady)
	assert.False(t, s.Checks[CheckKeyGPUResources])
}

// nodeListsOfAPass counts the node lists of one unrechecked compute-plane run
// over a cluster from newClient.
func nodeListsOfAPass(t *testing.T, newClient func() *fake.Clientset) int32 {
	t.Helper()
	client := newClient()
	var lists atomic.Int32
	client.PrependReactor("list", "nodes", func(ktesting.Action) (bool, runtime.Object, error) {
		lists.Add(1)
		return false, nil, nil
	})
	_ = Run(core.WithLogger(context.Background(), testLog()), client, nil, "", "", "", false, RoleComputePlane)
	return lists.Load()
}

// A recheck that cannot read what the first pass saw fail does not undo that
// failure: the CPU-only node's GPU Resources failure is published, and it
// still fails the startup gate. Before, the recheck's read error replaced it,
// the row became unknown and left the summary, and the gate let it through.
func TestRun_RecheckKeepsAFailureItCouldNotObserve(t *testing.T) {
	firstPass := nodeListsOfAPass(t, cpuComputeCluster)
	client := cpuComputeCluster()
	var lists atomic.Int32
	client.PrependReactor("list", "nodes", func(ktesting.Action) (bool, runtime.Object, error) {
		if lists.Add(1) > firstPass {
			return true, nil, apierrors.NewServiceUnavailable("etcd leader change")
		}
		return false, nil, nil
	})
	t.Setenv(StartupGateEnv, "true")

	out, err := runPublishing(context.Background(), t, client, "", "", RoleComputePlane)
	var notReady *NotReadyError
	require.ErrorAs(t, err, &notReady)
	assert.Equal(t, NotReadyError{Failed: 1}, *notReady)
	assert.Equal(t, ExitNotReady, ExitCode(err))
	assert.Greater(t, lists.Load(), firstPass, "the recheck tried to read the nodes")
	assert.Contains(t, out, "The recheck could not observe "+GPUResourcesLabel)

	s := readPublishedSummary(t, client, recheckSummaryNS)
	assert.False(t, s.VerdictReady)
	gpu, published := s.Checks[CheckKeyGPUResources]
	assert.True(t, published, "the failed row stays in the summary")
	assert.False(t, gpu)
}

// A recheck that can read what its first pass could not replaces the unknown
// with what it observed, a failure included.
func TestSuite_RecheckReplacesAnUnknownWithWhatItObserves(t *testing.T) {
	for name, tc := range map[string]struct {
		second func(*ValidationState)
		want   outcome
	}{
		"failure": {func(s *ValidationState) { s.GPUAvailable = false }, failed},
		"pass":    {func(s *ValidationState) { s.GPUAvailable = true }, passed},
		"unknown": {func(s *ValidationState) { s.markUnobserved(CheckKeyGPUResources) }, unobserved},
	} {
		var runs int
		c := check{
			name: GPUResourcesLabel,
			run: func(_ context.Context, s *ValidationState) {
				if runs++; runs == 1 {
					s.markUnobserved(CheckKeyGPUResources)
					return
				}
				tc.second(s)
			},
			adopt: func(dst, src *ValidationState) {
				dst.GPUAvailable = src.GPUAvailable
				adoptUnobserved(dst, src, CheckKeyGPUResources)
			},
			critical: func(s *ValidationState) outcome {
				return flagOutcome(s.GPUAvailable, s.Unobserved[CheckKeyGPUResources])
			},
		}
		state := &ValidationState{Log: testLog()}
		results := []*ValidationState{state.fork()}
		c.run(context.Background(), results[0])
		(&suite{}).recheck(context.Background(), state, []check{c}, results, [][]string{nil})
		assert.Equal(t, 2, runs, name)
		assert.Equal(t, tc.want, c.critical(results[0]), name)
	}
}

// GPUs exposed without the GPU Operator (Manual Instance Configuration) are
// not a missing GPU Operator: its row reads what GPU Resources found, so it
// gives a non-blocking warning and no install advice.
func TestRun_GPUOperatorReadsTheGPUResourcesResult(t *testing.T) {
	client := readyComputeCluster()
	out, err := runPublishing(context.Background(), t, client, "", "", RoleComputePlane)
	require.NoError(t, err, out)
	assert.Contains(t, out, "GPUs discovered via alternative mechanism")
	assert.NotContains(t, out, "helm install gpu-operator")

	s := readPublishedSummary(t, client, recheckSummaryNS)
	assert.False(t, s.Checks[CheckKeyGPUOperator])
	assert.True(t, slices.ContainsFunc(s.Warnings, func(w string) bool {
		return strings.Contains(w, "GPUs are discoverable via alternative mechanism")
	}), s.Warnings)
}

// The GPU Operator row runs again when the recheck replaces the GPU Resources
// result it read, so the GPUs the recheck found are not reported missing.
func TestRun_RecheckRerunsAChecksReaderWhenItReplacesItsResult(t *testing.T) {
	firstPass := nodeListsOfAPass(t, func() *fake.Clientset { return readyComputeCluster() })
	client := readyComputeCluster()
	var lists atomic.Int32
	client.PrependReactor("list", "nodes", func(ktesting.Action) (bool, runtime.Object, error) {
		if lists.Add(1) == firstPass {
			return true, nil, apierrors.NewForbidden(corev1.Resource("nodes"), "", errors.New("denied"))
		}
		return false, nil, nil
	})

	out, err := runPublishing(context.Background(), t, client, "", "", RoleComputePlane)
	require.NoError(t, err, out)
	assert.Contains(t, out, "helm install gpu-operator", "the first pass saw no GPUs")
	assert.Equal(t, 2, strings.Count(out, "  GPU Operator Status\x1b"), "GPU Operator ran again")

	assert.NotContains(t, out, "Install GPU Operator using the command above",
		"the first pass's install advice is dropped")

	s := readPublishedSummary(t, client, recheckSummaryNS)
	assert.True(t, s.Checks[CheckKeyGPUResources])
	assert.True(t, slices.ContainsFunc(s.Warnings, func(w string) bool {
		return strings.Contains(w, "GPUs are discoverable via alternative mechanism")
	}), s.Warnings)
}

// A preflight run is graded by its launcher, which can run it again, so it
// does not recheck.
func TestRun_PreflightDoesNotRecheck(t *testing.T) {
	log, buf := bufferLog()
	err := Run(core.WithLogger(context.Background(), log), cpuComputeCluster(), nil, "", "", "", false,
		RoleComputePlane)
	require.ErrorIs(t, err, ErrNotReady)
	assert.Equal(t, 1, strings.Count(buf.String(), "  "+GPUResourcesLabel+"\x1b"))
	assert.NotContains(t, buf.String(), "run once more")
}

// A rechecked check's first warnings and advice are dropped with its first
// result, and a warning another check already gave is not repeated.
func TestRun_RecheckReplacesOnlyTheRecheckedResult(t *testing.T) {
	client := readyComputeCluster()
	var denied atomic.Bool
	client.PrependReactor("get", "resource", func(ktesting.Action) (bool, runtime.Object, error) {
		if denied.CompareAndSwap(false, true) {
			return true, nil, apierrors.NewForbidden(corev1.Resource("resource"), "", errors.New("denied"))
		}
		return false, nil, nil
	})

	out, err := runPublishing(context.Background(), t, client, "", "", RoleComputePlane)
	require.NoError(t, err, out)
	assert.Contains(t, out, "could not read the admissionregistration.k8s.io/v1 API", "the first pass could not")

	s := readPublishedSummary(t, client, recheckSummaryNS)
	assert.True(t, s.Checks[CheckKeyWebhooks])
	for _, w := range s.Warnings {
		assert.NotContains(t, w, "Admission Webhooks")
	}
	assert.NotEmpty(t, s.Warnings, "the other checks' warnings are kept")
	seen := map[string]bool{}
	for _, w := range s.Warnings {
		assert.False(t, seen[w], "repeated warning %q", w)
		seen[w] = true
	}
}

// A check run again does not repeat a warning another check gave on the first
// pass: the Tier-1 recheck reads the NVCF Gateways afresh and reports ignored
// gateway-name entries again, which the LoadBalancer check reported already.
func TestRun_RecheckDoesNotRepeatAWarning(t *testing.T) {
	t.Setenv(PostInstallEnv, "true")
	t.Setenv(nvcfGatewayNamesEnv, "no-namespace")
	client := gatewayDiscoveryClient(gatewayRequiredPairs()...)
	require.NoError(t, client.Tracker().Add(makeNode("node-1", true, 0)))

	out, err := runPublishing(context.Background(), t, client, "", "", RoleControlPlane)
	require.ErrorIs(t, err, ErrNotReady)
	require.Equal(t, 2, strings.Count(out, "  Tier-1 Deployment Readiness\x1b"), "Tier-1 is rechecked")

	var ignored int
	for _, w := range readPublishedSummary(t, client, recheckSummaryNS).Warnings {
		if strings.Contains(w, "ignoring "+gatewayNamesSetting) {
			ignored++
		}
	}
	assert.Equal(t, 1, ignored)
}

// The second ServerVersion, which the Control Plane row is judged on, is
// retried like any read, so one 503 does not fail the row.
func TestCheckControlPlaneHealth_RetriesServerVersion(t *testing.T) {
	client := fake.NewSimpleClientset(makeNode("node-1", true, 0))
	var calls atomic.Int32
	client.PrependReactor("get", "version", func(ktesting.Action) (bool, runtime.Object, error) {
		if calls.Add(1) == 1 {
			return true, nil, apierrors.NewServiceUnavailable("apiserver restarting")
		}
		return false, nil, nil
	})
	state := &ValidationState{Log: testLog(), ControlPlaneHealthy: true, NodesAllReady: true}
	checkControlPlaneHealth(context.Background(), client, state)
	assert.True(t, state.ControlPlaneHealthy)
	assert.Equal(t, int32(2), calls.Load())
}

// The checks a network-checks ConfigMap drives run on the recheck when only
// the recheck could load it.
func TestRun_RecheckRunsTheChecksOfAConfigMapItLoaded(t *testing.T) {
	cfg := inventoryNetworkChecks(false)
	client := readyComputeCluster(cfg)
	var denied atomic.Bool
	client.PrependReactor("get", "configmaps", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.(ktesting.GetAction).GetName() == cfg.Name && denied.CompareAndSwap(false, true) {
			return true, nil, apierrors.NewForbidden(corev1.Resource("configmaps"), cfg.Name, errors.New("denied"))
		}
		return false, nil, nil
	})

	out, err := runPublishing(context.Background(), t, client, cfg.Namespace, cfg.Name, RoleComputePlane)
	require.NoError(t, err, out)
	assert.Equal(t, 1, strings.Count(out, "  Configurable Network Policy Validation\x1b"))

	s := readPublishedSummary(t, client, recheckSummaryNS)
	assert.Contains(t, s.Checks, CheckKeyConfigurableNetpol)
	assert.Contains(t, s.NetpolPairs, "app-to-db")
}

// A check the run's deadline cuts short on the recheck reports the deadline,
// not the cluster: its first result stands, and the summary is published.
func TestRun_RecheckCutShortKeepsTheFirstResult(t *testing.T) {
	var lookups atomic.Int32
	stubDNS(t, func(ctx context.Context) bool {
		if lookups.Add(1) == 1 {
			return false
		}
		<-ctx.Done()
		return true
	})
	client := readyComputeCluster()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	out, err := runPublishing(ctx, t, client, "", "", RoleComputePlane)
	require.ErrorIs(t, err, ErrNotReady, out)
	assert.Equal(t, ExitNotReady, ExitCode(err))
	assert.Contains(t, out, "The run ended during the recheck of Control Plane")

	s := readPublishedSummary(t, client, recheckSummaryNS)
	assert.False(t, s.VerdictReady)
	assert.False(t, s.Checks[CheckKeyControlPlane])
}

// gpuCheck is a critical check whose runs come out as runs says, in turn,
// logging an error line for each failure.
func gpuCheck(runs ...outcome) check {
	n := 0
	return check{
		name: GPUResourcesLabel,
		run: func(_ context.Context, s *ValidationState) {
			switch runs[min(n, len(runs)-1)] {
			case passed:
				s.GPUAvailable = true
			case failed:
				printError(s.Log, fmt.Sprintf("no GPU on run %d", n+1))
			case unobserved:
				s.markUnobserved(CheckKeyGPUResources)
			}
			n++
		},
		adopt: func(dst, src *ValidationState) {
			dst.GPUAvailable = src.GPUAvailable
			adoptUnobserved(dst, src, CheckKeyGPUResources)
		},
		critical: func(s *ValidationState) outcome {
			return flagOutcome(s.GPUAvailable, s.Unobserved[CheckKeyGPUResources])
		},
	}
}

// A critical check that fails its first run and passes its second is recorded
// as passed, with a warning naming what the first run saw. One whose first run
// observed nothing, or that failed twice, adds no such warning.
func TestSuite_RecheckThatClearsAFailureWarns(t *testing.T) {
	for name, tc := range map[string]struct {
		runs []outcome
		want string
	}{
		"failed, then passed": {[]outcome{failed, passed}, GPUResourcesLabel + ": failed its first run and " +
			"passed when run again, so it may fail intermittently; the first run saw: no GPU on run 1"},
		"unobserved, then passed": {runs: []outcome{unobserved, passed}},
		"failed twice":            {runs: []outcome{failed, failed}},
	} {
		state := &ValidationState{Log: testLog()}
		(&suite{}).runChecks(context.Background(), state, []check{gpuCheck(tc.runs...)}, true)
		if tc.want == "" {
			assert.Empty(t, state.Warnings, name)
			continue
		}
		assert.True(t, state.GPUAvailable, name)
		assert.Equal(t, []string{tc.want}, state.Warnings, name)
	}
}

// Every critical row that keeps the verdict from Ready belongs to exactly one
// check that the recheck runs again, and no check runs again for a run that is
// Ready, however many non-critical rows did not pass.
func TestSuite_EveryBlockingRowIsRechecked(t *testing.T) {
	su := &suite{}
	blocking := func(s *ValidationState) []string {
		var names []string
		for _, c := range su.checks(s.Role) {
			if c.blocks(s) {
				names = append(names, c.name)
			}
		}
		return names
	}
	compute := func() *ValidationState {
		return &ValidationState{
			Log: testLog(), Role: RoleComputePlane, ControlPlaneHealthy: true, NodesAllReady: true,
			WebhooksSupported: true, NetworkPoliciesSupported: true, SMBCSIDriverOK: true, GPUAvailable: true,
			GPUOperatorInstalled: true,
		}
	}
	controlPlane := healthyControlPlaneState
	for name, tc := range map[string]struct {
		state  func() *ValidationState
		mutate func(*ValidationState)
		check  string
	}{
		"control plane failed": {compute, func(s *ValidationState) { s.ControlPlaneHealthy = false }, "Control Plane"},
		"control plane unobserved": {controlPlane, func(s *ValidationState) {
			s.markUnobserved(CheckKeyControlPlane)
		}, "Control Plane"},
		"webhooks failed": {compute, func(s *ValidationState) { s.WebhooksSupported = false }, "Admission Webhooks"},
		"webhooks unobserved": {controlPlane, func(s *ValidationState) {
			s.markUnobserved(CheckKeyWebhooks)
		}, "Admission Webhooks"},
		"network checks unloadable": {compute, func(s *ValidationState) { s.NetworkChecksErr = "denied" },
			"Network Checks"},
		"critical endpoint unreachable": {controlPlane, func(s *ValidationState) {
			s.ReachabilityOK, s.ReachabilityCriticalOK = cpRow(false), cpRow(false)
		}, "Endpoint Reachability"},
		"storage class failed": {controlPlane, func(s *ValidationState) { s.DefaultStorageClassOK = cpRow(false) },
			"Default StorageClass"},
		"storage class unobserved": {controlPlane, func(s *ValidationState) { s.DefaultStorageClassOK = nil },
			"Default StorageClass"},
		"CRDs failed": {controlPlane, func(s *ValidationState) { s.GatewayAPICRDsOK = cpRow(false) },
			"Gateway API CRDs"},
		"CRDs unobserved": {controlPlane, func(s *ValidationState) { s.GatewayAPICRDsOK = nil }, "Gateway API CRDs"},
		"overlay failed": {controlPlane, func(s *ValidationState) { s.NodeToNodeOK = cpRow(false) },
			"Node-to-Node Communication"},
		"overlay unobserved": {controlPlane, func(s *ValidationState) { s.NodeToNodeOK = nil }, "Node-to-Node Communication"},
		"Tier-1 failed": {controlPlane, func(s *ValidationState) { s.Tier1DeploymentsOK = cpRow(false) },
			"Tier-1 Deployments"},
		"Tier-1 unobserved": {controlPlane, func(s *ValidationState) { s.Tier1DeploymentsOK = nil }, "Tier-1 Deployments"},
		"Tier-2 failed": {controlPlane, func(s *ValidationState) { s.Tier2StatefulSetsOK = cpRow(false) },
			"Tier-2 StatefulSets"},
		"Tier-2 unobserved": {controlPlane, func(s *ValidationState) { s.Tier2StatefulSetsOK = nil }, "Tier-2 StatefulSets"},
		"GPU failed":        {compute, func(s *ValidationState) { s.GPUAvailable = false }, GPUResourcesLabel},
		"GPU unobserved": {compute, func(s *ValidationState) {
			s.markUnobserved(CheckKeyGPUResources)
		}, GPUResourcesLabel},
		"critical pair failed": {compute, func(s *ValidationState) {
			s.ConfigurableNetPolOK, s.ConfigurableNetPolCriticalOK = cpRow(false), cpRow(false)
		}, "Configurable Network Policies"},
		"critical enforcement failed": {controlPlane, func(s *ValidationState) {
			s.EnforcementCritical, s.EnforcementOK = true, cpRow(false)
		}, "Network Policy Enforcement"},
		"critical enforcement unobserved": {compute, func(s *ValidationState) { s.EnforcementCritical = true },
			"Network Policy Enforcement"},
	} {
		s := tc.state()
		require.Empty(t, blocking(s), name)
		tc.mutate(s)
		require.Error(t, printSummary(s), name)
		assert.Equal(t, []string{tc.check}, blocking(s), name)
	}

	for name, tc := range map[string]struct {
		state  func() *ValidationState
		mutate func(*ValidationState)
	}{
		"Envoy, routes and LoadBalancer": {controlPlane, func(s *ValidationState) {
			s.EnvoyGatewayOK, s.GatewayRoutesOK, s.ExternalLBOK = cpRow(false), nil, cpRow(false)
		}},
		"overlay not applicable": {controlPlane, func(s *ValidationState) {
			s.NodeToNodeOK, s.NodeToNodeNotApplicable = nil, "one eligible node"
		}},
		"NotReady worker and unconfirmed policies": {compute, func(s *ValidationState) {
			s.NodesAllReady, s.NotReadyNodes, s.NetworkPoliciesSupported = false, 1, false
		}},
		"SMB and GPU operator": {compute, func(s *ValidationState) {
			s.SMBCSIDriverOK, s.GPUOperatorInstalled = false, false
		}},
		"non-critical endpoint, pair and enforcement": {compute, func(s *ValidationState) {
			s.ReachabilityOK, s.ConfigurableNetPolOK, s.EnforcementOK = cpRow(false), cpRow(false), cpRow(false)
		}},
	} {
		s := tc.state()
		tc.mutate(s)
		require.NoError(t, printSummary(s), name)
		assert.Empty(t, blocking(s), name)
	}
}

// A forked state carries the settings and cluster facts every check reads,
// and none of the results.
func TestValidationState_ForkCarriesNoResults(t *testing.T) {
	s := &ValidationState{
		Log: testLog(), Role: RoleControlPlane, PostInstall: true, K8sVersion: "v1.30.0",
		ControlPlaneHealthy: true, NodesAllReady: true,
		Warnings: []string{"w"}, Recommendations: []string{"r"}, Unobserved: map[string]bool{"k": true},
	}
	f := s.fork()
	assert.Equal(t, RoleControlPlane, f.Role)
	assert.True(t, f.PostInstall)
	assert.Equal(t, "v1.30.0", f.K8sVersion)
	assert.True(t, f.ControlPlaneHealthy)
	assert.Empty(t, f.Warnings)
	assert.Empty(t, f.Recommendations)
	assert.Empty(t, f.Unobserved)
	f.markUnobserved("other")
	assert.NotContains(t, s.Unobserved, "other")
}
