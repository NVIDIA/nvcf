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

package clusteragent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const (
	testBackendNS  = "nvca-operator"
	testSystemNS   = "nvca-system"
	testRequestsNS = "nvcf-backend"
	testClusterID  = "cluster-uuid-1"
	testCluster    = "edge-1"
)

func newFakeMaintainer(dynObjs, k8sObjs []runtime.Object) (*k8sMaintainer, *dynamicfake.FakeDynamicClient, *k8sfake.Clientset) {
	scheme := runtime.NewScheme()
	gvrToListKind := map[schema.GroupVersionResource]string{
		nvcfBackendGVR: "NVCFBackendList",
		icmsRequestGVR: "ICMSRequestList",
	}
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, dynObjs...)
	cs := k8sfake.NewSimpleClientset(k8sObjs...)
	return &k8sMaintainer{dc: dc, cs: cs}, dc, cs
}

func backendObj(backendNS, clusterID, clusterName, systemNS, requestsNS string) *unstructured.Unstructured {
	cc := map[string]interface{}{
		"clusterId":   clusterID,
		"clusterName": clusterName,
	}
	if systemNS != "" {
		cc["systemNamespace"] = systemNS
	}
	if requestsNS != "" {
		cc["requestsNamespace"] = requestsNS
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "nvcf.nvidia.io/v1",
		"kind":       "NVCFBackend",
		"metadata":   map[string]interface{}{"namespace": backendNS, "name": "backend"},
		"spec": map[string]interface{}{
			"version":       "2.30.4",
			"clusterConfig": cc,
		},
	}}
}

func defaultBackend() *unstructured.Unstructured {
	return backendObj(testBackendNS, testClusterID, testCluster, testSystemNS, testRequestsNS)
}

func agentConfigObj(systemNS, configYAML string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: agentConfigConfigMapName, Namespace: systemNS},
		Data:       map[string]string{agentConfigKey: configYAML},
	}
}

func nvcaDeployObj(systemNS string, replicas int32, complete bool) *appsv1.Deployment {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: nvcaDeploymentName, Namespace: systemNS, Generation: 2},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 2},
	}
	if complete {
		d.Status.UpdatedReplicas = replicas
		d.Status.AvailableReplicas = replicas
		d.Status.UnavailableReplicas = 0
	}
	return d
}

func icmsRequestWithFinalizers(ns, name, fid, vid string, finalizers ...string) *unstructured.Unstructured {
	u := icmsRequest(ns, name, fid, vid, "", statusCompleted, false)
	fin := make([]interface{}, len(finalizers))
	for i, f := range finalizers {
		fin[i] = f
	}
	u.Object["metadata"].(map[string]interface{})["finalizers"] = fin
	return u
}

// --- Drain / Undrain ---

// backendObjWithOverrideValues seeds an NVCFBackend CR with a pre-existing
// spec.overrides.featureGate.values list, simulating a cluster already
// carrying prior CLI-set overrides.
func backendObjWithOverrideValues(backendNS, clusterID, clusterName, systemNS, requestsNS string, overrideValues ...string) *unstructured.Unstructured {
	b := backendObj(backendNS, clusterID, clusterName, systemNS, requestsNS)
	vals := make([]interface{}, len(overrideValues))
	for i, v := range overrideValues {
		vals[i] = v
	}
	b.Object["spec"].(map[string]interface{})["overrides"] = map[string]interface{}{
		"featureGate": map[string]interface{}{"values": vals},
	}
	return b
}

