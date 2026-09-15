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
	"strings"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// mountPointMinBytes is the recursive size threshold below which an
// enumerated directory isn't worth a webhook mount. Drops .gitkeep-style
// stubs and tiny config files.
//
// Held at 100 KB. v0.0.62 lowered it to 8 KB to also capture the small
// GPU JIT caches (/home/nvs/.nv/ComputeCache ~26 KB, /home/nvs/.triton/
// cache) so a restore wouldn't re-JIT-compile them — a warmup
// optimization. But mounting those caches REGRESSED the restore: on
// whisper a CUDA ComputeCache shard "f" appeared in the Triton model repo
// /data/models at restore time (NOT in the captured tree), and Triton's
// strict scan rejected the whole repo → "failed to load all models" →
// cudaHostUnregister abort (GCP-H100-a 2026-06-11). The proven-working
// v0.0.61 restore used 100 KB (JIT caches filtered) and loaded cleanly.
// Reverted to 100 KB: correctness (working restore) beats the few-second
// warmup win. Capturing JIT caches without polluting the model repo is a
// separate task (needs the /data/models/f root cause nailed down).
const mountPointMinBytes int64 = 100 << 10

// enumerateMountPoints walks a captured rootfs upperdir and returns one
// ExtractPath per directory whose direct children include files. It is
// the generic replacement for the curated rootfsExtractCatalog
// (nvsnap#88): every engine cache the source pod wrote at capture time
// gets a mount, regardless of which engine put it there.
//
// Algorithm:
//
//	BFS the tree starting at rootfsDir. For each directory:
//	  - if any direct child is a file → record this directory and STOP
//	    recursing into it (the whole subtree is covered by this mount).
//	  - else (only subdirs) → recurse into each subdir.
//
// Result: a set of paths at the shallowest depth that contains files,
// per branch of the tree. This avoids both extremes:
//
//   - Mounting at the top level (/root, /usr) would mask base-image
//     content the source pod did NOT write to (e.g. /root/.bashrc,
//     /usr/bin/python). The captured tree only holds source-pod writes,
//     so mounting it whole hides everything else.
//   - Mounting at the file level would emit one mount per file —
//     thousands of mounts blow up the pod spec.
//
// Trade-off: a directory that has both files AND subdirs will mount as
// one unit (so any nested subdir not modified by the source still has
// its base-image content masked, but only within the modified parent).
// Acceptable: if the source pod modified a directory, it usually owns
// the whole directory's namespace for its purposes.
//
// Output paths are absolute (rooted at "/") as they would appear
// inside the source pod, matching the existing ExtractPath shape.
// Returned in lexicographic order so manifest output is stable across
// runs of identical content.
//
// Honors minSubdirBytes as a per-directory noise filter — directories
// whose recursive size is below this don't pay the mount overhead.
// Pass 0 to include every non-empty directory.
//
// SKIP set (defense-in-depth even though capture-side excludes these):
// /proc, /sys, /dev, /tmp, /run, /lost+found, /var/log, /var/run.
// Anything matching is dropped from the result.
func enumerateMountPoints(rootfsDir string, minSubdirBytes int64) []checkpointstore.ExtractPath {
	var out []checkpointstore.ExtractPath
	walkForMountPoints(rootfsDir, rootfsDir, minSubdirBytes, &out)
	return out
}

