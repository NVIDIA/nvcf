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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Tool handling for POST /v1/responses. The sample accepts function, custom,
// and namespace tools and validates tool-call history the way the OpenAI API
// does. It does not run a model, so tool calls are decided by fixed rules:
//
//   - tool_choice "none" never calls a tool.
//   - tool_choice "required", or a function or custom object, always calls
//     one tool (the first tool, or the named one).
//   - tool_choice "auto" (the default) calls a tool only when the last user
//     text contains "call <name>" or "call <namespace>.<name>".
//   - Argument values come from "<property>=<value>" in the user text,
//     otherwise a placeholder for the property type.
//   - When the last input item is a tool output, the reply is text that
//     repeats that output.

const (
	toolKindFunction = "function"
	toolKindCustom   = "custom"
)

type responsesToolDef struct {
	Namespace  string
	Name       string
	Kind       string
	Parameters json.RawMessage
}

func (tool responsesToolDef) label() string {
	if tool.Namespace == "" {
		return tool.Name
	}
	return tool.Namespace + "." + tool.Name
}

type toolRequestError struct {
	message string
	param   string
}

func (err *toolRequestError) Error() string { return err.message }

type toolEnvelope struct {
	Type        string            `json:"type"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Parameters  json.RawMessage   `json:"parameters"`
	Tools       []json.RawMessage `json:"tools"`
}

func parseResponsesTools(raw []json.RawMessage) ([]responsesToolDef, error) {
	var tools []responsesToolDef
	for index, rawTool := range raw {
		param := fmt.Sprintf("tools[%d]", index)
		var envelope toolEnvelope
		if err := json.Unmarshal(rawTool, &envelope); err != nil {
			return nil, &toolRequestError{"invalid tool: " + err.Error(), param}
		}
		switch envelope.Type {
		case toolKindFunction, toolKindCustom:
			if envelope.Name == "" {
				return nil, &toolRequestError{"Missing required parameter: '" + param + ".name'.", param + ".name"}
			}
			tools = append(tools, responsesToolDef{Name: envelope.Name, Kind: envelope.Type, Parameters: envelope.Parameters})
		case "namespace":
			nested, err := parseNamespaceTool(envelope, param)
			if err != nil {
				return nil, err
			}
			tools = append(tools, nested...)
		}
		// Other tool types, such as web_search, are accepted and ignored.
	}
	return tools, nil
}

func parseNamespaceTool(envelope toolEnvelope, param string) ([]responsesToolDef, error) {
	if envelope.Name == "" {
		return nil, &toolRequestError{"Missing required parameter: '" + param + ".name'.", param + ".name"}
	}
	if envelope.Description == "" {
		return nil, &toolRequestError{"Missing required parameter: '" + param + ".description'.", param + ".description"}
	}
	if len(envelope.Tools) == 0 {
		return nil, &toolRequestError{"Invalid '" + param + ".tools': array too short. Expected an array with minimum length 1.", param + ".tools"}
	}
	var tools []responsesToolDef
	for index, rawTool := range envelope.Tools {
		nestedParam := fmt.Sprintf("%s.tools[%d]", param, index)
		var nested toolEnvelope
		if err := json.Unmarshal(rawTool, &nested); err != nil {
			return nil, &toolRequestError{"invalid tool: " + err.Error(), nestedParam}
		}
		if nested.Type != toolKindFunction && nested.Type != toolKindCustom {
			return nil, &toolRequestError{
				fmt.Sprintf("Invalid value: '%s'. Supported values are: 'function' and 'custom'.", nested.Type),
				nestedParam + ".type",
			}
		}
		if nested.Name == "" {
			return nil, &toolRequestError{"Missing required parameter: '" + nestedParam + ".name'.", nestedParam + ".name"}
		}
		tools = append(tools, responsesToolDef{
			Namespace: envelope.Name, Name: nested.Name, Kind: nested.Type, Parameters: nested.Parameters,
		})
	}
	return tools, nil
}

type responsesInputItem struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Namespace string          `json:"namespace"`
	Content   json.RawMessage `json:"content"`
	Output    json.RawMessage `json:"output"`
}

type outputPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// toolCallRef is a tool call from the input history.
type toolCallRef struct {
	kind  string
	label string
}

type parsedResponsesInput struct {
	userText string
	// toolOutput is set when the last input item is a tool output.
	toolOutput *struct {
		label string
		text  string
	}
}

func parseResponsesInput(raw json.RawMessage) (parsedResponsesInput, error) {
	var parsed parsedResponsesInput
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return parsed, nil
	}
	if trimmed[0] == '"' {
		_ = json.Unmarshal(trimmed, &parsed.userText)
		return parsed, nil
	}
	if trimmed[0] != '[' {
		return parsed, nil
	}
	var rawItems []json.RawMessage
	if err := json.Unmarshal(trimmed, &rawItems); err != nil {
		return parsed, &toolRequestError{"invalid input: " + err.Error(), "input"}
	}

	calls := map[string]toolCallRef{}
	for index, rawItem := range rawItems {
		param := fmt.Sprintf("input[%d]", index)
		var item responsesInputItem
		if err := json.Unmarshal(rawItem, &item); err != nil {
			return parsed, &toolRequestError{"invalid input item: " + err.Error(), param}
		}
		parsed.toolOutput = nil
		switch item.Type {
		case "function_call", "custom_tool_call":
			if item.CallID == "" {
				return parsed, &toolRequestError{"Missing required parameter: '" + param + ".call_id'.", param + ".call_id"}
			}
			if item.Name == "" {
				return parsed, &toolRequestError{"Missing required parameter: '" + param + ".name'.", param + ".name"}
			}
			kind := toolKindFunction
			if item.Type == "custom_tool_call" {
				kind = toolKindCustom
			}
			calls[item.CallID] = toolCallRef{kind: kind, label: responsesToolDef{Namespace: item.Namespace, Name: item.Name}.label()}
		case "function_call_output", "custom_tool_call_output":
			kind, noun := toolKindFunction, "function call"
			if item.Type == "custom_tool_call_output" {
				kind, noun = toolKindCustom, "custom tool call"
			}
			if item.CallID == "" {
				return parsed, &toolRequestError{"Missing required parameter: '" + param + ".call_id'.", param + ".call_id"}
			}
			call, found := calls[item.CallID]
			if !found || call.kind != kind {
				return parsed, &toolRequestError{
					fmt.Sprintf("No tool call found for %s output with call_id %s.", noun, item.CallID),
					"input",
				}
			}
			text, err := toolOutputText(item.Output, param+".output")
			if err != nil {
				return parsed, err
			}
			parsed.toolOutput = &struct {
				label string
				text  string
			}{call.label, text}
		case "message", "":
			if item.Role == "user" {
				parsed.userText = contentText(item.Content)
			}
		}
	}
	return parsed, nil
}

// toolOutputText validates a tool output and returns its text. The output is
// a string or an array of input_text, input_image, and input_file parts.
func toolOutputText(raw json.RawMessage, param string) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return "", &toolRequestError{"invalid output: " + err.Error(), param}
		}
		return text, nil
	}
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return "", &toolRequestError{
			fmt.Sprintf("Invalid type for '%s': expected one of a string or an array of objects.", param), param,
		}
	}
	var parts []outputPart
	if err := json.Unmarshal(trimmed, &parts); err != nil {
		return "", &toolRequestError{"invalid output: " + err.Error(), param}
	}
	var texts []string
	for index, part := range parts {
		switch part.Type {
		case "input_text":
			texts = append(texts, part.Text)
		case "input_image":
			texts = append(texts, "[image]")
		case "input_file":
			texts = append(texts, "[file]")
		default:
			return "", &toolRequestError{
				fmt.Sprintf("Invalid value: '%s'. Supported values are: 'input_text', 'input_image', and 'input_file'.", part.Type),
				fmt.Sprintf("%s[%d].type", param, index),
			}
		}
	}
	return strings.Join(texts, "\n"), nil
}

func contentText(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}
	if trimmed[0] == '"' {
		var text string
		_ = json.Unmarshal(trimmed, &text)
		return text
	}
	var parts []outputPart
	if err := json.Unmarshal(trimmed, &parts); err != nil {
		return ""
	}
	var texts []string
	for _, part := range parts {
		if part.Type == "input_text" || part.Type == "text" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n")
}

type toolChoice struct {
	mode      string
	kind      string
	name      string
	namespace string
}

func parseToolChoice(raw json.RawMessage) (toolChoice, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return toolChoice{mode: "auto"}, nil
	}
	if trimmed[0] == '"' {
		var mode string
		_ = json.Unmarshal(trimmed, &mode)
		switch mode {
		case "none", "auto", "required":
			return toolChoice{mode: mode}, nil
		}
		return toolChoice{}, &toolRequestError{
			fmt.Sprintf("Invalid value: '%s'. Supported values are: 'none', 'auto', and 'required'.", mode), "tool_choice",
		}
	}
	var object struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	}
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return toolChoice{}, &toolRequestError{"invalid tool_choice: " + err.Error(), "tool_choice"}
	}
	if object.Type == toolKindFunction || object.Type == toolKindCustom {
		return toolChoice{mode: "named", kind: object.Type, name: object.Name, namespace: object.Namespace}, nil
	}
	return toolChoice{mode: "auto"}, nil
}

type toolPlan struct {
	// call is the tool the reply calls. When nil, the reply is text.
	call *responsesToolDef
	// userText is the last user text, used for argument values.
	userText string
	// replyText is set when the reply answers a tool output.
	replyText string
	hasReply  bool
}

func planResponsesTools(request *responsesRequest) (toolPlan, error) {
	tools, err := parseResponsesTools(request.Tools)
	if err != nil {
		return toolPlan{}, err
	}
	input, err := parseResponsesInput(request.Input)
	if err != nil {
		return toolPlan{}, err
	}
	choice, err := parseToolChoice(request.ToolChoice)
	if err != nil {
		return toolPlan{}, err
	}
	plan := toolPlan{userText: input.userText}

	if input.toolOutput != nil {
		plan.hasReply = true
		plan.replyText = fmt.Sprintf("The %s tool returned: %s", input.toolOutput.label, input.toolOutput.text)
		return plan, nil
	}
	if len(tools) == 0 || choice.mode == "none" {
		return plan, nil
	}

	switch choice.mode {
	case "required":
		plan.call = &tools[0]
	case "named":
		for index := range tools {
			tool := tools[index]
			if tool.Kind == choice.kind && tool.Name == choice.name && (choice.namespace == "" || tool.Namespace == choice.namespace) {
				plan.call = &tools[index]
				break
			}
		}
		if plan.call == nil {
			return plan, &toolRequestError{
				fmt.Sprintf("Tool choice %s '%s' not found in 'tools' parameter.", choice.kind, choice.name), "tool_choice",
			}
		}
	default:
		plan.call = toolNamedInText(tools, input.userText)
	}
	return plan, nil
}

var callPattern = regexp.MustCompile(`(?i)\bcall\s+([A-Za-z0-9_.-]+)`)

func toolNamedInText(tools []responsesToolDef, text string) *responsesToolDef {
	for _, match := range callPattern.FindAllStringSubmatch(text, -1) {
		wanted := strings.TrimRight(match[1], ".")
		for index := range tools {
			if tools[index].label() == wanted || tools[index].Name == wanted {
				return &tools[index]
			}
		}
	}
	return nil
}

// mockToolInput returns the arguments JSON for a function call, or the text
// input for a custom tool call.
func mockToolInput(tool responsesToolDef, userText string) string {
	if tool.Kind == toolKindCustom {
		if _, value, found := strings.Cut(userText, "input="); found {
			return strings.TrimSpace(value)
		}
		return "mock input for " + tool.Name
	}

	var schema struct {
		Properties map[string]struct {
			Type    any   `json:"type"`
			Enum    []any `json:"enum"`
			Default any   `json:"default"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	_ = json.Unmarshal(tool.Parameters, &schema)

	required := map[string]bool{}
	for _, name := range schema.Required {
		required[name] = true
	}
	names := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		names = append(names, name)
	}
	sort.Strings(names)

	arguments := map[string]any{}
	for _, name := range names {
		property := schema.Properties[name]
		given, specified := userValue(userText, name)
		if !required[name] && !specified {
			continue
		}
		kind := propertyType(property.Type)
		switch {
		case specified:
			arguments[name] = typedValue(kind, given)
		case len(property.Enum) > 0:
			arguments[name] = property.Enum[0]
		case property.Default != nil:
			arguments[name] = property.Default
		default:
			arguments[name] = placeholderValue(kind, name)
		}
	}
	encoded, _ := json.Marshal(arguments)
	return string(encoded)
}