// backendOverrideValues reads spec.overrides.featureGate.values back off the
// single NVCFBackend CR in backendNS, the same field patchMaintenanceFeatureFlag
// writes.
func backendOverrideValues(t *testing.T, dc *dynamicfake.FakeDynamicClient, backendNS string) []string {
	t.Helper()
	list, err := dc.Resource(nvcfBackendGVR).Namespace(backendNS).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing NVCFBackend: %v", err)
	}
	if len(list.Items) == 0 {
		t.Fatalf("no NVCFBackend found in namespace %q", backendNS)
	}
	values, _, err := unstructured.NestedStringSlice(list.Items[0].Object, "spec", "overrides", "featureGate", "values")
	if err != nil {
		t.Fatalf("reading spec.overrides.featureGate.values: %v", err)
	}
	return values
}

func TestDrainPatchesNVCFBackendOverrides(t *testing.T) {
	m, dc, _ := newFakeMaintainer(
		[]runtime.Object{backendObjWithOverrideValues(testBackendNS, testClusterID, testCluster, testSystemNS, testRequestsNS, "LogPosting")},
		nil,
	)

	res, err := m.Drain(context.Background(), DrainOptions{BackendNS: testBackendNS})
	if err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if !res.ConfigChanged || !res.RolloutTriggered {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.Mode != maintenanceModeCordonAndDrain {
		t.Errorf("Mode = %q, want %q", res.Mode, maintenanceModeCordonAndDrain)
	}
	got := backendOverrideValues(t, dc, testBackendNS)
	if !slices.Contains(got, cordonAndDrainFeatureFlag) {
		t.Errorf("overrides missing feature flag: %v", got)
	}
	if !slices.Contains(got, "LogPosting") {
		t.Errorf("drain dropped the pre-existing LogPosting override: %v", got)
	}
}

func TestDrainIdempotent(t *testing.T) {
	m, dc, _ := newFakeMaintainer(
		[]runtime.Object{backendObjWithOverrideValues(testBackendNS, testClusterID, testCluster, testSystemNS, testRequestsNS, cordonAndDrainFeatureFlag)},
		nil,
	)

	res, err := m.Drain(context.Background(), DrainOptions{BackendNS: testBackendNS})
	if err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if res.ConfigChanged || res.RolloutTriggered {
		t.Fatalf("expected no-op, got %+v", res)
	}
	before := backendOverrideValues(t, dc, testBackendNS)
	if !slices.Equal(before, []string{cordonAndDrainFeatureFlag}) {
		t.Errorf("idempotent drain must not touch overrides, got %v", before)
	}
}

func TestDrainDryRunMutatesNothing(t *testing.T) {
	m, dc, _ := newFakeMaintainer(
		[]runtime.Object{backendObjWithOverrideValues(testBackendNS, testClusterID, testCluster, testSystemNS, testRequestsNS, "LogPosting")},
		nil,
	)

	res, err := m.Drain(context.Background(), DrainOptions{BackendNS: testBackendNS, DryRun: true})
	if err != nil {
		t.Fatalf("Drain dry-run returned error: %v", err)
	}
	if !res.DryRun || !res.ConfigChanged || res.RolloutTriggered {
		t.Fatalf("unexpected dry-run result: %+v", res)
	}
	got := backendOverrideValues(t, dc, testBackendNS)
	if !slices.Equal(got, []string{"LogPosting"}) {
		t.Errorf("dry-run mutated overrides: %v", got)
	}
}

func TestDrainExpectClusterID(t *testing.T) {
	newM := func() (*k8sMaintainer, *dynamicfake.FakeDynamicClient) {
		m, dc, _ := newFakeMaintainer([]runtime.Object{defaultBackend()}, nil)
		return m, dc
	}

	t.Run("mismatch aborts before any write", func(t *testing.T) {
		m, dc := newM()
		_, err := m.Drain(context.Background(), DrainOptions{BackendNS: testBackendNS, ExpectClusterID: "wrong-id"})
		if err == nil {
			t.Fatal("expected refusal on cluster-id mismatch")
		}
		if got := backendOverrideValues(t, dc, testBackendNS); len(got) != 0 {
			t.Errorf("overrides mutated despite mismatch: %v", got)
		}
	})

	t.Run("matches by id", func(t *testing.T) {
		m, _ := newM()
		if _, err := m.Drain(context.Background(), DrainOptions{BackendNS: testBackendNS, ExpectClusterID: testClusterID}); err != nil {
			t.Fatalf("expected match by id to proceed: %v", err)
		}
	})

	t.Run("matches by name", func(t *testing.T) {
		m, _ := newM()
		if _, err := m.Drain(context.Background(), DrainOptions{BackendNS: testBackendNS, ExpectClusterID: testCluster}); err != nil {
			t.Fatalf("expected match by name to proceed: %v", err)
		}
	})
}

func TestDrainNoBackend(t *testing.T) {
	m, _, _ := newFakeMaintainer(nil, nil)
	if _, err := m.Drain(context.Background(), DrainOptions{BackendNS: testBackendNS}); err == nil {
		t.Fatal("expected error when no NVCFBackend exists")
	}
}

func TestUndrainRemovesOverride(t *testing.T) {
	m, dc, _ := newFakeMaintainer(
		[]runtime.Object{backendObjWithOverrideValues(testBackendNS, testClusterID, testCluster, testSystemNS, testRequestsNS, cordonAndDrainFeatureFlag, "LogPosting")},
		nil,
	)

	res, err := m.Undrain(context.Background(), DrainOptions{BackendNS: testBackendNS})
	if err != nil {
		t.Fatalf("Undrain returned error: %v", err)
	}
	if !res.ConfigChanged || !res.RolloutTriggered {
		t.Fatalf("unexpected result: %+v", res)
	}
	got := backendOverrideValues(t, dc, testBackendNS)
	if slices.Contains(got, cordonAndDrainFeatureFlag) {
		t.Errorf("undrain left the feature flag: %v", got)
	}
	if !slices.Contains(got, "LogPosting") {
		t.Errorf("undrain removed an unrelated override: %v", got)
	}
}

func TestUndrainIdempotent(t *testing.T) {
	m, dc, _ := newFakeMaintainer(
		[]runtime.Object{backendObjWithOverrideValues(testBackendNS, testClusterID, testCluster, testSystemNS, testRequestsNS, "LogPosting")},
		nil,
	)
	res, err := m.Undrain(context.Background(), DrainOptions{BackendNS: testBackendNS})
	if err != nil {
		t.Fatalf("Undrain returned error: %v", err)
	}
	if res.ConfigChanged || res.RolloutTriggered {
		t.Fatalf("expected no-op undrain, got %+v", res)
	}
	got := backendOverrideValues(t, dc, testBackendNS)
	if !slices.Equal(got, []string{"LogPosting"}) {
		t.Errorf("idempotent undrain must not touch overrides, got %v", got)
	}
}

// --- Drain / Undrain: waiting for the NVCA operator's own reconcile ---
//
// These tests simulate the operator's effect by pre-seeding agent-config and
// the NVCA Deployment directly, since no real operator runs against the fake
// client. That is also what makes them regression tests for the original
// bug: waitForMaintenanceRollout must not report success just because the
// Deployment trivially already satisfies the completion check before the
// operator has done anything (see TestDrainRolloutTimesOutWhenConfigNeverUpdates).

func TestDrainReportsRolloutCompleteWhenOperatorHasAlreadyReconciled(t *testing.T) {
	// Simulates the operator having already regenerated agent-config and
	// rolled out NVCA by the time the CLI's first poll runs.
	cfg := "agent:\n  featureFlags:\n  - " + cordonAndDrainFeatureFlag + "\n"
	m, _, _ := newFakeMaintainer(
		[]runtime.Object{defaultBackend()},
		[]runtime.Object{agentConfigObj(testSystemNS, cfg), nvcaDeployObj(testSystemNS, 1, true)},
	)

	res, err := m.Drain(context.Background(), DrainOptions{BackendNS: testBackendNS, Timeout: time.Second})
	if err != nil {
		t.Fatalf("Drain returned error: %v", err)
	}
	if !res.RolloutComplete {
		t.Fatalf("expected rollout to be reported complete, got %+v", res)
	}
}

func TestDrainRolloutTimesOutWhenConfigNeverUpdates(t *testing.T) {
	// Regression test for the original bug: the Deployment already looks
	// "complete" from a prior rollout (this is exactly the trivially-true
	// state that misled the old Deployment-only check), but agent-config
	// was never regenerated with the flag, i.e. the operator never actually
	// reconciled the CR change. The wait must not report success.
	prev := rolloutPollInterval
	rolloutPollInterval = time.Millisecond
	t.Cleanup(func() { rolloutPollInterval = prev })

	cfg := "agent:\n  featureFlags:\n  - LogPosting\n"
	m, dc, _ := newFakeMaintainer(
		[]runtime.Object{defaultBackend()},
		[]runtime.Object{agentConfigObj(testSystemNS, cfg), nvcaDeployObj(testSystemNS, 1, true)},
	)

	res, err := m.Drain(context.Background(), DrainOptions{BackendNS: testBackendNS, Timeout: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("timeout must not be a hard error: %v", err)
	}
	if !res.ConfigChanged || !res.RolloutTriggered || res.RolloutComplete {
		t.Fatalf("unexpected result: %+v", res)
	}
	if !strings.Contains(res.Message, "has not finished") {
		t.Errorf("message = %q, want a timeout note", res.Message)
	}
	// The CR patch itself is still what we're verifying was submitted.
	got := backendOverrideValues(t, dc, testBackendNS)
	if !slices.Contains(got, cordonAndDrainFeatureFlag) {
		t.Errorf("overrides not patched: %v", got)
	}
}

func TestDrainRolloutTimesOutWhenDeploymentNeverStabilizes(t *testing.T) {
	// The inverse partial case: agent-config already reflects the flag (the
	// operator started reconciling), but the Deployment rollout has not
	// stabilized yet.
	prev := rolloutPollInterval
	rolloutPollInterval = time.Millisecond
	t.Cleanup(func() { rolloutPollInterval = prev })

	cfg := "agent:\n  featureFlags:\n  - " + cordonAndDrainFeatureFlag + "\n"
	m, _, _ := newFakeMaintainer(
		[]runtime.Object{defaultBackend()},
		[]runtime.Object{agentConfigObj(testSystemNS, cfg), nvcaDeployObj(testSystemNS, 1, false)},
	)

	res, err := m.Drain(context.Background(), DrainOptions{BackendNS: testBackendNS, Timeout: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("timeout must not be a hard error: %v", err)
	}
	if res.RolloutComplete {
		t.Fatal("expected timeout while the Deployment has not stabilized")
	}
}

func TestWaitForMaintenanceRolloutWaitsForObservedGeneration(t *testing.T) {
	prev := rolloutPollInterval
	rolloutPollInterval = time.Millisecond
	t.Cleanup(func() { rolloutPollInterval = prev })

	// Replicas look complete, but the controller has not observed the latest
	// spec generation yet, so the status still reflects the prior rollout.
	d := nvcaDeployObj(testSystemNS, 1, true)
	d.Generation = 3
	d.Status.ObservedGeneration = 2
	cfg := "agent:\n  featureFlags:\n  - " + cordonAndDrainFeatureFlag + "\n"
	m, _, _ := newFakeMaintainer(nil, []runtime.Object{agentConfigObj(testSystemNS, cfg), d})

	if err := m.waitForMaintenanceRollout(context.Background(), testBackendNS, testSystemNS, 10*time.Millisecond, true); err == nil {
		t.Fatal("expected timeout while ObservedGeneration < Generation, got nil")
	}
}

func TestDrainForceSkipsRolloutWait(t *testing.T) {
	m, _, _ := newFakeMaintainer(
		[]runtime.Object{defaultBackend()},
		[]runtime.Object{agentConfigObj(testSystemNS, "agent:\n"), nvcaDeployObj(testSystemNS, 1, false)},
	)
	res, err := m.Drain(context.Background(), DrainOptions{BackendNS: testBackendNS, Force: true, Timeout: time.Hour})
	if err != nil {
		t.Fatalf("Drain --force returned error: %v", err)
	}
	if !res.RolloutTriggered || res.RolloutComplete {
		t.Fatalf("force should submit the CR change but not wait: %+v", res)
	}
}

func TestDrainForceHasNoEffectWhenAlreadyInDesiredState(t *testing.T) {
	// Unlike the old ConfigMap/Deployment-restart mechanism, there is no
	// separate "restart" action for --force to retrigger once the CR is
	// already in the desired state: the operator owns the actual rollout,
	// and re-submitting an unchanged CR produces no new reconcile.
	m, _, _ := newFakeMaintainer(
		[]runtime.Object{backendObjWithOverrideValues(testBackendNS, testClusterID, testCluster, testSystemNS, testRequestsNS, cordonAndDrainFeatureFlag)},
		nil,
	)
	res, err := m.Drain(context.Background(), DrainOptions{BackendNS: testBackendNS, Force: true})
	if err != nil {
		t.Fatalf("Drain --force returned error: %v", err)
	}
	if res.ConfigChanged || res.RolloutTriggered {
		t.Fatalf("expected a no-op, got %+v", res)
	}
	if res.Message != "already in the requested state; no change" {
		t.Errorf("Message = %q", res.Message)
	}
}

// --- agent-config YAML helpers ---

func TestAddFeatureFlagToConfig(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "appends to existing section",
			in:   "agent:\n  featureFlags:\n  - LogPosting\n",
			want: "agent:\n  featureFlags:\n  - CordonAndDrainMaintenance\n  - LogPosting\n",
		},
		{
			name: "already present is unchanged",
			in:   "agent:\n  featureFlags:\n  - CordonAndDrainMaintenance\n",
			want: "agent:\n  featureFlags:\n  - CordonAndDrainMaintenance\n",
		},
		{
			name: "creates section under agent",
			in:   "agent:\n  logLevel: info\n",
			want: "agent:\n  featureFlags:\n  - CordonAndDrainMaintenance\n  logLevel: info\n",
		},
		{
			name: "no agent section is a no-op",
			in:   "other:\n  x: y\n",
			want: "other:\n  x: y\n",
		},
		{
			name: "flag in another section is not treated as duplicate",
			in:   "other:\n- CordonAndDrainMaintenance\nagent:\n  logLevel: info\n",
			want: "other:\n- CordonAndDrainMaintenance\nagent:\n  featureFlags:\n  - CordonAndDrainMaintenance\n  logLevel: info\n",
		},
		{
			name: "agent anchor with trailing whitespace is matched",
			in:   "agent:  \n  logLevel: info\n",
			want: "agent:  \n  featureFlags:\n  - CordonAndDrainMaintenance\n  logLevel: info\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := addFeatureFlagToConfig(tc.in, cordonAndDrainFeatureFlag); got != tc.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}

// --- Kill ---

func killSeed() []runtime.Object {
	return []runtime.Object{
		defaultBackend(),
		icmsRequest(testRequestsNS, "r1", "fn-1", "v1", "", statusCompleted, false),
		icmsRequest(testRequestsNS, "r2", "fn-1", "v2", "", statusInProgress, false),
		icmsRequest(testRequestsNS, "r3", "fn-2", "v1", "", statusCompleted, true),
	}
}

func icmsExists(t *testing.T, dc *dynamicfake.FakeDynamicClient, ns, name string) bool {
	t.Helper()
	_, err := dc.Resource(icmsRequestGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err == nil {
		return true
	}
	if apierrors.IsNotFound(err) {
		return false
	}
	t.Fatalf("unexpected error checking %s/%s: %v", ns, name, err)
	return false
}

func TestKillFunctionMatchesVersion(t *testing.T) {
	m, dc, _ := newFakeMaintainer(killSeed(), nil)

	res, err := m.KillFunction(context.Background(), "fn-1", "v2", KillOptions{BackendNS: testBackendNS})
	if err != nil {
		t.Fatalf("KillFunction returned error: %v", err)
	}
	if len(res.Affected) != 1 || res.Affected[0].Name != "r2" {
		t.Fatalf("affected = %+v, want only r2", res.Affected)
	}
	if icmsExists(t, dc, testRequestsNS, "r2") {
		t.Error("r2 should have been deleted")
	}
	if !icmsExists(t, dc, testRequestsNS, "r1") {
		t.Error("r1 (other version) must remain")
	}
}

func TestKillFunctionAllVersions(t *testing.T) {
	m, dc, _ := newFakeMaintainer(killSeed(), nil)

	res, err := m.KillFunction(context.Background(), "fn-1", "", KillOptions{BackendNS: testBackendNS})
	if err != nil {
		t.Fatalf("KillFunction returned error: %v", err)
	}
	if len(res.Affected) != 2 {
		t.Fatalf("affected = %+v, want both fn-1 versions", res.Affected)
	}
	if icmsExists(t, dc, testRequestsNS, "r1") || icmsExists(t, dc, testRequestsNS, "r2") {
		t.Error("both fn-1 versions should be deleted")
	}
	if !icmsExists(t, dc, testRequestsNS, "r3") {
		t.Error("fn-2 must remain")
	}
}

func TestKillResultCarriesReason(t *testing.T) {
	m, _, _ := newFakeMaintainer(killSeed(), nil)
	res, err := m.KillFunction(context.Background(), "fn-1", "v2", KillOptions{BackendNS: testBackendNS, Reason: "node maintenance"})
	if err != nil {
		t.Fatalf("KillFunction returned error: %v", err)
	}
	if res.Reason != "node maintenance" {
		t.Errorf("Reason = %q, want %q", res.Reason, "node maintenance")
	}
}

func TestKillFunctionNotFound(t *testing.T) {
	m, _, _ := newFakeMaintainer(killSeed(), nil)
	if _, err := m.KillFunction(context.Background(), "missing", "", KillOptions{BackendNS: testBackendNS}); err == nil {
		t.Fatal("expected error for unknown function")
	}
}

func TestKillFunctionDryRunDeletesNothing(t *testing.T) {
	m, dc, _ := newFakeMaintainer(killSeed(), nil)

	res, err := m.KillFunction(context.Background(), "fn-1", "", KillOptions{BackendNS: testBackendNS, DryRun: true})
	if err != nil {
		t.Fatalf("KillFunction dry-run returned error: %v", err)
	}
	if !res.DryRun || len(res.Affected) != 2 {
		t.Fatalf("unexpected dry-run result: %+v", res)
	}
	if !icmsExists(t, dc, testRequestsNS, "r1") || !icmsExists(t, dc, testRequestsNS, "r2") {
		t.Error("dry-run must not delete anything")
	}
}

func TestKillAll(t *testing.T) {
	m, dc, _ := newFakeMaintainer(killSeed(), nil)

	res, err := m.KillAll(context.Background(), KillOptions{BackendNS: testBackendNS})
	if err != nil {
		t.Fatalf("KillAll returned error: %v", err)
	}
	if len(res.Affected) != 3 {
		t.Fatalf("affected = %d, want 3", len(res.Affected))
	}
	for _, name := range []string{"r1", "r2", "r3"} {
		if icmsExists(t, dc, testRequestsNS, name) {
			t.Errorf("%s should have been deleted", name)
		}
	}
}

func TestKillAllEmptyCluster(t *testing.T) {
	m, _, _ := newFakeMaintainer([]runtime.Object{defaultBackend()}, nil)
	res, err := m.KillAll(context.Background(), KillOptions{BackendNS: testBackendNS})
	if err != nil {
		t.Fatalf("KillAll on empty cluster must not error: %v", err)
	}
	if len(res.Affected) != 0 || res.FailedCount != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestKillPartialFailureReportsAggregateError(t *testing.T) {
	m, dc, _ := newFakeMaintainer(killSeed(), nil)
	dc.PrependReactor("delete", "icmsrequests", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if da, ok := action.(k8stesting.DeleteAction); ok && da.GetName() == "r2" {
			return true, nil, fmt.Errorf("simulated delete failure")
		}
		return false, nil, nil
	})

	res, err := m.KillAll(context.Background(), KillOptions{BackendNS: testBackendNS})
	if err == nil {
		t.Fatal("expected aggregate error on partial failure")
	}
	if res == nil || res.FailedCount != 1 {
		t.Fatalf("expected populated result with one failure, got %+v", res)
	}
	var failed *KilledRequest
	for i := range res.Affected {
		if res.Affected[i].Name == "r2" {
			failed = &res.Affected[i]
		}
	}
	if failed == nil || failed.Error == "" {
		t.Fatalf("r2 should carry a per-item error, got %+v", res.Affected)
	}
}

func TestStripFinalizersThenDelete(t *testing.T) {
	cr := icmsRequestWithFinalizers(testRequestsNS, "r1", "fn-1", "v1", "nvca.nvcf.nvidia.io/cleanup")
	m, dc, _ := newFakeMaintainer([]runtime.Object{defaultBackend(), cr}, nil)

	if err := m.stripFinalizers(context.Background(), testRequestsNS, "r1"); err != nil {
		t.Fatalf("stripFinalizers returned error: %v", err)
	}
	got, err := dc.Resource(icmsRequestGVR).Namespace(testRequestsNS).Get(context.Background(), "r1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get after strip: %v", err)
	}
	if len(got.GetFinalizers()) != 0 {
		t.Errorf("finalizers = %v, want empty", got.GetFinalizers())
	}
}

func TestKillForceDeletesFinalizedRequest(t *testing.T) {
	cr := icmsRequestWithFinalizers(testRequestsNS, "r1", "fn-1", "v1", "nvca.nvcf.nvidia.io/cleanup")
	m, dc, _ := newFakeMaintainer([]runtime.Object{defaultBackend(), cr}, nil)

	res, err := m.KillFunction(context.Background(), "fn-1", "", KillOptions{BackendNS: testBackendNS, Force: true})
	if err != nil {
		t.Fatalf("KillFunction --force returned error: %v", err)
	}
	if res.FailedCount != 0 || len(res.Affected) != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if icmsExists(t, dc, testRequestsNS, "r1") {
		t.Error("forced kill should have deleted the request")
	}
}

func TestResolveClusterAppliesNamespaceDefaults(t *testing.T) {
	// Backend with no system/requests namespace set.
	b := backendObj(testBackendNS, testClusterID, testCluster, "", "")
	m, _, _ := newFakeMaintainer([]runtime.Object{b}, nil)

	target, err := m.ResolveCluster(context.Background(), testBackendNS)
	if err != nil {
		t.Fatalf("ResolveCluster returned error: %v", err)
	}
	if target.SystemNamespace != defaultSystemNamespace || target.RequestsNamespace != defaultRequestsNamespace {
		t.Errorf("namespaces = %s/%s, want defaults %s/%s",
			target.SystemNamespace, target.RequestsNamespace, defaultSystemNamespace, defaultRequestsNamespace)
	}
	if target.ClusterID != testClusterID || target.ClusterName != testCluster {
		t.Errorf("identity = %s/%s, want %s/%s", target.ClusterID, target.ClusterName, testClusterID, testCluster)
	}
}
