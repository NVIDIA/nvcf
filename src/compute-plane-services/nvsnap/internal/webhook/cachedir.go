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
//     by a nvsnap-seed-cache init container (cp from the rox), then run
//     nvsnap-rootfs-restore in no-overlay mode to prewarm the tree into
//     page cache (same cgroup as the engine) and exec the entrypoint.
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
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"go.opentelemetry.io/otel/attribute"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/tracing"
)

const (
	// cacheDirVolumeName is the rox PVC (restore) / emptyDir (capture)
	// mounted at m.CacheDir.
	cacheDirVolumeName = "nvsnap-cachedir"

	// cacheRWVolumeName is the writable emptyDir that shadows the cache
	// subtree (<CacheDir>/cache) on restore. The rox is RO, but the
	// engine writes JIT/log/lock files into the cache at startup
	// (flashinfer_jit.log → EROFS on a pure-RO mount, verified). A
	// seed-cache init container copies the rox's cache subtree into this
	// emptyDir, then the main container mounts it RW over <CacheDir>/cache
	// — same path as capture (compile-cache reuse) AND writable. The
	// model (<CacheDir>/model) stays RO from the rox (never copied).
	// See ember-cache-env.md "On a shared read-only (ROX) store".
	cacheRWVolumeName = "nvsnap-cache-rw"

	// cacheSeedSrcPath is where the seed-cache init container mounts the
	// rox (RO) to copy the cache subtree from.
	cacheSeedSrcPath = "/nvsnap-cachedir-src"
	// cacheSeedDstPath is where the seed-cache init container mounts the
	// writable emptyDir to copy into.
	cacheSeedDstPath = "/nvsnap-cache-rw"
)

// cacheEnvEntry is one templated env var: a Name and a Value carrying
// {root}/{cache}/{model} placeholders resolved against m.CacheDir.
type cacheEnvEntry struct{ Name, Value string }

// defaultCacheEnvTemplate is the built-in cache+model env set, used when
// no ConfigMap template is mounted (Mutator.CacheEnvFile empty/absent or
// unreadable). Fail-safe: a missing or garbled ConfigMap never breaks
// admission — we silently fall back to this.
//
//   - cache subtree (<root>/cache): writable, seed-copied at restore.
//     HOME is the hammer (~/.cache/* deep_gemm/tvm-ffi/flashinfer/
//     sgl_kernel, ~/.triton, ~/.nv); use HOME not XDG_CACHE_HOME because
//     DeepGEMM hardcodes ~/.cache. The rest are redundant-with-HOME but
//     set for version robustness.
//   - model (<root>/model): large + read-only safe, RO-mounted, never
//     seed-copied. NIM_CACHE_PATH (NIM profile + TRT engines) and HF_HOME
//     share it (NIM writes ngc/, HF writes hub/ — no collision). Keeping
//     them OUT of <root>/cache is what stops the seed-copy from dragging
//     the whole model into the writable emptyDir every restore.
func defaultCacheEnvTemplate() []cacheEnvEntry {
	return []cacheEnvEntry{
		{"HOME", "{cache}"},
		{"TORCHINDUCTOR_CACHE_DIR", "{cache}/torchinductor"},
		{"TRITON_CACHE_DIR", "{cache}/.triton/cache"},
		{"VLLM_CACHE_ROOT", "{cache}/.cache/vllm"},
		{"CUDA_CACHE_PATH", "{cache}/.nv/ComputeCache"},
		{"NIM_CACHE_PATH", "{model}"},
		{"HF_HOME", "{model}"},
	}
}

// parseCacheEnvTemplate parses `NAME=value` lines (blank lines and lines
// starting with # are skipped). Returns nil if no valid entry is found, so
// the caller falls back to the built-in default.
func parseCacheEnvTemplate(data string) []cacheEnvEntry {
	out := make([]cacheEnvEntry, 0, strings.Count(data, "\n")+1)
	for line := range strings.SplitSeq(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			continue
		}
		out = append(out, cacheEnvEntry{Name: k, Value: strings.TrimSpace(v)})
	}
	return out
}

// resolveCacheEnv resolves a template against `root` (m.CacheDir): the
// cache subtree at <root>/cache, the model at <root>/model. Set IDENTICALLY
// at capture and restore — path consistency is the whole point.
func resolveCacheEnv(tmpl []cacheEnvEntry, root string) []corev1.EnvVar {
	rep := strings.NewReplacer(
		"{cache}", filepath.Join(root, "cache"),
		"{model}", filepath.Join(root, "model"),
		"{root}", root,
	)
	out := make([]corev1.EnvVar, 0, len(tmpl))
	for _, e := range tmpl {
		out = append(out, corev1.EnvVar{Name: e.Name, Value: filepath.Clean(rep.Replace(e.Value))})
	}
	return out
}

