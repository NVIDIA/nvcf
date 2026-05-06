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
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"text/template"

	"github.com/Masterminds/semver/v3"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/otelconfig/backendconfig"
	otelconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/otelconfig/config"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	OTelContainerEnv = "BYOO_OTEL_COLLECTOR_CONTAINER"
	// Envs injected into workload containers.
	OTelExporterLogsEndpointEnv    = "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"
	OTelExporterMetricsEndpointEnv = "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"
	OTelExporterTracesEndpointEnv  = "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"
	OTelHealthCheckEndpointEnv     = "OTEL_HEALTH_CHECK_ENDPOINT"
	OTelExporterLogsProtocolEnv    = "OTEL_EXPORTER_OTLP_LOGS_PROTOCOL"
	OTelExporterMetricsProtocolEnv = "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL"
	OTelExporterTracesProtocolEnv  = "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"

	FunctionSecretsPresentEnv = "FUNCTION_SECRETS_PRESENT"
	TaskSecretsPresentEnv     = "TASK_SECRETS_PRESENT" //nolint:gosec // false positive: env var name, not credential

	FluentbitContainerEnv = "BYOO_FLUENTBIT_CONTAINER"

	// DefaultFluentbitImage is the default Fluent Bit image used when not provided by upstream
	DefaultFluentbitImage = "fluent/fluent-bit:3.2"

	protocolGRPC                = "grpc"
	otelExporterPortGRPC        = 14357
	otelExporterPortHTTP        = 14358
	otelExporterPortHealthCheck = 13133
	// monitor the third party otel-collector process, metrics will look like: "otelcol_.*"
	otelExporterPortMetrics = 18888
	// monitor the byoo-otel-collector process, metrics will look like: "byoo_.*"
	otelExporterPortBYOOMetrics = 19090
	otelExporterHTTPPathLogs    = "/v1/logs"
	otelExporterHTTPPathMetrics = "/v1/metrics"
	otelExporterHTTPPathTraces  = "/v1/traces"
	otelHealthCheckPath         = "/health"

	backendType       = "non-gfn"
	ByooVersionCutOff = "0.119.4"

	OTelCollectorBindAllAddresses = "0.0.0.0"
)

// BYOO (Bring Your Own Observability) env vars that should be overrideable by customer values
var OverrideableEnvVars = map[string]bool{
	OTelExporterLogsEndpointEnv:    true,
	OTelExporterMetricsEndpointEnv: true,
	OTelExporterTracesEndpointEnv:  true,
	OTelHealthCheckEndpointEnv:     true,
	OTelExporterLogsProtocolEnv:    true,
	OTelExporterMetricsProtocolEnv: true,
	OTelExporterTracesProtocolEnv:  true,
}

// MakeOTelEnvSet creates envs that must be added to inference container and Helm Release pods.
func MakeOTelEnvSet(
	telLaunchSpec *TelemetriesLaunchSpecification,
	svcName string,
) (envs []corev1.EnvVar) {
	ls := telLaunchSpec.Telemetries
	svcURL := fmt.Sprintf("http://%s", svcName)
	if ls.Logs != nil {
		var endpoint string
		if strings.ToLower(ls.Logs.Protocol) == protocolGRPC {
			endpoint = fmt.Sprintf("%s:%d", svcURL, otelExporterPortGRPC)
		} else {
			endpoint = fmt.Sprintf("%s:%d%s", svcURL, otelExporterPortHTTP, otelExporterHTTPPathLogs)
		}
		envs = append(envs, []corev1.EnvVar{
			{Name: OTelExporterLogsProtocolEnv, Value: strings.ToLower(ls.Logs.Protocol)},
			{Name: OTelExporterLogsEndpointEnv, Value: endpoint},
		}...)
	}
	if ls.Metrics != nil {
		var endpoint string
		if strings.ToLower(ls.Metrics.Protocol) == protocolGRPC {
			endpoint = fmt.Sprintf("%s:%d", svcURL, otelExporterPortGRPC)
		} else {
			endpoint = fmt.Sprintf("%s:%d%s", svcURL, otelExporterPortHTTP, otelExporterHTTPPathMetrics)
		}
		envs = append(envs, []corev1.EnvVar{
			{Name: OTelExporterMetricsProtocolEnv, Value: strings.ToLower(ls.Metrics.Protocol)},
			{Name: OTelExporterMetricsEndpointEnv, Value: endpoint},
		}...)
	}
	if ls.Traces != nil {
		var endpoint string
		if strings.ToLower(ls.Traces.Protocol) == protocolGRPC {
			endpoint = fmt.Sprintf("%s:%d", svcURL, otelExporterPortGRPC)
		} else {
			endpoint = fmt.Sprintf("%s:%d%s", svcURL, otelExporterPortHTTP, otelExporterHTTPPathTraces)
		}
		envs = append(envs, []corev1.EnvVar{
			{Name: OTelExporterTracesProtocolEnv, Value: strings.ToLower(ls.Traces.Protocol)},
			{Name: OTelExporterTracesEndpointEnv, Value: endpoint},
		}...)
	}
	envs = append(envs, []corev1.EnvVar{
		{Name: OTelHealthCheckEndpointEnv, Value: fmt.Sprintf("%s:%d%s", svcURL, otelExporterPortHealthCheck, otelHealthCheckPath)},
	}...)
	return envs
}

