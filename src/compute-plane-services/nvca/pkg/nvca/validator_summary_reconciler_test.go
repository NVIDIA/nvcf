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

package nvca

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/clustervalidator"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
)

func newTestMetrics(t *testing.T) (*metrics.Metrics, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	ctx := metrics.WithDefaultMetrics(context.Background(),
		"nca-test", "test-cluster", "test-group", "v1.0.0",
		metrics.WithRegisterer(reg))
	m := metrics.FromContext(ctx)
	require.NotNil(t, m)
	t.Cleanup(func() { m.Destroy() })
	return m, reg
}

func testReconcilerLogger() *logrus.Entry {
	l := logrus.New()
	l.SetLevel(logrus.DebugLevel)
	return logrus.NewEntry(l)
}

// summaryCM builds a ConfigMap carrying a serialized ValidatorSummary.
// Returns the summary too so tests can compare expectations.
func summaryCM(t *testing.T, namespace, ranAt string, mutate func(s *clustervalidator.ValidatorSummary)) (*corev1.ConfigMap, *clustervalidator.ValidatorSummary) {
	t.Helper()
	s := &clustervalidator.ValidatorSummary{
		SchemaVersion:   clustervalidator.SummarySchemaVersion,
		RanAt:           ranAt,
		DurationSeconds: 12.3,
		Verdict:         "NVCF-Ready",
		VerdictReady:    true,
		Checks: map[string]bool{
			clustervalidator.CheckKeyControlPlane:           true,
			clustervalidator.CheckKeyWorkerNodesAllReady:    true,
			clustervalidator.CheckKeyWebhooks:               true,
			clustervalidator.CheckKeyNetworkPoliciesSupport: true,
			clustervalidator.CheckKeySMBCSI:                 true,
			clustervalidator.CheckKeyGPUResources:           true,
			clustervalidator.CheckKeyGPUOperator:            true,
		},
	}
	if mutate != nil {
		mutate(s)
	}
	payload, err := serializeSummary(s)
	require.NoError(t, err)
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clustervalidator.SummaryConfigMapName,
			Namespace: namespace,
		},
		Data: map[string]string{clustervalidator.SummaryConfigMapKey: payload},
	}, s
}

func serializeSummary(s *clustervalidator.ValidatorSummary) (string, error) {
	bb, err := json.Marshal(s)
	return string(bb), err
}

// EdgeCase 1: agent boots before any validator run; ConfigMap doesn't exist.
// Expectation: Start() returns nil, metrics stay at init-to-zero baseline,
// metrics stay at the init-to-zero baseline.
func TestReconciler_StartsCleanlyWhenConfigMapAbsent(t *testing.T) {
	m, reg := newTestMetrics(t)
	client := fake.NewSimpleClientset() // no CMs

	r := NewValidatorSummaryReconciler(client, "nvca-operator", m)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, r.Start(ctx))

	// Cluster-validator gauges should all exist at the init-to-zero baseline.
	val, ok := readGaugeLabels(t, reg, metrics.ClusterValidatorReadyMetricName, nil)
	require.True(t, ok)
	assert.Equal(t, 0.0, val)
	val, ok = readGaugeLabels(t, reg, metrics.ClusterValidatorLastRunTimestampMetricName, nil)
	require.True(t, ok)
	assert.Equal(t, 0.0, val)
}

// EdgeCase 2: agent restart after a successful run — the ConfigMap already
// exists. The informer's initial List delivers an Add event; metrics
// should reflect the existing summary immediately on Start() returning.
func TestReconciler_BootstrapsFromExistingConfigMap(t *testing.T) {
	m, reg := newTestMetrics(t)
	cm, _ := summaryCM(t, "nvca-operator", "2026-06-11T10:00:00Z", nil)
	client := fake.NewSimpleClientset(cm)

	r := NewValidatorSummaryReconciler(client, "nvca-operator", m)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, r.Start(ctx))

	// Need to give the informer one tick to deliver the synthetic Add
	// event from its initial List.
	require.Eventually(t, func() bool {
		v, ok := readGaugeLabels(t, reg, metrics.ClusterValidatorReadyMetricName, nil)
		return ok && v == 1.0
	}, 5*time.Second, 50*time.Millisecond,
		"agent restart must republish metrics from the existing CM")
}

