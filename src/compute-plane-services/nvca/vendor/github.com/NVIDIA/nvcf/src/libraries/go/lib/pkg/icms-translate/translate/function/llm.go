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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
)

const (
	LLMWorkerContainerName = "llm-worker"

	//nolint:gosec
	llmCredentialManagerImageEnv = "LLM_CREDENTIAL_MANAGER_IMAGE"
	//nolint:gosec
	llmCredentialManagerImageDefault = "nvcr.io/0651155215864979/ncp-dev/nvcf_worker_llm_credentials:2.109.0"
	llmRouterClientImageEnv          = "LLM_ROUTER_CLIENT_IMAGE"
	llmRouterClientImageDefault      = "nvcr.io/0651155215864979/ncp-dev/stargate-client:0.4.0"
	llmRequestRouterAddressEnv       = "LLM_REQUEST_ROUTER_ADDRESS"
	legacyStargateAddressEnv         = "STARGATE_ADDRESS"

	llmDirMountPath    = "/var/run/llm"
	llmWorkerTokenPath = llmDirMountPath + "/worker-token"
)

func normalizeLLMRequestRouterAddressEnvAliases(envSet map[string]string) {
	llmRequestRouterAddress := envSet[llmRequestRouterAddressEnv]
	if llmRequestRouterAddress == "" {
		llmRequestRouterAddress = envSet[legacyStargateAddressEnv]
	}
	if llmRequestRouterAddress == "" {
		return
	}
	if envSet[llmRequestRouterAddressEnv] == "" {
		envSet[llmRequestRouterAddressEnv] = llmRequestRouterAddress
	}
	if envSet[legacyStargateAddressEnv] == "" {
		envSet[legacyStargateAddressEnv] = llmRequestRouterAddress
	}
}

func newLLMRouterClientContainer(
	ls *LaunchSpecification,
	allEnvSet map[string]string,
	tcfg TranslateConfig,
	instanceID string,
	isHelm bool,
) (corev1.Container, error) {
	llmRouterClientImage := allEnvSet[llmRouterClientImageEnv]
	if llmRouterClientImage == "" {
		llmRouterClientImage = llmRouterClientImageDefault
		// return corev1.Container{}, fmt.Errorf("LLM router client image is not set")
	}
	llmRequestRouterAddress := allEnvSet[llmRequestRouterAddressEnv]
	if llmRequestRouterAddress == "" {
		llmRequestRouterAddress = allEnvSet[legacyStargateAddressEnv]
	}
	if llmRequestRouterAddress == "" {
		return corev1.Container{}, fmt.Errorf(
			"LLM request router address is not set (%s env or %s legacy env)",
			llmRequestRouterAddressEnv,
			legacyStargateAddressEnv,
		)
	}

	llmEnvSet := make(map[string]string, len(allEnvSet)+2)
	for k, v := range allEnvSet {
		llmEnvSet[k] = v
	}
	if llmEnvSet[llmRequestRouterAddressEnv] == "" && llmEnvSet[legacyStargateAddressEnv] == "" {
		llmEnvSet[llmRequestRouterAddressEnv] = llmRequestRouterAddress
	}
	normalizeLLMRequestRouterAddressEnvAliases(llmEnvSet)

	envs := common.MapToEnv(llmEnvSet)
	envs = append(envs,
		corev1.EnvVar{
			Name:  "INSTANCE_ID",
			Value: instanceID,
		},
		corev1.EnvVar{
			Name:  "SHARED_CONFIG_DIR",
			Value: ConfigDirPath,
		},
		corev1.EnvVar{
			Name:  "WORKER_TOKEN_PATH",
			Value: llmWorkerTokenPath,
		},
	)

	var upstreamHttpBaseUrl string
	svcPort := allEnvSet["INFERENCE_PORT"]
	if isHelm {
		svcName := allEnvSet["HELM_CHART_INFERENCE_SERVICE_NAME"]
		if tcfg.Namespace == "" {
			upstreamHttpBaseUrl = fmt.Sprintf("http://%s:%s", svcName, svcPort)
		} else {
			upstreamHttpBaseUrl = fmt.Sprintf("http://%s.%s.svc.cluster.local:%s", svcName, tcfg.Namespace, svcPort)
		}
	} else {
		upstreamHttpBaseUrl = fmt.Sprintf("http://127.0.0.1:%s", svcPort)
	}

	args := []string{
		fmt.Sprintf("--upstream-http-base-url=%s", upstreamHttpBaseUrl),
		fmt.Sprintf("--stargate-address=%s", llmRequestRouterAddress),
		fmt.Sprintf("--inference-server-id=%s", instanceID),
		fmt.Sprintf("--auth-token-file=%s", llmWorkerTokenPath),
		"--backend-connectivity=reverse",
		"--initial-input-tps=100",
	}
	if tcfg.StargateQUICInsecure {
		args = append(args, "--quic-insecure")
	}

	for _, model := range ls.Models {
		// Only include a model name if the LLM model config is present.
		if model.LLMModelConfig != nil {
			args = append(args, fmt.Sprintf("--model-name=%s", model.Name))
		}
	}

	c := corev1.Container{
		Name:            LLMWorkerContainerName,
		Image:           llmRouterClientImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Args:            args,
		Env:             common.SortEnvs(envs),
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewMilliQuantity(500, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(512*1<<20, resource.BinarySI),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewMilliQuantity(1000, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(1024*1<<20, resource.BinarySI),
			},
		},
		// TODO: add probes once /health and /ready are implemented
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "llm",
				MountPath: llmDirMountPath,
			},
			// config-data backs SHARED_CONFIG_DIR, shared with the credential
			// manager. Mount it so the nvcf client does not fail creating
			// ConfigDirPath at startup.
			{
				Name:      "config-data",
				MountPath: ConfigDirPath,
			},
		},
	}

	return c, nil
}

