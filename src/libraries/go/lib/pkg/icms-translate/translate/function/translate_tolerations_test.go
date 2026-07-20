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

func TestTranslateHelmChartLLM_AddsRouterAndCredentialContainers(t *testing.T) {
	msg := newHelmFunctionMessage()
	msg.Details.FunctionType = FunctionTypeLLM
	msg.LaunchSpecification.EnvironmentB64 = encodeTextEnv(map[string]string{
		common.UtilsImageEnv:                "nvcr.io/nvidia/utils:latest",
		common.InitImageEnv:                 "nvcr.io/nvidia/init:latest",
		"LLM_REQUEST_ROUTER_ADDRESS":        "llm-router.example.com:443",
		"INFERENCE_PORT":                    "8080",
		"HELM_CHART_INFERENCE_SERVICE_NAME": "inference-svc",
		"NVCF_WORKER_TOKEN":                 "worker-token",
		"NVCF_FQDN_GRPC":                    "grpc.example.com",
		"FUNCTION_ID":                       "function-id",
		"FUNCTION_VERSION_ID":               "function-version-id",
		"NCA_ID":                            "nca-id",
		llmRouterClientImageEnv:             "nvcr.io/nvidia/router:latest",
		llmCredentialManagerImageEnv:        "nvcr.io/nvidia/credential-manager:latest",
	})
	msg.LaunchSpecification.Models = Models{
		{Name: "model-a", LLMModelConfig: &LLMModelConfig{Tokenizer: "tokenizer-a"}},
		{Name: "model-b"},
	}

	objs, err := Translate(
		msg,
		TranslateConfig{
			TranslateConfig:        newFunctionTranslateConfig(nil),
			DefaultStargateAddress: "default-stargate.example.com:443",
		},
	)
	require.NoError(t, err)

	utilsPod := findPodByName(t, objs, common.UtilsPodName)
	require.Len(t, utilsPod.Spec.Containers, 2)

	router := findContainerByName(t, utilsPod.Spec.Containers, LLMWorkerContainerName)
	assert.Equal(t, "nvcr.io/nvidia/router:latest", router.Image)
	assert.Contains(t, router.Args, "--upstream-http-base-url=http://inference-svc:8080")
	assert.Contains(t, router.Args, "--stargate-address=llm-router.example.com:443")
	assert.Contains(t, router.Args, "--backend-connectivity=reverse")
	assert.Contains(t, router.Args, "--initial-input-tps=100")
	assert.NotContains(t, router.Args, "--reverse-tunnel")
	assert.NotContains(t, router.Args, "--do-calibration")
	assert.Contains(t, router.Args, "--model-name=model-a")
	assert.NotContains(t, router.Args, "--model-name=model-b")
	routerEnv := envSliceToMap(router.Env)
	assert.Equal(t, "llm-router.example.com:443", routerEnv["LLM_REQUEST_ROUTER_ADDRESS"])
	assert.Equal(t, "llm-router.example.com:443", routerEnv["STARGATE_ADDRESS"])

	credentialManager := findContainerByName(t, utilsPod.Spec.Containers, "llm-credential-manager")
	assert.Equal(t, "nvcr.io/nvidia/credential-manager:latest", credentialManager.Image)
	credentialEnv := envSliceToMap(credentialManager.Env)
	assert.Equal(t, "worker-token", credentialEnv["NVCF_WORKER_TOKEN"])
	assert.Equal(t, "grpc.example.com", credentialEnv["NVCF_FQDN_GRPC"])
	assert.Equal(t, ConfigDirPath, credentialEnv["SHARED_CONFIG_DIR"])
	assert.Equal(t, "/var/run/llm/worker-token", credentialEnv["WORKER_TOKEN_PATH"])

	require.NotNil(t, findVolumeByName(t, utilsPod.Spec.Volumes, "llm").EmptyDir)
}

