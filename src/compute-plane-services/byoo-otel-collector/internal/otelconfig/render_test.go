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
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v3"
)

func TestRenderOtelConfig(t *testing.T) {
	tests := []struct {
		name         string
		inputData    []byte
		workloadType WorkloadType
		backendType  BackendType
		expectError  bool
	}{
		{
			name:         "Valid Input VM Container",
			inputData:    []byte(`{"telemetries": {"logsTelemetry": {"protocol": "HTTP", "provider": "SPLUNK", "endpoint": "http://example.com", "name": "example-logs"}}}`),
			workloadType: Container,
			backendType:  VM,
			expectError:  false,
		},
		{
			name:         "Valid Input VM Helm",
			inputData:    []byte(`{"telemetries": {"logsTelemetry": {"protocol": "HTTP", "provider": "SPLUNK", "endpoint": "http://example.com", "name": "example-logs"}}}`),
			workloadType: Helm,
			backendType:  VM,
			expectError:  false,
		},
		{
			name:         "Valid Input K8s",
			inputData:    []byte(`{"telemetries": {"logsTelemetry": {"protocol": "HTTP", "provider": "SPLUNK", "endpoint": "http://example.com", "name": "example-logs"}}}`),
			workloadType: Container,
			backendType:  K8s,
			expectError:  false,
		},
		{
			name:         "Unknown Provider",
			inputData:    []byte(`{"telemetries": {"logsTelemetry": {"protocol": "HTTP", "provider": "UNKNOWN", "endpoint": "http://example.com", "name": "example-logs"}}}`),
			workloadType: Container,
			backendType:  VM,
			expectError:  true,
		},
		{
			name:         "Lowercase Protocol",
			inputData:    []byte(`{"telemetries": {"logsTelemetry": {"protocol": "http", "provider": "SPLUNK", "endpoint": "http://example.com", "name": "example-logs"}}}`),
			workloadType: Container,
			backendType:  VM,
			expectError:  false,
		},
		{
			name:         "Valid Input ServiceNow Traces",
			inputData:    []byte(`{"telemetries": {"tracesTelemetry": {"protocol": "http", "provider": "SERVICENOW", "endpoint": "https://otel-staging.example.invalid:8282", "name": "example-internal-traces"}}}`),
			workloadType: Container,
			backendType:  VM,
			expectError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCfg, err := RenderOtelConfigFromBytes(tt.inputData, TemplateConfig{
				BackendType:       tt.backendType,
				WorkloadType:      tt.workloadType,
				Namespace:         "foo",
				FunctionID:        "fake-function-id",
				FunctionVersionID: "fake-function-version-id",
			})
			if (err != nil) != tt.expectError {
				t.Errorf("RenderOtelConfig() error = %v, expectError %v", err, tt.expectError)
			}
			if !tt.expectError && len(gotCfg) == 0 {
				t.Errorf("Expected config, got none")
			}
		})
	}
}

func TestRenderOtelConfigWithMetricSubsetPipeline(t *testing.T) {
	gotCfg, err := RenderOtelConfigFromBytes(
		[]byte(`{"telemetries": {"metricsTelemetry": {"protocol": "HTTP", "provider": "PROMETHEUS", "endpoint": "https://metrics.example.invalid/api/v1/write", "name": "example-metrics"}}}`),
		TemplateConfig{
			BackendType:       K8s,
			WorkloadType:      Container,
			Namespace:         "sr-fake-namespace",
			FunctionID:        "fake-function-id",
			FunctionVersionID: "fake-function-version-id",
			InstanceID:        "fake-instance-id",
			ZoneName:          "fake-zone-name",
			MetricSubset: MetricSubsetConfig{
				Enabled:      true,
				FilterConfig: defaultMetricSubsetFilterConfig(),
			},
			WorkloadMetrics: WorkloadMetricsConfig{
				DropLabels: defaultWorkloadMetricsDropLabels,
			},
		},
	)

	assert.NoError(t, err)

	otelConfig := &OpenTelemetryConfig{}
	err = yaml.Unmarshal(gotCfg, otelConfig)
	assert.NoError(t, err)
	assert.Contains(t, otelConfig.Exporters, metricSubsetExporterID)
	assert.Contains(t, otelConfig.Processors, metricSubsetFilterProcessorID)
	assert.Contains(t, otelConfig.Processors, metricSubsetBatchProcessorID)
	assert.Equal(t, []string{"otlp"}, otelConfig.Service.Pipelines["metrics/metric_subset"].Receivers)
	assert.Equal(t, []string{metricSubsetExporterID}, otelConfig.Service.Pipelines["metrics/metric_subset"].Exporters)
	assert.Equal(t, []string{
		"memory_limiter",
		metricSubsetFilterProcessorID,
		"resource",
		"metrics_transform",
		metricSubsetBatchProcessorID,
	}, otelConfig.Service.Pipelines["metrics/metric_subset"].Processors)
}

