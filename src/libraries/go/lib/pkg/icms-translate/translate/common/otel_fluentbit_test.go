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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNewFluentbitConfigMap(t *testing.T) {
	cmName := "test-fluentbit-config"
	podName := "test-pod"

	cm := NewFluentbitConfigMap(cmName, podName, WorkloadTypeContainer)

	require.NotNil(t, cm)
	assert.Equal(t, cmName, cm.Name)
	assert.Contains(t, cm.Data, fluentbitConfigCMKey)
	assert.Contains(t, cm.Data, fluentbitParsersConfigKey)

	config := cm.Data[fluentbitConfigCMKey]
	assert.Contains(t, config, "[SERVICE]")
	assert.Contains(t, config, "[INPUT]")
	assert.Contains(t, config, "[FILTER]")
	assert.Contains(t, config, "[OUTPUT]")

	// Verify it uses specific pod name in path to reduce inotify watches
	assert.Contains(t, config, "/var/log/containers/"+podName+"_")

	// Verify it uses HTTP/OpenTelemetry output (port 14358, not 14357 gRPC)
	assert.Contains(t, config, "Name  opentelemetry")
	assert.Contains(t, config, "logs_uri /v1/logs")
	assert.Contains(t, config, "Host  localhost")
	assert.Contains(t, config, "Port  14358", "Should use HTTP port 14358, not gRPC port 14357")

	// Check parsers config
	parsers := cm.Data[fluentbitParsersConfigKey]
	assert.Contains(t, parsers, "[PARSER]")
	assert.Contains(t, parsers, "Name        docker")
	assert.Contains(t, parsers, "Name        cri")
}

func TestNewFluentbitContainer(t *testing.T) {
	fluentbitImage := "fluent/fluent-bit:latest"
	podName := "test-pod"
	podNamespace := "test-namespace"
	resources := GetDefaultContainerResourcesFluentbit()

	container := NewFluentbitContainer(fluentbitImage, podName, podNamespace, resources)

	assert.Equal(t, FluentbitContainerName, container.Name)
	assert.Equal(t, fluentbitImage, container.Image)
	assert.Equal(t, corev1.PullIfNotPresent, container.ImagePullPolicy)

	// Check environment variables
	require.Len(t, container.Env, 2)
	assert.Equal(t, "POD_NAME", container.Env[0].Name)
	require.NotNil(t, container.Env[0].ValueFrom)
	require.NotNil(t, container.Env[0].ValueFrom.FieldRef)
	assert.Equal(t, "metadata.name", container.Env[0].ValueFrom.FieldRef.FieldPath)
	assert.Equal(t, "POD_NAMESPACE", container.Env[1].Name)
	require.NotNil(t, container.Env[1].ValueFrom)
	require.NotNil(t, container.Env[1].ValueFrom.FieldRef)
	assert.Equal(t, "metadata.namespace", container.Env[1].ValueFrom.FieldRef.FieldPath)

	// Check volume mounts
	require.Len(t, container.VolumeMounts, 3)
	assert.Equal(t, fluentbitConfigVolumeName, container.VolumeMounts[0].Name)
	assert.Equal(t, fluentbitConfigDir, container.VolumeMounts[0].MountPath)
	assert.True(t, container.VolumeMounts[0].ReadOnly)
	assert.Equal(t, fluentbitVarLogVolumeName, container.VolumeMounts[1].Name)
	assert.Equal(t, fluentbitVarLogPath, container.VolumeMounts[1].MountPath)
	assert.True(t, container.VolumeMounts[1].ReadOnly)
	assert.Equal(t, fluentbitDBVolumeName, container.VolumeMounts[2].Name)
	assert.Equal(t, fluentbitDBPath, container.VolumeMounts[2].MountPath)
	assert.False(t, container.VolumeMounts[2].ReadOnly)

	// Check args
	require.Len(t, container.Args, 2)
	assert.Equal(t, "-c", container.Args[0])
	assert.Contains(t, container.Args[1], fluentbitConfigCMKey)
}

