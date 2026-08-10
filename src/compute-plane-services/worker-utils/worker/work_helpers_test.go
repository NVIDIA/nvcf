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

// White-box unit tests for the pure, deterministic helpers in work.go. These
// avoid any hard dependency (NATS, S3, gRPC, HTTP upstream) and are driven by
// in-memory data only.
package worker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metering"
	pb "github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
)

func TestGetTargetPollDuration(t *testing.T) {
	tests := []struct {
		name        string
		headerValue string
		setHeader   bool
		want        time.Duration
	}{
		{name: "no header defaults to one minute", setHeader: false, want: time.Minute},
		{name: "valid seconds", setHeader: true, headerValue: "30", want: 30 * time.Second},
		{name: "zero seconds", setHeader: true, headerValue: "0", want: 0},
		{name: "non-numeric falls back to default", setHeader: true, headerValue: "abc", want: time.Minute},
		{name: "empty value falls back to default", setHeader: true, headerValue: "", want: time.Minute},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, "http://example/", nil)
			require.NoError(t, err)
			if tc.setHeader {
				req.Header.Set("nvcf-poll-seconds", tc.headerValue)
			}
			assert.Equal(t, tc.want, getTargetPollDuration(req))
		})
	}
}

func TestHandleInferenceError(t *testing.T) {
	t.Run("op error maps to connection error", func(t *testing.T) {
		opErr := &net.OpError{Op: "dial", Err: errors.New("connection refused")}
		resp := handleInferenceError(opErr)
		require.NotNil(t, resp)
		assert.Equal(t, 500, resp.StatusCode)
		body, _ := io.ReadAll(resp.Body)
		assert.Contains(t, string(body), "Inference connection error")
	})

	t.Run("timeout net error maps to connection error", func(t *testing.T) {
		resp := handleInferenceError(timeoutErr{})
		require.NotNil(t, resp)
		body, _ := io.ReadAll(resp.Body)
		assert.Contains(t, string(body), "Inference connection error")
	})

	t.Run("generic error maps to internal error", func(t *testing.T) {
		resp := handleInferenceError(errors.New("boom"))
		require.NotNil(t, resp)
		assert.Equal(t, 500, resp.StatusCode)
		body, _ := io.ReadAll(resp.Body)
		assert.Contains(t, string(body), "Internal error while making inference request")
	})

	t.Run("non-timeout net error maps to internal error", func(t *testing.T) {
		resp := handleInferenceError(nonTimeoutNetErr{})
		require.NotNil(t, resp)
		body, _ := io.ReadAll(resp.Body)
		assert.Contains(t, string(body), "Internal error while making inference request")
	})
}

// midStreamErrReader returns some bytes then a non-EOF error.
type midStreamErrReader struct{ served bool }

func (r *midStreamErrReader) Read(p []byte) (int, error) {
	if !r.served {
		r.served = true
		n := copy(p, []byte("partial"))
		return n, nil
	}
	return 0, errors.New("mid-stream read error")
}

// timeoutErr implements net.Error reporting a timeout.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// nonTimeoutNetErr implements net.Error but is not a timeout and not an OpError.
type nonTimeoutNetErr struct{}

func (nonTimeoutNetErr) Error() string   { return "not a timeout" }
func (nonTimeoutNetErr) Timeout() bool   { return false }
func (nonTimeoutNetErr) Temporary() bool { return false }

func TestScanForArtifacts(t *testing.T) {
	t.Run("collects files, skips dirs and progress files", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(root, "progress"), []byte("p"), 0644))
		sub := filepath.Join(root, "sub")
		require.NoError(t, os.Mkdir(sub, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(sub, "b.bin"), []byte("b"), 0644))

		artifacts, err := scanForArtifacts(root)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{
			filepath.Join(root, "a.txt"),
			filepath.Join(sub, "b.bin"),
		}, artifacts)
	})

	t.Run("empty dir returns no artifacts", func(t *testing.T) {
		artifacts, err := scanForArtifacts(t.TempDir())
		require.NoError(t, err)
		assert.Empty(t, artifacts)
	})

	t.Run("missing dir returns error", func(t *testing.T) {
		_, err := scanForArtifacts(filepath.Join(t.TempDir(), "does-not-exist"))
		assert.Error(t, err)
	})
}

