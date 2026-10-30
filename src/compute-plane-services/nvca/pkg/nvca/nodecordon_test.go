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

package nvca

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	listersv1 "k8s.io/client-go/listers/core/v1"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

var cordonTestNow = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func cordonTestPod(name, node string, worker bool) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "sr-1"},
		Spec:       corev1.PodSpec{NodeName: node},
	}
	if worker {
		p.Labels = map[string]string{nvcatypes.ICMSRequestIDKey: "req-1"}
	}
	return p
}

func cordonTestNode(name string, unschedulable bool) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.NodeSpec{Unschedulable: unschedulable},
	}
}

type cordonTestEnv struct {
	signaler *nodeCordonSignaler
	client   *k8sfake.Clientset
	nodes    cache.Indexer
}

func newCordonTestEnv(t *testing.T, enabled bool, pods ...*corev1.Pod) *cordonTestEnv {
	t.Helper()
	podIdx := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	nodeIdx := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	objs := make([]runtime.Object, 0, len(pods))
	for _, p := range pods {
		require.NoError(t, podIdx.Add(p))
		objs = append(objs, p.DeepCopy())
	}
	client := k8sfake.NewSimpleClientset(objs...)
	fff := &featureflagmock.Fetcher{}
	if enabled {
		fff.EnabledFFs = []*featureflag.FeatureFlag{featureflag.NodeCordonSignal}
	}
	s := newNodeCordonSignaler(listersv1.NewPodLister(podIdx), listersv1.NewNodeLister(nodeIdx), client, fff)
	s.now = func() time.Time { return cordonTestNow }
	t.Cleanup(s.queue.ShutDown)
	return &cordonTestEnv{signaler: s, client: client, nodes: nodeIdx}
}

// annotation returns the cordon annotation of a pod as the API server holds it.
func (e *cordonTestEnv) annotation(t *testing.T, name string) (string, bool) {
	t.Helper()
	pod, err := e.client.CoreV1().Pods("sr-1").Get(context.Background(), name, metav1.GetOptions{})
	require.NoError(t, err)
	v, ok := pod.Annotations[nvcatypes.NodeCordonedAnnotationKey]
	return v, ok
}

func (e *cordonTestEnv) patches() int {
	n := 0
	for _, a := range e.client.Actions() {
		if a.Matches("patch", "pods") {
			n++
		}
	}
	return n
}

func TestNodeCordonSignaler_AnnotatesWorkerPodsOnACordonedNode(t *testing.T) {
	terminating := cordonTestPod("terminating", "node-a", true)
	terminating.DeletionTimestamp = &metav1.Time{Time: cordonTestNow}
	finished := cordonTestPod("finished", "node-a", true)
	finished.Status.Phase = corev1.PodSucceeded
	env := newCordonTestEnv(t, true,
		cordonTestPod("worker-a", "node-a", true),
		cordonTestPod("worker-b", "node-b", true),
		cordonTestPod("not-nvcf", "node-a", false),
		terminating, finished,
	)

	require.NoError(t, env.signaler.reconcileNode(t.Context(), cordonTestNode("node-a", true)))

	got, ok := env.annotation(t, "worker-a")
	assert.True(t, ok)
	assert.Equal(t, cordonTestNow.Format(time.RFC3339), got)
	for _, name := range []string{"worker-b", "not-nvcf", "terminating", "finished"} {
		_, ok := env.annotation(t, name)
		assert.False(t, ok, "%s must not be annotated", name)
	}
}

func TestNodeCordonSignaler_ClearsTheAnnotationOnUncordon(t *testing.T) {
	marked := cordonTestPod("marked", "node-a", true)
	marked.Annotations = map[string]string{nvcatypes.NodeCordonedAnnotationKey: "2026-10-30T11:00:00Z"}
	env := newCordonTestEnv(t, true, marked, cordonTestPod("unmarked", "node-a", true))

	require.NoError(t, env.signaler.reconcileNode(t.Context(), cordonTestNode("node-a", false)))

	_, ok := env.annotation(t, "marked")
	assert.False(t, ok)
	assert.Equal(t, 1, env.patches(), "only the marked pod needs a patch")
}

