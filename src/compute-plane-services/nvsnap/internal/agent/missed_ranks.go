// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// A rank the webhook never mutated: the API server admitted it unchanged
// when the webhook could not be reached (its failure policy is Ignore, so a
// workload is never blocked), or the pod opts out. It carries no nvsnap
// identity, so no capture can place it, and its instance cannot be captured
// while it runs; its next pod is admitted anew.

// Why a rank has no nvsnap identity.
const (
	missedNotAdmitted = "not-admitted"
	missedOptedOut    = "opted-out"
)

// eventNotAdmitted is the event on such a rank, once per pod.
const eventNotAdmitted = "NvsnapNotAdmitted"

// missedRanks returns the GPU pods of leader's instance of size ranks that
// carry no nvsnap identity, with why. An instance of one pod has no other
// rank: the pods of its controller are other instances.
func missedRanks(pods []corev1.Pod, leader *corev1.Pod, size int) map[*corev1.Pod]string {
	if size < 2 {
		return nil
	}
	out := map[*corev1.Pod]string{}
	for i := range pods {
		p := &pods[i]
		if p.UID == leader.UID || p.DeletionTimestamp != nil || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed ||
			!hasGPU(p) || !sameInstance(leader, p) {
			continue
		}
		if p.Annotations[cacheURIAnnotation] != "" || p.Annotations[webhook.AdmittedConfigAnnotation] != "" || p.Annotations[webhook.CRIURestoreAnnotation] != "" {
			continue
		}
		why := missedNotAdmitted
		if p.Labels[webhook.InjectLabel] == webhook.InjectDisabled {
			why = missedOptedOut
		}
		out[p] = why
	}
	return out
}

// reportMissed says, once per pod, that a rank of leader's instance has no
// nvsnap identity: in the log, as an event on the pod and in the admission
// metric.
func (c *criuAutoRestorer) reportMissed(missed map[*corev1.Pod]string, log *logrus.Entry) {
	for p, why := range missed {
		if _, said := c.attempted.LoadOrStore("missed/"+string(p.UID), struct{}{}); said {
			continue
		}
		metrics.CountAdmission(metrics.AdmitMissed, why)
		fl := log.WithFields(logrus.Fields{"rank": p.Name, "reason": why})
		if why == missedOptedOut {
			fl.Warn("CRIU group capture: skipped; a rank of the instance opts out of nvsnap, so the instance cannot be captured")
			c.a.podEvent(p, true, eventNotAdmitted, "this pod opts out of nvsnap (%s=%s); its instance cannot be captured", webhook.InjectLabel, webhook.InjectDisabled)
			continue
		}
		fl.Warn("CRIU group capture: skipped; a rank of the instance was admitted without the nvsnap webhook (unreachable when the pod was created), so the instance cannot be captured until its pods are replaced")
		c.a.podEvent(p, true, eventNotAdmitted, "this pod was admitted without the nvsnap webhook; its instance cannot be captured until its pods are replaced")
	}
}

// considerMissed reports the ranks of leader's instance of size ranks that
// carry no nvsnap identity, and whether there are any: then the instance
// cannot be captured as it runs.
func (c *criuAutoRestorer) considerMissed(pods []corev1.Pod, leader *corev1.Pod, size int, log *logrus.Entry) bool {
	missed := missedRanks(pods, leader, size)
	if len(missed) == 0 {
		return false
	}
	c.reportMissed(missed, log)
	return true
}
