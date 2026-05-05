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

package function

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
)

func TestTranslateContainer_AppliesConfiguredTolerations(t *testing.T) {
	customToleration := corev1.Toleration{
		Key:      "dedicated",
		Operator: corev1.TolerationOpEqual,
		Value:    "functions",
		Effect:   corev1.TaintEffectNoSchedule,
	}

	objs, err := Translate(
		newContainerFunctionMessage(),
		TranslateConfig{
			TranslateConfig: newFunctionTranslateConfig([]corev1.Toleration{
				customToleration,
				{
					Key:      common.NVIDIAGPUTolerationKey,
					Operator: corev1.TolerationOpExists,
					Effect:   corev1.TaintEffectNoSchedule,
				},
			}),
		},
	)
	require.NoError(t, err)

	inferencePod := findPodByName(t, objs, "0-request")
	assertTolerationsMatch(t, inferencePod.Spec.Tolerations,
		customToleration,
		corev1.Toleration{
			Key:      common.NVIDIAGPUTolerationKey,
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoSchedule,
		},
	)
}

func TestTranslateContainerUtilsDeploy_AppliesConfiguredTolerations(t *testing.T) {
	customToleration := corev1.Toleration{
		Key:      "workload-type",
		Operator: corev1.TolerationOpEqual,
		Value:    "sidecar",
		Effect:   corev1.TaintEffectNoExecute,
	}

	objs, err := Translate(
		newContainerFunctionMessage(),
		TranslateConfig{
			TranslateConfig:     newFunctionTranslateConfig([]corev1.Toleration{customToleration}),
			SidecarAsDeployment: true,
		},
	)
	require.NoError(t, err)

	inferencePod := findPodByName(t, objs, "0-request-inference")
	assertTolerationsMatch(t, inferencePod.Spec.Tolerations,
		customToleration,
		corev1.Toleration{
			Key:      common.NVIDIAGPUTolerationKey,
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoSchedule,
		},
	)

	utilsDeployment := findDeploymentByName(t, objs, "request-utils")
	assertTolerationsMatch(t, utilsDeployment.Spec.Template.Spec.Tolerations,
		customToleration,
		corev1.Toleration{
			Key:      common.NVIDIAGPUTolerationKey,
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoSchedule,
		},
	)
}

func TestTranslateHelmChartUtilsDeploy_AppliesConfiguredTolerations(t *testing.T) {
	customToleration := corev1.Toleration{
		Key:      "workload-type",
		Operator: corev1.TolerationOpEqual,
		Value:    "helm",
		Effect:   corev1.TaintEffectNoSchedule,
	}

	objs, err := Translate(
		newHelmFunctionMessage(),
		TranslateConfig{
			TranslateConfig:     newFunctionTranslateConfig([]corev1.Toleration{customToleration}),
			SidecarAsDeployment: true,
		},
	)
	require.NoError(t, err)

	utilsDeployment := findDeploymentByName(t, objs, "request-utils")
	assertTolerationsMatch(t, utilsDeployment.Spec.Template.Spec.Tolerations,
		customToleration,
		corev1.Toleration{
			Key:      common.NVIDIAGPUTolerationKey,
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoSchedule,
		},
	)
}

func newContainerFunctionMessage() CreationQueueMessage {
	return CreationQueueMessage{
		CreationQueueMessageMetadata: common.CreationQueueMessageMetadata{
			Action:            common.FunctionCreationAction,
			RequestID:         "request-id",
			MessageBatchID:    "batch-id",
			NCAID:             "nca-id",
			InstanceCount:     1,
			InstanceTypeValue: "g5",
			GPUType:           "A100",
			RequestedGPUCount: 1,
		},
		Details: Details{
			FunctionID:        "function-id",
			FunctionVersionID: "function-version-id",
			FunctionType:      FunctionTypeDefault,
		},
		LaunchSpecification: &LaunchSpecification{
			EnvironmentB64: encodeTextEnv(map[string]string{
				common.ContainerFunctionImageEnv: "nvcr.io/nvidia/function:latest",
				common.UtilsImageEnv:             "nvcr.io/nvidia/utils:latest",
				common.InitImageEnv:              "nvcr.io/nvidia/init:latest",
			}),
			ICMSEnvironment: "test",
			CloudProvider:   "aws",
		},
	}
}

func newHelmFunctionMessage() CreationQueueMessage {
	msg := newContainerFunctionMessage()
	msg.LaunchSpecification = &LaunchSpecification{
		EnvironmentB64: encodeTextEnv(map[string]string{
			common.UtilsImageEnv: "nvcr.io/nvidia/utils:latest",
			common.InitImageEnv:  "nvcr.io/nvidia/init:latest",
		}),
		ICMSEnvironment: "test",
		CloudProvider:   "aws",
		HelmChartLaunchSpecification: &common.HelmChartLaunchSpecification{
			HelmChartURL: "https://helm.example.com/charts/test-chart-0.1.0.tgz",
		},
	}
	return msg
}

func newFunctionTranslateConfig(tolerations []corev1.Toleration) common.TranslateConfig {
	return common.TranslateConfig{
		ObjectNameBase:               "request",
		InstanceTypeLabelSelectorKey: "node.kubernetes.io/instance-type",
		WorkloadResources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				"nvidia.com/gpu": resource.MustParse("1"),
			},
		},
		Tolerations: tolerations,
	}
}

func encodeTextEnv(envs map[string]string) string {
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

func findPodByName(t *testing.T, objs []metav1.Object, name string) *corev1.Pod {
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

func findDeploymentByName(t *testing.T, objs []metav1.Object, name string) *appsv1.Deployment {
	t.Helper()

	for _, obj := range objs {
		deployment, ok := obj.(*appsv1.Deployment)
		if ok && deployment.Name == name {
			return deployment
		}
	}

	t.Fatalf("deployment %q not found", name)
	return nil
}

func assertTolerationsMatch(t *testing.T, got []corev1.Toleration, want ...corev1.Toleration) {
	t.Helper()
	assert.ElementsMatch(t, want, got)
}
