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
	"errors"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/container/set"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/encoding/json"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/pool"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/tools"
)

const _llama3KnowledgeCutoff = "December 2023"

var (
	_toolRoles = set.New(
		models.ChatCompletionRoleTool,
		models.ChatCompletionRoleFunction,
	)

	_ TextTemplate = (*Llama31)(nil)
	_ TextTemplate = (*Llama32)(nil)
	_ TextTemplate = (*Quattro4)(nil)
)

type Llama31 struct {
	tools.NativeParseConfig

	StartToolHeader string
	KnowledgeCutoff string

	bufferedEncoderPool *json.BufferedEncoderPool
	bufferPool          *pool.Pool[*bytes.Buffer]
}

func NewLlama31() *Llama31 {
	return applyLlama31Defaults(&Llama31{
		NativeParseConfig: tools.NativeParseConfig{
			NativeToolUseFamily: tools.Llama31ToolUseFamily,
			ToolUseBeginToken:   `<function=`,
			ToolUseEndToken:     "</function>",
			ToolNoun:            "function",
			// No BOS token since that's added at the tokenizer level.
			HeaderBeginToken: "<|start_header_id|>",
			HeaderEndToken:   "<|end_header_id|>\n\n",
			EndOfTurnToken:   "<|eot_id|>",
			EndToolCallToken: "</function>",
			ToolNameEndToken: ">",
		},
		StartToolHeader: "<|start_header_id|>ipython<|end_header_id|>\n\n",
		KnowledgeCutoff: _llama3KnowledgeCutoff,
	})
}

func NewQuattro() *Llama31 {
	return applyLlama31Defaults(&Llama31{
		NativeParseConfig: tools.NativeParseConfig{
			NativeToolUseFamily: tools.Llama4ToolUseFamily,
			ToolUseBeginToken:   `<function=`,
			ToolUseEndToken:     "</function>",
			ToolNoun:            "function",
			// No BOS token since that's added at the tokenizer level.
			HeaderBeginToken: "<|header_start|>",
			HeaderEndToken:   "<|header_end|>\n\n",
			EndOfTurnToken:   "<|eot|>",
			EndToolCallToken: "</function>",
			ToolNameEndToken: ">",
		},
		StartToolHeader: "<|header_start|>ipython<|header_end|>\n\n",
	})
}

func (l *Llama31) GetForcedToolUsePrefix(
	choice *models.ChatCompletionToolChoiceField,
) string {
	switch {
	case l.NativeToolUseFamily == tools.Llama4ToolUseFamily:
		return ""
	case l.ReasoningBeginToken != "":
		// If we prefill a tool call, we conflict with <think> tokens
		return ""
	case choice.ToolChoice != nil:
		return l.ToolUseBeginToken + l.ToolNameBeginToken +
			choice.ToolChoice.Function.Name + l.ToolNameEndToken
	case ptr.Eq(choice.String, models.ChatToolSelectionRequired):
		return l.ToolUseBeginToken
	default:
		return ""
	}
}

func (*Llama31) DropTokens() []string {
	return nil
}

func (l *Llama31) Version() string { return "v1" }

