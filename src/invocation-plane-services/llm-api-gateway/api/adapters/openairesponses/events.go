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
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/ptr"
)

// Event types for SSE streaming
const (
	EventTypeResponseCreated                    = "response.created"
	EventTypeResponseInProgress                 = "response.in_progress"
	EventTypeResponseCompleted                  = "response.completed"
	EventTypeResponseFailed                     = "response.failed"
	EventTypeResponseIncomplete                 = "response.incomplete"
	EventTypeResponseOutputItemAdded            = "response.output_item.added"
	EventTypeResponseOutputItemDone             = "response.output_item.done"
	EventTypeResponseContentPartAdded           = "response.content_part.added"
	EventTypeResponseContentPartDone            = "response.content_part.done"
	EventTypeResponseOutputTextDelta            = "response.output_text.delta"
	EventTypeResponseOutputTextDone             = "response.output_text.done"
	EventTypeResponseTextAnnotationAdded        = "response.output_text.annotation.added"
	EventTypeResponseRefusalDelta               = "response.refusal.delta"
	EventTypeResponseRefusalDone                = "response.refusal.done"
	EventTypeResponseFunctionCallArgumentsDelta = "response.function_call_arguments.delta"
	EventTypeResponseFunctionCallArgumentsDone  = "response.function_call_arguments.done"
	EventTypeResponseReasoningSummaryPartAdded  = "response.reasoning_summary_part.added"
	EventTypeResponseReasoningSummaryPartDone   = "response.reasoning_summary_part.done"
	EventTypeResponseReasoningSummaryTextDelta  = "response.reasoning_summary_text.delta"
	EventTypeResponseReasoningSummaryTextDone   = "response.reasoning_summary_text.done"
	// New reasoning content events
	EventTypeResponseReasoningTextDelta       = "response.reasoning_text.delta"
	EventTypeResponseReasoningTextDone        = "response.reasoning_text.done"
	EventTypeResponseFileSearchCallInProgress = "response.file_search_call.in_progress"
	EventTypeResponseFileSearchCallSearching  = "response.file_search_call.searching"
	EventTypeResponseFileSearchCallCompleted  = "response.file_search_call.completed"
	EventTypeResponseWebSearchCallInProgress  = "response.web_search_call.in_progress"
	EventTypeResponseWebSearchCallSearching   = "response.web_search_call.searching"
	EventTypeResponseWebSearchCallCompleted   = "response.web_search_call.completed"
	// MCP events
	EventTypeResponseMCPListToolsInProgress = "response.mcp_list_tools.in_progress"
	EventTypeResponseMCPListToolsFailed     = "response.mcp_list_tools.failed"
	EventTypeResponseMCPListToolsCompleted  = "response.mcp_list_tools.completed"
	EventTypeResponseMCPCallInProgress      = "response.mcp_call.in_progress"
	EventTypeResponseMCPCallFailed          = "response.mcp_call.failed"
	EventTypeResponseMCPCallCompleted       = "response.mcp_call.completed"
	EventTypeResponseMCPCallArgumentsDelta  = "response.mcp_call_arguments.delta"
	EventTypeResponseMCPCallArgumentsDone   = "response.mcp_call_arguments.done"
	// Custom tool events
	EventTypeResponseCustomToolCallInputDelta = "response.custom_tool_call_input.delta"
	EventTypeResponseCustomToolCallInputDone  = "response.custom_tool_call_input.done"
	// Extended code interpreter events
	EventTypeResponseCodeInterpreterCallInterpreting = "response.code_interpreter_call.interpreting"
	EventTypeResponseCodeInterpreterCallCodeDelta    = "response.code_interpreter_call_code.delta"
	EventTypeResponseCodeInterpreterCallCodeDone     = "response.code_interpreter_call_code.done"
	// Other events
	EventTypeResponseQueued = "response.queued"
	EventTypeError          = "error"
)

