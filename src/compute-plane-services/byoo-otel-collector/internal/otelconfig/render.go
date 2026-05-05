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
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Telemetry struct {
	Protocol Protocol `json:"protocol"`
	Provider Provider `json:"provider"`
	Endpoint string   `json:"endpoint"`
	Name     string   `json:"name"`
}

type Telemetries struct {
	Logs    *Telemetry `json:"logsTelemetry,omitempty"`
	Metrics *Telemetry `json:"metricsTelemetry,omitempty"`
	Traces  *Telemetry `json:"tracesTelemetry,omitempty"`
}

// TelemetryConfig is the top-level structure for configured telemetry settings.
type TelemetryConfig struct {
	Telemetries Telemetries `json:"telemetries"`
}

// OTel config is yaml and has receivers, exporters, processors, extensions, and service
type OpenTelemetryConfig struct {
	Receivers  map[string]map[string]interface{} `yaml:"receivers"`
	Exporters  map[string]map[string]interface{} `yaml:"exporters"`
	Processors map[string]map[string]interface{} `yaml:"processors"`
	Extensions map[string]map[string]interface{} `yaml:"extensions"`
	Service    struct {
		Telemetry map[string]map[string]interface{} `yaml:"telemetry"`
		Pipelines map[string]struct {
			Receivers  []string `yaml:"receivers"`
			Exporters  []string `yaml:"exporters"`
			Processors []string `yaml:"processors"`
		} `yaml:"pipelines"`
		Extensions []string `yaml:"extensions"`
	} `yaml:"service"`
}

// Initialize the maps if they are nil
func initializeConfigMaps(otelConfig *OpenTelemetryConfig) {
	if otelConfig.Receivers == nil {
		otelConfig.Receivers = make(map[string]map[string]interface{})
	}
	if otelConfig.Exporters == nil {
		otelConfig.Exporters = make(map[string]map[string]interface{})
	}
	if otelConfig.Processors == nil {
		otelConfig.Processors = make(map[string]map[string]interface{})
	}
	if otelConfig.Extensions == nil {
		otelConfig.Extensions = make(map[string]map[string]interface{})
	}
	if otelConfig.Service.Telemetry == nil {
		otelConfig.Service.Telemetry = make(map[string]map[string]interface{})
	}
	if otelConfig.Service.Pipelines == nil {
		otelConfig.Service.Pipelines = make(map[string]struct {
			Receivers  []string `yaml:"receivers"`
			Exporters  []string `yaml:"exporters"`
			Processors []string `yaml:"processors"`
		})
	}
}

func RenderOtelConfigFromBytes(inputData []byte, tmplConfig TemplateConfig) ([]byte, error) {
	var telemetryConfig TelemetryConfig
	err := json.Unmarshal(inputData, &telemetryConfig)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling input data: %v", err)
	}
	return RenderOtelConfig(telemetryConfig, tmplConfig)
}

func RenderOtelConfig(telemetryConfig TelemetryConfig, tmplConfig TemplateConfig) ([]byte, error) {
	configData := &bytes.Buffer{}
	if err := ExecuteTemplate(configData, tmplConfig); err != nil {
		return nil, fmt.Errorf("execute config template: %v", err)
	}

	otelConfig := &OpenTelemetryConfig{}
	initializeConfigMaps(otelConfig)
	if err := yaml.Unmarshal(configData.Bytes(), otelConfig); err != nil {
		return nil, fmt.Errorf("failed to unmarshal backend config: %v", err)
	}

	if err := generateExportersAndService(telemetryConfig, otelConfig, tmplConfig); err != nil {
		return nil, fmt.Errorf("failed to generate exporters and service: %v", err)
	}

	// Create a buffer to hold the YAML output
	var buf bytes.Buffer

	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)

	// Marshal the final config back to YAML
	if err := encoder.Encode(otelConfig); err != nil {
		return nil, fmt.Errorf("failed to marshal final config: %v", err)
	}

	return buf.Bytes(), nil
}

func getCredentialsPath() string {
	if credentialPath := os.Getenv("ESS_SECRETS_PATH"); credentialPath != "" {
		return credentialPath
	}
	return "/etc/byoo-otel-collector/secrets"
}

