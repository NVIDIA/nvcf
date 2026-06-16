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
	"strconv"
	"strings"

	translatecommon "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nvcak8sutil "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcav1new "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce/kaischeduler"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	SMBServerPodName       = "nvcf-smb-server"
	SMBServerContainerName = "smb-server"
	//nolint
	readOnlySecretName = "nvcf-smb-ro-creds"
	//nolint
	readWriteSecretName = "nvcf-smb-rw-creds"

	readWriteUsername = "nvcf-smbuser-rw"
	readWriteUID      = uint16(1000)
	readWriteDirMode  = "0700"
	readWriteFileMode = "0600"
	readOnlyUsername  = "nvcf-smbuser-ro"
	readOnlyUID       = uint16(1001)
	readOnlyDirMode   = "0555"
	readOnlyFileMode  = "0444"
	smbGID            = uint16(1000)
)

//nolint:gocyclo
func (r *Reconciler) doSharedStorageSMB(ctx context.Context,
	st, stCopy *nvcav1new.StorageRequest,
) (reconcile.Result, error) {
	log := logf.FromContext(ctx)
	log.V(1).Info("Shared storage request reconciliation started", "phase", st.Status.Phase)

	var rerr error
	smbPod, err := newSMBPod(st, r.cfg)
	if err != nil {
		log.Error(err, "failed to initialize smb server pod spec")
		rerr = reconcile.TerminalError(fmt.Errorf("failed to initialize smb server pod spec: %w", err))
		stCopy.Status.Phase = nvcav1new.StorageFailed
		goto done
	}

	if r.fff.IsFeatureFlagEnabled(featureflag.KAIScheduler) {
		smbPod.Spec.SchedulerName = kaischeduler.SchedulerName
		labels := smbPod.GetLabels()
		if labels == nil {
			labels = make(map[string]string)
		}
		labels[kaischeduler.SchedulerQueueLabel] = kaischeduler.DefaultQueue
		smbPod.SetLabels(labels)
	}

	// Default names with backwards-compatibility field.
	if len(st.Spec.SharedStorage.WorkerPullSecretNames) == 0 && st.Spec.SharedStorage.WorkerPullSecretName != "" {
		st.Spec.SharedStorage.WorkerPullSecretNames = []string{st.Spec.SharedStorage.WorkerPullSecretName}
		// For updates.
		stCopy.Spec.SharedStorage.WorkerPullSecretNames = []string{st.Spec.SharedStorage.WorkerPullSecretName}
	}

	switch st.Status.Phase {
	case nvcav1new.StorageUnknown:
		if st.Spec.SharedStorage.SMBContainerImage == "" {
			log.Error(nil, "shared storage SMB container image is empty")
			rerr = reconcile.TerminalError(fmt.Errorf("smb container image is empty"))
			stCopy.Status.Phase = nvcav1new.StorageFailed
			goto done
		}
		if len(st.Spec.SharedStorage.WorkerPullSecretNames) == 0 {
			log.Error(nil, "shared storage worker image pull secret name is empty")
			rerr = reconcile.TerminalError(fmt.Errorf("smb worker pull secret name is empty"))
			stCopy.Status.Phase = nvcav1new.StorageFailed
			goto done
		}
		for _, secretName := range st.Spec.SharedStorage.WorkerPullSecretNames {
			secret := &corev1.Secret{}
			secret.Name, secret.Namespace = secretName, st.Namespace
			if err := r.Client.Get(ctx, client.ObjectKeyFromObject(secret), secret); k8serrors.IsNotFound(err) {
				log.WithValues("secret_name", secretName).
					Error(err, "shared storage image pull secret not found")
				rerr = reconcile.TerminalError(fmt.Errorf("smb pull secret not found"))
				stCopy.Status.Phase = nvcav1new.StorageFailed
				goto done
			}
		}

		// Verify the SMB CSI driver is installed and healthy
		csiDriver := &storagev1.CSIDriver{}
		csiDriver.Name = SMBCSIDriverName
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(csiDriver), csiDriver); err != nil {
			if k8serrors.IsNotFound(err) {
				err = fmtSMBCSIDriverNotFoundError(err)
				log.Error(err, "SMB CSI driver not found")
				rerr = reconcile.TerminalError(err)
				stCopy.Status.Phase = nvcav1new.StorageFailed
				meta.SetStatusCondition(&stCopy.Status.Conditions, metav1.Condition{
					Type:   ConditionTypeSMBCSIDriverInstalled,
					Status: metav1.ConditionFalse,
				})
				goto done
			}
			// Otherwise, requeue and retry
			return reconcile.Result{}, err
		}

		// CSI Driver is installed, set the condition to true
		meta.SetStatusCondition(&stCopy.Status.Conditions, metav1.Condition{
			Type:   ConditionTypeSMBCSIDriverInstalled,
			Status: metav1.ConditionTrue,
		})

		// Validation of the storage request was enough to mark this pending.
		stCopy.Status.Phase = nvcav1new.StoragePending
	case nvcav1new.StoragePending:
		var objsToCreate []client.Object

		configureSMBPod(smbPod, st)

		rwSecret, roSecret := newSMBSecrets()
		objsToCreate = append(objsToCreate, rwSecret, roSecret)

		// Create the PVC and PV if this is a task shared storage request
		if st.Spec.SharedStorage.TaskData != nil && st.Spec.SharedStorage.TaskData.StorageClassName != nil {
			pv, pvc := newSMBServerPVAndPVC(ctx, st.Namespace, st.Spec.SharedStorage.TaskData)
			objsToCreate = append(objsToCreate, pv, pvc)
		}

		objsToCreate = append(objsToCreate, smbPod)

		// Attempt to create resources one by one, skip if already exists
		for _, obj := range objsToCreate {
			if err := r.applyControlled(ctx, st, obj); err != nil && !k8serrors.IsAlreadyExists(err) {
				log.Error(err, "failed to create shared-storage object", "name", obj.GetName())
				// If the error is forbidden, fail
				if k8serrors.IsForbidden(err) || k8serrors.IsInvalid(err) {
					rerr = fmt.Errorf("failed to create shared-storage object %s: %w", obj.GetName(), err)
					stCopy.Status.Phase = nvcav1new.StorageFailed
					goto done
				}
				// Otherwise, requeue and retry
				return reconcile.Result{}, err
			}
		}

		// Move onto next phase
		stCopy.Status.Phase = nvcav1new.StorageInitRunning
	case nvcav1new.StorageInitRunning:
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(smbPod), smbPod); err != nil {
			if k8serrors.IsNotFound(err) {
				log.V(1).Info("nvcf-smb-server pod not found after creation, requeuing")
				return reconcile.Result{RequeueAfter: defaultRequeueDelay}, nil
			} else if nvcak8sutil.IsTransientK8sError(err) {
				log.V(1).Info("Transient error getting nvcf-smb-server pod, will retry", "error", err)
				return reconcile.Result{RequeueAfter: defaultRequeueDelay}, nil
			} else {
				log.Error(err, "Non-transient error getting nvcf-smb-server pod")
				return reconcile.Result{}, err
			}
		}
		switch smbPod.Status.Phase {
		case corev1.PodRunning:
			// Ensure containers ready status is true
			if !nvcak8sutil.IsPodReady(smbPod.Status) {
				return reconcile.Result{RequeueAfter: defaultRequeueDelay}, nil
			}
		case corev1.PodFailed, corev1.PodSucceeded:
			rerr = fmt.Errorf("pod %s stopped with status %v", smbPod.Name, smbPod.Status.Phase)
			log.Error(rerr, "pod did not start successfully")
			stCopy.Status.Phase = nvcav1new.StorageFailed
			goto done
		case corev1.PodPending, corev1.PodUnknown:
			return reconcile.Result{RequeueAfter: defaultRequeueDelay}, nil
		}

		// Create RW and RO PVs and StorageClass plus the definitions for PVCs
		sc := newStorageClass(st.Namespace)
		objsToCreate := []client.Object{sc}
		essStatus, essObjs := initSharedStoragePVs(
			SharedStorageSecretsReadWritePVCName,
			SharedStorageSecretsReadOnlyPVCName,
			SharedStorageVolumeSecretsSambaShare,
			sc.Name,
			st.Namespace,
			smbPod.Status.PodIP,
			st.Spec.SharedStorage.Size)
		objsToCreate = append(objsToCreate, essObjs...)
		knsStatus, knsObjs := initSharedStoragePVs(
			SharedStorageKNSReadWritePVCName,
			SharedStorageKNSReadOnlyPVCName,
			SharedStorageVolumeKNSSambaShare,
			sc.Name,
			st.Namespace,
			smbPod.Status.PodIP,
			st.Spec.SharedStorage.Size)
		objsToCreate = append(objsToCreate, knsObjs...)
		// If tasks are required add those PVCs
		var taskDataStatus *nvcav1new.SharedStorageTypeStatus
		if st.Spec.SharedStorage.TaskData != nil {
			status, taskDataObjs := initSharedStoragePVs(
				SharedStorageTaskDataReadWritePVCName,
				"",
				SharedStorageVolumeTaskDataSambaShare,
				sc.Name,
				st.Namespace,
				smbPod.Status.PodIP,
				st.Spec.SharedStorage.TaskData.Size)
			objsToCreate = append(objsToCreate, taskDataObjs...)
			taskDataStatus = &status
		}

		// Attempt to create resources one by one, skip if already exists
		for _, obj := range objsToCreate {
			if err := r.applyControlled(ctx, st, obj); err != nil && !k8serrors.IsAlreadyExists(err) {
				log.Error(err, "failed to create shared-storage object", "name", obj.GetName())
				// If the error is forbidden, fail
				if k8serrors.IsForbidden(err) || k8serrors.IsInvalid(err) {
					rerr = fmt.Errorf("failed to create shared-storage object %s: %w", obj.GetName(), err)
					stCopy.Status.Phase = nvcav1new.StorageFailed
					goto done
				}
				// Otherwise, requeue and retry
				return reconcile.Result{}, err
			}
		}

		// Save PVCs to fetch and check later
		stCopy.Status.SharedStorage = &nvcav1new.SharedStorageStatus{
			Secrets:  essStatus,
			KNS:      knsStatus,
			TaskData: taskDataStatus,
		}
		stCopy.Status.Phase = nvcav1new.StorageCreating
	case nvcav1new.StorageCreating:
		for _, phaseOp := range getAllStorageCreatingPhaseOps(st) {
			pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
				Name:      phaseOp.PVCName,
				Namespace: st.Namespace,
			}}
			if err := r.Client.Get(ctx, client.ObjectKeyFromObject(pvc), pvc); err != nil {
				// PVC only does not exist if it hasn't been created or K8s API is slow
				if k8serrors.IsNotFound(err) {
					// 1. Fetch the PV and check if it is available, if not requeue
					// 2. Create the PVC and bind it to the PV
					if phaseOp.CreatePVCIFNotExists {
						// Check if the PV exists and is available
						pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{
							Name: phaseOp.PVName,
						}}
						if err := r.Client.Get(ctx, client.ObjectKeyFromObject(pv), pv); err != nil {
							if k8serrors.IsNotFound(err) {
								log.V(1).Info("pv not found, requeueing", "pv", pv.Name)
								return reconcile.Result{RequeueAfter: defaultRequeueDelay}, nil
							} else if nvcak8sutil.IsTransientK8sError(err) {
								log.V(1).Info("Transient error getting PV, will retry", "pv", pv.Name, "error", err)
								return reconcile.Result{RequeueAfter: defaultRequeueDelay}, nil
							} else {
								log.Error(err, "Non-transient error getting PV", "pv", pv.Name)
								return reconcile.Result{}, err
							}
						}
						// ensure PV is in state Available then bind
						if pv.Status.Phase != corev1.VolumeAvailable {
							log.V(1).Info("pv not in Available state, requeueing", "pv", pv.Name)
							return reconcile.Result{RequeueAfter: defaultRequeueDelay}, nil
						}
						pvc = newPVC(phaseOp.PVCName,
							phaseOp.PVName,
							phaseOp.StorageClassName,
							phaseOp.AccessMode,
							phaseOp.Capacity)
						if err := r.applyControlled(ctx, st, pvc); err != nil && !k8serrors.IsAlreadyExists(err) {
							return reconcile.Result{}, err
						}
					}
					// Requeue to check if pvc is bound.
					return reconcile.Result{RequeueAfter: defaultRequeueDelay}, nil
				}
				if nvcak8sutil.IsTransientK8sError(err) {
					log.V(1).Info("Transient error getting PVC, will retry", "pvc", pvc.Name, "error", err)
					return reconcile.Result{RequeueAfter: defaultRequeueDelay}, nil
				}
				log.Error(err, "Non-transient error getting PVC", "pvc", pvc.Name)
				return reconcile.Result{}, err
			}
			// Reconcile again waiting for the claim to be bound
			if pvc.Status.Phase != corev1.ClaimBound {
				return reconcile.Result{RequeueAfter: defaultRequeueDelay}, nil
			}
		}

		// All PVCs are bound now move to storage ready state
		stCopy.Status.Phase = nvcav1new.StorageReady
	case nvcav1new.StorageReady:
		// Validate pod is still running if not mark as failed
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(smbPod), smbPod); err != nil {
			if k8serrors.IsNotFound(err) {
				rerr = err
				log.Error(rerr, "pod not found, marking as failed")
				stCopy.Status.Phase = nvcav1new.StorageFailed
				goto done
			}
			if nvcak8sutil.IsTransientK8sError(err) {
				log.V(1).Info("Transient error getting SMB server pod, will retry", "error", err)
				return reconcile.Result{RequeueAfter: defaultRequeueDelay}, nil
			}
			log.Error(err, "Non-transient error getting SMB server pod")
			return reconcile.Result{}, err
		}
		// If the pod is no longer ready, fail the storage request
		if !nvcak8sutil.IsPodReady(smbPod.Status) {
			rerr = fmt.Errorf("%s pod is not in a ready state any longer", smbPod.Name)
			log.Error(rerr, "pod is not in a ready state any longer, marking as failed")
			stCopy.Status.Phase = nvcav1new.StorageFailed
			goto done
		}
	case nvcav1new.StorageFailed:
	case nvcav1new.StorageRuntimeError:
	default:
		return reconcile.Result{}, reconcile.TerminalError(fmt.Errorf("unknown phase: %s", st.Status.Phase))
	}
