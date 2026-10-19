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

// Package openairesponses provides types and validation for the OpenAI Responses API.
// This package implements a complete adapter for the OpenAI Responses API endpoint,
// including request/response types, custom unmarshaling for polymorphic types,
// and comprehensive validation.
package openairesponses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/models"
)

// UnmarshalError represents JSON unmarshaling errors with field context
type UnmarshalError struct {
	Field string
	Msg   string
}

func (err *UnmarshalError) Error() string {
	return fmt.Sprintf("`%v`: %v", err.Field, err.Msg)
}

// Constants for OpenAI Responses API
const (
	ObjectResponse      = "response"
	ObjectList          = "list"
	ObjectInputMessage  = "message"
	ObjectOutputMessage = "message"
)

// Response statuses
const (
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
	StatusInProgress = "in_progress"
	StatusIncomplete = "incomplete"
)

// Message roles
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleSystem    = "system"
	RoleDeveloper = "developer"
)

// Service tier options
const (
	ServiceTierAuto    = "auto"
	ServiceTierDefault = "default"
	ServiceTierFlex    = "flex"
)

// Truncation options
const (
	TruncationAuto     = "auto"
	TruncationDisabled = "disabled"
)

// Response format types
const (
	ResponseFormatTypeText       = "text"
	ResponseFormatTypeJSONObject = "json_object"
	ResponseFormatTypeJSONSchema = "json_schema"
)

// Includable items
const (
	IncludableFileSearchResults      = "file_search_call.results"
	IncludableInputImageURL          = "message.input_image.image_url"
	IncludableComputerCallOutputURL  = "computer_call_output.output.image_url"
	IncludableWebSearchResults       = "web_search_call.results"
	IncludableWebSearchActionSources = "web_search_call.action.sources"
	IncludableCodeInterpreterOutputs = "code_interpreter_call.outputs"
	IncludableReasoningEncrypted     = "reasoning.encrypted_content"
	IncludableMessageLogprobs        = "message.output_text.logprobs"
)

// CreateRequest represents the request for creating a response
type CreateRequest struct {
	// Required fields
	Model string `json:"model"`
	Input Input  `json:"input"`

	// Optional fields from ResponseProperties
	PreviousResponseID *string    `json:"previous_response_id"`
	Reasoning          *Reasoning `json:"reasoning"`
	// Background is accepted but ignored. All requests are processed synchronously.
	Background      *bool       `json:"background"`
	MaxOutputTokens *int        `json:"max_output_tokens"`
	MaxToolCalls    *int        `json:"max_tool_calls"`
	Instructions    *string     `json:"instructions"`
	Text            *Text       `json:"text"`
	Tools           ToolSlice   `json:"tools"`
	ToolChoice      *ToolChoice `json:"tool_choice"`
	Prompt          *Prompt     `json:"prompt"`
	Truncation      *string     `json:"truncation"`

	// Optional fields from CreateModelResponseProperties / ModelResponseProperties
	Metadata    *Metadata `json:"metadata"`
	TopLogprobs *int      `json:"top_logprobs"`
	Temperature *float64  `json:"temperature"`
	TopP        *float64  `json:"top_p"`
	// Deprecated: User field is deprecated. Use SafetyIdentifier for safety/abuse detection
	// and PromptCacheKey for caching optimization instead.
	User             *string `json:"user"`
	SafetyIdentifier *string `json:"safety_identifier"`
	PromptCacheKey   *string `json:"prompt_cache_key"`
	ServiceTier      *string `json:"service_tier"`

	// Create-specific fields
	Include           []string               `json:"include"`
	ParallelToolCalls *bool                  `json:"parallel_tool_calls"`
	Store             *bool                  `json:"store"`
	Stream            *bool                  `json:"stream"`
	StreamOptions     *ResponseStreamOptions `json:"stream_options"`
	Conversation      *Conversation          `json:"conversation"`

	// UndecodedMembers holds declared members that fail to decode. The gateway
	// does not need them, so it forwards them for the backend to validate.
	UndecodedMembers map[string]json.RawMessage `json:"-"`
}

func (*CreateRequest) DecodesMembers() {}

// UnmarshalJSON checks only the members that the gateway needs for routing,
// relay mode, admission and session affinity.
func (r *CreateRequest) UnmarshalJSON(data []byte) error {
	failed, err := models.DecodeMembers(data, r,
		"model", "input", "stream", "max_output_tokens", "prompt_cache_key", "conversation")
	r.UndecodedMembers = failed
	return err
}

