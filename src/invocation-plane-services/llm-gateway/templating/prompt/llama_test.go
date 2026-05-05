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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
	"github.com/NVIDIA/nvcf/llm-api-gateway/templating/tools"
)

var llama31KnowledgePrompt = fmt.Sprintf(
	"Cutting Knowledge Date: December 2023\nToday Date: %v\n\n",
	time.Now().Format("02 January 2006"),
)

func Test_getGroqLlamaFormattedPrompt_NoTools_U(t *testing.T) {
	t.Parallel()
	// Arrange
	messages := []models.ChatMessage{
		{
			Role:    models.ChatCompletionRoleUser,
			Content: models.SingleTextContent("abc"),
		},
	}

	tests := map[TextTemplate]string{
		NewLlama31(): fmt.Sprintf(
			"<|start_header_id|>system<|end_header_id|>\n\nCutting Knowledge Date: December 2023\nToday Date: %v\n\n<|eot_id|>"+
				"<|start_header_id|>user<|end_header_id|>\n\nabc<|eot_id|><|start_header_id|>assistant<|end_header_id|>\n\n",
			time.Now().Format("02 January 2006"),
		),
	}
	// Act
	for template, expected := range tests {
		t.Run(fmt.Sprintf("%T", template), func(t *testing.T) {
			t.Parallel()
			// Act
			out, err := template.RenderText(messages, tools.Params{})
			require.NoError(t, err)
			assert.NotNil(t, out)

			// Assert
			assert.Equal(t, WrapText(expected), out)
		})
	}
}

func Test_getGroqLlamaFormattedPrompt_NoTools_SU(t *testing.T) {
	t.Parallel()
	// Arrange
	var (
		messages = []models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleSystem,
				Content: models.SingleTextContent("systemsg"),
			},
			{
				Role:    models.ChatCompletionRoleUser,
				Content: models.SingleTextContent("usermsg"),
			},
		}
		expected = "<|start_header_id|>system<|end_header_id|>\n\n%vsystemsg<|eot_id|><|start_header_id|>user<|end_header_id|>\n\nusermsg<|eot_id|><|start_header_id|>assistant<|end_header_id|>\n\n"
		tests    = map[TextTemplate]string{
			NewLlama31(): fmt.Sprintf(expected, llama31KnowledgePrompt),
			NewLlama32(): fmt.Sprintf(expected, llama31KnowledgePrompt),
		}
	)
	// Act
	for template, expected := range tests {
		t.Run(fmt.Sprintf("%T", template), func(t *testing.T) {
			t.Parallel()
			// Act
			out, err := template.RenderText(messages, tools.Params{})
			require.NoError(t, err)
			assert.NotNil(t, out)

			// Assert
			assert.Equal(t, WrapText(expected), out)
		})
	}
}

// test all state transitions A->S, A->U, S->A, S->U, U->A, U->S
func Test_getGroqLlamaFormattedPrompt_NoTools_ASUSAUA(t *testing.T) {
	t.Parallel()
	// Arrange
	var (
		messages = []models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleAssistant,
				Content: models.SingleTextContent("a1"),
			},
			{
				Role:    models.ChatCompletionRoleSystem,
				Content: models.SingleTextContent("s1"),
			},
			{
				Role:    models.ChatCompletionRoleUser,
				Content: models.SingleTextContent("u1"),
			},
			{
				Role:    models.ChatCompletionRoleSystem,
				Content: models.SingleTextContent("s2"),
			},
			{
				Role:    models.ChatCompletionRoleAssistant,
				Content: models.SingleTextContent("a2"),
			},
			{
				Role:    models.ChatCompletionRoleUser,
				Content: models.SingleTextContent("u2"),
			},
			{
				Role:    models.ChatCompletionRoleAssistant,
				Content: models.SingleTextContent("a3"),
			},
		}
		expected string
	)
	expected += "<|start_header_id|>system<|end_header_id|>\n\n%vs1\ns2<|eot_id|>"
	expected += "<|start_header_id|>assistant<|end_header_id|>\n\na1<|eot_id|>"
	expected += "<|start_header_id|>user<|end_header_id|>\n\nu1<|eot_id|>"
	expected += "<|start_header_id|>assistant<|end_header_id|>\n\na2<|eot_id|>"
	expected += "<|start_header_id|>user<|end_header_id|>\n\nu2<|eot_id|>"
	expected += "<|start_header_id|>assistant<|end_header_id|>\n\na3"
	// Act
	tests := map[TextTemplate]string{
		NewLlama31(): fmt.Sprintf(expected, llama31KnowledgePrompt),
		NewLlama32(): fmt.Sprintf(expected, llama31KnowledgePrompt),
	}
	// Act
	for template, expected := range tests {
		t.Run(fmt.Sprintf("%T", template), func(t *testing.T) {
			t.Parallel()
			// Act
			out, err := template.RenderText(messages, tools.Params{})
			require.NoError(t, err)
			assert.NotNil(t, out)

			// Assert
			assert.Equal(t, WrapText(expected), out)
		})
	}
}

