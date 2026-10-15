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

package upload

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/secrets"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/pkg/ngc"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"
)

// newTestSecrets writes a minimal secrets file and constructs a *secrets.Secrets
// backed by it. The NGC api key value is irrelevant for these tests because the
// httptest servers ignore the Authorization header.
func newTestSecrets(t *testing.T) *secrets.Secrets {
	t.Helper()

	secretsPath := filepath.Join(t.TempDir(), "secrets.json")
	require.NoError(t, os.WriteFile(secretsPath, []byte(`{"NGC_API_KEY":"test-key"}`), 0o600))

	s, err := secrets.New(context.Background(), secretsPath)
	require.NoError(t, err)
	return s
}

// writeFileOfSize creates a file containing exactly size deterministic bytes.
func writeFileOfSize(t *testing.T, size int64) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "result.bin")
	f, err := os.Create(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()

	if size > 0 {
		buf := make([]byte, size)
		for i := range buf {
			buf[i] = byte(i % 251)
		}
		_, err = f.Write(buf)
		require.NoError(t, err)
	}
	return path
}

// ----------------------------------------------------------------------------
// getChunk
// ----------------------------------------------------------------------------

func TestGetChunk(t *testing.T) {
	tests := []struct {
		name              string
		fileSize          int64
		expectedChunkSize int64
	}{
		{name: "zero bytes", fileSize: 0, expectedChunkSize: 5 * utils.OneMB},
		{name: "one byte", fileSize: 1, expectedChunkSize: 5 * utils.OneMB},
		{name: "exactly one chunk", fileSize: 5 * utils.OneMB, expectedChunkSize: 5 * utils.OneMB},
		{name: "just over one chunk", fileSize: 5*utils.OneMB + 1, expectedChunkSize: 5 * utils.OneMB},
		{name: "multi chunk", fileSize: 23 * utils.OneMB, expectedChunkSize: 5 * utils.OneMB},
		{name: "boundary 50GB", fileSize: 50 * utils.OneGB, expectedChunkSize: 5 * utils.OneMB},
		{name: "just over 50GB", fileSize: 50*utils.OneGB + 1, expectedChunkSize: 10 * utils.OneMB},
		{name: "boundary 100GB", fileSize: 100 * utils.OneGB, expectedChunkSize: 10 * utils.OneMB},
		{name: "just over 100GB", fileSize: 100*utils.OneGB + 1, expectedChunkSize: 20 * utils.OneMB},
		{name: "boundary 150GB", fileSize: 150 * utils.OneGB, expectedChunkSize: 20 * utils.OneMB},
		{name: "just over 150GB", fileSize: 150*utils.OneGB + 1, expectedChunkSize: 40 * utils.OneMB},
		{name: "boundary 350GB", fileSize: 350 * utils.OneGB, expectedChunkSize: 40 * utils.OneMB},
		{name: "very large file", fileSize: 350*utils.OneGB + 1, expectedChunkSize: 250 * utils.OneMB},
		{name: "huge file", fileSize: 2000 * utils.OneGB, expectedChunkSize: 250 * utils.OneMB},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chunkSize, chunkCount := getChunk(tc.fileSize)

			assert.Equal(t, tc.expectedChunkSize, chunkSize, "chunk size tier mismatch")

			// Invariants.
			assert.Greater(t, chunkSize, int64(0), "chunkSize must be positive")

			if tc.fileSize > 0 {
				assert.GreaterOrEqual(t, chunkCount, 1, "positive sizes must yield at least one chunk")
			}

			// The chunks must be able to hold the whole file.
			assert.GreaterOrEqual(t, chunkSize*int64(chunkCount), tc.fileSize,
				"chunkSize*chunkCount must cover the file size")
		})
	}
}

func TestGetChunkCountMath(t *testing.T) {
	// 23 MB at 5 MB parts -> ceil(23/5) = 5 parts.
	chunkSize, chunkCount := getChunk(23 * utils.OneMB)
	assert.Equal(t, int64(5*utils.OneMB), chunkSize)
	assert.Equal(t, 5, chunkCount)

	// A file one byte larger than a whole number of chunks rolls to the next chunk.
	chunkSize, chunkCount = getChunk(10*utils.OneMB + 1)
	assert.Equal(t, int64(5*utils.OneMB), chunkSize)
	assert.Equal(t, 3, chunkCount)

	// Zero bytes produces zero chunks (ceil(0) == 0).
	_, chunkCount = getChunk(0)
	assert.Equal(t, 0, chunkCount)
}

