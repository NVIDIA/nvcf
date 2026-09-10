/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package controller wires the post-Ready checkpoint reconciler (in
// pkg/nvca/nvsnap/reconciler) to a Pod informer + workqueue so the
// agent can drive it from a goroutine at startup.
//
// Responsibilities of this package:
//   - Watch pods cluster-wide (or namespace-scoped).
//   - Filter for the Hook A annotations (nvsnap.io/checkpoint-on-warm
//     "true" AND nvsnap.io/function-version-id non-empty).
//   - Enqueue a pod when it becomes PodReady.
//   - Pop a key, look up the pod, call Reconciler.Reconcile.
//   - On error, requeue with the workqueue's exponential backoff.
//
// What this package does NOT do (handled elsewhere or in follow-ups):
//   - Read the global feature flag — caller decides whether to Start.
//   - Bootstrap NvSnapFunctionState — done lazily inside Reconcile.
//   - Wire into NVCA agent startup (PR-7).
package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	nvsnapreconciler "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/nvsnap/reconciler"
)

// Controller wraps the reconciler with a Pod informer + workqueue.
// One Controller per NVCA agent process. Stopped by canceling the
// context passed to Run.
type Controller struct {
	// KubeClient is the shared K8s client. Run() constructs a
	// SharedInformerFactory scoped to Namespace from this client
	// each time it's invoked. (A future revision could accept an
	// externally-managed factory so the agent's existing informer
	// cache is reused; today Run owns its own.)
	KubeClient kubernetes.Interface

	// Reconciler is the state machine that handles one pod. The
	// controller calls Reconciler.Reconcile(ctx, pod) per workqueue
	// dequeue.
	Reconciler *nvsnapreconciler.Reconciler

	// Namespace scopes the Pod informer. Empty = cluster-wide.
	// In production NVCA tenant pods live in nvcf-backend; empty
	// works too if the agent has cluster-scoped pod-read RBAC.
	Namespace string

	// Workers is the number of goroutines popping from the
	// workqueue. Each runs one reconcile at a time. Default 2 —
	// reconciles are long-lived (warmup + checkpoint can be 10+ min),
	// so a small parallelism prevents the workqueue from starving
	// under bursty deploys.
	Workers int

	// SweepInterval is how often the pod-independent CFS recovery sweep
	// runs (nvca#104 durable-warm). Each tick reconciles every
	// NvSnapFunctionState against nvsnap-server and flips Warm for any whose
	// capture has landed but whose live reconcile died before writeStatus.
	// Default 60s. Set <0 to disable the sweep entirely.
	SweepInterval time.Duration

	// Log is the structured logger.
	Log logrus.FieldLogger

	// queue is built in Run.
	queue workqueue.RateLimitingInterface
}

// NewController constructs a Controller with sane defaults filled in.
// Caller must set KubeClient, Reconciler. Workers/Log/Namespace default
// if zero.
func NewController(kc kubernetes.Interface, r *nvsnapreconciler.Reconciler) *Controller {
	return &Controller{
		KubeClient: kc,
		Reconciler: r,
		Workers:    2,
	}
}

// Run starts the informer + workers and blocks until ctx is canceled.
// Returns ctx.Err() on shutdown (no other error path — informer + queue
// failures are logged and retried internally).
func (c *Controller) Run(ctx context.Context) error {
	if c.KubeClient == nil {
		return fmt.Errorf("controller: KubeClient is nil")
	}
	if c.Reconciler == nil {
		return fmt.Errorf("controller: Reconciler is nil")
	}
	if c.Log == nil {
		c.Log = logrus.NewEntry(logrus.New())
	}
	if c.Workers <= 0 {
		c.Workers = 2
	}
	c.queue = workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	defer c.queue.ShutDown()

	factory := informers.NewSharedInformerFactoryWithOptions(c.KubeClient, 0,
		informers.WithNamespace(c.Namespace))
	podInf := factory.Core().V1().Pods()
	informer := podInf.Informer()
	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			// Pods already Ready when the informer first sees them
			// (controller restart, cache resync) still count — pass
			// nil oldObj so observePodFirstReady treats it as a
			// transition. Mis-counting after a restart would
			// systematically under-report the savings.
			if p, ok := obj.(*corev1.Pod); ok {
				observePodFirstReady(nil, p)
			}
			c.enqueueIfEligible(obj)
		},
		UpdateFunc: func(oldObj, newObj any) {
			oldPod, _ := oldObj.(*corev1.Pod)
			newPod, _ := newObj.(*corev1.Pod)
			observePodFirstReady(oldPod, newPod)
			c.enqueueIfEligible(newObj)
		},
		// DeleteFunc intentionally omitted — a deleted pod has nothing
		// to reconcile; the workqueue's existing entry (if any) will
		// simply Get NotFound and log+drop.
	}); err != nil {
		return fmt.Errorf("install pod event handler: %w", err)
	}

	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
		return fmt.Errorf("controller: pod informer cache sync failed")
	}

	c.Log.WithField("workers", c.Workers).Info("nvsnap controller starting")
	for range c.Workers {
		go c.runWorker(ctx, podInf.Lister())
	}

	// Pod-independent CFS recovery sweep (nvca#104 durable-warm). Runs
	// on its own ticker, decoupled from the pod workqueue, so a capture
	// whose live reconcile died before writeStatus still reaches Warm.
	if c.SweepInterval == 0 {
		c.SweepInterval = 60 * time.Second
	}
	if c.SweepInterval > 0 {
		go c.runSweep(ctx)
	}

	<-ctx.Done()
	c.Log.Info("nvsnap controller stopping")
	return ctx.Err()
}

