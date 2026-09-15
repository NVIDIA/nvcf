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

package sharedcluster

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
)

func TestNodeInformer(t *testing.T) {
	// ctx := core.WithDefaultLogger(context.Background())
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	newNode := func(name string, labels ...map[string]string) *v1.Node {
		var ls map[string]string
		if len(labels) != 0 {
			ls = labels[0]
		}
		return &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: ls}}
	}
	nodes := []*v1.Node{
		newNode("node1"),
		newNode("node2", map[string]string{ScheduleLabelKey: trueVal}),
		newNode("node3", map[string]string{ScheduleLabelKey: "false"}),
	}

	k8sClient := fake.NewSimpleClientset(nodes[0])
	f := informers.NewSharedInformerFactoryWithOptions(
		k8sClient,
		0,
	)

	sharedClusterOn, hasSynced, err := AddNodePublisher(ctx, f.Core().V1().Nodes().Informer())
	require.NoError(t, err)

	f.Start(ctx.Done())
	require.True(t, cache.WaitForCacheSync(ctx.Done(), hasSynced), "node informer cache did not sync")

	assert.False(t, sharedClusterOn.Load())

	_, err = k8sClient.CoreV1().Nodes().Create(ctx, nodes[1], metav1.CreateOptions{})
	require.NoError(t, err)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.True(ct, sharedClusterOn.Load())
	}, 2*time.Second, 50*time.Millisecond)

	_, err = k8sClient.CoreV1().Nodes().Create(ctx, nodes[2], metav1.CreateOptions{})
	require.NoError(t, err)

	assert.True(t, sharedClusterOn.Load())

	err = k8sClient.CoreV1().Nodes().Delete(ctx, nodes[1].Name, metav1.DeleteOptions{})
	require.NoError(t, err)

	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.False(ct, sharedClusterOn.Load())
	}, 2*time.Second, 50*time.Millisecond)
}

func TestAddNotify(t *testing.T) {
	// ctx := core.WithDefaultLogger(context.Background())
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	subMu.Lock()
	sem = 0
	subMu.Unlock()

	addNotify(ctx, 0)
	assert.EqualValues(t, 0, sem)
	assert.False(t, sharedClusterOn.Load())

	addNotify(ctx, 1)
	assert.EqualValues(t, 1, sem)
	assert.True(t, sharedClusterOn.Load())

	addNotify(ctx, 1)
	assert.EqualValues(t, 2, sem)
	assert.True(t, sharedClusterOn.Load())

	addNotify(ctx, -2)
	assert.EqualValues(t, 0, sem)
	assert.False(t, sharedClusterOn.Load())

	addNotify(ctx, -1)
	assert.EqualValues(t, 0, sem)
	assert.False(t, sharedClusterOn.Load())

	addNotify(ctx, 1)
	assert.EqualValues(t, 1, sem)
	assert.True(t, sharedClusterOn.Load())
}
