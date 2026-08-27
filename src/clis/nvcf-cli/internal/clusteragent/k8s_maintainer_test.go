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
	"errors"
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
		miniServiceGVR: "MiniServiceList",
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

// backendObjWithBaseValues seeds an NVCFBackend CR with a pre-existing
// spec.featureGate.values list (the base spec, not overrides), simulating a
// cluster whose base spec already sets a maintenance flag directly.
func backendObjWithBaseValues(backendNS, clusterID, clusterName, systemNS, requestsNS string, baseValues ...string) *unstructured.Unstructured {
	b := backendObj(backendNS, clusterID, clusterName, systemNS, requestsNS)
	vals := make([]interface{}, len(baseValues))
	for i, v := range baseValues {
		vals[i] = v
	}
	b.Object["spec"].(map[string]interface{})["featureGate"] = map[string]interface{}{"values": vals}
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

func TestDrainConflictsWithBaseCordonMaintenance(t *testing.T) {
	m, dc, _ := newFakeMaintainer(
		[]runtime.Object{backendObjWithBaseValues(testBackendNS, testClusterID, testCluster, testSystemNS, testRequestsNS, cordonMaintenanceFeatureFlag)},
		nil,
	)

	_, err := m.Drain(context.Background(), DrainOptions{BackendNS: testBackendNS})
	if err == nil {
		t.Fatal("expected an error: base spec already sets CordonMaintenance, which the operator prefers over CordonAndDrainMaintenance")
	}
	if got := backendOverrideValues(t, dc, testBackendNS); len(got) != 0 {
		t.Errorf("overrides mutated despite conflict: %v", got)
	}
}

func TestUndrainConflictsWithBaseCordonAndDrain(t *testing.T) {
	m, dc, _ := newFakeMaintainer(
		[]runtime.Object{backendObjWithBaseValues(testBackendNS, testClusterID, testCluster, testSystemNS, testRequestsNS, cordonAndDrainFeatureFlag)},
		nil,
	)

	_, err := m.Undrain(context.Background(), DrainOptions{BackendNS: testBackendNS})
	if err == nil {
		t.Fatal("expected an error: base spec already sets CordonAndDrainMaintenance, which the operator keeps regardless of overrides")
	}
	if got := backendOverrideValues(t, dc, testBackendNS); len(got) != 0 {
		t.Errorf("overrides mutated despite conflict: %v", got)
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

func TestDrainDoesNotFalselyReportCompleteWhenConfigHasNoFeatureFlagsSection(t *testing.T) {
	// Regression test for a false-positive in the config membership check:
	// a config with no featureFlags: (or even agent:) section at all must
	// not be misread as "already has the flag". Deployment looks complete,
	// so this isolates the config-side check specifically.
	prev := rolloutPollInterval
	rolloutPollInterval = time.Millisecond
	t.Cleanup(func() { rolloutPollInterval = prev })

	cfg := "other:\n  x: y\n"
	m, _, _ := newFakeMaintainer(
		[]runtime.Object{defaultBackend()},
		[]runtime.Object{agentConfigObj(testSystemNS, cfg), nvcaDeployObj(testSystemNS, 1, true)},
	)

	res, err := m.Drain(context.Background(), DrainOptions{BackendNS: testBackendNS, Timeout: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("timeout must not be a hard error: %v", err)
	}
	if res.RolloutComplete {
		t.Fatal("must not report complete: agent-config has no featureFlags section, so the flag cannot be present")
	}
}

func TestDrainDoesNotFalselyReportCompleteWhenDeploymentMissing(t *testing.T) {
	// Regression test: a missing nvca Deployment must not be treated as a
	// trivially-satisfied rollout. Otherwise a stale agent-config left over
	// from a prior install (matching the requested flag state) combined with
	// no running nvca workload would be misreported as a complete rollout.
	prev := rolloutPollInterval
	rolloutPollInterval = time.Millisecond
	t.Cleanup(func() { rolloutPollInterval = prev })

	cfg := "agent:\n  featureFlags:\n  - " + cordonAndDrainFeatureFlag + "\n"
	m, _, _ := newFakeMaintainer(
		[]runtime.Object{defaultBackend()},
		[]runtime.Object{agentConfigObj(testSystemNS, cfg)},
	)

	res, err := m.Drain(context.Background(), DrainOptions{BackendNS: testBackendNS, Timeout: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("timeout must not be a hard error: %v", err)
	}
	if res.RolloutComplete {
		t.Fatal("must not report complete: the nvca Deployment does not exist")
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

// TestConfigHasFeatureFlag is a regression test for a false-positive in the
// prior membership check, which inferred "flag present" from
// addFeatureFlagToConfig returning its input unchanged. That mutator also
// returns its input unchanged when there is no featureFlags: (or even
// agent:) section to insert into at all, which would misreport an absent
// flag as present. configHasFeatureFlag must not have that false-positive
// path: it only ever returns true when the flag is actually listed.
func TestConfigHasFeatureFlag(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{
			name: "present in featureFlags section",
			in:   "agent:\n  featureFlags:\n  - CordonAndDrainMaintenance\n  - LogPosting\n",
			want: true,
		},
		{
			name: "absent from populated featureFlags section",
			in:   "agent:\n  featureFlags:\n  - LogPosting\n",
			want: false,
		},
		{
			name: "no featureFlags or agent section at all: must not false-positive",
			in:   "other:\n  x: y\n",
			want: false,
		},
		{
			name: "agent section present but no featureFlags key: must not false-positive",
			in:   "agent:\n  logLevel: info\n",
			want: false,
		},
		{
			name: "empty config: must not false-positive",
			in:   "",
			want: false,
		},
		{
			name: "flag in another section is not treated as a match",
			in:   "other:\n- CordonAndDrainMaintenance\nagent:\n  featureFlags:\n  - LogPosting\n",
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := configHasFeatureFlag(tc.in, cordonAndDrainFeatureFlag); got != tc.want {
				t.Errorf("configHasFeatureFlag(%q) = %v, want %v", tc.in, got, tc.want)
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

// TestKillReportsTerminatingWhenFinalizerBlocksDeletion is a regression test
// for the false-positive "[deleted]" report: when Delete is accepted but a
// finalizer keeps the object present (the real-world behavior when NVCA has
// not evicted the workload yet), the fake dynamic client's default tracker
// removes the object immediately regardless of finalizers, so a delete
// reactor is used to simulate the object surviving Delete, mirroring a real
// API server with a finalizer still set.
func TestKillReportsTerminatingWhenFinalizerBlocksDeletion(t *testing.T) {
	orig := killDeletionPollInterval
	killDeletionPollInterval = time.Millisecond
	t.Cleanup(func() { killDeletionPollInterval = orig })

	cr := icmsRequestWithFinalizers(testRequestsNS, "r1", "fn-1", "v1", "nvca.finalizers.nvidia.io")
	m, dc, _ := newFakeMaintainer([]runtime.Object{defaultBackend(), cr}, nil)
	dc.PrependReactor("delete", "icmsrequests", func(action k8stesting.Action) (bool, runtime.Object, error) {
		// Simulate the real API server: the delete is accepted (no error)
		// but the object, carrying a finalizer, is not actually removed.
		return true, nil, nil
	})

	res, err := m.KillFunction(context.Background(), "fn-1", "v1", KillOptions{
		BackendNS: testBackendNS,
		Timeout:   5 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected an error reporting the request is still terminating")
	}
	if !strings.Contains(err.Error(), "terminating") {
		t.Errorf("error = %q, want it to mention terminating", err.Error())
	}
	if res.TerminatingCount != 1 || res.FailedCount != 0 {
		t.Fatalf("TerminatingCount/FailedCount = %d/%d, want 1/0", res.TerminatingCount, res.FailedCount)
	}
	if len(res.Affected) != 1 || !res.Affected[0].Terminating || res.Affected[0].Error != "" {
		t.Fatalf("affected = %+v, want a single non-error Terminating entry", res.Affected)
	}
	if !icmsExists(t, dc, testRequestsNS, "r1") {
		t.Error("r1 must still exist: it was never actually removed, only marked for deletion")
	}
}

// TestKillWithinTimeoutReportsDeletedNotTerminating confirms the happy path
// still reports plain "deleted" (not terminating) when the object disappears
// before the deadline: the poll loop must not itself introduce a false
// negative on a normal, fast reconcile.
func TestKillWithinTimeoutReportsDeletedNotTerminating(t *testing.T) {
	orig := killDeletionPollInterval
	killDeletionPollInterval = time.Millisecond
	t.Cleanup(func() { killDeletionPollInterval = orig })

	m, _, _ := newFakeMaintainer(killSeed(), nil)

	res, err := m.KillFunction(context.Background(), "fn-1", "v2", KillOptions{
		BackendNS: testBackendNS,
		Timeout:   50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("KillFunction returned error: %v", err)
	}
	if res.TerminatingCount != 0 || len(res.Affected) != 1 || res.Affected[0].Terminating {
		t.Fatalf("unexpected result: %+v", res)
	}
}

// TestKillNegativeTimeoutRejected is a regression test: --timeout=-1s parses
// to a valid negative time.Duration with no error from the flag layer, so
// negative values must be rejected explicitly rather than silently falling
// back to DefaultKillTimeout like zero does.
// TestKillEvictsPodBackedInstanceAndMarksItTerminated asserts kill-function
// deletes the backing pod and marks the instance terminated on the CR. A
// delete reactor keeps the CR present after Delete (mirroring
// TestKillReportsTerminatingWhenFinalizerBlocksDeletion), so the patched
// status.instances is still inspectable afterward.
func TestKillEvictsPodBackedInstanceAndMarksItTerminated(t *testing.T) {
	cr := icmsRequestWithFinalizers(testRequestsNS, "r1", "fn-1", "v1", "nvca.finalizers.nvidia.io")
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "inst-a", Namespace: testRequestsNS}}
	m, dc, cs := newFakeMaintainer([]runtime.Object{defaultBackend(), cr}, []runtime.Object{pod})
	dc.PrependReactor("delete", "icmsrequests", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, nil
	})

	_, err := m.KillFunction(context.Background(), "fn-1", "v1", KillOptions{
		BackendNS: testBackendNS,
		Timeout:   5 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected an error reporting the request is still terminating (the fake finalizer never actually clears)")
	}

	if _, err := cs.CoreV1().Pods(testRequestsNS).Get(context.Background(), "inst-a", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("pod inst-a should have been deleted, got err=%v", err)
	}

	obj, err := dc.Resource(icmsRequestGVR).Namespace(testRequestsNS).Get(context.Background(), "r1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting r1: %v", err)
	}
	status, _, _ := unstructured.NestedString(obj.Object, "status", "instances", "inst-a", "lastReportedStatus")
	if status != "terminated" {
		t.Errorf("status.instances.inst-a.lastReportedStatus = %q, want %q", status, "terminated")
	}
}

// TestKillEvictsMiniServiceBackedInstanceAndMarksItTerminated is the
// MiniService-instance counterpart to TestKillEvictsPodBackedInstanceAndMarksItTerminated.
// Unlike a Pod instance, deleting the MiniService object is itself
// sufficient to drive real teardown (its own controller actively cleans up
// on deletion), so this only needs to verify the MiniService object gets
// deleted and the ICMSRequest's instance status still gets patched
// terminated the same way.
func TestKillEvictsMiniServiceBackedInstanceAndMarksItTerminated(t *testing.T) {
	cr := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "nvca.nvcf.nvidia.io/v2beta1",
		"kind":       "ICMSRequest",
		"metadata": map[string]interface{}{
			"namespace":  testRequestsNS,
			"name":       "r1",
			"finalizers": []interface{}{"nvca.finalizers.nvidia.io"},
		},
		"spec": map[string]interface{}{
			"functionDetails": map[string]interface{}{
				"functionId":        "fn-1",
				"functionVersionId": "v1",
			},
		},
		"status": map[string]interface{}{
			"requestStatus": statusCompleted,
			"instances": map[string]interface{}{
				"ms-a": map[string]interface{}{
					"id":           "ms-a",
					"instanceType": "MiniService",
					"status":       "Running",
				},
			},
		},
	}}
	ms := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "nvca.nvcf.nvidia.io/v1alpha1",
		"kind":       "MiniService",
		"metadata":   map[string]interface{}{"name": "ms-a"},
	}}
	m, dc, _ := newFakeMaintainer([]runtime.Object{defaultBackend(), cr, ms}, nil)
	dc.PrependReactor("delete", "icmsrequests", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, nil
	})

	_, err := m.KillFunction(context.Background(), "fn-1", "v1", KillOptions{
		BackendNS: testBackendNS,
		Timeout:   5 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected an error reporting the request is still terminating (the fake finalizer never actually clears)")
	}

	if _, err := dc.Resource(miniServiceGVR).Get(context.Background(), "ms-a", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("MiniService ms-a should have been deleted, got err=%v", err)
	}

	obj, err := dc.Resource(icmsRequestGVR).Namespace(testRequestsNS).Get(context.Background(), "r1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting r1: %v", err)
	}
	status, _, _ := unstructured.NestedString(obj.Object, "status", "instances", "ms-a", "lastReportedStatus")
	if status != "terminated" {
		t.Errorf("status.instances.ms-a.lastReportedStatus = %q, want %q", status, "terminated")
	}
}

// TestKillEvictsLegacyTypeMiniServiceInstance is a regression test: a legacy
// instance record with type: "MiniService" and no instanceType field must
// still be deleted as a MiniService, not defaulted to a Pod delete (which
// would silently mark it terminated without ever touching the real
// MiniService object).
func TestKillEvictsLegacyTypeMiniServiceInstance(t *testing.T) {
	cr := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "nvca.nvcf.nvidia.io/v2beta1",
		"kind":       "ICMSRequest",
		"metadata": map[string]interface{}{
			"namespace":  testRequestsNS,
			"name":       "r1",
			"finalizers": []interface{}{"nvca.finalizers.nvidia.io"},
		},
		"spec": map[string]interface{}{
			"functionDetails": map[string]interface{}{
				"functionId":        "fn-1",
				"functionVersionId": "v1",
			},
		},
		"status": map[string]interface{}{
			"requestStatus": statusCompleted,
			"instances": map[string]interface{}{
				"ms-a": map[string]interface{}{
					"id":     "ms-a",
					"type":   "MiniService",
					"status": "Running",
				},
			},
		},
	}}
	ms := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "nvca.nvcf.nvidia.io/v1alpha1",
		"kind":       "MiniService",
		"metadata":   map[string]interface{}{"name": "ms-a"},
	}}
	m, dc, _ := newFakeMaintainer([]runtime.Object{defaultBackend(), cr, ms}, nil)
	dc.PrependReactor("delete", "icmsrequests", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, nil
	})

	_, err := m.KillFunction(context.Background(), "fn-1", "v1", KillOptions{
		BackendNS: testBackendNS,
		Timeout:   5 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected an error reporting the request is still terminating (the fake finalizer never actually clears)")
	}

	if _, err := dc.Resource(miniServiceGVR).Get(context.Background(), "ms-a", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("MiniService ms-a should have been deleted via the legacy type field, got err=%v", err)
	}
}

