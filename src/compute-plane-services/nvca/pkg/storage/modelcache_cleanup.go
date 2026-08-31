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
	"slices"
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
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
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
		// labels.Selector.Add returns a new selector; it does not mutate the
		// receiver. Reassign so the requirement is actually applied.
		primaryPVSel = primaryPVSel.Add(*req)
	}
}

func (r *Reconciler) deleteModelCacheCleanupObject(
	ctx context.Context,
	obj client.Object,
	annotated bool,
	opts ...client.DeleteOption,
) error {
	if annotated {
		if obj == nil || obj.GetUID() == "" || obj.GetResourceVersion() == "" {
			return fmt.Errorf("refusing model cache cleanup without exact UID and resourceVersion for %T", obj)
		}
		uid := obj.GetUID()
		resourceVersion := obj.GetResourceVersion()
		opts = append(opts, client.Preconditions(metav1.Preconditions{
			UID:             &uid,
			ResourceVersion: &resourceVersion,
		}))
	}
	return r.Client.Delete(ctx, obj, opts...)
}

type modelCacheInitCleanupLeaseGuard struct {
	lease      *coordv1.Lease
	bindingUID apitypes.UID
	holder     string
}

func (r *Reconciler) lockModelCacheInitCleanupLease(
	ctx context.Context,
	lease *coordv1.Lease,
	bindingUID apitypes.UID,
	holder string,
) (*modelCacheInitCleanupLeaseGuard, error) {
	if lease == nil || lease.UID == "" || lease.ResourceVersion == "" {
		return nil, fmt.Errorf("refusing model cache init cleanup without an exact ownership Lease identity")
	}
	if err := ValidateModelCacheBindingUIDLabel(lease, bindingUID); err != nil {
		return nil, err
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != holder {
		return nil, fmt.Errorf("%w: model cache cleanup Lease holder changed from %q",
			errModelCacheBindingOwnership, holder)
	}
	old := lease.DeepCopy()
	now := metav1.NowMicro()
	lease.Spec.RenewTime = &now
	if lease.Spec.AcquireTime == nil {
		lease.Spec.AcquireTime = &now
	}
	if err := r.Client.Patch(ctx, lease,
		client.MergeFromWithOptions(old, client.MergeFromWithOptimisticLock{})); err != nil {
		return nil, fmt.Errorf("lock model cache init cleanup Lease %s/%s: %w",
			lease.Namespace, lease.Name, err)
	}
	return &modelCacheInitCleanupLeaseGuard{
		lease: lease.DeepCopy(), bindingUID: bindingUID, holder: holder,
	}, nil
}

func (r *Reconciler) validateModelCacheInitCleanupLeaseGuard(
	ctx context.Context,
	guard *modelCacheInitCleanupLeaseGuard,
) error {
	if guard == nil || guard.lease == nil {
		return fmt.Errorf("refusing annotated model cache init cleanup without a Lease guard")
	}
	current := &coordv1.Lease{}
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(guard.lease), current); err != nil {
		return fmt.Errorf("revalidate model cache init cleanup Lease %s/%s: %w",
			guard.lease.Namespace, guard.lease.Name, err)
	}
	if current.UID != guard.lease.UID || current.ResourceVersion != guard.lease.ResourceVersion {
		return fmt.Errorf("%w: model cache init cleanup Lease changed after authorization",
			errModelCacheBindingOwnership)
	}
	if err := ValidateModelCacheBindingUIDLabel(current, guard.bindingUID); err != nil {
		return err
	}
	if current.Spec.HolderIdentity == nil || *current.Spec.HolderIdentity != guard.holder {
		return fmt.Errorf("%w: model cache init cleanup Lease holder changed from %q",
			errModelCacheBindingOwnership, guard.holder)
	}
	return nil
}

