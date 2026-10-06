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

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/containerd"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

// fakeGPUProc writes the procfs entries gpushareTargets reads for one process.
func fakeGPUProc(t *testing.T, base string, hostPID, nsPID, sid int, maps []string, fds map[string]string) {
	t.Helper()
	dir := filepath.Join(base, fmt.Sprint(hostPID))
	if err := os.MkdirAll(filepath.Join(dir, "fd"), 0o755); err != nil {
		t.Fatal(err)
	}
	stat := fmt.Sprintf("%d (python3) S 1 %d %d 0 -1 0 0 0 0 0\n", hostPID, sid, sid)
	status := fmt.Sprintf("Name:\tpython3\nNSpid:\t%d\t%d\n", hostPID, nsPID)
	var m strings.Builder
	for i, p := range maps {
		fmt.Fprintf(&m, "7f00000%02d000-7f00000%02d100 r-xp 00000000 103:02 %d                       %s\n", i, i, 1000+i, p)
	}
	for f, c := range map[string]string{"stat": stat, "status": status, "maps": m.String()} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for fd, target := range fds {
		if err := os.Symlink(target, filepath.Join(dir, "fd", fd)); err != nil {
			t.Fatal(err)
		}
	}
}

const testShim = "/nvsnap/libnvsnap_gpushare.so"

// The session's shim processes are selected, leader first then by
// namespace pid; processes outside the session and helpers without GPU or
// shim are ignored.
func TestGPUShareTargets(t *testing.T) {
	base := t.TempDir()
	fakeGPUProc(t, base, 5000, 51, 5000, []string{testShim}, nil)                                       // API server, session leader
	fakeGPUProc(t, base, 5300, 689, 5000, []string{testShim, "/dev/nvidia3"}, nil)                      // TP3
	fakeGPUProc(t, base, 5100, 481, 5000, []string{testShim}, map[string]string{"7": "/dev/nvidiactl"}) // EngineCore
	fakeGPUProc(t, base, 5050, 480, 5000, nil, nil)                                                     // resource tracker
	fakeGPUProc(t, base, 7000, 12, 7000, []string{"/dev/nvidia0"}, nil)                                 // another session

	pids, lib, ok, err := gpushareTargets(base, 5000)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if want := []int{51, 481, 689}; !slices.Equal(pids, want) {
		t.Errorf("pids = %v, want %v", pids, want)
	}
	if lib != testShim {
		t.Errorf("lib = %q", lib)
	}
}

// A process that uses the GPU without the shim would reach CRIU with live
// driver state: refuse instead of producing a checkpoint that cannot restore.
func TestGPUShareTargets_GPUWithoutShim(t *testing.T) {
	base := t.TempDir()
	fakeGPUProc(t, base, 5000, 51, 5000, []string{testShim}, nil)
	fakeGPUProc(t, base, 5200, 70, 5000, []string{"/dev/nvidia0"}, nil)
	if _, _, _, err := gpushareTargets(base, 5000); err == nil || !strings.Contains(err.Error(), "5200") {
		t.Fatalf("want an error naming pid 5200, got %v", err)
	}
}

// No shim anywhere: a plain criu-v2 capture, not an error.
func TestGPUShareTargets_NoShim(t *testing.T) {
	base := t.TempDir()
	fakeGPUProc(t, base, 5000, 51, 5000, []string{"/dev/nvidia0"}, nil)
	if pids, _, ok, err := gpushareTargets(base, 5000); err != nil || ok || pids != nil {
		t.Fatalf("pids=%v ok=%v err=%v", pids, ok, err)
	}
}

func TestDumpV2Args_GPUShare(t *testing.T) {
	plain := dumpV2Args(10, 51, []string{"dev[195/0]:nvidia0"}, false, false, false, "")
	if !slices.Contains(plain, "--libdir") || slices.Contains(plain, "--image-io-mode") {
		t.Errorf("plain dump must load the CUDA plugin and keep buffered I/O: %v", plain)
	}
	gs := dumpV2Args(10, 51, []string{"dev[195/0]:nvidia0"}, true, true, true, "")
	if slices.Contains(gs, "--libdir") {
		t.Errorf("gpushare dump must not load the CUDA plugin: %v", gs)
	}
	i := slices.Index(gs, "--image-io-mode")
	if i < 0 || gs[i+1] != "direct" {
		t.Errorf("gpushare dump with direct I/O: %v", gs)
	}
	for _, want := range []string{"--leave-running", "--external", "dev[195/0]:nvidia0", "--ghost-links"} {
		if !slices.Contains(gs, want) {
			t.Errorf("gpushare dump lacks %q: %v", want, gs)
		}
	}
	if slices.Contains(dumpV2Args(10, 51, nil, false, true, false, ""), "--image-io-mode") {
		t.Errorf("--image-io-mode added although the bundled CRIU lacks it")
	}
}

