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
	"errors"
	"fmt"
	"testing"

	translatecommon "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/prometheus/client_golang/prometheus"
	promdto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/clustervalidator"
	metricsgctypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/gctypes"
	modelcachetypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/modelcachetypes"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/workloadtypes"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func TestMetricsLifecycle(t *testing.T) {
	ctx := context.Background()
	expectedNCAID := "some-nca-id"
	expectedClusterName := "mars"
	expectedClusterGroup := "mars-armada"
	expectedVersion := "1.0.0"
	reg := prometheus.NewRegistry()
	ctx = WithDefaultMetrics(ctx, expectedNCAID, expectedClusterName, expectedClusterGroup, expectedVersion,
		WithEventErrorTotalDefaultEvents([]string{"some_event"}),
		WithContainerCrashAndRestartTotalDefaultContainerNames([]string{"utils"}),
		WithRegisterer(reg))
	metrics := FromContext(ctx)
	t.Cleanup(func() { metrics.Destroy() })
	assert.NotNil(t, metrics)

	// Verifying some metrics are registered, not all of them
	require.NotNil(t, metrics.EventQueueLength)
	defaultLabels := metrics.WithDefaultLabelValues("some-event-kind")
	assert.Equal(t,
		append([]string{expectedNCAID, expectedClusterName, expectedClusterGroup, expectedVersion}, "some-event-kind"),
		defaultLabels)
	metrics.EventQueueLength.WithLabelValues(defaultLabels...).Set(float64(5))

	// Attempt to retrieve from a nil context
	assert.Nil(t, FromContext(nil))
	// Attempt to retrieve from an empty context
	assert.Nil(t, FromContext(context.Background()))

	// Test several cases with additionalLabelValues
	assert.Equal(t, metrics.defaultLabelValues, metrics.WithDefaultLabelValues())
	assert.Equal(t, append(metrics.defaultLabelValues, "foo", "bar"), metrics.WithDefaultLabelValues("foo", "bar"))

	// Check to ensure our default label pairs are correct
	lblPairs := metrics.GetDefaultLabelPairs()
	assert.Len(t, lblPairs, 4) // NCAID, ClusterName, ClusterGroup, Version
}

func TestK8sAPIMetrics(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	ctx = WithDefaultMetrics(ctx, "nca-123", "test-cluster", "test-group", "v1.0.0", WithRegisterer(reg))
	metrics := FromContext(ctx)
	t.Cleanup(func() { metrics.Destroy() })
	require.NotNil(t, metrics)

	// Test K8s API success metrics
	t.Run("RecordK8sAPISuccess", func(t *testing.T) {
		// Verify metrics exist
		require.NotNil(t, metrics.K8sAPISuccessTotal)
		require.NotNil(t, metrics.K8sAPIFailureTotal)

		// Record a success
		metrics.RecordK8sAPISuccess("pod")

		// Verify the metric was incremented
		metricFamilies, err := reg.Gather()
		require.NoError(t, err)

		var found bool
		for _, mf := range metricFamilies {
			if *mf.Name == "nvca_k8s_api_success_total" {
				// Find the metric with resource=pod
				for _, metric := range mf.Metric {
					var isPod bool
					for _, label := range metric.Label {
						if *label.Name == "resource" && *label.Value == "pod" {
							isPod = true
							break
						}
					}
					if isPod {
						found = true
						assert.Equal(t, float64(1), *metric.Counter.Value)

						// Verify labels
						require.Len(t, metric.Label, 5) // 4 default + resource
						break
					}
				}
				break
			}
		}
		assert.True(t, found, "nvca_k8s_api_success_total metric should be found for pod")
	})

	t.Run("RecordK8sAPIFailure", func(t *testing.T) {
		// Record a failure (using a resource not pre-initialized to verify new labels work)
		metrics.RecordK8sAPIFailure("deployment")

		// Verify the metric was incremented
		metricFamilies, err := reg.Gather()
		require.NoError(t, err)

		var found bool
		for _, mf := range metricFamilies {
			if *mf.Name == "nvca_k8s_api_failure_total" {
				// Find the metric with resource=deployment
				for _, metric := range mf.Metric {
					var isDeployment bool
					for _, label := range metric.Label {
						if *label.Name == "resource" && *label.Value == "deployment" {
							isDeployment = true
							break
						}
					}
					if isDeployment {
						found = true
						assert.Equal(t, float64(1), *metric.Counter.Value)
						break
					}
				}
				break
			}
		}
		assert.True(t, found, "nvca_k8s_api_failure_total metric should be found for deployment")
	})
}

func TestTrackK8sAPICall(t *testing.T) {
	ctx := context.Background()
	defaultLabelValues := []string{"nca-123", "test-cluster", "test-group", "v1.0.0"}
	reg := prometheus.NewRegistry()
	ctx = WithDefaultMetrics(ctx, defaultLabelValues[0], defaultLabelValues[1], defaultLabelValues[2], defaultLabelValues[3], WithRegisterer(reg))
	metrics := FromContext(ctx)
	t.Cleanup(func() { metrics.Destroy() })
	require.NotNil(t, metrics)

	tests := []struct {
		name            string
		operation       string
		resource        string
		err             error
		expectedSuccess bool
		expectedFailure bool
	}{
		{
			name:            "Success - no error",
			operation:       "get",
			resource:        "pod",
			err:             nil,
			expectedSuccess: true,
			expectedFailure: false,
		},
		{
			name:            "Success - NotFound error treated as success",
			operation:       "get",
			resource:        "secret",
			err:             k8serrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, "test-secret"),
			expectedSuccess: true,
			expectedFailure: false,
		},
		{
			name:            "Failure - generic error",
			operation:       "create",
			resource:        "deployment",
			err:             errors.New("connection timeout"),
			expectedSuccess: false,
			expectedFailure: true,
		},
		{
			name:            "Failure - forbidden error",
			operation:       "update",
			resource:        "configmap",
			err:             k8serrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "test-cm", errors.New("forbidden")),
			expectedSuccess: false,
			expectedFailure: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear registry for this test
			reg = prometheus.NewRegistry()
			ctx = WithDefaultMetrics(ctx, defaultLabelValues[0], defaultLabelValues[1], defaultLabelValues[2], defaultLabelValues[3], WithRegisterer(reg))
			metrics = FromContext(ctx)
			require.NotNil(t, metrics)

			// Call the function under test
			metrics.TrackK8sAPICall(tt.resource, tt.err)

			// Verify the metrics
			metricFamilies, err := reg.Gather()
			require.NoError(t, err)

			var successFound, failureFound bool
			var successValue, failureValue float64

			for _, mf := range metricFamilies {
				switch *mf.Name {
				case "nvca_k8s_api_success_total":
					// Find the metric with the test resource
					for _, metric := range mf.Metric {
						for _, label := range metric.Label {
							if *label.Name == "resource" && *label.Value == tt.resource {
								successFound = true
								successValue = *metric.Counter.Value
								break
							}
						}
					}
				case "nvca_k8s_api_failure_total":
					// Find the metric with the test resource
					for _, metric := range mf.Metric {
						for _, label := range metric.Label {
							if *label.Name == "resource" && *label.Value == tt.resource {
								failureFound = true
								failureValue = *metric.Counter.Value
								break
							}
						}
					}
				}
			}

			if tt.expectedSuccess {
				assert.True(t, successFound, "Success metric should be found for resource %s", tt.resource)
				assert.Equal(t, float64(1), successValue, "Success metric should be incremented")
			}

			if tt.expectedFailure {
				assert.True(t, failureFound, "Failure metric should be found for resource %s", tt.resource)
				assert.Equal(t, float64(1), failureValue, "Failure metric should be incremented")
			}

			if !tt.expectedSuccess && !tt.expectedFailure {
				t.Errorf("Test case should expect either success or failure")
			}
		})
	}
}

func TestMetricsNilSafety(t *testing.T) {
	// Test that metrics methods are nil-safe when metrics pointer is nil
	var metrics *Metrics

	// These should not panic when metrics is nil
	assert.NotPanics(t, func() {
		metrics.RecordK8sAPISuccess("pod")
	})

	assert.NotPanics(t, func() {
		metrics.RecordK8sAPIFailure("pod")
	})

	assert.NotPanics(t, func() {
		metrics.TrackK8sAPICall("pod", nil)
	})

	// Note: We don't test with &Metrics{} (uninitialized fields) because that's a code bug.
	// Metrics should always be created via NewDefaultMetrics which initializes all fields.
}

// TestK8sAPIMetricsResourceTypes tests all the different resource types we've instrumented
func TestK8sAPIMetricsResourceTypes(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	ctx = WithDefaultMetrics(ctx, "nca-123", "test-cluster", "test-group", "v1.0.0", WithRegisterer(reg))
	metrics := FromContext(ctx)
	t.Cleanup(func() { metrics.Destroy() })
	require.NotNil(t, metrics)

	// Test all resource types we've instrumented across the codebase
	// NOTE: We only track Get() calls as per requirements
	resourceTypes := []string{
		"pod", "deployment", "secret", "namespace", "node", "configmap",
		"icmsrequest", "serviceaccount", "rolebinding", "runtimeclass",
		"storageclass", "persistentvolumeclaim", "persistentvolume",
		"job", "csidriver", "miniservice", "networkpolicy",
	}

	// Only testing Get() calls since that's what we're actually tracking

	for _, resource := range resourceTypes {
		t.Run(fmt.Sprintf("%s_success", resource), func(t *testing.T) {
			// Test success case
			metrics.TrackK8sAPICall(resource, nil)

			// Verify metric was recorded
			metricFamilies, err := reg.Gather()
			require.NoError(t, err)

			found := false
			for _, mf := range metricFamilies {
				if *mf.Name == "nvca_k8s_api_success_total" {
					for _, metric := range mf.Metric {
						// Check if this metric has the right resource label
						hasResourceLabel := false
						for _, label := range metric.Label {
							if *label.Name == "resource" && *label.Value == resource {
								hasResourceLabel = true
							}
						}
						if hasResourceLabel {
							found = true
							assert.Greater(t, *metric.Counter.Value, float64(0),
								"Metric should be incremented for %s", resource)
						}
					}
				}
			}
			assert.True(t, found, "Should find success metric for %s", resource)
		})
	}
}

// TestK8sAPIMetricsErrors tests various K8s error scenarios
func TestK8sAPIMetricsErrors(t *testing.T) {
	ctx := context.Background()
	defaultLabelValues := []string{"nca-123", "test-cluster", "test-group", "v1.0.0"}

	tests := []struct {
		name            string
		err             error
		expectedSuccess bool
		expectedFailure bool
	}{
		{
			name:            "AlreadyExists error - should be failure",
			err:             k8serrors.NewAlreadyExists(schema.GroupResource{Resource: "pods"}, "test-pod"),
			expectedSuccess: false,
			expectedFailure: true,
		},
		{
			name:            "Conflict error - should be failure",
			err:             k8serrors.NewConflict(schema.GroupResource{Resource: "deployments"}, "test-deploy", errors.New("conflict")),
			expectedSuccess: false,
			expectedFailure: true,
		},
		{
			name:            "Invalid error - should be failure",
			err:             k8serrors.NewInvalid(schema.GroupKind{Group: "apps", Kind: "Deployment"}, "test", nil),
			expectedSuccess: false,
			expectedFailure: true,
		},
		{
			name:            "Unauthorized error - should be failure",
			err:             k8serrors.NewUnauthorized("unauthorized"),
			expectedSuccess: false,
			expectedFailure: true,
		},
		{
			name:            "ServiceUnavailable error - should be failure",
			err:             k8serrors.NewServiceUnavailable("service down"),
			expectedSuccess: false,
			expectedFailure: true,
		},
		{
			name:            "Timeout error - should be failure",
			err:             k8serrors.NewTimeoutError("timeout", 30),
			expectedSuccess: false,
			expectedFailure: true,
		},
		{
			name:            "NotFound error - should be SUCCESS (API responded correctly)",
			err:             k8serrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, "missing-secret"),
			expectedSuccess: true,
			expectedFailure: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			ctx := WithDefaultMetrics(ctx, defaultLabelValues[0], defaultLabelValues[1], defaultLabelValues[2], defaultLabelValues[3], WithRegisterer(reg))
			metrics := FromContext(ctx)
			require.NotNil(t, metrics)

			// Test the error handling (use a unique resource for this test)
			testResource := "test-resource-unique"
			metrics.TrackK8sAPICall(testResource, tt.err)

			// Verify the metrics
			metricFamilies, err := reg.Gather()
			require.NoError(t, err)

			// Find the metric value for our test resource
			var successValue, failureValue float64
			for _, mf := range metricFamilies {
				switch *mf.Name {
				case "nvca_k8s_api_success_total":
					for _, metric := range mf.Metric {
						for _, label := range metric.Label {
							if *label.Name == "resource" && *label.Value == testResource {
								successValue = *metric.Counter.Value
								break
							}
						}
					}
				case "nvca_k8s_api_failure_total":
					for _, metric := range mf.Metric {
						for _, label := range metric.Label {
							if *label.Name == "resource" && *label.Value == testResource {
								failureValue = *metric.Counter.Value
								break
							}
						}
					}
				}
			}

			if tt.expectedSuccess {
				assert.Equal(t, float64(1), successValue, "Should have incremented success metric for %s", tt.name)
				assert.Equal(t, float64(0), failureValue, "Should not have incremented failure metric for %s", tt.name)
			}

			if tt.expectedFailure {
				assert.Equal(t, float64(1), failureValue, "Should have incremented failure metric for %s", tt.name)
				assert.Equal(t, float64(0), successValue, "Should not have incremented success metric for %s", tt.name)
			}
		})
	}
}