func TestTranslateContainerLLM_AddsRouterAndCredentialContainers(t *testing.T) {
	msg := newContainerFunctionMessage()
	msg.Details.FunctionType = FunctionTypeLLM
	msg.LaunchSpecification.EnvironmentB64 = encodeTextEnv(map[string]string{
		common.ContainerFunctionImageEnv: "nvcr.io/nvidia/function:latest",
		common.UtilsImageEnv:             "nvcr.io/nvidia/utils:latest",
		common.InitImageEnv:              "nvcr.io/nvidia/init:latest",
		"LLM_REQUEST_ROUTER_ADDRESS":     "llm-router.example.com:443",
		"INFERENCE_PORT":                 "8080",
		"NVCF_WORKER_TOKEN":              "worker-token",
		"NVCF_FQDN_GRPC":                 "grpc.example.com",
		"FUNCTION_ID":                    "function-id",
		"FUNCTION_VERSION_ID":            "function-version-id",
		"NCA_ID":                         "nca-id",
		llmRouterClientImageEnv:          "nvcr.io/nvidia/router:latest",
		llmCredentialManagerImageEnv:     "nvcr.io/nvidia/credential-manager:latest",
	})
	msg.LaunchSpecification.Models = Models{
		{Name: "model-a", LLMModelConfig: &LLMModelConfig{Tokenizer: "tokenizer-a"}},
	}

	objs, err := Translate(
		msg,
		TranslateConfig{
			TranslateConfig: newFunctionTranslateConfig(nil),
		},
	)
	require.NoError(t, err)

	pod := findPodByName(t, objs, "0-request")
	require.Len(t, pod.Spec.Containers, 3)
	assert.Equal(t, "nvcr.io/nvidia/function:latest", findContainerByName(t, pod.Spec.Containers, inferenceContainerName).Image)

	router := findContainerByName(t, pod.Spec.Containers, LLMWorkerContainerName)
	assert.Equal(t, "nvcr.io/nvidia/router:latest", router.Image)
	assert.Contains(t, router.Args, "--upstream-http-base-url=http://127.0.0.1:8080")
	assert.Contains(t, router.Args, "--stargate-address=llm-router.example.com:443")
	assert.Contains(t, router.Args, "--backend-connectivity=reverse")
	assert.Contains(t, router.Args, "--initial-input-tps=100")
	assert.NotContains(t, router.Args, "--reverse-tunnel")
	assert.NotContains(t, router.Args, "--do-calibration")
	assert.Contains(t, router.Args, "--model-name=model-a")
	routerEnv := envSliceToMap(router.Env)
	assert.Equal(t, "llm-router.example.com:443", routerEnv["LLM_REQUEST_ROUTER_ADDRESS"])
	assert.Equal(t, "llm-router.example.com:443", routerEnv["STARGATE_ADDRESS"])

	initEnv := envSliceToMap(findContainerByName(t, pod.Spec.InitContainers, "init").Env)
	assert.Equal(t, "llm-router.example.com:443", initEnv["LLM_REQUEST_ROUTER_ADDRESS"])
	assert.Equal(t, "llm-router.example.com:443", initEnv["STARGATE_ADDRESS"])

	credentialManager := findContainerByName(t, pod.Spec.Containers, "llm-credential-manager")
	assert.Equal(t, "nvcr.io/nvidia/credential-manager:latest", credentialManager.Image)
	assert.Equal(t, "worker-token", envSliceToMap(credentialManager.Env)["NVCF_WORKER_TOKEN"])
	require.NotNil(t, findVolumeByName(t, pod.Spec.Volumes, "llm").EmptyDir)
}