const (
	ByooOTelCollectorPodNameBase = "byoo-otel-collector"
	ByooOTelConfigCMNameBase     = "byoo-otel-collector"

	K8sAppNameLabelKey = "app.kubernetes.io/name"
	// This label k/v must be set on all pods containing the byoo-otel-collector container
	// so metrics can be forwarded to the local prometheus instance running in a separate namespace.
	BYOOMetricsEgressTargetLabelKey   = "nvca.nvcf.nvidia.io/byoo-metrics-egress-target"
	BYOOMetricsEgressTargetLabelValue = "byoo-otel-collector"

	otelAcctSecretsCMName = "accounts-secrets"
	otelConfigCMKey       = "config.yaml"
	otelConfigDir         = "/etc/otel"
	otelCollSecretsDir    = "/etc/byoo-otel-collector/secrets" //nolint:gosec // false positive: directory path, not credential

	FluentbitContainerName    = "fluentbit-logs"
	fluentbitConfigCMKey      = "fluent-bit.conf"
	fluentbitConfigDir        = "/fluent-bit/etc"
	fluentbitConfigVolumeName = "fluentbit-config"
	fluentbitVarLogVolumeName = "varlog"
	fluentbitVarLogPath       = "/var/log"
	fluentbitDBVolumeName     = "fluentbit-db"
	fluentbitDBPath           = "/fluent-bit/db"
	fluentbitParsersConfigKey = "parsers.conf"
)

var (
	//go:embed manifests/fluent-bit.conf.tmpl
	fluentbitConfigTemplate string

	//go:embed manifests/parsers.conf
	fluentbitParsersConfig string

	// fluentbitConfigTemplateParsed is the parsed template, initialized at package load time.
	// If the template is invalid, the program will fail to start.
	fluentbitConfigTemplateParsed = template.Must(template.New("fluent-bit").Parse(fluentbitConfigTemplate))
)

func NewOTelServiceHelm(name string) *corev1.Service {
	return newOTelService(name, map[string]string{
		BYOOMetricsEgressTargetLabelKey: BYOOMetricsEgressTargetLabelValue,
	})
}

func newOTelService(name string, selector map[string]string) *corev1.Service {
	otelSvc := &corev1.Service{}
	otelSvc.Name = name
	otelSvc.Spec = corev1.ServiceSpec{
		Selector: selector,
	}

	for _, port := range newOTelContainerPorts() {
		otelSvc.Spec.Ports = append(otelSvc.Spec.Ports, corev1.ServicePort{
			Name:       port.Name,
			Protocol:   port.Protocol,
			Port:       port.ContainerPort,
			TargetPort: intstr.FromInt32(port.ContainerPort),
		})
	}

	return otelSvc
}

func FuncEnvVars() []corev1.EnvVar {
	return []corev1.EnvVar{
		{
			Name: "NVCF_FUNCTION_ID",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.labels['function-id']"},
			},
		},
		{
			Name: "NVCF_FUNCTION_VERSION_ID",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.labels['function-version-id']"},
			},
		},
	}
}

func TaskEnvVars() []corev1.EnvVar {
	return []corev1.EnvVar{
		{
			Name: "NVCT_TASK_ID",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.labels['task-id']"},
			},
		},
	}
}

func getCommonOTelEnvs(allEnvSet map[string]string, otelPodIPOverride string) []corev1.EnvVar {
	envs := []corev1.EnvVar{
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
			Value: allEnvSet[CloudProviderEnv],
		},
	}

	if otelPodIPOverride == "" {
		envs = append(envs, corev1.EnvVar{
			Name: "OTEL_POD_IP",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"},
			},
		})
	} else {
		envs = append(envs, corev1.EnvVar{
			Name:  "OTEL_POD_IP",
			Value: otelPodIPOverride,
		})
	}
	return envs
}

