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

package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestShadowRequestDispatched(t *testing.T) {
	var received atomic.Bool
	var receivedBody string
	var receivedPath string

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		receivedBody = string(body)
		receivedPath = r.URL.Path
		received.Store(true)
		w.WriteHeader(http.StatusOK)
	})

	shadower := NewTrafficShadower(10, 30*time.Second)
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(`{"model":"shadow","stream":true}`)))

	err := shadower.Shadow(req, handler)
	assert.NoError(t, err, "shadow should be admitted when under concurrency limit")

	assert.Eventually(t, func() bool { return received.Load() }, 5*time.Second, 10*time.Millisecond)
	assert.JSONEq(t, `{"model":"shadow","stream":true}`, receivedBody)
	assert.Equal(t, "/v1/chat/completions", receivedPath)
}

func TestShadowConcurrencyLimit(t *testing.T) {
	var requestCount atomic.Int32
	started := make(chan struct{})
	done := make(chan struct{})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		started <- struct{}{}
		<-done
		w.WriteHeader(http.StatusOK)
	})

	shadower := NewTrafficShadower(1, 30*time.Second)

	req1 := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(`{"n":1}`)))
	assert.NoError(t, shadower.Shadow(req1, handler))

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first shadow request did not start")
	}

	req2 := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(`{"n":2}`)))
	err := shadower.Shadow(req2, handler)
	assert.ErrorIs(t, err, errShadowConcurrencyLimit)
	assert.Contains(t, err.Error(), "max_concurrent=1")
	time.Sleep(100 * time.Millisecond)

	assert.Equal(t, int32(1), requestCount.Load(), "only one shadow request should have been sent")
	close(done)
}

func TestShadowDropDoesNotDispatch(t *testing.T) {
	started := make(chan struct{})
	done := make(chan struct{})
	var dispatchCount atomic.Int32

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dispatchCount.Add(1)
		close(started)
		<-done
		w.WriteHeader(http.StatusOK)
	})

	shadower := NewTrafficShadower(1, 30*time.Second)

	req1 := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
	assert.NoError(t, shadower.Shadow(req1, handler))

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first shadow request did not start")
	}

	req2 := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
	err := shadower.Shadow(req2, handler)
	assert.ErrorIs(t, err, errShadowConcurrencyLimit)
	time.Sleep(100 * time.Millisecond)

	assert.Equal(t, int32(1), dispatchCount.Load(), "dropped shadow request should not be dispatched")
	close(done)
}

func TestShadowTimeoutCancelsReplay(t *testing.T) {
	var timedOut atomic.Bool
	done := make(chan struct{})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		timedOut.Store(true)
		close(done)
	})

	shadower := NewTrafficShadower(10, 30*time.Second)
	shadower.timeout = 100 * time.Millisecond

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(`{"data":"test"}`)))
	assert.NoError(t, shadower.Shadow(req, handler))

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shadow request did not observe timeout")
	}

	assert.True(t, timedOut.Load())
}

func TestShadowContextNormalPrimaryCompletionKeepsShadowAlive(t *testing.T) {
	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(reqCtx)

	shadowCtx, finishPrimary := shadowContext(req, true)
	finishPrimary(nil)

	assertContextNotCanceled(t, shadowCtx)
	cancelReq()
	assertContextNotCanceled(t, shadowCtx)
}

func TestShadowContextRequestCancelBeforePrimaryFinishCancelsShadow(t *testing.T) {
	reqCtx, cancelReq := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(reqCtx)

	shadowCtx, finishPrimary := shadowContext(req, true)
	cancelReq()

	assertContextCanceled(t, shadowCtx)
	finishPrimary(nil)
}

func TestShadowContextProxyErrorCancelsShadowWithoutRequestContextCancel(t *testing.T) {
	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(reqCtx)

	shadowCtx, finishPrimary := shadowContext(req, true)
	finishPrimary(errors.New("proxy error"))

	assertContextNotCanceled(t, req.Context())
	assertContextCanceled(t, shadowCtx)
}