// TestKillStopsOnEvictionFailureInsteadOfReportingSuccess is a regression
// test: if evicting an instance fails (e.g. RBAC denies the Pod delete), the
// CLI must not proceed to delete the ICMSRequest CR and report success or
// terminating. With --force in particular, proceeding would strip the
// finalizer and remove the CR while the pod keeps running.
func TestKillStopsOnEvictionFailureInsteadOfReportingSuccess(t *testing.T) {
	cr := icmsRequestWithFinalizers(testRequestsNS, "r1", "fn-1", "v1", "nvca.finalizers.nvidia.io")
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "inst-a", Namespace: testRequestsNS}}
	m, dc, cs := newFakeMaintainer([]runtime.Object{defaultBackend(), cr}, []runtime.Object{pod})
	cs.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(corev1.Resource("pods"), "inst-a", errors.New("denied"))
	})

	res, err := m.KillFunction(context.Background(), "fn-1", "v1", KillOptions{
		BackendNS: testBackendNS,
		Force:     true,
	})
	if err == nil {
		t.Fatal("expected an error: eviction failed, so the CR must not be reported as killed")
	}
	if res.FailedCount != 1 || res.TerminatingCount != 0 {
		t.Fatalf("FailedCount/TerminatingCount = %d/%d, want 1/0", res.FailedCount, res.TerminatingCount)
	}
	if len(res.Affected) != 1 || res.Affected[0].Error == "" || res.Affected[0].Terminating {
		t.Fatalf("affected = %+v, want a single non-terminating entry carrying the eviction error", res.Affected)
	}
	if !icmsExists(t, dc, testRequestsNS, "r1") {
		t.Error("r1 must still exist: --force must not strip the finalizer when eviction itself failed")
	}
	if _, err := cs.CoreV1().Pods(testRequestsNS).Get(context.Background(), "inst-a", metav1.GetOptions{}); err != nil {
		t.Errorf("pod inst-a should still exist (delete was denied), got err=%v", err)
	}
}

