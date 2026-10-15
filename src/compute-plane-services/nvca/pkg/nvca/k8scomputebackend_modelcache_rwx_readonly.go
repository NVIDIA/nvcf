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

package nvca

import (
	"context"
	"fmt"
	"slices"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"

	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	modelcachetypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/modelcachetypes"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// regularModelCacheSelection parses the storage selection persisted on a
// request when it was created. It reports whether a selection is present at
// all; a request created before selections existed has none and takes the
// legacy path unchanged.
func regularModelCacheSelection(req *nvcav2beta1.ICMSRequest) (*nvcastorage.PersistedModelCacheStorageSelection, bool, error) {
	raw := req.Annotations[nvcastorage.ModelCacheStorageSelectionAnnotationKey]
	if raw == "" {
		return nil, false, nil
	}
	selection, err := nvcastorage.ParsePersistedModelCacheStorageSelection(raw)
	if err != nil {
		return nil, true, fmt.Errorf("parse persisted model cache storage selection: %w", err)
	}
	if selection.Workflow != nvcastorage.ModelCacheWorkflowRegular {
		return nil, true, fmt.Errorf("persisted model cache selection workflow %q is not the regular workflow",
			selection.Workflow)
	}
	return selection, true, nil
}

// setupRWXReadOnlyModelCachingForRequest is the regular-workflow cache flow
// for a provider qualified for ReadWriteMany: one claim per cache handle on
// the selected StorageClass, populated once by the writer Job, then mounted
// read-only by every reader in the namespace. Nothing is copied or rebound.
//
// The claim is shared across requests, so this function never deletes a claim
// another request may be mounting. An unpopulated claim whose writer failed is
// removed so the next request can retry; a populated claim is reclaimed only
// by the reference sweep once no request names it.
func (c K8sComputeBackend) setupRWXReadOnlyModelCachingForRequest(
	ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
	rwPVC *corev1.PersistentVolumeClaim,
	initJob *batchv1.Job,
	selection *nvcastorage.PersistedModelCacheStorageSelection,
) (ModelCachingState, string) {
	log := core.GetLogger(ctx)
	metrics := nvcametrics.FromContext(ctx)
	record := func(result, reason string) {
		if metrics != nil {
			metrics.RecordModelCacheResult(result, reason, string(types.HelmCacheBackendSharedFS))
		}
	}
	fail := func(reason string, err error) (ModelCachingState, string) {
		log.WithError(err).Errorf("regular model cache on a shared claim failed for request %v/%v, model caching will be disabled",
			req.Namespace, req.Name)
		record(modelcachetypes.ResultFailure, reason)
		return ModelCachingFailed, ""
	}
	if err := validateSharedClaimWriterJob(initJob, rwPVC.Name); err != nil {
		return fail(modelcachetypes.ReasonCacheSpecInvalid, err)
	}
	pvcs := c.clients.K8s.CoreV1().PersistentVolumeClaims(c.bk8s.podInstanceNamespace)

	current, err := pvcs.Get(ctx, rwPVC.Name, metav1.GetOptions{})
	switch {
	case errors.IsNotFound(err):
		created, err := pvcs.Create(ctx, sharedClaimFromArtifact(rwPVC, selection), metav1.CreateOptions{})
		if err != nil && !errors.IsAlreadyExists(err) {
			return fail(modelcachetypes.ReasonPVCSetupFailed, fmt.Errorf("create shared cache claim %s: %w", rwPVC.Name, err))
		}
		if err == nil {
			current = created
		} else if current, err = pvcs.Get(ctx, rwPVC.Name, metav1.GetOptions{}); err != nil {
			return fail(modelcachetypes.ReasonPVCSetupFailed, fmt.Errorf("get shared cache claim %s: %w", rwPVC.Name, err))
		}
	case err != nil:
		return fail(modelcachetypes.ReasonPVCSetupFailed, fmt.Errorf("get shared cache claim %s: %w", rwPVC.Name, err))
	}

	if !slices.Contains(current.Spec.AccessModes, corev1.ReadWriteMany) {
		// A claim of this name that is not a shared claim belongs to another
		// flow. Refuse rather than mount or delete what is not ours.
		return fail(modelcachetypes.ReasonCacheSpecInvalid,
			fmt.Errorf("claim %s/%s exists with access modes %v, not ReadWriteMany; refusing to share it",
				current.Namespace, current.Name, current.Spec.AccessModes))
	}
	if selection.StorageClassName != "" &&
		(current.Spec.StorageClassName == nil || *current.Spec.StorageClassName != selection.StorageClassName) {
		// Same handle, different backend: the selection recorded one class and
		// the claim lives on another. Mounting it would silently serve the old
		// backend; deleting it would destroy a cache other requests may use.
		got := "<none>"
		if current.Spec.StorageClassName != nil {
			got = *current.Spec.StorageClassName
		}
		return fail(modelcachetypes.ReasonCacheSpecInvalid,
			fmt.Errorf("claim %s/%s is on StorageClass %q, selection recorded %q; refusing to share it",
				current.Namespace, current.Name, got, selection.StorageClassName))
	}
	if current.Labels[nvcastorage.ModelCachePopulatedLabelKey] == nvcastorage.ModelCachePopulatedLabelValue {
		record(modelcachetypes.ResultSuccess, "")
		return ModelCachingCompleted, current.Name
	}

	jobs := c.clients.K8s.BatchV1().Jobs(c.bk8s.podInstanceNamespace)
	switch c.CheckInitCacheJobState(ctx, rwPVC.Name, initJob) {
	case InitCacheJobNotFound:
		job := initJob.DeepCopy()
		if job.Spec.Template.Annotations == nil {
			job.Spec.Template.Annotations = map[string]string{}
		}
		job.Spec.Template.Annotations[nvcastorage.ModelCacheWriterPVCUIDAnnotationKey] = string(current.UID)
		if _, err := jobs.Create(ctx, job, metav1.CreateOptions{}); err != nil && !errors.IsAlreadyExists(err) {
			return fail(modelcachetypes.ReasonPVCSetupFailed, fmt.Errorf("create writer job %s: %w", job.Name, err))
		}
		log.Infof("shared cache claim %s created on %s, writer job %s started", current.Name,
			selection.StorageClassName, initJob.Name)
		// The claim exists from here on, so every in-progress result names it:
		// the request records it as its cache reference and the reference
		// sweep can see the claim is in use while the writer runs.
		return ModelCachingInProgress, current.Name
	case InitCacheJobInProgress:
		return ModelCachingInProgress, current.Name
	case InitCacheJobFailed:
		background := metav1.DeletePropagationBackground
		if err := jobs.Delete(ctx, initJob.Name, metav1.DeleteOptions{PropagationPolicy: &background}); err != nil && !errors.IsNotFound(err) {
			log.WithError(err).Warnf("failed to delete failed writer job %s", initJob.Name)
		}
		// Unpopulated and ours to retry: remove the claim so the next request
		// starts clean. A populated claim never reaches this branch.
		if err := pvcs.Delete(ctx, current.Name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
			log.WithError(err).Warnf("failed to delete unpopulated shared cache claim %s", current.Name)
		}
		return fail(modelcachetypes.ReasonInitJobFailed, fmt.Errorf("writer job %s failed", initJob.Name))
	case InitCacheJobCompleted:
		job, err := jobs.Get(ctx, initJob.Name, metav1.GetOptions{})
		if err != nil {
			return fail(modelcachetypes.ReasonPVCSetupFailed, fmt.Errorf("get completed writer job %s: %w", initJob.Name, err))
		}
		if witness := job.Spec.Template.Annotations[nvcastorage.ModelCacheWriterPVCUIDAnnotationKey]; witness != string(current.UID) {
			// The Job populated an earlier claim of the same name. Its
			// completion says nothing about this claim; start a writer for it.
			background := metav1.DeletePropagationBackground
			if err := jobs.Delete(ctx, initJob.Name, metav1.DeleteOptions{PropagationPolicy: &background}); err != nil && !errors.IsNotFound(err) {
				return fail(modelcachetypes.ReasonPVCSetupFailed, fmt.Errorf("delete stale writer job %s: %w", initJob.Name, err))
			}
			return ModelCachingInProgress, current.Name
		}
		if err := c.markSharedClaimPopulated(ctx, current.Name); err != nil {
			return fail(modelcachetypes.ReasonPVCSetupFailed, err)
		}
		record(modelcachetypes.ResultSuccess, "")
		c.bk8s.EmitICMSEventf(req, corev1.EventTypeNormal, string(types.EventCategoryModelCaching),
			"shared cache claim %v populated", nil, current.Name)
		return ModelCachingCompleted, current.Name
	}
	return ModelCachingInProgress, current.Name
}

// sharedClaimFromArtifact turns the artifact's ReadWriteOnce writer claim into
// the shared claim: ReadWriteMany on the StorageClass the selection recorded.
func sharedClaimFromArtifact(
	rwPVC *corev1.PersistentVolumeClaim,
	selection *nvcastorage.PersistedModelCacheStorageSelection,
) *corev1.PersistentVolumeClaim {
	claim := rwPVC.DeepCopy()
	claim.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}
	if selection.StorageClassName != "" {
		sc := selection.StorageClassName
		claim.Spec.StorageClassName = &sc
	}
	return claim
}

