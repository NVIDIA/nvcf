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

package checkpointstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// makeTree builds a small tree at root with predictable contents for round-trip checks.
func makeTree(t *testing.T, root string) {
	t.Helper()
	mustMkdir(t, filepath.Join(root, "a"))
	mustMkdir(t, filepath.Join(root, "a", "b"))
	mustWrite(t, filepath.Join(root, "a", "b", "x.txt"), "hello\n")
	mustWrite(t, filepath.Join(root, "a", "y.bin"), "\x00\x01\x02\x03")
	mustWrite(t, filepath.Join(root, "top"), "root file\n")
	if err := os.Symlink("y.bin", filepath.Join(root, "a", "yslink")); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLocal_PutGetRoundTrip(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	dst := t.TempDir()
	makeTree(t, src)

	store, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}

	hash := "abc123"
	in := Manifest{
		SourcePodMeta: map[string]string{"namespace": "nvsnap-system", "name": "vllm-8b"},
	}
	out, err := store.Put(context.Background(), hash, []CaptureSource{{SrcPath: src}}, in)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if out.Hash != hash {
		t.Fatalf("Put manifest.Hash = %q, want %q", out.Hash, hash)
	}
	if out.FileCount == 0 || out.TotalSizeBytes == 0 {
		t.Fatalf("Put manifest counters not populated: %+v", out)
	}
	if out.CapturedAt.IsZero() {
		t.Fatalf("Put left CapturedAt zero")
	}

	got, err := store.Get(context.Background(), hash, dst)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Hash != hash {
		t.Fatalf("Get manifest.Hash = %q, want %q", got.Hash, hash)
	}

	// Files round-tripped correctly?
	if v := mustRead(t, filepath.Join(dst, "a", "b", "x.txt")); v != "hello\n" {
		t.Fatalf("a/b/x.txt: got %q", v)
	}
	if v := mustRead(t, filepath.Join(dst, "top")); v != "root file\n" {
		t.Fatalf("top: got %q", v)
	}
	// Symlink preserved as a symlink?
	target, err := os.Readlink(filepath.Join(dst, "a", "yslink"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != "y.bin" {
		t.Fatalf("symlink target: got %q, want %q", target, "y.bin")
	}
}

func TestLocal_PutSecondTimeReturnsErrExists(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	makeTree(t, src)
	store, _ := NewLocal(root)

	if _, err := store.Put(context.Background(), "h", []CaptureSource{{SrcPath: src}}, Manifest{}); err != nil {
		t.Fatal(err)
	}
	_, err := store.Put(context.Background(), "h", []CaptureSource{{SrcPath: src}}, Manifest{})
	if !errors.Is(err, ErrExists) {
		t.Fatalf("second Put: got %v, want ErrExists", err)
	}
}

func TestLocal_StatNotFound(t *testing.T) {
	root := t.TempDir()
	store, _ := NewLocal(root)
	_, err := store.Stat(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stat missing: got %v, want ErrNotFound", err)
	}
}

func TestLocal_GetNotFound(t *testing.T) {
	root := t.TempDir()
	store, _ := NewLocal(root)
	_, err := store.Get(context.Background(), "missing", t.TempDir())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing: got %v, want ErrNotFound", err)
	}
}

func TestLocal_DeleteRoundTrip(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	makeTree(t, src)
	store, _ := NewLocal(root)

	if _, err := store.Put(context.Background(), "h", []CaptureSource{{SrcPath: src}}, Manifest{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), "h"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Delete(context.Background(), "h"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete: got %v, want ErrNotFound", err)
	}
}

func TestLocal_PathFor(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	makeTree(t, src)
	store, _ := NewLocal(root)

	if _, err := store.PathFor("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("PathFor missing: got %v, want ErrNotFound", err)
	}
	if _, err := store.Put(context.Background(), "h", []CaptureSource{{SrcPath: src}}, Manifest{}); err != nil {
		t.Fatal(err)
	}
	p, err := store.PathFor("h")
	if err != nil {
		t.Fatal(err)
	}
	if v := mustRead(t, filepath.Join(p, "top")); v != "root file\n" {
		t.Fatalf("PathFor returned tree: got top=%q", v)
	}
}

// TestLocal_ConcurrentPutDedup verifies two concurrent Puts of the same hash
// produce exactly one capture; the loser sees ErrExists.
func TestLocal_ConcurrentPutDedup(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	makeTree(t, src)
	store, _ := NewLocal(root)

	const n = 8
	var success, exists atomic.Int32
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.Put(context.Background(), "h", []CaptureSource{{SrcPath: src}}, Manifest{})
			switch {
			case err == nil:
				success.Add(1)
			case errors.Is(err, ErrExists):
				exists.Add(1)
			default:
				t.Errorf("unexpected Put error: %v", err)
			}
		}()
	}
	wg.Wait()

	if success.Load() != 1 {
		t.Fatalf("got %d successful Puts, want 1", success.Load())
	}
	if exists.Load() != n-1 {
		t.Fatalf("got %d ErrExists, want %d", exists.Load(), n-1)
	}

	// Verify only one capture directory plus zero leftover tmp dirs.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var captures, tmps int
	for _, e := range entries {
		if e.IsDir() {
			if e.Name() == "h" {
				captures++
			} else {
				tmps++
			}
		}
	}
	if captures != 1 || tmps != 0 {
		t.Fatalf("after concurrent Put: captures=%d tmps=%d entries=%v", captures, tmps, entries)
	}
}
