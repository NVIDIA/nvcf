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

package treecopy

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// buildBenchTree writes nFiles regular files of fileSize bytes each
// into a fresh tempdir under b.TempDir(). Content is random bytes so
// the kernel can't fast-path it as a zero-page.
//
// Returns (src, totalBytes). Cleanup is automatic via b.TempDir().
func buildBenchTree(b *testing.B, nFiles int, fileSize int64) (root string, totalBytes int64) {
	b.Helper()
	src := b.TempDir()
	// Use 100 files per subdir to mimic CRIU's pages-N.img +
	// core-N.img + a-few-large blobs shape; this matters because a
	// flat dir of 7000 files vs a tree of 70 dirs × 100 files
	// stresses readdir differently.
	const filesPerDir = 100
	buf := make([]byte, fileSize)
	if _, err := rand.Read(buf); err != nil {
		b.Fatalf("rand: %v", err)
	}
	for i := range nFiles {
		dir := filepath.Join(src, fmt.Sprintf("d%04d", i/filesPerDir))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatalf("mkdir: %v", err)
		}
		path := filepath.Join(dir, fmt.Sprintf("f%05d.bin", i))
		if err := os.WriteFile(path, buf, 0o644); err != nil {
			b.Fatalf("write: %v", err)
		}
	}
	return src, int64(nFiles) * fileSize
}

// BenchmarkCopy_SmallFilesManyOf simulates the CRIU dump shape:
// many medium files (~16 MiB) totaling ~512 MiB. Throughput here is
// the closest analog to the production capture-write workload.
//
// Run with -benchtime=1x (one iteration) since each copy is large.
// Run with -bench=Copy -benchmem -benchtime=1x ./internal/treecopy/
func BenchmarkCopy_SmallFilesManyOf(b *testing.B) {
	src, total := buildBenchTree(b, 32, 16<<20) // 32 × 16 MiB = 512 MiB
	b.SetBytes(total)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		dst := b.TempDir()
		b.StartTimer()
		c := NewCopier(nil, nil)
		if _, _, err := c.Copy(context.Background(), src, dst); err != nil {
			b.Fatalf("copy: %v", err)
		}
	}
}

// BenchmarkCopy_LargeFilesFewOf simulates a different CRIU shape —
// a handful of very large files (pages-1.img up to several GiB on
// 70B models). Same total bytes as the small-files case so the
// throughput numbers are directly comparable.
func BenchmarkCopy_LargeFilesFewOf(b *testing.B) {
	src, total := buildBenchTree(b, 4, 128<<20) // 4 × 128 MiB = 512 MiB
	b.SetBytes(total)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		dst := b.TempDir()
		b.StartTimer()
		c := NewCopier(nil, nil)
		if _, _, err := c.Copy(context.Background(), src, dst); err != nil {
			b.Fatalf("copy: %v", err)
		}
	}
}
