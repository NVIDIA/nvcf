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

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/mcp"
)

// Exported constants for tool types, statuses, and choices
const (
	// Tool types
	ToolTypeFunction        = "function"
	ToolTypeBrowserSearch   = "browser_search"
	ToolTypeCodeInterpreter = "code_interpreter"
	ToolTypeMCP             = "mcp"

	// Tool choice options
	ToolChoiceNone     = "none"
	ToolChoiceAuto     = "auto"
	ToolChoiceRequired = "required"

	// Require approval options
	RequireApprovalAlways = "always"
	RequireApprovalNever  = "never"

	// Tool call statuses
	ToolCallStatusInProgress   = "in_progress"
	ToolCallStatusCompleted    = "completed"
	ToolCallStatusIncomplete   = "incomplete"
	ToolCallStatusFailed       = "failed"
	ToolCallStatusSearching    = "searching"
	ToolCallStatusInterpreting = "interpreting"
)

// Cached pointers for common approval strings to avoid repeated allocations
var (
	requireApprovalAlwaysPtr = ptr.To(RequireApprovalAlways)
)

// Interface assertions
var (
	_ Tool = (*FunctionTool)(nil)
	_ Tool = (*BrowserSearchTool)(nil)
	_ Tool = (*CodeInterpreterTool)(nil)
	_ Tool = (*MCPTool)(nil)
)

// isBrowserSearchType checks if a tool type is a browser search variant
func isBrowserSearchType(toolType string) bool {
	return toolType == ToolTypeBrowserSearch || strings.HasPrefix(toolType, "web_search_preview")
}

// Tool interface represents different types of tools
type Tool interface {
	GetType() string
	Validate() error
}

// ToolBase is embedded in all tool types
type ToolBase struct {
	Type string `json:"type"`
}

func (t ToolBase) GetType() string {
	return t.Type
}

// ValidateType checks if the Type field is set
func (t ToolBase) ValidateType() error {
	if t.Type == "" {
		return &UnmarshalError{
			Field: "type",
			Msg:   "is required",
		}
	}
	return nil
}

