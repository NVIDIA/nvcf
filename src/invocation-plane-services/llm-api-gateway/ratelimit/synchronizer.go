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
	"errors"
	"sync"
)

// ErrSynchronizerStopped is returned by Send after the synchronizer stopped.
var ErrSynchronizerStopped = errors.New("rate limit synchronizer is stopped")

type RateLimitEvent struct {
	Key         string
	Result      *RateLimitResult
	RequestID   string
	MustConsume bool
}

type Synchronizer interface {
	Send(context.Context, *RateLimitEvent) error
	Start()
	Stop()
}

var _ Synchronizer = (*nopSynchronizer)(nil)

func NewSynchronizer() Synchronizer {
	return nopSynchronizer{}
}

type nopSynchronizer struct{}

func (s nopSynchronizer) Send(context.Context, *RateLimitEvent) error { return nil }

func (s nopSynchronizer) Start() {}

func (s nopSynchronizer) Stop() {}

// eventQueue buffers rate-limit events between Send and a synchronizer's
// publish processors. Requests still finishing during shutdown can call Send
// after Stop, so sends after close return ErrSynchronizerStopped instead of
// panicking on the closed channel, and close releases senders blocked on a
// full queue.
type eventQueue struct {
	mu       sync.Mutex
	ch       chan *RateLimitEventWireFormat
	stopping chan struct{}
	senders  sync.WaitGroup
	closed   bool
}

// open creates the channel the publish processors read from.
func (q *eventQueue) open(size int) <-chan *RateLimitEventWireFormat {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.ch = make(chan *RateLimitEventWireFormat, size)
	q.stopping = make(chan struct{})
	return q.ch
}

func (q *eventQueue) send(ctx context.Context, event *RateLimitEventWireFormat) error {
	q.mu.Lock()
	if q.ch == nil || q.closed {
		q.mu.Unlock()
		return ErrSynchronizerStopped
	}
	q.senders.Add(1)
	ch, stopping := q.ch, q.stopping
	q.mu.Unlock()
	defer q.senders.Done()

	select {
	case ch <- event:
		return nil
	case <-stopping:
		return ErrSynchronizerStopped
	case <-ctx.Done():
		return ctx.Err()
	}
}

// close stops accepting events, waits for in-progress sends to finish, and
// closes the channel so processors drain what is queued. It reports whether
// the queue was open, so the caller knows to wait for its processors.
func (q *eventQueue) close() bool {
	q.mu.Lock()
	if q.ch == nil || q.closed {
		q.closed = true
		q.mu.Unlock()
		return false
	}
	q.closed = true
	close(q.stopping)
	q.mu.Unlock()

	q.senders.Wait()
	close(q.ch)
	return true
}

// length reports the queued events, or -1 before the queue is opened.
func (q *eventQueue) length() int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.ch == nil {
		return -1
	}
	return int64(len(q.ch))
}
