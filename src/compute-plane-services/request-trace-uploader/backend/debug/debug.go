// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package debug implements a backend that reports what it received and
// exports nothing.
//
// It exists so the read path can be exercised against a real Dynamo without
// credentials, a bucket, or a namespace. Selecting it reads each closed
// segment, reports what was in it, and reports success without transmitting
// anything or deleting the source.
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
type Client struct{}

// New builds the debug backend.
func New(config.Config) (backend.Client, error) { return &Client{}, nil }

// Submit reads the segment and reports what it contains.
//
// It reports counts and shapes only. Request identifiers, session
// identifiers, header values, and record bodies are deliberately absent: this
// is a diagnostic aid, and its output is subject to the same containment rules
// as any other uploader log.
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
