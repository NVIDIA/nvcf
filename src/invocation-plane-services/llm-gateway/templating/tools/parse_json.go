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
	"bytes"
	_ "embed"
	"fmt"
	"strings"
	"unsafe"

	mapstructure "github.com/go-viper/mapstructure/v2"
	zlog "github.com/rs/zerolog/log"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/encoding/json"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/pool"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
)

var (
	//go:embed embed/parallel_call_schema.json
	_parallelCallSchema      string
	_parallelCallParseConfig = NewSchemaConfig(
		_parallelCallSchema,
		func() CallExtractor { return &ParallelCalls{} },
	)

	//go:embed embed/single_call_schema.json
	_singleCallSchema      string
	_singleCallParseConfig = NewSchemaConfig(
		_singleCallSchema,
		func() CallExtractor { return &SingleCall{} },
	)

	_jsonBufferPool = pool.NewWithReleaser(
		func() *bytes.Buffer {
			return bytes.NewBuffer(make([]byte, 0, 512))
		},
		func(x *bytes.Buffer) {
			x.Reset()
		},
	)
	_jsonEncoderPool = json.NewBufferedEncoderPool(512 /* buffer cap */)

	_ ParseConfig = (*jsonParseConfig)(nil)
)

func GetToolCallJSONString(calls *[]models.ChatToolCall) (string, error) {
	concat := _jsonBufferPool.Get()
	defer _jsonBufferPool.Put(concat)

	// Reuse an encoder backed by the same buffer for each call that needs to
	// be marshaled.
	enc := _jsonEncoderPool.Get()
	defer _jsonEncoderPool.Put(enc)

	for _, call := range *calls {
		var (
			args      = call.Function.Arguments
			raw       = unsafe.Slice(unsafe.StringData(args), len(args))
			arguments json.RawMessage
		)

		if err := json.Unmarshal(raw, &arguments); err != nil {
			zlog.Error().
				Err(err).
				Msg("failed to unmarshal tool call arguments")
			continue
		}

		err := enc.Encode(models.NativeToolCall{
			ID:        &call.ID,
			Name:      call.Function.Name,
			Arguments: arguments,
		})
		if err != nil {
			zlog.Error().Err(err).Msg("failed to marshal tool call")
		} else {
			// n.b. WriteTo conventionally resets the buffer once it has been
			//      drained (when its implementation is [bytes.Buffer]), but
			//      since it's an interface, reset explicitly for sanity.
			enc.Buffer().WriteTo(concat) //nolint:errcheck
			enc.Buffer().Reset()
		}
	}

	return concat.String(), nil
}

func GetToolCallJSONStringFromFunctionCall(
	call *models.ChatCompletionFunctionCall,
) (string, error) {
	var (
		args      = call.Arguments
		raw       = unsafe.Slice(unsafe.StringData(args), len(args))
		arguments json.RawMessage
	)

	if err := json.Unmarshal(raw, &arguments); err != nil {
		return "", err
	}

	enc := _jsonEncoderPool.Get()
	defer _jsonEncoderPool.Put(enc)

	err := enc.Encode(models.NativeToolCall{
		Name:      call.Name,
		Arguments: arguments,
	})
	if err != nil {
		return "", fmt.Errorf("failed to marshal tool call: %w", err)
	}

	return enc.Buffer().String(), nil
}

type jsonParseConfig struct {
	toolUseBeginMarker string
	toolUseEndMarker   string
	schemaConfig       func(Params) *SchemaConfig
}

func newJSONParseConfig() *jsonParseConfig {
	return &jsonParseConfig{
		toolUseBeginMarker: "<tool-use>",
		toolUseEndMarker:   "</tool-use>",
		schemaConfig: func(params Params) *SchemaConfig {
			if params.ParallelToolCalls {
				return _parallelCallParseConfig
			}
			return _singleCallParseConfig
		},
	}
}

func (c *jsonParseConfig) ParseTools(
	params Params,
	message string,
) (string, []Call, error) {
	schemaConfig := c.schemaConfig(params)
	return parseJSONToolCalls(
		params,
		c.ToolUseBeginMarker(),
		c.ToolUseEndMarker(),
		schemaConfig,
		message,
	)
}

func (*jsonParseConfig) GenerateToolCallID() string {
	return NewShortCallID()
}

func (c *jsonParseConfig) ToolUseBeginMarker() string {
	return c.toolUseBeginMarker
}

func (c *jsonParseConfig) ToolUseEndMarker() string {
	return c.toolUseEndMarker
}

