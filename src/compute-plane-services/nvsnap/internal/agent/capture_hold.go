// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// A pod a CRIU capture may freeze is admitted held out of service
// (capturehold.Condition, a readiness gate the webhook adds): a capture
// stops the instance it captures, and captured while it serves, the
// instance fails its requests until its replacement has restored. The
// agent of the pod's node releases it (sets the condition True):
//
//   - a lone pod, or the leader (rank 0) of a multi-pod instance, once
//     whether its instance is captured is decided (considerGroupCapture):
//     not captured, the pod serves; captured, its pods are deleted and
//     their restored replacements, which are never held, serve;
//   - any other rank of a multi-pod instance, with its leader
//     (releaseInstanceHold), since the leader decides for the instance;
//   - a rank other than 0 whose leader is not held: a replacement of one
//     rank of a running instance starts cold and is never captured;
//   - any held pod past its limit (holdExpired), whatever else holds it:
//     longer than a capture can take, and a capture never starts on a pod
//     past it, so a pod is never released while it is being captured.
//
// The informer's resync (30 s) re-evaluates every pod, so a failed
// release is retried and the limit is noticed.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/capturehold"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// captureHoldLimit is the longest a warm pod is held before its capture
// starts: the capture's warmup and its own timeout, and a margin for the
// agent to see it end. A capture that starts within it marks the pods it
// stops (holdForCapture), and those are held for the capture's timeout
// from then.
const captureHoldLimit = criuGroupWarmup + criuGroupCaptureTimeout + 5*time.Minute

// capturingHoldLimit is the longest a pod is held once its capture has
// started.
const capturingHoldLimit = criuGroupCaptureTimeout + 5*time.Minute

// holdExpired reports whether pod has been held longer than anything may
// hold it: past captureHoldLimit since it became warm, or, while its
// instance is being captured, past capturingHoldLimit since that began.
func holdExpired(pod *corev1.Pod) bool {
	if !capturehold.Held(pod) || !capturehold.Warm(pod) {
		return false
	}
	if c := capturehold.Get(pod); c != nil && c.Status == corev1.ConditionFalse && c.Reason == capturehold.CapturingReason {
		return time.Since(c.LastTransitionTime.Time) > capturingHoldLimit
	}
	since, warm := capturehold.WarmSince(pod)
	return warm && time.Since(since) > captureHoldLimit
}

// considerCaptureHold releases pod, a pod of this node, when nothing
// holds it any more. undecided is considerGroupCapture's verdict for it.
func (c *criuAutoRestorer) considerCaptureHold(ctx context.Context, pod *corev1.Pod, undecided bool) {
	if !capturehold.Held(pod) || pod.DeletionTimestamp != nil {
		return
	}
	if holdExpired(pod) {
		c.a.log.WithField("pod", pod.Namespace+"/"+pod.Name).
			Warn("capture hold: the pod has been held longer than any capture takes; releasing it into service")
		c.releaseCaptureHold(ctx, pod, "HoldExpired", "held longer than any capture takes")
		return
	}
	if o, size, ranked := podRank(pod, modelvolume.CacheOrdinalAnnotation, modelvolume.CacheGroupSizeAnnotation); ranked && size > 1 && o > 0 {
		// Its leader decides for the instance and releases it with
		// itself (releaseInstanceHold). A rank whose leader is not held
		// (released, or admitted without the gate) has nothing to wait
		// for: a replacement of one rank of a running instance starts
		// cold, and is never captured with it.
		if c.leaderServes(ctx, pod) {
			c.releaseCaptureHold(ctx, pod, "LeaderSettled", "the instance's leader is not held for a capture")
		}
		return
	}
	if undecided {
		return
	}
	c.releaseInstanceHold(ctx, pod, "NotCaptured", "no capture of the instance applies")
}

// leaderServes reports whether rank, a rank other than 0 of a multi-pod
// instance, has a leader that is not held for a capture. False when the
// leader cannot be found or read: the rank stays held, and is tried again.
func (c *criuAutoRestorer) leaderServes(ctx context.Context, rank *corev1.Pod) bool {
	pods, err := c.a.kubeClient.CoreV1().Pods(rank.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		// Only the ordinal is compared: a leader admitted before its
		// StatefulSet scaled up carries the size of then, and one restored
		// from a group capture carries the group restore's rank, whose size
		// also counts the capture's companions.
		o, _, ok := podRank(p, modelvolume.CacheOrdinalAnnotation, modelvolume.CacheGroupSizeAnnotation)
		if !ok {
			o, _, ok = podRank(p, webhook.CRIUGroupOrdinalAnnotation, webhook.CRIUGroupSizeAnnotation)
		}
		if !ok {
			// Admitted while the webhook was down: no rank of nvsnap's,
			// and no gate. Its StatefulSet still numbers it.
			o, ok = statefulSetOrdinal(p)
		}
		if !ok || o != 0 || p.DeletionTimestamp != nil || !sameInstance(p, rank) {
			continue
		}
		return !capturehold.Held(p)
	}
	return false
}

