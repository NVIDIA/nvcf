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

package openairesponses

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Content types for input
const (
	ContentTypeInputText  = "input_text"
	ContentTypeInputImage = "input_image"
	ContentTypeInputFile  = "input_file"
)

// Content types for output
const (
	ContentTypeOutputText    = "output_text"
	ContentTypeRefusal       = "refusal"
	ContentTypeReasoningText = "reasoning_text"
)

// Item types
const (
	ItemTypeMessage              = "message"
	ItemTypeFunctionCall         = "function_call"
	ItemTypeFunctionCallOutput   = "function_call_output"
	ItemTypeWebSearchCall        = "web_search_call"
	ItemTypeCodeInterpreterCall  = "code_interpreter_call"
	ItemTypeReasoning            = "reasoning"
	ItemTypeReference            = "item_reference"
	ItemTypeMCPListTools         = "mcp_list_tools"
	ItemTypeMCPCall              = "mcp_call"
	ItemTypeMCPApprovalRequest   = "mcp_approval_request"
	ItemTypeMCPApprovalResponse  = "mcp_approval_response"
	ItemTypeCustomToolCall       = "custom_tool_call"
	ItemTypeCustomToolCallOutput = "custom_tool_call_output"
)

// Annotation types
const (
	AnnotationTypeFileCitation = "file_citation"
	AnnotationTypeURLCitation  = "url_citation"
	AnnotationTypeFilePath     = "file_path"
)

// Package-level empty slices to avoid repeated heap allocations
var (
	emptyReasoningContent      = []ReasoningContent{}
	emptyAnySlice              = []any{}
	emptyOutputContent         = []OutputContent{}
	emptyCodeInterpreterOutput = []CodeInterpreterOutput{}
	emptyMCPListToolsTools     = []MCPListToolsTool{}
)

// InputItem represents polymorphic input items
type InputItem struct {
	Type string `json:"type,omitempty"`

	Role string `json:"role,omitempty"`

	ID string `json:"id,omitempty"`

	Status         *string        `json:"status,omitempty"`
	MessageContent []InputContent `json:"content"`
	ToolCallData   any            `json:"-"`
}

// UnmarshalJSON implements custom unmarshaling for InputItem
func (item *InputItem) UnmarshalJSON(data []byte) error {
	// First unmarshal into a temporary struct to get the type
	var temp struct {
		Type string `json:"type"`
		Role string `json:"role"`
	}

	if err := json.Unmarshal(data, &temp); err != nil {
		return err
	}

	if temp.Type == ItemTypeMessage {
		return item.unmarshalMessage(data, true)
	}

	if temp.Type == "" && temp.Role != "" {
		return item.unmarshalMessage(data, false)
	}

	if temp.Type == ItemTypeReference {
		return item.unmarshalItemReference(data)
	}

	return item.unmarshalTypedItem(data)
}

func (item *InputItem) unmarshalMessage(data []byte, hasExplicitType bool) error {
	var msg struct {
		Type    string          `json:"type"`
		Role    string          `json:"role"`
		Status  *string         `json:"status"`
		Content json.RawMessage `json:"content"`
	}

	if err := json.Unmarshal(data, &msg); err != nil {
		return err
	}

	if hasExplicitType {
		item.Type = msg.Type
	} else {
		item.Type = ""
	}

	item.Role = msg.Role
	item.Status = msg.Status
	item.MessageContent = nil

	if len(msg.Content) == 0 {
		return nil
	}

	// Handle polymorphic content field - can be string or array
	var text string
	if err := json.Unmarshal(msg.Content, &text); err == nil {
		item.MessageContent = []InputContent{NewInputTextContent(text)}
		return nil
	}

	var inputContents []InputContent
	if err := json.Unmarshal(msg.Content, &inputContents); err != nil {
		return &UnmarshalError{
			Field: "content",
			Msg:   "must be string or array of content items",
		}
	}

	item.MessageContent = inputContents
	return nil
}

