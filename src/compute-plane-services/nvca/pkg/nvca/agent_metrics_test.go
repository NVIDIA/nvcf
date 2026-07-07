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

package nvca

import (
	"bufio"
	_ "embed"
	"runtime"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
)

// linuxOnlyProcessMetrics lists metrics emitted by client_golang's
// `processCollector` only when the build includes the procfs-enabled
// implementation (see vendor/.../process_collector_procfsenabled.go:
// `//go:build !windows && !js && !wasip1 && !darwin`).
//
// CI runs on Linux so these are expected to be present there and the benchmark
// correctly lists them. Local dev machines (macOS, Windows) use the
// platform-specific process collectors that don't emit these, so a strict
// "every benchmark metric must exist" assertion produces a false-positive
// regression when a developer runs `go test` locally. Skip the network metrics
// on non-Linux to keep local runs honest without dropping the signal on CI.
var linuxOnlyProcessMetrics = map[string]bool{
	"process_network_receive_bytes_total":  true,
	"process_network_transmit_bytes_total": true,
}

//go:embed testdata/nvca_metrics_benchmark.prom
var benchmarkMetricsFile string

// TestMetricsBackwardCompatibility ensures that all metrics from the benchmark file
// are present in the current implementation, maintaining backward compatibility
// while allowing new metrics to be added.
func TestMetricsBackwardCompatibility(t *testing.T) {
	// 1. Parse benchmark file for expected metrics
	benchmarkMetrics := parseMetricsFromPromFile(t)
	require.NotEmpty(t, benchmarkMetrics, "benchmark metrics should not be empty")

	// 2. Create test registry and initialize NVCA metrics
	registry := prometheus.NewRegistry()

	// Initialize default metrics with test registry
	metricsOpts := []nvcametrics.DefaultMetricsOption{
		nvcametrics.WithEventErrorTotalDefaultEvents(append(getAgentEvents(), getNVCAMetricEvents()...)),
		nvcametrics.WithContainerCrashAndRestartTotalDefaultContainerNames(GetDefaultWorkloadContainerNamesToWatch()),
		nvcametrics.WithRegisterer(registry),
	}

	testMetrics := nvcametrics.NewDefaultMetrics(
		"JPd-JcvUWp0_ZuA-6zmjZqCJk7uj-NkZMG350tC8jOw",
		"mcamp-dt-local",
		"mcamp-dt-local",
		"2.49.0-rc2",
		metricsOpts...,
	)
	require.NotNil(t, testMetrics, "metrics should be initialized")

	// Initialize metrics with at least one value so they appear in Gather()
	// This simulates what happens when the agent actually runs

	// Event metrics - these should already be initialized by options, but let's ensure they're set
	for _, evt := range getAgentEvents() {
		testMetrics.EventErrorTotal.WithLabelValues(testMetrics.WithDefaultLabelValues(evt)...).Add(0)
		testMetrics.EventQueueLength.WithLabelValues(testMetrics.WithDefaultLabelValues(evt)...).Set(0)
		testMetrics.EventProcessLatency.WithLabelValues(testMetrics.WithDefaultLabelValues(evt)...)
	}

	// Container metrics
	for _, container := range GetDefaultWorkloadContainerNamesToWatch() {
		testMetrics.ContainerCrashTotal.WithLabelValues(testMetrics.WithDefaultLabelValues(container)...).Add(0)
		testMetrics.ContainerRestartTotal.WithLabelValues(testMetrics.WithDefaultLabelValues(container)...).Add(0)
	}

	// Instance type metrics - initialize with example instance type
	testMetrics.SetInstanceTypeMetrics("ON-PREM.GPU.H100_1x", 0, 0, 0)

	// K8s API metrics
	testMetrics.K8sAPISuccessTotal.WithLabelValues(testMetrics.WithDefaultLabelValues("node")...).Add(0)
	testMetrics.K8sAPIFailureTotal.WithLabelValues(testMetrics.WithDefaultLabelValues("node")...).Add(0)

	// HTTP metrics (registered by HTTP middleware)
	// These are registered when core.NewHTTPMiddleware is used with WithRequestMetrics("nvca")
	httpDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "nvca_http_duration_seconds",
		Help: "Duration of HTTP requests.",
	}, []string{"path", "method"})
	httpCount := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvca_http_request_counts",
		Help: "Counter of HTTP requests.",
	}, []string{"path", "method", "status_code"})
	registry.MustRegister(httpDuration)
	registry.MustRegister(httpCount)
	// Initialize with at least one value
	httpDuration.WithLabelValues("/healthz", "GET")
	httpCount.WithLabelValues("/healthz", "GET", "200").Add(0)

	// Controller-runtime metrics
	// These are normally registered when controllers start running
	// We'll register them directly to our test registry with the nvca_ prefix
	// to match what appears in the /metrics endpoint
	ctrlActiveWorkers := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nvca_controller_runtime_active_workers",
		Help: "Number of currently used workers per controller",
	}, []string{"controller", "nvca_nca_id", "nvca_cluster_name", "nvca_cluster_group", "nvca_version"})
	ctrlMaxConcurrent := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nvca_controller_runtime_max_concurrent_reconciles",
		Help: "Maximum number of concurrent reconciles per controller",
	}, []string{"controller", "nvca_nca_id", "nvca_cluster_name", "nvca_cluster_group", "nvca_version"})
	ctrlReconcileErrors := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvca_controller_runtime_reconcile_errors_total",
		Help: "Total number of reconciliation errors per controller",
	}, []string{"controller", "nvca_nca_id", "nvca_cluster_name", "nvca_cluster_group", "nvca_version"})
	ctrlReconcileTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvca_controller_runtime_reconcile_total",
		Help: "Total number of reconciliations per controller",
	}, []string{"controller", "result", "nvca_nca_id", "nvca_cluster_name", "nvca_cluster_group", "nvca_version"})

	registry.MustRegister(ctrlActiveWorkers)
	registry.MustRegister(ctrlMaxConcurrent)
	registry.MustRegister(ctrlReconcileErrors)
	registry.MustRegister(ctrlReconcileTotal)

	// Initialize with at least one value for each
	ctrlActiveWorkers.WithLabelValues("sharedstorage", "JPd-JcvUWp0_ZuA-6zmjZqCJk7uj-NkZMG350tC8jOw", "mcamp-dt-local", "mcamp-dt-local", "2.49.0-rc2").Set(0)
	ctrlMaxConcurrent.WithLabelValues("sharedstorage", "JPd-JcvUWp0_ZuA-6zmjZqCJk7uj-NkZMG350tC8jOw", "mcamp-dt-local", "mcamp-dt-local", "2.49.0-rc2").Set(1)
	ctrlReconcileErrors.WithLabelValues("sharedstorage", "JPd-JcvUWp0_ZuA-6zmjZqCJk7uj-NkZMG350tC8jOw", "mcamp-dt-local", "mcamp-dt-local", "2.49.0-rc2").Add(0)
	ctrlReconcileTotal.WithLabelValues("sharedstorage", "success", "JPd-JcvUWp0_ZuA-6zmjZqCJk7uj-NkZMG350tC8jOw", "mcamp-dt-local", "mcamp-dt-local", "2.49.0-rc2").Add(0)

	// 3. Gather current metrics from the test registry
	currentMetrics := make(map[string]bool)

	// Gather from NVCA custom registry (includes all metrics we've registered)
	nvcaMetricFamilies, err := registry.Gather()
	require.NoError(t, err, "gathering NVCA metrics should not error")
	for _, mf := range nvcaMetricFamilies {
		if mf.Name != nil {
			currentMetrics[*mf.Name] = true
		}
	}

	// Also register standard Go and process metrics (these are in prometheus.DefaultGatherer)
	// but only when using DefaultRegisterer. Since we're using a custom registry,
	// we need to check the standard metrics separately
	defaultMetricFamilies, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err, "gathering default metrics should not error")
	for _, mf := range defaultMetricFamilies {
		if mf.Name != nil {
			currentMetrics[*mf.Name] = true
		}
	}

	// 4. Verify all benchmark metrics exist in current metrics.
	//    On non-Linux runtimes (dev laptops on macOS/Windows), client_golang's
	//    process collector doesn't emit the `process_network_*` counters — those
	//    are procfs-enabled-only. Don't fail the test on that platform-specific
	//    gap: CI runs on Linux and continues to enforce them there.
	var missingMetrics []string
	for metricName := range benchmarkMetrics {
		if !currentMetrics[metricName] {
			if runtime.GOOS != "linux" && linuxOnlyProcessMetrics[metricName] {
				t.Logf("Skipping %s on %s: emitted only by the procfs-enabled process collector",
					metricName, runtime.GOOS)
				continue
			}
			missingMetrics = append(missingMetrics, metricName)
		}
	}

	// Report missing metrics
	if len(missingMetrics) > 0 {
		t.Errorf("The following %d metrics from the benchmark file are missing in the current implementation:\n  - %s",
			len(missingMetrics),
			strings.Join(missingMetrics, "\n  - "))
	}

	// 5. Log any new metrics (informational only, not a failure)
	var newMetrics []string
	for metricName := range currentMetrics {
		if !benchmarkMetrics[metricName] {
			newMetrics = append(newMetrics, metricName)
		}
	}

	if len(newMetrics) > 0 {
		t.Logf("New metrics added (not in benchmark, %d total):\n  + %s",
			len(newMetrics),
			strings.Join(newMetrics, "\n  + "))
	}

	// Summary log
	t.Logf("Metrics compatibility check: %d benchmark metrics validated, %d current metrics total",
		len(benchmarkMetrics), len(currentMetrics))
}