// validateSharedClaimWriterJob requires the writer Job to mount the shared
// claim read-write exactly once, so a completed Job means that claim holds
// the model.
func validateSharedClaimWriterJob(job *batchv1.Job, claimName string) error {
	if job == nil || claimName == "" {
		return fmt.Errorf("shared claim writer Job or claim name is missing")
	}
	found := 0
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name != ModelVolumeName {
			continue
		}
		found++
		pvc := v.PersistentVolumeClaim
		if pvc == nil || pvc.ClaimName != claimName {
			return fmt.Errorf("shared claim writer Job volume %q does not mount claim %q", ModelVolumeName, claimName)
		}
		if pvc.ReadOnly {
			return fmt.Errorf("shared claim writer Job mounts %q read-only", ModelVolumeName)
		}
	}
	if found != 1 {
		return fmt.Errorf("shared claim writer Job has %d %q volumes, want exactly one", found, ModelVolumeName)
	}
	// A volume that no container mounts writes nothing. A completed Job would
	// then mark an empty claim populated.
	spec := job.Spec.Template.Spec
	for _, containers := range [][]corev1.Container{spec.InitContainers, spec.Containers} {
		for _, ctr := range containers {
			for _, m := range ctr.VolumeMounts {
				if m.Name == ModelVolumeName && !m.ReadOnly {
					return nil
				}
			}
		}
	}
	return fmt.Errorf("shared claim writer Job has no container mounting %q read-write", ModelVolumeName)
}

