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
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// orchTestEnv builds an isolated, self-contained environment for one
// orchestrator test: fake /proc with cgroup + mountinfo for one PID,
// fake host FS containing the overlay upperdir + kubelet pod dirs +
// hostPath sources, plus a Local Backend.
type orchTestEnv struct {
	tempRoot    string // umbrella tempdir
	procRoot    string // fake /proc
	hostFS      string // fake "/" (host root the agent sees through HostFSRoot)
	staging     string
	backendRoot string

	pid      int
	podUID   string
	upperdir string // host-absolute path within hostFS

	backend *checkpointstore.Local
}

func newOrchTestEnv(t *testing.T) *orchTestEnv {
	t.Helper()
	root := t.TempDir()
	env := &orchTestEnv{
		tempRoot:    root,
		procRoot:    filepath.Join(root, "proc"),
		hostFS:      filepath.Join(root, "hostfs"),
		staging:     filepath.Join(root, "staging"),
		backendRoot: filepath.Join(root, "backend"),
		pid:         5000,
		podUID:      "a082a22a-2f66-4f4e-b80c-d2fc8aa4a010",
		upperdir:    "/var/lib/containerd/overlay/snapshots/4732/fs",
	}
	for _, p := range []string{env.procRoot, env.hostFS, env.staging, env.backendRoot} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b, err := checkpointstore.NewLocal(env.backendRoot)
	if err != nil {
		t.Fatal(err)
	}
	env.backend = b
	return env
}

