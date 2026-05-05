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

package webhook

import (
	"context"
	"errors"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	cmnnvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
)

var (
	_ admission.Handler = (*helmStorageMutatingWebhook)(nil)
)

type helmStorageMutatingWebhook struct{}

func newHelmStorageMutatingWebhook() admission.Handler {
	return &helmStorageMutatingWebhook{}
}

// Handle handles admission requests.
//
// This is a sub and will be removed in a future release.
func (v *helmStorageMutatingWebhook) Handle(_ context.Context, _ admission.Request) admission.Response {
	return admission.Allowed("")
}

func (v *helmStorageMutatingWebhook) mutate(ctx context.Context, obj client.Object) (ok bool, warnings admission.Warnings, err error) {
	log := core.GetLogger(ctx)

	var mutations []podMutateFunc
	annotations := obj.GetAnnotations()

	// Mutate if this is SS needs RW PVC
	if rwPVCName, ok := annotations[cmnnvcastorage.HelmWebhookSharedStorageKNSReadWritePVCNameAnnotationKey]; ok {
		log.Debugf("found annotation %s with pvc %s", cmnnvcastorage.HelmWebhookSharedStorageKNSReadWritePVCNameAnnotationKey, rwPVCName)
		// add read-write PVC volume and mount
		mutations = append(mutations, getSharedVolumeAppendFunc(rwPVCName,
			cmnnvcastorage.SharedStorageVolumeKNSTokenVolumeName,
			cmnnvcastorage.SharedStorageVolumeKNSTokenMountPath,
			false))
	}

	// Mutate if this is SS needs RO PVC
	if roPVCName, ok := annotations[cmnnvcastorage.HelmWebhookSharedStorageKNSReadOnlyPVCNameAnnotationKey]; ok {
		// add read only PVC volume and mount
		mutations = append(mutations, getSharedVolumeAppendFunc(roPVCName,
			cmnnvcastorage.SharedStorageVolumeKNSTokenVolumeName,
			cmnnvcastorage.SharedStorageVolumeKNSTokenMountPath,
			true))
	}

	// Mutate if this is KNS needs RW PVC
	if rwPVCName, ok := annotations[cmnnvcastorage.HelmWebhookSharedStorageSecretsReadWritePVCNameAnnotationKey]; ok {
		// add read-write PVC volume and mount
		mutations = append(mutations, getSharedVolumeAppendFunc(rwPVCName,
			cmnnvcastorage.SharedStorageSecretsVolumeName,
			cmnnvcastorage.SharedStorageVolumeSecretsMountPath,
			false))
	}

	// Mutate if this is KNS needs RO PVC
	if roPVCName, ok := annotations[cmnnvcastorage.HelmWebhookSharedStorageSecretsReadOnlyPVCNameAnnotationKey]; ok {
		// add read only PVC volume and mount
		mutations = append(mutations, getSharedVolumeAppendFunc(roPVCName,
			cmnnvcastorage.SharedStorageSecretsVolumeName,
			cmnnvcastorage.SharedStorageVolumeSecretsMountPath,
			true))
	}

	// If mutations were added drop all reserved ones shared storage ones
	if len(mutations) > 0 {
		mutations = append([]podMutateFunc{
			getSharedVolumeDropAllReservedFunc(
				cmnnvcastorage.IsSharedStorageVolumeName,
				cmnnvcastorage.IsSharedStorageVolumeMountPath)}, mutations...)
	}

	// Mutate if this is Task Results need RW PVC
	if rwPVCName, ok := annotations[cmnnvcastorage.HelmWebhookSharedStorageTaskDataReadWritePVCNameAnnotationKey]; ok {
		// Add drop all reserved task-data volumes
		mutations = append([]podMutateFunc{getSharedVolumeDropAllReservedFunc(
			func(volumeName string) bool {
				return volumeName == cmnnvcastorage.SharedStorageTaskDataVolumeName
			},
			func(mountPath string) bool {
				return mountPath == cmnnvcastorage.SharedStorageVolumeTaskDataMountPath
			})}, mutations...)

		// add read write PVC volume and mount
		mutations = append(mutations, getSharedVolumeAppendFunc(rwPVCName,
			cmnnvcastorage.SharedStorageTaskDataVolumeName,
			cmnnvcastorage.SharedStorageVolumeTaskDataMountPath,
			false))
	}

	// Mutate if this is model cache pvc annotation exists
	if pvcName, ok := annotations[cmnnvcastorage.WebhookModelCachePVCNameAnnotationKey]; ok {
		mutations = append(mutations, getModelCachePVCVolumeAppendFunc(pvcName))
		mutations = append(mutations, getModelCachePVCVolumeMountAppendFunc())
	}

	var errs []error
	for _, mf := range mutations {
		switch t := obj.(type) {
		case *corev1.Pod:
			mf(ctx, &t.Spec)
		case *appsv1.Deployment:
			// NO-OP: Deployments are intentionally not mutated. Pod mutation handles
			// the actual PodSpec when pods are created.
		case *appsv1.ReplicaSet:
			// NO-OP: ReplicaSets are intentionally not mutated. Pod mutation handles
			// the actual PodSpec when pods are created.
		case *appsv1.StatefulSet:
			// NO-OP: StatefulSets are intentionally not mutated. Pod mutation handles
			// the actual PodSpec when pods are created.
		case *batchv1.Job:
			// NO-OP: Jobs are intentionally not mutated. Pod mutation handles
			// the actual PodSpec when pods are created.
		case *batchv1.CronJob:
			// NO-OP: CronJobs are intentionally not mutated. Pod mutation handles
			// the actual PodSpec when pods are created.
		}
	}

	return len(mutations) > 0, warnings, errors.Join(errs...)
}

