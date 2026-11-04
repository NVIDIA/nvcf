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
	"encoding/json"
	"errors"
	"io"
	"strings"

	mapstructure "github.com/go-viper/mapstructure/v2"
)

var (
	//go:embed embed/bare_array_call_schema.json
	_bareArrayCallSchema      string
	_bareArrayCallParseConfig = NewSchemaConfig(
		_bareArrayCallSchema,
		func() CallExtractor { return &Call{} },
	)

	//go:embed embed/bare_array_parameters_call_schema.json
	_bareArrayParametersCallSchema      string
	_bareArrayParametersCallParseConfig = NewSchemaConfig(
		_bareArrayParametersCallSchema,
		func() CallExtractor { return &CallParameters{} },
	)

	_ ParseConfig = (*arrayParseConfig)(nil)
)

// BareArrayParametersCallSchema returns the JSON schema used to validate tool
// calls in the Llama 4 "bare array" format, which uses the "parameters" field
// (not "arguments").
func BareArrayParametersCallSchema() string {
	return _bareArrayParametersCallSchema
}

// XXX: This parser was formerly used by mistral large, but is not currently in
// use. We need to re-examine how well this works with streaming.
type arrayParseConfig struct{}

func (arrayParseConfig) CanBeToolCall(value string) bool {
	return strings.HasPrefix(value, "[")
}

func (arrayParseConfig) ParseTools(
	params Params,
	message string,
) (string, []Call, error) {
	return parseArrayWithArgumentsToolCalls(params, message)
}

func (arrayParseConfig) GenerateToolCallID() string {
	return NewMistralToolCallID()
}

func (arrayParseConfig) ToolUseBeginMarker() string {
	return "["
}

func (arrayParseConfig) ToolUseEndMarker() string {
	return "]"
}

func (arrayParseConfig) ToolEnvelope(_ Params) (ToolEnvelope, bool) {
	return ToolEnvelope{
		BeginMarker: "[",
		EndMarker:   "]",
		Schema:      _bareArrayCallSchema,
	}, true
}

// This parses expects the LLM to return a json array of tools.
// Examples:
//
//	[
//	  {"name":"tool_name","arguments":{"arg_name":"arg_value"}},
//	  {"name":"tool_name2","arguments":{}}
//	]
func parseArrayToolCalls(
	params Params,
	message string,
	cfg *SchemaConfig,
) (string, []Call, error) {
	start := strings.Index(message, "[")
	if start == -1 {
		return message, nil, nil
	}
	precedingText := message[:start]
	message = message[start:]

	var (
		decoder = json.NewDecoder(strings.NewReader(message))
		parsed  []any
	)
	if err := decoder.Decode(&parsed); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return "", nil, ErrNeedMoreData
		}
		return "", nil, ErrToolParseFailed
	}

	if err := cfg.schema.Validate(parsed); err != nil {
		return "", nil, err
	}

	var calls []Call
	if len(parsed) > 0 {
		calls = make([]Call, 0, len(parsed))
		for _, x := range parsed {
			callParams := cfg.factory()
			if err := mapstructure.Decode(x, &callParams); err != nil {
				return "", nil, ErrToolParseFailed
			}
			calls = append(calls, callParams.ExtractCalls()...)

			if !params.ParallelToolCalls {
				// Return just the first tool call if parallel tool calling is
				// disabled
				break
			}
		}
	}
	return precedingText, calls, nil
}

// This parses expects the LLM to return a json array of tools.
// Examples:
// [
// {"name":"tool_name","arguments":{"arg_name":"arg_value"}},
// {"name":"tool_name2","arguments":{}}
// ]
func parseArrayWithArgumentsToolCalls(
	params Params,
	message string,
) (string, []Call, error) {
	return parseArrayToolCalls(params, message, _bareArrayCallParseConfig)
}

// parseArrayWithParametersToolCalls parses tool calls for Llama 4 (quattro)
// which uses "parameters" instead of "arguments". Examples:
// [
// {"name":"tool_name","parameters":{"arg_name":"arg_value"}},
// {"name":"tool_name2","parameters":{}},
// ]
//
// Foo.
func ParseArrayWithParametersToolCalls(
	toolsInfo Params,
	message string,
) (string, []Call, error) {
	return parseArrayToolCalls(
		toolsInfo,
		message,
		_bareArrayParametersCallParseConfig,
	)
}
