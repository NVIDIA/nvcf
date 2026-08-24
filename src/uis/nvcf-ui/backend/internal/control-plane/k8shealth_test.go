// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controlplane

import (
	"context"
	"fmt"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func ptr[T any](v T) *T { return &v }

func TestReplicasReady(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		obj     workloadObject
		wantErr bool
	}{
		{
			name:    "deployment ready",
			obj:     &deploymentWorkload{appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: ptr[int32](3)}, Status: appsv1.DeploymentStatus{Replicas: 3, ReadyReplicas: 3}}},
			wantErr: false,
		},
		{
			name:    "deployment not ready",
			obj:     &deploymentWorkload{appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: ptr[int32](3)}, Status: appsv1.DeploymentStatus{Replicas: 3, ReadyReplicas: 2}}},
			wantErr: true,
		},
		{
			name:    "deployment nil replicas defaults to one",
			obj:     &deploymentWorkload{appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: nil}, Status: appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1}}},
			wantErr: false,
		},
		{
			name:    "statefulset ready",
			obj:     &statefulSetWorkload{appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{Replicas: ptr[int32](2)}, Status: appsv1.StatefulSetStatus{Replicas: 2, ReadyReplicas: 2}}},
			wantErr: false,
		},
		{
			name:    "statefulset not ready",
			obj:     &statefulSetWorkload{appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{Replicas: ptr[int32](2)}, Status: appsv1.StatefulSetStatus{Replicas: 1, ReadyReplicas: 1}}},
			wantErr: true,
		},
		{
			name:    "daemonset ready",
			obj:     &daemonSetWorkload{appsv1.DaemonSet{Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 4, NumberReady: 4}}},
			wantErr: false,
		},
		{
			name:    "daemonset not ready",
			obj:     &daemonSetWorkload{appsv1.DaemonSet{Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 4, NumberReady: 3}}},
			wantErr: true,
		},
		{
			name:    "replicaset ready",
			obj:     &replicaSetWorkload{appsv1.ReplicaSet{Spec: appsv1.ReplicaSetSpec{Replicas: ptr[int32](1)}, Status: appsv1.ReplicaSetStatus{Replicas: 1, ReadyReplicas: 1}}},
			wantErr: false,
		},
		{
			name:    "replicaset not ready",
			obj:     &replicaSetWorkload{appsv1.ReplicaSet{Spec: appsv1.ReplicaSetSpec{Replicas: ptr[int32](1)}, Status: appsv1.ReplicaSetStatus{Replicas: 1, ReadyReplicas: 0}}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.obj.replicasReady(); (err != nil) != tt.wantErr {
				t.Errorf("replicasReady() err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestWorkloadAccessors(t *testing.T) {
	t.Parallel()

	sel := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}}

	tests := []struct {
		name string
		obj  workloadObject
	}{
		{name: "deployment", obj: &deploymentWorkload{appsv1.Deployment{Spec: appsv1.DeploymentSpec{Selector: sel}}}},
		{name: "statefulset", obj: &statefulSetWorkload{appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{Selector: sel}}}},
		{name: "daemonset", obj: &daemonSetWorkload{appsv1.DaemonSet{Spec: appsv1.DaemonSetSpec{Selector: sel}}}},
		{name: "replicaset", obj: &replicaSetWorkload{appsv1.ReplicaSet{Spec: appsv1.ReplicaSetSpec{Selector: sel}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.obj.object() == nil {
				t.Error("object() returned nil")
			}
			s, err := tt.obj.podSelector()
			if err != nil {
				t.Fatalf("podSelector() error: %v", err)
			}
			if got := s.String(); got != "app=x" {
				t.Errorf("selector = %q, want app=x", got)
			}
		})
	}
}

func TestCheckWorkloads(t *testing.T) {
	t.Parallel()

	const ns, name = "default", "web"
	selLabels := map[string]string{"app": "web"}

	deploy := func(replicas, ready int32) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(replicas),
				Selector: &metav1.LabelSelector{MatchLabels: selLabels},
			},
			Status: appsv1.DeploymentStatus{Replicas: replicas, ReadyReplicas: ready},
		}
	}
	pod := func(phase corev1.PodPhase, ready bool) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "web-abc", Labels: selLabels},
			Status: corev1.PodStatus{
				Phase:             phase,
				ContainerStatuses: []corev1.ContainerStatus{{Name: "c", Ready: ready}},
			},
		}
	}

	tests := []struct {
		name    string
		objects []client.Object // seeded into the fake cluster; nil => deployment absent
		listErr bool            // make the pod List call fail
		wantErr bool
	}{
		{name: "all healthy", objects: []client.Object{deploy(1, 1), pod(corev1.PodRunning, true)}, wantErr: false},
		{name: "deployment missing", objects: nil, wantErr: true},
		{name: "replicas not ready", objects: []client.Object{deploy(2, 1)}, wantErr: true},
		{name: "pod not running", objects: []client.Object{deploy(1, 1), pod(corev1.PodPending, true)}, wantErr: true},
		{name: "pod container not ready", objects: []client.Object{deploy(1, 1), pod(corev1.PodRunning, false)}, wantErr: true},
		{name: "list pods fails", objects: []client.Object{deploy(1, 1)}, listErr: true, wantErr: true},
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.objects...)
			if tt.listErr {
				builder = builder.WithInterceptorFuncs(interceptor.Funcs{
					List: func(_ context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) error {
						return fmt.Errorf("list failed")
					},
				})
			}
			cl := builder.Build()
			m := &Monitor{
				k8sClient: cl,
				components: []componentHealth{{
					Namespace: ns,
					workloads: []k8sWorkload{{Name: name, Type: "deployment", obj: &deploymentWorkload{}}},
				}},
			}

			if err := m.checkWorkloads(context.Background(), 0); (err != nil) != tt.wantErr {
				t.Errorf("checkWorkloads() err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
