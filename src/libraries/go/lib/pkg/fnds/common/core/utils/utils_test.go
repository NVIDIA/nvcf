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

package utils

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func assertBool(t *testing.T, expected bool, actual bool) {
	if expected != actual {
		t.Errorf("expected %v, got %v", expected, actual)
	}
}

func assertNoError(t *testing.T, err error) {
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestHasZeroValues(t *testing.T) {
	type testStruct struct {
		Str     string
		Uuid    uuid.UUID
		Time    time.Time
		Details json.RawMessage
	}

	t.Run("happy path", func(t *testing.T) {
		ts := testStruct{
			Str:     "test",
			Uuid:    uuid.New(),
			Time:    time.Now(),
			Details: json.RawMessage(`{"key": "value"}`),
		}
		assertBool(t, false, HasZeroValues(ts))
	})
	t.Run("str empty", func(t *testing.T) {
		ts := testStruct{
			Uuid:    uuid.New(),
			Time:    time.Time{},
			Details: json.RawMessage(""),
		}
		assertBool(t, true, HasZeroValues(ts))
	})

	t.Run("uuid empty", func(t *testing.T) {
		ts := testStruct{
			Str:     "test",
			Time:    time.Now(),
			Details: json.RawMessage(`{"key": "value"}`),
		}
		assertBool(t, true, HasZeroValues(ts))
	})

	t.Run("time empty", func(t *testing.T) {
		ts := testStruct{
			Str:     "test",
			Uuid:    uuid.New(),
			Details: json.RawMessage(`{"key": "value"}`),
		}
		assertBool(t, true, HasZeroValues(ts))
	})

	t.Run("details default", func(t *testing.T) {
		ts := testStruct{
			Str:  "test",
			Uuid: uuid.New(),
			Time: time.Now(),
		}
		assertBool(t, true, HasZeroValues(ts))
	})

	t.Run("json unmarshal missing details", func(t *testing.T) {
		jsonString := `{"Str":"test","Uuid":"123e4567-e89b-12d3-a456-426614174000","Time":"2021-09-01T00:00:00Z"}`
		var ts testStruct
		err := json.Unmarshal([]byte(jsonString), &ts)
		assertNoError(t, err)
		assertBool(t, true, HasZeroValues(ts))
	})

	t.Run("json unmarshal null details", func(t *testing.T) {
		jsonString := `{"Str":"test","Uuid":"123e4567-e89b-12d3-a456-426614174000","Time":"2021-09-01T00:00:00Z","Details":null}`
		var ts testStruct
		err := json.Unmarshal([]byte(jsonString), &ts)
		assertNoError(t, err)
		assertBool(t, false, HasZeroValues(ts))
	})

	t.Run("json unmarshal {} details", func(t *testing.T) {
		jsonString := `{"Str":"test","Uuid":"123e4567-e89b-12d3-a456-426614174000","Time":"2021-09-01T00:00:00Z","Details":{}}`
		var ts testStruct
		err := json.Unmarshal([]byte(jsonString), &ts)
		assertNoError(t, err)
		assertBool(t, false, HasZeroValues(ts))
	})
	t.Run("nil pointer", func(t *testing.T) {
		var ts *testStruct
		assertBool(t, true, HasZeroValues(ts))
	})

	t.Run("non-struct pointer", func(t *testing.T) {
		str := "test"
		assertBool(t, false, HasZeroValues(&str))
	})

	t.Run("non-struct value", func(t *testing.T) {
		str := "test"
		assertBool(t, false, HasZeroValues(str))
	})
}
