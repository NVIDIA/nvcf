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

package metrics

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	ictx "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/context"
)

const (
	EventQueueLengthMetricNameTmpl         = "%s_event_queue_length"
	EventQueueProcessLatencyMetricNameTmpl = "%s_event_process_latency"

	// Label keys
	EventNameLabel   = "nvca_operator_event_name"
	NCAIDLabel       = "nvca_operator_nca_id"
	ClusterNameLabel = "nvca_operator_cluster_name"
	VersionLabel     = "nvca_operator_version"
)

func withDefaultLabels(additionalLabels ...string) []string {
	return append([]string{
		NCAIDLabel,
		ClusterNameLabel,
		VersionLabel},
		additionalLabels...)
}

// Metrics is a struct contains the set of nvca metrics pointers,
// reference:
// https://docs.google.com/document/d/11dJ7yKX7IOGWZLp9EgLfU25YqfYCW_6Fytqx2kvQoo0/edit#heading=h.cqbpr1nozi13
type Metrics struct {
	EventQueueLength            *prometheus.GaugeVec
	EventProcessLatency         *prometheus.SummaryVec
	ConflictingMaintenanceModes *prometheus.CounterVec

	// label values
	defaultLabelValues []string
	// Prefix for metric names.
	prefix string
}

func (m *Metrics) Destroy() {
	prometheus.Unregister(m.EventQueueLength)
	prometheus.Unregister(m.EventProcessLatency)
	prometheus.Unregister(m.ConflictingMaintenanceModes)
}

func NewDefaultMetrics(prefix string, defaultLabelValues []string) *Metrics {
	m := &Metrics{
		prefix:             prefix,
		defaultLabelValues: defaultLabelValues,
	}
	if m.prefix == "" {
		m.prefix = "nvca_operator"
	}

	m.EventQueueLength = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: fmt.Sprintf(EventQueueLengthMetricNameTmpl, m.prefix),
		Help: "Lengths of the NVCA Operator event queues",
	}, withDefaultLabels(EventNameLabel))

	m.EventProcessLatency = promauto.NewSummaryVec(prometheus.SummaryOpts{
		Name:       fmt.Sprintf(EventQueueProcessLatencyMetricNameTmpl, m.prefix),
		Help:       "Latency of NVCA Operator event processing",
		MaxAge:     1 * time.Hour,
		Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
	}, withDefaultLabels(EventNameLabel))

	m.ConflictingMaintenanceModes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: fmt.Sprintf("%s_conflicting_maintenance_modes_total", m.prefix),
		Help: "Number of times conflicting maintenance modes (CordonMaintenance and CordonAndDrainMaintenance) were detected during merge",
	}, withDefaultLabels())

	return m
}

func (m *Metrics) WithDefaultLabelValues(additionalLvs ...string) []string {
	// Return a copy of the original slice to avoid any sharing of
	// resources we're going to completely copy the slice by creating
	// a new one
	lblVals := make([]string, len(m.defaultLabelValues)+len(additionalLvs))
	copy(lblVals, m.defaultLabelValues)
	for i := len(m.defaultLabelValues); i < len(m.defaultLabelValues)+len(additionalLvs); i++ {
		lblVals[i] = additionalLvs[i-len(m.defaultLabelValues)]
	}
	return lblVals
}

const ctxKey ictx.Key = "metrics"

func withMetrics(parent context.Context, m *Metrics) context.Context {
	return context.WithValue(parent, ctxKey, m)
}

func WithDefaultMetrics(parent context.Context, prefix string, defaultLabelValues []string) context.Context {
	return withMetrics(parent, NewDefaultMetrics(prefix, defaultLabelValues))
}

func FromContext(ctx context.Context) *Metrics {
	if ctx == nil {
		return nil
	}
	if m, ok := ctx.Value(ctxKey).(*Metrics); ok {
		return m
	}
	return nil
}
