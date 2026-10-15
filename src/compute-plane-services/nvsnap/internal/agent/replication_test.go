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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/objectstore"
)

// fakeBucket is an in-memory objectstore.Bucket for tests.
type fakeBucket struct {
	name string
	mu   sync.Mutex
	objs map[string][]byte
}

func newFakeBucket(name string) *fakeBucket {
	return &fakeBucket{name: name, objs: map[string][]byte{}}
}

func (b *fakeBucket) Name() string { return b.name }

func (b *fakeBucket) Head(_ context.Context, key string) (objectstore.ObjectInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.objs[key]
	if !ok {
		return objectstore.ObjectInfo{}, objectstore.ErrNotFound
	}
	return objectstore.ObjectInfo{Key: key, Size: int64(len(v))}, nil
}

func (b *fakeBucket) Get(_ context.Context, key string) (io.ReadCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.objs[key]
	if !ok {
		return nil, objectstore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(v)), nil
}

func (b *fakeBucket) Put(_ context.Context, key string, r io.Reader, _ int64) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.objs[key] = data
	return nil
}

func (b *fakeBucket) List(_ context.Context, prefix string) ([]objectstore.ObjectInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []objectstore.ObjectInfo
	for k, v := range b.objs {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, objectstore.ObjectInfo{Key: k, Size: int64(len(v))})
		}
	}
	return out, nil
}

func testAgent(t *testing.T, cacheDir string) *Agent {
	t.Helper()
	a := &Agent{log: logrus.New()}
	a.config.RootfsCapture.CacheDir = cacheDir
	return a
}

