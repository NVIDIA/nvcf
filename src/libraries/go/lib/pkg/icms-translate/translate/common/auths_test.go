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
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

// dockerConfigAuthsEqual compares two Docker config JSON strings by parsed auths (order-independent).
func dockerConfigAuthsEqual(a, b string) bool {
	var x, y struct {
		Auths map[string]struct{ Auth string } `json:"auths"`
	}
	if err := json.Unmarshal([]byte(a), &x); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(b), &y); err != nil {
		return false
	}
	if len(x.Auths) != len(y.Auths) {
		return false
	}
	for k, v := range x.Auths {
		if y.Auths[k] != v {
			return false
		}
	}
	return true
}

func secretDataEqual(t *testing.T, exp, got map[string]string) bool {
	t.Helper()
	if len(exp) != len(got) {
		return false
	}
	for k, expV := range exp {
		gotV, ok := got[k]
		if !ok {
			return false
		}
		if k == corev1.DockerConfigJsonKey {
			if !dockerConfigAuthsEqual(expV, gotV) {
				return false
			}
		} else if expV != gotV {
			return false
		}
	}
	return true
}

func TestImageRegistryAuthConfig_ParseAndMarshal(t *testing.T) {
	// Test JSON input
	inputJSON := `{
		"k8sSecrets": [
			{
				"auths": {
					"docker.io": {
						"auth": "JG9hdXRodG9rZW46bnZhcGktZm9vCg=="
					}
				}
			},
			{
				"auths": {
					"example.ecr.us-west-1.example.com": {
						"auth": "QVdTX0FDQ0VTU19LRVk6QVdTX1NFQ1JFVF9LRVk="
					}
				}
			},
			{
				"auths": {
					"registry.example.com": {
						"auth": "JG9hdXRodG9rZW46bnZhcGktZm9vCg=="
					}
				}
			}
		]
	}`

	// Test unmarshaling
	var config RegistryAuthConfig
	err := json.Unmarshal([]byte(inputJSON), &config)
	assert.NoError(t, err, "Failed to unmarshal JSON")

	// Validate the parsed structure
	assert.Len(t, config.K8sSecrets, 3, "Expected 3 secrets")

	// Check first secret
	assert.Contains(t, config.K8sSecrets[0].Auths, "docker.io")
	assert.Equal(t, "JG9hdXRodG9rZW46bnZhcGktZm9vCg==", config.K8sSecrets[0].Auths["docker.io"].Auth)

	// Check second secret
	assert.Contains(t, config.K8sSecrets[1].Auths, "example.ecr.us-west-1.example.com")
	assert.Equal(t, "QVdTX0FDQ0VTU19LRVk6QVdTX1NFQ1JFVF9LRVk=", config.K8sSecrets[1].Auths["example.ecr.us-west-1.example.com"].Auth)

	// Check third secret
	assert.Contains(t, config.K8sSecrets[2].Auths, "registry.example.com")
	assert.Equal(t, "JG9hdXRodG9rZW46bnZhcGktZm9vCg==", config.K8sSecrets[2].Auths["registry.example.com"].Auth)

	// Test marshaling back to JSON
	outputJSON, err := json.Marshal(config)
	assert.NoError(t, err, "Failed to marshal back to JSON")

	// Unmarshal the output to verify it matches the input
	var config2 RegistryAuthConfig
	err = json.Unmarshal(outputJSON, &config2)
	assert.NoError(t, err, "Failed to unmarshal output JSON")

	// Compare the two configs
	assert.Equal(t, config, config2, "Marshaled and unmarshaled configs should be identical")
}

func TestRegistryAuthConfig_EmptySecrets(t *testing.T) {
	// Test with empty secrets array
	inputJSON := `{
		"k8sSecrets": []
	}`

	var config RegistryAuthConfig
	err := json.Unmarshal([]byte(inputJSON), &config)
	assert.NoError(t, err, "Failed to unmarshal JSON with empty secrets")
	assert.Empty(t, config.K8sSecrets, "Expected empty secrets array")
}