func exporterLogs(config TelemetryConfig, otelConfig *OpenTelemetryConfig) (exporterId string, err error) {
	var exporterType, exporterName string
	var exporterCredential interface{}

	var extensionType, extensionName, extensionId string
	var extensionCredential interface{}
	credentialPath := getCredentialsPath()

	switch config.Telemetries.Logs.Provider {
	case ProviderSplunk:
		exporterType = "splunk_hec"
		exporterName = fmt.Sprintf("%s-%s-logs", config.Telemetries.Logs.Provider, config.Telemetries.Logs.Name)
		exporterId = fmt.Sprintf("%s/%s", exporterType, exporterName)
		exporterCredential := fmt.Sprintf("${file:%s}", filepath.Join(credentialPath, config.Telemetries.Logs.Name))

		otelConfig.Exporters[exporterId] = map[string]interface{}{
			"endpoint": config.Telemetries.Logs.Endpoint,
			"token":    exporterCredential,
		}
	case ProviderGrafana:
		exporterType = "otlphttp"
		exporterName = fmt.Sprintf("%s-%s-logs", config.Telemetries.Logs.Provider, config.Telemetries.Logs.Name)
		exporterId = fmt.Sprintf("%s/%s", exporterType, exporterName)

		extensionType = "basicauth"
		extensionName = fmt.Sprintf("%s-%s-logs", config.Telemetries.Logs.Provider, config.Telemetries.Logs.Name)
		extensionId = fmt.Sprintf("%s/%s", extensionType, extensionName)

		extensionCredential = map[string]string{
			"username": fmt.Sprintf("${file:%s-instanceId}", filepath.Join(credentialPath, config.Telemetries.Logs.Name)),
			"password": fmt.Sprintf("${file:%s-apiKey}", filepath.Join(credentialPath, config.Telemetries.Logs.Name)),
		}

		otelConfig.Exporters[exporterId] = map[string]interface{}{
			"endpoint": config.Telemetries.Logs.Endpoint,
			"auth": map[string]interface{}{
				"authenticator": extensionId, // Using grafana_cloud authenticator
			},
		}
		otelConfig.Extensions[extensionId] = map[string]interface{}{
			"client_auth": extensionCredential,
		}
		otelConfig.Service.Extensions = append(otelConfig.Service.Extensions, extensionId)
	case ProviderDatadog:
		exporterType = "datadog"
		exporterName = fmt.Sprintf("%s-%s-logs", config.Telemetries.Logs.Provider, config.Telemetries.Logs.Name)
		exporterId = fmt.Sprintf("%s/%s", exporterType, exporterName)
		exporterCredential = fmt.Sprintf("${file:%s}", filepath.Join(credentialPath, config.Telemetries.Logs.Name))

		otelConfig.Exporters[exporterId] = map[string]interface{}{
			"api": map[string]interface{}{
				"site":              config.Telemetries.Logs.Endpoint,
				"key":               exporterCredential,
				"fail_on_invalid_key": false,
			},
			"host_metadata": map[string]interface{}{
				"enabled":         true,
				"hostname_source": "first_resource",
			},
		}
	case ProviderKratosLogs:
		exporterType = "otlphttp"
		exporterName = fmt.Sprintf("%s-%s-logs", config.Telemetries.Logs.Provider, config.Telemetries.Logs.Name)
		exporterId = fmt.Sprintf("%s/%s", exporterType, exporterName)

		collectorId := fmt.Sprintf("${file:%s-collectorId}", filepath.Join(credentialPath, config.Telemetries.Logs.Name))

		otelConfig.Exporters[exporterId] = map[string]interface{}{
			"logs_endpoint": config.Telemetries.Logs.Endpoint,
			"encoding": "json",
			"headers": map[string]interface{}{
				"collector-id": collectorId,
			},
			"tls": map[string]interface{}{
				"cert_file": filepath.Join(credentialPath, fmt.Sprintf("%s-clientCert", config.Telemetries.Logs.Name)),
				"key_file":  filepath.Join(credentialPath, fmt.Sprintf("%s-clientKey", config.Telemetries.Logs.Name)),
			},
		}
	case ProviderAzureMonitor:
		exporterType = "azuremonitor"
		exporterName = fmt.Sprintf("%s-%s-logs", config.Telemetries.Logs.Provider, config.Telemetries.Logs.Name)
		exporterId = fmt.Sprintf("%s/%s", exporterType, exporterName)

		fileName := fmt.Sprintf("%s-instrumentationKey", config.Telemetries.Logs.Name)
		instrumentationKey := filepath.Join(credentialPath, fileName)

		fileName = fmt.Sprintf("%s-applicationId", config.Telemetries.Logs.Name)
		applicationId := filepath.Join(credentialPath, fileName)

		fileName = fmt.Sprintf("%s-liveEndpoint", config.Telemetries.Logs.Name)
		liveEndpoint := filepath.Join(credentialPath, fileName)

		ingestionEndpoint := config.Telemetries.Logs.Endpoint

		otelConfig.Exporters[exporterId] = map[string]interface{}{
			"connection_string": fmt.Sprintf("InstrumentationKey=${file:%s};IngestionEndpoint=%s;LiveEndpoint=${file:%s};ApplicationId=${file:%s}", instrumentationKey, ingestionEndpoint, liveEndpoint, applicationId),
		}
	case ProviderOtelCollector:
		exporterType = "otlphttp"
		exporterName = fmt.Sprintf("%s-%s-logs", config.Telemetries.Logs.Provider, config.Telemetries.Logs.Name)
		exporterId = fmt.Sprintf("%s/%s", exporterType, exporterName)
		exporterCredential = fmt.Sprintf("${file:%s}", filepath.Join(credentialPath, config.Telemetries.Logs.Name))

		otelConfig.Exporters[exporterId] = map[string]interface{}{
			"endpoint": config.Telemetries.Logs.Endpoint,
			"headers": map[string]interface{}{
				"Authorization": fmt.Sprintf("Bearer %s", exporterCredential),
			},
		}
	default:
		return "", fmt.Errorf("invalid logs provider: %s", config.Telemetries.Logs.Provider)
	}
	return exporterId, nil
}