func (l *Llama31) RenderText(
	messages []models.ChatMessage,
	params tools.Params,
) (Prompt, error) {
	textbuf := l.bufferPool.Get()
	defer l.bufferPool.Put(textbuf)

	systembuf := l.bufferPool.Get()
	defer l.bufferPool.Put(systembuf)

	forcedInstr, toolsEnabled := l.GetForcedUseInstruction(
		params.ToolChoice,
	)

	toolsbuf := l.bufferPool.Get()
	defer l.bufferPool.Put(toolsbuf)

	// Prefix the date prompt even when no tools are provided
	if len(l.KnowledgeCutoff) > 0 {
		toolsbuf.WriteString(`Cutting Knowledge Date: `)
		toolsbuf.WriteString(l.KnowledgeCutoff)
		toolsbuf.WriteString("\nToday Date: ")
		toolsbuf.WriteString(time.Now().Format("02 January 2006"))
		toolsbuf.WriteString("\n\n")
	}

	if len(params.Tools) > 0 {
		if toolsEnabled {
			// Leading newline is intentional
			toolsbuf.WriteString(`
You have access to the following functions:

`)

			enc := l.bufferedEncoderPool.Get()
			defer l.bufferedEncoderPool.Put(enc)

			for _, tool := range params.Tools {
				if err := enc.Encode(tool.Function); err != nil {
					return nil, err
				}

				writeStrings(
					toolsbuf,
					"Use the function '",
					tool.Function.Name,
					"' to '",
					ptr.Deref(tool.Function.Description),
					"'\n",
				)

				enc.Buffer().WriteTo(toolsbuf) //nolint:errcheck
				toolsbuf.WriteByte('\n')
			}

			inst := "\n- Only call one function at a time"
			if params.ParallelToolCalls {
				inst = "\n- You can make multiple function calls, but put each function call on a separate line"
			}

			// Leading newline is intentional
			toolsbuf.WriteString(`
Think very carefully before calling functions.
If you choose to call a function ONLY reply in the following format with no prefix or suffix:

<function=example_function_name>{"example_name": "example_value"}</function>

Reminder:
- If looking for real time information use relevant functions before falling back to brave_search
- Function calls MUST follow the specified format, start with <function= and end with </function>
- Required parameters MUST be specified` + inst + `
- Put the entire function call reply on one line

`)
		}
		if forcedInstr != "" {
			toolsbuf.WriteString(forcedInstr)
			toolsbuf.WriteString("\n\n")
		}
	}

	// Add the assistant preamble
	forcedToolPrompt := l.GetForcedToolUsePrefix(&params.ToolChoice)
	if len(forcedToolPrompt) > 0 ||
		messages[len(messages)-1].Role != models.ChatCompletionRoleAssistant {
		messages = append(
			messages,
			models.ChatMessage{
				Role:    models.ChatCompletionRoleAssistant,
				Content: models.SingleTextContent(forcedToolPrompt),
			},
		)
	}

	var prompt Prompt
	for i, message := range messages {
		switch message.Role {
		case models.ChatCompletionRoleSystem:
			systembuf.WriteString(message.Content.MustSingleText())
			systembuf.WriteByte('\n')
			continue

		case models.ChatCompletionRoleUser:
			l.WriteRoleHeader(textbuf, "user")
			for _, content := range message.Content {
				switch c := content.(type) {
				case *models.ContentPartImageURL:
					if textbuf.Len() > 0 {
						prompt = append(prompt, models.ContentPartText(textbuf.String()))
						textbuf.Reset()
					}
					prompt = append(prompt, c)
				case models.ContentPartText:
					textbuf.WriteString(c.String())
				}
			}
			textbuf.WriteString(l.EndOfTurnToken)
		case models.ChatCompletionRoleAssistant:
			l.WriteRoleHeader(textbuf, "assistant")
			switch {
			case message.ToolCalls != nil && len(*message.ToolCalls) > 0:
				for _, toolCall := range *message.ToolCalls {
					writeStrings(
						textbuf,
						l.ToolUseBeginToken,
						toolCall.Function.Name,
						l.ToolNameEndToken,
						toolCall.Function.Arguments,
						l.EndToolCallToken,
					)
					if toolCall != (*message.ToolCalls)[len(*message.ToolCalls)-1] {
						textbuf.WriteByte('\n')
					}
				}
			case message.FunctionCall != nil:
				writeStrings(
					textbuf,
					l.ToolUseBeginToken,
					message.FunctionCall.Name,
					l.ToolNameEndToken,
					message.FunctionCall.Arguments,
					l.EndToolCallToken,
				)
			default:
				textbuf.WriteString(message.Content.MustSingleText())
			}
			if i < len(messages)-1 {
				textbuf.WriteString(l.EndOfTurnToken)
			}
		case models.ChatCompletionRoleFunction, models.ChatCompletionRoleTool:
			// Llama 3.1 doesn't use tool call ID information
			writeStrings(
				textbuf,
				l.StartToolHeader,
				message.Content.MustSingleText(),
				l.EndOfTurnToken,
			)
		}
	}

	systemPrompt := l.GenerateSysPrompt(systembuf.String(), toolsbuf.String())
	if len(prompt) == 0 { // single modal prompt
		return WrapText(systemPrompt + textbuf.String()), nil
	}

	prompt = append(WrapText(systemPrompt), prompt...)
	return append(prompt, models.ContentPartText(textbuf.String())), nil
}

