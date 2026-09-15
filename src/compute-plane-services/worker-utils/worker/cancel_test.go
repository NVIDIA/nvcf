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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
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

// An upstream cancel is expected teardown and must stay out of the error
// stream. Anything else must still be reported at error level.
func TestLogWorkRequestResult_Levels(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		level zapcore.Level
		msg   string
	}{
		{
			name: "nil error logs nothing",
			err:  nil,
		},
		{
			name:  "upstream cancel is demoted to debug",
			err:   fmt.Errorf("failed to send POST request: %w", ErrUpstreamCancel),
			level: zapcore.DebugLevel,
			msg:   "request aborted by upstream cancel",
		},
		{
			name:  "unrelated failure stays at error",
			err:   errors.New("inference container exploded"),
			level: zapcore.ErrorLevel,
			msg:   "failed to handle request",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zapcore.DebugLevel)
			defer zap.ReplaceGlobals(zap.New(core))()

			logWorkRequestResult(tc.err, "req-123")

			if tc.err == nil {
				if logs.Len() != 0 {
					t.Fatalf("expected no log lines, got %v", logs.All())
				}
				return
			}

			entries := logs.All()
			if len(entries) != 1 {
				t.Fatalf("expected exactly 1 log line, got %v", entries)
			}
			if entries[0].Level != tc.level {
				t.Errorf("level = %v, want %v", entries[0].Level, tc.level)
			}
			if entries[0].Message != tc.msg {
				t.Errorf("message = %q, want %q", entries[0].Message, tc.msg)
			}
		})
	}
}

// The cancel arrives while an HTTP send is in flight, so the error the worker
// classifies is a *url.Error produced by net/http rather than the bare
// sentinel. Guard that the cause still survives errors.Is through that wrapping.
func TestLogWorkRequestResult_RealCancelledRequest(t *testing.T) {
	// hold the response open until the client goes away so the cancel lands
	// mid-flight, and so Close does not wait on a sleeping handler
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancelCause(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel(ErrUpstreamCancel)
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	//nolint:bodyclose // the request is cancelled, so there is no body to close
	_, doErr := http.DefaultClient.Do(req)
	if doErr == nil {
		t.Fatal("expected the in-flight request to fail")
	}
	wrapped := fmt.Errorf("failed to send POST request: %w", doErr)

	core, logs := observer.New(zapcore.DebugLevel)
	defer zap.ReplaceGlobals(zap.New(core))()

	logWorkRequestResult(wrapped, "req-123")

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 log line, got %v", entries)
	}
	if entries[0].Level != zapcore.DebugLevel {
		t.Errorf("level = %v, want debug (got message %q)", entries[0].Level, entries[0].Message)
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