func TestRenderOtelConfigWithDebugMode(t *testing.T) {
	gotCfg, err := RenderOtelConfigFromBytes(
		[]byte(`{"telemetries": {"logsTelemetry": {"protocol": "HTTP", "provider": "SPLUNK", "endpoint": "https://logs.example.invalid", "name": "example-logs"}, "metricsTelemetry": {"protocol": "HTTP", "provider": "PROMETHEUS", "endpoint": "https://metrics.example.invalid/api/v1/write", "name": "example-metrics"}}}`),
		TemplateConfig{
			BackendType:       K8s,
			WorkloadType:      Container,
			Namespace:         "sr-fake-namespace",
			FunctionID:        "fake-function-id",
			FunctionVersionID: "fake-function-version-id",
			DebugMode:         true,
		},
	)

	assert.NoError(t, err)

	otelConfig := &OpenTelemetryConfig{}
	err = yaml.Unmarshal(gotCfg, otelConfig)
	assert.NoError(t, err)
	assert.Contains(t, otelConfig.Exporters, "debug")
	assert.Equal(t, "debug", otelConfig.Service.Telemetry["logs"]["level"])
	assert.Equal(t, true, otelConfig.Service.Telemetry["logs"]["development"])
	assert.Contains(t, otelConfig.Service.Pipelines["logs"].Exporters, "debug")
	assert.Contains(t, otelConfig.Service.Pipelines["metrics"].Exporters, "debug")
}

func TestRenderOtelConfigWithMetricSubsetPipelineMatchesExample(t *testing.T) {
	t.Setenv("ESS_SECRETS_PATH", "")

	gotCfg, err := RenderOtelConfigFromBytes(
		[]byte(`{"telemetries": {"metricsTelemetry": {"protocol": "HTTP", "provider": "PROMETHEUS", "endpoint": "https://workload-metrics.example.invalid/api/v1/write", "name": "workload-metrics"}}}`),
		TemplateConfig{
			BackendType:       K8s,
			WorkloadType:      Container,
			Namespace:         "sr-fake-namespace",
			FunctionID:        "fake-function-id",
			FunctionVersionID: "fake-function-version-id",
			InstanceID:        "fake-instance-id",
			ZoneName:          "fake-zone-name",
			MetricSubset: MetricSubsetConfig{
				Enabled:      true,
				FilterConfig: defaultMetricSubsetFilterConfig(),
			},
			WorkloadMetrics: WorkloadMetricsConfig{
				DropLabels: defaultWorkloadMetricsDropLabels,
			},
		},
	)
	if err != nil {
		t.Fatalf("failed to render metric subset config: %v", err)
	}

	const examplePath = "../../examples/otelconfigs/k8s/config_function_container_metric_subset.yaml"
	if os.Getenv("UPDATE_METRIC_SUBSET_EXAMPLE") == "true" {
		if err := os.WriteFile(examplePath, gotCfg, 0o644); err != nil {
			t.Fatalf("failed to update metric subset example config: %v", err)
		}
	}

	expectedCfg, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatalf("failed to read metric subset example config: %v", err)
	}

	assertYAMLConfigEqual(t, expectedCfg, gotCfg)
}

func assertYAMLConfigEqual(t *testing.T, expectedYAML, actualYAML []byte) {
	t.Helper()

	var actualMap, expectedMap map[string]interface{}
	if err := yaml.Unmarshal(actualYAML, &actualMap); err != nil {
		t.Fatalf("failed to unmarshal actual YAML to map: %v", err)
	}
	if err := yaml.Unmarshal(expectedYAML, &expectedMap); err != nil {
		t.Fatalf("failed to unmarshal expected YAML to map: %v", err)
	}

	if !assert.Equal(t, expectedMap, actualMap) {
		t.Errorf("transformed OtelConfig mismatch:\nExpected OtelConfig YAML:\n%s\n\nActual OtelConfigYAML:\n%s", string(expectedYAML), string(actualYAML))
	}
}