// TestKillPropagatesMalformedInstanceStatusError is a regression test: a
// status.instances value that is not a map (corrupt/unexpected data) must
// surface as an error from evictInstances, not be silently treated as "no
// instances to evict."
func TestKillPropagatesMalformedInstanceStatusError(t *testing.T) {
	cr := icmsRequestWithFinalizers(testRequestsNS, "r1", "fn-1", "v1", "nvca.finalizers.nvidia.io")
	// status.instances must be a map[string]InstanceStatus; force it to a
	// string to simulate corrupt/unexpected data shape.
	if err := unstructured.SetNestedField(cr.Object, "not-a-map", "status", "instances"); err != nil {
		t.Fatalf("seeding malformed status.instances: %v", err)
	}
	m, dc, _ := newFakeMaintainer([]runtime.Object{defaultBackend(), cr}, nil)

	res, err := m.KillFunction(context.Background(), "fn-1", "v1", KillOptions{
		BackendNS: testBackendNS,
		Force:     true,
	})
	if err == nil {
		t.Fatal("expected an error: malformed status.instances must not be silently ignored")
	}
	if res.FailedCount != 1 {
		t.Fatalf("FailedCount = %d, want 1", res.FailedCount)
	}
	if !icmsExists(t, dc, testRequestsNS, "r1") {
		t.Error("r1 must still exist: eviction must fail closed on malformed data, not proceed to delete")
	}
}

