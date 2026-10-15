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

package ngc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// boundedCtx returns a context with a small real deadline. It lets the first
// request complete (so callers observe the real formatted error) while
// guaranteeing the outer backoff.Retry loop is cut off long before the real
// exponential backoff series would elapse. We never assert on wall-clock
// durations; the deadline only bounds retries so tests stay fast.
//
// The NGC client wraps every call in backoff.Retry and returns a plain error
// on any non-2xx response, so even non-retryable 4xx responses are retried up
// to 5 times. Without a bounded context these tests would sleep through the
// real backoff schedule.
func boundedCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

// handlerFunc lets each test inject a per-request handler. It receives the
// request and the (1-based) request count so handlers can vary behavior
// across retries if needed.
type handlerFunc func(w http.ResponseWriter, r *http.Request, callNum int64)

// newTestClient spins up an httptest server with the supplied handler and
// returns an NgcClient pointed at it. The server is registered for cleanup.
// httptest picks an ephemeral port; no fixed ports are bound.
func newTestClient(t *testing.T, org, team string, h handlerFunc) (*NgcClient, *httptest.Server, *int64) {
	t.Helper()

	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&calls, 1)
		h(w, r, n)
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(org, team, srv.URL)
	require.NoError(t, err)
	require.NotNil(t, c)

	return c, srv, &calls
}

// writeStatus is a tiny helper for handlers that just need a code + body.
func writeStatus(code int, body string) handlerFunc {
	return func(w http.ResponseWriter, _ *http.Request, _ int64) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

func TestNewClient(t *testing.T) {
	t.Run("org only", func(t *testing.T) {
		t.Parallel()
		c, err := NewClient("myorg", "", "https://api.example.com")
		require.NoError(t, err)
		require.NotNil(t, c)
		assert.Equal(t, "https://api.example.com/v2/org/myorg", c.ngcUrl)
	})

	t.Run("org and team", func(t *testing.T) {
		t.Parallel()
		c, err := NewClient("myorg", "myteam", "https://api.example.com")
		require.NoError(t, err)
		require.NotNil(t, c)
		assert.Equal(t, "https://api.example.com/v2/org/myorg/team/myteam", c.ngcUrl)
	})
}

func TestCreateModel(t *testing.T) {
	t.Parallel()
	t.Run("happy path 200", func(t *testing.T) {
		t.Parallel()
		c, _, calls := newTestClient(t, "org", "", func(w http.ResponseWriter, r *http.Request, _ int64) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/v2/org/org/models", r.URL.Path)
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
			assert.Equal(t, "Bearer key123", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusOK)
		})

		err := c.CreateModel(context.Background(), "key123", "my-model")
		require.NoError(t, err)
		assert.Equal(t, int64(1), atomic.LoadInt64(calls))
	})

	t.Run("409 conflict treated as success", func(t *testing.T) {
		t.Parallel()
		c, _, _ := newTestClient(t, "org", "", writeStatus(http.StatusConflict, "exists"))
		err := c.CreateModel(context.Background(), "key", "my-model")
		require.NoError(t, err)
	})

	t.Run("non-2xx returns formatted error with code and body", func(t *testing.T) {
		t.Parallel()
		c, _, _ := newTestClient(t, "org", "", writeStatus(http.StatusBadRequest, "bad payload"))
		err := c.CreateModel(context.Background(), "key", "my-model")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create model")
		assert.Contains(t, err.Error(), "400")
		assert.Contains(t, err.Error(), "bad payload")
	})
}

