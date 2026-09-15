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

// Capture-method classification for the Mutate dispatch.
//
// Rootfs captures and CRIU dumps restore through different mechanisms:
//
//   - CRIU dump  → L2 per-capture PVC fast path (read-only rox mount at
//     /nvsnap-checkpoint + restore-entrypoint). See l2_mount.go.
//   - Rootfs tree → L1 overlay reinjection in buildPatches: a writable
//     per-pod OverlayFS over the read-only captured tree, with the
//     captured bytes cascade-fetched to the restore node by the agent.
//     This is the SAME path the test scripts exercise; NVCF restores
//     reuse it rather than a bespoke rootfs-on-PVC mount (a RO PVC can't
//     accept the engine's writes into its warmed cache/model dirs).
//
// The Mutate dispatch picks the path off the authoritative CaptureMethod
// stamped at capture time (v0.0.56), falling back to manifestIsRootfs
// only for pre-v0.0.56 manifests that predate the field.

package webhook

import (
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// manifestIsRootfs reports whether a capture manifest describes a
// rootfs (filesystem-tree) capture vs a CRIU process dump. Rootfs
// captures carry RootfsExtractPaths and/or a Volume of Type "rootfs".
func manifestIsRootfs(m checkpointstore.Manifest) bool {
	if len(m.RootfsExtractPaths) > 0 {
		return true
	}
	for _, v := range m.Volumes {
		if v.Type == "rootfs" {
			return true
		}
	}
	return false
}
