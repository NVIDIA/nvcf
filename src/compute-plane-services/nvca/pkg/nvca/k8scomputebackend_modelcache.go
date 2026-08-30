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
	stderrors "errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	modelcachetypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/modelcachetypes"
	nvcak8sutil "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/encryption"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	nvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// skip option only for UT, real cluster detach check is must
var (
	skipVolumeDetachCheck = false
	ROAccessMode          = []v1.PersistentVolumeAccessMode{v1.ReadOnlyMany}
	RWXAccessMode         = []v1.PersistentVolumeAccessMode{v1.ReadWriteMany}
)

type PVCState string

const (
	PVCNoState         PVCState = "PVCNoState"
	PVCQueryError      PVCState = "PVCStateQueryError"
	PVCNotFound        PVCState = "PVCNotFound"
	PVCFoundBound      PVCState = "PVCFoundBound"
	PVCFoundUnBound    PVCState = "PVCFoundUnBound"
	PVCUpdateFailed    PVCState = "PVCUpdateFailed"
	PVCFoundBindFailed PVCState = "PVCFoundBindFailed"
)

type ModelCachingState string

const (
	ModelCachingInProgress    ModelCachingState = "ModelCachingInProgress"
	ModelCachingFailed        ModelCachingState = "ModelCachingFailed"
	ModelCachingCompleted     ModelCachingState = "ModelCachingCompleted"
	ModelCachingCleanupFailed ModelCachingState = "ModelCachingCleanupFailed"
)

type InitCacheJobState string

const (
	InitCacheJobNotFound   InitCacheJobState = "InitCacheJobNotFound"
	InitCacheJobFailed     InitCacheJobState = "InitCacheJobFailed"
	InitCacheJobInProgress InitCacheJobState = "InitCacheJobInProgress"
	InitCacheJobCompleted  InitCacheJobState = "InitCacheJobCompleted"
)

type ROPVCSetupPhase string

const (
	ROPVCSetupQueryFailed ROPVCSetupPhase = "ROPVCSetupQueryFailed"
	ROPVCSetupInProgress  ROPVCSetupPhase = "ROPVCSetupInProgress"
	ROPVUpdateFailed      ROPVCSetupPhase = "ROPVUpdateFailed"
	ROPVCSetupFailed      ROPVCSetupPhase = "ROPVCSetupFailed"
	ROPVCSetupCompleted   ROPVCSetupPhase = "ROPVCSetupCompleted"
)

func (c K8sComputeBackend) CleanupModelCachingSetupArtifacts(ctx context.Context, req *nvcav2beta1.ICMSRequest) error {
	c.bk8s.modelCacheMtx.Lock()
	defer c.bk8s.modelCacheMtx.Unlock()
	log := core.GetLogger(ctx)
	log.Debugf("decoding caching artifacts")

	_, _, _, _, icjDecoded, bdDecode := getArtifactsFromReq(req)
	isMiniServiceType := req.Spec.CreationMsgInfo.FunctionLaunchSpecification != nil &&
		req.Spec.CreationMsgInfo.FunctionLaunchSpecification.HelmChartLaunchSpecification != nil
	if isMiniServiceType {
		return nil
	}
	binding, cleanupSharedResources, err := c.regularModelCacheCleanupBinding(ctx, req)
	if err != nil {
		return fmt.Errorf("resolve regular model cache binding for setup artifact cleanup: %w", err)
	}
	if !cleanupSharedResources {
		return nil
	}
	if binding == nil && !c.bk8s.cachingSupportEnabled {
		return nil
	}
	if icjDecoded.Specification == "" || bdDecode.Specification == "" {
		if binding != nil {
			return fmt.Errorf("persisted durable regular model cache has incomplete cleanup artifacts")
		}
		return nil
	}

	mf := func(obj client.Object) {}

	rwPVC, initJob, err := getModelCacheK8sArtifacts(ctx, bdDecode, icjDecoded, mf)
	if err != nil {
		log.WithError(err).Error("failed getModelCacheK8sArtifacts, model caching will be disabled")
		return fmt.Errorf("failed to cleanup in-flight cache job: %w", err)
	}
	if binding != nil {
		// Binding-scoped cleanup must use the transition-aware path. It proves PV
		// ownership, changes an exact bound Retain PV to Delete, and then removes
		// the Job and PVC with identity preconditions. The same request may resume
		// this cleanup after the binding entered Retiring above.
		return c.CleanupModelCachingResources(ctx, req, rwPVC, initJob.Name)
	}

	// Preserve the annotation-free legacy cleanup behavior.
	namespace := c.bk8s.podInstanceNamespace
	if initJob != nil {
		backgroundDeletion := metav1.DeletePropagationBackground
		deleteOptions := metav1.DeleteOptions{PropagationPolicy: &backgroundDeletion}
		err = c.clients.K8s.BatchV1().Jobs(namespace).
			Delete(ctx, initJob.Name, deleteOptions)
		if err != nil && !errors.IsNotFound(err) {
			log.WithError(err).Warnf("failed to cleanup initCacheJob %v/%v, needs manual cleanup",
				namespace, initJob.Name)
			return fmt.Errorf("failed to cleanup in-flight cache job: %w", err)
		}
	}

	if rwPVC != nil {
		err = c.clients.K8s.CoreV1().PersistentVolumeClaims(namespace).
			Delete(ctx, rwPVC.Name, metav1.DeleteOptions{})
		if err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("failed to delete pvc %v: %w", rwPVC.Name, err)
		}
	}
	return nil
}

