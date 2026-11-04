/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package controller

import (
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func mkPodWithReady(annotations map[string]string, created time.Time, ready bool, readyAt time.Time) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations:       annotations,
			CreationTimestamp: metav1.NewTime(created),
		},
	}
	if ready {
		p.Status.Conditions = []corev1.PodCondition{{
			Type:               corev1.PodReady,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(readyAt),
		}}
	} else {
		// Pod has the condition but Status=False (not yet ready).
		// Mirrors what kubelet sets while warming.
		p.Status.Conditions = []corev1.PodCondition{{
			Type:   corev1.PodReady,
			Status: corev1.ConditionFalse,
		}}
	}
	return p
}

// counterByMode reads the current counter value for a given mode
// label off the global registry. Tests assert by delta to remain
// independent of other tests running first.
func counterByMode(t *testing.T, mode string) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := podFirstReadyTotal.WithLabelValues(mode).Write(m); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	return m.GetCounter().GetValue()
}

func histSampleCountByRestored(t *testing.T, restored string) uint64 {
	t.Helper()
	m := &dto.Metric{}
	if err := podFirstReadySeconds.WithLabelValues(restored).(interface {
		Write(*dto.Metric) error
	}).Write(m); err != nil {
		t.Fatalf("read histogram: %v", err)
	}
	return m.GetHistogram().GetSampleCount()
}

func TestObserve_RestoredPodTransition(t *testing.T) {
	beforeCount := counterByMode(t, "restored")
	beforeHist := histSampleCountByRestored(t, "true")

	created := time.Date(2026, 5, 31, 22, 56, 0, 0, time.UTC)
	old := mkPodWithReady(map[string]string{
		restoreFromAnnotation:       "85ec4d75ee57c1be...",
		functionVersionIDAnnotation: "fv-1",
	}, created, false, time.Time{})
	new := mkPodWithReady(map[string]string{
		restoreFromAnnotation:       "85ec4d75ee57c1be...",
		functionVersionIDAnnotation: "fv-1",
	}, created, true, created.Add(43*time.Second))

	observePodFirstReady(old, new)

	if got := counterByMode(t, "restored"); got != beforeCount+1 {
		t.Errorf("restored counter delta = %v, want +1", got-beforeCount)
	}
	if got := histSampleCountByRestored(t, "true"); got != beforeHist+1 {
		t.Errorf("histogram(restored=true) sample-count delta = %v, want +1", got-beforeHist)
	}
}

func TestObserve_ColdStartPodTransition(t *testing.T) {
	beforeCount := counterByMode(t, "cold-start")
	beforeHist := histSampleCountByRestored(t, "false")

	created := time.Date(2026, 5, 31, 22, 50, 0, 0, time.UTC)
	old := mkPodWithReady(map[string]string{
		checkpointOnWarmAnnotation:  "true",
		functionVersionIDAnnotation: "fv-1",
	}, created, false, time.Time{})
	new := mkPodWithReady(map[string]string{
		checkpointOnWarmAnnotation:  "true",
		functionVersionIDAnnotation: "fv-1",
	}, created, true, created.Add(188*time.Second))

	observePodFirstReady(old, new)

	if got := counterByMode(t, "cold-start"); got != beforeCount+1 {
		t.Errorf("cold-start counter delta = %v, want +1", got-beforeCount)
	}
	if got := histSampleCountByRestored(t, "false"); got != beforeHist+1 {
		t.Errorf("histogram(restored=false) sample-count delta = %v, want +1", got-beforeHist)
	}
}

func TestObserve_PodAlreadyReadyOnOldDoesNotDoubleCount(t *testing.T) {
	// Both old and new are Ready=True → no transition, no observation.
	// Without this guard, every status field update on a Ready pod
	// would re-observe and inflate the histogram.
	before := counterByMode(t, "restored")

	created := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	old := mkPodWithReady(map[string]string{restoreFromAnnotation: "h"}, created, true, created.Add(43*time.Second))
	new := mkPodWithReady(map[string]string{restoreFromAnnotation: "h"}, created, true, created.Add(43*time.Second))

	observePodFirstReady(old, new)

	if got := counterByMode(t, "restored"); got != before {
		t.Errorf("counter advanced on no-op update; delta=%v", got-before)
	}
}

func TestObserve_NilOldPodTreatedAsTransition(t *testing.T) {
	// Pod was already Ready when the informer first saw it (controller
	// restart / cache resync). We still observe — under-counting would
	// systematically hide the savings after every controller bounce.
	before := counterByMode(t, "restored")

	created := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	new := mkPodWithReady(map[string]string{restoreFromAnnotation: "h"}, created, true, created.Add(43*time.Second))

	observePodFirstReady(nil, new)

	if got := counterByMode(t, "restored"); got != before+1 {
		t.Errorf("nil-old counter delta = %v, want +1", got-before)
	}
}

func TestObserve_PodWithoutNvSnapAnnotationsIgnored(t *testing.T) {
	// kube-system / unrelated pods that happen to share the informer
	// should NOT be counted. The annotation predicate is the only
	// filter that keeps us out of those metrics.
	before := counterByMode(t, "cold-start")

	created := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	old := mkPodWithReady(nil, created, false, time.Time{})
	new := mkPodWithReady(nil, created, true, created.Add(60*time.Second))

	observePodFirstReady(old, new)

	if got := counterByMode(t, "cold-start"); got != before {
		t.Errorf("non-NVCA pod was counted; delta=%v", got-before)
	}
}

func TestObserve_NewPodNotReadyIsNoOp(t *testing.T) {
	before := counterByMode(t, "restored")

	created := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	old := mkPodWithReady(map[string]string{restoreFromAnnotation: "h"}, created, false, time.Time{})
	new := mkPodWithReady(map[string]string{restoreFromAnnotation: "h"}, created, false, time.Time{})

	observePodFirstReady(old, new)

	if got := counterByMode(t, "restored"); got != before {
		t.Errorf("not-ready pod was counted; delta=%v", got-before)
	}
}

func TestObserve_NegativeLatencyDropped(t *testing.T) {
	// Defensive: a malformed pod with readyAt < createdAt would skew
	// the histogram badly. We drop, not panic.
	before := histSampleCountByRestored(t, "true")

	created := time.Date(2026, 5, 31, 22, 0, 0, 0, time.UTC)
	old := mkPodWithReady(map[string]string{restoreFromAnnotation: "h"}, created, false, time.Time{})
	new := mkPodWithReady(map[string]string{restoreFromAnnotation: "h"}, created, true, created.Add(-5*time.Second))

	observePodFirstReady(old, new)

	if got := histSampleCountByRestored(t, "true"); got != before {
		t.Errorf("negative latency was observed; should drop. delta=%v", got-before)
	}
}

func TestObserve_NilNewPodNoPanic(t *testing.T) {
	// Defensive: tests UpdateFunc cast-failure pattern (cast to *Pod
	// returns nil if the object was a tombstone or wrong type).
	observePodFirstReady(nil, nil) // must not panic
}
