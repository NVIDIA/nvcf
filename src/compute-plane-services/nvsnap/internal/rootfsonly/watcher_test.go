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
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// fakePod builds a minimal Pod object suitable for HandlePodEvent.
func fakePod(uid types.UID, name string, labels map[string]string, ready bool) *corev1.Pod {
	cond := corev1.ConditionFalse
	if ready {
		cond = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "nvsnap-system",
			Name:      name,
			UID:       uid,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			NodeName: "node-A",
			Containers: []corev1.Container{{
				Name:  "main",
				Image: "vllm/vllm-openai:v0.11.2",
				Args:  []string{"vllm serve --model meta-llama/Llama-3.1-8B-Instruct --tp 2"},
				// Multi-GPU is the rootfs path's domain; single-GPU is gated
				// out (CRIU handles those). Default to 2 GPUs so existing
				// tests exercise the rootfs path; tests that want to assert
				// the single-GPU skip override this back to 1 (or 0).
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("2"),
					},
				},
			}},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{
				Type:   corev1.PodReady,
				Status: cond,
			}},
		},
	}
}

// watcherTestEnv mirrors orchTestEnv but for watcher-level tests: a real
// Capturer + Backend + fake /proc + fake host fs, plus a fake K8s client
// (the watcher only uses the K8s client at Run() time; HandlePodEvent
// works without one).
type watcherTestEnv struct {
	*orchTestEnv
}

func newWatcherEnv(t *testing.T) *watcherTestEnv {
	t.Helper()
	return &watcherTestEnv{orchTestEnv: newOrchTestEnv(t)}
}

func (e *watcherTestEnv) watcher() *Watcher {
	return &Watcher{
		Capturer:    e.capturer(),
		Composer:    &HashInputComposer{CUDADriverMajor: 580},
		KubeClient:  fake.NewSimpleClientset(),
		NodeName:    "node-A",
		WarmupDelay: 0, // skip in tests
		Concurrency: 4,
	}
}

// TestWatcher_TriggersCaptureForReadyLabeledPod is the happy-path: pod
// labeled nvsnap.io/capture=true and Ready → capture runs.
func TestWatcher_TriggersCaptureForReadyLabeledPod(t *testing.T) {
	env := newWatcherEnv(t)
	env.addProc(t, env.upperdirMountinfo())
	env.addUpperdirContent(t)

	w := env.watcher()
	// We need sem channel; normally allocated in Run().
	w.sem = make(chan struct{}, w.concurrency())

	pod := fakePod("uid-1", "vllm-8b", map[string]string{DefaultCaptureLabel: "true"}, true)
	pod.UID = types.UID(env.podUID) // align with the fake /proc cgroup

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	w.HandlePodEvent(ctx, pod)

	// HandlePodEvent fires capture in a goroutine. Wait until Backend.Stat
	// returns a Manifest (or test times out).
	hash := checkpointstore.ComputeHash(w.Composer.Compose(pod, 0))
	if !waitFor(t, 3*time.Second, func() bool {
		_, err := env.backend.Stat(ctx, hash)
		return err == nil
	}) {
		t.Fatalf("capture never landed in backend (hash=%s)", checkpointstore.ShortHash(hash))
	}
}

// TestWatcher_DedupSamePod verifies repeated events for the same UID
// produce only one capture even when fired in rapid succession.
func TestWatcher_DedupSamePod(t *testing.T) {
	env := newWatcherEnv(t)
	env.addProc(t, env.upperdirMountinfo())
	env.addUpperdirContent(t)

	// Hook the Capturer to count Captures via a wrapper Backend.
	count := &countingBackend{Backend: env.backend}
	capturer := env.capturer()
	capturer.Backend = count
	w := env.watcher()
	w.Capturer = capturer
	w.sem = make(chan struct{}, w.concurrency())

	pod := fakePod(types.UID(env.podUID), "vllm-8b", map[string]string{DefaultCaptureLabel: "true"}, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for range 5 {
		w.HandlePodEvent(ctx, pod)
	}
	hash := checkpointstore.ComputeHash(w.Composer.Compose(pod, 0))
	if !waitFor(t, 3*time.Second, func() bool {
		_, err := count.Stat(ctx, hash)
		return err == nil
	}) {
		t.Fatalf("capture never landed")
	}
	// Allow any in-flight goroutines to finish.
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&count.put); got != 1 {
		t.Fatalf("expected 1 Backend.Put, got %d (dedup failed)", got)
	}
}

func TestWatcher_SkipsUnlabeledPod(t *testing.T) {
	env := newWatcherEnv(t)
	w := env.watcher()
	w.sem = make(chan struct{}, w.concurrency())

	pod := fakePod("uid-2", "unlabeled", nil, true) // no labels
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	w.HandlePodEvent(ctx, pod)

	// Wait briefly to make sure no capture starts.
	time.Sleep(100 * time.Millisecond)
	entries, _ := os.ReadDir(env.backendRoot)
	for _, e := range entries {
		if e.IsDir() {
			t.Fatalf("unlabeled pod produced a capture: %s", e.Name())
		}
	}
}