// writerJobFinished reports whether a Job reached a terminal state. A failed
// Pod is not terminal while retries remain; only the JobComplete or JobFailed
// condition is, with CompletionTime kept as a fallback for a completed Job
// whose conditions have not been populated.
func writerJobFinished(job *batchv1.Job) bool {
	for _, cond := range job.Status.Conditions {
		if (cond.Type == batchv1.JobComplete || cond.Type == batchv1.JobFailed) && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return job.Status.CompletionTime != nil
}

// cleanupSharedClaimRequestArtifacts is request cleanup for the shared-claim
// flow. The claim is shared and always kept for the reference sweep. The
// writer Job is shared too while it runs: deleting it would cancel population
// for every other request waiting on the same claim, so only a finished Job is
// removed.
func (c K8sComputeBackend) cleanupSharedClaimRequestArtifacts(ctx context.Context, initJob *batchv1.Job, claimName string) error {
	log := core.GetLogger(ctx)
	jobs := c.clients.K8s.BatchV1().Jobs(c.bk8s.podInstanceNamespace)
	job, err := jobs.Get(ctx, initJob.Name, metav1.GetOptions{})
	switch {
	case errors.IsNotFound(err):
		return nil
	case err != nil:
		return fmt.Errorf("get shared claim writer job %s: %w", initJob.Name, err)
	}
	if !writerJobFinished(job) {
		log.Debugf("shared claim writer %v still running for claim %v, leaving it for the other readers", job.Name, claimName)
		return nil
	}
	background := metav1.DeletePropagationBackground
	if err := jobs.Delete(ctx, job.Name, metav1.DeleteOptions{PropagationPolicy: &background}); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("delete finished shared claim writer job %s: %w", job.Name, err)
	}
	log.Debugf("leaving shared cache claim %v for the reference sweep", claimName)
	return nil
}

// markSharedClaimPopulated sets the durable populated marker on the claim,
// retrying on conflict. Readers gate on this label, never on Job state.
func (c K8sComputeBackend) markSharedClaimPopulated(ctx context.Context, claimName string) error {
	pvcs := c.clients.K8s.CoreV1().PersistentVolumeClaims(c.bk8s.podInstanceNamespace)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := pvcs.Get(ctx, claimName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get shared cache claim %s: %w", claimName, err)
		}
		if current.Labels == nil {
			current.Labels = map[string]string{}
		}
		if current.Labels[nvcastorage.ModelCachePopulatedLabelKey] == nvcastorage.ModelCachePopulatedLabelValue {
			return nil
		}
		current.Labels[nvcastorage.ModelCachePopulatedLabelKey] = nvcastorage.ModelCachePopulatedLabelValue
		if _, err := pvcs.Update(ctx, current, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("mark shared cache claim %s populated: %w", claimName, err)
		}
		return nil
	})
}

// regularModelCacheKeepsSharedClaim reports whether a request's persisted
// selection is the shared-claim flow, whose claim outlives any one request
// and is reclaimed by the reference sweep rather than by request cleanup.
func regularModelCacheKeepsSharedClaim(req *nvcav2beta1.ICMSRequest) bool {
	selection, present, err := regularModelCacheSelection(req)
	return err == nil && present &&
		selection.Mode == nvcastorage.ModelCacheSelectionDurable &&
		selection.Transition == nvcastorage.ModelCacheTransitionRWXReadOnly
}
