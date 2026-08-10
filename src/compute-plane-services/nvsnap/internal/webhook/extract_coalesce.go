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
	"path"
	"sort"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// coalesceExtractCacheRoots groups RootfsExtractPaths that share a
// parent directory and collapses each group ≥ coalesceMinSiblings into
// a single mount at the parent.
//
// Why: the per-extract-path OverlayFS approach mounts each captured
// leaf (e.g. /root/.tilelang/cache/<hash>) as its own overlay. Runtime
// code that writes a tempfile to a sibling not in the manifest (e.g.
// /root/.tilelang/cache/tmp/) and renames it into the captured cache
// dir crosses filesystem boundaries → os.rename returns EXDEV.
// Mounting one overlay at the parent (/root/.tilelang/cache/) puts
// the captured leaves, runtime-created siblings, and tempfiles on the
// SAME overlay — renames stay intra-fs.
//
// The 3-sibling threshold is a deliberate cache-root signal: __pycache__
// directories are always a single leaf per module dir (so they stay
// per-leaf and don't shadow the module's source files), but TileLang /
// deep_gemm / tvm-ffi cache dirs have many sibling kernel directories
// and benefit from collapse.
const coalesceMinSiblings = 3

func coalesceExtractCacheRoots(paths []checkpointstore.ExtractPath) []checkpointstore.ExtractPath {
	if len(paths) < coalesceMinSiblings {
		return paths
	}

	type group struct {
		items []checkpointstore.ExtractPath
		order int // index of first append, for stable output ordering
	}
	byParent := make(map[string]*group, len(paths))
	for i, p := range paths {
		parent := path.Dir(p.Path)
		if g, ok := byParent[parent]; ok {
			g.items = append(g.items, p)
		} else {
			byParent[parent] = &group{items: []checkpointstore.ExtractPath{p}, order: i}
		}
	}

	// Stable order: groups appear in the same position as their first
	// member did in the input.
	parents := make([]string, 0, len(byParent))
	for parent := range byParent {
		parents = append(parents, parent)
	}
	sort.Slice(parents, func(i, j int) bool {
		return byParent[parents[i]].order < byParent[parents[j]].order
	})

	out := make([]checkpointstore.ExtractPath, 0, len(paths))
	for _, parent := range parents {
		g := byParent[parent]
		if len(g.items) >= coalesceMinSiblings {
			var totalSize, totalFiles int64
			for _, item := range g.items {
				totalSize += item.SizeBytes
				totalFiles += item.FileCount
			}
			out = append(out, checkpointstore.ExtractPath{
				Path:      parent,
				Category:  g.items[0].Category,
				SizeBytes: totalSize,
				FileCount: totalFiles,
			})
		} else {
			out = append(out, g.items...)
		}
	}
	return out
}
