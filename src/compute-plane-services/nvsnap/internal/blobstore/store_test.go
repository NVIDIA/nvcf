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

package blobstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// hashOf returns sha256 hex of b — handy for table-driven tests.
func hashOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// TestPutBlob_ContentAddressedRoundTrip — happy path. Caller
// streams known bytes with the matching sha; OpenBlob returns
// the same bytes. This is the only test the agent uploader
// (#58) cares about: bytes in, bytes out.
func TestPutBlob_ContentAddressedRoundTrip(t *testing.T) {
	s := newStore(t)
	body := []byte("hello nvsnap blob world")
	sha := hashOf(body)

	existed, err := s.PutBlob(sha, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	if existed {
		t.Errorf("first put: existed = true, want false")
	}

	f, err := s.OpenBlob(sha)
	if err != nil {
		t.Fatalf("OpenBlob: %v", err)
	}
	defer func() { _ = f.Close() }()
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("body mismatch: got %q, want %q", got, body)
	}
}

// TestPutBlob_Idempotent — second PUT of the same sha+body is
// a no-op (existed=true). The agent uploader does an idempotent
// retry on transient errors; we MUST NOT corrupt the blob on
// the second attempt.
func TestPutBlob_Idempotent(t *testing.T) {
	s := newStore(t)
	body := []byte("idempotent")
	sha := hashOf(body)

	if _, err := s.PutBlob(sha, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	existed, err := s.PutBlob(sha, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("second put: %v", err)
	}
	if !existed {
		t.Errorf("second put: existed = false, want true")
	}
}

// TestPutBlob_HashMismatchRejected — declared sha doesn't match
// the bytes. Store MUST reject and leave nothing on disk so a
// later legit put for the real sha can land cleanly.
func TestPutBlob_HashMismatchRejected(t *testing.T) {
	s := newStore(t)
	body := []byte("real bytes")
	wrongSha := strings.Repeat("a", 64) // valid hex but wrong content

	_, err := s.PutBlob(wrongSha, bytes.NewReader(body))
	if !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("err = %v, want ErrHashMismatch", err)
	}
	if s.HasBlob(wrongSha) {
		t.Error("blob present after mismatch — temp file leaked")
	}
	// Critically: no leftover .part-* in blobs/<aa>/
	dir := filepath.Join(s.DataDir, "blobs", wrongSha[:2])
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".part-") {
			t.Errorf("leftover temp: %s", e.Name())
		}
	}
}

// TestPutBlob_InvalidShaRejected — non-hex / wrong-length / dot
// strings must be rejected at the validSha256Hex gate, not
// reach the filesystem.
func TestPutBlob_InvalidShaRejected(t *testing.T) {
	s := newStore(t)
	cases := []string{
		"",
		"deadbeef",               // too short
		strings.Repeat("a", 65),  // too long
		strings.Repeat("g", 64),  // non-hex char
		strings.Repeat("A", 64),  // uppercase rejected (case-fold defense)
		"../../../../etc/passwd", // path traversal attempt
		strings.Repeat("a", 32) + "/" + strings.Repeat("b", 31),
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			_, err := s.PutBlob(c, bytes.NewReader([]byte("x")))
			if err == nil {
				t.Errorf("PutBlob(%q): err = nil, want error", c)
			}
		})
	}
}

