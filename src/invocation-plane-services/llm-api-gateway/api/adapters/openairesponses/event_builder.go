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
	"fmt"
)

// EventBuilder helps create events with proper error handling and logging
type EventBuilder struct {
	sseWriter *SSEWriter
}

// NewEventBuilder creates a new event builder
func NewEventBuilder(sseWriter *SSEWriter) *EventBuilder {
	return &EventBuilder{
		sseWriter: sseWriter,
	}
}

// SendResponseCreated sends a response.created event
func (eb *EventBuilder) SendResponseCreated(response *Response) error {
	if err := eb.sseWriter.SendResponseCreated(response); err != nil {
		return fmt.Errorf("failed to send response.created event: %w", err)
	}
	return nil
}

// SendResponseInProgress sends a response.in_progress event
func (eb *EventBuilder) SendResponseInProgress(response *Response) error {
	if err := eb.sseWriter.SendResponseInProgress(response); err != nil {
		return fmt.Errorf("failed to send response.in_progress event: %w", err)
	}
	return nil
}

// SendResponseCompleted sends a response.completed event
func (eb *EventBuilder) SendResponseCompleted(response *Response) error {
	if err := eb.sseWriter.SendResponseCompleted(response); err != nil {
		return fmt.Errorf("failed to send response.completed event: %w", err)
	}
	return nil
}

// SendResponseFailed sends a response.failed event
func (eb *EventBuilder) SendResponseFailed(response *Response) error {
	if err := eb.sseWriter.SendResponseFailed(response); err != nil {
		return fmt.Errorf("failed to send response.failed event: %w", err)
	}
	return nil
}

// SendOutputItemAdded sends an output_item.added event
func (eb *EventBuilder) SendOutputItemAdded(index int, item OutputItem) error {
	if err := eb.sseWriter.SendOutputItemAdded(index, item); err != nil {
		return fmt.Errorf("failed to send output_item.added event: %w", err)
	}
	return nil
}

// SendOutputItemDone sends an output_item.done event
func (eb *EventBuilder) SendOutputItemDone(index int, item OutputItem) error {
	if err := eb.sseWriter.SendOutputItemDone(index, item); err != nil {
		return fmt.Errorf("failed to send output_item.done event: %w", err)
	}
	return nil
}

// SendContentPartAdded sends a content_part.added event
func (eb *EventBuilder) SendContentPartAdded(
	itemID string,
	outputIndex int,
	contentIndex int,
	part OutputContent,
) error {
	if err := eb.sseWriter.SendContentPartAdded(itemID, outputIndex, contentIndex, part); err != nil {
		return fmt.Errorf("failed to send content_part.added event: %w", err)
	}
	return nil
}

// SendContentPartDone sends a content_part.done event
func (eb *EventBuilder) SendContentPartDone(
	itemID string,
	outputIndex int,
	contentIndex int,
	part OutputContent,
) error {
	if err := eb.sseWriter.SendContentPartDone(itemID, outputIndex, contentIndex, part); err != nil {
		return fmt.Errorf("failed to send content_part.done event: %w", err)
	}
	return nil
}

// SendOutputTextDelta sends an output_text.delta event
func (eb *EventBuilder) SendOutputTextDelta(
	itemID string,
	outputIndex int,
	contentIndex int,
	delta string,
) error {
	if err := eb.sseWriter.SendOutputTextDelta(itemID, outputIndex, contentIndex, delta); err != nil {
		return fmt.Errorf(
			"failed to send output_text.delta event (item=%s, output=%d, content=%d): %w",
			itemID,
			outputIndex,
			contentIndex,
			err,
		)
	}
	return nil
}

// SendOutputTextDone sends an output_text.done event
func (eb *EventBuilder) SendOutputTextDone(
	itemID string,
	outputIndex int,
	contentIndex int,
	text string,
) error {
	if err := eb.sseWriter.SendOutputTextDone(itemID, outputIndex, contentIndex, text); err != nil {
		return fmt.Errorf(
			"failed to send output_text.done event (item=%s, output=%d, content=%d): %w",
			itemID,
			outputIndex,
			contentIndex,
			err,
		)
	}
	return nil
}