func GetDefaultContainerResourcesBYOO() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewMilliQuantity(500, resource.DecimalSI), // 500m
			corev1.ResourceMemory: *resource.NewQuantity(1<<31, resource.BinarySI),     // 2Gi
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewMilliQuantity(500, resource.DecimalSI), // 500m
			corev1.ResourceMemory: *resource.NewQuantity(1<<31, resource.BinarySI),     // 2Gi
		},
	}
}

func getOTelContainerBase(
	otelContainerImage string,
	otelContainerVolumeMounts []corev1.VolumeMount,
	otelEnvs []corev1.EnvVar,
	resources corev1.ResourceRequirements,
) corev1.Container {
	return corev1.Container{
		Name:            ByooOTelCollectorPodNameBase,
		Image:           otelContainerImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Env:             SortEnvs(otelEnvs),
		SecurityContext: NewInfraContainerSecurityContext(),
		VolumeMounts:    otelContainerVolumeMounts,
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/health",
					Port:   intstr.FromInt32(13133),
					Scheme: corev1.URISchemeHTTP,
				},
			},
			InitialDelaySeconds: 30,
			PeriodSeconds:       30,
			FailureThreshold:    3,
			SuccessThreshold:    1,
			TimeoutSeconds:      1,
		},
		Ports:     newOTelContainerPorts(),
		Resources: resources,
	}
}

// NewOTelContainer creates a BYOO OTel collector container.
func NewOTelContainer(
	allEnvSet map[string]string,
	workloadType string,
	clusterRegion string,
	instanceID string,
	telemetries string,
	namespace string,
	addtlOtelEnvs []corev1.EnvVar,
	oTelPodIPBindAddressOverride string,
	resources corev1.ResourceRequirements,
) corev1.Container {
	otelContainerImage := allEnvSet[OTelContainerEnv]
	byoOtelConfig := OTelVersion(otelContainerImage)
	otelContainerVolumeMounts := []corev1.VolumeMount{
		{
			Name:      otelConfigVolumeName,
			MountPath: otelConfigDir,
		},
		{
			Name:      otelCollSecretsVolumeName,
			MountPath: otelCollSecretsDir,
		},
		{
			Name:      EssDataVolumeName,
			MountPath: EssConfigDir,
		},
		{
			Name:      EssSecretDataVolumeName,
			MountPath: EssSecretsDir,
			ReadOnly:  true,
		},
	}

	var otelEnvs []corev1.EnvVar
	if byoOtelConfig == "v2" {
		otelEnvs = append(getCommonOTelEnvs(allEnvSet, oTelPodIPBindAddressOverride), []corev1.EnvVar{
			{
				Name:  "NVCF_INSTANCE_ID",
				Value: instanceID,
			},
			{
				Name:  "NVCF_WORKLOAD_TYPE",
				Value: workloadType,
			},
			{
				Name:  "NVCF_BACKEND_TYPE",
				Value: backendType,
			},
			{
				Name: "NVCF_NAMESPACE",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
				},
			},
			{
				Name:  "NVCF_CLUSTER_REGION",
				Value: clusterRegion,
			},
			{
				Name:  "OTEL_TRACING_ACCESS_TOKEN",
				Value: allEnvSet["TRACING_ACCESS_TOKEN"],
			},
			{
				Name:  "OTEL_EXPORTER_OTLP_ENDPOINT",
				Value: allEnvSet["OTEL_EXPORTER_OTLP_ENDPOINT"],
			},
		}...)
	} else {
		otelEnvs = append(getCommonOTelEnvs(allEnvSet, oTelPodIPBindAddressOverride), []corev1.EnvVar{
			{
				Name:  "INSTANCE_ID",
				Value: instanceID,
			},
			{
				Name:  "OTEL_TRACING_ACCESS_TOKEN",
				Value: allEnvSet["TRACING_ACCESS_TOKEN"],
			},
			{
				Name:  "OTEL_EXPORTER_OTLP_ENDPOINT",
				Value: allEnvSet["OTEL_EXPORTER_OTLP_ENDPOINT"],
			},
		}...)
	}
	otelEnvs = append(otelEnvs, addtlOtelEnvs...)

	otelContainer := getOTelContainerBase(otelContainerImage, otelContainerVolumeMounts, otelEnvs, resources)

	if byoOtelConfig == "v2" {
		otelContainer.Args = []string{
			"--byoo-accounts-secrets", EssAccountSecretsDest,
			"--byoo-secrets-folder", otelCollSecretsDir,
			"--telemetries",
			telemetries,
			"--otel-config-path", path.Join(otelConfigDir, otelConfigCMKey),
		}
	} else {
		otelContainer.Args = []string{
			"--byoo-accounts-secrets", EssAccountSecretsDest,
			"--byoo-secrets-folder", otelCollSecretsDir,
			"--",
			"--config", path.Join(otelConfigDir, otelConfigCMKey),
		}
	}

	return otelContainer
}