func TestRegistryAuthConfig_MultipleRegistries(t *testing.T) {
	// Test with multiple registries in a single secret
	inputJSON := `{
		"k8sSecrets": [
			{
				"auths": {
					"docker.io": {
						"auth": "JG9hdXRodG9rZW46bnZhcGktZm9vCg=="
					},
					"registry.example.com": {
						"auth": "JG9hdXRodG9rZW46bnZhcGktZm9vCg=="
					}
				}
			}
		]
	}`

	var config RegistryAuthConfig
	err := json.Unmarshal([]byte(inputJSON), &config)
	assert.NoError(t, err, "Failed to unmarshal JSON with multiple registries")

	// Validate the parsed structure
	assert.Len(t, config.K8sSecrets, 1, "Expected 1 secret")
	assert.Len(t, config.K8sSecrets[0].Auths, 2, "Expected 2 registries in the secret")

	// Check both registries
	assert.Contains(t, config.K8sSecrets[0].Auths, "docker.io")
	assert.Contains(t, config.K8sSecrets[0].Auths, "registry.example.com")
	assert.Equal(t, "JG9hdXRodG9rZW46bnZhcGktZm9vCg==", config.K8sSecrets[0].Auths["docker.io"].Auth)
	assert.Equal(t, "JG9hdXRodG9rZW46bnZhcGktZm9vCg==", config.K8sSecrets[0].Auths["registry.example.com"].Auth)
}

func TestRegistryAuthConfig_InvalidJSON(t *testing.T) {
	// Test with invalid JSON
	invalidJSON := `{
		"k8sSecrets": [
			{
				"auths": {
					"docker.io": {
						"invalid_field": "value"
					}
				}
			}
		]
	}`

	var config RegistryAuthConfig
	err := json.Unmarshal([]byte(invalidJSON), &config)
	assert.NoError(t, err, "Should not error on extra fields")
	assert.Len(t, config.K8sSecrets, 1, "Should still parse the valid parts")
}

func TestRegistryAuthConfig_Base64EncodedCredentials(t *testing.T) {
	// Create the docker config JSON structure
	config := RegistryAuthConfig{
		K8sSecrets: []RegistryAuthSecret{
			{
				Auths: map[string]RegistryAuth{
					"docker.io": {
						Auth: "JG9hdXRodG9rZW46bnZhcGktZm9vCg==",
					},
					"example.ecr.us-west-1.example.com": {
						Auth: "QVdTX0FDQ0VTU19LRVk6QVdTX1NFQ1JFVF9LRVk=",
					},
					"registry.example.com": {
						Auth: "JG9hdXRodG9rZW46bnZhcGktZm9vCg==",
					},
				},
			},
		},
	}

	// Marshal to JSON
	jsonBytes, err := json.Marshal(config)
	assert.NoError(t, err, "Failed to marshal config to JSON")

	// Base64 encode the JSON
	base64Encoded := base64.StdEncoding.EncodeToString(jsonBytes)

	// Simulate the CONTAINER_REGISTRIES_CREDENTIALS environment variable
	envValue := base64Encoded

	// Decode the base64 string
	decodedBytes, err := base64.StdEncoding.DecodeString(envValue)
	assert.NoError(t, err, "Failed to decode base64 string")

	// Unmarshal the decoded JSON
	var decodedConfig RegistryAuthConfig
	err = json.Unmarshal(decodedBytes, &decodedConfig)
	assert.NoError(t, err, "Failed to unmarshal decoded JSON")

	// Verify the decoded config matches the original
	assert.Equal(t, config, decodedConfig, "Decoded config should match original")

	// Verify specific registry credentials
	assert.Len(t, decodedConfig.K8sSecrets, 1, "Expected 1 secret")
	assert.Len(t, decodedConfig.K8sSecrets[0].Auths, 3, "Expected 3 registries")

	// Check Registry Hub credentials
	assert.Contains(t, decodedConfig.K8sSecrets[0].Auths, "docker.io")
	assert.Equal(t, "JG9hdXRodG9rZW46bnZhcGktZm9vCg==", decodedConfig.K8sSecrets[0].Auths["docker.io"].Auth)

	// Check AWS ECR credentials
	assert.Contains(t, decodedConfig.K8sSecrets[0].Auths, "example.ecr.us-west-1.example.com")
	assert.Equal(t, "QVdTX0FDQ0VTU19LRVk6QVdTX1NFQ1JFVF9LRVk=", decodedConfig.K8sSecrets[0].Auths["example.ecr.us-west-1.example.com"].Auth)

	// Check NGC credentials
	assert.Contains(t, decodedConfig.K8sSecrets[0].Auths, "registry.example.com")
	assert.Equal(t, "JG9hdXRodG9rZW46bnZhcGktZm9vCg==", decodedConfig.K8sSecrets[0].Auths["registry.example.com"].Auth)
}