func TestKillNegativeTimeoutRejected(t *testing.T) {
	m, dc, _ := newFakeMaintainer(killSeed(), nil)

	_, err := m.KillAll(context.Background(), KillOptions{BackendNS: testBackendNS, Timeout: -1 * time.Second})
	if err == nil {
		t.Fatal("expected an error for a negative --timeout")
	}
	if !strings.Contains(err.Error(), "negative") {
		t.Errorf("error = %q, want it to mention the timeout must not be negative", err.Error())
	}
	if !icmsExists(t, dc, testRequestsNS, "r1") {
		t.Error("KillAll must not delete anything when --timeout validation fails")
	}
}

// simulatedDeleteError is a typed error a delete reactor can inject, so tests
// can confirm the aggregate error returned by Kill* still lets a caller reach
// the original cause via errors.As instead of only a flattened string.
type simulatedDeleteError struct{ detail string }

func (e *simulatedDeleteError) Error() string { return "simulated delete failure: " + e.detail }

// TestKillAggregateErrorWrapsUnderlyingCause is a regression test: the
// aggregate error from a partial kill failure must still let
// errors.As reach the original per-item error, not just a summary string.
func TestKillAggregateErrorWrapsUnderlyingCause(t *testing.T) {
	m, dc, _ := newFakeMaintainer(killSeed(), nil)
	want := &simulatedDeleteError{detail: "r2"}
	dc.PrependReactor("delete", "icmsrequests", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if da, ok := action.(k8stesting.DeleteAction); ok && da.GetName() == "r2" {
			return true, nil, want
		}
		return false, nil, nil
	})

	_, err := m.KillAll(context.Background(), KillOptions{BackendNS: testBackendNS})
	if err == nil {
		t.Fatal("expected aggregate error on partial failure")
	}
	var got *simulatedDeleteError
	if !errors.As(err, &got) {
		t.Fatalf("errors.As could not find the underlying cause in: %v", err)
	}
	if got != want {
		t.Errorf("recovered cause = %+v, want %+v", got, want)
	}
}