func newOTelContainerPorts() []corev1.ContainerPort {
	return []corev1.ContainerPort{
		{ContainerPort: otelExporterPortGRPC, Name: "otlp-grpc", Protocol: corev1.ProtocolTCP},
		{ContainerPort: otelExporterPortHTTP, Name: "otlp-http", Protocol: corev1.ProtocolTCP},
		{ContainerPort: otelExporterPortMetrics, Name: "metrics", Protocol: corev1.ProtocolTCP},
		{ContainerPort: otelExporterPortHealthCheck, Name: "health", Protocol: corev1.ProtocolTCP},
		{ContainerPort: otelExporterPortBYOOMetrics, Name: "byoo-metrics", Protocol: corev1.ProtocolTCP},
	}
}

func NewOTelConfigMap(
	telLaunchSpec *TelemetriesLaunchSpecification,
	tmplConfig backendconfig.TemplateConfig,
	name string,
) (*corev1.ConfigMap, error) {
	otelConfigCM := &corev1.ConfigMap{}
	otelConfigCM.Name = name

	var logs, metrics, traces *otelconfig.Telemetry
	if telLaunchSpec.Telemetries.Logs != nil {
		logs = &otelconfig.Telemetry{
			Protocol: otelconfig.Protocol(telLaunchSpec.Telemetries.Logs.Protocol),
			Provider: otelconfig.Provider(telLaunchSpec.Telemetries.Logs.Provider),
			Endpoint: telLaunchSpec.Telemetries.Logs.Endpoint,
			Name:     telLaunchSpec.Telemetries.Logs.Name,
		}
	}
	if telLaunchSpec.Telemetries.Metrics != nil {
		metrics = &otelconfig.Telemetry{
			Protocol: otelconfig.Protocol(telLaunchSpec.Telemetries.Metrics.Protocol),
			Provider: otelconfig.Provider(telLaunchSpec.Telemetries.Metrics.Provider),
			Endpoint: telLaunchSpec.Telemetries.Metrics.Endpoint,
			Name:     telLaunchSpec.Telemetries.Metrics.Name,
		}
	}
	if telLaunchSpec.Telemetries.Traces != nil {
		traces = &otelconfig.Telemetry{
			Protocol: otelconfig.Protocol(telLaunchSpec.Telemetries.Traces.Protocol),
			Provider: otelconfig.Provider(telLaunchSpec.Telemetries.Traces.Provider),
			Endpoint: telLaunchSpec.Telemetries.Traces.Endpoint,
			Name:     telLaunchSpec.Telemetries.Traces.Name,
		}
	}

	telSpec := otelconfig.TelemetryConfig{
		Telemetries: struct {
			Logs    *otelconfig.Telemetry `json:"logsTelemetry,omitempty"`
			Metrics *otelconfig.Telemetry `json:"metricsTelemetry,omitempty"`
			Traces  *otelconfig.Telemetry `json:"tracesTelemetry,omitempty"`
		}{
			Logs:    logs,
			Metrics: metrics,
			Traces:  traces,
		},
	}
	otelConfigData, err := otelconfig.RenderOtelConfig(telSpec, tmplConfig)
	if err != nil {
		return nil, fmt.Errorf("render otel config data: %v", err)
	}
	otelConfigCM.Data = map[string]string{otelConfigCMKey: string(otelConfigData)}
	return otelConfigCM, nil
}

const (
	otelConfigVolumeName      = "otel-config-data"
	otelCollSecretsVolumeName = "otel-collector-secret-data" //nolint:gosec // false positive: volume name, not credential
)

func OTelVersion(otelContainerImage string) string {
	byoOtelConfig := "v1"
	versionStr := otelContainerImage[strings.LastIndex(otelContainerImage, ":")+1:]
	currentOtelCollectorContainerVersion, err := semver.NewVersion(versionStr)
	if err != nil {
		fmt.Printf("failed to parse otel collector container version %q. Configuring v1 otel collector config\n", versionStr)
	} else {
		cutoffVersion := semver.MustParse(ByooVersionCutOff)
		if currentOtelCollectorContainerVersion.GreaterThan(cutoffVersion) {
			byoOtelConfig = "v2"
		}
	}

	return byoOtelConfig
}

