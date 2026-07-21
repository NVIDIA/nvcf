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
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	modelcachetypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics/modelcachetypes"
	nvcak8sutil "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/encryption"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// skip option only for UT, real cluster detach check is must
var (
	skipVolumeDetachCheck = false
	ROAccessMode          = []v1.PersistentVolumeAccessMode{v1.ReadOnlyMany}
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

	if !c.bk8s.cachingSupportEnabled || isMiniServiceType || icjDecoded.Specification == "" {
		return nil
	}

	mf := func(obj client.Object) {}

	rwPVC, initJob, err := getModelCacheK8sArtifacts(ctx, bdDecode, icjDecoded, mf)
	if err != nil {
		log.WithError(err).Error("failed getModelCacheK8sArtifacts, model caching will be disabled")
		return fmt.Errorf("failed to cleanup in-flight cache job: %w", err)
	}

	// cleanup InitJob & its pods, this will clear the rw-pvc also
	backgroundDeletion := metav1.DeletePropagationBackground
	err = c.clients.K8s.BatchV1().Jobs(c.bk8s.podInstanceNamespace).Delete(ctx, initJob.Name, metav1.DeleteOptions{
		PropagationPolicy: &backgroundDeletion,
	})
	if err != nil && !errors.IsNotFound(err) {
		log.WithError(err).Warnf("failed to cleanup initCacheJob %v/%v in SetupPVCForReaders, needs manual cleanup",
			c.bk8s.podInstanceNamespace, initJob.Name)
		return fmt.Errorf("failed to cleanup in-flight cache job: %w", err)
	}

	// now purge the RWPVC
	err = c.clients.K8s.CoreV1().PersistentVolumeClaims(c.bk8s.podInstanceNamespace).Delete(ctx, rwPVC.Name, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete pvc %v, err: %v", rwPVC.Name, err)
	}
	return nil
}