func walkForMountPoints(root, dir string, minSubdirBytes int64, out *[]checkpointstore.ExtractPath) {
	abs := absoluteFromTreeRoot(root, dir)
	isRoot := abs == "/"

	// Skip set applies to candidate mount paths only — not to the
	// tree root itself, which exists purely as a starting point. We
	// also never emit a mount for "/" (can't mount over the
	// container's whole rootfs from a webhook).
	if !isRoot && shouldSkipMountPoint(abs) {
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return // unreadable dir — skip; not load-bearing for capture correctness
	}

	hasFile := false
	var subdirs []string
	for _, e := range entries {
		if e.IsDir() {
			subdirs = append(subdirs, filepath.Join(dir, e.Name()))
			continue
		}
		// Symlinks count as "file" for mount-here-or-recurse purposes; a
		// symlink in a directory means the source pod laid that link
		// down, so we want to expose it.
		if e.Type().IsRegular() || e.Type()&os.ModeSymlink != 0 {
			hasFile = true
		}
	}

	// Base-image-owned top-level FHS dirs must NEVER be emitted as a
	// whole-directory mount, even when the captured upperdir has a direct
	// file there (e.g. an app writes a config straight into /etc).
	// Overlaying the captured app-only view of these masks the base
	// image's content at the same path — mounting /etc whole hides
	// /etc/{passwd,ssl/certs,nsswitch.conf}; /opt hides
	// /opt/nim/start_server.sh — and the restored container breaks (the
	// /opt masking was the v0.0.60 crash; /etc surfaced in v0.0.62 once
	// the size filter dropped to 8KB). We still DESCEND so app-written
	// SUBDIRS (/opt/nim/.cache, /home/nvs/.triton/cache, /root/.cache/X)
	// mount normally. App-owned top-level dirs (/data, /config,
	// /workspace, /sgl-workspace, ...) are deliberately NOT listed.
	neverWhole := !isRoot && isNeverMountWholeDir(abs)

	// For non-root dirs that contain direct file children: emit ONE
	// mount here, covering the whole subtree. Don't recurse. (Skipped for
	// base-owned dirs — descend into them instead.)
	if !isRoot && hasFile && !neverWhole {
		size, files := dirSize(dir)
		if size >= minSubdirBytes && files > 0 {
			*out = append(*out, checkpointstore.ExtractPath{
				Path:      abs,
				Category:  categorizePath(abs),
				SizeBytes: size,
				FileCount: files,
			})
		}
		return
	}

	// HuggingFace / NGC hub cache ROOT must be captured + mounted WHOLE.
	// The hub library lays out each model as
	//   models--<org>--<model>/{refs,snapshots,blobs}
	// (snapshots/<rev>/* are SYMLINKS into blobs/, refs/<name> names the
	// revision) and downloads into a sibling `tmp/` under the hub root,
	// then RENAMES the staged file into models--*/blobs/. Two facts force
	// mounting the whole hub dir as ONE overlay rather than the per-model
	// dir:
	//   1. The triad is internally interdependent — splitting at blobs/
	//      drops the tiny refs/ + snapshots/ scaffolding and the restored
	//      engine sees a broken cache and re-downloads (whisper-large-v3
	//      NIM, GCP-H100-a 2026-06-10).
	//   2. The library's tmp -> blobs rename: if only models--* is an
	//      overlay, hub/tmp sits on the container layer and the rename
	//      crosses filesystems, failing with EXDEV "Invalid cross-device
	//      link (os error 18)" (whisper, GCP-H100-a 2026-06-11). Mounting
	//      the hub root keeps tmp and the model dirs on the SAME overlay so
	//      the rename stays intra-filesystem.
	// Detect the hub root by a CHILD that is itself a {snapshots,blobs}
	// triad, emit the hub dir whole, and stop descending. Deliberately NOT
	// a generic "dir with ≥2 content subdirs" rule — that would wrongly
	// coarsen independent-sibling dirs like .../dist-packages/{pkgA,pkgB}
	// into a single mount that shadows base-image packages.
	//
	// Triton model repository: same problem, different signature. A dir
	// whose children are Triton model dirs (each holding a config.pbtxt)
	// is ONE repository — Riva BLS ensembles reference sibling models by
	// repo-relative path, so splitting it per-model breaks model load
	// ("directory name must equal model name" → cudaHostUnregister abort,
	// whisper-large-v3 Riva, GCP-H100-a 2026-06-11). Detect by a CHILD
	// containing config.pbtxt, emit the repo root whole, stop. Like the
	// hub rule this is a specific structural signature, not a generic
	// coarsening, so it can't shadow base-image dirs.
	if !isRoot && !neverWhole {
		for _, sd := range subdirs {
			if hasSnapshotsAndBlobs(sd) || dirContainsFile(sd, "config.pbtxt") {
				size, files := dirSize(dir)
				if size >= minSubdirBytes && files > 0 {
					*out = append(*out, checkpointstore.ExtractPath{
						Path:      abs,
						Category:  categorizePath(abs),
						SizeBytes: size,
						FileCount: files,
					})
				}
				return
			}
		}
	}

	// Root, or a non-fan-out dir with only subdirs: recurse to find the
	// shallowest meaningful mount point.
	for _, sd := range subdirs {
		walkForMountPoints(root, sd, minSubdirBytes, out)
	}
}

// absoluteFromTreeRoot maps an on-disk path under the captured tree
// root to the absolute path it represents inside the source pod's
// container fs. The captured tree root itself maps to "/".
func absoluteFromTreeRoot(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." {
		return "/"
	}
	return "/" + filepath.ToSlash(rel)
}

// shouldSkipMountPoint returns true for paths that are known to be
// per-pod state OR purely runtime-managed and must never be replayed
// across pods. Mirrors (and is a superset of) the capture-side
// exclude list — defense in depth.
func shouldSkipMountPoint(absPath string) bool {
	switch absPath {
	case "/",
		// File-level kubelet/runtime per-pod state — capture-side
		// excludes these as files, but a defensive check costs nothing.
		"/etc/hostname", "/etc/hosts", "/etc/resolv.conf",
		"/etc/mtab", "/etc/ld.so.cache":
		return true
	}
	for _, prefix := range mountPointSkipPrefixes {
		if absPath == prefix || strings.HasPrefix(absPath, prefix+"/") {
			return true
		}
	}
	return false
}

