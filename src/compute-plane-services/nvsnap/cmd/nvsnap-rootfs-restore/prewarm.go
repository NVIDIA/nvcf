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

// prewarm.go — page-cache prewarm of the captured rootfs, done IN the
// restored workload (nvsnap-rootfs-restore is PID 1), right before pivot_root.
//
// Why here, not the agent: in the NVCA/L2 flow the agent never touches the
// per-pod restore — the webhook mounts the rox PVC at /nvsnap-captured and
// THIS binary builds the overlay and exec's the engine. So the only place
// that owns the lowerdir (the rox tree) and runs ahead of the engine's
// mmap weight-load is right here.
//
// Effect: a freshly snapshot-restored Hyperdisk-ML rox serves the engine's
// random mmap faults at ~350 MB/s (DeepSeek-V4 TP=8: 160 GB load ≈ 512 s).
// A single sequential read of the same tree hydrates it at HDML's
// sequential rate (~2.4 GB/s) into the page cache, so the engine's faults
// then hit RAM. Net startup drops even though we pay the sequential read,
// because it replaces a much slower random read.
//
// Why synchronous (blocking) before exec: syscall.Exec replaces this
// process image, so a background goroutine would not survive. Reading
// inline before pivot_root+exec guarantees the cache is warm — and fully
// AHEAD of the engine, so there's no rox read contention between prewarm
// and the engine (the failure mode of a concurrent sidecar).
//
// Same cgroup as the engine: the pages are charged to the inference
// container's memory cgroup, which is already sized to hold the model in
// host RAM during mmap load — so the cgroup-eviction problem that defeated
// an under-sized prewarmer does not apply here.
//
// Gated by NVSNAP_PREWARM (default on; set NVSNAP_PREWARM=0 to disable).

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	prewarmWorkers = 6
	prewarmBufSize = 4 << 20 // 4 MiB
)

// prewarmEnabled reports whether prewarm should run (default true).
func prewarmEnabled(getenv func(string) string) bool {
	return getenv("NVSNAP_PREWARM") != "0"
}

// prewarmCapturedRootfs sequentially reads every regular file under root
// (the rox lowerdir) into the page cache using a small worker pool.
// Best-effort: per-file read errors are tolerated; it logs a summary to
// stderr (the inference container's log). Blocks until done.
func prewarmCapturedRootfs(root string) {
	start := time.Now()
	files := make(chan string, prewarmWorkers*4)
	var mu sync.Mutex
	var bytesRead, fileCount int64

	var wg sync.WaitGroup
	for i := 0; i < prewarmWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, prewarmBufSize)
			for p := range files {
				n := readIntoCache(p, buf)
				mu.Lock()
				bytesRead += n
				fileCount++
				mu.Unlock()
			}
		}()
	}

	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries, keep walking
		}
		if d.Type().IsRegular() {
			files <- path
		}
		return nil
	})
	close(files)
	wg.Wait()

	elapsed := time.Since(start)
	mbps := 0.0
	if elapsed.Seconds() > 0 {
		mbps = float64(bytesRead) / 1e6 / elapsed.Seconds()
	}
	fmt.Fprintf(os.Stderr,
		"nvsnap-rootfs-restore: page-cache prewarm complete: %d files, %.2f GB, %.0fs, %.0f MB/s (walkErr=%v)\n",
		fileCount, float64(bytesRead)/1e9, elapsed.Seconds(), mbps, walkErr)
}

// readIntoCache streams one file through the page cache via io.Discard,
// returning bytes read. The fixed buffer keeps RSS flat; the pages land in
// the reclaimable file cache.
func readIntoCache(path string, buf []byte) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	n, _ := io.CopyBuffer(io.Discard, f, buf)
	return n
}