// TestK8sAPIMetricsLabelsValidation tests that proper labels are applied
func TestK8sAPIMetricsLabelsValidation(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	ctx = WithDefaultMetrics(ctx, "nca-123", "test-cluster", "test-group", "v1.0.0", WithRegisterer(reg))
	metrics := FromContext(ctx)
	t.Cleanup(func() { metrics.Destroy() })
	require.NotNil(t, metrics)

	// Test with specific resource
	testResource := "deployment"
	metrics.TrackK8sAPICall(testResource, nil)

	// Verify the labels are correctly applied
	metricFamilies, err := reg.Gather()
	require.NoError(t, err)

	for _, mf := range metricFamilies {
		if *mf.Name == "nvca_k8s_api_success_total" {
			// Find the metric with our test resource
			for _, metric := range mf.Metric {
				var isTestResource bool
				for _, label := range metric.Label {
					if *label.Name == "resource" && *label.Value == testResource {
						isTestResource = true
						break
					}
				}
				if !isTestResource {
					continue
				}

				// Check that all expected labels are present
				expectedLabels := map[string]string{
					"nvca_nca_id":        "nca-123",
					"nvca_cluster_name":  "test-cluster",
					"nvca_cluster_group": "test-group",
					"nvca_version":       "v1.0.0",
					"resource":           testResource,
				}

				actualLabels := make(map[string]string)
				for _, label := range metric.Label {
					actualLabels[*label.Name] = *label.Value
				}

				for expectedName, expectedValue := range expectedLabels {
					actualValue, exists := actualLabels[expectedName]
					assert.True(t, exists, "Label %s should exist", expectedName)
					assert.Equal(t, expectedValue, actualValue, "Label %s should have correct value", expectedName)
				}
			}
		}
	}
}

// TestK8sAPIMetricsFromContext tests context integration
func TestK8sAPIMetricsFromContext(t *testing.T) {
	ctx := context.Background()
	defaultLabelValues := []string{"nca-123", "test-cluster", "test-group", "v1.0.0"}

	t.Run("WithMetricsInContext", func(t *testing.T) {
		reg := prometheus.NewRegistry()
		ctx = WithDefaultMetrics(ctx, defaultLabelValues[0], defaultLabelValues[1], defaultLabelValues[2], defaultLabelValues[3], WithRegisterer(reg))

		// Simulate what happens in real code - get metrics from context
		metrics := FromContext(ctx)
		require.NotNil(t, metrics, "Should be able to get metrics from context")

		// Use the metrics like in production code
		metrics.TrackK8sAPICall("pod", nil)

		// Verify it worked
		metricFamilies, err := reg.Gather()
		require.NoError(t, err)

		found := false
		for _, mf := range metricFamilies {
			if *mf.Name == "nvca_k8s_api_success_total" {
				// Find the pod metric and verify it was incremented
				for _, metric := range mf.Metric {
					var isPod bool
					for _, label := range metric.Label {
						if *label.Name == "resource" && *label.Value == "pod" {
							isPod = true
							break
						}
					}
					if isPod {
						found = true
						assert.Equal(t, float64(1), *metric.Counter.Value)
						break
					}
				}
			}
		}
		assert.True(t, found, "Should find the success metric for pod")
	})

	t.Run("WithoutMetricsInContext", func(t *testing.T) {
		// Test with empty context (like some of our nil-safe checks)
		emptyCtx := context.Background()
		metrics := FromContext(emptyCtx)
		assert.Nil(t, metrics, "Should be nil when no metrics in context")
	})

	t.Run("NilContext", func(t *testing.T) {
		// Test with nil context
		metrics := FromContext(nil)
		assert.Nil(t, metrics, "Should be nil when context is nil")
	})
}

// TestK8sAPIMetricsConcurrency tests concurrent access to metrics
func TestK8sAPIMetricsConcurrency(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	ctx = WithDefaultMetrics(ctx, "nca-123", "test-cluster", "test-group", "v1.0.0", WithRegisterer(reg))
	metrics := FromContext(ctx)
	t.Cleanup(func() { metrics.Destroy() })
	require.NotNil(t, metrics)

	// Test concurrent metric recording like what might happen in production
	const numGoroutines = 50
	const numCallsPerGoroutine = 10

	done := make(chan bool, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer func() { done <- true }()

			for j := 0; j < numCallsPerGoroutine; j++ {
				// Mix of success and failure calls
				if j%2 == 0 {
					metrics.TrackK8sAPICall("pod", nil)
				} else {
					metrics.TrackK8sAPICall("pod", errors.New("test error"))
				}
			}
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Verify final metrics
	metricFamilies, err := reg.Gather()
	require.NoError(t, err)

	var successTotal, failureTotal float64
	for _, mf := range metricFamilies {
		switch *mf.Name {
		case "nvca_k8s_api_success_total":
			// Find the pod metric
			for _, metric := range mf.Metric {
				for _, label := range metric.Label {
					if *label.Name == "resource" && *label.Value == "pod" {
						successTotal = *metric.Counter.Value
						break
					}
				}
			}
		case "nvca_k8s_api_failure_total":
			// Find the pod metric
			for _, metric := range mf.Metric {
				for _, label := range metric.Label {
					if *label.Name == "resource" && *label.Value == "pod" {
						failureTotal = *metric.Counter.Value
						break
					}
				}
			}
		}
	}

	expectedSuccessTotal := float64(numGoroutines * numCallsPerGoroutine / 2)
	expectedFailureTotal := float64(numGoroutines * numCallsPerGoroutine / 2)

	assert.Equal(t, expectedSuccessTotal, successTotal, "Success total should match expected")
	assert.Equal(t, expectedFailureTotal, failureTotal, "Failure total should match expected")
}

// TestGCMetricsLifecycle tests GC metrics integration with Metrics struct
func TestGCMetricsLifecycle(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	ctx = WithDefaultMetrics(ctx, "nca-123", "test-cluster", "test-group", "v1.0.0", WithRegisterer(reg))
	metrics := FromContext(ctx)
	t.Cleanup(func() { metrics.Destroy() })
	require.NotNil(t, metrics)

	// Verify GC metrics are initialized
	require.NotNil(t, metrics.OrphanedResourceCleanupTotal)
	require.NotNil(t, metrics.GCCleanerRunTotal)

	// Test RecordOrphanedResourceCleanup
	metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypeNamespace, metricsgctypes.StatusSuccess)
	metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypePVC, metricsgctypes.StatusFailure)

	// Test RecordGCCleanerRun
	metrics.RecordGCCleanerRun("NamespaceCleaner", metricsgctypes.StatusSuccess)
	metrics.RecordGCCleanerRun("PersistentVolumeCleaner", metricsgctypes.StatusFailure)

	// Verify metrics were recorded
	metricFamilies, err := reg.Gather()
	require.NoError(t, err)

	var foundCleanup, foundCleaner bool
	for _, mf := range metricFamilies {
		switch *mf.Name {
		case OrphanedResourceCleanupTotalMetricName:
			foundCleanup = true
			assert.Greater(t, len(mf.Metric), 0, "Should have orphaned resource cleanup metrics")
		case GCCleanerRunTotalMetricName:
			foundCleaner = true
			assert.Greater(t, len(mf.Metric), 0, "Should have GC cleaner run metrics")
		}
	}

	assert.True(t, foundCleanup, "OrphanedResourceCleanupTotal metric should be found")
	assert.True(t, foundCleaner, "GCCleanerRunTotal metric should be found")
}

// TestGCMetricsNilSafety tests nil-safety of GC metrics methods
func TestGCMetricsNilSafety(t *testing.T) {
	var metrics *Metrics

	// These should not panic when metrics is nil
	assert.NotPanics(t, func() {
		metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypeNamespace, metricsgctypes.StatusSuccess)
	})

	assert.NotPanics(t, func() {
		metrics.RecordGCCleanerRun("TestCleaner", metricsgctypes.StatusSuccess)
	})
}

// TestGCMetricsLabels tests that GC metrics have correct labels (backwards compatibility)
func TestGCMetricsLabels(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	ctx = WithDefaultMetrics(ctx, "nca-123", "test-cluster", "test-group", "v1.0.0", WithRegisterer(reg))
	metrics := FromContext(ctx)
	t.Cleanup(func() { metrics.Destroy() })
	require.NotNil(t, metrics)

	// Record metrics
	metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypeNamespace, metricsgctypes.StatusSuccess)
	metrics.RecordGCCleanerRun("NamespaceCleaner", metricsgctypes.StatusSuccess)

	// Verify labels
	metricFamilies, err := reg.Gather()
	require.NoError(t, err)

	for _, mf := range metricFamilies {
		switch *mf.Name {
		case OrphanedResourceCleanupTotalMetricName:
			// Find the metric with namespace+success (which we incremented)
			var metric *promdto.Metric
			for _, m := range mf.Metric {
				hasResourceType := false
				hasStatus := false
				for _, label := range m.Label {
					if *label.Name == ResourceTypeLabel && *label.Value == metricsgctypes.ResourceTypeNamespace {
						hasResourceType = true
					}
					if *label.Name == StatusLabel && *label.Value == metricsgctypes.StatusSuccess {
						hasStatus = true
					}
				}
				if hasResourceType && hasStatus {
					metric = m
					break
				}
			}
			require.NotNil(t, metric, "Should find metric with namespace+success")

			// Should have: ClusterName, ClusterGroup, Version, ResourceType, Status (no NCAID for backwards compatibility)
			expectedLabels := map[string]string{
				ClusterNameLabel:  "test-cluster",
				ClusterGroupLabel: "test-group",
				VersionLabel:      "v1.0.0",
				ResourceTypeLabel: metricsgctypes.ResourceTypeNamespace,
				StatusLabel:       metricsgctypes.StatusSuccess,
			}

			actualLabels := make(map[string]string)
			for _, label := range metric.Label {
				actualLabels[*label.Name] = *label.Value
			}

			for expectedName, expectedValue := range expectedLabels {
				actualValue, exists := actualLabels[expectedName]
				assert.True(t, exists, "Label %s should exist", expectedName)
				assert.Equal(t, expectedValue, actualValue, "Label %s should have correct value", expectedName)
			}

			// Should NOT have NCAID for backwards compatibility
			_, hasNCAID := actualLabels[NCAIDLabel]
			assert.False(t, hasNCAID, "GC metrics should not have NCAID label for backwards compatibility")

		case GCCleanerRunTotalMetricName:
			// Find the metric with NamespaceCleaner+success (which we incremented)
			var metric *promdto.Metric
			for _, m := range mf.Metric {
				hasCleanerName := false
				hasStatus := false
				for _, label := range m.Label {
					if *label.Name == CleanerNameLabel && *label.Value == "NamespaceCleaner" {
						hasCleanerName = true
					}
					if *label.Name == StatusLabel && *label.Value == metricsgctypes.StatusSuccess {
						hasStatus = true
					}
				}
				if hasCleanerName && hasStatus {
					metric = m
					break
				}
			}
			require.NotNil(t, metric, "Should find metric with NamespaceCleaner+success")

			// Should have: ClusterName, ClusterGroup, Version, CleanerName, Status (no NCAID for backwards compatibility)
			expectedLabels := map[string]string{
				ClusterNameLabel:  "test-cluster",
				ClusterGroupLabel: "test-group",
				VersionLabel:      "v1.0.0",
				CleanerNameLabel:  "NamespaceCleaner",
				StatusLabel:       metricsgctypes.StatusSuccess,
			}

			actualLabels := make(map[string]string)
			for _, label := range metric.Label {
				actualLabels[*label.Name] = *label.Value
			}

			for expectedName, expectedValue := range expectedLabels {
				actualValue, exists := actualLabels[expectedName]
				assert.True(t, exists, "Label %s should exist", expectedName)
				assert.Equal(t, expectedValue, actualValue, "Label %s should have correct value", expectedName)
			}

			// Should NOT have NCAID for backwards compatibility
			_, hasNCAID := actualLabels[NCAIDLabel]
			assert.False(t, hasNCAID, "GC metrics should not have NCAID label for backwards compatibility")
		}
	}
}

