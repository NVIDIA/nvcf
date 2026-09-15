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

package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/linkedin/goavro/v2"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.uber.org/zap"

	types "github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/constants"
)

const stageTransitionEventSchema = `{
	"type": "record",
	"name": "stage_transition_event",
	"fields": [
		{"name": "NcaId", "type": {
					"type": "string",
					"logicalType": "uuid"
				 }},
		{"name": "FunctionId", "type": {
					"type": "string",
					"logicalType": "uuid"
				 }},
		{"name": "FunctionVersionId", "type": {
					"type": "string",
					"logicalType": "uuid"
				 }},
		{"name": "InstanceId", "type": {
					"type": "string",
					"logicalType": "uuid"
				 }},
		{"name": "Event", "type": "string"},
		{"name": "EventType", "type": "string"},
		{"name": "Timestamp", "type": {
				 "type": "long",
				 "logicalType": "timestamp-micros"
				}},
		{"name": "Details", "type": "string"}
	]
}
`

func CreateAvroStageTransitionEventCodec(logger *otelzap.Logger) *goavro.Codec {
	codec, err := goavro.NewCodec(stageTransitionEventSchema)
	if err != nil {
		logger.Fatal(fmt.Sprintf("Error creating Avro codec: %v", err))
	}
	return codec
}

func (c *Codex) DecodeStageTransitionEvent(traceCtx context.Context, data []byte) (types.StageTransitionEvent, error) {
	// Tracing is now handled by the external library

	decodedEvent, _, err := c.stageTransitionEvent.NativeFromBinary(data)
	if err != nil {
		c.logger.ErrorContext(traceCtx, "failed to decode event", zap.Error(err))
	}

	// Access fields from decoded map
	if decodedMap, ok := decodedEvent.(map[string]interface{}); ok {
		ncaId := decodedMap["NcaId"].(string)
		functionId := decodedMap["FunctionId"].(string)
		functionVersionId := decodedMap["FunctionVersionId"].(string)
		instanceId := decodedMap["InstanceId"].(string)
		event := decodedMap["Event"].(string)
		eventType := decodedMap["EventType"].(string)
		timestamp := decodedMap["Timestamp"].(time.Time)
		detailsStr := decodedMap["Details"].(string)
		details := json.RawMessage(detailsStr)

		functionIdUUID, err := uuid.Parse(functionId)
		if err != nil {
			return types.ErrStageTransitionEvent, err
		}
		functionVersionIdUUID, err := uuid.Parse(functionVersionId)
		if err != nil {
			return types.ErrStageTransitionEvent, err
		}
		//instanceIdUUID, err := uuid.Parse(instanceId)
		//if err != nil {
		//	return ErrStageTransitionEvent, err
		//}

		ste, err := types.NewStageTransitionEvent(
			ncaId, functionIdUUID, functionVersionIdUUID, instanceId, event, eventType, timestamp, details)
		if err != nil {
			c.logger.ErrorContext(traceCtx, "failed to create stage transition event", zap.Error(err))
			return types.ErrStageTransitionEvent, err
		}
		return ste, nil

	} else {
		c.logger.ErrorContext(traceCtx, "decoded data is not a map")
		return types.ErrStageTransitionEvent, fmt.Errorf("decoded data is not a map")
	}
}

func (c *Codex) EncodeStageTransitionEvent(traceCtx context.Context, event types.StageTransitionEvent) ([]byte, error) {
	// Tracing is now handled by the external library

	detailsStr := string(event.Details)

	ste := map[string]interface{}{
		"NcaId":             event.NcaId,
		"FunctionId":        event.FunctionId.String(),
		"FunctionVersionId": event.FunctionVersionId.String(),
		"InstanceId":        event.InstanceId,
		"Event":             event.Event,
		"EventType":         event.EventType,
		"Timestamp":         event.Timestamp,
		"Details":           detailsStr,
	}
	binarySte, err := c.stageTransitionEvent.BinaryFromNative(nil, ste)
	if err != nil {
		c.logger.ErrorContext(traceCtx, "failed to encode stage transition event", zap.Error(err))
		return nil, err
	}

	return binarySte, nil
}

type Event struct {
	Name string
}

var ErrEvent = Event{Name: "INVALID_EVENT"}

func NewEvent(name string) (Event, error) {
	if _, ok := constants.ValidEvents[name]; !ok {
		return ErrEvent, fmt.Errorf("event is not valid")
	}
	return Event{Name: name}, nil
}

func ParseInstanceId(input string) (string, uuid.UUID, string, error) {
	bytes := []byte(input)
	i := 0
	offset := 36
	if len(bytes) >= offset {
		for ; i < len(bytes); i++ {
			prefix := bytes[0:i]
			body := bytes[i : i+offset]
			suffix := bytes[i+offset:]
			parsed, err := uuid.Parse(string(body))
			if err != nil {
				continue
			} else {
				return string(prefix), parsed, string(suffix), nil
			}
		}
	}
	return "error", uuid.Nil, "", fmt.Errorf("invalid instance id")
}
