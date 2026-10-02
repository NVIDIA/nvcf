/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package cache

import (
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
)

const (
	ModelDataVolumeName = "model-data"
	ModelDirPath        = "/config/models"
	ResourceDirPath     = "/config/resources"
)

// NVMesh will fail if the cache size is less than 1Gi
const minCacheSize = 1 << 30 // 1Gi

// The control plane reports the artifact bytes. The writer stores them on an
// XFS volume whose internal log, allocation group metadata and reserve pool
// are not available for data, close to 100Mi on a 1Gi volume, and it writes a
// manifest alongside the files. Two 484Mi checkpoints therefore fail with
// ENOSPC on a 1Gi claim. Size the claim with headroom over the reported bytes
// and round up to whole GiB, the granularity the storage classes provision in.
const (
	cacheSizeHeadroomPercent int64 = 10
	cacheSizeHeadroomMin     int64 = 128 << 20 // 128Mi
	gib                      int64 = 1 << 30
)

// cacheVolumeSize returns the claim size for the reported artifact bytes. A
// missing or non-positive report keeps the historical 1Gi minimum.
func cacheVolumeSize(artifactBytes int64) int64 {
	if artifactBytes <= 0 {
		return minCacheSize
	}
	headroom := artifactBytes * cacheSizeHeadroomPercent / 100
	if headroom < cacheSizeHeadroomMin {
		headroom = cacheSizeHeadroomMin
	}
	total := artifactBytes + headroom
	rounded := (total + gib - 1) / gib * gib
	if rounded < minCacheSize {
		return minCacheSize
	}
	return rounded
}

func Translate(
	cacheLaunchSpec *common.CacheLaunchSpecification,
	allEnvSet map[string]string,
	workerImagePullSecrets []*corev1.Secret,
	altCacheHandle string,
) (objs []metav1.Object, err error) {
	if !cacheLaunchSpec.CacheArtifacts {
		return nil, nil
	}

	cls := *cacheLaunchSpec

	if cls.CacheHandle == "" {
		cls.CacheHandle = altCacheHandle
	}
	cls.CacheSize = cacheVolumeSize(cls.CacheSize)

	imagePullSecretRefs := make([]corev1.LocalObjectReference, len(workerImagePullSecrets))
	for i, pullSecret := range workerImagePullSecrets {
		imagePullSecretRefs[i].Name = pullSecret.Name
	}

	cachePVC := newBlockDeviceRWPVC(cls)
	cacheInitJob := newInitJob(cls, allEnvSet,
		cachePVC,
		imagePullSecretRefs,
	)

	objs = append(objs, cachePVC, cacheInitJob)

	return objs, nil
}

func NewModelDataVolumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{
			Name:      ModelDataVolumeName,
			MountPath: ModelDirPath,
		},
		{
			Name:      ModelDataVolumeName,
			MountPath: ResourceDirPath,
		},
	}
}

func newBlockDeviceRWPVC(cacheLaunchSpec common.CacheLaunchSpecification) *corev1.PersistentVolumeClaim {
	volumeMode := corev1.PersistentVolumeFilesystem
	scName := "nvcf-sc"
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "rw-pvc-" + cacheLaunchSpec.CacheHandle,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			VolumeMode:  &volumeMode,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: *resource.NewQuantity(cacheLaunchSpec.CacheSize, resource.BinarySI),
				},
			},
			StorageClassName: &scName,
		},
	}
	return pvc
}

func newInitJob(
	cacheLaunchSpec common.CacheLaunchSpecification,
	allEnvSet map[string]string,
	cachePVC *corev1.PersistentVolumeClaim,
	imagePullSecrets []corev1.LocalObjectReference,
) *batchv1.Job {
	systemID := int64(65532)
	termGPSeconds := int64(1)
	backoffLimit := int32(3)
	completions := int32(1)
	parallelism := int32(1)

	initContainerImage := allEnvSet[common.InitImageEnv]

	// Volumes
	var volumes []corev1.Volume

	configDir := "/config/shared"
	configVolume := corev1.Volume{
		Name: "config-data",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}
	volumes = append(volumes, configVolume)

	modelsDir := "/config/models"
	resourcesDir := "/config/resources"
	modelsVolume := corev1.Volume{
		Name: ModelDataVolumeName,
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: cachePVC.Name,
			},
		},
	}
	volumes = append(volumes, modelsVolume)

	instanceDir := common.NVCFInfoDir
	instanceVolume := common.NewInstanceDataVolume()
	volumes = append(volumes, instanceVolume)

	// Volume mounts.
	initContainerVolumeMounts := []corev1.VolumeMount{
		{
			Name:      modelsVolume.Name,
			MountPath: modelsDir,
		},
		{
			Name:      modelsVolume.Name,
			MountPath: resourcesDir,
		},
		{
			Name:      configVolume.Name,
			MountPath: configDir,
		},
		{
			Name:      instanceVolume.Name,
			MountPath: instanceDir,
		},
	}

	envs := common.MapToEnv(allEnvSet)
	common.SortEnvs(envs)

	// The init container configured as a downloader does not need
	// ess configuration, so delete the assertion token to prevent ess init.
	if allEnvSet[common.SecretsAssertionTokenEnv] != "" {
		envs = removeESSAssertionToken(envs)
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "writer-job-" + cacheLaunchSpec.CacheHandle,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Completions:  &completions,
			Parallelism:  &parallelism,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name: "writer-" + cacheLaunchSpec.CacheHandle,
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					SecurityContext: &corev1.PodSecurityContext{
						FSGroup: &systemID,
					},
					ImagePullSecrets:              imagePullSecrets,
					TerminationGracePeriodSeconds: &termGPSeconds,
					Containers: []corev1.Container{
						{
							Name:  common.InitContainerName,
							Image: initContainerImage,
							SecurityContext: &corev1.SecurityContext{
								RunAsUser:  &systemID,
								RunAsGroup: &systemID,
							},
							// The writer reports its failure on stdout and exits
							// non-zero. Surface those last lines as the termination
							// message so NVCA can classify the failure, for example
							// a full cache volume, without reading pod logs.
							TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
							VolumeMounts:             initContainerVolumeMounts,
							Env:                      envs,
						},
					},
					Volumes: volumes,
				},
			},
		},
	}
	return job
}

func removeESSAssertionToken(envs []corev1.EnvVar) []corev1.EnvVar {
	// The env will be at the end of the sorted envs slice.
	for i := len(envs) - 1; i >= 0; i-- {
		if envs[i].Name == common.SecretsAssertionTokenEnv {
			envs = append(envs[:i], envs[i+1:]...)
			break
		}
	}
	return envs
}
