// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package debug implements a backend that reports what it received and
// exports nothing.
//
// It exists so the read path can be exercised against a real Dynamo without
// credentials, a bucket, or a namespace. Selecting it reads each closed
// segment, reports what was in it, and reports success without transmitting
// anything or deleting the source.
//
// config.EnvDebugVerbosity controls how much it logs. The default,
// config.DebugVerbosityBasic, logs one summary line per segment.
// config.DebugVerbosityDetailed adds one line per record. Both levels omit
// request identifiers, session identifiers, header values, and request or
// response bodies: verbosity changes resolution, not what is safe to log.
package debug

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/record"
)

func init() {
	backend.Register(config.BackendDebug, New)
}

// Client reports segments instead of exporting them.
type Client struct {
	verbosity config.DebugVerbosity
}

// New builds the debug backend. Unset or unrecognized verbosity behaves as
// config.DebugVerbosityBasic: an empty Config, such as one built directly in
// a test rather than through config.Load, still gets the safe default.
func New(cfg config.Config) (backend.Client, error) {
	return &Client{verbosity: cfg.DebugVerbosity}, nil
}

// Submit reads the segment and reports what it contains.
//
// At config.DebugVerbosityBasic, the default, it reports counts and shapes
// only. At config.DebugVerbosityDetailed it also logs one line per record
// with non-identifying metrics and metadata. Neither level ever logs a
// request identifier, a session identifier, a header value, or a request or
// response body: this is a diagnostic aid, and its output is subject to the
// same containment rules as any other uploader log regardless of verbosity.
func (c *Client) Submit(ctx context.Context, request backend.SubmitRequest) (string, error) {
	reader, err := record.Open(request.Path)
	if err != nil {
		return "", fmt.Errorf("debug backend: %w", err)
	}
	defer reader.Close()

	withRequestID := 0
	withSession := 0
	withHeaders := 0
	incomplete := 0
	index := 0

	// A segment can hold many records, so a scan must not outlive a shutdown.
	// The check runs before each advance and once more after the loop, so a
	// cancellation that lands while the final read reaches the end of the
	// segment is still reported rather than returning success.
	for {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("debug backend: stop reading segment: %w", err)
		}
		if !reader.Next() {
			break
		}
		rec := reader.Record()
		if _, ok := rec.RequestID(); ok {
			withRequestID++
		}
		if _, ok := rec.SessionID(); ok {
			withSession++
		}
		if len(rec.Headers()) > 0 {
			withHeaders++
		}
		if rec.Payload != nil && !rec.Payload.Complete {
			incomplete++
		}
		if c.verbosity == config.DebugVerbosityDetailed {
			logRecord(request.Segment.Index, index, rec)
		}
		index++
	}
	if err := reader.Err(); err != nil {
		return "", fmt.Errorf("debug backend: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("debug backend: stop reading segment: %w", err)
	}

	stats := reader.Stats()
	slog.Info("debug backend read a segment",
		"segment", request.Segment.Index,
		"records", stats.Records,
		"record_bytes", stats.Bytes,
		"unparseable", stats.Unparseable,
		"unknown_event_types", stats.Unknown,
		"with_request_id", withRequestID,
		"with_session_id", withSession,
		"with_headers", withHeaders,
		"incomplete_payloads", incomplete,
		"by_event_type", formatCounts(stats.ByEventType))

	return fmt.Sprintf("debug-%d", request.Segment.Index), nil
}

// Status reports success. Nothing was transmitted, so nothing can be pending.
func (c *Client) Status(context.Context, string) (backend.Status, error) {
	return backend.StatusSuccess, nil
}

// Capabilities declares that debug exports nothing, so a caller must never
// delete a source segment on the strength of a debug Submit succeeding.
func (c *Client) Capabilities() backend.Capabilities {
	return backend.Capabilities{
		ResubmitSafe:        true,
		TerminalOutcomeSync: true,
		OutOfOrderTolerant:  true,
		AcceptedFormats:     []backend.Format{backend.FormatGzipJSONL},
		Exports:             false,
	}
}

