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

package storage

import (
	"context"
	"fmt"
	"sync"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	coordv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
)

func NewModelCacheInitNamespace() *corev1.Namespace {
	namespace := &corev1.Namespace{}
	namespace.Name = ModelCacheInitNamespace
	namespace.Labels = map[string]string{
		"app.kubernetes.io/managed-by": "nvca",
	}
	return namespace
}

// buildControllerModelCache is different from other controller builders because it adds runnables
// to the manager and watches resources cluster-wide.
func buildControllerModelCache(r *Reconciler, mgr ctrl.Manager, _ ControllerOptions) error {
	storageReqType := nvcav1new.ModelCacheRequest

	// if err := setModelCacheHandleIndex(mgr.GetFieldIndexer()); err != nil {
	// 	return fmt.Errorf("failed to index model cache handle field: %v", err)
	// }
	if err := setStorageRequestNameIndex(mgr.GetFieldIndexer()); err != nil {
		return fmt.Errorf("failed to index storage request name field: %v", err)
	}

	// Run cleanup thread alongside controller.
	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		log := logf.FromContext(ctx)
		// Run immediately, then every tick.
		if err := r.cleanupIdleModelCaches(ctx); err != nil {
			log.Error(err, "Error cleaning up idle model caches on init")
		}

		t := time.NewTicker(r.k8sTimeConfig.ModelCacheIdleCleanupPeriod)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-t.C:
				if err := r.cleanupIdleModelCaches(ctx); err != nil {
					log.Error(err, "Error cleaning up idle model caches")
				}
			}
		}
	})); err != nil {
		return fmt.Errorf("add model cache idle cleanup runnable: %v", err)
	}

	if err := mgr.Add(r.initStatuses); err != nil {
		return fmt.Errorf("add init modelcache status cache runnable: %v", err)
	}

	// The fan-out handlers are used to map primary and secondary objects to one or more
	// storage requests. Primary object reconciliation all happens within the model cache init namespace
	// or on the primary PV, and fans out to all waiting storage requests.
	// Secondary object reconciliation only targets one storage request.
	fanOutPredicateOption := builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
		_, _, _, acceptEvent := checkModelCacheFanOutEvent(obj)
		return acceptEvent
	}))
	fanOutEventHandler := handler.TypedEnqueueRequestsFromMapFunc(getModelCacheFanOutEventHandlerMapFunc(mgr.GetClient()))
	return builder.
		ControllerManagedBy(mgr).
		Named(string(storageReqType)).
		For(&nvcav1new.StorageRequest{}, fanOutPredicateOption).
		Watches(&batchv1.Job{}, fanOutEventHandler).
		Watches(&corev1.Pod{}, fanOutEventHandler).
		Watches(&corev1.PersistentVolumeClaim{}, fanOutEventHandler).
		Watches(&coordv1.Lease{}, fanOutEventHandler).
		Watches(&corev1.PersistentVolume{}, fanOutEventHandler).
		Complete(r)
}

func checkModelCacheFanOutEvent(obj client.Object) (
	cacheHandle string,
	stName, stNamespace string,
	acceptEvent bool,
) {
	// Reconcile the storage request itself.
	if t, ok := obj.(*nvcav1new.StorageRequest); ok {
		stName, stNamespace = t.Name, t.Namespace
		acceptEvent = t.Spec.Type == nvcav1new.ModelCacheRequest
		return //nolint:nakedret
	}

	labels := obj.GetLabels()
	if labels == nil {
		return //nolint:nakedret
	}

	// Fan out to multiple storage requests.
	if cacheHandle = labels[modelCacheHandleLabelKey]; cacheHandle != "" {
		acceptEvent = true
		return //nolint:nakedret
	}

	// Reconcile the storage request itself from a child object.
	stName = labels[StorageRequestOwnerKey]
	stNamespace = labels[StorageRequestNamespaceKey]
	ownerRef, foundOwnerRef := getStorageRequestOwnerReference(nvcav1new.ModelCacheRequest, obj)
	if foundOwnerRef && stName == "" && stNamespace == "" {
		stName, stNamespace = ownerRef.Name, obj.GetNamespace()
	}
	acceptEvent = stName != "" && stName == nvcav1new.ModelCacheRequest.Name() && stNamespace != ""
	return //nolint:nakedret
}

func getModelCacheFanOutEventHandlerMapFunc(c client.Client) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) (reqs []reconcile.Request) {
		cacheHandle, stName, stNamespace, acceptEvent := checkModelCacheFanOutEvent(obj)
		if !acceptEvent {
			return nil
		}

		var typeStr string
		if gvk := obj.GetObjectKind().GroupVersionKind(); gvk != (schema.GroupVersionKind{}) {
			typeStr = gvk.String()
		} else {
			typeStr = fmt.Sprintf("%T", obj)
		}

		log := logf.FromContext(ctx).WithValues(
			"type", typeStr,
			"name", obj.GetName(),
		)

		stList := &nvcav1new.StorageRequestList{}
		if cacheHandle != "" {
			// List is backed by a cache so this is fairly quick.
			if err := c.List(ctx, stList, client.MatchingLabels{
				modelCacheHandleLabelKey: cacheHandle,
			}); err != nil {
				log.Error(err, "Failed to list storage requests for fan-out event")
				return nil
			}
			for _, st := range stList.Items {
				log.V(2).Info("Fanning out event for modelcache object",
					"st_name", st.Name, "st_namespace", st.Namespace)
				req := reconcile.Request{}
				req.Name, req.Namespace = st.Name, st.Namespace
				reqs = append(reqs, req)
			}
		} else {
			log.V(2).Info("Reconciling specific storage request for modelcache object",
				"st_name", stName, "st_namespace", stNamespace)
			req := reconcile.Request{}
			req.Name, req.Namespace = stName, stNamespace
			reqs = append(reqs, req)
		}

		return reqs
	}
}

