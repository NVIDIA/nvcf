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

package rootfsonly

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestIsNIMImage(t *testing.T) {
	cases := []struct {
		image string
		want  bool
	}{
		{"nvcr.io/nim/meta/llama-3.3-70b-instruct:1.15.5", true},
		{"nvcr.io/nim/qwen/qwen3-32b:1.0.0", true},
		{"stg.nvcr.io/nim/foo/bar:1.0", true},
		{"vllm/vllm-openai:v0.11.2", false},
		{"stg.nvcr.io/zq9tgrjzrfpo/nvsnap-agent:v0.14.23", false},
		{"nvcr.io/nvidia/tensorrt-llm/release:1.3.0rc5", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsNIMImage(tc.image); got != tc.want {
			t.Errorf("IsNIMImage(%q) = %v, want %v", tc.image, got, tc.want)
		}
	}
}

// pod returns a minimal PodSpec for tests.
func pod(image string, volMounts []corev1.VolumeMount, volumes []corev1.Volume) *corev1.PodSpec {
	return &corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:         "main",
			Image:        image,
			VolumeMounts: volMounts,
		}},
		Volumes: volumes,
	}
}

func TestClassifyVolumes_VLLMShape(t *testing.T) {
	// Mirrors deploy/k8s/vllm-8b.yaml shape: nvsnap tooling volumes (skip),
	// /dev/shm (skip), one HF cache hostPath (capture).
	mounts := []corev1.VolumeMount{
		{Name: "shm", MountPath: "/dev/shm"},
		{Name: "nvsnap-lib", MountPath: "/nvsnap-lib"},
		{Name: "hf-cache", MountPath: "/root/.cache/huggingface"},
	}
	volumes := []corev1.Volume{
		{Name: "shm", VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory},
		}},
		{Name: "nvsnap-lib", VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		}},
		{Name: "hf-cache", VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/containerd/nvsnap-hf-cache"},
		}},
	}
	got := ClassifyVolumes(pod("vllm/vllm-openai:v0.11.2", mounts, volumes), 0)

	wantNames := map[string]VolumeKind{
		"rootfs":   VolumeRootfs,   // non-NIM image → rootfs included
		"hf-cache": VolumeUserData, // hostPath at user-data path → captured
	}
	if len(got) != len(wantNames) {
		t.Fatalf("classified %d volumes, want %d: %+v", len(got), len(wantNames), got)
	}
	for _, c := range got {
		w, ok := wantNames[c.Name]
		if !ok {
			t.Errorf("unexpected classified volume %s (kind=%s)", c.Name, c.Kind)
			continue
		}
		if c.Kind != w {
			t.Errorf("%s: kind=%s, want %s", c.Name, c.Kind, w)
		}
	}

	// Detailed checks on the user-data volume.
	for _, c := range got {
		if c.Name == "hf-cache" {
			if c.VolumeType != "hostPath" {
				t.Errorf("hf-cache VolumeType = %q, want hostPath", c.VolumeType)
			}
			if c.HostPath != "/var/lib/containerd/nvsnap-hf-cache" {
				t.Errorf("hf-cache HostPath = %q", c.HostPath)
			}
			if c.MountPath != "/root/.cache/huggingface" {
				t.Errorf("hf-cache MountPath = %q", c.MountPath)
			}
		}
	}
}

