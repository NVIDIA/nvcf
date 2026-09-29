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
// after Stop, so a send after close returns ErrSynchronizerStopped instead of
// panicking on the closed channel.
type eventQueue struct {
	mu     sync.RWMutex
	ch     chan *RateLimitEventWireFormat
	closed bool
}

// open creates the channel the publish processors read from.
func (q *eventQueue) open(size int) <-chan *RateLimitEventWireFormat {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.ch = make(chan *RateLimitEventWireFormat, size)
	return q.ch
}

func (q *eventQueue) send(event *RateLimitEventWireFormat) error {
	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.ch == nil || q.closed {
		return ErrSynchronizerStopped
	}
	q.ch <- event
	return nil
}

// close stops accepting events and reports whether the queue was open, so the
// caller knows to wait for its processors.
func (q *eventQueue) close() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.ch == nil || q.closed {
		q.closed = true
		return false
	}
	q.closed = true
	close(q.ch)
	return true
}

// length reports the queued events, or -1 before the queue is opened.
func (q *eventQueue) length() int64 {
	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.ch == nil {
		return -1
	}
	return int64(len(q.ch))
}
