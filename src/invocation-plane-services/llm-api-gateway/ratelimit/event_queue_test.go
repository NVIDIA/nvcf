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
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func TestEventQueueRejectsSendsAfterClose(t *testing.T) {
	t.Parallel()

	var q eventQueue
	if err := q.send(&RateLimitEventWireFormat{}); !errors.Is(err, ErrSynchronizerStopped) {
		t.Fatalf("send before open error = %v, want ErrSynchronizerStopped", err)
	}
	if got := q.length(); got != -1 {
		t.Fatalf("length before open = %d, want -1", got)
	}

	q.open(1)
	if err := q.send(&RateLimitEventWireFormat{}); err != nil {
		t.Fatalf("send error = %v", err)
	}
	if got := q.length(); got != 1 {
		t.Fatalf("length = %d, want 1", got)
	}
	if !q.close() {
		t.Fatal("close() = false, want true for an open queue")
	}
	if q.close() {
		t.Fatal("second close() = true, want false")
	}
	if err := q.send(&RateLimitEventWireFormat{}); !errors.Is(err, ErrSynchronizerStopped) {
		t.Fatalf("send after close error = %v, want ErrSynchronizerStopped", err)
	}
}

// countingNATSPublisher is safe for the synchronizer's concurrent processors.
type countingNATSPublisher struct {
	published atomic.Int64
}

func (p *countingNATSPublisher) Publish(string, []byte, ...nats.PubOpt) (*nats.PubAck, error) {
	p.published.Add(1)
	return &nats.PubAck{}, nil
}

// TestNATSSynchronizerSendDuringStop sends from many goroutines while the
// synchronizer stops, as requests finishing during shutdown do. Every send
// must either succeed or report ErrSynchronizerStopped, never panic.
func TestNATSSynchronizerSendDuringStop(t *testing.T) {
	t.Parallel()

	cfg, err := NewNATSSyncConfig("rate-limit-events", "cluster-a")
	if err != nil {
		t.Fatal(err)
	}
	synchronizer := NewNATSSynchronizer(&countingNATSPublisher{}, &fakeNATSDrainer{}, cfg, "cluster-a")
	synchronizer.Start()

	event := &RateLimitEvent{
		Key:    "org:123",
		Result: &RateLimitResult{Requested: 1, RateLimit: RateLimit{Limit: 10, Period: time.Minute}},
	}
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for range 16 {
		wg.Go(func() {
			for range 200 {
				if err := synchronizer.Send(context.Background(), event); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	synchronizer.Stop()
	wg.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, ErrSynchronizerStopped) {
			t.Fatalf("Send() error = %v, want nil or ErrSynchronizerStopped", err)
		}
	}
	if err := synchronizer.Send(context.Background(), event); !errors.Is(err, ErrSynchronizerStopped) {
		t.Fatalf("Send() after Stop error = %v, want ErrSynchronizerStopped", err)
	}
}
