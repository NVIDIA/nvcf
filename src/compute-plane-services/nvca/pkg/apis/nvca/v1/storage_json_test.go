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

package v1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/internal/compat"
)

func TestStorageRequestSpecUnmarshalLegacyAliases(t *testing.T) {
	data, err := json.Marshal(map[string]any{
		"type":                             string(ModelCacheRequest),
		compat.LegacyRequestNameKey():      "req-a",
		compat.LegacyRequestNamespaceKey(): "ns-a",
		"modelCache": map[string]any{
			"cacheHandle": "cache-a",
		},
	})
	require.NoError(t, err)

	var spec StorageRequestSpec
	require.NoError(t, json.Unmarshal(data, &spec))
	assert.Equal(t, "req-a", spec.ICMSRequestName)
	assert.Equal(t, "ns-a", spec.ICMSRequestNamespace)
	require.NotNil(t, spec.ModelCache)
	assert.Equal(t, "cache-a", spec.ModelCache.CacheHandle)
}

func TestStorageRequestSpecDoesNotAcceptNearMatchLegacyAliases(t *testing.T) {
	data, err := json.Marshal(map[string]any{
		"type":                 string(ModelCacheRequest),
		"spatRequestName":      "req-a",
		"spatRequestNamespace": "ns-a",
		"modelCache": map[string]any{
			"cacheHandle": "cache-a",
		},
	})
	require.NoError(t, err)

	var spec StorageRequestSpec
	require.NoError(t, json.Unmarshal(data, &spec))
	assert.Empty(t, spec.ICMSRequestName)
	assert.Empty(t, spec.ICMSRequestNamespace)
	require.NotNil(t, spec.ModelCache)
	assert.Equal(t, "cache-a", spec.ModelCache.CacheHandle)
}

func TestStorageRequestSpecRejectsConflictingAliases(t *testing.T) {
	data, err := json.Marshal(map[string]any{
		"type":                             string(ModelCacheRequest),
		compat.ICMSRequestNameKey:          "req-a",
		compat.LegacyRequestNameKey():      "req-b",
		compat.ICMSRequestNamespaceKey:     "ns-a",
		compat.LegacyRequestNamespaceKey(): "ns-a",
	})
	require.NoError(t, err)

	var spec StorageRequestSpec
	err = json.Unmarshal(data, &spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ICMS request name")
}

func TestStorageRequestSpecMarshalUsesCanonicalAliasesOnly(t *testing.T) {
	spec := StorageRequestSpec{
		Type:                 ModelCacheRequest,
		ICMSRequestName:      "req-a",
		ICMSRequestNamespace: "ns-a",
		ModelCache: &ModelCacheSpec{
			CacheHandle: "cache-a",
		},
	}

	data, err := json.Marshal(spec)
	require.NoError(t, err)
	assert.Contains(t, string(data), compat.ICMSRequestNameKey)
	assert.Contains(t, string(data), compat.ICMSRequestNamespaceKey)
	assert.NotContains(t, string(data), compat.LegacyRequestNameKey())
	assert.NotContains(t, string(data), compat.LegacyRequestNamespaceKey())
}
