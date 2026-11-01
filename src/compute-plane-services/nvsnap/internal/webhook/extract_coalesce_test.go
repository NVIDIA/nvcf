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

package webhook

import (
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// TestCoalesceExtractCacheRoots_CollapsesCacheSiblings: 22 sibling
// kernel dirs under /root/.tilelang/cache/ collapse into one mount at
// the parent. The lone /usr/.../numpy/ma/__pycache__ stays per-leaf
// (single child — no risk of shadowing the module's source files).
func TestCoalesceExtractCacheRoots_CollapsesCacheSiblings(t *testing.T) {
	in := []checkpointstore.ExtractPath{
		{Path: "/usr/lib/python3.12/site-packages/numpy/ma/__pycache__", Category: "py-cache", SizeBytes: 100, FileCount: 5},
	}
	for i := 0; i < 22; i++ {
		in = append(in, checkpointstore.ExtractPath{
			Path:      "/root/.tilelang/cache/k" + string(rune('a'+i)),
			Category:  "tilelang-cache",
			SizeBytes: 1000, FileCount: 3,
		})
	}

	out := coalesceExtractCacheRoots(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 entries (1 leaf + 1 coalesced), got %d: %+v", len(out), out)
	}

	// Stable order: __pycache__ came first in input, so it must come
	// first in output.
	if out[0].Path != "/usr/lib/python3.12/site-packages/numpy/ma/__pycache__" {
		t.Errorf("out[0].Path = %q, want the __pycache__ leaf", out[0].Path)
	}
	if out[1].Path != "/root/.tilelang/cache" {
		t.Errorf("out[1].Path = %q, want /root/.tilelang/cache (coalesced parent)", out[1].Path)
	}
	if out[1].SizeBytes != 22*1000 || out[1].FileCount != 22*3 {
		t.Errorf("coalesced totals = (%d, %d), want (22000, 66)", out[1].SizeBytes, out[1].FileCount)
	}
}

// TestCoalesceExtractCacheRoots_BelowThresholdStaysPerLeaf: 2 siblings
// is below the threshold (3) so they stay per-leaf. Avoids accidental
// shadowing when the parent might contain non-captured source files.
func TestCoalesceExtractCacheRoots_BelowThresholdStaysPerLeaf(t *testing.T) {
	in := []checkpointstore.ExtractPath{
		{Path: "/foo/bar/a", SizeBytes: 1, FileCount: 1},
		{Path: "/foo/bar/b", SizeBytes: 2, FileCount: 2},
	}
	out := coalesceExtractCacheRoots(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 leaves preserved, got %d: %+v", len(out), out)
	}
	if out[0].Path != "/foo/bar/a" || out[1].Path != "/foo/bar/b" {
		t.Errorf("unexpected leaf paths: %+v", out)
	}
}

// TestCoalesceExtractCacheRoots_EmptyInput is a sanity guard for the
// length<minSiblings short-circuit.
func TestCoalesceExtractCacheRoots_EmptyInput(t *testing.T) {
	if got := coalesceExtractCacheRoots(nil); got != nil {
		t.Errorf("nil input → want nil, got %+v", got)
	}
	if got := coalesceExtractCacheRoots([]checkpointstore.ExtractPath{}); len(got) != 0 {
		t.Errorf("empty input → want empty, got %+v", got)
	}
}