type statusCache interface {
	manager.Runnable
	manager.LeaderElectionRunnable
	sync.Locker
	RLock()
	RUnlock()
	get(cacheHandle string) (nvcav1new.StorageRequestStatus, bool)
	put(cacheHandle string, ss nvcav1new.StorageRequestStatus)
	delete(cacheHandle string)
	keys() []string
}

type initStatusCache struct {
	c client.Client
	m map[string]nvcav1new.StorageRequestStatus
	sync.RWMutex
}

func newInitStatusCache(c client.Client) statusCache {
	return &initStatusCache{
		c: c,
		m: map[string]nvcav1new.StorageRequestStatus{},
	}
}

var (
	_ manager.Runnable               = (*initStatusCache)(nil)
	_ manager.LeaderElectionRunnable = (*initStatusCache)(nil)
)

func (s *initStatusCache) Start(ctx context.Context) error {
	log := logf.FromContext(ctx)

	log.Info("Starting init status cache")

	s.Lock()
	defer s.Unlock()

	// List is backed by a cache so this is fairly quick.
	stList := &nvcav1new.StorageRequestList{}
	if err := s.c.List(ctx, stList, client.MatchingFields{
		objectNameFieldPath: nvcav1new.ModelCacheRequest.Name(),
	}); err != nil {
		log.Error(err, "Failed to list storage requests for init cache status")
		return nil
	}

	// Existing leases denote which model caches are still initializing.
	leaseList := &coordv1.LeaseList{}
	if err := s.c.List(ctx, leaseList); err != nil {
		log.Error(err, "Failed to list leases for init cache status")
		return nil
	}
	activeCacheHandles := sets.New[string]()
	leaseHolders := sets.New[string]()
	for _, lease := range leaseList.Items {
		if lease.Labels == nil {
			continue
		}
		cacheHandle := lease.Labels[modelCacheHandleLabelKey]
		if cacheHandle == "" {
			continue
		}
		activeCacheHandles.Insert(cacheHandle)
		if lease.Spec.HolderIdentity != nil {
			leaseHolders.Insert(*lease.Spec.HolderIdentity)
		}
	}

	for _, st := range stList.Items {
		if st.Spec.ModelCache == nil || st.Spec.ModelCache.CacheHandle == "" {
			continue
		}
		// Model cache requests not initializing should be ignored.
		if !leaseHolders.Has(st.Spec.ICMSRequestName) || !activeCacheHandles.Has(st.Spec.ModelCache.CacheHandle) ||
			(st.Status.ModelCache != nil && st.Status.ModelCache.ROPVCName != "") {
			continue
		}
		log.V(1).Info("Adding cache handle with lease owner's phase to init cache status set",
			"cacheHandle", st.Spec.ModelCache.CacheHandle, "phase", st.Status.Phase)
		s.put(st.Spec.ModelCache.CacheHandle, st.Status)
	}
	return nil
}

func (s *initStatusCache) NeedLeaderElection() bool { return false }

func (s *initStatusCache) get(cacheHandle string) (nvcav1new.StorageRequestStatus, bool) {
	ss, ok := s.m[cacheHandle]
	return ss, ok
}

func (s *initStatusCache) put(cacheHandle string, ss nvcav1new.StorageRequestStatus) {
	s.m[cacheHandle] = ss
}

func (s *initStatusCache) delete(cacheHandle string) {
	delete(s.m, cacheHandle)
}

func (s *initStatusCache) keys() []string {
	keys := make([]string, len(s.m))
	i := 0
	for key := range s.m {
		keys[i] = key
		i++
	}
	return keys
}

const (
	objectNameFieldPath = "metadata.name"
)

func setStorageRequestNameIndex(fi client.FieldIndexer) error {
	return fi.IndexField(context.Background(),
		&nvcav1new.StorageRequest{},
		objectNameFieldPath,
		objectNameExtractValues,
	)
}

func objectNameExtractValues(o client.Object) []string { return []string{o.GetName()} }

// TODO: migrate from labels to CRD "spec.fields[*].selectableFields" once all clusters are upgraded to k8s 1.32+
// https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/custom-resources/#crd-selectable-fields
//
// const modelCacheHandleFieldPath = "spec.modelCache.cacheHandle"
//
// func setModelCacheHandleIndex(fi client.FieldIndexer) error {
// 	return fi.IndexField(context.Background(),
// 		&nvcav1new.StorageRequest{},
// 		modelCacheHandleFieldPath,
// 		modelCacheHandleExtractValues,
// 	)
// }
//
// func modelCacheHandleExtractValues(o client.Object) []string {
// 	st := o.(*nvcav1new.StorageRequest)
// 	if st.Spec.ModelCache == nil {
// 		return nil
// 	}
// 	return []string{st.Spec.ModelCache.CacheHandle}
// }