func userValue(text, property string) (string, bool) {
	pattern := regexp.MustCompile(`(?:^|\s)` + regexp.QuoteMeta(property) + `=(\S+)`)
	if match := pattern.FindStringSubmatch(text); match != nil {
		return match[1], true
	}
	return "", false
}

func propertyType(raw any) string {
	switch value := raw.(type) {
	case string:
		return value
	case []any:
		for _, entry := range value {
			if name, ok := entry.(string); ok && name != "null" {
				return name
			}
		}
	}
	return "string"
}

func typedValue(kind, value string) any {
	switch kind {
	case "integer":
		if number, err := strconv.ParseInt(value, 10, 64); err == nil {
			return number
		}
	case "number":
		if number, err := strconv.ParseFloat(value, 64); err == nil {
			return number
		}
	case "boolean":
		if flag, err := strconv.ParseBool(value); err == nil {
			return flag
		}
	}
	return value
}

func placeholderValue(kind, name string) any {
	switch kind {
	case "integer":
		return 1
	case "number":
		return 1.5
	case "boolean":
		return true
	case "array":
		return []any{}
	case "object":
		return map[string]any{}
	}
	return "mock_" + name
}

// responsesToolResponse is a Responses object whose output is a tool call.
type responsesToolResponse struct {
	ID                string          `json:"id"`
	Object            string          `json:"object"`
	CreatedAt         int64           `json:"created_at"`
	CompletedAt       *int64          `json:"completed_at,omitempty"`
	Status            string          `json:"status"`
	Model             string          `json:"model"`
	Output            []any           `json:"output"`
	ParallelToolCalls bool            `json:"parallel_tool_calls"`
	ToolChoice        any             `json:"tool_choice"`
	Tools             json.RawMessage `json:"tools"`
	Usage             *responsesUsage `json:"usage"`
}

