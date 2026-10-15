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

package task

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
)

func TestTranslateContainerCreatesTaskPod(t *testing.T) {
	objs, err := Translate(
		newTaskMessage(false),
		newTaskTranslateConfig(),
	)

	require.NoError(t, err)
	pod := findTaskPodByName(t, objs, "0-request-task")
	require.Len(t, pod.Spec.InitContainers, 1)
	assert.Equal(t, common.InitContainerName, pod.Spec.InitContainers[0].Name)
	assert.Equal(t, "nvcr.io/nvidia/init:latest", pod.Spec.InitContainers[0].Image)

	taskContainer := findTaskContainerByName(t, pod.Spec.Containers, taskContainerName)
	assert.Equal(t, "nvcr.io/nvidia/task:latest", taskContainer.Image)
	assert.Equal(t, []string{"--input", "value"}, taskContainer.Args)
	assert.Equal(t, corev1.RestartPolicyNever, pod.Spec.RestartPolicy)

	utilsContainer := findTaskContainerByName(t, pod.Spec.Containers, common.UtilsContainerName)
	assert.Equal(t, "nvcr.io/nvidia/utils:latest", utilsContainer.Image)
	assert.Equal(t, string(common.UploadResult), envValue(utilsContainer.Env, resultHandlingStratEnvKey))
}

func TestTranslateContainerWithCacheAndSecrets(t *testing.T) {
	msg := newTaskMessage(false)
	msg.LaunchSpecification.EnvironmentB64 = encodeTaskTextEnv(map[string]string{
		common.ContainerTaskImageEnv:    "nvcr.io/nvidia/task:latest",
		common.UtilsImageEnv:            "nvcr.io/nvidia/utils:latest",
		common.InitImageEnv:             "nvcr.io/nvidia/init:latest",
		common.ESSAgentContainerEnv:     "nvcr.io/nvidia/ess:latest",
		common.TaskContainerEnvEnv:      base64.StdEncoding.EncodeToString([]byte(`[{"key":"TASK_ENV","value":"value"}]`)),
		common.SecretsAssertionTokenEnv: "assertion-token",
		common.TaskSecretsPresentEnv:    "true",
		"TERMINATION_GRACE_PERIOD":      "PT2H",
	})
	msg.LaunchSpecification.CacheLaunchSpecification = &common.CacheLaunchSpecification{
		CacheArtifacts: true,
		CacheHandle:    "cache-handle",
		CacheSize:      1,
	}

	objs, err := Translate(msg, newTaskTranslateConfig())
	require.NoError(t, err)

	pod := findTaskPodByName(t, objs, "0-request-task")
	assert.Equal(t, int64(2*60*60), *pod.Spec.TerminationGracePeriodSeconds)
	assert.Equal(t, "value", envValue(findTaskContainerByName(t, pod.Spec.Containers, taskContainerName).Env, "TASK_ENV"))
	assert.Equal(t, "ess", findTaskContainerByName(t, pod.Spec.Containers, "ess").Name)
	require.Len(t, pod.Spec.InitContainers, 2)
	assert.Equal(t, "ess-init", pod.Spec.InitContainers[1].Name)

	assert.NotNil(t, findTaskPVCByName(t, objs, "rw-pvc-cache-handle"))
	assert.NotNil(t, findTaskJobByName(t, objs, "writer-job-cache-handle"))
}

func TestTranslateHelmChartCreatesUtilsPod(t *testing.T) {
	objs, err := Translate(
		newTaskMessage(true),
		TranslateConfig{
			TranslateConfig: common.TranslateConfig{
				ObjectNameBase:               "request",
				InstanceTypeLabelSelectorKey: "node.kubernetes.io/instance-type",
			},
		},
	)

	require.NoError(t, err)
	pod := findTaskPodByName(t, objs, common.UtilsPodName)
	assert.Equal(t, corev1.RestartPolicyNever, pod.Spec.RestartPolicy)
	assert.Equal(t, int64(60*60), *pod.Spec.TerminationGracePeriodSeconds)
	require.Len(t, pod.Spec.InitContainers, 1)
	assert.Equal(t, common.InitContainerName, pod.Spec.InitContainers[0].Name)
	assert.Equal(t, "nvcr.io/nvidia/init:latest", pod.Spec.InitContainers[0].Image)

	utilsContainer := findTaskContainerByName(t, pod.Spec.Containers, common.UtilsContainerName)
	assert.Equal(t, "nvcr.io/nvidia/utils:latest", utilsContainer.Image)
	assert.Equal(t, "request-task", envValue(utilsContainer.Env, "INSTANCE_ID"))
	assert.Equal(t, string(common.UploadResult), envValue(utilsContainer.Env, resultHandlingStratEnvKey))
	assert.NotNil(t, findTaskVolumeByName(t, pod.Spec.Volumes, "task-data").EmptyDir)
}