func TestCreateModelVersion(t *testing.T) {
	t.Parallel()
	t.Run("happy path 200", func(t *testing.T) {
		t.Parallel()
		c, _, _ := newTestClient(t, "org", "team", func(w http.ResponseWriter, r *http.Request, _ int64) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/v2/org/org/team/team/models/my-model/versions", r.URL.Path)
			w.WriteHeader(http.StatusOK)
		})
		err := c.CreateModelVersion(context.Background(), "key", "my-model", "v1")
		require.NoError(t, err)
	})

	t.Run("409 conflict treated as success", func(t *testing.T) {
		t.Parallel()
		c, _, _ := newTestClient(t, "org", "", writeStatus(http.StatusConflict, "exists"))
		err := c.CreateModelVersion(context.Background(), "key", "my-model", "v1")
		require.NoError(t, err)
	})

	t.Run("non-2xx returns formatted error", func(t *testing.T) {
		t.Parallel()
		c, _, _ := newTestClient(t, "org", "", writeStatus(http.StatusBadRequest, "nope"))
		err := c.CreateModelVersion(context.Background(), "key", "my-model", "v1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create version")
		assert.Contains(t, err.Error(), "400")
		assert.Contains(t, err.Error(), "nope")
	})
}

func TestStartModelUpload(t *testing.T) {
	t.Parallel()
	t.Run("happy path decodes response", func(t *testing.T) {
		t.Parallel()
		c, _, _ := newTestClient(t, "org", "", func(w http.ResponseWriter, r *http.Request, _ int64) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/v2/org/org/files/multipart", r.URL.Path)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"uploadID":"up-1","partSize":1024,"urls":["https://s3/part1","https://s3/part2"]}`))
		})

		resp, err := c.StartModelUpload(context.Background(), "key", "m", "v1", "model.bin", 2048, 1024)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "up-1", resp.UploadId)
		assert.Equal(t, 1024, resp.PartSize)
		assert.Equal(t, []string{"https://s3/part1", "https://s3/part2"}, resp.Urls)
	})

	t.Run("non-2xx returns formatted error and nil response", func(t *testing.T) {
		t.Parallel()
		c, _, _ := newTestClient(t, "org", "", writeStatus(http.StatusBadRequest, "init failed"))
		resp, err := c.StartModelUpload(context.Background(), "key", "m", "v1", "model.bin", 2048, 1024)
		require.Error(t, err)
		assert.Nil(t, resp)
		assert.Contains(t, err.Error(), "failed to initiate model upload")
		assert.Contains(t, err.Error(), "400")
		assert.Contains(t, err.Error(), "init failed")
	})

	t.Run("malformed JSON on 200 returns unmarshal error", func(t *testing.T) {
		t.Parallel()
		c, _, _ := newTestClient(t, "org", "", func(w http.ResponseWriter, _ *http.Request, _ int64) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{not valid json`))
		})
		resp, err := c.StartModelUpload(context.Background(), "key", "m", "v1", "model.bin", 2048, 1024)
		require.Error(t, err)
		assert.Nil(t, resp)
	})
}

func TestUploadModel(t *testing.T) {
	t.Parallel()
	t.Run("happy path 200", func(t *testing.T) {
		t.Parallel()
		c, srv, _ := newTestClient(t, "org", "", func(w http.ResponseWriter, r *http.Request, _ int64) {
			assert.Equal(t, http.MethodPut, r.Method)
			w.WriteHeader(http.StatusOK)
		})
		err := c.UploadModel(context.Background(), srv.URL+"/presigned", []byte("chunk-data"))
		require.NoError(t, err)
	})

	t.Run("non-2xx returns formatted error", func(t *testing.T) {
		t.Parallel()
		c, srv, _ := newTestClient(t, "org", "", writeStatus(http.StatusBadRequest, "upload rejected"))
		err := c.UploadModel(context.Background(), srv.URL+"/presigned", []byte("chunk-data"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to upload file")
		assert.Contains(t, err.Error(), "400")
		assert.Contains(t, err.Error(), "upload rejected")
	})
}

func TestCompleteModelUpload(t *testing.T) {
	t.Parallel()
	t.Run("happy path 200", func(t *testing.T) {
		t.Parallel()
		c, _, _ := newTestClient(t, "org", "", func(w http.ResponseWriter, r *http.Request, _ int64) {
			assert.Equal(t, http.MethodPut, r.Method)
			assert.Equal(t, "/v2/org/org/files/multipart", r.URL.Path)
			w.WriteHeader(http.StatusOK)
		})
		err := c.CompleteModelUpload(context.Background(), "key", "m", "v1", "model.bin", "deadbeef", "up-1")
		require.NoError(t, err)
	})

	t.Run("non-2xx returns formatted error", func(t *testing.T) {
		t.Parallel()
		c, _, _ := newTestClient(t, "org", "", writeStatus(http.StatusBadRequest, "complete failed"))
		err := c.CompleteModelUpload(context.Background(), "key", "m", "v1", "model.bin", "deadbeef", "up-1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to complete model upload")
		assert.Contains(t, err.Error(), "400")
		assert.Contains(t, err.Error(), "complete failed")
	})
}

func TestAbortModelUpload(t *testing.T) {
	t.Parallel()
	t.Run("happy path 200", func(t *testing.T) {
		t.Parallel()
		c, _, _ := newTestClient(t, "org", "", func(w http.ResponseWriter, r *http.Request, _ int64) {
			assert.Equal(t, http.MethodDelete, r.Method)
			assert.Equal(t, "/v2/org/org/files/multipart", r.URL.Path)
			w.WriteHeader(http.StatusOK)
		})
		err := c.AbortModelUpload(context.Background(), "key", "m", "v1", "model.bin", "up-1")
		require.NoError(t, err)
	})

	t.Run("non-2xx returns formatted error", func(t *testing.T) {
		t.Parallel()
		c, _, _ := newTestClient(t, "org", "", writeStatus(http.StatusBadRequest, "abort failed"))
		err := c.AbortModelUpload(context.Background(), "key", "m", "v1", "model.bin", "up-1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to abort model upload")
		assert.Contains(t, err.Error(), "400")
		assert.Contains(t, err.Error(), "abort failed")
	})
}

func TestUpdateModel(t *testing.T) {
	t.Parallel()
	t.Run("happy path 200", func(t *testing.T) {
		t.Parallel()
		c, _, _ := newTestClient(t, "org", "", func(w http.ResponseWriter, r *http.Request, _ int64) {
			assert.Equal(t, http.MethodPatch, r.Method)
			assert.Equal(t, "/v2/org/org/models/m/versions/v1", r.URL.Path)
			w.WriteHeader(http.StatusOK)
		})
		err := c.UpdateModel(context.Background(), "key", "m", "v1")
		require.NoError(t, err)
	})

	t.Run("non-2xx returns formatted error", func(t *testing.T) {
		t.Parallel()
		c, _, _ := newTestClient(t, "org", "", writeStatus(http.StatusBadRequest, "update failed"))
		err := c.UpdateModel(context.Background(), "key", "m", "v1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to update model version")
		assert.Contains(t, err.Error(), "400")
		assert.Contains(t, err.Error(), "update failed")
	})
}

// TestRetryBoundedByContext drives a persistently failing (5xx) handler with a
// context that is already cancelled. retryablehttp and the outer backoff loop
// both honor context cancellation, so the call must return promptly with an
// error rather than sleeping through the real backoff series. We never assert
// wall-clock durations.
func TestRetryBoundedByContext(t *testing.T) {
	t.Parallel()
	cancelledCtx := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}

	t.Run("CreateModel with cancelled context", func(t *testing.T) {
		t.Parallel()
		c, _, _ := newTestClient(t, "org", "", writeStatus(http.StatusInternalServerError, "boom"))
		err := c.CreateModel(cancelledCtx(), "key", "m")
		require.Error(t, err)
	})

	t.Run("StartModelUpload with cancelled context", func(t *testing.T) {
		t.Parallel()
		c, _, _ := newTestClient(t, "org", "", writeStatus(http.StatusInternalServerError, "boom"))
		resp, err := c.StartModelUpload(cancelledCtx(), "key", "m", "v1", "f", 1, 1)
		require.Error(t, err)
		assert.Nil(t, resp)
	})

	t.Run("UploadModel with cancelled context", func(t *testing.T) {
		t.Parallel()
		c, srv, _ := newTestClient(t, "org", "", writeStatus(http.StatusInternalServerError, "boom"))
		err := c.UploadModel(cancelledCtx(), srv.URL+"/presigned", []byte("data"))
		require.Error(t, err)
	})
}

// TestRetryBoundedByDeadline drives a persistently failing handler with a
// short real deadline rather than a pre-cancelled context. The outer
// backoff.Retry loop retries the non-2xx error, but the deadline cuts the loop
// off long before the real exponential backoff series elapses, so the call
// returns an error without sleeping through the full schedule. We assert only
// that an error is returned, never a wall-clock duration.
func TestRetryBoundedByDeadline(t *testing.T) {
	t.Parallel()

	t.Run("CreateModel non-2xx under deadline", func(t *testing.T) {
		t.Parallel()
		c, _, _ := newTestClient(t, "org", "", writeStatus(http.StatusBadRequest, "bad"))
		err := c.CreateModel(boundedCtx(t), "key", "m")
		require.Error(t, err)
	})

	t.Run("UpdateModel non-2xx under deadline", func(t *testing.T) {
		t.Parallel()
		c, _, _ := newTestClient(t, "org", "", writeStatus(http.StatusBadRequest, "bad"))
		err := c.UpdateModel(boundedCtx(t), "key", "m", "v1")
		require.Error(t, err)
	})
}

// TestRetryRecoversAfterTransientFailure exercises the success-after-retry
// path. retryablehttp (NumRetries=3) retries 5xx responses at the transport
// layer, so a handler that fails once then returns 200 lets the method
// ultimately succeed and cover the post-retry success branch.
func TestRetryRecoversAfterTransientFailure(t *testing.T) {
	t.Parallel()

	t.Run("CreateModel recovers", func(t *testing.T) {
		t.Parallel()
		c, _, calls := newTestClient(t, "org", "", func(w http.ResponseWriter, _ *http.Request, n int64) {
			if n == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
		})
		err := c.CreateModel(context.Background(), "key", "m")
		require.NoError(t, err)
		assert.GreaterOrEqual(t, atomic.LoadInt64(calls), int64(2))
	})

	t.Run("StartModelUpload recovers and decodes", func(t *testing.T) {
		t.Parallel()
		c, _, _ := newTestClient(t, "org", "", func(w http.ResponseWriter, _ *http.Request, n int64) {
			if n == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"uploadID":"up-2","partSize":512,"urls":["u"]}`))
		})
		resp, err := c.StartModelUpload(context.Background(), "key", "m", "v1", "f", 1, 1)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, "up-2", resp.UploadId)
	})
}

