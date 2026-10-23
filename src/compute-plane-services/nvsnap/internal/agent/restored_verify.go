// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// A capture is verified by restoring it, but restored processes are not a
// served engine: an engine can restore and then never finish its first
// step, its GPUs spinning, its pods briefly Ready. So a capture being
// verified becomes complete only once every pod its restore made has been
// Ready for restoredServedFor, and its restore fails when they have not
// managed that by restoredServeDeadline. An engine that hangs while it
// still answers its readiness probe gives up within its own step timeout
// and exits, which the restored-pod watch fails (restored_watch.go).

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

const (
	// restoredServedFor is how long every pod of a restore must stay Ready
	// to verify its capture: longer than a hung engine takes to drop its
	// readiness or to give up on its step and exit (vLLM: 300 s).
	restoredServedFor = 6 * time.Minute
	// restoredServeDeadline bounds, from its restore, how long a pod may
	// take to have served restoredServedFor.
	restoredServeDeadline = 20 * time.Minute
)

// considerVerify verifies the capture a restored pod is being verified
// from, or fails its restore. Only the first pod the record names acts, so
// the agents of a group's nodes do not both decide.
func (c *criuAutoRestorer) considerVerify(ctx context.Context, pod *corev1.Pod, ref restoredRef) {
	key := ref.group
	if key == "" {
		key = pod.Annotations[webhook.CRIUGroupAnnotation]
	}
	if key == "" {
		return
	}
	log := c.a.log.WithFields(map[string]interface{}{"pod": pod.Namespace + "/" + pod.Name, "criuGroup": key})
	cm, err := c.a.kubeClient.CoreV1().ConfigMaps(c.a.criuFailedNamespace()).Get(ctx, criuGroupConfigMapName(key), metav1.GetOptions{})
	if err != nil || cm.Data["state"] != criuGroupStateVerifying {
		return
	}
	uids := restoredPodUIDs(cm.Data[groupRestoredPodsKey])
	if len(uids) == 0 || uids[0] != pod.UID {
		return
	}
	pods, err := c.a.kubeClient.CoreV1().Pods(pod.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		log.WithError(err).Warn("CRIU restore: cannot list the restored pods; their capture is verified on a later event")
		return
	}
	served, why := restoreServed(pods.Items, uids, time.Now())
	switch {
	case served:
		pinned := cm.Data[groupRestoredPodsKey]
		if err := c.updateGroupRecord(ctx, key, func(d map[string]string) bool {
			if d["state"] != criuGroupStateVerifying || d[groupRestoredPodsKey] != pinned {
				return false // another restore or capture took the record over
			}
			d["state"], d["verifiedAt"] = criuGroupStateComplete, time.Now().UTC().Format(time.RFC3339)
			delete(d, "sourceNamespace")
			return true
		}); err != nil {
			log.WithError(err).Warn("CRIU restore: cannot record the capture verified; it is tried again on a later event")
			return
		}
		log.WithField("servedFor", restoredServedFor.String()).Info("CRIU restore: the restored pods served; their capture is verified")
	case why != "":
		c.failRestored(ctx, pod, ref, fmt.Errorf("the restored engine never served: %s", why))
	}
}

// restoreServed reports whether every pod of uids among pods has been
// Ready for restoredServedFor at now; else why its restore failed, or ""
// while it may still serve or while a pod of it is gone (deleted, not
// failed: the capture stays to be verified by the namespace's next
// restore).
func restoreServed(pods []corev1.Pod, uids []types.UID, now time.Time) (served bool, failure string) {
	byUID := make(map[types.UID]*corev1.Pod, len(pods))
	for i := range pods {
		byUID[pods[i].UID] = &pods[i]
	}
	// A pod gone first: the rest of an instance missing a pod cannot
	// serve, which says nothing about the capture.
	for _, uid := range uids {
		if p := byUID[uid]; p == nil || p.DeletionTimestamp != nil {
			return false, ""
		}
	}
	served = true
	for _, uid := range uids {
		p := byUID[uid]
		since := readySince(p)
		if !since.IsZero() && now.Sub(since) >= restoredServedFor {
			continue
		}
		served = false
		if at := restoredAt(p); !at.IsZero() && now.Sub(at.Time) >= restoredServeDeadline {
			return false, fmt.Sprintf("pod %s was not Ready for %s within %s of its restore", p.Name, restoredServedFor, restoredServeDeadline)
		}
	}
	return served, ""
}

// readySince returns when pod last became Ready, or zero when it is not.
func readySince(pod *corev1.Pod) time.Time {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			if c.Status == corev1.ConditionTrue {
				return c.LastTransitionTime.Time
			}
			return time.Time{}
		}
	}
	return time.Time{}
}

// restoredPodUIDs returns the pods of a groupRestoredPodsKey value, in its
// order.
func restoredPodUIDs(v string) []types.UID {
	var out []types.UID
	for _, e := range strings.Split(v, ",") {
		if u, _, ok := strings.Cut(e, "/"); ok && u != "" {
			out = append(out, types.UID(u))
		}
	}
	return out
}
