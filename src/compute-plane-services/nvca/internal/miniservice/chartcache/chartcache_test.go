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

package chartcache

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var data = []byte(`{"hello":"world"}`)

func TestChartCache(t *testing.T) {
	var err error
	var servicePort int32 = 8000
	baseInput := ChartCacheInput{
		HelmChartURL:         "https://foo.bar.com/mychart-1.0.1.tgz",
		HelmChartServicePort: &servicePort,
		HelmChartServiceName: "entrypoint",
		Values:               []byte(`{"foo": "bar"}`),
		K8sVersion:           "1.29.2",
		APIVersions:          []string{"foo", "bar"},
	}
	t.Run("Start", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		// Start with no dir to test temp handler.
		chartCache := New("").(*localChartCache)
		err = chartCache.Start(ctx)
		require.NoError(t, err)
		assert.NotEmpty(t, chartCache.dir)

		tmpDir := t.TempDir()
		chartCache = New(tmpDir).(*localChartCache)

		cacheKey, err := baseInput.makeCacheKey()
		require.NoError(t, err)
		assert.Equal(t, "70dc68f23e1da069ae90", cacheKey)
		expFilePathFunction := baseInput.makeItemFilePath(tmpDir, cacheKey)
		writeGZipFile(t, chartCache, expFilePathFunction, data)

		// Test case: Start should have added all cache items to cache.
		err = chartCache.Start(ctx)
		require.NoError(t, err)
		gotFound, err := chartCache.Get(baseInput, io.Discard)
		require.NoError(t, err)
		assert.True(t, gotFound)
	})
	t.Run("Get", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		tmpDir := t.TempDir()
		chartCache := New(tmpDir).(*localChartCache)
		err := chartCache.Start(ctx)
		require.NoError(t, err)

		cacheKey, err := baseInput.makeCacheKey()
		require.NoError(t, err)

		input1 := baseInput
		w := &bytes.Buffer{}
		expFilePath := input1.makeItemFilePath(tmpDir, cacheKey)

		// Test case: the LRU cache has not been written to yet so found should be false.
		gotFound, err := chartCache.Get(input1, w)
		require.NoError(t, err)
		assert.False(t, gotFound)

		_, err = chartCache.Put(input1, bytes.NewReader(data), int64(len(data)))
		require.NoError(t, err)
		assert.FileExists(t, expFilePath)

		gotFound, err = chartCache.Get(input1, w)
		require.NoError(t, err)
		assert.True(t, gotFound)
		assert.Equal(t, string(data), w.String())

		// Test case: different item does not exist in cache
		input2 := baseInput
		input2.APIVersions = nil
		w.Reset()
		gotFound, err = chartCache.Get(input2, w)
		require.NoError(t, err)
		assert.False(t, gotFound)
	})

	t.Run("Put", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		tmpDir := t.TempDir()
		chartCache := New(tmpDir).(*localChartCache)
		err := chartCache.Start(ctx)
		require.NoError(t, err)

		input1 := baseInput

		cacheKey1, err := baseInput.makeCacheKey()
		require.NoError(t, err)

		expFilePath1 := input1.makeItemFilePath(tmpDir, cacheKey1)

		// Test case: write item to cache
		r := bytes.NewReader(data)
		gotHash, err := chartCache.Put(input1, r, int64(r.Len()))
		require.NoError(t, err)
		assert.Equal(t, "sha256:93a23971a914e5eacbf0a8d25154cda309c3c1c72fbb9914d47c60f3cb681588", gotHash)
		_, gotFound := chartCache.cache.Peek(cacheKey1)
		assert.True(t, gotFound)
		assert.FileExists(t, expFilePath1)

		// Test case: write item to cache, but ENOSPC error leads to eviction
		i := 0
		chartCache.putFile = func(fp string) (writerFile, error) {
			i++
			f, err := os.OpenFile(fp, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0600)
			// Return no error the second time this function is called.
			if i > 1 {
				return f, err
			}
			return &mockWriterFile{
				File: f,
				err:  syscall.ENOSPC,
			}, err
		}
		input2 := baseInput
		input2.Values = []byte(`{"bar":"foo"}`)
		r = bytes.NewReader(data)
		_, err = chartCache.Put(input2, r, int64(r.Len()))
		require.NoError(t, err)
		_, gotFound = chartCache.cache.Peek(cacheKey1)
		assert.False(t, gotFound)
		cacheKey2, err := input2.makeCacheKey()
		require.NoError(t, err)
		_, gotFound = chartCache.cache.Peek(cacheKey2)
		assert.True(t, gotFound)
	})

	t.Run("Delete", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		tmpDir := t.TempDir()
		chartCache := New(tmpDir).(*localChartCache)
		err := chartCache.Start(ctx)
		require.NoError(t, err)
		// Test case: item exists in cache
		input1 := baseInput
		input2 := baseInput
		input2.HelmChartServiceName = "entrypoint2"
		input3 := baseInput
		input3.HelmChartServiceName = "entrypoint3"
		input4 := baseInput
		input4.HelmChartServiceName = "entrypoint4"
		cacheKey1, err := input1.makeCacheKey()
		require.NoError(t, err)
		expFilePath1 := input1.makeItemFilePath(tmpDir, cacheKey1)
		cacheKey2, err := input2.makeCacheKey()
		require.NoError(t, err)
		expFilePath2 := input2.makeItemFilePath(tmpDir, cacheKey2)
		cacheKey3, err := input3.makeCacheKey()
		require.NoError(t, err)
		expFilePath3 := input3.makeItemFilePath(tmpDir, cacheKey3)
		cacheKey4, err := input4.makeCacheKey()
		require.NoError(t, err)
		expFilePath4 := input4.makeItemFilePath(tmpDir, cacheKey4)

		r := bytes.NewReader(data)
		_, err = chartCache.Put(input1, r, int64(r.Len()))
		require.NoError(t, err)
		assert.FileExists(t, expFilePath1)
		r = bytes.NewReader(data)
		_, err = chartCache.Put(input2, r, int64(r.Len()))
		require.NoError(t, err)
		assert.FileExists(t, expFilePath2)
		r = bytes.NewReader(data)
		_, err = chartCache.Put(input3, r, int64(r.Len()))
		require.NoError(t, err)
		assert.FileExists(t, expFilePath3)
		r = bytes.NewReader(data)
		_, err = chartCache.Put(input4, r, int64(r.Len()))
		require.NoError(t, err)
		assert.FileExists(t, expFilePath4)
		_, gotFound := chartCache.cache.Peek(cacheKey1)
		assert.True(t, gotFound)
		_, gotFound = chartCache.cache.Peek(cacheKey2)
		assert.True(t, gotFound)
		_, gotFound = chartCache.cache.Peek(cacheKey3)
		assert.True(t, gotFound)
		_, gotFound = chartCache.cache.Peek(cacheKey4)
		assert.True(t, gotFound)

		gotFuncEntries, err := os.ReadDir(tmpDir)
		require.NoError(t, err)
		assert.Len(t, gotFuncEntries, 4)

		// Deleting input1 should remove the directory but not its siblings.
		err = chartCache.Delete(input1)
		require.NoError(t, err)
		gotFuncEntries, err = os.ReadDir(tmpDir)
		require.NoError(t, err)
		assert.Len(t, gotFuncEntries, 3)
		assert.NoFileExists(t, expFilePath1)
		assert.FileExists(t, expFilePath2)
		assert.FileExists(t, expFilePath3)
		assert.FileExists(t, expFilePath4)
		_, gotFound = chartCache.cache.Peek(cacheKey1)
		assert.False(t, gotFound)
		_, gotFound = chartCache.cache.Peek(cacheKey2)
		assert.True(t, gotFound)
		_, gotFound = chartCache.cache.Peek(cacheKey3)
		assert.True(t, gotFound)
		_, gotFound = chartCache.cache.Peek(cacheKey4)
		assert.True(t, gotFound)

		// Delete the second one, same outcome.
		err = chartCache.Delete(input2)
		require.NoError(t, err)
		gotFuncEntries, err = os.ReadDir(tmpDir)
		require.NoError(t, err)
		assert.Len(t, gotFuncEntries, 2)
		assert.NoFileExists(t, expFilePath1)
		assert.NoFileExists(t, expFilePath2)
		assert.FileExists(t, expFilePath3)
		assert.FileExists(t, expFilePath4)
		_, gotFound = chartCache.cache.Peek(cacheKey1)
		assert.False(t, gotFound)
		_, gotFound = chartCache.cache.Peek(cacheKey2)
		assert.False(t, gotFound)
		_, gotFound = chartCache.cache.Peek(cacheKey3)
		assert.True(t, gotFound)
		_, gotFound = chartCache.cache.Peek(cacheKey4)
		assert.True(t, gotFound)

		// Delete them all but ensure parent dir still exists
		err = chartCache.Delete(input3)
		require.NoError(t, err)
		err = chartCache.Delete(input4)
		require.NoError(t, err)
		gotFuncEntries, err = os.ReadDir(tmpDir)
		require.NoError(t, err)
		assert.Len(t, gotFuncEntries, 0)
		assert.NoFileExists(t, expFilePath1)
		assert.NoFileExists(t, expFilePath2)
		assert.NoFileExists(t, expFilePath3)
		assert.NoFileExists(t, expFilePath4)

		// Put then delete.
		r = bytes.NewReader(data)
		gotHash, err := chartCache.Put(input1, r, int64(r.Len()))
		require.NoError(t, err)
		assert.Equal(t, "sha256:93a23971a914e5eacbf0a8d25154cda309c3c1c72fbb9914d47c60f3cb681588", gotHash)
		exists, err := chartCache.Get(input1, io.Discard)
		require.NoError(t, err)
		assert.True(t, exists)
		gotFuncEntries, err = os.ReadDir(tmpDir)
		require.NoError(t, err)
		assert.Len(t, gotFuncEntries, 1)
		assert.FileExists(t, expFilePath1)
		err = chartCache.Delete(input1)
		require.NoError(t, err)
		gotFuncEntries, err = os.ReadDir(tmpDir)
		require.NoError(t, err)
		assert.Len(t, gotFuncEntries, 0)
		assert.NoFileExists(t, expFilePath1)

	})
}

type mockWriterFile struct {
	*os.File
	err error
}

func (w *mockWriterFile) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	return w.File.Write(p)
}

func writeGZipFile(t *testing.T, chartCache *localChartCache, filePath string, data []byte) {
	t.Helper()
	err := os.MkdirAll(filepath.Dir(filePath), 0700)
	require.NoError(t, err)
	tf, err := chartCache.putFile(filePath)
	require.NoError(t, err)
	gzwt := gzip.NewWriter(tf)
	_, err = gzwt.Write(data)
	require.NoError(t, err)
	err = gzwt.Close()
	require.NoError(t, err)
}
