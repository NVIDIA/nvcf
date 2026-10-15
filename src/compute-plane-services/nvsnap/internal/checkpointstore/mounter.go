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

	corev1 "k8s.io/api/core/v1"
)

// PodMount is the bundle a Mounter returns for the webhook to inject into
// a customer pod. The webhook adds Volume to pod.spec.volumes, attaches
// VolumeMount to the engine container, and prepends InitContainers to
// pod.spec.initContainers.
//
// The MountPath inside VolumeMount is set by the caller (it's the engine's
// expected cache path, e.g. "/opt/nim/.cache" or "/root/.cache/huggingface").
// Volume.Name and VolumeMount.Name must match.
//
// InitContainers is non-empty for prefetch-style backends (e.g. GCS
// download-then-bind-mount). When non-empty, each init container is
// expected to mount Volume itself (via the same Volume.Name) and write
// the prefetched contents to it.
type PodMount struct {
	Volume         corev1.Volume
	VolumeMount    corev1.VolumeMount
	InitContainers []corev1.Container
}

// Mounter renders the pod-yaml fragments needed to mount a single named
// volume from a stored capture into a customer container. Different
// backends emit different volume types (hostPath subPath, PVC + subPath,
// gcsfuse CSI inline, etc.), so the webhook can stay backend-agnostic.
//
// vol identifies which slice of the capture to surface — typically
// taken from manifest.Volumes. The implementation is responsible for
// mapping vol.Name to its on-disk location (e.g. "volumes/<name>/" or
// "rootfs/") and exposing exactly that slice at vol.MountPath inside
// the pod.
//
// Mount returns ErrNotFound if no capture exists for hash. Implementations
// should make Mount cheap — it's called on the admission path.
type Mounter interface {
	Mount(ctx context.Context, hash string, vol VolumeMeta) (PodMount, error)
}

// Backend combines Store (capture/restore from the agent's POV) and
// Mounter (pod-volume rendering for the webhook). A single concrete
// backend implementation typically produces both: e.g. the GPD-ROX
// backend writes to a PVC during Put and references the same PVC by
// name during Mount.
//
// Wiring code constructs one Backend at startup based on cluster
// configuration and passes it to both the agent and the webhook.
//
// Existing implementations:
//   - Local — node-local directory or shared RWX mount; emits hostPath
//     volumes. Same-node restores only (use a multi-node-aware backend
//     for fan-out across nodes).
//
// Planned (#TBD):
//   - GPDROX  — one PVC per capture, accessModes [RWO, ROX]; agent
//     spawns a one-shot capture Job, webhook references the
//     PVC by name.
//   - GCSFuse — GCS-backed Store + gcsfuse CSI inline-volume Mounter.
//   - Filestore — when the GCP API is enabled in the cluster's project,
//     this is just Local pointed at a Filestore RWX mount.
type Backend interface {
	Store
	Mounter
}
