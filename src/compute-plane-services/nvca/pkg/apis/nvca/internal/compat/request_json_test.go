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

package compat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLegacyRequestKeysAreStable(t *testing.T) {
	assert.Equal(t, LegacyRequestNameKey(), LegacyRequestNameKey())
	assert.Equal(t, LegacyRequestNamespaceKey(), LegacyRequestNamespaceKey())
	assert.NotEqual(t, ICMSRequestNameKey, LegacyRequestNameKey())
	assert.NotEqual(t, ICMSRequestNamespaceKey, LegacyRequestNamespaceKey())
}

func TestDecodeRequestNamePrefersCanonicalValue(t *testing.T) {
	fields := map[string]json.RawMessage{
		ICMSRequestNameKey:     mustMarshalRawMessage(t, "req-a"),
		LegacyRequestNameKey(): mustMarshalRawMessage(t, "req-a"),
	}

	got, err := DecodeRequestName(fields)
	require.NoError(t, err)
	assert.Equal(t, "req-a", got)
}

func TestDecodeRequestNameAcceptsLegacyAlias(t *testing.T) {
	fields := map[string]json.RawMessage{
		LegacyRequestNameKey(): mustMarshalRawMessage(t, "req-a"),
	}

	got, err := DecodeRequestName(fields)
	require.NoError(t, err)
	assert.Equal(t, "req-a", got)
}

func TestDecodeRequestNameIgnoresNearMatchAlias(t *testing.T) {
	fields := map[string]json.RawMessage{
		"spatRequestName": mustMarshalRawMessage(t, "req-a"),
	}

	got, err := DecodeRequestName(fields)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestDecodeRequestNameRejectsConflictingAliases(t *testing.T) {
	fields := map[string]json.RawMessage{
		ICMSRequestNameKey:     mustMarshalRawMessage(t, "req-a"),
		LegacyRequestNameKey(): mustMarshalRawMessage(t, "req-b"),
	}

	got, err := DecodeRequestName(fields)
	require.Error(t, err)
	assert.Empty(t, got)
	assert.Contains(t, err.Error(), "conflicting")
}

func TestDecodeRequestNamespaceAcceptsLegacyAlias(t *testing.T) {
	fields := map[string]json.RawMessage{
		LegacyRequestNamespaceKey(): mustMarshalRawMessage(t, "ns-a"),
	}

	got, err := DecodeRequestNamespace(fields)
	require.NoError(t, err)
	assert.Equal(t, "ns-a", got)
}

func TestDecodeRequestNamespaceIgnoresNearMatchAlias(t *testing.T) {
	fields := map[string]json.RawMessage{
		"spatRequestNamespace": mustMarshalRawMessage(t, "ns-a"),
	}

	got, err := DecodeRequestNamespace(fields)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestDecodeRequestNamespaceRejectsInvalidJSON(t *testing.T) {
	fields := map[string]json.RawMessage{
		LegacyRequestNamespaceKey(): json.RawMessage(`123`),
	}

	got, err := DecodeRequestNamespace(fields)
	require.Error(t, err)
	assert.Empty(t, got)
	assert.Contains(t, err.Error(), "decode ICMS request namespace")
}

func mustMarshalRawMessage(t *testing.T, value string) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return json.RawMessage(data)
}
