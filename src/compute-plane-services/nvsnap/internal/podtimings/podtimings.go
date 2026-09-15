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

// Package podtimings classifies pods as cold-start vs NvSnap-restored
// and computes their lifecycle latency.
//
// Used by `nvsnap pods` and `kubectl nvsnap pods` to give operators an
// at-a-glance view of how much pod-start time the NvSnap cache is
// saving across their cluster.
//
// Classification is annotation-driven: a pod with the
// `nvsnap.io/restore-from` annotation populated (non-empty hash) is
// counted as Restored; everything else is ColdStart. The annotation
// is what NVCA's Hook A stamps on pods whose function-version has a
// usable checkpoint cached locally — see the NVCA × NvSnap design doc.
//
// Latency is computed from the Pod's CreationTimestamp to the
// lastTransitionTime of the Ready=True condition. Pods that have not
// reached Ready are reported with NotReady = true and Duration = 0;
// they are intentionally excluded from Summary's averages so a few
// in-flight pods don't poison the displayed numbers.
package podtimings

import (
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// RestoreFromAnnotation is the annotation key NVCA's Hook A stamps to
// tell NvSnap's restore webhook which checkpoint to inject. Duplicated
// as a constant here so we don't pull NVCA into this CLI's dependency
// closure.
const RestoreFromAnnotation = "nvsnap.io/restore-from"

// CheckpointOnWarmAnnotation is the annotation key NVCA's Hook A stamps
// for Cold pods so Hook B's reconciler knows to capture them after
// readiness. Presence means "this is a Cold pod that will be captured"
// — useful for marking the pod that triggered each fvID's initial
// capture.
const CheckpointOnWarmAnnotation = "nvsnap.io/checkpoint-on-warm"

// FunctionVersionIDAnnotation is the annotation key NVCA's Hook A
// stamps with the function-version id so downstream tools can group +
// summarize without re-deriving from labels.
const FunctionVersionIDAnnotation = "nvsnap.io/function-version-id"

// Mode classifies how a pod started.
type Mode string

const (
	// ModeColdStart indicates the pod started without a restore-from
	// annotation. NvSnap had no checkpoint cached for this
	// function-version at admission time, so the pod went through the
	// full image pull + init + warmup path.
	ModeColdStart Mode = "cold-start"

	// ModeRestored indicates the pod's restore-from annotation was
	// populated, so NvSnap's restore webhook injected the restore init
	// container and CRIU brought the process back to the captured state.
	ModeRestored Mode = "restored"
)

// PodTiming is the per-pod result of Compute. All time fields are
// in UTC. Duration is the wall-clock latency from pod admission to
// Ready=True; zero when NotReady.
type PodTiming struct {
	Namespace         string        `json:"namespace"`
	Name              string        `json:"name"`
	NodeName          string        `json:"nodeName,omitempty"`
	Mode              Mode          `json:"mode"`
	FunctionVersionID string        `json:"functionVersionId,omitempty"`
	Hash              string        `json:"hash,omitempty"` // shortHash form
	CreatedAt         time.Time     `json:"createdAt"`
	ReadyAt           *time.Time    `json:"readyAt,omitempty"`
	Duration          time.Duration `json:"durationSeconds"` // 0 when NotReady
	NotReady          bool          `json:"notReady,omitempty"`
}

// Compute derives a PodTiming for each pod in the input slice. Pods
// without a CreationTimestamp are skipped (impossible in practice but
// keeps Compute total). Result is sorted oldest-first, then by name,
// for stable presentation.
func Compute(pods []corev1.Pod) []PodTiming {
	out := make([]PodTiming, 0, len(pods))
	for i := range pods {
		t := classify(&pods[i])
		if t.CreatedAt.IsZero() {
			continue
		}
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].Name < out[j].Name
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

func classify(p *corev1.Pod) PodTiming {
	t := PodTiming{
		Namespace: p.Namespace,
		Name:      p.Name,
		NodeName:  p.Spec.NodeName,
		CreatedAt: p.CreationTimestamp.UTC(),
		Mode:      ModeColdStart,
	}
	ann := p.Annotations
	if rf, ok := ann[RestoreFromAnnotation]; ok && rf != "" {
		t.Mode = ModeRestored
		t.Hash = shortHash(rf)
	}
	t.FunctionVersionID = ann[FunctionVersionIDAnnotation]

	readyAt, ready := podReadyAt(p)
	if ready {
		t.ReadyAt = &readyAt
		t.Duration = readyAt.Sub(t.CreatedAt).Round(time.Second)
	} else {
		t.NotReady = true
	}
	return t
}

// podReadyAt returns the UTC time the pod transitioned Ready=True
// (and true). Pods that never reached Ready return zero + false.
func podReadyAt(p *corev1.Pod) (time.Time, bool) {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return c.LastTransitionTime.UTC(), true
		}
	}
	return time.Time{}, false
}

// shortHash returns the first 16 hex characters of a sha256 hex
// string, or the whole input if shorter. We display short hashes in
// the table for readability; the full hash is still on the pod's
// annotation if anyone needs to grep for it.
func shortHash(h string) string {
	const n = 16
	if len(h) <= n {
		return h
	}
	return h[:n]
}
