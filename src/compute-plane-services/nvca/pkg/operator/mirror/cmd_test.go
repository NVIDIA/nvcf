/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package mirror

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestDecodeImagePullSecrets(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expected    []corev1.LocalObjectReference
		expectError bool
	}{
		{
			name:        "empty string returns empty slice",
			input:       "",
			expected:    []corev1.LocalObjectReference{},
			expectError: false,
		},
		{
			name:        "invalid base64 returns error",
			input:       "not-valid-base64!@#$",
			expected:    []corev1.LocalObjectReference{},
			expectError: true,
		},
		{
			name:        "invalid JSON returns error",
			input:       base64.StdEncoding.EncodeToString([]byte("not-json")),
			expected:    []corev1.LocalObjectReference{},
			expectError: true,
		},
		{
			name: "single secret",
			input: func() string {
				secrets := []corev1.LocalObjectReference{{Name: "my-secret"}}
				b, _ := json.Marshal(secrets)
				return base64.StdEncoding.EncodeToString(b)
			}(),
			expected:    []corev1.LocalObjectReference{{Name: "my-secret"}},
			expectError: false,
		},
		{
			name: "multiple secrets",
			input: func() string {
				secrets := []corev1.LocalObjectReference{
					{Name: "first-secret"},
					{Name: "second-secret"},
					{Name: "third-secret"},
				}
				b, _ := json.Marshal(secrets)
				return base64.StdEncoding.EncodeToString(b)
			}(),
			expected: []corev1.LocalObjectReference{
				{Name: "first-secret"},
				{Name: "second-secret"},
				{Name: "third-secret"},
			},
			expectError: false,
		},
		{
			name: "filters out empty names",
			input: func() string {
				secrets := []corev1.LocalObjectReference{
					{Name: "valid"},
					{Name: ""},
					{Name: "also-valid"},
					{Name: ""},
				}
				b, _ := json.Marshal(secrets)
				return base64.StdEncoding.EncodeToString(b)
			}(),
			expected: []corev1.LocalObjectReference{
				{Name: "valid"},
				{Name: "also-valid"},
			},
			expectError: false,
		},
		{
			name: "all empty names returns empty slice",
			input: func() string {
				secrets := []corev1.LocalObjectReference{
					{Name: ""},
					{Name: ""},
				}
				b, _ := json.Marshal(secrets)
				return base64.StdEncoding.EncodeToString(b)
			}(),
			expected:    []corev1.LocalObjectReference{},
			expectError: false,
		},
		{
			name: "struct format with name field",
			input: func() string {
				type secret struct {
					Name string `json:"name"`
				}
				secrets := []secret{
					{Name: "secret-one"},
					{Name: "secret-two"},
				}
				b, _ := json.Marshal(secrets)
				return base64.StdEncoding.EncodeToString(b)
			}(),
			expected: []corev1.LocalObjectReference{
				{Name: "secret-one"},
				{Name: "secret-two"},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := DecodeImagePullSecrets(tt.input)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDecodeImagePullSecrets_EmptySliceNotNil(t *testing.T) {
	// Verify that we always return an empty slice, not nil
	tests := []struct {
		name        string
		input       string
		expectError bool
	}{
		{"empty string", "", false},
		{"invalid base64", "invalid!@#$", true},
		{"invalid JSON", base64.StdEncoding.EncodeToString([]byte("not-json")), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := DecodeImagePullSecrets(tt.input)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.NotNil(t, result, "Should return empty slice, not nil")
			assert.Empty(t, result)
		})
	}
}

func TestDecodeImagePullSecrets_PreservesOrder(t *testing.T) {
	// Verify that the order of secrets is preserved
	secrets := []corev1.LocalObjectReference{
		{Name: "zebra"},
		{Name: "apple"},
		{Name: "monkey"},
		{Name: "banana"},
	}
	b, err := json.Marshal(secrets)
	require.NoError(t, err)
	input := base64.StdEncoding.EncodeToString(b)

	result, err := DecodeImagePullSecrets(input)
	require.NoError(t, err)

	assert.Len(t, result, 4)
	assert.Equal(t, "zebra", result[0].Name)
	assert.Equal(t, "apple", result[1].Name)
	assert.Equal(t, "monkey", result[2].Name)
	assert.Equal(t, "banana", result[3].Name)
}

func TestDecodeImagePullSecrets_LargeInput(t *testing.T) {
	// Test with a large number of secrets
	secrets := make([]corev1.LocalObjectReference, 100)
	for i := 0; i < 100; i++ {
		secrets[i] = corev1.LocalObjectReference{Name: "secret-" + string(rune(i))}
	}

	b, err := json.Marshal(secrets)
	require.NoError(t, err)
	input := base64.StdEncoding.EncodeToString(b)

	result, err := DecodeImagePullSecrets(input)
	require.NoError(t, err)
	assert.Len(t, result, 100)
}

func TestDecodeImagePullSecrets_SpecialCharacters(t *testing.T) {
	// Test with secrets containing special characters in names
	secrets := []corev1.LocalObjectReference{
		{Name: "my-secret-123"},
		{Name: "secret.with.dots"},
		{Name: "secret_with_underscores"},
		{Name: "UPPERCASE-SECRET"},
	}

	b, err := json.Marshal(secrets)
	require.NoError(t, err)
	input := base64.StdEncoding.EncodeToString(b)

	result, err := DecodeImagePullSecrets(input)
	require.NoError(t, err)
	assert.Equal(t, secrets, result)
}

func TestGetKubeConfig(t *testing.T) {
	tests := []struct {
		name           string
		kubeconfigPath string
		expectError    bool
		description    string
	}{
		{
			name:           "empty path uses in-cluster config",
			kubeconfigPath: "",
			expectError:    true, // Will fail when not running in cluster
			description:    "Empty path should attempt in-cluster config",
		},
		{
			name:           "invalid path returns error",
			kubeconfigPath: "/non/existent/path/kubeconfig",
			expectError:    true,
			description:    "Non-existent file should return error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := getKubeConfig(tt.kubeconfigPath)
			if tt.expectError {
				assert.Error(t, err, tt.description)
			} else {
				assert.NoError(t, err, tt.description)
			}
		})
	}
}

func TestRunAction_NoSecrets(t *testing.T) {
	// Test that runAction returns early when no secrets are configured
	// This is tested indirectly through the CLI, but we can verify the logic

	// Empty input
	secrets, err := DecodeImagePullSecrets("")
	require.NoError(t, err)
	assert.Empty(t, secrets)

	// Extract secret names
	secretNames := make([]string, 0, len(secrets))
	for _, ref := range secrets {
		secretNames = append(secretNames, ref.Name)
	}
	assert.Empty(t, secretNames)
}

func TestRunAction_ExtractsSecretNames(t *testing.T) {
	// Test the secret name extraction logic used in runAction
	secrets := []corev1.LocalObjectReference{
		{Name: "secret-one"},
		{Name: ""},
		{Name: "secret-two"},
	}

	b, err := json.Marshal(secrets)
	require.NoError(t, err)
	input := base64.StdEncoding.EncodeToString(b)

	decoded, err := DecodeImagePullSecrets(input)
	require.NoError(t, err)

	// Extract secret names (mimicking runAction logic)
	secretNames := make([]string, 0, len(decoded))
	for _, ref := range decoded {
		secretNames = append(secretNames, ref.Name)
	}

	assert.Len(t, secretNames, 2)
	assert.Equal(t, []string{"secret-one", "secret-two"}, secretNames)
}

func TestDecodeImagePullSecrets_ReservedNames(t *testing.T) {
	tests := []struct {
		name         string
		secretNames  []string
		expectError  bool
		errorMessage string
	}{
		{
			name:         "nvca-image-pull-secret is reserved",
			secretNames:  []string{"nvca-image-pull-secret"},
			expectError:  true,
			errorMessage: "secret name \"nvca-image-pull-secret\" is reserved",
		},
		{
			name:         "ngc-service-api-key is reserved",
			secretNames:  []string{"ngc-service-api-key"},
			expectError:  true,
			errorMessage: "secret name \"ngc-service-api-key\" is reserved",
		},
		{
			name:         "ngc-api-key is reserved",
			secretNames:  []string{"ngc-api-key"},
			expectError:  true,
			errorMessage: "secret name \"ngc-api-key\" is reserved",
		},
		{
			name:         "oauth-client-secret-key is reserved",
			secretNames:  []string{"oauth-client-secret-key"},
			expectError:  true,
			errorMessage: "secret name \"oauth-client-secret-key\" is reserved",
		},
		{
			name:         "oauth-client-id is reserved",
			secretNames:  []string{"oauth-client-id"},
			expectError:  true,
			errorMessage: "secret name \"oauth-client-id\" is reserved",
		},
		{
			name:         "otel-nvca-config is reserved",
			secretNames:  []string{"otel-nvca-config"},
			expectError:  true,
			errorMessage: "secret name \"otel-nvca-config\" is reserved",
		},
		{
			name:         "nvca-webhook-tls-server-certs is reserved",
			secretNames:  []string{"nvca-webhook-tls-server-certs"},
			expectError:  true,
			errorMessage: "secret name \"nvca-webhook-tls-server-certs\" is reserved",
		},
		{
			name:         "nvca-webhook-tls-ca-certs is reserved",
			secretNames:  []string{"nvca-webhook-tls-ca-certs"},
			expectError:  true,
			errorMessage: "secret name \"nvca-webhook-tls-ca-certs\" is reserved",
		},
		{
			name:        "valid secret name passes",
			secretNames: []string{"my-custom-secret"},
			expectError: false,
		},
		{
			name:        "multiple valid secrets pass",
			secretNames: []string{"my-secret-1", "my-secret-2", "another-valid-secret"},
			expectError: false,
		},
		{
			name:         "reserved name in list with valid names fails",
			secretNames:  []string{"valid-secret", "nvca-image-pull-secret", "another-valid"},
			expectError:  true,
			errorMessage: "secret name \"nvca-image-pull-secret\" is reserved",
		},
		{
			name:         "multiple reserved names fails on first one",
			secretNames:  []string{"ngc-api-key", "oauth-client-secret-key"},
			expectError:  true,
			errorMessage: "secret name \"ngc-api-key\" is reserved",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create secrets list
			secrets := make([]corev1.LocalObjectReference, len(tt.secretNames))
			for i, name := range tt.secretNames {
				secrets[i] = corev1.LocalObjectReference{Name: name}
			}

			// Encode to base64 JSON
			b, err := json.Marshal(secrets)
			require.NoError(t, err)
			input := base64.StdEncoding.EncodeToString(b)

			// Test DecodeImagePullSecrets
			result, err := DecodeImagePullSecrets(input)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMessage != "" {
					assert.Contains(t, err.Error(), tt.errorMessage)
				}
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, len(tt.secretNames), len(result))
			}
		})
	}
}