// cacheDirEnvVars returns the built-in default cache+model env set rooted
// at `root`. Kept as a free function for the no-ConfigMap path and tests;
// the Mutator method cacheEnvVars layers the ConfigMap override on top.
func cacheDirEnvVars(root string) []corev1.EnvVar {
	return resolveCacheEnv(defaultCacheEnvTemplate(), root)
}

// cacheEnvVars resolves the cache+model env set for a CAPTURE pod from the
// mounted ConfigMap template (Mutator.CacheEnvFile) when present, else the
// built-in default. Fail-safe: any read/parse problem → default, never an
// admission failure. The injected result is also (filtered) stamped into
// the manifest at capture as the per-checkpoint single source of truth;
// restore replays from the manifest, never from this file again.
func (m *Mutator) cacheEnvVars(root string) []corev1.EnvVar {
	tmpl := defaultCacheEnvTemplate()
	if m.CacheEnvFile != "" {
		if b, err := os.ReadFile(m.CacheEnvFile); err == nil {
			if parsed := parseCacheEnvTemplate(string(b)); len(parsed) > 0 {
				tmpl = parsed
			}
		}
	}
	return resolveCacheEnv(tmpl, root)
}

// sortedEnvVars converts a stamped CacheEnv map into a name-sorted env
// slice — deterministic patch order for the restore replay.
func sortedEnvVars(env map[string]string) []corev1.EnvVar {
	names := make([]string, 0, len(env))
	for k := range env {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]corev1.EnvVar, 0, len(names))
	for _, k := range names {
		out = append(out, corev1.EnvVar{Name: k, Value: env[k]})
	}
	return out
}

