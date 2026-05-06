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

import "encoding/json"

type PromptFunctionObject struct {
	Name string `json:"name"`
}

type PromptToolCallObject struct {
	ID         string               `json:"id"`
	Type       string               `json:"type"`
	Function   PromptFunctionObject `json:"function"`
	Parameters json.RawMessage      `json:"parameters"`
}

type ToolCallWithParameters struct {
	ToolCall PromptToolCallObject `json:"tool_call"`
}

type ToolCallsWithParameters struct {
	ToolCalls []PromptToolCallObject `json:"tool_calls"`
}

type FunctionCallWithParameters struct {
	Function   PromptFunctionObject `json:"function"`
	Parameters json.RawMessage      `json:"parameters"`
}

type NativeToolCall struct {
	ID        any             `json:"id,omitempty"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}