func (item *InputItem) unmarshalItemReference(data []byte) error {
	var ref struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}

	if err := json.Unmarshal(data, &ref); err != nil {
		return err
	}

	item.Type = ref.Type
	item.ID = ref.ID

	return nil
}

func (item *InputItem) unmarshalTypedItem(data []byte) error {
	var base struct {
		Type   string  `json:"type"`
		Status *string `json:"status"`
	}

	if err := json.Unmarshal(data, &base); err != nil {
		return err
	}

	item.Type = base.Type
	item.Status = base.Status

	switch base.Type {
	case ItemTypeFunctionCall:
		return item.unmarshalFunctionCall(data)
	case ItemTypeFunctionCallOutput:
		return item.unmarshalFunctionCallOutput(data)
	case ItemTypeWebSearchCall:
		return item.unmarshalWebSearchCall(data)
	case ItemTypeCodeInterpreterCall:
		return item.unmarshalCodeInterpreterCall(data)
	case ItemTypeReasoning:
		return item.unmarshalReasoning(data)
	case ItemTypeMCPListTools:
		return item.unmarshalMCPListTools(data)
	case ItemTypeMCPCall:
		return item.unmarshalMCPCall(data)
	case ItemTypeMCPApprovalRequest:
		return item.unmarshalMCPApprovalRequest(data)
	case ItemTypeMCPApprovalResponse:
		return item.unmarshalMCPApprovalResponse(data)
	case ItemTypeCustomToolCallOutput:
		return item.unmarshalCustomToolCallOutput(data)
	default:
		return &UnmarshalError{
			Field: "type",
			Msg:   fmt.Sprintf("unknown item type: %s", base.Type),
		}
	}
}

func (item *InputItem) unmarshalFunctionCall(data []byte) error {
	var call FunctionToolCall
	if err := json.Unmarshal(data, &call); err != nil {
		return err
	}
	item.ToolCallData = call
	return nil
}

func (item *InputItem) unmarshalFunctionCallOutput(data []byte) error {
	var output FunctionCallOutput
	if err := json.Unmarshal(data, &output); err != nil {
		return err
	}
	item.ToolCallData = output
	return nil
}

func (item *InputItem) unmarshalWebSearchCall(data []byte) error {
	var call WebSearchToolCall
	if err := json.Unmarshal(data, &call); err != nil {
		return err
	}
	item.ToolCallData = call
	return nil
}

func (item *InputItem) unmarshalCodeInterpreterCall(data []byte) error {
	var call CodeInterpreterToolCall
	if err := json.Unmarshal(data, &call); err != nil {
		return err
	}
	item.ToolCallData = call
	return nil
}

func (item *InputItem) unmarshalReasoning(data []byte) error {
	var reasoning ReasoningItem
	if err := json.Unmarshal(data, &reasoning); err != nil {
		return err
	}
	item.ToolCallData = reasoning
	return nil
}

func (item *InputItem) unmarshalMCPApprovalRequest(data []byte) error {
	var approval MCPApprovalRequest
	if err := json.Unmarshal(data, &approval); err != nil {
		return err
	}
	item.ToolCallData = approval
	return nil
}

func (item *InputItem) unmarshalMCPCall(data []byte) error {
	var mcpCall MCPToolCall
	if err := json.Unmarshal(data, &mcpCall); err != nil {
		return err
	}
	item.ToolCallData = mcpCall
	return nil
}

func (item *InputItem) unmarshalMCPApprovalResponse(data []byte) error {
	var approval MCPApprovalResponse
	if err := json.Unmarshal(data, &approval); err != nil {
		return err
	}
	item.ToolCallData = approval
	return nil
}

