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
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"

	cmnnvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
)

func TestGetModelCachePVCAppend(t *testing.T) {
	tests := []struct {
		name     string
		pvcName  string
		podSpec  corev1.PodSpec
		expected corev1.PodSpec
		expMod   bool
	}{
		{
			name:    "existing",
			pvcName: "model-cache-pvc",
			podSpec: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{
						Name: "foo-init",
						VolumeMounts: []corev1.VolumeMount{
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: cmnnvcastorage.ModelCachePodModelMountPath},
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: cmnnvcastorage.ModelCachePodResourcesMountPath},
						},
					},
				},
				Containers: []corev1.Container{
					{
						Name: "foo",
						VolumeMounts: []corev1.VolumeMount{
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: cmnnvcastorage.ModelCachePodModelMountPath},
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: cmnnvcastorage.ModelCachePodResourcesMountPath},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: cmnnvcastorage.ModelCachePodVolumeName,
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						},
					},
				},
			},
			expMod: true,
			expected: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{
						Name: "foo-init",
						VolumeMounts: []corev1.VolumeMount{
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: cmnnvcastorage.ModelCachePodModelMountPath},
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: cmnnvcastorage.ModelCachePodResourcesMountPath},
						},
					},
				},
				Containers: []corev1.Container{
					{
						Name: "foo",
						VolumeMounts: []corev1.VolumeMount{
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: cmnnvcastorage.ModelCachePodModelMountPath},
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: cmnnvcastorage.ModelCachePodResourcesMountPath},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: cmnnvcastorage.ModelCachePodVolumeName,
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: "model-cache-pvc",
								ReadOnly:  true,
							},
						},
					},
				},
			},
		},
		{
			name:    "existing with custom path",
			pvcName: "model-cache-pvc",
			podSpec: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{
						Name: "foo-init",
						VolumeMounts: []corev1.VolumeMount{
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: "/my-models"},
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: "/my-resources"},
						},
					},
				},
				Containers: []corev1.Container{
					{
						Name: "foo",
						VolumeMounts: []corev1.VolumeMount{
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: "/my-models"},
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: "/my-resources"},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: cmnnvcastorage.ModelCachePodVolumeName,
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						},
					},
				},
			},
			expMod: true,
			expected: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{
						Name: "foo-init",
						VolumeMounts: []corev1.VolumeMount{
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: "/my-models"},
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: "/my-resources"},
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: cmnnvcastorage.ModelCachePodModelMountPath},
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: cmnnvcastorage.ModelCachePodResourcesMountPath},
						},
					},
				},
				Containers: []corev1.Container{
					{
						Name: "foo",
						VolumeMounts: []corev1.VolumeMount{
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: "/my-models"},
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: "/my-resources"},
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: cmnnvcastorage.ModelCachePodModelMountPath},
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: cmnnvcastorage.ModelCachePodResourcesMountPath},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: cmnnvcastorage.ModelCachePodVolumeName,
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: "model-cache-pvc",
								ReadOnly:  true,
							},
						},
					},
				},
			},
		},
		{
			name:    "no volumes",
			pvcName: "model-cache-pvc",
			podSpec: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{Name: "foo-init"},
				},
				Containers: []corev1.Container{
					{Name: "foo"},
				},
			},
			expMod: true,
			expected: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{
						Name: "foo-init",
						VolumeMounts: []corev1.VolumeMount{
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: cmnnvcastorage.ModelCachePodModelMountPath},
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: cmnnvcastorage.ModelCachePodResourcesMountPath},
						},
					},
				},
				Containers: []corev1.Container{
					{
						Name: "foo",
						VolumeMounts: []corev1.VolumeMount{
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: cmnnvcastorage.ModelCachePodModelMountPath},
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: cmnnvcastorage.ModelCachePodResourcesMountPath},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: cmnnvcastorage.ModelCachePodVolumeName,
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: "model-cache-pvc",
								ReadOnly:  true,
							},
						},
					},
				},
			},
		},
		{
			name:    "other volume",
			pvcName: "model-cache-pvc",
			podSpec: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{Name: "foo-init"},
				},
				Containers: []corev1.Container{
					{Name: "foo"},
				},
				Volumes: []corev1.Volume{
					{
						Name: "other-volume",
					},
				},
			},
			expMod: true,
			expected: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{
						Name: "foo-init",
						VolumeMounts: []corev1.VolumeMount{
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: cmnnvcastorage.ModelCachePodModelMountPath},
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: cmnnvcastorage.ModelCachePodResourcesMountPath},
						},
					},
				},
				Containers: []corev1.Container{
					{
						Name: "foo",
						VolumeMounts: []corev1.VolumeMount{
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: cmnnvcastorage.ModelCachePodModelMountPath},
							{Name: cmnnvcastorage.ModelCachePodVolumeName, MountPath: cmnnvcastorage.ModelCachePodResourcesMountPath},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: "other-volume",
					},
					{
						Name: cmnnvcastorage.ModelCachePodVolumeName,
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: "model-cache-pvc",
								ReadOnly:  true,
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			gotMod := false
			for _, mf := range []podMutateFunc{
				getModelCachePVCVolumeAppendFunc(tt.pvcName),
				getModelCachePVCVolumeMountAppendFunc(),
			} {
				mod := mf(ctx, &tt.podSpec)
				gotMod = gotMod || mod
			}
			assert.Equal(t, tt.expected, tt.podSpec)
			assert.Equal(t, tt.expMod, gotMod)
		})
	}
}
