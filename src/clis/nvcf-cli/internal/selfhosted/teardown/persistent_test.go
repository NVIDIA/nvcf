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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestRemovePersistent_DeletesOnlyLabeled: 3 PVCs in one namespace (2 labeled,
// 1 not). Only the 2 labeled PVCs must be deleted.
func TestRemovePersistent_DeletesOnlyLabeled(t *testing.T) {
	labelKey := "nvcf.nvidia.com/cluster"
	labelVal := "my-cluster"
	selector := labelKey + "=" + labelVal

	pvcLabeled1 := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "data-0",
			Namespace: "nvcf-system",
			Labels:    map[string]string{labelKey: labelVal},
		},
	}
	pvcLabeled2 := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "data-1",
			Namespace: "nvcf-system",
			Labels:    map[string]string{labelKey: labelVal},
		},
	}
	pvcUnlabeled := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unrelated-pvc",
			Namespace: "nvcf-system",
		},
	}

	kube := fake.NewSimpleClientset(pvcLabeled1, pvcLabeled2, pvcUnlabeled)

	err := RemovePersistent(context.Background(), kube, []string{"nvcf-system"}, selector)
	require.NoError(t, err)

	// After deletion, only the unlabeled PVC should remain.
	remaining, err := kube.CoreV1().PersistentVolumeClaims("nvcf-system").List(
		context.Background(), metav1.ListOptions{},
	)
	require.NoError(t, err)
	require.Len(t, remaining.Items, 1)
	assert.Equal(t, "unrelated-pvc", remaining.Items[0].Name)
}

// TestRemovePersistent_MultiNamespace: PVCs spread across two namespaces.
func TestRemovePersistent_MultiNamespace(t *testing.T) {
	labelKey := "nvcf.nvidia.com/cluster"
	labelVal := "x"
	selector := labelKey + "=" + labelVal

	ns1pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vol-ns1",
			Namespace: "ns1",
			Labels:    map[string]string{labelKey: labelVal},
		},
	}
	ns2pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vol-ns2",
			Namespace: "ns2",
			Labels:    map[string]string{labelKey: labelVal},
		},
	}

	kube := fake.NewSimpleClientset(ns1pvc, ns2pvc)

	err := RemovePersistent(context.Background(), kube, []string{"ns1", "ns2"}, selector)
	require.NoError(t, err)

	for _, ns := range []string{"ns1", "ns2"} {
		pvcs, err := kube.CoreV1().PersistentVolumeClaims(ns).List(
			context.Background(), metav1.ListOptions{},
		)
		require.NoError(t, err)
		assert.Empty(t, pvcs.Items, "expected no PVCs in namespace %s after deletion", ns)
	}
}

// TestRemovePersistent_NoMatchingPVCs: selector matches nothing; no error.
func TestRemovePersistent_NoMatchingPVCs(t *testing.T) {
	kube := fake.NewSimpleClientset()
	err := RemovePersistent(context.Background(), kube, []string{"nvcf-system"}, "nvcf.nvidia.com/cluster=ghost")
	require.NoError(t, err)
}