// returns the expected OpenTelemetry YAML configuration as a byte slice for function workloads
func createExpectedOtelConfigYAMLForInternalTelemetryFunction(tracesTelemetryName string) []byte {
	internalTelemetryYAMLString := fmt.Sprintf(`receivers: {}
exporters:
  otlp/SERVICENOW-%s-traces:
    endpoint: endpoint:8283
    headers:
      lightstep-access-token: ${file:/etc/byoo-otel-collector/secrets/%s}
processors: {}
extensions: {}
service:
  telemetry:
    logs:
      level: warn
      initial_fields:
        public: "true"
    metrics:
      level: detailed
      readers:
        - pull:
            exporter:
              prometheus:
                host: "${env:OTEL_POD_IP:-0.0.0.0}"
                port: 18888
    resource:
      attributes:
        - name: service.namespace
          value: test-namespace
        - name: service.name
          value: byoo-otel-collector
        - name: function.id
          value: test-function-id
        - name: function.version.id
          value: test-function-version-id
    traces:
      processors:
        - batch:
            exporter:
              otlp:
                protocol: grpc
                endpoint: ${env:OTEL_EXPORTER_OTLP_ENDPOINT:-http://localhost:4317}
                headers:
                  - name: lightstep-access-token
                    value: ${env:OTEL_TRACING_ACCESS_TOKEN}
  pipelines:
    traces:
      receivers:
        - otlp
      exporters:
        - otlp/SERVICENOW-%s-traces
      processors:
        - memory_limiter
        - attributes/add-metadata
        - batch
  extensions:
    - healthcheckv2
    - cgroup_runtime
`, tracesTelemetryName, tracesTelemetryName, tracesTelemetryName)
	return []byte(internalTelemetryYAMLString)
}

// returns the expected OpenTelemetry YAML configuration as a byte slice for task workloads
func createExpectedOtelConfigYAMLForInternalTelemetryTask(tracesTelemetryName string) []byte {
	internalTelemetryYAMLString := fmt.Sprintf(`receivers: {}
exporters:
  otlp/SERVICENOW-%s-traces:
    endpoint: endpoint:8283
    headers:
      lightstep-access-token: ${file:/etc/byoo-otel-collector/secrets/%s}
processors: {}
extensions: {}
service:
  telemetry:
    logs:
      level: warn
      initial_fields:
        public: "true"
    metrics:
      level: detailed
      readers:
        - pull:
            exporter:
              prometheus:
                host: "${env:OTEL_POD_IP:-0.0.0.0}"
                port: 18888
    resource:
      attributes:
        - name: service.namespace
          value: test-namespace
        - name: service.name
          value: byoo-otel-collector
        - name: task.id
          value: test-task-id
    traces:
      processors:
        - batch:
            exporter:
              otlp:
                protocol: grpc
                endpoint: ${env:OTEL_EXPORTER_OTLP_ENDPOINT:-http://localhost:4317}
                headers:
                  - name: lightstep-access-token
                    value: ${env:OTEL_TRACING_ACCESS_TOKEN}
  pipelines:
    traces:
      receivers:
        - otlp
      exporters:
        - otlp/SERVICENOW-%s-traces
      processors:
        - memory_limiter
        - attributes/add-metadata
        - batch
  extensions:
    - healthcheckv2
    - cgroup_runtime
`, tracesTelemetryName, tracesTelemetryName, tracesTelemetryName)
	return []byte(internalTelemetryYAMLString)
}

