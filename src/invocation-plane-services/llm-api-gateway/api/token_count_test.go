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

package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/api/adapters/openairesponses"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
)

func TestMediaInputContributesToTokenEstimate(t *testing.T) {
	for _, kind := range []string{"audio_url", "video_url", "input_audio"} {
		t.Run(kind, func(t *testing.T) {
			estimate := func(data string) int {
				var request *models.ChatCompletionRequest
				if kind == "input_audio" {
					var responseRequest openairesponses.CreateRequest
					body := `{"model":"model","input":[{"role":"user","content":[{"type":"input_audio","input_audio":{"format":"ogg","data":"` + data + `"}}]}]}`
					if err := json.Unmarshal([]byte(body), &responseRequest); err != nil {
						t.Fatal(err)
					}
					request = ConvertToChatCompletionRequest(&responseRequest)
				} else {
					body := `{"model":"model","messages":[{"role":"user","content":[{"type":"` + kind + `","` + kind + `":{"url":"data:media;base64,` + data + `"}}]}]}`
					if err := json.Unmarshal([]byte(body), &request); err != nil {
						t.Fatal(err)
					}
				}
				return estimatedInputTokensForNormalizedRequest(request.Model, request)
			}
			if short, long := estimate("AAAA"), estimate(strings.Repeat("AAAA", 128)); long <= short {
				t.Fatalf("media omitted from estimate: short=%d long=%d", short, long)
			}
		})
	}
}

func TestEstimatedInputTokensIncludesForwardedToolPayloads(t *testing.T) {
	t.Parallel()

	description := strings.Repeat("lookup field ", 32)
	parameters := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": description,
			},
		},
	}
	request := &models.ChatCompletionRequest{
		Model: "test-model",
		Messages: &[]models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleUser,
				Content: models.SingleTextContent("hello"),
			},
		},
	}
	baseline := estimatedInputTokensForNormalizedRequest(request.Model, request)

	request.Tools = &[]models.ChatTool{
		{
			Type: models.ToolTypeFunction,
			Function: models.ChatFunctionSpec{
				Name:        "lookup",
				Description: &description,
				Parameters:  &parameters,
			},
		},
	}

	got := estimatedInputTokensForNormalizedRequest(request.Model, request)
	if got <= baseline {
		t.Fatalf("estimated input tokens = %d, want > baseline %d", got, baseline)
	}
}

func TestEstimatedInputTokensIncludesForwardedMultimodalPayloads(t *testing.T) {
	t.Parallel()

	request := &models.ChatCompletionRequest{
		Model: "test-model",
		Messages: &[]models.ChatMessage{
			{
				Role: models.ChatCompletionRoleUser,
				Content: []models.ContentPart{
					models.ContentPartText("describe this"),
				},
			},
		},
	}
	baseline := estimatedInputTokensForNormalizedRequest(request.Model, request)

	(*request.Messages)[0].Content = append(
		(*request.Messages)[0].Content,
		&models.ContentPartImageURL{
			URL:    "data:image/png;base64," + strings.Repeat("a", 512),
			Detail: "high",
		},
		models.ContentPartDocument{
			Data: map[string]any{
				"title": "specification",
				"body":  strings.Repeat("document content ", 64),
			},
		},
	)

	got := estimatedInputTokensForNormalizedRequest(request.Model, request)
	if got <= baseline {
		t.Fatalf("estimated input tokens = %d, want > baseline %d", got, baseline)
	}
}

func TestEstimatedTokenCountForValueFallsBackWhenJSONMarshalFails(t *testing.T) {
	t.Parallel()

	got := estimatedTokenCountForValue(make(chan int))
	if got == 0 {
		t.Fatal("estimated token count = 0, want fallback estimate")
	}
}

func TestConvertToChatCompletionRequestNamespaceToolsAndContentOutput(t *testing.T) {
	var request openairesponses.CreateRequest
	body := `{"model":"model",
		"tools":[
			{"type":"namespace","name":"a","description":"A","tools":[
				{"type":"function","name":"run","description":"Run a","parameters":{"type":"object"}}]},
			{"type":"namespace","name":"b","description":"B","tools":[
				{"type":"function","name":"run","description":"Run b","parameters":{"type":"object"}},
				{"type":"function","name":"stop","parameters":{"type":"object"}}]},
			{"type":"function","name":"top","parameters":{"type":"object"}}],
		"input":[
			{"type":"function_call","namespace":"a","name":"run","call_id":"c1","arguments":"{}"},
			{"type":"function_call_output","call_id":"c1","output":[{"type":"input_text","text":"picked question"}]},
			{"type":"function_call","name":"top","call_id":"c2","arguments":"{}"},
			{"type":"function_call_output","call_id":"c2","output":"plain"}]}`
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatal(err)
	}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}

	chat := ConvertToChatCompletionRequest(&request)

	var names []string
	for _, tool := range *chat.Tools {
		names = append(names, tool.Function.Name)
	}
	if got, want := strings.Join(names, ","), "run,run,stop,top"; got != want {
		t.Fatalf("tool names = %s, want %s", got, want)
	}

	var toolTexts []string
	for _, message := range *chat.Messages {
		if message.Role != models.ChatCompletionRoleTool {
			continue
		}
		for _, part := range message.Content {
			if text, ok := part.(models.ContentPartText); ok {
				toolTexts = append(toolTexts, text.String())
			}
		}
	}
	if got, want := strings.Join(toolTexts, ","), "picked question,plain"; got != want {
		t.Fatalf("tool output text = %s, want %s", got, want)
	}
}

func TestConvertToChatCompletionRequestCustomTools(t *testing.T) {
	var request openairesponses.CreateRequest
	body := `{"model":"model",
		"tools":[
			{"type":"custom","name":"apply_patch","description":"Apply a patch"},
			{"type":"namespace","name":"editor","description":"Editing","tools":[
				{"type":"custom","name":"insert"},
				{"type":"function","name":"open","parameters":{"type":"object"}}]}],
		"input":[
			{"type":"custom_tool_call","call_id":"c1","name":"apply_patch","input":"*** Begin Patch"},
			{"type":"custom_tool_call_output","call_id":"c1","output":[{"type":"input_text","text":"applied"}]}]}`
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatal(err)
	}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}

	chat := ConvertToChatCompletionRequest(&request)

	var names []string
	for _, tool := range *chat.Tools {
		names = append(names, tool.Function.Name)
	}
	if got, want := strings.Join(names, ","), "apply_patch,insert,open"; got != want {
		t.Fatalf("tool names = %s, want %s", got, want)
	}

	messages := *chat.Messages
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(messages))
	}
	call := (*messages[0].ToolCalls)[0]
	if call.ID != "c1" || call.Function.Name != "apply_patch" || call.Function.Arguments != "*** Begin Patch" {
		t.Fatalf("unexpected tool call: %+v", call)
	}
	if messages[1].Role != models.ChatCompletionRoleTool || *messages[1].ToolCallID != "c1" {
		t.Fatalf("unexpected tool result: %+v", messages[1])
	}
	if estimatedInputTokensForNormalizedRequest(chat.Model, chat) <= 0 {
		t.Fatal("expected a positive token estimate")
	}
}
