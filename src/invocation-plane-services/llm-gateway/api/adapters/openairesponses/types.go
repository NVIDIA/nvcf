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

// Reasoning effort levels
const (
	ReasoningEffortLow    = "low"
	ReasoningEffortMedium = "medium"
	ReasoningEffortHigh   = "high"
)

// Text represents text formatting options
type Text struct {
	Format *TextFormat `json:"format,omitempty"`
}

// TextFormat represents response format configuration
type TextFormat struct {
	Type string `json:"type"`

	// For JSON schema format
	Name        string         `json:"name,omitempty"`
	Description *string        `json:"description,omitempty"`
	Schema      map[string]any `json:"schema,omitempty"`
	Strict      *bool          `json:"strict,omitempty"`
}

// Reasoning represents reasoning configuration
type Reasoning struct {
	Effort          *string `json:"effort,omitempty"`
	Summary         *string `json:"summary,omitempty"`
	GenerateSummary *string `json:"generate_summary,omitempty"`
}

// Metadata represents key-value metadata
type Metadata map[string]string

// GroqTimingMetrics represents timing metrics for inference
type GroqTimingMetrics struct {
	PromptTime     float64  `json:"prompt_time"`
	CompletionTime float64  `json:"completion_time"`
	TotalTime      float64  `json:"total_time"`
	QueueTime      *float64 `json:"queue_time"`
}

// GroqMetadata represents Groq-specific metadata
type GroqMetadata struct {
	Metrics *GroqTimingMetrics `json:"metrics"`
}

// ResponseUsage represents token usage information
type ResponseUsage struct {
	InputTokens         int                 `json:"input_tokens"`
	InputTokensDetails  InputTokensDetails  `json:"input_tokens_details"`
	OutputTokens        int                 `json:"output_tokens"`
	OutputTokensDetails OutputTokensDetails `json:"output_tokens_details"`
	TotalTokens         int                 `json:"total_tokens"`
}

// InputTokensDetails represents detailed input token usage information
type InputTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

// OutputTokensDetails represents detailed output token usage information
type OutputTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// ResponseError represents API error details
type ResponseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// IncompleteDetails represents incomplete response details
type IncompleteDetails struct {
	Reason string `json:"reason"`
}

// Prompt represents a reference to a prompt template
type Prompt struct {
	ID        string                  `json:"id"`
	Version   *string                 `json:"version"`
	Variables ResponsePromptVariables `json:"variables"`
}

// ResponsePromptVariables is a map of variable substitutions for prompt templates.
// Values can be strings or InputContent types (text, image, file).
type ResponsePromptVariables map[string]any

// ResponseStreamOptions configures streaming behavior
type ResponseStreamOptions struct {
	IncludeObfuscation *bool `json:"include_obfuscation"`
}

// Conversation represents conversation management.
// Can be either a conversation ID string or a ConversationParam object.
type Conversation struct {
	ID *string `json:"id"`
}
