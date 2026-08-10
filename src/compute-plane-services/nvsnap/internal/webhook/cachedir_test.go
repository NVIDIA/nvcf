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
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func envToMap(vars []corev1.EnvVar) map[string]string {
	m := map[string]string{}
	for _, e := range vars {
		m[e.Name] = e.Value
	}
	return m
}

// Template parse + placeholder resolution: {cache}/{model}/{root} resolve
// against the cache dir; comments, blank lines, and malformed lines skip.
func TestParseAndResolveCacheEnvTemplate(t *testing.T) {
	tmpl := parseCacheEnvTemplate("# comment\nHOME={cache}\n\nHF_HOME={model}\nX={root}/x\nnoeq line\n=nokey\n")
	got := envToMap(resolveCacheEnv(tmpl, "/opt/nvsnap"))
	want := map[string]string{
		"HOME":    "/opt/nvsnap/cache",
		"HF_HOME": "/opt/nvsnap/model",
		"X":       "/opt/nvsnap/x",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries %v, want %d", len(got), got, len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

// cacheEnvVars: ConfigMap-file template overrides the built-in default;
// a missing/empty file falls back to the default (fail-safe, never errors).
func TestCacheEnvVars_FileOverrideAndFallback(t *testing.T) {
	m := &Mutator{}
	if def := envToMap(m.cacheEnvVars("/opt/nvsnap")); def["HF_HOME"] != "/opt/nvsnap/model" {
		t.Fatalf("default: HF_HOME = %q", def["HF_HOME"])
	}

	f := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(f, []byte("HOME={cache}\nONLY_ME={model}/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.CacheEnvFile = f
	ov := envToMap(m.cacheEnvVars("/opt/nvsnap"))
	if len(ov) != 2 || ov["ONLY_ME"] != "/opt/nvsnap/model/x" || ov["HOME"] != "/opt/nvsnap/cache" {
		t.Fatalf("file override = %v", ov)
	}

	m.CacheEnvFile = "/no/such/file"
	if fb := envToMap(m.cacheEnvVars("/opt/nvsnap")); fb["HF_HOME"] != "/opt/nvsnap/model" {
		t.Fatalf("missing-file fallback did not use default: %v", fb)
	}
}

// Restore replays the stamped manifest env verbatim, name-sorted, and does
// NOT depend on the ConfigMap (path consistency across a CM edit).
func TestSortedEnvVars_Deterministic(t *testing.T) {
	out := sortedEnvVars(map[string]string{"ZED": "1", "ABC": "2", "MID": "3"})
	if len(out) != 3 || out[0].Name != "ABC" || out[1].Name != "MID" || out[2].Name != "ZED" {
		t.Fatalf("not name-sorted: %v", out)
	}
}

func cacheDirPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "p",
			Namespace: "ns",
			// Capture-side cachedir injection is opt-in via this label.
			Labels: map[string]string{CaptureLabel: "true"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "inference"}},
		},
	}
}

// The env set must point HOME + the JIT caches under <root>/cache and
// HF_HOME under <root>/model — identical strings are what the restore
// side re-emits (path consistency is the whole mechanism).
func TestCacheDirEnvVars(t *testing.T) {
	want := map[string]string{
		"HOME":                    "/opt/nvsnap/cache",
		"TORCHINDUCTOR_CACHE_DIR": "/opt/nvsnap/cache/torchinductor",
		"NIM_CACHE_PATH":          "/opt/nvsnap/model",
		"TRITON_CACHE_DIR":        "/opt/nvsnap/cache/.triton/cache",
		"VLLM_CACHE_ROOT":         "/opt/nvsnap/cache/.cache/vllm",
		"CUDA_CACHE_PATH":         "/opt/nvsnap/cache/.nv/ComputeCache",
		"HF_HOME":                 "/opt/nvsnap/model",
	}
	got := map[string]string{}
	for _, e := range cacheDirEnvVars("/opt/nvsnap") {
		got[e.Name] = e.Value
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	// Mandatory three (ember) must be present.
	for _, k := range []string{"HOME", "TORCHINDUCTOR_CACHE_DIR", "NIM_CACHE_PATH"} {
		if _, ok := got[k]; !ok {
			t.Errorf("mandatory env %s missing", k)
		}
	}
}

// Capture-inject: cachedir mode adds an emptyDir at the cache root + the
// env vars on the main container.
func TestCacheDirCapturePatches(t *testing.T) {
	m := &Mutator{CacheDir: "/opt/nvsnap", MainContainer: 0}
	patches := m.cacheDirCapturePatches(cacheDirPod())
	if len(patches) == 0 {
		t.Fatal("expected capture patches in cachedir mode")
	}
	var sawVol, sawMount, sawHome bool
	for _, p := range patches {
		switch {
		case p.Path == "/spec/volumes/-":
			if v, ok := p.Value.(corev1.Volume); ok && v.Name == cacheDirVolumeName && v.EmptyDir != nil {
				sawVol = true
			}
		case strings.HasSuffix(p.Path, "/volumeMounts/-"):
			if vm, ok := p.Value.(corev1.VolumeMount); ok && vm.MountPath == "/opt/nvsnap" {
				sawMount = true
			}
		case strings.HasSuffix(p.Path, "/env/-"):
			if e, ok := p.Value.(corev1.EnvVar); ok && e.Name == "HOME" && e.Value == "/opt/nvsnap/cache" {
				sawHome = true
			}
		}
	}
	if !sawVol {
		t.Error("missing emptyDir volume for cache dir")
	}
	if !sawMount {
		t.Error("missing volumeMount at /opt/nvsnap")
	}
	if !sawHome {
		t.Error("missing HOME=/opt/nvsnap/cache env")
	}
}

// Off by default: no CacheDir → no patches (standard rootfs path).
func TestCacheDirCapturePatches_DisabledWhenUnset(t *testing.T) {
	m := &Mutator{MainContainer: 0}
	if p := m.cacheDirCapturePatches(cacheDirPod()); p != nil {
		t.Errorf("expected nil patches when CacheDir unset, got %d", len(p))
	}
}

// Opt-in only: a pod WITHOUT the nvsnap.io/capture=true label gets no
// injection even in cachedir mode. This is the generic gate that keeps the
// webhook from touching unrelated/infra pods (e.g. helm-chart NVCF
// miniservice pods) in any cluster, NVCA or not.
func TestCacheDirCapturePatches_RequiresCaptureLabel(t *testing.T) {
	m := &Mutator{CacheDir: "/opt/nvsnap", MainContainer: 0}
	pod := cacheDirPod()
	pod.Labels = nil // un-labeled pod
	if p := m.cacheDirCapturePatches(pod); p != nil {
		t.Errorf("expected nil patches for un-labeled pod, got %d", len(p))
	}
	// Wrong value also skips.
	pod.Labels = map[string]string{CaptureLabel: "false"}
	if p := m.cacheDirCapturePatches(pod); p != nil {
		t.Errorf("expected nil patches when capture label != true, got %d", len(p))
	}
}

// Idempotent: a pod already carrying the cache mount is not re-injected.
func TestCacheDirCapturePatches_Idempotent(t *testing.T) {
	m := &Mutator{CacheDir: "/opt/nvsnap", MainContainer: 0}
	pod := cacheDirPod()
	pod.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{
		{Name: cacheDirVolumeName, MountPath: "/opt/nvsnap"},
	}
	if p := m.cacheDirCapturePatches(pod); p != nil {
		t.Errorf("expected no re-inject when cache mount present, got %d patches", len(p))
	}
}