func TestProbeBucketsForHash(t *testing.T) {
	hash := "abc123"
	home := newFakeBucket("home")
	peer := newFakeBucket("peer")
	a := testAgent(t, t.TempDir())
	a.objectStoreHome = home
	a.objectStorePeers = []objectstore.Bucket{peer}

	// Miss everywhere.
	if _, err := a.probeBucketsForHash(context.Background(), hash); !errors.Is(err, checkpointstore.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	// Hit on peer only → peer wins.
	_ = peer.Put(context.Background(), hash+"/manifest.json", bytes.NewReader([]byte("{}")), 2)
	got, err := a.probeBucketsForHash(context.Background(), hash)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if got.Name() != "peer" {
		t.Fatalf("want peer, got %s", got.Name())
	}

	// Hit on home too → home wins (probed first).
	_ = home.Put(context.Background(), hash+"/manifest.json", bytes.NewReader([]byte("{}")), 2)
	got, _ = a.probeBucketsForHash(context.Background(), hash)
	if got.Name() != "home" {
		t.Fatalf("want home, got %s", got.Name())
	}
}

func TestUploadCaptureToObjectStore_KeysAndIdempotency(t *testing.T) {
	cacheDir := t.TempDir()
	hash := "deadbeef"
	// Local capture layout: <cache>/<hash>/{manifest.json,tree/rootfs/f}
	capDir := filepath.Join(cacheDir, hash)
	mustWrite(t, filepath.Join(capDir, "manifest.json"), `{"hash":"deadbeef"}`)
	mustWrite(t, filepath.Join(capDir, "tree", "rootfs", "f"), "hello")

	home := newFakeBucket("home")
	a := testAgent(t, cacheDir)
	a.objectStoreHome = home

	if err := a.UploadCaptureToObjectStore(context.Background(), hash); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if _, ok := home.objs[hash+"/manifest.json"]; !ok {
		t.Errorf("manifest not uploaded under %s/manifest.json", hash)
	}
	if got := string(home.objs[hash+"/tree/rootfs/f"]); got != "hello" {
		t.Errorf("tree file content = %q, want hello", got)
	}
	// Idempotent re-push: same size → HEAD-skip, no error.
	if err := a.UploadCaptureToObjectStore(context.Background(), hash); err != nil {
		t.Fatalf("re-upload: %v", err)
	}
}

func TestUploadCaptureToObjectStore_DisabledNoop(t *testing.T) {
	a := testAgent(t, t.TempDir())
	a.objectStoreHome = nil // replication disabled
	if err := a.UploadCaptureToObjectStore(context.Background(), "x"); err != nil {
		t.Fatalf("disabled replication should no-op, got %v", err)
	}
}

// TestReplicateFromObjectStore_RoundTrip pushes a capture into a fake home bucket,
// then replicates it through a real Local backend and asserts the committed
// tree + manifest land on disk — the cross-cluster pull→replay path.
func TestReplicateFromObjectStore_RoundTrip(t *testing.T) {
	cacheDir := t.TempDir()
	hash := "f00dcafe"

	// Seed a home bucket with manifest.json + a tree file, keyed <hash>/...
	home := newFakeBucket("home")
	m := checkpointstore.Manifest{
		Hash:                 hash,
		CaptureMethod:        "rootfs",
		CaptureFormatVersion: checkpointstore.CaptureFormatVersion,
		CapturedOnNodes:      []string{"source-cluster-node"},
		TotalSizeBytes:       5,
		FileCount:            1,
	}
	mj, _ := json.Marshal(m)
	_ = home.Put(context.Background(), hash+"/manifest.json", bytes.NewReader(mj), int64(len(mj)))
	_ = home.Put(context.Background(), hash+"/tree/rootfs/weights.bin", bytes.NewReader([]byte("hello")), 5)

	// Real Local backend rooted at the cache dir (matches production).
	local, err := checkpointstore.NewLocal(cacheDir)
	if err != nil {
		t.Fatalf("new local: %v", err)
	}
	a := testAgent(t, cacheDir)
	a.objectStoreHome = home
	a.captureBackend = local

	if repErr := a.ReplicateFromObjectStore(context.Background(), hash); repErr != nil {
		t.Fatalf("replicate: %v", repErr)
	}

	// The Local backend should now Stat the hash and the tree file exists.
	if _, statErr := local.Stat(context.Background(), hash); statErr != nil {
		t.Fatalf("expected committed capture, Stat: %v", statErr)
	}
	got, err := os.ReadFile(filepath.Join(cacheDir, hash, "tree", "rootfs", "weights.bin"))
	if err != nil {
		t.Fatalf("read committed file: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("committed content = %q, want hello", got)
	}
	// Staging must be cleaned up.
	if _, err := os.Stat(filepath.Join(cacheDir, ".pull", hash)); !os.IsNotExist(err) {
		t.Errorf("staging dir not cleaned up")
	}

	// Idempotent: second replicate hits the fast path (already committed).
	if err := a.ReplicateFromObjectStore(context.Background(), hash); err != nil {
		t.Fatalf("second replicate should no-op, got %v", err)
	}
}

// stubL2 implements the StatInNamespace interface the replicate path
// type-asserts a.l2Backend to. roxNS records the namespace it was
// queried with; present controls whether the rox PVC "exists".
type stubL2 struct {
	checkpointstore.Backend
	present  bool
	gotNS    string
	statErr  error
	statHash string
}

func (s *stubL2) StatInNamespace(_ context.Context, hash, ns string) (checkpointstore.Manifest, error) {
	s.gotNS = ns
	s.statHash = hash
	if s.statErr != nil {
		return checkpointstore.Manifest{}, s.statErr
	}
	if s.present {
		return checkpointstore.Manifest{Hash: hash}, nil
	}
	return checkpointstore.Manifest{}, checkpointstore.ErrNotFound
}

// Bug 3: idempotency must gate on the L2 rox PVC (what cross-cluster
// restore mounts), scoped to the source pod's namespace — NOT the
// chain Stat (which succeeds when only L1 has the hash). When the L2
// rox is present the replicate short-circuits without re-promoting;
// when it is missing the replicate proceeds (self-heals a prior L2
// failure that left only L1 committed).
func TestReplicateFromObjectStore_L2IdempotencyCheck(t *testing.T) {
	seed := func(t *testing.T) (*Agent, *fakeBucket, string, *checkpointstore.Local) { //nolint:unparam // fakeBucket returned for test symmetry across seed variants
		t.Helper()
		cacheDir := t.TempDir()
		hash := "abcdef0123456789"
		home := newFakeBucket("home")
		m := checkpointstore.Manifest{
			Hash:                 hash,
			CaptureMethod:        "rootfs",
			CaptureFormatVersion: checkpointstore.CaptureFormatVersion,
			TotalSizeBytes:       5,
			FileCount:            1,
			SourcePodMeta:        map[string]string{"namespace": "nvcf-backend"},
		}
		mj, _ := json.Marshal(m)
		_ = home.Put(context.Background(), hash+"/manifest.json", bytes.NewReader(mj), int64(len(mj)))
		_ = home.Put(context.Background(), hash+"/tree/rootfs/w.bin", bytes.NewReader([]byte("hello")), 5)
		local, err := checkpointstore.NewLocal(cacheDir)
		if err != nil {
			t.Fatalf("new local: %v", err)
		}
		a := testAgent(t, cacheDir)
		a.objectStoreHome = home
		a.captureBackend = local
		return a, home, hash, local
	}

	t.Run("rox present → skip, no L1 commit", func(t *testing.T) {
		a, _, hash, local := seed(t)
		l2 := &stubL2{present: true}
		a.l2Backend = l2
		if err := a.ReplicateFromObjectStore(context.Background(), hash); err != nil {
			t.Fatalf("replicate: %v", err)
		}
		if l2.gotNS != "nvcf-backend" {
			t.Errorf("StatInNamespace queried ns %q, want nvcf-backend (source pod ns from manifest)", l2.gotNS)
		}
		// Skipped before download/Put — nothing committed to L1.
		if _, err := local.Stat(context.Background(), hash); err == nil {
			t.Error("expected no L1 commit when L2 rox already present")
		}
	})

	t.Run("rox missing → proceeds and commits", func(t *testing.T) {
		a, _, hash, local := seed(t)
		l2 := &stubL2{present: false}
		a.l2Backend = l2
		if err := a.ReplicateFromObjectStore(context.Background(), hash); err != nil {
			t.Fatalf("replicate: %v", err)
		}
		if l2.gotNS != "nvcf-backend" {
			t.Errorf("StatInNamespace queried ns %q, want nvcf-backend", l2.gotNS)
		}
		// Proceeded to commit (self-heal of a prior L2-only failure).
		if _, err := local.Stat(context.Background(), hash); err != nil {
			t.Errorf("expected L1 commit when L2 rox missing, Stat: %v", err)
		}
	})
}

func TestReplicateFromObjectStore_NotFound(t *testing.T) {
	cacheDir := t.TempDir()
	local, _ := checkpointstore.NewLocal(cacheDir)
	a := testAgent(t, cacheDir)
	a.objectStoreHome = newFakeBucket("home") // empty
	a.captureBackend = local
	if err := a.ReplicateFromObjectStore(context.Background(), "missing"); !errors.Is(err, checkpointstore.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestReplicateFromObjectStore_DisabledErrors(t *testing.T) {
	a := testAgent(t, t.TempDir())
	a.objectStoreHome = nil
	if err := a.ReplicateFromObjectStore(context.Background(), "x"); err == nil {
		t.Fatal("expected error when replication disabled")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