func TestFilterImageRegistryAuths(t *testing.T) {
	config := RegistryAuthConfig{
		K8sSecrets: []RegistryAuthSecret{
			{
				Auths: map[string]RegistryAuth{
					"docker.io": {
						Auth: "Zm9vdXNlcjpmb29wYXNzCg==",
					},
					"example.ecr.us-west-1.example.com": {
						Auth: "QVdTX0FDQ0VTU19LRVk6QVdTX1NFQ1JFVF9LRVk=",
					},
					"registry.example.com": {
						Auth: "JG9hdXRodG9rZW46bnZhcGktZm9vCg==",
					},
				},
			},
			{
				Auths: map[string]RegistryAuth{
					"docker.io": {
						Auth: "YmFydXNlcjpiYXJwYXNzCg==",
					},
					"example2.ecr.us-west-1.example.com": {
						Auth: "c29tZWF3c2NyZWQK",
					},
					"registry.example.com": {
						Auth: "JG9hdXRodG9rZW46bnZhcGktZm9vCg==",
					},
				},
			},
		},
	}

	images := []string{"public-docker:latest",
		"example.ecr.us-west-1.example.com/foo:latest",
		"no-cred.example.com/found:latest",
		"example.ecr.us-west-1.example.com/bar:latest",
		"example.ecr.us-west-1.example.com/bar@sha256:2f72cc11a6fcd0271ecef8c61056ee1eb1243be3805bf9a9df98f92f7636b05c",
	}

	secrets, err := FilterImageRegistryAuths(RegistryAuthConfig{K8sSecrets: []RegistryAuthSecret{}}, images...)
	require.NoError(t, err)
	assert.Empty(t, secrets)

	secrets, err = FilterImageRegistryAuths(config, images...)
	require.NoError(t, err)
	assert.Equal(t, []RegistryAuthSecret{
		{
			Auths: map[string]RegistryAuth{
				"docker.io": {
					Auth: "Zm9vdXNlcjpmb29wYXNzCg==",
				},
				"example.ecr.us-west-1.example.com": {
					Auth: "QVdTX0FDQ0VTU19LRVk6QVdTX1NFQ1JFVF9LRVk=",
				},
			},
		},
		{
			Auths: map[string]RegistryAuth{
				"docker.io": {
					Auth: "YmFydXNlcjpiYXJwYXNzCg==",
				},
			},
		},
	}, secrets)

	// Image parse failure
	_, err = FilterImageRegistryAuths(config, "!@#%$!")
	require.EqualError(t, err, "could not parse reference: !@#%$!")
}