func TestNodeCordonSignaler_KeepsTheTimeOfAnAnnotationThatIsAlreadySet(t *testing.T) {
	const first = "2026-10-30T11:00:00Z"
	marked := cordonTestPod("marked", "node-a", true)
	marked.Annotations = map[string]string{nvcatypes.NodeCordonedAnnotationKey: first}
	env := newCordonTestEnv(t, true, marked)

	require.NoError(t, env.signaler.reconcileNode(t.Context(), cordonTestNode("node-a", true)))

	got, _ := env.annotation(t, "marked")
	assert.Equal(t, first, got)
	assert.Zero(t, env.patches())
}

func TestNodeCordonSignaler_IgnoresAPodThatDisappeared(t *testing.T) {
	env := newCordonTestEnv(t, true, cordonTestPod("gone", "node-a", true))
	env.client.PrependReactor("patch", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(corev1.Resource("pods"), "gone")
	})

	assert.NoError(t, env.signaler.reconcileNode(t.Context(), cordonTestNode("node-a", true)))
}

func TestNodeCordonSignaler_ReturnsPatchErrorsAfterTryingEveryPod(t *testing.T) {
	env := newCordonTestEnv(t, true,
		cordonTestPod("failing", "node-a", true),
		cordonTestPod("healthy", "node-a", true),
	)
	env.client.PrependReactor("patch", "pods", func(a clienttesting.Action) (bool, runtime.Object, error) {
		if a.(clienttesting.PatchAction).GetName() == "failing" {
			return true, nil, errors.New("boom")
		}
		return false, nil, nil
	})

	err := env.signaler.reconcileNode(t.Context(), cordonTestNode("node-a", true))

	require.ErrorContains(t, err, "boom")
	_, ok := env.annotation(t, "healthy")
	assert.True(t, ok, "one failing pod must not stop the others")
}

func TestNodeCordonSignaler_ProcessNextItem(t *testing.T) {
	t.Run("annotates the pods of the queued node", func(t *testing.T) {
		env := newCordonTestEnv(t, true, cordonTestPod("worker-a", "node-a", true))
		require.NoError(t, env.nodes.Add(cordonTestNode("node-a", true)))
		env.signaler.queue.Add("node-a")

		assert.True(t, env.signaler.processNextItem(t.Context()))

		_, ok := env.annotation(t, "worker-a")
		assert.True(t, ok)
	})

	t.Run("does nothing while the feature flag is off", func(t *testing.T) {
		env := newCordonTestEnv(t, false, cordonTestPod("worker-a", "node-a", true))
		require.NoError(t, env.nodes.Add(cordonTestNode("node-a", true)))
		env.signaler.queue.Add("node-a")

		assert.True(t, env.signaler.processNextItem(t.Context()))

		assert.Zero(t, env.patches())
	})

	t.Run("drops a node that no longer exists", func(t *testing.T) {
		env := newCordonTestEnv(t, true, cordonTestPod("worker-a", "node-a", true))
		env.signaler.queue.Add("node-a")

		assert.True(t, env.signaler.processNextItem(t.Context()))

		assert.Zero(t, env.patches())
		assert.Zero(t, env.signaler.queue.NumRequeues("node-a"))
	})

	t.Run("requeues a node whose pods could not be patched", func(t *testing.T) {
		env := newCordonTestEnv(t, true, cordonTestPod("worker-a", "node-a", true))
		require.NoError(t, env.nodes.Add(cordonTestNode("node-a", true)))
		env.client.PrependReactor("patch", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("boom")
		})
		env.signaler.queue.Add("node-a")

		assert.True(t, env.signaler.processNextItem(t.Context()))

		assert.Equal(t, 1, env.signaler.queue.NumRequeues("node-a"))
	})
}

func TestShouldReconcileNodeUpdate(t *testing.T) {
	node := func(unschedulable bool, rv string) *corev1.Node {
		n := cordonTestNode("node-a", unschedulable)
		n.ResourceVersion = rv
		return n
	}
	tests := []struct {
		name     string
		old, new *corev1.Node
		want     bool
	}{
		{name: "cordoned", old: node(false, "1"), new: node(true, "2"), want: true},
		{name: "uncordoned", old: node(true, "1"), new: node(false, "2"), want: true},
		{name: "periodic resync of an unchanged node", old: node(true, "5"), new: node(true, "5"), want: true},
		{name: "unrelated node change", old: node(true, "5"), new: node(true, "6"), want: false},
		{name: "unrelated change on a schedulable node", old: node(false, "5"), new: node(false, "6"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, shouldReconcileNodeUpdate(tt.old, tt.new))
		})
	}
}