func (c K8sComputeBackend) SetupModelCachingForRequest(ctx context.Context,
	rwPVC *v1.PersistentVolumeClaim,
	initJob *batchv1.Job,
	req *nvcav2beta1.ICMSRequest,
	encryptionRequired bool,
	mf mutateFunc,
) (ModelCachingState, string, error) {
	c.bk8s.modelCacheMtx.Lock()
	defer c.bk8s.modelCacheMtx.Unlock()

	log := core.GetLogger(ctx)
	log.Debugf("decoding caching artifacts")
	metrics := nvcametrics.FromContext(ctx)

	bindingUID, bindingScoped, err := regularModelCacheBindingUID(req)
	if err != nil {
		return ModelCachingFailed, "", nvcaerrors.TerminalError(
			fmt.Errorf("resolve regular model cache binding ownership: %w", err))
	}

	var binding *nvcav2beta1.ModelCacheBinding
	if bindingScoped {
		binding, err = c.bk8s.activeModelCacheBindingForRuntime(ctx, req)
		if err != nil {
			if stderrors.Is(err, errRegularModelCacheBindingRetiring) {
				cleanupErr := c.CleanupModelCachingResources(ctx, req, rwPVC, initJob.Name)
				if cleanupErr != nil {
					return regularModelCacheResultForError(true, cleanupErr)
				}
			}
			return regularModelCacheResultForError(true, err)
		}
		if bindingScoped && binding != nil &&
			binding.Spec.Decision.Transition == nvcastorage.ModelCacheTransitionRWXReadOnly {
			if encryptionRequired {
				return regularModelCacheResultForError(true, nvcaerrors.TerminalError(
					fmt.Errorf("rwxReadOnly model cache transition does not support NVMesh encryption")))
			}
			return c.setupRWXReadOnlyModelCachingForRequest(
				ctx, rwPVC, initJob, req, binding, bindingUID)
		}
	}
	if encryptionRequired {
		//If encryption is used, then we need to update StorageClass name in PVC.
		storageClassName, err := encryption.SetupEncryption(ctx, c.clients, req.Spec.NCAId, req.Namespace)
		if err != nil {
			log.WithError(err).Error("failed to set up cache encryption, resort to non-caching")
			return regularModelCacheResultForError(bindingScoped,
				fmt.Errorf("set up regular model cache encryption: %w", err))
		}

		if bindingScoped {
			expectedStorageClass, expectedErr := regularModelCacheExpectedStorageClassName(binding)
			if expectedErr != nil {
				return regularModelCacheResultForError(true, expectedErr)
			}
			if storageClassName != expectedStorageClass {
				return regularModelCacheResultForError(true, fmt.Errorf(
					"derived encrypted StorageClass %q does not match binding intent %q", storageClassName, expectedStorageClass))
			}
		}
		rwPVC.Spec.StorageClassName = &storageClassName
	}

	roPVCName, err := regularModelCacheReaderPVCName(rwPVC.Name)
	if err != nil {
		return regularModelCacheResultForError(bindingScoped, nvcaerrors.TerminalError(err))
	}
	roPVCState, err := c.checkPVCState(ctx, roPVCName, bindingUID, bindingScoped, true, binding)
	switch roPVCState {
	case PVCNotFound:
		jS, jobStateErr := c.CheckInitCacheJobState(ctx, rwPVC.Name, initJob, bindingUID, bindingScoped)
		if jobStateErr != nil && bindingScoped {
			return regularModelCacheResultForError(true, jobStateErr)
		}
		switch jS {
		case InitCacheJobNotFound:
			pvLabelSel, err := makePVLabelSelectorForCacheRequest(req)
			if err != nil {
				log.WithError(err).Error("failed to create label requirement for cache PV, resort to non-caching")
				return regularModelCacheResultForError(bindingScoped, err)
			}
			// check if PV for the function exists, if so, continue as ModelCachingInProgress
			// lets find the underlying PV for this function/task
			pvObjList, err := c.clients.K8s.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{
				LabelSelector: pvLabelSel})
			if err != nil && !errors.IsNotFound(err) {
				if bindingScoped {
					return regularModelCacheResultForError(true,
						fmt.Errorf("list regular model cache PVs: %w", err))
				}
				return ModelCachingFailed, "", nil
			}
			if errors.IsNotFound(err) || (pvObjList != nil && len(pvObjList.Items) == 0) {
				err = c.SetupInitCacheJobBlockDevice(ctx, rwPVC, initJob, req)
				if err != nil {
					c.bk8s.EmitICMSEvent(req, v1.EventTypeWarning,
						string(types.EventCategoryModelCaching), "failed caching setup, resort to non-caching", nil)
					log.WithError(err).Error("failed SetupInitCacheJobBlockDevice, model caching will be disabled")
					return regularModelCacheResultForError(bindingScoped, err)
				}
				return ModelCachingInProgress, "", nil
			}
			if pvObjList != nil && len(pvObjList.Items) == 1 {
				// let it reconcile again
				roPVCState, setupErr := c.SetupPVCForReaders(ctx, rwPVC, initJob.Name, req, mf)
				if setupErr == nil {
					return ModelCachingInProgress, "", nil
				}
				if bindingScoped && regularModelCacheErrorIsRetryable(setupErr) {
					return ModelCachingInProgress, "", setupErr
				}
				log.WithError(setupErr).Errorf(
					"failed to SetupPVCForReaders at %v, model caching will be disabled", roPVCState)
				cleanupErr := c.CleanupModelCachingResources(ctx, req, rwPVC, initJob.Name)
				if cleanupErr != nil {
					log.WithError(cleanupErr).Error(
						"failed to cleanup ModelCaching resources, needs manual cleanup")
					if bindingScoped {
						return regularModelCacheResultForError(true, cleanupErr)
					}
				}
				c.bk8s.EmitICMSEvent(req, v1.EventTypeWarning,
					string(types.EventCategoryModelCaching), "failed pvc setup, resort to non-caching", nil)
				metrics.EventErrorTotal.WithLabelValues(
					metrics.WithDefaultLabelValues(EventModelCachingFailed)...).Inc()
				if bindingScoped {
					return regularModelCacheResultForError(true, setupErr)
				}
				return ModelCachingFailed, "", nil
			}
			return ModelCachingFailed, "", nil
		case InitCacheJobFailed:
			// this is an irrecoverable error on InitCacheJob, NVCA will switch to
			// No Caching Workflow.
			cleanupErr := c.CleanupModelCachingResources(ctx, req, rwPVC, initJob.Name)
			if cleanupErr != nil {
				log.WithError(cleanupErr).Error(
					"failed to cleanup ModelCaching resources, needs manual cleanup")
				if bindingScoped {
					return regularModelCacheResultForError(true, cleanupErr)
				}
			}
			c.bk8s.EmitICMSEventf(req, v1.EventTypeWarning,
				string(types.EventCategoryModelCaching), "%v failed, resort to non-caching", nil, initJob.Name)
			reason := c.getInitCacheJobFailureReason(ctx, initJob)
			metrics.RecordModelCacheResult(
				modelcachetypes.ResultFailure, reason, string(types.HelmCacheBackendNVMesh))
			if bindingScoped {
				return regularModelCacheResultForError(true,
					fmt.Errorf("init cache Job %s failed", initJob.Name))
			}
			return ModelCachingFailed, "", nil
		case InitCacheJobCompleted:
			roPVCState, setupErr := c.SetupPVCForReaders(ctx, rwPVC, initJob.Name, req, mf)
			if setupErr == nil {
				return ModelCachingInProgress, "", nil
			}
			if bindingScoped && regularModelCacheErrorIsRetryable(setupErr) {
				return ModelCachingInProgress, "", setupErr
			}
			log.WithError(setupErr).Errorf(
				"failed to SetupPVCForReaders at %v, model caching will be disabled", roPVCState)
			cleanupErr := c.CleanupModelCachingResources(ctx, req, rwPVC, initJob.Name)
			if cleanupErr != nil {
				log.WithError(cleanupErr).Error(
					"failed to cleanup ModelCaching resources, needs manual cleanup")
				if bindingScoped {
					return regularModelCacheResultForError(true, cleanupErr)
				}
			}
			c.bk8s.EmitICMSEvent(req, v1.EventTypeWarning,
				string(types.EventCategoryModelCaching), "failed pvc setup, resort to non-caching", nil)
			metrics.EventErrorTotal.WithLabelValues(
				metrics.WithDefaultLabelValues(EventModelCachingFailed)...).Inc()
			metrics.RecordModelCacheResult(modelcachetypes.ResultFailure,
				modelcachetypes.ReasonPVCSetupFailed, string(types.HelmCacheBackendNVMesh))
			if bindingScoped {
				return regularModelCacheResultForError(true, setupErr)
			}
			return ModelCachingFailed, "", nil
		case InitCacheJobInProgress:
			return ModelCachingInProgress, "", nil
		}
	case PVCQueryError:
		log.WithError(err).Error("failed to query ROPVC")
		if bindingScoped {
			return regularModelCacheResultForError(true, err)
		}
		// Preserve the annotation-free legacy retry behavior.
		return ModelCachingInProgress, "", nil
	case PVCFoundUnBound:
		log.Debugf("ROPVC is still unbound, continue wait")
		return ModelCachingInProgress, "", nil
	case PVCFoundBindFailed:
		log.WithError(err).Errorf(
			"ROPVC is not getting bound, cleanup Modelcaching resource and deploy without caching")
		cleanupErr := c.CleanupModelCachingResources(ctx, req, rwPVC, initJob.Name)
		if cleanupErr != nil {
			log.WithError(cleanupErr).Errorf(
				"failed to cleanup ModelCaching resources, needs manual cleanup")
			if bindingScoped {
				return regularModelCacheResultForError(true, cleanupErr)
			}
		}
		c.bk8s.EmitICMSEventf(req, v1.EventTypeWarning,
			string(types.EventCategoryModelCaching), "%v bind failed, resort to non-caching", nil, roPVCName)
		metrics.EventErrorTotal.WithLabelValues(
			metrics.WithDefaultLabelValues(EventPVCModelCachingError)...).Inc()
		metrics.EventErrorTotal.WithLabelValues(
			metrics.WithDefaultLabelValues(EventModelCachingFailed)...).Inc()
		metrics.RecordModelCacheResult(modelcachetypes.ResultFailure,
			modelcachetypes.ReasonPVCBindFailed, string(types.HelmCacheBackendNVMesh))
		if bindingScoped {
			if err == nil {
				err = fmt.Errorf("reader PVC %s bind failed", roPVCName)
			}
			return regularModelCacheResultForError(true, err)
		}
		return ModelCachingFailed, "", nil
	case PVCFoundBound:
		log.Infof("ROPVC %v setup completed, Modelcaching will be enabled for request %v/%v",
			roPVCName, req.Namespace, req.Name)
		transitionTargets, _, transitionErr := c.regularModelCacheTransitionTargets(
			ctx, req, rwPVC.Name, initJob.Name)
		if transitionErr != nil {
			log.WithError(transitionErr).Error("refusing unverified model cache transition cleanup")
			return regularModelCacheResultForError(bindingScoped, transitionErr)
		}
		// The successful writer-to-reader transition publishes the shared cache,
		// so another request reference does not block this exact Job cleanup.
		jobNamespace := c.bk8s.podInstanceNamespace
		jobToDelete := initJob
		if bindingScoped {
			jobNamespace = transitionTargets.namespace
			jobToDelete = transitionTargets.initJob
		}
		if jobToDelete != nil {
			backgroundDeletion := metav1.DeletePropagationBackground
			deleteOptions := metav1.DeleteOptions{PropagationPolicy: &backgroundDeletion}
			if bindingScoped {
				deleteOptions, err = modelCacheDeleteOptions(jobToDelete, &backgroundDeletion)
				if err != nil {
					return regularModelCacheResultForError(true, err)
				}
			}
			err = c.clients.K8s.BatchV1().Jobs(jobNamespace).
				Delete(ctx, jobToDelete.Name, deleteOptions)
			if err != nil && !errors.IsNotFound(err) {
				log.WithError(err).Warnf(
					"failed to cleanup initCacheJob %v/%v, needs manual cleanup",
					jobNamespace, jobToDelete.Name)
				if bindingScoped {
					return regularModelCacheResultForError(true,
						fmt.Errorf("delete binding-owned init cache Job: %w", err))
				}
			}
		}
		metrics.EventErrorTotal.WithLabelValues(
			metrics.WithDefaultLabelValues(EventModelCachingSuccess)...).Inc()
		metrics.RecordModelCacheResult(
			modelcachetypes.ResultSuccess, "", string(types.HelmCacheBackendNVMesh))
		return ModelCachingCompleted, roPVCName, nil
	}
	if bindingScoped {
		return regularModelCacheResultForError(true,
			fmt.Errorf("unexpected regular model cache state %q", roPVCState))
	}
	return ModelCachingFailed, "", nil
}