func exporterMetrics(config TelemetryConfig, otelConfig *OpenTelemetryConfig) (exporterId string, err error) {
	var exporterType, exporterName string
	var exporterCredential interface{}

	var extensionType, extensionName, extensionId string
	var extensionCredential interface{}
	credentialPath := getCredentialsPath()

	switch config.Telemetries.Metrics.Provider {
	case ProviderGrafana:
		exporterType = "otlphttp"
		exporterName = fmt.Sprintf("%s-%s-metrics", config.Telemetries.Metrics.Provider, config.Telemetries.Metrics.Name)
		exporterId = fmt.Sprintf("%s/%s", exporterType, exporterName)

		extensionType = "basicauth"
		extensionName = fmt.Sprintf("%s-%s-metrics", config.Telemetries.Metrics.Provider, config.Telemetries.Metrics.Name)
		extensionId = fmt.Sprintf("%s/%s", extensionType, extensionName)

		extensionCredential = map[string]string{
			"username": fmt.Sprintf("${file:%s-instanceId}", filepath.Join(credentialPath, config.Telemetries.Metrics.Name)),
			"password": fmt.Sprintf("${file:%s-apiKey}", filepath.Join(credentialPath, config.Telemetries.Metrics.Name)),
		}

		otelConfig.Exporters[exporterId] = map[string]interface{}{
			"endpoint": config.Telemetries.Metrics.Endpoint,
			"auth": map[string]interface{}{
				"authenticator": extensionId,
			},
		}
		otelConfig.Extensions[extensionId] = map[string]interface{}{
			"client_auth": extensionCredential,
		}

		otelConfig.Service.Extensions = append(otelConfig.Service.Extensions, extensionId)

	case ProviderThanos, ProviderPrometheus:
		exporterType = "prometheusremotewrite"
		exporterName = fmt.Sprintf("%s-%s-metrics", config.Telemetries.Metrics.Provider, config.Telemetries.Metrics.Name)
		exporterId = fmt.Sprintf("%s/%s", exporterType, exporterName)

		secretsPathPrefix := filepath.Join(credentialPath, config.Telemetries.Metrics.Name)

		exporterCredential = make(map[string]string)
		if creds, ok := exporterCredential.(map[string]string); ok {
			creds["cert_file"] = fmt.Sprintf("%s-clientCert", secretsPathPrefix)
			creds["key_file"] = fmt.Sprintf("%s-clientKey", secretsPathPrefix)

			ca_file := fmt.Sprintf("%s-caFile", secretsPathPrefix)
			if _, err := os.Stat(ca_file); err == nil {
				creds["ca_file"] = ca_file
			}
		}

		otelConfig.Exporters[exporterId] = map[string]interface{}{
			"endpoint": config.Telemetries.Metrics.Endpoint,
			"tls":      exporterCredential,
		}

	case ProviderDatadog:
		exporterType = "datadog"
		exporterName = fmt.Sprintf("%s-%s-metrics", config.Telemetries.Metrics.Provider, config.Telemetries.Metrics.Name)
		exporterId = fmt.Sprintf("%s/%s", exporterType, exporterName)
		exporterCredential = fmt.Sprintf("${file:%s}", filepath.Join(credentialPath, config.Telemetries.Metrics.Name))

		otelConfig.Exporters[exporterId] = map[string]interface{}{
			"api": map[string]interface{}{
				"site":              config.Telemetries.Metrics.Endpoint,
				"key":               exporterCredential,
				"fail_on_invalid_key": false,
			},
			"host_metadata": map[string]interface{}{
				"enabled":         true,
				"hostname_source": "first_resource",
			},
		}
	case ProviderAzureMonitor:
		exporterType = "azuremonitor"
		exporterName = fmt.Sprintf("%s-%s-metrics", config.Telemetries.Metrics.Provider, config.Telemetries.Metrics.Name)
		exporterId = fmt.Sprintf("%s/%s", exporterType, exporterName)

		fileName := fmt.Sprintf("%s-instrumentationKey", config.Telemetries.Metrics.Name)
		instrumentationKey := filepath.Join(credentialPath, fileName)

		fileName = fmt.Sprintf("%s-applicationId", config.Telemetries.Metrics.Name)
		applicationId := filepath.Join(credentialPath, fileName)

		fileName = fmt.Sprintf("%s-liveEndpoint", config.Telemetries.Metrics.Name)
		liveEndpoint := filepath.Join(credentialPath, fileName)

		ingestionEndpoint := config.Telemetries.Metrics.Endpoint

		otelConfig.Exporters[exporterId] = map[string]interface{}{
			"connection_string": fmt.Sprintf("InstrumentationKey=${file:%s};IngestionEndpoint=%s;LiveEndpoint=${file:%s};ApplicationId=${file:%s}", instrumentationKey, ingestionEndpoint, liveEndpoint, applicationId),
		}
	case ProviderOtelCollector:
		exporterType = "otlphttp"
		exporterName = fmt.Sprintf("%s-%s-metrics", config.Telemetries.Metrics.Provider, config.Telemetries.Metrics.Name)
		exporterId = fmt.Sprintf("%s/%s", exporterType, exporterName)
		exporterCredential = fmt.Sprintf("${file:%s}", filepath.Join(credentialPath, config.Telemetries.Metrics.Name))

		otelConfig.Exporters[exporterId] = map[string]interface{}{
			"endpoint": config.Telemetries.Metrics.Endpoint,
			"headers": map[string]interface{}{
				"Authorization": fmt.Sprintf("Bearer %s", exporterCredential),
			},
		}
	default:
		return "", fmt.Errorf("invalid metrics provider: %s", config.Telemetries.Metrics.Provider)
	}
	return exporterId, nil
}