func getOTelVolumesBase() []corev1.Volume {
	return []corev1.Volume{
		{
			Name: otelCollSecretsVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}
}

func NewOTelVolumesDeprecated(otelConfigCMName string) []corev1.Volume {
	volumes := getOTelVolumesBase()

	configVolume := corev1.Volume{
		Name: otelConfigVolumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: otelConfigCMName,
				},
				Optional: new(bool),
			},
		},
	}

	volumes = append([]corev1.Volume{configVolume}, volumes...)
	return volumes
}

func NewOTelVolumes(otelConfigCMName string) []corev1.Volume {
	volumes := getOTelVolumesBase()

	configVolume := corev1.Volume{
		Name: otelConfigVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}

	volumes = append([]corev1.Volume{configVolume}, volumes...)
	return volumes
}

// GetDefaultContainerResourcesFluentbit returns the default resource requirements for the fluentbit container.
func GetDefaultContainerResourcesFluentbit() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewMilliQuantity(100, resource.DecimalSI), // 100m
			corev1.ResourceMemory: *resource.NewQuantity(128<<20, resource.BinarySI),   // 128Mi
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewMilliQuantity(200, resource.DecimalSI), // 200m
			corev1.ResourceMemory: *resource.NewQuantity(256<<20, resource.BinarySI),   // 256Mi
		},
	}
}

// NewFluentbitConfigMap creates a ConfigMap with Fluent Bit configuration for collecting container logs.
func NewFluentbitConfigMap(name string, podName string, workloadType string) *corev1.ConfigMap {
	// Fluent Bit's OpenTelemetry output plugin uses HTTP protocol, so we must use port 14358 (HTTP)
	// Use specific pod name in path to minimize file watches and prevent "too many open files" error

	var logPath string
	var excludePath string

	if workloadType == WorkloadTypeHelm {
		// For Helm chart deployments, Fluent Bit runs in utils pod but needs to collect logs from
		// all application pods in the namespace (names are unpredictable).
		// Exclude infrastructure pods: utils, SMB server, OTEL collector, and Fluent Bit itself.
		logPath = "/var/log/containers/*_${POD_NAMESPACE}_*.log"
		excludePath = "*.gz,*.zip,*.bz2,*_utils_*,*_nvcf-smb-server_*,*_byoo-otel-collector_*,*_fluentbit-logs_*"
	} else {
		// For container deployments, use specific pod name and container name patterns
		logPath = fmt.Sprintf("/var/log/containers/%s_${POD_NAMESPACE}_inference-*.log,/var/log/containers/%s_${POD_NAMESPACE}_task-*.log", podName, podName)
		excludePath = "*.gz,*.zip,*.bz2"
	}

	var fluentbitConfigBuf bytes.Buffer
	if err := fluentbitConfigTemplateParsed.Execute(&fluentbitConfigBuf, map[string]interface{}{
		"LogPath":     logPath,
		"ExcludePath": excludePath,
		"Port":        otelExporterPortHTTP,
	}); err != nil {
		// This should never happen with a valid template, but handle it gracefully
		panic(fmt.Sprintf("failed to execute fluent-bit config template: %v", err))
	}
	fluentbitConfig := fluentbitConfigBuf.String()

	cm := &corev1.ConfigMap{}
	cm.Name = name
	cm.Data = map[string]string{
		fluentbitConfigCMKey:      fluentbitConfig,
		fluentbitParsersConfigKey: fluentbitParsersConfig,
	}
	return cm
}

// NewFluentbitContainer creates a Fluent Bit sidecar container for collecting customer logs.
func NewFluentbitContainer(fluentbitImage string, podName string, podNamespace string, resources corev1.ResourceRequirements) corev1.Container {
	return corev1.Container{
		Name:            FluentbitContainerName,
		Image:           fluentbitImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Env: []corev1.EnvVar{
			{
				Name: "POD_NAME",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
				},
			},
			{
				Name: "POD_NAMESPACE",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
				},
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      fluentbitConfigVolumeName,
				MountPath: fluentbitConfigDir,
				ReadOnly:  true,
			},
			{
				Name:      fluentbitVarLogVolumeName,
				MountPath: fluentbitVarLogPath,
				ReadOnly:  true,
			},
			{
				Name:      fluentbitDBVolumeName,
				MountPath: fluentbitDBPath,
				ReadOnly:  false,
			},
		},
		Resources:       resources,
		SecurityContext: NewFluentbitSecurityContext(),
		Args: []string{
			"-c",
			path.Join(fluentbitConfigDir, fluentbitConfigCMKey),
		},
	}
}

