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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestGVKCache_ConcurrentAccess(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))

	cache := newGVKCache(scheme)

	objectTypes := []client.Object{
		&corev1.Pod{},
		&corev1.Service{},
		&corev1.ConfigMap{},
		&corev1.Secret{},
		&corev1.Namespace{},
		&appsv1.Deployment{},
		&appsv1.ReplicaSet{},
		&appsv1.StatefulSet{},
	}

	const numGoroutines = 100
	const iterationsPerGoroutine = 50

	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines*iterationsPerGoroutine)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for j := 0; j < iterationsPerGoroutine; j++ {
				obj := objectTypes[(goroutineID+j)%len(objectTypes)]
				gvk, err := cache.Get(obj)
				if err != nil {
					errors <- err
					return
				}
				if gvk.Kind == "" {
					errors <- assert.AnError
					return
				}
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	var errs []error
	for err := range errors {
		errs = append(errs, err)
	}
	assert.Empty(t, errs, "concurrent access should not produce errors")
}

func TestGVKCache_ConcurrentPrePopulateAndGet(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))

	cache := newGVKCache(scheme)

	const numGoroutines = 50
	const iterations = 30

	var wg sync.WaitGroup

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				if id%2 == 0 {
					cache.PrePopulate(&corev1.Pod{}, corev1.SchemeGroupVersion.WithKind("Pod"))
					cache.PrePopulate(&appsv1.Deployment{}, appsv1.SchemeGroupVersion.WithKind("Deployment"))
				} else {
					_, _ = cache.Get(&corev1.Pod{})
					_, _ = cache.Get(&appsv1.Deployment{})
				}
			}
		}(i)
	}

	wg.Wait()
}

func TestGVKCache_ContextIntegration(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	cache := newGVKCache(scheme)
	ctx := context.Background()

	t.Run("no cache in context falls back to scheme lookup", func(t *testing.T) {
		gvk, err := getObjectGVK(ctx, scheme, &corev1.Pod{})
		require.NoError(t, err)
		assert.Equal(t, "Pod", gvk.Kind)
		assert.Equal(t, "v1", gvk.Version)
	})

	t.Run("cache in context uses cache", func(t *testing.T) {
		ctxWithCache := withGVKCache(ctx, cache)

		gvk, err := getObjectGVK(ctxWithCache, scheme, &corev1.Pod{})
		require.NoError(t, err)
		assert.Equal(t, "Pod", gvk.Kind)

		gvk2, err := getObjectGVK(ctxWithCache, scheme, &corev1.Pod{})
		require.NoError(t, err)
		assert.Equal(t, gvk, gvk2)
	})

	t.Run("getGVKCache returns nil when not in context", func(t *testing.T) {
		assert.Nil(t, getGVKCache(ctx))
	})

	t.Run("getGVKCache returns cache when in context", func(t *testing.T) {
		ctxWithCache := withGVKCache(ctx, cache)
		assert.Same(t, cache, getGVKCache(ctxWithCache))
	})
}

func TestGVKCache_ConcurrentContextUsage(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))

	cache := newGVKCache(scheme)
	ctx := withGVKCache(context.Background(), cache)

	const numGoroutines = 100
	const iterations = 50

	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines*iterations)

	objectTypes := []client.Object{
		&corev1.Pod{},
		&corev1.Service{},
		&appsv1.Deployment{},
		&appsv1.ReplicaSet{},
	}

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				obj := objectTypes[(id+j)%len(objectTypes)]
				gvk, err := getObjectGVK(ctx, scheme, obj)
				if err != nil {
					errors <- err
					return
				}
				if gvk.Kind == "" {
					t.Errorf("goroutine %d iteration %d: got empty Kind", id, j)
					return
				}
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	var errs []error
	for err := range errors {
		errs = append(errs, err)
	}
	assert.Empty(t, errs)
}

func TestGetObjectGVKOrUnknown(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	ctx := context.Background()

	t.Run("returns GVK for known type", func(t *testing.T) {
		gvk := getObjectGVKOrUnknown(ctx, scheme, &corev1.Pod{})
		assert.Equal(t, "Pod", gvk.Kind)
		assert.Equal(t, "v1", gvk.Version)
	})

	t.Run("returns unknownGVK for unregistered type", func(t *testing.T) {
		emptyScheme := runtime.NewScheme()
		gvk := getObjectGVKOrUnknown(ctx, emptyScheme, &corev1.Pod{})
		assert.Equal(t, unknownGVK, gvk)
	})

	t.Run("returns object's existing GVK if set", func(t *testing.T) {
		pod := &corev1.Pod{}
		pod.SetGroupVersionKind(schema.GroupVersionKind{Group: "custom", Version: "v99", Kind: "CustomPod"})
		gvk := getObjectGVKOrUnknown(ctx, scheme, pod)
		assert.Equal(t, "CustomPod", gvk.Kind)
		assert.Equal(t, "v99", gvk.Version)
		assert.Equal(t, "custom", gvk.Group)
	})
}

