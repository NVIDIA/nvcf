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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
)

func TestNewLLMRouterClientContainer(t *testing.T) {
	type spec struct {
		name       string
		ls         *LaunchSpecification
		allEnvSet  map[string]string
		tcfg       TranslateConfig
		instanceID string
		isHelm     bool
		expError   string
		validate   func(t *testing.T, c corev1.Container)
	}

	cases := []spec{
		{
			name: "container mode with env-set LLM request router address injects legacy stargate env",
			ls:   &LaunchSpecification{},
			allEnvSet: map[string]string{
				"LLM_REQUEST_ROUTER_ADDRESS": "llm-router.example.com:443",
				"INFERENCE_PORT":             "8080",
			},
			tcfg:       TranslateConfig{},
			instanceID: "inst-123",
			isHelm:     false,
			validate: func(t *testing.T, c corev1.Container) {
				assert.Equal(t, LLMWorkerContainerName, c.Name)
				assert.Equal(t, "nvcr.io/0651155215864979/ncp-dev/stargate-client:0.4.0", llmRouterClientImageDefault)
				assert.Equal(t, llmRouterClientImageDefault, c.Image)
				assert.Equal(t, corev1.PullIfNotPresent, c.ImagePullPolicy)

				assert.Contains(t, c.Args, "--upstream-http-base-url=http://127.0.0.1:8080")
				assert.Contains(t, c.Args, "--stargate-address=llm-router.example.com:443")
				envMap := envSliceToMap(c.Env)
				assert.Equal(t, "llm-router.example.com:443", envMap["LLM_REQUEST_ROUTER_ADDRESS"])
				assert.Equal(t, "llm-router.example.com:443", envMap["STARGATE_ADDRESS"])
				assert.Contains(t, c.Args, "--inference-server-id=inst-123")
				assert.Contains(t, c.Args, "--auth-token-file=/var/run/llm/worker-token")
				assert.Contains(t, c.Args, "--reverse-tunnel")
				assert.NotContains(t, c.Args, "--quic-insecure")
			},
		},
		{
			name: "configured default stargate address without env returns error",
			ls:   &LaunchSpecification{},
			allEnvSet: map[string]string{
				"INFERENCE_PORT": "9090",
			},
			tcfg: TranslateConfig{
				DefaultStargateAddress: "default-stargate.example.com:443",
			},
			expError: "LLM request router address is not set " +
				"(LLM_REQUEST_ROUTER_ADDRESS env or STARGATE_ADDRESS legacy env)",
		},
		{
			name: "LLM request router env controls when both env names are present",
			ls:   &LaunchSpecification{},
			allEnvSet: map[string]string{
				"LLM_REQUEST_ROUTER_ADDRESS": "llm-router.example.com:443",
				"STARGATE_ADDRESS":           "legacy-router.example.com:443",
				"INFERENCE_PORT":             "8080",
			},
			tcfg:       TranslateConfig{},
			instanceID: "inst-both",
			isHelm:     false,
			validate: func(t *testing.T, c corev1.Container) {
				assert.Contains(t, c.Args, "--stargate-address=llm-router.example.com:443")
				envMap := envSliceToMap(c.Env)
				assert.Equal(t, "llm-router.example.com:443", envMap["LLM_REQUEST_ROUTER_ADDRESS"])
				assert.Equal(t, "legacy-router.example.com:443", envMap["STARGATE_ADDRESS"])
			},
		},
		{
			name: "helm mode without namespace uses service name",
			ls:   &LaunchSpecification{},
			allEnvSet: map[string]string{
				"STARGATE_ADDRESS":                  "stargate.example.com:443",
				"INFERENCE_PORT":                    "8080",
				"HELM_CHART_INFERENCE_SERVICE_NAME": "my-inference-svc",
			},
			tcfg:       TranslateConfig{},
			instanceID: "inst-789",
			isHelm:     true,
			validate: func(t *testing.T, c corev1.Container) {
				assert.Contains(t, c.Args, "--upstream-http-base-url=http://my-inference-svc:8080")
			},
		},
		{
			name: "helm mode with namespace",
			ls:   &LaunchSpecification{},
			allEnvSet: map[string]string{
				"STARGATE_ADDRESS":                  "stargate.example.com:443",
				"INFERENCE_PORT":                    "8080",
				"HELM_CHART_INFERENCE_SERVICE_NAME": "my-inference-svc",
			},
			tcfg: TranslateConfig{
				TranslateConfig: common.TranslateConfig{
					Namespace: "my-namespace",
				},
			},
			instanceID: "inst-789",
			isHelm:     true,
			validate: func(t *testing.T, c corev1.Container) {
				assert.Contains(t, c.Args, "--upstream-http-base-url=http://my-inference-svc.my-namespace.svc.cluster.local:8080")
			},
		},
		{
			name: "QUIC insecure enabled",
			ls:   &LaunchSpecification{},
			allEnvSet: map[string]string{
				"STARGATE_ADDRESS": "stargate.example.com:443",
				"INFERENCE_PORT":   "8080",
			},
			tcfg: TranslateConfig{
				StargateQUICInsecure: true,
			},
			instanceID: "inst-quic",
			isHelm:     false,
			validate: func(t *testing.T, c corev1.Container) {
				assert.Contains(t, c.Args, "--quic-insecure")
			},
		},
		{
			name: "custom router client image from env",
			ls:   &LaunchSpecification{},
			allEnvSet: map[string]string{
				llmRouterClientImageEnv: "custom-registry.io/llm-router:v1",
				"STARGATE_ADDRESS":      "stargate.example.com:443",
				"INFERENCE_PORT":        "8080",
			},
			tcfg:       TranslateConfig{},
			instanceID: "inst-custom",
			isHelm:     false,
			validate: func(t *testing.T, c corev1.Container) {
				assert.Equal(t, "custom-registry.io/llm-router:v1", c.Image)
			},
		},
		{
			name: "models with LLM config produce --model-name args",
			ls: &LaunchSpecification{
				Models: Models{
					{Name: "model-a", LLMModelConfig: &LLMModelConfig{Tokenizer: "tok-a"}},
					{Name: "model-b"},
					{Name: "model-c", LLMModelConfig: &LLMModelConfig{Tokenizer: "tok-c"}},
				},
			},
			allEnvSet: map[string]string{
				"STARGATE_ADDRESS": "stargate.example.com:443",
				"INFERENCE_PORT":   "8080",
			},
			tcfg:       TranslateConfig{},
			instanceID: "inst-models",
			isHelm:     false,
			validate: func(t *testing.T, c corev1.Container) {
				assert.Contains(t, c.Args, "--model-name=model-a")
				assert.NotContains(t, c.Args, "--model-name=model-b")
				assert.Contains(t, c.Args, "--model-name=model-c")
			},
		},
		{
			name: "no models produces no --model-name args",
			ls:   &LaunchSpecification{},
			allEnvSet: map[string]string{
				"STARGATE_ADDRESS": "stargate.example.com:443",
				"INFERENCE_PORT":   "8080",
			},
			tcfg:       TranslateConfig{},
			instanceID: "inst-no-models",
			isHelm:     false,
			validate: func(t *testing.T, c corev1.Container) {
				for _, arg := range c.Args {
					assert.NotContains(t, arg, "--model-name=")
				}
			},
		},
		{
			name:      "missing request router address from env and config returns error",
			ls:        &LaunchSpecification{},
			allEnvSet: map[string]string{},
			tcfg:      TranslateConfig{},
			expError: "LLM request router address is not set " +
				"(LLM_REQUEST_ROUTER_ADDRESS env or STARGATE_ADDRESS legacy env)",
		},
		{
			name: "request router env overrides config default",
			ls:   &LaunchSpecification{},
			allEnvSet: map[string]string{
				"LLM_REQUEST_ROUTER_ADDRESS": "env-llm-router.example.com:443",
				"INFERENCE_PORT":             "8080",
			},
			tcfg: TranslateConfig{
				DefaultStargateAddress: "config-stargate.example.com:443",
			},
			instanceID: "inst-override",
			isHelm:     false,
			validate: func(t *testing.T, c corev1.Container) {
				assert.Contains(t, c.Args, "--stargate-address=env-llm-router.example.com:443")
			},
		},
		{
			name: "resource requests and limits are set",
			ls:   &LaunchSpecification{},
			allEnvSet: map[string]string{
				"STARGATE_ADDRESS": "stargate.example.com:443",
				"INFERENCE_PORT":   "8080",
			},
			tcfg:       TranslateConfig{},
			instanceID: "inst-resources",
			isHelm:     false,
			validate: func(t *testing.T, c corev1.Container) {
				assert.Equal(t, *resource.NewMilliQuantity(500, resource.DecimalSI), c.Resources.Requests[corev1.ResourceCPU])
				assert.Equal(t, *resource.NewQuantity(512*1<<20, resource.BinarySI), c.Resources.Requests[corev1.ResourceMemory])
				assert.Equal(t, *resource.NewMilliQuantity(1000, resource.DecimalSI), c.Resources.Limits[corev1.ResourceCPU])
				assert.Equal(t, *resource.NewQuantity(1024*1<<20, resource.BinarySI), c.Resources.Limits[corev1.ResourceMemory])
			},
		},
		{
			name: "volume mount is configured",
			ls:   &LaunchSpecification{},
			allEnvSet: map[string]string{
				"STARGATE_ADDRESS": "stargate.example.com:443",
				"INFERENCE_PORT":   "8080",
			},
			tcfg:       TranslateConfig{},
			instanceID: "inst-vol",
			isHelm:     false,
			validate: func(t *testing.T, c corev1.Container) {
				require.Len(t, c.VolumeMounts, 1)
				assert.Equal(t, "llm", c.VolumeMounts[0].Name)
				assert.Equal(t, "/var/run/llm", c.VolumeMounts[0].MountPath)
			},
		},
		{
			name: "INSTANCE_ID and SHARED_CONFIG_DIR and WORKER_TOKEN_PATH envs are set",
			ls:   &LaunchSpecification{},
			allEnvSet: map[string]string{
				"STARGATE_ADDRESS": "stargate.example.com:443",
				"INFERENCE_PORT":   "8080",
			},
			tcfg:       TranslateConfig{},
			instanceID: "inst-env-check",
			isHelm:     false,
			validate: func(t *testing.T, c corev1.Container) {
				envMap := envSliceToMap(c.Env)
				assert.Equal(t, "inst-env-check", envMap["INSTANCE_ID"])
				assert.Equal(t, ConfigDirPath, envMap["SHARED_CONFIG_DIR"])
				assert.Equal(t, "/var/run/llm/worker-token", envMap["WORKER_TOKEN_PATH"])
				assert.Equal(t, "stargate.example.com:443", envMap["LLM_REQUEST_ROUTER_ADDRESS"])
				assert.Equal(t, "stargate.example.com:443", envMap["STARGATE_ADDRESS"])
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			c, err := newLLMRouterClientContainer(tt.ls, tt.allEnvSet, tt.tcfg, tt.instanceID, tt.isHelm)
			if tt.expError != "" {
				assert.EqualError(t, err, tt.expError)
				return
			}
			require.NoError(t, err)
			tt.validate(t, c)
		})
	}
}