// TestKillTimeoutShorterThanPollIntervalIsHonored is a regression test: the
// deletion wait must not sleep through a poll interval longer than the
// configured --timeout before reporting Terminating. Uses a long poll
// interval and a short timeout, and asserts the call returns well within the
// poll interval.
func TestKillTimeoutShorterThanPollIntervalIsHonored(t *testing.T) {
	orig := killDeletionPollInterval
	killDeletionPollInterval = time.Minute
	t.Cleanup(func() { killDeletionPollInterval = orig })

	cr := icmsRequestWithFinalizers(testRequestsNS, "r1", "fn-1", "v1", "nvca.finalizers.nvidia.io")
	m, dc, _ := newFakeMaintainer([]runtime.Object{defaultBackend(), cr}, nil)
	dc.PrependReactor("delete", "icmsrequests", func(action k8stesting.Action) (bool, runtime.Object, error) {
		// Delete is accepted but the object is never actually removed,
		// simulating a finalizer the fake tracker can't model natively.
		return true, nil, nil
	})

	const timeout = 10 * time.Millisecond
	// Generous scheduling tolerance so this doesn't flake under CI load, but
	// still tight enough to prove the wait tracks --timeout rather than the
	// 1-minute killDeletionPollInterval: prior to the fix this took the full
	// poll interval to return.
	const tolerance = 2 * time.Second

	start := time.Now()
	res, err := m.KillFunction(context.Background(), "fn-1", "v1", KillOptions{
		BackendNS: testBackendNS,
		Timeout:   timeout,
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error reporting the request is still terminating")
	}
	if res.TerminatingCount != 1 {
		t.Fatalf("TerminatingCount = %d, want 1", res.TerminatingCount)
	}
	if elapsed >= timeout+tolerance {
		t.Errorf("elapsed = %s, want close to the configured --timeout of %s (+%s tolerance): the wait must be bounded by --timeout, not the poll interval", elapsed, timeout, tolerance)
	}
}

// TestKillClassifiesOnlyLocalDeadlineAsTerminating is a regression test: a
// context.DeadlineExceeded-shaped error from the Get call must only be
// treated as "still terminating" when it actually came from
// waitForICMSRequestGone's own synthetic per-Get deadline. An unrelated
// transport/client-level timeout that happens to produce the same error
// shape, well before that deadline, must still surface as a real error
// instead of being silently reported as a successful (if incomplete)
// termination wait.
func TestKillClassifiesOnlyLocalDeadlineAsTerminating(t *testing.T) {
	cr := icmsRequestWithFinalizers(testRequestsNS, "r1", "fn-1", "v1", "nvca.finalizers.nvidia.io")
	m, dc, _ := newFakeMaintainer([]runtime.Object{defaultBackend(), cr}, nil)
	dc.PrependReactor("delete", "icmsrequests", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, nil
	})
	dc.PrependReactor("get", "icmsrequests", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if ga, ok := action.(k8stesting.GetAction); ok && ga.GetName() == "r1" {
			// Simulate a spurious client/transport timeout unrelated to our
			// own deadline: it arrives immediately, long before the
			// generous Timeout below could have elapsed.
			return true, nil, context.DeadlineExceeded
		}
		return false, nil, nil
	})

	_, err := m.KillFunction(context.Background(), "fn-1", "v1", KillOptions{
		BackendNS: testBackendNS,
		Timeout:   time.Hour,
	})
	if err == nil {
		t.Fatal("expected the spurious Get error to surface as a real failure")
	}
	if strings.Contains(err.Error(), "still terminating") {
		t.Errorf("a spurious transport timeout must not be misreported as the deletion deadline elapsing, got: %v", err)
	}
}

