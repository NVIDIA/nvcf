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

package otelconfig

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/internal/logger"
	"github.com/stretchr/testify/assert"
)

func TestMain(m *testing.M) {
	logger.Init()
	os.Exit(m.Run())
}

func TestGenerateConfig(t *testing.T) {
	tempDir := t.TempDir()
	outputFile := filepath.Join(tempDir, "config.yaml")

	// Set required environment variable for the test
	os.Setenv("NVCF_BACKEND_TYPE", "non-gfn")
	os.Setenv("NVCF_INSTANCE_ID", "fake-instance-id")
	os.Setenv("NVCF_NAMESPACE", "sr-fake-namespace")
	os.Setenv("NVCF_WORKLOAD_TYPE", "function")
	os.Setenv("NVCT_TASK_ID", "fake-task-id")
	defer func() {
		os.Unsetenv("NVCF_BACKEND_TYPE")
		os.Unsetenv("NVCF_INSTANCE_ID")
		os.Unsetenv("NVCF_NAMESPACE")
		os.Unsetenv("NVCF_WORKLOAD_TYPE")
		os.Unsetenv("NVCT_TASK_ID")
	}()

	secretsFile := filepath.Join("../../testdata", "telemetry_endpoint_kratos_thanos_stg.json")
	secretsJSON, err := os.ReadFile(secretsFile)
	assert.NoError(t, err)
	telemetries := base64.StdEncoding.EncodeToString(secretsJSON)

	err = GenerateConfig(outputFile, telemetries)
	assert.NoError(t, err)

	content, err := os.ReadFile(outputFile)
	assert.NoError(t, err)
	assert.Contains(t, string(content), "https://sandbox-receivers.thanos.example.com/api/v1/receive")
	assert.Contains(t, string(content), "kratos-thanos-sandbox")
}

