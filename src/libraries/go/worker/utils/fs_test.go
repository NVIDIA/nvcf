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

package utils

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zip"
	"github.com/stretchr/testify/require"
	"github.com/valyala/bytebufferpool"
)

func TestZipFolderAndAddFileToZip(t *testing.T) {
	src := t.TempDir()
	// Nested layout to exercise relative-path computation and directory skipping.
	require.NoError(t, os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(src, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("beta-content"), 0o644))

	var buf bytes.Buffer
	require.NoError(t, ZipFolder(src, &buf))

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)

	got := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)
		data, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
		got[filepath.ToSlash(f.Name)] = string(data)
	}

	require.Equal(t, "alpha", got["a.txt"])
	require.Equal(t, "beta-content", got["sub/b.txt"])
	require.Len(t, got, 2, "directories themselves are not added as entries")
}

func TestZipFolderWalkError(t *testing.T) {
	// Point at a path that does not exist so filepath.Walk surfaces an error
	// into the walk callback (err != nil branch).
	var buf bytes.Buffer
	err := ZipFolder(filepath.Join(t.TempDir(), "does-not-exist"), &buf)
	require.Error(t, err)
}

func TestAddFileToZipOpenError(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	defer zw.Close()

	missing := filepath.Join(t.TempDir(), "nope.txt")
	err := AddFileToZip(missing, t.TempDir(), zw)
	require.Error(t, err, "opening a missing file must fail")
}

func TestMultipartReadToBuffer(t *testing.T) {
	t.Run("splits input into parts and returns total size", func(t *testing.T) {
		// 25 bytes with maxPartSize 10 => parts of 10, 10, 5.
		payload := bytes.Repeat([]byte("x"), 25)
		dst := make(chan *bytebufferpool.ByteBuffer, 8)

		total, err := MultipartReadToBuffer(bytes.NewReader(payload), dst, 10)
		close(dst)
		require.NoError(t, err)
		require.Equal(t, int64(25), total)

		var sizes []int
		var reassembled []byte
		for b := range dst {
			sizes = append(sizes, b.Len())
			reassembled = append(reassembled, b.Bytes()...)
			bytebufferpool.Put(b)
		}
		require.Equal(t, []int{10, 10, 5}, sizes)
		require.Equal(t, payload, reassembled)
	})

	t.Run("exact multiple of part size emits no trailing empty buffer", func(t *testing.T) {
		payload := bytes.Repeat([]byte("y"), 20)
		dst := make(chan *bytebufferpool.ByteBuffer, 8)

		total, err := MultipartReadToBuffer(bytes.NewReader(payload), dst, 10)
		close(dst)
		require.NoError(t, err)
		require.Equal(t, int64(20), total)

		count := 0
		for b := range dst {
			count++
			bytebufferpool.Put(b)
		}
		require.Equal(t, 2, count, "final EOF read with written==0 must not push an empty buffer")
	})

	t.Run("empty input emits nothing", func(t *testing.T) {
		dst := make(chan *bytebufferpool.ByteBuffer, 1)
		total, err := MultipartReadToBuffer(bytes.NewReader(nil), dst, 10)
		close(dst)
		require.NoError(t, err)
		require.Equal(t, int64(0), total)
		_, ok := <-dst
		require.False(t, ok, "channel should be empty")
	})

	t.Run("propagates reader error", func(t *testing.T) {
		dst := make(chan *bytebufferpool.ByteBuffer, 1)
		r := &errReader{data: []byte("abc"), failAfter: 3, err: errors.New("read boom")}

		total, err := MultipartReadToBuffer(r, dst, 100)
		close(dst)
		require.Error(t, err)
		require.EqualError(t, err, "read boom")
		require.Equal(t, int64(3), total)
		_, ok := <-dst
		require.False(t, ok, "buffer must be returned to pool, not pushed, on error")
	})
}

// errReader returns data, then a non-EOF error.
type errReader struct {
	data      []byte
	off       int
	failAfter int
	err       error
}

func (e *errReader) Read(p []byte) (int, error) {
	if e.off >= e.failAfter {
		return 0, e.err
	}
	n := copy(p, e.data[e.off:])
	e.off += n
	if e.off >= e.failAfter {
		return n, e.err
	}
	return n, nil
}