// TestKillPreservesUnrelatedErrorRacingWithLocalDeadline is a regression
// test for the inverse edge case: even when our own synthetic deadline has
// genuinely elapsed (a vanishingly small Timeout guarantees getCtx.Err() ==
// DeadlineExceeded by the time it's checked), an unrelated error returned by
// the same Get call (e.g. Forbidden) must not be discarded and silently
// replaced with a "still terminating" result. Both localDeadlineExceeded and
// errors.Is(err, context.DeadlineExceeded) must hold before that happens.
func TestKillPreservesUnrelatedErrorRacingWithLocalDeadline(t *testing.T) {
	cr := icmsRequestWithFinalizers(testRequestsNS, "r1", "fn-1", "v1", "nvca.finalizers.nvidia.io")
	m, dc, _ := newFakeMaintainer([]runtime.Object{defaultBackend(), cr}, nil)
	dc.PrependReactor("delete", "icmsrequests", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, nil
	})
	wantErr := errors.New("forbidden")
	dc.PrependReactor("get", "icmsrequests", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if ga, ok := action.(k8stesting.GetAction); ok && ga.GetName() == "r1" {
			return true, nil, wantErr
		}
		return false, nil, nil
	})

	_, err := m.KillFunction(context.Background(), "fn-1", "v1", KillOptions{
		BackendNS: testBackendNS,
		// A vanishingly small timeout: our own getCtx deadline will have
		// elapsed by the time we check getCtx.Err(), but the reactor's
		// "forbidden" error has nothing to do with that deadline.
		Timeout: time.Nanosecond,
	})
	if err == nil {
		t.Fatal("expected the unrelated Get error to surface")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("expected errors.Is to reach the original cause (%v), got: %v", wantErr, err)
	}
	if strings.Contains(err.Error(), "still terminating") {
		t.Errorf("an unrelated error racing with the local deadline must not be misreported as terminating, got: %v", err)
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