// releaseInstanceHold releases leader and, for a multi-pod instance, every
// other held pod of its instance.
//
// The decision to release does not depend on the hold's state, so each
// pod is released at its current version.
func (c *criuAutoRestorer) releaseInstanceHold(ctx context.Context, leader *corev1.Pod, reason, message string) {
	if cur, err := c.a.kubeClient.CoreV1().Pods(leader.Namespace).Get(ctx, leader.Name, metav1.GetOptions{}); err == nil && cur.UID == leader.UID {
		leader = cur
	}
	c.releaseCaptureHold(ctx, leader, reason, message)
	if _, size, ranked := podRank(leader, modelvolume.CacheOrdinalAnnotation, modelvolume.CacheGroupSizeAnnotation); !ranked || size < 2 {
		return
	}
	pods, err := c.a.kubeClient.CoreV1().Pods(leader.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		c.a.log.WithError(err).WithField("pod", leader.Namespace+"/"+leader.Name).
			Warn("capture hold: cannot list the instance's pods to release them; each is released by its own node's agent")
		return
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.UID != leader.UID && groupMember(leader, p) && capturehold.Held(p) && p.DeletionTimestamp == nil {
			c.releaseCaptureHold(ctx, p, reason, message)
		}
	}
}

// holdForCapture marks the held members of an instance whose capture is
// about to start as being captured: their hold then lasts the capture's
// timeout from now (holdExpired). Each member is read afresh and marked
// only if it is unchanged since (its resourceVersion), so a release that
// raced it is never overwritten. serves names a member already released
// into service: the instance must not be captured. The mark's time is
// this attempt's: an attempt that does not start the capture removes it
// (unholdForCapture).
func (c *criuAutoRestorer) holdForCapture(ctx context.Context, members []*corev1.Pod, log *logrus.Entry) (serves string, err error) {
	for _, m := range members {
		if !capturehold.HasGate(m) {
			continue
		}
		cur, err := c.a.kubeClient.CoreV1().Pods(m.Namespace).Get(ctx, m.Name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("read member %s: %w", m.Name, err)
		}
		if cur.UID != m.UID || cur.DeletionTimestamp != nil {
			return "", fmt.Errorf("member %s was replaced or is being deleted", m.Name)
		}
		if capturehold.Settled(cur) {
			return m.Name, nil
		}
		if err := c.a.patchCaptureHold(ctx, cur, corev1.ConditionFalse, capturehold.CapturingReason, "the instance is being captured"); err != nil {
			return "", fmt.Errorf("mark member %s as being captured: %w", m.Name, err)
		}
		log.WithField("member", m.Name).Debug("capture hold: member held for the capture")
	}
	return "", nil
}

// unholdForCapture removes the capturing mark of members whose capture
// did not start: they stay held, by the limit since they became warm.
func (c *criuAutoRestorer) unholdForCapture(ctx context.Context, members []*corev1.Pod, log *logrus.Entry) {
	for _, m := range members {
		if !capturehold.HasGate(m) {
			continue
		}
		cur, err := c.a.kubeClient.CoreV1().Pods(m.Namespace).Get(ctx, m.Name, metav1.GetOptions{})
		if err != nil || cur.UID != m.UID {
			continue
		}
		if cond := capturehold.Get(cur); cond == nil || cond.Status != corev1.ConditionFalse || cond.Reason != capturehold.CapturingReason {
			continue
		}
		if err := c.a.patchCaptureHold(ctx, cur, corev1.ConditionFalse, capturehold.AwaitingReason, "waiting for the instance's capture"); err != nil {
			log.WithError(err).WithField("member", m.Name).Warn("capture hold: cannot clear the member's capturing mark; it expires with the capture's timeout")
		}
	}
}