// parseMetricsFromPromFile parses the embedded Prometheus format file and extracts all metric names
// from lines that start with "# TYPE"
func parseMetricsFromPromFile(t *testing.T) map[string]bool {
	t.Helper()

	metrics := make(map[string]bool)
	scanner := bufio.NewScanner(strings.NewReader(benchmarkMetricsFile))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Look for TYPE declarations: "# TYPE metric_name metric_type"
		if strings.HasPrefix(line, "# TYPE ") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				metricName := parts[2]
				metrics[metricName] = true
			}
		}
	}

	require.NoError(t, scanner.Err(), "scanning benchmark file should not error")
	return metrics
}

// gatherMetricsFromRegistry gathers all metrics from a Prometheus registry
// and returns a map of metric names
func gatherMetricsFromRegistry(t *testing.T, registry prometheus.Gatherer) map[string]bool {
	t.Helper()

	metrics := make(map[string]bool)
	metricFamilies, err := registry.Gather()
	require.NoError(t, err, "gathering metrics should not error")

	for _, mf := range metricFamilies {
		if mf.Name != nil {
			metrics[*mf.Name] = true
		}
	}

	return metrics
}

// TestMetricsRegistryIsolation verifies that using a custom registry properly isolates metrics
func TestMetricsRegistryIsolation(t *testing.T) {
	// Create two separate registries
	registry1 := prometheus.NewRegistry()
	registry2 := prometheus.NewRegistry()

	// Initialize metrics with different registries
	metrics1 := nvcametrics.NewDefaultMetrics(
		"test-nca-1",
		"cluster-1",
		"group-1",
		"1.0.0",
		nvcametrics.WithRegisterer(registry1),
	)
	require.NotNil(t, metrics1)

	metrics2 := nvcametrics.NewDefaultMetrics(
		"test-nca-2",
		"cluster-2",
		"group-2",
		"2.0.0",
		nvcametrics.WithRegisterer(registry2),
	)
	require.NotNil(t, metrics2)

	// Gather from each registry
	gathered1 := gatherMetricsFromRegistry(t, registry1)
	gathered2 := gatherMetricsFromRegistry(t, registry2)

	// Both should have the same metric names since they use the same initialization
	require.NotEmpty(t, gathered1, "registry1 should have metrics")
	require.NotEmpty(t, gathered2, "registry2 should have metrics")

	// Verify they have the same metric names (structure)
	for metricName := range gathered1 {
		require.True(t, gathered2[metricName], "metric %s should exist in both registries", metricName)
	}
}