// TestGCMetricsResourceTypes tests all resource type constants
func TestGCMetricsResourceTypes(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	ctx = WithDefaultMetrics(ctx, "nca-123", "test-cluster", "test-group", "v1.0.0", WithRegisterer(reg))
	metrics := FromContext(ctx)
	t.Cleanup(func() { metrics.Destroy() })
	require.NotNil(t, metrics)

	resourceTypes := []string{
		metricsgctypes.ResourceTypeNamespace,
		metricsgctypes.ResourceTypeStorageRequest,
		metricsgctypes.ResourceTypePVC,
		metricsgctypes.ResourceTypeStorageClass,
		metricsgctypes.ResourceTypePersistentVolume,
	}

	for _, resourceType := range resourceTypes {
		t.Run(resourceType, func(t *testing.T) {
			// Test success status
			metrics.RecordOrphanedResourceCleanup(resourceType, metricsgctypes.StatusSuccess)

			// Verify metric was recorded
			metricFamilies, err := reg.Gather()
			require.NoError(t, err)

			found := false
			for _, mf := range metricFamilies {
				if *mf.Name == OrphanedResourceCleanupTotalMetricName {
					for _, metric := range mf.Metric {
						// Check if this metric has BOTH the right resource_type AND status=success
						hasResourceTypeLabel := false
						hasSuccessStatus := false
						for _, label := range metric.Label {
							if *label.Name == ResourceTypeLabel && *label.Value == resourceType {
								hasResourceTypeLabel = true
							}
							if *label.Name == StatusLabel && *label.Value == metricsgctypes.StatusSuccess {
								hasSuccessStatus = true
							}
						}
						if hasResourceTypeLabel && hasSuccessStatus {
							found = true
							assert.Greater(t, *metric.Counter.Value, float64(0),
								"Metric should be incremented for %s", resourceType)
						}
					}
				}
			}
			assert.True(t, found, "Should find cleanup metric for %s with status=success", resourceType)
		})
	}
}

// TestGCMetricsStatusValues tests status constants
func TestGCMetricsStatusValues(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	ctx = WithDefaultMetrics(ctx, "nca-123", "test-cluster", "test-group", "v1.0.0", WithRegisterer(reg))
	metrics := FromContext(ctx)
	t.Cleanup(func() { metrics.Destroy() })
	require.NotNil(t, metrics)

	statuses := []string{metricsgctypes.StatusSuccess, metricsgctypes.StatusFailure}

	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypeNamespace, status)

			// Verify metric was recorded
			metricFamilies, err := reg.Gather()
			require.NoError(t, err)

			found := false
			for _, mf := range metricFamilies {
				if *mf.Name == OrphanedResourceCleanupTotalMetricName {
					for _, metric := range mf.Metric {
						// Check if this metric has BOTH the right status AND resource_type=namespace
						hasStatusLabel := false
						hasNamespaceResource := false
						for _, label := range metric.Label {
							if *label.Name == StatusLabel && *label.Value == status {
								hasStatusLabel = true
							}
							if *label.Name == ResourceTypeLabel && *label.Value == metricsgctypes.ResourceTypeNamespace {
								hasNamespaceResource = true
							}
						}
						if hasStatusLabel && hasNamespaceResource {
							found = true
							assert.Greater(t, *metric.Counter.Value, float64(0),
								"Metric should be incremented for status %s", status)
						}
					}
				}
			}
			assert.True(t, found, "Should find cleanup metric for status %s with resource_type=namespace", status)
		})
	}
}

// TestModelCacheResultMetric tests the model cache result metric
func TestModelCacheResultMetric(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	ctx = WithDefaultMetrics(ctx, "nca-123", "test-cluster", "test-group", "v1.0.0", WithRegisterer(reg))
	metrics := FromContext(ctx)
	t.Cleanup(func() { metrics.Destroy() })
	require.NotNil(t, metrics)

	// Verify metric is initialized
	require.NotNil(t, metrics.ModelCacheResultTotal)

	t.Run("RecordSuccess", func(t *testing.T) {
		metrics.RecordModelCacheResult("success", "", "nvmesh")

		metricFamilies, err := reg.Gather()
		require.NoError(t, err)

		found := false
		for _, mf := range metricFamilies {
			if *mf.Name == ModelCacheResultTotalMetricName {
				found = true
				require.Greater(t, len(mf.Metric), 0)

				// Find the success metric for the nvmesh backend.
				for _, metric := range mf.Metric {
					var resultLabel, reasonLabel, backendLabel string
					for _, label := range metric.Label {
						switch *label.Name {
						case ResultLabel:
							resultLabel = *label.Value
						case FailureReasonLabel:
							reasonLabel = *label.Value
						case BackendLabel:
							backendLabel = *label.Value
						}
					}
					if resultLabel == "success" && backendLabel == "nvmesh" {
						assert.Equal(t, "", reasonLabel, "Success should have empty failure_reason")
						assert.Equal(t, float64(1), *metric.Counter.Value)
					}
				}
			}
		}
		assert.True(t, found, "ModelCacheResultTotal metric should be found")
	})

	t.Run("RecordFailure", func(t *testing.T) {
		metrics.RecordModelCacheResult("failure", "pvc_bind_failed", "samba")

		metricFamilies, err := reg.Gather()
		require.NoError(t, err)

		found := false
		for _, mf := range metricFamilies {
			if *mf.Name == ModelCacheResultTotalMetricName {
				for _, metric := range mf.Metric {
					var resultLabel, reasonLabel, backendLabel string
					for _, label := range metric.Label {
						switch *label.Name {
						case ResultLabel:
							resultLabel = *label.Value
						case FailureReasonLabel:
							reasonLabel = *label.Value
						case BackendLabel:
							backendLabel = *label.Value
						}
					}
					if resultLabel == "failure" && reasonLabel == "pvc_bind_failed" && backendLabel == "samba" {
						found = true
						assert.Equal(t, float64(1), *metric.Counter.Value)
					}
				}
			}
		}
		assert.True(t, found, "Failure metric with pvc_bind_failed reason and samba backend should be found")
	})
}

// TestModelCacheResultMetricNilSafety tests nil-safety of model cache result metric
func TestModelCacheResultMetricNilSafety(t *testing.T) {
	var metrics *Metrics

	assert.NotPanics(t, func() {
		metrics.RecordModelCacheResult("success", "", "nvmesh")
	})

	assert.NotPanics(t, func() {
		metrics.RecordModelCacheResult("failure", "pvc_bind_failed", "samba")
	})

	assert.NotPanics(t, func() {
		metrics.RecordModelCacheBackendSelected("samba")
		metrics.RecordModelCachePopulate("samba")
		metrics.RecordModelCacheReuse("samba")
		metrics.RecordModelCacheReclaimed("samba")
		metrics.SetModelCacheBackendCount("samba", 3)
	})
}

// TestModelCacheResultMetricLabels tests that model cache metrics have correct labels
func TestModelCacheResultMetricLabels(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	ctx = WithDefaultMetrics(ctx, "nca-123", "test-cluster", "test-group", "v1.0.0", WithRegisterer(reg))
	metrics := FromContext(ctx)
	t.Cleanup(func() { metrics.Destroy() })
	require.NotNil(t, metrics)

	metrics.RecordModelCacheResult("failure", "image_pull", "nvmesh")

	metricFamilies, err := reg.Gather()
	require.NoError(t, err)

	// Should have: ClusterName, ClusterGroup, Version, Result, FailureReason, Backend (no NCAID for storage metrics)
	expectedLabels := map[string]string{
		ClusterNameLabel:   "test-cluster",
		ClusterGroupLabel:  "test-group",
		VersionLabel:       "v1.0.0",
		ResultLabel:        "failure",
		FailureReasonLabel: "image_pull",
		BackendLabel:       "nvmesh",
	}

	found := false
	for _, mf := range metricFamilies {
		if *mf.Name == ModelCacheResultTotalMetricName {
			require.Greater(t, len(mf.Metric), 0)
			for _, metric := range mf.Metric {
				actualLabels := make(map[string]string)
				for _, label := range metric.Label {
					actualLabels[*label.Name] = *label.Value
				}
				if actualLabels[ResultLabel] == "failure" && actualLabels[FailureReasonLabel] == "image_pull" &&
					actualLabels[BackendLabel] == "nvmesh" {
					found = true
					for expectedName, expectedValue := range expectedLabels {
						actualValue, exists := actualLabels[expectedName]
						assert.True(t, exists, "Label %s should exist", expectedName)
						assert.Equal(t, expectedValue, actualValue, "Label %s should have correct value", expectedName)
					}
					// Should NOT have NCAID for backwards compatibility with storage metrics
					_, hasNCAID := actualLabels[NCAIDLabel]
					assert.False(t, hasNCAID, "Model cache metrics should not have NCAID label for backwards compatibility")
				}
			}
		}
	}
	assert.True(t, found, "Should find metric with result=failure, failure_reason=image_pull")
}

// TestModelCacheResultMetricFailureReasons tests all failure reason values
func TestModelCacheResultMetricFailureReasons(t *testing.T) {
	for _, reason := range modelcachetypes.AllFailureReasons {
		t.Run(reason, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			ctx := context.Background()
			ctx = WithDefaultMetrics(ctx, "nca-123", "test-cluster", "test-group", "v1.0.0", WithRegisterer(reg))
			metrics := FromContext(ctx)
			require.NotNil(t, metrics)

			metrics.RecordModelCacheResult("failure", reason, "nvmesh")

			metricFamilies, err := reg.Gather()
			require.NoError(t, err)

			found := false
			for _, mf := range metricFamilies {
				if *mf.Name == ModelCacheResultTotalMetricName {
					for _, metric := range mf.Metric {
						var reasonLabel, backendLabel string
						for _, label := range metric.Label {
							switch *label.Name {
							case FailureReasonLabel:
								reasonLabel = *label.Value
							case BackendLabel:
								backendLabel = *label.Value
							}
						}
						if reasonLabel == reason && backendLabel == "nvmesh" {
							found = true
							assert.Equal(t, float64(1), *metric.Counter.Value)
						}
					}
				}
			}
			assert.True(t, found, "Should find metric for failure reason %s", reason)
		})
	}
}