func (c K8sComputeBackend) SetupModelCachingForRequest(ctx context.Context,
	rwPVC *v1.PersistentVolumeClaim,
	initJob *batchv1.Job,
	req *nvcav2beta1.ICMSRequest,
	mf mutateFunc,
) (ModelCachingState, string) {
	c.bk8s.modelCacheMtx.Lock()
	defer c.bk8s.modelCacheMtx.Unlock()

	log := core.GetLogger(ctx)
	log.Debugf("decoding caching artifacts")
	metrics := nvcametrics.FromContext(ctx)

	if c.bk8s.nvmeshEncryptionEnabled {
		//If encryption is used, then we need to update StorageClass name in PVC.
		storageClassName, err := encryption.SetupEncryption(ctx, c.clients, req.Spec.NCAId, req.Namespace)
		if err != nil {
			log.WithError(err).Error("failed to set up cache encryption, resort to non-caching")
			return ModelCachingFailed, ""
		}

		*rwPVC.Spec.StorageClassName = storageClassName
	}

	roPVCName := strings.ReplaceAll(rwPVC.Name, RWPVCSuffix, ROPVCSuffix)
	roPVCState, err := c.CheckPVCState(ctx, roPVCName)
	switch roPVCState {
	case PVCNotFound:
		jS := c.CheckInitCacheJobState(ctx, rwPVC.Name, initJob)
		switch jS {
		case InitCacheJobNotFound:
			pvLabelSel, err := makePVLabelSelectorForCacheRequest(req)
			if err != nil {
				log.WithError(err).Error("failed to create label requirement for cache PV, resort to non-caching")
				return ModelCachingFailed, ""
			}
			// check if PV for the function exists, if so, continue as ModelCachingInProgress
			// lets find the underlying PV for this function/task
			pvObjList, err := c.clients.K8s.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{
				LabelSelector: pvLabelSel})
			if (err != nil && errors.IsNotFound(err)) || (pvObjList != nil && len(pvObjList.Items) == 0) {
				err = c.SetupInitCacheJobBlockDevice(ctx, rwPVC, initJob, req)
				if err != nil {
					c.bk8s.eventRecorder.Event(req, v1.EventTypeWarning,
						string(types.EventCategoryModelCaching), "failed caching setup, resort to non-caching")
					log.WithError(err).Error("failed SetupInitCacheJobBlockDevice, model caching will be disabled")
					return ModelCachingFailed, ""
				}
				return ModelCachingInProgress, ""
			} else if pvObjList != nil && len(pvObjList.Items) == 1 {
				// let it reconcile again
				mc := ModelCachingInProgress
				roPVCState, err := c.SetupPVCForReaders(ctx, rwPVC, initJob.Name, req, mf)
				if err != nil {
					log.WithError(err).Errorf("failed to SetupPVCForReaders at %v, model caching will be disabled", roPVCState)
					err = c.CleanupModelCachingResources(ctx, rwPVC, initJob.Name)
					if err != nil {
						log.WithError(err).Error("failed to cleanup ModelCaching resources, needs manual cleanup")
					}
					c.bk8s.eventRecorder.Event(req, v1.EventTypeWarning,
						string(types.EventCategoryModelCaching), "failed pvc setup, resort to non-caching")
					metrics.EventErrorTotal.WithLabelValues(metrics.WithDefaultLabelValues(EventModelCachingFailed)...).Inc()
					mc = ModelCachingFailed
				}
				return mc, ""
			}
		case InitCacheJobFailed:
			// this is an irrecoverable error on InitCacheJob, NVCA will switch to
			// No Caching Workflow
			// Caller will need to use the PodSpec without ROPVC VolumeMount
			err = c.CleanupModelCachingResources(ctx, rwPVC, initJob.Name)
			if err != nil {
				log.WithError(err).Error("failed to cleanup ModelCaching resources, needs manual cleanup")
			}
			c.bk8s.eventRecorder.Eventf(req, v1.EventTypeWarning,
				string(types.EventCategoryModelCaching), "%v failed, resort to non-caching", initJob.Name)
			reason := c.getInitCacheJobFailureReason(ctx, initJob)
			metrics.RecordModelCacheResult(modelcachetypes.ResultFailure, reason, string(types.HelmCacheBackendNVMesh))
			return ModelCachingFailed, ""
		case InitCacheJobCompleted:
			mc := ModelCachingInProgress
			roPVCState, err := c.SetupPVCForReaders(ctx, rwPVC, initJob.Name, req, mf)
			if err != nil {
				log.WithError(err).Errorf("failed to SetupPVCForReaders at %v, model caching will be disabled", roPVCState)
				err = c.CleanupModelCachingResources(ctx, rwPVC, initJob.Name)
				if err != nil {
					log.WithError(err).Error("failed to cleanup ModelCaching resources, needs manual cleanup")
				}
				c.bk8s.eventRecorder.Event(req, v1.EventTypeWarning,
					string(types.EventCategoryModelCaching), "failed pvc setup, resort to non-caching")
				metrics.EventErrorTotal.WithLabelValues(metrics.WithDefaultLabelValues(EventModelCachingFailed)...).Inc()
				metrics.RecordModelCacheResult(modelcachetypes.ResultFailure, modelcachetypes.ReasonPVCSetupFailed, string(types.HelmCacheBackendNVMesh))
				mc = ModelCachingFailed
			}
			return mc, ""
		case InitCacheJobInProgress:
			return ModelCachingInProgress, ""
		}
	case PVCQueryError:
		log.WithError(err).Error("failed to query ROPVC")
		// this is a transient error, will reattempt
		return ModelCachingInProgress, ""
	case PVCFoundUnBound:
		// if it has been more than 2 mins since PVC was created
		// Clear RWPVC, ROPVC, InitCacheJob and Disable Model Caching on the Request
		log.Debugf("ROPVC is still unbound, continue wait")
		return ModelCachingInProgress, ""
	case PVCFoundBindFailed:
		log.WithError(err).Errorf("ROPVC is not getting bound, cleanup Modelcaching resource and deploy without caching")
		err := c.CleanupModelCachingResources(ctx, rwPVC, initJob.Name)
		if err != nil {
			// TODO: Perform Deeper Cleanup on reconciliation
			log.WithError(err).Errorf("failed to cleanup ModelCaching resources, needs manual cleanup")
		}
		c.bk8s.eventRecorder.Eventf(req, v1.EventTypeWarning,
			string(types.EventCategoryModelCaching), "%v bind failed, resort to non-caching", roPVCName)
		metrics.EventErrorTotal.WithLabelValues(metrics.WithDefaultLabelValues(EventPVCModelCachingError)...).Inc()
		metrics.EventErrorTotal.WithLabelValues(metrics.WithDefaultLabelValues(EventModelCachingFailed)...).Inc()
		metrics.RecordModelCacheResult(modelcachetypes.ResultFailure, modelcachetypes.ReasonPVCBindFailed, string(types.HelmCacheBackendNVMesh))
		return ModelCachingFailed, ""
	case PVCFoundBound:
		log.Infof("ROPVC %v setup completed, Modelcaching will be enabled for request %v/%v", roPVCName, req.Namespace, req.Name)
		// cleanup InitJob & its pods
		// rw-pvc is deleted in setup of ro-pvc
		backgroundDeletion := metav1.DeletePropagationBackground
		err = c.clients.K8s.BatchV1().Jobs(c.bk8s.podInstanceNamespace).Delete(ctx, initJob.Name, metav1.DeleteOptions{
			PropagationPolicy: &backgroundDeletion,
		})
		if err != nil && !errors.IsNotFound(err) {
			log.WithError(err).Warnf("failed to cleanup initCacheJob %v/%v, needs manual cleanup",
				c.bk8s.podInstanceNamespace, initJob.Name)
		}
		metrics.EventErrorTotal.WithLabelValues(metrics.WithDefaultLabelValues(EventModelCachingSuccess)...).Inc()
		metrics.RecordModelCacheResult(modelcachetypes.ResultSuccess, "", string(types.HelmCacheBackendNVMesh))
		return ModelCachingCompleted, roPVCName
	}
	// Never reached
	return ModelCachingFailed, ""
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

