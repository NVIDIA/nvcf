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

package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
)

// Phase 2 of the 16-node distribution plan (TRANSPORT-ARCHITECTURE.md):
// range-chunked download for large files. Single-stream HTTP caps at
// ~1.4 Gbit/s (Spike 3); splitting one large file into N parallel
// Range GETs lifts per-receiver throughput toward the storage / NIC
// ceiling without changing the protocol — both peer + blobstore
// servers already support Range via http.ServeContent.

// rangeFetchThreshold: files at or above this size route through the
// chunked path. With rangeFetchChunks=1, the chunked path is really
// "single Range GET with userspace 4 MiB WriteAt loop" — empirically
// the fastest receive path in pure Go (~1.20 GB/s for vllm-small
// cross-node). The non-chunked path uses io.Copy's default 32 KiB
// buffer which is significantly slower (~0.93 GB/s) due to syscall
// overhead. We keep the threshold low so large files use the
// efficient WriteAt path; small files (< 64 MiB) use io.Copy which
// is fine because their wall-clock contribution is small.
const rangeFetchThreshold int64 = 64 * 1024 * 1024

// rangeFetchChunks: how many parallel range chunks per large file.
// Currently 1 — the multi-chunk userspace path was slower than
// chunks=1 in the v0.24.8 bench (8 workers × 8 chunks = 64 streams,
// too many). The splice-based multi-chunk path showed promise in
// microbench (1.7 GB/s) but regressed in production cascade due to
// http.Client contention on small files (GH #112).
const rangeFetchChunks = 1

// downloadToFile fetches `expectedSize` bytes and writes them atomically
// to `dstPath`. For files >= rangeFetchThreshold, uses N parallel Range
// GETs concurrently writing to disjoint byte ranges of the destination
// via pwrite. For smaller files, single GET.
//
// `urls` is a non-empty list of source URLs all serving the same
// content. For small files, the first URL is used. For chunked
// downloads, chunks are distributed round-robin across the URLs (Phase 3
// of the 16-node distribution plan: when multiple peers have a large
// file, one receiver pulls chunks from all of them concurrently
// instead of all chunks from a single primary peer).
//
// The destination directory is created if missing. Temp file +
// fsync + rename for crash-safety.
func downloadToFile(
	ctx context.Context,
	httpClient *http.Client,
	urls []string,
	expectedSize int64,
	dstPath string,
) error {
	if expectedSize < 0 {
		return fmt.Errorf("downloadToFile: negative expected size %d", expectedSize)
	}
	if len(urls) == 0 {
		return fmt.Errorf("downloadToFile: no source URLs")
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}
	tmp := dstPath + ".part"

	// Small file: single GET from first URL, stream to disk.
	if expectedSize < rangeFetchThreshold {
		return downloadSingleStream(ctx, httpClient, urls[0], expectedSize, tmp, dstPath)
	}
	// Large file: parallel range chunks distributed across URLs.
	return downloadRangeChunked(ctx, httpClient, urls, expectedSize, tmp, dstPath)
}

func downloadSingleStream(
	ctx context.Context,
	httpClient *http.Client,
	url string,
	expectedSize int64,
	tmp, dstPath string,
) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("status %d: %s", resp.StatusCode, body)
	}
	return writeBodyAtomic(resp.Body, expectedSize, tmp, dstPath)
}

