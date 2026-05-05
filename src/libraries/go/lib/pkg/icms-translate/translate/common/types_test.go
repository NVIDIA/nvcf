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

package common

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_decodeTelemetriesLaunchSpecification(t *testing.T) {
	const (
		inB64  = `ewoidGVsZW1ldHJpZXMiOiB7CiAgImxvZ3NUZWxlbWV0cnkiOgogICAgIHsKICAgICAgICJwcm90b2NvbCI6ICJodHRwIiwKICAgICAgICJwcm92aWRlciI6ICJHUkFGQU5BX0NMT1VEIiwKICAgICAgICJlbmRwb2ludCI6ICJlbmRwb2ludCIsCiAgICAgICAibmFtZSI6ICJ0ZWxlbWV0cnktZm9vIgogICAgfSwKICAgICJtZXRyaWNzVGVsZW1ldHJ5IjogewogICAgICAicHJvdG9jb2wiOiAiaHR0cCIsCiAgICAgICJwcm92aWRlciI6ICJHUkFGQU5BX0NMT1VEIiwKICAgICAgImVuZHBvaW50IjogImVuZHBvaW50IiwKICAgICAgIm5hbWUiOiAidGVsZW1ldHJ5LWJheiIKICAgfSwKICAgInRyYWNlc1RlbGVtZXRyeSI6IHsKICAgICAicHJvdG9jb2wiOiAiaHR0cCIsCiAgICAgInByb3ZpZGVyIjogIkdSQUZBTkFfQ0xPVUQiLAogICAgICJlbmRwb2ludCI6ICJlbmRwb2ludCIsCiAgICAgIm5hbWUiOiAidGVsZW1ldHJ5LWJhciIKICAgfQogIH0KfQo=`
		inJSON = `
{
"telemetries": {
  "logsTelemetry":
     {
       "protocol": "http",
       "provider": "GRAFANA_CLOUD",
       "endpoint": "endpoint",
       "name": "telemetry-foo"
    },
    "metricsTelemetry": {
      "protocol": "http",
      "provider": "GRAFANA_CLOUD",
      "endpoint": "endpoint",
      "name": "telemetry-baz"
   },
   "tracesTelemetry": {
     "protocol": "http",
     "provider": "GRAFANA_CLOUD",
     "endpoint": "endpoint",
     "name": "telemetry-bar"
   }
  }
}
`
	)
	expTelLS := (*TelemetriesLaunchSpecification)(
		&TelemetriesLaunchSpecification{
			Telemetries: struct {
				Logs    *Telemetry `json:"logsTelemetry,omitempty"`
				Metrics *Telemetry `json:"metricsTelemetry,omitempty"`
				Traces  *Telemetry `json:"tracesTelemetry,omitempty"`
			}{
				Logs: &Telemetry{
					Protocol: "http",
					Endpoint: "endpoint",
					Provider: "GRAFANA_CLOUD",
					Name:     "telemetry-foo",
				},
				Metrics: &Telemetry{
					Protocol: "http",
					Endpoint: "endpoint",
					Provider: "GRAFANA_CLOUD",
					Name:     "telemetry-baz",
				},
				Traces: &Telemetry{
					Protocol: "http",
					Endpoint: "endpoint",
					Provider: "GRAFANA_CLOUD",
					Name:     "telemetry-bar",
				},
			},
		},
	)

	gotTelLS := &TelemetriesLaunchSpecification{}
	gotErr := gotTelLS.UnmarshalJSON(nil)
	assert.NoError(t, gotErr)
	assert.Equal(t, &TelemetriesLaunchSpecification{}, gotTelLS)

	gotTelLS = &TelemetriesLaunchSpecification{}
	gotErr = gotTelLS.UnmarshalJSON([]byte(inB64))
	assert.NoError(t, gotErr)
	assert.Equal(t, expTelLS, gotTelLS)

	gotTelLS = &TelemetriesLaunchSpecification{}
	gotErr = gotTelLS.UnmarshalJSON([]byte(inJSON))
	assert.NoError(t, gotErr)
	assert.Equal(t, expTelLS, gotTelLS)
}

func TestHasJSONPrefix(t *testing.T) {
	type spec struct {
		name     string
		input    []byte
		expected bool
	}

	cases := []spec{
		{name: "empty", input: nil, expected: false},
		{name: "empty string", input: []byte(""), expected: false},
		{name: "whitespace only", input: []byte("   \t\n"), expected: false},
		{name: "object", input: []byte(`{"a":1}`), expected: true},
		{name: "object with leading space", input: []byte("  \n\t{\"a\":1}"), expected: true},
		{name: "array", input: []byte(`[]`), expected: true},
		{name: "array with elements", input: []byte(`[1,2]`), expected: true},
		{name: "array with leading space", input: []byte("  [\n]"), expected: true},
		{name: "string json", input: []byte(`"hello"`), expected: false},
		{name: "number", input: []byte(`42`), expected: false},
		{name: "null", input: []byte(`null`), expected: false},
		{name: "true", input: []byte(`true`), expected: false},
		{name: "bare word", input: []byte(`notjson`), expected: false},
		{name: "open brace only", input: []byte(`{`), expected: true},
		{name: "open bracket only", input: []byte(`[`), expected: true},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, HasJSONPrefix(tt.input))
		})
	}
}