type Llama32 struct {
	Llama31
}

func (l *Llama32) Version() string { return "v1" }

func NewLlama32() *Llama32 {
	return &Llama32{
		Llama31: *applyLlama31Defaults(NewLlama31()),
	}
}

func (l *Llama32) RenderText(
	messages []models.ChatMessage,
	params tools.Params,
) (Prompt, error) {
	const imageTag = "<|image|>"

	var (
		hasImages    bool
		hasSysPrompt bool
	)
	for _, m := range messages {
		switch m.Role {
		case models.ChatCompletionRoleSystem:
			hasSysPrompt = true
		case models.ChatCompletionRoleUser:
			for _, c := range m.Content {
				_, isImage := c.(*models.ContentPartImageURL)
				if isImage {
					hasImages = true
				}
			}
		}
	}

	if hasImages && hasSysPrompt {
		return nil, errors.New(
			"prompting with images is incompatible with system messages",
		)
	}

	forcedInstr, toolsEnabled := l.GetForcedUseInstruction(
		params.ToolChoice,
	)

	toolsbuf := l.bufferPool.Get()
	defer l.bufferPool.Put(toolsbuf)

	if !hasImages {
		toolsbuf.WriteString("Cutting Knowledge Date: ")
		toolsbuf.WriteString(_llama3KnowledgeCutoff)
		toolsbuf.WriteString("\nToday Date: ")
		toolsbuf.WriteString(time.Now().Format("02 January 2006"))
		toolsbuf.WriteString("\n\n")
	}

	enc := l.bufferedEncoderPool.Get()
	defer l.bufferedEncoderPool.Put(enc)

	if len(params.Tools) > 0 {
		if toolsEnabled {
			// Leading newline is intentional
			toolsbuf.WriteString("\nYou have access to the following functions:\n\n")

			for _, tool := range params.Tools {
				if err := enc.Encode(tool.Function); err != nil {
					return nil, err
				}

				writeStrings(
					toolsbuf,
					"Use the function '",
					tool.Function.Name,
					"' to '",
					ptr.Deref(tool.Function.Description),
					"'\n",
				)
				enc.Buffer().WriteTo(toolsbuf) //nolint:errcheck
				enc.Buffer().Reset()
				toolsbuf.WriteByte('\n')
			}

			singleToolInstruction := "\n- Only call one function at a time"
			if params.ParallelToolCalls {
				singleToolInstruction = "\n- You can make multiple function calls, " +
					"but put each function call on a separate line"
			}

			// Leading newline is intentional
			toolsbuf.WriteString(`
Think very carefully before calling functions.
If you choose to call a function ONLY reply in the following format with no prefix or suffix:

<function=example_function_name>{"example_name": "example_value"}</function>

Reminder:
- If looking for real time information use relevant functions before falling back to brave_search
- Function calls MUST follow the specified format, start with <function= and end with </function>
- Required parameters MUST be specified` + singleToolInstruction + `
- Put the entire function call reply on one line

`)
		}
		if forcedInstr != "" {
			toolsbuf.WriteString(forcedInstr)
			toolsbuf.WriteString("\n\n")
		}
	}

	// Add the assistant preamble
	forcedToolPrompt := l.GetForcedToolUsePrefix(&params.ToolChoice)
	if len(forcedToolPrompt) > 0 ||
		messages[len(messages)-1].Role != models.ChatCompletionRoleAssistant {
		messages = append(
			messages,
			models.ChatMessage{
				Role:    models.ChatCompletionRoleAssistant,
				Content: models.SingleTextContent(forcedToolPrompt),
			},
		)
	}

	// Since llama 3.2 does not support images and system messages, we must put
	// the tool instructions in a user message if an image is present.
	if hasImages {
		if messages[0].Role != models.ChatCompletionRoleUser {
			messages = append([]models.ChatMessage{{
				Role:    models.ChatCompletionRoleUser,
				Content: models.SingleTextContent(toolsbuf.String()),
			}}, messages...)
		} else {
			messages[0].Content = append(
				models.SingleTextContent(toolsbuf.String()),
				messages[0].Content...,
			)
		}
		toolsbuf.Reset()
	}

	textbuf := l.bufferPool.Get()
	defer l.bufferPool.Put(textbuf)

	systembuf := l.bufferPool.Get()
	defer l.bufferPool.Put(systembuf)

	var prompt Prompt
	for i, msg := range messages {
		switch msg.Role {
		case models.ChatCompletionRoleSystem:
			systembuf.WriteString(msg.Content.MustSingleText())
			systembuf.WriteByte('\n')
			continue

		case models.ChatCompletionRoleUser:
			l.WriteRoleHeader(textbuf, "user")
			// Images must precede text
			for _, content := range msg.Content {
				if c, ok := content.(*models.ContentPartImageURL); ok {
					if textbuf.Len() > 0 {
						prompt = append(
							prompt,
							models.ContentPartText(textbuf.String()),
						)
						textbuf.Reset()
					}
					prompt = append(prompt, c) // TODO(mway): Normalize all of these
				}
			}
			for _, content := range msg.Content {
				if c, ok := content.(models.ContentPartText); ok {
					textbuf.WriteString(c.String())
				}
			}
			textbuf.WriteString(l.EndOfTurnToken)
		case models.ChatCompletionRoleAssistant:
			l.WriteRoleHeader(textbuf, "assistant")
			switch {
			case msg.ToolCalls != nil && len(*msg.ToolCalls) > 0:
				for _, toolCall := range *msg.ToolCalls {
					writeStrings(
						textbuf,
						l.ToolUseBeginToken,
						toolCall.Function.Name,
						l.ToolNameEndToken,
						toolCall.Function.Arguments,
						l.EndToolCallToken,
					)
					if toolCall != (*msg.ToolCalls)[len(*msg.ToolCalls)-1] {
						textbuf.WriteByte('\n')
					}
				}
			case msg.FunctionCall != nil:
				writeStrings(
					textbuf,
					l.ToolUseBeginToken,
					msg.FunctionCall.Name,
					l.ToolNameEndToken,
					msg.FunctionCall.Arguments,
					l.EndToolCallToken,
				)
			default:
				textbuf.WriteString(msg.Content.MustSingleText())
			}
			if i < len(messages)-1 {
				textbuf.WriteString(l.EndOfTurnToken)
			}
		case models.ChatCompletionRoleFunction, models.ChatCompletionRoleTool:
			// Llama 3.2 doesn't use tool call ID information
			writeStrings(
				textbuf,
				l.StartToolHeader,
				msg.Content.MustSingleText(),
				l.EndOfTurnToken,
			)
		}
	}

	systemPrompt := l.GenerateSysPrompt(systembuf.String(), toolsbuf.String())
	if len(prompt) == 0 { // single modal prompt
		prompt = WrapText(systemPrompt + textbuf.String())
	} else {
		if len(systemPrompt) > 0 {
			prompt = append(WrapText(systemPrompt), prompt...)
		}
		prompt = append(prompt, models.ContentPartText(textbuf.String()))
	}
	for _, content := range prompt {
		if text, ok := content.(models.ContentPartText); ok {
			if strings.Contains(text.String(), imageTag) {
				return nil, errors.New(
					"messages must not contain the image token: " + imageTag,
				)
			}
		}
	}
	return prompt, nil
}

