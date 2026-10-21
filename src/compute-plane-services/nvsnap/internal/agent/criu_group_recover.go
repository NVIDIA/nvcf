// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// An agent that stops during an instance capture (a rollout sends it
// SIGTERM) takes the GPU suspend it was running with it: the instance's
// ranks may be left suspended, holding no GPU memory and serving nothing,
// while their pods read Ready. An agent that starts and finds a capture
// in progress with a rank on its node was part of it: it fails the capture
// and deletes the instance's pods, so their controllers replace them.

import (
	"context"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (c *criuAutoRestorer) recoverInterruptedCaptures(ctx context.Context, started time.Time) {
	a := c.a
	cms, err := a.kubeClient.CoreV1().ConfigMaps(a.criuFailedNamespace()).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=nvsnap"})
	if err != nil {
		a.log.WithError(err).Warn("CRIU group capture: cannot list the group records to recover interrupted captures")
		return
	}
	for _, cm := range cms.Items {
		if !strings.HasPrefix(cm.Name, criuGroupConfigMapPrefix) || cm.Data["state"] != criuGroupStateCapturing {
			continue
		}
		at, err := time.Parse(time.RFC3339, cm.Data["startedAt"])
		if err != nil || !at.Before(started) {
			continue // begun since this agent started: not interrupted by it
		}
		ns, name, ok := strings.Cut(cm.Data["source"], "/")
		if !ok {
			continue
		}
		leader, err := a.kubeClient.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			continue
		}
		pods, err := a.kubeClient.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			continue
		}
		var instance []*corev1.Pod
		here := false
		for i := range pods.Items {
			p := &pods.Items[i]
			if p.DeletionTimestamp == nil && sameInstance(leader, p) && gpuContainer(p) != "" {
				instance = append(instance, p)
				here = here || p.Spec.NodeName == a.config.NodeName
			}
		}
		if !here {
			continue
		}
		key := strings.TrimPrefix(cm.Name, criuGroupConfigMapPrefix)
		log := a.log.WithFields(logrus.Fields{"criuGroup": key, "pod": ns + "/" + name})
		log.Error("CRIU group capture was interrupted by this agent's restart; its ranks may be suspended; deleting the instance's pods")
		startedAt := cm.Data["startedAt"]
		if err := c.updateGroupRecord(ctx, key, func(d map[string]string) bool {
			if d["state"] != criuGroupStateCapturing || d["startedAt"] != startedAt {
				return false
			}
			d["state"], d["reason"] = criuGroupStateFailed, "the capturing agent restarted during the capture"
			return true
		}); err != nil {
			log.WithError(err).Warn("CRIU group capture: cannot fail the interrupted capture's record")
		}
		for _, p := range instance {
			if err := a.kubeClient.CoreV1().Pods(p.Namespace).Delete(ctx, p.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				log.WithError(err).WithField("rank", p.Name).Error("cannot delete a rank of the interrupted capture")
			}
		}
	}
}