func (c K8sComputeBackend) CleanupModelCachingResources(ctx context.Context,
	rwPVC *v1.PersistentVolumeClaim, initJobName string) error {
	log := core.GetLogger(ctx)
	var pvcObj *v1.PersistentVolumeClaim
	var err error

	// cleanup InitJob & its pods
	backgroundDeletion := metav1.DeletePropagationBackground
	err = c.clients.K8s.BatchV1().Jobs(c.bk8s.podInstanceNamespace).Delete(ctx, initJobName, metav1.DeleteOptions{
		PropagationPolicy: &backgroundDeletion,
	})
	if err != nil && !errors.IsNotFound(err) {
		log.WithError(err).Warnf("failed to cleanup initCacheJob %v/%v in SetupPVCForReaders, needs manual cleanup",
			c.bk8s.podInstanceNamespace, initJobName)
	}

	// ROPVC
	roPVCName := strings.ReplaceAll(rwPVC.Name, RWPVCSuffix, ROPVCSuffix)
	// Get the BackedPV Object
	pvcObj, err = c.clients.K8s.CoreV1().PersistentVolumeClaims(c.bk8s.podInstanceNamespace).Get(ctx, roPVCName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			log.Debugf("ROPVC was never setup, try obtaining the RWPVC")
			pvcObj, err = c.clients.K8s.CoreV1().PersistentVolumeClaims(c.bk8s.podInstanceNamespace).Get(ctx, rwPVC.Name, metav1.GetOptions{})
			if err != nil {
				if errors.IsNotFound(err) {
					log.Warnf("RWPVC was also never setup, no PV to update and no PVCs to cleanup")
					return nil
				}
				return fmt.Errorf("failed to get ROPVC, err: %v", err)
			}
		} else {
			return fmt.Errorf("failed to get ROPVC, err: %v", err)
		}
	}

	// get the pvName
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
			return fmt.Errorf("failed to update PV %v with PersistentVolumeReclaimPolicy:Delete, err: %v", pvName, err)
		}
	} else {
		log.WithError(err).Errorf("unabled to set PersistentVolumeReclaimPolicy because PV name is unknown")
	}

	// deleting the RWPVC
	err = c.clients.K8s.CoreV1().PersistentVolumeClaims(c.bk8s.podInstanceNamespace).Delete(ctx, rwPVC.Name, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete RWPVC, err:%v", err)
	}

	// delete the ROPVC
	err = c.clients.K8s.CoreV1().PersistentVolumeClaims(c.bk8s.podInstanceNamespace).Delete(ctx, roPVCName, metav1.DeleteOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete ROPVC, err:%v", err)
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

func (c K8sComputeBackend) CheckPVCState(ctx context.Context, roPVCName string) (PVCState, error) {
	log := core.GetLogger(ctx)
	roPVCObj, err := c.clients.K8s.CoreV1().PersistentVolumeClaims(c.bk8s.podInstanceNamespace).Get(ctx, roPVCName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			log.Debugf("PVC %v/%v doesn't exist", c.bk8s.podInstanceNamespace, roPVCName)
			return PVCNotFound, nil
		}
		log.WithError(err).Errorf("failed to query for ROPVC %v/%v", c.bk8s.podInstanceNamespace, roPVCName)
		return PVCQueryError, err
	}
	log.Debugf("PVC %v/%v exists", c.bk8s.podInstanceNamespace, roPVCName)
	if reflect.DeepEqual(roPVCObj.Spec.AccessModes, ROAccessMode) {
		pvObj, err := c.clients.K8s.CoreV1().PersistentVolumes().Get(ctx, roPVCObj.Spec.VolumeName, metav1.GetOptions{})
		if err != nil && !errors.IsNotFound(err) {
			return PVCQueryError, fmt.Errorf("failed to get PV %v to check volume attachment status: %v", roPVCObj.Spec.VolumeName, err)
		}
		if pvObj != nil && pvObj.Spec.PersistentVolumeReclaimPolicy == v1.PersistentVolumeReclaimDelete {
			err = c.clients.K8s.CoreV1().PersistentVolumeClaims(c.bk8s.podInstanceNamespace).Delete(ctx, roPVCName, metav1.DeleteOptions{})
			if err != nil && errors.IsNotFound(err) {
				// error out PVCBind to resort to non-cache
				return PVCFoundBindFailed, fmt.Errorf("failed to delete dangling ROPVC %v, modelcaching setup failed", roPVCObj.Name)
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

	if time.Since(roPVCObj.ObjectMeta.CreationTimestamp.Time) > c.bk8s.k8sTimeConfig.ModelCacheROPVCBindTimeGracePeriod {
		return PVCFoundBindFailed,
			fmt.Errorf("pvc %v didn't bind within %v", roPVCName, c.bk8s.k8sTimeConfig.ModelCacheROPVCBindTimeGracePeriod)
	}
	log.Warnf("PVC %v is still unbound, continue to wait, phase: %v", roPVCName, roPVCObj.Status.Phase)
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
					return fmt.Errorf("failed to get PVC %v to update with RebindRequestedAnnotation: %v", roPVCObj.Name, err)
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
					fmt.Errorf("failed to update PVC %v with RebindRequestedAnnotation, err: %v", roPVCObj.Name, retryErr)
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
						return fmt.Errorf("failed to get PV %v to check volume attachment status: %v", volumeName, err)
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
	roPVCName := strings.ReplaceAll(rwPVC.Name, RWPVCSuffix, ROPVCSuffix)
	var pvName string
	var pvObj *v1.PersistentVolume
	var err error

	pvcCur, _ := c.clients.K8s.CoreV1().PersistentVolumeClaims(c.bk8s.podInstanceNamespace).Get(ctx, rwPVC.Name, metav1.GetOptions{})
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
			return ROPVCSetupQueryFailed, fmt.Errorf("failed to query PV list for selector %s", pvLabelSel)
		}
		if len(pvObjList.Items) > 1 {
			return ROPVCSetupQueryFailed, fmt.Errorf("found %v PVs for functionVersionId", len(pvObjList.Items))
		}
		if len(pvObjList.Items) == 1 {
			pvName = pvObjList.Items[0].Name
		}
	} else {
		pvName = pvcCur.Spec.VolumeName
	}

	// in the event List returns empty
	if pvName == "" {
		return ROPVCSetupQueryFailed, fmt.Errorf("failed to get Bound PV %v in SetupPVCForReaders", pvName)
	}

	pvObj, err = c.clients.K8s.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
	if err != nil {
		return ROPVCSetupQueryFailed, fmt.Errorf("failed to get PV %v in SetupPVCForReaders", pvName)
	}

	// update the PV with an identifying label.
	var labelKey, labelVal string
	if req.Spec.FunctionDetails.FunctionVersionID != "" {
		labelKey, labelVal = fnVersionIDLabelString, req.Spec.FunctionDetails.FunctionVersionID
	} else {
		labelKey, labelVal = taskIDLabelString, req.Spec.TaskDetails.TaskID
	}
	if _, ok := pvObj.Labels[labelKey]; !ok {
		retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Retrieve the latest version of PV before attempting update
			// RetryOnConflict uses exponential backoff to avoid exhausting the apiserver
			pvObj, err := c.clients.K8s.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("failed to get PV %v to update with PersistentVolumeReclaimPolicy:Delete: %v", pvName, err)
			}

			if pvObj.Labels == nil {
				pvObj.Labels = make(map[string]string)
			}
			pvObj.Labels[labelKey] = labelVal

			_, updateErr := c.clients.K8s.CoreV1().PersistentVolumes().Update(ctx, pvObj, metav1.UpdateOptions{})
			return updateErr
		})
		if retryErr != nil {
			return ROPVUpdateFailed, fmt.Errorf("failed to update PV for ReadOnlyPVC binding: err: %v", err)
		}
	}

	// cleanup initJob & its pods
	backgroundDeletion := metav1.DeletePropagationBackground
	err = c.clients.K8s.BatchV1().Jobs(c.bk8s.podInstanceNamespace).Delete(ctx, initJobName, metav1.DeleteOptions{
		PropagationPolicy: &backgroundDeletion,
	})
	if err != nil && !errors.IsNotFound(err) {
		log.WithError(err).Warnf("failed to cleanup initCacheJob %v/%v, needs manual cleanup",
			c.bk8s.podInstanceNamespace, initJobName)
	}

	// wait for volumeDetach if fails, skip caching
	if !skipVolumeDetachCheck {
		err = c.waitForVolumeDetach(ctx, pvName)
		if err != nil {
			return ROPVCSetupQueryFailed, err
		}
	}

	// cleanup RWPVC
	err = c.clients.K8s.CoreV1().PersistentVolumeClaims(c.bk8s.podInstanceNamespace).Delete(ctx, rwPVC.Name, metav1.DeleteOptions{})
	if err != nil && errors.IsNotFound(err) {
		log.Infof("RWPVC %v/%v cleaned-up, setup ROPVC", c.bk8s.podInstanceNamespace, rwPVC.Name)
	}

	// if the ClaimRef was already Updated to the ROPVCName, skip update
	if pvObj.Spec.ClaimRef.Name != roPVCName {
		retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Retrieve the latest version of PV before attempting update
			// RetryOnConflict uses exponential backoff to avoid exhausting the apiserver
			pvObj, err := c.clients.K8s.CoreV1().PersistentVolumes().Get(ctx, pvObj.Name, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("failed to get PV %v to update with PersistentVolumeReclaimPolicy:Delete: %v", pvName, err)
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
			return ROPVUpdateFailed, fmt.Errorf("failed to update PV for ReadOnlyPVC binding: err: %v", err)
		}
	}

	// now that the PV is modified as for ReadOnlyMany, create a new PVC using the rwPVC
	// with only the following modifications
	// Name -> roPVCName
	// AccessMode -> ReadOnlyMany
	rwPVC.Name = roPVCName
	rwPVC.Spec.AccessModes = ROAccessMode

	mf(rwPVC)

	_, err = c.clients.K8s.CoreV1().PersistentVolumeClaims(c.bk8s.podInstanceNamespace).Create(ctx, rwPVC, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return ROPVCSetupFailed, fmt.Errorf("failed to create ROPVC for readers, %v", err)
	}
	return ROPVCSetupCompleted, nil
}