type Quattro4 struct {
	Llama31
}

func NewQuattro4() *Quattro4 {
	return &Quattro4{
		Llama31: *applyLlama31Defaults(&Llama31{
			NativeParseConfig: tools.NativeParseConfig{
				NativeToolUseFamily: tools.Llama4ToolUseFamily,
				ToolUseBeginToken:   `[`,
				ToolUseEndToken:     `]`,
				ToolNoun:            "function",
				// No BOS token since that's added at the tokenizer level.
				HeaderBeginToken: "<|header_start|>",
				HeaderEndToken:   "<|header_end|>\n\n",
				EndOfTurnToken:   "<|eot|>",
				EndToolCallToken: "", // No end marker for JSON array format
				ToolNameEndToken: "", // Not used with JSON array format
			},
			StartToolHeader: "<|header_start|>ipython<|header_end|>\n\n",
		}),
	}
}

func (q *Quattro4) DropTokens() []string {
	return []string{
		"<|python_start|>",
		"<|python_end|>",
	}
}

func (q *Quattro4) Version() string { return "v1" }

func (q *Quattro4) RenderText(
	messages []models.ChatMessage,
	params tools.Params,
) (Prompt, error) {
	forcedToolUseSystemPromptInstruction, toolCallingEnabled := q.GetForcedUseInstruction(
		params.ToolChoice,
	)

	toolsbuf := q.bufferPool.Get()
	defer q.bufferPool.Put(toolsbuf)

	if len(params.Tools) > 0 && toolCallingEnabled {
		// Build the new system prompt for Llama 4
		toolsbuf.WriteString(
			`You are a helpful assistant and an expert in function composition. You can answer general questions using your internal knowledge OR invoke functions when necessary. Follow these strict guidelines:

1. FUNCTION CALLS:
- ONLY use functions that are EXPLICITLY listed in the function list below
- If NO functions are listed (empty function list []), respond ONLY with internal knowledge or "I don't have access to [Unavailable service] information"
- If a function is not in the list, respond ONLY with internal knowledge or "I don't have access to [Unavailable service] information"
- If ALL required parameters are present AND the query EXACTLY matches a listed function's purpose: output ONLY the function call(s)
- Use exact format: [
  {
    "name": "<tool_name_foo>",
    "parameters": {
      "<param1_name>": "<param1_value>",
      "<param2_name>": "<param2_value>"
    }
  }
]

Examples:

CORRECT: [
  {
    "name": "get_weather",
    "parameters": {
      "location": "Vancouver"
    }
  },
  {
    "name": "calculate_route",
    "parameters": {
      "start": "Boston",
      "end": "New York"
    }
  }
] <- Only if get_weather and calculate_route are in function list

INCORRECT: [
  {
    "name": "population_projections",
    "parameters": {
      "country": "United States",
      "years": 20
    }
  }
]}] <- Bad json format

INCORRECT: Let me check the weather: [
{
  "name": "get_weather",
  "parameters": {
    "location": "Vancouver"
  }
}]

INCORRECT: [
{
  "name": "get_events",
  "parameters": {
    "location": "Singapore"
  }
}] <- If function not in list


2. RESPONSE RULES:
- For pure function requests matching a listed function: ONLY output the function call(s)
- For knowledge questions: ONLY output text
- For missing parameters: ONLY request the specific missing parameters
- For unavailable services (not in function list): output ONLY with internal knowledge or "I don't have access to [Unavailable service] information". Do NOT execute a function call.
- If the query asks for information beyond what a listed function provides: output ONLY with internal knowledge about your limitations
- NEVER combine text and function calls in the same response
- NEVER suggest alternative functions when the requested service is unavailable
- NEVER create or invent new functions not listed below


3. STRICT BOUNDARIES:
- ONLY use functions from the list below - no exceptions
- NEVER use a function as an alternative to unavailable information
- NEVER call functions not present in the function list
- NEVER add explanatory text to function calls
- NEVER respond with empty brackets
- Use proper JSON syntax for function calls
- Check the function list carefully before responding


4. TOOL RESPONSE HANDLING:
- When receiving tool responses: provide concise, natural language responses
- Don't repeat tool response verbatim
- Don't add supplementary information

`,
		)

		toolsbuf.WriteString(
			"\nHere is a list of functions in JSON format that you can invoke:\n\n[\n",
		)

		for i, tool := range params.Tools {
			toolJSON, err := json.Marshal(tool.Function)
			if err != nil {
				return nil, err
			}
			toolsbuf.WriteString("    ")
			toolsbuf.Write(toolJSON)
			if i < len(params.Tools)-1 {
				toolsbuf.WriteByte(',')
			}
			toolsbuf.WriteByte('\n')
		}

		toolsbuf.WriteString("]\n")

		if forcedToolUseSystemPromptInstruction != "" {
			toolsbuf.WriteByte('\n')
			toolsbuf.WriteString(forcedToolUseSystemPromptInstruction)
			toolsbuf.WriteByte('\n')
		}
	}

	// Add the assistant preamble
	forcedToolPrompt := q.GetForcedToolUsePrefix(&params.ToolChoice)
	if len(forcedToolPrompt) > 0 ||
		messages[len(messages)-1].Role != models.ChatCompletionRoleAssistant {
		messages = append(
			messages,
			models.ChatMessage{
				Role:    models.ChatCompletionRoleAssistant,
				Content: models.SingleTextContent(forcedToolPrompt),
			},
		)
	}

	textbuf := q.bufferPool.Get()
	defer q.bufferPool.Put(textbuf)

	systembuf := q.bufferPool.Get()
	defer q.bufferPool.Put(systembuf)

	enc := q.bufferedEncoderPool.Get()
	defer q.bufferedEncoderPool.Put(enc)

	var (
		// Keep track of tool calls for proper response formatting
		pendingToolCalls []models.ChatToolCall
		prompt           Prompt
		skip             int
	)
	for i, msg := range messages {
		if skip > 0 {
			skip--
			continue
		}
		switch msg.Role {
		case models.ChatCompletionRoleSystem:
			systembuf.WriteString(msg.Content.MustSingleText())
			systembuf.WriteByte('\n')
			continue

		case models.ChatCompletionRoleUser:
			q.WriteRoleHeader(textbuf, "user")
			for _, content := range msg.Content {
				switch c := content.(type) {
				case *models.ContentPartImageURL:
					if textbuf.Len() > 0 {
						prompt = append(prompt, models.ContentPartText(textbuf.String()))
						textbuf.Reset()
					}
					prompt = append(prompt, c)
				case models.ContentPartText:
					textbuf.WriteString(c.String())
				}
			}
			textbuf.WriteString(q.EndOfTurnToken)
		case models.ChatCompletionRoleAssistant:
			q.WriteRoleHeader(textbuf, "assistant")
			switch {
			case msg.ToolCalls != nil && len(*msg.ToolCalls) > 0:
				// Save tool calls for formatting tool responses
				pendingToolCalls = ptr.Deref(msg.ToolCalls)
				// Format as JSON array
				textbuf.WriteString("[\n")
				for j, call := range *msg.ToolCalls {
					var args map[string]any
					err := json.UnmarshalString(call.Function.Arguments, &args)
					if err != nil {
						return nil, err
					}

					toolCallObj := map[string]any{
						"name":       call.Function.Name,
						"parameters": args,
					}

					enc.Buffer().Reset()
					enc.SetIndent("  ", "  ")
					if err := enc.Encode(toolCallObj); err != nil {
						return nil, err
					}
					textbuf.WriteString("  ")
					textbuf.Write(enc.TrimmedBytes())

					if j < len(*msg.ToolCalls)-1 {
						textbuf.WriteByte(',')
					}
					textbuf.WriteByte('\n')
				}
				textbuf.WriteByte(']')
			case msg.FunctionCall != nil:
				// Format as JSON array with single element
				var args map[string]any
				err := json.UnmarshalString(msg.FunctionCall.Arguments, &args)
				if err != nil {
					return nil, err
				}

				toolCallObj := map[string]any{
					"name":       msg.FunctionCall.Name,
					"parameters": args,
				}

				enc.Buffer().Reset()
				enc.SetIndent("", "  ")
				if err := enc.Encode([]any{toolCallObj}); err != nil {
					return nil, err
				}
				textbuf.Write(enc.TrimmedBytes())
			default:
				textbuf.WriteString(msg.Content.MustSingleText())
			}
			if i < len(messages)-1 {
				textbuf.WriteString(q.EndOfTurnToken)
			}
		case models.ChatCompletionRoleFunction, models.ChatCompletionRoleTool:
			// Check if this is the first tool response after tool calls
			isFirstResponse := i > 0 && len(pendingToolCalls) > 0 &&
				messages[i-1].Role == models.ChatCompletionRoleAssistant
			if isFirstResponse {
				// Format tool responses as JSON array
				writeStrings(textbuf, q.StartToolHeader, "[\n")

				// We need to collect all consecutive tool responses
				var (
					toolResponses = []string{msg.Content.MustSingleText()}
					j             = i + 1
				)
				for j < len(messages) && _toolRoles.Contains(messages[j].Role) {
					toolResponses = append(
						toolResponses,
						messages[j].Content.MustSingleText(),
					)
					j++
				}

				// Format each response
				for k, response := range toolResponses {
					textbuf.WriteString("  {\n    \"response\": ")
					textbuf.WriteString(response)
					textbuf.WriteString("\n  }")
					if k < len(toolResponses)-1 {
						textbuf.WriteByte(',')
					}
					textbuf.WriteByte('\n')
				}

				textbuf.WriteByte(']')
				textbuf.WriteString(q.EndOfTurnToken)

				// Clear pending tool calls
				pendingToolCalls = nil

				// Skip the tool responses we've already processed
				skip = len(toolResponses) - 1
			} else {
				// Fallback to simple format if not part of a tool call sequence
				writeStrings(
					textbuf,
					q.StartToolHeader,
					msg.Content.MustSingleText(),
					q.EndOfTurnToken,
				)
			}
		}
	}

	systemPrompt := q.GenerateSysPrompt(systembuf.String(), toolsbuf.String())

	if len(prompt) == 0 { // single modal prompt
		return WrapText(systemPrompt + textbuf.String()), nil
	}

	tmp := make(Prompt, 0, len(prompt)+2)
	tmp = append(tmp, models.ContentPartText(systemPrompt))
	tmp = append(tmp, prompt...)
	tmp = append(tmp, models.ContentPartText(textbuf.String()))
	return tmp, nil
}