// maxLoggedFieldBytes bounds a free-text metadata field before logRecord logs
// it. Model, ToolClass, Status, and DropReason come straight from segment
// JSON with no upstream length validation, so a malformed or adversarial
// record could otherwise put an arbitrarily large value in a field this
// backend treats as short metadata, inflating one log line without limit.
const maxLoggedFieldBytes = 256

// truncate bounds a string before it is logged. It cuts on a byte boundary
// rather than a rune boundary: precise multi-byte truncation is not worth the
// complexity for a diagnostic aid, and a split multi-byte character at the
// boundary is still visibly marked as truncated.
func truncate(s string) string {
	if len(s) <= maxLoggedFieldBytes {
		return s
	}
	return s[:maxLoggedFieldBytes] + "...(truncated)"
}

// logRecord logs one detailed-verbosity line for a single record. It carries
// non-identifying metrics and metadata only: token counts, timing, model
// name, tool class and status, and whether a payload is complete. It never
// logs a request identifier, a session identifier, a header value, or a
// request or response body, matching the containment rule Submit's basic
// summary already follows. Free-text fields are bounded by truncate.
func logRecord(segment, index int, rec *record.Record) {
	args := []any{
		"segment", segment,
		"record", index,
		"event_type", rec.EventType,
		"bytes", len(rec.Raw),
	}
	if _, ok := rec.RequestID(); ok {
		args = append(args, "has_request_id", true)
	}
	if _, ok := rec.SessionID(); ok {
		args = append(args, "has_session_id", true)
	}
	if len(rec.Headers()) > 0 {
		args = append(args, "has_headers", true)
	}
	if req := rec.Request; req != nil {
		if req.Model != "" {
			args = append(args, "model", truncate(req.Model))
		}
		if req.InputTokens != nil {
			args = append(args, "input_tokens", *req.InputTokens)
		}
		if req.OutputTokens != nil {
			args = append(args, "output_tokens", *req.OutputTokens)
		}
		if req.CachedTokens != nil {
			args = append(args, "cached_tokens", *req.CachedTokens)
		}
		if req.TTFTMS != nil {
			args = append(args, "ttft_ms", *req.TTFTMS)
		}
		if req.TotalTimeMS != nil {
			args = append(args, "total_time_ms", *req.TotalTimeMS)
		}
		if req.QueueDepth != nil {
			args = append(args, "queue_depth", *req.QueueDepth)
		}
	}
	if tool := rec.Tool; tool != nil {
		if tool.ToolClass != "" {
			args = append(args, "tool_class", truncate(tool.ToolClass))
		}
		if tool.Status != "" {
			args = append(args, "status", truncate(tool.Status))
		}
		if tool.DurationMS != nil {
			args = append(args, "duration_ms", *tool.DurationMS)
		}
	}
	if payload := rec.Payload; payload != nil {
		args = append(args, "payload_complete", payload.Complete)
		if payload.Model != "" {
			args = append(args, "model", truncate(payload.Model))
		}
		if payload.DropReason != "" {
			args = append(args, "drop_reason", truncate(payload.DropReason))
		}
	}
	slog.Info("debug backend read a record", args...)
}

// formatCounts renders the per-event-type counts in a stable order so log
// lines from different runs compare directly.
func formatCounts(counts map[record.EventType]int) string {
	if len(counts) == 0 {
		return "none"
	}
	types := make([]string, 0, len(counts))
	for eventType := range counts {
		types = append(types, string(eventType))
	}
	sort.Strings(types)

	out := ""
	for i, eventType := range types {
		if i > 0 {
			out += " "
		}
		out += fmt.Sprintf("%s=%d", eventType, counts[record.EventType(eventType)])
	}
	return out
}
