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

package webhook

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
)

// The steps a pod needs when it reads a shared read-only volume, used by
// both branches of the webhook. The checkpoint-restore branch (cachedir.go,
// election.go) mounts a captured cache tree; the model-volume branch
// (model_volume.go, cache_volume.go) mounts a model and a cache set. What
// to mount and when is each branch's decision. How a pod reads it is the
// same everywhere and lives here:
//
//   - sweepStep reads the volume into the node's page cache with parallel
//     byte-range readers before the engine starts, because the engine's own
//     loader reads from one thread and a single stream over a network block
//     device is latency-bound (docs/BENCHMARK.md, "Page-cache prewarm").
//   - seedStep copies one directory of the volume into the pod's writable
//     cache, because the engine must be able to write its cache and the
//     volume is shared.
//   - appendInits and prependInit put init containers on the pod and create
//     the list exactly once across every patch of the admission, so no
//     branch needs to know what another branch added before it.
//
// Both steps run in the workload's own image, which is already pulled, and
// are best-effort by construction: nothing here may keep a pod from
// starting.

// SeedIndexFile, written at the root of a seeded cache, lists the relative
// paths the seed placed there, one per line, sorted. The agent compares the
// cachedir against it after the engine is Ready to find what was added.
const SeedIndexFile = ".nvsnap-seeded"

// volumeAt is a pod volume and the path a step reads or writes it at.
type volumeAt struct {
	Volume string
	Path   string
}

// sweepResources give the sweep real cores and no memory ceiling. Page
// cache is charged to the cgroup that faults it in, and a memory limit
// below the tree size makes the kernel evict the sweep's own pages as it
// reads, so the engine finds only the last limit's worth warm. Measured on
// GB300 with a 512Mi limit (2026-10-01): the sweep finished 33 GB in 8 to
// 14 s and the engine still read the weights in 43 to 45 s, the same as
// with no sweep. The pages are reclaimable and move to the pod's cgroup
// when the init exits.
var sweepResources = corev1.ResourceRequirements{
	Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("128Mi")},
	Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
}

// copyResources suit a cp of a compile cache, about a gigabyte. No memory
// limit for the same page-cache reason as the sweep.
var copyResources = corev1.ResourceRequirements{
	Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
	Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
}

// readerPosture is who a step runs as.
type readerPosture int

const (
	// inheritPosture runs the step as the engine container does, so it
	// passes the same admission policies and can read what the engine can.
	inheritPosture readerPosture = iota
	// rootPosture runs the step as root with only the capabilities cp -a
	// needs. The captured tree of a checkpoint restore is root-owned and
	// 0700, so a non-root image user cannot even stat it.
	rootPosture
)

// harden applies the posture and the resources to a step container.
func harden(c *corev1.Container, res corev1.ResourceRequirements, posture readerPosture, main *corev1.Container) {
	if posture == rootPosture {
		root := int64(0)
		c.SecurityContext = &corev1.SecurityContext{
			RunAsUser:    &root,
			Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}, Add: []corev1.Capability{"CHOWN", "FOWNER", "DAC_OVERRIDE", "DAC_READ_SEARCH"}},
		}
		modelvolume.Harden(c, res, nil)
		return
	}
	modelvolume.Harden(c, res, main.SecurityContext)
}

// sweepStep is the page-cache sweep of src, or false when policy turns it
// off: NVSNAP_PREWARM on the engine container wins, then the storage
// profile, and with no profile the sweep is on.
func (m *Mutator) sweepStep(name string, main *corev1.Container, src volumeAt, posture readerPosture) (corev1.Container, bool) {
	if !m.prewarmWanted(*main) {
		return corev1.Container{}, false
	}
	c := corev1.Container{
		Name:         name,
		Image:        main.Image,
		Command:      []string{"sh", "-c", prewarmCommand(src.Path, m.prewarmWorkers())},
		VolumeMounts: []corev1.VolumeMount{{Name: src.Volume, MountPath: src.Path, ReadOnly: true}},
	}
	harden(&c, sweepResources, posture, main)
	return c, true
}

// seedStep copies src.Path/srcSubdir into dst.Path/dstSubdir and opens the copy to any
// user the engine runs as: the copy is a per-pod scratch, not the shared
// volume. A missing or failed source leaves the cache empty and the engine
// compiles as it would have anyway; the step always exits 0 and logs how
// many files it placed.
func seedStep(name string, main *corev1.Container, src volumeAt, srcSubdir string, dst volumeAt, dstSubdir string, label string, posture readerPosture) corev1.Container {
	from, to := src.Path, dst.Path
	if srcSubdir != "" {
		from = src.Path + "/" + srcSubdir
	}
	if dstSubdir != "" {
		to = dst.Path + "/" + dstSubdir
	}
	c := corev1.Container{
		Name:    name,
		Image:   main.Image,
		Command: []string{"/bin/sh", "-c"},
		// The index of seeded paths lets the agent tell what the engine
		// adds later (docs/proposals/helm-chart-cache-refresh.md).
		Args: []string{fmt.Sprintf("if [ -d %[1]s ]; then cp -a %[1]s/. %[2]s/ 2>/dev/null; chmod -R a+rwX %[2]s 2>/dev/null; fi; mkdir -p %[2]s && (cd %[2]s && find . -type f ! -name %[4]s | sed 's|^\\./||' | sort > %[4]s) 2>/dev/null; echo \"nvsnap: seeded %[3]s, $(find %[2]s -type f 2>/dev/null | wc -l) files\"; exit 0",
			from, to, label, SeedIndexFile)},
		VolumeMounts: []corev1.VolumeMount{
			{Name: src.Volume, MountPath: src.Path, ReadOnly: true},
			{Name: dst.Volume, MountPath: dst.Path},
		},
	}
	harden(&c, copyResources, posture, main)
	return c
}