func (q *Quattro4) ParseTools(
	params tools.Params,
	message string,
) (string, []tools.Call, error) {
	return tools.ParseArrayWithParametersToolCalls(params, message)
}

func (q *Quattro4) ToolParseConfig() tools.ParseConfig {
	return q
}

func (q *Quattro4) ToolEnvelope(_ tools.Params) (tools.ToolEnvelope, bool) {
	return tools.ToolEnvelope{
		BeginMarker: q.ToolUseBeginMarker(),
		EndMarker:   q.ToolUseEndMarker(),
		Schema:      tools.BareArrayParametersCallSchema(),
	}, true
}

func (q *Quattro4) GetForcedToolUsePrefix(
	_ *models.ChatCompletionToolChoiceField,
) string {
	// Prefilling is disabled for Quattro4Prompt due to performance issues with
	// token splitting.
	return ""
}

func (q *Quattro4) CanBeToolCall(value string) bool {
	if len(q.ToolUseBeginMarker()) > len(value) {
		return strings.HasPrefix(q.ToolUseBeginMarker(), value)
	}
	return strings.HasPrefix(value, q.ToolUseBeginMarker())
}

func applyLlama31Defaults(dst *Llama31) *Llama31 {
	dst.bufferedEncoderPool = json.NewBufferedEncoderPool(0 /* initial size */)
	dst.bufferPool = pool.NewWithReleaser(
		func() *bytes.Buffer {
			return bytes.NewBuffer(make([]byte, 0, 512))
		},
		func(x *bytes.Buffer) {
			x.Reset()
		},
	)
	return dst
}
