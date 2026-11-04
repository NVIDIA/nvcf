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
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSourceKeyFromURL(t *testing.T) {
	require.Equal(t, "", sourceKeyFromURL(""))
	require.Equal(t, "", sourceKeyFromURL("%"), "unparseable url")
	require.Equal(t, "", sourceKeyFromURL("https://host/"), "empty path")
	require.Equal(t, "models/m.bin", sourceKeyFromURL("https://host/models/m.bin"))
	require.Equal(t, "models/m.bin?versionId=v2", sourceKeyFromURL("https://host/models/m.bin?versionId=v2"))
}

func TestRedactURL(t *testing.T) {
	require.Equal(t, "https://host/p?REDACTED", redactURL("https://user:pass@host/p?token=secret"))
	require.Equal(t, "https://host/p", redactURL("https://user:pass@host/p"), "userinfo stripped, no query")
	require.Equal(t, "REDACTED", redactURL("%"), "unparseable url never leaks")
}

func TestParseRangeTotalLength(t *testing.T) {
	withRange := func(v string) *http.Response {
		return &http.Response{Header: http.Header{"Content-Range": []string{v}}}
	}
	total, err := parseRangeTotalLength(withRange("bytes 0-3/100"))
	require.NoError(t, err)
	require.Equal(t, int64(100), total)

	_, err = parseRangeTotalLength(&http.Response{Header: http.Header{}})
	require.Error(t, err, "missing Content-Range")
	_, err = parseRangeTotalLength(withRange("garbage"))
	require.Error(t, err, "no slash")
	_, err = parseRangeTotalLength(withRange("bytes 0-3/notanumber"))
	require.Error(t, err, "non-numeric total")
}

func TestArtifactManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, writeArtifactManifest(dir, map[string]string{"models/m": "m/rel"}))

	m, exists, err := readArtifactManifest(dir)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, "m/rel", m["models/m"])

	m2, exists2, err2 := readArtifactManifest(t.TempDir())
	require.NoError(t, err2)
	require.False(t, exists2, "missing manifest")
	require.Empty(t, m2)

	bad := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bad, ".nvcf_manifest.json"), []byte("{not json"), 0644))
	_, _, err3 := readArtifactManifest(bad)
	require.Error(t, err3, "corrupt manifest")
}

func TestIsRepoWritable(t *testing.T) {
	require.True(t, (&ArtifactsDownloader{baseRepo: t.TempDir()}).isRepoWritable())
	require.False(t, (&ArtifactsDownloader{baseRepo: ""}).isRepoWritable(), "empty repo is not writable")
}

func TestFetchFirstChunk(t *testing.T) {
	newDownloader := func() *ArtifactsDownloader {
		return NewArtifactDownloader("", 1, 1, 4) // chunkSize 4
	}

	t.Run("200 downloads the whole file (no ranges)", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("wholefile"))
		}))
		defer srv.Close()
		var buf bytes.Buffer
		chunks, err := newDownloader().fetchFirstChunk(context.Background(), srv.URL, &buf)
		require.NoError(t, err)
		require.Equal(t, int64(0), chunks, "no further chunks when ranges unsupported")
		require.Equal(t, "wholefile", buf.String())
	})

	t.Run("206 computes the chunk count", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Range", "bytes 0-3/10")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("0123"))
		}))
		defer srv.Close()
		var buf bytes.Buffer
		chunks, err := newDownloader().fetchFirstChunk(context.Background(), srv.URL, &buf)
		require.NoError(t, err)
		require.Equal(t, int64(3), chunks, "ceil(10/4)")
		require.Equal(t, "0123", buf.String())
	})

	t.Run("416 retries without the range header", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Range") != "" {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			_, _ = w.Write([]byte("full"))
		}))
		defer srv.Close()
		var buf bytes.Buffer
		chunks, err := newDownloader().fetchFirstChunk(context.Background(), srv.URL, &buf)
		require.NoError(t, err)
		require.Equal(t, int64(0), chunks)
		require.Equal(t, "full", buf.String())
	})

	t.Run("5xx is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		var buf bytes.Buffer
		_, err := newDownloader().fetchFirstChunk(context.Background(), srv.URL, &buf)
		require.Error(t, err)
	})
}
