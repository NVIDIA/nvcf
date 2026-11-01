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

package hostisolation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func Test_validateNoMixedTenants(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	k8sClient := k8sfake.NewSimpleClientset()
	infFac := informers.NewSharedInformerFactory(k8sClient, 0)
	pi := infFac.Core().V1().Pods()
	pii, pil := pi.Informer(), pi.Lister()
	ni := infFac.Core().V1().Nodes()
	nii, ndil := ni.Informer(), ni.Lister()

	infFac.Start(ctx.Done())

	var err error

	// No pods or nodes.
	err = validateNoMixedTenants(ctx, ndil, pil)
	require.NoError(t, err)

	// Nodes no pods.
	node1 := &corev1.Node{}
	node1.Name = "node1"
	_, err = k8sClient.CoreV1().Nodes().Create(ctx, node1, metav1.CreateOptions{})
	require.NoError(t, err)
	cache.WaitForCacheSync(ctx.Done(), nii.HasSynced, pii.HasSynced)
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		nodes, err := ndil.List(labels.Everything())
		if assert.NoError(ct, err) {
			assert.Len(ct, nodes, 1)
		}
	}, 2*time.Second, 50*time.Millisecond)

	err = validateNoMixedTenants(ctx, ndil, pil)
	require.NoError(t, err)

	// Pod not on node.
	pod1 := &corev1.Pod{}
	pod1.Name = "pod1"
	pod1.Labels = map[string]string{types.NCAIDKey: types.MakeNCAIDLabelValue("nca1")}
	pod1.Spec.NodeName = "node2"
	_, err = k8sClient.CoreV1().Pods("default").Create(ctx, pod1, metav1.CreateOptions{})
	require.NoError(t, err)
	cache.WaitForCacheSync(ctx.Done(), nii.HasSynced, pii.HasSynced)
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		pods, err := pil.List(labels.NewSelector().Add(ncaIDReq))
		if assert.NoError(ct, err) {
			assert.Len(ct, pods, 1)
		}
	}, 2*time.Second, 100*time.Millisecond)

	err = validateNoMixedTenants(ctx, ndil, pil)
	require.NoError(t, err)

	// Pod on node.
	pod2 := &corev1.Pod{}
	pod2.Name = "pod2"
	pod2.Labels = map[string]string{types.NCAIDKey: types.MakeNCAIDLabelValue("nca1")}
	pod2.Spec.NodeName = "node1"
	_, err = k8sClient.CoreV1().Pods("default").Create(ctx, pod2, metav1.CreateOptions{})
	require.NoError(t, err)
	cache.WaitForCacheSync(ctx.Done(), nii.HasSynced, pii.HasSynced)
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		pods, err := pil.List(labels.NewSelector().Add(ncaIDReq))
		if assert.NoError(ct, err) {
			assert.Len(ct, pods, 2)
		}
	}, 2*time.Second, 100*time.Millisecond)

	err = validateNoMixedTenants(ctx, ndil, pil)
	require.NoError(t, err)

	// Pod of different tenant on node.
	pod3 := &corev1.Pod{}
	pod3.Name = "pod3"
	pod3.Labels = map[string]string{types.NCAIDKey: types.MakeNCAIDLabelValue("nca2")}
	pod3.Spec.NodeName = "node1"
	_, err = k8sClient.CoreV1().Pods("other").Create(ctx, pod3, metav1.CreateOptions{})
	require.NoError(t, err)
	cache.WaitForCacheSync(ctx.Done(), nii.HasSynced, pii.HasSynced)
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		pods, err := pil.List(labels.NewSelector().Add(ncaIDReq))
		if assert.NoError(ct, err) {
			assert.Len(ct, pods, 3)
		}
	}, 2*time.Second, 100*time.Millisecond)

	err = validateNoMixedTenants(ctx, ndil, pil)
	require.EqualError(t, err, "mixed tenants on nodes: node1=nca1,nca2")
}