// cacheDirCapturePatches injects the cache/model env vars and an emptyDir
// at m.CacheDir onto a CAPTURE pod (one with no restore-from). The engine
// writes its caches + model there; the agent's cachedir capture grabs the
// whole dir as the rox PVC root. No-op when cachedir mode is off or the
// main-container index is out of range.
func (m *Mutator) cacheDirCapturePatches(pod *corev1.Pod) []PatchOp {
	if m.CacheDir == "" {
		return nil
	}
	// Opt-in only: inject the cachedir capture plumbing into pods explicitly
	// labeled for capture (nvsnap.io/capture: "true"), matching the rootfs
	// capture watcher. Un-labeled pods (system/infra, helm-chart miniservice,
	// anything not meant for capture) are left untouched. See CaptureLabel.
	if pod.Labels[CaptureLabel] != "true" {
		return nil
	}
	if m.MainContainer < 0 || m.MainContainer >= len(pod.Spec.Containers) {
		return nil
	}
	main := pod.Spec.Containers[m.MainContainer]

	// Idempotency: if the cache volume/mount is already present (re-admit,
	// or the workload pre-wired it), don't double-inject.
	for _, vm := range main.VolumeMounts {
		if vm.Name == cacheDirVolumeName || vm.MountPath == m.CacheDir {
			return nil
		}
	}

	patches := make([]PatchOp, 0, 5+len(m.cacheEnvVars(m.CacheDir)))
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

	patches = append(patches, PatchOp{
		Op:   "add",
		Path: "/spec/volumes/-",
		Value: corev1.Volume{
			Name:         cacheDirVolumeName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
	}, PatchOp{
		Op:    "add",
		Path:  fmt.Sprintf("/spec/containers/%d/volumeMounts/-", m.MainContainer),
		Value: corev1.VolumeMount{Name: cacheDirVolumeName, MountPath: m.CacheDir},
	})
	for _, e := range m.cacheEnvVars(m.CacheDir) {
		patches = append(patches, appendEnv(m.MainContainer, e))
	}
	return patches
}

// tryL2CacheDir injects a cachedir RESTORE: the rox PVC mounted
// read-only at m.CacheDir directly (no overlayfs), the cache/model env
// vars set identically to capture, and nvsnap-rootfs-restore as the
// entrypoint in no-overlay mode (prewarm the tree, then exec the
// workload). Returns (patches, nil) on success, (nil, ErrNotFound) when
// the rox isn't Bound (caller falls back to L1) or the capture predates
// EntryArgv recording.
func (m *Mutator) tryL2CacheDir(ctx context.Context, pod *corev1.Pod, hash string, manifest checkpointstore.Manifest) ([]PatchOp, error) {
	ctx, span := tracing.Tracer().Start(ctx, "webhook.cachedir_restore")
	defer span.End()
	span.SetAttributes(attribute.String("nvsnap.hash", checkpointstore.ShortHash(hash)))

	if m.CacheDir == "" {
		return nil, fmt.Errorf("cachedir restore: Mutator.CacheDir not configured")
	}
	if m.MainContainer < 0 || m.MainContainer >= len(pod.Spec.Containers) {
		return nil, fmt.Errorf("MainContainer index %d out of range (have %d containers)",
			m.MainContainer, len(pod.Spec.Containers))
	}
	// The shim execs the workload's recorded source entrypoint after
	// prewarm. Same rule as the overlay path: never fall back to the
	// pod's command/args (ENTRYPOINT-only images carry neither). If the
	// capture predates EntryArgv, fall through to L1.
	if len(manifest.EntryArgv) == 0 {
		return nil, fmt.Errorf("cachedir restore: capture %s has no recorded EntryArgv (re-capture needed): %w",
			checkpointstore.ShortHash(hash), checkpointstore.ErrNotFound)
	}

	// Resolve the rox PVC (ErrNotFound = not Bound → caller falls to L1).
	pm, err := m.L2Backend.Mount(ctx, hash, checkpointstore.VolumeMeta{
		Name:      cacheDirVolumeName,
		MountPath: m.CacheDir,
		Type:      "cachedir",
		Namespace: pod.Namespace,
	})
	if err != nil {
		return nil, err
	}
	roxVol := pm.Volume
	roxVol.Name = cacheDirVolumeName
	if roxVol.PersistentVolumeClaim != nil {
		roxVol.PersistentVolumeClaim.ReadOnly = true
	}

	main := pod.Spec.Containers[m.MainContainer]
	for _, vm := range main.VolumeMounts {
		if vm.Name == cacheDirVolumeName || vm.MountPath == m.CacheDir ||
			vm.Name == nvsnapToolsVolumeName || vm.MountPath == nvsnapToolsMountPath {
			return nil, nil // already wired
		}
	}

	argvJSON, err := json.Marshal(manifest.EntryArgv)
	if err != nil {
		return nil, fmt.Errorf("marshal entrypoint argv: %w", err)
	}

	root := m.HostBundleRoot
	if root == "" {
		root = DefaultHostBundleRoot
	}
	hostPathDir := corev1.HostPathDirectory

	patches := make([]PatchOp, 0, 11+len(manifest.CacheEnv))
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

	// Volumes:
	//   - roxVol: rox PVC (RO) — the captured cache+model tree.
	//   - toolsVol: hostPath bundle (the nvsnap-rootfs-restore shim binary).
	//   - cacheRW: writable emptyDir that shadows <CacheDir>/cache so the
	//     engine's JIT/log/lock writes don't hit EROFS on the RO rox.
	toolsVol := corev1.Volume{Name: nvsnapToolsVolumeName, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
		Path: root + "/nvsnap",
		Type: &hostPathDir,
	}}}
	cacheRWVol := corev1.Volume{Name: cacheRWVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}
	vols := []corev1.Volume{roxVol, toolsVol, cacheRWVol}
	for i := range vols {
		patches = append(patches, PatchOp{Op: "add", Path: "/spec/volumes/-", Value: vols[i]})
	}
	// Main-container mounts. Order matters for nested paths: rox at
	// <CacheDir> first (model RO), then the writable emptyDir shadowing
	// <CacheDir>/cache (the JIT cache, RW). The model under
	// <CacheDir>/model stays read-only from the rox.
	cacheSub := filepath.Join(m.CacheDir, "cache")
	for _, vm := range []corev1.VolumeMount{
		{Name: cacheDirVolumeName, MountPath: m.CacheDir, ReadOnly: true},
		{Name: cacheRWVolumeName, MountPath: cacheSub},
		{Name: nvsnapToolsVolumeName, MountPath: nvsnapToolsMountPath, ReadOnly: true},
	} {
		patches = append(patches, PatchOp{
			Op:    "add",
			Path:  fmt.Sprintf("/spec/containers/%d/volumeMounts/-", m.MainContainer),
			Value: vm,
		})
	}

	// seed-cache init container: copy the rox's cache subtree into the
	// writable emptyDir before the engine starts, so the compile caches
	// are present at the SAME path (reuse) AND writable. The model is NOT
	// copied — it stays RO-mounted (the big part). Uses the workload's
	// own image (cp/sh present, already pulled). Tolerates an absent
	// cache subtree (cold-ish: engine rebuilds, slower but correct).
	if pod.Spec.InitContainers == nil {
		patches = append(patches, PatchOp{Op: "add", Path: "/spec/initContainers", Value: []any{}})
	}
	// Run as root: the rox cache files are root-owned (the engine wrote
	// them at capture), so a non-root image USER can't even stat them
	// (verified: "cp: cannot stat … Permission denied"). cp -a preserves
	// the original ownership into the emptyDir, so the engine still
	// reads/writes its own files afterward.
	seedRoot := int64(0)
	seedInit := corev1.Container{
		Name:  "nvsnap-seed-cache",
		Image: main.Image,
		// cp -a preserves the capture's root-owned restrictive perms
		// (cache/ is 0700 root, torchinductor 0755 root). The restored
		// engine's Triton python stub runs NON-root, so it then can't
		// traverse/write the cache (PermissionError [Errno 13] on
		// /opt/nvsnap/cache/torchinductor, verified). chmod -R a+rwX opens
		// the pod-local ephemeral cache so any UID the engine runs as can
		// read + write it. Safe: it's a per-pod scratch copy, not shared.
		Command: []string{"sh", "-c", fmt.Sprintf("if [ -d %s/cache ]; then cp -a %s/cache/. %s/ && chmod -R a+rwX %s; fi", cacheSeedSrcPath, cacheSeedSrcPath, cacheSeedDstPath, cacheSeedDstPath)},
		VolumeMounts: []corev1.VolumeMount{
			{Name: cacheDirVolumeName, MountPath: cacheSeedSrcPath, ReadOnly: true},
			{Name: cacheRWVolumeName, MountPath: cacheSeedDstPath},
		},
		SecurityContext: &corev1.SecurityContext{RunAsUser: &seedRoot},
	}
	patches = append(patches, PatchOp{Op: "add", Path: "/spec/initContainers/-", Value: seedInit})

	// Cache/model env — REPLAYED from the manifest (the per-checkpoint
	// single source of truth), verbatim, so the paths match exactly what
	// the capture pod ran with regardless of any later ConfigMap edit.
	// Fall back to recomputing from CacheDir for pre-v0.1.0 cachedir
	// captures that predate the stamped CacheEnv. NOTE: never read the
	// live ConfigMap here — that would reintroduce the path-drift the
	// stamp exists to prevent.
	var envs []corev1.EnvVar
	if len(manifest.CacheEnv) > 0 {
		envs = sortedEnvVars(manifest.CacheEnv)
	} else {
		envs = cacheDirEnvVars(m.CacheDir)
	}
	envs = append(envs,
		corev1.EnvVar{Name: "NVSNAP_NO_OVERLAY", Value: "1"},
		corev1.EnvVar{Name: "NVSNAP_PREWARM_DIR", Value: m.CacheDir},
		corev1.EnvVar{Name: "NVSNAP_ORIG_COMMAND", Value: string(argvJSON)},
		corev1.EnvVar{Name: "NVSNAP_ORIG_CWD", Value: manifest.EntryCwd},
		// NOTE: do NOT set HF_HUB_OFFLINE here. It only suppresses benign HF
		// negative-cache (.no_exist) warnings, but vLLM's arg_utils keys off
		// HF_HUB_OFFLINE to rewrite --model from the repo-id to the resolved
		// local snapshot path (engine/arg_utils.py: "when use hf offline,
		// replace model ... to local model path"). Capture (cold, online) keeps
		// the repo-id, so offline-at-restore changes the model string ->
		// different vLLM torch.compile config_hash -> compile-cache MISS ->
		// ~20s recompile every restore (gpt-oss-120b, 2026-06-19). The warnings
		// are harmless; the recompile is not. Leave offline unset so capture and
		// restore compute the same config_hash and the compile cache is reused.
	)
	for _, e := range envs {
		patches = append(patches, appendEnv(m.MainContainer, e))
	}

	// Override command to the shim; clear args (re-applied from
	// NVSNAP_ORIG_COMMAND by the shim after prewarm).
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

	// NOTE: unlike the overlay path, NO securityContext / SYS_ADMIN /
	// seccomp-unconfined changes — the no-overlay shim only reads files
	// (prewarm) and exec's; it issues no mount(2)/pivot_root. Device
	// isolation and the default profiles stay fully intact.
	return patches, nil
}
