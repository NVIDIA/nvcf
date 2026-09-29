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

package metrics

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pylonv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/api/v1alpha1"
)

const endpointsHeader = `
# HELP nvcf_pylon_operator_endpoints Number of InferenceEndpoints by condition type and status.
# TYPE nvcf_pylon_operator_endpoints gauge
`

func endpointsSeries(values map[string]int) string {
	var b strings.Builder
	b.WriteString(endpointsHeader)
	for _, c := range []string{"Ready", "Registered", "TransportReady"} {
		for _, s := range []string{"False", "True", "Unknown"} {
			fmt.Fprintf(&b, "nvcf_pylon_operator_endpoints{condition=%q,status=%q} %d\n", c, s, values[c+"="+s])
		}
	}
	return b.String()
}

func conditions(ready, transport, registered metav1.ConditionStatus) []metav1.Condition {
	return []metav1.Condition{
		{Type: string(pylonv1alpha1.ConditionReady), Status: ready},
		{Type: string(pylonv1alpha1.ConditionTransportReady), Status: transport},
		{Type: string(pylonv1alpha1.ConditionRegistered), Status: registered},
		{Type: "SomethingElse", Status: metav1.ConditionTrue},
	}
}

func TestPreInitialised(t *testing.T) {
	m := New()
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, m.Register(reg))

	assert.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(endpointsSeries(nil)), "nvcf_pylon_operator_endpoints"))
	assert.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(`
# HELP nvcf_pylon_operator_probe_failures_total Health probes that left Ready False, by reason.
# TYPE nvcf_pylon_operator_probe_failures_total counter
nvcf_pylon_operator_probe_failures_total{reason="HealthProbeFailed"} 0
nvcf_pylon_operator_probe_failures_total{reason="ModelNameMismatch"} 0
nvcf_pylon_operator_probe_failures_total{reason="NoReadyEndpoints"} 0
nvcf_pylon_operator_probe_failures_total{reason="ServiceNotFound"} 0
`), "nvcf_pylon_operator_probe_failures_total"))
	count, err := testutil.GatherAndCount(reg, "nvcf_pylon_operator_probe_duration_seconds")
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	count, err = testutil.GatherAndCount(reg, "nvcf_pylon_operator_transport_replicas_ready")
	require.NoError(t, err)
	assert.Zero(t, count, "per-endpoint series appear on the first reconcile")
	count, err = testutil.GatherAndCount(reg, "nvcf_pylon_operator_registered")
	require.NoError(t, err)
	assert.Zero(t, count, "per-endpoint series appear on the first reconcile")
	count, err = testutil.GatherAndCount(reg, "nvcf_pylon_operator_time_to_registered_seconds")
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	assert.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(scrapeFailuresHeader+`
nvcf_pylon_operator_scrape_failures_total{reason="connect"} 0
nvcf_pylon_operator_scrape_failures_total{reason="parse"} 0
nvcf_pylon_operator_scrape_failures_total{reason="status"} 0
nvcf_pylon_operator_scrape_failures_total{reason="timeout"} 0
`), "nvcf_pylon_operator_scrape_failures_total"))
}

const scrapeFailuresHeader = `
# HELP nvcf_pylon_operator_scrape_failures_total Failed scrapes of transport pod metrics, by reason.
# TYPE nvcf_pylon_operator_scrape_failures_total counter
`

func TestRegisteredGauge(t *testing.T) {
	m := New()
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, m.Register(reg))

	m.SetEndpointConditions("models/a", conditions(metav1.ConditionTrue, metav1.ConditionTrue, metav1.ConditionTrue))
	m.SetEndpointConditions("models/b", conditions(metav1.ConditionTrue, metav1.ConditionFalse, metav1.ConditionFalse))
	m.SetEndpointConditions("models/c", nil)
	const header = `
# HELP nvcf_pylon_operator_registered 1 when the Registered condition of the InferenceEndpoint is True, else 0, by endpoint namespace/name.
# TYPE nvcf_pylon_operator_registered gauge
`
	assert.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(header+`
nvcf_pylon_operator_registered{endpoint="models/a"} 1
nvcf_pylon_operator_registered{endpoint="models/b"} 0
nvcf_pylon_operator_registered{endpoint="models/c"} 0
`), "nvcf_pylon_operator_registered"))

	m.SetEndpointConditions("models/a", conditions(metav1.ConditionTrue, metav1.ConditionTrue, metav1.ConditionUnknown))
	m.ForgetEndpoint("models/b")
	m.ForgetEndpoint("models/c")
	assert.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(header+`
nvcf_pylon_operator_registered{endpoint="models/a"} 0
`), "nvcf_pylon_operator_registered"))
}

func TestRegistrationObserverMetrics(t *testing.T) {
	m := New()
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, m.Register(reg))

	m.ObserveTimeToRegistered(45 * time.Second)
	m.IncScrapeFailure(ScrapeFailureTimeout)
	m.IncScrapeFailure(ScrapeFailureTimeout)
	m.IncScrapeFailure(ScrapeFailureParse)

	assert.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(scrapeFailuresHeader+`
nvcf_pylon_operator_scrape_failures_total{reason="connect"} 0
nvcf_pylon_operator_scrape_failures_total{reason="parse"} 1
nvcf_pylon_operator_scrape_failures_total{reason="status"} 0
nvcf_pylon_operator_scrape_failures_total{reason="timeout"} 2
`), "nvcf_pylon_operator_scrape_failures_total"))
	families, err := reg.Gather()
	require.NoError(t, err)
	var found bool
	for _, f := range families {
		if f.GetName() == "nvcf_pylon_operator_time_to_registered_seconds" {
			found = true
			h := f.GetMetric()[0].GetHistogram()
			assert.Equal(t, uint64(1), h.GetSampleCount())
			assert.InDelta(t, 45.0, h.GetSampleSum(), 1e-9)
		}
	}
	assert.True(t, found)
}