// TestKataRuntimeIsolationEnabledMetric tests the Kata runtime isolation gauge metric
func TestKataRuntimeIsolationEnabledMetric(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	ctx = WithDefaultMetrics(ctx, "nca-123", "test-cluster", "test-group", "v1.0.0", WithRegisterer(reg))
	metrics := FromContext(ctx)
	t.Cleanup(func() { metrics.Destroy() })
	require.NotNil(t, metrics)

	// Verify metric is initialized
	require.NotNil(t, metrics.KataRuntimeIsolationEnabled)

	// Helper to get the current gauge value
	getGaugeValue := func() (float64, bool) {
		metricFamilies, err := reg.Gather()
		require.NoError(t, err)
		for _, mf := range metricFamilies {
			if *mf.Name == KataRuntimeIsolationEnabledMetricName {
				for _, metric := range mf.Metric {
					return *metric.Gauge.Value, true
				}
			}
		}
		return 0, false
	}

	t.Run("InitializedToZero", func(t *testing.T) {
		value, found := getGaugeValue()
		assert.True(t, found, "KataRuntimeIsolationEnabled metric should be found")
		assert.Equal(t, float64(0), value, "Should be initialized to 0 (disabled)")
	})

	t.Run("SetEnabled", func(t *testing.T) {
		metrics.SetKataRuntimeIsolationEnabled(true)
		value, found := getGaugeValue()
		assert.True(t, found, "KataRuntimeIsolationEnabled metric should be found")
		assert.Equal(t, float64(1), value, "Should be 1 when enabled")
	})

	t.Run("SetDisabled", func(t *testing.T) {
		metrics.SetKataRuntimeIsolationEnabled(false)
		value, found := getGaugeValue()
		assert.True(t, found, "KataRuntimeIsolationEnabled metric should be found")
		assert.Equal(t, float64(0), value, "Should be 0 when disabled")
	})
}

// TestKataRuntimeIsolationEnabledMetricNilSafety tests nil-safety
func TestKataRuntimeIsolationEnabledMetricNilSafety(t *testing.T) {
	var metrics *Metrics

	assert.NotPanics(t, func() {
		metrics.SetKataRuntimeIsolationEnabled(true)
	})

	assert.NotPanics(t, func() {
		metrics.SetKataRuntimeIsolationEnabled(false)
	})
}

// TestMaintenanceModeStateMetric tests the maintenance-mode gauge: exactly one
// mode series is 1 and the rest are 0 (one-hot encoding).
func TestMaintenanceModeStateMetric(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	ctx = WithDefaultMetrics(ctx, "nca-123", "test-cluster", "test-group", "v1.0.0", WithRegisterer(reg))
	metrics := FromContext(ctx)
	t.Cleanup(func() { metrics.Destroy() })
	require.NotNil(t, metrics)
	require.NotNil(t, metrics.MaintenanceModeState)

	// modeValues returns the gauge value keyed by the `mode` label value.
	modeValues := func() map[string]float64 {
		metricFamilies, err := reg.Gather()
		require.NoError(t, err)
		out := map[string]float64{}
		for _, mf := range metricFamilies {
			if *mf.Name != MaintenanceModeStateMetricName {
				continue
			}
			for _, metric := range mf.Metric {
				for _, lp := range metric.Label {
					if *lp.Name == MaintenanceModeLabel {
						out[*lp.Value] = *metric.Gauge.Value
					}
				}
			}
		}
		return out
	}

	t.Run("InitializedToZeroForAllModes", func(t *testing.T) {
		values := modeValues()
		require.Len(t, values, len(types.AllMaintenanceModes), "every mode series should be present")
		for _, mm := range types.AllMaintenanceModes {
			assert.Equal(t, float64(0), values[mm.String()], "mode %s should start at 0", mm)
		}
	})

	t.Run("OneHotOnSet", func(t *testing.T) {
		metrics.SetMaintenanceModeState(types.MaintenanceModeCordonAndDrain)
		values := modeValues()
		assert.Equal(t, float64(1), values[types.MaintenanceModeCordonAndDrain.String()])
		assert.Equal(t, float64(0), values[types.MaintenanceModeCordon.String()])
		assert.Equal(t, float64(0), values[types.MaintenanceModeNone.String()])
	})

	t.Run("SwitchClearsPreviousMode", func(t *testing.T) {
		metrics.SetMaintenanceModeState(types.MaintenanceModeNone)
		values := modeValues()
		assert.Equal(t, float64(1), values[types.MaintenanceModeNone.String()])
		assert.Equal(t, float64(0), values[types.MaintenanceModeCordonAndDrain.String()])
		assert.Equal(t, float64(0), values[types.MaintenanceModeCordon.String()])
	})
}

// TestMaintenanceModeStateMetricNilSafety tests nil-safety.
func TestMaintenanceModeStateMetricNilSafety(t *testing.T) {
	var metrics *Metrics
	assert.NotPanics(t, func() {
		metrics.SetMaintenanceModeState(types.MaintenanceModeCordon)
	})
}

// TestWithMaintenanceModeOption tests the WithMaintenanceMode option sets the
// active mode series to 1 at construction.
func TestWithMaintenanceModeOption(t *testing.T) {
	reg := prometheus.NewRegistry()
	ctx := WithDefaultMetrics(context.Background(), "nca-123", "test-cluster", "test-group", "v1.0.0",
		WithRegisterer(reg),
		WithMaintenanceMode(types.MaintenanceModeCordon))
	metrics := FromContext(ctx)
	t.Cleanup(func() { metrics.Destroy() })
	require.NotNil(t, metrics)

	metricFamilies, err := reg.Gather()
	require.NoError(t, err)
	found := false
	for _, mf := range metricFamilies {
		if *mf.Name != MaintenanceModeStateMetricName {
			continue
		}
		for _, metric := range mf.Metric {
			for _, lp := range metric.Label {
				if *lp.Name == MaintenanceModeLabel && *lp.Value == types.MaintenanceModeCordon.String() {
					assert.Equal(t, float64(1), *metric.Gauge.Value, "configured mode should be 1")
					found = true
				}
			}
		}
	}
	assert.True(t, found, "MaintenanceModeState metric for CordonOnly should be found")
}

// TestWithKataRuntimeIsolationEnabledOption tests the WithKataRuntimeIsolationEnabled option
func TestWithKataRuntimeIsolationEnabledOption(t *testing.T) {
	t.Run("WithEnabled", func(t *testing.T) {
		reg := prometheus.NewRegistry()
		ctx := WithDefaultMetrics(context.Background(), "nca-123", "test-cluster", "test-group", "v1.0.0",
			WithRegisterer(reg),
			WithKataRuntimeIsolationEnabled(true))
		metrics := FromContext(ctx)
		t.Cleanup(func() { metrics.Destroy() })
		require.NotNil(t, metrics)

		metricFamilies, err := reg.Gather()
		require.NoError(t, err)
		for _, mf := range metricFamilies {
			if *mf.Name == KataRuntimeIsolationEnabledMetricName {
				require.Greater(t, len(mf.Metric), 0)
				assert.Equal(t, float64(1), *mf.Metric[0].Gauge.Value, "Should be 1 when created with enabled=true")
				return
			}
		}
		t.Fatal("KataRuntimeIsolationEnabled metric not found")
	})

	t.Run("WithDisabled", func(t *testing.T) {
		reg := prometheus.NewRegistry()
		ctx := WithDefaultMetrics(context.Background(), "nca-123", "test-cluster", "test-group", "v1.0.0",
			WithRegisterer(reg),
			WithKataRuntimeIsolationEnabled(false))
		metrics := FromContext(ctx)
		t.Cleanup(func() { metrics.Destroy() })
		require.NotNil(t, metrics)

		metricFamilies, err := reg.Gather()
		require.NoError(t, err)
		for _, mf := range metricFamilies {
			if *mf.Name == KataRuntimeIsolationEnabledMetricName {
				require.Greater(t, len(mf.Metric), 0)
				assert.Equal(t, float64(0), *mf.Metric[0].Gauge.Value, "Should be 0 when created with enabled=false")
				return
			}
		}
		t.Fatal("KataRuntimeIsolationEnabled metric not found")
	})
}

// TestKataRuntimeIsolationEnabledMetricLabels tests that proper labels are applied
func TestKataRuntimeIsolationEnabledMetricLabels(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	ctx = WithDefaultMetrics(ctx, "nca-123", "test-cluster", "test-group", "v1.0.0", WithRegisterer(reg))
	metrics := FromContext(ctx)
	t.Cleanup(func() { metrics.Destroy() })
	require.NotNil(t, metrics)

	metrics.SetKataRuntimeIsolationEnabled(true)

	metricFamilies, err := reg.Gather()
	require.NoError(t, err)

	for _, mf := range metricFamilies {
		if *mf.Name == KataRuntimeIsolationEnabledMetricName {
			require.Greater(t, len(mf.Metric), 0)

			metric := mf.Metric[0]
			expectedLabels := map[string]string{
				NCAIDLabel:        "nca-123",
				ClusterNameLabel:  "test-cluster",
				ClusterGroupLabel: "test-group",
				VersionLabel:      "v1.0.0",
			}

			actualLabels := make(map[string]string)
			for _, label := range metric.Label {
				actualLabels[*label.Name] = *label.Value
			}

			// Should have exactly 4 default labels (no additional labels)
			assert.Len(t, metric.Label, 4, "Should have exactly 4 default labels")

			for expectedName, expectedValue := range expectedLabels {
				actualValue, exists := actualLabels[expectedName]
				assert.True(t, exists, "Label %s should exist", expectedName)
				assert.Equal(t, expectedValue, actualValue, "Label %s should have correct value", expectedName)
			}
		}
	}
}

// TestGCMetricsConcurrency tests concurrent access to GC metrics
func TestGCMetricsConcurrency(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	ctx = WithDefaultMetrics(ctx, "nca-123", "test-cluster", "test-group", "v1.0.0", WithRegisterer(reg))
	metrics := FromContext(ctx)
	t.Cleanup(func() { metrics.Destroy() })
	require.NotNil(t, metrics)

	const numGoroutines = 50
	const numCallsPerGoroutine = 10

	done := make(chan bool, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer func() { done <- true }()

			for j := 0; j < numCallsPerGoroutine; j++ {
				// Mix of success and failure calls
				if j%2 == 0 {
					metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypeNamespace, metricsgctypes.StatusSuccess)
					metrics.RecordGCCleanerRun("TestCleaner", metricsgctypes.StatusSuccess)
				} else {
					metrics.RecordOrphanedResourceCleanup(metricsgctypes.ResourceTypePVC, metricsgctypes.StatusFailure)
					metrics.RecordGCCleanerRun("TestCleaner", metricsgctypes.StatusFailure)
				}
			}
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Verify final metrics
	metricFamilies, err := reg.Gather()
	require.NoError(t, err)

	for _, mf := range metricFamilies {
		switch *mf.Name {
		case OrphanedResourceCleanupTotalMetricName:
			// Should have metrics for both namespace+success and pvc+failure
			assert.Greater(t, len(mf.Metric), 0, "Should have cleanup metrics")
			var totalCount float64
			for _, metric := range mf.Metric {
				totalCount += *metric.Counter.Value
			}
			expectedTotal := float64(numGoroutines * numCallsPerGoroutine)
			assert.Equal(t, expectedTotal, totalCount, "Total cleanup count should match expected")

		case GCCleanerRunTotalMetricName:
			// Should have metrics for both success and failure
			assert.Greater(t, len(mf.Metric), 0, "Should have cleaner run metrics")
			var totalCount float64
			for _, metric := range mf.Metric {
				totalCount += *metric.Counter.Value
			}
			expectedTotal := float64(numGoroutines * numCallsPerGoroutine)
			assert.Equal(t, expectedTotal, totalCount, "Total cleaner run count should match expected")
		}
	}
}