// NewFluentbitSecurityContext creates a security context for the Fluent Bit container.
func NewFluentbitSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
	}
}

// NewFluentbitVolumes creates the volumes needed for the Fluent Bit container.
func NewFluentbitVolumes(fluentbitConfigCMName string) []corev1.Volume {
	return []corev1.Volume{
		{
			Name: fluentbitConfigVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: fluentbitConfigCMName,
					},
				},
			},
		},
		{
			Name: fluentbitVarLogVolumeName,
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: fluentbitVarLogPath,
				},
			},
		},
		{
			Name: fluentbitDBVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}
}

// SetupPodTelemetry configures BYOO telemetry for a pod with OTEL collector.
// Use SetupFluentBit separately to add Fluent Bit log collection.
func SetupPodTelemetry(
	pod *corev1.Pod,
	telLaunchSpec *TelemetriesLaunchSpecification,
	allEnvSet map[string]string,
	workloadType string,
	clusterRegion string,
	instanceID string,
	namespace string,
	addtlOtelEnvs []corev1.EnvVar,
	appName string,
	otelConfigCMName string,
	oTelPodIPBindAddressOverride string,
	otelResources corev1.ResourceRequirements,
) error {
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}

	if appName != "" {
		pod.Labels[K8sAppNameLabelKey] = appName
	}
	pod.Labels[BYOOMetricsEgressTargetLabelKey] = BYOOMetricsEgressTargetLabelValue

	telStr, err := json.Marshal(telLaunchSpec)
	if err != nil {
		return fmt.Errorf("marshal telemetries: %v", err)
	}
	telB64 := base64.StdEncoding.EncodeToString(telStr)

	pod.Spec.Containers = append(
		pod.Spec.Containers,
		NewOTelContainer(
			allEnvSet, workloadType, clusterRegion, instanceID, telB64, namespace,
			addtlOtelEnvs, oTelPodIPBindAddressOverride, otelResources,
		),
	)
	byoOtelConfig := OTelVersion(allEnvSet[OTelContainerEnv])
	if byoOtelConfig == "v2" {
		pod.Spec.Volumes = append(pod.Spec.Volumes, NewOTelVolumes(otelConfigCMName)...)
	} else {
		pod.Spec.Volumes = append(pod.Spec.Volumes, NewOTelVolumesDeprecated(otelConfigCMName)...)
	}

	return nil
}

// SetupFluentBit configures Fluent Bit sidecar container for collecting customer container logs.
// This should be called after SetupPodTelemetry succeeds.
// Returns the Fluent Bit ConfigMap if Fluent Bit is enabled, nil otherwise.
func SetupFluentBit(
	pod *corev1.Pod,
	telLaunchSpec *TelemetriesLaunchSpecification,
	allEnvSet map[string]string,
	workloadType string,
	namespace string,
	fluentbitEnabled bool,
	fluentbitResources corev1.ResourceRequirements,
) *corev1.ConfigMap {
	// Add Fluent Bit container for collecting customer container logs when:
	// 1. Feature flag is enabled
	// 2. Logs telemetry is configured
	if !fluentbitEnabled || telLaunchSpec.Telemetries.Logs == nil {
		return nil
	}

	// TODO: Remove the default image fallback once callers reliably provide
	// BYOO_FLUENTBIT_CONTAINER in the EnvironmentB64 field for all BYOO requests.
	fluentbitImage := allEnvSet[FluentbitContainerEnv]
	if fluentbitImage == "" {
		fluentbitImage = DefaultFluentbitImage
	}

	fluentbitConfigCMName := fmt.Sprintf("%s-fluentbit-config", pod.Name) // passing pod name reduces file watches
	fluentbitCM := NewFluentbitConfigMap(fluentbitConfigCMName, pod.Name, workloadType)
	fluentbitContainer := NewFluentbitContainer(fluentbitImage, pod.Name, namespace, fluentbitResources)
	pod.Spec.Containers = append(pod.Spec.Containers, fluentbitContainer)
	pod.Spec.Volumes = append(pod.Spec.Volumes, NewFluentbitVolumes(fluentbitConfigCMName)...)

	return fluentbitCM
}