// references for K8sComputeBackend are that of PVCNames
// PVCs are created in the podInstanceNamespace
func (c K8sComputeBackend) ComputeCleanupCacheReferences(ctx context.Context, cacheReferences []string) error {
	log := core.GetLogger(ctx)
	for _, pvc := range cacheReferences {
		log.Infof("cleaning-up pvc %v, since it was last accessed more than 60 mins ago", pvc)
		pvcObj, err := c.clients.K8s.CoreV1().PersistentVolumeClaims(c.bk8s.podInstanceNamespace).Get(ctx, pvc, metav1.GetOptions{})
		if err != nil {
			if !errors.IsNotFound(err) {
				log.Debugf("pvc %v already cleaned-up", pvc)
				continue
			}
			log.WithError(err).Errorf("failed to cleanup PVC %v/%v and backing PV, needs manual cleanup", c.bk8s.podInstanceNamespace, pvc)
			continue
		}
		if bindingUID := pvcObj.Labels[nvcastorage.ModelCacheBindingUIDLabelKey]; bindingUID != "" {
			log.Infof("skipping periodic cleanup of PVC %v/%v owned by model cache binding %v",
				c.bk8s.podInstanceNamespace, pvc, bindingUID)
			continue
		}
		pvName := pvcObj.Spec.VolumeName
		if pvName != "" {
			retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				// Retrieve the latest version of PV before attempting update
				// RetryOnConflict uses exponential backoff to avoid exhausting the apiserver
				pvObj, err := c.clients.K8s.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
				if err != nil {
					return fmt.Errorf("failed to get PV %v to update with PersistentVolumeReclaimPolicy:Delete: %v", pvName, err)
				}

				// update policy
				pvObj.Spec.PersistentVolumeReclaimPolicy = v1.PersistentVolumeReclaimDelete

				_, updateErr := c.clients.K8s.CoreV1().PersistentVolumes().Update(ctx, pvObj, metav1.UpdateOptions{})
				return updateErr
			})
			if retryErr != nil {
				return fmt.Errorf("failed to update PV %v with PersistentVolumeReclaimPolicy:Delete: %v", pvName, retryErr)
			}

			// now purge the PVC
			err = c.clients.K8s.CoreV1().PersistentVolumeClaims(c.bk8s.podInstanceNamespace).Delete(ctx, pvc, metav1.DeleteOptions{})
			if err != nil && !errors.IsNotFound(err) {
				return fmt.Errorf("failed to delete ROPVC, err: %v", err)
			}
		}
	}
	return nil
}
func regularModelCacheErrorIsRetryable(err error) bool {
	if err == nil || nvcaerrors.IsTerminal(err) {
		return false
	}
	return nvcak8sutil.IsTransientK8sError(err)
}

func regularModelCacheErrorIsKubernetesAPI(err error) bool {
	var apiStatus errors.APIStatus
	return stderrors.As(err, &apiStatus)
}

func regularModelCacheResultForError(
	bindingScoped bool,
	err error,
) (ModelCachingState, string, error) {
	if !bindingScoped {
		return ModelCachingFailed, "", nil
	}
	if err == nil {
		err = fmt.Errorf("regular model cache operation failed without an error")
	}
	if nvcaerrors.IsTerminal(err) {
		return ModelCachingFailed, "", err
	}
	if regularModelCacheErrorIsRetryable(err) ||
		regularModelCacheErrorIsKubernetesAPI(err) {
		return ModelCachingInProgress, "", err
	}
	return ModelCachingFailed, "", nvcaerrors.TerminalError(err)
}

func modelCacheDeleteOptions(
	obj metav1.Object,
	propagationPolicy *metav1.DeletionPropagation,
) (metav1.DeleteOptions, error) {
	options := metav1.DeleteOptions{PropagationPolicy: propagationPolicy}
	if obj == nil || reflect.ValueOf(obj).IsNil() {
		return options, fmt.Errorf("binding-owned model cache delete target is nil")
	}
	if obj.GetUID() == "" || obj.GetResourceVersion() == "" {
		return options, fmt.Errorf(
			"binding-owned model cache object %s/%s has incomplete delete identity (UID %q, resourceVersion %q)",
			obj.GetNamespace(), obj.GetName(), obj.GetUID(), obj.GetResourceVersion())
	}
	uid := obj.GetUID()
	resourceVersion := obj.GetResourceVersion()
	options.Preconditions = &metav1.Preconditions{
		UID:             &uid,
		ResourceVersion: &resourceVersion,
	}
	return options, nil
}

func validateRegularModelCachePVClaimForSetup(
	pv *v1.PersistentVolume,
	binding *nvcav2beta1.ModelCacheBinding,
	pvc *v1.PersistentVolumeClaim,
	namespace string,
	rwPVCName string,
	roPVCName string,
) error {
	if pv == nil || pv.Spec.ClaimRef == nil {
		return fmt.Errorf("regular model cache PV has no claimRef")
	}
	claimRef := pv.Spec.ClaimRef
	if pvc != nil {
		return validateRegularModelCachePVForPVC(binding, pvc, pv,
			[]v1.PersistentVolumeAccessMode{v1.ReadWriteOnce})
	}
	if claimRef.Namespace != namespace || (claimRef.Name != rwPVCName && claimRef.Name != roPVCName) {
		return fmt.Errorf("PV %s claimRef does not match binding-owned PVC %s/%s or %s",
			pv.Name, namespace, rwPVCName, roPVCName)
	}
	expectedModes := []v1.PersistentVolumeAccessMode{v1.ReadWriteOnce}
	if claimRef.Name == roPVCName {
		expectedModes = ROAccessMode
	}
	return validateRegularModelCachePVIdentity(binding, pv, expectedModes)
}

