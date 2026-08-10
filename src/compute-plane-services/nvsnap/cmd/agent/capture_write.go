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

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/treecopy"
)

// capturePlan mirrors the type defined in the gpdrox backend. Duplicated
// here to avoid pulling the gpdrox package into the subcommand path
// (would create a circular dependency: cmd/agent → internal/agent →
// internal/checkpointstore/gpdrox → ... ). Schema is stable and small;
// any change must be made in lockstep.
type capturePlan struct {
	Hash     string                          `json:"hash"`
	HostRoot string                          `json:"host_root"`
	DestRoot string                          `json:"dest_root"`
	Sources  []checkpointstore.CaptureSource `json:"sources"`
}

// sourceResult is the per-source counter we emit on stdout for the
// agent to ingest from Job pod logs.
type sourceResult struct {
	Kind   checkpointstore.SourceKind `json:"kind"`
	Source string                     `json:"source"`
	Dst    string                     `json:"dst"`
	Bytes  int64                      `json:"bytes"`
	Files  int64                      `json:"files"`
}

// runCaptureWrite is the unified writer subcommand. Invoked when the
// agent binary is started with `capture-copy` (legacy name kept for
// backward-compat with v0.17.x Jobs already in flight). Decodes the
// plan, dispatches per-source by Kind:
//
//   - SourceKindRootfs (default for empty Kind): treecopy.Copier from
//     HostRoot/Source.SrcPath into DestRoot/Source.DstSubpath, applying
//     mountinfo Excludes.
//   - SourceKindCRIU: shell out to `criu dump` against Source.CRIUTargetPID,
//     writing image files into DestRoot/Source.DstSubpath. Externals are
//     passed through verbatim.
//
// Per-source counts are emitted to stdout as one JSON line. Fatal errors
// exit non-zero so the K8s Job status reflects them.
func runCaptureWrite(ctx context.Context, log logrus.FieldLogger) error {
	encoded := os.Getenv("NVSNAP_CAPTURE_PLAN")
	if encoded == "" {
		return errors.New("NVSNAP_CAPTURE_PLAN env var is empty")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("decode NVSNAP_CAPTURE_PLAN: %w", err)
	}
	var plan capturePlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		return fmt.Errorf("unmarshal plan: %w", err)
	}
	if plan.DestRoot == "" {
		return errors.New("plan must specify dest_root")
	}
	if len(plan.Sources) == 0 {
		return errors.New("plan has no sources")
	}
	// HostRoot is required for rootfs sources but optional when every
	// source is CRIU (which doesn't read host paths). Per-kind validation below.
	if err := os.MkdirAll(plan.DestRoot, 0o755); err != nil {
		return fmt.Errorf("mkdir dest root %q: %w", plan.DestRoot, err)
	}

	results := make([]sourceResult, 0, len(plan.Sources))

	var totalBytes, totalFiles int64
	for _, src := range plan.Sources {
		kind := src.Kind
		if kind == "" {
			kind = checkpointstore.SourceKindRootfs
		}
		fullDst := filepath.Join(plan.DestRoot, src.DstSubpath)
		if err := os.MkdirAll(filepath.Dir(fullDst), 0o755); err != nil {
			return fmt.Errorf("mkdir parent of %q: %w", fullDst, err)
		}

		switch kind {
		case checkpointstore.SourceKindRootfs, checkpointstore.SourceKindCRIU:
			// rootfs and criu share the writer machinery: both are
			// "copy a host directory tree into the PVC mount." The
			// only difference is that rootfs may apply mountinfo
			// Excludes (bind-mounts to skip); CRIU dumps are clean
			// (CRIU never writes anything outside the dump dir) so
			// Excludes is always empty for that kind.
			//
			// Both require HostRoot (the writer Job mounts the host
			// filesystem at HostRoot to read SrcPath, which is given
			// as an absolute host path).
			if plan.HostRoot == "" {
				return fmt.Errorf("%s source requires plan.host_root", kind)
			}
			fullSrc := filepath.Join(plan.HostRoot, src.SrcPath)
			copier := treecopy.NewCopier(src.Excludes, log.WithField("source", src.DstSubpath))
			bytes, files, copyErr := copier.Copy(ctx, fullSrc, fullDst)
			if copyErr != nil {
				return fmt.Errorf("%s copy %s → %s: %w", kind, fullSrc, fullDst, copyErr)
			}
			log.WithFields(logrus.Fields{
				"kind":     string(kind),
				"src":      src.SrcPath,
				"dst":      src.DstSubpath,
				"size_mib": bytes / 1024 / 1024,
				"files":    files,
			}).Info("source copied")
			results = append(results, sourceResult{
				Kind: kind, Source: src.SrcPath, Dst: src.DstSubpath, Bytes: bytes, Files: files,
			})
			totalBytes += bytes
			totalFiles += files

		default:
			return fmt.Errorf("unknown source kind %q (allowed: rootfs, criu)", kind)
		}
	}

	out := struct {
		Hash       string         `json:"hash"`
		TotalBytes int64          `json:"total_bytes"`
		TotalFiles int64          `json:"total_files"`
		Sources    []sourceResult `json:"sources"`
	}{
		Hash:       plan.Hash,
		TotalBytes: totalBytes,
		TotalFiles: totalFiles,
		Sources:    results,
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		return fmt.Errorf("emit summary: %w", err)
	}
	return nil
}