func TestMiniServiceMetricsRecorders(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	ctx = WithDefaultMetrics(ctx, "nca-123", "test-cluster", "test-group", "v1.0.0", WithRegisterer(reg))
	metrics := FromContext(ctx)
	t.Cleanup(func() { metrics.Destroy() })
	require.NotNil(t, metrics)

	metrics.RecordMiniServiceReconcilePhase("runtime-nca", "Running")
	metrics.RecordMiniServicePhaseTransition("runtime-nca", "Pending", "Running")
	metrics.RecordMiniServiceFailure("runtime-nca", "CreateFailed")
	metrics.SetMiniServiceReadyStatus("runtime-nca", 1)
	metrics.RecordMiniServiceReValRequest("runtime-nca", "/v1/reval", "200")
	metrics.RecordMiniServiceEventError("EVENT_TRANSLATE_FUNCTION_ERROR")

	metricFamilies, err := reg.Gather()
	require.NoError(t, err)

	findMetric := func(metricName string, expectedLabels map[string]string) *promdto.Metric {
		for _, mf := range metricFamilies {
			if *mf.Name != metricName {
				continue
			}
			for _, metric := range mf.Metric {
				actualLabels := make(map[string]string)
				for _, lbl := range metric.Label {
					actualLabels[*lbl.Name] = *lbl.Value
				}
				match := true
				for k, v := range expectedLabels {
					if actualLabels[k] != v {
						match = false
						break
					}
				}
				if match {
					return metric
				}
			}
		}
		return nil
	}

	reconcileMetric := findMetric(MiniServiceReconcilePhaseTotalMetricName, map[string]string{
		ClusterNameLabel:      "test-cluster",
		ClusterGroupLabel:     "test-group",
		VersionLabel:          "v1.0.0",
		NCAIDLabel:            "runtime-nca",
		MiniServicePhaseLabel: "Running",
	})
	require.NotNil(t, reconcileMetric)
	assert.Equal(t, float64(1), *reconcileMetric.Counter.Value)

	transitionMetric := findMetric(MiniServicePhaseTransitionsTotalMetricName, map[string]string{
		ClusterNameLabel:  "test-cluster",
		ClusterGroupLabel: "test-group",
		VersionLabel:      "v1.0.0",
		NCAIDLabel:        "runtime-nca",
		FromPhaseLabel:    "Pending",
		ToPhaseLabel:      "Running",
	})
	require.NotNil(t, transitionMetric)
	assert.Equal(t, float64(1), *transitionMetric.Counter.Value)

	failureMetric := findMetric(MiniServiceFailuresTotalMetricName, map[string]string{
		ClusterNameLabel:   "test-cluster",
		ClusterGroupLabel:  "test-group",
		VersionLabel:       "v1.0.0",
		NCAIDLabel:         "runtime-nca",
		FailureReasonLabel: "CreateFailed",
	})
	require.NotNil(t, failureMetric)
	assert.Equal(t, float64(1), *failureMetric.Counter.Value)

	readyMetric := findMetric(MiniServiceReadyStatusMetricName, map[string]string{
		ClusterNameLabel:       "test-cluster",
		ClusterGroupLabel:      "test-group",
		VersionLabel:           "v1.0.0",
		NCAIDLabel:             "runtime-nca",
		FunctionIDLabel:        "",
		FunctionVersionIDLabel: "",
		TaskIDLabel:            "",
	})
	require.NotNil(t, readyMetric)
	assert.Equal(t, float64(1), *readyMetric.Gauge.Value)

	revalMetric := findMetric(MiniServiceReValRequestTotalMetricName, map[string]string{
		ClusterNameLabel:  "test-cluster",
		ClusterGroupLabel: "test-group",
		VersionLabel:      "v1.0.0",
		NCAIDLabel:        "runtime-nca",
		EndpointLabel:     "/v1/reval",
		HTTPCodeLabel:     "200",
	})
	require.NotNil(t, revalMetric)
	assert.Equal(t, float64(1), *revalMetric.Counter.Value)

	eventErrorMetric := findMetric(MiniServiceEventErrorTotalMetricName, map[string]string{
		NCAIDLabel:        "nca-123",
		ClusterNameLabel:  "test-cluster",
		ClusterGroupLabel: "test-group",
		VersionLabel:      "v1.0.0",
		EventKindLabel:    "EVENT_TRANSLATE_FUNCTION_ERROR",
	})
	require.NotNil(t, eventErrorMetric)
	assert.Equal(t, float64(1), *eventErrorMetric.Counter.Value)
}

// TestMetricsInitializedToZero verifies that counter metrics are initialized to zero
// so they appear in the first Prometheus scrape.
func TestMetricsInitializedToZero(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	ctx = WithDefaultMetrics(ctx, "nca-123", "test-cluster", "test-group", "v1.0.0", WithRegisterer(reg))
	metrics := FromContext(ctx)
	t.Cleanup(func() { metrics.Destroy() })
	require.NotNil(t, metrics)

	// Gather metrics immediately - before any calls to increment
	metricFamilies, err := reg.Gather()
	require.NoError(t, err)

	// Helper to find metric value by name and label values
	findMetricValue := func(metricName string, labelName string, labelValue string) (float64, bool) {
		for _, mf := range metricFamilies {
			if *mf.Name == metricName {
				for _, metric := range mf.Metric {
					for _, label := range metric.Label {
						if *label.Name == labelName && *label.Value == labelValue {
							return *metric.Counter.Value, true
						}
					}
				}
			}
		}
		return 0, false
	}

	t.Run("K8sAPISuccessTotal initialized to zero", func(t *testing.T) {
		expectedResources := []string{
			"csidriver", "deployment", "namespace", "node", "pod", "runtimeclass",
			"secret", "serviceaccount", "icmsrequest", "storageclass", "storagerequests",
		}
		for _, resource := range expectedResources {
			value, found := findMetricValue(K8sAPISuccessTotalMetricName, "resource", resource)
			assert.True(t, found, "K8sAPISuccessTotal should be initialized for resource %s", resource)
			assert.Equal(t, float64(0), value, "K8sAPISuccessTotal should be zero for resource %s", resource)
		}
	})

	t.Run("K8sAPIFailureTotal initialized to zero", func(t *testing.T) {
		expectedResources := []string{
			"csidriver", "deployment", "namespace", "node", "pod", "runtimeclass",
			"secret", "serviceaccount", "icmsrequest", "storageclass", "storagerequests",
		}
		for _, resource := range expectedResources {
			value, found := findMetricValue(K8sAPIFailureTotalMetricName, "resource", resource)
			assert.True(t, found, "K8sAPIFailureTotal should be initialized for resource %s", resource)
			assert.Equal(t, float64(0), value, "K8sAPIFailureTotal should be zero for resource %s", resource)
		}
	})

	t.Run("ModelCache backend counters initialized to zero", func(t *testing.T) {
		counters := []string{
			ModelCacheBackendSelectedTotalMetricName,
			ModelCachePopulateTotalMetricName,
			ModelCacheReuseTotalMetricName,
			ModelCacheReclaimedTotalMetricName,
		}
		backends := []string{"nvmesh", "sharedfs", "samba", "ephemeral"}
		for _, name := range counters {
			for _, backend := range backends {
				value, found := findMetricValue(name, BackendLabel, backend)
				assert.True(t, found, "%s should be initialized for backend %s", name, backend)
				assert.Equal(t, float64(0), value, "%s should be zero for backend %s", name, backend)
			}
		}
	})

	t.Run("MiniServiceEventErrorTotal initialized to zero", func(t *testing.T) {
		expectedEventKinds := []string{
			"EVENT_TRANSLATE_FUNCTION_ERROR",
			"EVENT_TRANSLATE_TASK_ERROR",
		}
		for _, eventKind := range expectedEventKinds {
			value, found := findMetricValue(MiniServiceEventErrorTotalMetricName, "event_kind", eventKind)
			assert.True(t, found, "MiniServiceEventErrorTotal should be initialized for event_kind %s", eventKind)
			assert.Equal(t, float64(0), value, "MiniServiceEventErrorTotal should be zero for event_kind %s", eventKind)
		}
	})

	t.Run("OrphanedResourceCleanupTotal initialized to zero", func(t *testing.T) {
		expectedResourceTypes := []string{
			metricsgctypes.ResourceTypeNamespace,
			metricsgctypes.ResourceTypePersistentVolume,
			metricsgctypes.ResourceTypePVC,
			metricsgctypes.ResourceTypePod,
			metricsgctypes.ResourceTypeStorageClass,
			metricsgctypes.ResourceTypeStorageRequest,
		}
		for _, resourceType := range expectedResourceTypes {
			// Check both success and failure statuses
			for _, status := range []string{metricsgctypes.StatusSuccess, metricsgctypes.StatusFailure} {
				// Need to check both resource_type and status labels match
				var found bool
				var value float64
				for _, mf := range metricFamilies {
					if *mf.Name == OrphanedResourceCleanupTotalMetricName {
						for _, metric := range mf.Metric {
							hasResourceType := false
							hasStatus := false
							for _, label := range metric.Label {
								if *label.Name == "resource_type" && *label.Value == resourceType {
									hasResourceType = true
								}
								if *label.Name == "status" && *label.Value == status {
									hasStatus = true
								}
							}
							if hasResourceType && hasStatus {
								found = true
								value = *metric.Counter.Value
								break
							}
						}
					}
				}
				assert.True(t, found, "OrphanedResourceCleanupTotal should be initialized for resource_type=%s, status=%s", resourceType, status)
				assert.Equal(t, float64(0), value, "OrphanedResourceCleanupTotal should be zero for resource_type=%s, status=%s", resourceType, status)
			}
		}
	})

	t.Run("GCCleanerRunTotal initialized to zero", func(t *testing.T) {
		expectedCleanerNames := []string{
			"NamespaceCleaner",
			"PersistentVolumeCleaner",
			"PodCleaner",
			"StorageClassCleaner",
		}
		for _, cleanerName := range expectedCleanerNames {
			// Check both success and failure statuses
			for _, status := range []string{metricsgctypes.StatusSuccess, metricsgctypes.StatusFailure} {
				var found bool
				var value float64
				for _, mf := range metricFamilies {
					if *mf.Name == GCCleanerRunTotalMetricName {
						for _, metric := range mf.Metric {
							hasCleanerName := false
							hasStatus := false
							for _, label := range metric.Label {
								if *label.Name == "cleaner_name" && *label.Value == cleanerName {
									hasCleanerName = true
								}
								if *label.Name == "status" && *label.Value == status {
									hasStatus = true
								}
							}
							if hasCleanerName && hasStatus {
								found = true
								value = *metric.Counter.Value
								break
							}
						}
					}
				}
				assert.True(t, found, "GCCleanerRunTotal should be initialized for cleaner_name=%s, status=%s", cleanerName, status)
				assert.Equal(t, float64(0), value, "GCCleanerRunTotal should be zero for cleaner_name=%s, status=%s", cleanerName, status)
			}
		}
	})

	t.Run("KataRuntimeIsolationEnabled initialized to zero", func(t *testing.T) {
		var found bool
		var value float64
		for _, mf := range metricFamilies {
			if *mf.Name == KataRuntimeIsolationEnabledMetricName {
				for _, metric := range mf.Metric {
					found = true
					value = *metric.Gauge.Value
					break
				}
			}
		}
		assert.True(t, found, "KataRuntimeIsolationEnabled should be initialized")
		assert.Equal(t, float64(0), value, "KataRuntimeIsolationEnabled should be zero (disabled)")
	})

	t.Run("ModelCacheResultTotal initialized to zero", func(t *testing.T) {
		// Helper to find model cache metric by result and failure_reason
		findModelCacheMetric := func(resultValue, failureReasonValue string) (float64, bool) {
			for _, mf := range metricFamilies {
				if *mf.Name == ModelCacheResultTotalMetricName {
					for _, metric := range mf.Metric {
						hasResult := false
						hasReason := false
						for _, label := range metric.Label {
							if *label.Name == ResultLabel && *label.Value == resultValue {
								hasResult = true
							}
							if *label.Name == FailureReasonLabel && *label.Value == failureReasonValue {
								hasReason = true
							}
						}
						if hasResult && hasReason {
							return *metric.Counter.Value, true
						}
					}
				}
			}
			return 0, false
		}

		// Success should be initialized
		value, found := findModelCacheMetric(modelcachetypes.ResultSuccess, "")
		assert.True(t, found, "ModelCacheResultTotal should be initialized for result=success")
		assert.Equal(t, float64(0), value, "ModelCacheResultTotal should be zero for result=success")

		// All failure reasons should be initialized
		for _, reason := range modelcachetypes.AllFailureReasons {
			value, found := findModelCacheMetric(modelcachetypes.ResultFailure, reason)
			assert.True(t, found, "ModelCacheResultTotal should be initialized for result=failure, failure_reason=%s", reason)
			assert.Equal(t, float64(0), value, "ModelCacheResultTotal should be zero for result=failure, failure_reason=%s", reason)
		}
	})

	t.Run("WorkloadResultTotal initialized to zero", func(t *testing.T) {
		// Helper to find workload result metric by label values
		findWorkloadResultMetric := func(wt, wk, status, fc string) (float64, bool) {
			for _, mf := range metricFamilies {
				if *mf.Name == WorkloadResultTotalMetricName {
					for _, metric := range mf.Metric {
						hasWT := false
						hasWK := false
						hasStatus := false
						hasFC := false
						for _, label := range metric.Label {
							if *label.Name == WorkloadTypeLabel && *label.Value == wt {
								hasWT = true
							}
							if *label.Name == WorkloadKindLabel && *label.Value == wk {
								hasWK = true
							}
							if *label.Name == WorkloadStatusLabel && *label.Value == status {
								hasStatus = true
							}
							if *label.Name == FailureCategoryLabel && *label.Value == fc {
								hasFC = true
							}
						}
						if hasWT && hasWK && hasStatus && hasFC {
							return *metric.Counter.Value, true
						}
					}
				}
			}
			return 0, false
		}

		for _, wt := range workloadtypes.AllWorkloadTypes {
			for _, wk := range workloadtypes.AllWorkloadKinds {
				// Success should be initialized
				value, found := findWorkloadResultMetric(string(wt), string(wk), string(workloadtypes.WorkloadStatusSuccess), "")
				assert.True(t, found, "WorkloadResultTotal should be initialized for workload_type=%s, workload_kind=%s, workload_status=success", wt, wk)
				assert.Equal(t, float64(0), value, "WorkloadResultTotal should be zero for workload_type=%s, workload_kind=%s, workload_status=success", wt, wk)

				// All failure categories should be initialized
				for _, fc := range workloadtypes.AllFailureCategories {
					value, found := findWorkloadResultMetric(string(wt), string(wk), string(workloadtypes.WorkloadStatusFailure), string(fc))
					assert.True(t, found,
						"WorkloadResultTotal should be initialized for workload_type=%s, workload_kind=%s, workload_status=failure, failure_category=%s", wt, wk, fc)
					assert.Equal(t, float64(0), value,
						"WorkloadResultTotal should be zero for workload_type=%s, workload_kind=%s, workload_status=failure, failure_category=%s", wt, wk, fc)
				}
			}
		}
	})

	t.Run("SchedulerWorkloadCount initialized to zero", func(t *testing.T) {
		for _, scheduler := range []string{SchedulerNameDefault, SchedulerNameKAI} {
			for _, wk := range workloadtypes.AllWorkloadKinds {
				var found bool
				var value float64
				for _, mf := range metricFamilies {
					if *mf.Name == SchedulerWorkloadCountMetricName {
						for _, metric := range mf.Metric {
							hasScheduler := false
							hasWK := false
							for _, label := range metric.Label {
								if *label.Name == SchedulerNameLabel && *label.Value == scheduler {
									hasScheduler = true
								}
								if *label.Name == WorkloadKindLabel && *label.Value == string(wk) {
									hasWK = true
								}
							}
							if hasScheduler && hasWK {
								found = true
								value = *metric.Gauge.Value
								break
							}
						}
					}
				}
				assert.True(t, found, "SchedulerWorkloadCount should be initialized for scheduler=%s, workload_kind=%s", scheduler, wk)
				assert.Equal(t, float64(0), value, "SchedulerWorkloadCount should be zero for scheduler=%s, workload_kind=%s", scheduler, wk)
			}
		}
	})

	t.Run("UpstreamRequestTotal initialized to zero", func(t *testing.T) {
		for _, op := range AllUpstreamOperations {
			// success series
			var found bool
			var value float64
			for _, mf := range metricFamilies {
				if *mf.Name == UpstreamRequestTotalMetricName {
					for _, metric := range mf.Metric {
						hasOp := false
						hasStatus := false
						hasHTTPStatus := false
						for _, label := range metric.Label {
							if *label.Name == OperationLabel && *label.Value == op {
								hasOp = true
							}
							if *label.Name == StatusLabel && *label.Value == "success" {
								hasStatus = true
							}
							if *label.Name == HTTPStatusLabel && *label.Value == "200" {
								hasHTTPStatus = true
							}
						}
						if hasOp && hasStatus && hasHTTPStatus {
							found = true
							value = *metric.Counter.Value
							break
						}
					}
				}
			}
			assert.True(t, found, "UpstreamRequestTotal should be initialized for operation=%s,status=success", op)
			assert.Equal(t, float64(0), value, "UpstreamRequestTotal should be zero for operation=%s,status=success", op)
		}
	})

	t.Run("StorageRequestDuration pre-registered for all phases", func(t *testing.T) {
		// Verify all 6 storage phases appear on the first scrape so Prometheus panels
		// can compute Ready vs all-terminal ratios without gaps.
		findHistogramCount := func(phase string) (uint64, bool) {
			for _, mf := range metricFamilies {
				if *mf.Name == StorageRequestDurationMetricName {
					for _, metric := range mf.Metric {
						for _, label := range metric.Label {
							if *label.Name == StorageRequestPhaseLabel && *label.Value == phase {
								return metric.Histogram.GetSampleCount(), true
							}
						}
					}
				}
			}
			return 0, false
		}
		for _, phase := range []string{"Pending", "InitRunning", "Creating", "Ready", "Failed", "RuntimeError"} {
			count, found := findHistogramCount(phase)
			assert.True(t, found, "StorageRequestDuration should be pre-registered for phase %s", phase)
			assert.Equal(t, uint64(0), count, "StorageRequestDuration count should be zero for phase %s", phase)
		}
	})

	t.Run("StorageRequestDuration uses long-running SLO buckets", func(t *testing.T) {
		// The metric must be a histogram with the coarse, minutes-scale buckets tuned
		// for the 4-minute (240s) provisioning SLO, including the 240s boundary.
		var bounds []float64
		for _, mf := range metricFamilies {
			if *mf.Name == StorageRequestDurationMetricName {
				require.NotEmpty(t, mf.Metric)
				for _, b := range mf.Metric[0].Histogram.Bucket {
					bounds = append(bounds, b.GetUpperBound())
				}
				break
			}
		}
		assert.Equal(t, storageRequestDurationBucketsSeconds, bounds,
			"histogram bucket boundaries must match the OTel-aligned long-running buckets")
		assert.Contains(t, bounds, float64(240), "the 240s SLO boundary must be a bucket")
	})
}

