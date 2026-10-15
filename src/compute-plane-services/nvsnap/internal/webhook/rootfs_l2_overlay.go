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

// Rootfs restore from the L2 per-capture rox PVC, WRITABLE + FAN-OUT.
//
// The rox-<hash> PVC is ReadOnlyMany (Hyperdisk-ML), so the SAME PVC
// mounts into N restore pods across N nodes simultaneously — zero-copy
// fan-out (the "scale-out hero"). But a rootfs restore's engine WRITES
// into its warmed cache/model dirs at startup (NIM/Riva extracts the
// model tarball into /data/models, rewrites the NGC filemap), and a RO
// PVC can't take those writes (EROFS).
//
// Fix: per-pod OverlayFS — the rox PVC subtree as the read-only
// lowerdir, a per-pod emptyDir as the writable upperdir. Reads come warm
// from the shared rox; the engine's writes land in the pod-local upper.
// We get BOTH fan-out (shared rox, no per-node copy, no node pin) AND
// writability.
//
// The overlay mount needs CAP_SYS_ADMIN, which a webhook can't do — so a
// privileged init container (`nvsnap-overlay-mount`) performs the mounts
// inside the pod and they propagate to the main container via a shared
// emptyDir mounted Bidirectional. Layout inside the pod:
//
//	/nvsnap-rox/      <- rox PVC (RO)            : rootfs/<p>, volumes/<name>
//	/nvsnap-scratch/  <- emptyDir (RW)           : upper/<p>, work/<p>
//	/nvsnap-merged/   <- emptyDir (Bidirectional): the overlay mountpoints
//
// For each captured path the init container runs
//
//	mount -t overlay overlay \
//	  -o lowerdir=/nvsnap-rox/<sub>,upperdir=/nvsnap-scratch/upper/<p>,workdir=/nvsnap-scratch/work/<p> \
//	  /nvsnap-merged/<p>
//
// and the main container mounts /nvsnap-merged subPath=<p> (writable) at
// the original path. The init container completes before the main
// container starts, so each subPath bind captures a live overlay mount.
//
// NVCA admits privileged function pods and GCP-H100-a has no Kyverno,
// so the privileged init container + Bidirectional propagation is
// admissible there. This path is tried first for rootfs captures; on
// ErrNotFound (rox not Bound) it falls through to the L1 host-overlay
// path (buildPatches), which pins to the capture node.

package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"go.opentelemetry.io/otel/attribute"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/tracing"
)

// Captured-tree layout inside the rox PVC. The agent's rootfsonly
// orchestrator stages the upperdir under "rootfs/" and each user-data
// volume under "volumes/<name>/"; nvsnap-rootfs-restore reads those
// subdirs of capturedMountPath.
const (
	// Whole-rootfs overlay (B′) injected mount points. The captured tree
	// (rox PVC) mounts RO at capturedMountPath; nvsnap-rootfs-restore reads
	// its "rootfs"/"volumes" subdirs from there. scratch is the per-pod
	// writable overlay upper+work.
	capturedVolumeName = "nvsnap-captured"
	capturedMountPath  = "/nvsnap-captured"
	scratchVolumeName  = "nvsnap-overlay-scratch"
	scratchMountPath   = "/nvsnap-scratch"
)

// volMount mirrors the shim's NVSNAP_ROOTFS_VOLUMES element (cmd/nvsnap-
// rootfs-restore). Captured non-rootfs volumes the shim binds into the
// merged root at their original mount paths.
type volMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
}

