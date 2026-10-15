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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/treecopy"
)

// Compile-time assertion that *Local satisfies Backend (Store + Mounter).
var _ Backend = (*Local)(nil)

// Local is a Store backed by a directory on the node's local filesystem,
// typically a node-local NVMe path like /var/lib/nvsnap/cache. Layout:
//
//	<root>/
//	  <hash>/
//	    manifest.json
//	    tree/                    # the captured directory tree, as-is
//	  <hash>.tmp.<unique>/       # in-flight Put; renamed atomically on success
//
// This is the canonical "always-present" Store; higher-tier Stores (GCS)
// are typically wrapped by a NodeCache that uses Local as the local hot
// layer.
type Local struct {
	root string

	// putMu serializes Put for the same hash so two concurrent captures
	// don't both succeed (the first wins, second sees ErrExists). Map
	// keyed by hash; values are *sync.Mutex.
	putMu sync.Map
}

// NewLocal returns a Local Store rooted at dir. The directory is created
// if it doesn't exist.
func NewLocal(dir string) (*Local, error) {
	if dir == "" {
		return nil, errors.New("checkpointstore: NewLocal: dir is empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("checkpointstore: NewLocal: %w", err)
	}
	return &Local{root: dir}, nil
}

// Root returns the directory the store is rooted at. Used by tests and by
// callers that want to bind-mount the cache directly into a pod.
func (l *Local) Root() string { return l.root }

// PathFor returns the on-disk directory for a given hash's tree. Caller
// can bind-mount this directly. Returns ErrNotFound if the hash isn't
// present.
func (l *Local) PathFor(hash string) (string, error) {
	p := filepath.Join(l.root, hash, "tree")
	if _, err := os.Stat(p); err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", err
	}
	return p, nil
}

// Stat returns the manifest for a stored capture from the local tree.
func (l *Local) Stat(_ context.Context, hash string) (Manifest, error) {
	return readManifest(filepath.Join(l.root, hash, "manifest.json"))
}

// Put writes each source's contents directly to its DstSubpath under the
// hash-keyed cache tree, then atomically renames into place. ONE pass
// over the source data — no intermediate staging copy. Source-side
// filtering (excludes, file-type discipline, xattrs, hardlinks) is
// handled by the shared treecopy.Copier. The orchestrator hands paths
// over and gets a populated manifest back.
func (l *Local) Put(ctx context.Context, hash string, sources []CaptureSource, m Manifest) (Manifest, error) {
	muAny, _ := l.putMu.LoadOrStore(hash, &sync.Mutex{})
	mu := muAny.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	finalDir := filepath.Join(l.root, hash)
	if _, err := os.Stat(finalDir); err == nil {
		return Manifest{}, ErrExists
	}

	tmpDir, err := os.MkdirTemp(l.root, hash+".tmp.*")
	if err != nil {
		return Manifest{}, fmt.Errorf("mkdir tmp: %w", err)
	}
	// On any error past this point we remove tmpDir so we don't leak.
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	treeRoot := filepath.Join(tmpDir, "tree")
	if err := os.MkdirAll(treeRoot, 0o755); err != nil {
		return Manifest{}, fmt.Errorf("mkdir tree: %w", err)
	}

	var totalSize, totalFiles int64
	for _, src := range sources {
		dst := filepath.Join(treeRoot, src.DstSubpath)
		copier := treecopy.NewCopier(src.Excludes, nil)
		bytes, files, copyErr := copier.Copy(ctx, src.SrcPath, dst)
		if copyErr != nil {
			return Manifest{}, fmt.Errorf("copy %s → %s: %w", src.SrcPath, src.DstSubpath, copyErr)
		}
		totalSize += bytes
		totalFiles += files
	}

	m.Hash = hash
	if m.CapturedAt.IsZero() {
		m.CapturedAt = time.Now().UTC()
	}
	if m.CaptureFormatVersion == 0 {
		m.CaptureFormatVersion = CaptureFormatVersion
	}
	if m.TotalSizeBytes == 0 {
		m.TotalSizeBytes = totalSize
	}
	if m.FileCount == 0 {
		m.FileCount = totalFiles
	}
	if err := writeManifest(filepath.Join(tmpDir, "manifest.json"), m); err != nil {
		return Manifest{}, fmt.Errorf("write manifest: %w", err)
	}

	if err := os.Rename(tmpDir, finalDir); err != nil {
		if _, statErr := os.Stat(finalDir); statErr == nil {
			return Manifest{}, ErrExists
		}
		return Manifest{}, fmt.Errorf("rename: %w", err)
	}
	committed = true
	return m, nil
}