// TestWorkloadResultMetric tests the workload result metric
func TestWorkloadResultMetric(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	ctx = WithDefaultMetrics(ctx, "nca-123", "test-cluster", "test-group", "v1.0.0", WithRegisterer(reg))
	metrics := FromContext(ctx)
	t.Cleanup(func() { metrics.Destroy() })
	require.NotNil(t, metrics)

	// Verify metric is initialized
	require.NotNil(t, metrics.WorkloadResultTotal)

	t.Run("RecordSuccess", func(t *testing.T) {
		metrics.RecordWorkloadStatus(
			workloadtypes.WorkloadTypeContainer,
			workloadtypes.WorkloadKindFunction,
			workloadtypes.WorkloadStatusSuccess,
			workloadtypes.FailureCategoryNone,
		)

		metricFamilies, err := reg.Gather()
		require.NoError(t, err)

		found := false
		for _, mf := range metricFamilies {
			if *mf.Name == WorkloadResultTotalMetricName {
				for _, metric := range mf.Metric {
					labels := make(map[string]string)
					for _, label := range metric.Label {
						labels[*label.Name] = *label.Value
					}
					if labels[WorkloadTypeLabel] == string(workloadtypes.WorkloadTypeContainer) &&
						labels[WorkloadKindLabel] == string(workloadtypes.WorkloadKindFunction) &&
						labels[WorkloadStatusLabel] == string(workloadtypes.WorkloadStatusSuccess) &&
						labels[FailureCategoryLabel] == "" {
						found = true
						assert.Equal(t, float64(1), *metric.Counter.Value)
					}
				}
			}
		}
		assert.True(t, found, "WorkloadResultTotal success metric should be found")
	})

	t.Run("RecordFailure", func(t *testing.T) {
		metrics.RecordWorkloadStatus(
			workloadtypes.WorkloadTypeHelm,
			workloadtypes.WorkloadKindTask,
			workloadtypes.WorkloadStatusFailure,
			workloadtypes.FailureCategoryImagePull,
		)

		metricFamilies, err := reg.Gather()
		require.NoError(t, err)

		found := false
		for _, mf := range metricFamilies {
			if *mf.Name == WorkloadResultTotalMetricName {
				for _, metric := range mf.Metric {
					labels := make(map[string]string)
					for _, label := range metric.Label {
						labels[*label.Name] = *label.Value
					}
					if labels[WorkloadTypeLabel] == string(workloadtypes.WorkloadTypeHelm) &&
						labels[WorkloadKindLabel] == string(workloadtypes.WorkloadKindTask) &&
						labels[WorkloadStatusLabel] == string(workloadtypes.WorkloadStatusFailure) &&
						labels[FailureCategoryLabel] == string(workloadtypes.FailureCategoryImagePull) {
						found = true
						assert.Equal(t, float64(1), *metric.Counter.Value)
					}
				}
			}
		}
		assert.True(t, found, "WorkloadResultTotal failure metric should be found")
	})
}

// TestWorkloadResultMetricNilSafety tests nil-safety of workload result metric
func TestWorkloadResultMetricNilSafety(t *testing.T) {
	var metrics *Metrics

	assert.NotPanics(t, func() {
		metrics.RecordWorkloadStatus(
			workloadtypes.WorkloadTypeContainer,
			workloadtypes.WorkloadKindFunction,
			workloadtypes.WorkloadStatusSuccess,
			workloadtypes.FailureCategoryNone,
		)
	})

	assert.NotPanics(t, func() {
		metrics.RecordWorkloadStatus(
			workloadtypes.WorkloadTypeHelm,
			workloadtypes.WorkloadKindTask,
			workloadtypes.WorkloadStatusFailure,
			workloadtypes.FailureCategoryImagePull,
		)
	})
}

// TestWorkloadResultMetricLabels tests that workload result metrics have correct labels
func TestWorkloadResultMetricLabels(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	ctx = WithDefaultMetrics(ctx, "nca-123", "test-cluster", "test-group", "v1.0.0", WithRegisterer(reg))
	metrics := FromContext(ctx)
	t.Cleanup(func() { metrics.Destroy() })
	require.NotNil(t, metrics)

	metrics.RecordWorkloadStatus(
		workloadtypes.WorkloadTypeContainer,
		workloadtypes.WorkloadKindFunction,
		workloadtypes.WorkloadStatusFailure,
		workloadtypes.FailureCategoryNoCapacity,
	)

	metricFamilies, err := reg.Gather()
	require.NoError(t, err)

	expectedLabels := map[string]string{
		NCAIDLabel:           "nca-123",
		ClusterNameLabel:     "test-cluster",
		ClusterGroupLabel:    "test-group",
		VersionLabel:         "v1.0.0",
		WorkloadTypeLabel:    string(workloadtypes.WorkloadTypeContainer),
		WorkloadKindLabel:    string(workloadtypes.WorkloadKindFunction),
		WorkloadStatusLabel:  string(workloadtypes.WorkloadStatusFailure),
		FailureCategoryLabel: string(workloadtypes.FailureCategoryNoCapacity),
	}

	found := false
	for _, mf := range metricFamilies {
		if *mf.Name == WorkloadResultTotalMetricName {
			for _, metric := range mf.Metric {
				actualLabels := make(map[string]string)
				for _, label := range metric.Label {
					actualLabels[*label.Name] = *label.Value
				}
				if actualLabels[WorkloadTypeLabel] == string(workloadtypes.WorkloadTypeContainer) &&
					actualLabels[WorkloadKindLabel] == string(workloadtypes.WorkloadKindFunction) &&
					actualLabels[WorkloadStatusLabel] == string(workloadtypes.WorkloadStatusFailure) &&
					actualLabels[FailureCategoryLabel] == string(workloadtypes.FailureCategoryNoCapacity) {
					found = true
					for expectedName, expectedValue := range expectedLabels {
						actualValue, exists := actualLabels[expectedName]
						assert.True(t, exists, "Label %s should exist", expectedName)
						assert.Equal(t, expectedValue, actualValue, "Label %s should have correct value", expectedName)
					}
				}
			}
		}
	}
	assert.True(t, found, "Should find metric with expected labels")
}

