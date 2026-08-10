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
	"testing"

	"github.com/stretchr/testify/require"
)

// rangeServer serves body honoring Range requests with 206 partial content,
// mimicking a pre-signed object store that supports byte ranges.
func rangeServer(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "" {
			_, _ = w.Write(body)
			return
		}
		// Parse "bytes=start-end".
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

func TestFetchChunk(t *testing.T) {
	body := []byte("0123456789ABCDEF") // 16 bytes
	chunkSize := 4

	t.Run("206 writes chunk at the correct offset", func(t *testing.T) {
		srv := rangeServer(t, body)
		defer srv.Close()

		d := NewArtifactDownloader("", 1, 1, chunkSize)
		dst := filepath.Join(t.TempDir(), "out.bin")
		f, err := os.Create(dst)
		require.NoError(t, err)

		// chunk 2 -> bytes 8-11 ("89AB")
		require.NoError(t, d.fetchChunk(context.Background(), srv.URL, f, 2))
		require.NoError(t, f.Close())

		got, err := os.ReadFile(dst)
		require.NoError(t, err)
		require.Equal(t, "89AB", string(got[8:12]))
	})

	t.Run("non-206 is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		d := NewArtifactDownloader("", 1, 1, chunkSize)
		f, err := os.Create(filepath.Join(t.TempDir(), "out.bin"))
		require.NoError(t, err)
		defer f.Close()

		err = d.fetchChunk(context.Background(), srv.URL, f, 0)
		require.Error(t, err)
	})
}

func TestDownloadFileFromUrlMultiChunk(t *testing.T) {
	body := []byte("0123456789ABCDEF0123456789") // 26 bytes
	chunkSize := 4

	srv := rangeServer(t, body)
	defer srv.Close()

	d := NewArtifactDownloader("", 2, 2, chunkSize)
	// Target in a not-yet-existing nested dir to exercise dir creation.
	target := filepath.Join(t.TempDir(), "nested", "dir", "file.bin")
	require.NoError(t, d.downloadFileFromUrl(context.Background(), srv.URL, target))

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, body, got, "all chunks reassembled in order")
}

func TestIsRepoWritableCreatesMissingDir(t *testing.T) {
	// baseRepo does not exist yet; isRepoWritable should MkdirAll then succeed.
	repo := filepath.Join(t.TempDir(), "new-repo")
	d := NewArtifactDownloader(repo, 1, 1, 4)
	require.True(t, d.isRepoWritable())

	_, err := os.Stat(repo)
	require.NoError(t, err, "isRepoWritable must create the repo directory")
}

func TestIsRepoWritableFalseWhenPathIsFile(t *testing.T) {
	// A regular file as baseRepo: MkdirAll fails, CreateTemp under it fails.
	file := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0644))

	d := NewArtifactDownloader(file, 1, 1, 4)
	require.False(t, d.isRepoWritable())
}

func TestNormalizeArtifactPathStripsLeadingComponent(t *testing.T) {
	// An absolute path under baseRepo with multiple components drops the first
	// component after the repo prefix (the artifact-name directory).
	base := "/repo"
	got := normalizeArtifactPath(base, "/repo/simple_int8/1/model.bin")
	require.Equal(t, filepath.Join("1", "model.bin"), got)
}