// releaseCaptureHold sets pod's capturehold.Condition True. A pod already
// released, gone, or not held is left as it is.
func (c *criuAutoRestorer) releaseCaptureHold(ctx context.Context, pod *corev1.Pod, reason, message string) {
	if !capturehold.Held(pod) {
		return
	}
	log := c.a.log.WithField("pod", pod.Namespace+"/"+pod.Name)
	if err := c.a.patchCaptureHold(ctx, pod, corev1.ConditionTrue, reason, message); err != nil {
		log.WithError(err).Warn("capture hold: cannot release the pod into service; tried again on its next event")
		return
	}
	log.WithField("reason", reason).Info("capture hold: pod released into service")
}

// patchCaptureHold sets pod's capturehold.Condition to status, only if
// the pod is unchanged since pod was read (its resourceVersion): the
// decision to set it was made on that state. A conflict is an error, and
// the decision is made again on the pod's next event. A pod gone is no
// error.
func (a *Agent) patchCaptureHold(ctx context.Context, pod *corev1.Pod, status corev1.ConditionStatus, reason, message string) error {
	meta := map[string]any{}
	if pod.ResourceVersion != "" {
		meta["resourceVersion"] = pod.ResourceVersion
	}
	patch, err := json.Marshal(map[string]any{"metadata": meta, "status": map[string]any{"conditions": []corev1.PodCondition{{
		Type:               capturehold.Condition,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.NewTime(time.Now()),
	}}}})
	if err != nil {
		return err
	}
	_, err = a.kubeClient.CoreV1().Pods(pod.Namespace).Patch(ctx, pod.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{}, "status")
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// captureHoldAdmits reports whether pod may be held out of service for its
// capture (webhook.Mutator.CaptureHoldAdmits): not when holding it could
// keep its own instance from starting. A StatefulSet of several
// pods that creates them in order (OrderedReady, the default) creates
// each only once the one before is Ready: held, its first pod would keep
// the rest from being created, and a capture that waits for all of them
// would never start.
func (a *Agent) captureHoldAdmits(ctx context.Context, pod *corev1.Pod) (bool, string) {
	if groveInstance(pod) != "" || hasLWSLabel(pod) {
		// Their operators can start the pods of an instance in order
		// (LeaderWorkerSet LeaderReady, Grove startsAfter): a held first
		// pod would keep the rest from starting, and the capture waits for
		// all of them.
		return false, "a LeaderWorkerSet or Grove instance may start its pods in order"
	}
	c := metav1.GetControllerOf(pod)
	if c == nil || c.Kind != "StatefulSet" {
		return true, ""
	}
	sts, err := a.kubeClient.AppsV1().StatefulSets(pod.Namespace).Get(ctx, c.Name, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Sprintf("cannot read StatefulSet %s: %v", c.Name, err)
	}
	if sts.Spec.PodManagementPolicy != appsv1.ParallelPodManagement && (sts.Spec.Replicas == nil || *sts.Spec.Replicas > 1) {
		return false, fmt.Sprintf("StatefulSet %s creates its pods in order (%s)", c.Name, appsv1.OrderedReadyPodManagement)
	}
	if sts.Spec.Ordinals != nil && sts.Spec.Ordinals.Start != 0 {
		// Its pods are numbered from Start: no pod is rank 0, the leader
		// its other ranks would wait for.
		return false, fmt.Sprintf("StatefulSet %s numbers its pods from %d", c.Name, sts.Spec.Ordinals.Start)
	}
	return true, ""
}

// hasLWSLabel reports whether pod is a LeaderWorkerSet's.
func hasLWSLabel(pod *corev1.Pod) bool {
	for k := range pod.Labels {
		if strings.HasPrefix(k, "leaderworkerset.sigs.k8s.io/") {
			return true
		}
	}
	return false
}

// statefulSetOrdinal returns the ordinal a StatefulSet gave pod: its
// apps.kubernetes.io/pod-index label, or the number its name ends with.
func statefulSetOrdinal(pod *corev1.Pod) (int, bool) {
	ref := metav1.GetControllerOf(pod)
	if ref == nil || ref.Kind != "StatefulSet" {
		return 0, false
	}
	s, ok := pod.Labels["apps.kubernetes.io/pod-index"]
	if !ok {
		i := strings.LastIndexByte(pod.Name, '-')
		if i < 0 || pod.Name[:i] != ref.Name {
			return 0, false
		}
		s = pod.Name[i+1:]
	}
	o, err := strconv.Atoi(s)
	if err != nil || o < 0 {
		return 0, false
	}
	return o, true
}
