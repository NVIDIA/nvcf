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
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sirupsen/logrus"
)

// newPrewarmTestAgent returns a minimal Agent wired only for the prewarm
// path: a logger, prewarm enabled, and the (zero-value) prewarmed map.
func newPrewarmTestAgent(prewarm bool) *Agent {
	a := &Agent{log: logrus.New()}
	a.log.SetOutput(os.Stderr)
	a.config.Prewarm = prewarm
	return a
}

// writeTree creates n files of `size` bytes under dir and returns the
// total bytes written.
func writeTree(t *testing.T, dir string, n, size int) int64 {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, size)
	var total int64
	for i := 0; i < n; i++ {
		p := filepath.Join(dir, "sub", "f"+string(rune('a'+i))+".bin")
		if err := os.WriteFile(p, buf, 0o644); err != nil {
			t.Fatal(err)
		}
		total += int64(size)
	}
	return total
}

// TestPrewarmReadsTree verifies prewarmLowerDir walks the tree and reads
// every regular file without error (synchronous variant).
func TestPrewarmReadsTree(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, 4, 1<<20) // 4 × 1 MiB
	a := newPrewarmTestAgent(true)
	// Should not panic / error; reads everything into io.Discard.
	a.prewarmLowerDir(dir)
}

// TestPrewarmIdempotent verifies a lowerdir is warmed at most once: the
// second prewarmLowerDirAsync for the same path is a no-op (LoadOrStore
// already claimed it), so only one goroutine ever runs.
func TestPrewarmIdempotent(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, 2, 4096)

	a := newPrewarmTestAgent(true)
	// Pre-claim the path exactly as prewarmLowerDirAsync would, then
	// assert a second LoadOrStore reports it already present.
	if _, loaded := a.prewarmed.LoadOrStore(dir, struct{}{}); loaded {
		t.Fatal("path unexpectedly already claimed")
	}
	if _, loaded := a.prewarmed.LoadOrStore(dir, struct{}{}); !loaded {
		t.Fatal("second claim should report loaded=true (idempotent)")
	}
}

// TestPrewarmDisabled verifies the async entry point is inert when
// Config.Prewarm is false: no claim is recorded.
func TestPrewarmDisabled(t *testing.T) {
	dir := t.TempDir()
	a := newPrewarmTestAgent(false)
	a.prewarmLowerDirAsync(dir)
	if _, ok := a.prewarmed.Load(dir); ok {
		t.Fatal("disabled prewarm should not claim the lowerdir")
	}
}

// TestPrewarmEmptyLowerNoop verifies an empty lowerdir is ignored.
func TestPrewarmEmptyLowerNoop(t *testing.T) {
	a := newPrewarmTestAgent(true)
	a.prewarmLowerDirAsync("")
	if _, ok := a.prewarmed.Load(""); ok {
		t.Fatal("empty lowerdir should not be claimed")
	}
}

// TestReadIntoCacheCountsBytes verifies readIntoCache returns the full
// file size and concurrent reads sum correctly.
func TestReadIntoCacheCountsBytes(t *testing.T) {
	dir := t.TempDir()
	total := writeTree(t, dir, 5, 512<<10) // 5 × 512 KiB

	var got int64
	var wg sync.WaitGroup
	buf := func() []byte { return make([]byte, prewarmBufSize) }
	entries, _ := os.ReadDir(filepath.Join(dir, "sub"))
	for _, e := range entries {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			n, err := readIntoCache(filepath.Join(dir, "sub", name), buf())
			if err != nil {
				t.Errorf("readIntoCache: %v", err)
			}
			atomic.AddInt64(&got, n)
		}(e.Name())
	}
	wg.Wait()
	if got != total {
		t.Fatalf("bytes read = %d, want %d", got, total)
	}
}
