// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package webhook

// A configuration CRIU cannot serve falls back to the caches
// (docs/design/CRIU-CAPTURE-FALLBACK.md §2.2 and §2.7). Its group record
// says why: the capture failed, its restore failed, or the workload does
// not start under the gpushare library. Its GPU pods then get the
// cachedir a cluster without CRIU gives them, the agent on each rank's
// node captures that rank's cachedir into the checkpoint directory, and a
// later pod of the rank is seeded from the capture, fetched to its node
// the way a checkpoint is.

import (
	"context"
	"path"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

const (
	// CaptureMethodAnnotation names how a pod of a CRIU cluster is captured
	// when it is not by CRIU: "cachedir" for a fallback pod.
	CaptureMethodAnnotation = "nvsnap.io/capture-method"
	// FallbackReasonAnnotation is the group record state that made the pod
	// fall back; the agent captures the cachedir of a pod that carries it.
	FallbackReasonAnnotation = "nvsnap.io/fallback-reason"
	// FallbackCaptureAnnotation names the cachedir capture a fallback pod
	// is seeded from; the agent on its node fetches it.
	FallbackCaptureAnnotation = "nvsnap.io/fallback-capture"

	// FallbackGPUShareFailed is the reason under which the gpushare library
	// is left out: the workload does not start under it.
	FallbackGPUShareFailed = "gpushare-failed"

	// FallbackCapturePrefix starts the name of every cachedir capture in
	// the checkpoint directory: cachedir-<configuration key>-<rank>.
	FallbackCapturePrefix = "cachedir-"
	// FallbackReadySubdir holds, under the checkpoint directory, one
	// directory per capture; the agent writes FallbackReadyFile in it once
	// the capture is whole on the node. A pod mounts only its own.
	FallbackReadySubdir = ".cachedir-ready"
	// FallbackReadyFile is the marker in it.
	FallbackReadyFile = "ready"
	// FallbackManifestFile is the file beside a capture's tree listing
	// what it holds: its files, directories and symbolic links, which the
	// seed makes.
	FallbackManifestFile = "nvsnap-cachedir.json"
	// FallbackCaptureTree is the cachedir's copy inside a capture, beside
	// the capture's manifest.
	FallbackCaptureTree = "tree"

	fallbackCaptureVolume = "nvsnap-fallback-capture"
	fallbackReadyVolume   = "nvsnap-fallback-ready"
	fallbackCaptureMount  = "/nvsnap-fallback-capture"
	fallbackReadyMount    = "/nvsnap-fallback-ready"
	fallbackSeedInit      = "nvsnap-seed-fallback"
	// fallbackSeedDeadline bounds the wait for the capture to reach the
	// node, in seconds: past it the engine starts as it would have,
	// compiling.
	fallbackSeedDeadline = 900
)

// CRIUFallback is why a configuration falls back, and the capture of the
// pod's rank to seed it from.
type CRIUFallback struct {
	// Reason is the group record state: failed, restore-failed or
	// gpushare-failed.
	Reason string
	// Capture is the complete cachedir capture of the pod's rank, a
	// directory of the checkpoint directory; "" for none yet.
	Capture string
}

// FallbackCaptureID is the name of the cachedir capture of rank ordinal of
// the configuration whose group record key is key.
func FallbackCaptureID(key string, ordinal int) string {
	return FallbackCapturePrefix + key + "-" + strconv.Itoa(ordinal)
}

// validFallbackCapture reports a capture name the webhook may turn into a
// host path: one segment, of the capture prefix.
func validFallbackCapture(id string) bool {
	return strings.HasPrefix(id, FallbackCapturePrefix) && !strings.ContainsAny(id, "/\x00") && id != "." && id != ".."
}

// criuFallback reports whether pod's configuration falls back, and how.
// Only a GPU pod admitted fresh on a CRIU cluster can: a restore
// placeholder is CRIU's.
func (m *Mutator) criuFallback(ctx context.Context, pod *corev1.Pod) (CRIUFallback, bool) {
	if !m.CRIUCapture || m.CRIUFallback == nil || m.CacheDir == "" || gpuRequest(pod) == 0 {
		return CRIUFallback{}, false
	}
	if pod.Annotations[RestoreFromAnnotation] != "" || pod.Annotations[CRIUGroupAnnotation] != "" {
		return CRIUFallback{}, false
	}
	uri, ok := m.cacheURI(pod)
	if !ok {
		return CRIUFallback{}, false
	}
	fb, ok := m.CRIUFallback(ctx, pod.Namespace, uri, podOrdinal(pod))
	if !ok || fb.Reason == "" {
		return CRIUFallback{}, false
	}
	if fb.Capture != "" && !validFallbackCapture(fb.Capture) {
		fb.Capture = ""
	}
	return fb, true
}

type criuFallbackKey struct{}

// withCRIUFallback carries the admission's fallback decision to the
// branches that decorate the pod's cachedir.
func withCRIUFallback(ctx context.Context, fb CRIUFallback) context.Context {
	return context.WithValue(ctx, criuFallbackKey{}, fb)
}

// criuFallbackOf returns the admission's fallback decision, if any.
func criuFallbackOf(ctx context.Context) (CRIUFallback, bool) {
	fb, ok := ctx.Value(criuFallbackKey{}).(CRIUFallback)
	return fb, ok
}

// fallbackPatches stamps a fallback pod and, when its rank has a complete
// capture, seeds its cachedir from it: the capture's directory on the node
// and its ready directory are mounted into an init container that waits
// for the agent to have the capture whole on the node, then copies it into
// the cachedir. The pod's containers never see the checkpoint directory.
// The cachedir volume and its mount are the caller's.
func (m *Mutator) fallbackPatches(pod *corev1.Pod, main *corev1.Container, fb CRIUFallback) []PatchOp {
	mp := newMetaPatcher(pod)
	patches := mp.annotation(CaptureMethodAnnotation, "cachedir")
	patches = append(patches, mp.annotation(FallbackReasonAnnotation, fb.Reason)...)
	if fb.Capture == "" {
		return patches
	}
	root := m.CheckpointHostRoot
	if root == "" {
		return patches
	}
	patches = append(patches, mp.annotation(FallbackCaptureAnnotation, fb.Capture)...)
	orCreate := corev1.HostPathDirectoryOrCreate
	vols := []corev1.Volume{
		{Name: fallbackCaptureVolume, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
			Path: path.Join(root, fb.Capture), Type: &orCreate}}},
		{Name: fallbackReadyVolume, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
			Path: path.Join(root, FallbackReadySubdir, fb.Capture), Type: &orCreate}}},
	}
	for i := range vols {
		patches = append(patches, PatchOp{Op: "add", Path: "/spec/volumes/-", Value: vols[i]})
	}
	// Root reads the capture whatever owners the fetch left; the copy is
	// the pod's scratch and is opened to the engine's user.
	seed := seedStep(fallbackSeedInit, main, volumeAt{Volume: fallbackCaptureVolume, Path: fallbackCaptureMount}, FallbackCaptureTree,
		volumeAt{Volume: cacheDirVolumeName, Path: m.CacheDir}, "", "fallback cache "+fb.Capture, rootPosture, false)
	seed.Command = append(seed.Command, "--marker", path.Join(fallbackReadyMount, FallbackReadyFile),
		"--deadline", strconv.Itoa(fallbackSeedDeadline), "--links", path.Join(fallbackCaptureMount, FallbackManifestFile))
	seed.VolumeMounts = append(seed.VolumeMounts, corev1.VolumeMount{Name: fallbackReadyVolume, MountPath: fallbackReadyMount, ReadOnly: true})
	return m.withHelperVolume(pod, appendInits(pod, patches, seed))
}
