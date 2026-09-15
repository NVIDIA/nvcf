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

package cleanup

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestNewSentinelWatcher(t *testing.T) {
	k8sClient := fake.NewSimpleClientset()
	onShutdown := func(ctx context.Context) {}

	watcher := NewSentinelWatcher("test-namespace", k8sClient, onShutdown)

	assert.NotNil(t, watcher)
	assert.Equal(t, "test-namespace", watcher.namespace)
	assert.NotNil(t, watcher.k8sClient)
	assert.NotNil(t, watcher.onShutdown)
}

func TestSentinelWatcher_TriggerShutdown_OnlyOnce(t *testing.T) {
	k8sClient := fake.NewSimpleClientset()
	var callCount int
	var mu sync.Mutex

	onShutdown := func(ctx context.Context) {
		mu.Lock()
		callCount++
		mu.Unlock()
	}

	watcher := NewSentinelWatcher("test-namespace", k8sClient, onShutdown)

	ctx := context.Background()

	// Call triggerShutdown multiple times
	watcher.triggerShutdown(ctx)
	watcher.triggerShutdown(ctx)
	watcher.triggerShutdown(ctx)

	// Verify onShutdown was called only once
	mu.Lock()
	assert.Equal(t, 1, callCount, "onShutdown should only be called once")
	mu.Unlock()
}

func TestSentinelWatcher_TriggerShutdown_NilCallback(t *testing.T) {
	k8sClient := fake.NewSimpleClientset()

	// Create watcher with nil callback - should not panic
	watcher := NewSentinelWatcher("test-namespace", k8sClient, nil)

	ctx := context.Background()

	// Should not panic
	assert.NotPanics(t, func() {
		watcher.triggerShutdown(ctx)
	})
}

func TestSentinelWatcher_Start_InitialStateDeleting(t *testing.T) {
	// Create sentinel with deletion timestamp
	sentinel := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:              ShutdownSentinelConfigMapName,
			Namespace:         "test-namespace",
			Finalizers:        []string{SentinelFinalizer},
			DeletionTimestamp: &metav1.Time{Time: time.Now()},
		},
	}

	k8sClient := fake.NewSimpleClientset(sentinel)
	var shutdownCalled bool
	var mu sync.Mutex

	onShutdown := func(ctx context.Context) {
		mu.Lock()
		shutdownCalled = true
		mu.Unlock()
	}

	watcher := NewSentinelWatcher("test-namespace", k8sClient, onShutdown)

	// Create a context that will be cancelled after a short time
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// Run Start in a goroutine since it blocks
	done := make(chan struct{})
	go func() {
		watcher.Start(ctx)
		close(done)
	}()

	// Wait for context to be cancelled
	<-ctx.Done()
	<-done

	// Verify shutdown was triggered
	mu.Lock()
	assert.True(t, shutdownCalled, "onShutdown should be called when sentinel is already being deleted")
	mu.Unlock()
}

func TestSentinelWatcher_Start_ContextCancellation(t *testing.T) {
	// Create sentinel without deletion timestamp
	sentinel := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:       ShutdownSentinelConfigMapName,
			Namespace:  "test-namespace",
			Finalizers: []string{SentinelFinalizer},
		},
	}

	k8sClient := fake.NewSimpleClientset(sentinel)
	onShutdown := func(ctx context.Context) {}

	watcher := NewSentinelWatcher("test-namespace", k8sClient, onShutdown)

	// Create a context that will be cancelled almost immediately
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		watcher.Start(ctx)
		close(done)
	}()

	// Cancel context and verify Start returns
	cancel()

	select {
	case <-done:
		// Success - Start returned after context cancelled
	case <-time.After(5 * time.Second):
		require.Fail(t, "Start did not return after context cancellation")
	}
}

func TestSentinelWatcher_Start_SentinelNotFound(t *testing.T) {
	// No sentinel - IsSentinelBeingDeleted returns true for not found
	k8sClient := fake.NewSimpleClientset()
	var shutdownCalled bool
	var mu sync.Mutex

	onShutdown := func(ctx context.Context) {
		mu.Lock()
		shutdownCalled = true
		mu.Unlock()
	}

	watcher := NewSentinelWatcher("test-namespace", k8sClient, onShutdown)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		watcher.Start(ctx)
		close(done)
	}()

	<-ctx.Done()
	<-done

	// When sentinel is not found, IsSentinelBeingDeleted returns true
	mu.Lock()
	assert.True(t, shutdownCalled, "onShutdown should be called when sentinel is not found")
	mu.Unlock()
}

