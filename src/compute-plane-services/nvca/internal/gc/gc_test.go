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

package gc

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	bartfake "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned/fake"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// mockJob implements the Cleaner interface for testing
type mockJob struct {
	name     string
	duration time.Duration
	err      error
	runCount atomic.Uint64
}

func (m *mockJob) Name() string {
	return m.name
}

func (m *mockJob) Run(ctx context.Context) error {
	m.runCount.Add(1)

	// Simulate work by sleeping for the specified duration
	select {
	case <-time.After(m.duration):
		return m.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestNewGCController(t *testing.T) {
	clients := &kubeclients.KubeClients{
		K8s:  fake.NewSimpleClientset(),
		BART: bartfake.NewSimpleClientset(),
	}

	controller := NewRunnable(clients, nil, 30*time.Minute, types.DefaultICMSRequestNamespace)

	assert.NotNil(t, controller)
	assert.Equal(t, 4, len(controller.cleaners))
	assert.Equal(t, 30*time.Minute, controller.interval)
}

func TestGCController_runCleaners(t *testing.T) {
	clients := &kubeclients.KubeClients{
		K8s:  fake.NewSimpleClientset(),
		BART: bartfake.NewSimpleClientset(),
	}

	controller := NewRunnable(clients, nil, 30*time.Minute, types.DefaultICMSRequestNamespace)

	// This should not panic and should run all cleaners
	ctx := context.Background()
	controller.runCleaners(ctx)

	// Test that all expected cleaners are present
	expectedNames := []string{"StorageClassCleaner", "PersistentVolumeCleaner", "NamespaceCleaner", "PodCleaner"}
	assert.Equal(t, len(expectedNames), len(controller.cleaners))

	for i, cleaner := range controller.cleaners {
		assert.Equal(t, expectedNames[i], cleaner.Name())
	}
}

func TestGCController_runCleaners_EmptyCleaners(t *testing.T) {
	controller := &Runnable{
		cleaners: []Cleaner{},
		interval: 30 * time.Minute,
	}

	ctx := context.Background()

	// This should not panic with empty cleaners
	controller.runCleaners(ctx)
}

func TestGCController_Start_WithMockCleaners(t *testing.T) {
	job1 := &mockJob{name: "Job1", duration: 10 * time.Millisecond}
	job2 := &mockJob{name: "Job2", duration: 20 * time.Millisecond}
	job3 := &mockJob{name: "Job3", duration: 30 * time.Millisecond}

	controller := &Runnable{
		cleaners: []Cleaner{job1, job2, job3},
		interval: 100 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	// Start the controller - it should run cleaners immediately and then once more during the test
	errCh := make(chan error, 1)
	go func() {
		defer close(errCh)
		err := controller.Start(ctx)
		if err != nil && err != context.DeadlineExceeded && err != context.Canceled {
			errCh <- err
		}
	}()

	assert.EventuallyWithT(t, func(collect *assert.CollectT) {
		// All jobs should have run at least once (immediate run)
		assert.GreaterOrEqual(collect, job1.runCount.Load(), uint64(1))
		assert.GreaterOrEqual(collect, job2.runCount.Load(), uint64(1))
		assert.GreaterOrEqual(collect, job3.runCount.Load(), uint64(1))

		// They may have run twice (immediate + one tick) but should not run more than 3 times
		assert.LessOrEqual(collect, job1.runCount.Load(), uint64(3))
		assert.LessOrEqual(collect, job2.runCount.Load(), uint64(3))
		assert.LessOrEqual(collect, job3.runCount.Load(), uint64(3))
	}, 1*time.Second, 10*time.Millisecond)

	// Check for any errors from the controller goroutine
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Unexpected error from controller.Start: %v", err)
		}
	default:
		// No error received, which is expected
	}
}

func TestGCController_TickerExecution(t *testing.T) {
	// Create a controller with very short interval for testing
	controller := &Runnable{
		cleaners: []Cleaner{
			&mockJob{name: "test-cleaner", duration: 1 * time.Millisecond},
		},
		interval: 50 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	// Start the controller
	errCh := make(chan error, 1)
	go func() {
		defer close(errCh)
		err := controller.Start(ctx)
		if err != nil && err != context.DeadlineExceeded && err != context.Canceled {
			errCh <- err
		}
	}()

	// Wait for the context to expire
	<-ctx.Done()

	// The mock job should have run multiple times (immediate + at least one tick)
	mockCleaner := controller.cleaners[0].(*mockJob)
	assert.GreaterOrEqual(t, mockCleaner.runCount.Load(), uint64(2))

	// Check for any errors from the controller goroutine
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Unexpected error from controller.Start: %v", err)
		}
	default:
		// No error received, which is expected
	}
}

