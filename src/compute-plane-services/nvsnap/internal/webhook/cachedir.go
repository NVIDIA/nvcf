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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"go.opentelemetry.io/otel/attribute"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
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
//   - switches: VLLM_ENABLE_STARTUP_PLAN makes vLLM (0.27+) persist its
//     KV-memory profiling result under VLLM_CACHE_ROOT/startup_plan, so
//     a warm boot skips the memory measurement and the CUDA-graph memory
//     estimation pass. Off by default upstream; a stale plan is ignored
//     by fingerprint and free-memory checks, so it is safe to force on.
//     Anything the engine can write to disk should land in the cache.
func defaultCacheEnvTemplate() []cacheEnvEntry {
	return []cacheEnvEntry{
		{"HOME", "{cache}"},
		{"TORCHINDUCTOR_CACHE_DIR", "{cache}/torchinductor"},
		{"TRITON_CACHE_DIR", "{cache}/.triton/cache"},
		{"VLLM_CACHE_ROOT", "{cache}/.cache/vllm"},
		{"VLLM_ENABLE_STARTUP_PLAN", "1"},
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
	return m.cacheDirCapturePatchesFor(pod, false)
}

// cacheDirCapturePatchesFor is cacheDirCapturePatches with the opt-in
// label check skipped for an elected leader, which is chosen by the
// election rather than by the chart author.
func (m *Mutator) cacheDirCapturePatchesFor(pod *corev1.Pod, elected bool) []PatchOp {
	if m.CacheDir == "" {
		return nil
	}
	// Opt-in only: inject the cachedir capture plumbing into pods explicitly
	// labeled for capture (nvsnap.io/capture: "true"), matching the rootfs
	// capture watcher. Un-labeled pods (system/infra, helm-chart miniservice,
	// anything not meant for capture) are left untouched. See CaptureLabel.
	if !elected && pod.Labels[CaptureLabel] != "true" {
		return nil
	}
	if m.MainContainer < 0 || m.MainContainer >= len(pod.Spec.Containers) {
		return nil
	}
	// A model already served from a cluster model cache (NVCA rewrites the
	// pod's model volume to its read-only claim) is left alone: the
	// cachedir env would point the engine at a second download and the
	// capture would hold a second copy of the model next to that claim.
	if modelServedFromClusterCache(pod) {
		m.logger().WithField("pod", pod.Namespace+"/"+pod.Name).Info("cachedir: model served from a cluster model cache; not injecting the capture cachedir")
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

	envs := m.cacheEnvFor(m.CacheDir+"/cache", m.CacheDir+"/model")
	patches := make([]PatchOp, 0, 5+len(envs))
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
	for _, e := range envs {
		patches = append(patches, appendEnv(m.MainContainer, e))
	}
	patches = append(patches, m.cacheDirInitPatches(pod, &main)...)
	return patches
}

// cacheDirInitName is the init container that creates the cache and model
// subdirectories of a fresh cachedir emptyDir.
const cacheDirInitName = "nvsnap-cachedir-init"

// cacheDirInitPatches injects an init that creates <CacheDir>/cache and
// <CacheDir>/model, world-writable with the sticky bit. An emptyDir starts
// empty; engines that create their own cache tree (vLLM, HF) never noticed,
// but NIM refuses to start when NIM_CACHE_PATH does not exist ("Unable to
// read from NIM_CACHE_PATH", dev1 2026-09-28). The init runs the workload's
// own image so no extra pull is needed, inherits its user posture and is
// hardened for enforced function namespaces.
func (m *Mutator) cacheDirInitPatches(pod *corev1.Pod, main *corev1.Container) []PatchOp {
	for i := range pod.Spec.InitContainers {
		if pod.Spec.InitContainers[i].Name == cacheDirInitName {
			return nil
		}
	}
	cache, model := filepath.Join(m.CacheDir, "cache"), filepath.Join(m.CacheDir, "model")
	init := corev1.Container{
		Name:         cacheDirInitName,
		Image:        main.Image,
		Command:      []string{"/bin/sh", "-c"},
		Args:         []string{fmt.Sprintf("mkdir -p %s %s && chmod 1777 %s %s", cache, model, cache, model)},
		VolumeMounts: []corev1.VolumeMount{{Name: cacheDirVolumeName, MountPath: m.CacheDir}},
	}
	modelvolume.Harden(&init, cacheDirInitResources, main.SecurityContext)
	return prependInit(pod, nil, init)
}

// cacheDirInitResources: two mkdirs.
var cacheDirInitResources = corev1.ResourceRequirements{
	Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
	Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m"), corev1.ResourceMemory: resource.MustParse("128Mi")},
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

	// Resolve the rox PVC (ErrNotFound = not Bound → caller falls to L1).
	pm, err := m.L2Backend.Mount(ctx, hash, cacheDirVolumeMeta(m.CacheDir, pod.Namespace))
	if err != nil {
		return nil, err
	}
	// Cache/model env: REPLAYED from the manifest (the per-checkpoint
	// single source of truth), verbatim, so the paths match exactly what
	// the capture pod ran with regardless of any later ConfigMap edit.
	// Fall back to recomputing from CacheDir for pre-v0.1.0 cachedir
	// captures that predate the stamped CacheEnv. NOTE: never read the
	// live ConfigMap here; that would reintroduce the path-drift the
	// stamp exists to prevent.
	var envs []corev1.EnvVar
	if len(manifest.CacheEnv) > 0 {
		envs = sortedEnvVars(manifest.CacheEnv)
	} else {
		envs = cacheDirEnvVars(m.CacheDir)
	}
	return m.cacheDirRestorePatches(pod, pm.Volume, envs)
}

// cacheDirVolumeMeta is the L2 mount request for the cachedir rox.
func cacheDirVolumeMeta(cacheDir, namespace string) checkpointstore.VolumeMeta {
	return checkpointstore.VolumeMeta{
		Name:      cacheDirVolumeName,
		MountPath: cacheDir,
		Type:      "cachedir",
		Namespace: namespace,
	}
}

// cacheDirRestorePatches builds the cachedir restore decoration around a
// rox volume: the rox mounted read-only at m.CacheDir, a writable emptyDir
// shadowing the cache subtree seeded from the rox, the page-cache prewarm,
// and the cache env. The volume may name a claim that does not exist yet
// (an election follower); nothing here checks the cluster.
func (m *Mutator) cacheDirRestorePatches(pod *corev1.Pod, roxVol corev1.Volume, envs []corev1.EnvVar) ([]PatchOp, error) {
	if m.MainContainer < 0 || m.MainContainer >= len(pod.Spec.Containers) {
		return nil, fmt.Errorf("MainContainer index %d out of range (have %d containers)",
			m.MainContainer, len(pod.Spec.Containers))
	}
	roxVol.Name = cacheDirVolumeName
	if roxVol.PersistentVolumeClaim != nil {
		roxVol.PersistentVolumeClaim.ReadOnly = true
	}

	main := pod.Spec.Containers[m.MainContainer]
	for _, vm := range main.VolumeMounts {
		if vm.Name == cacheDirVolumeName || vm.MountPath == m.CacheDir {
			return nil, nil // already wired
		}
	}

	patches := make([]PatchOp, 0, 11+len(envs))
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
	cacheRWVol := corev1.Volume{Name: cacheRWVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}
	vols := []corev1.Volume{roxVol, cacheRWVol}
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
	} {
		patches = append(patches, PatchOp{
			Op:    "add",
			Path:  fmt.Sprintf("/spec/containers/%d/volumeMounts/-", m.MainContainer),
			Value: vm,
		})
	}

	// Two reader steps, shared with the model-volume branch (reader_steps.go):
	// the seed copies the rox's cache subtree into the writable emptyDir so
	// the compile caches are present at the same path and writable, and the
	// sweep reads the rox tree, model included, into the page cache ahead of
	// the engine. The model is never copied; it stays read-only from the rox.
	// Both run as root: the rox files are root-owned (the engine wrote them at
	// capture), so a non-root image user cannot even stat them; cp -a keeps
	// that ownership and the chmod opens the per-pod scratch copy to whatever
	// UID the engine runs as. Whether the sweep runs is a property of the
	// volume (70B A/B in docs/BENCHMARK.md): the storage profile decides, and
	// the pod's own NVSNAP_PREWARM=0/1 wins.
	rox := volumeAt{Volume: cacheDirVolumeName, Path: cacheSeedSrcPath}
	inits := []corev1.Container{seedStep("nvsnap-seed-cache", &main, rox, "cache", volumeAt{Volume: cacheRWVolumeName, Path: cacheSeedDstPath}, "", "restore cache", rootPosture)}
	if sweep, ok := m.sweepStep("nvsnap-prewarm", &main, rox, rootPosture); ok {
		inits = append(inits, sweep)
	}
	patches = appendInits(pod, patches, inits...)

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
	for _, e := range envs {
		patches = append(patches, appendEnv(m.MainContainer, e))
	}

	// The command is left exactly as authored. The old shim rewrite exec'd
	// the CAPTURED pod's argv, which for the bash-wrapper convention was the
	// idle sleep, so the restored pod seeded 2.2GB and then exited without
	// serving (dev1, 2026-09-24). Nothing here needs a wrapper.
	// No securityContext, SYS_ADMIN or seccomp changes: this path only adds
	// volumes, mounts, env and a copying init container.
	return patches, nil
}

// prewarmChunkBytes is the read unit of the sweep: 256 MiB, 16 dd blocks
// of 16 MiB, so a handful of multi-gigabyte safetensors still spread over
// every reader.
const prewarmChunkBytes = 256 << 20

// prewarmCommand is the sweep. Parallelism must come from byte ranges,
// not files: a checkpoint tree is a dozen files, four of them the
// weights, so a per-file fan-out collapses to one reader. Measured on an
// NVMesh volume (dev1 2026-09-28): one stream 270 MB/s, four 1.0 GB/s,
// eight 2.1 GB/s; the old file-batched sweep read 30 GB serially in 67 s.
// Large files are split into prewarmChunkBytes ranges read with dd; small
// files are batched through cat. Symlinks are followed so a Hugging Face
// snapshot is read through its entry names, which lets the sweep skip
// original/ (the PyTorch .pth copy nothing opens) and avoid the blobs
// directory, where the same bytes would be read a second time.
// Best-effort: every failure is swallowed, a restore never fails here.
func prewarmCommand(root string, workers int) string {
	return fmt.Sprintf(`{ find -L %[1]s -type f -size +64M ! -path '*/original/*' ! -path '*/blobs/*' -printf '%%s|%%p\n' 2>/dev/null | awk -F'|' -v c=%[2]d '{ n=int(($1+c-1)/c); for (i=0;i<n;i++) printf "%%d|%%s\n", i, $2 }' | xargs -d '\n' -r -P %[3]d -n 1 sh -c 'i=${0%%%%|*}; f=${0#*|}; dd if="$f" of=/dev/null bs=16M skip=$((i*16)) count=16 2>/dev/null'; find -L %[1]s -type f ! -size +64M ! -path '*/original/*' ! -path '*/blobs/*' -print0 2>/dev/null | xargs -0 -r -P %[3]d -n 16 cat > /dev/null 2>&1; } || true`, root, prewarmChunkBytes, workers)
}

// nvcaModelVolumeName is the volume NVCA gives a container function's
// model; when its source is a PersistentVolumeClaim the model comes from
// the NVCA model cache.
const nvcaModelVolumeName = "model-data"

// modelServedFromClusterCache reports whether the pod's model volume is a
// claim rather than pod-local storage.
func modelServedFromClusterCache(pod *corev1.Pod) bool {
	for i := range pod.Spec.Volumes {
		v := &pod.Spec.Volumes[i]
		if v.Name == nvcaModelVolumeName && v.PersistentVolumeClaim != nil {
			return true
		}
	}
	return false
}
