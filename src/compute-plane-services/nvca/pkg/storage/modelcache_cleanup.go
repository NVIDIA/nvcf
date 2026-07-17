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
	"errors"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	coordv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
)

// errVolumeStillAttached signals that a cache init writer volume has not yet
// detached. Cleanup treats it as a requeue, not a failure, so the single
// reconcile worker is never blocked polling for detachment.
var errVolumeStillAttached = errors.New("model cache init writer volume still attached")

// volumeDetachRequeueInterval is how soon cleanup retries after finding the
// writer volume still attached.
const volumeDetachRequeueInterval = 2 * time.Second

var primaryPVSel labels.Selector

func init() {
	primaryPVSel = labels.NewSelector()
	for _, existsKey := range []string{
		primaryPVLabelKey,
	} {
		req, err := labels.NewRequirement(existsKey, selection.Exists, nil)
		if err != nil {
			panic(err)
		}
		primaryPVSel.Add(*req)
	}
}

func (r *Reconciler) doCleanupModelCacheNVMesh(ctx context.Context, st *nvcav1new.StorageRequest) (reconcile.Result, error) { //nolint
	log := logf.FromContext(ctx)

	log.Info("Cleaning model cache for storage request")

	// Set the cleanup condition to pending in case of errors.
	if meta.FindStatusCondition(st.Status.Conditions, ConditionTypeCleanupSuccessful) == nil {
		meta.SetStatusCondition(&st.Status.Conditions, metav1.Condition{
			Type:   ConditionTypeCleanupSuccessful,
			Status: metav1.ConditionFalse,
			Reason: ConditionReasonSomeObjectsPendingDeletion,
		})
	}

	var errs []error
	errs = append(errs, r.cleanupInitModelCache(ctx, st, false)...)

	cwResourceLabels := getClusterWideResourceLabels(st)

	pvList := &corev1.PersistentVolumeList{}
	if err := r.Client.List(ctx, pvList, client.MatchingLabels(cwResourceLabels)); err != nil {
		log.Error(err, "Failed to list PVs for storage request")
		return reconcile.Result{}, errors.Join(append(errs, err)...)
	}

	pvcList := &corev1.PersistentVolumeClaimList{}
	if err := r.Client.List(ctx, pvcList,
		client.MatchingLabels(cwResourceLabels),
		client.InNamespace(st.Namespace),
	); err != nil {
		log.Error(err, "Failed to list PVCs for storage request")
		return reconcile.Result{}, errors.Join(append(errs, err)...)
	}

	// PVC's can be deleted before pods are, and will be finalized once the pod is deleted.
	for _, pvc := range pvcList.Items {
		if pvc.DeletionTimestamp != nil {
			log.V(1).Info("PVC has already been deleted", "pvc", pvc.Name)
		} else if err := r.Client.Delete(ctx, &pvc); err != nil && !apierrors.IsNotFound(err) {
			log.Error(err, "Failed to delete PVC, manual cleanup needed", "pvc", pvc.Name)
			errs = append(errs, err)
		}
	}

	// Secondary PV's should NOT have reclaim policy set to "Delete" on termination.
	// Only the primary PV should on termination, to preserve the NVMesh volume.
	for _, pv := range pvList.Items {
		if pv.DeletionTimestamp != nil {
			log.V(1).Info("PV has already been deleted", "pv", pv.Name)
		} else if err := r.Client.Delete(ctx, &pv); err != nil && !apierrors.IsNotFound(err) {
			log.Error(err, "Failed to delete PV, manual cleanup needed", "pv", pv.Name)
			errs = append(errs, err)
		}
	}

	// A still-attaching writer volume is not a failure: requeue and retry without
	// blocking the single reconcile worker (the previous in-reconcile poll could
	// starve every other StorageRequest, including the terminating-namespace
	// finalizer escape hatch). Separate that sentinel from real errors.
	var realErrs []error
	pendingDetach := false
	for _, e := range errs {
		if errors.Is(e, errVolumeStillAttached) {
			pendingDetach = true
			continue
		}
		realErrs = append(realErrs, e)
	}

	if len(realErrs) == 0 && pendingDetach {
		meta.SetStatusCondition(&st.Status.Conditions, metav1.Condition{
			Type:    ConditionTypeCleanupSuccessful,
			Status:  metav1.ConditionFalse,
			Reason:  ConditionReasonSomeObjectsPendingDeletion,
			Message: "waiting for cache init volume to detach before deleting the writer PVC",
		})
		return reconcile.Result{RequeueAfter: volumeDetachRequeueInterval}, nil
	}

	if len(realErrs) == 0 {
		meta.SetStatusCondition(&st.Status.Conditions, metav1.Condition{
			Type:    ConditionTypeCleanupSuccessful,
			Status:  metav1.ConditionTrue,
			Reason:  ConditionReasonAllObjectsDeleted,
			Message: "All init and secondary model cache objects were cleaned up",
		})
	} else {
		meta.SetStatusCondition(&st.Status.Conditions, metav1.Condition{
			Type:    ConditionTypeCleanupSuccessful,
			Status:  metav1.ConditionFalse,
			Reason:  ConditionReasonSomeObjectsPendingDeletion,
			Message: fmt.Sprintf("errors encountered while cleaning up: %+q", realErrs),
		})
	}

	return reconcile.Result{}, errors.Join(realErrs...)
}