done:

	// If this is the first instance of storage failing, mark it as such
	if st.Status.Phase != nvcav1new.StorageFailed && stCopy.Status.Phase == nvcav1new.StorageFailed {
		if _, err := doCleanupNamespaced(ctx, r.Client, stCopy); err != nil {
			log.Error(err, "failed to cleanup shared storage object on storage failure, needs manual cleanup")
		}
	}

	return reconcile.Result{}, rerr
}

type storageCreatingPhaseOp struct {
	CreatePVCIFNotExists bool

	PVCName          string
	PVName           string
	StorageClassName string
	AccessMode       corev1.PersistentVolumeAccessMode
	Capacity         resource.Quantity
}

func getAllStorageCreatingPhaseOps(st *nvcav1new.StorageRequest) []storageCreatingPhaseOp {
	var allStorageCreationOps []storageCreatingPhaseOp
	allStatuses := []nvcav1new.SharedStorageTypeStatus{st.Status.SharedStorage.Secrets, st.Status.SharedStorage.KNS}
	if st.Status.SharedStorage.TaskData != nil {
		allStatuses = append(allStatuses, *st.Status.SharedStorage.TaskData)
	}

	for _, status := range allStatuses {
		// Check for read only PVC
		if status.ReadOnlyPVCName != "" {
			op := storageCreatingPhaseOp{
				CreatePVCIFNotExists: status.CreatePVCIfNotExists,
				PVCName:              status.ReadOnlyPVCName,
				PVName:               status.ReadOnlyPVName,
				StorageClassName:     status.StorageClassName,
				AccessMode:           status.ReadOnlyAccessMode,
				Capacity:             status.StorageCapacity,
			}
			allStorageCreationOps = append(allStorageCreationOps, op)
		}

		// Check for read write PVC
		if status.ReadWritePVCName != "" {
			op := storageCreatingPhaseOp{
				CreatePVCIFNotExists: status.CreatePVCIfNotExists,
				PVCName:              status.ReadWritePVCName,
				PVName:               status.ReadWritePVName,
				StorageClassName:     status.StorageClassName,
				AccessMode:           status.ReadWriteAccessMode,
				Capacity:             status.StorageCapacity,
			}
			allStorageCreationOps = append(allStorageCreationOps, op)
		}
	}

	return allStorageCreationOps
}