func TestNewFluentbitVolumes(t *testing.T) {
	configMapName := "test-fluentbit-config"
	volumes := NewFluentbitVolumes(configMapName)

	require.Len(t, volumes, 3)

	// Check config volume
	assert.Equal(t, fluentbitConfigVolumeName, volumes[0].Name)
	require.NotNil(t, volumes[0].VolumeSource.ConfigMap)
	assert.Equal(t, configMapName, volumes[0].VolumeSource.ConfigMap.LocalObjectReference.Name)

	// Check varlog volume
	assert.Equal(t, fluentbitVarLogVolumeName, volumes[1].Name)
	require.NotNil(t, volumes[1].VolumeSource.HostPath)
	assert.Equal(t, fluentbitVarLogPath, volumes[1].VolumeSource.HostPath.Path)

	// Check DB volume
	assert.Equal(t, fluentbitDBVolumeName, volumes[2].Name)
	require.NotNil(t, volumes[2].VolumeSource.EmptyDir)
}

func TestSetupPodTelemetry_WithFluentbit(t *testing.T) {
	// Create a test pod
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Spec: corev1.PodSpec{},
	}

	// Create telemetry launch spec with logs
	telLaunchSpec := &TelemetriesLaunchSpecification{
		Telemetries: struct {
			Logs    *Telemetry `json:"logsTelemetry,omitempty"`
			Metrics *Telemetry `json:"metricsTelemetry,omitempty"`
			Traces  *Telemetry `json:"tracesTelemetry,omitempty"`
		}{
			Logs: &Telemetry{
				Protocol: "http",
				Provider: "SPLUNK",
				Endpoint: "https://splunk.example.com",
				Name:     "splunk-logs",
			},
		},
	}

	// Create environment set with required variables
	allEnvSet := map[string]string{
		OTelContainerEnv:      "nvcr.io/test/otel-collector:1.0.0",
		FluentbitContainerEnv: "fluent/fluent-bit:latest",
		CloudProviderEnv:      "DGXCLOUD",
	}

	// Call SetupPodTelemetry
	err := SetupPodTelemetry(
		pod,
		telLaunchSpec,
		allEnvSet,
		WorkloadTypeContainer,
		"us-west-2",
		"test-instance",
		"test-namespace",
		[]corev1.EnvVar{},
		"test-app",
		"test-otel-config",
		"",
		corev1.ResourceRequirements{}, // Use default OTEL resources
	)
	require.NoError(t, err)

	// Call SetupFluentBit
	fluentbitCM := SetupFluentBit(
		pod,
		telLaunchSpec,
		allEnvSet,
		WorkloadTypeContainer,
		"test-namespace",
		true,                          // FluentbitEnabled
		corev1.ResourceRequirements{}, // Use default Fluentbit resources
	)
	require.NotNil(t, fluentbitCM, "Fluentbit ConfigMap should be created when logs telemetry is enabled")

	// Verify OTEL container was added
	otelContainerFound := false
	for _, container := range pod.Spec.Containers {
		if container.Name == ByooOTelCollectorPodNameBase {
			otelContainerFound = true
			break
		}
	}
	assert.True(t, otelContainerFound, "OTEL container should be added to the pod")

	// Verify Fluentbit container was added
	fluentbitContainerFound := false
	for _, container := range pod.Spec.Containers {
		if container.Name == FluentbitContainerName {
			fluentbitContainerFound = true
			assert.Equal(t, "fluent/fluent-bit:latest", container.Image)
			break
		}
	}
	assert.True(t, fluentbitContainerFound, "Fluentbit container should be added to the pod")

	// Verify Fluentbit volumes were added
	fluentbitConfigVolumeFound := false
	fluentbitVarlogVolumeFound := false
	for _, volume := range pod.Spec.Volumes {
		if volume.Name == fluentbitConfigVolumeName {
			fluentbitConfigVolumeFound = true
		}
		if volume.Name == fluentbitVarLogVolumeName {
			fluentbitVarlogVolumeFound = true
		}
	}
	assert.True(t, fluentbitConfigVolumeFound, "Fluentbit config volume should be added")
	assert.True(t, fluentbitVarlogVolumeFound, "Fluentbit varlog volume should be added")

	// Verify ConfigMap content
	assert.Contains(t, fluentbitCM.Data, fluentbitConfigCMKey)
	config := fluentbitCM.Data[fluentbitConfigCMKey]
	assert.Contains(t, config, "inference")
	assert.Contains(t, config, "task")
}