func Test_getGroqLlamaFormattedPrompt_Tools_SA(t *testing.T) {
	t.Parallel()
	var (
		messages = []models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleSystem,
				Content: models.SingleTextContent("s1"),
			},
			{
				Role:    models.ChatCompletionRoleAssistant,
				Content: models.SingleTextContent("a1"),
			},
		}
		required  = models.ChatToolSelectionRequired
		toolsInfo = tools.Params{
			ToolChoice: models.ChatCompletionToolChoiceField{
				String: &required,
			},
		}
		expected = "<|start_header_id|>system<|end_header_id|>\n\n%vs1<|eot_id|>"
	)
	expected += "<|start_header_id|>assistant<|end_header_id|>\n\na1<|eot_id|>"
	expected += "<|start_header_id|>assistant<|end_header_id|>\n\n%v"
	tests := map[TextTemplate]string{
		NewLlama31(): fmt.Sprintf(expected, llama31KnowledgePrompt, "<function="),
		NewLlama32(): fmt.Sprintf(expected, llama31KnowledgePrompt, "<function="),
	}
	for template, expected := range tests {
		t.Run(fmt.Sprintf("%T", template), func(t *testing.T) {
			t.Parallel()
			out, err := template.RenderText(messages, toolsInfo)
			require.NoError(t, err)
			assert.NotNil(t, out)

			assert.Equal(t, WrapText(expected), out)
		})
	}
}

func Test_getGroqLlamaFormattedPrompt_Tools_UAtU(t *testing.T) {
	t.Parallel()
	// Arrange
	var (
		fooDescription = "foo is mostly about bar"
		toolsInfo      = tools.Params{
			Tools: []tools.Tool{
				{
					Type: "object",
					Function: tools.ChatFunctionSpec{
						Name:        "foo",
						Description: &fooDescription,
						Parameters: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"num": map[string]any{
									"type":        "int",
									"description": "the number",
								},
							},
							"required": []string{"num"},
						},
					},
				},
			},
		}
		messages = []models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleUser,
				Content: models.SingleTextContent("u1"),
			},
			{
				Role: models.ChatCompletionRoleAssistant,
				ToolCalls: &[]models.ChatToolCall{
					{
						ID:   "12345",
						Type: "object",
						Function: models.ChatToolCallFunction{
							Name:      "foo",
							Arguments: "{\"num\":12}",
						},
					},
				},
			},
			{
				Role:    models.ChatCompletionRoleUser,
				Content: models.SingleTextContent("u2"),
			},
		}
	)

	// Assert

	llama31Expected := `<|start_header_id|>system<|end_header_id|>

` + llama31KnowledgePrompt + `
You have access to the following functions:

Use the function 'foo' to 'foo is mostly about bar'
{"name":"foo","description":"foo is mostly about bar","parameters":{"properties":{"num":{"description":"the number","type":"int"}},"required":["num"],"type":"object"}}


Think very carefully before calling functions.
If you choose to call a function ONLY reply in the following format with no prefix or suffix:

<function=example_function_name>{"example_name": "example_value"}</function>

Reminder:
- If looking for real time information use relevant functions before falling back to brave_search
- Function calls MUST follow the specified format, start with <function= and end with </function>
- Required parameters MUST be specified
- Only call one function at a time
- Put the entire function call reply on one line

<|eot_id|><|start_header_id|>user<|end_header_id|>

u1<|eot_id|><|start_header_id|>assistant<|end_header_id|>

<function=foo>{"num":12}</function><|eot_id|><|start_header_id|>user<|end_header_id|>

u2<|eot_id|><|start_header_id|>assistant<|end_header_id|>

`

	tests := map[TextTemplate]string{
		NewLlama31(): llama31Expected,
		NewLlama32(): llama31Expected,
	}

	for template, expected := range tests {
		t.Run(fmt.Sprintf("%T", template), func(t *testing.T) {
			t.Parallel()
			// Act
			out, err := template.RenderText(messages, toolsInfo)
			require.NoError(t, err)
			assert.NotNil(t, out)

			// Assert
			assert.Equal(t, WrapText(expected), out)
		})
	}
}