// addProc writes /proc/<pid>/cgroup and /proc/<pid>/mountinfo.
func (e *orchTestEnv) addProc(t *testing.T, mountinfoLines string) {
	t.Helper()
	pidDir := filepath.Join(e.procRoot, fmt.Sprintf("%d", e.pid))
	if err := os.MkdirAll(pidDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cg := fmt.Sprintf("0::/kubepods/burstable/pod%s/c001/process\n", e.podUID)
	if err := os.WriteFile(filepath.Join(pidDir, "cgroup"), []byte(cg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pidDir, "mountinfo"), []byte(mountinfoLines), 0o644); err != nil {
		t.Fatal(err)
	}
}

// upperdirMountinfo returns a mountinfo line set with one overlay root + one bind mount that
// the orchestrator should treat as an exclude.
func (e *orchTestEnv) upperdirMountinfo() string {
	return fmt.Sprintf(
		// mount-id parent maj:min root mountpoint opts - fstype src superopts
		"100 99 0:1 / / rw,relatime - overlay overlay rw,upperdir=%s\n"+
			"101 100 0:2 / /etc/hostname rw,relatime - tmpfs tmpfs rw\n",
		e.upperdir,
	)
}

// addUpperdirContent populates the fake host's overlay upperdir with files
// the rootfs capture should pick up, plus files in excluded paths that
// must be skipped.
//
// Capture-side exclude list (alwaysExcludeRootfsPaths in
// orchestrator.go) drops /tmp + /etc/hostname; the fixture verifies
// both kinds (mountinfo-derived AND static) are honored.
func (e *orchTestEnv) addUpperdirContent(t *testing.T) {
	t.Helper()
	root := filepath.Join(e.hostFS, e.upperdir)
	must := func(p, s string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(filepath.Join(root, "root", "log.txt"), "vllm warm output\n")
	// Engine cache under /root/.cache — should be captured. (Pre-nvsnap#88
	// this fixture used /tmp/engine_cache, but /tmp is now in the static
	// exclude list since /tmp content is per-pod scratch.)
	must(filepath.Join(root, "root", ".cache", "engine_cache", "k1.bin"), "kernel-bytes")
	// File at an excluded mount path — must NOT appear in capture.
	must(filepath.Join(root, "etc", "hostname"), "should-be-excluded")
	// File at a static-exclude path — must NOT appear in capture.
	must(filepath.Join(root, "tmp", "scratch", "torchinductor_12345"), "per-pod-tempfile")
}

// addHostPathVolume creates a hostPath dir with content the orchestrator
// should capture as a user-data volume.
func (e *orchTestEnv) addHostPathVolume(t *testing.T, hostPath string, files map[string]string) {
	t.Helper()
	root := filepath.Join(e.hostFS, hostPath)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, body := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// addEmptyDirVolume creates the kubelet-canonical path for an emptyDir
// volume and populates it.
func (e *orchTestEnv) addEmptyDirVolume(t *testing.T, volName string, files map[string]string) {
	t.Helper()
	root := filepath.Join(e.hostFS, "var", "lib", "kubelet", "pods", e.podUID,
		"volumes", "kubernetes.io~empty-dir", volName)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, body := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func (e *orchTestEnv) capturer() *Capturer {
	return &Capturer{
		Backend:           e.backend,
		PIDResolver:       &PIDResolver{ProcRoot: e.procRoot},
		HostFSRoot:        e.hostFS,
		KubeletPodsDir:    "/var/lib/kubelet/pods",
		MountinfoProcRoot: e.procRoot,
	}
}

func TestCapture_VLLMRoundTrip(t *testing.T) {
	env := newOrchTestEnv(t)
	env.addProc(t, env.upperdirMountinfo())
	env.addUpperdirContent(t)
	env.addHostPathVolume(t, "/var/lib/containerd/nvsnap-hf-cache", map[string]string{
		"models/Llama-3.1-8B/safetensors.json": `{"version":1}`,
		"models/Llama-3.1-8B/weights.bin":      "weights-bytes",
	})

	req := CaptureRequest{
		PodUID:    env.podUID,
		Namespace: "nvsnap-system",
		Name:      "vllm-8b",
		Spec: pod("vllm/vllm-openai:v0.11.2",
			[]corev1.VolumeMount{
				{Name: "shm", MountPath: "/dev/shm"},
				{Name: "nvsnap-lib", MountPath: "/nvsnap-lib"},
				{Name: "hf-cache", MountPath: "/root/.cache/huggingface"},
			},
			[]corev1.Volume{
				{Name: "shm", VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory},
				}},
				{Name: "nvsnap-lib", VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				}},
				{Name: "hf-cache", VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/containerd/nvsnap-hf-cache"},
				}},
			}),
		MainContainer: 0,
		HashInput: checkpointstore.HashInput{
			ImageDigest:          "sha256:vllm",
			ModelID:              "Llama-3.1-8B",
			EngineCompatFlags:    []string{"--tp=2"},
			CUDADriverMajor:      580,
			CaptureFormatVersion: checkpointstore.CaptureFormatVersion,
		},
	}
	m, err := env.capturer().Capture(context.Background(), req)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}

	// Manifest sanity.
	if len(m.Volumes) != 2 {
		t.Fatalf("expected 2 captured volumes (rootfs + hf-cache), got %d: %+v", len(m.Volumes), m.Volumes)
	}
	hash := checkpointstore.ComputeHash(req.HashInput)
	treePath, err := env.backend.PathFor(hash)
	if err != nil {
		t.Fatal(err)
	}

	// Rootfs upperdir present.
	if v := mustReadFile(t, filepath.Join(treePath, "rootfs", "root", "log.txt")); v != "vllm warm output\n" {
		t.Errorf("rootfs/root/log.txt: got %q", v)
	}
	if v := mustReadFile(t, filepath.Join(treePath, "rootfs", "root", ".cache", "engine_cache", "k1.bin")); v != "kernel-bytes" {
		t.Errorf("rootfs/root/.cache/engine_cache/k1.bin: got %q", v)
	}
	// Mountinfo-derived exclude (kubelet bind-mounted /etc/hostname).
	if _, err := os.Stat(filepath.Join(treePath, "rootfs", "etc", "hostname")); !os.IsNotExist(err) {
		t.Errorf("rootfs/etc/hostname should be excluded (mountinfo), but exists or other error: %v", err)
	}
	// Static exclude (nvsnap#88: /tmp is alwaysExcludeRootfsPaths).
	if _, err := os.Stat(filepath.Join(treePath, "rootfs", "tmp", "scratch", "torchinductor_12345")); !os.IsNotExist(err) {
		t.Errorf("rootfs/tmp/.../torchinductor must be excluded (static), but exists or other error: %v", err)
	}

	// Hostpath volume present.
	if v := mustReadFile(t, filepath.Join(treePath, "volumes", "hf-cache", "models", "Llama-3.1-8B", "weights.bin")); v != "weights-bytes" {
		t.Errorf("hf-cache/.../weights.bin: got %q", v)
	}
}