func initSharedStoragePVs(
	rwPVCName,
	roPVCName,
	sambaVolumeShare,
	storageClassName,
	namespace,
	podIP string,
	capacity resource.Quantity,
) (nvcav1new.SharedStorageTypeStatus, []client.Object) {
	var createdObjects []client.Object
	status := nvcav1new.SharedStorageTypeStatus{
		StorageClassName:     storageClassName,
		StorageCapacity:      capacity,
		CreatePVCIfNotExists: true,
	}

	if rwPVCName != "" {
		rwPV := newPV(fmt.Sprintf("%s-%s", namespace, rwPVCName),
			storageClassName,
			sambaVolumeShare,
			podIP,
			readWriteSecretName,
			namespace,
			readWriteUID,
			smbGID,
			readWriteDirMode,
			readWriteFileMode,
			corev1.ReadWriteMany,
			capacity)
		createdObjects = append(createdObjects, rwPV)
		status.ReadWritePVName = rwPV.Name
		status.ReadWritePVCName = rwPVCName
		status.ReadWriteAccessMode = corev1.ReadWriteMany
	}

	if roPVCName != "" {
		roPV := newPV(fmt.Sprintf("%s-%s", namespace, roPVCName),
			storageClassName,
			sambaVolumeShare,
			podIP,
			readOnlySecretName,
			namespace,
			readOnlyUID,
			smbGID,
			readOnlyDirMode,
			readOnlyFileMode,
			corev1.ReadOnlyMany,
			capacity)
		createdObjects = append(createdObjects, roPV)
		status.ReadOnlyPVName = roPV.Name
		status.ReadOnlyPVCName = roPVCName
		status.ReadOnlyAccessMode = corev1.ReadOnlyMany
	}

	return status, createdObjects
}

