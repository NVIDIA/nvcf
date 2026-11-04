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
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// rangeAwareServer serves a fixed payload via http.ServeContent, which
// honors Range natively. Counts requests so tests can assert chunk
// count for the chunked path.
type rangeAwareServer struct {
	srv      *httptest.Server
	payload  []byte
	requests int64
	ranges   []string // captured Range header per request, in order
}

func newRangeAwareServer(t *testing.T, payload []byte) *rangeAwareServer {
	t.Helper()
	ras := &rangeAwareServer{payload: payload}
	mux := http.NewServeMux()
	mux.HandleFunc("/data", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&ras.requests, 1)
		if rh := r.Header.Get("Range"); rh != "" {
			ras.ranges = append(ras.ranges, rh)
		}
		http.ServeContent(w, r, "data", time.Time{}, &readSeekerAt{data: payload})
	})
	ras.srv = httptest.NewServer(mux)
	t.Cleanup(ras.srv.Close)
	return ras
}

// readSeekerAt is io.ReadSeeker over a byte slice — http.ServeContent
// requires both io.Reader and io.Seeker. (bytes.Reader exists but we
// want a tiny self-contained one for the test.)
type readSeekerAt struct {
	data []byte
	pos  int64
}

func (r *readSeekerAt) Read(p []byte) (int, error) {
	if r.pos >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += int64(n)
	return n, nil
}

func (r *readSeekerAt) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		r.pos = offset
	case io.SeekCurrent:
		r.pos += offset
	case io.SeekEnd:
		r.pos = int64(len(r.data)) + offset
	}
	return r.pos, nil
}

func (ras *rangeAwareServer) URL() string { return ras.srv.URL + "/data" }
func (ras *rangeAwareServer) Hits() int64 { return atomic.LoadInt64(&ras.requests) }

func TestDownloadToFile_Small_SingleStream(t *testing.T) {
	// 1 MiB payload — well below threshold.
	payload := randomBytes(t, 1*1024*1024)
	ras := newRangeAwareServer(t, payload)

	dstDir := t.TempDir()
	dst := filepath.Join(dstDir, "small.bin")

	if err := downloadToFile(context.Background(), http.DefaultClient, []string{ras.URL()}, int64(len(payload)), dst); err != nil {
		t.Fatalf("downloadToFile failed: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if !equalBytes(got, payload) {
		t.Errorf("payload mismatch for small file")
	}
	if ras.Hits() != 1 {
		t.Errorf("small file should be 1 request, got %d", ras.Hits())
	}
	if len(ras.ranges) != 0 {
		t.Errorf("small file should not send Range header, got %v", ras.ranges)
	}
}

func TestDownloadToFile_Large_RangeChunked(t *testing.T) {
	// 128 MiB payload — above 64 MiB threshold.
	payload := randomBytes(t, 128*1024*1024)
	ras := newRangeAwareServer(t, payload)

	dstDir := t.TempDir()
	dst := filepath.Join(dstDir, "large.bin")

	if err := downloadToFile(context.Background(), http.DefaultClient, []string{ras.URL()}, int64(len(payload)), dst); err != nil {
		t.Fatalf("downloadToFile failed: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if !equalBytes(got, payload) {
		t.Errorf("payload mismatch for large file (got %d bytes, want %d)", len(got), len(payload))
	}
	// chunks=1 + threshold=64MiB: file above threshold routes through
	// downloadRangeChunked with one chunk = one Range GET.
	if got := ras.Hits(); got != int64(rangeFetchChunks) {
		t.Errorf("expected %d request, got %d", rangeFetchChunks, got)
	}
	if len(ras.ranges) != rangeFetchChunks {
		t.Errorf("expected %d Range headers, got %d: %v", rangeFetchChunks, len(ras.ranges), ras.ranges)
	}
}

// TestDownloadToFile_Threshold_* tests are skipped — rangeFetchThreshold
// is now set to MaxInt64 (chunked path disabled). The boundary behavior
// these tests covered is no longer meaningful. Kept as t.Skip stubs so
// future readers see the intent if we ever revive chunking.
func TestDownloadToFile_Threshold_BoundaryBelow(t *testing.T) {
	t.Skip("chunked path disabled (rangeFetchThreshold = MaxInt64)")
}

func TestDownloadToFile_Threshold_BoundaryAt(t *testing.T) {
	t.Skip("chunked path disabled (rangeFetchThreshold = MaxInt64)")
}

func TestDownloadToFile_AboveThreshold_ContentIntact(t *testing.T) {
	// Chunked path currently disabled (rangeFetchThreshold = MaxInt64).
	// If we ever re-enable it, this test should be revived to catch the
	// dup()-shared-OFD bug we hit on 2026-05-18 where parallel chunks
	// raced on a single file position and left holes in the dest file.
	t.Skip("chunked path disabled — see range_fetch.go threshold comment")
}

func TestDownloadToFile_LargeSizeMismatch(t *testing.T) {
	payload := randomBytes(t, 128*1024*1024)
	ras := newRangeAwareServer(t, payload)
	dst := filepath.Join(t.TempDir(), "mismatch.bin")
	// Lie about size — should fail.
	err := downloadToFile(context.Background(), http.DefaultClient, []string{ras.URL()}, int64(len(payload))+100, dst)
	if err == nil {
		t.Fatalf("expected size mismatch error, got nil")
	}
	// And the dest shouldn't exist after the failure.
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("partial download left on disk after failure")
	}
}

func TestDownloadToFile_Zero(t *testing.T) {
	// Zero-byte file — edge case.
	ras := newRangeAwareServer(t, []byte{})
	dst := filepath.Join(t.TempDir(), "empty.bin")
	if err := downloadToFile(context.Background(), http.DefaultClient, []string{ras.URL()}, 0, dst); err != nil {
		t.Fatalf("downloadToFile(empty) failed: %v", err)
	}
	st, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat empty: %v", err)
	}
	if st.Size() != 0 {
		t.Errorf("empty file should be 0 bytes, got %d", st.Size())
	}
}