func TestNewLLMCredentialManagerContainer(t *testing.T) {
	type spec struct {
		name      string
		allEnvSet map[string]string
		tcfg      TranslateConfig
		validate  func(t *testing.T, c corev1.Container)
	}

	cases := []spec{
		{
			name: "default credential manager image",
			allEnvSet: map[string]string{
				"NVCF_WORKER_TOKEN":   "test-token",
				"NVCF_FQDN_GRPC":      "grpc.example.com",
				"FUNCTION_ID":         "func-123",
				"FUNCTION_VERSION_ID": "ver-456",
				"NCA_ID":              "nca-789",
			},
			tcfg: TranslateConfig{},
			validate: func(t *testing.T, c corev1.Container) {
				assert.Equal(t, "llm-credential-manager", c.Name)
				assert.Equal(t, llmCredentialManagerImageDefault, c.Image)
				assert.Equal(t, corev1.PullIfNotPresent, c.ImagePullPolicy)
			},
		},
		{
			name: "custom credential manager image from env",
			allEnvSet: map[string]string{
				llmCredentialManagerImageEnv: "custom-registry.io/cred-mgr:v2",
			},
			tcfg: TranslateConfig{},
			validate: func(t *testing.T, c corev1.Container) {
				assert.Equal(t, "custom-registry.io/cred-mgr:v2", c.Image)
			},
		},
		{
			name: "env vars are propagated",
			allEnvSet: map[string]string{
				"NVCF_WORKER_TOKEN":   "my-token",
				"NVCF_FQDN_GRPC":      "grpc.test.com",
				"FUNCTION_ID":         "f-001",
				"FUNCTION_VERSION_ID": "fv-002",
				"NCA_ID":              "n-003",
			},
			tcfg: TranslateConfig{},
			validate: func(t *testing.T, c corev1.Container) {
				envMap := envSliceToMap(c.Env)
				assert.Equal(t, "my-token", envMap["NVCF_WORKER_TOKEN"])
				assert.Equal(t, "grpc.test.com", envMap["NVCF_FQDN_GRPC"])
				assert.Equal(t, "f-001", envMap["FUNCTION_ID"])
				assert.Equal(t, "fv-002", envMap["FUNCTION_VERSION_ID"])
				assert.Equal(t, "n-003", envMap["NCA_ID"])
				assert.Equal(t, ConfigDirPath, envMap["SHARED_CONFIG_DIR"])
				assert.Equal(t, "/var/run/llm/worker-token", envMap["WORKER_TOKEN_PATH"])
			},
		},
		{
			name:      "empty env set uses defaults for image and empty env values",
			allEnvSet: map[string]string{},
			tcfg:      TranslateConfig{},
			validate: func(t *testing.T, c corev1.Container) {
				assert.Equal(t, llmCredentialManagerImageDefault, c.Image)
				envMap := envSliceToMap(c.Env)
				assert.Equal(t, "", envMap["NVCF_WORKER_TOKEN"])
				assert.Equal(t, "", envMap["NVCF_FQDN_GRPC"])
			},
		},
		{
			name:      "resource requests and limits",
			allEnvSet: map[string]string{},
			tcfg:      TranslateConfig{},
			validate: func(t *testing.T, c corev1.Container) {
				assert.Equal(t, *resource.NewMilliQuantity(125, resource.DecimalSI), c.Resources.Requests[corev1.ResourceCPU])
				assert.Equal(t, *resource.NewQuantity(64*1<<20, resource.BinarySI), c.Resources.Requests[corev1.ResourceMemory])
				assert.Equal(t, *resource.NewMilliQuantity(250, resource.DecimalSI), c.Resources.Limits[corev1.ResourceCPU])
				assert.Equal(t, *resource.NewQuantity(128*1<<20, resource.BinarySI), c.Resources.Limits[corev1.ResourceMemory])
			},
		},
		{
			name:      "volume mount is configured",
			allEnvSet: map[string]string{},
			tcfg:      TranslateConfig{},
			validate: func(t *testing.T, c corev1.Container) {
				require.Len(t, c.VolumeMounts, 1)
				assert.Equal(t, "llm", c.VolumeMounts[0].Name)
				assert.Equal(t, "/var/run/llm", c.VolumeMounts[0].MountPath)
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			c, err := newLLMCredentialManagerContainer(tt.allEnvSet, tt.tcfg)
			require.NoError(t, err)
			tt.validate(t, c)
		})
	}
}

func TestNewLLMClientVolumes(t *testing.T) {
	vols := newLLMClientVolumes()

	require.Len(t, vols, 1)
	assert.Equal(t, "llm", vols[0].Name)
	require.NotNil(t, vols[0].VolumeSource.EmptyDir)
}

func envSliceToMap(envs []corev1.EnvVar) map[string]string {
	m := make(map[string]string, len(envs))
	for _, e := range envs {
		if e.ValueFrom == nil {
			m[e.Name] = e.Value
		}
	}
	return m
}
