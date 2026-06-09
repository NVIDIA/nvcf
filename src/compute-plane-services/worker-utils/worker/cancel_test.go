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

package worker

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// Worker stub with just the fields the cancel handler touches.
func newTestWorker() *NVCFWorker {
	return &NVCFWorker{
		inFlightCancels: make(map[string]context.CancelCauseFunc),
	}
}

func TestHandleCancelMessage_FiresRegisteredCancel(t *testing.T) {
	w := newTestWorker()
	requestId := "req-123"
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	w.cancelSubMu.Lock()
	w.inFlightCancels[requestId] = cancel
	w.cancelSubMu.Unlock()

	w.handleCancelMessage(&nats.Msg{
		Subject: "nvcf.cancel.fvid",
		Data:    []byte(requestId),
	})

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("ctx should have been cancelled")
	}
	if !errors.Is(context.Cause(ctx), ErrUpstreamCancel) {
		t.Fatalf("expected ErrUpstreamCancel cause, got %v", context.Cause(ctx))
	}
}

func TestHandleCancelMessage_IgnoresUnknownRequest(t *testing.T) {
	w := newTestWorker()
	known := "req-known"
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	w.cancelSubMu.Lock()
	w.inFlightCancels[known] = cancel
	w.cancelSubMu.Unlock()

	w.handleCancelMessage(&nats.Msg{
		Subject: "nvcf.cancel.fvid",
		Data:    []byte("req-unknown"),
	})

	select {
	case <-ctx.Done():
		t.Fatal("ctx should not have been cancelled for unknown request id")
	case <-time.After(50 * time.Millisecond):
	}
}

// Hammer register/deregister + delivery to surface map races under -race.
func TestHandleCancelMessage_Concurrent(t *testing.T) {
	w := newTestWorker()
	const n = 200

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "req-" + strconv.Itoa(i)
			ctx, cancel := context.WithCancelCause(context.Background())
			w.cancelSubMu.Lock()
			w.inFlightCancels[id] = cancel
			w.cancelSubMu.Unlock()

			// half are cancelled via NATS, half via local deregister
			if i%2 == 0 {
				w.handleCancelMessage(&nats.Msg{Data: []byte(id)})
				select {
				case <-ctx.Done():
				case <-time.After(time.Second):
					t.Errorf("ctx %s not cancelled", id)
				}
			}

			w.cancelSubMu.Lock()
			delete(w.inFlightCancels, id)
			w.cancelSubMu.Unlock()
			cancel(nil)
		}(i)
	}

	// concurrent garbage cancels for unknown ids should be safe and silent
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w.handleCancelMessage(&nats.Msg{Data: []byte("ghost-" + strconv.Itoa(i))})
		}(i)
	}

	wg.Wait()
}
