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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Store is a disk-backed content-addressed blob store.
//
// Layout under DataDir:
//
//	blobs/<aa>/<bb...>          -- raw file bytes, name = sha256 hex
//	captures/<hash>/manifest.json
//
// The two-level fanout keeps blob dirs at ~256 entries each at
// ~64K blobs total — well under any sane filesystem's listing
// performance cliff.
//
// All writes go via tmp + fsync + rename so a crashed process
// never leaves a half-written blob under its final name. Readers
// only ever observe complete files.
type Store struct {
	DataDir string

	// captureLocks serializes mutating ops on the same capture
	// hash. Manifest writes already use rename-atomic semantics,
	// but a per-capture lock keeps DELETE from racing PUT in a
	// way that orphans blobs.
	captureLocks sync.Map
}

// New constructs a Store rooted at dataDir, ensuring the layout
// dirs exist. dataDir should be on a PVC for durability.
func New(dataDir string) (*Store, error) {
	if dataDir == "" {
		return nil, errors.New("blobstore: dataDir required")
	}
	for _, sub := range []string{"blobs", "captures"} {
		if err := os.MkdirAll(filepath.Join(dataDir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", sub, err)
		}
	}
	return &Store{DataDir: dataDir}, nil
}

// blobPath returns the on-disk path for a blob of given sha256
// hex. Caller is responsible for validating the hex string.
func (s *Store) blobPath(sha string) string {
	return filepath.Join(s.DataDir, "blobs", sha[:2], sha[2:])
}

// validSha256Hex enforces lowercase 64-char hex. We reject
// uppercase and short strings explicitly so callers can't
// accidentally hit a different blob via case-folding on
// case-insensitive filesystems.
func validSha256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// ErrHashMismatch is returned by PutBlob when the streamed body's
// sha256 does not match the declared hex.
var ErrHashMismatch = errors.New("blobstore: sha256 mismatch")

// PutBlob streams the body to disk, verifying sha256 matches the
// expected hex. Returns (alreadyExisted, error). If the on-disk
// hash mismatches the expected, the temp file is removed and
// ErrHashMismatch is returned.
//
// Idempotent: if the blob already exists, body is fully consumed
// (so the caller doesn't have to special-case partial reads) but
// no disk write happens.
func (s *Store) PutBlob(sha string, body io.Reader) (alreadyExisted bool, err error) {
	if !validSha256Hex(sha) {
		return false, fmt.Errorf("invalid sha256 hex %q", sha)
	}
	dst := s.blobPath(sha)

	if _, statErr := os.Stat(dst); statErr == nil {
		// Already have it — drain body so HTTP/1.1 keep-alive
		// can reuse the connection cleanly.
		_, _ = io.Copy(io.Discard, body)
		return true, nil
	}

	if mkErr := os.MkdirAll(filepath.Dir(dst), 0o755); mkErr != nil {
		return false, mkErr
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".part-*")
	if err != nil {
		return false, err
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), body); err != nil {
		cleanup()
		return false, fmt.Errorf("copy body: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != sha {
		cleanup()
		return false, fmt.Errorf("%w: declared %s, got %s", ErrHashMismatch, sha, got)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return false, fmt.Errorf("fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return false, err
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		_ = os.Remove(tmpPath)
		return false, fmt.Errorf("rename: %w", err)
	}
	return false, nil
}

// HasBlob reports whether the blob exists locally.
func (s *Store) HasBlob(sha string) bool {
	if !validSha256Hex(sha) {
		return false
	}
	_, err := os.Stat(s.blobPath(sha))
	return err == nil
}

// OpenBlob returns an *os.File for reading. Caller must Close.
// Returns os.ErrNotExist for unknown hashes; other errors map
// 1:1 to the underlying os.Open error.
func (s *Store) OpenBlob(sha string) (*os.File, error) {
	if !validSha256Hex(sha) {
		return nil, fmt.Errorf("invalid sha256 hex %q", sha)
	}
	return os.Open(s.blobPath(sha))
}

// DeleteBlob removes the blob file. Idempotent — missing is a
// no-op so the GC sweep doesn't have to coordinate with retention.
func (s *Store) DeleteBlob(sha string) error {
	if !validSha256Hex(sha) {
		return fmt.Errorf("invalid sha256 hex %q", sha)
	}
	if err := os.Remove(s.blobPath(sha)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Manifest is the on-disk representation of a capture's file
// list. The hash field at PUT time keys the on-disk dir; it's
// derived by the caller (typically content-hash of the dump dir's
// canonical layout) and treated opaquely by the store.
type Manifest struct {
	Files []ManifestFile `json:"files"`
}

// ManifestFile describes one file in a capture manifest.
type ManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// validCaptureHash mirrors validSha256Hex but is also tolerant of
// the longer composite IDs used by the agent (e.g.,
// "vllm-small__nvsnap-system__20260509-192743"). Path traversal is
// the only real concern; we reject anything containing path
// separators or a leading dot.
func validCaptureHash(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	if strings.ContainsAny(s, "/\\\x00") {
		return false
	}
	return true
}

// PutManifest writes manifest.json under captures/<hash>/. It
// validates the shape and per-file sha256 before persisting.
// Idempotent: rewriting with the same body is a clean replace.
func (s *Store) PutManifest(hash string, m *Manifest) error {
	if !validCaptureHash(hash) {
		return fmt.Errorf("invalid capture hash %q", hash)
	}
	if m == nil {
		return errors.New("nil manifest")
	}
	for i, f := range m.Files {
		if f.Path == "" || strings.HasPrefix(f.Path, "/") || strings.Contains(f.Path, "..") {
			return fmt.Errorf("file[%d]: invalid path %q (must be relative, no ..)", i, f.Path)
		}
		if !validSha256Hex(f.SHA256) {
			return fmt.Errorf("file[%d] %q: invalid sha256", i, f.Path)
		}
		if f.Size < 0 {
			return fmt.Errorf("file[%d] %q: negative size", i, f.Path)
		}
	}

	unlock := s.lockCapture(hash)
	defer unlock()

	dir := filepath.Join(s.DataDir, "captures", hash)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(dir, "manifest.json")
	tmp, err := os.CreateTemp(dir, ".manifest-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("encode manifest: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("fsync manifest: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, dst)
}

// GetManifest reads and decodes the capture manifest. Returns
// os.ErrNotExist if the capture isn't here.
func (s *Store) GetManifest(hash string) (*Manifest, error) {
	if !validCaptureHash(hash) {
		return nil, fmt.Errorf("invalid capture hash %q", hash)
	}
	dst := filepath.Join(s.DataDir, "captures", hash, "manifest.json")
	f, err := os.Open(dst)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var m Manifest
	if err := json.NewDecoder(f).Decode(&m); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	return &m, nil
}

// DeleteCapture removes the capture's manifest and dir. Does NOT
// touch referenced blobs — orphan GC is a separate sweep that
// scans manifests to compute the live set.
func (s *Store) DeleteCapture(hash string) error {
	if !validCaptureHash(hash) {
		return fmt.Errorf("invalid capture hash %q", hash)
	}
	unlock := s.lockCapture(hash)
	defer unlock()
	dir := filepath.Join(s.DataDir, "captures", hash)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return nil
}

// DiskStats reports disk usage of the underlying filesystem
// holding DataDir. The /healthz endpoint surfaces this so an
// operator can spot a near-full PVC before puts start failing.
type DiskStats struct {
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
	UsedBytes  uint64 `json:"used_bytes"`
}

// DiskStats reports total/free/used bytes of the filesystem holding DataDir.
func (s *Store) DiskStats() (*DiskStats, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(s.DataDir, &st); err != nil {
		return nil, err
	}
	total := st.Blocks * uint64(st.Bsize) //nolint:gosec // statfs block size is non-negative
	free := st.Bavail * uint64(st.Bsize)  //nolint:gosec // statfs block size is non-negative
	return &DiskStats{
		TotalBytes: total,
		FreeBytes:  free,
		UsedBytes:  total - free,
	}, nil
}

// Stats aggregates blobstore-wide counts for the operator UI.
// Walking the two-level blob fanout is O(blobs); at the design
// ceiling of ~64K blobs this is fast enough to compute on demand
// without caching.
type Stats struct {
	BlobCount     int        `json:"blob_count"`
	BlobBytes     int64      `json:"blob_bytes"`
	CaptureCount  int        `json:"capture_count"`
	ManifestCount int        `json:"manifest_count"`
	Disk          *DiskStats `json:"disk"`
}

// Stats aggregates blobstore-wide counts and disk usage.
func (s *Store) Stats() (*Stats, error) {
	out := &Stats{}
	blobsDir := filepath.Join(s.DataDir, "blobs")
	err := filepath.Walk(blobsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == blobsDir {
				return nil
			}
			return err
		}
		if info.IsDir() || strings.HasPrefix(info.Name(), ".part-") {
			return nil
		}
		out.BlobCount++
		out.BlobBytes += info.Size()
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk blobs: %w", err)
	}

	capturesDir := filepath.Join(s.DataDir, "captures")
	entries, err := os.ReadDir(capturesDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read captures: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		out.CaptureCount++
		if _, err := os.Stat(filepath.Join(capturesDir, e.Name(), "manifest.json")); err == nil {
			out.ManifestCount++
		}
	}

	if ds, err := s.DiskStats(); err == nil {
		out.Disk = ds
	}
	return out, nil
}

// CaptureSummary is one row in the captures listing — enough for
// a UI table without forcing a full manifest read per capture.
type CaptureSummary struct {
	Hash        string    `json:"hash"`
	FileCount   int       `json:"file_count"`
	TotalBytes  int64     `json:"total_bytes"`
	HasManifest bool      `json:"has_manifest"`
	ModifiedAt  time.Time `json:"modified_at"`
}

// ListCaptures returns all capture summaries on disk. Reads the
// manifest for each to compute FileCount / TotalBytes. At demo
// scale (tens of captures) this is fine; if the catalog grows
// large, swap to a cached index.
func (s *Store) ListCaptures() ([]CaptureSummary, error) {
	dir := filepath.Join(s.DataDir, "captures")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []CaptureSummary{}, nil
		}
		return nil, err
	}
	out := make([]CaptureSummary, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		row := CaptureSummary{Hash: e.Name()}
		manifestPath := filepath.Join(dir, e.Name(), "manifest.json")
		if fi, statErr := os.Stat(manifestPath); statErr == nil {
			row.HasManifest = true
			row.ModifiedAt = fi.ModTime()
			if m, mErr := s.GetManifest(e.Name()); mErr == nil {
				row.FileCount = len(m.Files)
				for _, f := range m.Files {
					row.TotalBytes += f.Size
				}
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// lockCapture returns a per-hash mutex's unlock fn. Concurrent
// writers to different captures don't contend.
func (s *Store) lockCapture(hash string) func() {
	mu, _ := s.captureLocks.LoadOrStore(hash, &sync.Mutex{})
	m := mu.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}