// cleanupInitModelCache deletes the init objects (job, pods, lease, pull
// secrets, writer PV/PVC) for the request's cache handle. retainWriterPVC
// keeps the writer RW PVC and its volume: the shared-FS backend retains the
// bound writer claim as the durable populated marker, but its init job and
// lease still must not accumulate per cache handle.
func (r *Reconciler) cleanupInitModelCache(ctx context.Context, st *nvcav1new.StorageRequest, retainWriterPVC bool) (errs []error) {
	log := logf.FromContext(ctx)

	log.V(1).Info("Cleaning up model cache init objects", "retainWriterPVC", retainWriterPVC)

	listOpts := []client.ListOption{
		client.MatchingLabels(map[string]string{
			modelCacheHandleLabelKey: st.Spec.ModelCache.CacheHandle,
		}),
		client.InNamespace(ModelCacheInitNamespace),
	}

	// Delete Job and its Pods first.
	jobList := &batchv1.JobList{}
	if err := r.Client.List(ctx, jobList, listOpts...); err != nil {
		log.Error(err, "Init job list failed, manual cleanup needed")
		errs = append(errs, err)
	}
	switch l := len(jobList.Items); l {
	case 0:
	case 1:
		job := jobList.Items[0]
		if job.DeletionTimestamp == nil {
			log.V(1).Info("Deleting model cache init job", "job", job.Name)
			if err := r.Client.Delete(ctx, &job,
				client.PropagationPolicy(metav1.DeletePropagationForeground),
			); err != nil && !apierrors.IsNotFound(err) {
				log.Error(err, "Init job delete failed, manual cleanup needed")
				errs = append(errs, err)
			}
		}
	default:
		// This should never happen, but log it in case there's a bug.
		log.Error(fmt.Errorf("unexpected number of init jobs"),
			"Found more than one init job to delete", "found", l)
	}

	// Attempt to explicitly delete pods since they may have a grace period.
	podList := &corev1.PodList{}
	if err := r.Client.List(ctx, podList, listOpts...); err != nil {
		log.Error(err, "Init job pod list failed, manual cleanup needed")
		errs = append(errs, err)
	}
	gracePeriod := 0
	// There could be some failed pods.
	for _, pod := range podList.Items {
		log.V(1).Info("Deleting model cache init job pod", "pod", pod.Name)
		if err := r.Client.Delete(ctx, &pod,
			client.GracePeriodSeconds(gracePeriod),
		); err != nil && !apierrors.IsNotFound(err) {
			log.Error(err, "Init job pod delete failed, manual cleanup needed")
			errs = append(errs, err)
		}
	}

	if !retainWriterPVC {
		// Delete the RW PVC once all Pods are deleted.
		pvcList := &corev1.PersistentVolumeClaimList{}
		if err := r.Client.List(ctx, pvcList, listOpts...); err != nil {
			log.Error(err, "RW PVC list failed, manual cleanup needed")
			errs = append(errs, err)
		}
		switch l := len(pvcList.Items); l {
		case 0:
		case 1:
			pvc := pvcList.Items[0]
			if pvc.DeletionTimestamp == nil {
				// Delete the RW PVC only once its volume has detached, but NEVER block
				// the single reconcile worker polling for it: a single-shot check that
				// returns a requeue sentinel when still attached (see isVolumeDetached).
				deletePVC := true
				if pvc.Spec.VolumeName != "" {
					detached, err := r.isVolumeDetached(ctx, pvc.Spec.VolumeName)
					switch {
					case err != nil:
						log.Error(err, "Failed to check PV detachment", "pv", pvc.Spec.VolumeName)
						errs = append(errs, err)
						deletePVC = false
					case !detached:
						log.V(1).Info("Init RW PVC volume still attached, will requeue", "pv", pvc.Spec.VolumeName)
						errs = append(errs, errVolumeStillAttached)
						deletePVC = false
					}
				}
				if deletePVC {
					log.V(1).Info("Deleting model cache init RW PVC", "pvc", pvc.Name)
					if err := r.Client.Delete(ctx, &pvc); err != nil && !apierrors.IsNotFound(err) {
						log.Error(err, "Init RW PVC delete failed, manual cleanup needed", "pvc", pvc.Name)
						errs = append(errs, err)
					}
				}
			}
		default:
			// This should never happen, but log it in case there's a bug.
			log.Error(fmt.Errorf("unexpected number of init RW PVCs"),
				"Found more than one PVC to delete", "found", l)
		}

		// The Samba writer binds a static plumbing SMB PV; delete it with the writer
		// PVC. Static PVs have no provisioner, so a Released one leaks forever, and
		// its stale claimRef would block a later writer PVC from re-binding.
		// NotFound is the normal case for the other backends.
		sambaWriterPV := &corev1.PersistentVolume{}
		sambaWriterPV.Name = sambaModelCacheWriterPVName(st.Spec.ModelCache.CacheHandle)
		if err := r.Client.Delete(ctx, sambaWriterPV); err != nil && !apierrors.IsNotFound(err) {
			log.Error(err, "Samba writer PV delete failed, manual cleanup needed", "pv", sambaWriterPV.Name)
			errs = append(errs, err)
		}
	}

	// Finally delete the rest. Do not fail on these.
	lease := &coordv1.Lease{}
	lease.Name = buildInitLeaseName(st.Spec.ModelCache.CacheHandle)
	lease.Namespace = ModelCacheInitNamespace
	if err := r.Client.Delete(ctx, lease); err != nil && !apierrors.IsNotFound(err) {
		log.Error(err, "Lease deletion failed, manual cleanup needed")
		errs = append(errs, err)
	}
	for _, job := range jobList.Items {
		for _, pullSecretName := range job.Spec.Template.Spec.ImagePullSecrets {
			secret := &corev1.Secret{}
			secret.Name = pullSecretName.Name
			secret.Namespace = job.Namespace
			if err := r.Client.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
				log.Error(err, "Image pull secret deletion failed, manual cleanup needed",
					"secret_name", pullSecretName)
				errs = append(errs, err)
			}
		}
	}

	return errs
}