func TestGCController_WithCleanerError(t *testing.T) {
	expectedErr := errors.New("cleaner error")
	controller := &Runnable{
		cleaners: []Cleaner{
			&mockJob{name: "good-cleaner", duration: 1 * time.Millisecond},
			&mockJob{name: "bad-cleaner", duration: 1 * time.Millisecond, err: expectedErr},
		},
		interval: 100 * time.Millisecond,
	}

	ctx := context.Background()

	// This should not panic even with errors
	controller.runCleaners(ctx)

	// Both cleaners should have run
	assert.Equal(t, uint64(1), controller.cleaners[0].(*mockJob).runCount.Load())
	assert.Equal(t, uint64(1), controller.cleaners[1].(*mockJob).runCount.Load())
}

func TestGCController_ContextCancellation(t *testing.T) {
	controller := &Runnable{
		cleaners: []Cleaner{
			&mockJob{name: "test-cleaner", duration: 1 * time.Second}, // Long running to test cancellation
		},
		interval: 10 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Start should return when context is cancelled
	err := controller.Start(ctx)
	if err != nil && err != context.DeadlineExceeded && err != context.Canceled {
		t.Errorf("Expected context cancellation error, got: %v", err)
	}

	// Should have run at least once (immediate execution)
	mockCleaner := controller.cleaners[0].(*mockJob)
	assert.GreaterOrEqual(t, mockCleaner.runCount.Load(), uint64(1))
}

func TestNvcaICMSRequestGetter_GetICMSRequest_Success(t *testing.T) {
	icmsRequest := &nvcav2beta1.ICMSRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-icms-request",
			Namespace: types.DefaultICMSRequestNamespace,
		},
	}

	bartClient := bartfake.NewSimpleClientset(icmsRequest)
	getter := &nvcaICMSRequestGetter{client: bartClient, icmsRequestNamespace: types.DefaultICMSRequestNamespace}

	ctx := context.Background()
	result, err := getter.GetICMSRequest(ctx, "test-icms-request")

	assert.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "test-icms-request", result.Name)
	assert.Equal(t, types.DefaultICMSRequestNamespace, result.Namespace)
}

func TestNvcaICMSRequestGetter_GetICMSRequest_NotFound(t *testing.T) {
	bartClient := bartfake.NewSimpleClientset()
	getter := &nvcaICMSRequestGetter{client: bartClient, icmsRequestNamespace: types.DefaultICMSRequestNamespace}

	ctx := context.Background()
	result, err := getter.GetICMSRequest(ctx, "non-existent-icms-request")

	assert.Error(t, err)
	// The fake client may return nil or a zero-value struct on error
	if result != nil {
		assert.Empty(t, result.Name)
	}
}

func TestNvcaICMSRequestGetter_GetICMSRequest_WithContext(t *testing.T) {
	bartClient := bartfake.NewSimpleClientset()
	getter := &nvcaICMSRequestGetter{client: bartClient, icmsRequestNamespace: types.DefaultICMSRequestNamespace}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	result, err := getter.GetICMSRequest(ctx, "test-icms-request")

	assert.Error(t, err)
	// The fake client may return nil or a zero-value struct on error
	if result != nil {
		assert.Empty(t, result.Name)
	}
}
