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

package mscontroller

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
)

func (r *Reconciler) doStorageRequests(ctx context.Context,
	ms *v1alpha1.MiniService,
	icmsReq *nvcav2beta1.ICMSRequest,
	objMutators objectMutatorSet,
	workerImagePullSecrets []*corev1.Secret,
	cacheInitJob *batchv1.Job,
	cacheInitPVC *corev1.PersistentVolumeClaim,
) (allReady bool, err error) {
	log := logf.FromContext(ctx)

	sts, err := r.makeStorageRequests(icmsReq, workerImagePullSecrets, cacheInitJob, cacheInitPVC)
	if err != nil {
		log.Error(err, "Failed to make StorageRequests")
		return false, reconcile.TerminalError(err)
	}
	stNames := sets.New[string]()
	for _, st := range sts {
		if err := r.create(ctx, ms, objMutators, nil, r.Client, st); err != nil {
			log.Error(err, "Failed to create StorageRequest")
			return false, err
		}
		// Mark the miniservice as caching in progress for agent to handle.
		if st.Spec.Type == nvcav2beta1.ModelCacheRequest {
			ms.Status.Phase = v1alpha1.MiniServiceCacheInProgress
		}
		stNames.Insert(st.Name)
	}

	stList := &nvcav2beta1.StorageRequestList{}
	if err := r.Client.List(ctx, stList, client.InNamespace(ms.Spec.Namespace)); err != nil {
		return false, err
	}

	for _, st := range stList.Items {
		switch st.Status.Phase {
		case nvcav2beta1.StorageFailed, nvcav2beta1.StorageRuntimeError:
			lerr := fmt.Errorf("storage request %s has failed", st.Spec.Type)
			switch st.Spec.Type {
			case nvcav2beta1.ModelCacheRequest:
				log.Error(lerr, "Storage failed, model caching will be disabled", "phase", st.Status.Phase)
				meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
					Type:    v1alpha1.MiniServiceConditionCacheSuccessful,
					Status:  metav1.ConditionFalse,
					Reason:  v1alpha1.MiniServiceStatusReasonCachingFailed,
					Message: "Model caching failed, continuing install without a cache",
				})
				// Clear the name to indicate that the reconciler can proceed.
				stNames.Delete(st.Name)
			case nvcav2beta1.SharedStorageRequest:
				log.Error(lerr, "Shared storage failed", "phase", st.Status.Phase)
				return false, reconcile.TerminalError(lerr)
			case nvcav2beta1.InternalPersistentStorageRequest:
				log.Error(lerr, "Internal persistent storage failed", "phase", st.Status.Phase)
				return false, reconcile.TerminalError(lerr)
			}
		case nvcav2beta1.StorageReady:
			switch st.Spec.Type {
			case nvcav2beta1.ModelCacheRequest:
				meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
					Type:   v1alpha1.MiniServiceConditionCacheSuccessful,
					Status: metav1.ConditionTrue,
					Reason: v1alpha1.MiniServiceStatusReasonArtifactsCached,
				})
			case nvcav2beta1.SharedStorageRequest:
			case nvcav2beta1.InternalPersistentStorageRequest:
			}

			stNames.Delete(st.Name)

			log.V(1).Info("StorageRequest succeeded", "type", st.Spec.Type)
		default:
			switch st.Spec.Type {
			case nvcav2beta1.ModelCacheRequest:
				meta.SetStatusCondition(&ms.Status.Conditions, metav1.Condition{
					Type:   v1alpha1.MiniServiceConditionCacheSuccessful,
					Status: metav1.ConditionFalse,
					Reason: v1alpha1.MiniServiceStatusReasonCachingInProgress,
				})
			case nvcav2beta1.SharedStorageRequest:
			case nvcav2beta1.InternalPersistentStorageRequest:
			}
			log.V(1).Info("Storage is in progress, waiting for status update",
				"type", st.Spec.Type, "phase", st.Status.Phase)
		}
	}

	return stNames.Len() == 0, nil
}

func (r *Reconciler) makeStorageRequests(
	icmsReq *nvcav2beta1.ICMSRequest,
	workerImagePullSecrets []*corev1.Secret,
	cacheInitJob *batchv1.Job,
	cacheInitPVC *corev1.PersistentVolumeClaim,
) (sts []*nvcav2beta1.StorageRequest, err error) {
	if r.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.CachingSupport) &&
		r.FeatureFlagFetcher.IsFeatureFlagEnabled(featureflag.HelmCachingSupport) &&
		cacheInitJob != nil && cacheInitPVC != nil {
		st, err := nvcastorage.NewModelCacheStorageRequest(icmsReq, r.FeatureFlagFetcher)
		if err != nil {
			return nil, err
		}
		sts = append(sts, st)
	}
	if r.FeatureFlagFetcher.IsFeatureFlagEnabled(&featureflag.HelmSharedStorage.FeatureFlag) {
		sts = append(sts, nvcastorage.NewSharedStorageRequest(icmsReq, r.FeatureFlagFetcher, r.cfg,
			workerImagePullSecrets))
	}
	if r.FeatureFlagFetcher.IsFeatureFlagEnabled(&featureflag.HelmInternalPersistentStorage.FeatureFlag) {
		sts = append(sts, nvcastorage.NewInternalPersistentStorageRequest(icmsReq,
			featureflag.HelmInternalPersistentStorage.Spec,
			r.FeatureFlagFetcher))
	}
	return sts, nil
}

func getAnnotationsForReadyStorageRequests(stList *nvcav2beta1.StorageRequestList) (
	instanceAnnos, utilsAnnos map[string]string,
) {
	if len(stList.Items) == 0 {
		return nil, nil
	}
	instanceAnnos, utilsAnnos = map[string]string{}, map[string]string{}
	for _, st := range stList.Items {
		switch st.Spec.Type {
		case nvcav2beta1.ModelCacheRequest:
			// Model caching is allowed to fail.
			if st.Status.ModelCache != nil {
				instanceAnnos[nvcastorage.WebhookModelCachePVCNameAnnotationKey] = st.Status.ModelCache.ROPVCName
				utilsAnnos[nvcastorage.WebhookModelCachePVCNameAnnotationKey] = st.Status.ModelCache.ROPVCName
			}
		case nvcav2beta1.SharedStorageRequest:
			instanceAnnos[nvcastorage.HelmWebhookSharedStorageSecretsReadOnlyPVCNameAnnotationKey] =
				st.Status.SharedStorage.Secrets.ReadOnlyPVCName
			utilsAnnos[nvcastorage.HelmWebhookSharedStorageSecretsReadWritePVCNameAnnotationKey] =
				st.Status.SharedStorage.Secrets.ReadWritePVCName
			instanceAnnos[nvcastorage.HelmWebhookSharedStorageKNSReadOnlyPVCNameAnnotationKey] =
				st.Status.SharedStorage.KNS.ReadOnlyPVCName
			utilsAnnos[nvcastorage.HelmWebhookSharedStorageKNSReadWritePVCNameAnnotationKey] =
				st.Status.SharedStorage.KNS.ReadWritePVCName
		case nvcav2beta1.InternalPersistentStorageRequest:
			instanceAnnos[nvcastorage.HelmWebhookInternalPersistentStorageStorageClassNameAnnotationKey] =
				st.Status.InternalPersistentStorage.StorageClassName
		}
	}

	return instanceAnnos, utilsAnnos
}