// injectVllmLoadStrategy appends --safetensors-load-strategy=prefetch to a
// vLLM restore command so vLLM reads the cached weights from the rox overlay
// in parallel instead of sequentially (vLLM disables auto-prefetch on
// non-network filesystems like overlay). It's a no-op unless argv is a vLLM
// `serve` invocation AND no load-strategy/load-format is already set — never
// override the user's explicit choice, and never touch non-vLLM engines
// (NIM/sglang/trtllm have different/absent equivalents). Returns argv
// unchanged in every other case.
func injectVllmLoadStrategy(argv []string) []string {
	isVllm, hasServe := false, false
	for _, a := range argv {
		base := a
		if i := strings.LastIndex(a, "/"); i >= 0 {
			base = a[i+1:]
		}
		if base == "vllm" {
			isVllm = true
		}
		if a == "serve" {
			hasServe = true
		}
		// Respect any explicit loader choice the workload already made.
		if strings.HasPrefix(a, "--safetensors-load-strategy") || strings.HasPrefix(a, "--load-format") {
			return argv
		}
	}
	if !isVllm || !hasServe {
		return argv
	}
	out := make([]string, len(argv), len(argv)+1)
	copy(out, argv)
	return append(out, "--safetensors-load-strategy=prefetch")
}

// tryL2RootfsOverlay injects the writable-overlay-over-rox-PVC mounts for
// a rootfs capture. Returns (patches, nil) on success, (nil, ErrNotFound)
// when the rox PVC isn't Bound (caller falls back to L1), or a wrapped
// error.
// validateMountPath rejects container mount paths that are unsafe to feed
// to the rootfs-restore shim's overlay options / bind targets. A legitimate
// K8s mountPath is a clean absolute path; anything else (relative, "..",
// or carrying NUL/newline/comma/colon) is refused at admission rather than
// risking a corrupt overlay option string in the privileged shim.
func validateMountPath(p string) error {
	if p == "" {
		return fmt.Errorf("empty path")
	}
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("not absolute")
	}
	if filepath.Clean(p) != p {
		return fmt.Errorf("not a clean path")
	}
	if strings.ContainsAny(p, "\x00\n\r,:") {
		return fmt.Errorf("contains a disallowed character")
	}
	return nil
}