func TestTranslateContainerWithCacheAndSecrets(t *testing.T) {
	msg := newContainerFunctionMessage()
	msg.LaunchSpecification.EnvironmentB64 = encodeTextEnv(map[string]string{
		common.ContainerFunctionImageEnv: "nvcr.io/nvidia/function:latest",
		common.UtilsImageEnv:             "nvcr.io/nvidia/utils:latest",
		common.InitImageEnv:              "nvcr.io/nvidia/init:latest",
		common.ESSAgentContainerEnv:      "nvcr.io/nvidia/ess:latest",
		common.InferenceContainerEnvEnv:  base64.StdEncoding.EncodeToString([]byte(`[{"key":"INFERENCE_ENV","value":"value"}]`)),
		common.SecretsAssertionTokenEnv:  "assertion-token",
		common.FunctionSecretsPresentEnv: "true",
	})
	msg.LaunchSpecification.CacheLaunchSpecification = &common.CacheLaunchSpecification{
		CacheArtifacts: true,
		CacheHandle:    "function-cache",
		CacheSize:      1,
	}

	objs, err := Translate(
		msg,
		TranslateConfig{
			TranslateConfig: newFunctionTranslateConfig(nil),
		},
	)
	require.NoError(t, err)

	pod := findPodByName(t, objs, "0-request")
	assert.Equal(t, "value", envSliceToMap(findContainerByName(t, pod.Spec.Containers, inferenceContainerName).Env)["INFERENCE_ENV"])
	assert.Equal(t, "ess", findContainerByName(t, pod.Spec.Containers, "ess").Name)
	require.Len(t, pod.Spec.InitContainers, 2)
	assert.Equal(t, "ess-init", pod.Spec.InitContainers[1].Name)
	assert.NotNil(t, findObjectByName(t, objs, "rw-pvc-function-cache"))
	assert.NotNil(t, findObjectByName(t, objs, "writer-job-function-cache"))
}

func TestTranslateHelmChartWithCacheAndSecrets(t *testing.T) {
	msg := newHelmFunctionMessage()
	msg.LaunchSpecification.EnvironmentB64 = encodeTextEnv(map[string]string{
		common.UtilsImageEnv:             "nvcr.io/nvidia/utils:latest",
		common.InitImageEnv:              "nvcr.io/nvidia/init:latest",
		common.ESSAgentContainerEnv:      "nvcr.io/nvidia/ess:latest",
		common.SecretsAssertionTokenEnv:  "assertion-token",
		common.FunctionSecretsPresentEnv: "true",
	})
	msg.LaunchSpecification.CacheLaunchSpecification = &common.CacheLaunchSpecification{
		CacheArtifacts: true,
		CacheHandle:    "helm-function-cache",
		CacheSize:      1,
	}

	objs, err := Translate(
		msg,
		TranslateConfig{
			TranslateConfig: newFunctionTranslateConfig(nil),
		},
	)
	require.NoError(t, err)

	pod := findPodByName(t, objs, common.UtilsPodName)
	assert.Equal(t, "ess", findContainerByName(t, pod.Spec.Containers, "ess").Name)
	assert.Equal(t, common.UtilsContainerName, findContainerByName(t, pod.Spec.Containers, common.UtilsContainerName).Name)
	require.Len(t, pod.Spec.InitContainers, 2)
	assert.Equal(t, "ess-init", pod.Spec.InitContainers[1].Name)
	assert.NotNil(t, findObjectByName(t, objs, "rw-pvc-helm-function-cache"))
	assert.NotNil(t, findObjectByName(t, objs, "writer-job-helm-function-cache"))
}

func TestTranslateContainerStreamingAddsLLSEnvs(t *testing.T) {
	t.Setenv(NVCFSBSZoneDNSEnv, "http://sbs.example.com:8000")
	t.Setenv(NVCFStreamingInterfaceEnv, "CUSTOM")

	msg := newContainerFunctionMessage()
	msg.Details.FunctionType = FunctionTypeStreaming
	msg.LaunchSpecification.EnvironmentB64 = encodeTextEnv(map[string]string{
		common.ContainerFunctionImageEnv: "nvcr.io/nvidia/function:latest",
		common.NICLLSUtilsImageEnv:       "nvcr.io/nvidia/lls-utils:latest",
		common.InitImageEnv:              "nvcr.io/nvidia/init:latest",
	})

	objs, err := Translate(
		msg,
		TranslateConfig{
			TranslateConfig: newFunctionTranslateConfig(nil),
		},
	)
	require.NoError(t, err)

	utilsContainer := findContainerByName(t, findPodByName(t, objs, "0-request").Spec.Containers, common.UtilsContainerName)
	envs := envSliceToMap(utilsContainer.Env)
	assert.Equal(t, "http://sbs.example.com:8000", envs["ZONE_DNS"])
	assert.Equal(t, "CUSTOM", envs["STREAMING_INTERFACE"])
}

