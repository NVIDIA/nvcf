// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package backend defines the export destination contract and the registry
// that maps a configured backend name to a compiled-in implementation.
package backend

import (
	"context"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/segment"
)

// Client submits one prepared segment and reads its terminal status. Submit
// and Status are separate so a slow confirmation cannot block other segments.
type Client interface {
	Submit(context.Context, SubmitRequest) (string, error)
	Status(context.Context, string) (Status, error)
	// Capabilities declares what this backend guarantees. Core behavior
	// derives from these properties instead of branching on backend identity.
	Capabilities() Capabilities
}

// Capabilities describes one backend's contract with the service that submits
// to it.
type Capabilities struct {
	// ResubmitSafe reports whether Submit can be called again for the same
	// segment after an unconfirmed or failed attempt without side effects
	// beyond the intended upload.
	ResubmitSafe bool
	// TerminalOutcomeSync reports whether Submit does not return until the
	// outcome is durable, so a Status call has nothing left to confirm.
	TerminalOutcomeSync bool
	// OutOfOrderTolerant reports whether the backend accepts segments in an
	// order other than the one segment.Discover produced them in.
	OutOfOrderTolerant bool
	// AcceptedFormats lists the segment encodings this backend can ingest.
	AcceptedFormats []Format
	// MaxObjectBytes bounds one submitted segment. Zero means no bound.
	MaxObjectBytes int64
	// Exports reports whether a successful Submit is a real export whose
	// source segment is safe to delete. A diagnostic backend that reads a
	// segment without sending it anywhere, such as debug, reports false so a
	// successful read never deletes the only copy of real Dynamo output.
	Exports bool
}

// Format identifies a segment encoding a backend can ingest.
type Format string

const (
	// FormatGzipJSONL is the format segment.Discover already produces: a
	// gzip-compressed stream of newline-delimited JSON records.
	FormatGzipJSONL Format = "gzip+jsonl"
)

// SubmitRequest identifies one prepared segment without exposing its contents.
type SubmitRequest struct {
	Segment segment.Segment
	Path    string
}

// Status is an upload operation state.
type Status string

const (
	StatusPending Status = "pending"
	StatusSuccess Status = "success"
	StatusFailure Status = "failure"
)