// StreamEvent is a wrapper for all streaming events
type StreamEvent struct {
	Type string `json:"type"`
	Data any    `json:"-"` // Not directly serialized, handled by SSE library
}

// ResponseCreatedEvent is emitted when a response is created
type ResponseCreatedEvent struct {
	Type           string    `json:"type"`
	SequenceNumber int       `json:"sequence_number"`
	Response       *Response `json:"response"`
}

// ResponseInProgressEvent is emitted when the response is in progress
type ResponseInProgressEvent struct {
	Type           string    `json:"type"`
	SequenceNumber int       `json:"sequence_number"`
	Response       *Response `json:"response"`
}

// ResponseCompletedEvent is emitted when the model response is complete
type ResponseCompletedEvent struct {
	Type           string    `json:"type"`
	SequenceNumber int       `json:"sequence_number"`
	Response       *Response `json:"response"`
}

// ResponseFailedEvent is emitted when a response fails
type ResponseFailedEvent struct {
	Type           string    `json:"type"`
	SequenceNumber int       `json:"sequence_number"`
	Response       *Response `json:"response"`
}

// ResponseIncompleteEvent is emitted when a response finishes as incomplete
type ResponseIncompleteEvent struct {
	Type           string    `json:"type"`
	SequenceNumber int       `json:"sequence_number"`
	Response       *Response `json:"response"`
}

// ResponseOutputItemAddedEvent is emitted when a new output item is added
type ResponseOutputItemAddedEvent struct {
	Type           string     `json:"type"`
	SequenceNumber int        `json:"sequence_number"`
	OutputIndex    int        `json:"output_index"`
	Item           OutputItem `json:"item"`
}

// ResponseOutputItemDoneEvent is emitted when an output item is marked done
type ResponseOutputItemDoneEvent struct {
	Type           string     `json:"type"`
	SequenceNumber int        `json:"sequence_number"`
	OutputIndex    int        `json:"output_index"`
	Item           OutputItem `json:"item"`
}

// ResponseContentPartAddedEvent is emitted when a new content part is added
type ResponseContentPartAddedEvent struct {
	Type           string        `json:"type"`
	SequenceNumber int           `json:"sequence_number"`
	ItemID         string        `json:"item_id"`
	OutputIndex    int           `json:"output_index"`
	ContentIndex   int           `json:"content_index"`
	Part           OutputContent `json:"part"`
}

// ResponseContentPartDoneEvent is emitted when a content part is done
type ResponseContentPartDoneEvent struct {
	Type           string        `json:"type"`
	SequenceNumber int           `json:"sequence_number"`
	ItemID         string        `json:"item_id"`
	OutputIndex    int           `json:"output_index"`
	ContentIndex   int           `json:"content_index"`
	Part           OutputContent `json:"part"`
}

// ResponseOutputTextDeltaEvent is emitted when there is an additional text delta
type ResponseOutputTextDeltaEvent struct {
	Type           string   `json:"type"`
	SequenceNumber int      `json:"sequence_number"`
	ItemID         string   `json:"item_id"`
	OutputIndex    int      `json:"output_index"`
	ContentIndex   int      `json:"content_index"`
	Delta          string   `json:"delta"`
	Logprobs       []string `json:"logprobs"`
}

// ResponseReasoningTextDeltaEvent is emitted when there is a reasoning text delta
type ResponseReasoningTextDeltaEvent struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	ContentIndex   int    `json:"content_index"`
	Delta          string `json:"delta"`
}

// ResponseOutputTextDoneEvent is emitted when text content is finalized
type ResponseOutputTextDoneEvent struct {
	Type           string   `json:"type"`
	SequenceNumber int      `json:"sequence_number"`
	ItemID         string   `json:"item_id"`
	OutputIndex    int      `json:"output_index"`
	ContentIndex   int      `json:"content_index"`
	Text           string   `json:"text"`
	Logprobs       []string `json:"logprobs"`
}