func TestTransportReplicasReady(t *testing.T) {
	m := New()
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, m.Register(reg))

	m.SetTransportReplicasReady("models/a", 1)
	m.SetTransportReplicasReady("models/b", 0)
	m.SetTransportReplicasReady("models/a", 2)
	const header = `
# HELP nvcf_pylon_operator_transport_replicas_ready Ready transport pods of each InferenceEndpoint, by endpoint namespace/name.
# TYPE nvcf_pylon_operator_transport_replicas_ready gauge
`
	assert.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(header+`
nvcf_pylon_operator_transport_replicas_ready{endpoint="models/a"} 2
nvcf_pylon_operator_transport_replicas_ready{endpoint="models/b"} 0
`), "nvcf_pylon_operator_transport_replicas_ready"))

	// Forgetting an endpoint deletes its series, even one without
	// conditions.
	m.SetEndpointConditions("models/a", conditions(metav1.ConditionTrue, metav1.ConditionTrue, metav1.ConditionTrue))
	m.ForgetEndpoint("models/a")
	m.ForgetEndpoint("models/b")
	count, err := testutil.GatherAndCount(reg, "nvcf_pylon_operator_transport_replicas_ready")
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestRegisterTwiceFails(t *testing.T) {
	reg := prometheus.NewRegistry()
	require.NoError(t, New().Register(reg))
	assert.Error(t, New().Register(reg))
}

func TestEndpointConditions(t *testing.T) {
	m := New()
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, m.Register(reg))

	m.SetEndpointConditions("models/a", conditions(metav1.ConditionTrue, metav1.ConditionUnknown, metav1.ConditionUnknown))
	m.SetEndpointConditions("models/b", conditions(metav1.ConditionFalse, metav1.ConditionUnknown, metav1.ConditionUnknown))
	m.SetEndpointConditions("models/c", conditions(metav1.ConditionTrue, metav1.ConditionTrue, metav1.ConditionFalse))
	assert.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(endpointsSeries(map[string]int{
		"Ready=True": 2, "Ready=False": 1,
		"TransportReady=Unknown": 2, "TransportReady=True": 1,
		"Registered=Unknown": 2, "Registered=False": 1,
	})), "nvcf_pylon_operator_endpoints"))

	// Updating an endpoint replaces its previous state.
	m.SetEndpointConditions("models/b", conditions(metav1.ConditionTrue, metav1.ConditionUnknown, metav1.ConditionUnknown))
	m.ForgetEndpoint("models/c")
	m.ForgetEndpoint("models/unknown")
	assert.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(endpointsSeries(map[string]int{
		"Ready=True":             2,
		"TransportReady=Unknown": 2,
		"Registered=Unknown":     2,
	})), "nvcf_pylon_operator_endpoints"))

	// An endpoint without conditions counts nowhere.
	m.SetEndpointConditions("models/a", nil)
	m.ForgetEndpoint("models/b")
	assert.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(endpointsSeries(nil)), "nvcf_pylon_operator_endpoints"))
}

func TestProbeMetrics(t *testing.T) {
	m := New()
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, m.Register(reg))
	m.ObserveProbeDuration(20 * time.Millisecond)
	m.ObserveProbeDuration(2 * time.Second)
	m.IncProbeFailure(pylonv1alpha1.ReadyReasonHealthProbeFailed)

	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		switch f.GetName() {
		case "nvcf_pylon_operator_probe_duration_seconds":
			assert.Equal(t, uint64(2), f.GetMetric()[0].GetHistogram().GetSampleCount())
			assert.InDelta(t, 2.02, f.GetMetric()[0].GetHistogram().GetSampleSum(), 1e-9)
		case "nvcf_pylon_operator_probe_failures_total":
			for _, s := range f.GetMetric() {
				want := 0.0
				if s.GetLabel()[0].GetValue() == string(pylonv1alpha1.ReadyReasonHealthProbeFailed) {
					want = 1
				}
				assert.Equal(t, want, s.GetCounter().GetValue(), s.GetLabel()[0].GetValue())
			}
		}
	}
}

func TestNilMetricsIsANoop(t *testing.T) {
	var m *Metrics
	assert.NotPanics(t, func() {
		m.ObserveProbeDuration(time.Second)
		m.IncProbeFailure(pylonv1alpha1.ReadyReasonServiceNotFound)
		m.SetEndpointConditions("models/a", conditions(metav1.ConditionTrue, metav1.ConditionTrue, metav1.ConditionTrue))
		m.SetTransportReplicasReady("models/a", 1)
		m.ObserveTimeToRegistered(time.Second)
		m.IncScrapeFailure(ScrapeFailureConnect)
		m.ForgetEndpoint("models/a")
	})
}