func (r *Reconciler) deleteModelCacheInitCleanupObject(
	ctx context.Context,
	obj client.Object,
	annotated bool,
	guard *modelCacheInitCleanupLeaseGuard,
	opts ...client.DeleteOption,
) error {
	if annotated {
		if err := r.validateModelCacheInitCleanupLeaseGuard(ctx, guard); err != nil {
			return err
		}
	}
	return r.deleteModelCacheCleanupObject(ctx, obj, annotated, opts...)
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

	bindingUID, annotated, _, err := r.validateModelCacheBindingForCleanup(ctx, st)
	if err != nil {
		return modelCacheCleanupErrorResult(err)
	}
	cwResourceLabels := getClusterWideResourceLabels(st)

	pvList := &corev1.PersistentVolumeList{}
	if err := r.Client.List(ctx, pvList, client.MatchingLabels(cwResourceLabels)); err != nil {
		log.Error(err, "Failed to list PVs for storage request")
		return modelCacheCleanupErrorResult(fmt.Errorf("list reader PVs for model cache cleanup: %w", err))
	}
	pvcList := &corev1.PersistentVolumeClaimList{}
	if err := r.Client.List(ctx, pvcList,
		client.MatchingLabels(cwResourceLabels),
		client.InNamespace(st.Namespace),
	); err != nil {
		log.Error(err, "Failed to list PVCs for storage request")
		return modelCacheCleanupErrorResult(fmt.Errorf("list reader PVCs for model cache cleanup: %w", err))
	}
	if annotated {
		if err := validateAnnotatedModelCacheReaderCleanup(st, bindingUID, &pvList.Items, &pvcList.Items); err != nil {
			return reconcile.Result{}, err
		}
		for i := range pvList.Items {
			if err := ValidateModelCacheReaderOwnership(
				&pvList.Items[i], bindingUID, apitypes.UID(st.Annotations[ICMSRequestUIDAnnotationKey])); err != nil {
				return reconcile.Result{}, fmt.Errorf(
					"refusing per-request model cache cleanup for PV %q: %w", pvList.Items[i].Name, err)
			}
		}
		for i := range pvcList.Items {
			if err := ValidateModelCacheReaderOwnership(
				&pvcList.Items[i], bindingUID, apitypes.UID(st.Annotations[ICMSRequestUIDAnnotationKey])); err != nil {
				return reconcile.Result{}, fmt.Errorf(
					"refusing per-request model cache cleanup for PVC %s/%s: %w",
					pvcList.Items[i].Namespace, pvcList.Items[i].Name, err)
			}
		}
	}

	initCleanupErrs := r.cleanupInitModelCache(ctx, st, false)
	if len(initCleanupErrs) != 0 {
		meta.SetStatusCondition(&st.Status.Conditions, metav1.Condition{
			Type:    ConditionTypeCleanupSuccessful,
			Status:  metav1.ConditionFalse,
			Reason:  ConditionReasonSomeObjectsPendingDeletion,
			Message: fmt.Sprintf("errors encountered while cleaning up init objects: %+q", initCleanupErrs),
		})
		return modelCacheSuccessfulInitCleanupResult(initCleanupErrs)
	}

	// PVC's can be deleted before pods are, and will be finalized once the pod is deleted.
	for _, pvc := range pvcList.Items {
		if pvc.DeletionTimestamp != nil {
			log.V(1).Info("PVC has already been deleted", "pvc", pvc.Name)
		} else if err := r.deleteModelCacheCleanupObject(ctx, &pvc, annotated); err != nil &&
			!apierrors.IsNotFound(err) {
			log.Error(err, "Failed to delete PVC, manual cleanup needed", "pvc", pvc.Name)
			return modelCacheCleanupErrorResult(fmt.Errorf(
				"delete reader PVC %s/%s: %w", pvc.Namespace, pvc.Name, err))
		}
	}

	// Secondary PV's should NOT have reclaim policy set to "Delete" on termination.
	// Only the primary PV should on termination, to preserve the NVMesh volume.
	for _, pv := range pvList.Items {
		if pv.DeletionTimestamp != nil {
			log.V(1).Info("PV has already been deleted", "pv", pv.Name)
		} else if err := r.deleteModelCacheCleanupObject(ctx, &pv, annotated); err != nil &&
			!apierrors.IsNotFound(err) {
			log.Error(err, "Failed to delete PV, manual cleanup needed", "pv", pv.Name)
			return modelCacheCleanupErrorResult(fmt.Errorf("delete reader PV %q: %w", pv.Name, err))
		}
	}

	meta.SetStatusCondition(&st.Status.Conditions, metav1.Condition{
		Type:    ConditionTypeCleanupSuccessful,
		Status:  metav1.ConditionTrue,
		Reason:  ConditionReasonAllObjectsDeleted,
		Message: "All init and secondary model cache objects were cleaned up",
	})
	return reconcile.Result{}, nil
}