func (r *Reconciler) cleanupIdleModelCaches(ctx context.Context) error { //nolint
	log := logf.FromContext(ctx)

	log.V(1).Info("Cleaning up idle model caches")

	stList := &nvcav1new.StorageRequestList{}
	if err := r.Client.List(ctx, stList, client.MatchingFields{
		objectNameFieldPath: nvcav1new.ModelCacheRequest.Name(),
	}); err != nil {
		return err
	}

	pvs := &corev1.PersistentVolumeList{}
	if err := r.Client.List(ctx, pvs, client.MatchingLabelsSelector{
		Selector: primaryPVSel,
	}); err != nil {
		return err
	}

	// Collect all volume handles from active storage requests to filter out PVs.
	activeVolumeHandles := sets.Set[string]{}
	for _, st := range stList.Items {
		if st.DeletionTimestamp != nil {
			continue
		}
		if st.Status.ModelCache != nil && st.Status.ModelCache.VolumeHandle != "" {
			activeVolumeHandles = activeVolumeHandles.Insert(st.Status.ModelCache.VolumeHandle)
		}
	}

	now := r.nowFunc()
	var updatedPVs []*corev1.PersistentVolume
	foundCacheHandles := sets.New[string]()
	storageClassesToDelete := sets.New[string]()
	for _, pv := range pvs.Items {
		if pv.Annotations == nil {
			continue
		}
		// Collect existing primary PV cache handles.
		if pv.Labels != nil {
			cacheHandle := pv.Labels[modelCacheHandleLabelKey]
			if _, ok := r.initStatuses.get(cacheHandle); cacheHandle != "" && ok {
				foundCacheHandles.Insert(cacheHandle)
			}
		}

		primaryPVLastReferencedStr, ok := pv.Annotations[primaryPVLastReferencedAnnotationKey]
		if !ok {
			// All primary PVs must have the last-referenced annotation.
			continue
		}
		primaryPVLastReferenced, err := time.Parse(primaryPVLastReferencedTimeFormat, primaryPVLastReferencedStr)
		if err != nil {
			log.Error(err, "Failed to parse primary PV last referenced time", "name", pv.Name)
			continue
		}
		switch pv.Status.Phase {
		case corev1.VolumeAvailable, corev1.VolumeReleased, corev1.VolumePending:
			// The volume should have been bound by some claim within the idle period.
			// If not, it should be deleted.
			if primaryPVLastReferenced.Add(r.k8sTimeConfig.ModelCacheIdlePeriod).After(now) {
				continue
			}
			if pv.Spec.CSI != nil && activeVolumeHandles.Has(pv.Spec.CSI.VolumeHandle) {
				continue
			}
		case corev1.VolumeFailed:
			// Failed volumes should be cleaned up regardless.
		default:
			// Bound volumes are in use.
			continue
		}

		storageClassesToDelete = storageClassesToDelete.Insert(pv.Spec.StorageClassName)

		// Now that all secondary references to the underlying volume are gone,
		// it can be deleted.
		upv := &pv
		if upv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimDelete {
			upv.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
			if err := r.Client.Update(ctx, upv); err != nil {
				return err
			}
		}
		updatedPVs = append(updatedPVs, upv)
	}

	// First pass GC: delete all cache handles not found in existing PV's.
	// This handles missed deletions.
	r.initStatuses.Lock()
	cacheHandles := r.initStatuses.keys()
	for _, cacheHandle := range cacheHandles {
		if !foundCacheHandles.Has(cacheHandle) {
			r.initStatuses.delete(cacheHandle)
		}
	}
	r.initStatuses.Unlock()

	// Storage classes are shared between PV's, and should be removed from the set to delete
	// if at least one PV is bound that references a storage class.
	for _, pv := range pvs.Items {
		if pv.Status.Phase == corev1.VolumeBound {
			storageClassesToDelete = storageClassesToDelete.Delete(pv.Spec.StorageClassName)
		}
	}

	for _, pv := range updatedPVs {
		// Only delete storage classes created by this controller.
		if storageClassesToDelete.Has(pv.Spec.StorageClassName) {
			deleteStorageClassIfEncrypted(ctx, r.Client, pv.Spec.StorageClassName)
		}

		if pv.DeletionTimestamp != nil {
			log.V(1).Info("PV has already been deleted", "pv", pv.Name)
		} else {
			log.Info("Deleting idle model cache PV", "pv", pv.Name)
			// PVC's should be cleaned up when the storage request is deleted,
			// so the primary volume be deleted.
			if err := r.Client.Delete(ctx, pv); err != nil && !apierrors.IsNotFound(err) {
				log.Error(err, "Failed to delete PV, manual cleanup needed")
			} else {
				r.metrics.RecordModelCacheReclaimed(string(HelmCacheBackendNVMesh))
			}
			// Second pass GC: delete now-deleted PV's.
			if pv.Labels != nil {
				r.initStatuses.delete(pv.Labels[modelCacheHandleLabelKey])
			}
		}
	}

	// Refresh the NVMesh inventory gauge (primary PVs are NVMesh-only).
	r.metrics.SetModelCacheBackendCount(string(HelmCacheBackendNVMesh), len(pvs.Items))

	// Samba caches key reuse on a per-handle backing PVC, not an NVMesh primary
	// PV, so they are reclaimed separately (and set the Samba gauge there).
	if err := r.reclaimIdleSambaModelCaches(ctx, stList); err != nil {
		log.Error(err, "Failed to reclaim idle Samba model caches")
	}

	// Shared-FS caches retain the writer PVC as the durable backing store; it
	// has no primary PV, so it too is reclaimed by its own pass.
	if err := r.reclaimIdleSharedFSModelCaches(ctx, stList); err != nil {
		log.Error(err, "Failed to reclaim idle shared-FS model caches")
	}

	return nil
}