func Test_generateExportersAndService(t *testing.T) {
	type args struct {
		config                 TelemetryConfig
		otelConfig             *OpenTelemetryConfig
		expectedOtelConfigYAML []byte
		tmplConfig             TemplateConfig
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "Export internal traces for function workload",
			args: args{
				config: TelemetryConfig{
					Telemetries: Telemetries{
						Traces: &Telemetry{
							Name:     "example-trace",
							Protocol: "http",
							Provider: "SERVICENOW",
							Endpoint: "endpoint:8283",
						},
					},
				},
				otelConfig: func() *OpenTelemetryConfig {
					config := &OpenTelemetryConfig{}
					initializeConfigMaps(config)
					return config
				}(),
				tmplConfig: TemplateConfig{
					FunctionID:        "test-function-id",
					FunctionVersionID: "test-function-version-id",
					Namespace:         "test-namespace",
				},
				expectedOtelConfigYAML: createExpectedOtelConfigYAMLForInternalTelemetryFunction("example-trace"),
			},
			wantErr: false,
		},
		{
			name: "Export internal traces for task workload",
			args: args{
				config: TelemetryConfig{
					Telemetries: Telemetries{
						Traces: &Telemetry{
							Name:     "example-trace",
							Protocol: "http",
							Provider: "SERVICENOW",
							Endpoint: "endpoint:8283",
						},
					},
				},
				otelConfig: func() *OpenTelemetryConfig {
					config := &OpenTelemetryConfig{}
					initializeConfigMaps(config)
					return config
				}(),
				tmplConfig: TemplateConfig{
					TaskID:    "test-task-id",
					Namespace: "test-namespace",
				},
				expectedOtelConfigYAML: createExpectedOtelConfigYAMLForInternalTelemetryTask("example-trace"),
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := generateExportersAndService(tt.args.config, tt.args.otelConfig, tt.args.tmplConfig); (err != nil) != tt.wantErr {
				t.Errorf("generateExportersAndService() error = %v, wantErr %v", err, tt.wantErr)
			}

			// Marshal the actual otelConfig to YAML
			actualYAML, errActual := yaml.Marshal(tt.args.otelConfig)
			if errActual != nil {
				t.Fatalf("Failed to marshal actual otelConfig to YAML: %v", errActual)
			}

			var actualMap, expectedMap map[string]interface{}
			if err := yaml.Unmarshal(actualYAML, &actualMap); err != nil {
				t.Fatalf("Failed to unmarshal actualYAML to map: %v", err)
			}
			if err := yaml.Unmarshal(tt.args.expectedOtelConfigYAML, &expectedMap); err != nil {
				t.Fatalf("Failed to unmarshal expectedYAML to map: %v", err)
			}

			if !assert.Equal(t, expectedMap, actualMap) {
				// If they are not equal, the assert.Equal would have printed a diff of the maps.
				// For additional context, you can still print the YAML diff if desired.
				t.Errorf("Transformed OtelConfig mismatch:\nExpected OtelConfig YAML:\n%s\n\nActual OtelConfigYAML:\n%s", string(tt.args.expectedOtelConfigYAML), string(actualYAML))
			}
		})
	}
}

func TestGenerateExportersAndServiceAddsLogChunkProcessor(t *testing.T) {
	cfg := TelemetryConfig{
		Telemetries: Telemetries{
			Logs: &Telemetry{
				Name:     "example-logs",
				Protocol: ProtocolHTTP,
				Provider: ProviderSplunk,
				Endpoint: "https://splunk.example.invalid",
			},
		},
	}
	otelConfig := &OpenTelemetryConfig{}
	initializeConfigMaps(otelConfig)

	err := generateExportersAndService(cfg, otelConfig, TemplateConfig{
		Namespace: "test-namespace",
		LogChunking: LogChunkingConfig{
			MaxBodyBytes: 983040,
			DryRun:       true,
		},
	})

	assert.NoError(t, err)
	assert.Equal(t, []string{
		"memory_limiter",
		"attributes/add-metadata",
		"logchunk/byoo",
		"batch",
	}, otelConfig.Service.Pipelines["logs"].Processors)
	assert.Equal(t, map[string]interface{}{
		"max_body_bytes": 983040,
		"dry_run":        true,
	}, otelConfig.Processors["logchunk/byoo"])

	exporter := otelConfig.Exporters["splunk_hec/SPLUNK-example-logs-logs"]
	assert.Equal(t, map[string]interface{}{
		"enabled":       true,
		"num_consumers": 10,
		"queue_size":    1000,
	}, exporter["sending_queue"])
}