func TestFilterHelmRegistryAuths(t *testing.T) {
	config := RegistryAuthConfig{
		K8sSecrets: []RegistryAuthSecret{
			{
				Auths: map[string]RegistryAuth{
					"helm.staging.example.com": {
						Auth: "JG9hdXRodG9rZW46bnZhcGktc3RnLWZvbwo=",
					},
					"helm.example.com": {
						Auth: "JG9hdXRodG9rZW46bnZhcGktZm9vCg==",
					},
					"helm.other.example.com": {
						Auth: "Zm9vdXNlcjpmb29wYXNzCg==",
					},
				},
			},
			{
				Auths: map[string]RegistryAuth{
					"helm.example.com": {
						Auth: "JG9hdXRodG9rZW46bnZhcGktYmFyCg==",
					},
					"oci.private.example.com:51001": {
						Auth: "b2NpcHJpdmF0ZXVzZXI6b2NpcHJpdmF0ZXBhc3MK",
					},
				},
			},
			{
				Auths: map[string]RegistryAuth{
					"oci.private.example.com": {
						Auth: "b2NpcHJpdmF0ZXVzZXIyOm9jaXByaXZhdGVwYXNzMgo=",
					},
				},
			},
		},
	}

	type spec struct {
		helmChartURL string
		expSecrets   []RegistryAuthSecret
		expError     string
	}

	cases := []spec{
		{
			helmChartURL: "https://helm.staging.example.com/org/repo/charts/test-chart-0.0.1.tgz",
			expSecrets: []RegistryAuthSecret{
				{
					Auths: map[string]RegistryAuth{
						"helm.staging.example.com": {
							Auth: "JG9hdXRodG9rZW46bnZhcGktc3RnLWZvbwo=",
						},
					},
				},
			},
		},
		{
			helmChartURL: "https://helm.example.com/org/repo/charts/test-chart-0.0.1.tgz",
			expSecrets: []RegistryAuthSecret{
				{
					Auths: map[string]RegistryAuth{
						"helm.example.com": {
							Auth: "JG9hdXRodG9rZW46bnZhcGktZm9vCg==",
						},
					},
				},
				{
					Auths: map[string]RegistryAuth{
						"helm.example.com": {
							Auth: "JG9hdXRodG9rZW46bnZhcGktYmFyCg==",
						},
					},
				},
			},
		},
		{
			helmChartURL: "oci://oci.private.example.com:51001/charts/test-chart-0.0.1.tgz",
			expSecrets: []RegistryAuthSecret{
				{
					Auths: map[string]RegistryAuth{
						"oci.private.example.com:51001": {
							Auth: "b2NpcHJpdmF0ZXVzZXI6b2NpcHJpdmF0ZXBhc3MK",
						},
					},
				},
			},
		},
		{
			helmChartURL: "oci://foo.example.com:51001/charts/test-chart-0.0.1.tgz",
			expSecrets:   nil,
		},
		{
			helmChartURL: ":blah",
			expError:     "parse helm chart URL: parse \":blah\": missing protocol scheme",
		},
	}

	for _, tt := range cases {
		t.Run(tt.helmChartURL, func(t *testing.T) {
			secrets, err := FilterHelmRegistryAuths(config, tt.helmChartURL)
			if tt.expError != "" {
				assert.EqualError(t, err, tt.expError)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expSecrets, secrets)
			}
		})
	}
}

