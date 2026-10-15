// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// Every gpushare pod has a chunk store on its node,
// <checkpoint root>/gpushare-pods/<pod uid>, that a checkpoint moves into
// the checkpoint it takes. A store outlives its pod when a dump stops
// after the suspend, or the pod goes away mid-capture, and it holds the
// pod's whole GPU memory. The reaper removes the stores of pods no longer
// on the node.

import (
	"context"
	"os"
	"path/filepath"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	gpushareStoreReapInterval = 10 * time.Minute
	// gpushareStoreReapGrace spares a store younger than this: its pod may
	// be admitted and not yet listed.
	gpushareStoreReapGrace = 10 * time.Minute
)

func (a *Agent) startGPUShareStoreReaper(ctx context.Context) {
	if a.kubeClient == nil || a.config.NodeName == "" {
		return
	}
	go func() {
		t := time.NewTicker(gpushareStoreReapInterval)
		defer t.Stop()
		for {
			a.reapNode(ctx, time.Now())
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func (a *Agent) reapNode(ctx context.Context, now time.Time) {
	pods, err := a.kubeClient.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + a.config.NodeName})
	if err != nil {
		a.log.WithError(err).Warn("node reaper: cannot list this node's pods; keeping every store and checkpoint")
		return
	}
	a.reapGPUShareStores(pods.Items, now)
	a.reapCheckpoints(ctx, pods.Items, now)
}

func (a *Agent) reapGPUShareStores(pods []corev1.Pod, now time.Time) {
	root := filepath.Join(a.config.CheckpointDir, GPUSharePodStoresSubdir)
	ents, err := os.ReadDir(root)
	if err != nil {
		return // no stores on this node
	}
	live := map[string]bool{}
	for _, p := range pods {
		live[string(p.UID)] = true
	}
	for _, uid := range orphanStores(root, ents, live, now) {
		dir := filepath.Join(root, uid)
		if err := os.RemoveAll(dir); err != nil {
			a.log.WithError(err).WithField("store", dir).Warn("gpushare store reaper: cannot remove the store of a pod gone from this node")
			continue
		}
		a.log.WithField("podUID", uid).Info("gpushare store reaper: removed the store of a pod gone from this node")
	}
}

// orphanStores returns the stores under root of no live pod, past the grace.
func orphanStores(root string, ents []os.DirEntry, live map[string]bool, now time.Time) []string {
	var out []string
	for _, e := range ents {
		if !e.IsDir() || live[e.Name()] {
			continue
		}
		fi, err := os.Stat(filepath.Join(root, e.Name()))
		if err != nil || now.Sub(fi.ModTime()) < gpushareStoreReapGrace {
			continue
		}
		out = append(out, e.Name())
	}
	return out
}