func (c K8sComputeBackend) CleanupModelCachingResources(ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
	rwPVC *v1.PersistentVolumeClaim,
	initJobName string,
) error {
	log := core.GetLogger(ctx)
	if rwPVC == nil {
		return fmt.Errorf("regular model cache cleanup PVC is nil")
	}

	binding, cleanupSharedResources, err := c.regularModelCacheCleanupBinding(ctx, req)
	if err != nil {
		return fmt.Errorf("resolve regular model cache binding for cleanup: %w", err)
	}
	if !cleanupSharedResources {
		return nil
	}
	if binding == nil {
		// Preserve the annotation-free legacy ordering and best-effort Job cleanup.
		backgroundDeletion := metav1.DeletePropagationBackground
		err = c.clients.K8s.BatchV1().Jobs(c.bk8s.podInstanceNamespace).Delete(
			ctx, initJobName, metav1.DeleteOptions{PropagationPolicy: &backgroundDeletion})
		if err != nil && !errors.IsNotFound(err) {
			log.WithError(err).Warnf("failed to cleanup initCacheJob %v/%v, needs manual cleanup",
				c.bk8s.podInstanceNamespace, initJobName)
		}
	}

	roPVCName, err := regularModelCacheReaderPVCName(rwPVC.Name)
	if err != nil {
		return err
	}
	namespace := c.bk8s.podInstanceNamespace
	var targets *regularModelCacheCleanupTargets
	if binding != nil {
		targets, err = c.validateRegularModelCacheCleanupTargets(ctx, binding, rwPVC.Name, initJobName)
		if err != nil {
			return fmt.Errorf("validate regular model cache cleanup targets: %w", err)
		}
		namespace = targets.namespace
	}

	var pvcObj *v1.PersistentVolumeClaim
	if binding != nil {
		pvcObj = targets.roPVC
		if pvcObj == nil {
			pvcObj = targets.rwPVC
		}
	} else {
		pvcObj, err = c.clients.K8s.CoreV1().PersistentVolumeClaims(namespace).
			Get(ctx, roPVCName, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			log.Debugf("ROPVC was never setup, try obtaining the RWPVC")
			pvcObj, err = c.clients.K8s.CoreV1().PersistentVolumeClaims(namespace).
				Get(ctx, rwPVC.Name, metav1.GetOptions{})
		}
		if err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("failed to get ROPVC, err: %v", err)
		}
	}

	var pvName string
	if pvcObj != nil {
		pvName = pvcObj.Spec.VolumeName
	}
	// For binding-scoped cleanup, prove PV ownership before the first write.
	if binding != nil && pvName != "" {
		pvObj, getErr := c.clients.K8s.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
		if getErr != nil {
			return fmt.Errorf("get regular model cache PV %v before cleanup: %w", pvName, getErr)
		}
		if err := validateRegularModelCacheCleanupPV(binding, pvcObj, pvObj); err != nil {
			return fmt.Errorf("validate regular model cache PV before cleanup: %w", err)
		}
	}

	// cleanup InitJob & its pods only after all binding-owned targets have been validated.
	if binding != nil && targets.initJob != nil {
		backgroundDeletion := metav1.DeletePropagationBackground
		deleteOptions, optionsErr := modelCacheDeleteOptions(targets.initJob, &backgroundDeletion)
		if optionsErr != nil {
			return fmt.Errorf("build binding-owned init Job delete preconditions: %w", optionsErr)
		}
		err = c.clients.K8s.BatchV1().Jobs(namespace).
			Delete(ctx, targets.initJob.Name, deleteOptions)
		if err != nil && !errors.IsNotFound(err) {
			log.WithError(err).Warnf("failed to cleanup initCacheJob %v/%v, needs manual cleanup",
				namespace, initJobName)
			return fmt.Errorf("delete binding-owned init cache Job: %w", err)
		}
	}

	if pvcObj == nil {
		log.Warnf("RWPVC and ROPVC were never setup, no PV or PVC to cleanup")
		return nil
	}

	if pvName != "" {
		retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Retrieve the latest version of PV before attempting update
			// RetryOnConflict uses exponential backoff to avoid exhausting the apiserver
			pvObj, err := c.clients.K8s.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("failed to get PV %v to update with PersistentVolumeReclaimPolicy:Delete: %w", pvName, err)
			}
			if binding != nil {
				if err := validateRegularModelCacheCleanupPV(binding, pvcObj, pvObj); err != nil {
					return err
				}
			}

			// update policy
			pvObj.Spec.PersistentVolumeReclaimPolicy = v1.PersistentVolumeReclaimDelete

			_, updateErr := c.clients.K8s.CoreV1().PersistentVolumes().Update(ctx, pvObj, metav1.UpdateOptions{})
			return updateErr
		})
		if retryErr != nil {
			return fmt.Errorf("failed to update PV %v with PersistentVolumeReclaimPolicy:Delete: %w", pvName, retryErr)
		}
	} else {
		log.Errorf("unable to set PersistentVolumeReclaimPolicy because PV name is unknown")
	}

	pvcsToDelete := []struct {
		kind string
		name string
		obj  *v1.PersistentVolumeClaim
	}{
		{kind: "RWPVC", name: rwPVC.Name},
		{kind: "ROPVC", name: roPVCName},
	}
	if binding != nil {
		pvcsToDelete[0].obj = targets.rwPVC
		pvcsToDelete[1].obj = targets.roPVC
	}
	for _, target := range pvcsToDelete {
		if binding != nil && target.obj == nil {
			continue
		}
		deleteOptions := metav1.DeleteOptions{}
		if binding != nil {
			var optionsErr error
			deleteOptions, optionsErr = modelCacheDeleteOptions(target.obj, nil)
			if optionsErr != nil {
				return fmt.Errorf("build binding-owned %s delete preconditions: %w",
					target.kind, optionsErr)
			}
		}
		err = c.clients.K8s.CoreV1().PersistentVolumeClaims(namespace).
			Delete(ctx, target.name, deleteOptions)
		if err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("failed to delete %s: %w", target.kind, err)
		}
	}
	return nil
}

func getModelCacheK8sArtifacts(ctx context.Context, bdArt function.LaunchArtifact,
	jobArt function.LaunchArtifact, mf mutateFunc) (*v1.PersistentVolumeClaim, *batchv1.Job, error) {
	log := core.GetLogger(ctx)
	obj, err := nvcak8sutil.GetObjectFromEncodedString(bdArt.Specification, reflect.TypeOf(&v1.PersistentVolumeClaim{}))
	if err != nil {
		return nil, nil, fmt.Errorf("error while decoding YAML object: %v", err)
	}

	pvc := obj.(*v1.PersistentVolumeClaim)

	mf(pvc)

	obj, err = nvcak8sutil.GetObjectFromEncodedString(jobArt.Specification, reflect.TypeOf(&batchv1.Job{}))
	if err != nil {
		return nil, nil, fmt.Errorf("error while decoding YAML object: %v", err)
	}

	job := obj.(*batchv1.Job)

	mf(job)

	log.Debugf("caching artifacts are decoded successfully, PVC: %v/%v, Job: %v/%v", pvc.Namespace, pvc.Name, job.Namespace, job.Name)
	return pvc, job, nil
}

/*
Returns:
	PVCQueryError -> If API Server Call to Get PVC Errors
	PVCNotFound -> If ROPVC Is Not Found, Caller will need to SetupPVCForReaders
	PVCUpdateFailed -> ROPVCFound, but failed to update the OwnerReferences, Caller should re-attempt update
	PVCFoundUnBound -> ROPVCFound, OwnerReference Updated but PVC is Still Unbound, not usable
	PVCFoundBound -> ROPVCFound and Usable, Workers Can be created with this PVC Name for volume Name
*/

func (c K8sComputeBackend) CheckPVCState(
	ctx context.Context,
	roPVCName string,
) (PVCState, error) {
	return c.checkPVCState(ctx, roPVCName, "", false, false, nil)
}

func (c K8sComputeBackend) checkPVCState(
	ctx context.Context,
	roPVCName string,
	bindingUID k8stypes.UID,
	bindingScoped bool,
	bindingScopedReader bool,
	binding *nvcav2beta1.ModelCacheBinding,
) (PVCState, error) {
	log := core.GetLogger(ctx)
	roPVCObj, err := c.clients.K8s.CoreV1().
		PersistentVolumeClaims(c.bk8s.podInstanceNamespace).
		Get(ctx, roPVCName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			log.Debugf("PVC %v/%v doesn't exist", c.bk8s.podInstanceNamespace, roPVCName)
			return PVCNotFound, nil
		}
		log.WithError(err).Errorf(
			"failed to query for ROPVC %v/%v", c.bk8s.podInstanceNamespace, roPVCName)
		return PVCQueryError, err
	}
	if bindingScoped {
		if err := requireRegularModelCacheBindingUID(roPVCObj, bindingUID); err != nil {
			return PVCFoundBindFailed, err
		}
	}
	if bindingScoped && bindingScopedReader {
		if !reflect.DeepEqual(roPVCObj.Spec.AccessModes, ROAccessMode) {
			return PVCFoundBindFailed, fmt.Errorf(
				"binding-owned reader PVC %s/%s does not use ReadOnlyMany",
				roPVCObj.Namespace, roPVCObj.Name)
		}
		expectedStorageClass, expectedErr := regularModelCacheExpectedStorageClassName(binding)
		if expectedErr != nil {
			return PVCFoundBindFailed, expectedErr
		}
		if roPVCObj.Spec.StorageClassName == nil || *roPVCObj.Spec.StorageClassName != expectedStorageClass {
			return PVCFoundBindFailed, fmt.Errorf(
				"binding-owned reader PVC %s/%s StorageClass does not match %q",
				roPVCObj.Namespace, roPVCObj.Name, expectedStorageClass)
		}
		// A pending reader has not yet acquired its claim UID in the PV. Validate
		// the complete PVC-to-PV ownership chain before declaring it Bound.
		if isPVCBound(roPVCObj) {
			if roPVCObj.Spec.VolumeName == "" {
				return PVCFoundBindFailed, fmt.Errorf(
					"binding-owned reader PVC %s/%s has no bound PV",
					roPVCObj.Namespace, roPVCObj.Name)
			}
			pvObj, pvGetErr := c.clients.K8s.CoreV1().PersistentVolumes().
				Get(ctx, roPVCObj.Spec.VolumeName, metav1.GetOptions{})
			if pvGetErr != nil {
				return PVCQueryError, fmt.Errorf(
					"get binding-owned reader PV %s: %w", roPVCObj.Spec.VolumeName, pvGetErr)
			}
			if err := validateRegularModelCachePVForPVC(binding, roPVCObj, pvObj, ROAccessMode); err != nil {
				return PVCFoundBindFailed, err
			}
		}
	} else if !bindingScoped && reflect.DeepEqual(roPVCObj.Spec.AccessModes, ROAccessMode) {
		pvObj, pvGetErr := c.clients.K8s.CoreV1().PersistentVolumes().
			Get(ctx, roPVCObj.Spec.VolumeName, metav1.GetOptions{})
		if pvGetErr != nil && !errors.IsNotFound(pvGetErr) {
			return PVCQueryError, fmt.Errorf(
				"failed to get PV %v to check volume attachment status: %w",
				roPVCObj.Spec.VolumeName, pvGetErr)
		}
		if pvObj != nil && pvObj.Spec.PersistentVolumeReclaimPolicy == v1.PersistentVolumeReclaimDelete {
			// Preserve the annotation-free legacy dangling-PVC behavior.
			err = c.clients.K8s.CoreV1().
				PersistentVolumeClaims(c.bk8s.podInstanceNamespace).
				Delete(ctx, roPVCName, metav1.DeleteOptions{})
			if err != nil && errors.IsNotFound(err) {
				return PVCFoundBindFailed,
					fmt.Errorf("failed to delete dangling ROPVC %v, modelcaching setup failed", roPVCObj.Name)
			}
			return PVCNotFound, nil
		}
	}

	// reference already added return, job should also exist
	if isPVCBound(roPVCObj) {
		return PVCFoundBound, nil
	}

	ps, err := c.handleLostPVC(ctx, roPVCObj)
	if ps != PVCNoState {
		return ps, err
	}

	if time.Since(roPVCObj.ObjectMeta.CreationTimestamp.Time) >
		c.bk8s.k8sTimeConfig.ModelCacheROPVCBindTimeGracePeriod {
		return PVCFoundBindFailed,
			fmt.Errorf("pvc %v didn't bind within %v",
				roPVCName, c.bk8s.k8sTimeConfig.ModelCacheROPVCBindTimeGracePeriod)
	}
	log.Warnf("PVC %v is still unbound, continue to wait, phase: %v",
		roPVCName, roPVCObj.Status.Phase)
	return PVCFoundUnBound, nil
}

