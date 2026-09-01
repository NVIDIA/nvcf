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
// The initial scaffold intentionally does not provide an implementation.
type Client interface {
	Submit(context.Context, SubmitRequest) (string, error)
	Status(context.Context, string) (Status, error)
}

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
