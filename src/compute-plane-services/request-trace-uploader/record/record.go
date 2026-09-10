// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package record models the Dynamo v1.4.0 request trace format.
//
// Dynamo v1.4.0 writes every record type to one segment family as gzipped
// JSON lines. A record is classified by its event_type, never by the file it
// came from. Dynamo v1.3.x additionally emitted a separate AuditRecord type to
// its own family; that format is detected and rejected rather than parsed.
package record

import "encoding/json"

// Schema is the only schema string Dynamo emits for request traces. It is
// unchanged between v1.3.x and v1.4.0, so it cannot be used to tell the
// versions apart.
const Schema = "dynamo.request.trace.v1"

// EventType classifies a record. Only these five exist in v1.4.0.
type EventType string

const (
	EventRequestEnd     EventType = "request_end"
	EventRequestPayload EventType = "request_payload"
	EventToolStart      EventType = "tool_start"
	EventToolEnd        EventType = "tool_end"
	EventToolError      EventType = "tool_error"
)

// Known reports whether the event type is one this build understands.
// Unrecognized types are still exported, so this only drives counting.
func (e EventType) Known() bool {
	switch e {
	case EventRequestEnd, EventRequestPayload, EventToolStart, EventToolEnd, EventToolError:
		return true
	}
	return false
}

// Record is one Dynamo request trace record.
//
// Raw holds the original bytes. Everything the uploader exports is derived
// from Raw rather than re-serialized, so a record survives a round trip even
// if it carries fields this build does not model.
type Record struct {
	Schema    string          `json:"schema"`
	EventType EventType       `json:"event_type"`
	TimeMS    uint64          `json:"event_time_unix_ms"`
	Source    string          `json:"event_source,omitempty"`
	Agent     *AgentContext   `json:"agent_context,omitempty"`
	Request   *RequestMetrics `json:"request,omitempty"`
	Tool      *ToolEvent      `json:"tool,omitempty"`
	Payload   *Payload        `json:"payload,omitempty"`

	Raw []byte `json:"-"`
}

// AgentContext carries session identity for harness-sourced records.
type AgentContext struct {
	SessionID       string `json:"session_id"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
}

// RequestMetrics is the metadata on a request_end record. It carries no
// request or response body.
type RequestMetrics struct {
	RequestID    string   `json:"request_id"`
	XRequestID   string   `json:"x_request_id,omitempty"`
	Model        string   `json:"model,omitempty"`
	InputTokens  *uint64  `json:"input_tokens,omitempty"`
	OutputTokens *uint64  `json:"output_tokens,omitempty"`
	CachedTokens *uint64  `json:"cached_tokens,omitempty"`
	TTFTMS       *float64 `json:"ttft_ms,omitempty"`
	TotalTimeMS  *float64 `json:"total_time_ms,omitempty"`
	QueueDepth   *uint64  `json:"queue_depth,omitempty"`
}

// ToolEvent is the metadata on a tool_start, tool_end, or tool_error record.
// It carries no request identifier, so tool records correlate by session.
type ToolEvent struct {
	ToolCallID string   `json:"tool_call_id"`
	ToolClass  string   `json:"tool_class"`
	Status     string   `json:"status,omitempty"`
	DurationMS *float64 `json:"duration_ms,omitempty"`
	ErrorType  string   `json:"error_type,omitempty"`
}

// Payload is the request and response content on a request_payload record.
// This is the only record type that carries bodies or headers.
type Payload struct {
	RequestID          string            `json:"request_id"`
	Endpoint           string            `json:"endpoint,omitempty"`
	Model              string            `json:"model,omitempty"`
	Request            json.RawMessage   `json:"request,omitempty"`
	Response           json.RawMessage   `json:"response,omitempty"`
	HTTPRequestHeaders map[string]string `json:"http_request_headers,omitempty"`
	Complete           bool              `json:"payload_complete"`
	DropReason         string            `json:"payload_drop_reason,omitempty"`
}

// RequestID returns the record's request identifier and whether it has one.
//
// The location differs by record type: request_end nests it under request,
// request_payload nests it under payload, and tool records carry none.
func (r *Record) RequestID() (string, bool) {
	if r.Payload != nil && r.Payload.RequestID != "" {
		return r.Payload.RequestID, true
	}
	if r.Request != nil && r.Request.RequestID != "" {
		return r.Request.RequestID, true
	}
	return "", false
}

// SessionID returns the record's session identifier and whether it has one.
func (r *Record) SessionID() (string, bool) {
	if r.Agent != nil && r.Agent.SessionID != "" {
		return r.Agent.SessionID, true
	}
	return "", false
}

// Headers returns the record's captured HTTP headers. Only request_payload
// records carry them, and only those named in Dynamo's capture allowlist.
func (r *Record) Headers() map[string]string {
	if r.Payload == nil {
		return nil
	}
	return r.Payload.HTTPRequestHeaders
}