// Get copies the stored capture tree for hash into dstDir and returns its manifest.
func (l *Local) Get(ctx context.Context, hash, dstDir string) (Manifest, error) {
	src := filepath.Join(l.root, hash, "tree")
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return Manifest{}, ErrNotFound
		}
		return Manifest{}, err
	}
	if _, _, err := copyTree(ctx, src, dstDir); err != nil {
		return Manifest{}, err
	}
	return readManifest(filepath.Join(l.root, hash, "manifest.json"))
}

// Mount returns a PodMount that bind-mounts the specific volume's
// subdirectory of the on-disk capture tree into a pod at vol.MountPath,
// read-only. The Volume.Name is unique per (hash, vol.Name) so multiple
// Mount calls for the same hash produce non-conflicting pod-spec entries.
//
// IMPORTANT: hostPath references work only when the FRESH pod is scheduled
// on the same node where Local has the tree. For multi-node fan-out, use
// a backend that either (a) shares storage across nodes (e.g. an RWX PVC
// mount under Local's root visible at the same path on every node), or
// (b) provisions per-capture PVCs (GPD-ROX). When Local's root is itself
// a shared RWX mount visible at the same path on every node, this
// hostPath approach still works.
func (l *Local) Mount(_ context.Context, hash string, vol VolumeMeta) (PodMount, error) {
	// Don't os.Stat the local cache: the webhook is answered by any
	// agent in the nvsnap-webhook Service (load-balanced). The agent
	// answering may not have this particular hash captured locally —
	// the manifest CM is cluster-wide, so we know the layout, but the
	// bytes live on whichever node Manifest.CapturedOnNodes points at.
	// nodeAffinity (emitted by the webhook Mutator from CapturedOnNodes)
	// guarantees the kubelet that ultimately mounts the hostPath runs
	// on a node that DOES have the data. Verifying existence here
	// would create a false negative when the webhook lands on the
	// "wrong" agent and would force a 5-second peer-fetch within the
	// admission timeout — which can't transfer 100GB+ captures.
	treePath := filepath.Join(l.root, hash, "tree")
	subdir := VolumeSubpath(vol)
	if subdir == "" {
		return PodMount{}, fmt.Errorf("checkpointstore: Local.Mount: cannot derive subpath for volume %+v", vol)
	}
	name := mountVolumeName(hash, vol)
	hpType := corev1.HostPathDirectory
	return PodMount{
		Volume: corev1.Volume{
			Name: name,
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: filepath.Join(treePath, subdir),
					Type: &hpType,
				},
			},
		},
		VolumeMount: corev1.VolumeMount{
			Name:      name,
			MountPath: vol.MountPath,
			ReadOnly:  true,
		},
	}, nil
}

// VolumeSubpath maps a VolumeMeta to its subdirectory within a captured
// tree. The orchestrator writes captures as:
//   - tree/rootfs/                — the source pod's overlay upperdir
//   - tree/volumes/<name>/        — each captured user-data volume
//   - tree/rootfs/<absolute-path> — accessed via "rootfs-extract" Type,
//     MountPath is the absolute container path
//
// "rootfs-extract" is the type for engine cache subpaths discovered by
// the orchestrator's RootfsExtractPaths scan. The Volume.Name carries
// no addressability for this type — the absolute MountPath is what
// indexes into the captured tree.
//
// Exported because nvsnap#88's generic OverlayFS-for-all-volumes work
// in the agent's restore-overlay resolver needs to map any captured
// volume — not just rootfs-extract — to its on-disk lower dir.
func VolumeSubpath(vol VolumeMeta) string {
	switch vol.Type {
	case "rootfs":
		return "rootfs"
	case "rootfs-extract":
		if vol.MountPath == "" || vol.MountPath == "/" {
			return ""
		}
		return filepath.Join("rootfs", strings.TrimPrefix(vol.MountPath, "/"))
	case "hostPath", "emptyDir":
		if vol.Name == "" {
			return ""
		}
		return filepath.Join("volumes", vol.Name)
	default:
		return ""
	}
}

// volNameHashLen is the hash prefix length used in pod-spec Volume
// names. K8s caps Volume.Name at 63 chars (RFC1123 label). The prefix
// `nvsnap-` is 5 and the `-<sanitized-vol-name>` suffix can be up to
// ~25 chars for typical rootfs extract paths. That leaves plenty of
// budget for the hash. 16 hex chars = 64 bits — birthday-collision
// at ~2^32 (~4B concurrent captures), well past any plausible horizon.
// The full 32-char ShortHash is still used for label values, ConfigMap
// names, and PVC names where the name budget allows it.
const volNameHashLen = 16