func TestParseWorkloadImagePullSecrets(t *testing.T) {
	config := RegistryAuthConfig{
		K8sSecrets: []RegistryAuthSecret{
			{
				Auths: map[string]RegistryAuth{
					"registry.example.com": {
						Auth: base64.StdEncoding.EncodeToString([]byte("$oauthtoken:nvapi-foo")),
					},
					"other.example.com": {
						Auth: base64.StdEncoding.EncodeToString([]byte("otheruser:otherpass")),
					},
				},
			},
			{
				Auths: map[string]RegistryAuth{
					"private.registry.example.com:51001": {
						Auth: base64.StdEncoding.EncodeToString([]byte("privateuser:privatepass")),
					},
				},
			},
		},
	}
	CanaryConfig := RegistryAuthConfig{
		K8sSecrets: []RegistryAuthSecret{
			{
				Auths: map[string]RegistryAuth{
					"registry.example.com": {
						Auth: base64.StdEncoding.EncodeToString([]byte("$oauthtoken:nvapi-foo")),
					},
					"staging.registry.example.com": {
						Auth: base64.StdEncoding.EncodeToString([]byte("$oauthtoken:nvapi-canary-foo")),
					},
					"other.example.com": {
						Auth: base64.StdEncoding.EncodeToString([]byte("otheruser:otherpass")),
					},
				},
			},
			{
				Auths: map[string]RegistryAuth{
					"private.registry.example.com:51001": {
						Auth: base64.StdEncoding.EncodeToString([]byte("privateuser:privatepass")),
					},
				},
			},
		},
	}
	configBytes, err := json.Marshal(config)
	require.NoError(t, err)
	expRegistryAuthConfig := base64.StdEncoding.EncodeToString(configBytes)
	canaryConfigBytes, err := json.Marshal(CanaryConfig)
	require.NoError(t, err)
	expCanaryRegistryAuthConfig := base64.StdEncoding.EncodeToString(canaryConfigBytes)

	type spec struct {
		name           string
		allEnvSet      map[string]string
		isHelm         bool
		expSecretDatas map[string]map[string]string
		expError       string
	}

	cases := []spec{
		{
			name: "reg cred container",
			allEnvSet: map[string]string{
				"CONTAINER_REGISTRIES_CREDENTIALS": expRegistryAuthConfig,
				"INFERENCE_CONTAINER":              "registry.example.com/myorg/myimg:latest",
				"UTILS_CONTAINER":                  "registry.example.com/org/utilsimage:latest",
			},
			isHelm: false,
			expSecretDatas: map[string]map[string]string{
				"workload-secretname-regcred-0": {
					corev1.DockerConfigJsonKey: `{"auths":{"registry.example.com":{"auth":"JG9hdXRodG9rZW46bnZhcGktZm9v"}}}`,
				},
			},
		},
		{
			name: "reg cred container canary",
			allEnvSet: map[string]string{
				"CONTAINER_REGISTRIES_CREDENTIALS": expCanaryRegistryAuthConfig,
				"TASK_CONTAINER":                   "staging.registry.example.com/myorg/myimg:latest",
				"UTILS_CONTAINER":                  "registry.example.com/org/utilsimage:latest",
			},
			isHelm: false,
			expSecretDatas: map[string]map[string]string{
				"workload-secretname-regcred-0": {
					corev1.DockerConfigJsonKey: `{"auths":{"staging.registry.example.com":{"auth":"JG9hdXRodG9rZW46bnZhcGktY2FuYXJ5LWZvbw=="}}}`,
				},
			},
		},
		{
			name: "reg cred helm",
			allEnvSet: map[string]string{
				"CONTAINER_REGISTRIES_CREDENTIALS": expRegistryAuthConfig,
				"INFERENCE_CONTAINER":              "staging.registry.example.com/myorg/myimg:latest",
				"UTILS_CONTAINER":                  "registry.example.com/org/utilsimage:latest",
			},
			isHelm: true,
			expSecretDatas: map[string]map[string]string{
				"workload-secretname-regcred-0": {
					corev1.DockerConfigJsonKey: `{"auths":{"registry.example.com":{"auth":"JG9hdXRodG9rZW46bnZhcGktZm9v"},"other.example.com":{"auth":"b3RoZXJ1c2VyOm90aGVycGFzcw=="}}}`,
				},
				"workload-secretname-regcred-1": {
					corev1.DockerConfigJsonKey: `{"auths":{"private.registry.example.com:51001":{"auth":"cHJpdmF0ZXVzZXI6cHJpdmF0ZXBhc3M="}}}`,
				},
			},
		},
		{
			name: "reg cred helm canary",
			allEnvSet: map[string]string{
				"CONTAINER_REGISTRIES_CREDENTIALS": expCanaryRegistryAuthConfig,
				"INFERENCE_CONTAINER":              "staging.registry.example.com/myorg/myimg:latest",
				"UTILS_CONTAINER":                  "registry.example.com/org/utilsimage:latest",
			},
			isHelm: true,
			expSecretDatas: map[string]map[string]string{
				"workload-secretname-regcred-0": {
					corev1.DockerConfigJsonKey: `{"auths":{"staging.registry.example.com":{"auth":"JG9hdXRodG9rZW46bnZhcGktY2FuYXJ5LWZvbw=="},"registry.example.com":{"auth":"JG9hdXRodG9rZW46bnZhcGktZm9v"},"other.example.com":{"auth":"b3RoZXJ1c2VyOm90aGVycGFzcw=="}}}`,
				},
				"workload-secretname-regcred-1": {
					corev1.DockerConfigJsonKey: `{"auths":{"private.registry.example.com:51001":{"auth":"cHJpdmF0ZXVzZXI6cHJpdmF0ZXBhc3M="}}}`,
				},
			},
		},
		{
			name: "no cred container",
			allEnvSet: map[string]string{
				"INFERENCE_CONTAINER": "registry.example.com/myorg/myimg:latest",
				"UTILS_CONTAINER":     "registry.example.com/org/utilsimage:latest",
			},
			isHelm: false,
		},
		{
			name: "no cred container canary",
			allEnvSet: map[string]string{
				"INFERENCE_CONTAINER": "staging.registry.example.com/myorg/myimg:latest",
				"UTILS_CONTAINER":     "registry.example.com/org/utilsimage:latest",
			},
			isHelm: false,
		},
		{
			name: "no cred helm",
			allEnvSet: map[string]string{
				"INFERENCE_CONTAINER": "registry.example.com/myorg/myimg:latest",
				"UTILS_CONTAINER":     "registry.example.com/org/utilsimage:latest",
			},
			isHelm: true,
		},
		{
			name: "no cred helm canary",
			allEnvSet: map[string]string{
				"INFERENCE_CONTAINER": "staging.registry.example.com/myorg/myimg:latest",
				"UTILS_CONTAINER":     "registry.example.com/org/utilsimage:latest",
			},
			isHelm: true,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			name := "secretname"
			gotSecrets, gotErr := ParseWorkloadImagePullSecrets(name, tt.allEnvSet, tt.isHelm)
			if tt.expError != "" {
				assert.EqualError(t, gotErr, tt.expError)
			} else {
				require.NoError(t, gotErr)
				if assert.Len(t, gotSecrets, len(tt.expSecretDatas)) {
					for _, gotSecret := range gotSecrets {
						if assert.Contains(t, tt.expSecretDatas, gotSecret.Name) {
							assert.Truef(t, secretDataEqual(t, tt.expSecretDatas[gotSecret.Name], gotSecret.StringData), "secret %s data mismatch", gotSecret.Name)
							delete(tt.expSecretDatas, gotSecret.Name)
						}
					}
					assert.Empty(t, tt.expSecretDatas)
				}
			}
		})
	}
}