func newPV(pvName, storageClassName, sambaVolumeShare, podIP, credsSecretName, namespace string,
	uid, gid uint16,
	dirMode, fileMode string,
	accessMode corev1.PersistentVolumeAccessMode,
	capacity resource.Quantity,
) *corev1.PersistentVolume {
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: pvName,
		},
		Spec: corev1.PersistentVolumeSpec{
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: capacity,
			},
			AccessModes: []corev1.PersistentVolumeAccessMode{
				accessMode,
			},
			StorageClassName: storageClassName,
			MountOptions: []string{
				fmt.Sprintf("dir_mode=%s", dirMode),
				fmt.Sprintf("file_mode=%s", fileMode),
				fmt.Sprintf("uid=%d", uid),
				fmt.Sprintf("gid=%d", gid),
				"noperm",
				"mfsymlinks",
				"cache=strict",
				"noserverino",
				"seal",
				"vers=3.1.1",
			},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
		},
	}
	pv.Spec.CSI = &corev1.CSIPersistentVolumeSource{
		Driver: "smb.csi.k8s.io",
		// VolumeHandle needs to be unique for a new CIFS mount
		// This creates a separate one for the rw vs ro using the uid as the
		// the suffix to distinguish the two
		VolumeHandle: fmt.Sprintf("%s/%s#%d", podIP, sambaVolumeShare, uid),
		NodePublishSecretRef: &corev1.SecretReference{
			Name:      credsSecretName,
			Namespace: namespace,
		},
		NodeExpandSecretRef: &corev1.SecretReference{
			Name:      credsSecretName,
			Namespace: namespace,
		},
		NodeStageSecretRef: &corev1.SecretReference{
			Name:      credsSecretName,
			Namespace: namespace,
		},
		VolumeAttributes: map[string]string{
			"source": fmt.Sprintf("//%s/%s", podIP, sambaVolumeShare),
		},
	}
	return pv
}

