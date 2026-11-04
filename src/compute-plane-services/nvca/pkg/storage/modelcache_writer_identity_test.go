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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

func writerJobFixture() *batchv1.Job {
	nonRoot := int64(65532)
	return &batchv1.Job{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), FSGroup: &nonRoot},
		InitContainers: []corev1.Container{{Name: "prepare",
			VolumeMounts: []corev1.VolumeMount{{Name: ModelCachePodVolumeName, MountPath: "/config/models"}}}},
		Containers: []corev1.Container{
			{Name: "init", SecurityContext: &corev1.SecurityContext{RunAsUser: &nonRoot, RunAsGroup: &nonRoot},
				VolumeMounts: []corev1.VolumeMount{{Name: ModelCachePodVolumeName, MountPath: "/config/models"}}},
			{Name: "sidecar",
				VolumeMounts: []corev1.VolumeMount{{Name: ModelCachePodVolumeName, MountPath: "/config/models", ReadOnly: true}}},
		},
	}}}}
}

func TestApplySharedClaimWriterIdentity(t *testing.T) {
	t.Run("root elevates only the containers that write the claim", func(t *testing.T) {
		job := writerJobFixture()
		ApplySharedClaimWriterIdentity(job, ModelCacheWriterIdentityRoot)
		spec := job.Spec.Template.Spec
		for _, c := range []corev1.Container{spec.InitContainers[0], spec.Containers[0]} {
			require.NotNil(t, c.SecurityContext, c.Name)
			assert.EqualValues(t, 0, *c.SecurityContext.RunAsUser, c.Name)
			assert.EqualValues(t, 0, *c.SecurityContext.RunAsGroup, c.Name)
			assert.False(t, *c.SecurityContext.RunAsNonRoot, c.Name)
		}
		assert.Nil(t, spec.Containers[1].SecurityContext, "the read-only sidecar keeps its identity")
		assert.True(t, *spec.SecurityContext.RunAsNonRoot, "the pod-level setting is left alone; the container value overrides it")
		assert.EqualValues(t, 65532, *spec.SecurityContext.FSGroup)
	})

	for _, identity := range []string{"", ModelCacheWriterIdentityFSGroup} {
		t.Run("identity "+identity+" leaves the translator's job untouched", func(t *testing.T) {
			job := writerJobFixture()
			want := job.DeepCopy()
			ApplySharedClaimWriterIdentity(job, identity)
			assert.Equal(t, want, job)
		})
	}
}

func TestSharedClaimWriterIdentityFromRequest(t *testing.T) {
	identity, err := sharedClaimWriterIdentityFromRequest(nil)
	require.NoError(t, err)
	assert.Empty(t, identity, "a request created before selections existed has no identity")

	selection := &PersistedModelCacheStorageSelection{
		Version:             ModelCacheStorageSelectionVersion,
		Workflow:            ModelCacheWorkflowHelm,
		Mode:                ModelCacheSelectionDurable,
		StorageClassName:    DefaultModelCacheStorageClassName,
		StorageClassUID:     "sc-uid",
		StorageClassDigest:  "v1:sha256:" + strings.Repeat("a", 64),
		ProfileDigest:       "sha256:" + strings.Repeat("b", 64),
		Provider:            "ociFss",
		Provisioner:         "fss.csi.oraclecloud.com",
		Transition:          ModelCacheTransitionRWXReadOnly,
		WriterIdentity:      ModelCacheWriterIdentityRoot,
		RequiredAccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
	}
	raw, err := selection.Marshal()
	require.NoError(t, err)
	identity, err = sharedClaimWriterIdentityFromRequest(map[string]string{ModelCacheStorageSelectionAnnotationKey: raw})
	require.NoError(t, err)
	assert.Equal(t, ModelCacheWriterIdentityRoot, identity)

	_, err = sharedClaimWriterIdentityFromRequest(map[string]string{ModelCacheStorageSelectionAnnotationKey: "{not json"})
	require.Error(t, err, "a corrupt selection is reported, not ignored")
}

// sharedFSSelectionAnnotation is the selection the agent persists on a Helm
// request whose cache StorageClass is a ReadWriteMany driver, with the writer
// identity it resolved from the CSIDriver.
func sharedFSSelectionAnnotation(t *testing.T, identity string) string {
	t.Helper()
	selection := &PersistedModelCacheStorageSelection{
		Version:             ModelCacheStorageSelectionVersion,
		Workflow:            ModelCacheWorkflowHelm,
		Mode:                ModelCacheSelectionDurable,
		StorageClassName:    DefaultModelCacheStorageClassName,
		StorageClassUID:     "sc-uid",
		StorageClassDigest:  "v1:sha256:" + strings.Repeat("a", 64),
		ProfileDigest:       "sha256:" + strings.Repeat("b", 64),
		Provider:            "ociFss",
		Provisioner:         "fss.csi.oraclecloud.com",
		Transition:          ModelCacheTransitionRWXReadOnly,
		WriterIdentity:      identity,
		RequiredAccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
	}
	raw, err := selection.Marshal()
	require.NoError(t, err)
	return raw
}