// ResponseReasoningTextDoneEvent is emitted when reasoning text is finalized
type ResponseReasoningTextDoneEvent struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	ContentIndex   int    `json:"content_index"`
	Text           string `json:"text"`
}

// ResponseTextAnnotationAddedEvent is emitted when a text annotation is added
type ResponseTextAnnotationAddedEvent struct {
	Type            string     `json:"type"`
	ItemID          string     `json:"item_id"`
	OutputIndex     int        `json:"output_index"`
	ContentIndex    int        `json:"content_index"`
	AnnotationIndex int        `json:"annotation_index"`
	Annotation      Annotation `json:"annotation"`
}

// ResponseRefusalDeltaEvent is emitted when there is a partial refusal text
type ResponseRefusalDeltaEvent struct {
	Type         string `json:"type"`
	ItemID       string `json:"item_id"`
	OutputIndex  int    `json:"output_index"`
	ContentIndex int    `json:"content_index"`
	Delta        string `json:"delta"`
}

// ResponseRefusalDoneEvent is emitted when refusal text is finalized
type ResponseRefusalDoneEvent struct {
	Type         string `json:"type"`
	ItemID       string `json:"item_id"`
	OutputIndex  int    `json:"output_index"`
	ContentIndex int    `json:"content_index"`
	Refusal      string `json:"refusal"`
}

// ResponseFunctionCallArgumentsDeltaEvent is emitted when there is a partial function-call arguments delta
type ResponseFunctionCallArgumentsDeltaEvent struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	Delta          string `json:"delta"`
}

// ResponseFunctionCallArgumentsDoneEvent is emitted when function-call arguments are finalized
type ResponseFunctionCallArgumentsDoneEvent struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	Arguments      string `json:"arguments"`
}

// ReasoningSummaryPart represents a reasoning summary part
type ReasoningSummaryPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ResponseReasoningSummaryPartAddedEvent is emitted when a new reasoning summary part is added
type ResponseReasoningSummaryPartAddedEvent struct {
	Type           string               `json:"type"`
	SequenceNumber int                  `json:"sequence_number"`
	ItemID         string               `json:"item_id"`
	OutputIndex    int                  `json:"output_index"`
	SummaryIndex   int                  `json:"summary_index"`
	Part           ReasoningSummaryPart `json:"part"`
}

// ResponseReasoningSummaryPartDoneEvent is emitted when a reasoning summary part is completed
type ResponseReasoningSummaryPartDoneEvent struct {
	Type           string               `json:"type"`
	SequenceNumber int                  `json:"sequence_number"`
	ItemID         string               `json:"item_id"`
	OutputIndex    int                  `json:"output_index"`
	SummaryIndex   int                  `json:"summary_index"`
	Part           ReasoningSummaryPart `json:"part"`
}

// ResponseReasoningSummaryTextDeltaEvent is emitted when a delta is added to a reasoning summary text
type ResponseReasoningSummaryTextDeltaEvent struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	SummaryIndex   int    `json:"summary_index"`
	Delta          string `json:"delta"`
}

// ResponseReasoningSummaryTextDoneEvent is emitted when a reasoning summary text is completed
type ResponseReasoningSummaryTextDoneEvent struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	SummaryIndex   int    `json:"summary_index"`
	Text           string `json:"text"`
}

// ResponseFileSearchCallInProgressEvent is emitted when a file search call is initiated
type ResponseFileSearchCallInProgressEvent struct {
	Type        string `json:"type"`
	OutputIndex int    `json:"output_index"`
	ItemID      string `json:"item_id"`
}

// ResponseFileSearchCallSearchingEvent is emitted when a file search is currently searching
type ResponseFileSearchCallSearchingEvent struct {
	Type        string `json:"type"`
	OutputIndex int    `json:"output_index"`
	ItemID      string `json:"item_id"`
}

