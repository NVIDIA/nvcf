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

package mscontroller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/miniservice"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func newWorkerIdentityScheme(t *testing.T) *runtime.Scheme {
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, rbacv1.AddToScheme(s))
	return s
}

func assertWorkerIdentityMeta(t *testing.T, meta metav1.ObjectMeta, msName string) {
	assert.Equal(t, msName, meta.Labels[nvcatypes.MiniserviceNameLabel])
	assert.Equal(t, "true", meta.Annotations[nvcatypes.InfraObjectAnnotationKey])
}

func TestEnsureWorkerIdentity_CreatesObjects(t *testing.T) {
	ctx := context.Background()
	const ns, msName = "inst-789", "sr-inst-789-miniservice"
	c := fake.NewClientBuilder().WithScheme(newWorkerIdentityScheme(t)).Build()

	require.NoError(t, ensureWorkerIdentity(ctx, c, ns, msName))

	key := client.ObjectKey{Namespace: ns, Name: miniservice.WorkerServiceAccountName}

	sa := &corev1.ServiceAccount{}
	require.NoError(t, c.Get(ctx, key, sa))
	require.NotNil(t, sa.AutomountServiceAccountToken)
	assert.False(t, *sa.AutomountServiceAccountToken)
	assertWorkerIdentityMeta(t, sa.ObjectMeta, msName)

	role := &rbacv1.Role{}
	require.NoError(t, c.Get(ctx, key, role))
	assert.Empty(t, role.Rules)
	assertWorkerIdentityMeta(t, role.ObjectMeta, msName)

	rb := &rbacv1.RoleBinding{}
	require.NoError(t, c.Get(ctx, key, rb))
	assert.Equal(t, rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: miniservice.WorkerServiceAccountName}, rb.RoleRef)
	assert.Equal(t, []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: miniservice.WorkerServiceAccountName, Namespace: ns}}, rb.Subjects)
	assertWorkerIdentityMeta(t, rb.ObjectMeta, msName)

	// Idempotent.
	require.NoError(t, ensureWorkerIdentity(ctx, c, ns, msName))
}

func TestEnsureWorkerIdentity_ResetsDriftedRoleAndBinding(t *testing.T) {
	ctx := context.Background()
	const ns, msName = "inst-789", "sr-inst-789-miniservice"
	name := miniservice.WorkerServiceAccountName

	squattedRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{""},
			Resources: []string{"secrets"},
			Verbs:     []string{"get", "list"},
		}},
	}
	squattedBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name},
		Subjects: []rbacv1.Subject{
			{Kind: rbacv1.ServiceAccountKind, Name: name, Namespace: ns},
			{Kind: rbacv1.ServiceAccountKind, Name: "attacker", Namespace: ns},
		},
	}
	c := fake.NewClientBuilder().WithScheme(newWorkerIdentityScheme(t)).
		WithObjects(squattedRole, squattedBinding).Build()

	require.NoError(t, ensureWorkerIdentity(ctx, c, ns, msName))

	key := client.ObjectKey{Namespace: ns, Name: name}
	role := &rbacv1.Role{}
	require.NoError(t, c.Get(ctx, key, role))
	assert.Empty(t, role.Rules, "worker Role must be reset to grant nothing")

	rb := &rbacv1.RoleBinding{}
	require.NoError(t, c.Get(ctx, key, rb))
	assert.Equal(t, []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: name, Namespace: ns}}, rb.Subjects,
		"worker RoleBinding must be reset to the single worker subject")
}

func TestInjectWorkerTokenVolume(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			ServiceAccountName: "default",
			InitContainers:     []corev1.Container{{Name: "init"}},
			Containers:         []corev1.Container{{Name: "utils"}, {Name: "sidecar"}},
		},
	}

	injectWorkerTokenVolume(pod, "cl-abc123")

	assert.Equal(t, miniservice.WorkerServiceAccountName, pod.Spec.ServiceAccountName)
	require.NotNil(t, pod.Spec.AutomountServiceAccountToken)
	assert.False(t, *pod.Spec.AutomountServiceAccountToken)

	require.Len(t, pod.Spec.Volumes, 1)
	vol := pod.Spec.Volumes[0]
	assert.Equal(t, miniserviceWorkerTokenVolumeName, vol.Name)
	require.NotNil(t, vol.Projected)
	require.Len(t, vol.Projected.Sources, 1)
	sat := vol.Projected.Sources[0].ServiceAccountToken
	require.NotNil(t, sat)
	assert.Equal(t, "nvcf-icms:cl-abc123", sat.Audience)
	require.NotNil(t, sat.ExpirationSeconds)
	assert.EqualValues(t, 900, *sat.ExpirationSeconds)
	assert.Equal(t, "token", sat.Path)

	for _, ctr := range pod.Spec.Containers {
		require.Len(t, ctr.VolumeMounts, 1, ctr.Name)
		assert.Equal(t, miniserviceWorkerTokenMountPath, ctr.VolumeMounts[0].MountPath)
		assert.True(t, ctr.VolumeMounts[0].ReadOnly)
		assert.Contains(t, ctr.Env, corev1.EnvVar{Name: "NVCF_TOKEN_FILE_PATH", Value: "/var/run/secrets/tokens/token"})
		assert.Contains(t, ctr.Env, corev1.EnvVar{Name: "NVCF_IDENTITY_SOURCE", Value: "psat"})
	}
	assert.Empty(t, pod.Spec.InitContainers[0].VolumeMounts, "init containers must not receive the worker token")
	assert.Empty(t, pod.Spec.InitContainers[0].Env)
}