func (m *Mutator) tryL2RootfsOverlay(ctx context.Context, pod *corev1.Pod, hash string, manifest checkpointstore.Manifest) ([]PatchOp, error) {
	ctx, span := tracing.Tracer().Start(ctx, "webhook.rootfs_l2_overlay")
	defer span.End()
	span.SetAttributes(attribute.String("nvsnap.hash", checkpointstore.ShortHash(hash)))
	if m.MainContainer < 0 || m.MainContainer >= len(pod.Spec.Containers) {
		return nil, fmt.Errorf("MainContainer index %d out of range (have %d containers)",
			m.MainContainer, len(pod.Spec.Containers))
	}

	// Resolve the rox PVC Volume via the L2 backend (ErrNotFound = not
	// Bound → caller falls back to L1). We only use the returned Volume's
	// PVC reference; we mount it whole at /nvsnap-rox (RO) ourselves.
	pm, err := m.L2Backend.Mount(ctx, hash, checkpointstore.VolumeMeta{
		Name:      "nvsnap-rox",
		MountPath: "/nvsnap-rox",
		Type:      "rootfs",
		Namespace: pod.Namespace,
	})
	if err != nil {
		return nil, err
	}
	roxVol := pm.Volume
	roxVol.Name = capturedVolumeName
	if roxVol.PersistentVolumeClaim != nil {
		roxVol.PersistentVolumeClaim.ReadOnly = true
	}

	main := pod.Spec.Containers[m.MainContainer]

	// The shim execs the workload's original entrypoint after pivot_root.
	// The ONLY source of truth is the capture-recorded source PID-1 argv
	// (full /proc/1/cmdline: binary + args). We deliberately do NOT fall
	// back to the pod's command/args: an ENTRYPOINT-only image (NIM,
	// whisper, this gpt-oss NVCA pod) carries only args — or nothing — in
	// the Pod spec, so execing those drops the image's entrypoint binary
	// and silently launches the wrong thing (observed: NVSNAP_ORIG_COMMAND
	// = ["--model",...] with no vllm binary). If the capture predates
	// EntryArgv recording, bail with ErrNotFound so Mutate falls through
	// to the L1 per-path path, which runs the image ENTRYPOINT unchanged.
	if len(manifest.EntryArgv) == 0 {
		return nil, fmt.Errorf("rootfs whole-overlay: capture %s has no recorded EntryArgv (re-capture needed): %w",
			checkpointstore.ShortHash(hash), checkpointstore.ErrNotFound)
	}
	// vLLM warm restores read the model from the captured cache on the rox
	// OVERLAY, where vLLM disables its parallel safetensors loader (it only
	// auto-prefetches on NFS/Lustre), so it reads shards SEQUENTIALLY at the
	// disk's single-stream ceiling (~0.57 GiB/s; gpt-oss-120b TP=4 spent 91s).
	// Forcing --safetensors-load-strategy=prefetch parallelizes the read and
	// the same load drops to ~20s (measured, 5x). vLLM itself recommends this
	// flag for non-network filesystems. We append it to the restored argv
	// (the proven mechanism, automatic) when absent. vLLM-only; other engines
	// are left untouched.
	restoreArgv := injectVllmLoadStrategy(manifest.EntryArgv)
	argvJSON, err := json.Marshal(restoreArgv)
	if err != nil {
		return nil, fmt.Errorf("marshal entrypoint argv: %w", err)
	}

	// Captured non-rootfs volumes (emptyDir-shaped) the shim binds into
	// the merged root at their original mount paths. Few (O(1)).
	// The shim mounts via the mount(2) syscall (no shell), but we still
	// reject malformed paths at admission as defense-in-depth: a path that
	// isn't a clean absolute path, or carries a NUL/newline/comma/colon,
	// has no legitimate origin and could corrupt the overlay option string
	// or the JSON env the shim parses (nvsnap#91).
	rootfsVols := make([]volMount, 0, len(manifest.Volumes))
	for _, v := range manifest.Volumes {
		if v.Type == "rootfs" {
			continue // the rootfs upperdir is the overlay lower, not a bind
		}
		if errVP := validateMountPath(v.MountPath); errVP != nil {
			return nil, fmt.Errorf("capture %s: rejecting unsafe captured volume mount path %q: %w",
				checkpointstore.ShortHash(hash), v.MountPath, errVP)
		}
		rootfsVols = append(rootfsVols, volMount{Name: v.Name, MountPath: v.MountPath})
	}
	volsJSON, err := json.Marshal(rootfsVols)
	if err != nil {
		return nil, fmt.Errorf("marshal rootfs volumes: %w", err)
	}

	// Idempotency: if the customer pre-wired the captured mount or the
	// bundle, don't double-inject.
	for _, vm := range main.VolumeMounts {
		if vm.MountPath == capturedMountPath || vm.Name == capturedVolumeName ||
			vm.MountPath == nvsnapToolsMountPath || vm.Name == nvsnapToolsVolumeName {
			return nil, nil
		}
	}

	root := m.HostBundleRoot
	if root == "" {
		root = DefaultHostBundleRoot
	}
	hostPathDir := corev1.HostPathDirectory

	patches := make([]PatchOp, 0, 16)
	if pod.Spec.Volumes == nil {
		patches = append(patches, PatchOp{Op: "add", Path: "/spec/volumes", Value: []any{}})
	}
	if main.VolumeMounts == nil {
		patches = append(patches, PatchOp{
			Op:    "add",
			Path:  fmt.Sprintf("/spec/containers/%d/volumeMounts", m.MainContainer),
			Value: []any{},
		})
	}
	if main.Env == nil {
		patches = append(patches, PatchOp{
			Op:    "add",
			Path:  fmt.Sprintf("/spec/containers/%d/env", m.MainContainer),
			Value: []any{},
		})
	}

	// O(1) Pod footprint, independent of capture path count:
	//   - nvsnap-captured: the rox PVC (RO), the whole captured tree.
	//   - nvsnap-overlay-scratch: per-pod writable upper+work for the overlay.
	//   - nvsnap-tools: hostPath bundle (the nvsnap-rootfs-restore shim binary),
	//     staged on every node by the agent DaemonSet.
	scratch := corev1.Volume{Name: scratchVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}
	toolsVol := corev1.Volume{Name: nvsnapToolsVolumeName, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
		Path: root + "/nvsnap",
		Type: &hostPathDir,
	}}}
	vols := []corev1.Volume{roxVol, scratch, toolsVol}
	for i := range vols {
		patches = append(patches, PatchOp{Op: "add", Path: "/spec/volumes/-", Value: vols[i]})
	}
	for _, vm := range []corev1.VolumeMount{
		{Name: capturedVolumeName, MountPath: capturedMountPath, ReadOnly: true},
		{Name: scratchVolumeName, MountPath: scratchMountPath},
		{Name: nvsnapToolsVolumeName, MountPath: nvsnapToolsMountPath, ReadOnly: true},
	} {
		patches = append(patches, PatchOp{
			Op:    "add",
			Path:  fmt.Sprintf("/spec/containers/%d/volumeMounts/-", m.MainContainer),
			Value: vm,
		})
	}

	// Shim env contract (see cmd/nvsnap-rootfs-restore).
	for _, e := range []corev1.EnvVar{
		{Name: "NVSNAP_CAPTURED_DIR", Value: capturedMountPath},
		{Name: "NVSNAP_SCRATCH_DIR", Value: scratchMountPath},
		{Name: "NVSNAP_ORIG_COMMAND", Value: string(argvJSON)},
		{Name: "NVSNAP_ORIG_CWD", Value: manifest.EntryCwd},
		{Name: "NVSNAP_ROOTFS_VOLUMES", Value: string(volsJSON)},
	} {
		patches = append(patches, appendEnv(m.MainContainer, e))
	}

	// Override command to the shim; clear any args (they're carried in
	// NVSNAP_ORIG_COMMAND and re-applied by the shim after pivot_root).
	cmdOp := "replace"
	if main.Command == nil {
		cmdOp = "add"
	}
	patches = append(patches, PatchOp{
		Op:    cmdOp,
		Path:  fmt.Sprintf("/spec/containers/%d/command", m.MainContainer),
		Value: []string{nvsnapToolsMountPath + "/nvsnap-rootfs-restore"},
	})
	if main.Args != nil {
		patches = append(patches, PatchOp{
			Op:   "remove",
			Path: fmt.Sprintf("/spec/containers/%d/args", m.MainContainer),
		})
	}

	// securityContext: runAsUser=0 + rootfsOverlayCaps (DAC_OVERRIDE,
	// CHOWN, FOWNER, SYS_ADMIN), NOT privileged. The shim needs SYS_ADMIN
	// for the overlay mount + rbind + pivot_root; privileged would expose
	// all host /dev/nvidiaN nodes and break single-GPU isolation, so we
	// grant the targeted caps instead (isolation preserved, v0.0.66/67).
	patches = append(patches, securityContextPatches(m.MainContainer, main.SecurityContext, false, rootfsOverlayCaps)...)

	// seccomp + AppArmor Unconfined on the workload: the shim calls
	// mount(2)/pivot_root, which the RuntimeDefault seccomp profile (the
	// cluster's seccompDefault) and the COS default AppArmor profile
	// block even WITH CAP_SYS_ADMIN. A privileged container gets both
	// unconfined automatically; we grant them explicitly so the shim's
	// mounts work while everything else (device isolation) stays intact.
	// securityContextPatches above already created the container
	// securityContext, so adding the field here is safe.
	patches = append(patches, PatchOp{
		Op:    "add",
		Path:  fmt.Sprintf("/spec/containers/%d/securityContext/seccompProfile", m.MainContainer),
		Value: corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined},
	})
	// AppArmor is a pod annotation keyed by container name; "/" in the
	// key is escaped as ~1 in a JSON Patch path. The pod already has
	// annotations (the restore-from stamp), so "add" the key.
	apparmorKey := strings.ReplaceAll("container.apparmor.security.beta.kubernetes.io/"+main.Name, "/", "~1")
	patches = append(patches, PatchOp{
		Op:    "add",
		Path:  "/metadata/annotations/" + apparmorKey,
		Value: "unconfined",
	})

	// NO nodeAffinity pin and NO topology-spread: rox is ReadOnlyMany so
	// the scheduler bin-packs across nodes; each pod uses its own assigned
	// GPU (v0.0.66/67), so dense packing is correct and leaves whole nodes
	// free for large multi-GPU functions.
	return patches, nil
}
