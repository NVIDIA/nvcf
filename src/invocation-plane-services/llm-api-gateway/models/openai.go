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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/must"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/servicetier"
)

type UnmarshalError struct {
	Field string
	Msg   string
}

func (err *UnmarshalError) Error() string {
	return fmt.Sprintf("`%v`: %v", err.Field, err.Msg)
}

const (
	ObjectList                = "list"
	ObjectModel               = "model"
	ObjectFunction            = "function"
	ObjectChatCompletion      = "chat.completion"
	ObjectChatCompletionChunk = "chat.completion.chunk"
)

type OAIModel struct {
	ID                  string   `json:"id"`
	Object              string   `json:"object"`
	CreatedAt           int64    `json:"created"`
	OwnedBy             string   `json:"owned_by"`
	Active              bool     `json:"active"`
	ContextWindow       uint32   `json:"context_window"`
	PublicApps          []string `json:"public_apps"`
	MaxCompletionTokens uint32   `json:"max_completion_tokens"`
}

type ModelListResponse struct {
	Object string     `json:"object"`
	Data   []OAIModel `json:"data"`
}

type ChatResponseFormatType string

const (
	ChatResponseTextFormatType       ChatResponseFormatType = "text"
	ChatResponseJSONObjectFormatType ChatResponseFormatType = "json_object"
	ChatResponseJSONSchemaFormatType ChatResponseFormatType = "json_schema"
)

func (t ChatResponseFormatType) String() string {
	return string(t)
}

type JSONSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Schema      map[string]any `json:"schema"`
	Strict      bool           `json:"strict"`
}

const (
	ChatToolSelectionNone     = "none"
	ChatToolSelectionAuto     = "auto"
	ChatToolSelectionRequired = "required"
)

func IsValidChatToolSelection(selection string) bool {
	switch selection {
	case ChatToolSelectionNone, ChatToolSelectionAuto, ChatToolSelectionRequired:
		return true
	default:
		return false
	}
}

const (
	FinishReasonLength       = "length"
	FinishReasonStop         = "stop"
	FinishReasonToolCalls    = "tool_calls"
	FinishReasonFunctionCall = "function_call"
)

const (
	ChatCompletionRoleAssistant = "assistant"
	ChatCompletionRoleSystem    = "system"
	ChatCompletionRoleDeveloper = "developer"
	ChatCompletionRoleUser      = "user"
	ChatCompletionRoleFunction  = "function"
	ChatCompletionRoleTool      = "tool"
)

const (
	ReasoningFormatRaw    = "raw"
	ReasoningFormatHidden = "hidden"
	ReasoningFormatParsed = "parsed"
	ReasoningFormatAuto   = "auto"

	ReasoningEffortNone    = "none"
	ReasoningEffortDefault = "default"
	ReasoningEffortLow     = "low"
	ReasoningEffortMedium  = "medium"
	ReasoningEffortHigh    = "high"
)

const (
	ToolTypeFunction        = "function"
	ToolTypeMCP             = "mcp"
	ToolTypeBrowserSearch   = "browser_search"
	ToolTypeBrowserOpen     = "browser.open"
	ToolTypeBrowserFind     = "browser.find"
	ToolTypePython          = "python"
	ToolTypeCodeInterpreter = "code_interpreter"
)

type ChatToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ChatToolCall struct {
	ID       string               `json:"id"`
	Type     string               `json:"type"`
	Function ChatToolCallFunction `json:"function"`
}

type ChatFunctionSpec struct {
	Name        string          `json:"name"`
	Description *string         `json:"description"`
	Parameters  *map[string]any `json:"parameters"`
	Strict      *bool           `json:"strict"`
}

type ChatTool struct {
	Type            string            `json:"type"`
	Function        ChatFunctionSpec  `json:"function"`
	ServerLabel     string            `json:"server_label,omitempty"`
	ServerURL       string            `json:"server_url,omitempty"`
	ConnectorID     string            `json:"connector_id,omitempty"`
	SessionID       *string           `json:"session_id,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	AllowedTools    []string          `json:"allowed_tools,omitempty"`
	RequireApproval any               `json:"require_approval,omitempty"`
	Authorization   string            `json:"-"`
}

type ChatFunctionChoice struct {
	Name string `json:"name"`
}

type ChatToolChoice struct {
	Type     string             `json:"type"`
	Function ChatFunctionChoice `json:"function"`
}

type ContentPartType string

const (
	ContentPartTypeText     ContentPartType = "text"
	ContentPartTypeImageURL ContentPartType = "image_url"
	ContentPartTypeDocument ContentPartType = "document"
)

func (t ContentPartType) String() string {
	return string(t)
}

var SupportedContentType = []ContentPartType{
	ContentPartTypeText,
	ContentPartTypeImageURL,
	ContentPartTypeDocument,
}

type ContentPart interface {
	ContentType() ContentPartType
}

type ChatMessageContent []ContentPart

type ContentPartText string

func (t ContentPartText) ContentType() ContentPartType {
	return ContentPartTypeText
}

func (t ContentPartText) String() string {
	return string(t)
}

type ContentPartImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail"`
	Image  []byte `json:"-"`
}

