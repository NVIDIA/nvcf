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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var validK8sWorkloadTypes = map[string]bool{
	"deployment":  true,
	"statefulset": true,
	"daemonset":   true,
	"replicaset":  true,
}

// workloadObject is a health-checkable k8s workload. object() returns the
// concrete, scheme-registered API object to fetch into (the wrapper holds it by
// value rather than embedding it, so the client resolves the real kind's GVK);
// replicasReady and podSelector then read from that fetched object.
type workloadObject interface {
	object() client.Object
	replicasReady() error
	podSelector() (labels.Selector, error)
}

type deploymentWorkload struct{ d appsv1.Deployment }

func (w *deploymentWorkload) object() client.Object { return &w.d }
func (w *deploymentWorkload) replicasReady() error {
	if w.d.Status.ReadyReplicas != ptrVal(w.d.Spec.Replicas) || w.d.Status.Replicas != w.d.Status.ReadyReplicas {
		return fmt.Errorf("replicas not ready: desired=%d ready=%d", ptrVal(w.d.Spec.Replicas), w.d.Status.ReadyReplicas)
	}
	return nil
}
func (w *deploymentWorkload) podSelector() (labels.Selector, error) {
	return metav1.LabelSelectorAsSelector(w.d.Spec.Selector)
}

type statefulSetWorkload struct{ s appsv1.StatefulSet }

func (w *statefulSetWorkload) object() client.Object { return &w.s }
func (w *statefulSetWorkload) replicasReady() error {
	if w.s.Status.ReadyReplicas != ptrVal(w.s.Spec.Replicas) || w.s.Status.Replicas != w.s.Status.ReadyReplicas {
		return fmt.Errorf("replicas not ready: desired=%d ready=%d", ptrVal(w.s.Spec.Replicas), w.s.Status.ReadyReplicas)
	}
	return nil
}
func (w *statefulSetWorkload) podSelector() (labels.Selector, error) {
	return metav1.LabelSelectorAsSelector(w.s.Spec.Selector)
}

type daemonSetWorkload struct{ d appsv1.DaemonSet }

func (w *daemonSetWorkload) object() client.Object { return &w.d }
func (w *daemonSetWorkload) replicasReady() error {
	if w.d.Status.NumberReady != w.d.Status.DesiredNumberScheduled {
		return fmt.Errorf("replicas not ready: desired=%d ready=%d", w.d.Status.DesiredNumberScheduled, w.d.Status.NumberReady)
	}
	return nil
}
func (w *daemonSetWorkload) podSelector() (labels.Selector, error) {
	return metav1.LabelSelectorAsSelector(w.d.Spec.Selector)
}

type replicaSetWorkload struct{ r appsv1.ReplicaSet }

func (w *replicaSetWorkload) object() client.Object { return &w.r }
func (w *replicaSetWorkload) replicasReady() error {
	if w.r.Status.ReadyReplicas != ptrVal(w.r.Spec.Replicas) || w.r.Status.Replicas != w.r.Status.ReadyReplicas {
		return fmt.Errorf("replicas not ready: desired=%d ready=%d", ptrVal(w.r.Spec.Replicas), w.r.Status.ReadyReplicas)
	}
	return nil
}
func (w *replicaSetWorkload) podSelector() (labels.Selector, error) {
	return metav1.LabelSelectorAsSelector(w.r.Spec.Selector)
}

func ptrVal(p *int32) int32 {
	if p == nil {
		return 1
	}
	return *p
}

// workloadSelector fetches the workload, checks replicas, and returns the pod label selector.
func (m *Monitor) workloadSelector(ctx context.Context, namespace string, w k8sWorkload) (labels.Selector, error) {
	nn := types.NamespacedName{Namespace: namespace, Name: w.Name}
	if err := m.k8sClient.Get(ctx, nn, w.obj.object()); err != nil {
		return nil, err
	}
	if err := w.obj.replicasReady(); err != nil {
		return nil, err
	}
	return w.obj.podSelector()
}

// checkWorkloads returns nil if every workload's pods are running and ready,
// otherwise an error describing the first problem found. All workloads are
// looked up in the component's namespace.
func (m *Monitor) checkWorkloads(ctx context.Context, index int) error {
	namespace := m.components[index].Namespace
	for _, w := range m.components[index].workloads {
		sel, err := m.workloadSelector(ctx, namespace, w)
		if err != nil {
			return fmt.Errorf("workload %s/%s: %w", namespace, w.Name, err)
		}

		var podList corev1.PodList
		if err := m.k8sClient.List(ctx, &podList, &client.ListOptions{
			Namespace:     namespace,
			LabelSelector: sel,
		}); err != nil {
			return fmt.Errorf("list pods for %s/%s: %w", namespace, w.Name, err)
		}

		for _, pod := range podList.Items {
			if pod.Status.Phase != corev1.PodRunning {
				return fmt.Errorf("pod %s is in phase %s", pod.Name, pod.Status.Phase)
			}
			for _, cs := range pod.Status.ContainerStatuses {
				if !cs.Ready {
					return fmt.Errorf("pod %s container %s is not ready", pod.Name, cs.Name)
				}
			}
		}
	}
	return nil
}