func TestSentinelWatcher_Start_UpdateToDeleting(t *testing.T) {
	// Create a sentinel that is not being deleted initially
	sentinel := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:       ShutdownSentinelConfigMapName,
			Namespace:  "test-namespace",
			Finalizers: []string{SentinelFinalizer},
		},
	}

	k8sClient := fake.NewSimpleClientset(sentinel)
	var shutdownCalled bool
	var mu sync.Mutex

	onShutdown := func(ctx context.Context) {
		mu.Lock()
		shutdownCalled = true
		mu.Unlock()
	}

	watcher := NewSentinelWatcher("test-namespace", k8sClient, onShutdown)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go watcher.Start(ctx)

	// Give time for the informer to start and sync
	time.Sleep(200 * time.Millisecond)

	// Initial state shouldn't trigger shutdown
	mu.Lock()
	initialShutdownCalled := shutdownCalled
	mu.Unlock()
	assert.False(t, initialShutdownCalled, "onShutdown should not be called for non-deleting sentinel")

	// Update the sentinel to have deletion timestamp (simulating deletion)
	sentinel.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	_, err := k8sClient.CoreV1().ConfigMaps("test-namespace").Update(ctx, sentinel, metav1.UpdateOptions{})
	require.NoError(t, err)

	// Give time for the update to propagate
	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	assert.True(t, shutdownCalled, "onShutdown should be called when sentinel is updated to deleting")
	mu.Unlock()
}

func TestSentinelWatcher_Start_SentinelDeleted(t *testing.T) {
	// Create a sentinel
	sentinel := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ShutdownSentinelConfigMapName,
			Namespace: "test-namespace",
		},
	}

	k8sClient := fake.NewSimpleClientset(sentinel)
	var shutdownCalled bool
	var mu sync.Mutex

	onShutdown := func(ctx context.Context) {
		mu.Lock()
		shutdownCalled = true
		mu.Unlock()
	}

	watcher := NewSentinelWatcher("test-namespace", k8sClient, onShutdown)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go watcher.Start(ctx)

	// Give time for the informer to start and sync
	time.Sleep(200 * time.Millisecond)

	// Delete the sentinel
	err := k8sClient.CoreV1().ConfigMaps("test-namespace").Delete(ctx, ShutdownSentinelConfigMapName, metav1.DeleteOptions{})
	require.NoError(t, err)

	// Give time for the delete to propagate
	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	assert.True(t, shutdownCalled, "onShutdown should be called when sentinel is deleted")
	mu.Unlock()
}

func TestSentinelWatcher_Start_IgnoreOtherConfigMaps(t *testing.T) {
	// Create the sentinel and another configmap
	sentinel := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:       ShutdownSentinelConfigMapName,
			Namespace:  "test-namespace",
			Finalizers: []string{SentinelFinalizer},
		},
	}
	otherCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "other-configmap",
			Namespace: "test-namespace",
		},
	}

	k8sClient := fake.NewSimpleClientset(sentinel, otherCM)
	var shutdownCalled bool
	var mu sync.Mutex

	onShutdown := func(ctx context.Context) {
		mu.Lock()
		shutdownCalled = true
		mu.Unlock()
	}

	watcher := NewSentinelWatcher("test-namespace", k8sClient, onShutdown)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go watcher.Start(ctx)

	// Give time for the informer to start and sync
	time.Sleep(200 * time.Millisecond)

	// Update the other configmap with deletion timestamp
	otherCM.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	_, _ = k8sClient.CoreV1().ConfigMaps("test-namespace").Update(ctx, otherCM, metav1.UpdateOptions{})

	// Delete the other configmap
	_ = k8sClient.CoreV1().ConfigMaps("test-namespace").Delete(ctx, "other-configmap", metav1.DeleteOptions{})

	// Give time for the updates to propagate
	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	assert.False(t, shutdownCalled, "onShutdown should not be called for other configmaps")
	mu.Unlock()
}