// reclaimIdleSharedFSModelCaches deletes retained shared-FS writer PVCs (the
// durable per-handle backing claim on the shared class) for cacheHandles that
// no active StorageRequest references and that have been idle past the
// model-cache idle period. Samba backing PVCs also carry the populated label
// but are reclaimed with their per-handle server by reclaimIdleSambaModelCaches.
func (r *Reconciler) reclaimIdleSharedFSModelCaches(ctx context.Context, stList *nvcav1new.StorageRequestList) error {
	log := logf.FromContext(ctx)

	activeHandles := sets.New[string]()
	for _, st := range stList.Items {
		if st.DeletionTimestamp != nil || st.Spec.ModelCache == nil {
			continue
		}
		if h := st.Spec.ModelCache.CacheHandle; h != "" {
			activeHandles.Insert(h)
		}
	}

	pvcs := &corev1.PersistentVolumeClaimList{}
	if err := r.Client.List(ctx, pvcs,
		client.InNamespace(ModelCacheInitNamespace),
		client.MatchingLabels{cachePopulatedLabelKey: cachePopulatedLabelValue},
	); err != nil {
		return err
	}

	now := r.nowFunc()
	count := 0
	var errs []error
	for i := range pvcs.Items {
		pvc := &pvcs.Items[i]
		if pvc.Labels[sambaModelCacheComponentLabelKey] == sambaModelCacheComponentLabelValue {
			continue
		}
		handle := pvc.Labels[modelCacheHandleLabelKey]
		if handle == "" {
			continue
		}
		count++
		if activeHandles.Has(handle) {
			continue
		}
		// No last-referenced annotation yet means no consumer has attached since
		// populate; treat as active rather than reclaiming a cache mid-rollout.
		lastRefStr, ok := pvc.Annotations[primaryPVLastReferencedAnnotationKey]
		if !ok {
			continue
		}
		lastRef, err := time.Parse(primaryPVLastReferencedTimeFormat, lastRefStr)
		if err != nil {
			log.Error(err, "Failed to parse shared-FS writer PVC last-referenced time", "pvc", pvc.Name)
			continue
		}
		if lastRef.Add(r.k8sTimeConfig.ModelCacheIdlePeriod).After(now) {
			continue
		}
		log.Info("Reclaiming idle shared-FS model cache", "cacheHandle", handle, "pvc", pvc.Name)
		if err := r.Client.Delete(ctx, pvc); err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, err)
			continue
		}
		r.metrics.RecordModelCacheReclaimed(string(HelmCacheBackendSharedFS))
	}
	r.metrics.SetModelCacheBackendCount(string(HelmCacheBackendSharedFS), count)
	return errors.Join(errs...)
}

