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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
)

func TestTranslateReturnsNilWhenCacheArtifactsDisabled(t *testing.T) {
	objs, err := Translate(&common.CacheLaunchSpecification{}, nil, nil, "task-id")

	require.NoError(t, err)
	assert.Nil(t, objs)
}

func TestTranslateCreatesPVCAndInitJob(t *testing.T) {
	objs, err := Translate(
		&common.CacheLaunchSpecification{
			CacheArtifacts: true,
			CacheSize:      1,
		},
		map[string]string{
			common.InitImageEnv:             "nvcr.io/nvidia/init:latest",
			common.SecretsAssertionTokenEnv: "assertion-token",
		},
		[]*corev1.Secret{{ObjectMeta: metav1ObjectMeta("pull-secret")}},
		"task-id",
	)

	require.NoError(t, err)
	require.Len(t, objs, 2)

	pvc, ok := objs[0].(*corev1.PersistentVolumeClaim)
	require.True(t, ok)
	assert.Equal(t, "rw-pvc-task-id", pvc.Name)
	storageRequest := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	assert.Equal(t, int64(minCacheSize), storageRequest.Value())

	job, ok := objs[1].(*batchv1.Job)
	require.True(t, ok)
	assert.Equal(t, "writer-job-task-id", job.Name)
	assert.Equal(t, "nvcr.io/nvidia/init:latest", job.Spec.Template.Spec.Containers[0].Image)
	assert.Equal(t, "pull-secret", job.Spec.Template.Spec.ImagePullSecrets[0].Name)
	assert.NotContains(t, envNames(job.Spec.Template.Spec.Containers[0].Env), common.SecretsAssertionTokenEnv)
}

func TestNewModelDataVolumeMounts(t *testing.T) {
	mounts := NewModelDataVolumeMounts()

	require.Len(t, mounts, 2)
	assert.Equal(t, ModelDataVolumeName, mounts[0].Name)
	assert.Equal(t, ModelDirPath, mounts[0].MountPath)
	assert.Equal(t, ModelDataVolumeName, mounts[1].Name)
	assert.Equal(t, ResourceDirPath, mounts[1].MountPath)
}

func envNames(envs []corev1.EnvVar) []string {
	names := make([]string, 0, len(envs))
	for _, env := range envs {
		names = append(names, env.Name)
	}
	return names
}

func metav1ObjectMeta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name}
}