type responseFunctionCallItem struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	CallID    string `json:"call_id"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responseCustomToolCallItem struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	CallID    string `json:"call_id"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Input     string `json:"input"`
}

type toolCallOutput struct {
	response responsesToolResponse
	// started and finished are the output item before and after its payload
	// streams.
	started, finished any
	itemID            string
	payload           string
	custom            bool
	tool              responsesToolDef
}

func newToolCallOutput(request *responsesRequest, tool responsesToolDef, payload string) toolCallOutput {
	sequence := responseSequence.Add(1)
	callID := fmt.Sprintf("call_%d", sequence)
	output := toolCallOutput{payload: payload, custom: tool.Kind == toolKindCustom, tool: tool}
	if output.custom {
		output.itemID = fmt.Sprintf("ctc_%d", sequence)
		output.started = responseCustomToolCallItem{
			ID: output.itemID, Type: "custom_tool_call", Status: "in_progress", CallID: callID, Namespace: tool.Namespace, Name: tool.Name,
		}
		output.finished = responseCustomToolCallItem{
			ID: output.itemID, Type: "custom_tool_call", Status: "completed", CallID: callID, Namespace: tool.Namespace, Name: tool.Name, Input: payload,
		}
	} else {
		output.itemID = fmt.Sprintf("fc_%d", sequence)
		output.started = responseFunctionCallItem{
			ID: output.itemID, Type: "function_call", Status: "in_progress", CallID: callID, Namespace: tool.Namespace, Name: tool.Name,
		}
		output.finished = responseFunctionCallItem{
			ID: output.itemID, Type: "function_call", Status: "completed", CallID: callID, Namespace: tool.Namespace, Name: tool.Name, Arguments: payload,
		}
	}

	tools := request.toolsJSON()
	inputTokens := countTokens(string(request.Input)) + countTokens(string(tools))
	outputTokens := countTokens(payload)
	completedAt := time.Now().Unix()
	output.response = responsesToolResponse{
		ID:                fmt.Sprintf("resp_%d", sequence),
		Object:            "response",
		CreatedAt:         completedAt,
		CompletedAt:       &completedAt,
		Status:            "completed",
		Model:             request.Model,
		Output:            []any{output.finished},
		ParallelToolCalls: true,
		ToolChoice:        request.toolChoiceValue(),
		Tools:             tools,
		Usage: &responsesUsage{
			InputTokens:        inputTokens,
			InputTokensDetails: responseInputTokenDetails{CachedTokens: 0},
			OutputTokens:       outputTokens,
			OutputTokensDetail: responseOutputTokenDetail{ReasoningTokens: 0},
			TotalTokens:        inputTokens + outputTokens,
		},
	}
	return output
}

