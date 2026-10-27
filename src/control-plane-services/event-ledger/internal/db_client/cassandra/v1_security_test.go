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
	"errors"
	"strings"
	"testing"

	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
)

func TestBuildEventsInsertUsesUnconditionalWrite(t *testing.T) {
	query, _, err := buildEventsInsert(types.StageTransitionEvent{})
	require.NoError(t, err)
	require.False(t, strings.Contains(query, "IF NOT EXISTS"))
}

func TestBuildDeploymentEventsInsertUsesUnconditionalWrite(t *testing.T) {
	query, _, err := buildDeploymentEventsInsert(types.DeploymentStageTransitionEvent{})
	require.NoError(t, err)
	require.False(t, strings.Contains(query, "IF NOT EXISTS"))
}

func TestV1EventLookupDoesNotUseDeploymentID(t *testing.T) {
	versionID := uuid.New()
	query, args := buildEventsExistenceSelect(types.StageTransitionEvent{
		FunctionVersionId: versionID,
		InstanceId:        "instance-1",
		Event:             "ready",
	})
	require.Equal(t, "SELECT event FROM events WHERE function_version_id = ? AND instance_id = ? AND event = ?", query)
	require.Equal(t, []interface{}{gocql.UUID(versionID), "instance-1", "ready"}, args)
}

func TestV2EventLookupIncludesDeploymentID(t *testing.T) {
	versionID, deploymentID := uuid.New(), uuid.New()
	query, args := buildDeploymentEventsExistenceSelect(types.DeploymentStageTransitionEvent{
		FunctionVersionId: versionID,
		DeploymentId:      deploymentID,
		InstanceId:        "instance-1",
		Event:             "ready",
	})
	require.Equal(t, "SELECT event FROM events_v2 WHERE function_version_id = ? AND deployment_id = ? AND instance_id = ? AND event = ?", query)
	require.Equal(t, []interface{}{gocql.UUID(versionID), gocql.UUID(deploymentID), "instance-1", "ready"}, args)
}

func TestInsertEventIfAbsent(t *testing.T) {
	readFailure := errors.New("lookup unavailable")
	writeFailure := errors.New("write unavailable")
	for _, tc := range []struct {
		name       string
		readErr    error
		writeErr   error
		wantWrite  bool
		wantInsert bool
		wantErr    error
	}{
		{name: "existing row is a duplicate"},
		{name: "missing row is inserted", readErr: gocql.ErrNotFound, wantWrite: true, wantInsert: true},
		{name: "read error does not write", readErr: readFailure, wantErr: readFailure},
		{name: "failed write does not claim success", readErr: gocql.ErrNotFound, writeErr: writeFailure, wantWrite: true, wantErr: writeFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inserted := true // A retry must clear a previous attempt's result.
			writes := 0
			err := insertEventIfAbsent(func() error { return tc.readErr }, func() error {
				writes++
				return tc.writeErr
			}, &inserted)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			if tc.wantWrite {
				require.Equal(t, 1, writes)
			} else {
				require.Zero(t, writes)
			}
			require.Equal(t, tc.wantInsert, inserted)
		})
	}
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
