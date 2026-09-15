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

package teardown

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// RemovePersistent deletes PersistentVolumeClaims across namespaces that match
// labelSelector. Used by `nvcf self-hosted down --remove-persistent` and
// `nvcf self-hosted uninstall --remove-persistent`.
//
// Deletion uses Background propagation so the call returns quickly; actual
// volume reclamation is asynchronous. If a PVC has a finalizer that prevents
// deletion (e.g. kubernetes.io/pvc-protection) the caller should wait for the
// PVC to disappear before declaring success.
func RemovePersistent(
	ctx context.Context,
	kube kubernetes.Interface,
	namespaces []string,
	labelSelector string,
) error {
	propagation := metav1.DeletePropagationBackground
	for _, ns := range namespaces {
		pvcs, err := kube.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector,
		})
		if err != nil {
			return fmt.Errorf("list pvcs in %s: %w", ns, err)
		}
		for _, pvc := range pvcs.Items {
			if err := kube.CoreV1().PersistentVolumeClaims(ns).Delete(ctx, pvc.Name, metav1.DeleteOptions{
				PropagationPolicy: &propagation,
			}); err != nil {
				return fmt.Errorf("delete pvc %s/%s: %w", ns, pvc.Name, err)
			}
		}
	}
	return nil
}
