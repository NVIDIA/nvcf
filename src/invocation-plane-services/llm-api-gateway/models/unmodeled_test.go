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
	"testing"
)

func TestUnmodeledMembers(t *testing.T) {
	t.Parallel()

	got := UnmodeledMembers(
		[]byte(`{"model":"m","MODEL":"m2","Messages":[],"chat_template_kwargs":{"enable_thinking":false},"debug":true,"top_k":20}`),
		ChatCompletionRequest{},
	)

	want := map[string]string{
		"chat_template_kwargs": `{"enable_thinking":false}`,
		"debug":                `true`,
		"top_k":                `20`,
	}
	if len(got) != len(want) {
		t.Fatalf("unmodeled members = %v, want keys %v", got, want)
	}
	for key, value := range want {
		if string(got[key]) != value {
			t.Fatalf("member %q = %s, want %s", key, got[key], value)
		}
	}
}

func TestUnmodeledMembersReturnsNilForNonObjects(t *testing.T) {
	t.Parallel()

	for _, data := range []string{``, `null`, `[]`, `"text"`, `{"broken"`, `{"model":"m"}`} {
		if got := UnmodeledMembers([]byte(data), ChatCompletionRequest{}); got != nil {
			t.Fatalf("UnmodeledMembers(%q) = %v, want nil", data, got)
		}
	}
}

func TestMergeJSONMembersKeepsTypedMembers(t *testing.T) {
	t.Parallel()

	merged, err := MergeJSONMembers(
		[]byte(`{"id":"typed","object":"chat.completion"}`),
		map[string]json.RawMessage{
			"ID":    json.RawMessage(`"extension"`),
			"nvext": json.RawMessage(`{"worker_id":"w-1"}`),
		},
	)
	if err != nil {
		t.Fatalf("MergeJSONMembers() error = %v", err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(merged, &got); err != nil {
		t.Fatalf("unmarshal merged: %v", err)
	}
	if string(got["id"]) != `"typed"` {
		t.Fatalf("id = %s, want typed value", got["id"])
	}
	if _, ok := got["ID"]; ok {
		t.Fatalf("case variant of a typed member was added: %s", merged)
	}
	if string(got["nvext"]) != `{"worker_id":"w-1"}` {
		t.Fatalf("nvext = %s, want extension value", got["nvext"])
	}
}

func TestChatCompletionResponseMarshalsExtensionsAtEachLevel(t *testing.T) {
	t.Parallel()

	response := ChatCompletionResponse{
		ID:     "chatcmpl-1",
		Object: ObjectChatCompletion,
		Choices: []ChatCompletionChoice{{
			Message: ChatCompletionMessage{
				Role:       ChatCompletionRoleAssistant,
				Extensions: map[string]json.RawMessage{"refusal": json.RawMessage(`"no"`)},
			},
			Logprobs: &ChatCompletionLogprobs{
				Extensions: map[string]json.RawMessage{"vendor_rank": json.RawMessage(`1`)},
			},
			Extensions: map[string]json.RawMessage{"stop_reason": json.RawMessage(`42`)},
		}},
		Usage:      ChatCompletionUsage{PromptTokens: 99, Raw: json.RawMessage(`{"prompt_tokens":1,"num_cached_tokens":2}`)},
		Extensions: map[string]json.RawMessage{"nvext": json.RawMessage(`{"worker_id":"w-1"}`)},
	}

	for _, value := range []any{response, &response} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var got struct {
			Nvext   json.RawMessage `json:"nvext"`
			Choices []struct {
				StopReason json.RawMessage `json:"stop_reason"`
				Message    struct {
					Refusal json.RawMessage `json:"refusal"`
				} `json:"message"`
				Logprobs struct {
					VendorRank json.RawMessage `json:"vendor_rank"`
				} `json:"logprobs"`
			} `json:"choices"`
			Usage map[string]json.RawMessage `json:"usage"`
		}
		if err := json.Unmarshal(encoded, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if string(got.Nvext) != `{"worker_id":"w-1"}` ||
			string(got.Choices[0].StopReason) != `42` ||
			string(got.Choices[0].Message.Refusal) != `"no"` ||
			string(got.Choices[0].Logprobs.VendorRank) != `1` {
			t.Fatalf("extensions missing from %s", encoded)
		}
		if string(got.Usage["num_cached_tokens"]) != `2` || string(got.Usage["prompt_tokens"]) != `1` {
			t.Fatalf("usage is not the raw upstream object: %s", encoded)
		}
	}
}

func TestAmbiguousMemberPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "top level shadow",
			body: `{"model":"m","messages":[{"role":"user","content":"long"}],"MESSAGES":[{"role":"user","content":"hi"}]}`,
			want: "messages",
		},
		{
			name: "message member shadow",
			body: `{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"user","content":"long","Content":"hi"}]}`,
			want: "messages[1].content",
		},
		{
			name: "content part shadow",
			body: `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"long","TEXT":"hi"}]}]}`,
			want: "messages[0].content[0].text",
		},
		{
			name: "image url shadow",
			body: `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"a","URL":"b"}}]}]}`,
			want: "messages[0].content[0].image_url.url",
		},
		{
			name: "tool function shadow",
			body: `{"model":"m","messages":[],"tools":[{"type":"function","function":{"name":"a"},"Function":{"name":"b"}}]}`,
			want: "tools[0].function",
		},
		{
			name: "tool choice object shadow",
			body: `{"model":"m","messages":[],"tool_choice":{"type":"function","function":{"name":"a"},"FUNCTION":{"name":"b"}}}`,
			want: "tool_choice.function",
		},
		{
			name: "canonical request",
			body: `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"tool_choice":"auto"}`,
		},
		{
			name: "single non canonical spelling",
			body: `{"MODEL":"m","Messages":[{"Role":"user","Content":"hi"}]}`,
		},
		{
			name: "case variants inside free form objects",
			body: `{"model":"m","messages":[],"chat_template_kwargs":{"A":1,"a":2},"tools":[{"type":"function","function":{"name":"f","parameters":{"properties":{"id":{},"ID":{}}}}}]}`,
		},
		{
			name: "case variants of unmodeled members",
			body: `{"model":"m","messages":[],"top_k":1,"TOP_K":2}`,
		},
		{
			name: "not an object",
			body: `[1,2]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := AmbiguousMemberPath([]byte(tt.body), ChatCompletionRequest{}); got != tt.want {
				t.Fatalf("AmbiguousMemberPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAmbiguousMemberPathForEmbeddingRequest(t *testing.T) {
	t.Parallel()

	got := AmbiguousMemberPath([]byte(`{"model":"m","input":"long","INPUT":"hi"}`), CreateEmbeddingRequest{})
	if got != "input" {
		t.Fatalf("AmbiguousMemberPath() = %q, want input", got)
	}
}
