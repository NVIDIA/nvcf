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

package k8sutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGetSecretNames(t *testing.T) {
	tests := []struct {
		name          string
		secrets       []*corev1.Secret
		expectedNames []string
	}{
		{
			name:          "empty slice",
			secrets:       []*corev1.Secret{},
			expectedNames: []string{},
		},
		{
			name:          "nil slice",
			secrets:       nil,
			expectedNames: []string{},
		},
		{
			name: "single secret",
			secrets: []*corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "my-secret",
					},
				},
			},
			expectedNames: []string{"my-secret"},
		},
		{
			name: "multiple secrets",
			secrets: []*corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "registry-secret-1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "registry-secret-2",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "private-registry",
					},
				},
			},
			expectedNames: []string{"registry-secret-1", "registry-secret-2", "private-registry"},
		},
		{
			name: "secrets with other metadata",
			secrets: []*corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "secret-with-namespace",
						Namespace: "kube-system",
						Labels: map[string]string{
							"app": "test",
						},
					},
					Type: corev1.SecretTypeDockerConfigJson,
					Data: map[string][]byte{
						".dockerconfigjson": []byte("{}"),
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "another-secret",
						Namespace: "default",
					},
					Type: corev1.SecretTypeOpaque,
				},
			},
			expectedNames: []string{"secret-with-namespace", "another-secret"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetSecretNames(tt.secrets)
			assert.Equal(t, tt.expectedNames, result)
		})
	}
}

func TestGetSecretNames_PreservesOrder(t *testing.T) {
	secrets := []*corev1.Secret{
		{ObjectMeta: metav1.ObjectMeta{Name: "zebra"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "alpha"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "beta"}},
	}

	result := GetSecretNames(secrets)
	expected := []string{"zebra", "alpha", "beta"}

	assert.Equal(t, expected, result, "function should preserve the original order of secrets")
}

func TestGetSecretNames_LengthMatches(t *testing.T) {
	secrets := []*corev1.Secret{
		{ObjectMeta: metav1.ObjectMeta{Name: "secret1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "secret2"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "secret3"}},
	}

	result := GetSecretNames(secrets)

	assert.Len(t, result, len(secrets), "result slice should have same length as input")
}