func TestShadowContextProxyErrorDoesNotCancelShadowWhenDisabled(t *testing.T) {
	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(reqCtx)

	shadowCtx, finishPrimary := shadowContext(req, false)
	finishPrimary(errors.New("proxy error"))

	assertContextNotCanceled(t, shadowCtx)
}

func TestShadowCancelledWhenRequestContextCancels(t *testing.T) {
	started := make(chan struct{})
	var canceled atomic.Bool

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		canceled.Store(true)
	})

	shadower := NewTrafficShadower(10, 30*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(`{"data":"test"}`))).WithContext(ctx)
	assert.NoError(t, shadower.Shadow(req, handler))

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("shadow request did not start")
	}

	cancel() // simulate request context ending (primary complete or client disconnect)

	assert.Eventually(t, func() bool { return canceled.Load() }, 1*time.Second, 10*time.Millisecond,
		"shadow should be canceled when request context is canceled")
}

func assertContextCanceled(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(1 * time.Second):
		t.Fatal("context was not canceled")
	}
}

func assertContextNotCanceled(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
		t.Fatalf("context was canceled: %v", ctx.Err())
	case <-time.After(50 * time.Millisecond):
	}
}

func TestShadowDroppedWhenAtCapacity(t *testing.T) {
	started := make(chan struct{})
	done := make(chan struct{})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-done
	})

	shadower := NewTrafficShadower(1, 30*time.Second)

	// Fill the single slot.
	req1 := httptest.NewRequest("POST", "/", bytes.NewReader([]byte(`{}`)))
	assert.NoError(t, shadower.Shadow(req1, handler))

	<-started

	// Second shadow is silently dropped — no panic, no dispatch.
	req2 := httptest.NewRequest("POST", "/", bytes.NewReader([]byte(`{}`)))
	err := shadower.Shadow(req2, handler)
	assert.ErrorIs(t, err, errShadowConcurrencyLimit)

	close(done)
}

func TestShadowDefaultPercentageIs100(t *testing.T) {
	var requestCount atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
	})

	shadower := NewTrafficShadower(1000, 30*time.Second)
	iterations := 100
	shadowPct := 0
	for range iterations {
		req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
		pct := shadowPct
		if pct <= 0 {
			pct = 100
		}
		if pct >= 100 || rand.IntN(100) < pct {
			assert.NoError(t, shadower.Shadow(req, handler))
		}
	}

	assert.Eventually(t, func() bool {
		return requestCount.Load() == int32(iterations)
	}, 10*time.Second, 50*time.Millisecond,
		"expected all %d requests shadowed when percentage is unset (default 100), got %d", iterations, requestCount.Load())
}

func TestShadowPercentage(t *testing.T) {
	iterations := 10000
	shadowPct := 50
	hitCount := 0
	for range iterations {
		if shadowPct >= 100 || rand.IntN(100) < shadowPct {
			hitCount++
		}
	}

	assert.Greater(t, hitCount, 4500, "expected roughly 50%% hit rate, got %d/%d", hitCount, iterations)
	assert.Less(t, hitCount, 5500, "expected roughly 50%% hit rate, got %d/%d", hitCount, iterations)
}

func TestShadowPercentage100(t *testing.T) {
	var requestCount atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
	})

	shadower := NewTrafficShadower(1000, 30*time.Second)
	iterations := 100
	shadowPct := 100
	for range iterations {
		req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
		if shadowPct >= 100 || rand.IntN(100) < shadowPct {
			assert.NoError(t, shadower.Shadow(req, handler))
		}
	}

	assert.Eventually(t, func() bool {
		return requestCount.Load() == int32(iterations)
	}, 10*time.Second, 50*time.Millisecond,
		"expected all %d requests to be shadowed, got %d", iterations, requestCount.Load())
}