func Test_getGroqLlamaFormattedPrompt_Tools_UAtTAU(t *testing.T) {
	t.Parallel()
	// Arrange
	var (
		fooDescription = "foo is mostly about bar"
		toolsInfo      = tools.Params{
			Tools: []tools.Tool{
				{
					Type: "object",
					Function: tools.ChatFunctionSpec{
						Name:        "foo",
						Description: &fooDescription,
						Parameters: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"num": map[string]any{
									"type":        "int",
									"description": "the number",
								},
							},
							"required": []string{"num"},
						},
					},
				},
			},
		}
		messages = []models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleUser,
				Content: models.SingleTextContent("u1"),
			},
			{
				Role: models.ChatCompletionRoleAssistant,
				ToolCalls: &[]models.ChatToolCall{
					{
						ID:   "12345",
						Type: "object",
						Function: models.ChatToolCallFunction{
							Name:      "foo",
							Arguments: "{\"num\":12}",
						},
					},
				},
			},
			{
				Role:    models.ChatCompletionRoleTool,
				Content: models.SingleTextContent("{\"result\": \"Is this Correct?\"}"),
			},
			{
				Role:    models.ChatCompletionRoleAssistant,
				Content: models.SingleTextContent("It sure is u1 foo!"),
			},
			{
				Role:    models.ChatCompletionRoleUser,
				Content: models.SingleTextContent("u2"),
			},
		}
	)
	// Act
	llama31Expected := `<|start_header_id|>system<|end_header_id|>

` + llama31KnowledgePrompt + `
You have access to the following functions:

Use the function 'foo' to 'foo is mostly about bar'
{"name":"foo","description":"foo is mostly about bar","parameters":{"properties":{"num":{"description":"the number","type":"int"}},"required":["num"],"type":"object"}}


Think very carefully before calling functions.
If you choose to call a function ONLY reply in the following format with no prefix or suffix:

<function=example_function_name>{"example_name": "example_value"}</function>

Reminder:
- If looking for real time information use relevant functions before falling back to brave_search
- Function calls MUST follow the specified format, start with <function= and end with </function>
- Required parameters MUST be specified
- Only call one function at a time
- Put the entire function call reply on one line

<|eot_id|><|start_header_id|>user<|end_header_id|>

u1<|eot_id|><|start_header_id|>assistant<|end_header_id|>

<function=foo>{"num":12}</function><|eot_id|><|start_header_id|>ipython<|end_header_id|>

{"result": "Is this Correct?"}<|eot_id|><|start_header_id|>assistant<|end_header_id|>

It sure is u1 foo!<|eot_id|><|start_header_id|>user<|end_header_id|>

u2<|eot_id|><|start_header_id|>assistant<|end_header_id|>

`
	// Assert
	tests := map[TextTemplate]string{
		NewLlama32(): llama31Expected,
	}
	for template, expected := range tests {
		t.Run(fmt.Sprintf("%T", template), func(t *testing.T) {
			t.Parallel()
			// Act
			out, err := template.RenderText(messages, toolsInfo)
			require.NoError(t, err)
			assert.NotNil(t, out)

			// Assert
			assert.Equal(t, WrapText(expected), out)
		})
	}
}