func TestReservedSecretNames_Complete(t *testing.T) {
	// Verify that all expected reserved names are in the set
	expectedReservedNames := []string{
		"nvca-image-pull-secret",
		"ngc-service-api-key",
		"ngc-api-key",
		"oauth-client-secret-key",
		"oauth-client-id",
		"otel-nvca-config",
		"nvca-webhook-tls-server-certs",
		"nvca-webhook-tls-ca-certs",
	}

	for _, name := range expectedReservedNames {
		assert.True(t, ReservedSecretNames.Has(name), "Expected %s to be in reserved names", name)
	}

	// Verify the set has exactly the expected number of entries
	assert.Equal(t, len(expectedReservedNames), ReservedSecretNames.Len(),
		"ReservedSecretNames should have exactly %d entries", len(expectedReservedNames))
}

func TestNewRunCommand(t *testing.T) {
	// Test that NewRunCommand creates a valid command structure
	cmd := NewRunCommand()

	assert.NotNil(t, cmd)
	assert.Equal(t, "run", cmd.Name)
	assert.NotEmpty(t, cmd.Usage)
	assert.NotNil(t, cmd.Action)
	assert.NotEmpty(t, cmd.Flags)

	// Verify expected flags are present
	flagNames := make(map[string]bool)
	for _, flag := range cmd.Flags {
		flagNames[flag.Names()[0]] = true
	}

	assert.True(t, flagNames["source-namespace"])
	assert.True(t, flagNames["target-namespace"])
	assert.True(t, flagNames["kubeconfig"])
	assert.True(t, flagNames["log-level"])
	assert.True(t, flagNames["resync-period"])
	assert.True(t, flagNames["additional-image-pull-secrets-b64"])
}