func TestCapture_NIMShape_CapturesRootfsAndNimCache(t *testing.T) {
	env := newOrchTestEnv(t)
	env.addProc(t, env.upperdirMountinfo())
	env.addUpperdirContent(t) // NIM upperdir IS captured now (e.g. whisper /opt/nim/.cache)
	env.addEmptyDirVolume(t, "nim-cache", map[string]string{
		"ngc/hub/models--nim--meta--llama-3.3-70b/blobs/abc": "nim-cache-bytes",
	})

	req := CaptureRequest{
		PodUID:    env.podUID,
		Namespace: "nvsnap-system",
		Name:      "nim-llama-70b",
		Spec: pod("nvcr.io/nim/meta/llama-3.3-70b-instruct:1.15.5",
			[]corev1.VolumeMount{
				{Name: "shm", MountPath: "/dev/shm"},
				{Name: "nim-cache", MountPath: "/opt/nim/.cache"},
			},
			[]corev1.Volume{
				{Name: "shm", VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory},
				}},
				{Name: "nim-cache", VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				}},
			}),
		MainContainer: 0,
		HashInput: checkpointstore.HashInput{
			ImageDigest:          "sha256:nim",
			ModelID:              "llama-3.3-70b",
			EngineCompatFlags:    []string{"tp:2"},
			CUDADriverMajor:      580,
			CaptureFormatVersion: checkpointstore.CaptureFormatVersion,
		},
	}
	m, err := env.capturer().Capture(context.Background(), req)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	// rootfs upperdir + nim-cache both captured (NIM no longer skips rootfs).
	if len(m.Volumes) != 2 {
		t.Fatalf("expected 2 captured volumes (rootfs + nim-cache), got %d: %+v",
			len(m.Volumes), m.Volumes)
	}
	var sawRootfs, sawNimCache bool
	for _, v := range m.Volumes {
		switch v.Name {
		case "rootfs":
			sawRootfs = true
		case "nim-cache":
			sawNimCache = true
		}
	}
	if !sawRootfs || !sawNimCache {
		t.Fatalf("want both rootfs and nim-cache; got %+v", m.Volumes)
	}
	hash := checkpointstore.ComputeHash(req.HashInput)
	treePath, _ := env.backend.PathFor(hash)
	// rootfs/ MUST exist now — it's where whisper-style /opt/nim/.cache lands.
	if _, err := os.Stat(filepath.Join(treePath, "rootfs")); err != nil {
		t.Fatalf("rootfs/ must exist for NIM capture now; got: %v", err)
	}
	if v := mustReadFile(t, filepath.Join(treePath, "volumes", "nim-cache",
		"ngc", "hub", "models--nim--meta--llama-3.3-70b", "blobs", "abc")); v != "nim-cache-bytes" {
		t.Errorf("nim-cache content not captured: %q", v)
	}
}

