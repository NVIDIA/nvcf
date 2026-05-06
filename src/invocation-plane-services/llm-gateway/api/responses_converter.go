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
	"maps"

	openairesponses "github.com/NVIDIA/nvcf/llm-api-gateway/api/adapters/openairesponses"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/servicetier"
	"github.com/NVIDIA/nvcf/llm-api-gateway/mcp"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
)

func ConvertToChatCompletionRequest(req *openairesponses.CreateRequest) *models.ChatCompletionRequest {
	chatReq := &models.ChatCompletionRequest{
		Model:           req.Model,
		User:            req.GetUserIdentifier(),
		Store:           req.Store,
		ReasoningFormat: ptr.To(models.ReasoningFormatAuto),
	}

	messages := buildMessages(req)
	chatReq.Messages = &messages

	setNumericParameters(req, chatReq)
	setServiceTier(req, chatReq)
	setMaxTokens(req, chatReq)
	setTools(req, chatReq)
	setResponseFormat(req, chatReq)
	setReasoning(req, chatReq)
	setParallelToolCalls(req, chatReq)
	setStream(req, chatReq)

	return chatReq
}

func buildMessages(req *openairesponses.CreateRequest) []models.ChatMessage {
	messages := []models.ChatMessage{}

	if req.Instructions != nil && ptr.Deref(req.Instructions) != "" {
		messages = append(messages, models.ChatMessage{
			Role:    models.ChatCompletionRoleSystem,
			Content: models.SingleTextContent(ptr.Deref(req.Instructions)),
		})
	}

	if len(req.Input.Items) > 0 {
		messages = append(messages, processInputItems(req.Input.Items)...)
	}

	return messages
}

func processInputItems(items []openairesponses.InputItem) []models.ChatMessage {
	messages := []models.ChatMessage{}

	for _, item := range items {
		switch item.Type {
		case openairesponses.ItemTypeMessage, "":
			if item.Role != "" && len(item.MessageContent) > 0 {
				chatContent := convertMessageContentToChatContent(item.MessageContent)
				if len(chatContent) > 0 {
					messages = append(messages, models.ChatMessage{
						Role:    item.Role,
						Content: chatContent,
					})
				}
			}

		case openairesponses.ItemTypeFunctionCall:
			if toolCallData, ok := item.ToolCallData.(openairesponses.FunctionToolCall); ok {
				messages = append(messages, models.ChatMessage{
					Role: models.ChatCompletionRoleAssistant,
					ToolCalls: &[]models.ChatToolCall{
						{
							ID:   toolCallData.CallID,
							Type: models.ToolTypeFunction,
							Function: models.ChatToolCallFunction{
								Name:      toolCallData.Name,
								Arguments: toolCallData.Arguments,
							},
						},
					},
				})
			}

		case openairesponses.ItemTypeFunctionCallOutput:
			if toolCallOutput, ok := item.ToolCallData.(openairesponses.FunctionCallOutput); ok {
				messages = append(messages, models.ChatMessage{
					Role:       models.ChatCompletionRoleTool,
					Content:    models.SingleTextContent(toolCallOutput.Output),
					ToolCallID: &toolCallOutput.CallID,
				})
			}

		case openairesponses.ItemTypeMCPCall:
			if mcpCall, ok := item.ToolCallData.(openairesponses.MCPToolCall); ok {
				messages = append(messages, models.ChatMessage{
					Role: models.ChatCompletionRoleAssistant,
					ToolCalls: &[]models.ChatToolCall{
						{
							ID:   mcpCall.ID,
							Type: models.ToolTypeFunction,
							Function: models.ChatToolCallFunction{
								Name:      mcp.CreateToolNameWithLabel(mcpCall.ServerLabel, mcpCall.Name),
								Arguments: mcpCall.Arguments,
							},
						},
					},
				})

				output := ptr.Deref(mcpCall.Output)
				if output != "" {
					messages = append(messages, models.ChatMessage{
						Role:       models.ChatCompletionRoleTool,
						Content:    models.SingleTextContent(output),
						ToolCallID: &mcpCall.ID,
					})
				}
			}
		}
	}

	return messages
}