// unmarshalMCPListTools allows accepting an mcp_list_tools item in input (OpenAI stateless replay)
func (item *InputItem) unmarshalMCPListTools(data []byte) error {
	var lt struct {
		Type        string  `json:"type"`
		ID          string  `json:"id"`
		ServerLabel string  `json:"server_label"`
		Error       *string `json:"error"`
		Tools       []struct {
			Annotations map[string]any `json:"annotations"`
			Description *string        `json:"description"`
			InputSchema map[string]any `json:"input_schema"`
			Name        string         `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(data, &lt); err != nil {
		return err
	}

	tools := make([]MCPListToolsTool, len(lt.Tools))
	for i, t := range lt.Tools {
		// Convert annotations to pointer form if present
		var anns *map[string]any
		if t.Annotations != nil {
			anns = &t.Annotations
		}
		tools[i] = MCPListToolsTool{
			Annotations: anns,
			Description: t.Description,
			InputSchema: t.InputSchema,
			Name:        t.Name,
		}
	}

	item.ToolCallData = MCPListTools{
		Type:        ItemTypeMCPListTools,
		ID:          lt.ID,
		ServerLabel: lt.ServerLabel,
		Tools:       tools,
		Error:       lt.Error,
	}
	return nil
}

func (item *InputItem) unmarshalCustomToolCallOutput(data []byte) error {
	var output CustomToolCallOutput
	if err := json.Unmarshal(data, &output); err != nil {
		return err
	}
	item.ToolCallData = output
	return nil
}

// isMessageType returns true for both explicit (type="message") and
// implicit (no type field) message formats per OpenAPI spec
func (item *InputItem) isMessageType() bool {
	return item.Type == ItemTypeMessage || item.Type == ""
}

// hasRequiredContent validates that content is present in the correct field:
// - Both typed and easy message formats must populate MessageContent
func (item *InputItem) hasRequiredContent() bool {
	if item.Type == ItemTypeMessage || item.Type == "" {
		return len(item.MessageContent) > 0
	}
	return true // Non-message types don't require content validation here
}

func (item *InputItem) Validate() error {
	// Skip validation for non-message types (function_call, item_reference, etc.)
	if item.Type != "" && item.Type != ItemTypeMessage {
		return nil
	}

	if item.Role == "" && item.isMessageType() {
		return &UnmarshalError{
			Field: "role",
			Msg:   "is required for message items",
		}
	}

	if item.Role != "" {
		validRoles := []string{RoleUser, RoleAssistant, RoleSystem, RoleDeveloper}
		var valid bool
		for _, role := range validRoles {
			if item.Role == role {
				valid = true
				break
			}
		}
		if !valid {
			return &UnmarshalError{
				Field: "role",
				Msg: fmt.Sprintf(
					"invalid role '%s', must be one of: %s",
					item.Role,
					strings.Join(validRoles, ", "),
				),
			}
		}

		// Note: Both EasyInputMessage and typed messages with type="message"
		// support all roles including assistant per the OpenAPI spec

		// Check the appropriate content field based on the type
		if !item.hasRequiredContent() {
			return &UnmarshalError{
				Field: "content",
				Msg:   "is required for messages",
			}
		}
	}

	return nil
}

// OutputItem represents polymorphic output items
type OutputItem struct {
	Type string `json:"type"`
	ID   string `json:"id"`

	// Common fields
	Status *string `json:"status,omitempty"`

	// For OutputMessage
	Role    string          `json:"role,omitempty"`
	Content []OutputContent `json:"content,omitempty"`

	// For tool calls - actual data stored in ToolCallData
	ToolCallData any `json:"-"`

	// For reasoning
	Summary []any `json:"summary,omitempty"`
	// ReasoningContentParts holds reasoning content parts and is serialized
	// as the `content` field for reasoning output items only.
	ReasoningContentParts []ReasoningContent `json:"-"`
}

// UnmarshalJSON implements custom unmarshaling for OutputItem
func (item *OutputItem) UnmarshalJSON(data []byte) error {
	var base struct {
		Type   string  `json:"type"`
		ID     string  `json:"id"`
		Status *string `json:"status"`
	}

	if err := json.Unmarshal(data, &base); err != nil {
		return err
	}

	item.Type = base.Type
	item.ID = base.ID
	item.Status = base.Status

	switch base.Type {
	case ItemTypeMessage:
		return item.unmarshalOutputMessage(data)
	case ItemTypeFunctionCall:
		return item.unmarshalFunctionCall(data)
	case ItemTypeWebSearchCall:
		return item.unmarshalWebSearchCall(data)
	case ItemTypeCodeInterpreterCall:
		return item.unmarshalCodeInterpreterCall(data)
	case ItemTypeReasoning:
		return item.unmarshalReasoning(data)
	case ItemTypeMCPListTools:
		return item.unmarshalMCPListTools(data)
	case ItemTypeMCPCall:
		return item.unmarshalMCPCall(data)
	case ItemTypeMCPApprovalRequest:
		return item.unmarshalMCPApprovalRequest(data)
	case ItemTypeCustomToolCall:
		return item.unmarshalCustomToolCall(data)
	default:
		return &UnmarshalError{
			Field: "type",
			Msg:   fmt.Sprintf("unknown output item type: %s", base.Type),
		}
	}
}

func (item *OutputItem) unmarshalOutputMessage(data []byte) error {
	var msg struct {
		Type    string          `json:"type"`
		ID      string          `json:"id"`
		Role    string          `json:"role"`
		Content []OutputContent `json:"content"`
		Status  *string         `json:"status"`
	}

	if err := json.Unmarshal(data, &msg); err != nil {
		return err
	}

	item.Role = msg.Role
	item.Content = msg.Content

	return nil
}

func (item *OutputItem) unmarshalFunctionCall(data []byte) error {
	var call FunctionToolCall
	if err := json.Unmarshal(data, &call); err != nil {
		return err
	}
	item.ToolCallData = call
	return nil
}

func (item *OutputItem) unmarshalWebSearchCall(data []byte) error {
	var call WebSearchToolCall
	if err := json.Unmarshal(data, &call); err != nil {
		return err
	}
	item.ToolCallData = call
	return nil
}

func (item *OutputItem) unmarshalCodeInterpreterCall(data []byte) error {
	var call CodeInterpreterToolCall
	if err := json.Unmarshal(data, &call); err != nil {
		return err
	}
	item.ToolCallData = call
	return nil
}

func (item *OutputItem) unmarshalReasoning(data []byte) error {
	var reasoning ReasoningItem
	if err := json.Unmarshal(data, &reasoning); err != nil {
		return err
	}
	item.Summary = reasoning.Summary
	item.ReasoningContentParts = reasoning.Content
	return nil
}

func (item *OutputItem) unmarshalMCPListTools(data []byte) error {
	var mcpList MCPListTools
	if err := json.Unmarshal(data, &mcpList); err != nil {
		return err
	}
	item.ToolCallData = mcpList
	return nil
}

func (item *OutputItem) unmarshalMCPCall(data []byte) error {
	var mcpCall MCPToolCall
	if err := json.Unmarshal(data, &mcpCall); err != nil {
		return err
	}
	item.ToolCallData = mcpCall
	return nil
}

func (item *OutputItem) unmarshalMCPApprovalRequest(data []byte) error {
	var mcpApproval MCPApprovalRequest
	if err := json.Unmarshal(data, &mcpApproval); err != nil {
		return err
	}
	item.ToolCallData = mcpApproval
	return nil
}

func (item *OutputItem) unmarshalCustomToolCall(data []byte) error {
	var customCall CustomToolCall
	if err := json.Unmarshal(data, &customCall); err != nil {
		return err
	}
	item.ToolCallData = customCall
	return nil
}

// GetFunctionToolCall returns the function tool call data if this output item is a function call
func (item *OutputItem) GetFunctionToolCall() (*FunctionToolCall, bool) {
	if item.Type == ItemTypeFunctionCall {
		if call, ok := item.ToolCallData.(FunctionToolCall); ok {
			return &call, true
		}
	}
	return nil, false
}

// GetWebSearchToolCall returns the web search tool call data if this output item is a web search call
func (item *OutputItem) GetWebSearchToolCall() (*WebSearchToolCall, bool) {
	if item.Type == ItemTypeWebSearchCall {
		if call, ok := item.ToolCallData.(WebSearchToolCall); ok {
			return &call, true
		}
	}
	return nil, false
}

// InputTextContent represents text input content
type InputTextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (c *InputTextContent) inputContentDiscriminator() string {
	return c.Type
}

// InputImageContent represents image input content
type InputImageContent struct {
	Type     string  `json:"type"`
	ImageURL *string `json:"image_url"`
	FileID   *string `json:"file_id"`
	Detail   string  `json:"detail"`
}

func (c *InputImageContent) inputContentDiscriminator() string {
	return c.Type
}

// InputFileContent represents file input content
type InputFileContent struct {
	Type     string  `json:"type"`
	FileID   *string `json:"file_id"`
	Filename *string `json:"filename"`
	FileURL  *string `json:"file_url"`
	FileData *string `json:"file_data"`
}

func (c *InputFileContent) inputContentDiscriminator() string {
	return c.Type
}

// InputContent represents polymorphic input content (discriminated union)
type InputContent struct {
	Type string           `json:"type"`
	Data inputContentData `json:"-"`
}

type inputContentData interface {
	inputContentDiscriminator() string
}

// NewInputTextContent creates a new InputContent with text content
func NewInputTextContent(text string) InputContent {
	content := &InputTextContent{
		Type: ContentTypeInputText,
		Text: text,
	}
	return InputContent{
		Type: ContentTypeInputText,
		Data: content,
	}
}

// NewInputImageContent creates a new InputContent with image content
func NewInputImageContent(imageURL *string, fileID *string, detail string) InputContent {
	content := &InputImageContent{
		Type:     ContentTypeInputImage,
		ImageURL: imageURL,
		FileID:   fileID,
		Detail:   detail,
	}
	return InputContent{
		Type: ContentTypeInputImage,
		Data: content,
	}
}

// NewInputFileContent creates a new InputContent with file content
func NewInputFileContent(
	fileID *string,
	filename *string,
	fileURL *string,
	fileData *string,
) InputContent {
	content := &InputFileContent{
		Type:     ContentTypeInputFile,
		FileID:   fileID,
		Filename: filename,
		FileURL:  fileURL,
		FileData: fileData,
	}
	return InputContent{
		Type: ContentTypeInputFile,
		Data: content,
	}
}

// UnmarshalJSON implements custom unmarshaling for InputContent using the registry
func (ic *InputContent) UnmarshalJSON(data []byte) error {
	var envelope struct {
		Type string `json:"type"`
	}

	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}

	if envelope.Type == "" {
		return &UnmarshalError{
			Field: "type",
			Msg:   "is required",
		}
	}

	ic.Type = envelope.Type

	switch envelope.Type {
	case ContentTypeInputText:
		var text InputTextContent
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		if text.Type == "" {
			text.Type = ContentTypeInputText
		}
		ic.Data = &text
	case ContentTypeInputImage:
		var image InputImageContent
		if err := json.Unmarshal(data, &image); err != nil {
			return err
		}
		image.Type = ContentTypeInputImage
		ic.Data = &image
	case ContentTypeInputFile:
		var file InputFileContent
		if err := json.Unmarshal(data, &file); err != nil {
			return err
		}
		file.Type = ContentTypeInputFile
		ic.Data = &file
	case ContentTypeOutputText, ContentTypeRefusal:
		// Support output content types for conversation history.
		// When including previous assistant responses in input, they may contain
		// output_text or refusal content which follow the OutputContent schema.
		var output OutputContent
		if err := json.Unmarshal(data, &output); err != nil {
			return err
		}
		if output.Type == "" {
			output.Type = envelope.Type
		}
		ic.Data = &output
	default:
		return &UnmarshalError{
			Field: "type",
			Msg:   fmt.Sprintf("unknown input content type: %s", envelope.Type),
		}
	}

	return nil
}

// MarshalJSON implements custom marshaling for InputContent
func (ic InputContent) MarshalJSON() ([]byte, error) {
	if ic.Data == nil {
		return json.Marshal(struct {
			Type string `json:"type"`
		}{
			Type: ic.Type,
		})
	}

	return json.Marshal(ic.Data)
}

// Type returns the discriminator type of this content (e.g., "input_text", "input_image")
func (ic *InputContent) Discriminator() string {
	if ic.Data != nil {
		return ic.Data.inputContentDiscriminator()
	}
	return ic.Type
}

// GetTextContent returns the text content data if this is a text content item
func (ic *InputContent) GetTextContent() (*InputTextContent, bool) {
	if text, ok := ic.Data.(*InputTextContent); ok {
		return text, true
	}
	return nil, false
}

// GetImageContent returns the image content data if this is an image content item
func (ic *InputContent) GetImageContent() (*InputImageContent, bool) {
	if image, ok := ic.Data.(*InputImageContent); ok {
		return image, true
	}
	return nil, false
}

// GetFileContent returns the file content data if this is a file content item
func (ic *InputContent) GetFileContent() (*InputFileContent, bool) {
	if file, ok := ic.Data.(*InputFileContent); ok {
		return file, true
	}
	return nil, false
}

// GetOutputContent returns the output content data if this is an output content item
func (ic *InputContent) GetOutputContent() (*OutputContent, bool) {
	if output, ok := ic.Data.(*OutputContent); ok {
		return output, true
	}
	return nil, false
}

// OutputContent represents different types of output content
type OutputContent struct {
	Type string `json:"type"`

	// For text content
	Text        string       `json:"text"`
	Annotations []Annotation `json:"annotations"`
	Logprobs    []string     `json:"logprobs"`

	// For refusal content
	Refusal string `json:"refusal,omitempty"`
}

func (c *OutputContent) inputContentDiscriminator() string {
	return c.Type
}

// ReasoningContent represents reasoning text content parts
type ReasoningContent struct {
	Type string `json:"type"` // Always ContentTypeReasoningText
	Text string `json:"text"`
}

// ReasoningSummaryItem represents a reasoning summary item
type ReasoningSummaryItem struct {
	Type string `json:"type"` // Always "summary_text"
	Text string `json:"text"`
}

// Annotation represents citations and file paths
type Annotation struct {
	Type string `json:"type"`

	// For file citations
	FileID string `json:"file_id,omitempty"`
	Index  *int   `json:"index,omitempty"`

	// For URL citations
	URL        string `json:"url,omitempty"`
	StartIndex *int   `json:"start_index,omitempty"`
	EndIndex   *int   `json:"end_index,omitempty"`
	Title      string `json:"title,omitempty"`
}

// ReasoningItem represents reasoning data
type ReasoningItem struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Summary []any  `json:"summary"`
	// Content holds the chain-of-thought text parts
	Content []ReasoningContent `json:"content,omitempty"`
	// EncryptedContent is present when included via include parameter
	EncryptedContent *string `json:"encrypted_content,omitempty"`
	Status           *string `json:"status,omitempty"`
}

// outputItemBase contains the common fields for JSON marshaling
type outputItemBase struct {
	Type   string  `json:"type"`
	ID     string  `json:"id"`
	Status *string `json:"status,omitempty"`
}

// outputItemMessage represents a message output item for JSON marshaling
type outputItemMessage struct {
	outputItemBase

	Role    string          `json:"role"`
	Content []OutputContent `json:"content"`
}

// outputItemFunctionCall represents a function call output item for JSON marshaling
type outputItemFunctionCall struct {
	outputItemBase

	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// outputItemWebSearchCall represents a web search call output item for JSON marshaling
type outputItemWebSearchCall struct {
	outputItemBase
}

// outputItemCodeInterpreterCall represents a code interpreter call output item for JSON marshaling
type outputItemCodeInterpreterCall struct {
	outputItemBase

	Code        *string                 `json:"code,omitempty"`
	ContainerID string                  `json:"container_id,omitempty"`
	Outputs     []CodeInterpreterOutput `json:"outputs,omitempty"`
}

// outputItemReasoning represents a reasoning output item for JSON marshaling
type outputItemReasoning struct {
	outputItemBase

	Content          []ReasoningContent `json:"content,omitempty"` // Chain-of-thought reasoning text
	Summary          []any              `json:"summary"`           // Required field per OpenAI spec (can be empty)
	EncryptedContent *string            `json:"encrypted_content,omitempty"`
}

// outputItemMCPListTools represents an MCP list tools output item for JSON marshaling
type outputItemMCPListTools struct {
	outputItemBase

	ServerLabel string             `json:"server_label"`
	Tools       []MCPListToolsTool `json:"tools"`
	Error       *string            `json:"error,omitempty"`
}

// outputItemMCPCall represents an MCP call output item for JSON marshaling
type outputItemMCPCall struct {
	outputItemBase

	ServerLabel       string  `json:"server_label"`
	Name              string  `json:"name"`
	Arguments         string  `json:"arguments"`
	ApprovalRequestID *string `json:"approval_request_id"`
	Output            *string `json:"output,omitempty"`
	Error             *string `json:"error,omitempty"`
}

// outputItemMCPApprovalRequest represents an MCP approval request output item for JSON marshaling
type outputItemMCPApprovalRequest struct {
	outputItemBase

	ServerLabel string `json:"server_label"`
	Name        string `json:"name"`
	Arguments   string `json:"arguments"`
}

// outputItemCustomToolCall represents a custom tool call output item for JSON marshaling
type outputItemCustomToolCall struct {
	outputItemBase

	Name  string `json:"name"`
	Input string `json:"input"`
}

func (item *OutputItem) MarshalJSON() ([]byte, error) {
	base := outputItemBase{
		Type:   item.Type,
		ID:     item.ID,
		Status: item.Status,
	}

	switch item.Type {
	case ItemTypeMessage:
		content := item.Content
		if content == nil {
			content = emptyOutputContent
		}
		return json.Marshal(outputItemMessage{
			outputItemBase: base,
			Role:           item.Role,
			Content:        content,
		})

	case ItemTypeFunctionCall:
		if call, ok := item.ToolCallData.(FunctionToolCall); ok {
			return json.Marshal(outputItemFunctionCall{
				outputItemBase: base,
				CallID:         call.CallID,
				Name:           call.Name,
				Arguments:      call.Arguments,
			})
		}

	case ItemTypeWebSearchCall:
		return json.Marshal(outputItemWebSearchCall{
			outputItemBase: base,
		})

	case ItemTypeCodeInterpreterCall:
		if call, ok := item.ToolCallData.(CodeInterpreterToolCall); ok {
			outputs := call.Outputs
			if outputs == nil {
				outputs = emptyCodeInterpreterOutput
			}
			return json.Marshal(outputItemCodeInterpreterCall{
				outputItemBase: base,
				Code:           call.Code,
				ContainerID:    call.ContainerID,
				Outputs:        outputs,
			})
		}

	case ItemTypeReasoning:
		// For reasoning items, content holds the chain-of-thought reasoning text
		// Summary is a required field but can be empty
		content := item.ReasoningContentParts
		if content == nil {
			content = emptyReasoningContent
		}
		summary := item.Summary
		if summary == nil {
			summary = emptyAnySlice
		}
		return json.Marshal(outputItemReasoning{
			outputItemBase:   base,
			Content:          content,
			Summary:          summary,
			EncryptedContent: nil,
		})

	case ItemTypeMCPListTools:
		if mcpList, ok := item.ToolCallData.(MCPListTools); ok {
			tools := mcpList.Tools
			if tools == nil {
				tools = emptyMCPListToolsTools
			}
			return json.Marshal(outputItemMCPListTools{
				outputItemBase: base,
				ServerLabel:    mcpList.ServerLabel,
				Tools:          tools,
				Error:          mcpList.Error,
			})
		}

	case ItemTypeMCPCall:
		if mcpCall, ok := item.ToolCallData.(MCPToolCall); ok {
			return json.Marshal(outputItemMCPCall{
				outputItemBase:    base,
				ServerLabel:       mcpCall.ServerLabel,
				Name:              mcpCall.Name,
				Arguments:         mcpCall.Arguments,
				ApprovalRequestID: mcpCall.ApprovalRequestID,
				Output:            mcpCall.Output,
				Error:             mcpCall.Error,
			})
		}

	case ItemTypeMCPApprovalRequest:
		if mcpApproval, ok := item.ToolCallData.(MCPApprovalRequest); ok {
			return json.Marshal(outputItemMCPApprovalRequest{
				outputItemBase: base,
				ServerLabel:    mcpApproval.ServerLabel,
				Name:           mcpApproval.Name,
				Arguments:      mcpApproval.Arguments,
			})
		}

	case ItemTypeCustomToolCall:
		if customCall, ok := item.ToolCallData.(CustomToolCall); ok {
			return json.Marshal(outputItemCustomToolCall{
				outputItemBase: base,
				Name:           customCall.Name,
				Input:          customCall.Input,
			})
		}
	}

	return json.Marshal(base)
}

// MCP Types

// MCPListTools represents a list of available MCP server tools
type MCPListTools struct {
	Type        string             `json:"type"` // Always "mcp_list_tools"
	ID          string             `json:"id"`
	ServerLabel string             `json:"server_label"`
	Tools       []MCPListToolsTool `json:"tools"`
	Error       *string            `json:"error"`
}

// MCPListToolsTool represents a single tool available on an MCP server
type MCPListToolsTool struct {
	Annotations *map[string]any `json:"annotations"`
	Description *string         `json:"description"`
	InputSchema map[string]any  `json:"input_schema"`
	Name        string          `json:"name"`
}

// MCPToolCall represents an invocation of a tool on an MCP server
type MCPToolCall struct {
	Type              string  `json:"type"` // Always "mcp_call"
	ID                string  `json:"id"`
	ServerLabel       string  `json:"server_label"`
	Name              string  `json:"name"`
	Arguments         string  `json:"arguments"` // JSON string
	ApprovalRequestID *string `json:"approval_request_id,omitempty"`
	Output            *string `json:"output"`
	Error             *string `json:"error"`
}

// MCPApprovalRequest represents an MCP approval request
type MCPApprovalRequest struct {
	Type        string `json:"type"` // Always "mcp_approval_request"
	ID          string `json:"id"`
	ServerLabel string `json:"server_label"`
	Name        string `json:"name"`
	Arguments   string `json:"arguments"` // JSON string
}

// MCPApprovalResponse represents an MCP approval response input item
type MCPApprovalResponse struct {
	Type              string `json:"type"`                // Always "mcp_approval_response"
	Approve           bool   `json:"approve"`             // true to approve, false to deny
	ApprovalRequestID string `json:"approval_request_id"` // ID of the approval request being responded to
}

// Custom Tool Call Types

// CustomToolCall represents a custom tool call
type CustomToolCall struct {
	Type   string  `json:"type"` // Always "custom_tool_call"
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Input  string  `json:"input"`
	Status *string `json:"status,omitempty"`
}

// CustomToolCallOutput represents output from a custom tool call
type CustomToolCallOutput struct {
	Type   string  `json:"type"` // Always "custom_tool_call_output"
	ID     string  `json:"id"`
	CallID string  `json:"call_id"`
	Output string  `json:"output"`
	Status *string `json:"status,omitempty"`
}
