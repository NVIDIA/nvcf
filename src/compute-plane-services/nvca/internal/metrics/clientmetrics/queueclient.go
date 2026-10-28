/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package clientmetrics

import (
	"context"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/semconv"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/semconv/msgsemconv"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue"
)

// Messaging operation names, bounded by construction.
const (
	msgOperationReceive    = "receive"
	msgOperationDelete     = "delete"
	msgOperationExtendLock = "extend_visibility"
)

// queueClient decorates a queue.Client so every operation records the messaging
// RED metric set through the shared Recorder.
//
// This is the whole of a new-transport decorator: declare the Family (see
// MessagingClientFamily), resolve its Instruments once, and call Record. No new
// meter, exporter, registry or label plumbing.
type queueClient struct {
	inner       queue.Client
	insts       *Instruments
	peerService string
	system      string
}

// NewQueueClient wraps inner so its operations are instrumented with the
// messaging semconv attribute set, tagged with peerService and system (for
// example msgsemconv.SystemSQS).
//
// It is meter-gated by construction: when rec is nil, inner is returned
// unchanged, so with metrics disabled there is zero overhead.
func NewQueueClient(inner queue.Client, rec *Recorder, peerService, system string) (queue.Client, error) {
	if rec == nil || inner == nil {
		return inner, nil
	}
	insts, err := rec.Instruments(MessagingClientFamily)
	if err != nil {
		return nil, err
	}
	return &queueClient{inner: inner, insts: insts, peerService: peerService, system: system}, nil
}

// destination returns a bounded messaging.destination.name. The queue type is
// used rather than the queue URL, which carries account and GPU specific
// segments and would mint a new series per queue.
func destination(info queue.MessageQueueInfo) string {
	return string(info.QueueType)
}

func (c *queueClient) record(ctx context.Context, operation, dest string, start time.Time, err error) {
	c.insts.Record(ctx, Observation{
		Duration:     time.Since(start),
		RequestSize:  -1,
		ResponseSize: -1,
		Attrs: msgsemconv.ClientAttrs(
			c.peerService, c.system, operation, dest, semconv.ClassifyError(err),
		),
	})
}

// ReceiveMessage delegates to the inner client and records the operation.
func (c *queueClient) ReceiveMessage(ctx context.Context, in queue.ReceiveMessageInput) ([]queue.ReceiveMessageOutput, error) {
	start := time.Now()
	out, err := c.inner.ReceiveMessage(ctx, in)
	c.record(ctx, msgOperationReceive, destination(in.QueueInfo), start, err)
	return out, err
}

// DeleteMessage delegates to the inner client and records the operation.
func (c *queueClient) DeleteMessage(ctx context.Context, in queue.DeleteMessageInput) error {
	start := time.Now()
	err := c.inner.DeleteMessage(ctx, in)
	c.record(ctx, msgOperationDelete, destination(in.QueueInfo), start, err)
	return err
}

// ChangeMessageVisibility delegates to the inner client and records the operation.
func (c *queueClient) ChangeMessageVisibility(ctx context.Context, in queue.ChangeMessageVisibilityInput) error {
	start := time.Now()
	err := c.inner.ChangeMessageVisibility(ctx, in)
	c.record(ctx, msgOperationExtendLock, destination(in.QueueInfo), start, err)
	return err
}

// IsMessageNotFoundError delegates unchanged; it performs no I/O.
func (c *queueClient) IsMessageNotFoundError(err error) bool {
	return c.inner.IsMessageNotFoundError(err)
}