func (request *responsesRequest) toolsJSON() json.RawMessage {
	if len(request.Tools) == 0 {
		return json.RawMessage("[]")
	}
	encoded, _ := json.Marshal(request.Tools)
	return encoded
}

func (request *responsesRequest) toolChoiceValue() any {
	trimmed := bytes.TrimSpace(request.ToolChoice)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "auto"
	}
	var value any
	_ = json.Unmarshal(trimmed, &value)
	return value
}

// splitPayload splits text into the pieces a model would stream. A single
// piece is returned for short text.
func splitPayload(text string) []string {
	const size = 12
	if len(text) <= size {
		return []string{text}
	}
	var pieces []string
	runes := []rune(text)
	for start := 0; start < len(runes); start += size {
		pieces = append(pieces, string(runes[start:min(start+size, len(runes))]))
	}
	return pieces
}

func streamToolCall(ctx context.Context, w http.ResponseWriter, output toolCallOutput, tuning benchmarkTuning) {
	setSSEHeaders(w)
	if !waitFor(ctx, tuning.TTFT, tuning.TTFTJitter) {
		return
	}

	inProgress := output.response
	inProgress.Status = "in_progress"
	inProgress.CompletedAt = nil
	inProgress.Output = []any{}
	inProgress.Usage = nil

	deltaEvent, doneEvent, doneKey := "response.function_call_arguments.delta", "response.function_call_arguments.done", "arguments"
	if output.custom {
		deltaEvent, doneEvent, doneKey = "response.custom_tool_call_input.delta", "response.custom_tool_call_input.done", "input"
	}

	sequence := 0
	emit := func(name string, data map[string]any) bool {
		data["type"] = name
		data["sequence_number"] = sequence
		sequence++
		if err := writeSSEJSON(w, name, data); err != nil {
			log.Printf("Responses event write failed: %v", err)
			return false
		}
		return true
	}

	if !emit("response.created", map[string]any{"response": inProgress}) ||
		!emit("response.in_progress", map[string]any{"response": inProgress}) ||
		!emit("response.output_item.added", map[string]any{"output_index": 0, "item": output.started}) {
		return
	}
	var pacer streamPacer
	defer pacer.stop()
	for index, piece := range splitPayload(output.payload) {
		if index > 0 && !pacer.wait(ctx, tuning.ITL, tuning.ITLJitter) {
			return
		}
		if !emit(deltaEvent, map[string]any{"item_id": output.itemID, "output_index": 0, "delta": piece}) {
			return
		}
	}
	if ctx.Err() != nil {
		return
	}

	done := map[string]any{"item_id": output.itemID, "output_index": 0, doneKey: output.payload}
	if !output.custom {
		done["name"] = output.tool.Name
	}
	if !emit(doneEvent, done) ||
		!emit("response.output_item.done", map[string]any{"output_index": 0, "item": output.finished}) ||
		!emit("response.completed", map[string]any{"response": output.response}) {
		return
	}
}

func writeToolRequestError(w http.ResponseWriter, err error) {
	var requestErr *toolRequestError
	if errors.As(err, &requestErr) {
		writeAPIErrorWithCode(w, http.StatusBadRequest, requestErr.message, requestErr.param, "invalid_value")
		return
	}
	writeAPIError(w, http.StatusBadRequest, err.Error(), "")
}
