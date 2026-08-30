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

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	modelcachetypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/modelcachetypes"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// setupRWXReadOnlyModelCachingForRequest implements the regular model-cache
// transition that publishes the populated writer claim itself to readers. The
// caller holds modelCacheMtx. Writer and worker Pods are in the same namespace,
// so the transition requires no second PVC, PV rewrite, volume detach, or data
// copy. Workload construction applies read-only intent to the PVC source and
// every matching mount after this method returns the writer claim name.
func (c K8sComputeBackend) setupRWXReadOnlyModelCachingForRequest(
	ctx context.Context,
	rwPVC *corev1.PersistentVolumeClaim,
	initJob *batchv1.Job,
	req *nvcav2beta1.ICMSRequest,
	binding *nvcav2beta1.ModelCacheBinding,
	bindingUID types.UID,
) (ModelCachingState, string, error) {
	if rwPVC == nil || initJob == nil || binding == nil {
		return regularModelCacheResultForError(true, nvcaerrors.TerminalError(
			fmt.Errorf("rwxReadOnly model cache intent is incomplete")))
	}
	if binding.Spec.Decision.Transition != nvcastorage.ModelCacheTransitionRWXReadOnly {
		return regularModelCacheResultForError(true, nvcaerrors.TerminalError(
			fmt.Errorf("model cache binding transition is %q, want %q",
				binding.Spec.Decision.Transition, nvcastorage.ModelCacheTransitionRWXReadOnly)))
	}
	if initJob.Spec.TTLSecondsAfterFinished != nil {
		return regularModelCacheResultForError(true, nvcaerrors.TerminalError(
			fmt.Errorf("rwxReadOnly writer Job must not use ttlSecondsAfterFinished")))
	}
	if err := validateRWXReadOnlyWriterJobPVC(initJob, rwPVC.Name); err != nil {
		return regularModelCacheResultForError(true,
			nvcaerrors.TerminalError(err))
	}

	current, err := c.clients.K8s.CoreV1().
		PersistentVolumeClaims(c.bk8s.podInstanceNamespace).
		Get(ctx, rwPVC.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		job, jobErr := c.getRWXReadOnlyWriterJob(ctx, initJob, binding, nil)
		if jobErr != nil {
			return regularModelCacheResultForError(true, jobErr)
		}
		if job != nil {
			return regularModelCacheResultForError(true, nvcaerrors.TerminalError(
				fmt.Errorf("rwxReadOnly writer PVC %s/%s is missing while writer Job %s/%s exists",
					c.bk8s.podInstanceNamespace, rwPVC.Name, job.Namespace, job.Name)))
		}
		if err := c.SetupInitCacheJobBlockDevice(ctx, rwPVC, initJob, req); err != nil {
			return regularModelCacheResultForError(true, err)
		}
		return ModelCachingInProgress, "", nil
	}
	if err != nil {
		return regularModelCacheResultForError(true,
			fmt.Errorf("get rwxReadOnly writer PVC %s/%s: %w",
				c.bk8s.podInstanceNamespace, rwPVC.Name, err))
	}
	if err := validateRegularModelCachePVC(current, rwPVC, binding, false); err != nil {
		return regularModelCacheResultForError(true, nvcaerrors.TerminalError(err))
	}
	if current.DeletionTimestamp != nil {
		return regularModelCacheResultForError(true, nvcaerrors.TerminalError(
			fmt.Errorf("rwxReadOnly writer PVC %s/%s is terminating",
				current.Namespace, current.Name)))
	}
	if err := bindRegularModelCacheWriterJobToPVC(initJob, current, binding); err != nil {
		return regularModelCacheResultForError(true, nvcaerrors.TerminalError(err))
	}

	pvcState, stateErr := c.checkPVCState(
		ctx, rwPVC.Name, bindingUID, true, false, binding)
	switch pvcState {
	case PVCQueryError:
		return regularModelCacheResultForError(true, stateErr)
	case PVCFoundUnBound:
		job, jobErr := c.getRWXReadOnlyWriterJob(ctx, initJob, binding, current)
		if jobErr != nil {
			return regularModelCacheResultForError(true, jobErr)
		}
		if job == nil {
			if err := c.SetupInitCacheJobBlockDevice(ctx, rwPVC, initJob, req); err != nil {
				return regularModelCacheResultForError(true, err)
			}
		}
		return ModelCachingInProgress, "", nil
	case PVCFoundBindFailed, PVCNotFound:
		if stateErr == nil {
			stateErr = fmt.Errorf("rwxReadOnly writer PVC %s/%s is not usable",
				c.bk8s.podInstanceNamespace, rwPVC.Name)
		}
		return c.failRWXReadOnlyModelCache(ctx, req, rwPVC, initJob, stateErr)
	case PVCFoundBound:
	default:
		return regularModelCacheResultForError(true, nvcaerrors.TerminalError(
			fmt.Errorf("unexpected rwxReadOnly writer PVC state %q", pvcState)))
	}
	current, err = c.getValidatedRWXReadOnlyBoundClaim(ctx, rwPVC, binding)
	if err != nil {
		return regularModelCacheResultForError(true, err)
	}
	if _, err := c.getRWXReadOnlyWriterJob(ctx, initJob, binding, current); err != nil {
		return regularModelCacheResultForError(true, err)
	}

	if current.Labels[nvcastorage.ModelCachePopulatedLabelKey] ==
		nvcastorage.ModelCachePopulatedLabelValue {
		published, err := c.validateRWXReadOnlyPublication(
			ctx, req, rwPVC, initJob, binding)
		if err != nil {
			return regularModelCacheResultForError(true, err)
		}
		return ModelCachingCompleted, published.Name, nil
	}

	jobState, jobErr := c.CheckInitCacheJobState(
		ctx, rwPVC.Name, initJob, bindingUID, true)
	if jobErr != nil {
		return regularModelCacheResultForError(true, jobErr)
	}
	switch jobState {
	case InitCacheJobNotFound:
		if err := c.SetupInitCacheJobBlockDevice(ctx, rwPVC, initJob, req); err != nil {
			return regularModelCacheResultForError(true, err)
		}
		return ModelCachingInProgress, "", nil
	case InitCacheJobInProgress:
		return ModelCachingInProgress, "", nil
	case InitCacheJobFailed:
		return c.failRWXReadOnlyModelCache(
			ctx, req, rwPVC, initJob, fmt.Errorf("init cache Job %s failed", initJob.Name))
	case InitCacheJobCompleted:
		if _, err := c.requireCompletedRWXReadOnlyWriterJob(
			ctx, initJob, binding, current); err != nil {
			return regularModelCacheResultForError(true, err)
		}
		if err := c.markRWXReadOnlyModelCachePopulated(
			ctx, req, rwPVC, initJob, binding); err != nil {
			return regularModelCacheResultForError(true, err)
		}
		published, err := c.validateRWXReadOnlyPublication(
			ctx, req, rwPVC, initJob, binding)
		if err != nil {
			return regularModelCacheResultForError(true, err)
		}
		nvcametrics.FromContext(ctx).RecordModelCacheResult(
			modelcachetypes.ResultSuccess, "", string(nvcatypes.HelmCacheBackendSharedFS))
		return ModelCachingCompleted, published.Name, nil
	default:
		return regularModelCacheResultForError(true, nvcaerrors.TerminalError(
			fmt.Errorf("unexpected rwxReadOnly writer Job state %q", jobState)))
	}
}