// reclaimIdleSambaModelCaches deletes the per-handle Samba server + backing PVC
// for cacheHandles that no active StorageRequest references and that have been
// idle past the model-cache idle period. Samba caches key reuse on the backing
// PVC (samba-<handle>) rather than an NVMesh primary PV, so they are GC'd here
// rather than by the primary-PV pass above.
func (r *Reconciler) reclaimIdleSambaModelCaches(ctx context.Context, stList *nvcav1new.StorageRequestList) error {
	log := logf.FromContext(ctx)

	activeHandles := sets.New[string]()
	for _, st := range stList.Items {
		if st.DeletionTimestamp != nil || st.Spec.ModelCache == nil {
			continue
		}
		if h := st.Spec.ModelCache.CacheHandle; h != "" {
			activeHandles.Insert(h)
		}
	}

	pvcs := &corev1.PersistentVolumeClaimList{}
	if err := r.Client.List(ctx, pvcs,
		client.InNamespace(ModelCacheInitNamespace),
		client.MatchingLabels{sambaModelCacheComponentLabelKey: sambaModelCacheComponentLabelValue},
	); err != nil {
		return err
	}
	// Refresh the inventory gauge: how many Samba backing PVCs (= per-handle
	// Samba servers) currently exist.
	r.metrics.SetModelCacheBackendCount(string(HelmCacheBackendSamba), len(pvcs.Items))

	now := r.nowFunc()
	var errs []error
	for i := range pvcs.Items {
		pvc := &pvcs.Items[i]
		handle := pvc.Annotations[sambaModelCacheHandleAnnotationKey]
		if handle == "" || activeHandles.Has(handle) {
			continue
		}
		// A backing PVC with no last-referenced annotation yet (writer still
		// populating) is treated as active and not reclaimed.
		lastRefStr, ok := pvc.Annotations[primaryPVLastReferencedAnnotationKey]
		if !ok {
			continue
		}
		lastRef, err := time.Parse(primaryPVLastReferencedTimeFormat, lastRefStr)
		if err != nil {
			log.Error(err, "Failed to parse Samba backing PVC last-referenced time", "pvc", pvc.Name)
			continue
		}
		if lastRef.Add(r.k8sTimeConfig.ModelCacheIdlePeriod).After(now) {
			continue
		}
		log.Info("Reclaiming idle Samba model cache", "cacheHandle", handle, "pvc", pvc.Name)
		if err := DeleteSambaModelCacheInfra(ctx, r.Client, handle); err != nil {
			errs = append(errs, err)
			continue
		}
		r.metrics.RecordModelCacheReclaimed(string(HelmCacheBackendSamba))
	}
	return errors.Join(errs...)
}

func deleteStorageClassIfEncrypted(ctx context.Context, c client.Client, scName string) {
	log := logf.FromContext(ctx).WithValues("storageclass", scName)

	sc := &storagev1.StorageClass{}
	sc.Name = scName
	if err := c.Get(ctx, client.ObjectKeyFromObject(sc), sc); err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "Failed to get model cache storage class, manual cleanup needed")
			return
		}
	}
	if !isStorageClassEncrypted(sc) {
		return
	}
	if sc.DeletionTimestamp != nil {
		return
	}

	log.Info("Deleting StorageClass no longer in use by model caches")

	if err := c.Delete(ctx, sc); err != nil && !apierrors.IsNotFound(err) {
		log.Error(err, "Failed to delete storage class, manual cleanup needed")
	}
}