// EdgeCase 3: validator pod panics mid-run (or CM was just never updated).
// No CM event fires; metrics stay at whatever they were before.
// This is tested via "ensure setting metrics once and then doing nothing
// leaves them in place".
func TestReconciler_StaleDataPersistsUntilNextUpdate(t *testing.T) {
	m, reg := newTestMetrics(t)
	// Directly drive the metric setter (no informer needed for this case).
	m.SetClusterValidatorSummary(&metrics.ClusterValidatorSummary{RanAtUnixSec: 1718100000,
		DurationSeconds: 12.3,
		VerdictReady:    true,
		Checks:          map[string]bool{clustervalidator.CheckKeyControlPlane: true},
	})

	// Simulate "nothing happens for a while" — no further reconcile events.
	// The series must still be present and unchanged: gauges hold their last
	// Set() value until the next update, so stale-but-present is the expected
	// behavior (genuine staleness is caught by the last_run_timestamp alert,
	// not by the series disappearing).
	val, ok := readGaugeLabels(t, reg, metrics.ClusterValidatorReadyMetricName, nil)
	require.True(t, ok, "the Ready series must persist when no further event fires")
	assert.Equal(t, 1.0, val, "value must remain at the last-set reading")

	val, ok = readGauge(t, reg, metrics.ClusterValidatorCheckStatusMetricName, "check", clustervalidator.CheckKeyControlPlane)
	require.True(t, ok, "the run's check_status series must persist")
	assert.Equal(t, 1.0, val)
}

// EdgeCase 4: customer deletes the summary ConfigMap. The reconciler must
// PRESERVE last-known-good metrics — an accidental delete must not wipe the
// SLI. (Reset is explicit only; see TestReconciler_ResetsOnResetConfigMap.)
func TestReconciler_PreservesMetricsOnConfigMapDelete(t *testing.T) {
	m, reg := newTestMetrics(t)
	// Pre-populate metrics with a run.
	m.SetClusterValidatorSummary(&metrics.ClusterValidatorSummary{RanAtUnixSec: 1718100000,
		DurationSeconds: 12.3,
		VerdictReady:    true,
		Checks:          map[string]bool{clustervalidator.CheckKeyControlPlane: true},
		Endpoints:       map[string]metrics.ClusterValidatorEndpoint{"NGC API": {Reachable: true, Critical: true}},
	})

	// Simulate the informer Delete event.
	r := &ValidatorSummaryReconciler{metrics: m}
	r.onDelete(testReconcilerLogger(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: clustervalidator.SummaryConfigMapName, Namespace: "nvca-operator"},
	})

	// The last-known-good series must still be present.
	val, ok := readGaugeLabels(t, reg, metrics.ClusterValidatorReadyMetricName, nil)
	require.True(t, ok, "deleting the summary CM must NOT prune the last-known-good series")
	assert.Equal(t, 1.0, val, "last-known-good Ready value must be preserved on delete")
}

// EdgeCase 5: tombstone (cache.DeletedFinalStateUnknown) — informer dropped
// intermediate events. Treated identically to a normal Delete: preserve.
func TestReconciler_PreservesMetricsOnTombstoneDelete(t *testing.T) {
	m, reg := newTestMetrics(t)
	m.SetClusterValidatorSummary(&metrics.ClusterValidatorSummary{RanAtUnixSec: 1718100000,
		DurationSeconds: 12.3,
		VerdictReady:    true,
		Checks:          map[string]bool{clustervalidator.CheckKeyControlPlane: true},
	})

	r := &ValidatorSummaryReconciler{metrics: m}
	r.onDelete(testReconcilerLogger(), cache.DeletedFinalStateUnknown{
		Key: "nvca-operator/cluster-validator-summary",
	})

	_, ok := readGaugeLabels(t, reg, metrics.ClusterValidatorReadyMetricName, nil)
	assert.True(t, ok, "tombstone delete must preserve last-known-good, same as a real Delete")
}

