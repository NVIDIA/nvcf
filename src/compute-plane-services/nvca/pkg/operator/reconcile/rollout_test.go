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

package operator

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
)

func TestHasNGCServiceAPIKeyChangedCheck(t *testing.T) {
	tests := []struct {
		name           string
		currentToken   string
		fetcherErr     error
		secretToken    []byte
		secretGetErr   error
		expectedResult bool
	}{
		{
			name:           "no change in API key",
			currentToken:   "test-token",
			secretToken:    []byte("test-token"),
			expectedResult: false,
		},
		{
			name:           "API key changed",
			currentToken:   "new-token",
			secretToken:    []byte("old-token"),
			expectedResult: true,
		},
		{
			name:           "fetcher error",
			currentToken:   "test-token",
			fetcherErr:     assert.AnError,
			expectedResult: false,
		},
		{
			name:           "secret getter error",
			currentToken:   "test-token",
			secretGetErr:   assert.AnError,
			expectedResult: false,
		},
		{
			name:           "secret missing expected data key",
			currentToken:   "test-token",
			secretToken:    nil,  // This will cause the secret to not have the expected key
			expectedResult: true, // Should detect change since key is missing
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fetcher := &mockTokenFetcher{
				token: tt.currentToken,
				err:   tt.fetcherErr,
			}

			secretGetter := func(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.Secret, error) {
				if tt.secretGetErr != nil {
					return nil, tt.secretGetErr
				}
				secretData := map[string][]byte{}
				if tt.secretToken != nil {
					secretData[NGCServiceAPIKeySecretName] = tt.secretToken
				}
				return &corev1.Secret{
					Data: secretData,
				}, nil
			}

			checker := hasNGCServiceAPIKeyChangedCheck(context.Background(), fetcher, secretGetter)
			result := checker()
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestHasImagePullSecretChangedCheck(t *testing.T) {
	tests := []struct {
		name             string
		currentToken     string
		fetcherErr       error
		nvcaSystemToken  string
		secretGetErr     error
		repoServer       string
		storedRepoServer string // repo server used to create the stored secret
		expectedResult   bool
	}{
		{
			name:             "no change in pull secret",
			currentToken:     "test-token",
			repoServer:       "nvcr.io",
			storedRepoServer: "nvcr.io",
			nvcaSystemToken:  "test-token",
			expectedResult:   false,
		},
		{
			name:             "pull secret changed",
			currentToken:     "test-token",
			repoServer:       "nvcr.io",
			storedRepoServer: "nvcr.io",
			nvcaSystemToken:  "test-token-older",
			expectedResult:   true,
		},
		{
			name:           "fetcher error",
			fetcherErr:     assert.AnError,
			expectedResult: false,
		},
		{
			name:           "secret getter error",
			currentToken:   "test-token",
			secretGetErr:   assert.AnError,
			expectedResult: false,
		},
		{
			name:             "secret missing dockerconfigjson key",
			currentToken:     "test-token",
			repoServer:       "nvcr.io",
			storedRepoServer: "nvcr.io",
			nvcaSystemToken:  "",
			expectedResult:   true, // Should detect change since content differs
		},
		{
			name:             "different repo server",
			currentToken:     "test-token",
			repoServer:       "different.repo.io",
			storedRepoServer: "nvcr.io", // stored secret was created with different repo server
			nvcaSystemToken:  "test-token",
			expectedResult:   true, // Should detect change due to different repo server
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fetcher := &mockTokenFetcher{
				token: tt.currentToken,
				err:   tt.fetcherErr,
			}

			var secretData []byte
			var err error
			storedRepoServer := tt.storedRepoServer
			if storedRepoServer == "" {
				storedRepoServer = tt.repoServer // default to same repo server if not specified
			}
			if tt.nvcaSystemToken != "" && storedRepoServer != "" {
				secretData, err = getImagePullSecretDockerConfigJSONFromNGCKey(storedRepoServer, tt.nvcaSystemToken)
				require.NoError(t, err)
			}

			secretGetter := func(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.Secret, error) {
				if tt.secretGetErr != nil {
					return nil, tt.secretGetErr
				}
				secretMap := map[string][]byte{}
				if len(secretData) > 0 {
					secretMap[".dockerconfigjson"] = secretData
				}
				return &corev1.Secret{
					Data: secretMap,
				}, nil
			}

			checker := hasImagePullSecretChangedCheck(context.Background(), fetcher, secretGetter, tt.repoServer)
			result := checker()
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestHasNVCFBackendChangedCheck(t *testing.T) {
	tests := []struct {
		name     string
		backend  *nvidiaiov1.NVCFBackend
		expected bool
	}{
		{
			name: "no changes",
			backend: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						Version: "1.0.0",
						AccountConfig: nvidiaiov1.AccountConfig{
							NCAID: "test-id",
						},
					},
				},
				Status: nvidiaiov1.NVCFBackendStatus{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						Version: "1.0.0",
						AccountConfig: nvidiaiov1.AccountConfig{
							NCAID: "test-id",
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "version changed",
			backend: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						Version: "1.0.1",
						AccountConfig: nvidiaiov1.AccountConfig{
							NCAID: "test-id",
						},
					},
				},
				Status: nvidiaiov1.NVCFBackendStatus{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						Version: "1.0.0",
						AccountConfig: nvidiaiov1.AccountConfig{
							NCAID: "test-id",
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "spec changed",
			backend: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						Version: "1.0.0",
						AccountConfig: nvidiaiov1.AccountConfig{
							NCAID: "new-id",
						},
					},
				},
				Status: nvidiaiov1.NVCFBackendStatus{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						Version: "1.0.0",
						AccountConfig: nvidiaiov1.AccountConfig{
							NCAID: "test-id",
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "empty status",
			backend: &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						Version: "1.0.0",
						AccountConfig: nvidiaiov1.AccountConfig{
							NCAID: "test-id",
						},
					},
				},
				Status: nvidiaiov1.NVCFBackendStatus{},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := hasNVCFBackendChangedCheck(newTestContext(), tt.backend)
			result := checker()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestHasNVCFBackendRolloutAnnotationChanged(t *testing.T) {
	tests := []struct {
		name            string
		oldAnnotations  map[string]string
		newAnnotations  map[string]string
		expectedChanged bool
	}{
		{
			name:            "no annotations in either",
			oldAnnotations:  map[string]string{},
			newAnnotations:  map[string]string{},
			expectedChanged: false,
		},
		{
			name:            "no rollout annotation in new",
			oldAnnotations:  map[string]string{NVCFBackendForceRolloutAnnotation: "2023-01-01T10:00:00Z"},
			newAnnotations:  map[string]string{},
			expectedChanged: false,
		},
		{
			name:            "empty rollout annotation in new",
			oldAnnotations:  map[string]string{NVCFBackendForceRolloutAnnotation: "2023-01-01T10:00:00Z"},
			newAnnotations:  map[string]string{NVCFBackendForceRolloutAnnotation: ""},
			expectedChanged: false,
		},
		{
			name:            "rollout annotation added in new",
			oldAnnotations:  map[string]string{},
			newAnnotations:  map[string]string{NVCFBackendForceRolloutAnnotation: "2023-01-01T10:00:00Z"},
			expectedChanged: true,
		},
		{
			name:            "rollout annotation value changed",
			oldAnnotations:  map[string]string{NVCFBackendForceRolloutAnnotation: "2023-01-01T10:00:00Z"},
			newAnnotations:  map[string]string{NVCFBackendForceRolloutAnnotation: "2023-01-02T10:00:00Z"},
			expectedChanged: true,
		},
		{
			name:            "rollout annotation value unchanged",
			oldAnnotations:  map[string]string{NVCFBackendForceRolloutAnnotation: "2023-01-01T10:00:00Z"},
			newAnnotations:  map[string]string{NVCFBackendForceRolloutAnnotation: "2023-01-01T10:00:00Z"},
			expectedChanged: false,
		},
		{
			name:            "other annotations unchanged, no rollout annotation",
			oldAnnotations:  map[string]string{"other-annotation": "value"},
			newAnnotations:  map[string]string{"other-annotation": "value"},
			expectedChanged: false,
		},
		{
			name: "other annotations changed, rollout annotation unchanged",
			oldAnnotations: map[string]string{
				"other-annotation":                "old-value",
				NVCFBackendForceRolloutAnnotation: "2023-01-01T10:00:00Z",
			},
			newAnnotations: map[string]string{
				"other-annotation":                "new-value",
				NVCFBackendForceRolloutAnnotation: "2023-01-01T10:00:00Z",
			},
			expectedChanged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hasNVCFBackendRolloutAnnotationChanged(context.Background(), tt.oldAnnotations, tt.newAnnotations)
			assert.Equal(t, tt.expectedChanged, result)
		})
	}
}

func TestHasNetworkConfigChangedCheck(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name                          string
		currentDDCSIPAllowList        []string
		statusDDCSIPAllowList         []string
		currentK8sClusterNetworkCIDRs []string
		statusK8sClusterNetworkCIDRs  []string
		expectedResult                bool
	}{
		{
			name:                          "no change in network config",
			currentDDCSIPAllowList:        []string{"1.2.3.4/32", "5.6.7.8/32"},
			statusDDCSIPAllowList:         []string{"1.2.3.4/32", "5.6.7.8/32"},
			currentK8sClusterNetworkCIDRs: []string{"10.0.0.0/8", "172.16.0.0/12"},
			statusK8sClusterNetworkCIDRs:  []string{"10.0.0.0/8", "172.16.0.0/12"},
			expectedResult:                false,
		},
		{
			name:                          "change in DDCS IP allow list",
			currentDDCSIPAllowList:        []string{"1.2.3.4/32", "9.10.11.12/32"},
			statusDDCSIPAllowList:         []string{"1.2.3.4/32", "5.6.7.8/32"},
			currentK8sClusterNetworkCIDRs: []string{"10.0.0.0/8", "172.16.0.0/12"},
			statusK8sClusterNetworkCIDRs:  []string{"10.0.0.0/8", "172.16.0.0/12"},
			expectedResult:                true,
		},
		{
			name:                          "change in K8s cluster network CIDRs",
			currentDDCSIPAllowList:        []string{"1.2.3.4/32", "5.6.7.8/32"},
			statusDDCSIPAllowList:         []string{"1.2.3.4/32", "5.6.7.8/32"},
			currentK8sClusterNetworkCIDRs: []string{"10.0.0.0/8", "192.168.0.0/16"},
			statusK8sClusterNetworkCIDRs:  []string{"10.0.0.0/8", "172.16.0.0/12"},
			expectedResult:                true,
		},
		{
			name:                          "change in both network configs",
			currentDDCSIPAllowList:        []string{"1.2.3.4/32", "9.10.11.12/32"},
			statusDDCSIPAllowList:         []string{"1.2.3.4/32", "5.6.7.8/32"},
			currentK8sClusterNetworkCIDRs: []string{"10.0.0.0/8", "192.168.0.0/16"},
			statusK8sClusterNetworkCIDRs:  []string{"10.0.0.0/8", "172.16.0.0/12"},
			expectedResult:                true,
		},
		{
			name:                          "empty to non-empty DDCS IP allow list",
			currentDDCSIPAllowList:        []string{"1.2.3.4/32"},
			statusDDCSIPAllowList:         []string{},
			currentK8sClusterNetworkCIDRs: []string{"10.0.0.0/8"},
			statusK8sClusterNetworkCIDRs:  []string{"10.0.0.0/8"},
			expectedResult:                true,
		},
		{
			name:                          "non-empty to empty DDCS IP allow list",
			currentDDCSIPAllowList:        []string{},
			statusDDCSIPAllowList:         []string{"1.2.3.4/32"},
			currentK8sClusterNetworkCIDRs: []string{"10.0.0.0/8"},
			statusK8sClusterNetworkCIDRs:  []string{"10.0.0.0/8"},
			expectedResult:                true,
		},
		{
			name:                          "nil to non-nil slice",
			currentDDCSIPAllowList:        []string{"1.2.3.4/32"},
			statusDDCSIPAllowList:         nil,
			currentK8sClusterNetworkCIDRs: []string{"10.0.0.0/8"},
			statusK8sClusterNetworkCIDRs:  []string{"10.0.0.0/8"},
			expectedResult:                true,
		},
		{
			name:                          "different order same values",
			currentDDCSIPAllowList:        []string{"5.6.7.8/32", "1.2.3.4/32"},
			statusDDCSIPAllowList:         []string{"1.2.3.4/32", "5.6.7.8/32"},
			currentK8sClusterNetworkCIDRs: []string{"10.0.0.0/8", "172.16.0.0/12"},
			statusK8sClusterNetworkCIDRs:  []string{"10.0.0.0/8", "172.16.0.0/12"},
			expectedResult:                true, // Order matters in our comparison
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := &BackendK8sCache{
				ddcsIPAllowList:        tt.currentDDCSIPAllowList,
				k8sClusterNetworkCIDRs: tt.currentK8sClusterNetworkCIDRs,
			}

			nb := &nvidiaiov1.NVCFBackend{
				Status: nvidiaiov1.NVCFBackendStatus{
					DDCSIPAllowList:        tt.statusDDCSIPAllowList,
					K8sClusterNetworkCIDRs: tt.statusK8sClusterNetworkCIDRs,
				},
			}

			check := hasNetworkConfigChangedCheck(ctx, bc.ddcsIPAllowList, bc.k8sClusterNetworkCIDRs, nb.Status)
			result := check()
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestHasNVCAResourcesChangedCheck(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name                         string
		newAgentResources            corev1.ResourceRequirements
		newWebhookResources          corev1.ResourceRequirements
		newOTelCollectorResources    corev1.ResourceRequirements
		statusAgentResources         *corev1.ResourceRequirements
		statusWebhookResources       *corev1.ResourceRequirements
		statusOTelCollectorResources *corev1.ResourceRequirements
		expectedResult               bool
	}{
		{
			name: "no change in resources",
			newAgentResources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					"cpu":    resource.MustParse("100m"),
					"memory": resource.MustParse("128Mi"),
				},
				Limits: corev1.ResourceList{
					"cpu":    resource.MustParse("200m"),
					"memory": resource.MustParse("256Mi"),
				},
			},
			newWebhookResources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					"cpu":    resource.MustParse("50m"),
					"memory": resource.MustParse("64Mi"),
				},
			},
			newOTelCollectorResources: corev1.ResourceRequirements{},
			statusAgentResources: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					"cpu":    resource.MustParse("100m"),
					"memory": resource.MustParse("128Mi"),
				},
				Limits: corev1.ResourceList{
					"cpu":    resource.MustParse("200m"),
					"memory": resource.MustParse("256Mi"),
				},
			},
			statusWebhookResources: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					"cpu":    resource.MustParse("50m"),
					"memory": resource.MustParse("64Mi"),
				},
			},
			statusOTelCollectorResources: &corev1.ResourceRequirements{},
			expectedResult:               false,
		},
		{
			name: "agent resources changed",
			newAgentResources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					"cpu":    resource.MustParse("200m"),
					"memory": resource.MustParse("256Mi"),
				},
			},
			newWebhookResources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					"cpu":    resource.MustParse("50m"),
					"memory": resource.MustParse("64Mi"),
				},
			},
			newOTelCollectorResources: corev1.ResourceRequirements{},
			statusAgentResources: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					"cpu":    resource.MustParse("100m"),
					"memory": resource.MustParse("128Mi"),
				},
			},
			statusWebhookResources: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					"cpu":    resource.MustParse("50m"),
					"memory": resource.MustParse("64Mi"),
				},
			},
			statusOTelCollectorResources: &corev1.ResourceRequirements{},
			expectedResult:               true,
		},
		{
			name: "webhook resources changed",
			newAgentResources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					"cpu":    resource.MustParse("100m"),
					"memory": resource.MustParse("128Mi"),
				},
			},
			newWebhookResources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					"cpu":    resource.MustParse("100m"),
					"memory": resource.MustParse("128Mi"),
				},
			},
			newOTelCollectorResources: corev1.ResourceRequirements{},
			statusAgentResources: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					"cpu":    resource.MustParse("100m"),
					"memory": resource.MustParse("128Mi"),
				},
			},
			statusWebhookResources: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					"cpu":    resource.MustParse("50m"),
					"memory": resource.MustParse("64Mi"),
				},
			},
			statusOTelCollectorResources: &corev1.ResourceRequirements{},
			expectedResult:               true,
		},
		{
			name: "both resources changed",
			newAgentResources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					"cpu":    resource.MustParse("200m"),
					"memory": resource.MustParse("256Mi"),
				},
			},
			newWebhookResources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					"cpu":    resource.MustParse("100m"),
					"memory": resource.MustParse("128Mi"),
				},
			},
			newOTelCollectorResources: corev1.ResourceRequirements{},
			statusAgentResources: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					"cpu":    resource.MustParse("100m"),
					"memory": resource.MustParse("128Mi"),
				},
			},
			statusWebhookResources: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					"cpu":    resource.MustParse("50m"),
					"memory": resource.MustParse("64Mi"),
				},
			},
			statusOTelCollectorResources: &corev1.ResourceRequirements{},
			expectedResult:               true,
		},
		{
			name: "status has nil agent resources",
			newAgentResources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					"cpu":    resource.MustParse("100m"),
					"memory": resource.MustParse("128Mi"),
				},
			},
			newWebhookResources:          corev1.ResourceRequirements{},
			newOTelCollectorResources:    corev1.ResourceRequirements{},
			statusAgentResources:         nil,
			statusWebhookResources:       &corev1.ResourceRequirements{},
			statusOTelCollectorResources: &corev1.ResourceRequirements{},
			expectedResult:               true,
		},
		{
			name:              "status has nil webhook resources",
			newAgentResources: corev1.ResourceRequirements{},
			newWebhookResources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					"cpu":    resource.MustParse("50m"),
					"memory": resource.MustParse("64Mi"),
				},
			},
			newOTelCollectorResources:    corev1.ResourceRequirements{},
			statusAgentResources:         &corev1.ResourceRequirements{},
			statusWebhookResources:       nil,
			statusOTelCollectorResources: &corev1.ResourceRequirements{},
			expectedResult:               true,
		},
		{
			name:              "otel collector resources changed",
			newAgentResources: corev1.ResourceRequirements{},
			newWebhookResources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					"cpu":    resource.MustParse("50m"),
					"memory": resource.MustParse("64Mi"),
				},
			},
			newOTelCollectorResources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					"cpu":    resource.MustParse("500m"),
					"memory": resource.MustParse("512Mi"),
				},
			},
			statusAgentResources: &corev1.ResourceRequirements{},
			statusWebhookResources: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					"cpu":    resource.MustParse("50m"),
					"memory": resource.MustParse("64Mi"),
				},
			},
			statusOTelCollectorResources: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					"cpu":    resource.MustParse("200m"),
					"memory": resource.MustParse("256Mi"),
				},
			},
			expectedResult: true,
		},
		{
			name:                "status has nil otel collector resources",
			newAgentResources:   corev1.ResourceRequirements{},
			newWebhookResources: corev1.ResourceRequirements{},
			newOTelCollectorResources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					"cpu":    resource.MustParse("200m"),
					"memory": resource.MustParse("256Mi"),
				},
			},
			statusAgentResources:         &corev1.ResourceRequirements{},
			statusWebhookResources:       &corev1.ResourceRequirements{},
			statusOTelCollectorResources: nil,
			expectedResult:               true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nbStatus := nvidiaiov1.NVCFBackendStatus{
				AgentConfig: nvidiaiov1.AgentConfig{
					AgentResources:         tt.statusAgentResources,
					WebhookResources:       tt.statusWebhookResources,
					OTelCollectorResources: tt.statusOTelCollectorResources,
				},
			}

			checker := hasNVCAResourcesChangedCheck(ctx, tt.newAgentResources, tt.newWebhookResources, tt.newOTelCollectorResources, nbStatus)
			result := checker()
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestHasAgentDeploymentConfigChangedCheck(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name              string
		newAgentConfig    nvidiaiov1.AgentConfig
		statusAgentConfig nvidiaiov1.AgentConfig
		expectedResult    bool
	}{
		{
			name: "no change in priority class name",
			newAgentConfig: nvidiaiov1.AgentConfig{
				DeploymentConfig: nvidiaiov1.DeploymentConfig{PriorityClassName: "high-priority"},
			},
			statusAgentConfig: nvidiaiov1.AgentConfig{
				DeploymentConfig: nvidiaiov1.DeploymentConfig{PriorityClassName: "high-priority"},
			},
			expectedResult: false,
		},
		{
			name: "priority class name changed",
			newAgentConfig: nvidiaiov1.AgentConfig{
				DeploymentConfig: nvidiaiov1.DeploymentConfig{PriorityClassName: "high-priority"},
			},
			statusAgentConfig: nvidiaiov1.AgentConfig{
				DeploymentConfig: nvidiaiov1.DeploymentConfig{PriorityClassName: "low-priority"},
			},
			expectedResult: true,
		},
		{
			name: "priority class name changed from empty",
			newAgentConfig: nvidiaiov1.AgentConfig{
				DeploymentConfig: nvidiaiov1.DeploymentConfig{PriorityClassName: "high-priority"},
			},
			statusAgentConfig: nvidiaiov1.AgentConfig{
				DeploymentConfig: nvidiaiov1.DeploymentConfig{PriorityClassName: ""},
			},
			expectedResult: true,
		},
		{
			name: "priority class name changed to empty",
			newAgentConfig: nvidiaiov1.AgentConfig{
				DeploymentConfig: nvidiaiov1.DeploymentConfig{PriorityClassName: ""},
			},
			statusAgentConfig: nvidiaiov1.AgentConfig{
				DeploymentConfig: nvidiaiov1.DeploymentConfig{PriorityClassName: "high-priority"},
			},
			expectedResult: true,
		},
		{
			name: "both empty priority class names",
			newAgentConfig: nvidiaiov1.AgentConfig{
				DeploymentConfig: nvidiaiov1.DeploymentConfig{PriorityClassName: ""},
			},
			statusAgentConfig: nvidiaiov1.AgentConfig{
				DeploymentConfig: nvidiaiov1.DeploymentConfig{PriorityClassName: ""},
			},
			expectedResult: false,
		},
		{
			name: "priority class name set for first time",
			newAgentConfig: nvidiaiov1.AgentConfig{
				DeploymentConfig: nvidiaiov1.DeploymentConfig{PriorityClassName: "system-critical"},
			},
			statusAgentConfig: nvidiaiov1.AgentConfig{
				DeploymentConfig: nvidiaiov1.DeploymentConfig{PriorityClassName: ""},
			},
			expectedResult: true,
		},
		{
			name: "priority class name removed",
			newAgentConfig: nvidiaiov1.AgentConfig{
				DeploymentConfig: nvidiaiov1.DeploymentConfig{PriorityClassName: ""},
			},
			statusAgentConfig: nvidiaiov1.AgentConfig{
				DeploymentConfig: nvidiaiov1.DeploymentConfig{PriorityClassName: "system-critical"},
			},
			expectedResult: true,
		},
		{
			name: "secret mirror source namespace changed",
			newAgentConfig: nvidiaiov1.AgentConfig{
				DeploymentConfig: nvidiaiov1.DeploymentConfig{
					SecretMirrorSourceNamespace: "new-namespace",
				},
			},
			statusAgentConfig: nvidiaiov1.AgentConfig{
				DeploymentConfig: nvidiaiov1.DeploymentConfig{
					SecretMirrorSourceNamespace: "old-namespace",
				},
			},
			expectedResult: true,
		},
		{
			name: "secret mirror label selector changed",
			newAgentConfig: nvidiaiov1.AgentConfig{
				DeploymentConfig: nvidiaiov1.DeploymentConfig{
					SecretMirrorLabelSelector: "app=new-app",
				},
			},
			statusAgentConfig: nvidiaiov1.AgentConfig{
				DeploymentConfig: nvidiaiov1.DeploymentConfig{
					SecretMirrorLabelSelector: "app=old-app",
				},
			},
			expectedResult: true,
		},
		{
			name: "secret mirror config unchanged",
			newAgentConfig: nvidiaiov1.AgentConfig{
				DeploymentConfig: nvidiaiov1.DeploymentConfig{
					SecretMirrorSourceNamespace: "same-namespace",
					SecretMirrorLabelSelector:   "app=same-app",
				},
			},
			statusAgentConfig: nvidiaiov1.AgentConfig{
				DeploymentConfig: nvidiaiov1.DeploymentConfig{
					SecretMirrorSourceNamespace: "same-namespace",
					SecretMirrorLabelSelector:   "app=same-app",
				},
			},
			expectedResult: false,
		},
		{
			name: "secret mirror config added",
			newAgentConfig: nvidiaiov1.AgentConfig{
				DeploymentConfig: nvidiaiov1.DeploymentConfig{
					SecretMirrorSourceNamespace: "new-namespace",
					SecretMirrorLabelSelector:   "app=new-app",
				},
			},
			statusAgentConfig: nvidiaiov1.AgentConfig{},
			expectedResult:    true,
		},
		{
			name:           "secret mirror config removed",
			newAgentConfig: nvidiaiov1.AgentConfig{},
			statusAgentConfig: nvidiaiov1.AgentConfig{
				DeploymentConfig: nvidiaiov1.DeploymentConfig{
					SecretMirrorSourceNamespace: "old-namespace",
					SecretMirrorLabelSelector:   "app=old-app",
				},
			},
			expectedResult: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nb := &nvidiaiov1.NVCFBackend{
				Status: nvidiaiov1.NVCFBackendStatus{
					AgentConfig: tt.statusAgentConfig,
				},
			}

			nb.Spec.AgentConfig = tt.newAgentConfig
			checker := hasAgentDeploymentConfigChanged(ctx, tt.newAgentConfig.DeploymentConfig, nb.Status)
			result := checker()
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestHasAdditionalImagePullSecretsChanged(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name           string
		newSecrets     []corev1.LocalObjectReference
		statusSecrets  []corev1.LocalObjectReference
		expectedResult bool
	}{
		{
			name:           "secrets added",
			newSecrets:     []corev1.LocalObjectReference{{Name: "secret-one"}, {Name: "secret-two"}},
			statusSecrets:  []corev1.LocalObjectReference{},
			expectedResult: true,
		},
		{
			name:           "secrets removed",
			newSecrets:     []corev1.LocalObjectReference{},
			statusSecrets:  []corev1.LocalObjectReference{{Name: "secret-one"}, {Name: "secret-two"}},
			expectedResult: true,
		},
		{
			name:           "secrets unchanged",
			newSecrets:     []corev1.LocalObjectReference{{Name: "secret-one"}, {Name: "secret-two"}},
			statusSecrets:  []corev1.LocalObjectReference{{Name: "secret-one"}, {Name: "secret-two"}},
			expectedResult: false,
		},
		{
			name:           "secrets modified - one added",
			newSecrets:     []corev1.LocalObjectReference{{Name: "secret-one"}, {Name: "secret-two"}, {Name: "secret-three"}},
			statusSecrets:  []corev1.LocalObjectReference{{Name: "secret-one"}, {Name: "secret-two"}},
			expectedResult: true,
		},
		{
			name:           "secrets modified - one removed",
			newSecrets:     []corev1.LocalObjectReference{{Name: "secret-one"}},
			statusSecrets:  []corev1.LocalObjectReference{{Name: "secret-one"}, {Name: "secret-two"}},
			expectedResult: true,
		},
		{
			name:           "secrets replaced",
			newSecrets:     []corev1.LocalObjectReference{{Name: "new-secret"}},
			statusSecrets:  []corev1.LocalObjectReference{{Name: "old-secret"}},
			expectedResult: true,
		},
		{
			name:           "both nil",
			newSecrets:     nil,
			statusSecrets:  nil,
			expectedResult: false,
		},
		{
			name:           "both empty",
			newSecrets:     []corev1.LocalObjectReference{},
			statusSecrets:  []corev1.LocalObjectReference{},
			expectedResult: false,
		},
		{
			name:           "nil vs empty (treated as equal)",
			newSecrets:     nil,
			statusSecrets:  []corev1.LocalObjectReference{},
			expectedResult: false,
		},
		{
			name:           "empty vs nil (treated as equal)",
			newSecrets:     []corev1.LocalObjectReference{},
			statusSecrets:  nil,
			expectedResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nb := &nvidiaiov1.NVCFBackend{
				Status: nvidiaiov1.NVCFBackendStatus{
					AgentConfig: nvidiaiov1.AgentConfig{
						DeploymentConfig: nvidiaiov1.DeploymentConfig{
							AdditionalImagePullSecrets: tt.statusSecrets,
						},
					},
				},
			}

			checker := hasAdditionalImagePullSecretsChanged(ctx, tt.newSecrets, nb.Status)
			result := checker()
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestStringSlicesEqual(t *testing.T) {
	tests := []struct {
		name     string
		a        []string
		b        []string
		expected bool
	}{
		{
			name:     "equal slices",
			a:        []string{"a", "b", "c"},
			b:        []string{"a", "b", "c"},
			expected: true,
		},
		{
			name:     "different lengths",
			a:        []string{"a", "b"},
			b:        []string{"a", "b", "c"},
			expected: false,
		},
		{
			name:     "same length different values",
			a:        []string{"a", "b", "c"},
			b:        []string{"a", "b", "d"},
			expected: false,
		},
		{
			name:     "different order",
			a:        []string{"a", "b", "c"},
			b:        []string{"c", "b", "a"},
			expected: false,
		},
		{
			name:     "both empty",
			a:        []string{},
			b:        []string{},
			expected: true,
		},
		{
			name:     "both nil",
			a:        nil,
			b:        nil,
			expected: true,
		},
		{
			name:     "one nil one empty",
			a:        nil,
			b:        []string{},
			expected: true, // cmp.Equal with cmpopts.EquateEmpty() treats nil and empty as equal
		},
		{
			name:     "one empty one nil",
			a:        []string{},
			b:        nil,
			expected: true, // cmp.Equal with cmpopts.EquateEmpty() treats nil and empty as equal
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cmp.Equal(tt.a, tt.b, cmpopts.EquateEmpty())
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestHasAgentDeploymentConfigChanged_DynamicConfigNoRepeatedRollout tests that no repeated rollouts occur.
// For NGC-managed clusters:
// - DeploymentConfig comes from bc.deploymentConfig (local operator config)
// - nb.Spec.AgentConfig.DeploymentConfig is ignored (NGC doesn't provide it)
// This simulates the scenario where:
// 1. Operator uses bc.deploymentConfig for NGC-managed clusters
// 2. Status contains the previously deployed config
// 3. mergeAgentConfigs is called to get the effective config for comparison
// Expected: No rollout should be triggered since effective config matches status
func TestHasAgentDeploymentConfigChanged_DynamicConfigNoRepeatedRollout(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name                     string
		bcDeploymentConfig       nvidiaiov1.DeploymentConfig // Local config from operator (used for NGC-managed)
		nbSpecDeploymentConfig   nvidiaiov1.DeploymentConfig // Not used for NGC-managed (NGC doesn't provide DeploymentConfig)
		nbStatusDeploymentConfig nvidiaiov1.DeploymentConfig // Previously deployed effective config
		expectedRollout          bool
		description              string
	}{
		{
			name: "no rollout when NGC config matches status",
			bcDeploymentConfig: nvidiaiov1.DeploymentConfig{
				// Local deployment config (still used for NGC-managed)
				PriorityClassName:           "system-priority",
				NodeSelectorKey:             "node-type",
				NodeSelectorValue:           "gpu",
				SecretMirrorSourceNamespace: "dynamic-namespace",
				SecretMirrorLabelSelector:   "app=dynamic",
				GenerateImagePullSecret:     false,
			},
			nbSpecDeploymentConfig: nvidiaiov1.DeploymentConfig{
				// NGC doesn't provide DeploymentConfig - ignored
			},
			nbStatusDeploymentConfig: nvidiaiov1.DeploymentConfig{
				// Previously deployed config (matches bc.deploymentConfig)
				PriorityClassName:           "system-priority",
				NodeSelectorKey:             "node-type",
				NodeSelectorValue:           "gpu",
				SecretMirrorSourceNamespace: "dynamic-namespace",
				SecretMirrorLabelSelector:   "app=dynamic",
				GenerateImagePullSecret:     false,
			},
			expectedRollout: false,
			description:     "NGC config matches status, should not rollout",
		},
		{
			name: "rollout when deployment config changes",
			bcDeploymentConfig: nvidiaiov1.DeploymentConfig{
				// Local deployment config changed
				PriorityClassName:           "system-priority",
				NodeSelectorKey:             "node-type",
				NodeSelectorValue:           "gpu",
				SecretMirrorSourceNamespace: "new-namespace",
				SecretMirrorLabelSelector:   "app=new",
				GenerateImagePullSecret:     false,
			},
			nbSpecDeploymentConfig: nvidiaiov1.DeploymentConfig{
				// NGC doesn't provide DeploymentConfig - ignored
			},
			nbStatusDeploymentConfig: nvidiaiov1.DeploymentConfig{
				// Old deployed config
				PriorityClassName:           "system-priority",
				NodeSelectorKey:             "node-type",
				NodeSelectorValue:           "gpu",
				SecretMirrorSourceNamespace: "old-namespace",
				SecretMirrorLabelSelector:   "app=old",
				GenerateImagePullSecret:     false,
			},
			expectedRollout: true,
			description:     "Deployment config changed, should trigger rollout",
		},
		{
			name: "no rollout when deployment config matches status with minimal fields",
			bcDeploymentConfig: nvidiaiov1.DeploymentConfig{
				// Local deployment config (minimal case)
				PriorityClassName:           "system-priority",
				NodeSelectorKey:             "node-type",
				NodeSelectorValue:           "gpu",
				SecretMirrorSourceNamespace: "",
				SecretMirrorLabelSelector:   "",
				GenerateImagePullSecret:     false,
			},
			nbSpecDeploymentConfig: nvidiaiov1.DeploymentConfig{
				// NGC doesn't provide DeploymentConfig - ignored
			},
			nbStatusDeploymentConfig: nvidiaiov1.DeploymentConfig{
				PriorityClassName:           "system-priority",
				NodeSelectorKey:             "node-type",
				NodeSelectorValue:           "gpu",
				SecretMirrorSourceNamespace: "",
				SecretMirrorLabelSelector:   "",
				GenerateImagePullSecret:     false,
			},
			expectedRollout: false,
			description:     "No changes, should not rollout",
		},
		{
			name: "rollout when deployment config priority changes",
			bcDeploymentConfig: nvidiaiov1.DeploymentConfig{
				// Local deployment config with new priority
				PriorityClassName:           "new-priority",
				NodeSelectorKey:             "node-type",
				NodeSelectorValue:           "gpu",
				SecretMirrorSourceNamespace: "",
				SecretMirrorLabelSelector:   "",
				GenerateImagePullSecret:     false,
			},
			nbSpecDeploymentConfig: nvidiaiov1.DeploymentConfig{
				// NGC doesn't provide DeploymentConfig - ignored
			},
			nbStatusDeploymentConfig: nvidiaiov1.DeploymentConfig{
				PriorityClassName:           "old-priority",
				NodeSelectorKey:             "node-type",
				NodeSelectorValue:           "gpu",
				SecretMirrorSourceNamespace: "",
				SecretMirrorLabelSelector:   "",
				GenerateImagePullSecret:     false,
			},
			expectedRollout: true,
			description:     "Deployment config priority changed, should trigger rollout",
		},
		{
			name: "no rollout when deployment config matches status completely",
			bcDeploymentConfig: nvidiaiov1.DeploymentConfig{
				// Local deployment config (complete)
				PriorityClassName:           "system-priority",
				NodeSelectorKey:             "node-type",
				NodeSelectorValue:           "gpu",
				SecretMirrorSourceNamespace: "prod-secrets",
				SecretMirrorLabelSelector:   "env=prod",
				GenerateImagePullSecret:     false,
			},
			nbSpecDeploymentConfig: nvidiaiov1.DeploymentConfig{
				// NGC doesn't provide DeploymentConfig - ignored
			},
			nbStatusDeploymentConfig: nvidiaiov1.DeploymentConfig{
				PriorityClassName:           "system-priority",
				NodeSelectorKey:             "node-type",
				NodeSelectorValue:           "gpu",
				SecretMirrorSourceNamespace: "prod-secrets",
				SecretMirrorLabelSelector:   "env=prod",
				GenerateImagePullSecret:     false,
			},
			expectedRollout: false,
			description:     "Deployment config matches status completely, should not rollout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock BackendK8sCache with local deployment config
			bc := &BackendK8sCache{
				deploymentConfig: tt.bcDeploymentConfig,
			}

			// Create NVCFBackend with NGC config from cluster DTO
			nb := &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						ClusterSource: nvcaoptypes.ClusterSourceNGCManaged, // Use NGC config directly
						AgentConfig: nvidiaiov1.AgentConfig{
							DeploymentConfig: tt.nbSpecDeploymentConfig,
						},
					},
				},
				Status: nvidiaiov1.NVCFBackendStatus{
					// AgentConfig is embedded directly in Status, not in NVCFBackendSpecT
					AgentConfig: nvidiaiov1.AgentConfig{
						DeploymentConfig: tt.nbStatusDeploymentConfig,
					},
				},
			}

			// Get effective merged config (simulates what the fix does)
			effectiveConfig := bc.mergeAgentConfigs(nb)

			// Check if rollout would be triggered using the effective config
			checker := hasAgentDeploymentConfigChanged(ctx, effectiveConfig.DeploymentConfig, nb.Status)
			result := checker()

			assert.Equal(t, tt.expectedRollout, result, tt.description)
		})
	}
}

func TestNewAgentConfigChangedCheck_IncludesNATSURL(t *testing.T) {
	ctx := newTestContext()
	clients := mockKubeClientsForIntegrationTests()
	bc := &BackendK8sCache{
		clients:              clients,
		envType:              nvidiaiov1.EnvTypeStage,
		ngcServiceKeyFetcher: &mockTokenFetcher{token: "randomkey"},
		operatorNamespace:    NVCAOperatorNamespace,
	}

	natsURL := "nats://nats.nats-system.svc.cluster.local:4222"
	nb := &nvidiaiov1.NVCFBackend{
		Spec: nvidiaiov1.NVCFBackendSpec{
			NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
				AccountConfig: nvidiaiov1.AccountConfig{
					NCAID: "ncaid1",
				},
				ClusterConfig: nvidiaiov1.ClusterConfig{
					ClusterID:        "some-cluster-id",
					CloudProvider:    "ON-PREM",
					ClusterGroupName: "FC-NVCF-Backend",
					ClusterName:      "byoc-test",
				},
				FeatureGate: nvidiaiov1.FeatureGate{
					OTELConfig: &nvidiaiov1.OTELConfig{},
					Values: []string{
						"LogPosting",
						"CachingSupport",
						"PeriodicInstanceStatusUpdate",
						"SharedCluster",
					},
				},
				ICMSConfig: nvidiaiov1.ICMSConfig{
					ICMSServiceURL: "https://stg.icms.nvcf.nvidia.com",
					TokenURL:       "https://stg.icms.nvcf.nvidia.com/token",
				},
				OAuthConfig: nvidiaiov1.OAuthConfig{
					ClientID:             "oauth-stg-abc123",
					PublicKeysetEndpoint: "https://stage-oauth.example.test/.well-known/jwks.json",
				},
				WebhookConfig: nvidiaiov1.WebhookConfig{
					ListenPort:  8001,
					ServicePort: 8002,
				},
				AgentConfig: nvidiaiov1.AgentConfig{
					NATSURL: &natsURL,
				},
			},
		},
	}

	agentConfig, err := bc.newAgentConfig(ctx, nb)
	require.NoError(t, err)
	err = bc.setupAgentConfigConfigMap(ctx, nb, agentConfig)
	require.NoError(t, err)

	checker, err := bc.newAgentConfigChangedCheck(ctx, nb)
	require.NoError(t, err)
	assert.False(t, checker())
}

// TestHasAgentWorkerConfigChanged_DynamicConfigNoRepeatedRollout tests that when
// dynamic worker config values are used, no repeated rollouts occur.
func TestHasAgentWorkerConfigChanged_DynamicConfigNoRepeatedRollout(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name                 string
		version              string
		bcWorkerConfig       nvidiaiov1.NVCFWorkerConfig
		nbSpecWorkerConfig   nvidiaiov1.NVCFWorkerConfig
		nbStatusWorkerConfig nvidiaiov1.NVCFWorkerConfig
		expectedRollout      bool
		description          string
	}{
		{
			name:           "no rollout when NGC config matches status",
			version:        "v2.44.0", // Version that supports CSI volume mount options
			bcWorkerConfig: nvidiaiov1.NVCFWorkerConfig{
				// Not used for NGC-managed clusters
			},
			nbSpecWorkerConfig: nvidiaiov1.NVCFWorkerConfig{
				// NGC provides complete config
				CacheMountOptionsEnabled: true,
				CacheMountOptions:        "local",
				WorkerDegradationPeriod:  300000000000, // 5 minutes in nanoseconds
			},
			nbStatusWorkerConfig: nvidiaiov1.NVCFWorkerConfig{
				CacheMountOptionsEnabled: true,
				CacheMountOptions:        "local",
				WorkerDegradationPeriod:  300000000000,
			},
			expectedRollout: false,
			description:     "NGC config matches status, should not rollout",
		},
		{
			name:           "rollout when NGC worker config changes",
			version:        "v2.44.0",
			bcWorkerConfig: nvidiaiov1.NVCFWorkerConfig{
				// Not used for NGC-managed clusters
			},
			nbSpecWorkerConfig: nvidiaiov1.NVCFWorkerConfig{
				// NGC provides complete config with changed value
				CacheMountOptionsEnabled: true,
				CacheMountOptions:        "local",
				WorkerDegradationPeriod:  600000000000, // 10 minutes - CHANGED
			},
			nbStatusWorkerConfig: nvidiaiov1.NVCFWorkerConfig{
				CacheMountOptionsEnabled: true,
				CacheMountOptions:        "local",
				WorkerDegradationPeriod:  300000000000, // 5 minutes
			},
			expectedRollout: true,
			description:     "NGC worker config changed, should trigger rollout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock BackendK8sCache (empty for NGC-managed, NVCFWorkerConfig comes from NGC)
			bc := &BackendK8sCache{}

			// Create NVCFBackend with NGC config
			nb := &nvidiaiov1.NVCFBackend{
				Spec: nvidiaiov1.NVCFBackendSpec{
					NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{
						Version:       tt.version,
						ClusterSource: nvcaoptypes.ClusterSourceNGCManaged, // Use NGC config directly
						AgentConfig: nvidiaiov1.AgentConfig{
							NVCFWorkerConfig: tt.nbSpecWorkerConfig,
						},
					},
				},
				Status: nvidiaiov1.NVCFBackendStatus{
					// AgentConfig is embedded directly in Status, not in NVCFBackendSpecT
					AgentConfig: nvidiaiov1.AgentConfig{
						NVCFWorkerConfig: tt.nbStatusWorkerConfig,
					},
				},
			}

			// Get effective merged config
			effectiveConfig := bc.mergeAgentConfigs(nb)

			// Check if rollout would be triggered
			checker := hasAgentWorkerConfigOptionsChanged(ctx, effectiveConfig.NVCFWorkerConfig, nb.Status)
			result := checker()

			assert.Equal(t, tt.expectedRollout, result, tt.description)
		})
	}
}