// TestMetricLabelValues verifies that metrics have the correct default labels
func TestMetricLabelValues(t *testing.T) {
	registry := prometheus.NewRegistry()

	testNCAID := "test-nca-id"
	testClusterName := "test-cluster"
	testClusterGroup := "test-group"
	testVersion := "1.0.0"

	metrics := nvcametrics.NewDefaultMetrics(
		testNCAID,
		testClusterName,
		testClusterGroup,
		testVersion,
		nvcametrics.WithRegisterer(registry),
	)
	require.NotNil(t, metrics)

	// Trigger a metric with default labels
	metrics.EventErrorTotal.WithLabelValues(metrics.WithDefaultLabelValues("TEST_EVENT")...).Inc()

	// Gather and verify labels
	metricFamilies, err := registry.Gather()
	require.NoError(t, err)

	// Find the event_error_total metric
	var found bool
	for _, mf := range metricFamilies {
		if mf.Name != nil && *mf.Name == nvcametrics.EventErrorTotalMetricName {
			found = true
			require.NotEmpty(t, mf.Metric, "should have at least one metric")

			// Check that the metric has the correct labels
			metric := mf.Metric[0]
			labels := make(map[string]string)
			for _, label := range metric.Label {
				if label.Name != nil && label.Value != nil {
					labels[*label.Name] = *label.Value
				}
			}

			// Verify default labels are present
			require.Equal(t, testNCAID, labels[nvcametrics.NCAIDLabel], "NCAID label should match")
			require.Equal(t, testClusterName, labels[nvcametrics.ClusterNameLabel], "cluster name label should match")
			require.Equal(t, testClusterGroup, labels[nvcametrics.ClusterGroupLabel], "cluster group label should match")
			require.Equal(t, testVersion, labels[nvcametrics.VersionLabel], "version label should match")
			require.Equal(t, "TEST_EVENT", labels[nvcametrics.EventNameLabel], "event name label should match")
		}
	}

	require.True(t, found, "should find the event_error_total metric")
}