// mountVolumeName returns a deterministic Kubernetes-DNS-safe Volume.Name
// unique per (hash, volume). Used by all backends so the same
// (hash, vol) pair is addressable by the same name in webhook patches.
// Bounded ≤ 63 chars (RFC1123 label) by truncating the hash prefix.
//
// stripNvSnapPrefix matters when a *re-capture* sees a pod whose volumes
// were themselves injected by a prior restore — their names already
// start with `nvsnap-<hash>-`. Naively wrapping again produces 70+ char
// names that K8s rejects (caught 2026-06-05 on the DeepSeek-V4-Flash
// warm-recapture bench on GCP-H100-a). Strip any prior nvsnap-prefix
// so the final form is always `nvsnap-<this-hash>-<short-name>`.
func mountVolumeName(hash string, vol VolumeMeta) string {
	short := hash
	if len(short) > volNameHashLen {
		short = short[:volNameHashLen]
	}
	return "nvsnap-" + short + "-" + sanitizeName(stripNvSnapPrefix(vol.Name))
}

// stripNvSnapPrefix peels a leading `nvsnap-<16hex>-` (current scheme) or
// `nvsnap-cache-<16hex>-` (legacy scheme, pre nvsnap#88) off vol.Name so
// re-injecting an already-injected volume name doesn't pyramid past
// the 63-char K8s limit. Returns s unchanged when no prefix matches.
func stripNvSnapPrefix(s string) string {
	for _, prefix := range []string{"nvsnap-cache-", "nvsnap-"} {
		if !strings.HasPrefix(s, prefix) {
			continue
		}
		rest := s[len(prefix):]
		// Expect <16hex>- next; tolerate longer hex too.
		dash := strings.IndexByte(rest, '-')
		if dash < 4 { // need at least a few hex chars to look like a hash
			continue
		}
		isHex := true
		for i := 0; i < dash; i++ {
			c := rest[i]
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				isHex = false
				break
			}
		}
		if !isHex {
			continue
		}
		return rest[dash+1:]
	}
	return s
}

// sanitizeName replaces non-DNS-safe chars in a volume name. K8s
// pod.spec.volumes[].name must match RFC1123 label rules: alphanumeric
// or '-', start/end alphanumeric, max 63 chars.
func sanitizeName(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			out = append(out, c)
		case c >= 'A' && c <= 'Z':
			out = append(out, c+'a'-'A')
		default:
			out = append(out, '-')
		}
	}
	// Trim leading/trailing '-' to stay RFC1123-compliant.
	for len(out) > 0 && out[0] == '-' {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == '-' {
		out = out[:len(out)-1]
	}
	if len(out) == 0 {
		return "v"
	}
	return string(out)
}

// Delete removes the stored capture tree for hash from the local root.
func (l *Local) Delete(_ context.Context, hash string) error {
	dir := filepath.Join(l.root, hash)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	return os.RemoveAll(dir)
}

// readManifest reads and decodes a manifest.json file. Returns ErrNotFound
// if the file is missing.
func readManifest(path string) (Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Manifest{}, ErrNotFound
		}
		return Manifest{}, err
	}
	defer func() { _ = f.Close() }()
	var m Manifest
	if err := json.NewDecoder(f).Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	return m, nil
}

func writeManifest(path string, m Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// copyTree recursively copies src into dst, preserving file mode, mtime,
// symlinks (without following), and reports total bytes + file count.
// Uses io.Copy for simplicity; a parallel walker is a future optimization
// for large trees (see #88's perf targets).
func copyTree(ctx context.Context, src, dst string) (totalBytes, totalFiles int64, err error) {
	err = filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dst, rel)
		if rel == "." {
			return os.MkdirAll(dstPath, info.Mode().Perm())
		}

		switch {
		case info.IsDir():
			if err := os.MkdirAll(dstPath, info.Mode().Perm()); err != nil {
				return err
			}
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_ = os.Remove(dstPath)
			if err := os.Symlink(target, dstPath); err != nil {
				return err
			}
		default:
			n, err := copyRegular(path, dstPath, info.Mode().Perm())
			if err != nil {
				return err
			}
			totalBytes += n
			totalFiles++
		}
		return nil
	})
	return totalBytes, totalFiles, err
}

func copyRegular(src, dst string, mode os.FileMode) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return 0, err
	}
	defer func() { _ = out.Close() }()
	return io.Copy(out, in)
}