func TestCapture_HashHitIsIdempotent(t *testing.T) {
	env := newOrchTestEnv(t)
	env.addProc(t, env.upperdirMountinfo())
	env.addUpperdirContent(t)

	capturer := env.capturer()
	req := CaptureRequest{
		PodUID:        env.podUID,
		Namespace:     "ns",
		Name:          "p",
		Spec:          pod("vllm/x", nil, nil),
		MainContainer: 0,
		HashInput: checkpointstore.HashInput{
			ImageDigest: "x", ModelID: "y", CUDADriverMajor: 1,
			CaptureFormatVersion: checkpointstore.CaptureFormatVersion,
		},
	}
	m1, err := capturer.Capture(context.Background(), req)
	if err != nil {
		t.Fatalf("first Capture: %v", err)
	}
	// Damage the host source after first capture; second Capture should not
	// touch it (idempotent fast-path via Backend.Stat hit).
	if rmErr := os.RemoveAll(filepath.Join(env.hostFS, env.upperdir)); rmErr != nil {
		t.Fatal(rmErr)
	}
	m2, err := capturer.Capture(context.Background(), req)
	if err != nil {
		t.Fatalf("second Capture: %v", err)
	}
	if m1.Hash != m2.Hash {
		t.Fatalf("idempotent capture changed hash: %q → %q", m1.Hash, m2.Hash)
	}
}

func TestCapture_PodNotRunning(t *testing.T) {
	env := newOrchTestEnv(t)
	// Don't add any /proc/<pid>/cgroup; resolver should return ErrPodNotRunning.
	req := CaptureRequest{
		PodUID:        env.podUID,
		Namespace:     "ns",
		Name:          "p",
		Spec:          pod("vllm/x", nil, nil),
		MainContainer: 0,
		HashInput:     checkpointstore.HashInput{ImageDigest: "x"},
	}
	_, err := env.capturer().Capture(context.Background(), req)
	if err == nil || !errors.Is(err, ErrPodNotRunning) {
		t.Fatalf("expected ErrPodNotRunning, got %v", err)
	}
}

func TestCapture_NilGuards(t *testing.T) {
	capturer := &Capturer{}
	_, err := capturer.Capture(context.Background(), CaptureRequest{Spec: &corev1.PodSpec{}})
	if err == nil {
		t.Fatal("expected error when Backend is nil")
	}
	backend, err := checkpointstore.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	capturer.Backend = backend
	_, err = capturer.Capture(context.Background(), CaptureRequest{Spec: &corev1.PodSpec{}})
	if err == nil {
		t.Fatal("expected error when PIDResolver is nil")
	}
	capturer.PIDResolver = &PIDResolver{ProcRoot: t.TempDir()}
	_, err = capturer.Capture(context.Background(), CaptureRequest{})
	if err == nil {
		t.Fatal("expected error when Spec is nil")
	}
}

func mustReadFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

// collectCacheEnv keeps only env vars whose value lives under the cache
// dir — the cache/model paths the engine ran with — stamped as the
// per-checkpoint SoT. Vars outside the cache dir (and the no-op cases)
// are excluded.
func TestCollectCacheEnv(t *testing.T) {
	spec := &corev1.PodSpec{Containers: []corev1.Container{{
		Name: "inference",
		Env: []corev1.EnvVar{
			{Name: "HOME", Value: "/opt/nvsnap/cache"},
			{Name: "HF_HOME", Value: "/opt/nvsnap/model"},
			{Name: "CACHE_ROOT", Value: "/opt/nvsnap"},    // == cacheDir
			{Name: "PATH", Value: "/usr/bin"},             // outside → excluded
			{Name: "CUDA_VISIBLE_DEVICES", Value: "0"},    // outside → excluded
			{Name: "DECOY", Value: "/opt/nvsnap-other/x"}, // prefix-but-not-under → excluded
		},
	}}}
	got := collectCacheEnv(spec, 0, "/opt/nvsnap")
	want := map[string]string{
		"HOME":       "/opt/nvsnap/cache",
		"HF_HOME":    "/opt/nvsnap/model",
		"CACHE_ROOT": "/opt/nvsnap",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	// No-ops: empty cacheDir, nil spec, out-of-range index → nil.
	if collectCacheEnv(spec, 0, "") != nil || collectCacheEnv(nil, 0, "/opt/nvsnap") != nil || collectCacheEnv(spec, 5, "/opt/nvsnap") != nil {
		t.Error("expected nil for empty cacheDir / nil spec / bad index")
	}
}