func TestGenerateExportersAndServiceAddsLogChunkDefaultsWhenEnabled(t *testing.T) {
	cfg := TelemetryConfig{
		Telemetries: Telemetries{
			Logs: &Telemetry{
				Name:     "example-logs",
				Protocol: ProtocolHTTP,
				Provider: ProviderSplunk,
				Endpoint: "https://splunk.example.invalid",
			},
		},
	}
	otelConfig := &OpenTelemetryConfig{}
	initializeConfigMaps(otelConfig)

	err := generateExportersAndService(cfg, otelConfig, TemplateConfig{
		Namespace: "test-namespace",
		LogChunking: LogChunkingConfig{
			Enabled: true,
		},
	})

	assert.NoError(t, err)
	assert.Equal(t, map[string]interface{}{
		"max_body_bytes": defaultLogChunkMaxBodyBytes,
		"dry_run":        false,
	}, otelConfig.Processors["logchunk/byoo"])
	exporter := otelConfig.Exporters["splunk_hec/SPLUNK-example-logs-logs"]
	assert.NotContains(t, exporter["sending_queue"].(map[string]interface{}), "batch")
}

func TestGenerateExportersAndServiceUsesExporterHelperQueueBatchConfig(t *testing.T) {
	cfg := TelemetryConfig{
		Telemetries: Telemetries{
			Logs: &Telemetry{
				Name:     "example-logs",
				Protocol: ProtocolHTTP,
				Provider: ProviderSplunk,
				Endpoint: "https://splunk.example.invalid",
			},
		},
	}
	otelConfig := &OpenTelemetryConfig{}
	initializeConfigMaps(otelConfig)

	minSize := int64(2_000_000)
	maxSize := int64(2_000_000)
	err := generateExportersAndService(cfg, otelConfig, TemplateConfig{
		Namespace: "test-namespace",
		OTelCollector: OTelCollectorConfig{
			ExporterHelper: ExporterHelperConfig{
				SendingQueue: SendingQueueConfig{
					Batch: SendingQueueBatchConfig{
						FlushTimeout: "200ms",
						Sizer:        "bytes",
						MinSize:      &minSize,
						MaxSize:      &maxSize,
					},
				},
			},
		},
	})

	assert.NoError(t, err)
	exporter := otelConfig.Exporters["splunk_hec/SPLUNK-example-logs-logs"]
	assert.Equal(t, map[string]interface{}{
		"enabled":       true,
		"num_consumers": 10,
		"queue_size":    1000,
		"batch": map[string]interface{}{
			"flush_timeout": "200ms",
			"sizer":         "bytes",
			"min_size":      int64(2_000_000),
			"max_size":      int64(2_000_000),
		},
	}, exporter["sending_queue"])
}