// Volume binding race conditions due to nvmesh & Kata,
// could cause PVC transition to Phase: Lost
// have it rebind (if enabled) by unsetting the annotations
func (c K8sComputeBackend) handleLostPVC(ctx context.Context, roPVCObj *v1.PersistentVolumeClaim) (PVCState, error) {
	log := core.GetLogger(ctx)
	if roPVCObj.Status.Phase == v1.ClaimLost {
		if !c.bk8s.pvcRebindEnabled {
			return PVCFoundBindFailed, fmt.Errorf("pvc %v is lost, failing bind as rebind attempt not enabled", roPVCObj.Name)
		}
		if _, ok := roPVCObj.ObjectMeta.Annotations[types.NVCARebindAttemptedAnnotationKey]; !ok {
			retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				// Retrieve the latest version of PVC before attempting update
				// RetryOnConflict uses exponential backoff to avoid exhausting the apiserver
				roPVCObjNewLocal, err := c.clients.K8s.CoreV1().PersistentVolumeClaims(roPVCObj.Namespace).Get(ctx,
					roPVCObj.Name, metav1.GetOptions{})
				if err != nil {
					return fmt.Errorf("failed to get PVC %v to update with RebindRequestedAnnotation: %w", roPVCObj.Name, err)
				}

				// add NVCARebindAttemptedAnnotationKey
				if roPVCObjNewLocal.Annotations == nil {
					roPVCObjNewLocal.Annotations = make(map[string]string)
				}
				roPVCObjNewLocal.Annotations[types.NVCARebindAttemptedAnnotationKey] = strconv.FormatBool(true)
				// delete bind-completed annotation
				delete(roPVCObjNewLocal.Annotations, types.PVCBindCompletedAnnotationKey)

				_, updateErr := c.clients.K8s.CoreV1().PersistentVolumeClaims(roPVCObj.Namespace).Update(ctx,
					roPVCObjNewLocal, metav1.UpdateOptions{})
				return updateErr
			})
			if retryErr != nil {
				return PVCFoundBindFailed,
					fmt.Errorf("failed to update PVC %v with RebindRequestedAnnotation: %w", roPVCObj.Name, retryErr)
			}
		} else {
			return PVCFoundBindFailed, fmt.Errorf("pvc %v lost again, with rebind-request", roPVCObj.Name)
		}
		log.Warnf("PVC %v is in Phase: %v, requested rebind, continue wait", roPVCObj.Name, roPVCObj.Status.Phase)
		return PVCFoundUnBound, nil
	}
	return PVCNoState, nil
}

// isPVCBound is mocked in tests
var isPVCBound = func(pvc *v1.PersistentVolumeClaim) bool {
	return pvc.Status.Phase == v1.ClaimBound
}

func (c K8sComputeBackend) waitForVolumeDetach(ctx context.Context, volumeName string) error {
	log := core.GetLogger(ctx)
	attachedInRwMode := false
	log.Debugf("Checking attachment status of: %v", volumeName)
	now := time.Now()
	timeout := time.After(c.bk8s.k8sTimeConfig.ModelCacheVolumeDetachmentTimeout)
	for {
		select {
		case <-time.After(100 * time.Millisecond):
			// Perform your polling logic here
			attachedInRwMode = false
			attachments, err := c.clients.K8s.StorageV1().VolumeAttachments().List(ctx, metav1.ListOptions{})
			if err != nil {
				log.Errorf("VolumeAttachments.List() failed: %v", err)
				return err
			}

			for _, attachment := range attachments.Items {
				if attachment.Spec.Source.PersistentVolumeName != nil &&
					strings.Compare(*attachment.Spec.Source.PersistentVolumeName, volumeName) == 0 {
					pvObj, err := c.clients.K8s.CoreV1().PersistentVolumes().Get(ctx, volumeName, metav1.GetOptions{})
					if err != nil {
						return fmt.Errorf("failed to get PV %v to check volume attachment status: %w", volumeName, err)
					}
					if len(pvObj.Spec.AccessModes) == 1 && pvObj.Spec.AccessModes[0] == v1.ReadOnlyMany {
						attachedInRwMode = false
					} else {
						log.Debugf("volume %v still attached. retrying after 100ms", volumeName)
						attachedInRwMode = true
					}
					break
				}
			}
			if !attachedInRwMode {
				// not attached in rwMode.
				log.Debugf("volume %v not attached.", volumeName)
				return nil
			}
		case t := <-timeout:
			//timed out
			return fmt.Errorf("volume %v still attached after %g seconds", volumeName, t.Sub(now).Seconds())
		}
	}
}