func exporterTraces(config TelemetryConfig, otelConfig *OpenTelemetryConfig) (exporterId string, err error) {
	var exporterType, exporterName string
	var exporterCredential interface{}

	var extensionType, extensionName, extensionId string
	var extensionCredential interface{}
	credentialPath := getCredentialsPath()

	switch config.Telemetries.Traces.Provider {
	case ProviderGrafana:
		exporterType = "otlphttp"
		exporterName = fmt.Sprintf("%s-%s-traces", config.Telemetries.Traces.Provider, config.Telemetries.Traces.Name)
		exporterId = fmt.Sprintf("%s/%s", exporterType, exporterName)

		extensionType = "basicauth"
		extensionName = fmt.Sprintf("%s-%s-traces", config.Telemetries.Traces.Provider, config.Telemetries.Traces.Name)
		extensionId = fmt.Sprintf("%s/%s", extensionType, extensionName)

		extensionCredential = map[string]string{
			"username": fmt.Sprintf("${file:%s-instanceId}", filepath.Join(credentialPath, config.Telemetries.Traces.Name)),
			"password": fmt.Sprintf("${file:%s-apiKey}", filepath.Join(credentialPath, config.Telemetries.Traces.Name)),
		}

		otelConfig.Exporters[exporterId] = map[string]interface{}{
			"endpoint": config.Telemetries.Traces.Endpoint,
			"auth": map[string]interface{}{
				"authenticator": extensionId, // Using grafana_cloud authenticator
			},
		}
		otelConfig.Extensions[extensionId] = map[string]interface{}{
			"client_auth": extensionCredential,
		}

		otelConfig.Service.Extensions = append(otelConfig.Service.Extensions, extensionId)

	case ProviderDatadog:
		exporterType = "datadog"
		exporterName = fmt.Sprintf("%s-%s-traces", config.Telemetries.Traces.Provider, config.Telemetries.Traces.Name)
		exporterId = fmt.Sprintf("%s/%s", exporterType, exporterName)
		exporterCredential = fmt.Sprintf("${file:%s}", filepath.Join(credentialPath, config.Telemetries.Traces.Name))

		otelConfig.Exporters[exporterId] = map[string]interface{}{
			"api": map[string]interface{}{
				"site":              config.Telemetries.Traces.Endpoint,
				"key":               exporterCredential,
				"fail_on_invalid_key": false,
			},
			"host_metadata": map[string]interface{}{
				"enabled":         true,
				"hostname_source": "first_resource",
			},
		}

	case ProviderServiceNow:
		exporterType = "otlp"
		exporterName = fmt.Sprintf("%s-%s-traces", config.Telemetries.Traces.Provider, config.Telemetries.Traces.Name)
		exporterId = fmt.Sprintf("%s/%s", exporterType, exporterName)
		exporterCredential = fmt.Sprintf("${file:%s}", filepath.Join(credentialPath, config.Telemetries.Traces.Name))

		otelConfig.Exporters[exporterId] = map[string]interface{}{
			"endpoint": config.Telemetries.Traces.Endpoint,
			"headers": map[string]interface{}{
				"lightstep-access-token": exporterCredential,
			},
		}
	case ProviderAzureMonitor:
		exporterType = "azuremonitor"
		exporterName = fmt.Sprintf("%s-%s-traces", config.Telemetries.Traces.Provider, config.Telemetries.Traces.Name)
		exporterId = fmt.Sprintf("%s/%s", exporterType, exporterName)

		fileName := fmt.Sprintf("%s-instrumentationKey", config.Telemetries.Traces.Name)
		instrumentationKey := filepath.Join(credentialPath, fileName)

		fileName = fmt.Sprintf("%s-applicationId", config.Telemetries.Traces.Name)
		applicationId := filepath.Join(credentialPath, fileName)

		fileName = fmt.Sprintf("%s-liveEndpoint", config.Telemetries.Traces.Name)
		liveEndpoint := filepath.Join(credentialPath, fileName)

		ingestionEndpoint := config.Telemetries.Traces.Endpoint

		otelConfig.Exporters[exporterId] = map[string]interface{}{
			"connection_string": fmt.Sprintf("InstrumentationKey=${file:%s};IngestionEndpoint=%s;LiveEndpoint=${file:%s};ApplicationId=${file:%s}", instrumentationKey, ingestionEndpoint, liveEndpoint, applicationId),
			"spaneventsenabled": true,
		}
	case ProviderOtelCollector:
		exporterType = "otlphttp"
		exporterName = fmt.Sprintf("%s-%s-traces", config.Telemetries.Traces.Provider, config.Telemetries.Traces.Name)
		exporterId = fmt.Sprintf("%s/%s", exporterType, exporterName)
		exporterCredential = fmt.Sprintf("${file:%s}", filepath.Join(credentialPath, config.Telemetries.Traces.Name))

		otelConfig.Exporters[exporterId] = map[string]interface{}{
			"endpoint": config.Telemetries.Traces.Endpoint,
			"headers": map[string]interface{}{
				"Authorization": fmt.Sprintf("Bearer %s", exporterCredential),
			},
		}
	default:
		return "", fmt.Errorf("invalid traces provider: %s", config.Telemetries.Traces.Provider)
	}
	return exporterId, nil
}