func Test_getQuattroFormattedPrompt_ForcedToolChoice(t *testing.T) {
	t.Parallel()
	// Arrange
	var (
		fooDescription = "foo is mostly about bar"
		barDescription = "bar is mostly about foo"
		toolsInfo      = tools.Params{
			Tools: []tools.Tool{
				{
					Type: "object",
					Function: tools.ChatFunctionSpec{
						Name:        "foo",
						Description: &fooDescription,
						Parameters: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"num": map[string]any{
									"type":        "int",
									"description": "the number",
								},
							},
							"required": []string{"num"},
						},
					},
				},
				{
					Type: "object",
					Function: tools.ChatFunctionSpec{
						Name:        "bar",
						Description: &barDescription,
						Parameters:  nil,
					},
				},
			},
			ToolChoice: models.ChatCompletionToolChoiceField{
				ToolChoice: &models.ChatToolChoice{
					Type: "function",
					Function: models.ChatFunctionChoice{
						Name: "foo",
					},
				},
			},
		}
		messages = []models.ChatMessage{
			{
				Role:    models.ChatCompletionRoleUser,
				Content: models.SingleTextContent("u1"),
			},
		}
	)

	// Assert
	// Note: quattroPrompt does not include the knowledge cutoff date in the system prompt.
	quattroExpected := `<|header_start|>system<|header_end|>

You are a helpful assistant and an expert in function composition. You can answer general questions using your internal knowledge OR invoke functions when necessary. Follow these strict guidelines:

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


Here is a list of functions in JSON format that you can invoke:

[
    {"name":"foo","description":"foo is mostly about bar","parameters":{"properties":{"num":{"description":"the number","type":"int"}},"required":["num"],"type":"object"}},
    {"name":"bar","description":"bar is mostly about foo","parameters":null}
]

You must use the function foo to answer the user query.
<|eot|><|header_start|>user<|header_end|>

u1<|eot|><|header_start|>assistant<|header_end|>

`
	// ^ Prefilling disabled due to performance issues with token splitting
	// Use Quattro4 (new prompt)
	out, err := NewQuattro4().RenderText(messages, toolsInfo)
	require.NoError(t, err)
	assert.NotNil(t, out)

	// Assert
	assert.Equal(t, WrapText(quattroExpected), out) // Compare with quattroExpected
}

func Test_getQuattroFormattedPrompt_ForcedToolChoice_WithImage(t *testing.T) {
	t.Parallel()
	// Arrange
	var (
		fooDescription = "foo is mostly about bar"
		barDescription = "bar is mostly about foo"
		toolsInfo      = tools.Params{
			Tools: []tools.Tool{
				{
					Type: "object",
					Function: tools.ChatFunctionSpec{
						Name:        "foo",
						Description: &fooDescription,
						Parameters: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"num": map[string]any{
									"type":        "int",
									"description": "the number",
								},
							},
							"required": []string{"num"},
						},
					},
				},
				{
					Type: "object",
					Function: tools.ChatFunctionSpec{
						Name:        "bar",
						Description: &barDescription,
						Parameters:  nil,
					},
				},
			},
			ToolChoice: models.ChatCompletionToolChoiceField{
				ToolChoice: &models.ChatToolChoice{
					Type: "function",
					Function: models.ChatFunctionChoice{
						Name: "foo",
					},
				},
			},
		}
		image1   = models.ContentPartImageURL{URL: "image_url_here"}
		textPart = models.ContentPartText("u1")
		messages = []models.ChatMessage{
			{
				Role: models.ChatCompletionRoleUser,
				Content: models.ChatMessageContent{
					&image1,
					textPart,
				},
			},
		}
	)

	// Expected: System prompt is PRESERVED for Quattro (like Llama 3.1), even with images.
	// Note: quattroPrompt does not include the knowledge cutoff date in the system prompt.
	systemPromptText := `<|header_start|>system<|header_end|>

You are a helpful assistant and an expert in function composition. You can answer general questions using your internal knowledge OR invoke functions when necessary. Follow these strict guidelines:

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


Here is a list of functions in JSON format that you can invoke:

[
    {"name":"foo","description":"foo is mostly about bar","parameters":{"properties":{"num":{"description":"the number","type":"int"}},"required":["num"],"type":"object"}},
    {"name":"bar","description":"bar is mostly about foo","parameters":null}
]

You must use the function foo to answer the user query.
<|eot|>`
	// ^ Use quattro EOT token
	// Prefilling disabled due to performance issues with token splitting
	userPromptText := `u1<|eot|><|header_start|>assistant<|header_end|>

`

	expectedPrompt := Prompt{
		models.ContentPartText(systemPromptText),
		models.ContentPartText(
			"<|header_start|>user<|header_end|>\n\n",
		), // Use quattro header tokens
		&image1,
		models.ContentPartText(userPromptText), // Expecting the prompt to force the "foo" function
	}

	// Act
	// Quattro (Llama 3.1 base) preserves system prompt with images
	out, err := NewQuattro4().RenderText(messages, toolsInfo) // Use QuattroV4 (new prompt)
	require.NoError(t, err)
	assert.NotNil(t, out)

	// Assert
	assert.Equal(t, expectedPrompt, out)
}