func TestTranslateHelmChartWithCacheAndSecrets(t *testing.T) {
	msg := newTaskMessage(true)
	msg.LaunchSpecification.EnvironmentB64 = encodeTaskTextEnv(map[string]string{
		common.UtilsImageEnv:            "nvcr.io/nvidia/utils:latest",
		common.InitImageEnv:             "nvcr.io/nvidia/init:latest",
		common.ESSAgentContainerEnv:     "nvcr.io/nvidia/ess:latest",
		common.SecretsAssertionTokenEnv: "assertion-token",
		common.TaskSecretsPresentEnv:    "true",
		"TERMINATION_GRACE_PERIOD":      "PT2H",
	})
	msg.LaunchSpecification.CacheLaunchSpecification = &common.CacheLaunchSpecification{
		CacheArtifacts: true,
		CacheHandle:    "helm-cache",
		CacheSize:      1,
	}

	objs, err := Translate(
		msg,
		TranslateConfig{
			TranslateConfig: common.TranslateConfig{
				ObjectNameBase:               "request",
				InstanceTypeLabelSelectorKey: "node.kubernetes.io/instance-type",
			},
		},
	)
	require.NoError(t, err)

	pod := findTaskPodByName(t, objs, common.UtilsPodName)
	assert.Equal(t, int64(2*60*60), *pod.Spec.TerminationGracePeriodSeconds)
	assert.Equal(t, "ess", findTaskContainerByName(t, pod.Spec.Containers, "ess").Name)
	require.Len(t, pod.Spec.InitContainers, 2)
	assert.Equal(t, "ess-init", pod.Spec.InitContainers[1].Name)
	assert.NotNil(t, findTaskPVCByName(t, objs, "rw-pvc-helm-cache"))
	assert.NotNil(t, findTaskJobByName(t, objs, "writer-job-helm-cache"))
}

func TestTranslateTaskRejectsInvalidContainerInputs(t *testing.T) {
	cases := []struct {
		name            string
		env             map[string]string
		wantErr         string
		wantErrContains bool
	}{
		{
			name: "invalid termination grace period",
			env: map[string]string{
				common.ContainerTaskImageEnv: "nvcr.io/nvidia/task:latest",
				common.UtilsImageEnv:         "nvcr.io/nvidia/utils:latest",
				common.InitImageEnv:          "nvcr.io/nvidia/init:latest",
				"TERMINATION_GRACE_PERIOD":   "not-a-duration",
			},
			wantErr:         "parse worker env termination grace period",
			wantErrContains: true,
		},
		{
			name: "invalid task args",
			env: map[string]string{
				common.ContainerTaskImageEnv: "nvcr.io/nvidia/task:latest",
				common.UtilsImageEnv:         "nvcr.io/nvidia/utils:latest",
				common.InitImageEnv:          "nvcr.io/nvidia/init:latest",
				common.TaskContainerArgsEnv:  `"unterminated`,
			},
			wantErr:         "parse container args",
			wantErrContains: true,
		},
		{
			name: "missing workload image",
			env: map[string]string{
				common.UtilsImageEnv: "nvcr.io/nvidia/utils:latest",
				common.InitImageEnv:  "nvcr.io/nvidia/init:latest",
			},
			wantErr: "no workload container image found",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			msg := newTaskMessage(false)
			msg.LaunchSpecification.EnvironmentB64 = encodeTaskTextEnv(tt.env)

			_, err := Translate(msg, newTaskTranslateConfig())
			if tt.wantErrContains {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.EqualError(t, err, tt.wantErr)
			}
		})
	}
}

func TestTaskDeepCopy(t *testing.T) {
	telemetries := &common.TelemetriesLaunchSpecification{}
	telemetries.Telemetries.Logs = &common.Telemetry{
		Protocol: "http",
		Provider: "SPLUNK",
		Endpoint: "https://logs.example.com",
		Name:     "logs",
	}
	src := &LaunchSpecification{
		ContainerImage: "nvcr.io/nvidia/task:latest",
		EnvironmentB64: "env",
		Telemetries:    telemetries,
		HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{
			HelmChartURL: "https://helm.example.com/chart.tgz",
			Values:       []byte("replicas: 1"),
		},
		CacheLaunchSpecification: &common.CacheLaunchSpecification{
			CacheArtifacts: true,
			CacheHandle:    "cache-handle",
			CacheSize:      1 << 30,
		},
	}

	copied := src.DeepCopy()
	require.NotNil(t, copied)
	src.Telemetries.Telemetries.Logs.Name = "changed"
	src.HelmChartLaunchSpecification.Values[0] = 'R'
	src.CacheLaunchSpecification.CacheHandle = "changed"

	assert.Equal(t, "logs", copied.Telemetries.Telemetries.Logs.Name)
	assert.Equal(t, []byte("replicas: 1"), copied.HelmChartLaunchSpecification.Values)
	assert.Equal(t, "cache-handle", copied.CacheLaunchSpecification.CacheHandle)

	details := (&Details{TaskID: "task-id", TaskType: "default"}).DeepCopy()
	require.NotNil(t, details)
	assert.Equal(t, "task-id", details.TaskID)

	var nilDetails *Details
	var nilLaunchSpec *LaunchSpecification
	assert.Nil(t, nilDetails.DeepCopy())
	assert.Nil(t, nilLaunchSpec.DeepCopy())
}