func newPVC(pvcName, pvName, storageClassName string,
	accessMode corev1.PersistentVolumeAccessMode,
	capacity resource.Quantity,
) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: pvcName,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				accessMode,
			},
			StorageClassName: &storageClassName,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: capacity,
				},
			},
			VolumeName: pvName,
		},
	}
}

func configureSMBPod(pod *corev1.Pod, st *nvcav1new.StorageRequest) {
	// Apply mutations to the pod
	pod.Spec.ImagePullSecrets = make([]corev1.LocalObjectReference, len(st.Spec.SharedStorage.WorkerPullSecretNames))
	for i, secretName := range st.Spec.SharedStorage.WorkerPullSecretNames {
		pod.Spec.ImagePullSecrets[i].Name = secretName
	}
	for i := range pod.Spec.Containers {
		pod.Spec.Containers[i].Image = st.Spec.SharedStorage.SMBContainerImage
		pod.Spec.Containers[i].Env = append(pod.Spec.Containers[i].Env,
			corev1.EnvVar{
				Name: "USERNAME",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						Key: "username",
						LocalObjectReference: corev1.LocalObjectReference{
							Name: readWriteSecretName,
						},
					},
				},
			},
			corev1.EnvVar{
				Name: "PASSWORD",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						Key: "password",
						LocalObjectReference: corev1.LocalObjectReference{
							Name: readWriteSecretName,
						},
					},
				},
			},
			corev1.EnvVar{
				Name: "UID",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						Key: "uid",
						LocalObjectReference: corev1.LocalObjectReference{
							Name: readWriteSecretName,
						},
					},
				},
			},
			corev1.EnvVar{
				Name: "GID",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						Key: "gid",
						LocalObjectReference: corev1.LocalObjectReference{
							Name: readWriteSecretName,
						},
					},
				},
			},
			corev1.EnvVar{
				Name: "ROUSERNAME",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						Key: "username",
						LocalObjectReference: corev1.LocalObjectReference{
							Name: readOnlySecretName,
						},
					},
				},
			},
			corev1.EnvVar{
				Name: "ROPASSWORD",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						Key: "password",
						LocalObjectReference: corev1.LocalObjectReference{
							Name: readOnlySecretName,
						},
					},
				},
			},
			corev1.EnvVar{
				Name: "ROUID",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						Key: "uid",
						LocalObjectReference: corev1.LocalObjectReference{
							Name: readOnlySecretName,
						},
					},
				},
			},
			corev1.EnvVar{
				Name: "ROGID",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						Key: "gid",
						LocalObjectReference: corev1.LocalObjectReference{
							Name: readOnlySecretName,
						},
					},
				},
			},
		)
	}

	// Add labels and annotations from storage request
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	// Overwrite, if we don't already have the label set to a non-empty value
	for k, v := range st.Labels {
		if curV, ok := pod.Labels[k]; !ok || curV == "" {
			pod.Labels[k] = v
		}
	}
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	// Overwrite, if we don't already have the annotation set to a non-empty value
	for k, v := range st.Annotations {
		if curV, ok := pod.Annotations[k]; !ok || curV == "" {
			pod.Annotations[k] = v
		}
	}
}

