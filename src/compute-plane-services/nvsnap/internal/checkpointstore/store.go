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

// Package checkpointstore provides content-addressed storage for rootfs-only
// captures. A capture is the directory tree the agent snapshots from a
// running pod (overlay upperdir + named user-data volumes). The same content
// is identified by the same Hash regardless of which node captured it,
// enabling cross-node fan-out: one pod warms a workload and writes the tree
// to the store, every other pod reads from the store.
//
// Phase 1 ships Local (file-tree on a node-local NVMe path) and GCS
// backends. See docs/MULTI-GPU-ROOTFS-FANOUT-DESIGN.md §3.
package checkpointstore

import (
	"context"
	"errors"
	"time"
)

// CaptureFormatVersion is bumped whenever the on-disk schema for a capture
// changes (manifest format, layout, included metadata). Hashes are recomputed
// across versions, so old captures stop matching.
const CaptureFormatVersion = 1

// ErrNotFound is returned by Stat / Get when no capture is stored under the
// given hash.
var ErrNotFound = errors.New("checkpointstore: not found")

// ErrExists is returned by Put when the hash already exists and the caller
// did not request overwrite. Stores treat captures as immutable: the
// agent re-running a capture for the same hash should observe a no-op,
// not corrupt the existing tree.
var ErrExists = errors.New("checkpointstore: hash already exists")

// SourceKind discriminates how a CaptureSource is materialized into the
// capture tree. Kept as a typed string rather than an int enum so the
// JSON plan that the per-capture Job receives is human-debuggable.
type SourceKind string

const (
	// SourceKindRootfs copies a host directory tree into the capture
	// (overlay upperdir, hostPath user-data volume, emptyDir contents).
	// SrcPath, DstSubpath, Excludes apply.
	SourceKindRootfs SourceKind = "rootfs"

	// SourceKindCRIU copies a CRIU dump directory into the capture tree
	// (nvsnap#63). SrcPath is the host path to the agent's local CRIU
	// dump (e.g. /var/lib/nvsnap/checkpoints/<hash>__<ts>); the writer
	// copies the whole tree verbatim — no Excludes, no mountinfo
	// filtering. Used by the PerCapturePVCBackend to promote a
	// just-finished CRIU dump into the per-capture RWX PVC so multi-
	// node restore can mount it ReadOnly in parallel (see
	// docs/L2-PVC-CRIU-DESIGN.md).
	SourceKindCRIU SourceKind = "criu"
)

// CaptureSource is one logical source the orchestrator wants captured into
// a single hash-keyed tree. Backends are responsible for materializing the
// source according to its Kind and writing the result to <tree>/<DstSubpath>.
//
// This is the "what to capture" handoff between the orchestrator and the
// Backend. It eliminates the double-copy that an intermediate srcDir
// staging step would force: backends drive the I/O directly from the
// authoritative source, in whatever execution context they need
// (Local: in-process; GPDRox: a one-shot per-capture Job).
type CaptureSource struct {
	// Kind selects the materialization strategy. Default (empty string)
	// is treated as "rootfs" for backward compatibility with v0.17.x.
	Kind SourceKind `json:"kind,omitempty"`

	// SrcPath is an absolute host path the backend will read from
	// (Kind=rootfs only — the agent reads it directly via the Local
	// backend post-5d).
	//
	// Examples:
	//   /var/lib/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots/<n>/fs   (rootfs upperdir)
	//   /var/lib/containerd/nvsnap-hf-cache                                              (hostPath user-data)
	//   /var/lib/kubelet/pods/<uid>/volumes/kubernetes.io~empty-dir/<name>            (emptyDir user-data)
	SrcPath string `json:"src_path,omitempty"`

	// DstSubpath is the relative path within the capture tree where the
	// materialized source's contents should land. e.g. "rootfs",
	// "volumes/hf-cache", ...
	DstSubpath string `json:"dst_subpath"`

	// Excludes are paths within SrcPath to skip during the copy. Used
	// for rootfs captures (mountinfo bind-mount paths). Paths are
	// absolute as they appear inside SrcPath (e.g. "/etc/hostname").
	Excludes []string `json:"excludes,omitempty"`
}