// This function will setup the PVC as follows
/*
1. Get the PV Name from the LaunchArtifact.CacheHanle-rw-pvc in bdArt.Specification
2. Validate the spec.volumeName exists for this PVC
3. Get the PV Object and update it as follows:
   1. Change the /spec/claimRef/name -> LaunchArtifact.CacheHanle-ro-pvc
   2. Remove the /spec/claimRef/resourceVersion
   3. Remove the /spec/claimRef/uid
   4. Change the /spec/accessModes -> ReadOnlyMany
   5. Set the /spec/mountOptions ->  ["ro","norecovery","nouuid"]
4. Update the PV Object
5. Once Updated, create a new PVC from bdArt.Specification updating the following
   1. Name -> $LaunchSpecification.CacheHandle-ro-pvc
   2. /spec/accessModes -> ReadOnlyMany
*/
func (c K8sComputeBackend) SetupPVCForReaders(ctx context.Context,
	rwPVC *v1.PersistentVolumeClaim, initJobName string, req *nvcav2beta1.ICMSRequest, mf mutateFunc) (ROPVCSetupPhase, error) {
	log := core.GetLogger(ctx)
	roPVCName, err := regularModelCacheReaderPVCName(rwPVC.Name)
	if err != nil {
		return ROPVCSetupFailed, err
	}
	bindingUID, bindingScoped, err := regularModelCacheBindingUID(req)
	if err != nil {
		return ROPVCSetupFailed, fmt.Errorf("resolve regular model cache binding ownership: %w", err)
	}
	var transitionTargets *regularModelCacheCleanupTargets
	if bindingScoped {
		transitionTargets, _, err = c.regularModelCacheTransitionTargets(
			ctx, req, rwPVC.Name, initJobName)
		if err != nil {
			phase := ROPVCSetupFailed
			if regularModelCacheErrorIsRetryable(err) {
				phase = ROPVCSetupQueryFailed
			}
			return phase, fmt.Errorf("validate regular model cache transition targets: %w", err)
		}
	}
	var pvName string
	var pvObj *v1.PersistentVolume

	pvcCur, pvcGetErr := c.clients.K8s.CoreV1().PersistentVolumeClaims(c.bk8s.podInstanceNamespace).
		Get(ctx, rwPVC.Name, metav1.GetOptions{})
	if errors.IsNotFound(pvcGetErr) {
		pvcCur = nil
	} else if pvcGetErr != nil {
		if bindingScoped {
			return ROPVCSetupQueryFailed, fmt.Errorf("get binding-owned writer PVC %s/%s: %w",
				c.bk8s.podInstanceNamespace, rwPVC.Name, pvcGetErr)
		}
		pvcCur = nil
	}
	if pvcCur == nil {
		// this would mean the RWPVC has been successfully purged,
		// lets find the underlying PV for this function/task
		pvLabelSel, err := makePVLabelSelectorForCacheRequest(req)
		if err != nil {
			log.WithError(err).Error("failed to create label requirement for cache PV, resort to non-caching")
			return ROPVCSetupFailed, nil
		}
		pvObjList, err := c.clients.K8s.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{
			LabelSelector: pvLabelSel})
		if err != nil && !errors.IsNotFound(err) {
			return ROPVCSetupQueryFailed,
				fmt.Errorf("failed to query PV list for selector %s: %w", pvLabelSel, err)
		}
		if len(pvObjList.Items) > 1 {
			return ROPVCSetupQueryFailed, fmt.Errorf("found %v PVs for functionVersionId", len(pvObjList.Items))
		}
		if len(pvObjList.Items) == 1 {
			pvName = pvObjList.Items[0].Name
		}
	} else {
		if bindingScoped {
			if err := validateRegularModelCachePVC(pvcCur, rwPVC, transitionTargets.binding, false); err != nil {
				return ROPVCSetupFailed, err
			}
		}
		pvName = pvcCur.Spec.VolumeName
	}

	// in the event List returns empty
	if pvName == "" {
		return ROPVCSetupQueryFailed, fmt.Errorf("failed to get Bound PV %v in SetupPVCForReaders", pvName)
	}

	pvObj, err = c.clients.K8s.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
	if err != nil {
		return ROPVCSetupQueryFailed,
			fmt.Errorf("failed to get PV %v in SetupPVCForReaders: %w", pvName, err)
	}
	if bindingScoped {
		gotBindingUID := pvObj.Labels[nvcastorage.ModelCacheBindingUIDLabelKey]
		if pvcCur == nil && gotBindingUID != string(bindingUID) {
			return ROPVCSetupFailed, fmt.Errorf(
				"writer PVC is absent and PV %s has binding UID %q, want %q",
				pvObj.Name, gotBindingUID, bindingUID)
		}
		if gotBindingUID != "" && gotBindingUID != string(bindingUID) {
			return ROPVCSetupFailed, fmt.Errorf(
				"PV %s belongs to model cache binding UID %q, not %q",
				pvObj.Name, gotBindingUID, bindingUID)
		}
		if err := validateRegularModelCachePVClaimForSetup(
			pvObj, transitionTargets.binding, pvcCur, c.bk8s.podInstanceNamespace, rwPVC.Name, roPVCName); err != nil {
			return ROPVCSetupFailed, err
		}
	}

	// update the PV with an identifying label.
	var labelKey, labelVal string
	if req.Spec.FunctionDetails.FunctionVersionID != "" {
		labelKey, labelVal = fnVersionIDLabelString, req.Spec.FunctionDetails.FunctionVersionID
	} else {
		labelKey, labelVal = taskIDLabelString, req.Spec.TaskDetails.TaskID
	}
	_, hasRequestLabel := pvObj.Labels[labelKey]
	hasBindingLabel := !bindingScoped ||
		pvObj.Labels[nvcastorage.ModelCacheBindingUIDLabelKey] == string(bindingUID)
	if !hasRequestLabel || !hasBindingLabel {
		retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Retrieve the latest version of PV before attempting update
			// RetryOnConflict uses exponential backoff to avoid exhausting the apiserver
			pvObj, err := c.clients.K8s.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf(
					"failed to get PV %v to update with PersistentVolumeReclaimPolicy:Delete: %w",
					pvName, err)
			}

			if pvObj.Labels == nil {
				pvObj.Labels = make(map[string]string)
			}
			if bindingScoped {
				gotBindingUID := pvObj.Labels[nvcastorage.ModelCacheBindingUIDLabelKey]
				if pvcCur == nil && gotBindingUID != string(bindingUID) {
					return fmt.Errorf(
						"writer PVC is absent and PV %s has binding UID %q, want %q",
						pvObj.Name, gotBindingUID, bindingUID)
				}
				if gotBindingUID != "" && gotBindingUID != string(bindingUID) {
					return fmt.Errorf(
						"PV %s belongs to model cache binding UID %q, not %q",
						pvObj.Name, gotBindingUID, bindingUID)
				}
				if err := validateRegularModelCachePVClaimForSetup(
					pvObj, transitionTargets.binding, pvcCur, c.bk8s.podInstanceNamespace, rwPVC.Name, roPVCName); err != nil {
					return err
				}
				pvObj.Labels[nvcastorage.ModelCacheBindingUIDLabelKey] = string(bindingUID)
			}
			pvObj.Labels[labelKey] = labelVal

			_, updateErr := c.clients.K8s.CoreV1().PersistentVolumes().Update(ctx, pvObj, metav1.UpdateOptions{})
			return updateErr
		})
		if retryErr != nil {
			return ROPVUpdateFailed,
				fmt.Errorf("failed to update PV for ReadOnlyPVC binding: %w", retryErr)
		}
	}

	// cleanup initJob & its pods
	jobNamespace := c.bk8s.podInstanceNamespace
	jobToDelete := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: initJobName}}
	jobDeleteOptions := metav1.DeleteOptions{}
	if bindingScoped {
		jobNamespace = transitionTargets.namespace
		jobToDelete = transitionTargets.initJob
	}
	if jobToDelete != nil {
		backgroundDeletion := metav1.DeletePropagationBackground
		jobDeleteOptions.PropagationPolicy = &backgroundDeletion
		if bindingScoped {
			jobDeleteOptions, err = modelCacheDeleteOptions(jobToDelete, &backgroundDeletion)
			if err != nil {
				return ROPVCSetupFailed,
					fmt.Errorf("build binding-owned init Job delete preconditions: %w", err)
			}
		}
		err = c.clients.K8s.BatchV1().Jobs(jobNamespace).
			Delete(ctx, jobToDelete.Name, jobDeleteOptions)
		if err != nil && !errors.IsNotFound(err) {
			log.WithError(err).Warnf("failed to cleanup initCacheJob %v/%v, needs manual cleanup",
				jobNamespace, jobToDelete.Name)
			if bindingScoped {
				return ROPVCSetupFailed, fmt.Errorf("delete binding-owned init cache Job: %w", err)
			}
		}
	}

	// wait for volumeDetach if fails, skip caching
	if !skipVolumeDetachCheck {
		err = c.waitForVolumeDetach(ctx, pvName)
		if err != nil {
			return ROPVCSetupQueryFailed, err
		}
	}

	// cleanup RWPVC
	rwPVCToDelete := rwPVC
	rwPVCDeleteOptions := metav1.DeleteOptions{}
	if bindingScoped {
		rwPVCToDelete = transitionTargets.rwPVC
	}
	if rwPVCToDelete != nil {
		if bindingScoped {
			rwPVCDeleteOptions, err = modelCacheDeleteOptions(rwPVCToDelete, nil)
			if err != nil {
				return ROPVCSetupFailed,
					fmt.Errorf("build binding-owned writer PVC delete preconditions: %w", err)
			}
		}
		err = c.clients.K8s.CoreV1().PersistentVolumeClaims(c.bk8s.podInstanceNamespace).
			Delete(ctx, rwPVCToDelete.Name, rwPVCDeleteOptions)
		if err != nil && errors.IsNotFound(err) {
			log.Infof("RWPVC %v/%v cleaned-up, setup ROPVC",
				c.bk8s.podInstanceNamespace, rwPVCToDelete.Name)
		} else if err != nil && bindingScoped {
			return ROPVCSetupFailed, fmt.Errorf("delete binding-owned writer PVC: %w", err)
		}
	}

	if bindingScoped {
		pvObj, err = c.clients.K8s.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
		if err != nil {
			return ROPVCSetupQueryFailed, fmt.Errorf("get binding-owned PV %v after writer deletion: %w", pvName, err)
		}
		if err := c.validateRegularModelCachePVClaimAfterWriterDelete(
			ctx, pvObj, pvcCur, transitionTargets.binding, c.bk8s.podInstanceNamespace, rwPVC.Name, roPVCName); err != nil {
			return ROPVCSetupFailed, err
		}
	}

	// if the ClaimRef was already Updated to the ROPVCName, skip update
	if pvObj.Spec.ClaimRef.Name != roPVCName {
		retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Retrieve the latest version of PV before attempting update
			// RetryOnConflict uses exponential backoff to avoid exhausting the apiserver
			pvObj, err := c.clients.K8s.CoreV1().PersistentVolumes().Get(ctx, pvObj.Name, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf(
					"failed to get PV %v to update with PersistentVolumeReclaimPolicy:Delete: %w",
					pvName, err)
			}
			if bindingScoped {
				if err := c.validateRegularModelCachePVClaimAfterWriterDelete(
					ctx, pvObj, pvcCur, transitionTargets.binding, c.bk8s.podInstanceNamespace, rwPVC.Name, roPVCName); err != nil {
					return err
				}
			}
			var newPVCRef v1.ObjectReference
			// prepare PV for ReadOnly Mode
			// Copy the current claimRef
			pvObj.Spec.ClaimRef.DeepCopyInto(&newPVCRef)

			newPVCRef.UID = ""
			newPVCRef.ResourceVersion = ""
			newPVCRef.Name = roPVCName

			// set the new PVCRef
			pvObj.Spec.ClaimRef = &newPVCRef
			pvObj.Spec.AccessModes = ROAccessMode
			pvObj.Spec.MountOptions = c.bk8s.csiVolumeMountOptions

			_, updateErr := c.clients.K8s.CoreV1().PersistentVolumes().Update(ctx, pvObj, metav1.UpdateOptions{})
			return updateErr
		})
		if retryErr != nil {
			return ROPVUpdateFailed,
				fmt.Errorf("failed to update PV for ReadOnlyPVC binding: %w", retryErr)
		}
	}

	// now that the PV is modified as for ReadOnlyMany, create a new PVC using the rwPVC
	// with only the following modifications
	// Name -> roPVCName
	// AccessMode -> ReadOnlyMany
	rwPVC.Name = roPVCName
	rwPVC.Spec.AccessModes = ROAccessMode

	mf(rwPVC)

	createdReader, err := c.clients.K8s.CoreV1().PersistentVolumeClaims(c.bk8s.podInstanceNamespace).
		Create(ctx, rwPVC, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) && bindingScoped {
		createdReader, err = c.clients.K8s.CoreV1().PersistentVolumeClaims(c.bk8s.podInstanceNamespace).
			Get(ctx, roPVCName, metav1.GetOptions{})
	}
	if err != nil && !errors.IsAlreadyExists(err) {
		return ROPVCSetupFailed, fmt.Errorf("failed to create ROPVC for readers: %w", err)
	}
	if bindingScoped {
		if err := validateRegularModelCachePVC(createdReader, rwPVC, transitionTargets.binding, true); err != nil {
			return ROPVCSetupFailed, err
		}
	}
	return ROPVCSetupCompleted, nil
}