func TestRestoreV2Args_GPUShare(t *testing.T) {
	plain := restoreV2Args(10, "/checkpoints/x", false, false)
	if !slices.Contains(plain, "--libdir") {
		t.Errorf("plain restore must load the CUDA plugin: %v", plain)
	}
	gs := restoreV2Args(10, "/checkpoints/x", true, true)
	if slices.Contains(gs, "--libdir") || !slices.Contains(gs, "--image-io-mode") || !slices.Contains(gs, "--restore-detached") {
		t.Errorf("gpushare restore argv: %v", gs)
	}
}

func TestCRIUSupportsDirectImageIO(t *testing.T) {
	if !criuSupportsDirectImageIO("  --image-io-mode MODE   writeback or direct\n") {
		t.Error("help text with the option not recognized")
	}
	if criuSupportsDirectImageIO("  --compress-region SIZE\n") {
		t.Error("older help text taken as supporting direct I/O")
	}
}

// A gpushare checkpoint's placeholder mounts the node bundle where the shim
// was mapped from and the checkpoint's chunk store where the processes saved
// to, requests the checkpoint's GPU count, and names node paths (not the
// agent's in-container checkpoint mount) in every hostPath.
func TestGeneratePlaceholderManifest_GPUShare(t *testing.T) {
	ckptRoot := t.TempDir()
	id := "ckpt-1"
	dir := filepath.Join(ckptRoot, id)
	if err := os.MkdirAll(filepath.Join(dir, GPUShareCheckpointSubdir, gpushareCkptSubdir), 0o755); err != nil {
		t.Fatal(err)
	}
	meta := CheckpointMetadata{
		ID: id, PodName: "vllm", PodNamespace: "ns", NodeName: "node-a", ContainerImage: "vllm:1",
		CapturePath: CapturePathCRIUV2,
		GPUShare:    &GPUShareInfo{PIDs: []int{51, 481}, StorePath: GPUShareStoreInContainer, LibPath: testShim},
	}
	b, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	gpuMap := "GPU-a\nGPU-b\nGPU-c\nGPU-d\n"
	if err := os.WriteFile(filepath.Join(dir, GPUShareCheckpointSubdir, gpushareCkptSubdir, "gpus"), []byte(gpuMap), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{config: Config{CheckpointDir: ckptRoot, CheckpointHostDir: "/var/lib/containerd/nvsnap-checkpoints"}}
	a.config.Webhook.HostBundleRoot = "/var/lib/containerd/nvsnap-bundle"

	out, err := a.GeneratePlaceholderManifest(context.Background(), PlaceholderManifestRequest{CheckpointID: id})
	if err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	if err := yaml.Unmarshal([]byte(out), &pod); err != nil {
		t.Fatalf("placeholder is not a valid pod: %v\n%s", err, out)
	}
	c := pod.Spec.Containers[0]
	if q := c.Resources.Limits["nvidia.com/gpu"]; q.String() != "4" {
		t.Errorf("GPU limit = %s, want 4 (the checkpoint's GPU map)", q.String())
	}
	mounts := map[string]corev1.VolumeMount{}
	for _, vm := range c.VolumeMounts {
		mounts[vm.MountPath] = vm
	}
	if vm, ok := mounts["/nvsnap"]; !ok || !vm.ReadOnly {
		t.Errorf("shim directory not mounted read-only at /nvsnap: %+v", mounts)
	}
	if _, ok := mounts[GPUShareStoreInContainer]; !ok {
		t.Errorf("chunk store not mounted at %s: %+v", GPUShareStoreInContainer, mounts)
	}
	paths := map[string]string{}
	for _, v := range pod.Spec.Volumes {
		if v.HostPath != nil {
			paths[v.Name] = v.HostPath.Path
		}
	}
	want := map[string]string{
		"checkpoints":           "/var/lib/containerd/nvsnap-checkpoints",
		"nvsnap-gpushare-lib":   "/var/lib/containerd/nvsnap-bundle/nvsnap",
		"nvsnap-gpushare-store": "/var/lib/containerd/nvsnap-checkpoints/ckpt-1/gpushare",
	}
	for name, p := range want {
		if paths[name] != p {
			t.Errorf("hostPath %s = %q, want %q", name, paths[name], p)
		}
	}
}

// A checkpoint without gpushare keeps the plain placeholder.
func TestGeneratePlaceholderManifest_Plain(t *testing.T) {
	ckptRoot := t.TempDir()
	dir := filepath.Join(ckptRoot, "c")
	_ = os.MkdirAll(dir, 0o755)
	b, _ := json.Marshal(CheckpointMetadata{ID: "c", PodName: "p", PodNamespace: "ns", NodeName: "n", ContainerImage: "i"})
	_ = os.WriteFile(filepath.Join(dir, "metadata.json"), b, 0o644)
	a := &Agent{config: Config{CheckpointDir: ckptRoot}}
	out, err := a.GeneratePlaceholderManifest(context.Background(), PlaceholderManifestRequest{CheckpointID: "c"})
	if err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	if err := yaml.Unmarshal([]byte(out), &pod); err != nil {
		t.Fatalf("invalid pod: %v", err)
	}
	for _, v := range pod.Spec.Volumes {
		if strings.HasPrefix(v.Name, "nvsnap-gpushare") {
			t.Errorf("plain checkpoint got gpushare volume %s", v.Name)
		}
	}
	if q := pod.Spec.Containers[0].Resources.Limits["nvidia.com/gpu"]; q.String() != "1" {
		t.Errorf("GPU limit = %s, want 1", q.String())
	}
}

func TestVisibleGPUUUIDs(t *testing.T) {
	dir := t.TempDir()
	write := func(env ...string) string {
		p := filepath.Join(dir, fmt.Sprint(len(env), "-environ"))
		if err := os.WriteFile(p, []byte(strings.Join(env, "\x00")+"\x00"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, tc := range []struct {
		env  []string
		want string
	}{
		{[]string{"PATH=/bin", "NVIDIA_VISIBLE_DEVICES=GPU-a1,GPU-b2"}, "GPU-a1,GPU-b2"},
		{[]string{"NVIDIA_VISIBLE_DEVICES=all", "X=1"}, ""},
		{[]string{"NVIDIA_VISIBLE_DEVICES=0,1", "X=1", "Y=2"}, ""},
		{[]string{"PATH=/bin"}, ""},
	} {
		if got := visibleGPUUUIDs(write(tc.env...)); got != tc.want {
			t.Errorf("env %v: got %q, want %q", tc.env, got, tc.want)
		}
	}
}

// Collecting the store moves the pod's chunks and chunk lists into the
// checkpoint and writes the GPU map there. The pod's own directory stays
// (the workload's mount points at it) but is left empty, so a later capture
// of the same pod does not write into this checkpoint.
func TestGPUShareCollectStore(t *testing.T) {
	root := t.TempDir()
	podStore := filepath.Join(root, GPUSharePodStoresSubdir, "uid-1")
	for _, f := range []string{"chunks/ab/abcd", "ckpt/gpu-51.chunks"} {
		p := filepath.Join(podStore, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ckptDir := filepath.Join(root, "ckpt-1")
	a := &Agent{config: Config{CheckpointDir: root}}
	ci := &containerd.ContainerInfo{Labels: map[string]string{"io.kubernetes.pod.uid": "uid-1"}}
	if err := a.gpushareCollectStore(ci, "/nonexistent-container-root", ckptDir, "GPU-a\nGPU-b\n"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"chunks/ab/abcd", "ckpt/gpu-51.chunks"} {
		if b, err := os.ReadFile(filepath.Join(ckptDir, GPUShareCheckpointSubdir, f)); err != nil || string(b) != f {
			t.Errorf("%s not moved into the checkpoint: %v", f, err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(ckptDir, GPUShareCheckpointSubdir, gpushareCkptSubdir, "gpus")); err != nil || string(b) != "GPU-a\nGPU-b\n" {
		t.Errorf("GPU map = %q, %v", b, err)
	}
	left, err := os.ReadDir(podStore)
	if err != nil {
		t.Fatalf("the pod's store directory must remain: %v", err)
	}
	if len(left) != 0 {
		t.Errorf("the pod's store must be emptied, still has %d entries", len(left))
	}
}