func TestShouldHandleBackwardsCompatibility(t *testing.T) {
	newReq := func(headers map[string]string) *http.Request {
		req, _ := http.NewRequest(http.MethodPost, "http://example/", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		return req
	}

	tests := []struct {
		name              string
		compatDisabledCfg bool
		headers           map[string]string
		want              bool
	}{
		{name: "default enabled", compatDisabledCfg: false, want: true},
		{name: "config disabled", compatDisabledCfg: true, want: false},
		{name: "event stream accept disables", headers: map[string]string{"Accept": "text/event-stream"}, want: false},
		{name: "feature header disables", headers: map[string]string{"nvcf-feature-disable-worker-compatibility": "true"}, want: false},
		{name: "non-stream accept still enabled", headers: map[string]string{"Accept": "application/json"}, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := &NVCFWorker{config: Config{V3BackwardsCompatibilityDisabled: tc.compatDisabledCfg}}
			assert.Equal(t, tc.want, w.shouldHandleBackwardsCompatibility(newReq(tc.headers)))
		})
	}
}

func TestLazyReadCloser_Read(t *testing.T) {
	t.Run("reads first then chained reader", func(t *testing.T) {
		second := io.NopCloser(strings.NewReader("world"))
		lrc := &lazyReadCloser{
			ReadCloser:     io.NopCloser(strings.NewReader("hello")),
			nextReadCloser: func() (io.ReadCloser, error) { return second, nil },
		}
		data, err := io.ReadAll(lrc)
		require.NoError(t, err)
		assert.Equal(t, "helloworld", string(data))
		assert.NoError(t, lrc.Close())
	})

	t.Run("nil next returns just the first reader", func(t *testing.T) {
		lrc := &lazyReadCloser{
			ReadCloser:     io.NopCloser(strings.NewReader("only")),
			nextReadCloser: nil,
		}
		data, err := io.ReadAll(lrc)
		require.NoError(t, err)
		assert.Equal(t, "only", string(data))
	})

	t.Run("error from next reader factory is propagated", func(t *testing.T) {
		wantErr := errors.New("factory failed")
		lrc := &lazyReadCloser{
			ReadCloser:     io.NopCloser(strings.NewReader("x")),
			nextReadCloser: func() (io.ReadCloser, error) { return nil, wantErr },
		}
		buf := make([]byte, 8)
		// Drain the first reader to reach EOF, which triggers the factory.
		n, _ := lrc.Read(buf)
		assert.Equal(t, 1, n)
		_, err := lrc.Read(buf)
		assert.ErrorIs(t, err, wantErr)
		// Close must remain safe (ReadCloser stays valid on factory error).
		assert.NoError(t, lrc.Close())
	})

	t.Run("empty first reader recurses into next", func(t *testing.T) {
		lrc := &lazyReadCloser{
			ReadCloser:     io.NopCloser(strings.NewReader("")),
			nextReadCloser: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("second")), nil },
		}
		data, err := io.ReadAll(lrc)
		require.NoError(t, err)
		assert.Equal(t, "second", string(data))
	})
}

func TestStreamRequestBody(t *testing.T) {
	w := &NVCFWorker{}
	work := &pb.WorkerInvokeFunctionRequest{RequestBody: []byte("body-bytes")}
	// requestStreamHandler is nil; streamRequestBody must not dereference it
	// eagerly. Reading the initial RequestBody must succeed; the lazy chained
	// reader is only invoked at EOF which the test deliberately avoids fully
	// triggering by passing a nil handler getter wrapped via the struct field.
	rc := w.streamRequestBody(work, nil)
	require.NotNil(t, rc)
	lrc, ok := rc.(*lazyReadCloser)
	require.True(t, ok)
	buf := make([]byte, len("body-bytes"))
	n, err := io.ReadFull(lrc.ReadCloser, buf)
	require.NoError(t, err)
	assert.Equal(t, "body-bytes", string(buf[:n]))
}

