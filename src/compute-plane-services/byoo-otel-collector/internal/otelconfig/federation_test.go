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
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const (
	federationExampleDir   = "../../examples/otelconfigs/k8s"
	federationTelemetries  = "../../testdata/otelcollector_stg.json"
	federationCustomTarget = "vmsingle.monitoring.svc.cluster.local:8428"
)

var federatedJobs = []string{
	"nvcf-worker",
	"nvca",
	"kubernetes-cadvisor",
	"kube-state-metrics",
	"nvidia-dcgm-exporter",
}

// federationExampleTemplateConfig matches the function template config that
// scripts/generate-otelconfig.go uses, so the default case can be compared with
// the examples that make update-examples writes.
func federationExampleTemplateConfig(workloadType WorkloadType, federation PlatformMetricsFederationConfig) TemplateConfig {
	return TemplateConfig{
		BackendType:       K8s,
		WorkloadType:      workloadType,
		Namespace:         "sr-fake-namespace",
		FunctionID:        "fake-function-id",
		FunctionVersionID: "fake-function-version-id",
		InstanceID:        "fake-instance-id",
		ZoneName:          "fake-zone-name",
		OTelCollector:     OTelCollectorConfig{PlatformMetricsFederation: federation},
		// Examples show a deployment that sets OTEL_EXPORTER_OTLP_ENDPOINT.
		CollectorTracesEnabled: true,
	}
}

func TestRenderOtelConfigPlatformMetricsFederationMatchesExamples(t *testing.T) {
	t.Setenv("ESS_SECRETS_PATH", "")
	disabled := false
	enabled := true
	telemetries, err := os.ReadFile(federationTelemetries)
	require.NoError(t, err)

	tests := []struct {
		name       string
		federation PlatformMetricsFederationConfig
		// suffix selects the example file. The unset case reuses the example
		// that make update-examples writes for the same telemetries.
		suffix     string
		wantTarget string
	}{
		{name: "unset", suffix: "", wantTarget: DefaultPlatformMetricsFederationTarget},
		{name: "enabled", federation: PlatformMetricsFederationConfig{Enabled: &enabled}, suffix: "",
			wantTarget: DefaultPlatformMetricsFederationTarget},
		{name: "custom target", federation: PlatformMetricsFederationConfig{Target: federationCustomTarget},
			suffix: "_federation_target", wantTarget: federationCustomTarget},
		{name: "disabled", federation: PlatformMetricsFederationConfig{Enabled: &disabled},
			suffix: "_federation_disabled"},
		{name: "disabled with target",
			federation: PlatformMetricsFederationConfig{Enabled: &disabled, Target: federationCustomTarget},
			suffix:     "_federation_disabled"},
	}

	for _, workloadType := range []WorkloadType{Container, Helm} {
		for _, tt := range tests {
			t.Run(fmt.Sprintf("%s/%s", workloadType, tt.name), func(t *testing.T) {
				gotCfg, err := RenderOtelConfigFromBytes(telemetries,
					federationExampleTemplateConfig(workloadType, tt.federation))
				require.NoError(t, err)

				assertFederatedJobs(t, gotCfg, tt.wantTarget)

				examplePath := filepath.Join(federationExampleDir,
					fmt.Sprintf("config_function_%s_otelcollector_stg%s.yaml", workloadType, tt.suffix))
				if tt.suffix != "" && os.Getenv("UPDATE_FEDERATION_EXAMPLES") == "true" {
					exampleConfig := append([]byte(metricSubsetExampleHeader), gotCfg...)
					require.NoError(t, os.WriteFile(examplePath, exampleConfig, 0o644))
				}
				expectedCfg, err := os.ReadFile(examplePath)
				require.NoError(t, err)
				assertYAMLConfigEqual(t, expectedCfg, gotCfg)
			})
		}
	}
}

// assertFederatedJobs checks the prometheus receiver. An empty wantTarget
// means federation is disabled: only the collector self-scrape job remains,
// and the metrics pipeline still exports workload metrics received over OTLP.
func assertFederatedJobs(t *testing.T, renderedConfig []byte, wantTarget string) {
	t.Helper()

	var cfg struct {
		Receivers struct {
			Prometheus struct {
				Config struct {
					ScrapeConfigs []struct {
						JobName       string `yaml:"job_name"`
						MetricsPath   string `yaml:"metrics_path"`
						StaticConfigs []struct {
							Targets []string `yaml:"targets"`
						} `yaml:"static_configs"`
					} `yaml:"scrape_configs"`
				} `yaml:"config"`
			} `yaml:"prometheus"`
		} `yaml:"receivers"`
		Service struct {
			Pipelines map[string]struct {
				Receivers []string `yaml:"receivers"`
			} `yaml:"pipelines"`
		} `yaml:"service"`
	}
	require.NoError(t, yaml.Unmarshal(renderedConfig, &cfg))

	var gotJobs []string
	for _, scrapeConfig := range cfg.Receivers.Prometheus.Config.ScrapeConfigs {
		if scrapeConfig.MetricsPath != "/federate" {
			continue
		}
		gotJobs = append(gotJobs, scrapeConfig.JobName)
		require.Len(t, scrapeConfig.StaticConfigs, 1, scrapeConfig.JobName)
		assert.Equal(t, []string{wantTarget}, scrapeConfig.StaticConfigs[0].Targets, scrapeConfig.JobName)
	}
	if wantTarget == "" {
		assert.Empty(t, gotJobs)
		require.Len(t, cfg.Receivers.Prometheus.Config.ScrapeConfigs, 1)
		assert.Equal(t, "opentelemetry-collector", cfg.Receivers.Prometheus.Config.ScrapeConfigs[0].JobName)
	} else {
		assert.Equal(t, federatedJobs, gotJobs)
	}
	assert.Equal(t, []string{"otlp", "prometheus"}, cfg.Service.Pipelines["metrics"].Receivers)
}

