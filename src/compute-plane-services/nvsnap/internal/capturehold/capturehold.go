// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package capturehold keeps a pod nvsnap may capture out of service until
// that is settled.
//
// A CRIU capture stops the instance it captures: it freezes the pods for
// the dump, and the dumped pods are then replaced by pods that restore
// from it. Captured while it serves, the instance fails the requests it
// is sent for the whole time (seen: a function reported ACTIVE the moment
// its only pod was Ready, invoked at once, and answered 504 while the
// capture held it). So the webhook gives such a pod a readiness gate,
// Condition, that the agent sets once the pod is warm (its containers
// are Ready) and either no capture applies or the capture is over. The
// pod's own probes still decide when it is warm; the gate only decides
// when it may serve.
//
// The gate keeps traffic off the pod where traffic follows readiness:
// Service endpoints, and the platform, which reports a function's
// instance running only once its pod is Ready, so a function's first
// instance is not invoked while held. A queue worker inside the pod that
// starts fetching work once its engine answers health checks (an NVCF
// container function's worker) is not stopped by it: a new replica of a
// function already serving may take requests while held.
package capturehold

import (
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Condition is the readiness gate a pod waits on while nvsnap may still
// capture it. True: the pod may serve.
const Condition corev1.PodConditionType = "nvsnap.io/capture-settled"

// HasGate reports whether pod's readiness waits on Condition.
func HasGate(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	for _, g := range pod.Spec.ReadinessGates {
		if g.ConditionType == Condition {
			return true
		}
	}
	return false
}

// Settled reports whether pod's Condition is True.
func Settled(pod *corev1.Pod) bool {
	c := condition(pod, Condition)
	return c != nil && c.Status == corev1.ConditionTrue
}

// Get returns pod's Condition, or nil when it has none yet.
func Get(pod *corev1.Pod) *corev1.PodCondition { return condition(pod, Condition) }

// CapturingReason is the reason of a False Condition set while the pod's
// instance is being captured.
const CapturingReason = "Capturing"

// AwaitingReason is the reason of a False Condition set when an attempt
// to capture the pod's instance did not start it: the pod stays held,
// waiting for the next attempt.
const AwaitingReason = "AwaitingCapture"

// Held reports whether pod is gated and not yet settled.
func Held(pod *corev1.Pod) bool { return HasGate(pod) && !Settled(pod) }

// Ready reports whether pod is Ready, or would be but for the hold: its
// containers are Ready and every other readiness gate is True. Everything
// nvsnap does at a pod's readiness (capture it, count it) does it to a
// held pod too; only serving waits.
func Ready(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	if c := condition(pod, corev1.PodReady); c != nil && c.Status == corev1.ConditionTrue {
		return true
	}
	return Held(pod) && Warm(pod)
}

// Warm reports whether pod's containers are Ready and every readiness
// gate other than Condition is True.
func Warm(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	if c := condition(pod, corev1.ContainersReady); c == nil || c.Status != corev1.ConditionTrue {
		return false
	}
	for _, g := range pod.Spec.ReadinessGates {
		if g.ConditionType == Condition {
			continue
		}
		if c := condition(pod, g.ConditionType); c == nil || c.Status != corev1.ConditionTrue {
			return false
		}
	}
	return true
}

// WarmSince is when pod's containers last became Ready; false when they
// are not.
func WarmSince(pod *corev1.Pod) (time.Time, bool) {
	c := condition(pod, corev1.ContainersReady)
	if c == nil || c.Status != corev1.ConditionTrue {
		return time.Time{}, false
	}
	return c.LastTransitionTime.Time, true
}

func condition(pod *corev1.Pod, t corev1.PodConditionType) *corev1.PodCondition {
	if pod == nil {
		return nil
	}
	for i := range pod.Status.Conditions {
		if pod.Status.Conditions[i].Type == t {
			return &pod.Status.Conditions[i]
		}
	}
	return nil
}