func TestHasOTelCollectorConfigChangedCheck(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name           string
		newConfig      *nvidiaiov1.OTelCollectorConfig
		statusConfig   *nvidiaiov1.OTelCollectorConfig
		expectedResult bool
	}{
		{
			name: "no change - all fields match",
			newConfig: &nvidiaiov1.OTelCollectorConfig{
				Enabled: true,
				ImageConfig: nvidiaiov1.ImageConfig{
					Repository: "nvcr.io/nvstaging/otel-collector",
					Tag:        "0.143.2",
				},
			},
			statusConfig: &nvidiaiov1.OTelCollectorConfig{
				Enabled: true,
				ImageConfig: nvidiaiov1.ImageConfig{
					Repository: "nvcr.io/nvstaging/otel-collector",
					Tag:        "0.143.2",
				},
			},
			expectedResult: false,
		},
		{
			name: "no change - all fields default",
			newConfig: &nvidiaiov1.OTelCollectorConfig{
				Enabled:     false,
				ImageConfig: nvidiaiov1.ImageConfig{},
			},
			statusConfig: &nvidiaiov1.OTelCollectorConfig{
				Enabled:     false,
				ImageConfig: nvidiaiov1.ImageConfig{},
			},
			expectedResult: false,
		},
		{
			name: "status nil - first reconcile triggers rollout",
			newConfig: &nvidiaiov1.OTelCollectorConfig{
				Enabled: false,
				ImageConfig: nvidiaiov1.ImageConfig{
					Repository: "nvcr.io/nvstaging/otel-collector",
					Tag:        "0.143.2",
				},
			},
			statusConfig:   nil,
			expectedResult: true,
		},
		{
			name: "enabled changed false to true",
			newConfig: &nvidiaiov1.OTelCollectorConfig{
				Enabled: true,
				ImageConfig: nvidiaiov1.ImageConfig{
					Repository: "nvcr.io/nvstaging/otel-collector",
					Tag:        "0.143.2",
				},
			},
			statusConfig: &nvidiaiov1.OTelCollectorConfig{
				Enabled: false,
				ImageConfig: nvidiaiov1.ImageConfig{
					Repository: "nvcr.io/nvstaging/otel-collector",
					Tag:        "0.143.2",
				},
			},
			expectedResult: true,
		},
		{
			name: "enabled changed true to false",
			newConfig: &nvidiaiov1.OTelCollectorConfig{
				Enabled: false,
				ImageConfig: nvidiaiov1.ImageConfig{
					Repository: "nvcr.io/nvstaging/otel-collector",
					Tag:        "0.143.2",
				},
			},
			statusConfig: &nvidiaiov1.OTelCollectorConfig{
				Enabled: true,
				ImageConfig: nvidiaiov1.ImageConfig{
					Repository: "nvcr.io/nvstaging/otel-collector",
					Tag:        "0.143.2",
				},
			},
			expectedResult: true,
		},
		{
			name: "image repo changed",
			newConfig: &nvidiaiov1.OTelCollectorConfig{
				Enabled: true,
				ImageConfig: nvidiaiov1.ImageConfig{
					Repository: "nvcr.io/nvidia/otel-collector",
					Tag:        "0.143.2",
				},
			},
			statusConfig: &nvidiaiov1.OTelCollectorConfig{
				Enabled: true,
				ImageConfig: nvidiaiov1.ImageConfig{
					Repository: "nvcr.io/nvstaging/otel-collector",
					Tag:        "0.143.2",
				},
			},
			expectedResult: true,
		},
		{
			name: "image tag changed",
			newConfig: &nvidiaiov1.OTelCollectorConfig{
				Enabled: true,
				ImageConfig: nvidiaiov1.ImageConfig{
					Repository: "nvcr.io/nvstaging/otel-collector",
					Tag:        "0.144.0",
				},
			},
			statusConfig: &nvidiaiov1.OTelCollectorConfig{
				Enabled: true,
				ImageConfig: nvidiaiov1.ImageConfig{
					Repository: "nvcr.io/nvstaging/otel-collector",
					Tag:        "0.143.2",
				},
			},
			expectedResult: true,
		},
		{
			name: "multiple fields changed",
			newConfig: &nvidiaiov1.OTelCollectorConfig{
				Enabled: false,
				ImageConfig: nvidiaiov1.ImageConfig{
					Repository: "nvcr.io/nvidia/otel-collector",
					Tag:        "0.144.0",
				},
			},
			statusConfig: &nvidiaiov1.OTelCollectorConfig{
				Enabled: true,
				ImageConfig: nvidiaiov1.ImageConfig{
					Repository: "nvcr.io/nvstaging/otel-collector",
					Tag:        "0.143.2",
				},
			},
			expectedResult: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nbStatus := nvidiaiov1.NVCFBackendStatus{
				AgentConfig: nvidiaiov1.AgentConfig{
					OTelCollectorConfig: tt.statusConfig,
				},
			}

			checker := hasOTelCollectorConfigChangedCheck(ctx, tt.newConfig, nbStatus)
			result := checker()
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestHasEnvOverridesChangedCheck(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name                       string
		newFunctionEnvOverridesB64 string
		newTaskEnvOverridesB64     string
		statusFunctionEnvOverrides string
		statusTaskEnvOverrides     string
		expectedResult             bool
	}{
		{
			name:                       "no change in env overrides",
			newFunctionEnvOverridesB64: "eyJJTklUX0NPTlRBSU5FUiI6InRlc3QifQ==",
			newTaskEnvOverridesB64:     "eyJVVElMU19DT05UQUlORVIiOiJ0ZXN0In0=",
			statusFunctionEnvOverrides: "eyJJTklUX0NPTlRBSU5FUiI6InRlc3QifQ==",
			statusTaskEnvOverrides:     "eyJVVElMU19DT05UQUlORVIiOiJ0ZXN0In0=",
			expectedResult:             false,
		},
		{
			name:                       "change in function env overrides",
			newFunctionEnvOverridesB64: "eyJJTklUX0NPTlRBSU5FUiI6Im5ldyJ9",
			newTaskEnvOverridesB64:     "eyJVVElMU19DT05UQUlORVIiOiJ0ZXN0In0=",
			statusFunctionEnvOverrides: "eyJJTklUX0NPTlRBSU5FUiI6InRlc3QifQ==",
			statusTaskEnvOverrides:     "eyJVVElMU19DT05UQUlORVIiOiJ0ZXN0In0=",
			expectedResult:             true,
		},
		{
			name:                       "change in task env overrides",
			newFunctionEnvOverridesB64: "eyJJTklUX0NPTlRBSU5FUiI6InRlc3QifQ==",
			newTaskEnvOverridesB64:     "eyJVVElMU19DT05UQUlORVIiOiJuZXcifQ==",
			statusFunctionEnvOverrides: "eyJJTklUX0NPTlRBSU5FUiI6InRlc3QifQ==",
			statusTaskEnvOverrides:     "eyJVVElMU19DT05UQUlORVIiOiJ0ZXN0In0=",
			expectedResult:             true,
		},
		{
			name:                       "change in both env overrides",
			newFunctionEnvOverridesB64: "eyJJTklUX0NPTlRBSU5FUiI6Im5ldyJ9",
			newTaskEnvOverridesB64:     "eyJVVElMU19DT05UQUlORVIiOiJuZXcifQ==",
			statusFunctionEnvOverrides: "eyJJTklUX0NPTlRBSU5FUiI6InRlc3QifQ==",
			statusTaskEnvOverrides:     "eyJVVElMU19DT05UQUlORVIiOiJ0ZXN0In0=",
			expectedResult:             true,
		},
		{
			name:                       "empty status to non-empty",
			newFunctionEnvOverridesB64: "eyJJTklUX0NPTlRBSU5FUiI6InRlc3QifQ==",
			newTaskEnvOverridesB64:     "",
			statusFunctionEnvOverrides: "",
			statusTaskEnvOverrides:     "",
			expectedResult:             true,
		},
		{
			name:                       "non-empty status to empty",
			newFunctionEnvOverridesB64: "",
			newTaskEnvOverridesB64:     "",
			statusFunctionEnvOverrides: "eyJJTklUX0NPTlRBSU5FUiI6InRlc3QifQ==",
			statusTaskEnvOverrides:     "",
			expectedResult:             true,
		},
		{
			name:                       "both empty - no change",
			newFunctionEnvOverridesB64: "",
			newTaskEnvOverridesB64:     "",
			statusFunctionEnvOverrides: "",
			statusTaskEnvOverrides:     "",
			expectedResult:             false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nb := nvidiaiov1.NVCFBackendStatus{
				FunctionEnvOverridesB64: tt.statusFunctionEnvOverrides,
				TaskEnvOverridesB64:     tt.statusTaskEnvOverrides,
			}

			check := hasEnvOverridesChangedCheck(ctx, tt.newFunctionEnvOverridesB64, tt.newTaskEnvOverridesB64, nb)
			result := check()
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}
