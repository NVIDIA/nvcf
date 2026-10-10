// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// A model volume bound for readers at <HostRoot>/<key> stays bound until no
// pod on the node uses it. The bind keeps the volume's filesystem mounted
// on the node: while it stands, the volume cannot detach, and a read-only
// view whose namespace is gone leaves its VolumeAttachment behind for good.
// Once no pod on the node has had the path for modelBindIdleGrace, the
// agent unbinds it and deletes its mount-holders, so the attachments end.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	modelBindReleaseInterval = time.Minute
	// modelBindIdleGrace keeps a bind through a pod's replacement.
	modelBindIdleGrace = 5 * time.Minute
)

func (c *ModelVolumeController) runBindRelease(ctx context.Context) {
	t := time.NewTicker(modelBindReleaseInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c.releaseIdleBinds(ctx, time.Now())
	}
}

// releaseIdleBinds unbinds the model volumes no pod on this node has used
// for modelBindIdleGrace.
func (c *ModelVolumeController) releaseIdleBinds(ctx context.Context, now time.Time) {
	ents, err := os.ReadDir(c.HostRoot)
	if err != nil {
		return
	}
	pods, err := c.Kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + c.NodeName})
	if err != nil {
		c.log().WithError(err).Warn("model volume: cannot list this node's pods; keeping every bind")
		return
	}
	used := hostPathKeysInUse(pods.Items)
	c.mu.Lock()
	if c.idleSince == nil {
		c.idleSince = map[string]time.Time{}
	}
	c.mu.Unlock()
	for _, e := range ents {
		key := e.Name()
		dst := filepath.Join(c.HostRoot, key)
		if !e.IsDir() || c.mountedDevice(dst) == "" {
			continue
		}
		c.mu.Lock()
		if used[key] {
			delete(c.idleSince, key)
			c.mu.Unlock()
			continue
		}
		since, seen := c.idleSince[key]
		if !seen {
			c.idleSince[key] = now
		}
		c.mu.Unlock()
		if !seen || now.Sub(since) < modelBindIdleGrace {
			continue
		}
		log := c.log().WithFields(logrus.Fields{"dst": dst, "node": c.NodeName})
		if err := c.unbind(dst); err != nil {
			log.WithError(err).Warn("model volume: cannot unbind an unused volume")
			continue
		}
		c.mu.Lock()
		delete(c.idleSince, key)
		var holders []string
		for k := range c.holders {
			if strings.Contains(k, key) {
				holders = append(holders, k)
			}
		}
		c.mu.Unlock()
		for _, k := range holders {
			c.mu.Lock()
			h := c.holders[k]
			delete(c.holders, k)
			c.mu.Unlock()
			if h != nil {
				if err := h.Delete(context.WithoutCancel(ctx)); err != nil {
					log.WithError(err).WithField("holder", k).Warn("model volume: cannot delete the mount-holder of an unbound volume")
				}
			}
		}
		log.Info("model volume: no pod on this node uses the volume; unbound it so it can detach")
	}
}

// hostPathKeysInUse returns the last path element of every hostPath
// volume of the pods still holding their mounts.
func hostPathKeysInUse(pods []corev1.Pod) map[string]bool {
	used := map[string]bool{}
	for _, p := range pods {
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		for _, v := range p.Spec.Volumes {
			if v.HostPath != nil {
				used[filepath.Base(v.HostPath.Path)] = true
			}
		}
	}
	return used
}