func newSMBPod(st *nvcav1new.StorageRequest, cfg nvcaconfig.Config) (*corev1.Pod, error) {
	smbServer := &corev1.Pod{}
	smbServer.Name = SMBServerPodName
	smbServer.Namespace = st.Namespace

	// Determine function version ID from labels if not set
	var id string
	taskID := st.Labels[nvcatypes.TaskIDKey]
	// TODO function-id/function-version-id
	functionVersionID, _ := nvcatypes.GetFunctionVersionIDLabelVal(st.Labels)
	if functionVersionID == "" && taskID == "" {
		return nil, fmt.Errorf("a non-empty label %s does not exist on sharedstorage object %s", nvcatypes.FunctionVersionIDKey, st.Name)
	} else if functionVersionID != "" {
		id = functionVersionID
	} else {
		id = taskID
	}

	smbResources := corev1.ResourceRequirements{
		Limits:   corev1.ResourceList(cfg.Agent.SharedStorage.Server.ContainerResources.Limits),
		Requests: corev1.ResourceList(cfg.Agent.SharedStorage.Server.ContainerResources.Requests),
		Claims:   cfg.Agent.SharedStorage.Server.ContainerResources.Claims,
	}
	smbServer.Spec = corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name: SMBServerContainerName,
				Args: []string{
					"-w", "NVIDIA",
					"-u", "$(USERNAME);$(PASSWORD);$(UID);smb;$(GID)",
					"-u", "$(ROUSERNAME);$(ROPASSWORD);$(ROUID);smb;$(ROGID)",
					"-p", "-g", "log level = 3",
					"-G", "share;log level = 3",
					"-f", id,
					"-s", fmt.Sprintf("%[1]s;/shared-data/%[1]s;yes;no;no;$(USERNAME),$(ROUSERNAME);;$(USERNAME);none", SharedStorageVolumeSecretsSambaShare),
					"-s", fmt.Sprintf("%[1]s;/shared-data/%[1]s;yes;no;no;$(USERNAME),$(ROUSERNAME);;$(USERNAME);none", SharedStorageVolumeKNSSambaShare),
				},
				Env: []corev1.EnvVar{
					{
						Name:  "GROUPID",
						Value: strconv.Itoa(int(smbGID)),
					},
				},
				VolumeMounts: []corev1.VolumeMount{
					{
						MountPath: "/shared-data",
						Name:      "data-volume",
					},
				},
				Ports: []corev1.ContainerPort{
					{
						ContainerPort: 445,
					},
					{
						Name:          "metrics",
						ContainerPort: 9922,
					},
				},
				ReadinessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{
						TCPSocket: &corev1.TCPSocketAction{
							Port: intstr.FromInt(445),
						},
					},
					InitialDelaySeconds: 3,
				},
				LivenessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{
						TCPSocket: &corev1.TCPSocketAction{
							Port: intstr.FromInt(445),
						},
					},
					InitialDelaySeconds: 30,
				},
				Resources: *smbResources.DeepCopy(),
			},
		},
		Volumes: []corev1.Volume{
			{
				Name: "data-volume",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{
						Medium:    corev1.StorageMediumMemory,
						SizeLimit: &st.Spec.SharedStorage.Size,
					},
				},
			},
		},
	}
	smbServer.Labels = map[string]string{
		"app.kubernetes.io/name": SMBServerPodName,
	}
	translatecommon.AddNVIDIAGPUNoScheduleToleration(&smbServer.Spec)
	if st.Spec.SharedStorage.Server != nil {
		nvcak8sutil.MergePodSpecTolerations(&smbServer.Spec, st.Spec.SharedStorage.Server.SMBServerPodTolerations...)
	}

	// Add mount for task-data if it is provided
	if st.Spec.SharedStorage.TaskData != nil {
		smbServer.Spec.Containers[0].Args = append(smbServer.Spec.Containers[0].Args,
			"-s", fmt.Sprintf("%[1]s;/task-data/%[1]s;yes;no;no;$(USERNAME),$(ROUSERNAME);;$(USERNAME);none", SharedStorageVolumeTaskDataSambaShare))
		smbServer.Spec.Containers[0].VolumeMounts = append(smbServer.Spec.Containers[0].VolumeMounts,
			corev1.VolumeMount{
				MountPath: "/task-data",
				Name:      SharedStorageTaskDataVolumeName,
			})

		// If storage class is specified we've got a PVC to mount to
		var smbServerStorageVol corev1.Volume
		if st.Spec.SharedStorage.TaskData.StorageClassName != nil {
			smbServerStorageVol = corev1.Volume{
				Name: SharedStorageTaskDataVolumeName,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: SharedStorageTaskDataSMBServerPVCName,
					},
				},
			}
		} else {
			smbServerStorageVol = corev1.Volume{
				Name: SharedStorageTaskDataVolumeName,
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{
						Medium:    corev1.StorageMediumDefault,
						SizeLimit: &st.Spec.SharedStorage.TaskData.Size,
					},
				},
			}
		}
		smbServer.Spec.Volumes = append(smbServer.Spec.Volumes, smbServerStorageVol)
	}

	if st.Spec.SharedStorage.Server != nil && st.Spec.SharedStorage.Server.SMBServerPodNodeAffinity != nil {
		smbServer.Spec.Affinity = st.Spec.SharedStorage.Server.SMBServerPodNodeAffinity.DeepCopy()
	}

	return smbServer, nil
}

