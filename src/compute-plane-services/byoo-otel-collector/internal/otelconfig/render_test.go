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
	"testing"

	"fmt"

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
      level: normal
      readers:
        - pull:
            exporter:
              prometheus:
                host: "${env:OTEL_POD_IP:-0.0.0.0}"
                port: 18888
    resource:
      function.id: test-function-id
      function.version.id: test-function-version-id
      service.name: byoo-otel-collector
      service.namespace: test-namespace
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
      level: normal
      readers:
        - pull:
            exporter:
              prometheus:
                host: "${env:OTEL_POD_IP:-0.0.0.0}"
                port: 18888
    resource:
      service.name: byoo-otel-collector
      service.namespace: test-namespace
      task.id: test-task-id
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