func TestGetTemplateConfig(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		expectErr bool
		expect    func(t *testing.T, cfg TemplateConfig)
	}{
		{
			name: "valid TaskID",
			env: map[string]string{
				"NVCF_BACKEND_TYPE":  "gfn",
				"NVCF_INSTANCE_ID":   "test-instance",
				"NVCF_NAMESPACE":     "test-ns",
				"NVCF_WORKLOAD_TYPE": "function",
				"NVCT_TASK_ID":       "task-123",
				"NVCF_ZONE_NAME":     "zone-1",
			},
			expectErr: false,
			expect: func(t *testing.T, cfg TemplateConfig) {
				assert.Equal(t, defaultLogExporterBatchMaxSizeBytes, cfg.LogExporterBatchMaxSizeBytes)
			},
		},
		{
			name: "valid FunctionID",
			env: map[string]string{
				"NVCF_BACKEND_TYPE":        "gfn",
				"NVCF_INSTANCE_ID":         "test-instance",
				"NVCF_NAMESPACE":           "test-ns",
				"NVCF_WORKLOAD_TYPE":       "function",
				"NVCF_FUNCTION_ID":         "func-1",
				"NVCF_FUNCTION_VERSION_ID": "ver-1",
				"NVCT_TASK_ID":             "",
				"NVCF_ZONE_NAME":           "zone-1",
			},
			expectErr: false,
		},
		{
			name: "custom log exporter batch max size",
			env: map[string]string{
				"NVCF_BACKEND_TYPE":                      "gfn",
				"NVCF_INSTANCE_ID":                       "test-instance",
				"NVCF_NAMESPACE":                         "test-ns",
				"NVCF_WORKLOAD_TYPE":                     "function",
				"NVCT_TASK_ID":                           "task-123",
				"NVCF_ZONE_NAME":                         "zone-1",
				"BYOO_LOG_EXPORTER_BATCH_MAX_SIZE_BYTES": "2000000",
			},
			expectErr: false,
			expect: func(t *testing.T, cfg TemplateConfig) {
				assert.Equal(t, 2_000_000, cfg.LogExporterBatchMaxSizeBytes)
			},
		},
		{
			name: "custom metric subset config",
			env: map[string]string{
				"NVCF_BACKEND_TYPE":                 "gfn",
				"NVCF_INSTANCE_ID":                  "test-instance",
				"NVCF_NAMESPACE":                    "test-ns",
				"NVCF_WORKLOAD_TYPE":                "function",
				"NVCT_TASK_ID":                      "task-123",
				"NVCF_ZONE_NAME":                    "zone-1",
				"BYOO_METRIC_SUBSET_ENABLED":        "true",
				"BYOO_METRIC_SUBSET_FILTER_CONFIG":  "error_mode: ignore\nmetric_conditions:\n  - 'metric.name == \"drop\"'\n",
				"BYOO_WORKLOAD_METRICS_DROP_LABELS": "metric_subset_enabled, custom_label, metric_subset_enabled",
			},
			expectErr: false,
			expect: func(t *testing.T, cfg TemplateConfig) {
				assert.True(t, cfg.MetricSubset.Enabled)
				assert.Equal(t, map[string]interface{}{
					"error_mode": "ignore",
					"metric_conditions": []interface{}{
						`metric.name == "drop"`,
					},
				}, cfg.MetricSubset.FilterConfig)
				assert.Equal(t, []string{"metric_subset_enabled", "custom_label"}, cfg.WorkloadMetricsDropLabels)
			},
		},
		{
			name: "invalid metric subset filter config",
			env: map[string]string{
				"NVCF_BACKEND_TYPE":                "gfn",
				"NVCF_INSTANCE_ID":                 "test-instance",
				"NVCF_NAMESPACE":                   "test-ns",
				"NVCF_WORKLOAD_TYPE":               "function",
				"NVCT_TASK_ID":                     "task-123",
				"NVCF_ZONE_NAME":                   "zone-1",
				"BYOO_METRIC_SUBSET_FILTER_CONFIG": "processors: []",
			},
			expectErr: true,
		},
		{
			name: "negative log exporter batch max size",
			env: map[string]string{
				"NVCF_BACKEND_TYPE":                      "gfn",
				"NVCF_INSTANCE_ID":                       "test-instance",
				"NVCF_NAMESPACE":                         "test-ns",
				"NVCF_WORKLOAD_TYPE":                     "function",
				"NVCT_TASK_ID":                           "task-123",
				"NVCF_ZONE_NAME":                         "zone-1",
				"BYOO_LOG_EXPORTER_BATCH_MAX_SIZE_BYTES": "-1",
			},
			expectErr: true,
		},
		{
			name: "missing NVCF_FUNCTION_ID",
			env: map[string]string{
				"NVCF_BACKEND_TYPE":        "gfn",
				"NVCF_INSTANCE_ID":         "test-instance",
				"NVCF_NAMESPACE":           "test-ns",
				"NVCF_WORKLOAD_TYPE":       "function",
				"NVCF_FUNCTION_ID":         "",
				"NVCF_FUNCTION_VERSION_ID": "ver-1",
				"NVCT_TASK_ID":             "",
				"NVCF_ZONE_NAME":           "",
			},
			expectErr: true,
		},
		{
			name: "missing NVCF_FUNCTION_VERSION_ID",
			env: map[string]string{
				"NVCF_BACKEND_TYPE":        "gfn",
				"NVCF_INSTANCE_ID":         "test-instance",
				"NVCF_NAMESPACE":           "test-ns",
				"NVCF_WORKLOAD_TYPE":       "function",
				"NVCF_FUNCTION_ID":         "func-1",
				"NVCF_FUNCTION_VERSION_ID": "",
				"NVCT_TASK_ID":             "",
				"NVCF_ZONE_NAME":           "",
			},
			expectErr: true,
		},
		{
			name: "have both NVCF_FUNCTION and NVCT_TASK",
			env: map[string]string{
				"NVCF_BACKEND_TYPE":        "gfn",
				"NVCF_INSTANCE_ID":         "test-instance",
				"NVCF_NAMESPACE":           "test-ns",
				"NVCF_WORKLOAD_TYPE":       "function",
				"NVCF_FUNCTION_ID":         "func-1",
				"NVCF_FUNCTION_VERSION_ID": "ver-1",
				"NVCT_TASK_ID":             "task-123",
				"NVCF_ZONE_NAME":           "zone-1",
			},
			expectErr: true,
		},
		{
			name: "missing required",
			env: map[string]string{
				"NVCF_BACKEND_TYPE":        "gfn",
				"NVCF_INSTANCE_ID":         "test-instance",
				"NVCF_NAMESPACE":           "test-ns",
				"NVCF_WORKLOAD_TYPE":       "function",
				"NVCF_FUNCTION_ID":         "",
				"NVCF_FUNCTION_VERSION_ID": "",
				"NVCT_TASK_ID":             "",
				"NVCF_ZONE_NAME":           "zone-1",
			},
			expectErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			envKeys := []string{
				"NVCF_BACKEND_TYPE",
				"NVCF_INSTANCE_ID",
				"NVCF_NAMESPACE",
				"NVCF_WORKLOAD_TYPE",
				"NVCF_FUNCTION_ID",
				"NVCF_FUNCTION_VERSION_ID",
				"NVCT_TASK_ID",
				"NVCF_ZONE_NAME",
				"NVCF_CLUSTER_REGION",
				"BYOO_LOG_CHUNK_MAX_BODY_BYTES",
				"BYOO_LOG_CHUNK_DRY_RUN",
				"BYOO_LOG_EXPORTER_BATCH_MAX_SIZE_BYTES",
				"BYOO_METRIC_SUBSET_ENABLED",
				"BYOO_METRIC_SUBSET_FILTER_CONFIG",
				"BYOO_WORKLOAD_METRICS_DROP_LABELS",
			}
			backup := map[string]*string{}
			for _, k := range envKeys {
				if v, ok := os.LookupEnv(k); ok {
					value := v
					backup[k] = &value
				} else {
					backup[k] = nil
				}
				os.Unsetenv(k)
			}
			for k, v := range tc.env {
				os.Setenv(k, v)
			}
			defer func() {
				for k, v := range backup {
					if v == nil {
						os.Unsetenv(k)
					} else {
						os.Setenv(k, *v)
					}
				}
			}()

			cfg, err := getTemplateConfig()
			if tc.expectErr {
				if err == nil {
					t.Errorf("expected error but got nil")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if tc.expect != nil {
					tc.expect(t, cfg)
				}
			}
		})
	}
}