// SendReasoningSummaryPartAdded sends a response.reasoning_summary_part.added event
func (eb *EventBuilder) SendReasoningSummaryPartAdded(
	itemID string,
	outputIndex int,
	summaryIndex int,
) error {
	if err := eb.sseWriter.SendReasoningSummaryPartAdded(itemID, outputIndex, summaryIndex); err != nil {
		return fmt.Errorf(
			"failed to send reasoning_summary_part.added event (item=%s, output=%d, summary=%d): %w",
			itemID,
			outputIndex,
			summaryIndex,
			err,
		)
	}
	return nil
}

// SendReasoningSummaryTextDelta sends a response.reasoning_summary_text.delta event
func (eb *EventBuilder) SendReasoningSummaryTextDelta(
	itemID string,
	outputIndex int,
	summaryIndex int,
	delta string,
) error {
	if err := eb.sseWriter.SendReasoningSummaryTextDelta(itemID, outputIndex, summaryIndex, delta); err != nil {
		return fmt.Errorf(
			"failed to send reasoning_summary_text.delta event (item=%s, output=%d, summary=%d, delta_len=%d): %w",
			itemID,
			outputIndex,
			summaryIndex,
			len(delta),
			err,
		)
	}
	return nil
}

// SendReasoningSummaryTextDone sends a response.reasoning_summary_text.done event
func (eb *EventBuilder) SendReasoningSummaryTextDone(
	itemID string,
	outputIndex int,
	summaryIndex int,
	text string,
) error {
	if err := eb.sseWriter.SendReasoningSummaryTextDone(itemID, outputIndex, summaryIndex, text); err != nil {
		return fmt.Errorf(
			"failed to send reasoning_summary_text.done event (item=%s, output=%d, summary=%d, text_len=%d): %w",
			itemID,
			outputIndex,
			summaryIndex,
			len(text),
			err,
		)
	}
	return nil
}

// SendReasoningSummaryPartDone sends a response.reasoning_summary_part.done event
func (eb *EventBuilder) SendReasoningSummaryPartDone(
	itemID string,
	outputIndex int,
	summaryIndex int,
	text string,
) error {
	if err := eb.sseWriter.SendReasoningSummaryPartDone(itemID, outputIndex, summaryIndex, text); err != nil {
		return fmt.Errorf(
			"failed to send reasoning_summary_part.done event (item=%s, output=%d, summary=%d, text_len=%d): %w",
			itemID,
			outputIndex,
			summaryIndex,
			len(text),
			err,
		)
	}
	return nil
}

// SendReasoningTextDelta sends a response.reasoning_text.delta event
func (eb *EventBuilder) SendReasoningTextDelta(
	itemID string,
	outputIndex int,
	contentIndex int,
	delta string,
) error {
	if err := eb.sseWriter.SendReasoningTextDelta(itemID, outputIndex, contentIndex, delta); err != nil {
		return fmt.Errorf(
			"failed to send reasoning_text.delta event (item=%s, output=%d, content=%d, delta_len=%d): %w",
			itemID,
			outputIndex,
			contentIndex,
			len(delta),
			err,
		)
	}
	return nil
}

// SendReasoningTextDone sends a response.reasoning_text.done event
func (eb *EventBuilder) SendReasoningTextDone(
	itemID string,
	outputIndex int,
	contentIndex int,
	text string,
) error {
	if err := eb.sseWriter.SendReasoningTextDone(itemID, outputIndex, contentIndex, text); err != nil {
		return fmt.Errorf(
			"failed to send reasoning_text.done event (item=%s, output=%d, content=%d, text_len=%d): %w",
			itemID,
			outputIndex,
			contentIndex,
			len(text),
			err,
		)
	}
	return nil
}

// SendFunctionCallArgumentsDelta sends a function_call_arguments.delta event
func (eb *EventBuilder) SendFunctionCallArgumentsDelta(
	callID string,
	outputIndex int,
	delta string,
) error {
	if err := eb.sseWriter.SendFunctionCallArgumentsDelta(callID, outputIndex, delta); err != nil {
		return fmt.Errorf("failed to send function_call_arguments.delta event: %w", err)
	}
	return nil
}

