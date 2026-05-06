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
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/fnds/common/core/utils"
)

type DeploymentStats struct {
	FunctionVersionId          uuid.UUID `json:"functionVersionId"`
	Pending                    int       `json:"pending"`
	PendingError               int       `json:"pendingError"`
	Building                   int       `json:"building"`
	BuildingError              int       `json:"buildingError"`
	DownloadingModel           int       `json:"downloadingModel"`
	DownloadingModelError      int       `json:"downloadingModelError"`
	DownloadingContainer       int       `json:"downloadingContainer"`
	DownloadingContainerError  int       `json:"downloadingContainerError"`
	InitializingContainer      int       `json:"initializingContainer"`
	InitializingContainerError int       `json:"initializingContainerError"`
	Ready                      int       `json:"ready"`
	// Active                     int       `json:"active"`
	RequestingTermination int `json:"requestingTermination"`
	Destroyed             int `json:"destroyed"`
}

// func NewDeploymentStats(
//
//	functionVersionId uuid.UUID,
//	pending int, pendingError int, building int, buildingError int,
//	downloadingModel int, downloadingModelError int,
//	downloadingContainer int, downloadingContainerError int,
//	initializingContainer int, initializingContainerError int,
//	ready int, active int, requestingTermination int, destroyed int,
//
// ) DeploymentStats {
func NewDeploymentStats(
	functionVersionId uuid.UUID,
	pending int,
	pendingError int,
	building int,
	buildingError int,
	downloadingModel int,
	downloadingModelError int,
	downloadingContainer int,
	downloadingContainerError int,
	initializingContainer int,
	initializingContainerError int,
	ready int,
	requestingTermination int,
	destroyed int,
) DeploymentStats {
	return DeploymentStats{
		FunctionVersionId:          functionVersionId,
		Pending:                    pending,
		PendingError:               pendingError,
		Building:                   building,
		BuildingError:              buildingError,
		DownloadingModel:           downloadingModel,
		DownloadingModelError:      downloadingModelError,
		DownloadingContainer:       downloadingContainer,
		DownloadingContainerError:  downloadingContainerError,
		InitializingContainer:      initializingContainer,
		InitializingContainerError: initializingContainerError,
		Ready:                      ready,
		// Active:                     active,
		RequestingTermination: requestingTermination,
		Destroyed:             destroyed,
	}
}

var ErrDeploymentStats = DeploymentStats{
	FunctionVersionId:          uuid.Nil,
	Pending:                    0,
	PendingError:               0,
	Building:                   0,
	BuildingError:              0,
	DownloadingModel:           0,
	DownloadingModelError:      0,
	DownloadingContainer:       0,
	DownloadingContainerError:  0,
	InitializingContainer:      0,
	InitializingContainerError: 0,
	Ready:                      0,
	// Active:                     0,
	RequestingTermination: 0,
	Destroyed:             0,
}

type StageTransitionEvent struct {
	NcaId             string          `json:"ncaId"`
	FunctionId        uuid.UUID       `json:"functionId"`
	FunctionVersionId uuid.UUID       `json:"functionVersionId"`
	InstanceId        string          `json:"instanceId"`
	Event             string          `json:"event"`
	EventType         string          `json:"eventType"`
	Timestamp         time.Time       `json:"timestamp"`
	Details           json.RawMessage `json:"details"`
}

func NewStageTransitionEvent(
	ncaId string,
	functionId uuid.UUID,
	functionVersionId uuid.UUID,
	instanceId string,
	event string,
	eventType string,
	timestamp time.Time,
	details json.RawMessage,
) (StageTransitionEvent, error) {
	ste := StageTransitionEvent{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
		InstanceId:        instanceId,
		Event:             event,
		EventType:         eventType,
		Timestamp:         timestamp,
		Details:           details,
	}
	if utils.HasZeroValues(ste) {
		return ErrStageTransitionEvent, fmt.Errorf("invalid stage transition event: cannot have zero values")
	}
	return ste, nil
}