func (cp ContentPartImageURL) ContentType() ContentPartType {
	return ContentPartTypeImageURL
}

type ContentPartDocument struct {
	Data map[string]any `json:"data"`
	ID   *string        `json:"id,omitempty"`
}

func (cp ContentPartDocument) ContentType() ContentPartType {
	return ContentPartTypeDocument
}

func (content *ChatMessageContent) HasNoMultimodalContent() bool {
	for _, part := range *content {
		if _, ok := part.(ContentPartText); !ok {
			return false
		}
	}
	return true
}

func (content *ChatMessageContent) TrySingleText() (string, error) {
	if len(*content) == 0 {
		return "", errors.New("empty content")
	}

	if len(*content) == 1 {
		text, ok := (*content)[0].(ContentPartText)
		if !ok {
			return "", errors.New("this model only supports content.type == text")
		}
		return text.String(), nil
	}

	var builder strings.Builder
	for _, part := range *content {
		text, ok := part.(ContentPartText)
		if !ok {
			return "", errors.New("this model only supports content.type == text")
		}
		builder.WriteString(text.String())
	}
	return builder.String(), nil
}

func (content *ChatMessageContent) MustSingleText() string {
	return must.Get(content.TrySingleText())
}

func SingleTextContent(text string) []ContentPart {
	return []ContentPart{ContentPartText(text)}
}

type ChatMessage struct {
	Role         string                      `json:"role"`
	Content      ChatMessageContent          `json:"content"`
	Name         *string                     `json:"name,omitempty"`
	ToolCalls    *[]ChatToolCall             `json:"tool_calls,omitempty"`
	Reasoning    *string                     `json:"reasoning,omitempty"`
	FunctionCall *ChatCompletionFunctionCall `json:"function_call,omitempty"`
	ToolCallID   *string                     `json:"tool_call_id,omitempty"`
	Channel      string                      `json:"channel,omitempty"`
}

type ChatResponseFormat struct {
	Type       *ChatResponseFormatType `json:"type"`
	JSONSchema *JSONSchema             `json:"json_schema,omitempty"`
}

type ChatCompletionStopField []string

type ChatCompletionToolChoiceField struct {
	String     *string
	ToolChoice *ChatToolChoice
}

type ChatCompletionFunctionChoiceField struct {
	String       *string
	FunctionCall *ChatFunctionChoice
}

type ChatCompletionRequest struct {
	Messages            *[]ChatMessage                    `json:"messages"`
	Model               string                            `json:"model"`
	Debug               bool                              `json:"debug"`
	ServiceTier         servicetier.Tier                  `json:"service_tier"`
	FrequencyPenalty    *float32                          `json:"frequency_penalty"`
	IncludeReasoning    *bool                             `json:"include_reasoning"`
	LogitBias           *map[string]int                   `json:"logit_bias"`
	Logprobs            *bool                             `json:"logprobs"`
	TopLogprobs         *uint32                           `json:"top_logprobs"`
	MaxTokens           *uint32                           `json:"max_tokens"`
	MaxCompletionTokens *uint32                           `json:"max_completion_tokens"`
	N                   *uint32                           `json:"n"`
	PresencePenalty     *float32                          `json:"presence_penalty"`
	ResponseFormat      *ChatResponseFormat               `json:"response_format"`
	Seed                *int64                            `json:"seed"`
	Stop                ChatCompletionStopField           `json:"stop"`
	Stream              *bool                             `json:"stream"`
	Temperature         *float32                          `json:"temperature"`
	TopP                *float32                          `json:"top_p"`
	Tools               *[]ChatTool                       `json:"tools"`
	Functions           *[]ChatFunctionSpec               `json:"functions"`
	ToolChoice          ChatCompletionToolChoiceField     `json:"tool_choice"`
	FunctionChoice      ChatCompletionFunctionChoiceField `json:"function_call"`
	ParallelToolCalls   *bool                             `json:"parallel_tool_calls"`
	User                *string                           `json:"user"`
	ReasoningFormat     *string                           `json:"reasoning_format"`
	ReasoningEffort     *string                           `json:"reasoning_effort"`
	StreamOptions       *ChatCompletionStreamOptions      `json:"stream_options"`
	Metadata            *map[string]string                `json:"metadata"`
	Store               *bool                             `json:"store"`
}

