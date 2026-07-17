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

// Package cacheprobe runs runtime CSI capability probes against a Helm model
// cache storage class to decide the reader access-mode strategy (ROX preferred,
// RWX fallback) for non-NVMesh shared-filesystem backends (nvcf-miniservice-sc).
package cacheprobe

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// ProbeTimeout bounds how long a single probe waits for its pod to run.
	ProbeTimeout = 60 * time.Second
	// ProbePollInterval is the probe pod status poll cadence.
	ProbePollInterval = 2 * time.Second
	// ProbePVCSize is the (tiny) size requested for probe PVCs.
	ProbePVCSize = "1Gi"
	// UnsupportedResultTTLSeconds caps the TTL of Unsupported probe results. A
	// negative result cannot be distinguished from a transient failure (CSI
	// driver briefly unavailable, apiserver timeout, node pressure), so honoring
	// the full positive TTL would let one bad probe poison every storage request
	// for that whole window. Re-probe soon instead; a genuinely unsupported
	// class just re-fails a cheap probe every few minutes.
	UnsupportedResultTTLSeconds = 300
	// DefaultProbeImage is the image used by the probe pod. Override via
	// Prober.Image for air-gapped clusters.
	DefaultProbeImage = "busybox:1.36"

	managedByLabel = "app.kubernetes.io/managed-by"
	managedByValue = "nvca"
	probeLabel     = "cache.nvcf.nvidia.com/probe"
)

// ProbeState represents the result of a capability probe.
type ProbeState string

const (
	StateUnknown     ProbeState = "Unknown"
	StateProbing     ProbeState = "Probing"
	StateSupported   ProbeState = "Supported"
	StateUnsupported ProbeState = "Unsupported"
)

// AccessModeStrategy is the chosen read strategy based on probe results.
type AccessModeStrategy string

const (
	// StrategyROX: readers mount ReadOnlyMany (preferred).
	StrategyROX AccessModeStrategy = "ROX"
	// StrategyRWX: readers mount ReadWriteMany in read-only mode.
	StrategyRWX AccessModeStrategy = "RWX"
	// StrategyFallback: no shared access mode works; caller falls back to
	// non-shared (per-pod) behavior.
	StrategyFallback AccessModeStrategy = "Fallback"
)

// Result holds the outcome of probing a single access mode.
type Result struct {
	State     ProbeState `json:"state"`
	CheckedAt time.Time  `json:"checkedAt"`
	Reason    string     `json:"reason,omitempty"`
	TTL       int        `json:"ttlSeconds"`
}

// Prober performs runtime CSI capability probes against a storage class by
// creating a tiny PVC + pod for each candidate access mode.
type Prober struct {
	client           client.Client
	namespace        string
	storageClassName string
	ttlSeconds       int
	// Image is the probe pod image; defaults to DefaultProbeImage.
	Image string
}

// NewProber creates a Prober for the given storage class.
func NewProber(c client.Client, namespace, storageClassName string, ttlSeconds int) *Prober {
	return &Prober{
		client:           c,
		namespace:        namespace,
		storageClassName: storageClassName,
		ttlSeconds:       ttlSeconds,
		Image:            DefaultProbeImage,
	}
}

// ProbeAccessMode tests whether the storage class supports the given access
// mode by creating a tiny PVC and a pod that mounts it; Supported if the pod
// reaches Running/Succeeded within the timeout. Probe resources are always
// cleaned up.
func (p *Prober) ProbeAccessMode(ctx context.Context, mode corev1.PersistentVolumeAccessMode) Result {
	logger := log.FromContext(ctx).WithValues("storageClass", p.storageClassName, "accessMode", string(mode))
	logger.Info("starting CSI capability probe")

	suffix := accessModeSuffix(mode)
	pvcName := fmt.Sprintf("probe-%s-%s", p.storageClassName, suffix)
	podName := fmt.Sprintf("probe-%s-%s", p.storageClassName, suffix)

	result := Result{State: StateProbing, CheckedAt: time.Now(), TTL: p.ttlSeconds}

	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		p.cleanupProbe(cleanupCtx, podName, pvcName)
	}()

	// Unsupported results carry a capped TTL (see UnsupportedResultTTLSeconds):
	// a failed probe may be a transient environment problem, not a property of
	// the storage class, and must not suppress re-probing for the full window.
	if err := p.createProbePVC(ctx, pvcName, mode); err != nil {
		result.State, result.Reason = StateUnsupported, fmt.Sprintf("PVC creation failed: %v", err)
		result.TTL = min(p.ttlSeconds, UnsupportedResultTTLSeconds)
		return result
	}
	if err := p.createProbePod(ctx, podName, pvcName); err != nil {
		result.State, result.Reason = StateUnsupported, fmt.Sprintf("Pod creation failed: %v", err)
		result.TTL = min(p.ttlSeconds, UnsupportedResultTTLSeconds)
		return result
	}
	if err := p.waitForPodRunning(ctx, podName); err != nil {
		logger.Info("probe pod did not reach Running", "error", err)
		result.State, result.Reason = StateUnsupported, fmt.Sprintf("Pod did not reach Running: %v", err)
		result.TTL = min(p.ttlSeconds, UnsupportedResultTTLSeconds)
		return result
	}

	logger.Info("probe succeeded")
	result.State = StateSupported
	return result
}