func TestClassifyVolumes_NIMShape(t *testing.T) {
	// Mirrors deploy/k8s/nim-llama-70b.yaml: NIM image → rootfs upperdir
	// (always captured now) + nim-cache emptyDir. The upperdir is what
	// catches NIMs like whisper that write /opt/nim/.cache to the rootfs
	// instead of a mounted volume.
	mounts := []corev1.VolumeMount{
		{Name: "shm", MountPath: "/dev/shm"},
		{Name: "nvsnap", MountPath: "/nvsnap"},
		{Name: "nvsnap-lib", MountPath: "/nvsnap-lib"},
		{Name: "checkpoints", MountPath: "/checkpoints"},
		{Name: "nim-cache", MountPath: "/opt/nim/.cache"},
	}
	volumes := []corev1.Volume{
		{Name: "shm", VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory},
		}},
		{Name: "nvsnap", VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		}},
		{Name: "nvsnap-lib", VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		}},
		{Name: "checkpoints", VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/containerd/nvsnap-checkpoints"},
		}},
		{Name: "nim-cache", VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		}},
	}
	got := ClassifyVolumes(pod("nvcr.io/nim/meta/llama-3.3-70b-instruct:1.15.5", mounts, volumes), 0)

	// rootfs upperdir + nim-cache (2 entries).
	if len(got) != 2 {
		t.Fatalf("classified %d volumes, want 2 (rootfs + nim-cache): %+v", len(got), got)
	}
	var sawRootfs, sawNimCache bool
	for _, c := range got {
		switch {
		case c.Kind == VolumeRootfs && c.Name == "rootfs":
			sawRootfs = true
		case c.Name == "nim-cache" && c.Kind == VolumeUserData && c.VolumeType == "emptyDir":
			sawNimCache = true
		}
	}
	if !sawRootfs {
		t.Errorf("NIM pod must still capture the rootfs upperdir; got %+v", got)
	}
	if !sawNimCache {
		t.Errorf("nim-cache user-data emptyDir missing; got %+v", got)
	}
}

func TestClassifyVolumes_EmptyDirMemorySkipped(t *testing.T) {
	mounts := []corev1.VolumeMount{
		{Name: "shm", MountPath: "/dev/shm"},
	}
	volumes := []corev1.Volume{
		{Name: "shm", VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory},
		}},
	}
	got := ClassifyVolumes(pod("any/image", mounts, volumes), 0)
	for _, c := range got {
		if c.Name == "shm" {
			t.Fatalf("/dev/shm should not be classified for capture: %+v", c)
		}
	}
}

func TestClassifyVolumes_UnsupportedVolumeTypesSkipped(t *testing.T) {
	mounts := []corev1.VolumeMount{
		{Name: "secret-vol", MountPath: "/etc/foo"},
		{Name: "cm-vol", MountPath: "/etc/bar"},
		{Name: "projected", MountPath: "/var/run/secrets/kubernetes.io"},
	}
	volumes := []corev1.Volume{
		{Name: "secret-vol", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "s"}}},
		{Name: "cm-vol", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{}}},
		{Name: "projected", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{}}},
	}
	got := ClassifyVolumes(pod("nvcr.io/nvidia/tensorrt-llm:1.0", mounts, volumes), 0)
	// Only the rootfs entry, no user-data captures.
	if len(got) != 1 || got[0].Kind != VolumeRootfs {
		t.Fatalf("got %+v; want only rootfs", got)
	}
}

func TestClassifyVolumes_VolumeNotMountedSkipped(t *testing.T) {
	// Volume defined in pod.spec.volumes but not mounted in main container.
	volumes := []corev1.Volume{
		{Name: "orphan", VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/orphan"},
		}},
	}
	got := ClassifyVolumes(pod("foo/bar", nil, volumes), 0)
	for _, c := range got {
		if c.Name == "orphan" {
			t.Fatalf("orphan volume (not mounted in main) should be skipped: %+v", c)
		}
	}
}

func TestClassifyVolumes_NilOrInvalidContainerIndex(t *testing.T) {
	if got := ClassifyVolumes(nil, 0); got != nil {
		t.Errorf("nil pod: got %v, want nil", got)
	}
	if got := ClassifyVolumes(pod("foo", nil, nil), 5); got != nil {
		t.Errorf("out-of-range main: got %v, want nil", got)
	}
	if got := ClassifyVolumes(pod("foo", nil, nil), -1); got != nil {
		t.Errorf("negative main: got %v, want nil", got)
	}
}