func TestDecodeWorkloadImageRegistryAuthConfig(t *testing.T) {
	config := RegistryAuthConfig{
		K8sSecrets: []RegistryAuthSecret{
			{
				Auths: map[string]RegistryAuth{
					"registry.example.com": {
						Auth: base64.StdEncoding.EncodeToString([]byte("$oauthtoken:nvapi-foo")),
					},
					"other.example.com": {
						Auth: base64.StdEncoding.EncodeToString([]byte("otheruser:otherpass")),
					},
				},
			},
			{
				Auths: map[string]RegistryAuth{
					"private.registry.example.com:51001": {
						Auth: base64.StdEncoding.EncodeToString([]byte("privateuser:privatepass")),
					},
				},
			},
		},
	}
	configBytes, err := json.Marshal(config)
	require.NoError(t, err)
	registryAuthConfig := base64.StdEncoding.EncodeToString(configBytes)

	type spec struct {
		name      string
		allEnvSet map[string]string
		expRACfg  RegistryAuthConfig
		expFound  bool
		expError  string
	}

	cases := []spec{
		{
			name: "reg cred container",
			allEnvSet: map[string]string{
				"CONTAINER_REGISTRIES_CREDENTIALS": registryAuthConfig,
				"UTILS_CONTAINER":                  "registry.example.com/org/utilsimage:latest",
			},
			expRACfg: RegistryAuthConfig{K8sSecrets: append([]RegistryAuthSecret{}, config.K8sSecrets...)},
			expFound: true,
		},
		{
			name: "no cred",
			allEnvSet: map[string]string{
				"UTILS_CONTAINER": "registry.example.com/org/utilsimage:latest",
			},
			expFound: false,
		},
		{
			name: "empty cred",
			allEnvSet: map[string]string{
				"CONTAINER_REGISTRIES_CREDENTIALS": base64.StdEncoding.EncodeToString([]byte(`{"k8sSecrets":[]}`)),
			},
			expRACfg: RegistryAuthConfig{K8sSecrets: []RegistryAuthSecret{}},
			expFound: false,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			gotRACfg, gotFound, gotErr := DecodeWorkloadImageRegistryAuthConfig(tt.allEnvSet)
			if tt.expError != "" {
				assert.EqualError(t, gotErr, tt.expError)
				assert.False(t, gotFound)
			} else {
				require.NoError(t, gotErr)
				assert.Equal(t, tt.expFound, gotFound)
				assert.Equal(t, tt.expRACfg, gotRACfg)
			}
		})
	}
}