// SendFunctionCallArgumentsDone sends a function_call_arguments.done event
func (eb *EventBuilder) SendFunctionCallArgumentsDone(
	callID string,
	outputIndex int,
	arguments string,
) error {
	err := eb.sseWriter.SendFunctionCallArgumentsDone(callID, outputIndex, arguments)
	if err != nil {
		return fmt.Errorf("failed to send function_call_arguments.done event: %w", err)
	}
	return nil
}

// SendError sends an error event
func (eb *EventBuilder) SendError(code *string, message string, param *string) error {
	if err := eb.sseWriter.SendError(code, message, param); err != nil {
		return fmt.Errorf("failed to send error event: %w", err)
	}
	return nil
}

// SendMCPListToolsInProgress sends an mcp_list_tools.in_progress event
func (eb *EventBuilder) SendMCPListToolsInProgress(itemID string, outputIndex int) error {
	if err := eb.sseWriter.SendMCPListToolsInProgress(itemID, outputIndex); err != nil {
		return fmt.Errorf("failed to send mcp_list_tools.in_progress event: %w", err)
	}
	return nil
}

// SendMCPListToolsCompleted sends an mcp_list_tools.completed event
func (eb *EventBuilder) SendMCPListToolsCompleted(itemID string, outputIndex int) error {
	if err := eb.sseWriter.SendMCPListToolsCompleted(itemID, outputIndex); err != nil {
		return fmt.Errorf("failed to send mcp_list_tools.completed event: %w", err)
	}
	return nil
}

// SendMCPCallInProgress sends an mcp_call.in_progress event
func (eb *EventBuilder) SendMCPCallInProgress(itemID string, outputIndex int) error {
	if err := eb.sseWriter.SendMCPCallInProgress(itemID, outputIndex); err != nil {
		return fmt.Errorf("failed to send mcp_call.in_progress event: %w", err)
	}
	return nil
}

// SendMCPCallCompleted sends an mcp_call.completed event
func (eb *EventBuilder) SendMCPCallCompleted(itemID string, outputIndex int) error {
	if err := eb.sseWriter.SendMCPCallCompleted(itemID, outputIndex); err != nil {
		return fmt.Errorf("failed to send mcp_call.completed event: %w", err)
	}
	return nil
}

// SendMCPCallFailed sends an mcp_call.failed event
func (eb *EventBuilder) SendMCPCallFailed(itemID string, outputIndex int) error {
	if err := eb.sseWriter.SendMCPCallFailed(itemID, outputIndex); err != nil {
		return fmt.Errorf("failed to send mcp_call.failed event: %w", err)
	}
	return nil
}

// SendMCPCallArgumentsDelta sends an mcp_call_arguments.delta event
func (eb *EventBuilder) SendMCPCallArgumentsDelta(
	itemID string,
	outputIndex int,
	delta string,
) error {
	if err := eb.sseWriter.SendMCPCallArgumentsDelta(itemID, outputIndex, delta); err != nil {
		return fmt.Errorf("failed to send mcp_call_arguments.delta event: %w", err)
	}
	return nil
}

// SendMCPCallArgumentsDone sends an mcp_call_arguments.done event
func (eb *EventBuilder) SendMCPCallArgumentsDone(
	itemID string,
	outputIndex int,
	arguments string,
) error {
	if err := eb.sseWriter.SendMCPCallArgumentsDone(itemID, outputIndex, arguments); err != nil {
		return fmt.Errorf("failed to send mcp_call_arguments.done event: %w", err)
	}
	return nil
}

// SendErrorFromError sends an error event from a Go error
func (eb *EventBuilder) SendErrorFromError(err error) error {
	if sendErr := eb.sseWriter.SendErrorFromError(err); sendErr != nil {
		return fmt.Errorf("failed to send error event: %w", sendErr)
	}
	return nil
}