func TestGVKCache_PrePopulate(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))

	t.Run("PrePopulate stores GVK and Get returns it", func(t *testing.T) {
		cache := newGVKCache(scheme)
		expectedGVK := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"}

		cache.PrePopulate(&corev1.Pod{}, expectedGVK)

		gvk, err := cache.Get(&corev1.Pod{})
		require.NoError(t, err)
		assert.Equal(t, expectedGVK, gvk)
	})

	t.Run("PrePopulate with custom GVK overrides scheme lookup", func(t *testing.T) {
		cache := newGVKCache(scheme)
		customGVK := schema.GroupVersionKind{Group: "custom", Version: "v99", Kind: "CustomPod"}

		cache.PrePopulate(&corev1.Pod{}, customGVK)

		gvk, err := cache.Get(&corev1.Pod{})
		require.NoError(t, err)
		assert.Equal(t, customGVK, gvk)
	})

	t.Run("PrePopulate works without scheme registration", func(t *testing.T) {
		emptyScheme := runtime.NewScheme()
		cache := newGVKCache(emptyScheme)
		expectedGVK := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"}

		cache.PrePopulate(&corev1.Pod{}, expectedGVK)

		gvk, err := cache.Get(&corev1.Pod{})
		require.NoError(t, err)
		assert.Equal(t, expectedGVK, gvk)
	})

	t.Run("Get without PrePopulate falls back to scheme", func(t *testing.T) {
		cache := newGVKCache(scheme)

		gvk, err := cache.Get(&corev1.Pod{})
		require.NoError(t, err)
		assert.Equal(t, "Pod", gvk.Kind)
		assert.Equal(t, "v1", gvk.Version)
	})

	t.Run("Get populates cache on miss", func(t *testing.T) {
		cache := newGVKCache(scheme)

		gvk1, err := cache.Get(&corev1.Pod{})
		require.NoError(t, err)

		gvk2, err := cache.Get(&corev1.Pod{})
		require.NoError(t, err)

		assert.Equal(t, gvk1, gvk2)
	})

	t.Run("multiple types can be pre-populated", func(t *testing.T) {
		cache := newGVKCache(scheme)

		cache.PrePopulate(&corev1.Pod{}, corev1.SchemeGroupVersion.WithKind("Pod"))
		cache.PrePopulate(&appsv1.Deployment{}, appsv1.SchemeGroupVersion.WithKind("Deployment"))
		cache.PrePopulate(&appsv1.ReplicaSet{}, appsv1.SchemeGroupVersion.WithKind("ReplicaSet"))

		podGVK, err := cache.Get(&corev1.Pod{})
		require.NoError(t, err)
		assert.Equal(t, "Pod", podGVK.Kind)

		depGVK, err := cache.Get(&appsv1.Deployment{})
		require.NoError(t, err)
		assert.Equal(t, "Deployment", depGVK.Kind)
		assert.Equal(t, "apps", depGVK.Group)

		rsGVK, err := cache.Get(&appsv1.ReplicaSet{})
		require.NoError(t, err)
		assert.Equal(t, "ReplicaSet", rsGVK.Kind)
	})
}

func TestGVKCache_ControllerPrePopulation(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))

	cache := newGVKCache(scheme)
	cache.PrePopulate(&corev1.Pod{}, corev1.SchemeGroupVersion.WithKind("Pod"))
	cache.PrePopulate(&appsv1.Deployment{}, appsv1.SchemeGroupVersion.WithKind("Deployment"))
	cache.PrePopulate(&appsv1.ReplicaSet{}, appsv1.SchemeGroupVersion.WithKind("ReplicaSet"))
	cache.PrePopulate(&appsv1.StatefulSet{}, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))
	cache.PrePopulate(&batchv1.Job{}, batchv1.SchemeGroupVersion.WithKind("Job"))
	cache.PrePopulate(&batchv1.CronJob{}, batchv1.SchemeGroupVersion.WithKind("CronJob"))
	cache.PrePopulate(&corev1.Secret{}, corev1.SchemeGroupVersion.WithKind("Secret"))

	tests := []struct {
		name        string
		obj         client.Object
		expectedGVK schema.GroupVersionKind
	}{
		{
			name:        "Pod",
			obj:         &corev1.Pod{},
			expectedGVK: schema.GroupVersionKind{Version: "v1", Kind: "Pod"},
		},
		{
			name:        "Deployment",
			obj:         &appsv1.Deployment{},
			expectedGVK: schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		},
		{
			name:        "ReplicaSet",
			obj:         &appsv1.ReplicaSet{},
			expectedGVK: schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "ReplicaSet"},
		},
		{
			name:        "StatefulSet",
			obj:         &appsv1.StatefulSet{},
			expectedGVK: schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "StatefulSet"},
		},
		{
			name:        "Job",
			obj:         &batchv1.Job{},
			expectedGVK: schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"},
		},
		{
			name:        "CronJob",
			obj:         &batchv1.CronJob{},
			expectedGVK: schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "CronJob"},
		},
		{
			name:        "Secret",
			obj:         &corev1.Secret{},
			expectedGVK: schema.GroupVersionKind{Version: "v1", Kind: "Secret"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gvk, err := cache.Get(tt.obj)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedGVK, gvk)
		})
	}
}
