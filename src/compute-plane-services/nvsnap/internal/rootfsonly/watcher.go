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
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// DefaultCaptureLabel is the pod-label key the agent watches for capture
// candidates. Operators set `nvsnap.io/capture: "true"` on workloads they
// want nvsnap to fan out across the cluster.
const DefaultCaptureLabel = "nvsnap.io/capture"

// Watcher subscribes to Pod events on the local node, detects warm pods
// (label + Ready), and triggers Capturer.Capture once per pod UID via the
// HashInputComposer. Idempotent at every layer: even if multiple events
// fire for the same pod, at most one capture runs in-process; if a stale
// capture already exists in the Backend, the orchestrator short-circuits.
//
// Field selectors restrict the informer to pods on NodeName so each agent
// only handles its own node's pods (DaemonSet pattern).
type Watcher struct {
	// Capturer + Composer must be supplied. Capturer.Backend is where
	// captures land; Composer turns pod specs into HashInputs.
	Capturer *Capturer
	Composer *HashInputComposer

	// KubeClient is the K8s API client. Required.
	KubeClient kubernetes.Interface

	// NodeName restricts the informer to pods on this node. Required for
	// DaemonSet-mode agents (the common case). Empty = watch all nodes
	// (debug/dev only — multiple agents would race on the same pod).
	NodeName string

	// Namespace narrows the watch (default: all namespaces).
	Namespace string

	// CaptureLabel is the label key whose presence + value="true" marks
	// a pod for capture. Default DefaultCaptureLabel.
	CaptureLabel string

	// WarmupDelay is the wait after Pod becomes Ready before capturing.
	// Gives the engine time to compile graphs / load weights / warm
	// kernels — capturing immediately on Ready risks a half-warmed
	// state. Default 60s.
	WarmupDelay time.Duration

	// Concurrency caps simultaneous in-flight captures. Default 2.
	// Each capture is I/O-heavy (multi-GB tar/copy); higher concurrency
	// thrashes the disk.
	Concurrency int

	// Log is the structured logger; nil disables logging.
	Log logrus.FieldLogger

	// internal state
	sem      chan struct{}
	captured sync.Map // map[types.UID]struct{} — pods already scheduled this lifetime
}

// Run starts the informer and blocks until ctx is cancelled. Returns
// ctx.Err() on cancellation, or an error if the informer fails to sync.
func (w *Watcher) Run(ctx context.Context) error {
	if err := w.validate(); err != nil {
		return err
	}
	w.sem = make(chan struct{}, w.concurrency())
	log := w.logger()

	tweakOpts := func(opts *metav1.ListOptions) {
		opts.LabelSelector = w.captureLabel() + "=true"
		if w.NodeName != "" {
			opts.FieldSelector = fields.OneTermEqualSelector("spec.nodeName", w.NodeName).String()
		}
	}

	factory := informers.NewSharedInformerFactoryWithOptions(
		w.KubeClient,
		30*time.Second,
		informers.WithNamespace(w.Namespace),
		informers.WithTweakListOptions(tweakOpts),
	)
	informer := factory.Core().V1().Pods().Informer()
	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { w.handlePodEvent(ctx, obj) },
		UpdateFunc: func(_, obj any) { w.handlePodEvent(ctx, obj) },
	}); err != nil {
		return fmt.Errorf("AddEventHandler: %w", err)
	}

	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
		return errors.New("rootfsonly.Watcher: informer cache failed to sync")
	}
	log.WithFields(logrus.Fields{
		"node":      w.NodeName,
		"namespace": w.Namespace,
		"label":     w.captureLabel(),
		"warmup":    w.WarmupDelay,
	}).Info("warm-pod watcher running")

	<-ctx.Done()
	return ctx.Err()
}

// HandlePodEvent is the testable seam. It runs the Ready-check + capture
// pipeline for a single object. Tests call this directly to skip the
// informer machinery.
func (w *Watcher) HandlePodEvent(ctx context.Context, obj any) {
	w.handlePodEvent(ctx, obj)
}