// Store is the abstract content-addressed capture store.
//
// All methods are safe for concurrent use. Implementations dedupe concurrent
// Puts of the same hash so two pods finishing warmup at the same time
// produce one capture.
type Store interface {
	// Put copies all sources into the store under hash and writes the
	// manifest. Each source's content lands at <tree>/<DstSubpath>; the
	// orchestrator pre-classified what goes where, but the backend owns
	// the actual filtered copy (one read, one write — no intermediate
	// stage). The returned manifest has the implementation-populated
	// SizeBytes / FileCount counters.
	//
	// Idempotent: if hash already exists, Put returns ErrExists and the
	// existing tree is unchanged. Callers that want to refresh a capture
	// should Delete first.
	Put(ctx context.Context, hash string, sources []CaptureSource, manifest Manifest) (Manifest, error)

	// Get ensures dstDir contains the tree stored under hash. On cache
	// hit (the implementation already has the tree locally), this is
	// near-instant; on cache miss it streams from the backing store.
	// Returns the manifest.
	//
	// dstDir must already exist. Get does not remove unrelated files
	// already in dstDir, but will overwrite any that conflict.
	Get(ctx context.Context, hash string, dstDir string) (Manifest, error)

	// Stat returns the manifest if hash exists, or ErrNotFound.
	// Implementations should make Stat cheap — used by webhooks on the
	// admission path.
	Stat(ctx context.Context, hash string) (Manifest, error)

	// Delete removes a capture from the store. Used by retention policy.
	// Returns ErrNotFound if the hash was already absent.
	Delete(ctx context.Context, hash string) error
}

// Copier materializes captured sources at a destination path on disk.
// It exists as a separate interface from Backend so the L2 backend
// (PerCapturePVCBackend, v0.0.51 mount-holder design) can orchestrate
// "create rwx PVC → create mount-holder → call Copier.Copy → delete
// mount-holder → snapshot" without itself needing privileged host
// access. The Copier is the only step that reads source pod rootfs
// via /host/proc/1/root; backends stay HTTP- and SQL-style and run
// anywhere with API access.
//
// Production implementation: internal/agent.AgentCopier — wraps the
// in-process treecopy.Copier with a fixed HostRoot (typically "/host"
// from the nvsnap-agent DaemonSet's host mount).
//
// Tests inject fakes to assert backends call Copy with the right
// (destRoot, sources) without doing actual filesystem I/O.
type Copier interface {
	// Copy materializes each source under destRoot/source.DstSubpath,
	// applying source.Excludes for rootfs sources. Returns the total
	// (bytes, files) actually written across all sources. The Copier
	// owns its own view of the host filesystem (typically /host as
	// mounted by the agent DaemonSet).
	Copy(ctx context.Context, destRoot string, sources []CaptureSource) (bytes int64, files int64, err error)
}