// TestWorkloadResultMetricFailureCategories tests all failure category values
func TestWorkloadResultMetricFailureCategories(t *testing.T) {
	for _, fc := range workloadtypes.AllFailureCategories {
		t.Run(string(fc), func(t *testing.T) {
			reg := prometheus.NewRegistry()
			ctx := context.Background()
			ctx = WithDefaultMetrics(ctx, "nca-123", "test-cluster", "test-group", "v1.0.0", WithRegisterer(reg))
			metrics := FromContext(ctx)
			require.NotNil(t, metrics)

			metrics.RecordWorkloadStatus(
				workloadtypes.WorkloadTypeContainer,
				workloadtypes.WorkloadKindFunction,
				workloadtypes.WorkloadStatusFailure,
				fc,
			)

			metricFamilies, err := reg.Gather()
			require.NoError(t, err)

			found := false
			for _, mf := range metricFamilies {
				if *mf.Name == WorkloadResultTotalMetricName {
					for _, metric := range mf.Metric {
						labels := make(map[string]string)
						for _, label := range metric.Label {
							labels[*label.Name] = *label.Value
						}
						if labels[WorkloadTypeLabel] == string(workloadtypes.WorkloadTypeContainer) &&
							labels[WorkloadKindLabel] == string(workloadtypes.WorkloadKindFunction) &&
							labels[WorkloadStatusLabel] == string(workloadtypes.WorkloadStatusFailure) &&
							labels[FailureCategoryLabel] == string(fc) {
							found = true
							assert.Equal(t, float64(1), *metric.Counter.Value)
						}
					}
				}
			}
			assert.True(t, found, "Should find metric for failure category %s", fc)
		})
	}
}

// TestICMSInstanceStateToFailureCategory tests the mapping function
func TestICMSInstanceStateToFailureCategory(t *testing.T) {
	tests := []struct {
		state    types.ICMSInstanceState
		expected workloadtypes.FailureCategory
	}{
		{types.ICMSInstanceFailedImagePullIssues, workloadtypes.FailureCategoryImagePull},
		{types.ICMSInstanceFailedInitContainerStuck, workloadtypes.FailureCategoryInitStuck},
		{types.ICMSInstanceFailedInitContainerRestartLoop, workloadtypes.FailureCategoryInitRestartLoop},
		{types.ICMSInstanceFailedContainerRestartLoop, workloadtypes.FailureCategoryContainerRestart},
		{types.ICMSInstanceKilledNoCapacity, workloadtypes.FailureCategoryNoCapacity},
		{types.ICMSInstanceKilledAdmissionError, workloadtypes.FailureCategoryAdmissionError},
		{types.ICMSInstanceSharedStorageFailure, workloadtypes.FailureCategorySharedStorage},
		{types.ICMSInstanceInternalPersistentStorageFailure, workloadtypes.FailureCategoryPersistentStorage},
		{types.ICMSInstanceDegradedWorker, workloadtypes.FailureCategoryDegradedWorker},
		{types.ICMSInstanceFailedNotFound, workloadtypes.FailureCategoryNotFound},
		{types.ICMSInstanceTerminatedTerminalError, workloadtypes.FailureCategoryTerminalError},
		{types.ICMSInstanceTerminatedDuetoSyncAction, workloadtypes.FailureCategorySyncAction},
		{types.ICMSInstanceTerminatedServiceMaintenance, workloadtypes.FailureCategoryServiceMaintenance},
		{types.ICMSInstanceTerminatedPreconditionFailure, workloadtypes.FailureCategoryPreconditionFail},
		{types.ICMSInstanceFailed, workloadtypes.FailureCategoryUnknown},
		// States that should return FailureCategoryNone
		{types.ICMSInstanceRunning, workloadtypes.FailureCategoryNone},
		{types.ICMSInstanceStarted, workloadtypes.FailureCategoryNone},
		{types.ICMSInstanceTerminated, workloadtypes.FailureCategoryNone},
		{types.ICMSInstanceStateNoStatus, workloadtypes.FailureCategoryNone},
		{types.ICMSInstanceSucceeded, workloadtypes.FailureCategoryNone},
	}

	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			result := ICMSInstanceStateToFailureCategory(tt.state)
			assert.Equal(t, tt.expected, result, "ICMSInstanceState %s should map to %q", tt.state, tt.expected)
		})
	}
}

// TestICMSInstanceStateToFailureCategoryCoverage verifies all failure-related
// ICMSInstanceState values have explicit mappings to non-empty FailureCategory.
// This test ensures new failure states are not silently missed.
func TestICMSInstanceStateToFailureCategoryCoverage(t *testing.T) {
	failureStates := []types.ICMSInstanceState{
		types.ICMSInstanceFailed,
		types.ICMSInstanceKilledNoCapacity,
		types.ICMSInstanceKilledAdmissionError,
		types.ICMSInstanceFailedInitContainerStuck,
		types.ICMSInstanceFailedImagePullIssues,
		types.ICMSInstanceFailedInitContainerRestartLoop,
		types.ICMSInstanceFailedContainerRestartLoop,
		types.ICMSInstanceFailedNotFound,
		types.ICMSInstanceTerminatedPreconditionFailure,
		types.ICMSInstanceTerminatedTerminalError,
		types.ICMSInstanceDegradedWorker,
		types.ICMSInstanceTerminatedDuetoSyncAction,
		types.ICMSInstanceTerminatedServiceMaintenance,
		types.ICMSInstanceSharedStorageFailure,
		types.ICMSInstanceInternalPersistentStorageFailure,
	}

	for _, state := range failureStates {
		t.Run(string(state), func(t *testing.T) {
			category := ICMSInstanceStateToFailureCategory(state)
			assert.NotEqual(t, workloadtypes.FailureCategoryNone, category,
				"failure state %s should map to a non-empty failure category", state)
		})
	}
}