// TestGetChunkS3PartLimit documents that getChunk does not actually enforce the
// S3 "<= 10,000 parts" constraint described in its own comment. At the 50GB
// boundary the 5MB tier yields 10240 parts, which exceeds 10,000. This test
// pins the current (buggy) behavior; see the source note in the task report.
func TestGetChunkS3PartLimit(t *testing.T) {
	_, chunkCount := getChunk(50 * utils.OneGB)
	assert.Equal(t, 10240, chunkCount,
		"50GB at the 5MB tier currently produces 10240 parts, over the documented 10k S3 limit")
}

// ----------------------------------------------------------------------------
// uploadResultFile
// ----------------------------------------------------------------------------

// startNgcStub returns an httptest server that emulates the NGC multipart API
// plus the presigned S3 PUT targets. presignedCount controls how many presigned
// urls StartModelUpload hands back; putStatus is the status code returned for the
// presigned PUTs.
func startNgcStub(t *testing.T, presignedCount int, putStatus int) (*httptest.Server, *stubCounters) {
	t.Helper()

	counters := &stubCounters{}

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Presigned chunk upload target (absolute URL handed to the client).
	mux.HandleFunc("/presigned/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		atomic.AddInt32(&counters.putCalls, 1)
		w.WriteHeader(putStatus)
	})

	// NGC multipart endpoint: POST starts the upload, PUT completes it,
	// DELETE aborts it.
	mux.HandleFunc("/v2/org/test-org/files/multipart", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			atomic.AddInt32(&counters.startCalls, 1)

			urls := make([]string, presignedCount)
			for i := range urls {
				urls[i] = fmt.Sprintf("%s/presigned/%d", srv.URL, i)
			}
			resp := ngc.ModelUploadResponse{
				UploadId: "upload-123",
				PartSize: 0,
				Urls:     urls,
			}
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(resp))
		case http.MethodPut:
			atomic.AddInt32(&counters.completeCalls, 1)
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			atomic.AddInt32(&counters.abortCalls, 1)
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected method %s on multipart endpoint", r.Method)
		}
	})

	return srv, counters
}

type stubCounters struct {
	startCalls    int32
	putCalls      int32
	completeCalls int32
	abortCalls    int32
	createModel   int32
	createVersion int32
	updateModel   int32
}

