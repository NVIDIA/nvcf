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

package models

import (
	"encoding/json"
	"strings"
	"testing"
)

// The gateway forwards a re-marshalled request, so marshaller output is wire output.
func TestChatCompletionRequestRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		request string
		want    []string
		notWant []string
	}{
		{
			name:    "no tool choice sent",
			request: `{"model":"m","messages":[{"role":"user","content":"What is 2+2?"}]}`,
			want:    []string{`"content":"What is 2+2?"`},
			notWant: []string{"tool_choice", "function_call", `"debug"`, "String", "ToolChoice", "FunctionCall"},
		},
		{
			name:    "tool choice string",
			request: `{"model":"m","messages":[{"role":"user","content":"hi"}],"tool_choice":"auto"}`,
			want:    []string{`"tool_choice":"auto"`},
			notWant: []string{"String", "ToolChoice"},
		},
		{
			name:    "tool choice object",
			request: `{"model":"m","messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"function","function":{"name":"f"}}}`,
			want:    []string{`"tool_choice":{"type":"function","function":{"name":"f"}}`},
		},
		{
			name:    "function call string",
			request: `{"model":"m","messages":[{"role":"user","content":"hi"}],"function_call":"none"}`,
			want:    []string{`"function_call":"none"`},
			notWant: []string{"FunctionCall"},
		},
		{
			name:    "function call object",
			request: `{"model":"m","messages":[{"role":"user","content":"hi"}],"function_call":{"name":"f"}}`,
			want:    []string{`"function_call":{"name":"f"}`},
		},
		{
			name:    "content array of one string collapses",
			request: `{"model":"m","messages":[{"role":"user","content":["hi"]}]}`,
			want:    []string{`"content":"hi"`},
		},
		{
			name:    "multimodal content keeps part types",
			request: `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"http://x/y.png"}}]}]}`,
			want: []string{
				`{"type":"text","text":"a"}`,
				`{"type":"image_url","image_url":{"url":"http://x/y.png"}}`,
			},
			notWant: []string{`"detail":""`},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var request ChatCompletionRequest
			if err := json.Unmarshal([]byte(test.request), &request); err != nil {
				t.Fatalf("unmarshal inbound request: %v", err)
			}

			outbound, err := json.Marshal(&request)
			if err != nil {
				t.Fatalf("marshal outbound request: %v", err)
			}

			for _, want := range test.want {
				if !strings.Contains(string(outbound), want) {
					t.Errorf("outbound body missing %s\ngot: %s", want, outbound)
				}
			}
			for _, notWant := range test.notWant {
				if strings.Contains(string(outbound), notWant) {
					t.Errorf("outbound body must not contain %s\ngot: %s", notWant, outbound)
				}
			}
		})
	}
}

func TestChatMessageContentMarshalRejectsUnknownPart(t *testing.T) {
	content := ChatMessageContent{unknownContentPart{}, unknownContentPart{}}
	if _, err := json.Marshal(content); err == nil {
		t.Fatal("expected error for unsupported content part")
	}
}

// A typed-nil part would otherwise serialize as a type with no member.
func TestChatMessageContentMarshalRejectsNilParts(t *testing.T) {
	tests := map[string]ChatMessageContent{
		"nil image_url": {ContentPartText("a"), (*ContentPartImageURL)(nil)},
		"nil document":  {ContentPartText("a"), (*ContentPartDocument)(nil)},
	}

	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := json.Marshal(content); err == nil {
				t.Fatal("expected error for nil content part")
			}
		})
	}
}

type unknownContentPart struct{}

func (unknownContentPart) ContentType() ContentPartType {
	return ContentPartType("unknown")
}
