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

// cachedir mode (the "ember" cache-reuse path) — capture/restore of a
// single canonical cache+model directory, mounted directly with NO
// overlayfs.
//
// Idea: redirect every JIT/compile cache an engine writes (torch
// inductor, triton, deep_gemm, sgl_kernel, flashinfer, NIM TRT engines)
// PLUS the model (HF_HOME) into ONE canonical path — m.CacheDir, e.g.
// "/opt/nvsnap" — set IDENTICALLY at capture and restore. Path
// consistency is what lets the engine reuse prebuilt kernels instead of
// recompiling (DeepSeek graph capture 379s→38s when the path matches).
//
//   - Capture pod (no restore-from): inject the env vars + an emptyDir
//     at m.CacheDir. The engine populates it; the agent (cachedir mode)
//     captures ONLY that dir as the rox PVC root.
//   - Restore pod (CaptureMethod=="cachedir"): mount the rox read-only
//     at m.CacheDir (model stays RO — the big part, never copied), shadow
//     the cache subtree <CacheDir>/cache with a writable emptyDir seeded
//     by a nvsnap-seed-cache init container (cp from the rox), prewarm the
//     rox tree into page cache with a nvsnap-prewarm init container, and run
//     the pod's own command untouched. No shim: a cachedir restore is a warm
//     cold-start of THIS pod, so its entrypoint runs exactly as authored.
//
// Why the writable cache shadow (ember rule #3, verified): engines write
// JIT/log/lock files into the cache at startup — flashinfer opens
// $HOME/.cache/flashinfer/<ver>/flashinfer_jit.log at import, which
// hard-crashes (OSError [Errno 30] Read-only file system) on a pure-RO
// mount. "Whatever path HOME points to must be writable." The cache is
// small (SGLang JIT ~56 MB, NIM ~1.5 GB) so the seed copy is cheap; the
// model (<CacheDir>/model, HF_HOME) is large and read-only safe (rule #4)
// so it stays RO-mounted from the rox. No overlayfs anywhere — the
// writable layer is a plain emptyDir shadow-mount.

package webhook

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

// appendEnv is the env-add helper. Always appends via "/env/-" because
// every call site here is downstream of tryL2Mount, which has already
// added CHECKPOINT_PATH and thus guaranteed the slice exists. The
// defensive bootstrap (Op=add against the array path) lives in
// tryL2Mount itself, not here.
func appendEnv(containerIdx int, e corev1.EnvVar) PatchOp {
	return PatchOp{
		Op:    "add",
		Path:  fmt.Sprintf("/spec/containers/%d/env/-", containerIdx),
		Value: e,
	}
}
