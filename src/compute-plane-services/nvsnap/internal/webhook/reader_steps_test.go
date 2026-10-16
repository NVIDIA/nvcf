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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// Two branches appending to a pod with no init containers create the list
// once, whichever goes first, and a prepend lands at index 0.
func TestInitList_CreatedOnceAcrossSteps(t *testing.T) {
	pod := &corev1.Pod{}
	var patches []PatchOp
	patches = appendInits(pod, patches, corev1.Container{Name: "a"})
	patches = appendInits(pod, patches, corev1.Container{Name: "b"}, corev1.Container{Name: "c"})
	patches = prependInit(pod, patches, corev1.Container{Name: "first"})
	patches = appendInitValues(pod, patches, map[string]any{"name": "raw"})
	var creates int
	var order []string
	for _, p := range patches {
		switch {
		case p.Path == "/spec/initContainers":
			creates++
		case strings.HasPrefix(p.Path, "/spec/initContainers/"):
			switch v := p.Value.(type) {
			case corev1.Container:
				order = append(order, v.Name+"@"+strings.TrimPrefix(p.Path, "/spec/initContainers/"))
			case map[string]any:
				order = append(order, v["name"].(string)+"@"+strings.TrimPrefix(p.Path, "/spec/initContainers/"))
			}
		}
	}
	if creates != 1 {
		t.Fatalf("the init list must be created exactly once, got %d in %+v", creates, patches)
	}
	if want := []string{"a@-", "b@-", "c@-", "first@0", "raw@-"}; strings.Join(order, " ") != strings.Join(want, " ") {
		t.Errorf("order = %v, want %v", order, want)
	}
	if patches[0].Path != "/spec/initContainers" {
		t.Errorf("the create must precede the first add: %+v", patches[0])
	}

	// A pod that already has init containers never gets a create.
	pod = &corev1.Pod{Spec: corev1.PodSpec{InitContainers: []corev1.Container{{Name: "chart"}}}}
	for _, p := range prependInit(pod, appendInits(pod, nil, corev1.Container{Name: "x"}), corev1.Container{Name: "y"}) {
		if p.Path == "/spec/initContainers" {
			t.Errorf("no create for a pod that has init containers: %+v", p)
		}
	}
	if appendInits(pod, nil) != nil {
		t.Error("nothing to add, nothing to patch")
	}
}

// The sweep and the seed are the same step in both branches; only the
// posture differs. Root posture is for the root-owned captured tree and
// adds only what cp -a needs; inherit posture follows the engine.
func TestReaderSteps_PostureAndResources(t *testing.T) {
	uid, nonRoot := int64(1000), true
	main := &corev1.Container{Name: "engine", Image: "img:1", SecurityContext: &corev1.SecurityContext{RunAsUser: &uid, RunAsNonRoot: &nonRoot}}
	m := &Mutator{}
	src := volumeAt{Volume: "ro", Path: "/ro"}

	sweep, ok := m.sweepStep("sweep", main, src, inheritPosture)
	if !ok {
		t.Fatal("no profile: the sweep is on")
	}
	if sweep.Image != "img:1" || len(sweep.VolumeMounts) != 1 || !sweep.VolumeMounts[0].ReadOnly || !strings.Contains(sweep.Command[2], "/ro") {
		t.Errorf("sweep reads the source read-only in the engine image: %+v", sweep)
	}
	if _, limited := sweep.Resources.Limits[corev1.ResourceMemory]; limited || sweep.Resources.Limits.Cpu().MilliValue() < 1000 {
		t.Errorf("sweep: CPU limited, memory not: %+v", sweep.Resources.Limits)
	}
	if sc := sweep.SecurityContext; sc.RunAsUser == nil || *sc.RunAsUser != uid || sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot || sc.Capabilities == nil {
		t.Errorf("inherit posture follows the engine and is hardened: %+v", sc)
	}

	seed := seedStep("seed", main, src, "3", volumeAt{Volume: "rw", Path: "/rw"}, "cache", "rank 3", rootPosture)
	if !strings.Contains(seed.Args[0], "cp -a /ro/3/. /rw/cache/") || !strings.Contains(seed.Args[0], "chmod -R a+rwX /rw/cache") || !strings.HasSuffix(seed.Args[0], "exit 0") {
		t.Errorf("seed copies the subdirectory into the destination and never fails: %q", seed.Args[0])
	}
	if !strings.Contains(seed.Args[0], "sort > "+SeedIndexFile) || !strings.Contains(seed.Args[0], "! -name "+SeedIndexFile) {
		t.Errorf("seed writes the index of seeded paths for the refresh delta scan: %q", seed.Args[0])
	}
	if len(seed.VolumeMounts) != 2 || !seed.VolumeMounts[0].ReadOnly || seed.VolumeMounts[1].ReadOnly {
		t.Errorf("seed mounts the source read-only and the destination writable: %+v", seed.VolumeMounts)
	}
	if _, limited := seed.Resources.Limits[corev1.ResourceMemory]; limited {
		t.Errorf("seed: no memory limit, page cache is charged to it: %+v", seed.Resources.Limits)
	}
	sc := seed.SecurityContext
	if sc.RunAsUser == nil || *sc.RunAsUser != 0 || sc.RunAsNonRoot != nil {
		t.Errorf("root posture runs as root and does not inherit the engine's non-root flag: %+v", sc)
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" || len(sc.Capabilities.Add) == 0 {
		t.Errorf("root posture drops everything and adds back only what cp -a needs: %+v", sc.Capabilities)
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Errorf("no privilege escalation in either posture: %+v", sc)
	}

	main.Env = []corev1.EnvVar{{Name: "NVSNAP_PREWARM", Value: "0"}}
	if _, ok := m.sweepStep("sweep", main, src, inheritPosture); ok {
		t.Error("NVSNAP_PREWARM=0 turns the sweep off in every branch")
	}
}