func generateExportersAndService(config TelemetryConfig, otelConfig *OpenTelemetryConfig, tmplConfig TemplateConfig) error {
	// health_check and healthcheckv2 extensions are present for all configurations
	otelConfig.Service.Extensions = []string{"healthcheckv2", "cgroupruntime"}

	// Default telemetry configuration for the collector's own metrics, logs, and traces
	otelConfig.Service.Telemetry = map[string]map[string]interface{}{
		"logs": {
			"level": "warn",
			"initial_fields": map[string]interface{}{
				"public": "true",
			},
		},
		"metrics": {
			"level": "normal",
			"readers": []map[string]interface{}{
				{
					"pull": map[string]interface{}{
						"exporter": map[string]interface{}{
							"prometheus": map[string]interface{}{
								"host": "${env:OTEL_POD_IP:-0.0.0.0}",
								"port": 18888,
							},
						},
					},
				},
			},
		},
		"traces": {
			"processors": []map[string]interface{}{
				{
					"batch": map[string]interface{}{
						"exporter": map[string]interface{}{
							"otlp": map[string]interface{}{
								"protocol": "grpc",
								"endpoint": "${env:OTEL_EXPORTER_OTLP_ENDPOINT}",
								"headers": map[string]interface{}{
									"lightstep-access-token": "${env:OTEL_TRACING_ACCESS_TOKEN}",
								},
							},
						},
					},
				},
			},
		},
	}

	resourceAttrs := map[string]string{
		"service.namespace": tmplConfig.Namespace,
		"service.name":      "byoo-otel-collector",
	}

	if tmplConfig.FunctionID != "" {
		resourceAttrs["function.id"] = tmplConfig.FunctionID
		resourceAttrs["function.version.id"] = tmplConfig.FunctionVersionID
	}
	if tmplConfig.TaskID != "" {
		resourceAttrs["task.id"] = tmplConfig.TaskID
	}

	finalResourceAttrs := make(map[string]interface{}, len(resourceAttrs))
	for k, v := range resourceAttrs {
		finalResourceAttrs[k] = v
	}
	otelConfig.Service.Telemetry["resource"] = finalResourceAttrs

	// Process Logs (if present)
	if config.Telemetries.Logs != nil {
		exporterId, err := exporterLogs(config, otelConfig)
		if err != nil {
			return fmt.Errorf("failed to generate exporter for logs: %v", err)
		}

		// create a new pipeline for logs
		logPipeline := otelConfig.Service.Pipelines["logs"]
		logPipeline.Receivers = []string{"otlp"}
		logPipeline.Exporters = []string{exporterId}
		logPipeline.Processors = []string{"memory_limiter", "attributes/add-metadata", "batch"}
		otelConfig.Service.Pipelines["logs"] = logPipeline
	}

	// Process Metrics (if present)
	if config.Telemetries.Metrics != nil {
		exporterId, err := exporterMetrics(config, otelConfig)
		if err != nil {
			return fmt.Errorf("failed to generate exporter for metrics: %v", err)
		}

		metricPipeline := otelConfig.Service.Pipelines["metrics"]
		metricPipeline.Receivers = []string{"otlp", "prometheus"}
		metricPipeline.Exporters = []string{exporterId}
		metricPipeline.Processors = []string{"memory_limiter", "filter/metrics", "resource", "metricstransform", "batch"}
		otelConfig.Service.Pipelines["metrics"] = metricPipeline
	}

	// Process Traces (if present)
	if config.Telemetries.Traces != nil {
		exporterId, err := exporterTraces(config, otelConfig)
		if err != nil {
			return fmt.Errorf("failed to generate exporter for traces: %v", err)
		}

		tracePipeline := otelConfig.Service.Pipelines["traces"]
		tracePipeline.Receivers = []string{"otlp"}
		tracePipeline.Exporters = []string{exporterId}
		tracePipeline.Processors = []string{"memory_limiter", "attributes/add-metadata", "batch"}
		otelConfig.Service.Pipelines["traces"] = tracePipeline
	}

	return nil
}