// TestHasBlob_AndDelete — exists/delete/idempotent-delete.
func TestHasBlob_AndDelete(t *testing.T) {
	s := newStore(t)
	body := []byte("deletable")
	sha := hashOf(body)

	if s.HasBlob(sha) {
		t.Fatal("blob exists before put")
	}
	if _, err := s.PutBlob(sha, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	if !s.HasBlob(sha) {
		t.Fatal("blob missing after put")
	}
	if err := s.DeleteBlob(sha); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if s.HasBlob(sha) {
		t.Error("blob still there after delete")
	}
	// Idempotent: missing delete returns nil.
	if err := s.DeleteBlob(sha); err != nil {
		t.Errorf("idempotent delete: %v", err)
	}
}

// TestPutManifest_RoundTripAndIdempotent — manifest write +
// read + rewrite. Agent re-uploads the manifest on retry; we
// must end up with the *latest* version, not append.
func TestPutManifest_RoundTripAndIdempotent(t *testing.T) {
	s := newStore(t)
	hash := "vllm-small__nvsnap-system__20260509-192743"
	m1 := &Manifest{Files: []ManifestFile{
		{Path: "inventory.img", SHA256: strings.Repeat("a", 64), Size: 99},
	}}
	if err := s.PutManifest(hash, m1); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetManifest(hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 1 || got.Files[0].Path != "inventory.img" {
		t.Errorf("got = %+v", got)
	}
	// Rewrite with a different file list — round-trip must reflect
	// the new content, not merge.
	m2 := &Manifest{Files: []ManifestFile{
		{Path: "pages-1.img", SHA256: strings.Repeat("b", 64), Size: 4096},
		{Path: "pstree.img", SHA256: strings.Repeat("c", 64), Size: 12},
	}}
	if putErr := s.PutManifest(hash, m2); putErr != nil {
		t.Fatal(putErr)
	}
	got, err = s.GetManifest(hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 2 {
		t.Errorf("after rewrite, len = %d, want 2", len(got.Files))
	}
	if got.Files[0].Path != "pages-1.img" {
		t.Errorf("got[0].Path = %q", got.Files[0].Path)
	}
}

// TestPutManifest_RejectsBadShape — the agent has a small
// schema bug (e.g., absolute path, ".." in path, malformed
// sha). Store rejects up front; nothing lands on disk.
func TestPutManifest_RejectsBadShape(t *testing.T) {
	s := newStore(t)
	hash := "test-bad"
	cases := []struct {
		name string
		m    *Manifest
	}{
		{"absolute-path", &Manifest{Files: []ManifestFile{
			{Path: "/etc/passwd", SHA256: strings.Repeat("a", 64), Size: 1},
		}}},
		{"dotdot-path", &Manifest{Files: []ManifestFile{
			{Path: "../../../escape", SHA256: strings.Repeat("a", 64), Size: 1},
		}}},
		{"bad-sha", &Manifest{Files: []ManifestFile{
			{Path: "x", SHA256: "deadbeef", Size: 1},
		}}},
		{"negative-size", &Manifest{Files: []ManifestFile{
			{Path: "x", SHA256: strings.Repeat("a", 64), Size: -1},
		}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.PutManifest(hash, tc.m); err == nil {
				t.Errorf("PutManifest: err = nil, want rejection")
			}
		})
	}
	// Bad shape never created the dir.
	if _, err := os.Stat(filepath.Join(s.DataDir, "captures", hash)); err == nil {
		t.Errorf("dir created despite all PUTs being rejected")
	}
}

// TestGetManifest_NotExist — receiver hits us for a capture we
// don't have. Must return os.ErrNotExist (not a generic error)
// so the cascade-fallback can distinguish "we don't have it"
// from "disk problem".
func TestGetManifest_NotExist(t *testing.T) {
	s := newStore(t)
	_, err := s.GetManifest("ghost-capture")
	if !os.IsNotExist(err) {
		t.Errorf("err = %v, want os.IsNotExist", err)
	}
}

// TestDeleteCapture_DoesNotTouchBlobs — capture metadata goes
// away but blobs stay (orphan GC is a separate sweep). This is
// load-bearing: deleting a capture before all peer references
// drain shouldn't bork another capture sharing the same blobs.
func TestDeleteCapture_DoesNotTouchBlobs(t *testing.T) {
	s := newStore(t)
	body := []byte("shared blob")
	sha := hashOf(body)
	if _, err := s.PutBlob(sha, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	hash := "to-be-deleted"
	if err := s.PutManifest(hash, &Manifest{Files: []ManifestFile{
		{Path: "shared", SHA256: sha, Size: int64(len(body))},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCapture(hash); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetManifest(hash); !os.IsNotExist(err) {
		t.Errorf("manifest still readable: %v", err)
	}
	if !s.HasBlob(sha) {
		t.Error("DeleteCapture removed shared blob — must leave for GC sweep")
	}
}

// TestDiskStats_HasNonZeroTotal — health-check sanity. We don't
// pin specific values (depends on test-runner FS) but free + used
// should equal total, and total > 0.
func TestDiskStats_HasNonZeroTotal(t *testing.T) {
	s := newStore(t)
	stats, err := s.DiskStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalBytes == 0 {
		t.Error("TotalBytes = 0")
	}
	if stats.UsedBytes+stats.FreeBytes > stats.TotalBytes {
		t.Errorf("used+free > total: used=%d free=%d total=%d",
			stats.UsedBytes, stats.FreeBytes, stats.TotalBytes)
	}
}

// TestPutBlob_ConcurrentSameSha — two writers race for the same
// sha. Both see success (one creates, one finds existing). The
// final blob is intact and equals the source body.
func TestPutBlob_ConcurrentSameSha(t *testing.T) {
	s := newStore(t)
	body := bytes.Repeat([]byte("racey"), 1024)
	sha := hashOf(body)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.PutBlob(sha, bytes.NewReader(body)); err != nil {
				t.Errorf("concurrent put: %v", err)
			}
		}()
	}
	wg.Wait()
	f, err := s.OpenBlob(sha)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	got, _ := io.ReadAll(f)
	if !bytes.Equal(got, body) {
		t.Errorf("after race, body mismatch (corruption)")
	}
}
