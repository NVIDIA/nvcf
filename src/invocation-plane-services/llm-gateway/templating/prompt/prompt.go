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
	"bytes"
	"io"
	"slices"
	"unsafe"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/pool"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/token"
)

var _normalizeMessagesBufferPool = pool.NewWithReleaser(
	func() *bytes.Buffer {
		return bytes.NewBuffer(make([]byte, 0, 128))
	},
	func(x *bytes.Buffer) {
		x.Reset()
	},
)

type Prompt []models.ContentPart

func WrapText(prompt string) Prompt {
	return Prompt{models.ContentPartText(prompt)}
}

func WrapTokens(tokens []uint32) Prompt {
	return Prompt{token.ContentTokens(tokens)}
}

func NewEmptyUserMessage() models.ChatMessage {
	return models.ChatMessage{
		Role:    models.ChatCompletionRoleUser,
		Content: models.SingleTextContent(""),
	}
}

func NewEmptyAssistantMessage() models.ChatMessage {
	return models.ChatMessage{
		Role:    models.ChatCompletionRoleAssistant,
		Content: models.SingleTextContent(""),
	}
}

// NormalizeMessages normalizes chat messages to account for system prompts
// regardless of model capability. The pad parameter controls whether empty
// messages should be added to enforce back and forth between the user and
// assistant.
//
// Note: if the model does not support system prompts, we place the system
// prompt at the beginning of the first user message.
//
// The preserveSystemMessageOrder parameter controls whether system messages
// should be kept in their original sequential order (true) or grouped at the
// beginning (false). This is useful for models like ALLAM that expect system
// messages to remain in their original positions in the conversation flow.
func NormalizeMessages(
	messages []models.ChatMessage,
	pad bool,
	supportsSystemPrompt bool,
	finalRole string,
	preserveSystemMessageOrder bool,
) []models.ChatMessage {
	var result []models.ChatMessage
	if preserveSystemMessageOrder {
		result = normalizeMessagesPreserveOrder(messages, pad)
	} else {
		result = normalizeMessagesReorder(messages, pad, supportsSystemPrompt)
	}

	nres := len(result)
	if finalRole != "" && (nres == 0 || result[nres-1].Role != finalRole) {
		var role string
		if len(result) > 0 {
			role = result[len(result)-1].Role
		}

		switch finalRole {
		case "":
			break
		case models.ChatCompletionRoleUser:
			if len(role) == 0 || role == models.ChatCompletionRoleAssistant {
				result = append(result, NewEmptyUserMessage())
			}
		case models.ChatCompletionRoleAssistant:
			if len(role) == 0 || role != models.ChatCompletionRoleAssistant {
				result = append(result, NewEmptyAssistantMessage())
			}
		default:
			panic("Unsupported final role")
		}
	}

	return result
}

func normalizeMessagesPreserveOrder(
	messages []models.ChatMessage,
	pad bool,
) []models.ChatMessage {
	var (
		result   = make([]models.ChatMessage, 0, len(messages))
		prevRole = models.ChatCompletionRoleAssistant
	)

	for _, m := range messages {
		if pad && m.Role != models.ChatCompletionRoleSystem {
			switch m.Role {
			case models.ChatCompletionRoleUser:
				if prevRole == models.ChatCompletionRoleUser {
					result = append(result, NewEmptyAssistantMessage())
				}
			case models.ChatCompletionRoleAssistant:
				if prevRole == models.ChatCompletionRoleAssistant {
					result = append(result, NewEmptyUserMessage())
				}
			default:
			}
			prevRole = m.Role
		}
		result = append(result, m)
	}

	return result
}

func normalizeMessagesReorder(
	messages []models.ChatMessage,
	pad bool,
	supportsSystemPrompt bool,
) []models.ChatMessage {
	var (
		result   = make([]models.ChatMessage, 0, len(messages))
		prevRole = models.ChatCompletionRoleAssistant
	)

	buf := _normalizeMessagesBufferPool.Get()
	defer _normalizeMessagesBufferPool.Put(buf)

	for _, m := range messages {
		if m.Role == models.ChatCompletionRoleSystem {
			if buf.Len() > 0 {
				buf.WriteByte('\n')
			}
			buf.WriteString(m.Content.MustSingleText())
		} else {
			if pad {
				switch m.Role {
				case models.ChatCompletionRoleUser:
					if prevRole == models.ChatCompletionRoleUser {
						result = append(result, NewEmptyAssistantMessage())
					}
				case models.ChatCompletionRoleAssistant:
					if prevRole == models.ChatCompletionRoleAssistant {
						result = append(result, NewEmptyUserMessage())
					}
				default:
				}
				prevRole = m.Role
			}
			result = append(result, m)
		}
	}

	if buf.Len() > 0 {
		if supportsSystemPrompt {
			result = append(result, models.ChatMessage{})
			copy(result[1:], result)
			result[0] = models.ChatMessage{
				Role:    models.ChatCompletionRoleSystem,
				Content: models.SingleTextContent(buf.String()),
			}
		} else {
			if len(result) > 0 {
				if result[0].Role == models.ChatCompletionRoleUser {
					content := slices.Clone(result[0].Content)
					switch c := content[0].(type) {
					case models.ContentPartText:
						if str := c.String(); len(str) > 0 {
							buf.WriteByte(' ')
							buf.WriteString(str)
						}

						content[0] = models.ContentPartText(buf.String())
					case *models.ContentPartImageURL:
						content = append(content, nil)
						copy(content[1:], content)
						content[0] = models.ContentPartText(buf.String())
					}
					result[0].Content = content
				} else {
					result = append(result, models.ChatMessage{})
					copy(result[1:], result)
					result[0] = models.ChatMessage{
						Role:    models.ChatCompletionRoleUser,
						Content: models.SingleTextContent(buf.String()),
					}
				}
			} else {
				result = []models.ChatMessage{
					{
						Role:    models.ChatCompletionRoleUser,
						Content: models.SingleTextContent(buf.String()),
					},
				}
			}
		}
	}

	return result
}

func writeStrings(dst io.Writer, strs ...string) {
	if w, ok := dst.(io.StringWriter); ok {
		for _, str := range strs {
			w.WriteString(str) //nolint:errcheck
		}
		return
	}

	for _, str := range strs {
		//nolint:errcheck
		dst.Write(unsafe.Slice(unsafe.StringData(str), len(str)))
	}
}