func TestGenerateExportersAndServiceAppliesCollectorOverrides(t *testing.T) {
	cfg := TelemetryConfig{
		Telemetries: Telemetries{
			Logs: &Telemetry{
				Name:     "example-logs",
				Protocol: ProtocolHTTP,
				Provider: ProviderSplunk,
				Endpoint: "https://splunk.example.invalid",
			},
			Metrics: &Telemetry{
				Name:     "example-metrics",
				Protocol: ProtocolHTTP,
				Provider: ProviderDatadog,
				Endpoint: "datadoghq.com",
			},
			Traces: &Telemetry{
				Name:     "example-traces",
				Protocol: ProtocolHTTP,
				Provider: ProviderServiceNow,
				Endpoint: "otel.example.invalid:4317",
			},
		},
	}
	otelConfig := &OpenTelemetryConfig{}
	initializeConfigMaps(otelConfig)

	retryEnabled := true
	queueConsumers := int64(3)
	queueSize := int64(2048)
	queueBatchMinSize := int64(123)
	queueBatchMaxSize := int64(456)
	memoryLimitMiB := int64(512)
	memorySpikeLimitMiB := int64(128)
	batchSendSize := int64(100)
	batchSendMaxSize := int64(200)
	logBatchSendSize := int64(340)
	logBatchSendMaxSize := int64(340)

	err := generateExportersAndService(cfg, otelConfig, TemplateConfig{
		Namespace: "test-namespace",
		OTelCollector: OTelCollectorConfig{
			ExporterHelper: ExporterHelperConfig{
				Timeout: "30s",
				RetryOnFailure: RetryOnFailureConfig{
					Enabled:         &retryEnabled,
					InitialInterval: "1s",
					MaxInterval:     "10s",
					MaxElapsedTime:  "2m",
				},
				SendingQueue: SendingQueueConfig{
					NumConsumers: &queueConsumers,
					QueueSize:    &queueSize,
					Batch: SendingQueueBatchConfig{
						FlushTimeout: "500ms",
						Sizer:        "bytes",
						MinSize:      &queueBatchMinSize,
						MaxSize:      &queueBatchMaxSize,
					},
				},
			},
			MemoryLimiter: MemoryLimiterConfig{
				CheckInterval: "2s",
				LimitMiB:      &memoryLimitMiB,
				SpikeLimitMiB: &memorySpikeLimitMiB,
			},
			Batch: BatchConfig{
				Timeout:          "1s",
				SendBatchSize:    &batchSendSize,
				SendBatchMaxSize: &batchSendMaxSize,
			},
			LogBatch: BatchConfig{
				Timeout:          "400ms",
				SendBatchSize:    &logBatchSendSize,
				SendBatchMaxSize: &logBatchSendMaxSize,
			},
		},
	})

	assert.NoError(t, err)

	for _, exporterID := range []string{
		"splunk_hec/SPLUNK-example-logs-logs",
		"datadog/DATADOG-example-metrics-metrics",
		"otlp/SERVICENOW-example-traces-traces",
	} {
		exporter := otelConfig.Exporters[exporterID]
		assert.Equal(t, "30s", exporter["timeout"], exporterID)
		assert.Equal(t, map[string]interface{}{
			"enabled":          true,
			"initial_interval": "1s",
			"max_interval":     "10s",
			"max_elapsed_time": "2m",
		}, exporter["retry_on_failure"], exporterID)
		assert.Equal(t, map[string]interface{}{
			"enabled":       true,
			"num_consumers": int64(3),
			"queue_size":    int64(2048),
			"batch": map[string]interface{}{
				"flush_timeout": "500ms",
				"sizer":         "bytes",
				"min_size":      int64(123),
				"max_size":      int64(456),
			},
		}, exporter["sending_queue"], exporterID)
	}

	assert.Equal(t, map[string]interface{}{
		"check_interval":  "2s",
		"limit_mib":       int64(512),
		"spike_limit_mib": int64(128),
	}, otelConfig.Processors["memory_limiter"])
	assert.Equal(t, map[string]interface{}{
		"send_batch_size":     int64(100),
		"timeout":             "1s",
		"send_batch_max_size": int64(200),
	}, otelConfig.Processors["batch"])
	assert.Equal(t, map[string]interface{}{
		"send_batch_size":     int64(340),
		"timeout":             "400ms",
		"send_batch_max_size": int64(340),
	}, otelConfig.Processors["batch/logs"])
	assert.Equal(t, []string{"memory_limiter", "attributes/add-metadata", "batch/logs"}, otelConfig.Service.Pipelines["logs"].Processors)
	assert.Equal(t, []string{"memory_limiter", "filter/metrics", "resource", "metrics_transform", "batch"}, otelConfig.Service.Pipelines["metrics"].Processors)
	assert.Equal(t, []string{"memory_limiter", "attributes/add-metadata", "batch"}, otelConfig.Service.Pipelines["traces"].Processors)
}