var ErrStageTransitionEvent = StageTransitionEvent{
	NcaId:             "error",
	FunctionId:        uuid.Nil,
	FunctionVersionId: uuid.Nil,
	InstanceId:        "error",
	Event:             "error",
	EventType:         "error",
	Timestamp:         time.Time{}, // Zero value for deterministic sentinel
	Details: json.RawMessage(`{
		"error": "invalid stage transition event",
		"reason": "unknown issue"
	}`),
}

// DeploymentStageTransitionEvent includes a DeploymentId along with standard event fields.
type DeploymentStageTransitionEvent struct {
	NcaId             string          `json:"ncaId"`
	FunctionId        uuid.UUID       `json:"functionId"`
	FunctionVersionId uuid.UUID       `json:"functionVersionId"`
	DeploymentId      uuid.UUID       `json:"deploymentId"`
	InstanceId        string          `json:"instanceId"`
	Event             string          `json:"event"`
	EventType         string          `json:"eventType"`
	Timestamp         time.Time       `json:"timestamp"`
	Details           json.RawMessage `json:"details"`
}

func NewDeploymentStageTransitionEvent(
	ncaId string,
	functionId, functionVersionId, deploymentId uuid.UUID,
	instanceId, event, eventType string,
	timestamp time.Time,
	details json.RawMessage,
) (DeploymentStageTransitionEvent, error) {
	ste := DeploymentStageTransitionEvent{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
		DeploymentId:      deploymentId,
		InstanceId:        instanceId,
		Event:             event,
		EventType:         eventType,
		Timestamp:         timestamp,
		Details:           details,
	}
	// Re-use HasZeroValues logic if appropriate, need to check its implementation
	// For now, basic check on key IDs
	if ncaId == "" || functionId == uuid.Nil || functionVersionId == uuid.Nil || instanceId == "" || event == "" || eventType == "" {
		return ErrDeploymentStageTransitionEvent, fmt.Errorf("invalid deployment stage transition event: missing required values")
	}
	return ste, nil
}

var ErrDeploymentStageTransitionEvent = DeploymentStageTransitionEvent{
	NcaId:             "error",
	FunctionId:        uuid.Nil,
	FunctionVersionId: uuid.Nil,
	DeploymentId:      uuid.Nil,
	InstanceId:        "error",
	Event:             "error",
	EventType:         "error",
	Timestamp:         time.Time{}, // Zero value for deterministic sentinel
	Details: json.RawMessage(`{
		"error": "invalid deployment stage transition event",
		"reason": "unknown issue"
	}`),
}

type Instance struct {
	InstanceId           string          `json:"instanceId"`
	LastEvent            string          `json:"lastEvent"`
	LastEventDetails     json.RawMessage `json:"lastEventDetails"`
	LastEventTimestamp   time.Time       `json:"lastEventTimestamp"`
	DeployStartTimestamp time.Time       `json:"deployStartTimestamp"`
	InstanceType         string          `json:"instanceType"`
}

func NewInstance(
	instanceId string,
	lastEvent string,
	lastEventDetails json.RawMessage,
	lastEventTimestamp time.Time,
	deployStartTimestamp time.Time,
	instanceType string,
) Instance {
	return Instance{
		InstanceId:           instanceId,
		LastEvent:            lastEvent,
		LastEventDetails:     lastEventDetails,
		LastEventTimestamp:   lastEventTimestamp,
		DeployStartTimestamp: deployStartTimestamp,
		InstanceType:         instanceType,
	}
}

var ErrInstance = Instance{
	InstanceId:           "error",
	LastEvent:            "error",
	LastEventDetails:     json.RawMessage(`{ "error": "invalid stage transition event" }`),
	LastEventTimestamp:   time.Time{}, // Zero value for deterministic sentinel
	DeployStartTimestamp: time.Time{}, // Zero value for deterministic sentinel
	InstanceType:         "error",
}

type ProblemDetails struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}