func TestDownloadToFile_Large_MultiSourceChunks(t *testing.T) {
	// 128 MiB is below rangeFetchThreshold (1 GiB) so this goes
	// through the single-stream path. Tests that downloadToFile
	// works correctly when given multiple URLs even though only
	// urls[0] is used for small files.
	payload := randomBytes(t, 128*1024*1024)
	src1 := newRangeAwareServer(t, payload)
	src2 := newRangeAwareServer(t, payload)
	dst := filepath.Join(t.TempDir(), "multi.bin")

	urls := []string{src1.URL(), src2.URL()}
	if err := downloadToFile(context.Background(), http.DefaultClient, urls, int64(len(payload)), dst); err != nil {
		t.Fatalf("downloadToFile failed: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if !equalBytes(got, payload) {
		t.Errorf("payload mismatch (got %d bytes, want %d)", len(got), len(payload))
	}
	// Small-file path: single GET, urls[0] gets the hit.
	if src1.Hits() != 1 || src2.Hits() != 0 {
		t.Errorf("expected src1 hit only, got src1=%d src2=%d", src1.Hits(), src2.Hits())
	}
}

func TestDownloadToFile_Large_ThreeSources(t *testing.T) {
	// Same as above — small payload (< rangeFetchThreshold), goes
	// through single-stream path using urls[0].
	payload := randomBytes(t, 80*1024*1024)
	src1 := newRangeAwareServer(t, payload)
	src2 := newRangeAwareServer(t, payload)
	src3 := newRangeAwareServer(t, payload)
	dst := filepath.Join(t.TempDir(), "three.bin")

	urls := []string{src1.URL(), src2.URL(), src3.URL()}
	if err := downloadToFile(context.Background(), http.DefaultClient, urls, int64(len(payload)), dst); err != nil {
		t.Fatalf("downloadToFile failed: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if !equalBytes(got, payload) {
		t.Errorf("payload mismatch")
	}
	if src1.Hits() != 1 || src2.Hits() != 0 || src3.Hits() != 0 {
		t.Errorf("expected only src1 hit, got src1=%d src2=%d src3=%d",
			src1.Hits(), src2.Hits(), src3.Hits())
	}
}

// Helpers

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return b
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Force a clearer compile error if io / time imports are missing.
var _ = fmt.Sprintf
var _ = io.EOF
var _ = time.Time{}