func modelCacheCleanupErrorResult(err error) (reconcile.Result, error) {
	if err == nil {
		return reconcile.Result{}, nil
	}
	if k8sutil.IsTransientK8sError(err) {
		return reconcile.Result{Requeue: true}, nil
	}
	return reconcile.Result{}, err
}

func validateAnnotatedModelCacheReaderCleanup(
	st *nvcav1new.StorageRequest,
	bindingUID apitypes.UID,
	pvs *[]corev1.PersistentVolume,
	pvcs *[]corev1.PersistentVolumeClaim,
) error {
	if st == nil || st.Spec.ModelCache == nil || pvs == nil || pvcs == nil {
		return fmt.Errorf("refusing annotated model cache cleanup with incomplete reader identity")
	}
	selection, err := ParsePersistedModelCacheStorageSelection(
		st.Annotations[ModelCacheStorageSelectionAnnotationKey])
	if err != nil {
		return fmt.Errorf("parse persisted model cache selection for reader cleanup: %w", err)
	}
	requestUID := apitypes.UID(st.Annotations[ICMSRequestUIDAnnotationKey])
	expectedPVName := "secondary-pv-" + st.Spec.ICMSRequestName
	expectedPVCName := "ro-pvc-" + st.Spec.ModelCache.CacheHandle
	if len(*pvs) > 1 || len(*pvcs) > 1 {
		return fmt.Errorf("refusing annotated model cache cleanup with %d reader PVs and %d reader PVCs",
			len(*pvs), len(*pvcs))
	}
	for i := range *pvs {
		pv := &(*pvs)[i]
		if pv.Name != expectedPVName || pv.Namespace != "" {
			return fmt.Errorf("refusing model cache cleanup for unexpected reader PV %q/%q",
				pv.Namespace, pv.Name)
		}
		if err := ValidateModelCacheReaderOwnership(pv, bindingUID, requestUID); err != nil {
			return err
		}
		if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != selection.Provisioner {
			return fmt.Errorf("refusing reader PV %q with provisioner other than %q",
				pv.Name, selection.Provisioner)
		}
		if pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
			return fmt.Errorf("refusing reader PV %q with reclaim policy %q; want Retain",
				pv.Name, pv.Spec.PersistentVolumeReclaimPolicy)
		}
		if !slices.Equal(pv.Spec.AccessModes, accessModesRO) {
			return fmt.Errorf("refusing reader PV %q with access modes %v", pv.Name, pv.Spec.AccessModes)
		}
		claimRef := pv.Spec.ClaimRef
		if claimRef == nil || claimRef.APIVersion != "v1" || claimRef.Kind != "PersistentVolumeClaim" ||
			claimRef.Namespace != st.Namespace || claimRef.Name != expectedPVCName {
			return fmt.Errorf("refusing reader PV %q with unexpected claimRef", pv.Name)
		}
	}
	for i := range *pvcs {
		pvc := &(*pvcs)[i]
		if pvc.Name != expectedPVCName || pvc.Namespace != st.Namespace {
			return fmt.Errorf("refusing model cache cleanup for unexpected reader PVC %s/%s",
				pvc.Namespace, pvc.Name)
		}
		if err := ValidateModelCacheReaderOwnership(pvc, bindingUID, requestUID); err != nil {
			return err
		}
		if pvc.Spec.VolumeName != expectedPVName || !slices.Equal(pvc.Spec.AccessModes, accessModesRO) {
			return fmt.Errorf("refusing reader PVC %s/%s with unexpected volume or access modes",
				pvc.Namespace, pvc.Name)
		}
	}
	if len(*pvs) == 1 && len(*pvcs) == 1 {
		pv := &(*pvs)[0]
		pvc := &(*pvcs)[0]
		if pvc.Status.Phase == corev1.ClaimBound &&
			(pvc.UID == "" || pv.Spec.ClaimRef.UID != pvc.UID) {
			return fmt.Errorf("refusing bound reader PVC %s/%s UID %q with PV claimRef UID %q",
				pvc.Namespace, pvc.Name, pvc.UID, pv.Spec.ClaimRef.UID)
		}
		if pv.Spec.ClaimRef.UID != "" && pv.Spec.ClaimRef.UID != pvc.UID {
			return fmt.Errorf("refusing reader PV %q claimRef UID %q for PVC UID %q",
				pv.Name, pv.Spec.ClaimRef.UID, pvc.UID)
		}
	}
	return nil
}

