/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	corev1 "k8s.io/api/core/v1"
)

// Annotation keys NVCA's Hook A stamps. Duplicated as constants here
// rather than imported from pkg/nvca to avoid a controller → nvca →
// controller cycle through nvsnap_controller_start.go. The values are
// stable contract — keep in sync with pkg/nvca/nvsnap_hook.go.
const (
	restoreFromAnnotation       = "nvsnap.io/restore-from"
	checkpointOnWarmAnnotation  = "nvsnap.io/checkpoint-on-warm"
	functionVersionIDAnnotation = "nvsnap.io/function-version-id"
)

var _ = functionVersionIDAnnotation // reserved for future per-fvID metric labeling

// Prometheus metrics for the NvSnap × NVCA observability story.
//
// nvca_nvsnap_pod_first_ready_seconds is the headline customer-facing
// number — the histogram comparing how long pods take to become
// Ready, split by whether NVCA's Hook A stamped a restore-from
// annotation on them. A side-by-side Grafana panel on this histogram
// is the "is NvSnap actually helping?" answer.
//
// Cardinality budget: only the `restored` label is exposed. We don't
// label by function_version_id even though the data exists — operators
// running thousands of FVs would blow the metric out, and per-FV
// drill-down is better served by ad-hoc PromQL with pod-label joins
// (kube-state-metrics gives us those).
var (
	podFirstReadySeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "nvca_nvsnap_pod_first_ready_seconds",
			Help: "Wall-clock seconds from Pod CreationTimestamp to first PodReady=True, " +
				"split by whether NVCA's Hook A stamped nvsnap.io/restore-from on the pod. " +
				"Cold-start vs NvSnap-restored side by side.",
			// Buckets tuned for the typical span: a NvSnap restore in
			// ~30-60s, a cold start in 1-5 minutes for a CPU function
			// and 5-20 minutes for a GPU function pulling a multi-GB
			// model. Wide tail catches misbehaving cold-starts.
			Buckets: []float64{5, 10, 20, 30, 60, 120, 180, 300, 600, 900, 1800},
		},
		[]string{"restored"},
	)

	podFirstReadyTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nvca_nvsnap_pods_first_ready_total",
			Help: "Count of pod first-ready observations, partitioned by mode. " +
				"The restored counter is the cold-starts you avoided.",
		},
		[]string{"mode"},
	)
)

// observePodFirstReady records a histogram observation + counter
// increment when newPod has just transitioned to PodReady=True
// (i.e., oldPod was not Ready, newPod is). No-ops in every other
// case, so safe to call from every UpdateFunc invocation.
//
// Pods without any NvSnap annotation (nvsnap.io/restore-from OR
// nvsnap.io/checkpoint-on-warm) are ignored — non-NVCA pods don't
// belong in this telemetry. This also avoids inflating the metric
// with kube-system / DaemonSet pods that share the same informer.
//
// Latency = newPod.PodReady.LastTransitionTime - newPod.CreationTimestamp.
// Using LastTransitionTime (rather than time.Now()) keeps the
// reported value stable across controller restarts: if the
// controller misses the Ready transition and catches up later,
// it still reports the true latency, not the catch-up delay.
func observePodFirstReady(oldPod, newPod *corev1.Pod) {
	if newPod == nil || !podIsNvSnapTouched(newPod) {
		return
	}
	if !isPodReadyTransition(oldPod, newPod) {
		return
	}
	created := newPod.CreationTimestamp.Time
	readyAt, ok := podReadyTransitionTime(newPod)
	if !ok || created.IsZero() {
		return
	}
	latency := readyAt.Sub(created).Seconds()
	if latency < 0 {
		// Shouldn't happen — defensive: a negative would skew the
		// histogram and be visible as a long tail. Drop.
		return
	}

	mode := "cold-start"
	restoredLabel := "false"
	if rf := newPod.Annotations[restoreFromAnnotation]; rf != "" {
		mode = "restored"
		restoredLabel = "true"
	}
	podFirstReadySeconds.WithLabelValues(restoredLabel).Observe(latency)
	podFirstReadyTotal.WithLabelValues(mode).Inc()
}

// podIsNvSnapTouched reports whether NVCA's Hook A stamped any
// annotation on the pod (restore-from for the restored path,
// checkpoint-on-warm for the cold-start path). Either marks the
// pod as "in scope" for our histogram.
func podIsNvSnapTouched(p *corev1.Pod) bool {
	if p == nil || p.Annotations == nil {
		return false
	}
	if v := p.Annotations[restoreFromAnnotation]; v != "" {
		return true
	}
	if v := p.Annotations[checkpointOnWarmAnnotation]; v != "" {
		return true
	}
	return false
}

// isPodReadyTransition returns true when newPod is Ready=True AND
// oldPod was not (nil oldPod counts as a transition — that's the
// "controller just started, caught a pod already Ready" path).
func isPodReadyTransition(oldPod, newPod *corev1.Pod) bool {
	if !podReadyTrue(newPod) {
		return false
	}
	if oldPod == nil {
		return true
	}
	return !podReadyTrue(oldPod)
}

func podReadyTrue(p *corev1.Pod) bool {
	if p == nil {
		return false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func podReadyTransitionTime(p *corev1.Pod) (time.Time, bool) {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return c.LastTransitionTime.Time, true
		}
	}
	return time.Time{}, false
}
