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

package prompt

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/nvidia-lpu/minijinja"
	"github.com/nvidia-lpu/parsec/orderedmap"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/encoding/json"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/must"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/pool"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/output"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/tools"
)

var _ TextTemplate = (*Jinja)(nil)

type JinjaParams struct {
	ReasoningConfig            output.ReasoningConfig
	ToolParseConfig            tools.ParseConfig
	FinalRole                  string
	RelaxedMessageOrdering     bool
	PreserveSystemMessageOrder bool
	PreserveSystemContentArray bool
	BOSToken                   string
	EOSToken                   string
	ImageToken                 string
	SupportsSystemPrompts      bool
	SupportsToolsNatively      bool
	PreProcessMessages         func([]models.ChatMessage)
	DropTokens                 []string
	Hash                       string
	PreserveToolContentArray   bool
	OrderToolParameters        bool
}

type Jinja struct {
	template             minijinja.Template
	toolUseTemplate      *minijinja.Template
	params               JinjaParams
	contentPartPool      *pool.Pool[map[string]string]
	contentPartSlicePool *pool.Pool[*[]any]
	toolCallMapPool      *pool.Pool[map[string]any]
}

func NewJinja(
	template minijinja.Template,
	toolUseTemplate *minijinja.Template,
	params JinjaParams,
) (*Jinja, error) {
	return &Jinja{
		template:        template,
		toolUseTemplate: toolUseTemplate,
		params:          params,
		// We intentionally do not clear the map here with a releaser because
		// map storage cannot be pooled the way that e.g. slices can. Instead
		// of clearing the map, we always write the same keys, which allows
		// reusing the underlying hmap buckets.
		contentPartPool: pool.New(func() map[string]string {
			return map[string]string{
				"type": "",
				"text": "",
			}
		}),
		contentPartSlicePool: pool.NewWithReleaser(
			func() *[]any {
				return ptr.To(make([]any, 0))
			},
			func(x *[]any) {
				*x = (*x)[:0]
			},
		),
		// We intentionally do not clear the map here with a releaser because
		// map storage cannot be pooled the way that e.g. slices can. Instead
		// of clearing the map, we always write the same keys, which allows
		// reusing the underlying hmap buckets.
		toolCallMapPool: pool.New(func() map[string]any {
			return map[string]any{
				"type": nil,
				"id":   nil,
				"function": map[string]any{
					"name":      nil,
					"arguments": nil,
				},
			}
		}),
	}, nil
}

func (j *Jinja) Params() *JinjaParams {
	return &j.params
}

func (j *Jinja) ReasoningConfig() output.ReasoningConfig {
	return j.params.ReasoningConfig
}

func (j *Jinja) ToolParseConfig() tools.ParseConfig {
	return j.params.ToolParseConfig
}

func (j *Jinja) GetForcedToolUsePrefix(
	_ *models.ChatCompletionToolChoiceField,
) string {
	return ""
}

func (j *Jinja) DropTokens() []string {
	return j.params.DropTokens
}

func orderToolParameters(params any) any {
	if params == nil {
		return nil
	}

	typed, ok := params.(map[string]any)
	if !ok {
		return wrapOrderedMapsRecursive(params)
	}

	var (
		ordered = orderedmap.New()
		seen    = make(map[string]bool)
		add     = func(key string) {
			if v, ok := typed[key]; ok {
				ordered.Set(key, wrapOrderedMapsRecursive(v))
				seen[key] = true
			}
		}
	)

	add("type")
	add("properties")
	add("required")
	add("responses")
	add("description")
	add("items")

	// Add remaining keys in sorted order
	keys := make([]string, 0, len(typed))
	for k := range typed {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		ordered.Set(k, wrapOrderedMapsRecursive(typed[k]))
	}

	return WrapOrderedMap(ordered)
}