// cleanupInitModelCache deletes the init objects (job, pods, lease, pull
// secrets, writer PV/PVC) for the request's cache handle. retainWriterPVC
// keeps the writer RW PVC and its volume: the shared-FS backend retains the
// bound writer claim as the durable populated marker, but its init job and
// lease still must not accumulate per cache handle.
func (r *Reconciler) cleanupInitModelCache(ctx context.Context, st *nvcav1new.StorageRequest, retainWriterPVC bool) (errs []error) {
	log := logf.FromContext(ctx)

	log.V(1).Info("Cleaning up model cache init objects", "retainWriterPVC", retainWriterPVC)
	if st == nil || st.Spec.ModelCache == nil || st.Spec.ModelCache.CacheHandle == "" {
		return []error{fmt.Errorf("cannot clean model cache init objects without a cache handle")}
	}
	bindingUID, annotated, requestReferencePresent, err := r.validateModelCacheBindingForCleanup(ctx, st)
	if err != nil {
		return []error{err}
	}
	var cleanupLeaseGuard *modelCacheInitCleanupLeaseGuard
	if annotated {
		if !requestReferencePresent {
			// Per-request reader cleanup remains authorized by the persisted
			// tombstone, but shared writer cleanup requires the live binding
			// reference and is skipped after that reference is released.
			log.V(1).Info("Skipping shared model cache init cleanup after request reference release")
			return nil
		}
		holderIdentity := modelCacheLeaseHolderIdentity(st)
		authorized, ownershipLease, err := r.validateModelCacheInitCleanupOwnership(
			ctx, st.Spec.ModelCache.CacheHandle, bindingUID, holderIdentity)
		if err != nil {
			return []error{err}
		}
		if !authorized {
			log.V(1).Info("Skipping shared model cache init cleanup owned by another request")
			return nil
		}
		cleanupLeaseGuard, err = r.lockModelCacheInitCleanupLease(
			ctx, ownershipLease, bindingUID, holderIdentity)
		if err != nil {
			return []error{err}
		}
	}

	matchLabels := map[string]string{
		modelCacheHandleLabelKey: st.Spec.ModelCache.CacheHandle,
	}
	if annotated {
		matchLabels[ModelCacheBindingUIDLabelKey] = string(bindingUID)
	}
	listOpts := []client.ListOption{
		client.MatchingLabels(matchLabels),
		client.InNamespace(ModelCacheInitNamespace),
	}

	// Discover every target before the first delete. An inconclusive API read is
	// never authority to continue destructive cleanup.
	jobList := &batchv1.JobList{}
	if err := r.Client.List(ctx, jobList, listOpts...); err != nil {
		return append(errs, fmt.Errorf("list model cache init Jobs: %w", err))
	}
	podList := &corev1.PodList{}
	if err := r.Client.List(ctx, podList, listOpts...); err != nil {
		return append(errs, fmt.Errorf("list model cache init Pods: %w", err))
	}
	pvcList := &corev1.PersistentVolumeClaimList{}
	if !retainWriterPVC {
		if err := r.Client.List(ctx, pvcList, listOpts...); err != nil {
			return append(errs, fmt.Errorf("list model cache init PVCs: %w", err))
		}
	}
	secretList := &corev1.SecretList{}
	if annotated {
		if err := r.Client.List(ctx, secretList, listOpts...); err != nil {
			return append(errs, fmt.Errorf("list model cache init pull Secrets: %w", err))
		}
	} else {
		seen := map[string]struct{}{}
		for _, job := range jobList.Items {
			for _, ref := range job.Spec.Template.Spec.ImagePullSecrets {
				if _, ok := seen[ref.Name]; ok {
					continue
				}
				seen[ref.Name] = struct{}{}
				secret := corev1.Secret{}
				err := r.Client.Get(ctx, client.ObjectKey{Name: ref.Name, Namespace: job.Namespace}, &secret)
				if apierrors.IsNotFound(err) {
					continue
				}
				if err != nil {
					return append(errs, fmt.Errorf("get model cache init pull Secret %s/%s: %w",
						job.Namespace, ref.Name, err))
				}
				secretList.Items = append(secretList.Items, secret)
			}
		}
	}

	if annotated {
		if len(jobList.Items) > 1 || len(pvcList.Items) > 1 {
			return append(errs, fmt.Errorf("refusing annotated cleanup with %d writer Jobs and %d writer PVCs",
				len(jobList.Items), len(pvcList.Items)))
		}
		for _, obj := range append(
			[]client.Object{}, modelCacheCleanupObjects(jobList, podList, pvcList, secretList)...,
		) {
			if err := ValidateModelCacheBindingUIDLabel(obj, bindingUID); err != nil {
				return append(errs, err)
			}
		}
	}

	deleteTarget := func(obj client.Object, opts ...client.DeleteOption) error {
		err := r.deleteModelCacheInitCleanupObject(ctx, obj, annotated, cleanupLeaseGuard, opts...)
		if err == nil || apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if len(jobList.Items) == 1 && jobList.Items[0].DeletionTimestamp == nil {
		job := &jobList.Items[0]
		if err := deleteTarget(job, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil {
			return append(errs, fmt.Errorf("delete model cache init Job %s/%s: %w",
				job.Namespace, job.Name, err))
		}
	}
	gracePeriod := int64(0)
	for i := range podList.Items {
		pod := &podList.Items[i]
		if err := deleteTarget(pod, client.GracePeriodSeconds(gracePeriod)); err != nil {
			return append(errs, fmt.Errorf("delete model cache init Pod %s/%s: %w",
				pod.Namespace, pod.Name, err))
		}
	}

	writerVolumeSettled := true
	if !retainWriterPVC && len(pvcList.Items) == 1 {
		pvc := &pvcList.Items[0]
		if pvc.DeletionTimestamp == nil {
			if pvc.Spec.VolumeName != "" {
				detached, err := r.isVolumeDetached(ctx, pvc.Spec.VolumeName)
				if err != nil {
					return append(errs, fmt.Errorf("check model cache writer PV %q detachment: %w",
						pvc.Spec.VolumeName, err))
				}
				if !detached {
					return append(errs, errVolumeStillAttached)
				}
			}
			if err := deleteTarget(pvc); err != nil {
				return append(errs, fmt.Errorf("delete model cache init PVC %s/%s: %w",
					pvc.Namespace, pvc.Name, err))
			}
		}
	}
	if !retainWriterPVC && len(pvcList.Items) > 1 {
		writerVolumeSettled = false
	}

	if writerVolumeSettled && !retainWriterPVC && !annotated {
		sambaWriterPV := &corev1.PersistentVolume{}
		sambaWriterPV.Name = sambaModelCacheWriterPVName(st.Spec.ModelCache.CacheHandle)
		if err := r.Client.Delete(ctx, sambaWriterPV); err != nil && !apierrors.IsNotFound(err) {
			return append(errs, fmt.Errorf("delete Samba model cache writer PV %q: %w",
				sambaWriterPV.Name, err))
		}
	}
	for i := range secretList.Items {
		secret := &secretList.Items[i]
		if err := deleteTarget(secret); err != nil {
			return append(errs, fmt.Errorf("delete model cache init pull Secret %s/%s: %w",
				secret.Namespace, secret.Name, err))
		}
	}

	lease := &coordv1.Lease{}
	if annotated {
		lease = cleanupLeaseGuard.lease.DeepCopy()
	} else {
		leaseKey := client.ObjectKey{
			Name: buildInitLeaseName(st.Spec.ModelCache.CacheHandle), Namespace: ModelCacheInitNamespace,
		}
		if err := r.Client.Get(ctx, leaseKey, lease); err != nil {
			if apierrors.IsNotFound(err) {
				return errs
			}
			return append(errs, fmt.Errorf("get model cache init Lease %s/%s: %w",
				leaseKey.Namespace, leaseKey.Name, err))
		}
	}
	if err := deleteTarget(lease); err != nil {
		return append(errs, fmt.Errorf("delete model cache init Lease %s/%s: %w",
			lease.Namespace, lease.Name, err))
	}

	return errs
}

func modelCacheCleanupObjects(
	jobs *batchv1.JobList,
	pods *corev1.PodList,
	pvcs *corev1.PersistentVolumeClaimList,
	secrets *corev1.SecretList,
) []client.Object {
	objects := make([]client.Object, 0,
		len(jobs.Items)+len(pods.Items)+len(pvcs.Items)+len(secrets.Items))
	for i := range jobs.Items {
		objects = append(objects, &jobs.Items[i])
	}
	for i := range pods.Items {
		objects = append(objects, &pods.Items[i])
	}
	for i := range pvcs.Items {
		objects = append(objects, &pvcs.Items[i])
	}
	for i := range secrets.Items {
		objects = append(objects, &secrets.Items[i])
	}
	return objects
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
		if pv.Labels != nil && pv.Labels[ModelCacheBindingUIDLabelKey] != "" {
			cacheHandle := pv.Labels[modelCacheHandleLabelKey]
			if _, ok := r.initStatuses.get(cacheHandle); cacheHandle != "" && ok {
				foundCacheHandles.Insert(cacheHandle)
			}
			continue
		}
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

	// Storage classes are shared between PVs. A binding-owned PV is outside this
	// legacy GC's authority, so it protects its class regardless of PV phase.
	for _, pv := range pvs.Items {
		if pv.Status.Phase == corev1.VolumeBound ||
			pv.Labels[ModelCacheBindingUIDLabelKey] != "" {
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

	// Bindings own the resources the passes above reclaim, and hold a
	// finalizer that keeps them alive. Retire the idle ones last, so a binding
	// is only released once its backing store has been reclaimed.
	if err := r.retireIdleModelCacheBindings(ctx, stList); err != nil {
		log.Error(err, "Failed to retire idle model cache bindings")
	}

	return nil
}

// retireIdleModelCacheBindings drives the binding lifecycle to completion.
//
// A binding is created Active with a protection finalizer, and releasing the
// last request only empties its reference list. Without this pass nothing ever
// sets Retiring and nothing ever removes the finalizer, so every binding, and
// the resources its finalizer protects, accumulates for the life of the
// cluster and cannot be deleted without editing finalizers by hand.
//
// Retirement is two phased and idle gated, so a warm cache survives a function
// scaling to zero:
//
//  1. Active, unreferenced, and idle past ModelCacheIdlePeriod -> Retiring,
//     which stops new references being accepted.
//  2. Retiring -> delete the resources the binding declares it owns, then drop
//     the finalizer so the object can go.
//
// A binding that regains a reference before step 2 is left alone.
func (r *Reconciler) retireIdleModelCacheBindings(
	ctx context.Context, stList *nvcav1new.StorageRequestList,
) error {
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

	bindings := &nvcav2beta1.ModelCacheBindingList{}
	if err := r.Client.List(ctx, bindings, client.InNamespace(ModelCacheInitNamespace)); err != nil {
		return err
	}

	now := r.nowFunc()
	var errs []error
	for i := range bindings.Items {
		binding := &bindings.Items[i]
		// Still referenced, or a live request still names its handle: in use.
		if len(binding.Status.RequestReferences) != 0 {
			continue
		}
		if h := binding.Labels[modelCacheHandleLabelKey]; h != "" && activeHandles.Has(h) {
			continue
		}

		switch binding.Status.Phase {
		case nvcav2beta1.ModelCacheBindingPhaseActive:
			if !r.modelCacheBindingIdle(binding, now) {
				continue
			}
			retiring := binding.DeepCopy()
			retiring.Status.Phase = nvcav2beta1.ModelCacheBindingPhaseRetiring
			transition := metav1.NewTime(now)
			retiring.Status.LastPhaseTransitionTime = &transition
			if err := r.Client.Status().Update(ctx, retiring); err != nil {
				if !apierrors.IsConflict(err) && !apierrors.IsNotFound(err) {
					errs = append(errs, err)
				}
				continue
			}
			log.Info("Model cache binding retiring", "binding", binding.Name)
		case nvcav2beta1.ModelCacheBindingPhaseRetiring:
			if err := r.releaseModelCacheBindingResources(ctx, binding); err != nil {
				errs = append(errs, err)
				continue
			}
			if err := r.dropModelCacheBindingFinalizer(ctx, binding); err != nil {
				errs = append(errs, err)
				continue
			}
			if err := r.Client.Delete(ctx, binding); err != nil && !apierrors.IsNotFound(err) {
				errs = append(errs, err)
				continue
			}
			log.Info("Model cache binding retired", "binding", binding.Name)
		}
	}
	return errors.Join(errs...)
}

// modelCacheBindingIdle reports whether an unreferenced binding has been idle
// long enough to retire. A binding that never recorded a transition is treated
// as idle from its creation.
func (r *Reconciler) modelCacheBindingIdle(
	binding *nvcav2beta1.ModelCacheBinding, now time.Time,
) bool {
	since := binding.CreationTimestamp.Time
	if binding.Status.LastPhaseTransitionTime != nil {
		since = binding.Status.LastPhaseTransitionTime.Time
	}
	if since.IsZero() {
		return false
	}
	return !since.Add(r.k8sTimeConfig.ModelCacheIdlePeriod).After(now)
}

// releaseModelCacheBindingResources deletes exactly what the binding recorded
// itself as owning. Nothing is inferred: an object the binding does not name is
// never touched, so a retirement cannot reach another cache's resources.
func (r *Reconciler) releaseModelCacheBindingResources(
	ctx context.Context, binding *nvcav2beta1.ModelCacheBinding,
) error {
	resources := binding.Spec.Resources
	ns := resources.WriterNamespace
	var errs []error

	del := func(obj client.Object) {
		if err := r.Client.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("release binding %s resource %T %s: %w",
				binding.Name, obj, obj.GetName(), err))
		}
	}

	for _, name := range resources.JobNames {
		policy := metav1.DeletePropagationBackground
		job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
		if err := r.Client.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &policy}); err != nil &&
			!apierrors.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("release binding %s Job %s: %w", binding.Name, name, err))
		}
	}
	for _, name := range resources.PersistentVolumeClaimNames {
		del(&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}})
	}
	for _, name := range resources.SecretNames {
		del(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}})
	}
	if resources.LeaseName != "" {
		del(&coordv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: resources.LeaseName, Namespace: ns}})
	}
	// Cluster scoped, and deleted after the claims above so a reader PV is
	// released rather than left bound to a claim that is going away.
	for _, name := range resources.PersistentVolumeNames {
		del(&corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: name}})
	}
	for _, name := range resources.StorageClassNames {
		del(&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: name}})
	}
	return errors.Join(errs...)
}

// dropModelCacheBindingFinalizer removes the protection finalizer once the
// resources it guards are gone.
func (r *Reconciler) dropModelCacheBindingFinalizer(
	ctx context.Context, binding *nvcav2beta1.ModelCacheBinding,
) error {
	if !slices.Contains(binding.Finalizers, nvcav2beta1.ModelCacheBindingFinalizer) {
		return nil
	}
	updated := binding.DeepCopy()
	updated.Finalizers = slices.DeleteFunc(updated.Finalizers, func(f string) bool {
		return f == nvcav2beta1.ModelCacheBindingFinalizer
	})
	if err := r.Client.Update(ctx, updated); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("drop binding %s finalizer: %w", binding.Name, err)
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