func convertMessageContentToChatContent(inputContent []openairesponses.InputContent) models.ChatMessageContent {
	content := models.ChatMessageContent{}

	for _, item := range inputContent {
		switch item.Discriminator() {
		case openairesponses.ContentTypeInputText:
			if textContent, ok := item.GetTextContent(); ok && textContent.Text != "" {
				content = append(content, models.ContentPartText(textContent.Text))
			}
		case openairesponses.ContentTypeOutputText:
			if outputContent, ok := item.GetOutputContent(); ok && outputContent.Text != "" {
				content = append(content, models.ContentPartText(outputContent.Text))
			}
		case openairesponses.ContentTypeRefusal:
			if outputContent, ok := item.GetOutputContent(); ok {
				switch {
				case outputContent.Refusal != "":
					content = append(content, models.ContentPartText(outputContent.Refusal))
				case outputContent.Text != "":
					content = append(content, models.ContentPartText(outputContent.Text))
				}
			}
		case openairesponses.ContentTypeInputImage:
			imageContent, ok := item.GetImageContent()
			if !ok {
				continue
			}

			var url string
			if imageContent.ImageURL != nil {
				url = ptr.Deref(imageContent.ImageURL)
			}

			detail := imageContent.Detail
			if detail == "" {
				detail = "auto"
			}

			content = append(content, &models.ContentPartImageURL{
				URL:    url,
				Detail: detail,
			})
		}
	}

	return content
}

func setNumericParameters(
	req *openairesponses.CreateRequest,
	chatReq *models.ChatCompletionRequest,
) {
	if req.Temperature != nil {
		temp := float32(ptr.Deref(req.Temperature))
		chatReq.Temperature = &temp
	}

	if req.TopP != nil {
		topP := float32(ptr.Deref(req.TopP))
		chatReq.TopP = &topP
	}

	if req.TopLogprobs != nil {
		chatReq.TopLogprobs = ptr.To(uint32(ptr.Deref(req.TopLogprobs)))
	}
}

func setServiceTier(
	req *openairesponses.CreateRequest,
	chatReq *models.ChatCompletionRequest,
) {
	if req.ServiceTier == nil {
		return
	}

	switch ptr.Deref(req.ServiceTier) {
	case "auto", "default":
		chatReq.ServiceTier = servicetier.Auto
	case "on_demand":
		chatReq.ServiceTier = servicetier.OnDemand
	case "flex":
		chatReq.ServiceTier = servicetier.Flex
	case "batch":
		chatReq.ServiceTier = servicetier.Batch
	default:
		chatReq.ServiceTier = servicetier.Auto
	}
}

func setMaxTokens(
	req *openairesponses.CreateRequest,
	chatReq *models.ChatCompletionRequest,
) {
	if req.MaxOutputTokens != nil {
		maxTokens := uint32(ptr.Deref(req.MaxOutputTokens))
		chatReq.MaxCompletionTokens = &maxTokens
	}
}

func setTools(
	req *openairesponses.CreateRequest,
	chatReq *models.ChatCompletionRequest,
) {
	if len(req.Tools) > 0 {
		tools := convertTools(req.Tools)
		if len(tools) > 0 {
			chatReq.Tools = &tools
		}
	}

	if req.ToolChoice != nil {
		setToolChoice(req.ToolChoice, chatReq)
	}
}

