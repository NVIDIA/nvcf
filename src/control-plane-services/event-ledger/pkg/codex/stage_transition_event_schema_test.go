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
	"log"
	"reflect"
	"strconv"
	"testing"
	"time"

	types "github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/testutils"

	"github.com/google/uuid"
)

func TestSTECodec(t *testing.T) {
	tests := []struct {
		name string
	}{
		{
			name: "BaseCase",
		},
	}
	for _, tt := range tests {

		t.Run(tt.name, func(t *testing.T) {
			logger := testutils.InitTestLogger(t)
			codex := NewCodex(logger)
			timestamp := time.Date(2025, time.January, 1, 12, 0, 0, 0, time.UTC)
			details := json.RawMessage(`{"type": "testType", "timestamp": "` + strconv.FormatInt(timestamp.UnixNano(), 10) + `"}`)
			ste, _ := types.NewStageTransitionEvent(
				"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
				uuid.New(),
				uuid.New(),
				uuid.New().String(),
				"test",
				"testType",
				timestamp,
				details)

			encoded, err := codex.EncodeStageTransitionEvent(context.Background(), ste)
			if err != nil {
				t.Errorf("error encoding message: %s", err.Error())
			}
			decoded, err := codex.DecodeStageTransitionEvent(context.Background(), encoded)
			if err != nil {
				t.Errorf("error decoding message: %s", err.Error())
			}
			if !reflect.DeepEqual(decoded, ste) {
				t.Errorf("decoded message body is wrong")
			}
		})
	}
}

func TestSTETruncation(t *testing.T) {
	t.Run("truncated payload", func(t *testing.T) {
		logger := testutils.InitTestLogger(t)
		codex := NewCodex(logger)
		details := json.RawMessage(`{"type":"testType","timestamp":"` + strconv.FormatInt(time.Now().UnixNano(), 10) + `"}`)
		ste, _ := types.NewStageTransitionEvent(
			"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			uuid.New(),
			uuid.New(),
			uuid.New().String(),
			"test",
			"testType",
			time.Now().UTC(),
			details)

		encoded, err := codex.EncodeStageTransitionEvent(context.Background(), ste)
		if err != nil {
			t.Errorf("error encoding message: %s", err.Error())
		}
		bustedDecode, err := codex.DecodeStageTransitionEvent(context.Background(), encoded[:len(encoded)-1])
		if err == nil {
			t.Errorf("erroneously decoded truncated message")
		}
		if !reflect.DeepEqual(bustedDecode, types.ErrStageTransitionEvent) {
			t.Errorf("decode returned wrong error-case struct")
		}
	})
}

func TestBadInputs(t *testing.T) {
	tests := []struct {
		name              string
		NcaId             string
		FunctionId        uuid.UUID
		FunctionVersionId uuid.UUID
		InstanceId        string
		Event             string
		EventType         string
		Timestamp         time.Time
		Details           json.RawMessage
	}{
		{
			name:              "Bad NcaId",
			NcaId:             "",
			FunctionId:        uuid.New(),
			FunctionVersionId: uuid.New(),
			InstanceId:        uuid.New().String(),
			Event:             "test",
			EventType:         "testType",
			Timestamp:         time.Now().UTC(),
			Details:           json.RawMessage(`{"type":"testType","timestamp":"` + strconv.FormatInt(time.Now().UnixNano(), 10) + `"}`),
		},
		{
			name:              "Bad FunctionId",
			NcaId:             "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			FunctionId:        uuid.Nil,
			FunctionVersionId: uuid.New(),
			InstanceId:        uuid.New().String(),
			Event:             "test",
			EventType:         "testType",
			Timestamp:         time.Now().UTC(),
			Details:           json.RawMessage(`{"type":"testType","timestamp":"` + strconv.FormatInt(time.Now().UnixNano(), 10) + `"}`),
		},
		{
			name:              "Bad FunctionVersionId",
			NcaId:             "0AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			FunctionId:        uuid.New(),
			FunctionVersionId: uuid.Nil,
			InstanceId:        uuid.New().String(),
			Event:             "test",
			EventType:         "testType",
			Timestamp:         time.Now().UTC(),
			Details:           json.RawMessage(`{"type":"testType","timestamp":"` + strconv.FormatInt(time.Now().UnixNano(), 10) + `"}`),
		},
		{
			name:              "Bad InstanceId",
			NcaId:             "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			FunctionId:        uuid.New(),
			FunctionVersionId: uuid.New(),
			InstanceId:        "",
			Event:             "test",
			EventType:         "testType",
			Timestamp:         time.Now().UTC(),
			Details:           json.RawMessage(`{"type":"testType","timestamp":"` + strconv.FormatInt(time.Now().UnixNano(), 10) + `"}`),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ste, err := types.NewStageTransitionEvent(tt.NcaId, tt.FunctionId, tt.FunctionVersionId, tt.InstanceId, tt.Event, tt.EventType, tt.Timestamp, tt.Details)
			if err == nil {
				t.Errorf("should have an error for an invalid input")
			}
			if !reflect.DeepEqual(ste, types.ErrStageTransitionEvent) {
				t.Errorf("should have returned ErrStageTransitionEvent")
			}
		})
	}
}

func TestParseInstanceId(t *testing.T) {
	tests := []struct {
		name string
		id   string
	}{
		{
			name: "prefix and suffix",
			id:   "0-sr-a91e51be-8b43-4acb-b50c-e744b4457fc6.np-ash-04-gs",
		},
		{
			name: "prefix only",
			id:   "0-sr-a91e51be-8b43-4acb-b50c-e744b4457fc6",
		},
		{
			name: "suffix only",
			id:   "a91e51be-8b43-4acb-b50c-e744b4457fc6.np-ash-04-gs",
		},
		{
			name: "pure uuid",
			id:   "a91e51be-8b43-4acb-b50c-e744b4457fc6",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefix, instanceId, suffix, err := ParseInstanceId(tt.id)
			if err != nil {
				t.Errorf("should not have an error")
			}
			log.Printf("prefix: %s, instanceId: %s, suffix: %s", prefix, instanceId, suffix)
		})
	}

}