// FunctionTool represents a function tool
type FunctionTool struct {
	ToolBase

	Name        string         `json:"name"`
	Description *string        `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Strict      *bool          `json:"strict"`
}

func (t *FunctionTool) Validate() error {
	if err := t.ValidateType(); err != nil {
		return err
	}
	if t.Type != ToolTypeFunction {
		return &UnmarshalError{
			Field: "type",
			Msg:   "must be 'function' for FunctionTool",
		}
	}
	if t.Name == "" {
		return &UnmarshalError{
			Field: "name",
			Msg:   "is required for function tools",
		}
	}
	return nil
}

// BrowserSearchTool represents a browser or web search tool
type BrowserSearchTool struct {
	ToolBase

	UserLocation      *UserLocation `json:"user_location"`
	SearchContextSize *string       `json:"search_context_size"`
}

func (t *BrowserSearchTool) Validate() error {
	if err := t.ValidateType(); err != nil {
		return err
	}
	if !isBrowserSearchType(t.Type) {
		return &UnmarshalError{
			Field: "type",
			Msg:   "must be 'browser_search' or 'web_search_preview*' for BrowserSearchTool",
		}
	}
	return nil
}

// CodeInterpreterTool represents a code interpreter tool
type CodeInterpreterTool struct {
	ToolBase

	Container any `json:"container"` // Can be string (container ID) or CodeInterpreterContainer
}

func (t *CodeInterpreterTool) Validate() error {
	if err := t.ValidateType(); err != nil {
		return err
	}
	if t.Type != ToolTypeCodeInterpreter {
		return &UnmarshalError{
			Field: "type",
			Msg:   "must be 'code_interpreter' for CodeInterpreterTool",
		}
	}
	// Container field is optional for code interpreter tools
	return nil
}

// MCPTool represents an MCP tool
type MCPTool struct {
	ToolBase

	ServerLabel       string              `json:"server_label"`
	ServerURL         string              `json:"server_url"`
	ConnectorID       string              `json:"connector_id,omitempty"`
	Headers           *map[string]string  `json:"headers"`
	Authorization     *string             `json:"authorization,omitempty"`
	AllowedTools      any                 `json:"allowed_tools,omitempty"`
	RequireApproval   *MCPRequireApproval `json:"require_approval,omitempty"`
	ServerDescription *string             `json:"server_description,omitempty"`
}

func (t *MCPTool) Validate() error {
	if err := t.ValidateType(); err != nil {
		return err
	}
	if t.Type != ToolTypeMCP {
		return &UnmarshalError{
			Field: "type",
			Msg:   "must be 'mcp' for MCPTool",
		}
	}
	if t.ServerLabel == "" {
		return &UnmarshalError{
			Field: "server_label",
			Msg:   "is required for MCP tools",
		}
	}
	if t.ServerURL == "" && t.ConnectorID == "" {
		return &UnmarshalError{
			Field: "connector_id",
			Msg:   "server_url or connector_id is required for MCP tools",
		}
	}
	// require_approval validation is handled by MCPRequireApproval.UnmarshalJSON
	return nil
}

// GetRequireApproval returns the require_approval configuration for this tool
func (t *MCPTool) GetRequireApproval() *mcp.RequireApproval {
	if t.RequireApproval == nil {
		return nil
	}

	// Convert openairesponses types to mcp types
	mcpApproval := &mcp.RequireApproval{
		String: t.RequireApproval.String,
	}

	if t.RequireApproval.Object != nil {
		mcpObj := &mcp.RequireApprovalObject{
			Always: t.RequireApproval.Object.Always,
		}
		if t.RequireApproval.Object.Never != nil {
			mcpObj.Never = &mcp.RequireApprovalNever{
				ToolNames: t.RequireApproval.Object.Never.ToolNames,
			}
		}
		mcpApproval.Object = mcpObj
	}

	return mcpApproval
}

// RequiresApprovalForTool checks if the given tool name requires approval
func (t *MCPTool) RequiresApprovalForTool(toolName string) bool {
	if t.RequireApproval == nil {
		return false
	}

	// String form: "always" or "never"
	if t.RequireApproval.String != nil {
		return ptr.Deref(t.RequireApproval.String) == RequireApprovalAlways
	}

	// Object form
	if obj := t.RequireApproval.Object; obj != nil {
		if obj.Never != nil {
			for _, n := range obj.Never.ToolNames {
				if n == toolName {
					return false
				}
			}
		}
		// If always key present, require approval for anything not explicitly in never
		if obj.HasAlways() {
			return true
		}
		// If only never is present but does not include this tool, require approval
		if obj.Never != nil {
			return true
		}
	}

	// Default to no approval required
	return false
}

// GetApprovalStringValue returns the approval value as a string for backward compatibility
func (t *MCPTool) GetApprovalStringValue() *string {
	if t.RequireApproval == nil {
		return nil
	}

	if t.RequireApproval.String != nil {
		return t.RequireApproval.String
	}
	// For object format, return "always" since it's more complex approval logic
	return requireApprovalAlwaysPtr
}

// MCPRequireApproval represents the polymorphic require_approval field
// It can be either a string ("always"|"never") or an object like
// {"never": {"tool_names": ["tool1", ...]}} or {"always": {...}}
type MCPRequireApproval struct {
	String *string                   `json:"-"`
	Object *MCPRequireApprovalObject `json:"-"`
}

// MCPRequireApprovalObject represents the object form of require_approval
type MCPRequireApprovalObject struct {
	Never *MCPRequireApprovalNever `json:"never,omitempty"`
	// Presence of the key indicates approval is always required; value is ignored
	Always json.RawMessage `json:"always,omitempty"`
}

// HasAlways returns true if the object had an "always" key
func (o *MCPRequireApprovalObject) HasAlways() bool {
	return o != nil && o.Always != nil && len(o.Always) > 0
}

// MCPRequireApprovalNever lists tool names that never require approval
type MCPRequireApprovalNever struct {
	ToolNames []string `json:"tool_names,omitempty"`
}

// UnmarshalJSON attempts to decode as a string first, then as an object.
// Failed deserialization is used as control flow per review guidance.
func (ra *MCPRequireApproval) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		switch s {
		case RequireApprovalAlways, RequireApprovalNever:
			ra.String = &s
			ra.Object = nil
			return nil
		default:
			return &UnmarshalError{
				Field: "require_approval",
				Msg:   "must be 'always' or 'never'",
			}
		}
	}

	var obj MCPRequireApprovalObject
	if err := json.Unmarshal(data, &obj); err == nil {
		ra.Object = &obj
		ra.String = nil
		return nil
	}

	return &UnmarshalError{
		Field: "require_approval",
		Msg:   "must be string or object",
	}
}

// buildRequireApprovalForJSON converts the RequireApproval field to appropriate JSON representation
func (t *MCPTool) buildRequireApprovalForJSON() any {
	if t.RequireApproval == nil {
		return nil
	}

	switch {
	case t.RequireApproval.String != nil:
		return ptr.Deref(t.RequireApproval.String)
	case t.RequireApproval.Object != nil:
		// Preserve object form to better match OpenAI echo behavior
		obj := map[string]any{}
		if t.RequireApproval.Object.HasAlways() {
			// OpenAI often includes read_only and tool_names (possibly empty)
			obj["always"] = map[string]any{
				"read_only":  nil,
				"tool_names": []string{},
			}
		}
		if t.RequireApproval.Object.Never != nil {
			obj["never"] = map[string]any{
				"read_only":  nil,
				"tool_names": t.RequireApproval.Object.Never.ToolNames,
			}
		}
		if len(obj) > 0 {
			return obj
		}
		// Fallback to string form if object carried no usable keys
		return RequireApprovalAlways
	default:
		return nil
	}
}

// sanitizeHeadersForJSON creates a copy of headers with Authorization values redacted
func (t *MCPTool) sanitizeHeadersForJSON() *map[string]string {
	if t.Headers == nil {
		return nil
	}

	sanitized := make(map[string]string, len(*t.Headers))
	for k, v := range *t.Headers {
		if strings.EqualFold(k, "authorization") {
			sanitized[k] = "<redacted>"
		} else {
			sanitized[k] = v
		}
	}
	return &sanitized
}

func (t *MCPTool) sanitizeAuthorizationForJSON() string {
	if t.Authorization == nil || ptr.Deref(t.Authorization) == "" {
		return ""
	}
	return "<redacted>"
}

// MarshalJSON implements custom JSON marshaling to redact server URLs for security
func (t *MCPTool) MarshalJSON() ([]byte, error) {
	var (
		serverURL         *string
		headers           *map[string]string
		serverDescription *string
	)

	isConnector := t.ConnectorID != ""

	switch {
	case isConnector:
		serverURL = nil
		headers = nil
		serverDescription = nil
	case mcp.IsInternalConnector(t.ServerURL):
		serverURL = nil
		headers = nil
		serverDescription = nil
	default:
		redactedURL := mcp.RedactServerURL(t.ServerURL)
		serverURL = &redactedURL
		headers = t.sanitizeHeadersForJSON()
		serverDescription = t.ServerDescription
	}

	out := struct {
		ToolBase

		ServerLabel       string             `json:"server_label"`
		ServerURL         *string            `json:"server_url"`
		ConnectorID       string             `json:"connector_id,omitempty"`
		Headers           *map[string]string `json:"headers"`
		Authorization     string             `json:"authorization,omitempty"`
		AllowedTools      any                `json:"allowed_tools"`
		RequireApproval   any                `json:"require_approval,omitempty"`
		ServerDescription *string            `json:"server_description"`
	}{
		ToolBase:          t.ToolBase,
		ServerLabel:       t.ServerLabel,
		ServerURL:         serverURL,
		ConnectorID:       t.ConnectorID,
		Headers:           headers,
		Authorization:     t.sanitizeAuthorizationForJSON(),
		AllowedTools:      t.AllowedTools,
		RequireApproval:   t.buildRequireApprovalForJSON(),
		ServerDescription: serverDescription,
	}

	return json.Marshal(out)
}

// UnmarshalTool unmarshals JSON data into the appropriate Tool type
func UnmarshalTool(data []byte) (Tool, error) {
	// First, unmarshal to get the type field
	var typeOnly ToolBase
	if err := json.Unmarshal(data, &typeOnly); err != nil {
		return nil, err
	}

	// Based on type, unmarshal to the appropriate concrete type
	switch {
	case typeOnly.Type == ToolTypeFunction:
		var tool FunctionTool
		if err := json.Unmarshal(data, &tool); err != nil {
			return nil, err
		}
		return &tool, nil
	case isBrowserSearchType(typeOnly.Type):
		var tool BrowserSearchTool
		if err := json.Unmarshal(data, &tool); err != nil {
			return nil, err
		}
		return &tool, nil
	case typeOnly.Type == ToolTypeCodeInterpreter:
		var tool CodeInterpreterTool
		if err := json.Unmarshal(data, &tool); err != nil {
			return nil, err
		}
		return &tool, nil
	case typeOnly.Type == ToolTypeMCP:
		var tool MCPTool
		if err := json.Unmarshal(data, &tool); err != nil {
			return nil, err
		}
		return &tool, nil
	default:
		return nil, &UnmarshalError{
			Field: "type",
			Msg:   fmt.Sprintf("unknown tool type: %s", typeOnly.Type),
		}
	}
}

// ToolSlice is a slice of Tools with custom JSON unmarshaling
type ToolSlice []Tool

// UnmarshalJSON implements custom unmarshaling for a slice of tools
func (ts *ToolSlice) UnmarshalJSON(data []byte) error {
	// First unmarshal as raw JSON array
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// Unmarshal each tool individually
	tools := make([]Tool, 0, len(raw))
	for i, rawTool := range raw {
		tool, err := UnmarshalTool(rawTool)
		if err != nil {
			return fmt.Errorf("failed to unmarshal tool at index %d: %w", i, err)
		}
		tools = append(tools, tool)
	}

	*ts = tools
	return nil
}

// FindFunctionTool searches for a function tool by name and returns its parameters schema
func (ts *ToolSlice) FindFunctionTool(name string) (map[string]any, bool) {
	for _, tool := range *ts {
		if ft, ok := tool.(*FunctionTool); ok && ft.Name == name {
			return ft.Parameters, true
		}
	}
	return nil, false
}

// ToolChoice represents tool choice options
type ToolChoice struct {
	Type string `json:"type,omitempty"`
	Name string `json:"name,omitempty"`
}

func (tc *ToolChoice) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		if str == ToolChoiceNone || str == ToolChoiceAuto || str == ToolChoiceRequired {
			tc.Type = str
			tc.Name = ""
			return nil
		}
		return &UnmarshalError{
			Field: "tool_choice",
			Msg:   fmt.Sprintf("invalid tool choice string: %s", str),
		}
	}

	var obj struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &obj); err == nil {
		tc.Type = obj.Type
		tc.Name = obj.Name
		return nil
	}

	return &UnmarshalError{
		Field: "tool_choice",
		Msg:   "must be string or object",
	}
}

func (tc ToolChoice) MarshalJSON() ([]byte, error) {
	if tc.Name == "" &&
		(tc.Type == ToolChoiceNone || tc.Type == ToolChoiceAuto || tc.Type == ToolChoiceRequired) {
		return json.Marshal(tc.Type)
	}
	return json.Marshal(struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}{
		Type: tc.Type,
		Name: tc.Name,
	})
}

// Tool call structs

// FunctionToolCall represents a function tool call
type FunctionToolCall struct {
	ID        *string `json:"id,omitempty"`
	Type      string  `json:"type"`
	CallID    string  `json:"call_id"`
	Name      string  `json:"name"`
	Arguments string  `json:"arguments"`
	Status    *string `json:"status,omitempty"`
}

// FunctionCallOutput represents function call output
type FunctionCallOutput struct {
	ID     *string `json:"id,omitempty"`
	CallID string  `json:"call_id"`
	Type   string  `json:"type"`
	Output string  `json:"output"`
	Status *string `json:"status,omitempty"`
}

// WebSearchToolCall represents a web search tool call
type WebSearchToolCall struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Status string `json:"status"`
}

// CodeInterpreterToolCall represents a code interpreter tool call
type CodeInterpreterToolCall struct {
	ID          string                  `json:"id"`
	Type        string                  `json:"type"`
	Status      string                  `json:"status"`
	ContainerID string                  `json:"container_id"`
	Code        *string                 `json:"code"`
	Outputs     []CodeInterpreterOutput `json:"outputs"`
}

// CodeInterpreterOutput represents code interpreter output as an interface
type CodeInterpreterOutput any

// CodeInterpreterOutputLogs represents logs output from the code interpreter
type CodeInterpreterOutputLogs struct {
	Type string `json:"type"` // Always "logs"
	Logs string `json:"logs"`
}

// CodeInterpreterOutputImage represents image output from the code interpreter
type CodeInterpreterOutputImage struct {
	Type string `json:"type"` // Always "image"
	URL  string `json:"url"`
}

// CodeInterpreterOutputFiles represents file output from the code interpreter
type CodeInterpreterOutputFiles struct {
	Type  string                      `json:"type"` // Always "files"
	Files []CodeInterpreterFileOutput `json:"files"`
}

// CodeInterpreterFileOutput represents a single file output
type CodeInterpreterFileOutput struct {
	MimeType string `json:"mime_type"`
	FileID   string `json:"file_id"`
}

// CodeInterpreterContainer represents the container configuration for code interpreter
type CodeInterpreterContainer struct {
	Type    string   `json:"type"` // Always "auto"
	FileIDs []string `json:"file_ids,omitempty"`
}

// UserLocation represents user location for web search
type UserLocation struct {
	Type     string  `json:"type"`
	Country  *string `json:"country,omitempty"`
	Region   *string `json:"region,omitempty"`
	City     *string `json:"city,omitempty"`
	Timezone *string `json:"timezone,omitempty"`
}