func writeBodyAtomic(body io.Reader, expectedSize int64, tmp, dstPath string) error {
	// O_NOFOLLOW: joinWithinRoot resolved the deepest existing ancestor, but
	// that check and this open are separate syscalls. Refusing to follow a
	// final-component symlink closes the window an attacker is most likely to
	// win. A swapped *parent* directory is still theoretically possible; it
	// requires the ability to create symlinks inside the checkpoint dir, and
	// nothing in the fetch path creates one -- every entry lands as a regular
	// file through this call -- so reaching that state already means root on
	// the node.
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}()
	n, err := io.Copy(f, body)
	if err != nil {
		return fmt.Errorf("io.Copy: %w", err)
	}
	if n != expectedSize {
		return fmt.Errorf("size mismatch: got %d, expected %d", n, expectedSize)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, dstPath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// downloadRangeChunked splits the file into rangeFetchChunks pieces
// and fetches each in parallel via HTTP Range. Chunks are distributed
// round-robin across the provided URLs — when multiple peers have the
// file, one receiver pulls chunks from all of them concurrently.
//
// All chunks write to the same temp file via pwrite at disjoint
// offsets — Go's (*os.File).WriteAt is concurrency-safe per the
// documented contract.
func downloadRangeChunked(
	ctx context.Context,
	httpClient *http.Client,
	urls []string,
	expectedSize int64,
	tmp, dstPath string,
) error {
	// O_NOFOLLOW as above: never write through a final-component symlink.
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	// Pre-allocate so chunk writes don't extend the file repeatedly.
	if err := f.Truncate(expectedSize); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("truncate: %w", err)
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}()

	chunkSize := (expectedSize + int64(rangeFetchChunks) - 1) / int64(rangeFetchChunks)
	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)
	for i := 0; i < rangeFetchChunks; i++ {
		start := int64(i) * chunkSize
		if start >= expectedSize {
			break
		}
		end := start + chunkSize - 1
		if end >= expectedSize {
			end = expectedSize - 1
		}
		// Round-robin chunk → URL assignment. With 1 URL, all chunks
		// go to the same source (Phase 2 behavior). With 2+ URLs,
		// chunks distribute (Phase 3 behavior).
		chunkURL := urls[i%len(urls)]
		wg.Add(1)
		go func(url string, start, end int64) {
			defer wg.Done()
			// Userspace WriteAt path — empirically the fastest stable
			// option in pure Go (4 MiB read+pwrite loop). Splice path
			// (fetchOneRangeViaSplice) is kept in tree but unused
			// pending the Rust receiver work (GH #112).
			if err := fetchOneRange(ctx, httpClient, url, start, end, f); err != nil {
				errOnce.Do(func() { firstErr = err })
			}
		}(chunkURL, start, end)
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, dstPath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// fetchOneRange GETs bytes [start, end] (inclusive, HTTP-Range
// semantics) from url and pwrites them into f at offset `start`.
func fetchOneRange(
	ctx context.Context,
	httpClient *http.Client,
	url string,
	start, end int64,
	f *os.File,
) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("range GET %d-%d: %w", start, end, err)
	}
	defer func() { _ = resp.Body.Close() }()
	// 206 Partial Content is the expected reply. Some servers (and
	// some Range requests for the entire file) may return 200 OK +
	// the whole body; tolerate both.
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("range GET %d-%d status %d: %s", start, end, resp.StatusCode, body)
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		clInt, _ := strconv.ParseInt(cl, 10, 64)
		expected := end - start + 1
		if clInt > 0 && clInt != expected {
			return fmt.Errorf("range %d-%d: content-length %d != %d", start, end, clInt, expected)
		}
	}
	// Stream the body in fixed-size buffers via WriteAt at increasing
	// offsets. WriteAt is safe for concurrent writers as long as their
	// ranges don't overlap — and ranges are by construction disjoint.
	//
	// Buffer size matters: pprof on receiver showed 57% of CPU time in
	// pwrite syscalls. Each pwrite is one syscall + kernel work; bigger
	// buffer = fewer syscalls. 256 KiB → 4 MiB drops syscall count 16×
	// for a 30 GiB file (from ~120K pwrites to ~7.5K). Trade-off: per-
	// chunk memory cost = workers × 4 MiB, which is tiny on an A3 node.
	buf := make([]byte, 4*1024*1024)
	offset := start
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.WriteAt(buf[:n], offset); werr != nil {
				return fmt.Errorf("WriteAt offset=%d: %w", offset, werr)
			}
			offset += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("range %d-%d read: %w", start, end, rerr)
		}
	}
	if offset != end+1 {
		return fmt.Errorf("range %d-%d short read: wrote up to offset %d", start, end, offset)
	}
	return nil
}