func validateRWXReadOnlyWriterJobPVC(job *batchv1.Job, pvcName string) error {
	if job == nil || pvcName == "" {
		return fmt.Errorf("rwxReadOnly writer Job or PVC name is missing")
	}
	modelVolumeCount := 0
	for _, volume := range job.Spec.Template.Spec.Volumes {
		if volume.Name != ModelVolumeName {
			continue
		}
		modelVolumeCount++
		claim := volume.PersistentVolumeClaim
		if claim == nil || claim.ClaimName != pvcName {
			return fmt.Errorf(
				"rwxReadOnly writer Job volume %q does not reference PVC %q",
				ModelVolumeName, pvcName)
		}
		if claim.ReadOnly {
			return fmt.Errorf("rwxReadOnly writer Job PVC volume %q is read-only", ModelVolumeName)
		}
	}
	if modelVolumeCount == 0 {
		return fmt.Errorf("rwxReadOnly writer Job has no %q volume", ModelVolumeName)
	}
	if modelVolumeCount != 1 {
		return fmt.Errorf("rwxReadOnly writer Job has %d %q volumes, want exactly one",
			modelVolumeCount, ModelVolumeName)
	}
	containers := append(
		append([]corev1.Container(nil), job.Spec.Template.Spec.InitContainers...),
		job.Spec.Template.Spec.Containers...)
	for _, container := range containers {
		for _, mount := range container.VolumeMounts {
			if mount.Name == ModelVolumeName && !mount.ReadOnly {
				return nil
			}
		}
		for _, device := range container.VolumeDevices {
			if device.Name == ModelVolumeName {
				return nil
			}
		}
	}
	return fmt.Errorf(
		"rwxReadOnly writer Job does not mount PVC %q writable", pvcName)
}
func validateRWXReadOnlyWriterJobPVCWitness(
	job *batchv1.Job,
	pvc *corev1.PersistentVolumeClaim,
) error {
	if job == nil || pvc == nil || pvc.UID == "" {
		return fmt.Errorf("rwxReadOnly writer Job PVC witness input is incomplete")
	}
	recorded := job.Spec.Template.Annotations[nvcastorage.ModelCacheWriterPVCUIDAnnotationKey]
	if recorded != string(pvc.UID) {
		return fmt.Errorf("rwxReadOnly writer Job records PVC UID %q, want %q",
			recorded, pvc.UID)
	}
	return nil
}