// Response represents the response from the responses API
type Response struct {
	// Required fields
	ID        string       `json:"id"`
	Object    string       `json:"object"`
	Status    string       `json:"status"`
	CreatedAt int64        `json:"created_at"`
	Output    []OutputItem `json:"output"`

	// Optional fields from ResponseProperties
	PreviousResponseID *string     `json:"previous_response_id"`
	Model              *string     `json:"model,omitempty"`
	Reasoning          *Reasoning  `json:"reasoning"`
	MaxOutputTokens    *int        `json:"max_output_tokens"`
	Instructions       *string     `json:"instructions,omitempty"`
	Text               *Text       `json:"text,omitempty"`
	Tools              ToolSlice   `json:"tools"`
	ToolChoice         *ToolChoice `json:"tool_choice,omitempty"`
	Prompt             *Prompt     `json:"prompt,omitempty"`
	Truncation         *string     `json:"truncation,omitempty"`

	// Optional fields from ModelResponseProperties
	Metadata         *Metadata                 `json:"metadata,omitempty"`
	ProviderMetadata *ProviderResponseMetadata `json:"provider_metadata"`
	// Deprecated: use ProviderMetadata. Kept for wire compatibility with existing clients.
	Groq        *ProviderResponseMetadata `json:"groq"`
	Temperature *float64                  `json:"temperature,omitempty"`
	TopP        *float64                  `json:"top_p,omitempty"`
	// Deprecated: User field is deprecated. Use SafetyIdentifier for safety/abuse detection
	// and PromptCacheKey for caching optimization instead.
	User             *string `json:"user"`
	SafetyIdentifier *string `json:"safety_identifier,omitempty"`
	PromptCacheKey   *string `json:"prompt_cache_key,omitempty"`
	ServiceTier      *string `json:"service_tier,omitempty"`

	// Response-specific fields
	Background        *bool              `json:"background,omitempty"`
	Error             *ResponseError     `json:"error"`
	IncompleteDetails *IncompleteDetails `json:"incomplete_details"`
	Usage             *ResponseUsage     `json:"usage,omitempty"`
	ParallelToolCalls *bool              `json:"parallel_tool_calls,omitempty"`
	Store             *bool              `json:"store,omitempty"`
	TopLogprobs       *int               `json:"top_logprobs"`
	MaxToolCalls      *int               `json:"max_tool_calls"`
}

func (r Response) MarshalJSON() ([]byte, error) {
	type alias Response
	r.syncProviderMetadata()
	return json.Marshal(alias(r))
}

func (r *Response) UnmarshalJSON(data []byte) error {
	type alias Response
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*r = Response(decoded)
	r.syncProviderMetadata()
	return nil
}

func (r *Response) syncProviderMetadata() {
	if r.ProviderMetadata == nil {
		r.ProviderMetadata = r.Groq
	}
	if r.Groq == nil {
		r.Groq = r.ProviderMetadata
	}
}

// Input represents polymorphic input (string or array of InputItem)
type Input struct {
	Items []InputItem `json:"-"`
}

func (i *Input) UnmarshalJSON(data []byte) error {
	// Try string first
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		// Convert string to EasyInputMessage with user role
		i.Items = []InputItem{{
			Role: "user",
			MessageContent: []InputContent{
				NewInputTextContent(text),
			},
			// No Type field - this is EasyInputMessage format
		}}
		return nil
	}

	// Try array of InputItems
	var items []InputItem
	itemsErr := json.Unmarshal(data, &items)
	if itemsErr == nil {
		i.Items = items
		return nil
	}
	// A real array with a bad item keeps the item error so the client sees the cause.
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("[")) {
		return itemsErr
	}

	return &UnmarshalError{
		Field: "input",
		Msg:   "must be string or array of input items",
	}
}

// MarshalJSON marshals the Input - needed for tests
func (i Input) MarshalJSON() ([]byte, error) {
	// Always marshal as array (even if it was originally a string)
	return json.Marshal(i.Items)
}

// Validate checks only what admission needs. The backend validates the rest.
func (r *CreateRequest) Validate() error {
	if r.Model == "" {
		return &UnmarshalError{
			Field: "model",
			Msg:   "is required",
		}
	}
	if len(r.Input.Items) == 0 && !r.hasStoredContext() {
		return &UnmarshalError{
			Field: "input",
			Msg:   "must provide input, prompt, previous_response_id or conversation",
		}
	}
	return nil
}

// hasStoredContext reports whether prompt, previous_response_id or
// conversation gives the context, even when its value fails to decode.
func (r *CreateRequest) hasStoredContext() bool {
	if r.Prompt != nil || r.PreviousResponseID != nil || r.Conversation != nil {
		return true
	}
	for key, value := range r.UndecodedMembers {
		if (strings.EqualFold(key, "prompt") || strings.EqualFold(key, "previous_response_id")) &&
			!bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return true
		}
	}
	return false
}

// Helper functions

// GetUserIdentifier returns the user identifier, prioritizing SafetyIdentifier over the deprecated User field.
func (r *CreateRequest) GetUserIdentifier() *string {
	switch {
	case r.SafetyIdentifier != nil:
		return r.SafetyIdentifier
	default:
		// Access deprecated field for backward compatibility

		return r.User
	}
}

// GetText extracts text from the first text content in a response
func (r *Response) GetText() string {
	for _, item := range r.Output {
		if item.Type == ItemTypeMessage && item.Role == RoleAssistant {
			for _, content := range item.Content {
				if content.Type == ContentTypeOutputText {
					return content.Text
				}
			}
		}
	}
	return ""
}

// IsCompleted returns true if the response is completed
func (r *Response) IsCompleted() bool {
	return r.Status == StatusCompleted
}

// HasError returns true if the response has an error
func (r *Response) HasError() bool {
	return r.Error != nil
}

// IsItems returns true if the input is an array of items
func (i *Input) IsItems() bool {
	return len(i.Items) > 0
}