// TestAllExpectedMetricsExist verifies that all expected NVCA metrics are registered
func TestAllExpectedMetricsExist(t *testing.T) {
	registry := prometheus.NewRegistry()

	metrics := nvcametrics.NewDefaultMetrics(
		"test-nca",
		"test-cluster",
		"test-group",
		"1.0.0",
		nvcametrics.WithRegisterer(registry),
	)
	require.NotNil(t, metrics)

	// Initialize metrics with at least one value so they appear in Gather()
	metrics.EventErrorTotal.WithLabelValues(metrics.WithDefaultLabelValues("TEST_EVENT")...).Add(0)
	metrics.EventQueueLength.WithLabelValues(metrics.WithDefaultLabelValues("TEST_EVENT")...).Set(0)
	metrics.EventProcessLatency.WithLabelValues(metrics.WithDefaultLabelValues("TEST_EVENT")...)
	metrics.ContainerCrashTotal.WithLabelValues(metrics.WithDefaultLabelValues("test-container")...).Add(0)
	metrics.ContainerRestartTotal.WithLabelValues(metrics.WithDefaultLabelValues("test-container")...).Add(0)
	metrics.QueueMessageProcessedTotal.WithLabelValues(metrics.WithDefaultLabelValues("RequestSpotInstances")...).Add(0)
	metrics.QueueMessageDequeuedTotal.WithLabelValues(metrics.WithDefaultLabelValues("creation", "A100")...).Add(0)
	metrics.QueueDequeueBatchSize.WithLabelValues(metrics.WithDefaultLabelValues("creation", "A100")...).Observe(1)
	metrics.ImagePullIssueTotal.WithLabelValues(metrics.WithDefaultLabelValues("test-registry")...).Add(0)
	metrics.K8sAPISuccessTotal.WithLabelValues(metrics.WithDefaultLabelValues("node")...).Add(0)
	metrics.K8sAPIFailureTotal.WithLabelValues(metrics.WithDefaultLabelValues("node")...).Add(0)
	metrics.SetInstanceTypeMetrics("test-type", 0, 0, 0)
	metrics.SetUnclassifiedGPUNodeCount("test-family", "test-machine", 0)
	metrics.SetTotalGPUNodeCount("test-family", "test-machine", 0)
	metrics.RecordStorageRequestDuration("success", 1.0)
	metrics.RecordMiniServiceReconcilePhase("test-nca", "test-phase")
	metrics.RecordMiniServicePhaseTransition("test-nca", "Installed", "Running")
	metrics.RecordMiniServiceFailure("test-nca", "ObjectsFailed")
	metrics.SetMiniServiceReadyStatus("test-nca", 1.0)
	metrics.RecordMiniServiceReValRequest("test-nca", "/test", "200")
	metrics.RecordMiniServiceEventError("TEST_EVENT")

	gathered := gatherMetricsFromRegistry(t, registry)

	// Expected NVCA metrics
	expectedMetrics := []string{
		nvcametrics.EventErrorTotalMetricName,
		nvcametrics.EventQueueLengthMetricName,
		nvcametrics.EventQueueProcessLatencyMetricName,
		nvcametrics.ContainerCrashTotalMetricName,
		nvcametrics.ContainerRestartTotalMetricName,
		nvcametrics.MessageQueueProcessedTotalMetricName,
		nvcametrics.MessageQueueDequeuedTotalMetricName,
		nvcametrics.MessageQueueDequeueBatchSizeMetricName,
		nvcametrics.ImagePullIssueTotalMetricName,
		nvcametrics.K8sAPISuccessTotalMetricName,
		nvcametrics.K8sAPIFailureTotalMetricName,
		nvcametrics.InstanceTypeCapacityMetricName,
		nvcametrics.InstanceTypeAllocatableMetricName,
		nvcametrics.InstanceTypeUnschedulableMetricName,
		nvcametrics.GPUNodeUnclassifiedCountMetricName,
		nvcametrics.GPUNodeTotalCountMetricName,
		nvcametrics.StorageRequestDurationMetricName,
		nvcametrics.MiniServiceReconcilePhaseTotalMetricName,
		nvcametrics.MiniServicePhaseTransitionsTotalMetricName,
		nvcametrics.MiniServiceFailuresTotalMetricName,
		nvcametrics.MiniServiceReadyStatusMetricName,
		nvcametrics.MiniServiceReValRequestTotalMetricName,
		nvcametrics.MiniServiceEventErrorTotalMetricName,
		nvcametrics.MaintenanceModeStateMetricName,
	}

	for _, expectedMetric := range expectedMetrics {
		require.True(t, gathered[expectedMetric], "metric %s should be registered", expectedMetric)
	}
}

