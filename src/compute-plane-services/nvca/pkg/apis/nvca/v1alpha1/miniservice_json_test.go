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

package v1alpha1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/internal/compat"
)

func TestMiniServiceSpecUnmarshalCanonicalRequestName(t *testing.T) {
	data, err := json.Marshal(map[string]any{
		"namespace":               "ns-a",
		compat.ICMSRequestNameKey: "req-a",
		"helmChartConfig":         map[string]any{},
	})
	require.NoError(t, err)

	var spec MiniServiceSpec
	require.NoError(t, json.Unmarshal(data, &spec))
	assert.Equal(t, "ns-a", spec.Namespace)
	assert.Equal(t, "req-a", spec.ICMSRequestName)
}

func TestMiniServiceSpecUnmarshalLegacyRequestName(t *testing.T) {
	data, err := json.Marshal(map[string]any{
		"namespace":                   "ns-a",
		compat.LegacyRequestNameKey(): "req-a",
		"helmChartConfig":             map[string]any{},
	})
	require.NoError(t, err)

	var spec MiniServiceSpec
	require.NoError(t, json.Unmarshal(data, &spec))
	assert.Equal(t, "req-a", spec.ICMSRequestName)
}

func TestMiniServiceSpecDoesNotAcceptNearMatchLegacyRequestName(t *testing.T) {
	data, err := json.Marshal(map[string]any{
		"namespace":       "ns-a",
		"spatRequestName": "req-a",
		"helmChartConfig": map[string]any{},
	})
	require.NoError(t, err)

	var spec MiniServiceSpec
	require.NoError(t, json.Unmarshal(data, &spec))
	assert.Empty(t, spec.ICMSRequestName)
}

func TestMiniServiceSpecRejectsConflictingRequestNames(t *testing.T) {
	data, err := json.Marshal(map[string]any{
		compat.ICMSRequestNameKey:     "req-a",
		compat.LegacyRequestNameKey(): "req-b",
		"helmChartConfig":             map[string]any{},
	})
	require.NoError(t, err)

	var spec MiniServiceSpec
	err = json.Unmarshal(data, &spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ICMS request name")
}

func TestMiniServiceSpecMarshalUsesCanonicalRequestName(t *testing.T) {
	spec := MiniServiceSpec{
		Namespace:       "ns-a",
		ICMSRequestName: "req-a",
	}

	data, err := json.Marshal(spec)
	require.NoError(t, err)
	assert.Contains(t, string(data), compat.ICMSRequestNameKey)
	assert.NotContains(t, string(data), compat.LegacyRequestNameKey())
}
