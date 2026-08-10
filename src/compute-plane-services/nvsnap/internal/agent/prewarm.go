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

// prewarm.go — agent-side page-cache prewarm for L2 rox restores.
//
// Why: on restore the engine mmaps the captured model weights off the
// per-capture rox (Hyperdisk-ML) volume. First-touch random mmap faults
// on a freshly snapshot-restored HDML volume hydrate block-by-block at
// ~350 MB/s (measured: DeepSeek-V4 TP=8, 160 GB load = 512 s). That is
// the dominant restore cost and it is NOT the disk's capability — HDML
// does ~2,400 MB/s on a sequential read.
//
// Fix: when the agent resolves the rox-backed lowerdir for an overlay
// (PrepareOverlay), it sequentially reads that tree into the Linux page
// cache ahead of the engine's load. Subsequent mmap faults then hit RAM
// instead of the disk. Because the rox is mounted ONCE per node and every
// per-pod overlay uses a lowerdir INTO that single mount, one prewarm
// warms the shared cache for the whole fan-out on that node.
//
// Crucial correctness condition (the reason a per-pod init container at a
// 128Mi memory limit failed before): page-cache pages are charged to the
// faulting cgroup and reclaimed when it crosses memory.max. The agent
// DaemonSet's memory limit therefore MUST be >= the working set (bumped
// to 256Gi in the DaemonSet) or the kernel evicts the warmed pages as
// fast as we read them. The agent's own RSS stays flat — we stream into
// io.Discard with a fixed buffer; only the reclaimable file cache grows.
//
// Idempotent + async: keyed by lowerdir, warmed at most once per agent
// lifetime, and launched in the background so it never blocks pod
// admission (the engine reads weights minutes later, after init + GPU
// bring-up, so the warm completes well ahead of need).

package agent

import (
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// prewarmWorkers is the number of files read concurrently. A handful of
// parallel sequential streams saturates HDML's sequential ceiling without
// degenerating into the random pattern we are trying to avoid.
const prewarmWorkers = 6

// prewarmBufSize is the per-worker copy buffer. Large blocks keep the
// read sequential and the syscall count low.
const prewarmBufSize = 4 << 20 // 4 MiB

// prewarmLowerDirAsync warms the page cache for the rox-backed lowerdir
// `lower`, at most once per agent lifetime. No-op when prewarm is
// disabled (Config.Prewarm=false) or `lower` is empty. Returns
// immediately; the read runs in a background goroutine.
func (a *Agent) prewarmLowerDirAsync(lower string) {
	// v0.0.88 instrumentation: log EVERY entry + the exact bail reason so
	// a single restore reveals whether this is reached and why it no-ops.
	log := a.log.WithFields(logrus.Fields{"subsys": "prewarm", "lower": lower, "prewarm_enabled": a.config.Prewarm})
	if !a.config.Prewarm {
		log.Info("prewarm: skipped (disabled)")
		return
	}
	if lower == "" {
		log.Info("prewarm: skipped (empty lowerdir)")
		return
	}
	// Idempotency: one warm per lowerdir. LoadOrStore returns loaded=true
	// if another PrepareOverlay (e.g. a sibling fan-out pod) already
	// claimed this path.
	if _, loaded := a.prewarmed.LoadOrStore(lower, struct{}{}); loaded {
		log.Info("prewarm: skipped (already warmed this lowerdir)")
		return
	}
	log.Info("prewarm: launching sequential read of lowerdir into page cache")
	go a.prewarmLowerDir(lower)
}

// prewarmLowerDir sequentially reads every regular file under `lower`
// into the page cache. Errors are logged and never propagated — prewarm
// is a best-effort accelerator; a failed read just means the engine pays
// the cold fault it would have paid anyway.
func (a *Agent) prewarmLowerDir(lower string) {
	log := a.log.WithFields(logrus.Fields{"subsys": "prewarm", "lower": lower})
	start := time.Now()

	files := make(chan string, prewarmWorkers*4)
	var bytesRead, fileCount int64
	var mu sync.Mutex

	var wg sync.WaitGroup
	for i := 0; i < prewarmWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, prewarmBufSize)
			for path := range files {
				n, err := readIntoCache(path, buf)
				mu.Lock()
				bytesRead += n
				fileCount++
				mu.Unlock()
				if err != nil {
					log.WithError(err).WithField("file", path).Debug("prewarm read failed")
				}
			}
		}()
	}

	walkErr := filepath.WalkDir(lower, func(path string, d os.DirEntry, err error) error {
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
	l := log.WithFields(logrus.Fields{
		"files":     fileCount,
		"gb":        float64(bytesRead) / 1e9,
		"elapsed_s": elapsed.Round(time.Second).Seconds(),
		"mb_per_s":  int64(mbps),
	})
	if walkErr != nil {
		l = l.WithError(walkErr)
	}
	l.Info("page-cache prewarm complete")
}

// readIntoCache streams a file through the page cache via io.Discard.
// Returns bytes read. The fixed buffer keeps the reader's RSS flat; the
// pages land in the (reclaimable) file cache, charged to the agent
// cgroup.
func readIntoCache(path string, buf []byte) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	return io.CopyBuffer(io.Discard, f, buf)
}