func TestParseWorkerImagePullSecrets(t *testing.T) {
	workerSecret := RegistryAuthSecret{
		Auths: map[string]RegistryAuth{
			"registry.example.com": {
				Auth: base64.StdEncoding.EncodeToString([]byte("$oauthtoken:nvapi-foo")),
			},
		},
	}
	configBytes, err := json.Marshal(workerSecret)
	require.NoError(t, err)
	expRegistryAuthConfig := base64.StdEncoding.EncodeToString(configBytes)

	type spec struct {
		name           string
		allEnvSet      map[string]string
		expSecretDatas []map[string]string
		expFound       bool
		expError       string
	}

	cases := []spec{
		{
			name: "reg cred",
			allEnvSet: map[string]string{
				"SIDECAR_REGISTRY_CREDENTIAL": expRegistryAuthConfig,
				"UTILS_CONTAINER":             "registry.example.com/org/utilsimage:latest",
			},
			expSecretDatas: []map[string]string{
				{
					corev1.DockerConfigJsonKey: `{"auths":{"registry.example.com":{"auth":"JG9hdXRodG9rZW46bnZhcGktZm9v"}}}`,
				},
			},
			expFound: true,
		},
		{
			name: "no cred",
			allEnvSet: map[string]string{
				"UTILS_CONTAINER": "registry.example.com/org/utilsimage:latest",
			},
			expFound: false,
		},
		{
			name: "empty cred",
			allEnvSet: map[string]string{
				"UTILS_CONTAINER":             "registry.example.com/org/utilsimage:latest",
				"SIDECAR_REGISTRY_CREDENTIAL": base64.StdEncoding.EncodeToString([]byte(`{"k8sSecrets":[]}`)),
			},
			expFound: false,
		},
		{
			name:      "no image",
			allEnvSet: map[string]string{},
			expError:  "no worker container envs found",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			name := "secretname"
			gotSecrets, gotErr := ParseWorkerImagePullSecrets(name, tt.allEnvSet)
			if tt.expError != "" {
				assert.EqualError(t, gotErr, tt.expError)
			} else {
				require.NoError(t, gotErr)
				if assert.Len(t, gotSecrets, len(tt.expSecretDatas)) {
					for i, gotSecret := range gotSecrets {
						assert.Equal(t, tt.expSecretDatas[i], gotSecret.StringData)
					}
				}
			}
		})
	}
}

