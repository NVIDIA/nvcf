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

package miniservice

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func podTemplate(sa string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{Spec: corev1.PodSpec{ServiceAccountName: sa}}
}

func TestValidateWorkloadObject(t *testing.T) {
	tests := []struct {
		name    string
		obj     client.Object
		wantErr string
	}{
		{name: "nil object", obj: nil},
		{name: "pod with exact worker SA", obj: &corev1.Pod{Spec: corev1.PodSpec{ServiceAccountName: "nvcf-worker"}}, wantErr: "worker ServiceAccount"},
		{name: "pod with prefixed worker SA", obj: &corev1.Pod{Spec: corev1.PodSpec{ServiceAccountName: "nvcf-worker-inst-1"}}, wantErr: "worker ServiceAccount"},
		{name: "pod with regular SA", obj: &corev1.Pod{Spec: corev1.PodSpec{ServiceAccountName: "miniservice-instance-permissions"}}},
		{name: "pod with empty SA", obj: &corev1.Pod{}},
		{name: "pod with similar but distinct SA", obj: &corev1.Pod{Spec: corev1.PodSpec{ServiceAccountName: "nvcf-workers"}}},
		{name: "deployment", obj: &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Template: podTemplate("nvcf-worker")}}, wantErr: "worker ServiceAccount"},
		{name: "replicaset", obj: &appsv1.ReplicaSet{Spec: appsv1.ReplicaSetSpec{Template: podTemplate("nvcf-worker")}}, wantErr: "worker ServiceAccount"},
		{name: "statefulset", obj: &appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{Template: podTemplate("nvcf-worker")}}, wantErr: "worker ServiceAccount"},
		{name: "daemonset", obj: &appsv1.DaemonSet{Spec: appsv1.DaemonSetSpec{Template: podTemplate("nvcf-worker")}}, wantErr: "worker ServiceAccount"},
		{name: "job", obj: &batchv1.Job{Spec: batchv1.JobSpec{Template: podTemplate("nvcf-worker")}}, wantErr: "worker ServiceAccount"},
		{name: "cronjob", obj: &batchv1.CronJob{Spec: batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: podTemplate("nvcf-worker")}}}}, wantErr: "worker ServiceAccount"},
		{name: "deployment with regular SA", obj: &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Template: podTemplate("app")}}},
		{name: "serviceaccount named nvcf-worker", obj: &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "nvcf-worker"}}, wantErr: "reserved worker identity name"},
		{name: "serviceaccount with other name", obj: &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "app"}}},
		{name: "role named nvcf-worker", obj: &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "nvcf-worker"}}, wantErr: "reserved worker identity name"},
		{name: "rolebinding named nvcf-worker", obj: &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "nvcf-worker"}}, wantErr: "reserved worker identity name"},
		{
			name: "rolebinding granting to worker SA",
			obj: &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "app-binding"},
				Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "nvcf-worker"}},
			},
			wantErr: "grants permissions to worker ServiceAccount",
		},
		{
			name: "rolebinding granting to app SA",
			obj: &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "app-binding"},
				Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "app"}},
			},
		},
		{
			name: "workload object claiming infra annotation",
			obj: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
				Name:        "cm",
				Annotations: map[string]string{nvcatypes.InfraObjectAnnotationKey: "true"},
			}},
			wantErr: "may not carry",
		},
		{name: "unrelated kind", obj: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWorkloadObject(tt.obj)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestValidateWorkloadObjects_JoinsErrors(t *testing.T) {
	err := ValidateWorkloadObjects([]client.Object{
		&corev1.Pod{Spec: corev1.PodSpec{ServiceAccountName: "nvcf-worker"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "ok"}},
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "nvcf-worker"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worker ServiceAccount")
	assert.Contains(t, err.Error(), "reserved worker identity name")
	assert.NoError(t, ValidateWorkloadObjects(nil))
}

func TestBuildWorkerAuth(t *testing.T) {
	const ns = "inst-789"
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "nvcf-worker", Namespace: ns, UID: types.UID("sa-uid")}}
	infra := map[string]string{nvcatypes.InfraObjectAnnotationKey: "true"}
	workerPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "utils", Namespace: ns, UID: types.UID("pod-uid"), Annotations: infra},
		Spec:       corev1.PodSpec{ServiceAccountName: "nvcf-worker"},
	}

	t.Run("registers infra-owned worker pod", func(t *testing.T) {
		auth := BuildWorkerAuth(ns, sa, workerPod)
		require.NotNil(t, auth)
		assert.Equal(t, "system:serviceaccount:inst-789:nvcf-worker", auth.Sub)
		assert.Equal(t, ns, auth.Namespace)
		assert.Equal(t, "sa-uid", auth.SAuid)
		assert.Equal(t, []nvcatypes.WorkerIdentifier{{Name: "utils", UID: "pod-uid"}}, auth.WorkerIdentifiers)
	})

	t.Run("ignores pods that bind the SA without the infra annotation", func(t *testing.T) {
		rogue := workerPod.DeepCopy()
		rogue.Name = "rogue"
		rogue.UID = types.UID("rogue-uid")
		rogue.Annotations = nil
		auth := BuildWorkerAuth(ns, sa, workerPod, rogue)
		require.NotNil(t, auth)
		assert.Len(t, auth.WorkerIdentifiers, 1)
		assert.Equal(t, "utils", auth.WorkerIdentifiers[0].Name)
	})

	t.Run("ignores pods with another SA or namespace", func(t *testing.T) {
		otherSA := workerPod.DeepCopy()
		otherSA.Spec.ServiceAccountName = "default"
		otherNS := workerPod.DeepCopy()
		otherNS.Namespace = "elsewhere"
		assert.Nil(t, BuildWorkerAuth(ns, sa, otherSA, otherNS))
	})

	t.Run("nil when SA is missing, misnamed, or has no UID", func(t *testing.T) {
		assert.Nil(t, BuildWorkerAuth(ns, nil, workerPod))
		assert.Nil(t, BuildWorkerAuth(ns, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: ns, UID: "x"}}, workerPod))
		assert.Nil(t, BuildWorkerAuth(ns, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "nvcf-worker", Namespace: ns}}, workerPod))
	})
}

func TestWorkerSubjectAndNames(t *testing.T) {
	assert.Equal(t, "system:serviceaccount:ns-a:nvcf-worker", WorkerSubject("ns-a"))
	assert.True(t, IsWorkerServiceAccountName("nvcf-worker"))
	assert.True(t, IsWorkerServiceAccountName("nvcf-worker-abc"))
	assert.False(t, IsWorkerServiceAccountName("nvcf-workers"))
	assert.False(t, IsWorkerServiceAccountName(""))
}
