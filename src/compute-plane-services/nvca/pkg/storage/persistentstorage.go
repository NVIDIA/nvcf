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
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
)

const (
	internalPersistentStorageResourceQuota = "nvcf-internal-persistent-storage-quota"
	defaultStorageRequest                  = "500Gi"
)

func (r *Reconciler) doInternalPersistentStorage(
	ctx context.Context,
	st,
	stCopy *nvcav1new.StorageRequest,
) (reconcile.Result, error) {
	log := logf.FromContext(ctx)

	resQuota := newInternalPersistentStorageResourceQuota(ctx, st)
	log.V(1).Info("Internal persistent storage request reconciliation started", "phase", st.Status.Phase)

	switch st.Status.Phase {
	case "":
		stCopy.Status.Phase = nvcav1new.StoragePending
	case nvcav1new.StoragePending:
		if err := r.applyControlled(ctx, st, resQuota); err != nil {
			if errors.IsAlreadyExists(err) {
				return reconcile.Result{Requeue: true}, nil
			}
			log.Error(err, "failed to create internal-persistent-storage resources")
			stCopy.Status.Phase = nvcav1new.StorageFailed
		} else {
			stCopy.Status.Phase = nvcav1new.StorageInitRunning
		}
	case nvcav1new.StorageInitRunning:
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(resQuota), resQuota); err != nil {
			if errors.IsNotFound(err) {
				log.V(1).Error(err, "requeueing to retry resourcequota fetch")
				return reconcile.Result{RequeueAfter: 100 * time.Millisecond}, nil
			}
			return reconcile.Result{}, err
		}
		// If the status has not been updated yet, loop back again
		if !reflect.DeepEqual(resQuota.Spec.Hard, resQuota.Status.Hard) {
			return reconcile.Result{RequeueAfter: defaultRequeueDelay}, nil
		}
		// Update the status now that it is ready
		stCopy.Status.InternalPersistentStorage = &nvcav1new.InternalPersistentStorageStatus{
			StorageClassName: st.Spec.InternalPersistentStorage.StorageClassName,
		}
		// All resource quotas have been created
		stCopy.Status.Phase = nvcav1new.StorageReady
	case nvcav1new.StorageReady:
		// validate resource quota has not been removed
		// if it has been removed we should mark as failed
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(resQuota), resQuota); err != nil {
			if errors.IsNotFound(err) {
				log.V(1).Error(err, "internal persistent storage resource quota was deleted, marking as failed")
				stCopy.Status.Phase = nvcav1new.StorageFailed
			} else {
				return reconcile.Result{RequeueAfter: defaultRequeueDelay}, err
			}
		}
	case nvcav1new.StorageFailed:
	case nvcav1new.StorageRuntimeError:
	default:
		return reconcile.Result{}, reconcile.TerminalError(fmt.Errorf("unknown phase: %s", st.Status.Phase))
	}

	// If this is the first instance of storage failing, mark it as such
	if st.Status.Phase != nvcav1new.StorageFailed && stCopy.Status.Phase == nvcav1new.StorageFailed {
		// We're going to ignore the reqeue here and rely on the final deletion to handle the cleanup
		if _, err := doCleanupNamespaced(ctx, r.Client, st); err != nil {
			log.Error(err, "failed to cleanup internal persisent storage resources on storage failure, needs manual cleanup")
		}
	}

	return reconcile.Result{}, nil
}

func newInternalPersistentStorageResourceQuota(_ context.Context, st *nvcav1new.StorageRequest) *corev1.ResourceQuota {
	resQuota := &corev1.ResourceQuota{}
	resQuota.Name = internalPersistentStorageResourceQuota
	resQuota.Namespace = st.Namespace

	// Add requests.storage for the specific storage class only at this point
	requestStorage, ok := st.Spec.InternalPersistentStorage.ResourceQuota.Hard[corev1.ResourceRequestsStorage]
	if !ok || requestStorage.IsZero() {
		requestStorage = resource.MustParse(defaultStorageRequest)
	}
	requestStorageKey := corev1.ResourceName(
		fmt.Sprintf("%s.storageclass.storage.k8s.io/requests.storage",
			st.Spec.InternalPersistentStorage.StorageClassName))
	resQuota.Spec.Hard = corev1.ResourceList{
		requestStorageKey: requestStorage,
	}
	return resQuota
}

// NewInternalPersistentStorageOwnerMergePatch creates a merge patch to patch an object
// with labels such that cluster-wide resources get cleaned up by the storage controller
// as they would if owned by the controller.
func NewInternalPersistentStorageOwnerMergePatch(v client.Object, namespace string) ([]byte, error) {
	oldObj, newObj := v.DeepCopyObject(), v.DeepCopyObject().(client.Object)

	newObjLabels := newObj.GetLabels()
	if newObjLabels == nil {
		newObjLabels = map[string]string{}
	}
	newObjLabels[StorageRequestOwnerKey] = nvcav1new.InternalPersistentStorageRequest.Name()
	newObjLabels[StorageRequestNamespaceKey] = namespace
	newObj.SetLabels(newObjLabels)

	oldB, err := json.Marshal(oldObj)
	if err != nil {
		return nil, err
	}
	newB, err := json.Marshal(newObj)
	if err != nil {
		return nil, err
	}
	return strategicpatch.CreateTwoWayMergePatch(oldB, newB, v)
}