// TestActionToWorkloadKind tests the action-to-workload-kind mapping
func TestActionToWorkloadKind(t *testing.T) {
	tests := []struct {
		name     string
		action   translatecommon.MessageAction
		expected workloadtypes.WorkloadKind
	}{
		{
			name:     "FunctionCreationAction maps to function",
			action:   translatecommon.FunctionCreationAction,
			expected: workloadtypes.WorkloadKindFunction,
		},
		{
			name:     "TaskCreationAction maps to task",
			action:   translatecommon.TaskCreationAction,
			expected: workloadtypes.WorkloadKindTask,
		},
		{
			name:     "TerminationAction defaults to function",
			action:   translatecommon.TerminationAction,
			expected: workloadtypes.WorkloadKindFunction,
		},
		{
			name:     "Unknown action defaults to function",
			action:   "unknown-action",
			expected: workloadtypes.WorkloadKindFunction,
		},
		{
			name:     "Empty action defaults to function",
			action:   "",
			expected: workloadtypes.WorkloadKindFunction,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ActionToWorkloadKind(tt.action)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestRecordUpstreamRequest tests the RecordUpstreamRequest helper.
func TestRecordUpstreamRequest(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	ctx = WithDefaultMetrics(ctx, "nca-123", "test-cluster", "test-group", "v1.0.0", WithRegisterer(reg))
	m := FromContext(ctx)
	t.Cleanup(func() { m.Destroy() })
	require.NotNil(t, m)

	findMetric := func(operation, status, httpStatus string) (float64, bool) {
		mfs, err := reg.Gather()
		require.NoError(t, err)
		for _, mf := range mfs {
			if *mf.Name != UpstreamRequestTotalMetricName {
				continue
			}
			for _, metric := range mf.Metric {
				labels := make(map[string]string)
				for _, lp := range metric.Label {
					labels[*lp.Name] = *lp.Value
				}
				if labels[OperationLabel] == operation && labels[StatusLabel] == status && labels[HTTPStatusLabel] == httpStatus {
					return *metric.Counter.Value, true
				}
			}
		}
		return 0, false
	}

	t.Run("success records http_status=200", func(t *testing.T) {
		m.RecordUpstreamRequest(UpstreamOperationHeartbeat, nil)
		val, found := findMetric(UpstreamOperationHeartbeat, "success", "200")
		assert.True(t, found)
		assert.Equal(t, float64(1), val)
	})

	t.Run("non-HTTP error records http_status=0", func(t *testing.T) {
		m.RecordUpstreamRequest(UpstreamOperationHeartbeat, fmt.Errorf("wrapped: %w", k8serrors.NewUnauthorized("bad token")))
		// non-HTTP error (no nvcaerrors.HTTPStatusError in chain) => http_status="0"
		val, found := findMetric(UpstreamOperationHeartbeat, "failure", "0")
		assert.True(t, found)
		assert.Equal(t, float64(1), val)
	})

	t.Run("register operation success", func(t *testing.T) {
		m.RecordUpstreamRequest(UpstreamOperationRegister, nil)
		val, found := findMetric(UpstreamOperationRegister, "success", "200")
		assert.True(t, found)
		assert.Equal(t, float64(1), val)
	})

	t.Run("credentials operation success", func(t *testing.T) {
		m.RecordUpstreamRequest(UpstreamOperationCredentials, nil)
		val, found := findMetric(UpstreamOperationCredentials, "success", "200")
		assert.True(t, found)
		assert.Equal(t, float64(1), val)
	})
}

// TestSchedulerWorkloadCountMetric tests the scheduler workload count gauge metric
func TestSchedulerWorkloadCountMetric(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	ctx = WithDefaultMetrics(ctx, "nca-123", "test-cluster", "test-group", "v1.0.0", WithRegisterer(reg))
	metrics := FromContext(ctx)
	t.Cleanup(func() { metrics.Destroy() })
	require.NotNil(t, metrics)

	require.NotNil(t, metrics.SchedulerWorkloadCount)

	getGaugeValue := func(schedulerName, workloadKind string) (float64, bool) {
		metricFamilies, err := reg.Gather()
		require.NoError(t, err)
		for _, mf := range metricFamilies {
			if *mf.Name == SchedulerWorkloadCountMetricName {
				for _, metric := range mf.Metric {
					labels := make(map[string]string)
					for _, label := range metric.Label {
						labels[*label.Name] = *label.Value
					}
					if labels[SchedulerNameLabel] == schedulerName &&
						labels[WorkloadKindLabel] == workloadKind {
						return *metric.Gauge.Value, true
					}
				}
			}
		}
		return 0, false
	}

	t.Run("InitializedToZero", func(t *testing.T) {
		for _, scheduler := range []string{SchedulerNameDefault, SchedulerNameKAI} {
			for _, wk := range workloadtypes.AllWorkloadKinds {
				value, found := getGaugeValue(scheduler, string(wk))
				assert.True(t, found, "SchedulerWorkloadCount should be initialized for scheduler=%s, workload_kind=%s", scheduler, wk)
				assert.Equal(t, float64(0), value, "SchedulerWorkloadCount should be zero for scheduler=%s, workload_kind=%s", scheduler, wk)
			}
		}
	})

	t.Run("SetAndRead", func(t *testing.T) {
		metrics.SetSchedulerWorkloadCount(SchedulerNameKAI, workloadtypes.WorkloadKindFunction, 5)
		metrics.SetSchedulerWorkloadCount(SchedulerNameDefault, workloadtypes.WorkloadKindTask, 3)

		value, found := getGaugeValue(SchedulerNameKAI, string(workloadtypes.WorkloadKindFunction))
		assert.True(t, found)
		assert.Equal(t, float64(5), value)

		value, found = getGaugeValue(SchedulerNameDefault, string(workloadtypes.WorkloadKindTask))
		assert.True(t, found)
		assert.Equal(t, float64(3), value)

		// Untouched combinations should still be zero
		value, found = getGaugeValue(SchedulerNameDefault, string(workloadtypes.WorkloadKindFunction))
		assert.True(t, found)
		assert.Equal(t, float64(0), value)
	})

	t.Run("DecreasesCorrectly", func(t *testing.T) {
		metrics.SetSchedulerWorkloadCount(SchedulerNameKAI, workloadtypes.WorkloadKindFunction, 5)
		value, found := getGaugeValue(SchedulerNameKAI, string(workloadtypes.WorkloadKindFunction))
		assert.True(t, found)
		assert.Equal(t, float64(5), value)

		metrics.SetSchedulerWorkloadCount(SchedulerNameKAI, workloadtypes.WorkloadKindFunction, 2)
		value, found = getGaugeValue(SchedulerNameKAI, string(workloadtypes.WorkloadKindFunction))
		assert.True(t, found)
		assert.Equal(t, float64(2), value)
	})
}

// TestSchedulerWorkloadCountMetricNilSafety tests nil-safety
func TestSchedulerWorkloadCountMetricNilSafety(t *testing.T) {
	var metrics *Metrics

	assert.NotPanics(t, func() {
		metrics.SetSchedulerWorkloadCount(SchedulerNameDefault, workloadtypes.WorkloadKindFunction, 5)
	})

	assert.NotPanics(t, func() {
		metrics.SetSchedulerWorkloadCount(SchedulerNameKAI, workloadtypes.WorkloadKindTask, 3)
	})
}

// TestSchedulerWorkloadCountMetricLabels tests that proper labels are applied
func TestSchedulerWorkloadCountMetricLabels(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	ctx = WithDefaultMetrics(ctx, "nca-123", "test-cluster", "test-group", "v1.0.0", WithRegisterer(reg))
	metrics := FromContext(ctx)
	t.Cleanup(func() { metrics.Destroy() })
	require.NotNil(t, metrics)

	metrics.SetSchedulerWorkloadCount(SchedulerNameKAI, workloadtypes.WorkloadKindFunction, 7)

	metricFamilies, err := reg.Gather()
	require.NoError(t, err)

	found := false
	for _, mf := range metricFamilies {
		if *mf.Name == SchedulerWorkloadCountMetricName {
			for _, metric := range mf.Metric {
				actualLabels := make(map[string]string)
				for _, label := range metric.Label {
					actualLabels[*label.Name] = *label.Value
				}
				if actualLabels[SchedulerNameLabel] == SchedulerNameKAI &&
					actualLabels[WorkloadKindLabel] == string(workloadtypes.WorkloadKindFunction) {
					found = true
					expectedLabels := map[string]string{
						NCAIDLabel:         "nca-123",
						ClusterNameLabel:   "test-cluster",
						ClusterGroupLabel:  "test-group",
						VersionLabel:       "v1.0.0",
						SchedulerNameLabel: SchedulerNameKAI,
						WorkloadKindLabel:  string(workloadtypes.WorkloadKindFunction),
					}
					assert.Len(t, metric.Label, 6, "Should have 4 default labels + scheduler_name + workload_kind")
					for expectedName, expectedValue := range expectedLabels {
						actualValue, exists := actualLabels[expectedName]
						assert.True(t, exists, "Label %s should exist", expectedName)
						assert.Equal(t, expectedValue, actualValue, "Label %s should have correct value", expectedName)
					}
				}
			}
		}
	}
	assert.True(t, found, "Should find SchedulerWorkloadCount metric with scheduler_name=kai-scheduler, workload_kind=function")
}

// TestRecordUpstreamRequestNilSafety tests nil-safety.
func TestRecordUpstreamRequestNilSafety(t *testing.T) {
	var m *Metrics
	assert.NotPanics(t, func() { m.RecordUpstreamRequest(UpstreamOperationHeartbeat, nil) })
	assert.NotPanics(t, func() { m.RecordUpstreamRequest(UpstreamOperationHeartbeat, fmt.Errorf("error")) })
}

// gatherClusterValidatorSeries returns every series for a cluster-validator
// metric family, so tests can assert which series are exposed.
func gatherClusterValidatorSeries(t *testing.T, reg *prometheus.Registry, name string) []*promdto.Metric {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() == name {
			return mf.Metric
		}
	}
	return nil
}

func labelValue(m *promdto.Metric, key string) string {
	for _, l := range m.Label {
		if l.GetName() == key {
			return l.GetValue()
		}
	}
	return ""
}

func hasLabel(m *promdto.Metric, key string) bool {
	for _, l := range m.Label {
		if l.GetName() == key {
			return true
		}
	}
	return false
}

// TestClusterValidatorMetrics_BaselineThenUpdateInPlace verifies the gauges
// start at the init-to-zero baseline and that a real run updates the same
// series in place. The metrics carry no run_timestamp label, so there is no
// per-run series churn.
func TestClusterValidatorMetrics_BaselineThenUpdateInPlace(t *testing.T) {
	reg := prometheus.NewRegistry()
	ctx := WithDefaultMetrics(context.Background(), "nca-1", "c1", "g1", "v1", WithRegisterer(reg))
	m := FromContext(ctx)
	require.NotNil(t, m)
	t.Cleanup(func() { m.Destroy() })

	// At init: exactly one baseline series at 0, with no run_timestamp label.
	ready := gatherClusterValidatorSeries(t, reg, ClusterValidatorReadyMetricName)
	require.Len(t, ready, 1)
	assert.False(t, hasLabel(ready[0], "run_timestamp"), "metrics must not carry a run_timestamp label")
	assert.Equal(t, 0.0, ready[0].GetGauge().GetValue())

	// First real run updates the same series in place.
	m.SetClusterValidatorSummary(&ClusterValidatorSummary{
		RanAtUnixSec:    1781175999,
		DurationSeconds: 90.8,
		VerdictReady:    true,
		Checks: map[string]bool{
			"control_plane": true,
			"smb_csi":       true,
		},
		Endpoints: map[string]ClusterValidatorEndpoint{
			"NGC API": {Reachable: true, Critical: true},
		},
	})

	ready = gatherClusterValidatorSeries(t, reg, ClusterValidatorReadyMetricName)
	require.Len(t, ready, 1, "ready stays a single series, updated in place")
	assert.False(t, hasLabel(ready[0], "run_timestamp"))
	assert.Equal(t, 1.0, ready[0].GetGauge().GetValue())

	// LastRunTimestamp exposes the run time as a value (not a label).
	lrt := gatherClusterValidatorSeries(t, reg, ClusterValidatorLastRunTimestampMetricName)
	require.Len(t, lrt, 1)
	assert.Equal(t, float64(1781175999), lrt[0].GetGauge().GetValue())
}

// TestClusterValidatorMetrics_RunUpdatesInPlaceAndPrunes confirms a new run
// overwrites the fixed gauges in place (no accumulation) and prunes
// config-driven series the new run no longer reports.
func TestClusterValidatorMetrics_RunUpdatesInPlaceAndPrunes(t *testing.T) {
	reg := prometheus.NewRegistry()
	ctx := WithDefaultMetrics(context.Background(), "nca-1", "c1", "g1", "v1", WithRegisterer(reg))
	m := FromContext(ctx)
	require.NotNil(t, m)
	t.Cleanup(func() { m.Destroy() })

	// Run 1: ready, with one configured endpoint.
	m.SetClusterValidatorSummary(&ClusterValidatorSummary{
		RanAtUnixSec: 1, VerdictReady: true,
		Checks:    map[string]bool{"control_plane": true},
		Endpoints: map[string]ClusterValidatorEndpoint{"NGC API": {Reachable: true, Critical: true}},
	})
	// Run 2: not ready, endpoint removed from config.
	m.SetClusterValidatorSummary(&ClusterValidatorSummary{
		RanAtUnixSec: 2, VerdictReady: false,
		Checks: map[string]bool{"control_plane": false},
	})

	ready := gatherClusterValidatorSeries(t, reg, ClusterValidatorReadyMetricName)
	require.Len(t, ready, 1, "ready is a single series updated in place")
	assert.Equal(t, 0.0, ready[0].GetGauge().GetValue(), "in-place update reflects the latest run")

	assert.Empty(t, gatherClusterValidatorSeries(t, reg, ClusterValidatorEndpointReachableMetricName),
		"an endpoint removed from config must be pruned")
}

// TestClusterValidatorMetrics_ResetRestoresBaseline confirms the explicit
// reset returns the gauges to the init-to-zero baseline.
func TestClusterValidatorMetrics_ResetRestoresBaseline(t *testing.T) {
	reg := prometheus.NewRegistry()
	ctx := WithDefaultMetrics(context.Background(), "nca-1", "c1", "g1", "v1", WithRegisterer(reg))
	m := FromContext(ctx)
	require.NotNil(t, m)
	t.Cleanup(func() { m.Destroy() })

	m.SetClusterValidatorSummary(&ClusterValidatorSummary{
		RanAtUnixSec: 1, VerdictReady: true,
		Checks: map[string]bool{"control_plane": true},
	})
	m.ResetClusterValidatorMetrics()

	ready := gatherClusterValidatorSeries(t, reg, ClusterValidatorReadyMetricName)
	require.Len(t, ready, 1, "reset must restore a single baseline series")
	assert.Equal(t, 0.0, ready[0].GetGauge().GetValue())
}

// TestClusterValidatorCheckKeysSync guards the two hand-maintained check-key
// lists against drift: the init-to-zero baseline in metrics
// (clusterValidatorCheckKeys) must expose exactly the same checks the validator
// emits (clustervalidator.AllCheckKeys). If they diverge, the baseline would
// initialize a different set than real runs produce — a silent metric gap.
func TestClusterValidatorCheckKeysSync(t *testing.T) {
	assert.ElementsMatch(t, clustervalidator.AllCheckKeys, clusterValidatorCheckKeys(),
		"clusterValidatorCheckKeys() must match clustervalidator.AllCheckKeys; "+
			"when adding a CheckKey* constant, update both lists")
}

// TestClusterValidatorMetrics_NetpolDirectionalSeries confirms each pair
// emits one series per (direction, policy_side) carrying that side's
// allow/deny status, and that the prior run's tuples are pruned.
func TestClusterValidatorMetrics_NetpolDirectionalSeries(t *testing.T) {
	reg := prometheus.NewRegistry()
	ctx := WithDefaultMetrics(context.Background(), "nca-1", "c1", "g1", "v1", WithRegisterer(reg))
	m := FromContext(ctx)
	require.NotNil(t, m)
	t.Cleanup(func() { m.Destroy() })

	m.SetClusterValidatorSummary(&ClusterValidatorSummary{
		RanAtUnixSec: 1, VerdictReady: false,
		NetpolPairs: map[string]ClusterValidatorNetpolPair{
			"agent-to-api": {
				Passed:   false,
				Critical: true,
				Directions: map[string]ClusterValidatorNetpolDirection{
					"a_to_b": {EgressAllowed: true, IngressAllowed: true},
					"b_to_a": {EgressAllowed: true, IngressAllowed: false},
				},
			},
		},
	})

	series := gatherClusterValidatorSeries(t, reg, ClusterValidatorNetpolPairPassedMetricName)
	require.Len(t, series, 4, "one series per direction × policy_side")

	type key struct{ direction, side string }
	got := make(map[key]float64, 4)
	for _, s := range series {
		assert.Equal(t, "agent-to-api", labelValue(s, ClusterValidatorNetpolPairLabel))
		assert.Equal(t, "true", labelValue(s, ClusterValidatorCriticalLabel))
		assert.False(t, hasLabel(s, "run_timestamp"), "netpol series must not carry a run_timestamp label")
		got[key{labelValue(s, ClusterValidatorDirectionLabel), labelValue(s, ClusterValidatorPolicySideLabel)}] =
			s.GetGauge().GetValue()
	}
	assert.Equal(t, 1.0, got[key{"a_to_b", "egress"}])
	assert.Equal(t, 1.0, got[key{"a_to_b", "ingress"}])
	assert.Equal(t, 1.0, got[key{"b_to_a", "egress"}])
	assert.Equal(t, 0.0, got[key{"b_to_a", "ingress"}], "b_to_a ingress is the blocked side")

	// A later run that drops the pair must prune every directional series.
	m.SetClusterValidatorSummary(&ClusterValidatorSummary{
		RanAtUnixSec: 2, VerdictReady: true,
	})
	assert.Empty(t, gatherClusterValidatorSeries(t, reg, ClusterValidatorNetpolPairPassedMetricName),
		"netpol series from the prior run must be pruned when the pair is gone")
}