func (c K8sComputeBackend) currentRWXReadOnlyBinding(
	ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
	wanted *nvcav2beta1.ModelCacheBinding,
) (*nvcav2beta1.ModelCacheBinding, error) {
	current, err := c.bk8s.activeModelCacheBindingForRuntime(ctx, req)
	if err != nil {
		return nil, err
	}
	if current == nil || wanted == nil || current.UID != wanted.UID {
		return nil, nvcaerrors.TerminalError(fmt.Errorf(
			"active rwxReadOnly model cache binding identity changed"))
	}
	if current.Spec.Decision.Transition != nvcastorage.ModelCacheTransitionRWXReadOnly {
		return nil, nvcaerrors.TerminalError(fmt.Errorf(
			"model cache binding transition is %q, want %q",
			current.Spec.Decision.Transition,
			nvcastorage.ModelCacheTransitionRWXReadOnly))
	}
	return current, nil
}

// validateRWXReadOnlyPublication re-reads every durable publication witness
// before returning the writer claim to a reader. The repeated binding and
// claim reads close the gaps between independent Kubernetes API operations;
// they do not claim transactional consistency across those objects.
func (c K8sComputeBackend) validateRWXReadOnlyPublication(
	ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
	wantedPVC *corev1.PersistentVolumeClaim,
	wantedJob *batchv1.Job,
	wantedBinding *nvcav2beta1.ModelCacheBinding,
) (*corev1.PersistentVolumeClaim, error) {
	liveBinding, err := c.currentRWXReadOnlyBinding(ctx, req, wantedBinding)
	if err != nil {
		return nil, err
	}
	claim, err := c.getValidatedRWXReadOnlyBoundClaim(ctx, wantedPVC, liveBinding)
	if err != nil {
		return nil, err
	}
	if claim.Labels[nvcastorage.ModelCachePopulatedLabelKey] !=
		nvcastorage.ModelCachePopulatedLabelValue {
		return nil, nvcaerrors.TerminalError(fmt.Errorf(
			"rwxReadOnly writer PVC %s/%s is not marked populated",
			claim.Namespace, claim.Name))
	}
	if _, err := c.requireCompletedRWXReadOnlyWriterJob(
		ctx, wantedJob, liveBinding, claim); err != nil {
		return nil, err
	}

	// Re-read after validating the Job so a concurrently retired binding or
	// replaced claim cannot be published from the earlier snapshot.
	liveBinding, err = c.currentRWXReadOnlyBinding(ctx, req, wantedBinding)
	if err != nil {
		return nil, err
	}
	finalClaim, err := c.getValidatedRWXReadOnlyBoundClaim(
		ctx, wantedPVC, liveBinding)
	if err != nil {
		return nil, err
	}
	if finalClaim.UID != claim.UID {
		return nil, nvcaerrors.TerminalError(fmt.Errorf(
			"rwxReadOnly writer PVC %s/%s UID changed from %q to %q during publication",
			claim.Namespace, claim.Name, claim.UID, finalClaim.UID))
	}
	if finalClaim.Labels[nvcastorage.ModelCachePopulatedLabelKey] !=
		nvcastorage.ModelCachePopulatedLabelValue {
		return nil, nvcaerrors.TerminalError(fmt.Errorf(
			"rwxReadOnly writer PVC %s/%s lost its populated marker during publication",
			finalClaim.Namespace, finalClaim.Name))
	}
	if _, err := c.requireCompletedRWXReadOnlyWriterJob(
		ctx, wantedJob, liveBinding, finalClaim); err != nil {
		return nil, err
	}
	if _, err := c.currentRWXReadOnlyBinding(ctx, req, wantedBinding); err != nil {
		return nil, err
	}
	return finalClaim, nil
}
func (c K8sComputeBackend) getValidatedRWXReadOnlyBoundClaim(
	ctx context.Context,
	wanted *corev1.PersistentVolumeClaim,
	binding *nvcav2beta1.ModelCacheBinding,
) (*corev1.PersistentVolumeClaim, error) {
	current, err := c.clients.K8s.CoreV1().
		PersistentVolumeClaims(c.bk8s.podInstanceNamespace).
		Get(ctx, wanted.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get rwxReadOnly writer PVC before publication: %w", err)
	}
	if err := validateRegularModelCachePVC(current, wanted, binding, false); err != nil {
		return nil, nvcaerrors.TerminalError(err)
	}
	if current.DeletionTimestamp != nil {
		return nil, nvcaerrors.TerminalError(fmt.Errorf(
			"rwxReadOnly writer PVC %s/%s is terminating",
			current.Namespace, current.Name))
	}
	if !isPVCBound(current) {
		return nil, nvcaerrors.TerminalError(fmt.Errorf(
			"rwxReadOnly writer PVC %s/%s is not Bound",
			current.Namespace, current.Name))
	}
	if current.Spec.VolumeName == "" {
		return nil, nvcaerrors.TerminalError(fmt.Errorf(
			"Bound rwxReadOnly writer PVC %s/%s has no PV name",
			current.Namespace, current.Name))
	}
	pv, err := c.clients.K8s.CoreV1().PersistentVolumes().
		Get(ctx, current.Spec.VolumeName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nvcaerrors.TerminalError(fmt.Errorf(
			"Bound rwxReadOnly writer PVC %s/%s references missing PV %q",
			current.Namespace, current.Name, current.Spec.VolumeName))
	}
	if err != nil {
		return nil, fmt.Errorf("get rwxReadOnly writer PV %s: %w",
			current.Spec.VolumeName, err)
	}
	if pv.DeletionTimestamp != nil {
		return nil, nvcaerrors.TerminalError(fmt.Errorf(
			"rwxReadOnly writer PV %s is terminating", pv.Name))
	}
	if pv.Status.Phase != corev1.VolumeBound {
		return nil, nvcaerrors.TerminalError(fmt.Errorf(
			"rwxReadOnly writer PV %s phase is %q, want %q",
			pv.Name, pv.Status.Phase, corev1.VolumeBound))
	}
	if err := validateRegularModelCachePVForPVC(
		binding, current, pv, RWXAccessMode); err != nil {
		return nil, nvcaerrors.TerminalError(err)
	}
	return current, nil
}

