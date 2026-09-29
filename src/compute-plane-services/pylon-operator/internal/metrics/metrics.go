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

// Package metrics defines the operator's Prometheus metrics. main registers
// them on the controller-runtime registry, which the manager's metrics server
// serves; tests register them on a private registry.
package metrics

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pylonv1alpha1 "github.com/NVIDIA/nvcf/src/compute-plane-services/pylon-operator/api/v1alpha1"
)

const (
	namespace = "nvcf"
	subsystem = "pylon_operator"
)

// ScrapeFailureReason classifies a failed scrape of a transport pod's
// metrics.
type ScrapeFailureReason string

const (
	// ScrapeFailureConnect: the connection failed or broke.
	ScrapeFailureConnect ScrapeFailureReason = "connect"
	// ScrapeFailureTimeout: the scrape did not finish within its timeout.
	ScrapeFailureTimeout ScrapeFailureReason = "timeout"
	// ScrapeFailureStatus: the pod answered with a non-2xx status.
	ScrapeFailureStatus ScrapeFailureReason = "status"
	// ScrapeFailureParse: the body is not the Prometheus text format.
	ScrapeFailureParse ScrapeFailureReason = "parse"
)

// ScrapeFailureReasons lists every ScrapeFailureReason.
func ScrapeFailureReasons() []ScrapeFailureReason {
	return []ScrapeFailureReason{ScrapeFailureConnect, ScrapeFailureTimeout, ScrapeFailureStatus, ScrapeFailureParse}
}

// timeToRegisteredBuckets span a transport that registers within seconds of
// a warm backend to one that waits out a long model weight load.
var timeToRegisteredBuckets = []float64{1, 2, 5, 10, 30, 60, 120, 300, 600, 1200, 1800, 3600}

var conditionStatuses = []metav1.ConditionStatus{
	metav1.ConditionTrue,
	metav1.ConditionFalse,
	metav1.ConditionUnknown,
}

// Metrics holds the operator's collectors and the per-endpoint condition
// state behind the endpoints gauge. The zero value is not usable; call New.
// All methods are safe on a nil receiver, which disables recording.
type Metrics struct {
	endpoints              *prometheus.GaugeVec
	probeDuration          prometheus.Histogram
	probeFailures          *prometheus.CounterVec
	transportReplicasReady *prometheus.GaugeVec
	registered             *prometheus.GaugeVec
	timeToRegistered       prometheus.Histogram
	scrapeFailures         *prometheus.CounterVec

	mu         sync.Mutex
	conditions map[string]map[pylonv1alpha1.ConditionType]metav1.ConditionStatus
}

// New builds the collectors and pre-initialises every label combination to
// zero so the series exist on the first scrape.
func New() *Metrics {
	m := &Metrics{
		endpoints: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "endpoints",
			Help:      "Number of InferenceEndpoints by condition type and status.",
		}, []string{"condition", "status"}),
		probeDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "probe_duration_seconds",
			Help:      "Duration of backend health probes.",
			Buckets:   prometheus.DefBuckets,
		}),
		probeFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "probe_failures_total",
			Help:      "Health probes that left Ready False, by reason.",
		}, []string{"reason"}),
		// The endpoint label is namespace/name, bounded by the number of
		// InferenceEndpoints. A series appears on an endpoint's first
		// reconcile and is deleted with the endpoint, so it cannot be
		// pre-initialised.
		transportReplicasReady: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "transport_replicas_ready",
			Help:      "Ready transport pods of each InferenceEndpoint, by endpoint namespace/name.",
		}, []string{"endpoint"}),
		// Per endpoint like transportReplicasReady.
		registered: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "registered",
			Help:      "1 when the Registered condition of the InferenceEndpoint is True, else 0, by endpoint namespace/name.",
		}, []string{"endpoint"}),
		timeToRegistered: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "time_to_registered_seconds",
			Help:      "Time from InferenceEndpoint creation to its first Registered True, observed once per endpoint.",
			Buckets:   timeToRegisteredBuckets,
		}),
		scrapeFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "scrape_failures_total",
			Help:      "Failed scrapes of transport pod metrics, by reason.",
		}, []string{"reason"}),
		conditions: map[string]map[pylonv1alpha1.ConditionType]metav1.ConditionStatus{},
	}
	for _, c := range pylonv1alpha1.ConditionTypes() {
		for _, s := range conditionStatuses {
			m.endpoints.WithLabelValues(string(c), string(s)).Set(0)
		}
	}
	for _, r := range pylonv1alpha1.ReadyFailureReasons() {
		m.probeFailures.WithLabelValues(string(r)).Add(0)
	}
	for _, r := range ScrapeFailureReasons() {
		m.scrapeFailures.WithLabelValues(string(r)).Add(0)
	}
	return m
}