// One rule for the cachedir env in both branches: cache paths follow the
// branch's cache root, model paths follow its model root or are dropped
// when the branch keeps the model elsewhere, and switches apply as written.
// The identity roots reproduce the template exactly, which is what the
// checkpoint-restore branch relies on.
func TestCacheEnvFor_OneRuleForBothBranches(t *testing.T) {
	m := &Mutator{CacheDir: "/opt/nvsnap"}
	want := map[string]string{}
	for _, e := range cacheDirEnvVars("/opt/nvsnap") {
		want[e.Name] = e.Value
	}
	if _, ok := want["VLLM_ENABLE_STARTUP_PLAN"]; !ok {
		t.Fatal("the default template carries a switch; this test depends on it")
	}

	identity := map[string]string{}
	for _, e := range m.cacheEnvFor("/opt/nvsnap/cache", "/opt/nvsnap/model") {
		identity[e.Name] = e.Value
	}
	if len(identity) != len(want) {
		t.Fatalf("identity roots must keep every entry: got %v want %v", identity, want)
	}
	for k, v := range want {
		if identity[k] != v {
			t.Errorf("identity: %s = %q, want %q", k, identity[k], v)
		}
	}

	rebased := map[string]string{}
	for _, e := range m.cacheEnvFor("/config/models/.nvsnap/cache/abc", "") {
		rebased[e.Name] = e.Value
	}
	if rebased["TORCHINDUCTOR_CACHE_DIR"] != "/config/models/.nvsnap/cache/abc/torchinductor" || rebased["HOME"] != "/config/models/.nvsnap/cache/abc" {
		t.Errorf("cache paths follow the cache root: %v", rebased)
	}
	if _, has := rebased["HF_HOME"]; has {
		t.Errorf("model paths are dropped when the branch keeps the model elsewhere: %v", rebased)
	}
	if _, has := rebased["NIM_CACHE_PATH"]; has {
		t.Errorf("model paths are dropped when the branch keeps the model elsewhere: %v", rebased)
	}
	if rebased["VLLM_ENABLE_STARTUP_PLAN"] != "1" {
		t.Errorf("switches apply as written in every branch: %v", rebased)
	}

	moved := map[string]string{}
	for _, e := range m.cacheEnvFor("/c", "/m/weights") {
		moved[e.Name] = e.Value
	}
	if moved["HF_HOME"] != "/m/weights" || moved["NIM_CACHE_PATH"] != "/m/weights" || moved["TRITON_CACHE_DIR"] != "/c/.triton/cache" {
		t.Errorf("model paths follow a model root when given: %v", moved)
	}

	for _, tc := range []struct {
		v    string
		want cacheEnvClass
	}{
		{"/opt/nvsnap/cache/x", envCachePath}, {"/opt/nvsnap/cache", envCachePath},
		{"/opt/nvsnap/model", envModelPath}, {"/opt/nvsnap", envModelPath},
		{"/opt/nvsnapx/cache", envSwitch}, {"1", envSwitch}, {"/tmp/other", envSwitch},
	} {
		if got := classifyCacheEnv(corev1.EnvVar{Value: tc.v}, "/opt/nvsnap"); got != tc.want {
			t.Errorf("classify %q = %v, want %v", tc.v, got, tc.want)
		}
	}
}
