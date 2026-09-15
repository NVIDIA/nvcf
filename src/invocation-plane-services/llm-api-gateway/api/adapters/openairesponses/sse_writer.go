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
	"context"
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"

	"go.jetify.com/sse"
)

// SSEWriter wraps the jetify SSE connection and provides typed event sending
type SSEWriter struct {
	conn           *sse.Conn
	ctx            context.Context
	sequenceNumber int
	mu             sync.Mutex // Protects sequenceNumber
}

// NewSSEWriter creates a new SSE writer from an HTTP response writer.
// It calls sse.Upgrade() which sets the HTTP status code (200 OK) and required
// SSE headers (Content-Type: text/event-stream, Cache-Control, Connection).
func NewSSEWriter(ctx context.Context, w http.ResponseWriter) (*SSEWriter, error) {
	// Upgrade the connection to SSE with retry disabled to avoid empty initial event
	// and with built-in heartbeat for keep-alive
	conn, err := sse.Upgrade(
		ctx,
		w,
		sse.WithRetryDelay(0),
		sse.WithHeartbeatInterval(15*time.Second),
		sse.WithHeartbeatComment("ping"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to upgrade to SSE: %w", err)
	}

	writer := &SSEWriter{
		conn: conn,
		ctx:  ctx,
	}

	return writer, nil
}

// getNextSequenceNumber returns the next sequence number, wrapping at MaxInt
func (w *SSEWriter) getNextSequenceNumber() int {
	w.mu.Lock()
	defer w.mu.Unlock()

	seq := w.sequenceNumber

	// Handle potential overflow - wrap back to 0
	if w.sequenceNumber == math.MaxInt {
		w.sequenceNumber = 0
	} else {
		w.sequenceNumber++
	}

	return seq
}

// SendEvent sends a typed event to the client
func (w *SSEWriter) SendEvent(eventType string, data any) error {
	// Create SSE event
	event := &sse.Event{
		Event: eventType,
		Data:  data,
	}

	// Send the event
	return w.conn.SendEvent(w.ctx, event)
}

// SendResponseCreated sends a response.created event
func (w *SSEWriter) SendResponseCreated(response *Response) error {
	event := NewResponseCreatedEvent(response)
	event.SequenceNumber = w.getNextSequenceNumber()
	return w.SendEvent(EventTypeResponseCreated, event)
}

// SendResponseInProgress sends a response.in_progress event
func (w *SSEWriter) SendResponseInProgress(response *Response) error {
	event := NewResponseInProgressEvent(response)
	event.SequenceNumber = w.getNextSequenceNumber()
	return w.SendEvent(EventTypeResponseInProgress, event)
}

// SendResponseCompleted sends a response.completed event
func (w *SSEWriter) SendResponseCompleted(response *Response) error {
	event := NewResponseCompletedEvent(response)
	event.SequenceNumber = w.getNextSequenceNumber()
	return w.SendEvent(EventTypeResponseCompleted, event)
}

// SendResponseFailed sends a response.failed event
func (w *SSEWriter) SendResponseFailed(response *Response) error {
	event := NewResponseFailedEvent(response)
	event.SequenceNumber = w.getNextSequenceNumber()
	return w.SendEvent(EventTypeResponseFailed, event)
}

// SendResponseIncomplete sends a response.incomplete event
func (w *SSEWriter) SendResponseIncomplete(response *Response) error {
	event := NewResponseIncompleteEvent(response)
	event.SequenceNumber = w.getNextSequenceNumber()
	return w.SendEvent(EventTypeResponseIncomplete, event)
}

// SendOutputItemAdded sends a response.output_item.added event
func (w *SSEWriter) SendOutputItemAdded(outputIndex int, item OutputItem) error {
	event := NewResponseOutputItemAddedEvent(outputIndex, item)
	event.SequenceNumber = w.getNextSequenceNumber()
	return w.SendEvent(EventTypeResponseOutputItemAdded, event)
}

// SendOutputItemDone sends a response.output_item.done event
func (w *SSEWriter) SendOutputItemDone(outputIndex int, item OutputItem) error {
	event := NewResponseOutputItemDoneEvent(outputIndex, item)
	event.SequenceNumber = w.getNextSequenceNumber()
	return w.SendEvent(EventTypeResponseOutputItemDone, event)
}

// SendContentPartAdded sends a response.content_part.added event
func (w *SSEWriter) SendContentPartAdded(
	itemID string,
	outputIndex int,
	contentIndex int,
	part OutputContent,
) error {
	event := NewResponseContentPartAddedEvent(itemID, outputIndex, contentIndex, part)
	event.SequenceNumber = w.getNextSequenceNumber()
	return w.SendEvent(EventTypeResponseContentPartAdded, event)
}

// SendContentPartDone sends a response.content_part.done event
func (w *SSEWriter) SendContentPartDone(
	itemID string,
	outputIndex int,
	contentIndex int,
	part OutputContent,
) error {
	event := NewResponseContentPartDoneEvent(itemID, outputIndex, contentIndex, part)
	event.SequenceNumber = w.getNextSequenceNumber()
	return w.SendEvent(EventTypeResponseContentPartDone, event)
}

// SendOutputTextDelta sends a response.output_text.delta event
func (w *SSEWriter) SendOutputTextDelta(
	itemID string,
	outputIndex int,
	contentIndex int,
	delta string,
) error {
	event := NewResponseOutputTextDeltaEvent(itemID, outputIndex, contentIndex, delta)
	event.SequenceNumber = w.getNextSequenceNumber()
	event.Logprobs = []string{} // Empty array to match OpenAI
	return w.SendEvent(EventTypeResponseOutputTextDelta, event)
}

// SendOutputTextDone sends a response.output_text.done event
func (w *SSEWriter) SendOutputTextDone(
	itemID string,
	outputIndex int,
	contentIndex int,
	text string,
) error {
	event := NewResponseOutputTextDoneEvent(itemID, outputIndex, contentIndex, text)
	event.SequenceNumber = w.getNextSequenceNumber()
	event.Logprobs = []string{} // Empty array to match OpenAI
	return w.SendEvent(EventTypeResponseOutputTextDone, event)
}

// SendReasoningTextDelta sends a response.reasoning_text.delta event
func (w *SSEWriter) SendReasoningTextDelta(
	itemID string,
	outputIndex int,
	contentIndex int,
	delta string,
) error {
	event := NewResponseReasoningTextDeltaEvent(itemID, outputIndex, contentIndex, delta)
	event.SequenceNumber = w.getNextSequenceNumber()
	return w.SendEvent(EventTypeResponseReasoningTextDelta, event)
}

// SendReasoningTextDone sends a response.reasoning_text.done event
func (w *SSEWriter) SendReasoningTextDone(
	itemID string,
	outputIndex int,
	contentIndex int,
	text string,
) error {
	event := NewResponseReasoningTextDoneEvent(itemID, outputIndex, contentIndex, text)
	event.SequenceNumber = w.getNextSequenceNumber()
	return w.SendEvent(EventTypeResponseReasoningTextDone, event)
}

// SendReasoningSummaryPartAdded sends a response.reasoning_summary_part.added event
func (w *SSEWriter) SendReasoningSummaryPartAdded(
	itemID string,
	outputIndex int,
	summaryIndex int,
) error {
	event := &ResponseReasoningSummaryPartAddedEvent{
		Type:         EventTypeResponseReasoningSummaryPartAdded,
		ItemID:       itemID,
		OutputIndex:  outputIndex,
		SummaryIndex: summaryIndex,
		Part: ReasoningSummaryPart{
			Type: "summary_text",
			Text: "",
		},
	}
	event.SequenceNumber = w.getNextSequenceNumber()
	return w.SendEvent(EventTypeResponseReasoningSummaryPartAdded, event)
}

// SendReasoningSummaryTextDelta sends a response.reasoning_summary_text.delta event
func (w *SSEWriter) SendReasoningSummaryTextDelta(
	itemID string,
	outputIndex int,
	summaryIndex int,
	delta string,
) error {
	event := &ResponseReasoningSummaryTextDeltaEvent{
		Type:         EventTypeResponseReasoningSummaryTextDelta,
		ItemID:       itemID,
		OutputIndex:  outputIndex,
		SummaryIndex: summaryIndex,
		Delta:        delta,
	}
	event.SequenceNumber = w.getNextSequenceNumber()
	return w.SendEvent(EventTypeResponseReasoningSummaryTextDelta, event)
}

// SendReasoningSummaryTextDone sends a response.reasoning_summary_text.done event
func (w *SSEWriter) SendReasoningSummaryTextDone(
	itemID string,
	outputIndex int,
	summaryIndex int,
	text string,
) error {
	event := &ResponseReasoningSummaryTextDoneEvent{
		Type:         EventTypeResponseReasoningSummaryTextDone,
		ItemID:       itemID,
		OutputIndex:  outputIndex,
		SummaryIndex: summaryIndex,
		Text:         text,
	}
	event.SequenceNumber = w.getNextSequenceNumber()
	return w.SendEvent(EventTypeResponseReasoningSummaryTextDone, event)
}

// SendReasoningSummaryPartDone sends a response.reasoning_summary_part.done event
func (w *SSEWriter) SendReasoningSummaryPartDone(
	itemID string,
	outputIndex int,
	summaryIndex int,
	text string,
) error {
	event := &ResponseReasoningSummaryPartDoneEvent{
		Type:         EventTypeResponseReasoningSummaryPartDone,
		ItemID:       itemID,
		OutputIndex:  outputIndex,
		SummaryIndex: summaryIndex,
		Part: ReasoningSummaryPart{
			Type: "summary_text",
			Text: text,
		},
	}
	event.SequenceNumber = w.getNextSequenceNumber()
	return w.SendEvent(EventTypeResponseReasoningSummaryPartDone, event)
}

// SendFunctionCallArgumentsDelta sends a response.function_call_arguments.delta event
func (w *SSEWriter) SendFunctionCallArgumentsDelta(
	itemID string,
	outputIndex int,
	delta string,
) error {
	event := NewResponseFunctionCallArgumentsDeltaEvent(itemID, outputIndex, delta)
	event.SequenceNumber = w.getNextSequenceNumber()
	return w.SendEvent(EventTypeResponseFunctionCallArgumentsDelta, event)
}

// SendFunctionCallArgumentsDone sends a response.function_call_arguments.done event
func (w *SSEWriter) SendFunctionCallArgumentsDone(
	itemID string,
	outputIndex int,
	arguments string,
) error {
	event := NewResponseFunctionCallArgumentsDoneEvent(itemID, outputIndex, arguments)
	event.SequenceNumber = w.getNextSequenceNumber()
	return w.SendEvent(EventTypeResponseFunctionCallArgumentsDone, event)
}

// SendMCPListToolsInProgress sends a response.mcp_list_tools.in_progress event
func (w *SSEWriter) SendMCPListToolsInProgress(itemID string, outputIndex int) error {
	event := &ResponseMCPListToolsInProgressEvent{
		Type:           EventTypeResponseMCPListToolsInProgress,
		ItemID:         itemID,
		OutputIndex:    outputIndex,
		SequenceNumber: w.getNextSequenceNumber(),
	}
	return w.SendEvent(EventTypeResponseMCPListToolsInProgress, event)
}

// SendMCPListToolsCompleted sends a response.mcp_list_tools.completed event
func (w *SSEWriter) SendMCPListToolsCompleted(itemID string, outputIndex int) error {
	event := &ResponseMCPListToolsCompletedEvent{
		Type:           EventTypeResponseMCPListToolsCompleted,
		ItemID:         itemID,
		OutputIndex:    outputIndex,
		SequenceNumber: w.getNextSequenceNumber(),
	}
	return w.SendEvent(EventTypeResponseMCPListToolsCompleted, event)
}

// SendMCPCallInProgress sends a response.mcp_call.in_progress event
func (w *SSEWriter) SendMCPCallInProgress(itemID string, outputIndex int) error {
	event := &ResponseMCPCallInProgressEvent{
		Type:           EventTypeResponseMCPCallInProgress,
		ItemID:         itemID,
		OutputIndex:    outputIndex,
		SequenceNumber: w.getNextSequenceNumber(),
	}
	return w.SendEvent(EventTypeResponseMCPCallInProgress, event)
}

// SendMCPCallCompleted sends a response.mcp_call.completed event
func (w *SSEWriter) SendMCPCallCompleted(itemID string, outputIndex int) error {
	event := &ResponseMCPCallCompletedEvent{
		Type:           EventTypeResponseMCPCallCompleted,
		ItemID:         itemID,
		OutputIndex:    outputIndex,
		SequenceNumber: w.getNextSequenceNumber(),
	}
	return w.SendEvent(EventTypeResponseMCPCallCompleted, event)
}

// SendMCPCallFailed sends a response.mcp_call.failed event
func (w *SSEWriter) SendMCPCallFailed(itemID string, outputIndex int) error {
	event := &ResponseMCPCallFailedEvent{
		Type:           EventTypeResponseMCPCallFailed,
		ItemID:         itemID,
		OutputIndex:    outputIndex,
		SequenceNumber: w.getNextSequenceNumber(),
	}
	return w.SendEvent(EventTypeResponseMCPCallFailed, event)
}

// SendMCPCallArgumentsDelta sends a response.mcp_call_arguments.delta event
func (w *SSEWriter) SendMCPCallArgumentsDelta(itemID string, outputIndex int, delta string) error {
	event := &ResponseMCPCallArgumentsDeltaEvent{
		Type:           EventTypeResponseMCPCallArgumentsDelta,
		ItemID:         itemID,
		OutputIndex:    outputIndex,
		Delta:          delta,
		SequenceNumber: w.getNextSequenceNumber(),
	}
	return w.SendEvent(EventTypeResponseMCPCallArgumentsDelta, event)
}

// SendMCPCallArgumentsDone sends a response.mcp_call_arguments.done event
func (w *SSEWriter) SendMCPCallArgumentsDone(
	itemID string,
	outputIndex int,
	arguments string,
) error {
	event := &ResponseMCPCallArgumentsDoneEvent{
		Type:           EventTypeResponseMCPCallArgumentsDone,
		ItemID:         itemID,
		OutputIndex:    outputIndex,
		Arguments:      arguments,
		SequenceNumber: w.getNextSequenceNumber(),
	}
	return w.SendEvent(EventTypeResponseMCPCallArgumentsDone, event)
}

// SendError sends an error event
func (w *SSEWriter) SendError(code *string, message string, param *string) error {
	return w.SendEvent(EventTypeError, NewResponseErrorEvent(code, message, param))
}

// SendErrorFromError sends an error event from a Go error
func (w *SSEWriter) SendErrorFromError(err error) error {
	return w.SendEvent(EventTypeError, NewResponseErrorEventFromError(err))
}

// SendDone sends the [DONE] signal to indicate stream completion
func (w *SSEWriter) SendDone() error {
	// Create a special event with just the data field
	event := &sse.Event{
		Data: "[DONE]",
	}

	return w.conn.SendEvent(w.ctx, event)
}

// Close closes the SSE writer and cleans up resources
func (w *SSEWriter) Close() {
	// The SSE connection and its heartbeat will be closed by the framework
	// when the context is cancelled or the connection is closed
}