type ChatCompletionStreamOptions struct {
	IncludeUsage *bool `json:"include_usage"`
}

type ChatCompletionFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ChatCompletionToolCall struct {
	ID       string                     `json:"id"`
	Type     string                     `json:"type"`
	Function ChatCompletionFunctionCall `json:"function"`
}

type ChatCompletionToolCallChunk struct {
	ID       string                     `json:"id"`
	Type     string                     `json:"type"`
	Function ChatCompletionFunctionCall `json:"function"`
	Index    uint32                     `json:"index"`
}

type ChatCompletionMessage struct {
	Role         string                      `json:"role"`
	Content      *string                     `json:"content,omitempty"`
	Reasoning    *string                     `json:"reasoning,omitempty"`
	ToolCalls    *[]ChatCompletionToolCall   `json:"tool_calls,omitempty"`
	FunctionCall *ChatCompletionFunctionCall `json:"function_call,omitempty"`
}

type ChatCompletionChunkDelta struct {
	Role           *string                        `json:"role,omitempty"`
	Content        *string                        `json:"content,omitempty"`
	Reasoning      *string                        `json:"reasoning,omitempty"`
	SendNilContent bool                           `json:"-"`
	ToolCalls      *[]ChatCompletionToolCallChunk `json:"tool_calls,omitempty"`
	FunctionCall   *ChatCompletionFunctionCall    `json:"function_call,omitempty"`
	Channel        string                         `json:"channel,omitempty"`
}

func (ccd *ChatCompletionChunkDelta) MarshalJSON() ([]byte, error) {
	type alias ChatCompletionChunkDelta
	if ccd.SendNilContent {
		return json.Marshal(&struct {
			*alias
			Content *string `json:"content"`
		}{
			alias: (*alias)(ccd),
		})
	}

	return json.Marshal(&struct {
		*alias
	}{
		alias: (*alias)(ccd),
	})
}

type ChatCompletionChoice struct {
	Index        uint32                `json:"index"`
	Message      ChatCompletionMessage `json:"message"`
	FinishReason string                `json:"finish_reason"`
}

type ChatCompletionChunkChoice struct {
	Index        uint32                   `json:"index"`
	Delta        ChatCompletionChunkDelta `json:"delta"`
	FinishReason *string                  `json:"finish_reason"`
}

type PromptTokensDetails struct {
	CachedTokens uint32 `json:"cached_tokens"`
}

type CompletionTokensDetails struct {
	ReasoningTokens uint32 `json:"reasoning_tokens"`
}

type ChatCompletionUsage struct {
	QueueTime               *float64                 `json:"queue_time,omitempty"`
	PromptTokens            uint32                   `json:"prompt_tokens"`
	PromptTime              float64                  `json:"prompt_time"`
	CompletionTokens        uint32                   `json:"completion_tokens"`
	CompletionTime          float64                  `json:"completion_time"`
	TotalTokens             uint32                   `json:"total_tokens"`
	TotalTime               float64                  `json:"total_time"`
	PromptTokensDetails     *PromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
}

type ChatCompletionResponse struct {
	ID                string                 `json:"id"`
	Object            string                 `json:"object"`
	CreatedAt         int64                  `json:"created"`
	Model             string                 `json:"model"`
	Choices           []ChatCompletionChoice `json:"choices"`
	Usage             ChatCompletionUsage    `json:"usage"`
	SystemFingerprint *string                `json:"system_fingerprint,omitempty"`
	ServiceTier       servicetier.Tier       `json:"service_tier,omitempty"`
}

func (c *ChatCompletionResponse) FirstFinishReason() string {
	if c == nil {
		return ""
	}
	for _, choice := range c.Choices {
		if choice.FinishReason != "" {
			return choice.FinishReason
		}
	}
	return ""
}