func newTaskMessage(helm bool) CreationQueueMessage {
	msg := CreationQueueMessage{
		CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
			RequestID:         "request-id",
			MessageBatchID:    "batch-id",
			NCAID:             "nca-id",
			InstanceCount:     1,
			InstanceTypeValue: "g5",
			GPUType:           "A100",
			RequestedGPUCount: 1,
		},
		Details: Details{
			TaskID:   "task-id",
			TaskType: "default",
		},
		LaunchSpecification: LaunchSpecification{
			ContainerImage: "nvcr.io/nvidia/task:latest",
			EnvironmentB64: encodeTaskTextEnv(map[string]string{
				common.ContainerTaskImageEnv: "nvcr.io/nvidia/task:latest",
				common.UtilsImageEnv:         "nvcr.io/nvidia/utils:latest",
				common.InitImageEnv:          "nvcr.io/nvidia/init:latest",
				common.TaskContainerArgsEnv:  "--input value",
			}),
			CloudProvider:          "aws",
			ICMSEnvironment:        "test",
			MaxRuntimeDuration:     "PT10M",
			MaxQueuedDuration:      "PT1M",
			ResultHandlingStrategy: common.UploadResult,
		},
	}
	if helm {
		msg.LaunchSpecification.ContainerImage = ""
		msg.LaunchSpecification.HelmChartLaunchSpecification = &common.HelmChartLaunchSpecification{
			HelmChartURL: "https://helm.example.com/charts/test-chart-0.1.0.tgz",
		}
	}
	return msg
}

func newTaskTranslateConfig() TranslateConfig {
	return TranslateConfig{
		TranslateConfig: common.TranslateConfig{
			ObjectNameBase:               "request",
			InstanceTypeLabelSelectorKey: "node.kubernetes.io/instance-type",
			WorkloadResources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					"nvidia.com/gpu": resource.MustParse("1"),
				},
			},
		},
	}
}

func encodeTaskTextEnv(envs map[string]string) string {
	keys := make([]string, 0, len(envs))
	for key := range envs {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("%s=%s", key, envs[key]))
	}

	return base64.StdEncoding.EncodeToString([]byte(strings.Join(lines, "\n")))
}

func findTaskPodByName(t *testing.T, objs []metav1.Object, name string) *corev1.Pod {
	t.Helper()

	for _, obj := range objs {
		pod, ok := obj.(*corev1.Pod)
		if ok && pod.Name == name {
			return pod
		}
	}

	t.Fatalf("pod %q not found", name)
	return nil
}

func findTaskContainerByName(t *testing.T, containers []corev1.Container, name string) corev1.Container {
	t.Helper()

	for _, container := range containers {
		if container.Name == name {
			return container
		}
	}

	t.Fatalf("container %q not found", name)
	return corev1.Container{}
}

func findTaskVolumeByName(t *testing.T, volumes []corev1.Volume, name string) corev1.VolumeSource {
	t.Helper()

	for _, volume := range volumes {
		if volume.Name == name {
			return volume.VolumeSource
		}
	}

	t.Fatalf("volume %q not found", name)
	return corev1.VolumeSource{}
}

func findTaskPVCByName(t *testing.T, objs []metav1.Object, name string) *corev1.PersistentVolumeClaim {
	t.Helper()

	for _, obj := range objs {
		pvc, ok := obj.(*corev1.PersistentVolumeClaim)
		if ok && pvc.Name == name {
			return pvc
		}
	}

	t.Fatalf("persistent volume claim %q not found", name)
	return nil
}

func findTaskJobByName(t *testing.T, objs []metav1.Object, name string) metav1.Object {
	t.Helper()

	for _, obj := range objs {
		if obj.GetName() == name {
			return obj
		}
	}

	t.Fatalf("job %q not found", name)
	return nil
}

func envValue(envs []corev1.EnvVar, name string) string {
	for _, env := range envs {
		if env.Name == name {
			return env.Value
		}
	}
	return ""
}