func convertTools(reqTools openairesponses.ToolSlice) []models.ChatTool {
	tools := make([]models.ChatTool, 0, len(reqTools))
	for _, tool := range reqTools {
		switch t := tool.(type) {
		case *openairesponses.FunctionTool:
			tools = append(tools, models.ChatTool{
				Type: models.ToolTypeFunction,
				Function: models.ChatFunctionSpec{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  &t.Parameters,
					Strict:      t.Strict,
				},
			})
		case *openairesponses.BrowserSearchTool:
			tools = append(tools, models.ChatTool{
				Type: models.ToolTypeBrowserSearch,
			})
		case *openairesponses.CodeInterpreterTool:
			tools = append(tools, models.ChatTool{
				Type: models.ToolTypeCodeInterpreter,
			})
		case *openairesponses.MCPTool:
			chatTool := models.ChatTool{
				Type:        models.ToolTypeMCP,
				ServerLabel: t.ServerLabel,
				ServerURL:   t.ServerURL,
				ConnectorID: t.ConnectorID,
			}
			if t.Headers != nil {
				chatTool.Headers = maps.Clone(ptr.Deref(t.Headers))
			}
			if t.Authorization != nil {
				chatTool.Authorization = ptr.Deref(t.Authorization)
			}
			if t.RequireApproval != nil {
				if t.RequireApproval.String != nil {
					chatTool.RequireApproval = ptr.Deref(t.RequireApproval.String)
				} else if t.RequireApproval.Object != nil {
					obj := map[string]any{}
					if t.RequireApproval.Object.HasAlways() {
						obj["always"] = map[string]any{}
					}
					if t.RequireApproval.Object.Never != nil {
						obj["never"] = map[string]any{
							"tool_names": t.RequireApproval.Object.Never.ToolNames,
						}
					}
					chatTool.RequireApproval = obj
				}
			}
			if t.AllowedTools != nil {
				switch allowed := t.AllowedTools.(type) {
				case []string:
					chatTool.AllowedTools = allowed
				case []any:
					toolNames := make([]string, 0, len(allowed))
					for _, value := range allowed {
						if name, ok := value.(string); ok {
							toolNames = append(toolNames, name)
						}
					}
					chatTool.AllowedTools = toolNames
				}
			}
			tools = append(tools, chatTool)
		}
	}

	return tools
}

func setToolChoice(
	toolChoice *openairesponses.ToolChoice,
	chatReq *models.ChatCompletionRequest,
) {
	if toolChoice.Name == "" {
		if toolChoice.Type == openairesponses.ToolChoiceNone ||
			toolChoice.Type == openairesponses.ToolChoiceAuto ||
			toolChoice.Type == openairesponses.ToolChoiceRequired {
			chatReq.ToolChoice = models.ChatCompletionToolChoiceField{
				String: &toolChoice.Type,
			}
		}
		return
	}

	if toolChoice.Type == openairesponses.ToolTypeFunction && toolChoice.Name != "" {
		chatReq.ToolChoice = models.ChatCompletionToolChoiceField{
			ToolChoice: &models.ChatToolChoice{
				Type: models.ToolTypeFunction,
				Function: models.ChatFunctionChoice{
					Name: toolChoice.Name,
				},
			},
		}
	}
}

func setResponseFormat(
	req *openairesponses.CreateRequest,
	chatReq *models.ChatCompletionRequest,
) {
	if req.Text == nil || req.Text.Format == nil {
		return
	}

	formatType := models.ChatResponseFormatType(req.Text.Format.Type)
	format := &models.ChatResponseFormat{
		Type: &formatType,
	}

	if req.Text.Format.Type == openairesponses.ResponseFormatTypeJSONSchema {
		format.JSONSchema = &models.JSONSchema{
			Name:        req.Text.Format.Name,
			Description: ptr.Deref(req.Text.Format.Description),
			Schema:      req.Text.Format.Schema,
			Strict:      ptr.Deref(req.Text.Format.Strict),
		}
	}

	chatReq.ResponseFormat = format
}

func setReasoning(
	req *openairesponses.CreateRequest,
	chatReq *models.ChatCompletionRequest,
) {
	if req.Reasoning == nil || req.Reasoning.Effort == nil {
		return
	}

	switch ptr.Deref(req.Reasoning.Effort) {
	case openairesponses.ReasoningEffortLow:
		chatReq.ReasoningEffort = ptr.To(models.ReasoningEffortLow)
	case openairesponses.ReasoningEffortMedium:
		chatReq.ReasoningEffort = ptr.To(models.ReasoningEffortMedium)
	case openairesponses.ReasoningEffortHigh:
		chatReq.ReasoningEffort = ptr.To(models.ReasoningEffortHigh)
	}
}

func setParallelToolCalls(
	req *openairesponses.CreateRequest,
	chatReq *models.ChatCompletionRequest,
) {
	if req.ParallelToolCalls != nil {
		chatReq.ParallelToolCalls = req.ParallelToolCalls
	}
}

func setStream(
	req *openairesponses.CreateRequest,
	chatReq *models.ChatCompletionRequest,
) {
	if req.Stream != nil && ptr.Deref(req.Stream) {
		chatReq.Stream = req.Stream
		chatReq.StreamOptions = &models.ChatCompletionStreamOptions{
			IncludeUsage: ptr.To(true),
		}
	}
}
