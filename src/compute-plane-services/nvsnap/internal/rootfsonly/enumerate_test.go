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

package rootfsonly

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// captureFS materializes a synthetic captured rootfs tree at root.
// Entries are absolute paths inside the source pod (e.g.
// "/root/.cache/deep_gemm/jit_cache.bin"); the helper places them
// under root verbatim. content is written as the file body.
func captureFS(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, content := range files {
		abs := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// extractPaths returns just the Path field of each ExtractPath, sorted.
// Used for stable assertions.
func extractPaths(eps []checkpointstore.ExtractPath) []string {
	out := make([]string, len(eps))
	for i, e := range eps {
		out[i] = e.Path
	}
	sort.Strings(out)
	return out
}

// TestEnumerate_DeepSeekShape mirrors the actual captured tree we
// observed on GCP-H100-a 2026-06-06: top-level dirs are intermediate
// path nodes; the source pod's writes are at /root/.cache/<engine>/
// and /usr/local/lib/python3.12/dist-packages/<pkg>/. Catalog today
// only catches huggingface; this enumerator must catch deep_gemm,
// flashinfer, sgl-workspace, and the dist-packages writes too.
func TestEnumerate_DeepSeekShape(t *testing.T) {
	root := t.TempDir()
	captureFS(t, root, map[string]string{
		// Engine caches under /root/.cache/<engine>/
		"/root/.cache/huggingface/blobs/abc": strings.Repeat("a", 200<<10), // 200 KiB
		"/root/.cache/deep_gemm/cubins/foo":  strings.Repeat("b", 200<<10),
		"/root/.cache/flashinfer/kernel":     strings.Repeat("c", 200<<10),
		"/root/.cache/torch_extensions/op":   strings.Repeat("d", 200<<10),
		// Python packages — source pod did pip install or the base image
		// upperdir captured them.
		"/usr/local/lib/python3.12/dist-packages/deep_gemm/__init__.py":  strings.Repeat("e", 200<<10),
		"/usr/local/lib/python3.12/dist-packages/flashinfer/__init__.py": strings.Repeat("f", 200<<10),
		// sglang workspace files.
		"/sgl-workspace/sglang/server.py": strings.Repeat("g", 200<<10),
		// File at the root tree level — should NOT be mounted (we
		// don't mount whole-/ even when the source wrote /etc/foo,
		// because /etc has per-pod files; capture excludes those).
		"/etc/ld.so.cache": "x", // tiny — also below MinBytes
	})

	got := extractPaths(enumerateMountPoints(root, 100<<10))
	// /etc/ld.so.cache is intentionally absent: it's a file (not a dir)
	// AND below MinBytes — the enumerator only emits DIRECTORIES with
	// direct file children (walkForMountPoints), so we assert dirs only.
	want := []string{
		"/root/.cache/deep_gemm/cubins",
		"/root/.cache/flashinfer",
		"/root/.cache/huggingface/blobs",
		"/root/.cache/torch_extensions",
		"/sgl-workspace/sglang",
		"/usr/local/lib/python3.12/dist-packages/deep_gemm",
		"/usr/local/lib/python3.12/dist-packages/flashinfer",
	}
	if !equalSorted(got, want) {
		t.Fatalf("paths mismatch:\n got: %v\nwant: %v", got, want)
	}
}

// TestEnumerate_HFHubMountedAtHubRoot: a HuggingFace/NGC hub cache must
// be emitted as ONE mount at the HUB ROOT (the parent of the
// models--<org>--<model> triad), NOT at the model dir and NOT split to
// blobs/. Two reasons, both generic to any huggingface_hub consumer
// (HF Transformers, NGC NIM, …) — the detection is purely structural
// (a child with both snapshots/ and blobs/), never path-specific:
//   - splitting at blobs/ drops the tiny refs/snapshots scaffolding
//     (symlinks into blobs) → broken cache → re-download (2026-06-10);
//   - mounting only the model dir leaves the hub's tmp/ staging dir on
//     the container layer, so the library's tmp -> blobs rename crosses
//     filesystems and fails with EXDEV (os error 18) (2026-06-11).
//
// Mounting the hub root keeps tmp/ and models--*/ on the same overlay.
func TestEnumerate_HFHubMountedAtHubRoot(t *testing.T) {
	cases := []struct {
		name, hub string
	}{
		{"ngc-nim", "/opt/nim/.cache/ngc/hub"},
		{"hf-transformers", "/root/.cache/huggingface/hub"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			model := tc.hub + "/models--org--model"
			captureFS(t, root, map[string]string{
				model + "/refs/main":                 "abc123",                   // tiny — filtered if split
				model + "/snapshots/abc123/cfg.json": strings.Repeat("s", 1<<10), // small
				model + "/blobs/sha256-aaaa":         strings.Repeat("b", 2<<20), // big payload
			})
			got := extractPaths(enumerateMountPoints(root, 100<<10))
			want := []string{tc.hub} // the hub root, not the model dir, not .../blobs
			if !equalSorted(got, want) {
				t.Fatalf("HF-hub must mount at hub root:\n got: %v\nwant: %v", got, want)
			}
		})
	}
}

// TestEnumerate_TritonRepoMountedWhole: a Triton model repository — a
// dir whose child model dirs each hold a config.pbtxt — must mount as
// ONE unit, not be split per model. A Riva BLS ensemble references its
// sibling models by repo-relative path, so splitting the repo across
// overlays breaks model load (whisper-large-v3 Riva, GCP-H100-a
// 2026-06-11: "directory name must equal model name" → cudaHostUnregister
// abort). Detection is the structural config.pbtxt signature, like the
// HF-hub one — it never coarsens dirs that lack it (so /opt etc. stay
// deep and don't shadow base-image content).
func TestEnumerate_TritonRepoMountedWhole(t *testing.T) {
	root := t.TempDir()
	repo := "/data/models"
	captureFS(t, root, map[string]string{
		repo + "/riva-trt-whisper/config.pbtxt":      strings.Repeat("c", 1<<10),
		repo + "/riva-trt-whisper/1/model.engine":    strings.Repeat("e", 2<<20),
		repo + "/whisper-bls/config.pbtxt":           strings.Repeat("c", 1<<10),
		repo + "/whisper-bls/1/riva_bls_config.yaml": strings.Repeat("y", 1<<10),
	})
	got := extractPaths(enumerateMountPoints(root, 100<<10))
	want := []string{repo} // whole repo, both model dirs together
	if !equalSorted(got, want) {
		t.Fatalf("Triton repo must mount whole:\n got: %v\nwant: %v", got, want)
	}
}

// TestEnumerate_NeverWholeMountSystemDirs: a base-owned top-level dir
// (/etc, /opt, ...) must NEVER be emitted as a whole mount even when the
// app wrote a file directly into it — overlaying it would mask the base
// image (/etc/passwd, /opt/nim/start_server.sh) and break the restore.
// We still descend so app subdirs mount. Regression for the /etc whole
// mount that v0.0.62's 8KB filter exposed (and the /opt v0.0.60 crash).
func TestEnumerate_NeverWholeMountSystemDirs(t *testing.T) {
	root := t.TempDir()
	captureFS(t, root, map[string]string{
		"/etc/some-app-config":        strings.Repeat("c", 50<<10), // direct file in /etc — must NOT emit /etc
		"/opt/app-direct-file":        strings.Repeat("o", 50<<10), // direct file in /opt — must NOT emit /opt
		"/opt/myengine/.cache/kernel": strings.Repeat("k", 50<<10), // app subdir — MUST emit
		"/data/models/m/config.pbtxt": strings.Repeat("d", 50<<10), // app-owned top-level — may emit
	})
	got := extractPaths(enumerateMountPoints(root, 8<<10))
	want := []string{
		"/data/models",         // app-owned, Triton repo → whole
		"/opt/myengine/.cache", // descended into /opt, app subdir emitted
		// NOT "/etc", NOT "/opt" — masking guard
	}
	if !equalSorted(got, want) {
		t.Fatalf("system dirs must not mount whole:\n got: %v\nwant: %v", got, want)
	}
}

// TestEnumerate_SkipsTmpAndRun: even if a buggy capture leaks /tmp or
// /run into the tree, the enumerator must defensively drop them.
func TestEnumerate_SkipsTmpAndRun(t *testing.T) {
	root := t.TempDir()
	captureFS(t, root, map[string]string{
		"/tmp/torchinductor_12345/cache":    strings.Repeat("a", 200<<10),
		"/run/secrets/serviceaccount/token": strings.Repeat("b", 200<<10),
		"/proc/self/maps":                   "x",
		"/sys/class/net/eth0/address":       "x",
		"/dev/nvidia0":                      "x",
		"/var/log/sglang.log":               strings.Repeat("c", 200<<10),
		"/var/run/foo":                      strings.Repeat("d", 200<<10),
		"/root/.cache/legitimate/file":      strings.Repeat("e", 200<<10),
	})

	got := extractPaths(enumerateMountPoints(root, 0))
	// /root/.cache/legitimate is the only thing that should pass.
	want := []string{"/root/.cache/legitimate"}
	if !equalSorted(got, want) {
		t.Fatalf("paths mismatch:\n got: %v\nwant: %v", got, want)
	}
}

// TestEnumerate_MinBytesFiltersNoise: trivial empty-ish dirs
// (.gitkeep, single config file) shouldn't take up a mount slot.
func TestEnumerate_MinBytesFiltersNoise(t *testing.T) {
	root := t.TempDir()
	captureFS(t, root, map[string]string{
		"/root/.cache/big/blob":      strings.Repeat("a", 1<<20), // 1 MiB
		"/root/.cache/tiny/.gitkeep": "x",                        // 1 byte
	})
	got := extractPaths(enumerateMountPoints(root, 100<<10))
	want := []string{"/root/.cache/big"}
	if !equalSorted(got, want) {
		t.Fatalf("paths mismatch:\n got: %v\nwant: %v", got, want)
	}
}

// TestEnumerate_DefaultThresholdDropsSmallCaches: the default
// mountPointMinBytes (100 KB) drops the small GPU JIT/compute caches
// (~26 KB each). v0.0.62 briefly lowered this to 8 KB to capture them
// (warmup optimization) but that regressed the restore — a CUDA cache
// shard polluted the Triton model repo at restore (/data/models/f) and
// Triton rejected the whole repo. Reverted to 100 KB; the big model
// dirs (>100 KB) still mount. Capturing JIT caches safely is a separate
// task. This guards the revert.
func TestEnumerate_DefaultThresholdDropsSmallCaches(t *testing.T) {
	root := t.TempDir()
	captureFS(t, root, map[string]string{
		"/home/nvs/.nv/ComputeCache/f/1/kernel":     strings.Repeat("a", 26<<10),  // <100KB → dropped
		"/home/nvs/.triton/cache/abc/cuda_utils.so": strings.Repeat("b", 26<<10),  // <100KB → dropped
		"/data/models/m/config.pbtxt":               strings.Repeat("c", 1),       // tiny but...
		"/data/models/m/1/model.engine":             strings.Repeat("d", 200<<10), // model dir >100KB → kept
	})
	got := extractPaths(enumerateMountPoints(root, mountPointMinBytes))
	want := []string{"/data/models"} // only the big model repo; small JIT caches filtered
	if !equalSorted(got, want) {
		t.Fatalf("100KB default must drop small JIT caches, keep model repo:\n got: %v\nwant: %v", got, want)
	}
}

// TestEnumerate_DirWithBothFilesAndSubdirsMountsHere: when a directory
// has BOTH a direct file AND a subdir, the algorithm mounts at the
// directory (covering the whole subtree). The subdir contents come
// along for the ride, which is the right behavior for engine caches
// that nest data alongside metadata files.
func TestEnumerate_DirWithBothFilesAndSubdirsMountsHere(t *testing.T) {
	root := t.TempDir()
	captureFS(t, root, map[string]string{
		"/root/.cache/engine/manifest.json": strings.Repeat("a", 200<<10),
		"/root/.cache/engine/blobs/file1":   strings.Repeat("b", 200<<10),
		"/root/.cache/engine/blobs/file2":   strings.Repeat("c", 200<<10),
	})
	got := extractPaths(enumerateMountPoints(root, 0))
	// Expect ONE mount at /root/.cache/engine, NOT separate mounts
	// for /root/.cache/engine and /root/.cache/engine/blobs.
	want := []string{"/root/.cache/engine"}
	if !equalSorted(got, want) {
		t.Fatalf("paths mismatch:\n got: %v\nwant: %v", got, want)
	}
}

// TestEnumerate_DeterministicOrder: same input → same output. Manifest
// stability across runs is a load-bearing property (hash-equality
// checks downstream).
func TestEnumerate_DeterministicOrder(t *testing.T) {
	root := t.TempDir()
	captureFS(t, root, map[string]string{
		"/a/data":          strings.Repeat("x", 200<<10),
		"/b/data":          strings.Repeat("x", 200<<10),
		"/c/sub/data":      strings.Repeat("x", 200<<10),
		"/root/.cache/d/x": strings.Repeat("x", 200<<10),
	})
	first := extractPaths(enumerateMountPoints(root, 0))
	second := extractPaths(enumerateMountPoints(root, 0))
	if !equalSorted(first, second) {
		t.Fatalf("non-deterministic output:\n first:  %v\n second: %v", first, second)
	}
}

// TestEnumerate_CategorizePath spot-checks the audit-tag function.
func TestEnumerate_CategorizePath(t *testing.T) {
	cases := map[string]string{
		"/root/.cache/huggingface":                          "hf-cache",
		"/root/.cache/deep_gemm/foo":                        "deep-gemm-cache",
		"/root/.cache/flashinfer":                           "flashinfer-cache",
		"/usr/local/lib/python3.12/dist-packages/deep_gemm": "python-dist-packages",
		"/sgl-workspace/sglang":                             "sglang-workspace",
		"/opt/nim/.cache":                                   "nim-cache",
		"/unknown/path":                                     "rootfs-extract",
	}
	for p, want := range cases {
		if got := categorizePath(p); got != want {
			t.Errorf("categorizePath(%q) = %q, want %q", p, got, want)
		}
	}
}

// TestAlwaysExcludeRootfsPaths_CoversSensitivePaths is a sanity check
// on the static exclude list — a regression guard so a future edit
// can't quietly drop /tmp or /etc/hostname from the captured-tree
// exclusion set.
func TestAlwaysExcludeRootfsPaths_CoversSensitivePaths(t *testing.T) {
	want := []string{
		"/tmp",             // transient / torchinductor JIT scratch
		"/run",             // kubelet SA token, secrets
		"/var/log",         // per-pod logs
		"/var/cache/apt",   // package manager state
		"/etc/hostname",    // kubelet-set
		"/etc/hosts",       // kubelet-set
		"/etc/resolv.conf", // kubelet-set
	}
	have := make(map[string]bool, len(alwaysExcludeRootfsPaths))
	for _, p := range alwaysExcludeRootfsPaths {
		have[p] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("alwaysExcludeRootfsPaths missing %q (must never be captured into tree/rootfs/)", w)
		}
	}
}

// TestCaptureExcludesUnion_DoesNotMaskRealCaches verifies the static
// exclude list doesn't accidentally drop a legitimate cache path. The
// /var/cache/apt entry must NOT match /var/cache/triton or similar.
func TestCaptureExcludesUnion_DoesNotMaskRealCaches(t *testing.T) {
	mustNotMatch := []string{
		"/var/cache/triton",        // hypothetical engine cache under /var/cache
		"/var/lib/postgres",        // /var/lib is broad; only /var/lib/dhcp + /var/lib/systemd are listed
		"/etc/ssl/certs",           // /etc IS in some image's data flow
		"/etc/sglang/config.json",  // engine config files
		"/root/.cache/huggingface", // canonical cache
		"/opt/nim/.cache",          // NIM cache
	}
	for _, p := range mustNotMatch {
		for _, x := range alwaysExcludeRootfsPaths {
			if p == x || strings.HasPrefix(p, x+"/") {
				t.Errorf("exclude %q masks legitimate path %q (false positive)", x, p)
			}
		}
	}
}

func equalSorted(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