func (c *jsonParseConfig) CanBeToolCall(value string) bool {
	if len(c.ToolUseBeginMarker()) > len(value) {
		return strings.HasPrefix(c.ToolUseBeginMarker(), value)
	}
	return strings.HasPrefix(value, c.ToolUseBeginMarker())
}

func (c *jsonParseConfig) ToolEnvelope(params Params) (ToolEnvelope, bool) {
	sc := c.schemaConfig(params)
	if sc == nil || sc.str == "" {
		return ToolEnvelope{}, false
	}

	return ToolEnvelope{
		BeginMarker: c.ToolUseBeginMarker(),
		EndMarker:   c.ToolUseEndMarker(),
		Schema:      sc.str,
	}, true
}

// This function parses JSON tool calls surrounded by a distinct marker. The
// JSON will be validated against a schema, then parsed into an object created
// by the toolCallObjectGenerator.
//
// Examples:
//
//	<tool-use>{"tool_call":{"id":"pending","type":"function","function":{"name":"name"},"parameters":{...}}}</tool-use>
//	<tool_call>{"name":"tool_name","arguments":{"arg_name":"arg_value"}}</tool_call><tool_call>{"name":"tool_name2","arguments":{}}</tool_call>
//
// TODO: Can parts of this function be moved to a helper function and reused
// for JSON mode parsing?
func parseJSONToolCalls(
	params Params,
	markBegin string,
	markEnd string,
	cfg *SchemaConfig,
	message string,
) (string, []Call, error) {
	// First find the markers that indicate the start and end of the tool calling
	start := strings.Index(message, markBegin)
	if start == -1 {
		return message, nil, nil
	}
	start += len(markBegin)
	var (
		trimmedMessage = message[start:]
		end            = strings.Index(trimmedMessage, markEnd)
	)
	if end == -1 {
		// Attempt to decode the message to handle the case where a tool was
		// output, followed by something else
		end = len(trimmedMessage)
	}
	trimmedMessage = trimmedMessage[:end]

	// Next find the curl braces that should begin and end the JSON
	jsonStart := strings.IndexByte(trimmedMessage, '{')
	if jsonStart == -1 {
		return message, nil, nil
	}
	trimmedMessage = trimmedMessage[jsonStart:]

	jsonEnd := strings.LastIndexByte(trimmedMessage, '}')
	if jsonEnd == -1 {
		return message, nil, nil
	}

	// Parse the JSON into a map[string]any and validate the schema.
	var (
		jsonStr   = trimmedMessage[:jsonEnd+1]
		raw       = unsafe.Slice(unsafe.StringData(jsonStr), len(jsonStr))
		firstPass any
	)

	// n.b. Explicitly use the unsafe bytes with [bytes.NewReader], rather than
	//      using either [bytes.NewBuffer] or [bytes.NewBufferString], in order
	//      to minimize overhead, as [bytes.NewReader] is more lightweight.
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&firstPass); err != nil {
		// if err := json.Unmarshal(raw, &firstPass); err != nil {
		return "", nil, fmt.Errorf(
			"%w: failed to unmarshal tool calls: %w\n\n%s",
			ErrToolParseFailed,
			err,
			jsonStr,
		)
	}
	if err := cfg.schema.Validate(firstPass); err != nil {
		return "", nil, fmt.Errorf(
			"%w: schema validation failed: %w",
			ErrToolParseFailed,
			err,
		)
	}

	// Decode the JSON into the tool call object.
	toolsObject := cfg.factory()
	if err := mapstructure.Decode(firstPass, toolsObject); err != nil {
		return "", nil, fmt.Errorf(
			"%w: failed to decode map structure: %w",
			ErrToolParseFailed,
			err,
		)
	}

	precedingText := message[:start-len(markBegin)]
	if params.ParallelToolCalls && end+len(markEnd) < len(message) {
		// Recurse into the rest of the string to find more
		// tool begin markers and tool calls.
		_, rest, err := parseJSONToolCalls(
			params,
			markBegin,
			markEnd,
			cfg,
			message[end+len(markEnd):],
		)
		if err == nil {
			var (
				numCalls = toolsObject.NumCalls() + len(rest)
				calls    []Call
			)
			if numCalls > 0 {
				calls = make([]Call, 0, numCalls)
			}

			toolsObject.ExtractCallsIter()(func(call Call) bool {
				calls = append(calls, call)
				return true
			})

			return precedingText, append(calls, rest...), nil
		}

		// Else, swallow error since we already found a successful tool call
		// and there is no expectation to find more.
	}

	return precedingText, toolsObject.ExtractCalls(), nil
}
