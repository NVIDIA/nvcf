// SPDX-FileCopyrightText: Copyright (c) 2023-2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// mutationsTotal counts total mutations by type and result
	mutationsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nvcf_webhook_mutations_total",
			Help: "Total number of pod mutations performed",
		},
		[]string{"mutation_type", "result"},
	)

	// mutationDuration tracks mutation latency
	mutationDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "nvcf_webhook_mutation_duration_seconds",
			Help:    "Duration of mutation operations in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"mutation_type"},
	)

	// patchesGenerated counts patches generated per mutation
	patchesGenerated = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "nvcf_webhook_patches_generated",
			Help:    "Number of JSON patches generated per mutation",
			Buckets: []float64{0, 1, 2, 5, 10, 20, 50},
		},
		[]string{"mutation_type"},
	)

	// podsProcessed counts total pods processed
	podsProcessed = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nvcf_webhook_pods_processed_total",
			Help: "Total number of pods processed by the webhook",
		},
		[]string{"namespace", "result"},
	)

	// inferenceContainersDetected counts pods with inference containers
	inferenceContainersDetected = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "nvcf_webhook_inference_containers_detected_total",
			Help: "Total number of pods with inference containers detected",
		},
	)

	// mutationsSkipped counts skipped mutations (already mutated)
	mutationsSkipped = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nvcf_webhook_mutations_skipped_total",
			Help: "Total number of mutations skipped (already applied)",
		},
		[]string{"mutation_type", "reason"},
	)

	// webhookRequestsTotal counts all webhook requests
	webhookRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nvcf_webhook_requests_total",
			Help: "Total number of webhook requests received",
		},
		[]string{"path", "status"},
	)

	// webhookRequestDuration tracks overall request latency
	webhookRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "nvcf_webhook_request_duration_seconds",
			Help:    "Duration of webhook requests in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"path"},
	)
)

// MetricsHandler returns the Prometheus metrics handler
func MetricsHandler() http.Handler {
	return promhttp.Handler()
}

// RecordMutation records a mutation event
func RecordMutation(mutationType string, patchCount int, durationSeconds float64, success bool) {
	result := "success"
	if !success {
		result = "error"
	}
	
	mutationsTotal.WithLabelValues(mutationType, result).Inc()
	mutationDuration.WithLabelValues(mutationType).Observe(durationSeconds)
	patchesGenerated.WithLabelValues(mutationType).Observe(float64(patchCount))
}

// RecordMutationSkipped records when a mutation was skipped
func RecordMutationSkipped(mutationType, reason string) {
	mutationsSkipped.WithLabelValues(mutationType, reason).Inc()
}

// RecordPodProcessed records a pod being processed
func RecordPodProcessed(namespace string, success bool) {
	result := "success"
	if !success {
		result = "error"
	}
	podsProcessed.WithLabelValues(namespace, result).Inc()
}

// RecordInferenceContainerDetected records detection of inference container
func RecordInferenceContainerDetected() {
	inferenceContainersDetected.Inc()
}

// RecordWebhookRequest records a webhook request
func RecordWebhookRequest(path, status string, durationSeconds float64) {
	webhookRequestsTotal.WithLabelValues(path, status).Inc()
	webhookRequestDuration.WithLabelValues(path).Observe(durationSeconds)
}

