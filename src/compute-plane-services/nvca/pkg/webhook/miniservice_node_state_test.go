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
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func nodeStateTestWebhook(enabled bool) *miniserviceMutatingWebhook {
	fff := &featureflagmock.Fetcher{}
	if enabled {
		fff.EnabledFFs = []*featureflag.FeatureFlag{featureflag.NodeCordonSignal}
	}
	return &miniserviceMutatingWebhook{fff: fff}
}

func nodeStateTestPodSpec() corev1.PodSpec {
	return corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "init"}},
		Containers:     []corev1.Container{{Name: "app"}, {Name: "sidecar"}},
	}
}

func findVolumes(ps *corev1.PodSpec, name string) []corev1.Volume {
	var out []corev1.Volume
	for _, v := range ps.Volumes {
		if v.Name == name {
			out = append(out, v)
		}
	}
	return out
}

func findMounts(c *corev1.Container, name string) []corev1.VolumeMount {
	var out []corev1.VolumeMount
	for _, m := range c.VolumeMounts {
		if m.Name == name {
			out = append(out, m)
		}
	}
	return out
}

func TestMutatePodSpec_NodeStateVolumeDisabledByDefault(t *testing.T) {
	ps := nodeStateTestPodSpec()
	nodeStateTestWebhook(false).mutatePodSpec(&ps, nvcatypes.MiniserviceMetadata{})

	assert.Empty(t, ps.Volumes)
	for i := range ps.Containers {
		assert.Empty(t, ps.Containers[i].VolumeMounts)
	}
}

func TestMutatePodSpec_NodeStateVolumeProjectsTheCordonAnnotation(t *testing.T) {
	ps := nodeStateTestPodSpec()
	nodeStateTestWebhook(true).mutatePodSpec(&ps, nvcatypes.MiniserviceMetadata{})

	vols := findVolumes(&ps, nvcatypes.NodeStateVolumeName)
	require.Len(t, vols, 1)
	require.NotNil(t, vols[0].DownwardAPI)
	require.Len(t, vols[0].DownwardAPI.Items, 1)
	item := vols[0].DownwardAPI.Items[0]
	assert.Equal(t, nvcatypes.NodeCordonedFileName, item.Path)
	require.NotNil(t, item.FieldRef)
	assert.Equal(t, "metadata.annotations['"+nvcatypes.NodeCordonedAnnotationKey+"']", item.FieldRef.FieldPath)

	for i := range ps.Containers {
		mounts := findMounts(&ps.Containers[i], nvcatypes.NodeStateVolumeName)
		require.Len(t, mounts, 1, "container %s", ps.Containers[i].Name)
		assert.Equal(t, nvcatypes.NodeStateMountPath, mounts[0].MountPath)
		assert.True(t, mounts[0].ReadOnly)
		// subPath mounts are never refreshed by the kubelet, so the whole directory must be mounted.
		assert.Empty(t, mounts[0].SubPath)
	}
	assert.Empty(t, ps.InitContainers[0].VolumeMounts, "init containers do not need the signal")
}

func TestMutatePodSpec_NodeStateVolumeIsIdempotent(t *testing.T) {
	ps := nodeStateTestPodSpec()
	wh := nodeStateTestWebhook(true)
	wh.mutatePodSpec(&ps, nvcatypes.MiniserviceMetadata{})
	wh.mutatePodSpec(&ps, nvcatypes.MiniserviceMetadata{})

	assert.Len(t, findVolumes(&ps, nvcatypes.NodeStateVolumeName), 1)
	for i := range ps.Containers {
		assert.Len(t, findMounts(&ps.Containers[i], nvcatypes.NodeStateVolumeName), 1)
	}
}

func TestMutatePodSpec_NodeStateVolumeLeavesAUserVolumeOfTheSameNameAlone(t *testing.T) {
	ps := nodeStateTestPodSpec()
	userVolume := corev1.Volume{
		Name:         nvcatypes.NodeStateVolumeName,
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	}
	ps.Volumes = []corev1.Volume{userVolume}
	nodeStateTestWebhook(true).mutatePodSpec(&ps, nvcatypes.MiniserviceMetadata{})

	assert.Equal(t, []corev1.Volume{userVolume}, ps.Volumes)
	for i := range ps.Containers {
		assert.Empty(t, ps.Containers[i].VolumeMounts)
	}
}

func TestMutatePodSpec_NodeStateVolumeSkipsContainersAlreadyUsingThePath(t *testing.T) {
	ps := nodeStateTestPodSpec()
	userMount := corev1.VolumeMount{Name: "data", MountPath: nvcatypes.NodeStateMountPath}
	ps.Containers[0].VolumeMounts = []corev1.VolumeMount{userMount}
	nodeStateTestWebhook(true).mutatePodSpec(&ps, nvcatypes.MiniserviceMetadata{})

	assert.Equal(t, []corev1.VolumeMount{userMount}, ps.Containers[0].VolumeMounts,
		"a container that already mounts the path keeps its own mount")
	assert.Len(t, findMounts(&ps.Containers[1], nvcatypes.NodeStateVolumeName), 1)
}
