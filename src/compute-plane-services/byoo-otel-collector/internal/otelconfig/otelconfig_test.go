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
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/internal/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			name: "BYOO log chunking enabled uses collector defaults",
			env: map[string]string{
				"NVCF_BACKEND_TYPE":         "gfn",
				"NVCF_INSTANCE_ID":          "test-instance",
				"NVCF_NAMESPACE":            "test-ns",
				"NVCF_WORKLOAD_TYPE":        "function",
				"NVCT_TASK_ID":              "task-123",
				"NVCF_ZONE_NAME":            "zone-1",
				"BYOO_LOG_CHUNKING_ENABLED": "true",
			},
			expectErr: false,
			expect: func(t *testing.T, cfg TemplateConfig) {
				assert.True(t, cfg.LogChunking.Enabled)
				assert.Equal(t, defaultLogChunkMaxPayloadBytes, cfg.LogChunking.MaxPayloadBytes)
				assert.False(t, cfg.LogChunking.DryRun)
			},
		},
		{
			name: "BYOO log chunk max payload bytes overrides deprecated max body bytes",
			env: map[string]string{
				"NVCF_BACKEND_TYPE":                "gfn",
				"NVCF_INSTANCE_ID":                 "test-instance",
				"NVCF_NAMESPACE":                   "test-ns",
				"NVCF_WORKLOAD_TYPE":               "function",
				"NVCT_TASK_ID":                     "task-123",
				"NVCF_ZONE_NAME":                   "zone-1",
				"BYOO_LOG_CHUNK_MAX_BODY_BYTES":    "131072",
				"BYOO_LOG_CHUNK_MAX_PAYLOAD_BYTES": "262144",
			},
			expectErr: false,
			expect: func(t *testing.T, cfg TemplateConfig) {
				assert.True(t, cfg.LogChunking.Enabled)
				assert.Equal(t, 262144, cfg.LogChunking.MaxPayloadBytes)
			},
		},
		{
			name: "BYOO deprecated log chunk max body bytes is accepted",
			env: map[string]string{
				"NVCF_BACKEND_TYPE":             "gfn",
				"NVCF_INSTANCE_ID":              "test-instance",
				"NVCF_NAMESPACE":                "test-ns",
				"NVCF_WORKLOAD_TYPE":            "function",
				"NVCT_TASK_ID":                  "task-123",
				"NVCF_ZONE_NAME":                "zone-1",
				"BYOO_LOG_CHUNK_MAX_BODY_BYTES": "131072",
			},
			expectErr: false,
			expect: func(t *testing.T, cfg TemplateConfig) {
				assert.True(t, cfg.LogChunking.Enabled)
				assert.Equal(t, 131072, cfg.LogChunking.MaxPayloadBytes)
			},
		},
		{
			name: "BYOO OTel collector config",
			env: map[string]string{
				"NVCF_BACKEND_TYPE":              "gfn",
				"NVCF_INSTANCE_ID":               "test-instance",
				"NVCF_NAMESPACE":                 "test-ns",
				"NVCF_WORKLOAD_TYPE":             "function",
				"NVCT_TASK_ID":                   "task-123",
				"NVCF_ZONE_NAME":                 "zone-1",
				"BYOO_OTEL_COLLECTOR_CONFIG_B64": base64.StdEncoding.EncodeToString([]byte(`{"exporterHelper":{"timeout":"30s"},"logSampling":{"samplingPercentage":10,"mode":"hash_seed","hashSeed":1234,"failClosed":false,"attributeSource":"record","fromAttribute":"log.id","samplingPriority":"sampling.priority"},"traceSampling":{"samplingPercentage":1,"mode":"hash_seed","hashSeed":1234,"failClosed":false}}`)),
			},
			expectErr: false,
			expect: func(t *testing.T, cfg TemplateConfig) {
				assert.Equal(t, "30s", cfg.OTelCollector.ExporterHelper.Timeout)
				require.NotNil(t, cfg.OTelCollector.LogSampling.SamplingPercentage)
				assert.Equal(t, 10.0, *cfg.OTelCollector.LogSampling.SamplingPercentage)
				assert.Equal(t, "hash_seed", cfg.OTelCollector.LogSampling.Mode)
				require.NotNil(t, cfg.OTelCollector.LogSampling.HashSeed)
				assert.Equal(t, uint32(1234), *cfg.OTelCollector.LogSampling.HashSeed)
				require.NotNil(t, cfg.OTelCollector.LogSampling.FailClosed)
				assert.False(t, *cfg.OTelCollector.LogSampling.FailClosed)
				assert.Equal(t, "record", cfg.OTelCollector.LogSampling.AttributeSource)
				assert.Equal(t, "log.id", cfg.OTelCollector.LogSampling.FromAttribute)
				assert.Equal(t, "sampling.priority", cfg.OTelCollector.LogSampling.SamplingPriority)
				require.NotNil(t, cfg.OTelCollector.TraceSampling.SamplingPercentage)
				assert.Equal(t, 1.0, *cfg.OTelCollector.TraceSampling.SamplingPercentage)
				assert.Equal(t, "hash_seed", cfg.OTelCollector.TraceSampling.Mode)
				require.NotNil(t, cfg.OTelCollector.TraceSampling.HashSeed)
				assert.Equal(t, uint32(1234), *cfg.OTelCollector.TraceSampling.HashSeed)
				require.NotNil(t, cfg.OTelCollector.TraceSampling.FailClosed)
				assert.False(t, *cfg.OTelCollector.TraceSampling.FailClosed)
			},
		},
		{
			name: "BYOO debug mode",
			env: map[string]string{
				"NVCF_BACKEND_TYPE":  "gfn",
				"NVCF_INSTANCE_ID":   "test-instance",
				"NVCF_NAMESPACE":     "test-ns",
				"NVCF_WORKLOAD_TYPE": "function",
				"NVCT_TASK_ID":       "task-123",
				"NVCF_ZONE_NAME":     "zone-1",
				"BYOO_DEBUG_MODE":    "true",
			},
			expectErr: false,
			expect: func(t *testing.T, cfg TemplateConfig) {
				assert.True(t, cfg.DebugMode)
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
				"BYOO_WORKLOAD_METRICS_DROP_LABELS": "custom_label",
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
				assert.Equal(t, []string{"metric_subset_enabled", "custom_label"}, cfg.WorkloadMetrics.DropLabels)
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
				"BYOO_LOG_CHUNKING_ENABLED",
				"BYOO_LOG_CHUNK_MAX_PAYLOAD_BYTES",
				"BYOO_LOG_CHUNK_MAX_BODY_BYTES",
				"BYOO_LOG_CHUNK_DRY_RUN",
				"BYOO_DEBUG_MODE",
				"BYOO_METRIC_SUBSET_ENABLED",
				"BYOO_METRIC_SUBSET_FILTER_CONFIG",
				"BYOO_WORKLOAD_METRICS_DROP_LABELS",
				"BYOO_OTEL_COLLECTOR_CONFIG_B64",
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

func TestOTelCollectorConfig_ValidateSampling(t *testing.T) {
	tests := []struct {
		name            string
		samplingPercent float64
		mode            string
		attributeSource string
		fromAttribute   string
		wantErr         string
	}{
		{name: "accepts zero", samplingPercent: 0, mode: "hash_seed"},
		{name: "rejects default hash seed below lower bound", samplingPercent: hashSeedMinSamplingPercentage / 2, wantErr: "at least"},
		{name: "accepts hash seed lower bound", samplingPercent: hashSeedMinSamplingPercentage, mode: "hash_seed"},
		{name: "rejects hash seed below lower bound", samplingPercent: hashSeedMinSamplingPercentage / 2, mode: "hash_seed", wantErr: "at least"},
		{name: "accepts proportional lower bound", samplingPercent: consistentMinSamplingPercentage, mode: "proportional"},
		{name: "rejects NaN", samplingPercent: math.NaN(), mode: "hash_seed", wantErr: "finite"},
		{name: "rejects positive infinity", samplingPercent: math.Inf(1), mode: "hash_seed", wantErr: "finite"},
		{name: "rejects negative infinity", samplingPercent: math.Inf(-1), mode: "hash_seed", wantErr: "finite"},
		{name: "rejects negative", samplingPercent: -1, mode: "hash_seed", wantErr: "between"},
		{name: "rejects exceeding float32", samplingPercent: float64(math.MaxFloat32) * 2, mode: "hash_seed", wantErr: "between"},
		{name: "rejects record attributes outside hash seed", samplingPercent: 1, mode: "proportional", attributeSource: "record", fromAttribute: "log.id", wantErr: "require hash_seed"},
		{name: "rejects unsupported mode", samplingPercent: 1, mode: "invalid", wantErr: "unsupported"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			samplingPercentage := tt.samplingPercent
			config := OTelCollectorConfig{
				LogSampling: LogSamplingConfig{
					SamplingPercentage: &samplingPercentage,
					Mode:               tt.mode,
					AttributeSource:    tt.attributeSource,
					FromAttribute:      tt.fromAttribute,
				},
				TraceSampling: SamplingConfig{SamplingPercentage: &samplingPercentage, Mode: tt.mode},
			}

			err := config.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestDecodeOTelCollectorConfigRejectsInvalidSampling(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte(`{
        "exporterHelper": {"timeout": "30s"},
        "logSampling": {"samplingPercentage": -1, "mode": "hash_seed"},
        "traceSampling": {"samplingPercentage": 0.000000000000001, "mode": "hash_seed"}
    }`))

	_, err := decodeOTelCollectorConfig(encoded)
	require.ErrorContains(t, err, "validate config: log sampling")
}

func TestApplyOTelCollectorConfigRejectsInvalidSampling(t *testing.T) {
	invalidSamplingPercentage := -1.0
	config := OTelCollectorConfig{
		LogSampling:   LogSamplingConfig{SamplingPercentage: &invalidSamplingPercentage},
		TraceSampling: SamplingConfig{SamplingPercentage: &invalidSamplingPercentage},
	}
	otelConfig := &OpenTelemetryConfig{}
	initializeConfigMaps(otelConfig)
	otelConfig.Service.Pipelines["logs"] = struct {
		Receivers  []string `yaml:"receivers"`
		Exporters  []string `yaml:"exporters"`
		Processors []string `yaml:"processors"`
	}{Processors: []string{"batch/logs"}}
	otelConfig.Service.Pipelines["traces"] = struct {
		Receivers  []string `yaml:"receivers"`
		Exporters  []string `yaml:"exporters"`
		Processors []string `yaml:"processors"`
	}{Processors: []string{"batch"}}

	require.ErrorContains(t, applyOTelCollectorConfig(otelConfig, config), "log sampling")

	assert.NotContains(t, otelConfig.Processors, "probabilistic_sampler/logs")
	assert.NotContains(t, otelConfig.Processors, "probabilistic_sampler/traces")
	assert.Equal(t, []string{"batch/logs"}, otelConfig.Service.Pipelines["logs"].Processors)
	assert.Equal(t, []string{"batch"}, otelConfig.Service.Pipelines["traces"].Processors)
}
