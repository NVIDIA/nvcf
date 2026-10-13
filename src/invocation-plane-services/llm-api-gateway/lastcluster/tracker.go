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

// Default caps on store calls still running in the background. The Olric
// client does not always honor context cancellation, so a lookup can outlive
// its deadline and a write can outlive its timeout. The caps bound the
// goroutines, and the request contexts they hold, when the store is slow or
// stuck. At the cap, lookups and writes are skipped instead of queued.
const (
	DefaultMaxInflightLookups = 1024
	DefaultMaxInflightWrites  = 1024
)

var errCapacity = errors.New("last-cluster store has too many calls in flight")

// Options configures a Tracker.
type Options struct {
	TTL           time.Duration
	LookupTimeout time.Duration
	// WriteTimeout bounds one background write. Zero uses DefaultWriteTimeout.
	WriteTimeout time.Duration
	// MaxInflightLookups and MaxInflightWrites cap store calls still running.
	// Zero uses the defaults.
	MaxInflightLookups int
	MaxInflightWrites  int
}

// Tracker looks up and records the last cluster per session. A nil Tracker is
// valid and does nothing.
type Tracker struct {
	store Store
	opts  Options

	// lookupSlots holds one token per lookup whose store call has not
	// returned yet.
	lookupSlots chan struct{}

	// writes counts background writes. idle is closed when it drops to zero.
	// A WaitGroup is not used because writes may start while Drain waits.
	mu     sync.Mutex
	writes int
	idle   chan struct{}

	// drainWaitHook runs each time Drain is about to wait. Tests only.
	drainWaitHook func()
}

func NewTracker(store Store, opts Options) *Tracker {
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = DefaultWriteTimeout
	}
	if opts.MaxInflightLookups <= 0 {
		opts.MaxInflightLookups = DefaultMaxInflightLookups
	}
	if opts.MaxInflightWrites <= 0 {
		opts.MaxInflightWrites = DefaultMaxInflightWrites
	}
	return &Tracker{
		store:       store,
		opts:        opts,
		lookupSlots: make(chan struct{}, opts.MaxInflightLookups),
	}
}

// eligible reports whether the request yields session values that recur
// across turns. Header and body session IDs stay stable. Payload sessions
// qualify only when the handler derived prefix-chain values, because their
// CacheAffinityKey hashes the full message list and changes every turn.
func eligible(reqCtx *requestctx.RequestContext) bool {
	switch reqCtx.SessionSource {
	case requestctx.SessionSourcePromptCacheKey,
		requestctx.SessionSourceConversationID,
		requestctx.SessionSourceHeader,
		requestctx.SessionSourceClaudeCodeHeader:
		return true
	case requestctx.SessionSourcePayload:
		return reqCtx.LastClusterWriteKey != ""
	default:
		return false
	}
}

type lookupResult struct {
	value string
	ok    bool
	err   error
}