// startFullNgcStub emulates the entire NGC model registry surface used by
// UploadResult: model create, version create, multipart start/complete, the
// presigned PUTs, and the final model status update.
func startFullNgcStub(t *testing.T, putStatus int) (*httptest.Server, *stubCounters) {
	t.Helper()

	counters := &stubCounters{}

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/presigned/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&counters.putCalls, 1)
		w.WriteHeader(putStatus)
	})

	// POST /models -> create model.
	mux.HandleFunc("/v2/org/test-org/models", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&counters.createModel, 1)
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/v2/org/test-org/files/multipart", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			atomic.AddInt32(&counters.startCalls, 1)
			// Derive chunk count from the requested size so any walked file works.
			var req struct {
				Size           int64 `json:"size"`
				CustomPartSize int64 `json:"customPartSize"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			count := 1
			if req.CustomPartSize > 0 {
				_, count = getChunk(req.Size)
			}
			urls := make([]string, count)
			for i := range urls {
				urls[i] = fmt.Sprintf("%s/presigned/%d", srv.URL, i)
			}
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(ngc.ModelUploadResponse{UploadId: "u", Urls: urls}))
		case http.MethodPut:
			atomic.AddInt32(&counters.completeCalls, 1)
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			atomic.AddInt32(&counters.abortCalls, 1)
			w.WriteHeader(http.StatusOK)
		}
	})

	// POST /models/<m>/versions and PATCH /models/<m>/versions/<v>.
	mux.HandleFunc("/v2/org/test-org/models/test-model/versions", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&counters.createVersion, 1)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v2/org/test-org/models/test-model/versions/v1", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&counters.updateModel, 1)
		w.WriteHeader(http.StatusOK)
	})

	return srv, counters
}

func newUploaderForServer(t *testing.T, serverURL string) *NgcUploader {
	t.Helper()

	client, err := ngc.NewClient("test-org", "", serverURL)
	require.NoError(t, err)

	return &NgcUploader{
		model:     "test-model",
		secrets:   newTestSecrets(t),
		ngcClient: client,
	}
}

func TestUploadResultFileHappyPath(t *testing.T) {
	// 12 MB file at 5 MB parts -> 3 chunks, so 3 presigned urls expected.
	const size = 12 * utils.OneMB
	_, chunkCount := getChunk(size)
	require.Equal(t, 3, chunkCount)

	srv, counters := startNgcStub(t, chunkCount, http.StatusOK)
	uploader := newUploaderForServer(t, srv.URL)

	localPath := writeFileOfSize(t, size)

	err := uploader.uploadResultFile(context.Background(), "v1", localPath, "result.bin", size)
	require.NoError(t, err)

	assert.Equal(t, int32(1), atomic.LoadInt32(&counters.startCalls))
	assert.Equal(t, int32(chunkCount), atomic.LoadInt32(&counters.putCalls), "one PUT per chunk")
	assert.Equal(t, int32(1), atomic.LoadInt32(&counters.completeCalls))
	assert.Equal(t, int32(0), atomic.LoadInt32(&counters.abortCalls), "no abort on success")
	assert.False(t, uploader.abortedUploads)
}

func TestUploadResultFileSingleChunk(t *testing.T) {
	// A small file fits in a single chunk.
	const size = 1024
	_, chunkCount := getChunk(size)
	require.Equal(t, 1, chunkCount)

	srv, counters := startNgcStub(t, chunkCount, http.StatusOK)
	uploader := newUploaderForServer(t, srv.URL)

	localPath := writeFileOfSize(t, size)

	err := uploader.uploadResultFile(context.Background(), "v1", localPath, "small.bin", size)
	require.NoError(t, err)

	assert.Equal(t, int32(1), atomic.LoadInt32(&counters.putCalls))
	assert.Equal(t, int32(1), atomic.LoadInt32(&counters.completeCalls))
}

func TestUploadResultFilePresignedCountMismatch(t *testing.T) {
	const size = 12 * utils.OneMB
	_, chunkCount := getChunk(size)
	require.Equal(t, 3, chunkCount)

	// Hand back one fewer presigned url than chunks to trigger the mismatch guard.
	srv, counters := startNgcStub(t, chunkCount-1, http.StatusOK)
	uploader := newUploaderForServer(t, srv.URL)

	localPath := writeFileOfSize(t, size)

	err := uploader.uploadResultFile(context.Background(), "v1", localPath, "result.bin", size)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "presigned urls")

	// No chunk PUTs should happen, and the upload must be aborted.
	assert.Equal(t, int32(0), atomic.LoadInt32(&counters.putCalls))
	assert.Equal(t, int32(1), atomic.LoadInt32(&counters.abortCalls), "mismatch must abort the upload")
	assert.True(t, uploader.abortedUploads)
}

func TestUploadResultFileChunkPutFailure(t *testing.T) {
	const size = 12 * utils.OneMB
	_, chunkCount := getChunk(size)

	// Presigned PUTs return 500 so each chunk upload fails.
	srv, counters := startNgcStub(t, chunkCount, http.StatusInternalServerError)
	uploader := newUploaderForServer(t, srv.URL)

	localPath := writeFileOfSize(t, size)

	// The presigned PUTs 500, and the underlying retryable HTTP client and the
	// backoff loop in UploadModel both retry; a short context deadline keeps the
	// test fast while still surfacing the upload failure.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := uploader.uploadResultFile(ctx, "v1", localPath, "result.bin", size)
	require.Error(t, err)

	assert.GreaterOrEqual(t, atomic.LoadInt32(&counters.putCalls), int32(1), "at least one PUT attempted")
	assert.Equal(t, int32(0), atomic.LoadInt32(&counters.completeCalls), "complete must not run on failure")
	assert.Equal(t, int32(1), atomic.LoadInt32(&counters.abortCalls), "chunk failure must abort the upload")
	assert.True(t, uploader.abortedUploads)
}

func TestUploadResultFileStartUploadFails(t *testing.T) {
	// A server that always 500s on the multipart POST makes StartModelUpload fail.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	uploader := newUploaderForServer(t, srv.URL)

	localPath := writeFileOfSize(t, 1024)

	// The retryable client and backoff loop retry on 500; a short deadline keeps
	// the test fast.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := uploader.uploadResultFile(ctx, "v1", localPath, "result.bin", 1024)
	require.Error(t, err)
	// StartModelUpload returns before abortUpload is armed, so no abort recorded.
	assert.False(t, uploader.abortedUploads)
}

func TestUploadResultFileMissingLocalFile(t *testing.T) {
	const size = 1024
	_, chunkCount := getChunk(size)

	srv, counters := startNgcStub(t, chunkCount, http.StatusOK)
	uploader := newUploaderForServer(t, srv.URL)

	missing := filepath.Join(t.TempDir(), "does-not-exist.bin")

	err := uploader.uploadResultFile(context.Background(), "v1", missing, "result.bin", size)
	require.Error(t, err)
	assert.True(t, os.IsNotExist(err) || err != nil)
	assert.Equal(t, int32(1), atomic.LoadInt32(&counters.abortCalls), "open failure must abort the upload")
	assert.True(t, uploader.abortedUploads)
}

// ----------------------------------------------------------------------------
// UploadResult / Submit / Wait
// ----------------------------------------------------------------------------

// writeResultTree builds a result directory containing a couple of files (one
// nested) and returns the root path.
func writeResultTree(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.bin"), []byte("hello world"), 0o600))

	sub := filepath.Join(root, "nested")
	require.NoError(t, os.Mkdir(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "b.bin"), []byte("second file"), 0o600))

	return root
}

func TestUploadResultHappyPath(t *testing.T) {
	srv, counters := startFullNgcStub(t, http.StatusOK)
	uploader := newUploaderForServer(t, srv.URL)

	root := writeResultTree(t)

	err := uploader.UploadResult(context.Background(), root, "v1")
	require.NoError(t, err)

	assert.Equal(t, int32(1), atomic.LoadInt32(&counters.createModel))
	assert.Equal(t, int32(1), atomic.LoadInt32(&counters.createVersion))
	assert.Equal(t, int32(2), atomic.LoadInt32(&counters.startCalls), "two files walked")
	assert.Equal(t, int32(2), atomic.LoadInt32(&counters.completeCalls))
	assert.Equal(t, int32(1), atomic.LoadInt32(&counters.updateModel))
}

func TestUploadResultWalkMissingPath(t *testing.T) {
	srv, _ := startFullNgcStub(t, http.StatusOK)
	uploader := newUploaderForServer(t, srv.URL)

	missing := filepath.Join(t.TempDir(), "no-such-dir")

	err := uploader.UploadResult(context.Background(), missing, "v1")
	require.Error(t, err)
}

func TestSubmitAndWaitSuccess(t *testing.T) {
	srv, counters := startFullNgcStub(t, http.StatusOK)
	uploader := newUploaderForServer(t, srv.URL)

	root := writeResultTree(t)

	uploader.Submit(context.Background(), root, "v1")
	require.NoError(t, uploader.Wait())

	assert.Equal(t, int32(1), atomic.LoadInt32(&counters.updateModel))
}

func TestSubmitAndWaitFailure(t *testing.T) {
	// Presigned PUTs 500 so the upload (and therefore the submitted job) fails.
	srv, _ := startFullNgcStub(t, http.StatusInternalServerError)
	uploader := newUploaderForServer(t, srv.URL)

	// Single-file tree: UploadResult walks files concurrently and each failing
	// uploadResultFile writes the shared NgcUploader.abortedUploads field without
	// synchronization (data race in the source, see report). Keeping one file
	// avoids two goroutines racing on that write while still exercising the
	// failure path end to end.
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "only.bin"), []byte("payload"), 0o600))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	uploader.Submit(ctx, root, "v1")
	require.Error(t, uploader.Wait())
}

// ----------------------------------------------------------------------------
// MockUploader
// ----------------------------------------------------------------------------

func TestMockUploader(t *testing.T) {
	m := NewMockUploader(2)
	require.NotNil(t, m)

	m.Submit(context.Background(), "some/path", "v1")
	require.NoError(t, m.Wait())
}

// ----------------------------------------------------------------------------
// NewUploader
// ----------------------------------------------------------------------------

func TestNewUploader(t *testing.T) {
	s := newTestSecrets(t)

	t.Run("org and model", func(t *testing.T) {
		u, err := NewUploader(context.Background(), "my-org/my-model", "https://ngc.example.com", s)
		require.NoError(t, err)
		assert.Equal(t, "my-model", u.model)
		assert.False(t, u.abortedUploads)
	})

	t.Run("org team and model", func(t *testing.T) {
		u, err := NewUploader(context.Background(), "my-org/my-team/my-model", "https://ngc.example.com", s)
		require.NoError(t, err)
		assert.Equal(t, "my-model", u.model)
	})

	t.Run("invalid location", func(t *testing.T) {
		_, err := NewUploader(context.Background(), "only-one-part", "https://ngc.example.com", s)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid results upload location")
	})

	t.Run("too many parts", func(t *testing.T) {
		_, err := NewUploader(context.Background(), "a/b/c/d", "https://ngc.example.com", s)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid results upload location")
	})
}