func (c K8sComputeBackend) getRWXReadOnlyWriterJob(
	ctx context.Context,
	wanted *batchv1.Job,
	binding *nvcav2beta1.ModelCacheBinding,
	pvc *corev1.PersistentVolumeClaim,
) (*batchv1.Job, error) {
	job, err := c.clients.K8s.BatchV1().Jobs(c.bk8s.podInstanceNamespace).
		Get(ctx, wanted.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get rwxReadOnly writer Job %s/%s: %w",
			c.bk8s.podInstanceNamespace, wanted.Name, err)
	}
	if job.DeletionTimestamp != nil {
		return nil, nvcaerrors.TerminalError(fmt.Errorf(
			"rwxReadOnly writer Job %s/%s is terminating", job.Namespace, job.Name))
	}
	if pvc != nil {
		if err := validateRWXReadOnlyWriterJobPVCWitness(job, pvc); err != nil {
			return nil, nvcaerrors.TerminalError(err)
		}
	}
	if err := validateRegularModelCacheJob(job, wanted, binding); err != nil {
		return nil, nvcaerrors.TerminalError(err)
	}
	return job, nil
}

func (c K8sComputeBackend) markRWXReadOnlyModelCachePopulated(
	ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
	wanted *corev1.PersistentVolumeClaim,
	wantedJob *batchv1.Job,
	binding *nvcav2beta1.ModelCacheBinding,
) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		liveBinding, err := c.currentRWXReadOnlyBinding(ctx, req, binding)
		if err != nil {
			return err
		}
		current, err := c.getValidatedRWXReadOnlyBoundClaim(ctx, wanted, liveBinding)
		if err != nil {
			return err
		}
		if _, err := c.requireCompletedRWXReadOnlyWriterJob(
			ctx, wantedJob, liveBinding, current); err != nil {
			return err
		}
		if current.Labels == nil {
			current.Labels = map[string]string{}
		}
		if current.Labels[nvcastorage.ModelCachePopulatedLabelKey] ==
			nvcastorage.ModelCachePopulatedLabelValue {
			return nil
		}
		current.Labels[nvcastorage.ModelCachePopulatedLabelKey] =
			nvcastorage.ModelCachePopulatedLabelValue
		_, err = c.clients.K8s.CoreV1().
			PersistentVolumeClaims(c.bk8s.podInstanceNamespace).
			Update(ctx, current, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("mark rwxReadOnly writer PVC populated: %w", err)
		}
		return nil
	})
}