func (w *Watcher) handlePodEvent(ctx context.Context, obj any) {
	pod, ok := obj.(*corev1.Pod)
	if !ok || pod == nil {
		return
	}
	if !w.isLabeledForCapture(pod) {
		return
	}
	if !IsPodReady(pod) {
		return
	}
	// Rootfs-only capture is the multi-GPU fallback (cuda-checkpoint can't
	// handle multi-GPU; libcudart wall is upstream-blocked). Single-GPU
	// workloads usually have a working CRIU + cuda-checkpoint path that
	// preserves in-memory state (compiled CUDA graphs, JIT caches, KV
	// init) — losing that for a rootfs-only restore is a regression most
	// callers don't want by default.
	//
	// EXCEPTION: NIM workloads with Triton/Riva backends (whisper, gemma-NIM)
	// hit cuda-checkpoint's host-pinned-memory blind spot and never recover
	// post-restore (nvsnap#89, repro 2026-06-08). For those, the operator
	// stamps nvsnap.io/capture=true explicitly — that label IS the opt-in
	// signal (the watch already filters by it at line 130), so we honor it
	// even for single-GPU pods and let rootfs replace CRIU.
	gpus := podGPURequest(pod)
	if gpus < 2 {
		w.logger().WithFields(logrus.Fields{
			"pod":  pod.Namespace + "/" + pod.Name,
			"gpus": gpus,
		}).Info("rootfs-only watcher capturing single-GPU pod (explicit nvsnap.io/capture=true opt-in)")
	}
	if _, alreadyScheduled := w.captured.LoadOrStore(pod.UID, struct{}{}); alreadyScheduled {
		return
	}
	go w.runCapture(ctx, pod.DeepCopy())
}

// podGPURequest sums nvidia.com/gpu across all containers' resources.limits.
// Returns 0 if no GPU is requested. Mirrors how K8s schedules: requests +
// limits agree for extended resources, so reading limits is sufficient.
func podGPURequest(pod *corev1.Pod) int64 {
	var total int64
	const gpuKey = corev1.ResourceName("nvidia.com/gpu")
	for i := range pod.Spec.Containers {
		c := &pod.Spec.Containers[i]
		if q, ok := c.Resources.Limits[gpuKey]; ok {
			total += q.Value()
		}
	}
	return total
}

// runCapture sleeps WarmupDelay, acquires a concurrency slot, and runs Capture.
// Failures are logged but don't tear down the watcher; transient errors are
// retried by re-firing on subsequent Pod Update events (we clear captured
// on persistent error so the next event re-tries).
func (w *Watcher) runCapture(ctx context.Context, pod *corev1.Pod) {
	log := w.logger().WithFields(logrus.Fields{
		"pod":     pod.Namespace + "/" + pod.Name,
		"pod_uid": string(pod.UID),
	})
	if w.WarmupDelay > 0 {
		select {
		case <-time.After(w.WarmupDelay):
		case <-ctx.Done():
			return
		}
	}
	select {
	case w.sem <- struct{}{}:
		defer func() { <-w.sem }()
	case <-ctx.Done():
		return
	}

	// Bound the capture to a sane upper limit; large workloads (vllm-70b)
	// take 5–10 min for tar+rsync, so 30 min is generous.
	captureCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	hashInput := w.Composer.Compose(pod, 0)
	req := CaptureRequest{
		PodUID:        string(pod.UID),
		Namespace:     pod.Namespace,
		Name:          pod.Name,
		Spec:          &pod.Spec,
		MainContainer: 0,
		HashInput:     hashInput,
	}
	// Resolve GPU metadata from the capture node's labels (best-effort;
	// empty on lookup failure). GPUType/DriverVersion are display-only;
	// the compute capability feeds the content hash (arch-specific caches
	// must not be reused across compute capabilities).
	if nodeName := pod.Spec.NodeName; nodeName != "" {
		if node, err := w.KubeClient.CoreV1().Nodes().Get(captureCtx, nodeName, metav1.GetOptions{}); err == nil {
			var ccap string
			req.GPUType, req.DriverVersion, ccap = gpuInfoFromNodeLabels(node.Labels)
			req.HashInput.GPUComputeCapability = ccap
		} else {
			log.WithError(err).Debug("node lookup for GPU metadata failed; capture proceeds without it")
		}
	}
	m, err := w.Capturer.Capture(captureCtx, req)
	if err != nil {
		log.WithError(err).Warn("capture failed; will retry on next Update event")
		// Allow retry on subsequent events.
		w.captured.Delete(pod.UID)
		return
	}
	log.WithFields(logrus.Fields{
		"hash":     m.Hash[:12],
		"size_mib": m.TotalSizeBytes / 1024 / 1024,
		"files":    m.FileCount,
	}).Info("capture committed for pod")
}