func TestWatcher_SkipsNotReadyPod(t *testing.T) {
	env := newWatcherEnv(t)
	w := env.watcher()
	w.sem = make(chan struct{}, w.concurrency())

	pod := fakePod("uid-3", "notready", map[string]string{DefaultCaptureLabel: "true"}, false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	w.HandlePodEvent(ctx, pod)
	time.Sleep(100 * time.Millisecond)

	entries, _ := os.ReadDir(env.backendRoot)
	for _, e := range entries {
		if e.IsDir() {
			t.Fatalf("not-ready pod produced a capture: %s", e.Name())
		}
	}
}

func TestWatcher_NilOrWrongObject(t *testing.T) {
	env := newWatcherEnv(t)
	w := env.watcher()
	w.sem = make(chan struct{}, w.concurrency())
	ctx := context.Background()
	// Should be no-ops, no panics.
	w.HandlePodEvent(ctx, nil)
	w.HandlePodEvent(ctx, "not a pod")
	w.HandlePodEvent(ctx, &corev1.ConfigMap{})
}

// TestWatcher_SingleGPUSkipped guards the architectural invariant: rootfs
// capture is the multi-GPU fallback; single-GPU pods (where CRIU works)
// must NOT be intercepted by the watcher. Regressing this loses
// in-memory state preservation that CRIU provides.
func TestWatcher_SingleGPUSkipped(t *testing.T) {
	env := newWatcherEnv(t)
	w := env.watcher()
	w.sem = make(chan struct{}, w.concurrency())

	pod := fakePod("uid-single-gpu", "vllm-small",
		map[string]string{DefaultCaptureLabel: "true"}, true)
	pod.Spec.Containers[0].Resources.Limits[corev1.ResourceName("nvidia.com/gpu")] =
		resource.MustParse("1")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	w.HandlePodEvent(ctx, pod)
	time.Sleep(100 * time.Millisecond)

	entries, _ := os.ReadDir(env.backendRoot)
	for _, e := range entries {
		if e.IsDir() {
			t.Fatalf("single-GPU pod produced a capture: %s (expected: skipped, CRIU path handles)", e.Name())
		}
	}
	if _, scheduled := w.captured.Load(pod.UID); scheduled {
		t.Fatalf("single-GPU pod should not be added to dedup map (CRIU path is independent)")
	}
}

func TestIsPodReady(t *testing.T) {
	if IsPodReady(nil) {
		t.Errorf("nil pod should not be ready")
	}
	pod := &corev1.Pod{}
	if IsPodReady(pod) {
		t.Errorf("pod with no conditions should not be ready")
	}
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
	if IsPodReady(pod) {
		t.Errorf("False condition: should not be ready")
	}
	pod.Status.Conditions[0].Status = corev1.ConditionTrue
	if !IsPodReady(pod) {
		t.Errorf("True condition: should be ready")
	}
}

func TestWatcher_FailedCaptureIsRetryable(t *testing.T) {
	// Use a Capturer with a Composer pointing at a non-existent /proc so
	// PIDResolver fails. This makes Capture return an error.
	env := newWatcherEnv(t)
	capturer := env.capturer()
	capturer.PIDResolver = &PIDResolver{ProcRoot: filepath.Join(env.tempRoot, "no-such-proc")}
	w := env.watcher()
	w.Capturer = capturer
	w.sem = make(chan struct{}, w.concurrency())

	pod := fakePod(types.UID(env.podUID), "p", map[string]string{DefaultCaptureLabel: "true"}, true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	w.HandlePodEvent(ctx, pod)
	time.Sleep(150 * time.Millisecond)

	// The dedup set should be cleared on capture failure so the next
	// event re-attempts.
	if _, scheduled := w.captured.Load(pod.UID); scheduled {
		t.Fatalf("after Capture failure, dedup set should NOT contain the UID (so retry can fire)")
	}
}

// countingBackend wraps a Backend and counts Put invocations.
type countingBackend struct {
	checkpointstore.Backend
	put int32
}

func (c *countingBackend) Put(ctx context.Context, hash string, sources []checkpointstore.CaptureSource, m checkpointstore.Manifest) (checkpointstore.Manifest, error) {
	atomic.AddInt32(&c.put, 1)
	return c.Backend.Put(ctx, hash, sources, m)
}

// waitFor polls cond every 10ms until it returns true or timeout.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// (silences "imported and not used" if k8s API import is later trimmed)
var _ = fmt.Sprintf

func TestGPUInfoFromNodeLabels(t *testing.T) {
	// GPU Operator: product + full driver version + compute capability.
	gt, dv, cc := gpuInfoFromNodeLabels(map[string]string{
		"nvidia.com/gpu.product":              "NVIDIA-H100-80GB-HBM3",
		"nvidia.com/cuda.driver-version.full": "580.95.05",
		"nvidia.com/gpu.compute.major":        "9",
		"nvidia.com/gpu.compute.minor":        "0",
	})
	if gt != "NVIDIA-H100-80GB-HBM3" || dv != "580.95.05" || cc != "9.0" {
		t.Errorf("operator labels: gpuType=%q driver=%q ccap=%q", gt, dv, cc)
	}
	// Split major/minor/rev → composed; GKE accelerator fallback for type.
	gt, dv, cc = gpuInfoFromNodeLabels(map[string]string{
		"cloud.google.com/gke-accelerator": "nvidia-h100-80gb",
		"nvidia.com/cuda.driver.major":     "580",
		"nvidia.com/cuda.driver.minor":     "95",
		"nvidia.com/cuda.driver.rev":       "05",
		"nvidia.com/gpu.compute.major":     "8",
		"nvidia.com/gpu.compute.minor":     "0",
	})
	if gt != "nvidia-h100-80gb" || dv != "580.95.05" || cc != "8.0" {
		t.Errorf("split labels: gpuType=%q driver=%q ccap=%q", gt, dv, cc)
	}
	// Nothing present → all empty (best-effort, never errors).
	if gt, dv, cc := gpuInfoFromNodeLabels(nil); gt != "" || dv != "" || cc != "" {
		t.Errorf("empty labels should yield empty: %q %q %q", gt, dv, cc)
	}
}