func TestRenderOtelConfigVMBackendIgnoresPlatformMetricsFederation(t *testing.T) {
	disabled := false
	telemetries, err := os.ReadFile(federationTelemetries)
	require.NoError(t, err)

	for _, workloadType := range []WorkloadType{Container, Helm} {
		t.Run(string(workloadType), func(t *testing.T) {
			tmplConfig := federationExampleTemplateConfig(workloadType, PlatformMetricsFederationConfig{})
			tmplConfig.BackendType = VM
			want, err := RenderOtelConfigFromBytes(telemetries, tmplConfig)
			require.NoError(t, err)

			tmplConfig.OTelCollector.PlatformMetricsFederation = PlatformMetricsFederationConfig{
				Enabled: &disabled,
				Target:  federationCustomTarget,
			}
			got, err := RenderOtelConfigFromBytes(telemetries, tmplConfig)
			require.NoError(t, err)
			assert.Equal(t, string(want), string(got))
		})
	}
}

func TestRenderOtelConfigRejectsInvalidFederationTarget(t *testing.T) {
	telemetries, err := os.ReadFile(federationTelemetries)
	require.NoError(t, err)

	_, err = RenderOtelConfigFromBytes(telemetries, federationExampleTemplateConfig(Container,
		PlatformMetricsFederationConfig{Target: "prometheus.example.invalid:9090\"\n  injected: true"}))
	require.ErrorContains(t, err, "validate BYOO OTel collector config: platform metrics federation: target")
}

func TestPlatformMetricsFederationConfigValidate(t *testing.T) {
	tests := []struct {
		target  string
		wantErr string
	}{
		{target: ""},
		{target: federationCustomTarget},
		{target: "Prometheus.Example.Invalid:9090"},
		{target: "10.0.0.1:9090"},
		{target: "[fd00::1]:9090"},
		{target: "prometheus.example.invalid", wantErr: "must be host:port"},
		{target: "http://prometheus.example.invalid:9090", wantErr: "must be host:port"},
		{target: ":9090", wantErr: "DNS name or IP address"},
		{target: "prometheus_example:9090", wantErr: "DNS name or IP address"},
		{target: "prometheus.example.invalid:http", wantErr: "port between 1 and 65535"},
		{target: "prometheus.example.invalid:0", wantErr: "port between 1 and 65535"},
		{target: "prometheus.example.invalid:65536", wantErr: "port between 1 and 65535"},
	}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			err := OTelCollectorConfig{
				PlatformMetricsFederation: PlatformMetricsFederationConfig{Target: tt.target},
			}.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "platform metrics federation: ")
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestGetTemplateConfigDecodesPlatformMetricsFederation(t *testing.T) {
	setFunctionTemplateEnv(t)
	t.Setenv("BYOO_OTEL_COLLECTOR_CONFIG_B64", base64.StdEncoding.EncodeToString(
		[]byte(`{"platformMetricsFederation": {"enabled": false, "target": "`+federationCustomTarget+`"}}`)))

	cfg, err := getTemplateConfig()
	require.NoError(t, err)
	require.NotNil(t, cfg.OTelCollector.PlatformMetricsFederation.Enabled)
	assert.False(t, *cfg.OTelCollector.PlatformMetricsFederation.Enabled)
	assert.False(t, cfg.PlatformMetricsFederationEnabled())
	assert.Equal(t, federationCustomTarget, cfg.PlatformMetricsFederationTarget())
}

func TestGetTemplateConfigRejectsInvalidPlatformMetricsFederation(t *testing.T) {
	setFunctionTemplateEnv(t)
	t.Setenv("BYOO_OTEL_COLLECTOR_CONFIG_B64", base64.StdEncoding.EncodeToString(
		[]byte(`{"platformMetricsFederation": {"target": "prometheus.example.invalid"}}`)))

	_, err := getTemplateConfig()
	require.ErrorContains(t, err, "BYOO_OTEL_COLLECTOR_CONFIG_B64: validate config: platform metrics federation: target")
}

func TestTemplateConfigPlatformMetricsFederationDefaults(t *testing.T) {
	cfg := TemplateConfig{}
	assert.True(t, cfg.PlatformMetricsFederationEnabled())
	assert.Equal(t, DefaultPlatformMetricsFederationTarget, cfg.PlatformMetricsFederationTarget())
	assert.True(t, cfg.OTelCollector.IsZero())
}

func setFunctionTemplateEnv(t *testing.T) {
	t.Helper()
	for name, value := range map[string]string{
		"NVCF_BACKEND_TYPE":        "non-gfn",
		"NVCF_INSTANCE_ID":         "test-instance",
		"NVCF_NAMESPACE":           "test-ns",
		"NVCF_WORKLOAD_TYPE":       "container",
		"NVCF_FUNCTION_ID":         "func-1",
		"NVCF_FUNCTION_VERSION_ID": "ver-1",
		"NVCT_TASK_ID":             "",
	} {
		t.Setenv(name, value)
	}
}
