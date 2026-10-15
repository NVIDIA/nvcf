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

// Agent-side Copier (v0.0.51) for the L2 mount-holder writer path.
//
// PerCapturePVCBackend used to spawn a writer Job in the source pod's
// namespace that ran `agent capture-write` to copy source rootfs into
// the rwx PVC. That Job ran in nvcf-backend, which enforces Kyverno
// policies (runAsNonRoot, no add capabilities, readOnlyRootFilesystem)
// — the Job can't traverse /host/proc/1/root and EACCES'd on lstat.
//
// v0.0.51 splits the work: the backend creates a tiny mount-holder
// pod (Kyverno-compliant, does no I/O) that triggers kubelet to mount
// the rwx PVC, then calls into this Copier — running inside the nvsnap-
// agent DaemonSet pod, which is privileged + has /host mounted — to
// stream source bytes directly into the PV mount path.
//
// Single bandwidth pass. No NVMe staging hop. No privileged pod in
// the source namespace.

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/treecopy"
)

// AgentCopier implements checkpointstore.Copier for the in-process
// agent. It reuses the treecopy package the writer Job used to invoke
// (so file-mode + ownership + excludes semantics are identical), but
// runs in the agent's process — no env-var marshaling, no subprocess
// hop.
type AgentCopier struct { //nolint:revive // exported name kept for API stability
	// HostRoot is the agent's view of the node's root filesystem,
	// typically "/host" (from the DaemonSet's `mountPath: /host`).
	// Each CaptureSource.SrcPath is joined under this.
	HostRoot string

	// Log is used per-source for progress + summary lines.
	Log *logrus.Entry
}

// NewAgentCopier constructs an AgentCopier. hostRoot defaults to
// "/host" when empty.
func NewAgentCopier(hostRoot string, log *logrus.Entry) *AgentCopier {
	if hostRoot == "" {
		hostRoot = "/host"
	}
	if log == nil {
		log = logrus.NewEntry(logrus.StandardLogger())
	}
	return &AgentCopier{HostRoot: hostRoot, Log: log}
}

// Copy implements checkpointstore.Copier. Iterates sources; for each
// rootfs/criu source, joins HostRoot + SrcPath as the read path and
// destRoot + DstSubpath as the write path, then delegates to
// treecopy.Copier. Returns aggregate (bytes, files) across all
// sources; on first error, returns the partial totals and the error
// (caller is responsible for cleaning up the rwx PVC + holder pod).
func (c *AgentCopier) Copy(ctx context.Context, destRoot string, sources []checkpointstore.CaptureSource) (bytesCopied, filesCopied int64, err error) {
	if destRoot == "" {
		return 0, 0, fmt.Errorf("AgentCopier.Copy: empty destRoot")
	}
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		return 0, 0, fmt.Errorf("mkdir destRoot %q: %w", destRoot, err)
	}
	var totalBytes, totalFiles int64
	for _, src := range sources {
		kind := src.Kind
		if kind == "" {
			kind = checkpointstore.SourceKindRootfs
		}
		fullDst := filepath.Join(destRoot, src.DstSubpath)
		if err := os.MkdirAll(filepath.Dir(fullDst), 0o755); err != nil {
			return totalBytes, totalFiles, fmt.Errorf("mkdir parent of %q: %w", fullDst, err)
		}
		switch kind {
		case checkpointstore.SourceKindRootfs, checkpointstore.SourceKindCRIU:
			fullSrc := filepath.Join(c.HostRoot, src.SrcPath)
			tc := treecopy.NewCopier(src.Excludes, c.Log.WithFields(logrus.Fields{
				"kind":   string(kind),
				"source": src.DstSubpath,
			}))
			bytes, files, err := tc.Copy(ctx, fullSrc, fullDst)
			if err != nil {
				return totalBytes, totalFiles, fmt.Errorf("%s copy %s → %s: %w", kind, fullSrc, fullDst, err)
			}
			c.Log.WithFields(logrus.Fields{
				"kind":     string(kind),
				"src":      src.SrcPath,
				"dst":      src.DstSubpath,
				"size_mib": bytes / 1024 / 1024,
				"files":    files,
			}).Info("source copied")
			totalBytes += bytes
			totalFiles += files
		default:
			return totalBytes, totalFiles, fmt.Errorf("AgentCopier: unknown source kind %q (allowed: rootfs, criu)", kind)
		}
	}
	c.Log.WithFields(logrus.Fields{
		"total_bytes": totalBytes,
		"total_files": totalFiles,
		"sources":     len(sources),
	}).Info("AgentCopier.Copy complete")
	return totalBytes, totalFiles, nil
}

// Compile-time check that AgentCopier satisfies the interface.
var _ checkpointstore.Copier = (*AgentCopier)(nil)
