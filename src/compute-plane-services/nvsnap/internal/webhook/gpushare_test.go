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
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// patchInto applies one JSON-patch op (add or replace) at parts below node
// and returns the updated node. Arrays support appending ("-") and
// addressing an element by index.
func patchInto(t *testing.T, node any, parts []string, op string, v any) any {
	t.Helper()
	key := parts[0]
	switch c := node.(type) {
	case map[string]any:
		if len(parts) == 1 {
			if _, exists := c[key]; exists && op == "add" {
				t.Fatalf("add would overwrite existing member %q", key)
			}
			c[key] = v
			return c
		}
		child, ok := c[key]
		if !ok {
			t.Fatalf("no member %q", key)
		}
		c[key] = patchInto(t, child, parts[1:], op, v)
		return c
	case []any:
		if key == "-" {
			if len(parts) != 1 || op != "add" {
				t.Fatalf("'-' only valid as the last token of an add")
			}
			return append(c, v)
		}
		i, err := strconv.Atoi(key)
		if err != nil || i < 0 || i >= len(c) {
			t.Fatalf("bad array index %q", key)
		}
		if len(parts) == 1 {
			if op != "replace" {
				t.Fatalf("only replace may address an existing element")
			}
			c[i] = v
			return c
		}
		c[i] = patchInto(t, c[i], parts[1:], op, v)
		return c
	default:
		t.Fatalf("cannot descend into %T at %q", node, key)
	}
	return nil
}

func applyPatches(t *testing.T, pod *corev1.Pod, ops []PatchOp) *corev1.Pod {
	t.Helper()
	raw, err := json.Marshal(pod)
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if uerr := json.Unmarshal(raw, &doc); uerr != nil {
		t.Fatal(uerr)
	}
	for _, op := range ops {
		vraw, merr := json.Marshal(op.Value)
		if merr != nil {
			t.Fatal(merr)
		}
		var v any
		if uerr := json.Unmarshal(vraw, &v); uerr != nil {
			t.Fatal(uerr)
		}
		doc = patchInto(t, doc, strings.Split(strings.TrimPrefix(op.Path, "/"), "/"), op.Op, v)
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var res corev1.Pod
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatal(err)
	}
	return &res
}

func gpushareTestPod(annotated bool, gpuEnv []corev1.EnvVar) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "vllm", Namespace: "ns"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "sidecar", Image: "proxy"},
			{
				Name: "engine", Image: "vllm", Env: gpuEnv,
				Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("4")}},
			},
		}},
	}
	if annotated {
		p.Annotations = map[string]string{GPUShareAnnotation: "true"}
	}
	return p
}

func envOf(c corev1.Container, name string) *corev1.EnvVar {
	for i := range c.Env {
		if c.Env[i].Name == name {
			return &c.Env[i]
		}
	}
	return nil
}

func mountAt(c corev1.Container, path string) *corev1.VolumeMount {
	for i := range c.VolumeMounts {
		if c.VolumeMounts[i].MountPath == path {
			return &c.VolumeMounts[i]
		}
	}
	return nil
}

func TestGPUSharePatches_NotAnnotated(t *testing.T) {
	m := &Mutator{}
	if p := m.gpusharePatches(gpushareTestPod(false, nil)); p != nil {
		t.Fatalf("pod without %s must be left alone, got %v", GPUShareAnnotation, p)
	}
}

// An annotated pod's GPU container gets the per-pod store, the node bundle
// and the library in LD_PRELOAD; the sidecar is untouched.
func TestGPUSharePatches_PlacesLibraryAndStore(t *testing.T) {
	m := &Mutator{HostBundleRoot: "/bundle", GPUShareHostRoot: "/ckpt/gpushare-pods"}
	pod := gpushareTestPod(true, nil)
	got := applyPatches(t, pod, m.gpusharePatches(pod))

	vols := map[string]corev1.Volume{}
	for _, v := range got.Spec.Volumes {
		vols[v.Name] = v
	}
	if v := vols[gpushareStoreVolume]; v.HostPath == nil || v.HostPath.Path != "/ckpt/gpushare-pods" ||
		v.HostPath.Type == nil || *v.HostPath.Type != corev1.HostPathDirectoryOrCreate {
		t.Errorf("store volume = %+v", v)
	}
	if v := vols[gpushareLibVolume]; v.HostPath == nil || v.HostPath.Path != "/bundle/nvsnap" {
		t.Errorf("library volume = %+v", v)
	}

	engine := got.Spec.Containers[1]
	store := mountAt(engine, GPUShareStorePath)
	if store == nil || store.Name != gpushareStoreVolume || store.SubPathExpr != "$("+gpusharePodUIDEnv+")" {
		t.Errorf("store mount = %+v; want a per-pod subPathExpr", store)
	}
	if lib := mountAt(engine, nvsnapToolsMountPath); lib == nil || !lib.ReadOnly {
		t.Errorf("library mount = %+v; want read-only at %s", lib, nvsnapToolsMountPath)
	}
	if e := envOf(engine, "LD_PRELOAD"); e == nil || e.Value != GPUShareLibPath {
		t.Errorf("LD_PRELOAD = %+v", e)
	}
	if e := envOf(engine, gpusharePodUIDEnv); e == nil || e.ValueFrom == nil || e.ValueFrom.FieldRef == nil ||
		e.ValueFrom.FieldRef.FieldPath != "metadata.uid" {
		t.Errorf("%s = %+v; want the pod uid from the downward API", gpusharePodUIDEnv, e)
	}

	side := got.Spec.Containers[0]
	if len(side.Env) != 0 || len(side.VolumeMounts) != 0 {
		t.Errorf("non-GPU container was modified: %+v", side)
	}
}