// prewarmWanted decides whether a reader gets the sweep. An explicit
// NVSNAP_PREWARM on the workload container wins ("0" off, anything else
// on); otherwise the storage profile decides, and with no profile the
// answer is on.
func (m *Mutator) prewarmWanted(main corev1.Container) bool {
	for _, e := range main.Env {
		if e.Name == "NVSNAP_PREWARM" {
			return e.Value != "0"
		}
	}
	if m.StorageProfile == nil {
		return true
	}
	return m.StorageProfile.PrewarmEnabled()
}

// prewarmWorkers is the sweep's reader count from the storage profile, or
// the default without one.
func (m *Mutator) prewarmWorkers() int {
	if m.StorageProfile == nil {
		return checkpointstore.DefaultPrewarmParallelism
	}
	return m.StorageProfile.PrewarmWorkers()
}

// cacheEnvClass is what a rendered cachedir env entry points at.
type cacheEnvClass int

const (
	// envCachePath is a path under <CacheDir>/cache: a compile cache that
	// moves with the pod's writable cache root.
	envCachePath cacheEnvClass = iota
	// envModelPath is a path under <CacheDir>/model or <CacheDir> itself:
	// the model, which lives wherever the branch put the model.
	envModelPath
	// envSwitch is any other value: a flag such as VLLM_ENABLE_STARTUP_PLAN=1
	// that applies as written wherever the caches live.
	envSwitch
)

// classifyCacheEnv is the one rule for what a template entry means. Both
// branches go through it, so a new kind of entry reaches every pod the
// same way or fails one shared test.
func classifyCacheEnv(e corev1.EnvVar, cacheDir string) cacheEnvClass {
	switch {
	case strings.HasPrefix(e.Value, cacheDir+"/cache"):
		return envCachePath
	case e.Value == cacheDir || strings.HasPrefix(e.Value, cacheDir+"/"):
		return envModelPath
	default:
		return envSwitch
	}
}

// cacheEnvFor renders the cachedir env template for a pod whose caches
// live under cacheRoot and whose model lives under modelRoot. Cache paths
// are rebased onto cacheRoot, model paths onto modelRoot or dropped when
// modelRoot is empty (the branch's landing volume holds the model and the
// chart addresses it itself), switches apply as written. The
// checkpoint-restore branch passes the agent's own roots, which is the
// identity; the model-volume branch passes its per-pod cache root.
func (m *Mutator) cacheEnvFor(cacheRoot, modelRoot string) []corev1.EnvVar {
	cacheBase, modelBase := m.CacheDir+"/cache", m.CacheDir+"/model"
	var out []corev1.EnvVar
	for _, e := range m.cacheEnvVars(m.CacheDir) {
		switch classifyCacheEnv(e, m.CacheDir) {
		case envCachePath:
			out = append(out, corev1.EnvVar{Name: e.Name, Value: cacheRoot + strings.TrimPrefix(e.Value, cacheBase)})
		case envModelPath:
			if modelRoot == "" {
				continue
			}
			if strings.HasPrefix(e.Value, modelBase) {
				out = append(out, corev1.EnvVar{Name: e.Name, Value: modelRoot + strings.TrimPrefix(e.Value, modelBase)})
			} else {
				out = append(out, e)
			}
		default:
			out = append(out, e)
		}
	}
	return out
}

// initListCreated reports whether a patch so far already created the pod's
// init container list.
func initListCreated(patches []PatchOp) bool {
	for i := range patches {
		if patches[i].Op == "add" && patches[i].Path == "/spec/initContainers" {
			return true
		}
	}
	return false
}

// ensureInitList creates the pod's init container list when the pod has
// none and no earlier patch created it.
func ensureInitList(pod *corev1.Pod, patches []PatchOp) []PatchOp {
	if pod.Spec.InitContainers == nil && !initListCreated(patches) {
		patches = append(patches, PatchOp{Op: "add", Path: "/spec/initContainers", Value: []any{}})
	}
	return patches
}

// appendInits adds init containers at the end of the pod's list.
func appendInits(pod *corev1.Pod, patches []PatchOp, inits ...corev1.Container) []PatchOp {
	vals := make([]any, len(inits))
	for i := range inits {
		vals[i] = inits[i]
	}
	return appendInitValues(pod, patches, vals...)
}

// appendInitValues is appendInits for callers that build the container as
// a raw patch value.
func appendInitValues(pod *corev1.Pod, patches []PatchOp, inits ...any) []PatchOp {
	if len(inits) == 0 {
		return patches
	}
	patches = ensureInitList(pod, patches)
	for i := range inits {
		patches = append(patches, PatchOp{Op: "add", Path: "/spec/initContainers/-", Value: inits[i]})
	}
	return patches
}

// prependInit adds an init container at the front of the pod's list, ahead
// of the chart's own.
func prependInit(pod *corev1.Pod, patches []PatchOp, init corev1.Container) []PatchOp {
	patches = ensureInitList(pod, patches)
	return append(patches, PatchOp{Op: "add", Path: "/spec/initContainers/0", Value: init})
}