func TestGenerateExportersAndServiceAddsMetricSubsetPipeline(t *testing.T) {
	cfg := TelemetryConfig{
		Telemetries: Telemetries{
			Metrics: &Telemetry{
				Name:     "example-metrics",
				Protocol: ProtocolHTTP,
				Provider: ProviderPrometheus,
				Endpoint: "https://metrics.example.invalid/api/v1/write",
			},
		},
	}
	otelConfig := &OpenTelemetryConfig{}
	initializeConfigMaps(otelConfig)
	otelConfig.Processors["batch"] = map[string]interface{}{
		"send_batch_size":     4096,
		"timeout":             "400ms",
		"send_batch_max_size": 8192,
	}
	filterConfig := map[string]interface{}{
		"error_mode": "ignore",
		"metric_conditions": []string{
			`metric.name != "RtdInstrument"`,
		},
	}

	err := generateExportersAndService(cfg, otelConfig, TemplateConfig{
		Namespace: "test-namespace",
		MetricSubset: MetricSubsetConfig{
			Enabled:      true,
			FilterConfig: filterConfig,
		},
		WorkloadMetrics: WorkloadMetricsConfig{
			DropLabels: []string{"metric_subset_enabled"},
		},
	})

	assert.NoError(t, err)
	assert.Equal(t, map[string]interface{}{
		"endpoint": "${env:OTEL_POD_IP:-0.0.0.0}:19091",
		"resource_to_telemetry_conversion": map[string]interface{}{
			"enabled": true,
		},
		"send_timestamps":     true,
		"metric_expiration":   "5m",
		"enable_open_metrics": true,
	}, otelConfig.Exporters[metricSubsetExporterID])
	assert.Equal(t, filterConfig, otelConfig.Processors[metricSubsetFilterProcessorID])
	assert.Equal(t, otelConfig.Processors["batch"], otelConfig.Processors[metricSubsetBatchProcessorID])

	workloadMetricsPipeline := otelConfig.Service.Pipelines["metrics"]
	assert.Equal(t, []string{"otlp", "prometheus"}, workloadMetricsPipeline.Receivers)
	assert.Equal(t, []string{"prometheusremotewrite/PROMETHEUS-example-metrics-metrics"}, workloadMetricsPipeline.Exporters)
	assert.Equal(t, []string{
		"memory_limiter",
		"filter/metrics",
		"resource",
		workloadMetricsDropLabelsProcessorID,
		"metrics_transform",
		"batch",
	}, workloadMetricsPipeline.Processors)
	assert.NotContains(t, workloadMetricsPipeline.Processors, metricSubsetFilterProcessorID)
	assert.Equal(t, map[string]interface{}{
		"attributes": []map[string]interface{}{
			{
				"key":    "metric_subset_enabled",
				"action": "delete",
			},
		},
	}, otelConfig.Processors[workloadMetricsDropLabelsProcessorID])

	metricSubsetPipeline := otelConfig.Service.Pipelines["metrics/metric_subset"]
	assert.Equal(t, []string{"otlp"}, metricSubsetPipeline.Receivers)
	assert.Equal(t, []string{metricSubsetExporterID}, metricSubsetPipeline.Exporters)
	assert.Equal(t, []string{
		"memory_limiter",
		metricSubsetFilterProcessorID,
		"resource",
		"metrics_transform",
		metricSubsetBatchProcessorID,
	}, metricSubsetPipeline.Processors)
}

func TestGenerateExportersAndServiceAddsWorkloadMetricsDropLabelsWithoutMetricSubset(t *testing.T) {
	cfg := TelemetryConfig{
		Telemetries: Telemetries{
			Metrics: &Telemetry{
				Name:     "example-metrics",
				Protocol: ProtocolHTTP,
				Provider: ProviderPrometheus,
				Endpoint: "https://metrics.example.invalid/api/v1/write",
			},
		},
	}
	otelConfig := &OpenTelemetryConfig{}
	initializeConfigMaps(otelConfig)

	err := generateExportersAndService(cfg, otelConfig, TemplateConfig{
		Namespace: "test-namespace",
		WorkloadMetrics: WorkloadMetricsConfig{
			DropLabels: []string{"workload_label"},
		},
	})

	assert.NoError(t, err)
	assert.NotContains(t, otelConfig.Exporters, metricSubsetExporterID)
	assert.NotContains(t, otelConfig.Service.Pipelines, "metrics/metric_subset")
	assert.Equal(t, map[string]interface{}{
		"attributes": []map[string]interface{}{
			{
				"key":    "workload_label",
				"action": "delete",
			},
		},
	}, otelConfig.Processors[workloadMetricsDropLabelsProcessorID])
	assert.Contains(t, otelConfig.Service.Pipelines["metrics"].Processors, workloadMetricsDropLabelsProcessorID)
}

