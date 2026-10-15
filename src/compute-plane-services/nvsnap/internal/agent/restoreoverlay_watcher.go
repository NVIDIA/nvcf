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

// Pod-delete watcher for OverlayFS cleanup (nvsnap#194).
//
// The webhook prepares an overlay per (pod UID, extract path) at
// admission time. Without a teardown path the agent would accumulate
// orphan overlay mounts + scratch trees on every pod cycle. This
// watcher subscribes to pod DELETE events for pods carrying the
// restore-from annotation and calls OverlayManager.Cleanup.
//
// A startup Sweep against the current live pod set covers the case
// where the agent crashed mid-flight and left mounts behind.

package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// overlaySweepInterval is the cadence at which the agent re-runs Sweep
// against the live restore-pod set, on top of the startup sweep and the
// per-event Cleanup. Catches leaks from missed DELETE events (informer
// down during a delete) and from overlays the webhook keyed under a
// stale ns_name after a CREATE→DELETE→CREATE on the same pod name.
const overlaySweepInterval = 5 * time.Minute

// startOverlayWatcher starts a goroutine that:
//
//  1. Runs an initial Sweep — any per-pod overlay scratch dir whose key
//     (UID or ns_name; the webhook keys by ns_name when admission UID is
//     empty per overlayKeyFor in internal/webhook/mutate.go) is not in
//     the current restore-pod live set gets unmounted + removed.
//  2. Subscribes to pod DELETE events cluster-wide. On delete of any pod
//     that carries nvsnap.io/restore-from, calls overlay.Cleanup keyed by
//     UID first then ns_name (handles both forms).
//  3. Re-runs Sweep every overlaySweepInterval. Catches leaks from
//     missed DELETE events (informer outage, agent restart between
//     CREATE and DELETE — observed 2026-06-08 when the v0.0.45 rollout
//     left an overlay live for a deleted pod the new agent never saw).
//
// kubeClient is nil-tolerated: the function logs and returns without
// starting the goroutine. Callers wire this in after they've populated
// a.kubeClient.
func (a *Agent) startOverlayWatcher(ctx context.Context, kubeClient kubernetes.Interface) error {
	if a.overlay == nil {
		return errors.New("startOverlayWatcher: overlay manager not initialised")
	}
	if kubeClient == nil {
		a.log.Warn("startOverlayWatcher: nil kubeClient — overlay cleanup-on-pod-delete disabled (Sweep still runs)")
		return a.overlay.Sweep(nil) // best-effort GC of anything stale
	}

	// Initial sweep: collect every restore-from pod's UID + ns_name and
	// prune scratch dirs that don't match either form. Failure here
	// doesn't gate admission; we log and continue.
	if alive, err := liveRestorePodKeys(ctx, kubeClient); err != nil {
		a.log.WithError(err).Warn("startOverlayWatcher: list restore pods failed; initial sweep skipped")
	} else if err := a.overlay.Sweep(alive); err != nil {
		a.log.WithError(err).Warn("startOverlayWatcher: initial sweep failed")
	}

	factory := informers.NewSharedInformerFactoryWithOptions(
		kubeClient, 30*time.Second,
	)
	informer := factory.Core().V1().Pods().Informer()
	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj any) { a.handleOverlayPodDelete(obj) },
	}); err != nil {
		return fmt.Errorf("AddEventHandler: %w", err)
	}

	go func() {
		factory.Start(ctx.Done())
		if !cache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
			a.log.Warn("overlay-watcher: informer cache sync failed")
			return
		}
		a.log.Info("overlay-watcher: running")

		// Periodic safety-net sweep: if an event was missed or the agent
		// missed a delete during its own restart window, this catches up
		// within overlaySweepInterval.
		ticker := time.NewTicker(overlaySweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				alive, err := liveRestorePodKeys(ctx, kubeClient)
				if err != nil {
					a.log.WithError(err).Warn("overlay-watcher: periodic sweep — list restore pods failed")
					continue
				}
				if err := a.overlay.Sweep(alive); err != nil {
					a.log.WithError(err).Warn("overlay-watcher: periodic sweep failed")
				}
			}
		}
	}()
	return nil
}

func (a *Agent) handleOverlayPodDelete(obj any) {
	pod, ok := podFromDeleteObj(obj)
	if !ok || pod == nil {
		return
	}
	if _, ok := pod.Annotations[webhook.RestoreFromAnnotation]; !ok {
		return
	}
	// Match the webhook's keying. Pod-delete events deliver a fully-
	// populated Pod (UID set), but the webhook may have keyed by
	// ns/name when admission lacked the UID. Try both: clean up by UID
	// first, then by ns/name. Cleanup is idempotent on missing keys.
	keys := []string{}
	if uid := string(pod.UID); uid != "" {
		keys = append(keys, uid)
	}
	if pod.Namespace != "" && pod.Name != "" {
		keys = append(keys, pod.Namespace+"_"+pod.Name)
	}
	for _, k := range keys {
		if err := a.overlay.Cleanup(k); err != nil {
			a.log.WithError(err).WithField("overlayKey", k).Warn("overlay-watcher: Cleanup failed")
		}
	}
}

// podFromDeleteObj unwraps the two shapes a Delete event hands you:
// the *corev1.Pod itself, or a cache.DeletedFinalStateUnknown when the
// informer missed the delete and is catching up.
func podFromDeleteObj(obj any) (*corev1.Pod, bool) {
	if pod, ok := obj.(*corev1.Pod); ok {
		return pod, true
	}
	tomb, ok := obj.(cache.DeletedFinalStateUnknown)
	if !ok {
		return nil, false
	}
	pod, ok := tomb.Obj.(*corev1.Pod)
	return pod, ok
}

// liveRestorePodKeys returns the set of every overlay-cache key that
// corresponds to a currently-alive restore-from pod. Both UID and the
// ns_name fallback are included because the webhook uses ns_name when
// admission pod.UID is empty (see overlayKeyFor in
// internal/webhook/mutate.go). Sweep treats the set as "do NOT delete
// these dirs"; any per-pod overlay dir whose name matches either form
// for a live pod is kept.
func liveRestorePodKeys(ctx context.Context, kc kubernetes.Interface) (map[string]struct{}, error) {
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pods, err := kc.CoreV1().Pods("").List(listCtx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, 2*len(pods.Items))
	for i := range pods.Items {
		p := &pods.Items[i]
		if _, ok := p.Annotations[webhook.RestoreFromAnnotation]; !ok {
			continue
		}
		if uid := string(p.UID); uid != "" {
			out[uid] = struct{}{}
		}
		if p.Namespace != "" && p.Name != "" {
			out[p.Namespace+"_"+p.Name] = struct{}{}
		}
	}
	return out, nil
}
