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

package fnds

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	nvcffndstypes "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/fnds/common/core/types"
)

func TestNewFakeFndsClient(t *testing.T) {
	tests := []struct {
		name     string
		ncaId    string
		expected string
	}{
		{
			name:     "EmptyNcaId",
			ncaId:    "",
			expected: "",
		},
		{
			name:     "ValidNcaId",
			ncaId:    "test-nca-id",
			expected: "test-nca-id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewFakeClient(tt.ncaId)
			assert.NotNil(t, client, "Client should not be nil")
			assert.Equal(t, tt.expected, client.NcaID, "NcaId should match expected value")
		})
	}
}

func TestFakeFndsClient_GetNcaId(t *testing.T) {
	tests := []struct {
		name     string
		ncaId    string
		expected string
	}{
		{
			name:     "EmptyNcaId",
			ncaId:    "",
			expected: "",
		},
		{
			name:     "ValidNcaId",
			ncaId:    "test-nca-id",
			expected: "test-nca-id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fakeFndsClient{NcaID: tt.ncaId}
			result := client.GetNcaId()
			assert.Equal(t, tt.expected, result, "GetNcaId should return the correct NcaId")
		})
	}
}

func TestFakeFndsClient_CreateEvent(t *testing.T) {
	// Create test StageTransitionEvent
	functionId := uuid.New()
	functionVersionId := uuid.New()
	instanceId := uuid.New().String()
	timestamp := time.Now().UTC()
	details := json.RawMessage(`{"type": "testType", "timestamp": "` + timestamp.Format(time.RFC3339Nano) + `"}`)

	tests := []struct {
		name      string
		ncaId     string
		eventData nvcffndstypes.StageTransitionEvent
	}{
		{
			name:  "ValidEvent",
			ncaId: "test-nca-id",
			eventData: nvcffndstypes.StageTransitionEvent{
				NcaId:             "test-nca-id",
				FunctionId:        functionId,
				FunctionVersionId: functionVersionId,
				InstanceId:        instanceId,
				Event:             "pending",
				EventType:         "testType",
				Timestamp:         timestamp,
				Details:           details,
			},
		},
		{
			name:  "InvalidEvent",
			ncaId: "test-nca-id",
			eventData: nvcffndstypes.StageTransitionEvent{
				NcaId:             "test-nca-id",
				FunctionId:        uuid.Nil,
				FunctionVersionId: uuid.Nil,
				InstanceId:        "",
				Event:             "invalid-event",
				EventType:         "testType",
				Timestamp:         timestamp,
				Details:           details,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fakeFndsClient{NcaID: tt.ncaId}
			err := client.CreateEvent(context.Background(), tt.eventData)
			assert.NoError(t, err, "CreateEvent should always return nil error for fakeFndsClient")
		})
	}
}
