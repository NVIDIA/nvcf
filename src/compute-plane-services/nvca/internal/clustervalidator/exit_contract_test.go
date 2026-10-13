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

package clustervalidator

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func cpRow(b bool) *bool { return &b }

// healthyControlPlaneState passes every control-plane row.
func healthyControlPlaneState() *ValidationState {
	return &ValidationState{
		Log: testLog(), Role: RoleControlPlane,
		ControlPlaneHealthy: true, NodesAllReady: true, WebhooksSupported: true, NetworkPoliciesSupported: true,
		DefaultStorageClassOK: cpRow(true), GatewayAPICRDsOK: cpRow(true), EnvoyGatewayOK: cpRow(true),
		GatewayRoutesOK: cpRow(true), ExternalLBOK: cpRow(true), NodeToNodeOK: cpRow(true),
		Tier1DeploymentsOK: cpRow(true), Tier2StatefulSetsOK: cpRow(true),
	}
}

// Each critical control-plane row blocks the verdict both when it failed and
// when it could not be observed, and the error says which; a non-critical row
// that failed does not block it.
func TestPrintSummary_EveryCriticalRowBlocksTheVerdict(t *testing.T) {
	require.NoError(t, printSummary(healthyControlPlaneState()))

	critical := map[string]func(*ValidationState) **bool{
		"Default StorageClass": func(s *ValidationState) **bool { return &s.DefaultStorageClassOK },
		"Gateway API CRDs":     func(s *ValidationState) **bool { return &s.GatewayAPICRDsOK },
		"Node-to-Node":         func(s *ValidationState) **bool { return &s.NodeToNodeOK },
		"Tier-1":               func(s *ValidationState) **bool { return &s.Tier1DeploymentsOK },
		"Tier-2":               func(s *ValidationState) **bool { return &s.Tier2StatefulSetsOK },
	}
	for name, row := range critical {
		t.Run(name, func(t *testing.T) {
			state := healthyControlPlaneState()
			*row(state) = cpRow(false)
			var notReady *NotReadyError
			require.ErrorAs(t, printSummary(state), &notReady)
			assert.Equal(t, NotReadyError{Failed: 1}, *notReady)

			state = healthyControlPlaneState()
			*row(state) = nil
			require.ErrorAs(t, printSummary(state), &notReady)
			assert.Equal(t, NotReadyError{Unobserved: 1}, *notReady)
		})
	}

	for name, row := range map[string]func(*ValidationState) **bool{
		"Envoy Gateway":          func(s *ValidationState) **bool { return &s.EnvoyGatewayOK },
		"Gateway Route CR Types": func(s *ValidationState) **bool { return &s.GatewayRoutesOK },
		"External Load Balancer": func(s *ValidationState) **bool { return &s.ExternalLBOK },
	} {
		state := healthyControlPlaneState()
		*row(state) = cpRow(false)
		assert.NoError(t, printSummary(state), "%s is not critical", name)
		*row(state) = nil
		assert.NoError(t, printSummary(state), "%s is not critical", name)
	}

	for name, set := range map[string]func(*ValidationState){
		"Control Plane":      func(s *ValidationState) { s.ControlPlaneHealthy = false },
		"Admission Webhooks": func(s *ValidationState) { s.WebhooksSupported = false },
	} {
		state := healthyControlPlaneState()
		set(state)
		assert.ErrorIs(t, printSummary(state), ErrNotReady, name)
	}
	state := healthyControlPlaneState()
	state.NodesAllReady, state.NotReadyNodes, state.NetworkPoliciesSupported = false, 1, false
	assert.NoError(t, printSummary(state), "NotReady workers and unconfirmed NetworkPolicies are warnings")
}

// The nvca-operator chart's validator Job reserves 60s past VALIDATOR_TIMEOUT
// for the probe cleanup and 75s for the summary write
// (nvcaop.clusterValidatorTiming), and its chart test checks the rendered Job
// against 60s and 73s. A bound that changes here must change there too.
func TestPostDeadlineBounds_MatchTheChartsReserve(t *testing.T) {
	assert.Equal(t, 60*time.Second, ProbeCleanupBound)
	assert.GreaterOrEqual(t, ProbeCleanupBound, 2*enforcementDeleteTimeout, "the enforcement cleanup fits too")
	assert.Equal(t, 73*time.Second, SummaryWriteBound)
}

func TestExitCode(t *testing.T) {
	failed := &NotReadyError{Failed: 1, Unobserved: 1}
	unobserved := &NotReadyError{Unobserved: 2}
	notPublished := errors.Join(ErrSummaryNotPublished, errors.New("forbidden"))
	for _, tc := range []struct {
		name                       string
		requireSummary, startupGat string
		err                        error
		want                       int
	}{
		{name: "ready", err: nil, want: 0},
		{name: "could not run", err: errors.New("no API server"), want: 1},
		{name: "launcher-graded Not-Ready", err: failed, want: 1},
		{name: "launcher-graded unobserved", err: unobserved, want: 1},
		{name: "published Not-Ready in a Job", requireSummary: "true", err: failed, want: ExitNotReady},
		{name: "unpublished run in a Job is retried", requireSummary: "true", err: notPublished, want: 1},
		{name: "Not-Ready the run had no time to recheck is retried", requireSummary: "true",
			err: &NotReadyError{Failed: 1, Unconfirmed: true}, want: 1},
		{name: "startup gate on an observed failure", startupGat: "true", err: failed, want: 1},
		{name: "startup gate on unobserved rows only", startupGat: "true", err: unobserved, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(RequireSummaryEnv, tc.requireSummary)
			t.Setenv(StartupGateEnv, tc.startupGat)
			assert.Equal(t, tc.want, ExitCode(tc.err))
		})
	}
}