func (c K8sComputeBackend) CheckInitCacheJobState(
	ctx context.Context,
	rwPVCName string,
	job *batchv1.Job,
	bindingUID k8stypes.UID,
	bindingScoped bool,
) (InitCacheJobState, error) {
	log := core.GetLogger(ctx)

	jS, err := c.clients.K8s.BatchV1().Jobs(c.bk8s.podInstanceNamespace).
		Get(ctx, job.Name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			log.WithField("job", job.Name).Errorf(
				"initCacheJob not found, it may have just been created, but it should be running")
			return InitCacheJobNotFound, nil
		}
		log.WithError(err).Errorf(
			"failed to query the initCacheJob %v/%v", job.Namespace, job.Name)
		return InitCacheJobNotFound, err
	}
	if bindingScoped {
		if err := requireRegularModelCacheBindingUID(jS, bindingUID); err != nil {
			return InitCacheJobFailed, err
		}
	}

	if jS.Status.CompletionTime != nil && jS.Status.Succeeded > 0 {
		log.Infof("init job %v/%v completed at %v",
			jS.Namespace, jS.Name, jS.Status.CompletionTime.ToUnstructured())
		return InitCacheJobCompleted, nil
	}

	// check the RWPVC state
	rwPVCState, err := c.checkPVCState(ctx, rwPVCName, bindingUID, bindingScoped, false, nil)
	switch rwPVCState {
	case PVCFoundBound:
		// no action
	case PVCFoundUnBound:
		log.WithError(err).Debugf("rwpvc %v is unbound", rwPVCName)
	case PVCQueryError:
		log.WithError(err).Errorf("transient failure to query the %v", rwPVCName)
		if bindingScoped {
			return InitCacheJobInProgress, err
		}
	case PVCNotFound, PVCFoundBindFailed:
		log.WithError(err).Errorf("rwpvc %v bind failed, caching will be skipped", rwPVCName)
		if bindingScoped && err != nil {
			return InitCacheJobFailed, err
		}
		return InitCacheJobFailed, nil
	}

	// Use the job's configured backoff limit, defaulting to K8s default of 6
	var backoffLimit int32 = 6
	if jS.Spec.BackoffLimit != nil {
		backoffLimit = *jS.Spec.BackoffLimit
	}
	if jS.Status.Failed > backoffLimit ||
		(jS.Status.Active != 0 &&
			time.Since(jS.ObjectMeta.CreationTimestamp.Time) >=
				c.bk8s.k8sTimeConfig.InitCacheJobFailureThreshold) {
		if jS.Status.Failed > backoffLimit {
			log.WithError(err).Errorf(
				"initCache job %v/%v has failed more than backoff limit (%d)",
				jS.Namespace, jS.Name, backoffLimit)
		} else {
			log.WithError(err).Errorf(
				"initCache job %v/%v has not completed within %v duration since launch",
				jS.Namespace, jS.Name, c.bk8s.k8sTimeConfig.InitCacheJobFailureThreshold)
		}
		return InitCacheJobFailed, nil
	}

	log.Debugf("init cache job is still running")
	return InitCacheJobInProgress, nil
}

// getInitCacheJobFailureReason returns the failure reason for a failed init cache job.
// This mirrors the logic in CheckInitCacheJobState to determine why the job failed.
func (c K8sComputeBackend) getInitCacheJobFailureReason(ctx context.Context, job *batchv1.Job) string {
	jS, err := c.clients.K8s.BatchV1().Jobs(c.bk8s.podInstanceNamespace).Get(ctx, job.Name, metav1.GetOptions{})
	if err != nil {
		return modelcachetypes.ReasonJobNotFound
	}
	// Use the job's configured backoff limit, defaulting to K8s default of 6
	var backoffLimit int32 = 6
	if jS.Spec.BackoffLimit != nil {
		backoffLimit = *jS.Spec.BackoffLimit
	}
	if jS.Status.Failed > backoffLimit {
		return modelcachetypes.ReasonJobBackoffExceeded
	}
	return modelcachetypes.ReasonJobTimeout
}