// An existing LD_PRELOAD keeps its libraries; the shim is appended once.
func TestGPUSharePatches_AppendsToExistingPreload(t *testing.T) {
	m := &Mutator{}
	pod := gpushareTestPod(true, []corev1.EnvVar{{Name: "A", Value: "1"}, {Name: "LD_PRELOAD", Value: "/usr/lib/libjemalloc.so"}})
	got := applyPatches(t, pod, m.gpusharePatches(pod))
	if e := envOf(got.Spec.Containers[1], "LD_PRELOAD"); e == nil || e.Value != "/usr/lib/libjemalloc.so:"+GPUShareLibPath {
		t.Errorf("LD_PRELOAD = %+v", e)
	}
	if n := len(got.Spec.Containers[1].Env); n != 3 {
		t.Errorf("env has %d entries, want 3 (A, LD_PRELOAD replaced in place, %s)", n, gpusharePodUIDEnv)
	}

	again := applyPatches(t, got, (&Mutator{}).gpusharePatches(got))
	if e := envOf(again.Spec.Containers[1], "LD_PRELOAD"); e.Value != "/usr/lib/libjemalloc.so:"+GPUShareLibPath {
		t.Errorf("re-admission changed LD_PRELOAD to %q", e.Value)
	}
}

// An LD_PRELOAD from a ConfigMap reference cannot be extended without
// dropping its value: that container is left alone, and with no other GPU
// container the pod is admitted unchanged.
func TestGPUSharePatches_PreloadFromReference(t *testing.T) {
	m := &Mutator{}
	pod := gpushareTestPod(true, []corev1.EnvVar{{Name: "LD_PRELOAD", ValueFrom: &corev1.EnvVarSource{
		ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "cm"}, Key: "k"},
	}}})
	if p := m.gpusharePatches(pod); p != nil {
		t.Fatalf("want no patches, got %v", p)
	}
}

// The node bundle already mounted at /nvsnap (rootfs restore) is reused.
func TestGPUSharePatches_ReusesMountedBundle(t *testing.T) {
	m := &Mutator{}
	pod := gpushareTestPod(true, nil)
	pod.Spec.Volumes = []corev1.Volume{{Name: nvsnapToolsVolumeName}}
	pod.Spec.Containers[1].VolumeMounts = []corev1.VolumeMount{{Name: nvsnapToolsVolumeName, MountPath: nvsnapToolsMountPath, ReadOnly: true}}
	got := applyPatches(t, pod, m.gpusharePatches(pod))
	for _, v := range got.Spec.Volumes {
		if v.Name == gpushareLibVolume {
			t.Fatalf("library volume added although the bundle is already mounted")
		}
	}
	n := 0
	for _, vm := range got.Spec.Containers[1].VolumeMounts {
		if vm.MountPath == nvsnapToolsMountPath {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d mounts at %s, want 1", n, nvsnapToolsMountPath)
	}
}

// Through Mutate: gpushare placement rides along with an otherwise plain
// admission (no restore, no model volume, no election).
func TestMutate_GPUShareOnly(t *testing.T) {
	m := &Mutator{Backend: newBackend(t)}
	pod := gpushareTestPod(true, nil)
	ops, err := m.Mutate(context.Background(), pod)
	if err != nil {
		t.Fatal(err)
	}
	got := applyPatches(t, pod, ops)
	if mountAt(got.Spec.Containers[1], GPUShareStorePath) == nil || envOf(got.Spec.Containers[1], "LD_PRELOAD") == nil {
		t.Fatalf("gpushare placement missing from Mutate's patches: %v", ops)
	}
}

// The store path must be a single top-level directory: the agent reaches it
// through /proc/<pid>/root, where an absolute symlink on the way (/var/run
// is one in the vLLM image) resolves against the agent's root, and the
// capture then reports the store missing.
func TestGPUShareStorePathIsTopLevel(t *testing.T) {
	if !strings.HasPrefix(GPUShareStorePath, "/") || strings.Count(GPUShareStorePath, "/") != 1 {
		t.Fatalf("GPUShareStorePath %q must be a top-level directory", GPUShareStorePath)
	}
}