func newLLMCredentialManagerContainer(allEnvSet map[string]string, _ TranslateConfig) (corev1.Container, error) {
	llmCredentialManagerImage := allEnvSet[llmCredentialManagerImageEnv]
	if llmCredentialManagerImage == "" {
		llmCredentialManagerImage = llmCredentialManagerImageDefault
		// return corev1.Container{}, fmt.Errorf("LLM credential manager image is not set")
	}

	envs := common.MakeWorkloadEnvVars(common.FunctionCreationAction)
	envs = append(envs,
		corev1.EnvVar{
			Name:  "NVCF_WORKER_TOKEN",
			Value: allEnvSet["NVCF_WORKER_TOKEN"],
		},
		corev1.EnvVar{
			Name:  "NVCF_FQDN_GRPC",
			Value: allEnvSet["NVCF_FQDN_GRPC"],
		},
		corev1.EnvVar{
			Name:  "FUNCTION_ID",
			Value: allEnvSet["FUNCTION_ID"],
		},
		corev1.EnvVar{
			Name:  "FUNCTION_VERSION_ID",
			Value: allEnvSet["FUNCTION_VERSION_ID"],
		},
		corev1.EnvVar{
			Name:  "NCA_ID",
			Value: allEnvSet["NCA_ID"],
		},
		corev1.EnvVar{
			Name:  "SHARED_CONFIG_DIR",
			Value: ConfigDirPath,
		},
		corev1.EnvVar{
			Name:  "WORKER_TOKEN_PATH",
			Value: llmWorkerTokenPath,
		},
	)

	c := corev1.Container{
		Name:            "llm-credential-manager",
		Image:           llmCredentialManagerImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Env:             common.SortEnvs(envs),
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewMilliQuantity(125, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(64*1<<20, resource.BinarySI),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewMilliQuantity(250, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(128*1<<20, resource.BinarySI),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "llm",
				MountPath: llmDirMountPath,
			},
			// config-data backs SHARED_CONFIG_DIR. The credential manager
			// creates and caches the worker token here, so it must be mounted.
			{
				Name:      "config-data",
				MountPath: ConfigDirPath,
			},
		},
	}
	return c, nil
}

func newLLMClientVolumes() []corev1.Volume {
	return []corev1.Volume{
		{
			Name: "llm",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}
}
