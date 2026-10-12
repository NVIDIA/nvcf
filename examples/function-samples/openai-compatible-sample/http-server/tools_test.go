// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const echoNamespaceTools = `[{"type":"namespace","name":"diagnostics","description":"Compatibility test tools.","tools":[
	{"type":"function","name":"echo","description":"Return the value.","parameters":{"type":"object",
		"properties":{"value":{"type":"string"},"count":{"type":"integer"},"mode":{"type":"string","enum":["fast","slow"]}},
		"required":["value","mode"],"additionalProperties":false}},
	{"type":"custom","name":"apply_patch","format":{"type":"text"}}]}]`

func responsesBody(extra string) string {
	return `{"model":"test-model",` + extra + `}`
}

func outputItem(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	output, ok := body["output"].([]any)
	if !ok || len(output) != 1 {
		t.Fatalf("output = %#v, want one item", body["output"])
	}
	return output[0].(map[string]any)
}

func TestResponsesNamespaceFunctionCall(t *testing.T) {
	recorder := postJSON(t, "/v1/responses", responsesBody(
		`"tools":`+echoNamespaceTools+`,"input":[{"role":"user","content":[{"type":"input_text","text":"please call echo value=NAMESPACE_OK count=3"}]}]`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := responseMap(t, recorder)
	item := outputItem(t, body)
	if item["type"] != "function_call" || item["namespace"] != "diagnostics" || item["name"] != "echo" || item["status"] != "completed" {
		t.Fatalf("unexpected item: %#v", item)
	}
	if id, _ := item["call_id"].(string); !strings.HasPrefix(id, "call_") {
		t.Fatalf("call_id = %#v", item["call_id"])
	}
	var arguments map[string]any
	if err := json.Unmarshal([]byte(item["arguments"].(string)), &arguments); err != nil {
		t.Fatalf("arguments not JSON: %v", err)
	}
	if arguments["value"] != "NAMESPACE_OK" || arguments["count"] != float64(3) || arguments["mode"] != "fast" {
		t.Fatalf("arguments = %#v", arguments)
	}
	if tools, _ := body["tools"].([]any); len(tools) != 1 {
		t.Fatalf("tools not echoed: %#v", body["tools"])
	}
	usage := body["usage"].(map[string]any)
	if usage["input_tokens"].(float64) <= 0 || usage["output_tokens"].(float64) <= 0 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestResponsesAutoWithoutNamedToolReturnsText(t *testing.T) {
	recorder := postJSON(t, "/v1/responses", responsesBody(`"tools":`+echoNamespaceTools+`,"input":"hello"`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if item := outputItem(t, responseMap(t, recorder)); item["type"] != "message" {
		t.Fatalf("item = %#v, want message", item)
	}
}

func TestResponsesToolChoice(t *testing.T) {
	tests := []struct {
		name       string
		choice     string
		wantType   string
		wantName   string
		wantStatus int
	}{
		{"none", `"none"`, "message", "", 200},
		{"required", `"required"`, "function_call", "echo", 200},
		{"named custom", `{"type":"custom","name":"apply_patch"}`, "custom_tool_call", "apply_patch", 200},
		{"named function in namespace", `{"type":"function","name":"echo","namespace":"diagnostics"}`, "function_call", "echo", 200},
		{"named missing", `{"type":"function","name":"missing"}`, "", "", 400},
		{"invalid string", `"sometimes"`, "", "", 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := postJSON(t, "/v1/responses", responsesBody(
				`"tools":`+echoNamespaceTools+`,"tool_choice":`+tt.choice+`,"input":"hello"`))
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, tt.wantStatus, recorder.Body.String())
			}
			if tt.wantStatus != 200 {
				return
			}
			item := outputItem(t, responseMap(t, recorder))
			if item["type"] != tt.wantType || (tt.wantName != "" && item["name"] != tt.wantName) {
				t.Fatalf("item = %#v", item)
			}
		})
	}
}

func TestResponsesCustomToolCall(t *testing.T) {
	recorder := postJSON(t, "/v1/responses", responsesBody(
		`"tools":`+echoNamespaceTools+`,"input":"call diagnostics.apply_patch input=*** Begin Patch"`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	item := outputItem(t, responseMap(t, recorder))
	if item["type"] != "custom_tool_call" || item["namespace"] != "diagnostics" || item["input"] != "*** Begin Patch" {
		t.Fatalf("item = %#v", item)
	}
	if id, _ := item["id"].(string); !strings.HasPrefix(id, "ctc_") {
		t.Fatalf("id = %#v", item["id"])
	}
}

func TestResponsesTopLevelFunctionOmitsNamespace(t *testing.T) {
	recorder := postJSON(t, "/v1/responses", responsesBody(
		`"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}],`+
			`"tool_choice":"required","input":"weather?"`))
	item := outputItem(t, responseMap(t, recorder))
	if _, present := item["namespace"]; present {
		t.Fatalf("namespace present on top-level tool: %#v", item)
	}
	if item["arguments"] != `{"city":"mock_city"}` {
		t.Fatalf("arguments = %#v", item["arguments"])
	}
}

