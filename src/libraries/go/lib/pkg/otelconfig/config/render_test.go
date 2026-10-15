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

package otelconfig

import (
	"testing"

	"fmt"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/otelconfig/backendconfig"
	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v3"
)

func TestRenderOtelConfig(t *testing.T) {
	tests := []struct {
		name         string
		inputData    []byte
		workloadType backendconfig.WorkloadType
		backendType  backendconfig.BackendType
		expectError  bool
	}{
		{
			name:         "Valid Input VM Container",
			inputData:    []byte(`{"telemetries": {"logsTelemetry": {"protocol": "HTTP", "provider": "SPLUNK", "endpoint": "http://example.com", "name": "example-logs"}}}`),
			workloadType: backendconfig.Container,
			backendType:  backendconfig.VM,
			expectError:  false,
		},
		{
			name:         "Valid Input VM Helm",
			inputData:    []byte(`{"telemetries": {"logsTelemetry": {"protocol": "HTTP", "provider": "SPLUNK", "endpoint": "http://example.com", "name": "example-logs"}}}`),
			workloadType: backendconfig.Helm,
			backendType:  backendconfig.VM,
			expectError:  false,
		},
		{
			name:         "Valid Input K8s",
			inputData:    []byte(`{"telemetries": {"logsTelemetry": {"protocol": "HTTP", "provider": "SPLUNK", "endpoint": "http://example.com", "name": "example-logs"}}}`),
			workloadType: backendconfig.Container,
			backendType:  backendconfig.K8s,
			expectError:  false,
		},
		{
			name:         "Unknown Provider",
			inputData:    []byte(`{"telemetries": {"logsTelemetry": {"protocol": "HTTP", "provider": "UNKNOWN", "endpoint": "http://example.com", "name": "example-logs"}}}`),
			workloadType: backendconfig.Container,
			backendType:  backendconfig.VM,
			expectError:  true,
		},
		{
			name:         "Lowercase Protocol",
			inputData:    []byte(`{"telemetries": {"logsTelemetry": {"protocol": "http", "provider": "SPLUNK", "endpoint": "http://example.com", "name": "example-logs"}}}`),
			workloadType: backendconfig.Container,
			backendType:  backendconfig.VM,
			expectError:  false,
		},
		{
			name:         "Valid Input ServiceNow Traces",
			inputData:    []byte(`{"telemetries": {"tracesTelemetry": {"protocol": "http", "provider": "SERVICENOW", "endpoint": "https://traces.example.com:8282", "name": "example-internal-traces"}}}`),
			workloadType: backendconfig.Container,
			backendType:  backendconfig.VM,
			expectError:  false,
		},
		{
			name:         "Valid Input Grafana Logs Metrics Traces",
			inputData:    []byte(`{"telemetries": {"logsTelemetry": {"protocol": "HTTP", "provider": "GRAFANA_CLOUD", "endpoint": "https://logs.example.com", "name": "grafana-logs"}, "metricsTelemetry": {"protocol": "HTTP", "provider": "GRAFANA_CLOUD", "endpoint": "https://metrics.example.com", "name": "grafana-metrics"}, "tracesTelemetry": {"protocol": "HTTP", "provider": "GRAFANA_CLOUD", "endpoint": "https://traces.example.com", "name": "grafana-traces"}}}`),
			workloadType: backendconfig.Container,
			backendType:  backendconfig.VM,
			expectError:  false,
		},
		{
			name:         "Valid Input Datadog Logs Metrics Traces",
			inputData:    []byte(`{"telemetries": {"logsTelemetry": {"protocol": "HTTP", "provider": "DATADOG", "endpoint": "datadoghq.com", "name": "datadog-logs"}, "metricsTelemetry": {"protocol": "HTTP", "provider": "DATADOG", "endpoint": "datadoghq.com", "name": "datadog-metrics"}, "tracesTelemetry": {"protocol": "HTTP", "provider": "DATADOG", "endpoint": "datadoghq.com", "name": "datadog-traces"}}}`),
			workloadType: backendconfig.Container,
			backendType:  backendconfig.VM,
			expectError:  false,
		},
		{
			name:         "Valid Input Azure Monitor Logs Metrics Traces",
			inputData:    []byte(`{"telemetries": {"logsTelemetry": {"protocol": "HTTP", "provider": "AZURE_MONITOR", "endpoint": "https://azure.example.com", "name": "azure-logs"}, "metricsTelemetry": {"protocol": "HTTP", "provider": "AZURE_MONITOR", "endpoint": "https://azure.example.com", "name": "azure-metrics"}, "tracesTelemetry": {"protocol": "HTTP", "provider": "AZURE_MONITOR", "endpoint": "https://azure.example.com", "name": "azure-traces"}}}`),
			workloadType: backendconfig.Container,
			backendType:  backendconfig.VM,
			expectError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCfg, err := RenderOtelConfigFromBytes(tt.inputData, backendconfig.TemplateConfig{
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

// returns the expected OpenTelemetry YAML configuration as a byte slice
func createExpectedOtelConfigYAMLForInternalTelemetry(tracesTelemetryName string) []byte {
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
    traces:
      processors:
        - batch:
            exporter:
              otlp:
                protocol: grpc
                endpoint: ${env:OTEL_EXPORTER_OTLP_ENDPOINT}
                headers:
                  lightstep-access-token: ${env:OTEL_TRACING_ACCESS_TOKEN}
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
    - cgroupruntime
`, tracesTelemetryName, tracesTelemetryName, tracesTelemetryName)
	return []byte(internalTelemetryYAMLString)
}

func Test_generateExportersAndService(t *testing.T) {
	type args struct {
		config                 TelemetryConfig
		otelConfig             *OpenTelemetryConfig
		expectedOtelConfigYAML []byte
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "Export internal traces",
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
				expectedOtelConfigYAML: createExpectedOtelConfigYAMLForInternalTelemetry("example-trace"),
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := generateExportersAndService(tt.args.config, tt.args.otelConfig); (err != nil) != tt.wantErr {
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