func TestParseHelmWorkloadPullSecrets(t *testing.T) {
	config := RegistryAuthConfig{
		K8sSecrets: []RegistryAuthSecret{
			{
				Auths: map[string]RegistryAuth{
					"helm.staging.example.com": {
						Auth: base64.StdEncoding.EncodeToString([]byte("stg-abc123:f25cc1b9-5b1c-4a82-a5f8-4879b56c59fc")),
					},
					"helm.example.com": {
						Auth: base64.StdEncoding.EncodeToString([]byte("$oauthtoken:nvapi-foo")),
					},
					"helm.other.example.com": {
						Auth: base64.StdEncoding.EncodeToString([]byte("otheruser:otherpass")),
					},
				},
			},
			{
				Auths: map[string]RegistryAuth{
					"oci.private.example.com:51001": {
						Auth: base64.StdEncoding.EncodeToString([]byte("ociprivateuser:ociprivatepass")),
					},
				},
			},
			{
				Auths: map[string]RegistryAuth{
					"oci.private.example.com": {
						Auth: base64.StdEncoding.EncodeToString([]byte("ociprivateuser2:ociprivatepass2")),
					},
				},
			},
		},
	}
	configBytes, err := json.Marshal(config)
	require.NoError(t, err)
	expRegistryAuthConfig := base64.StdEncoding.EncodeToString(configBytes)

	type spec struct {
		name         string
		helmChartURL string
		allEnvSet    map[string]string
		expAuthCfg   HelmAuthConfig
		expFound     bool
		expError     string
	}

	cases := []spec{
		{
			name:         "reg cred single",
			helmChartURL: "https://helm.staging.example.com/org/repo/charts/test-chart-0.0.1.tgz",
			allEnvSet: map[string]string{
				"HELM_REGISTRIES_CREDENTIALS": expRegistryAuthConfig,
			},
			expAuthCfg: HelmAuthConfig{
				K8sSecrets: []RegistryAuthSecret{
					{
						Auths: map[string]RegistryAuth{
							"helm.staging.example.com": {
								Auth: base64.StdEncoding.EncodeToString([]byte("stg-abc123:f25cc1b9-5b1c-4a82-a5f8-4879b56c59fc")),
							},
						},
					},
				},
			},
			expFound: true,
		},
		{
			name:         "non-NGC reg cred",
			helmChartURL: "oci://oci.private.example.com:51001/charts/test-chart-0.0.1.tgz",
			allEnvSet: map[string]string{
				"HELM_REGISTRIES_CREDENTIALS": expRegistryAuthConfig,
			},
			expAuthCfg: HelmAuthConfig{
				K8sSecrets: []RegistryAuthSecret{
					{
						Auths: map[string]RegistryAuth{
							"oci.private.example.com:51001": {
								Auth: base64.StdEncoding.EncodeToString([]byte("ociprivateuser:ociprivatepass")),
							},
						},
					},
				},
			},
			expFound: true,
		},
		{
			name:         "no matching creds",
			helmChartURL: "https://helm.blah.example.com/org/repo/charts/test-chart-0.0.1.tgz",
			allEnvSet: map[string]string{
				"HELM_REGISTRIES_CREDENTIALS": expRegistryAuthConfig,
			},
			expFound: false,
		},
		{
			name:         "no creds",
			helmChartURL: "http://foobar",
			expFound:     false,
		},
		{
			name: "empty creds",
			allEnvSet: map[string]string{
				"HELM_REGISTRIES_CREDENTIALS": base64.StdEncoding.EncodeToString([]byte(`{"k8sSecrets":[]}`)),
			}, helmChartURL: "http://foobar",
			expFound: false,
		},
		{
			name:         "bad helm chart url",
			helmChartURL: ":blah",
			allEnvSet: map[string]string{
				"HELM_REGISTRIES_CREDENTIALS": expRegistryAuthConfig,
			},
			expError: "filter helm registry pull secrets: parse helm chart URL: parse \":blah\": missing protocol scheme",
			expFound: false,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			gotAuthCfg, gotFound, gotErr := ParseHelmWorkloadAuthConfig(tt.helmChartURL, tt.allEnvSet)
			if tt.expError != "" {
				assert.EqualError(t, gotErr, tt.expError)
				assert.False(t, gotFound)
			} else {
				require.NoError(t, gotErr)
				assert.Equal(t, tt.expFound, gotFound)
				assert.Equal(t, tt.expAuthCfg, gotAuthCfg)
			}
		})
	}
}
