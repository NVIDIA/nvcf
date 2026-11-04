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
package invocation

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jellydator/ttlcache/v3"
	"github.com/nats-io/nats.go/jetstream"
)

// The work queue a stateful invocation lands in. These are owned by the
// invocation service, which publishes to requestWorkSubject and removes a
// cancelled request with the same subject-filtered purge used here. The
// formats are duplicated rather than shared because the owning service is
// written in another language; they must not drift.
func requestWorkStream(region, functionVersionId string) string {
	return fmt.Sprintf("rq_%s_%s", region, functionVersionId)
}

// requestWorkSubject is the subject a purge targets. It delegates to the
// publisher rather than restating the format: the two live in one package, and
// a second copy would let a change to the publisher silently desynchronise the
// purger, leaving purges that succeed while removing nothing.
func (f *FunctionInvoker) requestWorkSubject(functionVersionId string, requestId uuid.UUID) string {
	return f.requestStreamSubject(functionVersionId, requestId)
}

// PurgePendingWork drops a stateful work request that is still queued.
//
// It is called for sessions this pod issued a worker token for that never came
// back to CONNECT, at the point the pod is shutting down. Those tokens only
// exist in this pod's memory, so every one of those queued requests is already
// guaranteed to fail authentication whenever a worker eventually pulls it. Left
// in place they are pulled anyway, occupy a worker concurrency slot, and fail,
// which is what keeps a saturated function at zero goodput long after the
// restart that caused it.
//
// Purging by subject only removes messages still held by the stream. A session
// that already has a worker attached had its message delivered, so an
// established session is unaffected and remains free to reattach to another
// pod.
func (f *FunctionInvoker) PurgePendingWork(ctx context.Context, requestId uuid.UUID, functionVersionId string) error {
	streamName := requestWorkStream(f.region, functionVersionId)
	stream, err := f.workStream(ctx, streamName)
	if err != nil {
		return fmt.Errorf("failed to look up work stream %s: %w", streamName, err)
	}
	subject := f.requestWorkSubject(functionVersionId, requestId)
	if err := stream.Purge(ctx, jetstream.WithPurgeSubject(subject)); err != nil {
		return fmt.Errorf("failed to purge work subject %s: %w", subject, err)
	}
	return nil
}

// workStreamCacheCapacity bounds the resolved-handle cache.
//
// The key is one stream per region and function version, and the region is
// fixed for a pod, so the key space grows with every function version this pod
// has ever served. Small entries, but unbounded over the life of a process is
// not a property worth having in a cache that exists only to save a lookup.
const workStreamCacheCapacity = 1024

// workStreamCacheTTL keeps a handle only while it is being used. A version that
// stops receiving traffic should not hold a slot forever.
const workStreamCacheTTL = time.Hour

// workStream returns a handle for a work stream, resolving it once per stream.
//
// A purge is two JetStream calls, a lookup and the purge itself, and the
// lookup answer does not change: one stream per region and function version.
// Under saturation this path runs once per abandoned request, so caching the
// handle halves the calls the remedy makes against a queue that is already
// under strain.
func (f *FunctionInvoker) workStream(ctx context.Context, streamName string) (jetstream.Stream, error) {
	if cached := f.workStreams.Get(streamName); cached != nil {
		return cached.Value(), nil
	}
	stream, err := f.js.Stream(ctx, streamName)
	if err != nil {
		return nil, err
	}
	f.workStreams.Set(streamName, stream, ttlcache.DefaultTTL)
	return stream, nil
}