func (c K8sComputeBackend) CheckInitCacheJobState(ctx context.Context, rwPVCName string, job *batchv1.Job) InitCacheJobState {
	log := core.GetLogger(ctx)

	jS, err := c.clients.K8s.BatchV1().Jobs(c.bk8s.podInstanceNamespace).Get(ctx, job.Name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			log.WithField("job", job.Name).Errorf("initCacheJob not found, it may have just been created, but it should be running")
		} else {
			log.WithError(err).Errorf("failed to query the initCacheJob %v/%v", job.Namespace, job.Name)
		}
		return InitCacheJobNotFound
	}

	if jS.Status.CompletionTime != nil && jS.Status.Succeeded > 0 {
		log.Infof("init job %v/%v completed at %v", jS.Namespace, jS.Name, jS.Status.CompletionTime.ToUnstructured())
		return InitCacheJobCompleted
	}

	// check the RWPVC state
	rwPVCState, err := c.CheckPVCState(ctx, rwPVCName)
	switch rwPVCState {
	case PVCFoundBound:
		// no action
	case PVCFoundUnBound:
		log.WithError(err).Debugf("rwpvc %v is unbound", rwPVCName)
	case PVCQueryError:
		log.WithError(err).Errorf("transient failure to query the %v", rwPVCName)
	case PVCNotFound, PVCFoundBindFailed:
		log.WithError(err).Errorf("rwpvc %v bind failed, caching will be skipped", rwPVCName)
		return InitCacheJobFailed
	}

	// Use the job's configured backoff limit, defaulting to K8s default of 6
	var backoffLimit int32 = 6
	if jS.Spec.BackoffLimit != nil {
		backoffLimit = *jS.Spec.BackoffLimit
	}
	if jS.Status.Failed > backoffLimit ||
		(jS.Status.Active != 0 &&
			time.Since(jS.ObjectMeta.CreationTimestamp.Time) >= c.bk8s.k8sTimeConfig.InitCacheJobFailureThreshold) {
		if jS.Status.Failed > backoffLimit {
			log.WithError(err).Errorf("initCache job %v/%v has failed more than backoff limit (%d)",
				jS.Namespace, jS.Name, backoffLimit)
		} else {
			log.WithError(err).Errorf("initCache job %v/%v has not completed within %v duration since launch",
				jS.Namespace, jS.Name, c.bk8s.k8sTimeConfig.InitCacheJobFailureThreshold)
		}
		return InitCacheJobFailed
	}

	log.Debugf("init cache job is still running")
	return InitCacheJobInProgress
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
	_ *nvcav2beta1.ICMSRequest) error {
	log := core.GetLogger(ctx)
	var pvcCur *v1.PersistentVolumeClaim
	var err error

	log.Debug("SetupInitCacheJobBlockDevice for ModelCaching")

	pvcCur, err = c.clients.K8s.CoreV1().PersistentVolumeClaims(c.bk8s.podInstanceNamespace).Create(ctx, rwPVCObj, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create PVC %s/%s from artifact: %v", c.bk8s.podInstanceNamespace, rwPVCObj.Name, err)
	}

	log.Debugf("Created PVC %v/%v", c.bk8s.podInstanceNamespace, rwPVCObj.Name)
	// ICMS request gets purged
	if pvcCur != nil {
		_, err := c.clients.K8s.BatchV1().Jobs(c.bk8s.podInstanceNamespace).Create(ctx, initJob, metav1.CreateOptions{})
		if err != nil && !errors.IsAlreadyExists(err) {
			// the job need not be created again if another ICMS request references it
			return fmt.Errorf("failed to create Job %s/%s from artifact: %v", c.bk8s.podInstanceNamespace, rwPVCObj.Name, err)
		}
		log.Debugf("Created Job %v/%v", c.bk8s.podInstanceNamespace, initJob.Name)
	}
	return nil
}

var (
	fnVersionIDLabelString = fmt.Sprintf("%s/%s", nvcav1new.SchemeGroupVersion.Group, types.FunctionVersionIDKey)
	taskIDLabelString      = fmt.Sprintf("%s/%s", nvcav1new.SchemeGroupVersion.Group, types.TaskIDKey)
)

func makePVLabelSelectorForCacheRequest(req *nvcav2beta1.ICMSRequest) (string, error) {
	var vals []string
	var key string
	if req.Spec.FunctionDetails.FunctionVersionID != "" {
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