func TestTranslateRejectsInvalidMessages(t *testing.T) {
	cases := []struct {
		name    string
		message func() CreationQueueMessage
		config  TranslateConfig
		wantErr string
	}{
		{
			name:    "missing launch specification",
			message: func() CreationQueueMessage { return CreationQueueMessage{} },
			wantErr: "launch specification must be set",
		},
		{
			name: "launch artifacts are unsupported",
			message: func() CreationQueueMessage {
				msg := newContainerFunctionMessage()
				msg.LaunchArtifacts = LaunchArtifacts{{Type: LaunchArtifactTypePod, Specification: "pod"}}
				return msg
			},
			wantErr: "launch artifacts are not supported",
		},
		{
			name: "streaming helm function is unsupported",
			message: func() CreationQueueMessage {
				msg := newHelmFunctionMessage()
				msg.Details.FunctionType = FunctionTypeStreaming
				return msg
			},
			config: TranslateConfig{
				TranslateConfig: newFunctionTranslateConfig(nil),
			},
			wantErr: "LLS is not supported for Helm functions",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Translate(tt.message(), tt.config)
			require.EqualError(t, err, tt.wantErr)
		})
	}
}

func TestTranslateFunctionRejectsInvalidContainerInputs(t *testing.T) {
	cases := []struct {
		name            string
		env             map[string]string
		wantErr         string
		wantErrContains bool
	}{
		{
			name: "invalid inference args",
			env: map[string]string{
				common.ContainerFunctionImageEnv: "nvcr.io/nvidia/function:latest",
				common.UtilsImageEnv:             "nvcr.io/nvidia/utils:latest",
				common.InitImageEnv:              "nvcr.io/nvidia/init:latest",
				common.InferenceContainerArgsEnv: `"unterminated`,
			},
			wantErr:         "parse container args",
			wantErrContains: true,
		},
		{
			name: "missing inference image",
			env: map[string]string{
				common.UtilsImageEnv: "nvcr.io/nvidia/utils:latest",
				common.InitImageEnv:  "nvcr.io/nvidia/init:latest",
			},
			wantErr: "no inference container specified",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			msg := newContainerFunctionMessage()
			msg.LaunchSpecification.EnvironmentB64 = encodeTextEnv(tt.env)

			_, err := Translate(
				msg,
				TranslateConfig{
					TranslateConfig: newFunctionTranslateConfig(nil),
				},
			)
			if tt.wantErrContains {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.EqualError(t, err, tt.wantErr)
			}
		})
	}
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

func findContainerByName(t *testing.T, containers []corev1.Container, name string) corev1.Container {
	t.Helper()

	for _, container := range containers {
		if container.Name == name {
			return container
		}
	}

	t.Fatalf("container %q not found", name)
	return corev1.Container{}
}

func findVolumeByName(t *testing.T, volumes []corev1.Volume, name string) corev1.VolumeSource {
	t.Helper()

	for _, volume := range volumes {
		if volume.Name == name {
			return volume.VolumeSource
		}
	}

	t.Fatalf("volume %q not found", name)
	return corev1.VolumeSource{}
}

func findObjectByName(t *testing.T, objs []metav1.Object, name string) metav1.Object {
	t.Helper()

	for _, obj := range objs {
		if obj.GetName() == name {
			return obj
		}
	}

	t.Fatalf("object %q not found", name)
	return nil
}

func assertTolerationsMatch(t *testing.T, got []corev1.Toleration, want ...corev1.Toleration) {
	t.Helper()
	assert.ElementsMatch(t, want, got)
}