// DetermineStrategy probes ROX then RWX and returns the best supported strategy
// (or StrategyFallback), along with the per-mode results keyed by
// "<storageClass>_<MODE>".
func (p *Prober) DetermineStrategy(ctx context.Context) (AccessModeStrategy, map[string]Result) {
	results := make(map[string]Result)

	rox := p.ProbeAccessMode(ctx, corev1.ReadOnlyMany)
	results[ResultKey(p.storageClassName, StrategyROX)] = rox
	if rox.State == StateSupported {
		return StrategyROX, results
	}

	rwx := p.ProbeAccessMode(ctx, corev1.ReadWriteMany)
	results[ResultKey(p.storageClassName, StrategyRWX)] = rwx
	if rwx.State == StateSupported {
		return StrategyRWX, results
	}

	return StrategyFallback, results
}

// ResultKey is the map/ConfigMap key for a storage class + strategy result.
func ResultKey(storageClassName string, strategy AccessModeStrategy) string {
	return fmt.Sprintf("%s_%s", storageClassName, strategy)
}

func (p *Prober) probeLabels() map[string]string {
	return map[string]string{managedByLabel: managedByValue, probeLabel: "true"}
}

func (p *Prober) createProbePVC(ctx context.Context, name string, mode corev1.PersistentVolumeAccessMode) error {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: p.namespace, Labels: p.probeLabels()},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{mode},
			StorageClassName: &p.storageClassName,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(ProbePVCSize)},
			},
		},
	}
	if err := p.client.Create(ctx, pvc); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (p *Prober) createProbePod(ctx context.Context, name, pvcName string) error {
	image := p.Image
	if image == "" {
		image = DefaultProbeImage
	}
	automount := false
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: p.namespace, Labels: p.probeLabels()},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: &automount,
			RestartPolicy:                corev1.RestartPolicyNever,
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot: boolPtr(true),
				RunAsUser:    int64Ptr(1000),
				RunAsGroup:   int64Ptr(1000),
				FSGroup:      int64Ptr(1000),
			},
			Containers: []corev1.Container{{
				Name:  "probe",
				Image: image,
				// Exit immediately: a successful mount + start is the signal.
				// waitForPodRunning accepts PodSucceeded, so there is no need to
				// keep the pod alive (which would only delay probe cleanup).
				Command: []string{"sh", "-c", "echo probe-ok"},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("10m"),
						corev1.ResourceMemory: resource.MustParse("16Mi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("50m"),
						corev1.ResourceMemory: resource.MustParse("32Mi"),
					},
				},
				SecurityContext: &corev1.SecurityContext{
					ReadOnlyRootFilesystem: boolPtr(true),
					Capabilities:           &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
				VolumeMounts: []corev1.VolumeMount{{Name: "probe-vol", MountPath: "/mnt/probe"}},
			}},
			Volumes: []corev1.Volume{{
				Name: "probe-vol",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcName},
				},
			}},
		},
	}
	if err := p.client.Create(ctx, pod); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (p *Prober) waitForPodRunning(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()

	return wait.PollUntilContextCancel(ctx, ProbePollInterval, true, func(ctx context.Context) (bool, error) {
		pod := &corev1.Pod{}
		if err := p.client.Get(ctx, client.ObjectKey{Namespace: p.namespace, Name: name}, pod); err != nil {
			return false, nil
		}
		switch pod.Status.Phase {
		case corev1.PodRunning, corev1.PodSucceeded:
			return true, nil
		case corev1.PodFailed:
			return false, fmt.Errorf("probe pod failed: %s", pod.Status.Message)
		default:
			return false, nil
		}
	})
}

func (p *Prober) cleanupProbe(ctx context.Context, podName, pvcName string) {
	logger := log.FromContext(ctx)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: p.namespace}}
	if err := p.client.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
		logger.Error(err, "failed to delete probe pod", "pod", podName)
	}

	// Record the bound PV before deleting the PVC so it can be deleted
	// explicitly below.
	boundPVName := ""
	pvc := &corev1.PersistentVolumeClaim{}
	if err := p.client.Get(ctx, client.ObjectKey{Namespace: p.namespace, Name: pvcName}, pvc); err == nil {
		boundPVName = pvc.Spec.VolumeName
	} else if !apierrors.IsNotFound(err) {
		logger.Error(err, "failed to get probe PVC before cleanup", "pvc", pvcName)
	}
	pvc = &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: pvcName, Namespace: p.namespace}}
	if err := p.client.Delete(ctx, pvc); err != nil && !apierrors.IsNotFound(err) {
		logger.Error(err, "failed to delete probe PVC", "pvc", pvcName)
	}

	// A storage class with reclaimPolicy Delete removes the PV with the PVC,
	// but a Retain class leaves one Released PV object per probe. Delete the
	// PV explicitly; NotFound is the normal Delete-reclaim case.
	if boundPVName != "" {
		pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: boundPVName}}
		if err := p.client.Delete(ctx, pv); err != nil && !apierrors.IsNotFound(err) {
			logger.Error(err, "failed to delete probe PV", "pv", boundPVName)
		}
	}
}

func accessModeSuffix(mode corev1.PersistentVolumeAccessMode) string {
	switch mode {
	case corev1.ReadOnlyMany:
		return "rox"
	case corev1.ReadWriteMany:
		return "rwx"
	default:
		return "unknown"
	}
}

func boolPtr(b bool) *bool    { return &b }
func int64Ptr(i int64) *int64 { return &i }
