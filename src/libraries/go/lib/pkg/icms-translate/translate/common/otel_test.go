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

package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestMakeOTelEnvSet(t *testing.T) {
	type spec struct {
		name    string
		telLS   TelemetriesLaunchSpecification
		expEnvs []corev1.EnvVar
	}
	svcName := "123-svcname"

	cases := []spec{
		{
			name: "http",
			telLS: newTelemetryLaunchSpec(
				&Telemetry{
					Protocol: "http",
					Endpoint: "remotename:1234/otlp",
					Provider: "GRAFANA_CLOUD",
					Name:     "telemetry-foo",
				},
				&Telemetry{
					Protocol: "http",
					Endpoint: "remotename:1234/otlp",
					Provider: "GRAFANA_CLOUD",
					Name:     "telemetry-baz",
				},
				&Telemetry{
					Protocol: "http",
					Endpoint: "remotename:1234/otlp",
					Provider: "GRAFANA_CLOUD",
					Name:     "telemetry-bar",
				},
			),
			expEnvs: []corev1.EnvVar{
				{
					Name:  "OTEL_EXPORTER_OTLP_LOGS_PROTOCOL",
					Value: "http",
				},
				{
					Name:  "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT",
					Value: "http://123-svcname:14358/v1/logs",
				},
				{
					Name:  "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL",
					Value: "http",
				},
				{
					Name:  "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
					Value: "http://123-svcname:14358/v1/metrics",
				},
				{
					Name:  "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL",
					Value: "http",
				},
				{
					Name:  "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
					Value: "http://123-svcname:14358/v1/traces",
				},
				{
					Name:  "OTEL_HEALTH_CHECK_ENDPOINT",
					Value: "http://123-svcname:13133/health",
				},
			},
		},
		{
			name: "grpc",
			telLS: newTelemetryLaunchSpec(
				&Telemetry{
					Protocol: "grpc",
					Endpoint: "remotename:1234/otlp",
					Provider: "GRAFANA_CLOUD",
					Name:     "telemetry-foo",
				},
				&Telemetry{
					Protocol: "grpc",
					Endpoint: "remotename:1234/otlp",
					Provider: "GRAFANA_CLOUD",
					Name:     "telemetry-baz",
				},
				&Telemetry{
					Protocol: "grpc",
					Endpoint: "remotename:1234/otlp",
					Provider: "GRAFANA_CLOUD",
					Name:     "telemetry-bar",
				},
			),
			expEnvs: []corev1.EnvVar{
				{
					Name:  "OTEL_EXPORTER_OTLP_LOGS_PROTOCOL",
					Value: "grpc",
				},
				{
					Name:  "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT",
					Value: "http://123-svcname:14357",
				},
				{
					Name:  "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL",
					Value: "grpc",
				},
				{
					Name:  "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
					Value: "http://123-svcname:14357",
				},
				{
					Name:  "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL",
					Value: "grpc",
				},
				{
					Name:  "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
					Value: "http://123-svcname:14357",
				},
				{
					Name:  "OTEL_HEALTH_CHECK_ENDPOINT",
					Value: "http://123-svcname:13133/health",
				},
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			gotEnvs := MakeOTelEnvSet(&tt.telLS, svcName)
			assert.Equal(t, tt.expEnvs, gotEnvs)
		})
	}
}

func newTelemetryLaunchSpec(logs, metrics, traces *Telemetry) TelemetriesLaunchSpecification {
	tc := TelemetriesLaunchSpecification{}
	tc.Telemetries.Logs = logs
	tc.Telemetries.Metrics = metrics
	tc.Telemetries.Traces = traces
	return tc
}