func newSMBServerPVAndPVC(_ context.Context,
	namespace string,
	taskData *nvcav1new.SharedStorageTaskDataSpec,
) (*corev1.PersistentVolume, *corev1.PersistentVolumeClaim) {
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-%s", namespace, SharedStorageTaskDataSMBServerPVCName),
		},
		Spec: corev1.PersistentVolumeSpec{
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: taskData.Size,
			},
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteMany,
			},
			StorageClassName:              *taskData.StorageClassName,
			MountOptions:                  taskData.PVMountOptions,
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
		},
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: SharedStorageTaskDataSMBServerPVCName,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteMany,
			},
			StorageClassName: taskData.StorageClassName,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: taskData.Size,
				},
			},
			VolumeName: pv.Name,
		},
	}

	return pv, pvc
}

// rwSecret, roSecret, error
func newSMBSecrets() (*corev1.Secret, *corev1.Secret) {
	readWriteSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: readWriteSecretName,
		},
		Data: map[string][]byte{
			"username": []byte(readWriteUsername),
			"password": []byte(uuid.New().String()),
			"uid":      []byte(strconv.Itoa(int(readWriteUID))),
			"gid":      []byte(strconv.Itoa(int(smbGID))),
		},
	}

	readOnlySecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: readOnlySecretName,
		},
		Data: map[string][]byte{
			"username": []byte(readOnlyUsername),
			"password": []byte(uuid.New().String()),
			"uid":      []byte(strconv.Itoa(int(readOnlyUID))),
			"gid":      []byte(strconv.Itoa(int(smbGID))),
		},
	}
	return readWriteSecret, readOnlySecret
}

func newStorageClass(name string) *storagev1.StorageClass {
	sc := &storagev1.StorageClass{}
	sc.Name = name
	sc.Provisioner = SMBCSIDriverName
	bindingMode := strings.Clone(string(storagev1.VolumeBindingImmediate))
	sc.VolumeBindingMode = (*storagev1.VolumeBindingMode)(&bindingMode)
	controllerutil.AddFinalizer(sc, StorageRequestFinalizer)
	return sc
}