// ResponseFileSearchCallCompletedEvent is emitted when a file search call is completed
type ResponseFileSearchCallCompletedEvent struct {
	Type        string `json:"type"`
	OutputIndex int    `json:"output_index"`
	ItemID      string `json:"item_id"`
}

// ResponseWebSearchCallInProgressEvent is emitted when a web search call is initiated
type ResponseWebSearchCallInProgressEvent struct {
	Type        string `json:"type"`
	OutputIndex int    `json:"output_index"`
	ItemID      string `json:"item_id"`
}

// ResponseWebSearchCallSearchingEvent is emitted when a web search call is executing
type ResponseWebSearchCallSearchingEvent struct {
	Type        string `json:"type"`
	OutputIndex int    `json:"output_index"`
	ItemID      string `json:"item_id"`
}

// ResponseWebSearchCallCompletedEvent is emitted when a web search call is completed
type ResponseWebSearchCallCompletedEvent struct {
	Type        string `json:"type"`
	OutputIndex int    `json:"output_index"`
	ItemID      string `json:"item_id"`
}

// ResponseErrorEvent is emitted when an error occurs
type ResponseErrorEvent struct {
	Type    string  `json:"type"`
	Code    *string `json:"code"`
	Message string  `json:"message"`
	Param   *string `json:"param"`
}

// MCP Events

// ResponseMCPListToolsInProgressEvent is emitted when MCP tools list retrieval is in progress
type ResponseMCPListToolsInProgressEvent struct {
	Type           string `json:"type"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	SequenceNumber int    `json:"sequence_number"`
}

// ResponseMCPListToolsFailedEvent is emitted when MCP tools list retrieval fails
type ResponseMCPListToolsFailedEvent struct {
	Type           string `json:"type"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	SequenceNumber int    `json:"sequence_number"`
}

// ResponseMCPListToolsCompletedEvent is emitted when MCP tools list retrieval completes
type ResponseMCPListToolsCompletedEvent struct {
	Type           string `json:"type"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	SequenceNumber int    `json:"sequence_number"`
}

// ResponseMCPCallInProgressEvent is emitted when an MCP tool call is in progress
type ResponseMCPCallInProgressEvent struct {
	Type           string `json:"type"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	SequenceNumber int    `json:"sequence_number"`
}

// ResponseMCPCallFailedEvent is emitted when an MCP tool call fails
type ResponseMCPCallFailedEvent struct {
	Type           string `json:"type"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	SequenceNumber int    `json:"sequence_number"`
}

// ResponseMCPCallCompletedEvent is emitted when an MCP tool call completes
type ResponseMCPCallCompletedEvent struct {
	Type           string `json:"type"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	SequenceNumber int    `json:"sequence_number"`
}

// ResponseMCPCallArgumentsDeltaEvent is emitted when there is a delta for MCP call arguments
type ResponseMCPCallArgumentsDeltaEvent struct {
	Type           string `json:"type"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	Delta          string `json:"delta"`
	SequenceNumber int    `json:"sequence_number"`
}

// ResponseMCPCallArgumentsDoneEvent is emitted when MCP call arguments are complete
type ResponseMCPCallArgumentsDoneEvent struct {
	Type           string `json:"type"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	Arguments      string `json:"arguments"`
	SequenceNumber int    `json:"sequence_number"`
}

// Custom Tool Call Events

// ResponseCustomToolCallInputDeltaEvent is emitted when there is a delta for custom tool call input
type ResponseCustomToolCallInputDeltaEvent struct {
	Type           string `json:"type"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	Delta          string `json:"delta"`
	SequenceNumber int    `json:"sequence_number"`
}

// ResponseCustomToolCallInputDoneEvent is emitted when custom tool call input is complete
type ResponseCustomToolCallInputDoneEvent struct {
	Type           string `json:"type"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	Input          string `json:"input"`
	SequenceNumber int    `json:"sequence_number"`
}

// Extended Code Interpreter Events

