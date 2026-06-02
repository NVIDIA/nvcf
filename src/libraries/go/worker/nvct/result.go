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

package nvct

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"golang.org/x/oauth2"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/auth"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvct"
)

type StreamingClient struct {
	globalCtx       context.Context
	rwlock          sync.RWMutex
	lastMessageSent atomic.Pointer[time.Time]
	streamer        pb.Worker_SendResultMetadataClient
	nvctClient      pb.WorkerClient
	tokenSource     oauth2.TokenSource
}

// ------------------------------------------------------------------------

type Flusher interface {
	Flush() error
}

// ------------------------------------------------------------------------

// Constructor
func NewStreamingClient(ctx context.Context, nvctClient pb.WorkerClient, ts oauth2.TokenSource) *StreamingClient {
	// For gracefully shut down the streaming client in case the parent context is cancelled
	ctx = context.WithoutCancel(ctx)

	return &StreamingClient{
		globalCtx:   ctx,
		nvctClient:  nvctClient,
		tokenSource: ts,
	}
}

// ------------------------------------------------------------------------

// Send task result to NVCT server
func (c *StreamingClient) SendResult(request *pb.ResultMetadataRequest) error {
	zap.L().Info("Sending task result", zap.String("task id", request.TaskId))

	streamer, err := c.getStreamer(c.globalCtx)
	if err != nil {
		return err
	}

	if err = backoff.Retry(func() error {
		return streamer.Send(request)
	}, backoff.WithContext(backoff.NewExponentialBackOff(
		backoff.WithInitialInterval(1*time.Second),
		backoff.WithMaxElapsedTime(30*time.Second)),
		c.globalCtx,
	)); err != nil {
		zap.L().Error("failed to send task results", zap.Error(err))
		return err
	}
	c.lastMessageSent.Store(lo.ToPtr(time.Now()))
	return nil
}

// ------------------------------------------------------------------------

// Get up-to-date streamer
func (c *StreamingClient) getStreamer(ctx context.Context) (pb.Worker_SendResultMetadataClient, error) {
	c.clearOldStreamer()
	c.rwlock.RLock()
	if c.streamer != nil {
		defer c.rwlock.RUnlock()
		return c.streamer, nil
	}
	c.rwlock.RUnlock()

	c.rwlock.Lock()
	defer c.rwlock.Unlock()
	if c.streamer != nil {
		return c.streamer, nil
	}

	streamer, err := c.nvctClient.SendResultMetadata(ctx, auth.GrpcTokenFromSource(c.tokenSource))
	if err != nil {
		zap.L().Error("failed to create streaming request for sending results", zap.Error(err))
		return nil, err
	}
	c.streamer = streamer
	return c.streamer, nil
}

// ------------------------------------------------------------------------

// Clear old streamer which hasn't been used in the past minute
// since it will get timed out by ALB
func (c *StreamingClient) clearOldStreamer() {
	lastMessageSent := c.lastMessageSent.Load()
	if lastMessageSent != nil && time.Since(*lastMessageSent) > 50*time.Second {
		c.rwlock.Lock()
		defer c.rwlock.Unlock()
		lastMessageSent = c.lastMessageSent.Load() // check again in case there was a lock stampede
		if lastMessageSent != nil && time.Since(*lastMessageSent) > 50*time.Second {
			if c.streamer != nil {
				_, err := c.streamer.CloseAndRecv()
				if err != nil {
					zap.L().Warn("streamer send not closed successfully", zap.Error(err))
				}
				c.streamer = nil
			}
		}
	}
}

// ------------------------------------------------------------------------

// Close will flush out any messages not sent and wait for either an ack or an error
func (c *StreamingClient) Close() error {
	c.rwlock.Lock()
	defer c.rwlock.Unlock()
	if c.streamer == nil {
		return nil
	}

	closeCtx, cancel := context.WithTimeout(c.globalCtx, 10*time.Second)
	defer cancel()

	closeCh := lo.Async(func() error {
		_, err := c.streamer.CloseAndRecv()
		c.streamer = nil
		return err
	})

	select {
	case err := <-closeCh:
		if err != nil {
			zap.L().Error("failed to flush results", zap.Error(err))
			return err
		}
	case <-closeCtx.Done():
		zap.L().Error("timeout when closing result streamer", zap.Error(closeCtx.Err()))
		c.streamer = nil
		return closeCtx.Err()
	}

	return nil
}

// ------------------------------------------------------------------------

// Flush messages
func (c *StreamingClient) Flush() error {
	return c.Close()
}