// TestMetricTypes verifies that metrics have the correct Prometheus metric types
func TestMetricTypes(t *testing.T) {
	registry := prometheus.NewRegistry()

	metrics := nvcametrics.NewDefaultMetrics(
		"test-nca",
		"test-cluster",
		"test-group",
		"1.0.0",
		nvcametrics.WithRegisterer(registry),
	)
	require.NotNil(t, metrics)

	// Initialize metrics with at least one value so they appear in Gather()
	metrics.EventErrorTotal.WithLabelValues(metrics.WithDefaultLabelValues("TEST_EVENT")...).Add(0)
	metrics.EventQueueLength.WithLabelValues(metrics.WithDefaultLabelValues("TEST_EVENT")...).Set(0)
	metrics.EventProcessLatency.WithLabelValues(metrics.WithDefaultLabelValues("TEST_EVENT")...)
	metrics.ContainerCrashTotal.WithLabelValues(metrics.WithDefaultLabelValues("test-container")...).Add(0)
	metrics.ContainerRestartTotal.WithLabelValues(metrics.WithDefaultLabelValues("test-container")...).Add(0)
	metrics.QueueMessageProcessedTotal.WithLabelValues(metrics.WithDefaultLabelValues("RequestSpotInstances")...).Add(0)
	metrics.QueueMessageDequeuedTotal.WithLabelValues(metrics.WithDefaultLabelValues("creation", "A100")...).Add(0)
	metrics.QueueDequeueBatchSize.WithLabelValues(metrics.WithDefaultLabelValues("creation", "A100")...).Observe(1)
	metrics.ImagePullIssueTotal.WithLabelValues(metrics.WithDefaultLabelValues("test-registry")...).Add(0)
	metrics.K8sAPISuccessTotal.WithLabelValues(metrics.WithDefaultLabelValues("node")...).Add(0)
	metrics.K8sAPIFailureTotal.WithLabelValues(metrics.WithDefaultLabelValues("node")...).Add(0)
	metrics.SetInstanceTypeMetrics("test-type", 0, 0, 0)
	metrics.SetUnclassifiedGPUNodeCount("test-family", "test-machine", 0)
	metrics.SetTotalGPUNodeCount("test-family", "test-machine", 0)
	metrics.RecordStorageRequestDuration("success", 1.0)
	metrics.RecordMiniServiceReconcilePhase("test-nca", "test-phase")
	metrics.RecordMiniServicePhaseTransition("test-nca", "Installed", "Running")
	metrics.RecordMiniServiceFailure("test-nca", "ObjectsFailed")
	metrics.SetMiniServiceReadyStatus("test-nca", 1.0)
	metrics.RecordMiniServiceReValRequest("test-nca", "/test", "200")
	metrics.RecordMiniServiceEventError("TEST_EVENT")

	metricFamilies, err := registry.Gather()
	require.NoError(t, err)

	// Build a map of metric name to type
	metricTypes := make(map[string]dto.MetricType)
	for _, mf := range metricFamilies {
		if mf.Name != nil && mf.Type != nil {
			metricTypes[*mf.Name] = *mf.Type
		}
	}

	// Verify expected types
	expectedTypes := map[string]dto.MetricType{
		nvcametrics.EventErrorTotalMetricName:                  dto.MetricType_COUNTER,
		nvcametrics.EventQueueLengthMetricName:                 dto.MetricType_GAUGE,
		nvcametrics.EventQueueProcessLatencyMetricName:         dto.MetricType_SUMMARY,
		nvcametrics.ContainerCrashTotalMetricName:              dto.MetricType_COUNTER,
		nvcametrics.ContainerRestartTotalMetricName:            dto.MetricType_COUNTER,
		nvcametrics.MessageQueueProcessedTotalMetricName:       dto.MetricType_COUNTER,
		nvcametrics.MessageQueueDequeuedTotalMetricName:        dto.MetricType_COUNTER,
		nvcametrics.MessageQueueDequeueBatchSizeMetricName:     dto.MetricType_HISTOGRAM,
		nvcametrics.ImagePullIssueTotalMetricName:              dto.MetricType_COUNTER,
		nvcametrics.K8sAPISuccessTotalMetricName:               dto.MetricType_COUNTER,
		nvcametrics.K8sAPIFailureTotalMetricName:               dto.MetricType_COUNTER,
		nvcametrics.InstanceTypeCapacityMetricName:             dto.MetricType_GAUGE,
		nvcametrics.InstanceTypeAllocatableMetricName:          dto.MetricType_GAUGE,
		nvcametrics.InstanceTypeUnschedulableMetricName:        dto.MetricType_GAUGE,
		nvcametrics.GPUNodeUnclassifiedCountMetricName:         dto.MetricType_GAUGE,
		nvcametrics.GPUNodeTotalCountMetricName:                dto.MetricType_GAUGE,
		nvcametrics.StorageRequestDurationMetricName:           dto.MetricType_SUMMARY,
		nvcametrics.MiniServiceReconcilePhaseTotalMetricName:   dto.MetricType_COUNTER,
		nvcametrics.MiniServicePhaseTransitionsTotalMetricName: dto.MetricType_COUNTER,
		nvcametrics.MiniServiceFailuresTotalMetricName:         dto.MetricType_COUNTER,
		nvcametrics.MiniServiceReadyStatusMetricName:           dto.MetricType_GAUGE,
		nvcametrics.MiniServiceReValRequestTotalMetricName:     dto.MetricType_COUNTER,
		nvcametrics.MiniServiceEventErrorTotalMetricName:       dto.MetricType_COUNTER,
		nvcametrics.MaintenanceModeStateMetricName:             dto.MetricType_GAUGE,
	}

	for metricName, expectedType := range expectedTypes {
		actualType, exists := metricTypes[metricName]
		require.True(t, exists, "metric %s should exist", metricName)
		require.Equal(t, expectedType, actualType, "metric %s should have type %v", metricName, expectedType)
	}
}
