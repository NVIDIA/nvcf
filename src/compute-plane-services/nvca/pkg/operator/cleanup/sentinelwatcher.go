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

package cleanup

import (
	"context"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// SentinelWatcher monitors the shutdown sentinel ConfigMap for deletion.
type SentinelWatcher struct {
	namespace    string
	k8sClient    kubernetes.Interface
	onShutdown   func(ctx context.Context)
	shutdownOnce sync.Once
}

// NewSentinelWatcher creates a new SentinelWatcher.
func NewSentinelWatcher(namespace string, k8sClient kubernetes.Interface, onShutdown func(ctx context.Context)) *SentinelWatcher {
	return &SentinelWatcher{
		namespace:  namespace,
		k8sClient:  k8sClient,
		onShutdown: onShutdown,
	}
}

// Start starts watching the sentinel ConfigMap and blocks until the context is cancelled.
// When the sentinel is being deleted (deletionTimestamp is set), it calls the onShutdown callback.
func (w *SentinelWatcher) Start(ctx context.Context) {
	log := core.GetLogger(ctx)

	// Create an informer factory scoped to our namespace
	factory := informers.NewSharedInformerFactoryWithOptions(
		w.k8sClient,
		30*time.Second,
		informers.WithNamespace(w.namespace),
	)

	cmInformer := factory.Core().V1().ConfigMaps().Informer()
	_, _ = cmInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(_, newObj any) {
			cm, ok := newObj.(*corev1.ConfigMap)
			if !ok {
				return
			}
			if cm.Name != ShutdownSentinelConfigMapName {
				return
			}

			// Check if being deleted (deletionTimestamp is set)
			if cm.DeletionTimestamp != nil {
				w.triggerShutdown(ctx)
			}
		},
		DeleteFunc: func(obj any) {
			cm, ok := obj.(*corev1.ConfigMap)
			if !ok {
				// Handle DeletedFinalStateUnknown
				tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
				if !ok {
					return
				}
				cm, ok = tombstone.Obj.(*corev1.ConfigMap)
				if !ok {
					return
				}
			}
			if cm.Name != ShutdownSentinelConfigMapName {
				return
			}

			w.triggerShutdown(ctx)
		},
	})

	// Start the informer
	factory.Start(ctx.Done())

	// Wait for cache sync
	if !cache.WaitForCacheSync(ctx.Done(), cmInformer.HasSynced) {
		log.Error("Failed to sync sentinel ConfigMap cache")
		return
	}

	// Check initial state - if sentinel is already being deleted
	isDeleting, err := IsSentinelBeingDeleted(ctx, w.k8sClient, w.namespace, ShutdownSentinelConfigMapName)
	if err != nil {
		log.WithError(err).Warn("Failed to check initial sentinel state")
	} else if isDeleting {
		log.Infof("Sentinel ConfigMap %s/%s is already being deleted, triggering shutdown",
			w.namespace, ShutdownSentinelConfigMapName)
		w.triggerShutdown(ctx)
	}

	log.Infof("Sentinel watcher started for %s/%s", w.namespace, ShutdownSentinelConfigMapName)

	// Block until context is cancelled
	<-ctx.Done()
}

// triggerShutdown calls the onShutdown callback once
func (w *SentinelWatcher) triggerShutdown(ctx context.Context) {
	w.shutdownOnce.Do(func() {
		log := core.GetLogger(ctx)
		log.Infof("Sentinel ConfigMap %s/%s is being deleted, triggering shutdown",
			w.namespace, ShutdownSentinelConfigMapName)

		if w.onShutdown != nil {
			w.onShutdown(ctx)
		}
	})
}