// enqueueIfEligible adds the pod's key to the workqueue if it carries
// the Hook A annotations AND is currently PodReady. The dual filter
// (annotation + Ready) avoids queueing pods we can't act on, keeping
// the queue's working set bounded by the number of in-flight warmups.
func (c *Controller) enqueueIfEligible(obj any) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return
	}
	if !PodEligibleForCheckpointOnWarm(pod) {
		return
	}
	if !IsPodReady(pod) {
		return
	}
	key, err := cache.MetaNamespaceKeyFunc(pod)
	if err != nil {
		c.Log.WithError(err).Warn("nvsnap controller: build key failed; dropping")
		return
	}
	c.queue.Add(key)
}

// PodEligibleForCheckpointOnWarm is the static filter — checks that
// Hook A stamped both annotations the reconciler needs. Exported so
// the agent startup code can use the same predicate to install
// per-namespace informers conditionally.
func PodEligibleForCheckpointOnWarm(pod *corev1.Pod) bool {
	if pod == nil || pod.Annotations == nil {
		return false
	}
	if pod.Annotations[nvsnapreconciler.CheckpointOnWarmAnnotation] != "true" {
		return false
	}
	if pod.Annotations[nvsnapreconciler.FunctionVersionIDAnnotation] == "" {
		return false
	}
	return true
}

// IsPodReady returns true iff the pod's PodReady condition is True.
// Duplicated from k8sutil so this package has no internal-only NVCA
// dependency (lets the controller be vendored standalone if needed).
func IsPodReady(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func (c *Controller) runWorker(ctx context.Context, lister corev1listers.PodLister) {
	for {
		if shutdown := c.processNext(ctx, lister); shutdown {
			return
		}
	}
}

// runSweep drives the pod-independent CFS recovery sweep on a ticker
// until ctx is canceled (nvca#104 durable-warm). One pass runs
// immediately so a controller restart heals stuck-not-Warm CFS without
// waiting a full interval. SweepOnce is best-effort and never panics or
// returns an error, so this loop is just "tick → sweep".
func (c *Controller) runSweep(ctx context.Context) {
	c.Log.WithField("interval", c.SweepInterval).Info("nvsnap CFS recovery sweep starting")
	c.Reconciler.SweepOnce(ctx)
	ticker := time.NewTicker(c.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.Reconciler.SweepOnce(ctx)
		}
	}
}

// processNext pops one key, looks up the pod, and runs Reconcile. On
// error, re-queues with backoff. Returns true iff the queue was shut
// down (caller exits the loop).
func (c *Controller) processNext(ctx context.Context, lister corev1listers.PodLister) bool {
	key, quit := c.queue.Get()
	if quit {
		return true
	}
	defer c.queue.Done(key)

	ns, name, err := cache.SplitMetaNamespaceKey(key.(string))
	if err != nil {
		c.Log.WithError(err).WithField("key", key).Warn("nvsnap controller: bad key")
		c.queue.Forget(key)
		return false
	}

	pod, err := lister.Pods(ns).Get(name)
	if err != nil {
		// Pod gone — nothing to reconcile.
		c.Log.WithError(err).Debugf("nvsnap controller: pod %s/%s gone; dropping", ns, name)
		c.queue.Forget(key)
		return false
	}

	// Re-check eligibility — informer can race with annotation removal
	// (we drop the annotation in the reconciler on success).
	if !PodEligibleForCheckpointOnWarm(pod) {
		c.queue.Forget(key)
		return false
	}

	rctx, cancel := context.WithCancel(ctx)
	if err := c.Reconciler.Reconcile(rctx, pod); err != nil {
		cancel()
		c.Log.WithError(err).WithFields(logrus.Fields{
			"pod":         ns + "/" + name,
			"retry_count": c.queue.NumRequeues(key),
		}).Warn("nvsnap controller: reconcile failed; will retry")
		c.queue.AddRateLimited(key)
		return false
	}
	cancel()
	c.queue.Forget(key)
	return false
}
