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
	"fmt"
	"time"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

const (
	// How many seconds old a message has to be before we discard it.
	dropMessagesOlderThan = 120
)

// RateLimitEventWireFormat represents a rate limit event as transmitted over the sync transport.
type RateLimitEventWireFormat struct {
	Key         string        `json:"key"`
	Units       int64         `json:"units"`
	Rate        int64         `json:"rate"`
	Period      time.Duration `json:"period"`
	RequestID   string        `json:"request_id"`
	ClusterName string        `json:"cluster_name"`
	CreatedAt   int64         `json:"created_at"`
	MustConsume bool          `json:"must_consume"`
}

func ApplySynchronizedEvent(
	ctx context.Context,
	limiter RateLimiter,
	clusterName string,
	writesEnabled bool,
	rle *RateLimitEventWireFormat,
) error {
	if rle == nil {
		return fmt.Errorf("rate limit event is required")
	}

	if clusterName == "" {
		return fmt.Errorf("cluster name must be configured for rate limit synchronization")
	}

	log := telemetry.Logger(ctx)
	lag := time.Since(time.Unix(rle.CreatedAt, 0))

	telemetry.RecordWithContext(
		ctx,
		telemetry.RateLimitEventReplicationLag(),
		lag.Seconds(),
	)
	telemetry.AddWithContext(ctx, telemetry.RateLimitEventsReceived(), 0)
	telemetry.AddWithContext(ctx, telemetry.RateLimitEventsDropped(), 0, telemetry.DropReasonSameCluster())
	telemetry.AddWithContext(ctx, telemetry.RateLimitEventsDropped(), 0, telemetry.DropReasonOldMessage())
	telemetry.AddWithContext(ctx, telemetry.RateLimitEventsDropped(), 0, telemetry.DropReasonRemoteApplyDisabled())
	telemetry.AddWithContext(ctx, telemetry.RateLimitEventsDryRunWouldApply(), 0)
	telemetry.AddWithContext(ctx, telemetry.RateLimitEventsFailedApply(), 0)
	telemetry.AddWithContext(ctx, telemetry.RateLimitEventsApplied(), 0)
	telemetry.AddWithContext(ctx, telemetry.RateLimitEventsReceived(), 1)

	if rle.ClusterName == clusterName {
		log.Debug().
			Str("request_id", rle.RequestID).
			Str("source_cluster", rle.ClusterName).
			Msg("dropping same-cluster rate limit event")
		telemetry.AddWithContext(ctx, telemetry.RateLimitEventsDropped(), 1, telemetry.DropReasonSameCluster())
		return nil
	}

	if lag > dropMessagesOlderThan*time.Second {
		log.Debug().
			Str("request_id", rle.RequestID).
			Dur("lag", lag).
			Msg("dropping too-old rate limit event")
		telemetry.AddWithContext(ctx, telemetry.RateLimitEventsDropped(), 1, telemetry.DropReasonOldMessage())
		return nil
	}

	if !writesEnabled {
		log.Debug().
			Str("request_id", rle.RequestID).
			Msg("dropping rate limit event because remote application is disabled")
		telemetry.AddWithContext(ctx, telemetry.RateLimitEventsDropped(), 1, telemetry.DropReasonRemoteApplyDisabled())
		telemetry.AddWithContext(ctx, telemetry.RateLimitEventsDryRunWouldApply(), 1)
		return nil
	}

	_, err := checkLimitWithoutSync(
		ctx,
		limiter,
		rle.Key,
		RateLimit{
			Limit:  rle.Rate,
			Period: rle.Period,
		},
		rle.Units,
		false,
		rle.RequestID,
		rle.MustConsume,
	)
	if err != nil {
		telemetry.AddWithContext(ctx, telemetry.RateLimitEventsFailedApply(), 1)
		return err
	}
	telemetry.AddWithContext(ctx, telemetry.RateLimitEventsApplied(), 1)
	return err
}

func checkLimitWithoutSync(
	ctx context.Context,
	limiter RateLimiter,
	key string,
	l RateLimit,
	tokensRequested int64,
	testOnly bool,
	requestID string,
	mustConsume bool,
) (*RateLimitResult, error) {
	if rl, ok := limiter.(*rateLimiter); ok {
		return rl.checkLimit(ctx, key, l, tokensRequested, testOnly, requestID, mustConsume, false)
	}

	return limiter.CheckLimit(ctx, key, l, tokensRequested, testOnly, requestID, mustConsume)
}