// ResponseCodeInterpreterCallInterpretingEvent is emitted when code interpreter is interpreting
type ResponseCodeInterpreterCallInterpretingEvent struct {
	Type           string `json:"type"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	SequenceNumber int    `json:"sequence_number"`
}

// ResponseCodeInterpreterCallCodeDeltaEvent is emitted when there is a code delta
type ResponseCodeInterpreterCallCodeDeltaEvent struct {
	Type           string `json:"type"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	Delta          string `json:"delta"`
	SequenceNumber int    `json:"sequence_number"`
}

// ResponseCodeInterpreterCallCodeDoneEvent is emitted when code is complete
type ResponseCodeInterpreterCallCodeDoneEvent struct {
	Type           string `json:"type"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	Code           string `json:"code"`
	SequenceNumber int    `json:"sequence_number"`
}

// Other Events

// ResponseQueuedEvent is emitted when a response is queued
type ResponseQueuedEvent struct {
	Type           string    `json:"type"`
	Response       *Response `json:"response"`
	SequenceNumber int       `json:"sequence_number"`
}

// Helper functions to create events

// NewResponseCreatedEvent creates a new response created event
func NewResponseCreatedEvent(response *Response) *ResponseCreatedEvent {
	return &ResponseCreatedEvent{
		Type:     EventTypeResponseCreated,
		Response: response,
	}
}

// NewResponseInProgressEvent creates a new response in progress event
func NewResponseInProgressEvent(response *Response) *ResponseInProgressEvent {
	return &ResponseInProgressEvent{
		Type:     EventTypeResponseInProgress,
		Response: response,
	}
}

// NewResponseCompletedEvent creates a new response completed event
func NewResponseCompletedEvent(response *Response) *ResponseCompletedEvent {
	return &ResponseCompletedEvent{
		Type:     EventTypeResponseCompleted,
		Response: response,
	}
}

// NewResponseFailedEvent creates a new response failed event
func NewResponseFailedEvent(response *Response) *ResponseFailedEvent {
	return &ResponseFailedEvent{
		Type:     EventTypeResponseFailed,
		Response: response,
	}
}

// NewResponseIncompleteEvent creates a new response incomplete event
func NewResponseIncompleteEvent(response *Response) *ResponseIncompleteEvent {
	return &ResponseIncompleteEvent{
		Type:     EventTypeResponseIncomplete,
		Response: response,
	}
}

// NewResponseOutputItemAddedEvent creates a new output item added event
func NewResponseOutputItemAddedEvent(
	outputIndex int,
	item OutputItem,
) *ResponseOutputItemAddedEvent {
	return &ResponseOutputItemAddedEvent{
		Type:        EventTypeResponseOutputItemAdded,
		OutputIndex: outputIndex,
		Item:        item,
	}
}

// NewResponseOutputItemDoneEvent creates a new output item done event
func NewResponseOutputItemDoneEvent(outputIndex int, item OutputItem) *ResponseOutputItemDoneEvent {
	return &ResponseOutputItemDoneEvent{
		Type:        EventTypeResponseOutputItemDone,
		OutputIndex: outputIndex,
		Item:        item,
	}
}

// NewResponseContentPartAddedEvent creates a new content part added event
func NewResponseContentPartAddedEvent(
	itemID string,
	outputIndex int,
	contentIndex int,
	part OutputContent,
) *ResponseContentPartAddedEvent {
	return &ResponseContentPartAddedEvent{
		Type:         EventTypeResponseContentPartAdded,
		ItemID:       itemID,
		OutputIndex:  outputIndex,
		ContentIndex: contentIndex,
		Part:         part,
	}
}

// NewResponseContentPartDoneEvent creates a new content part done event
func NewResponseContentPartDoneEvent(
	itemID string,
	outputIndex int,
	contentIndex int,
	part OutputContent,
) *ResponseContentPartDoneEvent {
	return &ResponseContentPartDoneEvent{
		Type:         EventTypeResponseContentPartDone,
		ItemID:       itemID,
		OutputIndex:  outputIndex,
		ContentIndex: contentIndex,
		Part:         part,
	}
}

// NewResponseOutputTextDeltaEvent creates a new output text delta event
func NewResponseOutputTextDeltaEvent(
	itemID string,
	outputIndex int,
	contentIndex int,
	delta string,
) *ResponseOutputTextDeltaEvent {
	return &ResponseOutputTextDeltaEvent{
		Type:         EventTypeResponseOutputTextDelta,
		ItemID:       itemID,
		OutputIndex:  outputIndex,
		ContentIndex: contentIndex,
		Delta:        delta,
	}
}

// NewResponseReasoningTextDeltaEvent creates a new reasoning text delta event
func NewResponseReasoningTextDeltaEvent(
	itemID string,
	outputIndex int,
	contentIndex int,
	delta string,
) *ResponseReasoningTextDeltaEvent {
	return &ResponseReasoningTextDeltaEvent{
		Type:         EventTypeResponseReasoningTextDelta,
		ItemID:       itemID,
		OutputIndex:  outputIndex,
		ContentIndex: contentIndex,
		Delta:        delta,
	}
}

// NewResponseOutputTextDoneEvent creates a new output text done event
func NewResponseOutputTextDoneEvent(
	itemID string,
	outputIndex int,
	contentIndex int,
	text string,
) *ResponseOutputTextDoneEvent {
	return &ResponseOutputTextDoneEvent{
		Type:         EventTypeResponseOutputTextDone,
		ItemID:       itemID,
		OutputIndex:  outputIndex,
		ContentIndex: contentIndex,
		Text:         text,
	}
}

// NewResponseReasoningTextDoneEvent creates a new reasoning text done event
func NewResponseReasoningTextDoneEvent(
	itemID string,
	outputIndex int,
	contentIndex int,
	text string,
) *ResponseReasoningTextDoneEvent {
	return &ResponseReasoningTextDoneEvent{
		Type:         EventTypeResponseReasoningTextDone,
		ItemID:       itemID,
		OutputIndex:  outputIndex,
		ContentIndex: contentIndex,
		Text:         text,
	}
}

// NewResponseFunctionCallArgumentsDeltaEvent creates a new function call arguments delta event
func NewResponseFunctionCallArgumentsDeltaEvent(
	itemID string,
	outputIndex int,
	delta string,
) *ResponseFunctionCallArgumentsDeltaEvent {
	return &ResponseFunctionCallArgumentsDeltaEvent{
		Type:        EventTypeResponseFunctionCallArgumentsDelta,
		ItemID:      itemID,
		OutputIndex: outputIndex,
		Delta:       delta,
	}
}

// NewResponseFunctionCallArgumentsDoneEvent creates a new function call arguments done event
func NewResponseFunctionCallArgumentsDoneEvent(
	itemID string,
	outputIndex int,
	arguments string,
) *ResponseFunctionCallArgumentsDoneEvent {
	return &ResponseFunctionCallArgumentsDoneEvent{
		Type:        EventTypeResponseFunctionCallArgumentsDone,
		ItemID:      itemID,
		OutputIndex: outputIndex,
		Arguments:   arguments,
	}
}

// NewResponseErrorEvent creates a new error event
func NewResponseErrorEvent(code *string, message string, param *string) *ResponseErrorEvent {
	return &ResponseErrorEvent{
		Type:    EventTypeError,
		Code:    code,
		Message: message,
		Param:   param,
	}
}

// NewResponseErrorEventFromError creates a new error event from a Go error
func NewResponseErrorEventFromError(err error) *ResponseErrorEvent {
	return &ResponseErrorEvent{
		Type:    EventTypeError,
		Code:    ptr.To("server_error"),
		Message: err.Error(),
		Param:   nil,
	}
}
