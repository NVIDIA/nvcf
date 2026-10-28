// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package uploadertest

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/record"
)

// The structs below mirror the Dynamo v1.4.0 serde types field for field and
// in declaration order, so marshaled lines match what Dynamo writes. They are
// independent of the record package on purpose: fixtures must not inherit a
// mistake in the reader's model.
type dynamoRecord struct {
	Schema       string         `json:"schema"`
	EventType    string         `json:"event_type"`
	TimeMS       uint64         `json:"event_time_unix_ms"`
	Source       string         `json:"event_source,omitempty"`
	AgentContext *dynamoAgent   `json:"agent_context,omitempty"`
	Request      *dynamoRequest `json:"request,omitempty"`
	Tool         *dynamoTool    `json:"tool,omitempty"`
	Payload      *dynamoPayload `json:"payload,omitempty"`
}

type dynamoAgent struct {
	SessionID string `json:"session_id"`
}

type dynamoRequest struct {
	RequestID    string  `json:"request_id"`
	Model        string  `json:"model,omitempty"`
	InputTokens  uint64  `json:"input_tokens,omitempty"`
	OutputTokens uint64  `json:"output_tokens,omitempty"`
	TotalTimeMS  float64 `json:"total_time_ms,omitempty"`
}

type dynamoTool struct {
	ToolCallID string  `json:"tool_call_id"`
	ToolClass  string  `json:"tool_class"`
	StartedAt  uint64  `json:"started_at_unix_ms,omitempty"`
	Status     string  `json:"status,omitempty"`
	DurationMS float64 `json:"duration_ms,omitempty"`
	ErrorType  string  `json:"error_type,omitempty"`
}

type dynamoPayload struct {
	RequestID string            `json:"request_id"`
	Endpoint  string            `json:"endpoint"`
	Model     string            `json:"model"`
	Request   json.RawMessage   `json:"request,omitempty"`
	Response  json.RawMessage   `json:"response,omitempty"`
	Headers   map[string]string `json:"http_request_headers,omitempty"`
	Complete  bool              `json:"payload_complete"`
}

const (
	fixtureModel    = "test-model"
	fixtureRequest  = `{"model":"test-model","messages":[{"role":"user","content":"synthetic prompt"}]}`
	fixtureResponse = `{"id":"chatcmpl-test","object":"chat.completion","model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"synthetic reply"},"finish_reason":"stop"}]}`
)

func marshalLine(v any) string {
	line, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("uploadertest: marshal fixture: %v", err))
	}
	return string(line)
}

// RequestEnd returns a bare request_end record for requestID with fixed,
// synthetic metrics.
func RequestEnd(requestID string, timeMS uint64) string {
	return marshalLine(dynamoRecord{
		Schema:    record.Schema,
		EventType: string(record.EventRequestEnd),
		TimeMS:    timeMS,
		Source:    "dynamo",
		Request: &dynamoRequest{
			RequestID:    requestID,
			Model:        fixtureModel,
			InputTokens:  12,
			OutputTokens: 34,
			TotalTimeMS:  25.5,
		},
	})
}

// RequestPayload returns a bare request_payload record for requestID with a
// synthetic chat request and response. Nil headers omits the header map, as
// Dynamo does when no allowlisted header was captured.
func RequestPayload(requestID string, timeMS uint64, headers map[string]string) string {
	return marshalLine(dynamoRecord{
		Schema:    record.Schema,
		EventType: string(record.EventRequestPayload),
		TimeMS:    timeMS,
		Source:    "dynamo",
		Payload: &dynamoPayload{
			RequestID: requestID,
			Endpoint:  "openai.chat_completion",
			Model:     fixtureModel,
			Request:   json.RawMessage(fixtureRequest),
			Response:  json.RawMessage(fixtureResponse),
			Headers:   headers,
			Complete:  true,
		},
	})
}

// Tool returns a bare tool record of eventType for sessionID. tool_start is
// running, tool_end succeeded, and tool_error failed with a timeout; any other
// event type gets the bare tool identity, which suits unknown-type tests.
func Tool(eventType record.EventType, sessionID, toolCallID string, timeMS uint64) string {
	tool := &dynamoTool{ToolCallID: toolCallID, ToolClass: "search"}
	switch eventType {
	case record.EventToolStart:
		tool.StartedAt = timeMS
		tool.Status = "running"
	case record.EventToolEnd:
		tool.Status = "succeeded"
		tool.DurationMS = 12.5
	case record.EventToolError:
		tool.Status = "error"
		tool.ErrorType = "timeout"
	}
	return marshalLine(dynamoRecord{
		Schema:       record.Schema,
		EventType:    string(eventType),
		TimeMS:       timeMS,
		Source:       "harness",
		AgentContext: &dynamoAgent{SessionID: sessionID},
		Tool:         tool,
	})
}

