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

package tools

import (
	_ "embed"
	"errors"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
)

var (
	ErrToolParseFailed = errors.New("JSON does not match the expected schema for tool calls")
	ErrNeedMoreData    = errors.New("need more data")

	//go:embed embed/bare_call_schema.json
	_bareCallSchema    string
	_bareCallExtractor = func() CallExtractor { return &Call{} }

	_bareParseCallConfig = NewSchemaConfig(
		_bareCallSchema,
		_bareCallExtractor,
	)
)

type SchemaConfig struct {
	factory func() CallExtractor
	schema  *jsonschema.Schema
	str     string
}

// TODO: generate the json schemas from go objects
func NewSchemaConfig(
	str string,
	factory func() CallExtractor,
) *SchemaConfig {
	schema := jsonschema.MustCompileString(
		"response_schema.json",
		str,
	)
	return &SchemaConfig{
		str:     str,
		schema:  schema,
		factory: factory,
	}
}

type Params struct {
	ToolChoice        models.ChatCompletionToolChoiceField
	Tools             []Tool
	ParallelToolCalls bool
	EnableThinking    *bool
	ReasoningEffort   string
	Documents         []string
	EnableCitations   bool
}

type ParseConfig interface {
	ParseTools(Params, string) (string, []Call, error)
	GenerateToolCallID() string
	ToolUseBeginMarker() string
	ToolUseEndMarker() string
	CanBeToolCall(string) bool
}

// The generic tool call config we use for most models.
func NewDefaultParseConfig() ParseConfig {
	return newJSONParseConfig()
}

func NewDeepSeekParseConfig() ParseConfig {
	return &jsonParseConfig{
		toolUseBeginMarker: "<tool_call>",
		toolUseEndMarker:   "</tool_call>",
		schemaConfig: func(Params) *SchemaConfig {
			return _bareParseCallConfig
		},
	}
}

func NewMistralParseConfig() ParseConfig {
	return arrayParseConfig{}
}

// Tool is a copy of models.ChatTool used at templating time, augmented
// with a validator stored alongside the function spec for strict validation.
// This type mirrors the JSON shape of the request so prompt JSON remains identical.
type Tool struct {
	Type     string           `json:"type"`
	Function ChatFunctionSpec `json:"function"`
}

// ChatFunctionSpec mirrors models.ChatFunctionSpec but replaces the Strict
// flag with an attached compiled validator used when strict validation is enabled.
type ChatFunctionSpec struct {
	Name             string             `json:"name"`
	Description      *string            `json:"description"`
	Parameters       any                `json:"parameters"`
	ParametersString string             `json:"-"`
	Allowed          bool               `json:"-"`
	Validator        *jsonschema.Schema `json:"-"`
}