func (w *Watcher) isLabeledForCapture(pod *corev1.Pod) bool {
	if pod.Labels == nil {
		return false
	}
	v, ok := pod.Labels[w.captureLabel()]
	return ok && v == "true"
}

// IsPodReady reports whether pod's PodReady condition is True.
func IsPodReady(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func (w *Watcher) validate() error {
	if w.Capturer == nil {
		return errors.New("rootfsonly.Watcher: Capturer is nil")
	}
	if w.Composer == nil {
		return errors.New("rootfsonly.Watcher: Composer is nil")
	}
	if w.KubeClient == nil {
		return errors.New("rootfsonly.Watcher: KubeClient is nil")
	}
	return nil
}

func (w *Watcher) captureLabel() string {
	if w.CaptureLabel != "" {
		return w.CaptureLabel
	}
	return DefaultCaptureLabel
}

func (w *Watcher) concurrency() int {
	if w.Concurrency > 0 {
		return w.Concurrency
	}
	return 2
}

func (w *Watcher) logger() logrus.FieldLogger {
	if w.Log != nil {
		return w.Log
	}
	return logrus.NewEntry(logrus.New()).WithField("subsys", "rootfsonly.watcher")
}

// EnsureRetryable removes pod UID from the dedup set so a subsequent event
// will re-attempt capture. Public for the agent loop to call after manual
// retry triggers (e.g. user-driven). Tests use the same hook.
func (w *Watcher) EnsureRetryable(uid types.UID) {
	w.captured.Delete(uid)
}

// gpuInfoFromNodeLabels extracts display-only GPU product name and full
// driver version from a node's labels. Mirrors the CRIU path's
// populateFromNode label keys: the GPU Operator stamps
// nvidia.com/gpu.product + nvidia.com/cuda.driver-version.full (or the
// split major/minor/rev labels); GKE managed nodes use
// cloud.google.com/gke-accelerator. Returns empty strings for whatever
// isn't present. Pure (map in, strings out) for unit testing.
func gpuInfoFromNodeLabels(labels map[string]string) (gpuType, driverVersion, computeCapability string) {
	if v := labels["nvidia.com/gpu.product"]; v != "" {
		gpuType = v
	} else if v := labels["cloud.google.com/gke-accelerator"]; v != "" {
		gpuType = v
	}
	if v := labels["nvidia.com/cuda.driver-version.full"]; v != "" {
		driverVersion = v
	} else if major := labels["nvidia.com/cuda.driver.major"]; major != "" {
		driverVersion = major
		if minor := labels["nvidia.com/cuda.driver.minor"]; minor != "" {
			driverVersion += "." + minor
		}
		if rev := labels["nvidia.com/cuda.driver.rev"]; rev != "" {
			driverVersion += "." + rev
		}
	}
	// Compute capability "<major>.<minor>" (e.g. "9.0" for H100). The GPU
	// Operator's GFD stamps these. Feeds the content hash — the kernel/
	// engine compatibility boundary, NOT the product SKU string.
	if major := labels["nvidia.com/gpu.compute.major"]; major != "" {
		computeCapability = major
		if minor := labels["nvidia.com/gpu.compute.minor"]; minor != "" {
			computeCapability += "." + minor
		}
	}
	return gpuType, driverVersion, computeCapability
}