func TestNewOTelContainer(t *testing.T) {
	type testCase struct {
		name              string
		allEnvSet         map[string]string
		workloadType      string
		clusterRegion     string
		instanceID        string
		telemetries       string
		namespace         string
		addtlOtelEnvs     []corev1.EnvVar
		otelPodIPOverride string
		expectedArgs      []string
		expectedEnvs      []corev1.EnvVar
		expectedPorts     []corev1.ContainerPort
		expectedMounts    []corev1.VolumeMount
	}

	cases := []testCase{
		{
			name: "v2 configuration - default pod IP",
			allEnvSet: map[string]string{
				OTelContainerEnv:              "nvcr.io/qtfpt1h0bieu/nvcf-core/byoo-otel-collector:1.2.3",
				CloudProviderEnv:              "DGXCLOUD",
				"TRACING_ACCESS_TOKEN":        "test-trace-token-v2",
				"OTEL_EXPORTER_OTLP_ENDPOINT": "http://test-otel-endpoint-v2:4317",
			},
			workloadType:  WorkloadTypeHelm,
			clusterRegion: "us-west",
			instanceID:    "test-instance",
			telemetries:   "test-telemetries",
			namespace:     "test-ns",
			addtlOtelEnvs: []corev1.EnvVar{
				{Name: "EXTRA_ENV", Value: "extra-value"},
			},
			otelPodIPOverride: "",
			expectedArgs: []string{
				"--byoo-accounts-secrets",
				"/var/secrets/accounts-secrets.json",
				"--byoo-secrets-folder",
				"/etc/byoo-otel-collector/secrets",
				"--telemetries",
				"test-telemetries",
				"--otel-config-path",
				"/etc/otel/config.yaml",
			},
			expectedEnvs: []corev1.EnvVar{
				{
					Name: "K8S_NODE_NAME",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"},
					},
				},
				{
					Name: NCAIDEncodedEnvKey,
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.labels['nca-id']"},
					},
				},
				{
					Name:  CloudProviderEnv,
					Value: "DGXCLOUD",
				},
				{
					Name: "OTEL_POD_IP",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"},
					},
				},
				{
					Name:  "NVCF_INSTANCE_ID",
					Value: "test-instance",
				},
				{
					Name:  "NVCF_WORKLOAD_TYPE",
					Value: WorkloadTypeHelm,
				},
				{
					Name:  "NVCF_BACKEND_TYPE",
					Value: "non-gfn",
				},
				{
					Name: "NVCF_NAMESPACE",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
					},
				},
				{
					Name:  "NVCF_CLUSTER_REGION",
					Value: "us-west",
				},
				{
					Name:  "OTEL_TRACING_ACCESS_TOKEN",
					Value: "test-trace-token-v2",
				},
				{
					Name:  "OTEL_EXPORTER_OTLP_ENDPOINT",
					Value: "http://test-otel-endpoint-v2:4317",
				},
				{
					Name:  "EXTRA_ENV",
					Value: "extra-value",
				},
			},
			expectedPorts: []corev1.ContainerPort{
				{ContainerPort: 18888, Name: "metrics", Protocol: corev1.ProtocolTCP},
				{ContainerPort: 14357, Name: "otlp-grpc", Protocol: corev1.ProtocolTCP},
				{ContainerPort: 14358, Name: "otlp-http", Protocol: corev1.ProtocolTCP},
				{ContainerPort: 13133, Name: "health", Protocol: corev1.ProtocolTCP},
				{ContainerPort: 19090, Name: "byoo-metrics", Protocol: corev1.ProtocolTCP},
			},
			expectedMounts: []corev1.VolumeMount{
				{
					Name:      "otel-config-data",
					MountPath: "/etc/otel",
				},
				{
					Name:      "otel-collector-secret-data",
					MountPath: "/etc/byoo-otel-collector/secrets",
				},
				{
					Name:      "ess-data",
					MountPath: "/config/ess-agent",
				},
				{
					Name:      "secret-data",
					MountPath: "/var/secrets",
					ReadOnly:  true,
				},
			},
		},
		{
			name: "v2 configuration - override pod IP",
			allEnvSet: map[string]string{
				OTelContainerEnv:              "nvcr.io/qtfpt1h0bieu/nvcf-core/byoo-otel-collector:1.2.3",
				CloudProviderEnv:              "DGXCLOUD",
				"TRACING_ACCESS_TOKEN":        "test-trace-token-v2",
				"OTEL_EXPORTER_OTLP_ENDPOINT": "http://test-otel-endpoint-v2:4317",
			},
			workloadType:  WorkloadTypeHelm,
			clusterRegion: "us-west",
			instanceID:    "test-instance-override",
			telemetries:   "test-telemetries-override",
			namespace:     "test-ns-override",
			addtlOtelEnvs: []corev1.EnvVar{
				{Name: "EXTRA_ENV_OVERRIDE", Value: "extra-value-override"},
			},
			otelPodIPOverride: "1.2.3.4",
			expectedArgs: []string{
				"--byoo-accounts-secrets",
				"/var/secrets/accounts-secrets.json",
				"--byoo-secrets-folder",
				"/etc/byoo-otel-collector/secrets",
				"--telemetries",
				"test-telemetries-override",
				"--otel-config-path",
				"/etc/otel/config.yaml",
			},
			expectedEnvs: []corev1.EnvVar{
				{
					Name: "K8S_NODE_NAME",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"},
					},
				},
				{
					Name: NCAIDEncodedEnvKey,
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.labels['nca-id']"},
					},
				},
				{
					Name:  CloudProviderEnv,
					Value: "DGXCLOUD",
				},
				{
					Name:  "OTEL_POD_IP",
					Value: "1.2.3.4",
				},
				{
					Name:  "NVCF_INSTANCE_ID",
					Value: "test-instance-override",
				},
				{
					Name:  "NVCF_WORKLOAD_TYPE",
					Value: WorkloadTypeHelm,
				},
				{
					Name:  "NVCF_BACKEND_TYPE",
					Value: "non-gfn",
				},
				{
					Name: "NVCF_NAMESPACE",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
					},
				},
				{
					Name:  "NVCF_CLUSTER_REGION",
					Value: "us-west",
				},
				{
					Name:  "OTEL_TRACING_ACCESS_TOKEN",
					Value: "test-trace-token-v2",
				},
				{
					Name:  "OTEL_EXPORTER_OTLP_ENDPOINT",
					Value: "http://test-otel-endpoint-v2:4317",
				},
				{
					Name:  "EXTRA_ENV_OVERRIDE",
					Value: "extra-value-override",
				},
			},
			expectedPorts: []corev1.ContainerPort{
				{ContainerPort: 18888, Name: "metrics", Protocol: corev1.ProtocolTCP},
				{ContainerPort: 14357, Name: "otlp-grpc", Protocol: corev1.ProtocolTCP},
				{ContainerPort: 14358, Name: "otlp-http", Protocol: corev1.ProtocolTCP},
				{ContainerPort: 13133, Name: "health", Protocol: corev1.ProtocolTCP},
				{ContainerPort: 19090, Name: "byoo-metrics", Protocol: corev1.ProtocolTCP},
			},
			expectedMounts: []corev1.VolumeMount{
				{
					Name:      "otel-config-data",
					MountPath: "/etc/otel",
				},
				{
					Name:      "otel-collector-secret-data",
					MountPath: "/etc/byoo-otel-collector/secrets",
				},
				{
					Name:      "ess-data",
					MountPath: "/config/ess-agent",
				},
				{
					Name:      "secret-data",
					MountPath: "/var/secrets",
					ReadOnly:  true,
				},
			},
		},
		{
			name: "non-v2 configuration - default pod IP",
			allEnvSet: map[string]string{
				OTelContainerEnv:              "nvcr.io/qtfpt1h0bieu/nvcf-core/byoo-otel-collector:0.119.3",
				CloudProviderEnv:              "DGXCLOUD",
				"TRACING_ACCESS_TOKEN":        "test-trace-token-v1",
				"OTEL_EXPORTER_OTLP_ENDPOINT": "http://test-otel-endpoint-v1:4317",
			},
			workloadType:  WorkloadTypeHelm,
			clusterRegion: "us-west",
			instanceID:    "test-instance",
			telemetries:   "test-telemetries",
			namespace:     "test-ns",
			addtlOtelEnvs: []corev1.EnvVar{
				{Name: "EXTRA_ENV", Value: "extra-value"},
			},
			otelPodIPOverride: "",
			expectedArgs: []string{
				"--byoo-accounts-secrets",
				"/var/secrets/accounts-secrets.json",
				"--byoo-secrets-folder",
				"/etc/byoo-otel-collector/secrets",
				"--",
				"--config",
				"/etc/otel/config.yaml",
			},
			expectedEnvs: []corev1.EnvVar{
				{
					Name: "K8S_NODE_NAME",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{FieldPath: "spec.nodeName"},
					},
				},
				{
					Name: NCAIDEncodedEnvKey,
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.labels['nca-id']"},
					},
				},
				{
					Name:  CloudProviderEnv,
					Value: "DGXCLOUD",
				},
				{
					Name: "OTEL_POD_IP",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"},
					},
				},
				{
					Name:  "INSTANCE_ID",
					Value: "test-instance",
				},
				{
					Name:  "OTEL_TRACING_ACCESS_TOKEN",
					Value: "test-trace-token-v1",
				},
				{
					Name:  "OTEL_EXPORTER_OTLP_ENDPOINT",
					Value: "http://test-otel-endpoint-v1:4317",
				},
				{
					Name:  "EXTRA_ENV",
					Value: "extra-value",
				},
			},
			expectedPorts: []corev1.ContainerPort{
				{ContainerPort: 18888, Name: "metrics", Protocol: corev1.ProtocolTCP},
				{ContainerPort: 14357, Name: "otlp-grpc", Protocol: corev1.ProtocolTCP},
				{ContainerPort: 14358, Name: "otlp-http", Protocol: corev1.ProtocolTCP},
				{ContainerPort: 13133, Name: "health", Protocol: corev1.ProtocolTCP},
				{ContainerPort: 19090, Name: "byoo-metrics", Protocol: corev1.ProtocolTCP},
			},
			expectedMounts: []corev1.VolumeMount{
				{
					Name:      "otel-config-data",
					MountPath: "/etc/otel",
				},
				{
					Name:      "otel-collector-secret-data",
					MountPath: "/etc/byoo-otel-collector/secrets",
				},
				{
					Name:      "ess-data",
					MountPath: "/config/ess-agent",
				},
				{
					Name:      "secret-data",
					MountPath: "/var/secrets",
					ReadOnly:  true,
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			container := NewOTelContainer(tc.allEnvSet, tc.workloadType, tc.clusterRegion, tc.instanceID, tc.telemetries, tc.namespace, tc.addtlOtelEnvs, tc.otelPodIPOverride, GetDefaultContainerResourcesBYOO())

			assert.Equal(t, ByooOTelCollectorPodNameBase, container.Name)
			assert.Equal(t, tc.allEnvSet[OTelContainerEnv], container.Image)
			assert.Equal(t, corev1.PullIfNotPresent, container.ImagePullPolicy)
			assert.Equal(t, tc.expectedArgs, container.Args)

			// Sort expectedEnvs to ensure order doesn't affect the comparison
			tc.expectedEnvs = SortEnvs(tc.expectedEnvs)
			assert.ElementsMatch(t, tc.expectedEnvs, container.Env)
			assert.ElementsMatch(t, tc.expectedPorts, container.Ports)
			assert.ElementsMatch(t, tc.expectedMounts, container.VolumeMounts)

			assert.NotNil(t, container.SecurityContext)
			assert.Equal(t, []corev1.Capability{"NET_RAW"}, container.SecurityContext.Capabilities.Drop)

			assert.NotNil(t, container.Resources)
			assert.Equal(t, "500m", container.Resources.Limits.Cpu().String())
			assert.Equal(t, "2Gi", container.Resources.Limits.Memory().String())
			assert.Equal(t, "500m", container.Resources.Requests.Cpu().String())
			assert.Equal(t, "2Gi", container.Resources.Requests.Memory().String())

			assert.NotNil(t, container.ReadinessProbe)
			assert.Equal(t, int32(30), container.ReadinessProbe.InitialDelaySeconds)
			assert.Equal(t, int32(30), container.ReadinessProbe.PeriodSeconds)
			assert.Equal(t, int32(1), container.ReadinessProbe.TimeoutSeconds)
			assert.Equal(t, int32(3), container.ReadinessProbe.FailureThreshold)
			assert.Equal(t, int32(1), container.ReadinessProbe.SuccessThreshold)
			assert.Equal(t, "/health", container.ReadinessProbe.HTTPGet.Path)
			assert.Equal(t, intstr.FromInt32(13133), container.ReadinessProbe.HTTPGet.Port)
			assert.Equal(t, corev1.URISchemeHTTP, container.ReadinessProbe.HTTPGet.Scheme)
		})
	}
}
