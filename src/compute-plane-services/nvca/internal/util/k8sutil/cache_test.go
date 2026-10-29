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

package k8sutil

import (
	"fmt"
	"os"
	goruntime "runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/yaml"
)

func testManagedFields() []metav1.ManagedFieldsEntry {
	return []metav1.ManagedFieldsEntry{
		{
			Manager:    "creator",
			Operation:  metav1.ManagedFieldsOperationUpdate,
			APIVersion: "v1",
			FieldsType: "FieldsV1",
			FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:metadata":{"f:labels":{"f:app":{}}}}`)},
		},
		{
			Manager:     "kubelet",
			Operation:   metav1.ManagedFieldsOperationUpdate,
			APIVersion:  "v1",
			FieldsType:  "FieldsV1",
			FieldsV1:    &metav1.FieldsV1{Raw: []byte(`{"f:status":{"f:phase":{}}}`)},
			Subresource: "status",
		},
	}
}

func testPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:          "pod-1",
			Namespace:     "ns-1",
			Labels:        map[string]string{"app": "test"},
			ManagedFields: testManagedFields(),
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func TestStripManagedFields(t *testing.T) {
	t.Run("drops managedFields and keeps the rest of the object", func(t *testing.T) {
		pod := testPod()
		want := pod.DeepCopy()
		want.ManagedFields = nil

		out, err := StripManagedFields(pod)
		require.NoError(t, err)
		assert.Same(t, pod, out, "the transform updates the object in place")
		assert.Equal(t, want, out)
	})

	t.Run("is idempotent", func(t *testing.T) {
		once, err := StripManagedFields(testPod())
		require.NoError(t, err)
		want := once.(*corev1.Pod).DeepCopy()

		twice, err := StripManagedFields(once)
		require.NoError(t, err)
		assert.Equal(t, want, twice)
	})

	t.Run("drops managedFields from an unstructured object", func(t *testing.T) {
		raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(testPod())
		require.NoError(t, err)
		u := &unstructured.Unstructured{Object: raw}
		require.NotEmpty(t, u.GetManagedFields())

		out, err := StripManagedFields(u)
		require.NoError(t, err)
		assert.Empty(t, out.(*unstructured.Unstructured).GetManagedFields())
		assert.Equal(t, map[string]string{"app": "test"}, u.GetLabels())
	})

	t.Run("passes a tombstone through unchanged", func(t *testing.T) {
		tombstone := cache.DeletedFinalStateUnknown{Key: "ns-1/pod-1", Obj: testPod()}

		out, err := StripManagedFields(tombstone)
		require.NoError(t, err)
		assert.Equal(t, cache.DeletedFinalStateUnknown{Key: "ns-1/pod-1", Obj: testPod()}, out)
	})

	t.Run("passes values that are not objects through unchanged", func(t *testing.T) {
		for _, in := range []any{nil, "ns-1/pod-1", 42, struct{ Name string }{Name: "pod-1"}} {
			out, err := StripManagedFields(in)
			require.NoError(t, err)
			assert.Equal(t, in, out)
		}
	})
}

// BenchmarkStripManagedFieldsPodCache reports the heap that an informer store
// keeps per cached pod, without and with StripManagedFields, and the ratio of
// the two. Pods are decoded from protobuf, as informers decode them. The
// fixture carries the managedFields that an API server recorded for three
// field managers.
func BenchmarkStripManagedFieldsPodCache(b *testing.B) {
	data, err := os.ReadFile("testdata/pod_with_managed_fields.yaml")
	require.NoError(b, err)
	pod := &corev1.Pod{}
	require.NoError(b, yaml.Unmarshal(data, pod))
	require.Len(b, pod.ManagedFields, 3)
	encoded, err := pod.Marshal()
	require.NoError(b, err)

	const pods = 1000
	var full, stripped float64
	for i := 0; i < b.N; i++ {
		full = cachedPodHeapBytes(b, encoded, pods, nil)
		stripped = cachedPodHeapBytes(b, encoded, pods, StripManagedFields)
	}
	b.ReportMetric(full, "B/pod-full")
	b.ReportMetric(stripped, "B/pod-stripped")
	b.ReportMetric(stripped/full, "stripped/full")
}

// cachedPodHeapBytes decodes n pods into an informer store, passing each
// through transform when it is set, and returns the heap the store keeps per
// pod.
func cachedPodHeapBytes(b *testing.B, encoded []byte, n int, transform cache.TransformFunc) float64 {
	b.Helper()
	store := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{
		cache.NamespaceIndex: cache.MetaNamespaceIndexFunc,
	})
	before := liveHeapBytes()
	for i := range n {
		pod := &corev1.Pod{}
		require.NoError(b, pod.Unmarshal(encoded))
		pod.Name = fmt.Sprintf("pod-%d", i)
		var obj any = pod
		if transform != nil {
			var err error
			obj, err = transform(pod)
			require.NoError(b, err)
		}
		require.NoError(b, store.Add(obj))
	}
	after := liveHeapBytes()
	goruntime.KeepAlive(store)
	return float64(int64(after)-int64(before)) / float64(n)
}

func liveHeapBytes() uint64 {
	goruntime.GC()
	var stats goruntime.MemStats
	goruntime.ReadMemStats(&stats)
	return stats.HeapAlloc
}