func TestRecordContentType(t *testing.T) {
	// recordContentType uses the span from context. With a background context
	// the span is a non-recording no-op span; this exercises the code path and
	// must not panic.
	header := http.Header{}
	header.Add("Content-Type", "application/json")
	header.Add("Content-Type", "text/plain")
	recordContentType(context.Background(), header)
}

func TestCloserFunc(t *testing.T) {
	called := false
	var f closerFunc = func() error { called = true; return nil }
	require.NoError(t, f.Close())
	assert.True(t, called)

	wantErr := errors.New("close failed")
	var g closerFunc = func() error { return wantErr }
	assert.ErrorIs(t, g.Close(), wantErr)
}

func TestMapBackwardsCompatibilityHttpResponse_SmallBody(t *testing.T) {
	w := &NVCFWorker{}
	work := &pb.WorkerInvokeFunctionRequest{
		RequestId:                  "req-1",
		MaxDirectResponseSizeBytes: 1024,
		RequestTime:                timestamppb.Now(),
	}
	meteringEvent := metering.New(&metering.Config{}, work.RequestId, "sub", "nca", nil)
	responseDir := t.TempDir()

	body := []byte(`{"result": "ok"}`)
	res := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}

	mapped, err := w.mapBackwardsCompatibilityHttpResponse(context.Background(), work, res, responseDir, meteringEvent)
	require.NoError(t, err)
	require.NotNil(t, mapped)
	assert.Equal(t, http.StatusOK, mapped.StatusCode)
	got, err := io.ReadAll(mapped.Body)
	require.NoError(t, err)
	assert.Equal(t, body, got)
	require.NoError(t, mapped.Body.Close())
	assert.Equal(t, int64(len(body)), meteringEvent.InferenceSize)
}

func TestMapBackwardsCompatibilityHttpResponse_ErrorStatus(t *testing.T) {
	w := &NVCFWorker{}
	work := &pb.WorkerInvokeFunctionRequest{
		RequestId:                  "req-err",
		MaxDirectResponseSizeBytes: 1024,
		RequestTime:                timestamppb.Now(),
	}
	meteringEvent := metering.New(&metering.Config{}, work.RequestId, "sub", "nca", nil)

	t.Run("triton style error body surfaces error text", func(t *testing.T) {
		res := &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(`{"error": "bad input tensor"}`)),
		}
		mapped, err := w.mapBackwardsCompatibilityHttpResponse(context.Background(), work, res, t.TempDir(), meteringEvent)
		require.NoError(t, err)
		require.NotNil(t, mapped)
		assert.Equal(t, http.StatusBadRequest, mapped.StatusCode)
		got, _ := io.ReadAll(mapped.Body)
		assert.Contains(t, string(got), "bad input tensor")
	})

	t.Run("body read error is propagated", func(t *testing.T) {
		// A reader that errors mid-stream makes io.CopyN fail with a non-EOF
		// error, which mapBackwardsCompatibilityHttpResponse must surface.
		res := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(&midStreamErrReader{}),
		}
		_, err := w.mapBackwardsCompatibilityHttpResponse(context.Background(), &pb.WorkerInvokeFunctionRequest{
			RequestId:                  "req-read-err",
			MaxDirectResponseSizeBytes: 1 << 20,
			RequestTime:                timestamppb.Now(),
		}, res, t.TempDir(), meteringEvent)
		assert.Error(t, err)
	})

	t.Run("empty error body uses default inference error", func(t *testing.T) {
		res := &http.Response{
			StatusCode: http.StatusInternalServerError,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(`not json`)),
		}
		mapped, err := w.mapBackwardsCompatibilityHttpResponse(context.Background(), work, res, t.TempDir(), meteringEvent)
		require.NoError(t, err)
		require.NotNil(t, mapped)
		got, _ := io.ReadAll(mapped.Body)
		assert.Contains(t, string(got), "Inference error")
	})
}
