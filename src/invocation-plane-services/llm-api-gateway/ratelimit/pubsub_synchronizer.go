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

package ratelimit

import (
	"context"
	"io"
	"sync"
	"time"

	zlog "github.com/rs/zerolog/log"

	orionpubsub "github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/pubsub"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

const (
	// TODO(nhaskins): Tweak as necessary
	publishResultBufferSize    = 2000
	numPublishResultProcessors = 20
)

var _ Synchronizer = (*pubSubSynchronizer)(nil)

// NewPubSubSynchronizer creates a new rate limit synchronizer using PubSub.
// It takes ownership of the client in order to properly close it at shutdown.
// TODO: client ownership can be moved out of this struct if it's able to closed separately.
func NewPubSubSynchronizer(
	client io.Closer,
	publisher orionpubsub.Publisher,
	clusterName string,
) Synchronizer {
	p := &pubSubSynchronizer{
		client:      client,
		publisher:   publisher,
		clusterName: clusterName,
	}

	telemetry.RegisterRateLimitSynchronizerQueueLength(func() int64 {
		if p.resultChan == nil {
			return -1
		}
		return int64(len(p.resultChan))
	})

	return p
}

type pubSubSynchronizer struct {
	client      io.Closer
	publisher   orionpubsub.Publisher
	clusterName string
	wg          sync.WaitGroup
	resultChan  chan *RateLimitEventWireFormat
}

func (s *pubSubSynchronizer) Send(ctx context.Context, rle *RateLimitEvent) error {
	var (
		createdAt = time.Now().Unix()
		data      = RateLimitEventWireFormat{
			Key:         rle.Key,
			Units:       rle.Result.Requested,
			Rate:        rle.Result.RateLimit.Limit,
			Period:      rle.Result.RateLimit.Period,
			RequestID:   rle.RequestID,
			ClusterName: s.clusterName,
			CreatedAt:   createdAt,
			MustConsume: rle.MustConsume,
		}
	)

	queueStart := time.Now()
	s.resultChan <- &data
	telemetry.Record(
		telemetry.RateLimitSynchronizerQueueWait(),
		time.Since(queueStart).Seconds(),
	)

	return nil
}

func (s *pubSubSynchronizer) Start() {
	s.resultChan = make(chan *RateLimitEventWireFormat, publishResultBufferSize)
	for i := range numPublishResultProcessors {
		s.wg.Go(func() {
			s.processor(i)
		})
	}
}

func (s *pubSubSynchronizer) Stop() {
	zlog.Info().Msg("PubSubSynchronizer stopping")
	if s.resultChan != nil {
		close(s.resultChan)
		s.wg.Wait()
	}
	s.client.Close()
}

func (s *pubSubSynchronizer) processor(i int) {
	for {
		// This will block until there is something on the resultChan to pick up.
		data, ok := <-s.resultChan

		if !ok {
			// Channel closed. Stop this goroutine
			zlog.Debug().
				Int("processor id", i).
				Msg("stopping publishresult processing goroutine")
			break
		}

		// Check if the message is too old to be useful
		lag := time.Since(time.Unix(data.CreatedAt, 0)).Seconds()
		if lag > dropMessagesOlderThan {
			zlog.Debug().
				Str("request_id", data.RequestID).
				Float64("lag_seconds", lag).
				Msg("dropping too-old rate limit event in publisher")
			telemetry.Add(
				telemetry.RateLimitSynchronizerEventsDropped(),
				1,
				telemetry.SynchronizerDropReasonOldMessage(),
			)
			continue
		}

		t := time.Now()
		_, err := s.publisher.PublishJSON(context.Background(), data, "")
		if err != nil {
			zlog.Error().Err(err).Msg("failed to publish rate limit event")
			continue
		}
		telemetry.Record(
			telemetry.RateLimitSynchronizerPublishDuration(),
			time.Since(t).Seconds(),
		)
		zlog.Debug().
			Str("request_id", data.RequestID).
			Dur("publish_time", time.Since(t)).
			Msg("published rate limit event")
	}
}