func TestMessageAction_Normalize(t *testing.T) {
	type spec struct {
		name     string
		input    MessageAction
		expected MessageAction
	}

	cases := []spec{
		{
			name:     "legacy function action normalizes",
			input:    MessageAction("RequestSparInstances"),
			expected: FunctionCreationAction,
		},
		{
			name:     "legacy task action normalizes",
			input:    MessageAction("RequestSparInstancesForTask"),
			expected: TaskCreationAction,
		},
		{
			name:     "already normalized function action unchanged",
			input:    FunctionCreationAction,
			expected: FunctionCreationAction,
		},
		{
			name:     "already normalized task action unchanged",
			input:    TaskCreationAction,
			expected: TaskCreationAction,
		},
		{
			name:     "termination action unchanged",
			input:    TerminationAction,
			expected: TerminationAction,
		},
		{
			name:     "unknown action unchanged",
			input:    MessageAction("SomeOther"),
			expected: MessageAction("SomeOther"),
		},
		{
			name:     "short string no panic",
			input:    MessageAction("Request"),
			expected: MessageAction("Request"),
		},
		{
			name:     "empty string no panic",
			input:    MessageAction(""),
			expected: MessageAction(""),
		},
		{
			name:     "similar pattern wrong char at pos 8",
			input:    MessageAction("RequestSomeInstances"),
			expected: MessageAction("RequestSomeInstances"),
		},
		{
			name:     "wrong prefix",
			input:    MessageAction("CreateSparInstances"),
			expected: MessageAction("CreateSparInstances"),
		},
		{
			name:     "lowercase case sensitive",
			input:    MessageAction("requestsparinstances"),
			expected: MessageAction("requestsparinstances"),
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.input.Normalize()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMessageAction_UnmarshalJSON(t *testing.T) {
	type spec struct {
		name     string
		json     string
		expected MessageAction
	}

	cases := []spec{
		{
			name:     "legacy RequestSparInstances normalizes to RequestICMSInstances",
			json:     `"RequestSparInstances"`,
			expected: RequestICMSInstances,
		},
		{
			name:     "new RequestICMSInstances unchanged",
			json:     `"RequestICMSInstances"`,
			expected: RequestICMSInstances,
		},
		{
			name:     "legacy RequestSparInstancesForTask normalizes to RequestICMSInstancesForTask",
			json:     `"RequestSparInstancesForTask"`,
			expected: RequestICMSInstancesForTask,
		},
		{
			name:     "new RequestICMSInstancesForTask unchanged",
			json:     `"RequestICMSInstancesForTask"`,
			expected: RequestICMSInstancesForTask,
		},
		{
			name:     "TerminateInstances unchanged",
			json:     `"TerminateInstances"`,
			expected: TerminationAction,
		},
		{
			name:     "unknown action passed through",
			json:     `"UnknownAction"`,
			expected: MessageAction("UnknownAction"),
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var action MessageAction
			err := json.Unmarshal([]byte(tt.json), &action)
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, action)
		})
	}
}

func TestMessageAction_UnmarshalJSON_InStruct(t *testing.T) {
	type testStruct struct {
		Action MessageAction `json:"action"`
		Name   string        `json:"name"`
	}

	type spec struct {
		name     string
		json     string
		expected testStruct
	}

	cases := []spec{
		{
			name: "legacy action in struct normalizes",
			json: `{"action":"RequestSparInstances","name":"test"}`,
			expected: testStruct{
				Action: FunctionCreationAction,
				Name:   "test",
			},
		},
		{
			name: "legacy task action in struct normalizes",
			json: `{"action":"RequestSparInstancesForTask","name":"task-test"}`,
			expected: testStruct{
				Action: TaskCreationAction,
				Name:   "task-test",
			},
		},
		{
			name: "new ICMS action in struct unchanged",
			json: `{"action":"RequestICMSInstances","name":"icms"}`,
			expected: testStruct{
				Action: FunctionCreationAction,
				Name:   "icms",
			},
		},
		{
			name: "termination action in struct",
			json: `{"action":"TerminateInstances","name":"terminate"}`,
			expected: testStruct{
				Action: TerminationAction,
				Name:   "terminate",
			},
		},
		{
			name: "empty action in struct",
			json: `{"action":"","name":"empty"}`,
			expected: testStruct{
				Action: MessageAction(""),
				Name:   "empty",
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var result testStruct
			err := json.Unmarshal([]byte(tt.json), &result)
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMessageAction_UnmarshalJSON_Errors(t *testing.T) {
	type spec struct {
		name string
		json string
	}

	cases := []spec{
		{
			name: "invalid json",
			json: `not valid json`,
		},
		{
			name: "number instead of string",
			json: `123`,
		},
		{
			name: "array instead of string",
			json: `["action"]`,
		},
		{
			name: "object instead of string",
			json: `{"action":"test"}`,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var action MessageAction
			err := json.Unmarshal([]byte(tt.json), &action)
			assert.Error(t, err)
		})
	}
}
