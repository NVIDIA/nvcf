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

package downloader

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/types"
)

// multiFileRangeServer serves a set of files keyed by URL path, honoring Range
// requests with 206 partial content. requestCount tracks total requests so a
// cache-hit second pass can be asserted to issue no new fetches.
func multiFileRangeServer(t *testing.T, files map[string][]byte, requestCount *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requestCount != nil {
			atomic.AddInt32(requestCount, 1)
		}
		body, ok := files[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "" {
			_, _ = w.Write(body)
			return
		}
		spec := strings.TrimPrefix(rangeHeader, "bytes=")
		parts := strings.SplitN(spec, "-", 2)
		start, _ := strconv.Atoi(parts[0])
		end, _ := strconv.Atoi(parts[1])
		if start >= len(body) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if end >= len(body) {
			end = len(body) - 1
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body[start : end+1])
	}))
}

func TestDownloadArtifactsHappyPathAndCache(t *testing.T) {
	files := map[string][]byte{
		"/files/config.pbtxt":     []byte("config-bytes"),
		"/files/1/model.graphdef": []byte("graphdef-bytes-longer-than-a-chunk"),
	}
	var reqCount int32
	srv := multiFileRangeServer(t, files, &reqCount)
	defer srv.Close()

	downloadDir := t.TempDir()
	d := NewArtifactDownloader(downloadDir, 2, 2, 4)

	artifacts := []types.Artifact{
		{Name: "model_a", Version: "1", Path: "config.pbtxt", Url: srv.URL + "/files/config.pbtxt"},
		{Name: "model_a", Version: "1", Path: "1/model.graphdef", Url: srv.URL + "/files/1/model.graphdef"},
	}

	require.NoError(t, d.DownloadArtifacts(context.Background(), artifacts))

	for _, a := range artifacts {
		got, err := os.ReadFile(filepath.Join(downloadDir, a.Name, a.Path))
		require.NoError(t, err)
		require.Equal(t, files["/files/"+a.Path], got)
	}

	// A manifest entry should have been written for each installed artifact.
	manifest, exists, err := readArtifactManifest(downloadDir)
	require.NoError(t, err)
	require.True(t, exists)
	require.NotEmpty(t, manifest)

	// Second pass: everything is cached, so no new artifact bytes are fetched.
	before := atomic.LoadInt32(&reqCount)
	require.NoError(t, d.DownloadArtifacts(context.Background(), artifacts))
	require.Equal(t, before, atomic.LoadInt32(&reqCount), "cached artifacts must not be re-fetched")
}

func TestDownloadArtifactsFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	d := NewArtifactDownloader(t.TempDir(), 1, 1, 4)
	// fetchFirstChunkWithRetry retries a 5xx with exponential backoff up to
	// retryCount times. Bound the wait with a short context deadline so the
	// error path returns promptly instead of running the full backoff series.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	err := d.DownloadArtifacts(ctx, []types.Artifact{
		{Name: "model_a", Version: "1", Path: "f.bin", Url: srv.URL + "/files/f.bin"},
	})
	require.Error(t, err, "a 5xx download must surface as an error")
}

func TestDownloadArtifactsEmptyList(t *testing.T) {
	d := NewArtifactDownloader(t.TempDir(), 1, 1, 4)
	require.NoError(t, d.DownloadArtifacts(context.Background(), nil))
}
