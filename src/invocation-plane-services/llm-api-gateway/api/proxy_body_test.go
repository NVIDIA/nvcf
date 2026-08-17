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
	"testing"
)

// The proxy endpoints must forward the caller's body untouched apart from the
// fields they deliberately override, so they cannot pick up the marshalling
// defects that affect the bound chat completion model.
func TestRewriteJSONModelPreservesEverythingElse(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"caller-model","input":"hello","tool_choice":"auto","encoding_format":"float","unknown":{"a":[1,2]}}`)

	rewritten, changed, err := rewriteJSONModel(body, "routed-model")
	if err != nil {
		t.Fatalf("rewriteJSONModel: %v", err)
	}
	if !changed {
		t.Fatal("expected the model field to be rewritten")
	}

	var got map[string]any
	if err := json.Unmarshal(rewritten, &got); err != nil {
		t.Fatalf("unmarshal rewritten body: %v", err)
	}

	if got["model"] != "routed-model" {
		t.Errorf("model = %v, want routed-model", got["model"])
	}
	if got["input"] != "hello" {
		t.Errorf("input = %v, want the original string", got["input"])
	}
	if got["tool_choice"] != "auto" {
		t.Errorf("tool_choice = %v, want auto", got["tool_choice"])
	}
	if got["encoding_format"] != "float" {
		t.Errorf("encoding_format = %v, want float", got["encoding_format"])
	}
	if _, ok := got["unknown"]; !ok {
		t.Error("unknown passthrough field was dropped")
	}
}

func TestRewriteResponsesProxyBodyPreservesEverythingElse(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"caller-model","stream":false,"input":"hello","tools":[{"type":"function","name":"f"}]}`)

	rewritten, err := rewriteResponsesProxyBody(body, "routed-model", true)
	if err != nil {
		t.Fatalf("rewriteResponsesProxyBody: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(rewritten, &got); err != nil {
		t.Fatalf("unmarshal rewritten body: %v", err)
	}

	if got["model"] != "routed-model" {
		t.Errorf("model = %v, want routed-model", got["model"])
	}
	if got["stream"] != true {
		t.Errorf("stream = %v, want true", got["stream"])
	}
	if got["input"] != "hello" {
		t.Errorf("input = %v, want the original string", got["input"])
	}
	tools, ok := got["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v, want the original single-element array", got["tools"])
	}
}
