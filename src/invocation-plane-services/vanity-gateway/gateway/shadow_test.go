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
	config "ai-api-gateway-service/gateway_config"
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func fixedRandomBucket(bucket int) func() int {
	return func() int {
		return bucket
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

func TestShouldDispatchShadowPerBearerKeyUsesBearerBucket(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{}`))
	req.Header.Set("Authorization", "Bearer test-key")

	info := FunctionInfo{
		shadowModelNames:     []string{"private/facebook/opt-125m-shadow"},
		shadowPercentage:     47,
		shadowSamplingMethod: config.ShadowSamplingMethodPerBearerKey,
	}
	assert.True(t, shouldDispatchShadow(req, info, fixedRandomBucket(99)))

	info.shadowPercentage = 46
	assert.False(t, shouldDispatchShadow(req, info, fixedRandomBucket(0)))
}

func TestShouldDispatchShadowPerBearerKeySkipsMalformedBearerBelow100(t *testing.T) {
	tests := []struct {
		name          string
		authorization []string
	}{
		{name: "missing"},
		{name: "malformed bearer", authorization: []string{"Bearer"}},
		{name: "empty bearer credential", authorization: []string{"Bearer   "}},
		{name: "non bearer", authorization: []string{"Basic test-key"}},
		{name: "leading whitespace before scheme", authorization: []string{" Bearer test-key"}},
		{name: "duplicate authorization", authorization: []string{"Bearer test-key", "Bearer test-key"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{}`))
			for _, value := range tc.authorization {
				req.Header.Add("Authorization", value)
			}
			info := FunctionInfo{
				shadowModelNames:     []string{"private/facebook/opt-125m-shadow"},
				shadowPercentage:     99,
				shadowSamplingMethod: config.ShadowSamplingMethodPerBearerKey,
			}

			assert.False(t, shouldDispatchShadow(req, info, fixedRandomBucket(0)))
		})
	}
}

func TestShouldDispatchShadowRandomUsesInjectedBucket(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{}`))
	req.Header.Set("Authorization", "Bearer test-key")

	info := FunctionInfo{
		shadowModelNames:     []string{"private/facebook/opt-125m-shadow"},
		shadowPercentage:     40,
		shadowSamplingMethod: config.ShadowSamplingMethodRandom,
	}

	assert.True(t, shouldDispatchShadow(req, info, fixedRandomBucket(39)))
	assert.False(t, shouldDispatchShadow(req, info, fixedRandomBucket(40)))
}

func TestShouldDispatchShadowPerBearerKeyNormalizesBearerScheme(t *testing.T) {
	tests := []string{
		"Bearer test-key",
		"bearer test-key",
		"BEARER test-key",
		"BeArEr \t test-key",
	}

	for _, authorization := range tests {
		t.Run(authorization, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{}`))
			req.Header.Set("Authorization", authorization)
			info := FunctionInfo{
				shadowModelNames:     []string{"private/facebook/opt-125m-shadow"},
				shadowPercentage:     47,
				shadowSamplingMethod: config.ShadowSamplingMethodPerBearerKey,
			}

			assert.True(t, shouldDispatchShadow(req, info, fixedRandomBucket(99)))
		})
	}
}

func TestShadowBucketForBearerCredentialFixedVector(t *testing.T) {
	assert.Equal(t, 46, shadowBucketForBearerCredential([]byte("test-key")))
}

func TestShadowBucketForBearerCredentialKeepsNVCFKeyPrefixes(t *testing.T) {
	assert.Equal(t, 62, shadowBucketForBearerCredential([]byte("nvapi-test-key")))
	assert.Equal(t, 52, shadowBucketForBearerCredential([]byte("nvapi-stg-test-key")))
	assert.Equal(t, 97, shadowBucketForBearerCredential([]byte("nvapi-nvcf-test-key")))
}

func TestShadowBucketForBearerCredentialSyntheticDistribution(t *testing.T) {
	const samples = 10_000
	thresholds := []int{1, 5, 10, 25, 50, 75, 90, 99, 100}
	var histogram [100]int

	for i := range samples {
		credential := []byte("nvapi-nvcf-synthetic-" + strconv.Itoa(i))
		bucket := shadowBucketForBearerCredential(credential)
		histogram[bucket]++
	}

	for _, threshold := range thresholds {
		selected := 0
		for bucket := range threshold {
			selected += histogram[bucket]
		}

		lower := samples * max(threshold-5, 0) / 100
		upper := samples * min(threshold+5, 100) / 100
		assert.GreaterOrEqual(t, selected, lower, "threshold %d selected %d keys", threshold, selected)
		assert.LessOrEqual(t, selected, upper, "threshold %d selected %d keys", threshold, selected)
	}

	expected := float64(samples) / 100
	chiSquare := 0.0
	for _, count := range histogram {
		delta := float64(count) - expected
		chiSquare += delta * delta / expected
	}
	assert.Less(t, chiSquare, 150.0)
}