func getModelCachePVCVolumeAppendFunc(pvcName string) podMutateFunc {
	return func(_ context.Context, ps *corev1.PodSpec) bool {
		for i, v := range ps.Volumes {
			if v.Name == cmnnvcastorage.ModelCachePodVolumeName {
				if vpvc := v.VolumeSource.PersistentVolumeClaim; vpvc != nil && vpvc.ClaimName == pvcName && vpvc.ReadOnly { //nolint:staticcheck
					return false
				}
				ps.Volumes[i].VolumeSource = corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvcName,
						ReadOnly:  true,
					},
				}
				return true
			}
		}
		ps.Volumes = append(ps.Volumes, corev1.Volume{
			Name: cmnnvcastorage.ModelCachePodVolumeName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: pvcName,
					ReadOnly:  true,
				},
			},
		})
		return true
	}
}

func getModelCachePVCVolumeMountAppendFunc() podMutateFunc {
	return func(_ context.Context, ps *corev1.PodSpec) (mod bool) {
		for _, containers := range [][]corev1.Container{ps.InitContainers, ps.Containers} {
			modModels := addVolumeMount(containers, cmnnvcastorage.ModelCachePodVolumeName,
				cmnnvcastorage.ModelCachePodModelMountPath, false)
			mod = mod || modModels
			modResources := addVolumeMount(containers, cmnnvcastorage.ModelCachePodVolumeName,
				cmnnvcastorage.ModelCachePodResourcesMountPath, false)
			mod = mod || modResources
		}
		return mod
	}
}

func getSharedVolumeDropAllReservedFunc(
	isSharedStorageVolumeName func(string) bool,
	isSharedStorageVolumeMountPath func(string) bool,
) podMutateFunc {
	return func(ctx context.Context, ps *corev1.PodSpec) bool {
		log := core.GetLogger(ctx)

		var volumes []corev1.Volume
		for _, v := range ps.Volumes {
			// filter out ones that have the shared storage volume name
			if !isSharedStorageVolumeName(v.Name) {
				// Check if it has a persistent volume claim source, and ensure
				// it is not trying to mount a claim that is reserved
				if v.VolumeSource.PersistentVolumeClaim != nil && //nolint:staticcheck
					cmnnvcastorage.IsSharedStoragePVC(v.VolumeSource.PersistentVolumeClaim.ClaimName) { //nolint:staticcheck
					log.Debugf("removing reserved claim %s from PodSpec", v.VolumeSource.PersistentVolumeClaim.ClaimName) //nolint:staticcheck
					continue
				}
				volumes = append(volumes, v)
			}
		}
		ps.Volumes = volumes

		// Filter volume mounts that are reserved names
		for _, containers := range [][]corev1.Container{ps.InitContainers, ps.Containers} {
			for i := range containers {
				var vMounts []corev1.VolumeMount
				for _, v := range containers[i].VolumeMounts {
					if !isSharedStorageVolumeName(v.Name) &&
						!isSharedStorageVolumeMountPath(v.MountPath) {
						vMounts = append(vMounts, v)
					}
				}
				containers[i].VolumeMounts = vMounts
			}
		}

		return true
	}
}

func getSharedVolumeAppendFunc(
	pvcName,
	volumeName,
	mountPath string,
	mountReadOnly bool,
) podMutateFunc {
	return func(ctx context.Context, ps *corev1.PodSpec) (mod bool) {
		mod = addVolumeFunc(pvcName, volumeName)(ctx, ps)
		// Find and mutate or add volume mount if the path is specified
		for _, containers := range [][]corev1.Container{ps.InitContainers, ps.Containers} {
			modVM := addVolumeMount(containers, volumeName, mountPath, mountReadOnly)
			mod = mod || modVM
		}

		return mod
	}
}

// addVolumeFunc adds or updates a PVC volume with name volumeName.
func addVolumeFunc(pvcName, volumeName string) podMutateFunc {
	return func(_ context.Context, ps *corev1.PodSpec) bool {
		hasTaskDataVolume := false
		for i, v := range ps.Volumes {
			if v.Name == volumeName {
				hasTaskDataVolume = true
				ps.Volumes[i].VolumeSource = corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvcName,
					},
				}
				break
			}
		}
		if !hasTaskDataVolume {
			ps.Volumes = append(ps.Volumes, corev1.Volume{
				Name: volumeName,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvcName,
					},
				},
			})
		}
		return true
	}
}

func addVolumeMount(containers []corev1.Container, volumeName, mountPath string, readOnly bool) (mod bool) {
	for i, c := range containers {
		foundVolumeMount := false
		for _, v := range c.VolumeMounts {
			if v.Name == volumeName && v.MountPath == mountPath && v.ReadOnly == readOnly {
				foundVolumeMount = true
				break
			}
		}
		if !foundVolumeMount {
			mod = true
			containers[i].VolumeMounts = append(containers[i].VolumeMounts, corev1.VolumeMount{
				Name:      volumeName,
				MountPath: mountPath,
				ReadOnly:  readOnly,
			})
		}
	}
	return mod
}
