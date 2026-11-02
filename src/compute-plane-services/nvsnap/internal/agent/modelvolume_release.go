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
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
		c.releaseBind(ctx, key, dst)
	}
}

// releaseBind unbinds dst and deletes its mount-holders, unless a pod on
// the node took it up meanwhile.
func (c *ModelVolumeController) releaseBind(ctx context.Context, key, dst string) {
	defer c.lockBind(key)()
	log := c.log().WithFields(logrus.Fields{"dst": dst, "node": c.NodeName})
	pods, err := c.Kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + c.NodeName})
	if err != nil || hostPathKeysInUse(pods.Items)[key] {
		return // a reader arrived, or we cannot tell
	}
	if err := c.unbind(dst); err != nil {
		log.WithError(err).Warn("model volume: cannot unbind an unused volume")
		return
	}
	c.mu.Lock()
	delete(c.idleSince, key)
	for k := range c.holders {
		if strings.Contains(k, key) {
			delete(c.holders, k)
		}
	}
	c.mu.Unlock()
	// The holders by name, not from memory: the agent may have restarted
	// since it created them.
	for _, p := range pods.Items {
		if !strings.HasPrefix(p.Name, modelHolderPrefix) || !strings.Contains(p.Name, key) || p.DeletionTimestamp != nil {
			continue
		}
		if err := c.Kube.CoreV1().Pods(p.Namespace).Delete(context.WithoutCancel(ctx), p.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			log.WithError(err).WithField("holder", p.Namespace+"/"+p.Name).Warn("model volume: cannot delete the mount-holder of an unbound volume")
		}
	}
	log.Info("model volume: no pod on this node uses the volume; unbound it so it can detach")
}

// modelHolderPrefix starts the name of a reader's mount-holder pod.
const modelHolderPrefix = "nvsnap-model-holder-"

func (c *ModelVolumeController) lockBind(key string) func() {
	m, _ := c.bindLocks.LoadOrStore(key, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
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