func TestResponsesToolOutputReply(t *testing.T) {
	history := `{"type":"function_call","namespace":"diagnostics","call_id":"c1","name":"echo","arguments":"{}"},`
	for name, output := range map[string]string{
		"string": `"NAMESPACE_OK"`,
		"array":  `[{"type":"input_text","text":"NAMESPACE_OK"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			recorder := postJSON(t, "/v1/responses", responsesBody(
				`"tools":`+echoNamespaceTools+`,"tool_choice":"required","input":[{"role":"user","content":"go"},`+history+
					`{"type":"function_call_output","call_id":"c1","output":`+output+`}]`))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
			}
			item := outputItem(t, responseMap(t, recorder))
			text := item["content"].([]any)[0].(map[string]any)["text"]
			if item["type"] != "message" || text != "The diagnostics.echo tool returned: NAMESPACE_OK" {
				t.Fatalf("item = %#v", item)
			}
		})
	}
}

func TestResponsesCustomToolOutputReply(t *testing.T) {
	recorder := postJSON(t, "/v1/responses", responsesBody(
		`"input":[{"type":"custom_tool_call","call_id":"c2","name":"apply_patch","input":"x"},`+
			`{"type":"custom_tool_call_output","call_id":"c2","output":[{"type":"input_text","text":"applied"},{"type":"input_image","image_url":"https://example.com/a.png"}]}]`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	item := outputItem(t, responseMap(t, recorder))
	text := item["content"].([]any)[0].(map[string]any)["text"]
	if text != "The apply_patch tool returned: applied\n[image]" {
		t.Fatalf("text = %#v", text)
	}
}

func TestResponsesRejectsInvalidToolRequests(t *testing.T) {
	tests := map[string]struct {
		body      string
		wantParam string
	}{
		"namespace without name": {
			`"tools":[{"type":"namespace","description":"d","tools":[{"type":"function","name":"f"}]}],"input":"x"`, "tools[0].name"},
		"namespace without description": {
			`"tools":[{"type":"namespace","name":"n","tools":[{"type":"function","name":"f"}]}],"input":"x"`, "tools[0].description"},
		"namespace without tools": {
			`"tools":[{"type":"namespace","name":"n","description":"d","tools":[]}],"input":"x"`, "tools[0].tools"},
		"nested unsupported type": {
			`"tools":[{"type":"namespace","name":"n","description":"d","tools":[{"type":"mcp"}]}],"input":"x"`, "tools[0].tools[0].type"},
		"function without name": {
			`"tools":[{"type":"function"}],"input":"x"`, "tools[0].name"},
		"output without matching call": {
			`"input":[{"type":"function_call_output","call_id":"nope","output":"x"}]`, "input"},
		"function output for custom call": {
			`"input":[{"type":"custom_tool_call","call_id":"c","name":"n","input":"x"},{"type":"function_call_output","call_id":"c","output":"x"}]`, "input"},
		"output number": {
			`"input":[{"type":"function_call","call_id":"c","name":"n","arguments":"{}"},{"type":"function_call_output","call_id":"c","output":42}]`, "input[1].output"},
		"output audio part": {
			`"input":[{"type":"function_call","call_id":"c","name":"n","arguments":"{}"},` +
				`{"type":"function_call_output","call_id":"c","output":[{"type":"input_audio"}]}]`, "input[1].output[0].type"},
		"call without call_id": {
			`"input":[{"type":"function_call","name":"n","arguments":"{}"}]`, "input[0].call_id"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := postJSON(t, "/v1/responses", responsesBody(tt.body))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
			}
			errBody := responseMap(t, recorder)["error"].(map[string]any)
			if errBody["param"] != tt.wantParam || errBody["type"] != "invalid_request_error" {
				t.Fatalf("error = %#v, want param %q", errBody, tt.wantParam)
			}
		})
	}
}

func TestResponsesIgnoresOtherToolTypes(t *testing.T) {
	recorder := postJSON(t, "/v1/responses", responsesBody(`"tools":[{"type":"web_search"}],"input":"hi"`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestResponsesFunctionCallStream(t *testing.T) {
	recorder := postJSON(t, "/v1/responses", responsesBody(
		`"stream":true,"tools":`+echoNamespaceTools+`,"input":"call echo value=NAMESPACE_OK"`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	events := parseResponsesSSEEvents(t, recorder.Body.String())

	var names []string
	var arguments strings.Builder
	for index, event := range events {
		names = append(names, event.name)
		if got := sseSequence(t, event); got != index {
			t.Fatalf("sequence_number = %d at event %d", got, index)
		}
		if event.name == "response.function_call_arguments.delta" {
			arguments.WriteString(event.data["delta"].(string))
		}
	}
	want := []string{"response.created", "response.in_progress", "response.output_item.added"}
	for index, name := range want {
		if names[index] != name {
			t.Fatalf("event %d = %s, want %s (all: %v)", index, names[index], name, names)
		}
	}
	if names[len(names)-1] != "response.completed" || names[len(names)-2] != "response.output_item.done" ||
		names[len(names)-3] != "response.function_call_arguments.done" {
		t.Fatalf("unexpected tail: %v", names)
	}
	if strings.Count(strings.Join(names, ","), "response.function_call_arguments.delta") < 2 {
		t.Fatalf("arguments not split into deltas: %v", names)
	}

	added := events[2].data["item"].(map[string]any)
	if added["status"] != "in_progress" || added["arguments"] != "" || added["namespace"] != "diagnostics" {
		t.Fatalf("added item = %#v", added)
	}
	done := events[len(events)-3].data
	if done["arguments"] != arguments.String() || done["name"] != "echo" {
		t.Fatalf("done = %#v, deltas = %q", done, arguments.String())
	}
	completed := events[len(events)-1].data["response"].(map[string]any)
	item := completed["output"].([]any)[0].(map[string]any)
	if completed["status"] != "completed" || item["arguments"] != arguments.String() || item["type"] != "function_call" {
		t.Fatalf("completed = %#v", completed)
	}
}

func TestResponsesCustomToolCallStream(t *testing.T) {
	recorder := postJSON(t, "/v1/responses", responsesBody(
		`"stream":true,"tools":`+echoNamespaceTools+`,"tool_choice":{"type":"custom","name":"apply_patch"},"input":"patch"`))
	events := parseResponsesSSEEvents(t, recorder.Body.String())
	var input strings.Builder
	var sawDone bool
	for _, event := range events {
		switch event.name {
		case "response.custom_tool_call_input.delta":
			input.WriteString(event.data["delta"].(string))
		case "response.custom_tool_call_input.done":
			sawDone = true
			if event.data["input"] != input.String() || input.String() != "mock input for apply_patch" {
				t.Fatalf("input done = %#v, deltas = %q", event.data, input.String())
			}
		}
	}
	if !sawDone {
		t.Fatalf("missing custom_tool_call_input.done: %s", recorder.Body.String())
	}
}

func TestResponsesToolOutputReplyStream(t *testing.T) {
	recorder := postJSON(t, "/v1/responses", responsesBody(
		`"stream":true,"input":[{"type":"function_call","call_id":"c1","name":"echo","arguments":"{}"},`+
			`{"type":"function_call_output","call_id":"c1","output":[{"type":"input_text","text":"NAMESPACE_OK"}]}]`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	events := parseResponsesSSEEvents(t, recorder.Body.String())
	last := events[len(events)-1]
	if last.name != "response.completed" {
		t.Fatalf("last event = %s", last.name)
	}
	response := last.data["response"].(map[string]any)
	text := response["output"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]
	if text != "The echo tool returned: NAMESPACE_OK" {
		t.Fatalf("text = %#v", text)
	}
}