func (c K8sComputeBackend) SetupInitCacheJobBlockDevice(ctx context.Context,
	rwPVCObj *v1.PersistentVolumeClaim, initJob *batchv1.Job,
	req *nvcav2beta1.ICMSRequest) error {
	log := core.GetLogger(ctx)
	var pvcCur *v1.PersistentVolumeClaim
	var err error
	var binding *nvcav2beta1.ModelCacheBinding

	log.Debug("SetupInitCacheJobBlockDevice for ModelCaching")
	bindingUID, bindingScoped, err := regularModelCacheBindingUID(req)
	if err != nil {
		return fmt.Errorf("resolve regular model cache binding ownership: %w", err)
	}
	if bindingScoped {
		for _, obj := range []metav1.Object{rwPVCObj, initJob, &initJob.Spec.Template.ObjectMeta} {
			if err := requireRegularModelCacheBindingUID(obj, bindingUID); err != nil {
				return err
			}
		}
	}
	if bindingScoped {
		binding, err = c.bk8s.activeModelCacheBindingForRuntime(ctx, req)
		if err != nil {
			return err
		}
		if err := validateRegularModelCachePVC(rwPVCObj, rwPVCObj, binding, false); err != nil {
			return nvcaerrors.TerminalError(err)
		}
	}

	pvcs := c.clients.K8s.CoreV1().PersistentVolumeClaims(c.bk8s.podInstanceNamespace)
	if bindingScoped {
		pvcCur, err = pvcs.Get(ctx, rwPVCObj.Name, metav1.GetOptions{})
		switch {
		case err == nil:
			if err := validateRegularModelCachePVC(pvcCur, rwPVCObj, binding, false); err != nil {
				return err
			}
		case errors.IsNotFound(err):
			selection, selectionErr := persistedRegularModelCacheSelection(req)
			if selectionErr != nil {
				return nvcaerrors.TerminalError(selectionErr)
			}
			if selection == nil || selection.Mode != nvcastorage.ModelCacheSelectionDurable {
				return nvcaerrors.TerminalError(fmt.Errorf(
					"binding-scoped writer PVC creation requires a durable persisted selection"))
			}
			if !selection.EncryptionRequired {
				if rwPVCObj.Spec.StorageClassName == nil ||
					*rwPVCObj.Spec.StorageClassName != selection.StorageClassName {
					return nvcaerrors.TerminalError(fmt.Errorf(
						"writer PVC StorageClass does not match persisted selection %q",
						selection.StorageClassName))
				}
				if validationErr := nvcastorage.ValidateModelCacheStorageSelectionLiveWithClientset(
					ctx, c.clients.K8s, selection); validationErr != nil {
					wrapped := fmt.Errorf(
						"validate persisted StorageClass before writer PVC creation: %w", validationErr)
					if stderrors.Is(validationErr, nvcastorage.ErrModelCacheStorageSelectionDrift) ||
						errors.IsNotFound(validationErr) {
						return nvcaerrors.TerminalError(wrapped)
					}
					return wrapped
				}
			}
			pvcCur, err = pvcs.Create(ctx, rwPVCObj, metav1.CreateOptions{})
			if errors.IsAlreadyExists(err) {
				pvcCur, err = pvcs.Get(ctx, rwPVCObj.Name, metav1.GetOptions{})
			}
			if err != nil {
				return fmt.Errorf("create binding-owned PVC %s/%s: %w",
					c.bk8s.podInstanceNamespace, rwPVCObj.Name, err)
			}
			if err := validateRegularModelCachePVC(pvcCur, rwPVCObj, binding, false); err != nil {
				return err
			}
		default:
			return fmt.Errorf("get binding-owned PVC %s/%s: %w",
				c.bk8s.podInstanceNamespace, rwPVCObj.Name, err)
		}
	} else {
		pvcCur, err = pvcs.Create(ctx, rwPVCObj, metav1.CreateOptions{})
		if errors.IsAlreadyExists(err) {
			pvcCur, err = pvcs.Get(ctx, rwPVCObj.Name, metav1.GetOptions{})
		}
		if err != nil {
			return fmt.Errorf("failed to create PVC %s/%s from artifact: %v",
				c.bk8s.podInstanceNamespace, rwPVCObj.Name, err)
		}
	}

	if bindingScoped {
		if err := bindRegularModelCacheWriterJobToPVC(initJob, pvcCur, binding); err != nil {
			return nvcaerrors.TerminalError(err)
		}
		if err := validateRegularModelCacheJob(initJob, initJob, binding); err != nil {
			return nvcaerrors.TerminalError(err)
		}
		if err := c.revalidateRegularModelCacheBindingAfterCreate(
			ctx, req, rwPVCObj, initJob.Name, "writer PVC creation or adoption"); err != nil {
			return err
		}
	}
	if bindingScoped && binding != nil &&
		binding.Spec.Decision.Transition == nvcastorage.ModelCacheTransitionRWXReadOnly {
		if err := c.validatePersistedStorageClassBeforeRWXWriterJob(ctx, req); err != nil {
			return err
		}
	}
	log.Debugf("Created PVC %v/%v", c.bk8s.podInstanceNamespace, rwPVCObj.Name)
	// ICMS request gets purged
	if pvcCur != nil {
		_, err := c.clients.K8s.BatchV1().Jobs(c.bk8s.podInstanceNamespace).Create(ctx, initJob, metav1.CreateOptions{})
		if errors.IsAlreadyExists(err) && bindingScoped {
			existing, getErr := c.clients.K8s.BatchV1().Jobs(c.bk8s.podInstanceNamespace).
				Get(ctx, initJob.Name, metav1.GetOptions{})
			if getErr != nil {
				return fmt.Errorf("get existing Job %s/%s: %w",
					c.bk8s.podInstanceNamespace, initJob.Name, getErr)
			}
			if err := validateRegularModelCacheJob(existing, initJob, binding); err != nil {
				return err
			}
		} else if err != nil && !errors.IsAlreadyExists(err) {
			// the job need not be created again if another ICMS request references it
			return fmt.Errorf("failed to create Job %s/%s from artifact: %w", c.bk8s.podInstanceNamespace, initJob.Name, err)
		}
		log.Debugf("Created Job %v/%v", c.bk8s.podInstanceNamespace, initJob.Name)
		if bindingScoped {
			if err := c.revalidateRegularModelCacheBindingAfterCreate(
				ctx, req, rwPVCObj, initJob.Name, "writer Job creation or adoption"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c K8sComputeBackend) validatePersistedStorageClassBeforeRWXWriterJob(
	ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
) error {
	selection, err := persistedRegularModelCacheSelection(req)
	if err != nil {
		return nvcaerrors.TerminalError(err)
	}
	if selection == nil || selection.Mode != nvcastorage.ModelCacheSelectionDurable ||
		selection.Transition != nvcastorage.ModelCacheTransitionRWXReadOnly {
		return nvcaerrors.TerminalError(fmt.Errorf(
			"rwxReadOnly writer Job requires a durable persisted rwxReadOnly selection"))
	}
	if selection.EncryptionRequired {
		return nvcaerrors.TerminalError(fmt.Errorf(
			"rwxReadOnly writer Job selection cannot require NVMesh encryption"))
	}
	if err := nvcastorage.ValidateModelCacheStorageSelectionLiveWithClientset(
		ctx, c.clients.K8s, selection); err != nil {
		wrapped := fmt.Errorf("validate persisted StorageClass before rwxReadOnly writer Job creation: %w", err)
		if stderrors.Is(err, nvcastorage.ErrModelCacheStorageSelectionDrift) || errors.IsNotFound(err) {
			return nvcaerrors.TerminalError(wrapped)
		}
		return wrapped
	}
	return nil
}

func (c K8sComputeBackend) revalidateRegularModelCacheBindingAfterCreate(
	ctx context.Context,
	req *nvcav2beta1.ICMSRequest,
	rwPVC *v1.PersistentVolumeClaim,
	initJobName string,
	operation string,
) error {
	err := c.bk8s.validateModelCacheBindingForRuntime(ctx, req)
	if err == nil {
		return nil
	}
	if !stderrors.Is(err, errRegularModelCacheBindingRetiring) &&
		!stderrors.Is(err, errRegularModelCacheBindingReferenceReleased) {
		return fmt.Errorf("revalidate model cache binding after %s: %w", operation, err)
	}
	if cleanupErr := c.CleanupModelCachingResources(
		ctx, req, rwPVC, initJobName); cleanupErr != nil {
		return fmt.Errorf("revalidate model cache binding after %s: %w; compensating cleanup failed: %v",
			operation, err, cleanupErr)
	}
	return fmt.Errorf("revalidate model cache binding after %s: %w", operation, err)
}

var (
	fnVersionIDLabelString = fmt.Sprintf("%s/%s", nvcav1new.SchemeGroupVersion.Group, types.FunctionVersionIDKey)
	taskIDLabelString      = fmt.Sprintf("%s/%s", nvcav1new.SchemeGroupVersion.Group, types.TaskIDKey)
)

func makePVLabelSelectorForCacheRequest(req *nvcav2beta1.ICMSRequest) (string, error) {
	var vals []string
	var key string
	bindingUID, bindingScoped, err := regularModelCacheBindingUID(req)
	if err != nil {
		return "", err
	}
	if bindingScoped {
		key = nvcastorage.ModelCacheBindingUIDLabelKey
		vals = []string{string(bindingUID)}
	} else if req.Spec.FunctionDetails.FunctionVersionID != "" {
		key = fnVersionIDLabelString
		vals = []string{req.Spec.FunctionDetails.FunctionVersionID}
	} else {
		key = taskIDLabelString
		vals = []string{req.Spec.TaskDetails.TaskID}
	}
	labelReq, err := labels.NewRequirement(key, selection.Equals, vals)
	if err != nil {
		return "", err
	}
	return labels.NewSelector().Add(*labelReq).String(), nil
}