// mountPointSkipPrefixes are top-level paths that must NEVER be
// replayed cross-pod regardless of what the captured tree happens to
// contain. Capture-side excludes these from the tree in the first
// place; this list is the restore-side belt-and-braces.
var mountPointSkipPrefixes = []string{
	"/proc",
	"/sys",
	"/dev",
	"/tmp",
	"/run",
	"/var/run",
	"/var/log",
	"/lost+found",
}

// hasSnapshotsAndBlobs reports whether dir contains both a "snapshots"
// and a "blobs" subdirectory — the signature of a HuggingFace/NGC hub
// `models--<org>--<model>` entry. Used to detect the hub ROOT (the parent
// of such an entry) so the whole hub, including the library's tmp/
// staging dir, mounts as one overlay. Errors → false.
func hasSnapshotsAndBlobs(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	var snap, blob bool
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		switch e.Name() {
		case "snapshots":
			snap = true
		case "blobs":
			blob = true
		}
	}
	return snap && blob
}

// neverMountWholeDirs are base-image-owned top-level FHS directories
// that must never be emitted as a whole-directory mount (only their
// app-written subdirs may be). Overlaying the captured, app-only view of
// these masks the base image's files at the same path and breaks the
// restored container. App-owned top-level dirs (/data, /config,
// /workspace, /sgl-workspace, /models, …) are intentionally absent.
var neverMountWholeDirs = map[string]struct{}{
	"/etc": {}, "/usr": {}, "/bin": {}, "/sbin": {},
	"/lib": {}, "/lib64": {}, "/lib32": {}, "/libx32": {},
	"/opt": {}, "/var": {}, "/root": {}, "/home": {},
	"/boot": {}, "/srv": {},
}

// isNeverMountWholeDir reports whether abs is a base-owned top-level dir
// we must descend into rather than mount whole.
func isNeverMountWholeDir(abs string) bool {
	_, ok := neverMountWholeDirs[abs]
	return ok
}

// dirContainsFile reports whether dir has a direct child file with the
// given name. Used to detect a Triton model repository root: a directory
// whose child model dirs each hold a config.pbtxt. Errors → false.
func dirContainsFile(dir, name string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && e.Name() == name {
			return true
		}
	}
	return false
}

// dirSize returns the recursive size + file count of dir. Errors
// (permission denied, broken symlink, etc.) are silently skipped —
// a missing file just doesn't count.
func dirSize(dir string) (sizeBytes, fileCount int64) {
	size, files, _ := dirContentStats(dir)
	return size, files
}

// dirContentStats walks a directory tree and sums regular-file size +
// count. The bool return distinguishes "empty / not-a-dir" from
// "exists but has no regular files". Errors are silently skipped — a
// missing file just doesn't count.
//
// Used by orchestrator.go to fill VolumeMeta.SizeBytes / FileCount
// for both rootfs and user-data volumes. Was in the now-removed
// extract_paths.go; lifted to enumerate.go as the shared rootfsonly
// tree-walk helper.
//
//nolint:unparam // hasRegularFiles is part of the documented contract (distinguishes empty/not-a-dir); kept for callers that may need it
func dirContentStats(path string) (sizeBytes, fileCount int64, hasRegularFiles bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return 0, 0, false
	}
	var bytes, files int64
	_ = filepath.Walk(path, func(_ string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if fi.Mode().IsRegular() {
			files++
			bytes += fi.Size()
		}
		return nil
	})
	return bytes, files, true
}

// categorizePath returns a short human-readable tag for an absolute
// path, used in manifest output for audit. Best-effort — unknown
// paths get "rootfs-extract".
func categorizePath(absPath string) string {
	switch {
	case strings.HasPrefix(absPath, "/root/.cache/huggingface"):
		return "hf-cache"
	case strings.HasPrefix(absPath, "/root/.cache/torch") ||
		strings.HasPrefix(absPath, "/root/.triton"):
		return "torch-cache"
	case strings.HasPrefix(absPath, "/root/.cache/deep_gemm"):
		return "deep-gemm-cache"
	case strings.HasPrefix(absPath, "/root/.cache/flashinfer"):
		return "flashinfer-cache"
	case strings.HasPrefix(absPath, "/root/.cache/tensorrt_llm"):
		return "trtllm-cache"
	case strings.HasPrefix(absPath, "/root/.nv/ComputeCache"):
		return "cuda-program-cache"
	case strings.HasPrefix(absPath, "/opt/nim"):
		return "nim-cache"
	case strings.HasPrefix(absPath, "/usr/local/lib"):
		return "python-dist-packages"
	case strings.HasPrefix(absPath, "/sgl-workspace"):
		return "sglang-workspace"
	}
	return "rootfs-extract"
}