// TestNewClientError verifies the error path of NewClient when url.JoinPath
// receives a base URL it cannot parse.
func TestNewClientError(t *testing.T) {
	t.Parallel()
	// A control character in the scheme makes url.JoinPath fail to parse.
	_, err := NewClient("org", "team", "ht\x00tp://bad")
	require.Error(t, err)
}

// TestMethodsURLJoinError exercises the early url.JoinPath error branch in each
// method by giving the client an unparseable base URL. This is a white-box
// test: it builds a real client, then swaps in a poisoned ngcUrl so the
// per-method url.JoinPath call fails before any request is sent.
func TestMethodsURLJoinError(t *testing.T) {
	t.Parallel()

	newPoisoned := func(t *testing.T) *NgcClient {
		t.Helper()
		c, err := NewClient("org", "", "https://api.example.com")
		require.NoError(t, err)
		c.ngcUrl = "ht\x00tp://bad"
		return c
	}

	ctx := context.Background()

	require.Error(t, newPoisoned(t).CreateModel(ctx, "k", "m"))
	require.Error(t, newPoisoned(t).CreateModelVersion(ctx, "k", "m", "v"))

	_, err := newPoisoned(t).StartModelUpload(ctx, "k", "m", "v", "f", 1, 1)
	require.Error(t, err)

	require.Error(t, newPoisoned(t).CompleteModelUpload(ctx, "k", "m", "v", "f", "h", "u"))
	require.Error(t, newPoisoned(t).AbortModelUpload(ctx, "k", "m", "v", "f", "u"))
	require.Error(t, newPoisoned(t).UpdateModel(ctx, "k", "m", "v"))
}