func TestSetupPodTelemetry_WithoutFluentbit(t *testing.T) {
	// Create a test pod
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Spec: corev1.PodSpec{},
	}

	// Create telemetry launch spec with logs but NO fluentbit image env var
	telLaunchSpec := &TelemetriesLaunchSpecification{
		Telemetries: struct {
			Logs    *Telemetry `json:"logsTelemetry,omitempty"`
			Metrics *Telemetry `json:"metricsTelemetry,omitempty"`
			Traces  *Telemetry `json:"tracesTelemetry,omitempty"`
		}{
			Logs: &Telemetry{
				Protocol: "http",
				Provider: "SPLUNK",
				Endpoint: "https://splunk.example.com",
				Name:     "splunk-logs",
			},
		},
	}

	// Create environment set WITHOUT FluentbitContainerEnv
	allEnvSet := map[string]string{
		OTelContainerEnv: "nvcr.io/test/otel-collector:1.0.0",
		CloudProviderEnv: "DGXCLOUD",
	}

	// Call SetupPodTelemetry
	err := SetupPodTelemetry(
		pod,
		telLaunchSpec,
		allEnvSet,
		WorkloadTypeContainer,
		"us-west-2",
		"test-instance",
		"test-namespace",
		[]corev1.EnvVar{},
		"test-app",
		"test-otel-config",
		"",
		corev1.ResourceRequirements{}, // Use default OTEL resources
	)
	require.NoError(t, err)

	// Call SetupFluentBit
	fluentbitCM := SetupFluentBit(
		pod,
		telLaunchSpec,
		allEnvSet,
		WorkloadTypeContainer,
		"test-namespace",
		true,                          // FluentbitEnabled
		corev1.ResourceRequirements{}, // Use default Fluentbit resources
	)
	// With the default image fallback, FluentBit should now be deployed even without env var
	assert.NotNil(t, fluentbitCM, "Fluentbit ConfigMap should be created using default image")

	// Verify Fluentbit container WAS added with default image
	var fluentbitContainer *corev1.Container
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == FluentbitContainerName {
			fluentbitContainer = &pod.Spec.Containers[i]
			break
		}
	}
	require.NotNil(t, fluentbitContainer, "Fluentbit container should be added with default image")
	assert.Equal(t, DefaultFluentbitImage, fluentbitContainer.Image, "Should use default Fluentbit image when env var not set")
}

func TestSetupPodTelemetry_FeatureFlagDisabled(t *testing.T) {
	// Create a test pod
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Spec: corev1.PodSpec{},
	}

	// Create telemetry launch spec with logs
	telLaunchSpec := &TelemetriesLaunchSpecification{
		Telemetries: struct {
			Logs    *Telemetry `json:"logsTelemetry,omitempty"`
			Metrics *Telemetry `json:"metricsTelemetry,omitempty"`
			Traces  *Telemetry `json:"tracesTelemetry,omitempty"`
		}{
			Logs: &Telemetry{
				Protocol: "http",
				Provider: "SPLUNK",
				Endpoint: "https://splunk.example.com",
				Name:     "splunk-logs",
			},
		},
	}

	// Create environment set with all required variables
	allEnvSet := map[string]string{
		OTelContainerEnv:      "nvcr.io/test/otel-collector:1.0.0",
		FluentbitContainerEnv: "fluent/fluent-bit:latest",
		CloudProviderEnv:      "DGXCLOUD",
	}

	// Call SetupPodTelemetry
	err := SetupPodTelemetry(
		pod,
		telLaunchSpec,
		allEnvSet,
		WorkloadTypeContainer,
		"us-west-2",
		"test-instance",
		"test-namespace",
		[]corev1.EnvVar{},
		"test-app",
		"test-otel-config",
		"",
		corev1.ResourceRequirements{}, // Use default OTEL resources
	)
	require.NoError(t, err)

	// Call SetupFluentBit with feature flag DISABLED
	fluentbitCM := SetupFluentBit(
		pod,
		telLaunchSpec,
		allEnvSet,
		WorkloadTypeContainer,
		"test-namespace",
		false,                         // FluentbitEnabled = false
		corev1.ResourceRequirements{}, // Use default Fluentbit resources
	)
	assert.Nil(t, fluentbitCM, "Fluentbit ConfigMap should NOT be created when feature flag is disabled")

	// Verify Fluentbit container was NOT added
	fluentbitContainerFound := false
	for _, container := range pod.Spec.Containers {
		if container.Name == FluentbitContainerName {
			fluentbitContainerFound = true
			break
		}
	}
	assert.False(t, fluentbitContainerFound, "Fluentbit container should NOT be added when feature flag is disabled")
}

