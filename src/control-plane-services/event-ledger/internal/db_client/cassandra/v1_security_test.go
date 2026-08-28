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

package cassandra

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
)

func TestBuildEventsInsertUsesConditionalWrite(t *testing.T) {
	query, _, err := buildEventsInsert(types.StageTransitionEvent{})
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(query, "IF NOT EXISTS"))
}

func TestBuildDeploymentEventsInsertUsesConditionalWrite(t *testing.T) {
	query, _, err := buildDeploymentEventsInsert(types.DeploymentStageTransitionEvent{})
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(query, "IF NOT EXISTS"))
}

func TestExtractInstanceType(t *testing.T) {
	tests := []struct {
		name    string
		details json.RawMessage
		want    string
		wantErr bool
	}{
		{name: "nil details", details: nil, want: ""},
		{name: "empty details", details: json.RawMessage{}, want: ""},
		{name: "missing instance type", details: json.RawMessage(`{"other":"value"}`), want: ""},
		{name: "null instance type", details: json.RawMessage(`{"instanceType":null}`), want: ""},
		{name: "valid instance type", details: json.RawMessage(`{"instanceType":"gpu"}`), want: "gpu"},
		{name: "non-string instance type", details: json.RawMessage(`{"instanceType":123}`), wantErr: true},
		{name: "invalid JSON", details: json.RawMessage(`{"instanceType":`), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractInstanceType(tt.details)
			if tt.wantErr {
				require.Error(t, err)
				require.Empty(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