func Test_getGroqLlamaFormattedPrompt_Image(t *testing.T) {
	t.Parallel()
	var (
		pre      = models.ContentPartText("Pre")
		image1   = models.ContentPartImageURL{URL: "1"}
		middle   = models.ContentPartText("mid")
		image2   = models.ContentPartImageURL{URL: "2"}
		image3   = models.ContentPartImageURL{URL: "3"}
		image4   = models.ContentPartImageURL{URL: "4"}
		post     = models.ContentPartText("Post")
		messages = []models.ChatMessage{
			{
				Role: models.ChatCompletionRoleUser,
				Content: models.ChatMessageContent{
					pre,
					&image1,
					middle,
					&image2,
					post,
				},
			},
			{
				Role:    models.ChatCompletionRoleAssistant,
				Content: models.SingleTextContent("response"),
			},
			{
				Role: models.ChatCompletionRoleUser,
				Content: models.ChatMessageContent{
					&image3,
					&image4,
				},
			},
		}
		fooDescription = "foo is mostly about bar"
		toolsInfo      = tools.Params{
			Tools: []tools.Tool{
				{
					Type: "object",
					Function: tools.ChatFunctionSpec{
						Name:        "foo",
						Description: &fooDescription,
						Parameters: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"num": map[string]any{
									"type":        "int",
									"description": "the number",
								},
							},
							"required": []string{"num"},
						},
					},
				},
			},
		}
	)

	expected := Prompt{
		// system prompt should be omitted when image is provided
		models.ContentPartText("<|start_header_id|>user<|end_header_id|>\n\n"),
		&image1,
		&image2,
		models.ContentPartText(
			"\nYou have access to the following functions:\n\nUse the function 'foo' to 'foo is mostly about bar'\n{\"name\":\"foo\",\"description\":\"foo is mostly about bar\",\"parameters\":{\"properties\":{\"num\":{\"description\":\"the number\",\"type\":\"int\"}},\"required\":[\"num\"],\"type\":\"object\"}}\n\n\nThink very carefully before calling functions.\nIf you choose to call a function ONLY reply in the following format with no prefix or suffix:\n\n<function=example_function_name>{\"example_name\": \"example_value\"}</function>\n\nReminder:\n- If looking for real time information use relevant functions before falling back to brave_search\n- Function calls MUST follow the specified format, start with <function= and end with </function>\n- Required parameters MUST be specified\n- Only call one function at a time\n- Put the entire function call reply on one line\n\n" +
				"PremidPost<|eot_id|><|start_header_id|>assistant<|end_header_id|>\n\nresponse<|eot_id|><|start_header_id|>user<|end_header_id|>\n\n",
		),
		&image3,
		&image4,
		models.ContentPartText("<|eot_id|><|start_header_id|>assistant<|end_header_id|>\n\n"),
	}
	prompt, err := NewLlama32().RenderText(messages, toolsInfo)
	require.NoError(t, err)
	assert.Equal(t, expected, prompt)
}

func Test_rejectImageTag(t *testing.T) {
	t.Parallel()
	messages := []models.ChatMessage{
		{
			Role:    models.ChatCompletionRoleUser,
			Content: models.SingleTextContent("hello"),
		},
	}
	fooDescription := "foo is mostly about bar"
	toolsInfo := tools.Params{
		Tools: []tools.Tool{
			{
				Type: "object",
				Function: tools.ChatFunctionSpec{
					Name:        "<|image|>",
					Description: &fooDescription,
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"num": map[string]any{
								"type":        "int",
								"description": "the number",
							},
						},
						"required": []string{"num"},
					},
				},
			},
		},
	}
	_, err := NewLlama32().RenderText(messages, toolsInfo)
	require.Error(t, err)
}

func Test_CanBeToolCall(t *testing.T) {
	t.Parallel()
	prompt := NewLlama31()
	for _, test := range []struct {
		input    string
		expected bool
	}{
		{
			input:    "foobar",
			expected: false,
		},
		{
			input:    prompt.ToolUseBeginMarker(),
			expected: true,
		},
		{
			input:    prompt.ToolUseBeginMarker() + "get_current_weather",
			expected: true,
		},
		{
			input:    "junk" + prompt.ToolUseBeginMarker(),
			expected: false,
		},
	} {
		t.Run(test.input, func(t *testing.T) {
			assert.Equal(t, test.expected, prompt.CanBeToolCall(test.input))
		})
	}
}