func TestGetDefaultContainerResourcesFluentbit(t *testing.T) {
	resources := GetDefaultContainerResourcesFluentbit()

	// Check requests
	cpuRequest := resources.Requests[corev1.ResourceCPU]
	assert.Equal(t, "100m", cpuRequest.String())

	memRequest := resources.Requests[corev1.ResourceMemory]
	assert.Equal(t, "128Mi", memRequest.String())

	// Check limits
	cpuLimit := resources.Limits[corev1.ResourceCPU]
	assert.Equal(t, "200m", cpuLimit.String())

	memLimit := resources.Limits[corev1.ResourceMemory]
	assert.Equal(t, "256Mi", memLimit.String())
}

func TestSetupPodTelemetry_WithCustomResources(t *testing.T) {
	// Create a test pod
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Spec: corev1.PodSpec{},
	}

	// Create telemetry launch spec with logs
	telLaunchSpec := &TelemetriesLaunchSpecification{
		Telemetries: struct {
			Logs    *Telemetry `json:"logsTelemetry,omitempty"`
			Metrics *Telemetry `json:"metricsTelemetry,omitempty"`
			Traces  *Telemetry `json:"tracesTelemetry,omitempty"`
		}{
			Logs: &Telemetry{
				Protocol: "http",
				Provider: "SPLUNK",
				Endpoint: "https://splunk.example.com",
				Name:     "splunk-logs",
			},
		},
	}

	// Create environment set with required variables
	allEnvSet := map[string]string{
		OTelContainerEnv:      "nvcr.io/test/otel-collector:1.0.0",
		FluentbitContainerEnv: "fluent/fluent-bit:latest",
		CloudProviderEnv:      "DGXCLOUD",
	}

	// Custom resources
	customResources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewMilliQuantity(250, resource.DecimalSI),
			corev1.ResourceMemory: *resource.NewQuantity(512<<20, resource.BinarySI),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewMilliQuantity(500, resource.DecimalSI),
			corev1.ResourceMemory: *resource.NewQuantity(1<<30, resource.BinarySI),
		},
	}

	// Call SetupPodTelemetry
	err := SetupPodTelemetry(
		pod,
		telLaunchSpec,
		allEnvSet,
		WorkloadTypeContainer,
		"us-west-2",
		"test-instance",
		"test-namespace",
		[]corev1.EnvVar{},
		"test-app",
		"test-otel-config",
		"",
		corev1.ResourceRequirements{}, // Use default OTEL resources
	)
	require.NoError(t, err)

	// Call SetupFluentBit with custom resources
	fluentbitCM := SetupFluentBit(
		pod,
		telLaunchSpec,
		allEnvSet,
		WorkloadTypeContainer,
		"test-namespace",
		true,            // FluentbitEnabled
		customResources, // Custom Fluentbit resources
	)
	require.NotNil(t, fluentbitCM)

	// Verify Fluentbit container was added with custom resources
	var fluentbitContainer *corev1.Container
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == FluentbitContainerName {
			fluentbitContainer = &pod.Spec.Containers[i]
			break
		}
	}

	require.NotNil(t, fluentbitContainer, "Fluentbit container should be added")

	// Verify custom resources are used
	cpuRequest := fluentbitContainer.Resources.Requests[corev1.ResourceCPU]
	assert.Equal(t, "250m", cpuRequest.String())

	memRequest := fluentbitContainer.Resources.Requests[corev1.ResourceMemory]
	assert.Equal(t, "512Mi", memRequest.String())

	cpuLimit := fluentbitContainer.Resources.Limits[corev1.ResourceCPU]
	assert.Equal(t, "500m", cpuLimit.String())

	memLimit := fluentbitContainer.Resources.Limits[corev1.ResourceMemory]
	assert.Equal(t, "1Gi", memLimit.String())
}