// Wrap returns line inside the envelope the Dynamo v1.4.0 gzip file sink
// writes around every record. sinceStartMS is the writer's time since start,
// not wall time.
func Wrap(line string, sinceStartMS uint64) string {
	return marshalLine(struct {
		Timestamp uint64          `json:"timestamp"`
		Event     json.RawMessage `json:"event"`
	}{sinceStartMS, json.RawMessage(line)})
}

// LegacyAuditRecord returns a Dynamo v1.3.x AuditRecord line. v1.4.0 removed
// that type, so a reader must reject it rather than parse it.
func LegacyAuditRecord(requestID string) string {
	return marshalLine(struct {
		SchemaVersion      uint32 `json:"schema_version"`
		RequestID          string `json:"request_id"`
		RequestedStreaming bool   `json:"requested_streaming"`
		Model              string `json:"model"`
	}{1, requestID, false, fixtureModel})
}

// SegmentBuilder assembles a segment file in memory. Dynamo writes one gzip
// member per flush, so each Member call is one flush. The zero value is an
// empty, zero-length segment.
type SegmentBuilder struct {
	buf bytes.Buffer
}

// NewSegment returns an empty SegmentBuilder.
func NewSegment() *SegmentBuilder { return &SegmentBuilder{} }

// Member appends one complete gzip member holding lines, each newline
// terminated.
func (b *SegmentBuilder) Member(lines ...string) *SegmentBuilder {
	b.buf.Write(gzipMember(lines))
	return b
}

// PartialMember appends a member cut off halfway through its compressed
// data, as a writer killed mid-flush or a full disk leaves it. As the last
// member it is a truncated tail; followed by more members it is a partial
// member mid-file.
func (b *SegmentBuilder) PartialMember(lines ...string) *SegmentBuilder {
	member := gzipMember(lines)
	b.buf.Write(member[:len(member)/2])
	return b
}

// Bytes returns the segment built so far.
func (b *SegmentBuilder) Bytes() []byte {
	return bytes.Clone(b.buf.Bytes())
}

// gzipMember compresses lines into one member. The header carries no name or
// time, so equal lines always give equal bytes.
func gzipMember(lines []string) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	for _, line := range lines {
		if _, err := zw.Write([]byte(line + "\n")); err != nil {
			panic(fmt.Sprintf("uploadertest: compress fixture: %v", err))
		}
	}
	if err := zw.Close(); err != nil {
		panic(fmt.Sprintf("uploadertest: compress fixture: %v", err))
	}
	return buf.Bytes()
}

// Spool is a Dynamo spool directory. Segments are named
// <Prefix>.<index>.jsonl.gz with the index zero padded to at least six digits.
type Spool struct {
	Dir    string
	Prefix string
	t      testing.TB
}

// NewSpool returns an empty spool in a fresh temporary directory.
func NewSpool(t testing.TB, prefix string) *Spool {
	t.Helper()
	return &Spool{Dir: t.TempDir(), Prefix: prefix, t: t}
}

// Roll writes data as the next segment and returns its path. Like Dynamo, the
// next index is one past the highest index present for the prefix, or 0 when
// there is none, so rolling after a simulated restart continues the numbering.
func (s *Spool) Roll(data []byte) string {
	s.t.Helper()
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		s.t.Fatalf("uploadertest: read spool: %v", err)
	}
	pattern := regexp.MustCompile("^" + regexp.QuoteMeta(s.Prefix) + `\.(\d{6,})\.jsonl\.gz$`)
	next := 0
	for _, entry := range entries {
		match := pattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		index, err := strconv.Atoi(match[1])
		if err != nil {
			s.t.Fatalf("uploadertest: parse spool index %q: %v", match[1], err)
		}
		next = max(next, index+1)
	}
	return s.WriteAt(next, data)
}

// WriteAt writes data as the segment with the given index and returns its
// path, replacing any segment already there.
func (s *Spool) WriteAt(index int, data []byte) string {
	s.t.Helper()
	path := filepath.Join(s.Dir, fmt.Sprintf("%s.%06d.jsonl.gz", s.Prefix, index))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		s.t.Fatalf("uploadertest: write segment: %v", err)
	}
	return path
}