// EdgeCase 4b: explicit reset. Creating the reset ConfigMap clears the metrics
// to baseline, and the reconciler consumes (deletes) that ConfigMap so the
// one-shot signal doesn't re-fire.
func TestReconciler_ResetsOnResetConfigMap(t *testing.T) {
	m, reg := newTestMetrics(t)
	m.SetClusterValidatorSummary(&metrics.ClusterValidatorSummary{RanAtUnixSec: 1718100000,
		DurationSeconds: 12.3,
		VerdictReady:    true,
		Checks:          map[string]bool{clustervalidator.CheckKeyControlPlane: true},
	})

	resetCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: clustervalidator.SummaryResetConfigMapName, Namespace: "nvca-operator"},
	}
	cs := fake.NewSimpleClientset(resetCM)
	r := &ValidatorSummaryReconciler{
		client:             cs,
		namespace:          "nvca-operator",
		resetConfigMapName: clustervalidator.SummaryResetConfigMapName,
		metrics:            m,
	}

	r.onResetSignal(context.Background(), testReconcilerLogger(), resetCM)

	// Metrics reset to the init-to-zero baseline (series stays present at 0).
	val, ok := readGaugeLabels(t, reg, metrics.ClusterValidatorReadyMetricName, nil)
	require.True(t, ok, "baseline Ready series must be re-emitted after reset")
	assert.Equal(t, 0.0, val, "explicit reset must return Ready to the zero baseline")

	// Reset ConfigMap consumed (deleted).
	_, err := cs.CoreV1().ConfigMaps("nvca-operator").Get(
		context.Background(), clustervalidator.SummaryResetConfigMapName, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "reset ConfigMap must be deleted (consumed) after reset")
}

// EdgeCase 6: ConfigMap exists but the JSON is malformed (truncated write,
// schema drift, etc.). The reconciler must NOT zero out metrics — it
// must preserve last-known-good so a transient blip doesn't fire SLI alerts.
func TestReconciler_PreservesLastGoodOnParseError(t *testing.T) {
	m, reg := newTestMetrics(t)
	// Set a known-good state first.
	m.SetClusterValidatorSummary(&metrics.ClusterValidatorSummary{RanAtUnixSec: 1718100000,
		DurationSeconds: 12.3,
		VerdictReady:    true,
		Checks:          map[string]bool{clustervalidator.CheckKeyControlPlane: true},
	})

	// Feed a malformed CM through onUpsert directly.
	r := &ValidatorSummaryReconciler{metrics: m}
	r.onUpsert(testReconcilerLogger(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: clustervalidator.SummaryConfigMapName, Namespace: "nvca-operator"},
		Data:       map[string]string{clustervalidator.SummaryConfigMapKey: "{not json"},
	})

	// Last-good series must STILL be present.
	val, ok := readGaugeLabels(t, reg, metrics.ClusterValidatorReadyMetricName, nil)
	require.True(t, ok, "parse failure must not delete last-good Ready series")
	assert.Equal(t, 1.0, val)
}

// EdgeCase 7: schema version drift — same expectation as parse error.
func TestReconciler_PreservesLastGoodOnSchemaVersionMismatch(t *testing.T) {
	m, reg := newTestMetrics(t)
	m.SetClusterValidatorSummary(&metrics.ClusterValidatorSummary{RanAtUnixSec: 1718100000,
		DurationSeconds: 12.3,
		VerdictReady:    true,
		Checks:          map[string]bool{clustervalidator.CheckKeyControlPlane: true},
	})

	r := &ValidatorSummaryReconciler{metrics: m}
	r.onUpsert(testReconcilerLogger(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: clustervalidator.SummaryConfigMapName, Namespace: "nvca-operator"},
		Data: map[string]string{
			clustervalidator.SummaryConfigMapKey: `{"schemaVersion":"vFUTURE","ranAt":"2026-06-11T13:00:00Z"}`,
		},
	})

	val, ok := readGaugeLabels(t, reg, metrics.ClusterValidatorReadyMetricName, nil)
	require.True(t, ok, "schema-version drift must not delete last-good Ready series")
	assert.Equal(t, 1.0, val, "metrics must hold last-known-good (unsupported summary not applied)")
}

// EdgeCase 8: each new run updates the fixed gauges in place, and config-driven
// series (endpoints) removed between runs are pruned so they don't leak forever.
func TestReconciler_PrunesOldRunOnUpdate(t *testing.T) {
	m, reg := newTestMetrics(t)

	// First run.
	m.SetClusterValidatorSummary(&metrics.ClusterValidatorSummary{RanAtUnixSec: 1718100000,
		DurationSeconds: 12.3,
		VerdictReady:    true,
		Checks: map[string]bool{
			clustervalidator.CheckKeyControlPlane: true,
		},
		Endpoints: map[string]metrics.ClusterValidatorEndpoint{
			"Vault": {Reachable: true, Critical: false},
		},
	})

	// Second run with a different timestamp + endpoint "Vault" replaced
	// by "NGC API".
	m.SetClusterValidatorSummary(&metrics.ClusterValidatorSummary{RanAtUnixSec: 1718110800,
		DurationSeconds: 13.4,
		VerdictReady:    true,
		Checks: map[string]bool{
			clustervalidator.CheckKeyControlPlane: true,
		},
		Endpoints: map[string]metrics.ClusterValidatorEndpoint{
			"NGC API": {Reachable: true, Critical: true},
		},
	})

	// Ready is a single fixed series updated in place to the latest run's value.
	val, ok := readGaugeLabels(t, reg, metrics.ClusterValidatorReadyMetricName, nil)
	require.True(t, ok, "the Ready series must be present after the second run")
	assert.Equal(t, 1.0, val)
	// "Vault" endpoint must be gone (removed from config between runs).
	_, ok = readGaugeLabels(t, reg, metrics.ClusterValidatorEndpointReachableMetricName, map[string]string{
		"endpoint": "Vault",
		"critical": "false",
	})
	assert.False(t, ok, "removed endpoint must be pruned")
	// "NGC API" must be present after the second run.
	val, ok = readGaugeLabels(t, reg, metrics.ClusterValidatorEndpointReachableMetricName, map[string]string{
		"endpoint": "NGC API",
		"critical": "true",
	})
	require.True(t, ok)
	assert.Equal(t, 1.0, val)
}