// requireCompletedRWXReadOnlyWriterJob keeps the completed Job as the durable
// publication fence. NVCA never deletes it while the binding is Active. This
// prevents another NVCA replica with a stale pre-publication read from
// recreating a writer after readers have started.
func (c K8sComputeBackend) requireCompletedRWXReadOnlyWriterJob(
	ctx context.Context,
	wanted *batchv1.Job,
	binding *nvcav2beta1.ModelCacheBinding,
	pvc *corev1.PersistentVolumeClaim,
) (*batchv1.Job, error) {
	job, err := c.getRWXReadOnlyWriterJob(ctx, wanted, binding, pvc)
	if err != nil {
		return nil, err
	}
	if job == nil {
		return nil, nvcaerrors.TerminalError(fmt.Errorf(
			"rwxReadOnly publication fence Job %s/%s is missing",
			c.bk8s.podInstanceNamespace, wanted.Name))
	}
	if job.Status.CompletionTime == nil || job.Status.Succeeded == 0 {
		return nil, nvcaerrors.TerminalError(fmt.Errorf(
			"populated rwxReadOnly writer PVC has non-completed Job %s/%s",
			job.Namespace, job.Name))
	}
	return job, nil
}

func (c K8sComputeBackend) failRWXReadOnlyModelCache(
	ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
	rwPVC *corev1.PersistentVolumeClaim,
	initJob *batchv1.Job,
	cause error,
) (ModelCachingState, string, error) {
	log := core.GetLogger(ctx)
	if err := c.CleanupModelCachingResources(ctx, req, rwPVC, initJob.Name); err != nil {
		log.WithError(err).Error("failed to clean up rwxReadOnly model cache resources")
		return regularModelCacheResultForError(true, err)
	}
	nvcametrics.FromContext(ctx).RecordModelCacheResult(
		modelcachetypes.ResultFailure, modelcachetypes.ReasonPVCSetupFailed,
		string(nvcatypes.HelmCacheBackendSharedFS))
	return regularModelCacheResultForError(true, nvcaerrors.TerminalError(cause))
}