func TestFluentbitConfigContainsInferenceAndTaskContainers(t *testing.T) {
	podName := "test-pod"
	cm := NewFluentbitConfigMap("test-cm", podName, WorkloadTypeContainer)
	config := cm.Data[fluentbitConfigCMKey]

	// Verify the INPUT section targets specific pod's inference and task containers
	assert.True(t,
		strings.Contains(config, podName+"_${POD_NAMESPACE}_inference") || strings.Contains(config, podName+"_${POD_NAMESPACE}_task"),
		"Fluentbit config should target specific pod's inference or task containers")

	// Verify the tail input is configured
	assert.Contains(t, config, "Name              tail")
	assert.Contains(t, config, "Parser            docker")

	// Verify polling mode is used (not inotify) to prevent "too many open files"
	assert.Contains(t, config, "Inotify_Watcher   false")
	assert.Contains(t, config, "Exclude_Path      *.gz,*.zip,*.bz2")

	// Verify kubernetes filter is NOT present (design decision: no K8s API access required)
	assert.NotContains(t, config, "Name                kubernetes")

	// Verify modify filter adds basic metadata
	assert.Contains(t, config, "Name    modify")
	assert.Contains(t, config, "Add     service.name ${POD_NAME}")
	assert.Contains(t, config, "Add     service.namespace ${POD_NAMESPACE}")

	// Verify it uses HTTP/OpenTelemetry output (port 14358)
	assert.Contains(t, config, "Name  opentelemetry")
	assert.Contains(t, config, "Host  localhost")
	assert.Contains(t, config, "Port  14358", "Should use HTTP port 14358")
}

func TestFluentbitConfigForHelmWorkload(t *testing.T) {
	podName := UtilsPodName
	cm := NewFluentbitConfigMap("test-cm", podName, WorkloadTypeHelm)
	config := cm.Data[fluentbitConfigCMKey]

	// Verify the INPUT section uses wildcard pattern to capture all pods in namespace
	assert.Contains(t, config, "Path              /var/log/containers/*_${POD_NAMESPACE}_*.log",
		"Helm workload should use wildcard pattern to capture all pods")

	// Verify infrastructure pods are excluded
	assert.Contains(t, config, "*_utils_*", "Should exclude utils pod")
	assert.Contains(t, config, "*_nvcf-smb-server_*", "Should exclude SMB server pod")
	assert.Contains(t, config, "*_byoo-otel-collector_*", "Should exclude OTEL collector")
	assert.Contains(t, config, "*_fluentbit-logs_*", "Should exclude Fluent Bit itself")

	// Verify basic configuration
	assert.Contains(t, config, "Name              tail")
	assert.Contains(t, config, "Parser            docker")
	assert.Contains(t, config, "Inotify_Watcher   false")

	// Verify kubernetes filter is NOT present (design decision: no K8s API access required)
	assert.NotContains(t, config, "Name                kubernetes")

	// Verify modify filter adds basic metadata
	assert.Contains(t, config, "Name    modify")
	assert.Contains(t, config, "Add     service.name ${POD_NAME}")
}
