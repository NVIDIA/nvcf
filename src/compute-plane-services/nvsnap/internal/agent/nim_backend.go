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

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BackendKind classifies the inference backend baked into a container image.
//
// We need this because cuda-checkpoint does not serialize the host-pinned
// memory registration list (see nim_cudahost_unregister_bug memory entry).
// Workloads whose teardown path calls cudaHostUnregister on a buffer that was
// registered before checkpoint abort post-restore. The confirmed failing
// class is Triton + Riva NIMs (whisper-large-v3 + gemma-4-31b NIM repro on
// 2026-06-08); vLLM-backed NIMs (nim-llama-8b) pass.
type BackendKind string

// Backend kinds identified by DetectBackend for NIM workloads.
const (
	BackendRiva    BackendKind = "riva"
	BackendTriton  BackendKind = "triton"
	BackendVLLM    BackendKind = "vllm"
	BackendUnknown BackendKind = "unknown"
)

// UseRootfsCapture reports whether this backend must be routed to the rootfs
// + OverlayFS capture path instead of CRIU + cuda-checkpoint.
//
// Riva and Triton are hard-required: cuda-checkpoint does not serialize their
// host-pinned memory registration list and they abort post-restore. Other
// backends may still be routed to rootfs by the global default (see
// RootfsIsDefault) — that gate is checked separately by callers because it
// can be overridden cluster-wide without touching per-backend semantics.
func (b BackendKind) UseRootfsCapture() bool {
	return b == BackendRiva || b == BackendTriton
}

// RootfsIsDefault reports whether the agent should default ALL backends to
// the rootfs + OverlayFS capture path, falling back to CRIU + cuda-checkpoint
// only when explicitly opted in.
//
// v0.0.48: rootfs is now the default. The CRIU + cuda-checkpoint path stays
// in the codebase (no callers removed) and is reachable cluster-wide by
// setting NVSNAP_DEFAULT_CAPTURE_PATH=criu on the agent DaemonSet.
//
// Empty / unset env → rootfs. "criu" (case-insensitive) → CRIU. Anything
// else also → rootfs (fail-safe to the new default).
func RootfsIsDefault() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("NVSNAP_DEFAULT_CAPTURE_PATH")))
	return v != "criu"
}

// Per-request capture-path override values (CheckpointRequest.CapturePath).
const (
	capturePathAuto   = ""       // cluster default (RootfsIsDefault)
	capturePathCRIU   = "criu"   // force CRIU + cuda-checkpoint
	capturePathRootfs = "rootfs" // force rootfs + OverlayFS
)

// resolveCheckpointRedirect decides whether a /v1/checkpoint request runs via
// CRIU or is redirected to the rootfs path, honoring an explicit per-request
// override (requested) on top of the detected backend and the cluster default.
//
// Precedence:
//   - invalid `requested` → error.
//   - hard backend incapability (Riva/Triton): CRIU physically aborts there,
//     so an explicit "criu" → error (caller asked for the impossible); auto /
//     "rootfs" → redirect.
//   - explicit "rootfs" → redirect (always honored).
//   - cluster default is rootfs → redirect, UNLESS the caller explicitly chose
//     "criu" (that's the override — it re-enables CRIU on a capable cell even
//     when the cluster defaults to rootfs).
//   - otherwise → run CRIU.
//
// The OTHER hard incapability — multi-GPU — is enforced downstream after GPU
// discovery (checkpoint.go: distinctGPUs > 1 returns an error), so an explicit
// "criu" on a multi-GPU pod still fails loudly rather than silently degrading.
func resolveCheckpointRedirect(requested string, backend BackendKind, rootfsDefault bool) (redirect bool, err error) {
	switch requested {
	case capturePathAuto, capturePathCRIU, capturePathRootfs:
	default:
		return false, fmt.Errorf("invalid capturePath %q (want %q, %q, or \"\")", requested, capturePathCRIU, capturePathRootfs)
	}

	if backend.UseRootfsCapture() {
		if requested == capturePathCRIU {
			return false, fmt.Errorf("criu capture requested but unsupported: backend %q requires the rootfs path (cuda-checkpoint can't serialize its host-pinned memory registration list)", backend)
		}
		return true, nil
	}

	if requested == capturePathRootfs {
		return true, nil
	}
	if rootfsDefault && requested != capturePathCRIU {
		return true, nil
	}
	return false, nil
}

// DetectBackend classifies a running container by inspecting its rootfs via
// /proc/<pid>/root. pid must be the in-host PID of any process inside the
// container. rootfsOverride lets tests point detection at a fixture tree; it
// is "" in production.
//
// We inspect the live filesystem rather than regex-matching the image name
// because NGC re-namespaces NIMs occasionally (nvidia/* vs google/* vs
// meta/*) and customers mirror to private registries. The filesystem layout
// is set by the NIM base image and is stable across re-namings.
func DetectBackend(pid int, rootfsOverride string) BackendKind {
	root := rootfsOverride
	if root == "" {
		if pid <= 0 {
			return BackendUnknown
		}
		root = fmt.Sprintf("/proc/%d/root", pid)
	}

	// Riva ASR/TTS NIMs ship the riva tree at /opt/riva/.
	if st, err := os.Stat(filepath.Join(root, "opt", "riva")); err == nil && st.IsDir() {
		return BackendRiva
	}

	// For NIMs that aren't Riva, the entrypoint script names the backend.
	// nvcr.io/nim/* base images put it at /opt/nim/start_server.sh. We
	// search for a tritonserver invocation first because some NIM images
	// import vllm transitively even when Triton is the actual server.
	if data, err := os.ReadFile(filepath.Join(root, "opt", "nim", "start_server.sh")); err == nil {
		s := string(data)
		if strings.Contains(s, "tritonserver") {
			return BackendTriton
		}
		if strings.Contains(s, "vllm") || strings.Contains(s, "python -m vllm") {
			return BackendVLLM
		}
	}

	return BackendUnknown
}

// BackendRedirectError signals that a checkpoint request was rejected because
// the workload's backend requires the rootfs capture path. The HTTP layer
// translates this into a 422 with a structured body so the caller can
// re-issue the capture against the rootfsonly watcher.
type BackendRedirectError struct {
	Backend BackendKind
}

func (e *BackendRedirectError) Error() string {
	return fmt.Sprintf("backend %s requires rootfs capture path (cuda-checkpoint does not serialize host-pinned memory)", e.Backend)
}
