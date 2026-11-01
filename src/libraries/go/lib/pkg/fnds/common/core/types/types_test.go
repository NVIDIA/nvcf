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

package types

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewDeploymentStats(t *testing.T) {
	id := uuid.New()
	stats := NewDeploymentStats(id, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13)
	assert.Equal(t, id, stats.FunctionVersionId)
	assert.Equal(t, 1, stats.Pending)
	assert.Equal(t, 2, stats.PendingError)
	assert.Equal(t, 3, stats.Building)
	assert.Equal(t, 4, stats.BuildingError)
	assert.Equal(t, 5, stats.DownloadingModel)
	assert.Equal(t, 6, stats.DownloadingModelError)
	assert.Equal(t, 7, stats.DownloadingContainer)
	assert.Equal(t, 8, stats.DownloadingContainerError)
	assert.Equal(t, 9, stats.InitializingContainer)
	assert.Equal(t, 10, stats.InitializingContainerError)
	assert.Equal(t, 11, stats.Ready)
	assert.Equal(t, 12, stats.RequestingTermination)
	assert.Equal(t, 13, stats.Destroyed)
}

func TestNewStageTransitionEvent_Valid(t *testing.T) {
	ncaID := "nca-test-001"
	funcID := uuid.New()
	versionID := uuid.New()
	instanceID := "inst-001"
	event := "ACTIVE"
	eventType := "STAGE_TRANSITION"
	ts := time.Now()
	details := json.RawMessage("{}")

	ste, err := NewStageTransitionEvent(ncaID, funcID, versionID, instanceID, event, eventType, ts, details)
	require.NoError(t, err)
	assert.Equal(t, ncaID, ste.NcaId)
	assert.Equal(t, funcID, ste.FunctionId)
	assert.Equal(t, versionID, ste.FunctionVersionId)
	assert.Equal(t, instanceID, ste.InstanceId)
	assert.Equal(t, event, ste.Event)
	assert.Equal(t, eventType, ste.EventType)
	assert.Equal(t, ts, ste.Timestamp)
	assert.Equal(t, details, ste.Details)

}

func TestNewStageTransitionEvent_InvalidZeroNcaID(t *testing.T) {
	_, err := NewStageTransitionEvent("", uuid.New(), uuid.New(), "inst", "event", "type",
		time.Now(), json.RawMessage("{}"))
	assert.Error(t, err)
}

func TestNewStageTransitionEvent_InvalidNilUUID(t *testing.T) {
	_, err := NewStageTransitionEvent("nca", uuid.Nil, uuid.New(), "inst", "event", "type",
		time.Now(), json.RawMessage("{}"))
	assert.Error(t, err)
}

func TestNewDeploymentStageTransitionEvent_Valid(t *testing.T) {
	ncaID := "nca-test-001"
	funcID := uuid.New()
	versionID := uuid.New()
	deployID := uuid.New()
	instanceID := "inst-001"
	event := "ACTIVE"
	eventType := "DEPLOYMENT_STAGE"
	ts := time.Now()
	details := json.RawMessage("{}")

	ste, err := NewDeploymentStageTransitionEvent(ncaID, funcID, versionID, deployID, instanceID, event, eventType, ts, details)
	require.NoError(t, err)
	assert.Equal(t, ncaID, ste.NcaId)
	assert.Equal(t, funcID, ste.FunctionId)
	assert.Equal(t, versionID, ste.FunctionVersionId)
	assert.Equal(t, deployID, ste.DeploymentId)
	assert.Equal(t, instanceID, ste.InstanceId)
	assert.Equal(t, event, ste.Event)
	assert.Equal(t, eventType, ste.EventType)
	assert.Equal(t, ts, ste.Timestamp)
	assert.Equal(t, details, ste.Details)
}

func TestNewDeploymentStageTransitionEvent_MissingNcaID(t *testing.T) {
	_, err := NewDeploymentStageTransitionEvent("", uuid.New(), uuid.New(), uuid.New(),
		"inst", "event", "type", time.Now(), json.RawMessage("{}"))
	assert.Error(t, err)
}

func TestNewDeploymentStageTransitionEvent_MissingFunctionID(t *testing.T) {
	_, err := NewDeploymentStageTransitionEvent("nca", uuid.Nil, uuid.New(), uuid.New(),
		"inst", "event", "type", time.Now(), json.RawMessage("{}"))
	assert.Error(t, err)
}

func TestNewInstance(t *testing.T) {
	instanceID := "inst-001"
	lastEvent := "ACTIVE"
	details := json.RawMessage("{}")
	ts := time.Now()

	inst := NewInstance(instanceID, lastEvent, details, ts, ts, "GPU")
	assert.Equal(t, instanceID, inst.InstanceId)
	assert.Equal(t, lastEvent, inst.LastEvent)
	assert.Equal(t, "GPU", inst.InstanceType)
	assert.Equal(t, details, inst.LastEventDetails)
	assert.True(t, inst.LastEventTimestamp.Equal(ts), "expected LastEventTimestamp to equal ts")
	assert.True(t, inst.DeployStartTimestamp.Equal(ts), "expected DeployStartTimestamp to equal ts")
}