type ChatCompletionChunk struct {
	ID                string                      `json:"id"`
	Object            string                      `json:"object"`
	CreatedAt         int64                       `json:"created"`
	Model             string                      `json:"model"`
	SystemFingerprint *string                     `json:"system_fingerprint,omitempty"`
	Choices           []ChatCompletionChunkChoice `json:"choices"`
	Usage             *ChatCompletionUsage        `json:"usage,omitempty"`
	ServiceTier       servicetier.Tier            `json:"service_tier,omitempty"`
}

func (sf *ChatCompletionStopField) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		if len(single) > 0 {
			*sf = []string{single}
		}
		return nil
	}

	var multiple []string
	if err := json.Unmarshal(data, &multiple); err == nil {
		*sf = multiple
		return nil
	}

	return &UnmarshalError{
		Field: "stop",
		Msg:   "Must be either a string or an array of string",
	}
}

func (content *ChatMessageContent) UnmarshalJSON(data []byte) error {
	if len(data) == 0 {
		return nil
	}

	switch data[0] {
	case '"':
		var single ContentPartText
		if err := json.Unmarshal(data, &single); err == nil {
			*content = append(*content, single)
			return nil
		}
	case 'n':
		if string(data) == "null" {
			return nil
		}
	case '[':
		if trimmed := bytes.TrimSpace(data[1:]); len(trimmed) > 0 && trimmed[0] == '"' {
			var strs []string
			if err := json.Unmarshal(data, &strs); err == nil {
				result := ptr.Deref(content)
				for _, value := range strs {
					result = append(result, ContentPartText(value))
				}
				*content = result
				return nil
			}
		}

		type union struct {
			Type     ContentPartType      `json:"type"`
			Text     string               `json:"text,omitempty"`
			ImageURL *ContentPartImageURL `json:"image_url,omitempty"`
			Document *ContentPartDocument `json:"document,omitempty"`
		}

		var raw []union
		result := ptr.Deref(content)
		if err := json.Unmarshal(data, &raw); err == nil {
			for _, rawPart := range raw {
				switch rawPart.Type {
				case ContentPartTypeText:
					if rawPart.ImageURL != nil {
						return &UnmarshalError{
							Field: "image_url",
							Msg:   "image_url not supported with content type = text",
						}
					}
					if rawPart.Document != nil {
						return &UnmarshalError{
							Field: "document",
							Msg:   "document not supported with content type = text",
						}
					}
					result = append(result, ContentPartText(rawPart.Text))
				case ContentPartTypeImageURL:
					if rawPart.ImageURL == nil || rawPart.ImageURL.URL == "" {
						return &UnmarshalError{
							Field: "image_url.url",
							Msg:   "no image_url supplied in content of type image_url",
						}
					}
					result = append(result, rawPart.ImageURL)
				case ContentPartTypeDocument:
					if rawPart.Document == nil || rawPart.Document.Data == nil {
						return &UnmarshalError{
							Field: "document.data",
							Msg:   "no document data supplied in content of type document",
						}
					}
					result = append(result, rawPart.Document)
				default:
					return &UnmarshalError{
						Field: "content",
						Msg: fmt.Sprintf(
							"unknown content type %v. Supported content types are: %v",
							rawPart.Type,
							SupportedContentType,
						),
					}
				}
			}
			*content = result
			return nil
		}
	}

	return &UnmarshalError{
		Field: "content",
		Msg:   "Must be either a string or an array of ContentPart",
	}
}

func (tcf *ChatCompletionToolChoiceField) UnmarshalJSON(data []byte) error {
	var selection string
	if err := json.Unmarshal(data, &selection); err == nil {
		tcf.String = &selection
		return nil
	}

	var choice ChatToolChoice
	if err := json.Unmarshal(data, &choice); err == nil {
		tcf.ToolChoice = &choice
		return nil
	}

	return &UnmarshalError{
		Field: "tool_choice",
		Msg:   "Must be either a string or a tool_choice object",
	}
}

func (fcf *ChatCompletionFunctionChoiceField) UnmarshalJSON(data []byte) error {
	var selection string
	if err := json.Unmarshal(data, &selection); err == nil {
		fcf.String = &selection
		return nil
	}

	var choice ChatFunctionChoice
	if err := json.Unmarshal(data, &choice); err == nil {
		fcf.FunctionCall = &choice
		return nil
	}

	return &UnmarshalError{
		Field: "function_choice",
		Msg:   "Must be either a string or a function_choice object",
	}
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Param   string `json:"param"`
	Type    string `json:"type"`
}

type ErrorResponse struct {
	Error Error `json:"error"`
}