// Manifest is the metadata record persisted alongside every capture.
// One per hash. Stored as manifest.json at the root of each capture
// directory.
type Manifest struct {
	// Hash is the content-addressed identifier (full sha256 hex).
	Hash string `json:"hash"`

	// CaptureMethod records HOW this checkpoint was captured —
	// "rootfs" (filesystem-tree snapshot) or "criu" (process dump).
	// Set authoritatively by the agent at capture time (it chooses the
	// path per the CLAUDE.md rule-20 matrix), so the restore side
	// dispatches deterministically instead of inferring the type from
	// manifest shape. Empty on pre-v0.0.56 manifests — callers fall
	// back to the shape heuristic (RootfsExtractPaths / Volume Type)
	// in that case. Prevents "crossing paths" (a rootfs capture sent
	// to the CRIU restore-entrypoint, or vice versa).
	CaptureMethod string `json:"capture_method,omitempty"`

	// CacheDir is set only for CaptureMethod=="cachedir": the in-pod
	// mount path (e.g. "/nvsnap") that was captured as the entire PVC
	// root. On restore the webhook mounts the rox read-only at exactly
	// this path — no overlayfs, no pivot_root. Empty for rootfs/criu.
	CacheDir string `json:"cache_dir,omitempty"`

	// CacheEnv is the cache/model env set the capture pod actually ran
	// with (cachedir mode only), stamped from the live pod at capture.
	// It is the single source of truth for this checkpoint's env: the
	// restore webhook replays it verbatim and never re-reads the live
	// cachedir-env ConfigMap, so path-consistency (and thus JIT/compile
	// cache reuse) holds even if the ConfigMap is edited after capture.
	// Empty on pre-v0.1.0 cachedir manifests — restore falls back to
	// recomputing from CacheDir + the built-in/default template.
	CacheEnv map[string]string `json:"cache_env,omitempty"`

	// CapturedAt is the time the capture was finalized.
	CapturedAt time.Time `json:"captured_at"`

	// SourcePodMeta records the pod the capture came from, for audit.
	// Keys: namespace, name, uid, image, image_digest, model_id, engine.
	SourcePodMeta map[string]string `json:"source_pod_meta,omitempty"`

	// CaptureFormatVersion is the on-disk schema version. Mismatched
	// versions invalidate the capture and force a cold start.
	CaptureFormatVersion int `json:"capture_format_version"`

	// CapturedOnNodes is the list of K8s node names that hold a copy
	// of this capture's data. For Local backend with per-node hostPath
	// (the Phase 1 default), this is exactly one node — the node where
	// the agent ran the capture. For shared-storage backends (GPDRox
	// PVC, Filestore RWX), this is "all nodes" implicitly and the
	// field can be left empty (webhook treats empty as "any node").
	//
	// The webhook injects nodeAffinity from this list so FRESH pods
	// land on a node where their hostPath cache actually has files.
	CapturedOnNodes []string `json:"captured_on_nodes,omitempty"`

	// Volumes describes each captured directory: rootfs upperdir and
	// each user-data volume. SizeBytes and FileCount are populated by
	// the Store on Put.
	Volumes []VolumeMeta `json:"volumes"`

	// RootfsExtractPaths are well-known engine cache subpaths inside
	// the rootfs upperdir that the webhook should shadow-mount onto
	// FRESH pods (e.g. /root/.cache/huggingface, /opt/nim/.cache,
	// /root/.triton). Captured at capture time by scanning the upperdir
	// against a curated catalog; only paths that exist with non-trivial
	// content are recorded.
	//
	// This list is what makes restore work without modifying the
	// customer's pod yaml: the webhook injects one Volume + readOnly
	// VolumeMount per path, sourced from the captured tree.
	RootfsExtractPaths []ExtractPath `json:"rootfs_extract_paths,omitempty"`

	// TotalSizeBytes is the sum across volumes. Convenience field; the
	// authoritative value is sum(Volumes[].SizeBytes).
	TotalSizeBytes int64 `json:"total_size_bytes"`

	// FileCount is the sum across volumes. Same caveat as TotalSizeBytes.
	FileCount int64 `json:"file_count"`

	// EntryArgv is the source container's PID-1 argv (command + args),
	// recorded at capture time from /proc/<pid>/cmdline. The whole-rootfs
	// restore shim (cmd/nvsnap-rootfs-restore) execs this after pivot_root,
	// so restore works for images whose entrypoint comes from the image
	// ENTRYPOINT/CMD and is therefore absent from the restored Pod spec
	// (NIM, whisper). Empty for pre-existing captures; the webhook then
	// falls back to the restored pod's explicit command/args.
	EntryArgv []string `json:"entry_argv,omitempty"`

	// EntryCwd is the source container PID-1 working directory at capture
	// time. The shim chdir()s here before exec so relative paths in the
	// entrypoint resolve as they did pre-capture. Empty → "/".
	EntryCwd string `json:"entry_cwd,omitempty"`
}

// VolumeMeta is a single captured volume's metadata. Volume.Name == "rootfs"
// is the special-case rootfs upperdir; any other name is a user-data volume
// (hostPath or emptyDir) addressed by its Kubernetes volume name.
type VolumeMeta struct {
	Name      string `json:"name"`
	MountPath string `json:"mount_path"`
	Type      string `json:"type"` // "rootfs" | "hostPath" | "emptyDir"
	SizeBytes int64  `json:"size_bytes"`
	FileCount int64  `json:"file_count"`

	// Namespace is the K8s namespace to look up the backing PVC in.
	// Required by backends whose artifacts are namespace-scoped (the
	// L2 PerCapturePVCBackend looks for rox-<hash> in this namespace).
	// Ignored by namespace-agnostic backends (Local hostPath).
	// Set by the caller (admission webhook) to the consuming pod's
	// namespace — K8s only lets a pod mount PVCs in its own ns.
	Namespace string `json:"namespace,omitempty"`
}

// ExtractPath is a single subpath within a captured rootfs upperdir
// that the webhook will shadow-mount on restored pods. The same Path
// field is used both as the source subpath inside the captured tree
// (relative to tree/rootfs/) and as the destination MountPath inside
// the customer's container.
type ExtractPath struct {
	// Path is the absolute path inside the source pod's rootfs
	// (e.g. "/root/.cache/huggingface").
	Path string `json:"path"`

	// Category is a human-readable tag for the engine cache family.
	// Used for audit and per-engine policy; not load-bearing for the
	// webhook injection itself. Examples: "hf-cache", "triton-cache",
	// "torch-compile-cache", "nim-cache", "cuda-program-cache",
	// "trtllm-engine-cache", "vllm-cache".
	Category string `json:"category,omitempty"`

	// SizeBytes / FileCount for accounting + display.
	SizeBytes int64 `json:"size_bytes"`
	FileCount int64 `json:"file_count"`
}
