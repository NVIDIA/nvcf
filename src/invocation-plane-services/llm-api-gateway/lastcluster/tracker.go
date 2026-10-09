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

package lastcluster

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/requestctx"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

// DefaultWriteTimeout bounds one background store write.
const DefaultWriteTimeout = time.Second

// Options configures a Tracker.
type Options struct {
	TTL           time.Duration
	LookupTimeout time.Duration
	// WriteTimeout bounds one background write. Zero uses DefaultWriteTimeout.
	WriteTimeout time.Duration
}

// Tracker looks up and records the last cluster per session. A nil Tracker is
// valid and does nothing.
type Tracker struct {
	store Store
	opts  Options

	// inflight counts background writes. idle is closed when it drops to
	// zero. A WaitGroup is not used because writes may start while Drain is
	// waiting during shutdown.
	mu       sync.Mutex
	inflight int
	idle     chan struct{}
}

func NewTracker(store Store, opts Options) *Tracker {
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = DefaultWriteTimeout
	}
	return &Tracker{store: store, opts: opts}
}

// eligibleSource reports whether a session source yields a key that stays
// stable across turns. Payload-derived keys hash the full message list, which
// changes every turn, so they never return.
func eligibleSource(source string) bool {
	switch source {
	case requestctx.SessionSourcePromptCacheKey,
		requestctx.SessionSourceConversationID,
		requestctx.SessionSourceHeader:
		return true
	default:
		return false
	}
}

type lookupResult struct {
	value string
	ok    bool
	err   error
}

// Lookup returns the store key for the session and the remembered cluster ID.
// key is empty when the request has no eligible session, in which case no
// write should follow. clusterID is empty on a miss, error, or timeout. The
// lookup never takes longer than the configured lookup timeout.
func (t *Tracker) Lookup(
	ctx context.Context,
	reqCtx *requestctx.RequestContext,
	model string,
) (key string, clusterID string) {
	if t == nil || reqCtx == nil || reqCtx.CacheAffinityKey == "" {
		return "", ""
	}
	if !eligibleSource(reqCtx.SessionSource) || model == "" {
		telemetry.RecordLastClusterLookup(ctx, telemetry.LastClusterLookupSkipped, 0)
		return "", ""
	}
	// The deadline starts before any span or key work so the whole lookup,
	// not only the store call, fits in the lookup timeout.
	start := time.Now()
	lookupCtx, cancel := context.WithDeadline(ctx, start.Add(t.opts.LookupTimeout))
	defer cancel()
	key = Key(reqCtx.RoutingKey, model, reqCtx.CacheAffinityKey)

	ctx, span := telemetry.Tracer().Start(ctx, "llm-api-gateway.last_cluster_lookup")
	defer span.End()
	setFunctionID(span, reqCtx.RoutingKey)
	lookupCtx = trace.ContextWithSpan(lookupCtx, span)

	// The store call runs in its own goroutine so a store that ignores
	// context cancellation still cannot hold the request past the deadline.
	done := make(chan lookupResult, 1)
	go func() {
		value, ok, err := t.store.Get(lookupCtx, key)
		done <- lookupResult{value: value, ok: ok, err: err}
	}()

	var res lookupResult
	select {
	case res = <-done:
	case <-lookupCtx.Done():
		// Prefer a result that landed at the deadline.
		select {
		case res = <-done:
		default:
			res.err = lookupCtx.Err()
		}
	}

	result := telemetry.LastClusterLookupMiss
	switch {
	case errors.Is(res.err, context.DeadlineExceeded), errors.Is(res.err, context.Canceled):
		result = telemetry.LastClusterLookupTimeout
	case res.err != nil:
		result = telemetry.LastClusterLookupError
	case res.ok && validClusterID(res.value):
		result = telemetry.LastClusterLookupHit
		clusterID = res.value
	}
	telemetry.RecordLastClusterLookup(ctx, result, time.Since(start))
	span.SetAttributes(attribute.String("result", result))

	// A cancelled request (client gone) is not a store failure.
	if res.err == nil || errors.Is(ctx.Err(), context.Canceled) {
		return key, clusterID
	}
	span.RecordError(res.err)
	span.SetStatus(codes.Error, "last cluster lookup failed")
	logger := telemetry.Logger(ctx)
	if result == telemetry.LastClusterLookupTimeout {
		// A slow store times out every lookup at once; the counter and span
		// already count each one, so sample the log.
		sampled := logger.Sample(zerolog.Often)
		logger = &sampled
	}
	logger.Warn().
		Err(res.err).
		Str("request_id", reqCtx.RequestID).
		Str("model", model).
		Str("routing_key", reqCtx.RoutingKey).
		Str("result", result).
		Msg("stargate last-cluster lookup failed")
	return key, clusterID
}

// setFunctionID tags the span with the function ID, which the gateway takes
// from the routing key.
func setFunctionID(span trace.Span, routingKey string) {
	if routingKey != "" {
		span.SetAttributes(attribute.String("nvcf.function.id", routingKey))
	}
}

// Remember records clusterID for the session in the background when status is
// 2xx. key and model are the values Lookup used. It never blocks the caller.
// Empty, oversized, or non-printable cluster IDs are ignored.
func (t *Tracker) Remember(
	ctx context.Context,
	reqCtx *requestctx.RequestContext,
	model string,
	key string,
	status int,
	clusterID string,
) {
	if t == nil || key == "" || status < 200 || status >= 300 {
		return
	}
	clusterID = strings.TrimSpace(clusterID)
	if !validClusterID(clusterID) {
		return
	}
	var requestID, routingKey string
	if reqCtx != nil {
		requestID, routingKey = reqCtx.RequestID, reqCtx.RoutingKey
	}

	// The write outlives the request: keep the trace and logger from ctx but
	// not its cancellation.
	writeCtx := context.WithoutCancel(ctx)
	t.beginWrite()
	go func() {
		defer t.endWrite()
		writeCtx, cancel := context.WithTimeout(writeCtx, t.opts.WriteTimeout)
		defer cancel()
		writeCtx, span := telemetry.Tracer().Start(writeCtx, "llm-api-gateway.last_cluster_write")
		defer span.End()
		setFunctionID(span, routingKey)

		result := telemetry.LastClusterWriteOK
		err := t.store.Set(writeCtx, key, clusterID, t.opts.TTL)
		if err != nil {
			result = telemetry.LastClusterWriteError
			span.RecordError(err)
			span.SetStatus(codes.Error, "last cluster write failed")
			telemetry.Logger(writeCtx).Warn().
				Err(err).
				Str("request_id", requestID).
				Str("model", model).
				Str("routing_key", routingKey).
				Msg("stargate last-cluster write failed")
		}
		span.SetAttributes(attribute.String("result", result))
		telemetry.RecordLastClusterWrite(writeCtx, result)
	}()
}

func (t *Tracker) beginWrite() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.inflight == 0 {
		t.idle = make(chan struct{})
	}
	t.inflight++
}

func (t *Tracker) endWrite() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inflight--
	if t.inflight == 0 {
		close(t.idle)
	}
}

// Drain waits until no background write is in flight or ctx is done.
func (t *Tracker) Drain(ctx context.Context) {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.inflight == 0 {
		t.mu.Unlock()
		return
	}
	idle := t.idle
	t.mu.Unlock()
	select {
	case <-idle:
	case <-ctx.Done():
	}
}
