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

package client

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The wire field names below are part of the contract with ICMS and the
// control-plane validation-policy API. Assert them directly so an accidental
// json tag rename is caught here rather than at deploy time.

func TestKubernetesTypeJSONResourceOmitEmpty(t *testing.T) {
	t.Run("workload type omits resource", func(t *testing.T) {
		data, err := json.Marshal(KubernetesType{Group: "apps", Version: "v1", Kind: "Deployment"})
		require.NoError(t, err)
		assert.JSONEq(t, `{"group":"apps","version":"v1","kind":"Deployment"}`, string(data))
		assert.NotContains(t, string(data), "resource")
	})

	t.Run("cluster type includes resource", func(t *testing.T) {
		data, err := json.Marshal(KubernetesType{
			Group: "apps", Version: "v1", Kind: "Deployment", Resource: "deployments",
		})
		require.NoError(t, err)
		assert.JSONEq(t, `{"group":"apps","version":"v1","kind":"Deployment","resource":"deployments"}`, string(data))
	})
}

func TestHelmValidationPolicyDtoJSON(t *testing.T) {
	policy := HelmValidationPolicyDto{
		Name: "Unrestricted",
		ExtraKubernetesTypes: []KubernetesType{
			{Group: "leaderworkerset.x-k8s.io", Version: "v1", Kind: "LeaderWorkerSet"},
		},
	}
	data, err := json.Marshal(policy)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"name":"Unrestricted",
		"extraKubernetesTypes":[
			{"group":"leaderworkerset.x-k8s.io","version":"v1","kind":"LeaderWorkerSet"}
		]
	}`, string(data))
}

func TestHelmValidationPolicyDtoJSONOmitsEmptyTypes(t *testing.T) {
	data, err := json.Marshal(HelmValidationPolicyDto{Name: "Default"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"Default"}`, string(data))
	assert.NotContains(t, string(data), "extraKubernetesTypes")
}

func TestClusterHelmValidationPolicyJSON(t *testing.T) {
	policy := ClusterHelmValidationPolicy{
		Name: "Unrestricted",
		AllowedExtraKubernetesTypes: []KubernetesType{
			{Group: "leaderworkerset.x-k8s.io", Version: "v1", Kind: "LeaderWorkerSet", Resource: "leaderworkersets"},
		},
	}
	data, err := json.Marshal(policy)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"name":"Unrestricted",
		"allowedExtraKubernetesTypes":[
			{"group":"leaderworkerset.x-k8s.io","version":"v1","kind":"LeaderWorkerSet","resource":"leaderworkersets"}
		]
	}`, string(data))
}

func TestRegisterClusterRequestJSONWithPolicy(t *testing.T) {
	req := RegisterClusterRequest{
		ClusterName:      "c1",
		ClusterGroupName: "c1",
		NcaID:            "nca-123",
		CloudProvider:    "ON-PREM",
		Region:           "us-west",
		NvcaVersion:      "0.0.0",
		HelmValidationPolicy: &ClusterHelmValidationPolicy{
			Name: "Unrestricted",
			AllowedExtraKubernetesTypes: []KubernetesType{
				{Group: "apps", Version: "v1", Kind: "Deployment", Resource: "deployments"},
			},
		},
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(data, &decoded))
	policy, ok := decoded["helmValidationPolicy"].(map[string]any)
	require.True(t, ok, "helmValidationPolicy must be present")
	assert.Equal(t, "Unrestricted", policy["name"])
	types, ok := policy["allowedExtraKubernetesTypes"].([]any)
	require.True(t, ok)
	require.Len(t, types, 1)
	first := types[0].(map[string]any)
	assert.Equal(t, "deployments", first["resource"])
}

func TestRegisterClusterRequestJSONOmitsPolicyWhenNil(t *testing.T) {
	req := RegisterClusterRequest{
		ClusterName:      "c1",
		ClusterGroupName: "c1",
		NcaID:            "nca-123",
		CloudProvider:    "ON-PREM",
		Region:           "us-west",
		NvcaVersion:      "0.0.0",
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "helmValidationPolicy")
}

func TestGPUSpecificationDtoHelmValidationPolicyJSON(t *testing.T) {
	t.Run("present when set", func(t *testing.T) {
		spec := GPUSpecificationDto{
			HelmValidationPolicy: &HelmValidationPolicyDto{Name: "Default"},
		}
		data, err := json.Marshal(spec)
		require.NoError(t, err)
		assert.Contains(t, string(data), `"helmValidationPolicy"`)
	})

	t.Run("omitted when nil", func(t *testing.T) {
		data, err := json.Marshal(GPUSpecificationDto{})
		require.NoError(t, err)
		assert.NotContains(t, string(data), "helmValidationPolicy")
	})
}