func readPublishedSummary(t *testing.T, client *fake.Clientset, ns string) *ValidatorSummary {
	t.Helper()
	cm, err := client.CoreV1().ConfigMaps(ns).Get(context.Background(), SummaryConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)
	var s ValidatorSummary
	require.NoError(t, json.Unmarshal([]byte(cm.Data[SummaryConfigMapKey]), &s))
	return &s
}

// A Not-Ready run publishes the verdict it returns.
func TestRun_PublishesTheNotReadyVerdict(t *testing.T) {
	const ns = "nvca-operator"
	client := fake.NewSimpleClientset(makeNode("node-1", true, 0))
	err := Run(context.Background(), client, nil, "", "", ns, true, RoleControlPlane)
	require.ErrorIs(t, err, ErrNotReady)

	s := readPublishedSummary(t, client, ns)
	assert.False(t, s.VerdictReady)
	assert.Equal(t, "NVCF-Not-Ready", s.Verdict)
}

// In a launcher's Job a run that could not write its summary fails, so the
// Job retries it; elsewhere the verdict is returned and the write is only
// logged.
func TestRun_RequireSummaryFailsAnUnpublishedRun(t *testing.T) {
	const ns = "nvca-operator"
	denied := func() *fake.Clientset {
		client := fake.NewSimpleClientset(makeNode("node-1", true, 0))
		client.PrependReactor("create", "configmaps", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(corev1.Resource("configmaps"), SummaryConfigMapName,
				errors.New("denied by policy"))
		})
		return client
	}

	t.Setenv(RequireSummaryEnv, "true")
	err := Run(context.Background(), denied(), nil, "", "", ns, true, RoleControlPlane)
	require.ErrorIs(t, err, ErrSummaryNotPublished)
	assert.NotErrorIs(t, err, ErrNotReady, "an unpublished verdict is not a published Not-Ready")
	assert.Equal(t, 1, ExitCode(err))

	err = Run(context.Background(), fake.NewSimpleClientset(), nil, "", "", "", true, RoleControlPlane)
	require.ErrorIs(t, err, ErrSummaryNotPublished, "no summary namespace is an unpublished run too")

	client := fake.NewSimpleClientset(makeNode("node-1", true, 0))
	err = Run(context.Background(), client, nil, "", "", ns, true, RoleControlPlane)
	require.ErrorIs(t, err, ErrNotReady)
	assert.Equal(t, ExitNotReady, ExitCode(err), "a published Not-Ready fails the Job without a retry")

	t.Setenv(RequireSummaryEnv, "")
	err = Run(context.Background(), denied(), nil, "", "", ns, true, RoleControlPlane)
	require.ErrorIs(t, err, ErrNotReady)
	assert.Equal(t, 1, ExitCode(err))
}

// The operator's init container: a critical enforcement test that could not
// run makes the published verdict Not-Ready but lets the operator start.
func TestRun_StartupGateIgnoresUnobservedRowsOnly(t *testing.T) {
	const ns = "nvca-operator"
	t.Setenv(StartupGateEnv, "true")
	client := gatewayDiscoveryClient(
		"admissionregistration.k8s.io/v1/mutatingwebhookconfigurations",
		"admissionregistration.k8s.io/v1/validatingwebhookconfigurations",
	)
	require.NoError(t, client.Tracker().Add(makeNode("gpu-1", true, 8)))
	require.NoError(t, client.Tracker().Add(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "checks", Namespace: ns},
		Data:       map[string]string{configDataKey: "enforcement:\n  enabled: true\n  critical: true\n"},
	}))
	client.PrependReactor("create", "namespaces", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(corev1.Resource("namespaces"), "", errors.New("PSA"))
	})

	err := Run(context.Background(), client, nil, ns, "checks", ns, true, RoleComputePlane)
	var notReady *NotReadyError
	require.ErrorAs(t, err, &notReady)
	assert.Equal(t, NotReadyError{Unobserved: 1}, *notReady)
	assert.Equal(t, 0, ExitCode(err), "an unobserved row does not keep the operator from starting")
	assert.False(t, readPublishedSummary(t, client, ns).VerdictReady, "the published verdict is still Not-Ready")

	client = fake.NewSimpleClientset()
	err = Run(context.Background(), client, nil, "", "", ns, true, RoleComputePlane)
	require.ErrorAs(t, err, &notReady)
	assert.Positive(t, notReady.Failed)
	assert.Equal(t, 1, ExitCode(err), "an observed critical failure still gates startup")
}

// The enforcement pods run under restricted Pod Security, like the overlay
// probe, so a restricted namespace does not reject them.
func TestEnforcementPods_AreRestrictedCompliant(t *testing.T) {
	for _, pod := range []*corev1.Pod{
		buildServerPod("ns", "img"),
		buildProbePod("ns", "probe", "img", "client", "10.0.0.1", 0),
	} {
		sc := pod.Spec.Containers[0].SecurityContext
		require.NotNil(t, sc, pod.Name)
		assert.True(t, *sc.RunAsNonRoot, pod.Name)
		assert.False(t, *sc.AllowPrivilegeEscalation, pod.Name)
		assert.Equal(t, []corev1.Capability{"ALL"}, sc.Capabilities.Drop, pod.Name)
		assert.Equal(t, corev1.SeccompProfileTypeRuntimeDefault, sc.SeccompProfile.Type, pod.Name)
	}
}