// Lookup returns the store key Remember writes under and the remembered
// cluster ID. key is empty when the request has no eligible session, in which
// case no write should follow. clusterID is empty on a miss, error, or
// timeout. Payload sessions read under their lookup value and write under
// their write value; a first turn has no lookup value and skips the store.
// The lookup never takes longer than the configured lookup timeout.
func (t *Tracker) Lookup(
	ctx context.Context,
	reqCtx *requestctx.RequestContext,
	model string,
) (key string, clusterID string) {
	if t == nil || reqCtx == nil || reqCtx.CacheAffinityKey == "" {
		return "", ""
	}
	if !eligible(reqCtx) || model == "" {
		telemetry.RecordLastClusterLookup(ctx, telemetry.LastClusterLookupSkipped, 0)
		return "", ""
	}
	// The deadline starts before any span or key work so the whole lookup,
	// not only the store call, fits in the lookup timeout.
	start := time.Now()
	lookupValue, writeValue := reqCtx.CacheAffinityKey, reqCtx.CacheAffinityKey
	if reqCtx.SessionSource == requestctx.SessionSourcePayload {
		lookupValue, writeValue = reqCtx.LastClusterLookupKey, reqCtx.LastClusterWriteKey
	}
	key = Key(reqCtx.RoutingKey, model, writeValue)
	if lookupValue == "" {
		telemetry.RecordLastClusterLookup(ctx, telemetry.LastClusterLookupMiss, time.Since(start))
		return key, ""
	}
	lookupCtx, cancel := context.WithDeadline(ctx, start.Add(t.opts.LookupTimeout))
	defer cancel()
	lookupKey := Key(reqCtx.RoutingKey, model, lookupValue)

	ctx, span := telemetry.Tracer().Start(ctx, "llm-api-gateway.last_cluster_lookup")
	defer span.End()
	setFunctionID(span, reqCtx.RoutingKey)
	lookupCtx = trace.ContextWithSpan(lookupCtx, span)

	var res lookupResult
	select {
	case t.lookupSlots <- struct{}{}:
		res = t.runLookup(lookupCtx, lookupKey)
	default:
		res.err = errCapacity
	}

	result := telemetry.LastClusterLookupMiss
	switch {
	case errors.Is(res.err, context.DeadlineExceeded),
		errors.Is(res.err, context.Canceled),
		errors.Is(res.err, errCapacity):
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
		logger = sampled(logger)
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

// runLookup calls the store in its own goroutine so a store that ignores
// context cancellation still cannot hold the request past the deadline. The
// caller has taken a lookup slot; the goroutine frees it when the store call
// returns, not at the deadline.
func (t *Tracker) runLookup(ctx context.Context, key string) lookupResult {
	done := make(chan lookupResult, 1)
	go func() {
		defer func() { <-t.lookupSlots }()
		value, ok, err := t.store.Get(ctx, key)
		done <- lookupResult{value: value, ok: ok, err: err}
	}()

	select {
	case res := <-done:
		return res
	case <-ctx.Done():
		// Prefer a result that landed at the deadline.
		select {
		case res := <-done:
			return res
		default:
			return lookupResult{err: ctx.Err()}
		}
	}
}

// InflightLookups returns the number of store lookups that have not returned.
func (t *Tracker) InflightLookups() int {
	if t == nil {
		return 0
	}
	return len(t.lookupSlots)
}

func sampled(logger *zerolog.Logger) *zerolog.Logger {
	l := logger.Sample(zerolog.Often)
	return &l
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
// Empty, oversized, or non-printable cluster IDs are ignored. When the write
// cap is reached, the write is dropped and counted as an error.
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
	logFailure := func(logger *zerolog.Logger, err error) {
		logger.Warn().
			Err(err).
			Str("request_id", requestID).
			Str("model", model).
			Str("routing_key", routingKey).
			Msg("stargate last-cluster write failed")
	}

	if !t.beginWrite() {
		telemetry.RecordLastClusterWrite(ctx, telemetry.LastClusterWriteError)
		logFailure(sampled(telemetry.Logger(ctx)), errCapacity)
		return
	}

	// The write outlives the request: keep the trace and logger from ctx but
	// not its cancellation. The goroutine, and the context it holds, end when
	// the store call returns.
	writeCtx := context.WithoutCancel(ctx)
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
			logFailure(telemetry.Logger(writeCtx), err)
		}
		span.SetAttributes(attribute.String("result", result))
		telemetry.RecordLastClusterWrite(writeCtx, result)
	}()
}

// beginWrite reserves a write slot. It returns false at the write cap.
func (t *Tracker) beginWrite() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.writes >= t.opts.MaxInflightWrites {
		return false
	}
	if t.writes == 0 {
		t.idle = make(chan struct{})
	}
	t.writes++
	return true
}

func (t *Tracker) endWrite() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.writes--
	if t.writes == 0 {
		close(t.idle)
	}
}

// InflightWrites returns the number of store writes that have not returned.
func (t *Tracker) InflightWrites() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.writes
}

// Drain waits until no background write is in flight or ctx is done. Writes
// that start while Drain waits are waited for too.
func (t *Tracker) Drain(ctx context.Context) {
	if t == nil {
		return
	}
	for {
		t.mu.Lock()
		if t.writes == 0 {
			t.mu.Unlock()
			return
		}
		idle := t.idle
		t.mu.Unlock()
		if t.drainWaitHook != nil {
			t.drainWaitHook()
		}
		select {
		case <-idle:
		case <-ctx.Done():
			return
		}
	}
}