// resolveValidatorSummaryNamespace prefers the injected env var and
// otherwise falls back to the default install namespace. It must NEVER
// consult the agent's own namespace (SA mount / POD_NAMESPACE), since the
// summary lives in the operator/validator namespace.
func TestResolveValidatorSummaryNamespace(t *testing.T) {
	t.Run("env var wins", func(t *testing.T) {
		t.Setenv(clustervalidator.SummaryConfigMapNamespaceEnv, "custom-operator-ns")
		assert.Equal(t, "custom-operator-ns", resolveValidatorSummaryNamespace())
	})

	t.Run("falls back to default when env unset", func(t *testing.T) {
		t.Setenv(clustervalidator.SummaryConfigMapNamespaceEnv, "")
		assert.Equal(t, defaultValidatorSummaryNamespace, resolveValidatorSummaryNamespace())
	})

	t.Run("POD_NAMESPACE is intentionally ignored", func(t *testing.T) {
		// POD_NAMESPACE conventionally means the agent's OWN namespace.
		// Honoring it here would reintroduce the namespace-mismatch bug,
		// so the resolver must ignore it entirely.
		t.Setenv(clustervalidator.SummaryConfigMapNamespaceEnv, "")
		t.Setenv("POD_NAMESPACE", "nvca-system")
		assert.Equal(t, defaultValidatorSummaryNamespace, resolveValidatorSummaryNamespace(),
			"POD_NAMESPACE (agent's own ns) must not influence the summary watch namespace")
	})
}

// ---------------------------------------------------------------------------
// Helpers — gauge inspection via the Prometheus registry
// ---------------------------------------------------------------------------

func readGauge(t *testing.T, reg *prometheus.Registry, metricName, labelName, labelValue string) (float64, bool) {
	return readGaugeLabels(t, reg, metricName, map[string]string{labelName: labelValue})
}

func readGaugeLabels(t *testing.T, reg *prometheus.Registry, metricName string, mustMatch map[string]string) (float64, bool) {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != metricName {
			continue
		}
		for _, m := range mf.Metric {
			ok := true
			for needName, needValue := range mustMatch {
				found := false
				for _, l := range m.Label {
					if l.GetName() == needName && l.GetValue() == needValue {
						found = true
						break
					}
				}
				if !found {
					ok = false
					break
				}
			}
			if ok {
				return m.GetGauge().GetValue(), true
			}
		}
	}
	return 0, false
}

func TestNetpolDirectionsForMetrics(t *testing.T) {
	t.Run("empty Directions yields no series (unevaluated, e.g. missing namespace)", func(t *testing.T) {
		// The validator omits directions it could not evaluate. Synthesizing
		// zero-valued series here would misreport a policy block on every side,
		// so the mapping must stay empty and the metric absent for the pair.
		got := netpolDirectionsForMetrics(clustervalidator.PairStatus{Passed: false})
		assert.Empty(t, got, "unevaluated pair must not synthesize direction series")
	})

	t.Run("populated Directions are mapped through faithfully", func(t *testing.T) {
		got := netpolDirectionsForMetrics(clustervalidator.PairStatus{
			Passed: false,
			Directions: map[string]clustervalidator.DirectionStatus{
				clustervalidator.NetpolDirectionAToB: {EgressAllowed: true, IngressAllowed: false},
			},
		})
		require.Len(t, got, 1)
		assert.True(t, got[clustervalidator.NetpolDirectionAToB].EgressAllowed)
		assert.False(t, got[clustervalidator.NetpolDirectionAToB].IngressAllowed)
	})
}