// Collectors returns every collector, for registration.
func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{
		m.endpoints, m.probeDuration, m.probeFailures, m.transportReplicasReady,
		m.registered, m.timeToRegistered, m.scrapeFailures,
	}
}

// Register registers every collector on reg.
func (m *Metrics) Register(reg prometheus.Registerer) error {
	for _, c := range m.Collectors() {
		if err := reg.Register(c); err != nil {
			return err
		}
	}
	return nil
}

// ObserveProbeDuration records the duration of one health probe.
func (m *Metrics) ObserveProbeDuration(d time.Duration) {
	if m == nil {
		return
	}
	m.probeDuration.Observe(d.Seconds())
}

// IncProbeFailure counts a probe that left Ready False with reason.
func (m *Metrics) IncProbeFailure(reason pylonv1alpha1.ReadyReason) {
	if m == nil {
		return
	}
	m.probeFailures.WithLabelValues(string(reason)).Inc()
}

// ObserveTimeToRegistered records the time from an endpoint's creation to
// its first Registered True. Call it once per endpoint.
func (m *Metrics) ObserveTimeToRegistered(d time.Duration) {
	if m == nil {
		return
	}
	m.timeToRegistered.Observe(d.Seconds())
}

// IncScrapeFailure counts a failed scrape of a transport pod's metrics.
func (m *Metrics) IncScrapeFailure(reason ScrapeFailureReason) {
	if m == nil {
		return
	}
	m.scrapeFailures.WithLabelValues(string(reason)).Inc()
}

// SetEndpointConditions records the condition statuses of one endpoint,
// keyed by namespace/name, and refreshes the endpoints gauge and the
// endpoint's registered gauge. Condition types the operator does not own
// are ignored.
func (m *Metrics) SetEndpointConditions(key string, conditions []metav1.Condition) {
	if m == nil {
		return
	}
	state := map[pylonv1alpha1.ConditionType]metav1.ConditionStatus{}
	for _, t := range pylonv1alpha1.ConditionTypes() {
		for _, c := range conditions {
			if c.Type == string(t) {
				state[t] = c.Status
			}
		}
	}
	registered := 0.0
	if state[pylonv1alpha1.ConditionRegistered] == metav1.ConditionTrue {
		registered = 1
	}
	m.registered.WithLabelValues(key).Set(registered)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conditions[key] = state
	m.refreshLocked()
}

// SetTransportReplicasReady records the ready transport pods of one
// endpoint, keyed by namespace/name.
func (m *Metrics) SetTransportReplicasReady(key string, ready int32) {
	if m == nil {
		return
	}
	m.transportReplicasReady.WithLabelValues(key).Set(float64(ready))
}

// ForgetEndpoint drops an endpoint that no longer exists.
func (m *Metrics) ForgetEndpoint(key string) {
	if m == nil {
		return
	}
	m.transportReplicasReady.DeleteLabelValues(key)
	m.registered.DeleteLabelValues(key)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.conditions[key]; !ok {
		return
	}
	delete(m.conditions, key)
	m.refreshLocked()
}

func (m *Metrics) refreshLocked() {
	counts := map[pylonv1alpha1.ConditionType]map[metav1.ConditionStatus]int{}
	for _, state := range m.conditions {
		for t, s := range state {
			if counts[t] == nil {
				counts[t] = map[metav1.ConditionStatus]int{}
			}
			counts[t][s]++
		}
	}
	for _, t := range pylonv1alpha1.ConditionTypes() {
		for _, s := range conditionStatuses {
			m.endpoints.WithLabelValues(string(t), string(s)).Set(float64(counts[t][s]))
		}
	}
}