func (j *Jinja) RenderText(
	msgs []models.ChatMessage,
	params tools.Params,
) (Prompt, error) {
	// TODO(mway): Add jinja type encoder interface (to convert structs to C
	//             objects) in order to avoid excessive map use
	var (
		jsonTools []map[string]any
		parsecfg  = j.ToolParseConfig()
		prefill   string
	)
	if j.params.SupportsToolsNatively {
		if len(params.Tools) > 0 {
			prefix := j.GetForcedToolUsePrefix(&params.ToolChoice)
			if prefix != "" {
				msgs = append(msgs, models.ChatMessage{
					Role:    models.ChatCompletionRoleAssistant,
					Content: models.SingleTextContent(prefix),
				})
			}

			jsonTools = make([]map[string]any, 0, len(params.Tools))
			for _, tool := range params.Tools {
				var parameters any
				if j.params.OrderToolParameters {
					parameters = orderToolParameters(tool.Function.Parameters)
				} else {
					parameters = wrapOrderedMapsRecursive(tool.Function.Parameters)
				}

				jsonTools = append(jsonTools, map[string]any{
					"type": tool.Type,
					"function": map[string]any{
						"name":        tool.Function.Name,
						"description": ptr.Deref(tool.Function.Description),
						"parameters":  parameters,
					},
				})
			}
		}
	} else {
		var err error
		msgs, err = tools.HandleGenericToolCalling(
			msgs,
			params,
			parsecfg.ToolUseBeginMarker(),
			parsecfg.ToolUseEndMarker(),
		)
		if err != nil {
			return nil, err
		}
	}

	finalMsg := msgs[len(msgs)-1]
	// If the final message is an assistant message with only content, it is a prefill
	// We are not prefilling reasoning models as it's not clear how to mix reasoning and prefilling
	isPrefill := finalMsg.Role == models.ChatCompletionRoleAssistant &&
		finalMsg.ToolCalls == nil &&
		finalMsg.FunctionCall == nil &&
		j.params.FinalRole == models.ChatCompletionRoleUser &&
		j.ReasoningConfig() == nil
	if isPrefill {
		// To prefill we will:
		// 1. Remove the final assistant message before applying the HF template
		// 2. Jinja will template as normal which will end with a normal generation prompt
		// 3. We will append the prefill after the generation prompt
		prefill = strings.TrimSpace(finalMsg.Content.MustSingleText())
		msgs = msgs[:len(msgs)-1]
	}

	var (
		normalized = NormalizeMessages(
			msgs,
			!j.params.RelaxedMessageOrdering,
			j.params.SupportsSystemPrompts,
			j.params.FinalRole,
			j.params.PreserveSystemMessageOrder,
		)
		formatted       = make([]map[string]any, 0, len(normalized))
		functionCallIDs = make(map[string][]string, 0)
		imageToken      = j.params.ImageToken
		images          []*models.ContentPartImageURL
	)

	if j.params.PreProcessMessages != nil {
		j.params.PreProcessMessages(normalized)
	}

	// Convert models.ChatMessage to the format expected by the jinja template
	for msgIdx, msg := range normalized {
		message := map[string]any{
			"role": msg.Role,
		}

		var (
			noImages      = msg.Content.HasNoMultimodalContent()
			preserveArray = j.params.PreserveToolContentArray &&
				msg.Role == models.ChatCompletionRoleTool &&
				len(msg.Content) > 1
		)

		if j.params.PreserveSystemContentArray &&
			msgIdx == 0 &&
			msg.Role == models.ChatCompletionRoleSystem {
			preserveArray = true
		}

		switch {
		case len(msg.Content) == 0:
			message["content"] = ""

		case noImages && !preserveArray:
			content := msg.Content.MustSingleText()

			if len(imageToken) > 0 && strings.Contains(content, imageToken) {
				return nil, fmt.Errorf(
					"messages must not contain the image token: %s",
					imageToken,
				)
			}
			message["content"] = content

		default:
			// Preserve array structure for multimodal content or tool messages with PreserveToolContentArray
			content := j.contentPartSlicePool.Get()
			defer j.contentPartSlicePool.Put(content) //nolint:gocritic // deferInLoop

			preserveStructuredSystem := preserveArray &&
				j.params.PreserveSystemContentArray &&
				msgIdx == 0 &&
				msg.Role == models.ChatCompletionRoleSystem

			if preserveStructuredSystem {
				var builder strings.Builder
				builder.WriteByte('[')
				for idx, part := range msg.Content {
					if idx > 0 {
						builder.WriteString(", ")
					}
					switch c := part.(type) {
					case models.ContentPartText:
						escaped, err := json.Marshal(c.String())
						if err != nil {
							return nil, fmt.Errorf(
								"failed to marshal system content text: %w",
								err,
							)
						}
						builder.WriteString(`{"type": "text", "text": `)
						builder.Write(escaped)
						builder.WriteByte('}')
					default:
						return nil, fmt.Errorf(
							"unsupported content type in structured system message: %s",
							part.ContentType(),
						)
					}
				}
				builder.WriteByte(']')
				message["content"] = builder.String()
				break
			}

			for _, part := range msg.Content {
				contentPart := j.contentPartPool.Get()
				defer j.contentPartPool.Put(contentPart) //nolint:gocritic // deferInLoop

				switch c := part.(type) {
				case models.ContentPartText:
					if len(imageToken) > 0 && strings.Contains(c.String(), imageToken) {
						return nil, fmt.Errorf(
							"messages must not contain the image token: %s",
							imageToken,
						)
					}
					contentPart["type"] = "text"
					contentPart["text"] = c.String()
					*content = append(*content, contentPart)
				case *models.ContentPartImageURL:
					contentPart["type"] = "image"
					contentPart["text"] = ""
					*content = append(*content, contentPart)
					images = append(images, c)
				case *models.ContentPartDocument:
					contentPart["type"] = "document"
					documentDataJSON, err := json.Marshal(c.Data)
					if err != nil {
						return nil, fmt.Errorf("failed to marshal document data: %w", err)
					}
					// re-use the "text" field to avoid changing the pooled object
					// in a way that requires cleaning up after use.
					contentPart["text"] = string(documentDataJSON)
					*content = append(*content, contentPart)
				}
			}
			message["content"] = ptr.Deref(content)
		}

		var toolCalls []map[string]any
		switch msg.Role {
		case models.ChatCompletionRoleAssistant:
			if msg.Reasoning != nil {
				message["reasoning_content"] = ptr.Deref(msg.Reasoning)
			}
			if msg.ToolCalls != nil {
				for i := range len(*msg.ToolCalls) {
					call := j.toolCallMapPool.Get()
					defer j.toolCallMapPool.Put(call) //nolint:gocritic // deferInLoop

					call["type"] = "function"
					call["id"] = (*msg.ToolCalls)[i].ID

					fn := must.As[map[string]any](call["function"])
					fn["name"] = (*msg.ToolCalls)[i].Function.Name
					var args map[string]any
					err := json.UnmarshalString((*msg.ToolCalls)[i].Function.Arguments, &args)
					if err != nil {
						return nil, fmt.Errorf(
							"invalid JSON in messages[%d].tool_calls[%d].function.arguments: %w",
							msgIdx,
							i,
							err,
						)
					}
					fn["arguments"] = args
					toolCalls = append(toolCalls, call)
				}
			} else if msg.FunctionCall != nil {
				// Hugging face only supports tools. Convert the provided
				// function calls to tool calls.
				functionCallID := j.ToolParseConfig().GenerateToolCallID()
				// Save function call id for use in Function responses.
				functionCallIDs[msg.FunctionCall.Name] = append(
					functionCallIDs[msg.FunctionCall.Name],
					functionCallID,
				)

				call := j.toolCallMapPool.Get()
				defer j.toolCallMapPool.Put(call) //nolint:gocritic // deferInLoop

				call["type"] = "function"
				call["id"] = functionCallID

				fn := must.As[map[string]any](call["function"])
				fn["name"] = msg.FunctionCall.Name
				var args map[string]any
				err := json.UnmarshalString(msg.FunctionCall.Arguments, &args)
				if err != nil {
					return nil, fmt.Errorf("invalid JSON in messages[%d].function_call.arguments: %w", msgIdx, err)
				}
				fn["arguments"] = args

				toolCalls = append(toolCalls, call)
			}
		case models.ChatCompletionRoleTool:
			message["tool_call_id"] = ptr.Deref(msg.ToolCallID)
		case models.ChatCompletionRoleFunction:
			message["role"] = models.ChatCompletionRoleTool
			var toolCallID string
			toolCallIDs := functionCallIDs[*msg.Name]
			if len(toolCallIDs) == 0 {
				toolCallID = j.ToolParseConfig().GenerateToolCallID()
			} else {
				toolCallID = toolCallIDs[0]
				functionCallIDs[*msg.Name] = toolCallIDs[1:]
			}
			message["tool_call_id"] = toolCallID
		}
		message["tool_calls"] = toolCalls
		formatted = append(formatted, message)
	}
	if len(images) > 0 && len(imageToken) == 0 {
		panic("template not configured to support images")
	}

	data := map[string]any{
		"bos_token":             j.params.BOSToken,
		"eos_token":             j.params.EOSToken,
		"add_generation_prompt": true,
		"messages":              formatted,
		"tools":                 jsonTools,
	}
	if params.EnableThinking != nil {
		data["enable_thinking"] = ptr.Deref(params.EnableThinking)
	}
	if len(params.Documents) > 0 {
		data["documents"] = params.Documents
	}

	if params.EnableCitations {
		data["enable_citations"] = true
	}

	template := &j.template
	if j.toolUseTemplate != nil &&
		ptr.Deref(params.ToolChoice.String) != models.ChatToolSelectionNone {
		template = j.toolUseTemplate
	}

	rendered, err := template.Render(data)
	if prefill != "" {
		rendered = rendered + prefill
	}
	switch {
	case err != nil:
		return nil, err
	case len(images) == 0:
		return WrapText(rendered), nil
	default:
		// has images; below
	}

	// The prompt is long string at this point. We need to reformat this into a
	// list of text segments and image bytes. To do this, we:
	//
	//   1. Split the prompt at the next image token
	//   2. Append all text prior to the image token to the return value
	//   3. Append the image with the actual bytes to the return value
	//   4. Repeat steps 1-3 with the text after the image until all images
	//      have been found
	//
	// This logic assumes that images will be placed in the prompt in the same
	// order as in the chat messages.
	var (
		// n.b. Pessimistic allocation in which images are fully interleaved
		//      (i.e., none are contiguous) and so should be treated as
		//      fenceposts. For example:
		//        <text><image><text><image><text>
		//      would result in a prompt of len=5 (2*num_images+1).
		result = make(Prompt, 0, len(images)*2+1)
		before string
		found  bool
	)
	for _, image := range images {
		before, rendered, found = strings.Cut(rendered, imageToken)
		if !found {
			panic("image token not found")
		}
		if before != "" {
			result = append(result, models.ContentPartText(before))
		}
		result = append(result, image)
	}

	if rendered != "" {
		// Sanity check that there aren't any additional image tokens
		index := strings.Index(rendered, imageToken)
		if index != -1 {
			return nil, fmt.Errorf(
				"messages must not contain the image token: %s",
				imageToken,
			)
		}
		result = append(result, models.ContentPartText(rendered))
	}

	return result, nil
}

func (j *Jinja) Version() string {
	return j.params.Hash
}

// HashTemplate returns the SHA-256 hash for the provided template string.
func HashTemplate(template string) string {
	sum := sha256.Sum256([]byte(template))
	return hex.EncodeToString(sum[:])
}
