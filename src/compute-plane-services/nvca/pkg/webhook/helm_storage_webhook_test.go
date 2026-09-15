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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	cmnnvcastorage "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
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

func TestGetEphemeralModelCacheInitAppendFunc(t *testing.T) {
	ps := corev1.PodSpec{
		Containers: []corev1.Container{{Name: "workload"}},
	}
	envSet := map[string]string{"MODEL_URL": "https://models.example/m1", "API_KEY": "k"}
	mod := getEphemeralModelCacheInitAppendFunc("init-image:1", envSet)(t.Context(), &ps)
	assert.True(t, mod)

	if assert.Len(t, ps.InitContainers, 1) {
		ic := ps.InitContainers[0]
		assert.Equal(t, cmnnvcastorage.EphemeralModelCacheInitContainerName, ic.Name)
		assert.Equal(t, "init-image:1", ic.Image)
		// Env is injected explicitly (from the miniservice metadata ConfigMap),
		// sorted by name for deterministic admission output.
		assert.Equal(t, []corev1.EnvVar{
			{Name: "API_KEY", Value: "k"},
			{Name: "MODEL_URL", Value: "https://models.example/m1"},
		}, ic.Env)
		assert.Len(t, ic.VolumeMounts, 3)
	}

	assert.True(t, hasVolumeNamed(ps.Volumes, cmnnvcastorage.ModelCachePodVolumeName))
	assert.True(t, hasVolumeNamed(ps.Volumes, cmnnvcastorage.EphemeralModelCacheConfigDataVolumeName))
	for _, v := range ps.Volumes {
		assert.NotNilf(t, v.VolumeSource.EmptyDir, "volume %s should be emptyDir", v.Name)
	}
	assert.Len(t, ps.Containers[0].VolumeMounts, 2)

	// Idempotent: a second pass does not duplicate the container, volumes, or mounts.
	getEphemeralModelCacheInitAppendFunc("init-image:1", envSet)(t.Context(), &ps)
	assert.Len(t, ps.InitContainers, 1)
	assert.Len(t, ps.Volumes, 2)
	assert.Len(t, ps.Containers[0].VolumeMounts, 2)
}

// TestMutate_EphemeralModelCacheInitTrigger verifies the ephemeral init
// container is injected only when both the image annotation (from pod
// annotations) and the init env (from the miniservice metadata ConfigMap)
// are present.
func TestMutate_EphemeralModelCacheInitTrigger(t *testing.T) {
	ctx := context.Background()
	v := &helmStorageMutatingWebhook{}
	pod := &corev1.Pod{}
	pod.Annotations = map[string]string{
		cmnnvcastorage.WebhookEphemeralModelCacheInitImageAnnotationKey: "init-image:1",
	}
	pod.Spec.Containers = []corev1.Container{{Name: "workload"}}

	// Without metadata env: no injection.
	_, _, err := v.mutate(ctx, pod, nvcatypes.MiniserviceMetadata{})
	require.NoError(t, err)
	assert.Empty(t, pod.Spec.InitContainers)

	// With metadata env: init container injected with the env.
	meta := nvcatypes.MiniserviceMetadata{ModelCacheInitEnv: map[string]string{"MODEL_URL": "u"}}
	_, _, err = v.mutate(ctx, pod, meta)
	require.NoError(t, err)
	if assert.Len(t, pod.Spec.InitContainers, 1) {
		assert.Equal(t, "init-image:1", pod.Spec.InitContainers[0].Image)
		assert.Equal(t, []corev1.EnvVar{{Name: "MODEL_URL", Value: "u"}}, pod.Spec.InitContainers[0].Env)
	}
}

// TestMutate_StableVolumeOrderAcrossReadmission guards against the model-cache
// volume reordering that blocked KAI scheduling: the shared-storage volumes are
// dropped and re-added on every admission, so the model-cache volume/mount must
// also be dropped and re-added to keep a deterministic order. Otherwise a
// controller that re-submits the already-mutated pod via UPDATE (e.g. the KAI
// scheduler's pod-grouper) produces a reordered spec, which the API server
// rejects as an immutable-field change, leaving the pod unschedulable.
func TestMutate_StableVolumeOrderAcrossReadmission(t *testing.T) {
	ctx := context.Background()
	v := &helmStorageMutatingWebhook{}

	pod := &corev1.Pod{}
	pod.Annotations = map[string]string{
		cmnnvcastorage.HelmWebhookSharedStorageKNSReadWritePVCNameAnnotationKey:     "nvcf-kns-data-rw",
		cmnnvcastorage.HelmWebhookSharedStorageSecretsReadWritePVCNameAnnotationKey: "nvcf-secrets-data-rw",
		cmnnvcastorage.WebhookModelCachePVCNameAnnotationKey:                        "ro-pvc-cache",
	}
	pod.Spec.InitContainers = []corev1.Container{{Name: "init"}}
	pod.Spec.Containers = []corev1.Container{{Name: "utils"}}

	volNames := func(vs []corev1.Volume) []string {
		out := make([]string, len(vs))
		for i, x := range vs {
			out[i] = x.Name
		}
		return out
	}
	mountSig := func(ms []corev1.VolumeMount) []string {
		out := make([]string, len(ms))
		for i, m := range ms {
			out[i] = m.Name + ":" + m.MountPath
		}
		return out
	}

	// First admission (CREATE).
	_, _, err := v.mutate(ctx, pod, nvcatypes.MiniserviceMetadata{})
	require.NoError(t, err)
	wantVols := volNames(pod.Spec.Volumes)
	wantMounts := mountSig(pod.Spec.Containers[0].VolumeMounts)
	wantInitMounts := mountSig(pod.Spec.InitContainers[0].VolumeMounts)
	// Sanity: all three injected volumes are present.
	assert.Contains(t, wantVols, cmnnvcastorage.ModelCachePodVolumeName)

	// Second admission (simulates KAI's pod-grouper re-submitting the pod). The
	// order must be byte-for-byte identical, or the UPDATE is rejected.
	_, _, err = v.mutate(ctx, pod, nvcatypes.MiniserviceMetadata{})
	require.NoError(t, err)
	assert.Equal(t, wantVols, volNames(pod.Spec.Volumes), "volume order must be stable across re-admission")
	assert.Equal(t, wantMounts, mountSig(pod.Spec.Containers[0].VolumeMounts), "container mount order must be stable")
	assert.Equal(t, wantInitMounts, mountSig(pod.Spec.InitContainers[0].VolumeMounts), "init mount order must be stable")
}