func TestGenerateExportersAndServiceDoesNotAddMetricSubsetPipelineWithoutMetricsTelemetry(t *testing.T) {
	cfg := TelemetryConfig{
		Telemetries: Telemetries{
			Logs: &Telemetry{
				Name:     "example-logs",
				Protocol: ProtocolHTTP,
				Provider: ProviderSplunk,
				Endpoint: "https://splunk.example.invalid",
			},
		},
	}
	otelConfig := &OpenTelemetryConfig{}
	initializeConfigMaps(otelConfig)

	err := generateExportersAndService(cfg, otelConfig, TemplateConfig{
		Namespace: "test-namespace",
		MetricSubset: MetricSubsetConfig{
			Enabled: true,
		},
	})

	assert.NoError(t, err)
	assert.NotContains(t, otelConfig.Exporters, metricSubsetExporterID)
	assert.NotContains(t, otelConfig.Processors, metricSubsetFilterProcessorID)
	assert.NotContains(t, otelConfig.Service.Pipelines, "metrics/metric_subset")
}

// Test_exporterMetrics_Datadog_KeepsFirstCumulativeSample is a regression test
// for the missing nvct_worker_service_result_total metric in Datadog (task
// scenario). Without metrics.sums.initial_cumulative_monotonic_value=keep, the
// Datadog exporter drops the first observed sample of a cumulative monotonic
// counter, which silently loses single-sample counters emitted by short-lived
// task pods.
func Test_exporterMetrics_Datadog_KeepsFirstCumulativeSample(t *testing.T) {
	cfg := TelemetryConfig{
		Telemetries: Telemetries{
			Metrics: &Telemetry{
				Name:     "example-metrics",
				Protocol: ProtocolGRPC,
				Provider: ProviderDatadog,
				Endpoint: "datadoghq.com",
			},
		},
	}
	otelConfig := &OpenTelemetryConfig{}
	initializeConfigMaps(otelConfig)

	exporterId, err := exporterMetrics(cfg, otelConfig)
	if err != nil {
		t.Fatalf("exporterMetrics() unexpected error = %v", err)
	}

	expectedExporterId := fmt.Sprintf("datadog/%s-example-metrics-metrics", ProviderDatadog)
	assert.Equal(t, expectedExporterId, exporterId, "unexpected exporter id")

	exporter, ok := otelConfig.Exporters[exporterId]
	if !ok {
		t.Fatalf("exporter %q not registered in otelConfig.Exporters", exporterId)
	}

	metricsBlock, ok := exporter["metrics"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected exporter[\"metrics\"] to be a map, got %T", exporter["metrics"])
	}
	sumsBlock, ok := metricsBlock["sums"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected metrics[\"sums\"] to be a map, got %T", metricsBlock["sums"])
	}

	assert.Equal(t, "to_delta", sumsBlock["cumulative_monotonic_mode"],
		"cumulative_monotonic_mode must be set explicitly")
	assert.Equal(t, "keep", sumsBlock["initial_cumulative_monotonic_value"],
		"initial_cumulative_monotonic_value must be 'keep' so the first sample of "+
			"short-lived counters (e.g. nvct_worker_service_result_total) is not dropped")
	assert.Equal(t, "15s", exporter["timeout"],
		"exporter timeout must be set to bound the final batch flush before short-lived task pods terminate")
}

// Datadog metrics exporter must work for both GRPC and HTTP transport
// configurations — the cumulative-monotonic fix is protocol-agnostic.
func Test_exporterMetrics_Datadog_ProtocolAgnostic(t *testing.T) {
	for _, proto := range []Protocol{ProtocolGRPC, ProtocolHTTP} {
		t.Run(string(proto), func(t *testing.T) {
			cfg := TelemetryConfig{
				Telemetries: Telemetries{
					Metrics: &Telemetry{
						Name:     "example-metrics",
						Protocol: proto,
						Provider: ProviderDatadog,
						Endpoint: "datadoghq.com",
					},
				},
			}
			otelConfig := &OpenTelemetryConfig{}
			initializeConfigMaps(otelConfig)

			exporterId, err := exporterMetrics(cfg, otelConfig)
			if err != nil {
				t.Fatalf("exporterMetrics(proto=%s) unexpected error = %v", proto, err)
			}
			exporter := otelConfig.Exporters[exporterId]
			metricsBlock := exporter["metrics"].(map[string]interface{})
			sumsBlock := metricsBlock["sums"].(map[string]interface{})
			assert.Equal(t, "keep", sumsBlock["initial_cumulative_monotonic_value"])
		})
	}
}
