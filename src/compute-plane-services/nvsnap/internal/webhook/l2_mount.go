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

// L2 per-capture PVC mount path for the mutating webhook (nvsnap#63).
//
// When the nvsnap-server-driven L2 promote has succeeded for a hash
// (rox-<short-hash> PVC is Bound), the webhook injects:
//
//   - a Volume on pod.spec.volumes referencing the PVC (readOnly)
//   - a VolumeMount on the main container at L2MountPath (readOnly)
//   - CHECKPOINT_PATH env var pointing at L2MountPath
//
// And explicitly does NOT inject a nodeAffinity constraint — the PVC
// is RWX so the K8s scheduler can place the pod on any node.
//
// The L1 hostPath + nodeAffinity path stays as the fallback (called
// from Mutate when this returns ErrNotFound).
//
// Design: docs/L2-PVC-CRIU-DESIGN.md §"Restore-side resolver".

package webhook

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// tryL2Mount asks the L2 backend for a PodMount and translates it to
// JSON Patches against the pod spec.
//
// Returns (patches, nil) when the L2 mount applied; the caller emits
// these and returns to the admission controller.
// Returns (nil, ErrNotFound) when rox-<hash> isn't Bound — caller falls
// through to L1.
// Any other error returned is wrapped; caller logs + falls through.
func (m *Mutator) tryL2Mount(ctx context.Context, pod *corev1.Pod, hash string) ([]PatchOp, error) {
	mountPath := m.L2MountPath
	if mountPath == "" {
		mountPath = "/nvsnap-checkpoint"
	}
	pm, err := m.L2Backend.Mount(ctx, hash, checkpointstore.VolumeMeta{
		Name:      "nvsnap-checkpoint",
		MountPath: mountPath,
		Type:      "rootfs", // semantic placeholder — L2 PVC carries the whole dump tree
		// Namespace scopes the rox-<hash> PVC lookup to the consuming
		// pod's namespace. K8s requires same-namespace mounting and
		// L2 creates each rox PVC in the source pod's namespace
		// (nvsnap#82), so this MUST match the pod's namespace or the
		// mount will fail at admission.
		Namespace: pod.Namespace,
	})
	if err != nil {
		return nil, err
	}

	if m.MainContainer < 0 || m.MainContainer >= len(pod.Spec.Containers) {
		return nil, fmt.Errorf("MainContainer index %d out of range (have %d containers)",
			m.MainContainer, len(pod.Spec.Containers))
	}

	patches := []PatchOp{}

	// 1. Add the Volume.
	if pod.Spec.Volumes == nil {
		patches = append(patches, PatchOp{
			Op:    "add",
			Path:  "/spec/volumes",
			Value: []corev1.Volume{pm.Volume},
		})
	} else {
		patches = append(patches, PatchOp{
			Op:    "add",
			Path:  "/spec/volumes/-",
			Value: pm.Volume,
		})
	}

	// 2. Add the VolumeMount on the main container.
	mainC := pod.Spec.Containers[m.MainContainer]
	if mainC.VolumeMounts == nil {
		patches = append(patches, PatchOp{
			Op:    "add",
			Path:  fmt.Sprintf("/spec/containers/%d/volumeMounts", m.MainContainer),
			Value: []corev1.VolumeMount{pm.VolumeMount},
		})
	} else {
		patches = append(patches, PatchOp{
			Op:    "add",
			Path:  fmt.Sprintf("/spec/containers/%d/volumeMounts/-", m.MainContainer),
			Value: pm.VolumeMount,
		})
	}

	// 3. Set CHECKPOINT_PATH env var (restore-entrypoint reads it).
	envVar := corev1.EnvVar{Name: "CHECKPOINT_PATH", Value: mountPath}
	if mainC.Env == nil {
		patches = append(patches, PatchOp{
			Op:    "add",
			Path:  fmt.Sprintf("/spec/containers/%d/env", m.MainContainer),
			Value: []corev1.EnvVar{envVar},
		})
	} else {
		// CHECKPOINT_PATH may already be present (operator pre-set).
		// JSON Patch "add" on an existing array index appends; on a
		// named key replaces. Since /env/- always appends, the
		// existing entry stays. restore-entrypoint reads the FIRST
		// CHECKPOINT_PATH it sees, so we prepend by inserting at
		// index 0 — operator's value wins via dedupe by-name in
		// kubelet's env-merge. (Container env semantics: duplicate
		// names → last value wins. So appending makes our value the
		// canonical one if the operator's pre-set was wrong.)
		patches = append(patches, PatchOp{
			Op:    "add",
			Path:  fmt.Sprintf("/spec/containers/%d/env/-", m.MainContainer),
			Value: envVar,
		})
	}

	// 4. Prepend a nvsnap-l2-wait init container (nvsnap#147). This
	// gates the main container on the catalog's pvc_promote_state ==
	// "ready" — without it kubelet would try to mount the rox PVC
	// before the agent's async snap+clone finished, hanging the pod
	// in ContainerCreating (the rox PVC is WaitForFirstConsumer and
	// can't bind until snap+clone completes). The nvsnap-l2-wait
	// container does NOT mount the rox PVC; it talks to nvsnap-server
	// over HTTP and exits 0/1/2/3 per its documented contract.
	//
	// Skipped entirely when L2WaitImage is empty (back-compat for
	// clusters where nvsnap-l2-wait isn't deployed yet). In that mode
	// the rox PVC reference is still injected; kubelet will just
	// wait on the WFC binder until the agent finishes promote.
	if m.L2WaitImage != "" {
		waitC := buildL2WaitContainer(m.L2WaitImage, m.NvSnapServerURL, hash, m.L2WaitTimeout)
		if len(pod.Spec.InitContainers) == 0 {
			patches = append(patches, PatchOp{
				Op:    "add",
				Path:  "/spec/initContainers",
				Value: []corev1.Container{waitC},
			})
		} else {
			// Index 0 = prepend. nvsnap-l2-wait runs BEFORE all other
			// init containers so the rest of the init chain doesn't
			// fight with a not-yet-bound PVC.
			patches = append(patches, PatchOp{
				Op:    "add",
				Path:  "/spec/initContainers/0",
				Value: waitC,
			})
		}
	}

	// Make sure unused vars compile cleanly.
	_ = json.RawMessage(nil)

	// 5. Inject hostPath mounts + command override (nvsnap#147 second
	//    half). Without this, the rox PVC mount has no consumer —
	//    the workload's main container runs its own image entrypoint
	//    (vllm serve / ...) and cold-starts. The mutator wires
	//    /nvsnap + /nvsnap-lib as hostPath mounts (staged by the agent
	//    DaemonSet on every node — see scripts/restore-bundle-init.sh)
	//    and rewrites the main container's command to invoke
	//    /nvsnap/restore-entrypoint.
	//
	//    Pure hostPath — no function-pod-side image pull required.
	//    Function pods in nvcf-backend only have per-pod regcreds for
	//    their tenant registry path; this design keeps nvsnap images
	//    out of that path entirely.
	bundlePatches, err := m.restoreBundleInjectPatches(pod)
	if err != nil {
		return nil, fmt.Errorf("restoreBundleInjectPatches: %w", err)
	}
	patches = append(patches, bundlePatches...)

	return patches, nil
}

// buildL2WaitContainer assembles the nvsnap-l2-wait init container.
// Resources are sized for the binary's actual runtime profile (Go
// HTTP client + small JSON decoder; sub-50 MB RSS in steady state).
// Limits are 4x requests to avoid OOM on slow GC under high load.
// The webhook factory keeps this in one place so test assertions
// can pin the exact shape without scattering literals.
func buildL2WaitContainer(image, serverURL, hash, timeout string) corev1.Container {
	env := []corev1.EnvVar{
		{Name: "NVSNAP_SERVER_URL", Value: serverURL},
		{Name: "NVSNAP_CHECKPOINT_HASH", Value: hash},
	}
	if timeout != "" {
		env = append(env, corev1.EnvVar{Name: "NVSNAP_WAIT_TIMEOUT", Value: timeout})
	}
	return corev1.Container{
		Name:  "nvsnap-l2-wait",
		Image: image,
		Env:   env,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("16Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
		},
		ImagePullPolicy: corev1.PullIfNotPresent,
	}
}
