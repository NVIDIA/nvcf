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

package publisher

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
	config2 "github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/interfaces"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/testutils"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.uber.org/zap"
)

// MockBatchStorageClient is a mock implementation of the BatchStorageClient interface for testing.
type MockBatchStorageClient struct {
	mu           sync.Mutex
	StoreBatchFn func(batch []types.StageTransitionEvent) error
	batches      [][]types.StageTransitionEvent
	callCount    int

	// V2 support
	StoreBatchV2Fn func(batch []types.DeploymentStageTransitionEvent) error
	batchesV2      [][]types.DeploymentStageTransitionEvent
	callCountV2    int
}

// StoreBatch records the call and the batch, and can be customized with StoreBatchFn.
func (m *MockBatchStorageClient) StoreBatch(_ context.Context, batch []types.StageTransitionEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	// Store a copy of the batch to avoid modification issues if the publisher reuses the slice.
	batchCopy := make([]types.StageTransitionEvent, len(batch))
	copy(batchCopy, batch)
	m.batches = append(m.batches, batchCopy)
	if m.StoreBatchFn != nil {
		return m.StoreBatchFn(batch)
	}
	return nil
}

// StoreBatchV2 records the call and the V2 batch, and can be customized with StoreBatchV2Fn.
func (m *MockBatchStorageClient) StoreBatchV2(_ context.Context, batch []types.DeploymentStageTransitionEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCountV2++
	// Store a copy of the batch to avoid modification issues if the publisher reuses the slice.
	batchCopy := make([]types.DeploymentStageTransitionEvent, len(batch))
	copy(batchCopy, batch)
	m.batchesV2 = append(m.batchesV2, batchCopy)
	if m.StoreBatchV2Fn != nil {
		return m.StoreBatchV2Fn(batch)
	}
	return nil
}

// GetBatches returns all batches written to the storage client.
func (m *MockBatchStorageClient) GetBatches() [][]types.StageTransitionEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.batches
}

// GetBatchesV2 returns all V2 batches written to the storage client.
func (m *MockBatchStorageClient) GetBatchesV2() [][]types.DeploymentStageTransitionEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.batchesV2
}

// GetCallCount returns the number of times StoreBatch was called.
func (m *MockBatchStorageClient) GetCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

// GetCallCountV2 returns the number of times StoreBatchV2 was called.
func (m *MockBatchStorageClient) GetCallCountV2() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCountV2
}

func TestNewBatchedPublisherRejectsInvalidConfig(t *testing.T) {
	valid := config2.BatchedPublisherConfig{
		QueueSize:            10,
		BatchSize:            5,
		BatchIntervalSeconds: 1,
	}

	tests := []struct {
		name      string
		config    config2.BatchedPublisherConfig
		wantError string
	}{
		{name: "zero queue size", config: config2.BatchedPublisherConfig{QueueSize: 0, BatchSize: valid.BatchSize, BatchIntervalSeconds: valid.BatchIntervalSeconds}, wantError: "queue size"},
		{name: "negative queue size", config: config2.BatchedPublisherConfig{QueueSize: -1, BatchSize: valid.BatchSize, BatchIntervalSeconds: valid.BatchIntervalSeconds}, wantError: "queue size"},
		{name: "zero batch size", config: config2.BatchedPublisherConfig{QueueSize: valid.QueueSize, BatchSize: 0, BatchIntervalSeconds: valid.BatchIntervalSeconds}, wantError: "batch size"},
		{name: "negative batch size", config: config2.BatchedPublisherConfig{QueueSize: valid.QueueSize, BatchSize: -1, BatchIntervalSeconds: valid.BatchIntervalSeconds}, wantError: "batch size"},
		{name: "zero batch interval", config: config2.BatchedPublisherConfig{QueueSize: valid.QueueSize, BatchSize: valid.BatchSize, BatchIntervalSeconds: 0}, wantError: "batch interval"},
		{name: "negative batch interval", config: config2.BatchedPublisherConfig{QueueSize: valid.QueueSize, BatchSize: valid.BatchSize, BatchIntervalSeconds: -1}, wantError: "batch interval"},
	}

	logger := otelzap.New(zap.NewNop())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			publisher, err := NewBatchedPublisher(tt.config, nil, logger)
			require.Error(t, err)
			assert.Nil(t, publisher)
			require.ErrorContains(t, err, tt.wantError)
		})
	}
}

func TestBatchedPublisher_StartStop(t *testing.T) {
	logger := testutils.InitTestLogger(t)
	storageClient := &MockBatchStorageClient{}
	config := config2.BatchedPublisherConfig{
		QueueSize:            10,
		BatchSize:            5,
		BatchIntervalSeconds: 1,
	}
	p, err := NewBatchedPublisher(config, []interfaces.BatchStorageClient{storageClient}, logger)
	require.NoError(t, err, "Publisher should start without error")

	assert.False(t, p.stopped, "Publisher start stopped")

	p.Stop()
	assert.True(t, p.stopped, "Publisher should be stopped after Stop")
	// Ensure eventCh is closed by Stop
	_, ok := <-p.eventCh
	assert.False(t, ok, "eventCh should be closed after Stop")
	// Ensure eventV2Ch is closed by Stop
	_, ok = <-p.eventV2Ch
	assert.False(t, ok, "eventV2Ch should be closed after Stop")

	// Call Stop again, should be idempotent
	p.Stop()
	assert.True(t, p.stopped, "Publisher should still be stopped after second Stop")
}

// TestBatchedPublisher_WriteBatchError Test that the publisher keeps working even if the client returns an error.
func TestBatchedPublisher_WriteBatchErrorOnBatchAndStop(t *testing.T) {
	logger := testutils.InitTestLogger(t)

	// Create a client that returns an error
	errClient := &MockBatchStorageClient{
		StoreBatchFn: func(batch []types.StageTransitionEvent) error {
			return fmt.Errorf("simulated write error")
		},
	}

	config := config2.BatchedPublisherConfig{
		QueueSize:            10,
		BatchSize:            3,
		BatchIntervalSeconds: 1,
	}

	p, err := NewBatchedPublisher(config, []interfaces.BatchStorageClient{errClient}, logger)
	require.NoError(t, err, "Publisher should start without error")
	defer p.Stop()

	// Publish enough events to trigger a batch write
	for i := 0; i < 4; i++ {
		p.Publish(t.Context(), types.StageTransitionEvent{NcaId: fmt.Sprintf("test%d", i)})
	}

	// Wait for the batch to be processed
	assert.Eventually(t, func() bool {
		return errClient.GetCallCount() == 1
	}, time.Second*2, time.Millisecond*50, "Client should have been called once")

	// Verify the batch was attempted despite the error
	batches := errClient.GetBatches()
	assert.Len(t, batches, 1, "Should be one batch attempted")
	assert.Len(t, batches[0], 3, "Batch should contain 3 events")

	p.Stop()
	assert.Equal(t, errClient.GetCallCount(), 2, "Client should have been called twice, once for the batch and once for the final flush")
}

func TestBatchedPublisher_WriteBatchErrorOnInterval(t *testing.T) {
	logger := testutils.InitTestLogger(t)

	// Create a client that returns an error
	errClient := &MockBatchStorageClient{
		StoreBatchFn: func(batch []types.StageTransitionEvent) error {
			return fmt.Errorf("simulated write error on interval")
		},
	}

	config := config2.BatchedPublisherConfig{
		QueueSize:            10,
		BatchSize:            5, // Larger than events we'll publish
		BatchIntervalSeconds: 1, // Short interval to trigger batch write
	}

	p, err := NewBatchedPublisher(config, []interfaces.BatchStorageClient{errClient}, logger)
	require.NoError(t, err, "Publisher should start without error")
	defer p.Stop()

	// Publish some events but not enough to trigger a batch size-based write
	for i := 0; i < 2; i++ {
		p.Publish(t.Context(), types.StageTransitionEvent{NcaId: fmt.Sprintf("interval%d", i)})
	}

	// Wait for the interval to trigger a batch write
	assert.Eventually(t, func() bool {
		return errClient.GetCallCount() == 1
	}, time.Second*3, time.Millisecond*100, "Client should have been called once due to interval")

	// Verify the batch was attempted despite the error
	batches := errClient.GetBatches()
	assert.Len(t, batches, 1, "Should be one batch attempted")
	assert.Len(t, batches[0], 2, "Batch should contain 2 events")
}

func TestBatchedPublisher_Publish_BatchFull(t *testing.T) {
	logger := testutils.InitTestLogger(t)
	storageClient := &MockBatchStorageClient{}
	config := config2.BatchedPublisherConfig{
		QueueSize:            10,
		BatchSize:            3,
		BatchIntervalSeconds: 60, // High interval to ensure batch full triggers first
	}
	p, err := NewBatchedPublisher(config, []interfaces.BatchStorageClient{storageClient}, logger)
	require.NoError(t, err, "Publisher should start without error")
	defer p.Stop()

	for idx := range 3 {
		p.Publish(t.Context(), types.StageTransitionEvent{NcaId: fmt.Sprintf("nca%d", idx+1)})
	}

	// Wait for the batch to be processed
	assert.Eventually(t, func() bool {
		return storageClient.GetCallCount() == 1
	}, time.Second*2, time.Millisecond*50, "Client should have been called once for the full batch")

	batches := storageClient.GetBatches()
	assert.Len(t, batches, 1, "Should be one batch written")
	assert.Len(t, batches[0], 3, "First batch should contain 3 events")
	assert.Equal(t, "nca1", batches[0][0].NcaId)
	assert.Equal(t, "nca2", batches[0][1].NcaId)
	assert.Equal(t, "nca3", batches[0][2].NcaId)
}

func TestBatchedPublisher_BatchInterval(t *testing.T) {
	logger := testutils.InitTestLogger(t)
	storageClient := &MockBatchStorageClient{}
	config := config2.BatchedPublisherConfig{
		QueueSize:            10,
		BatchSize:            5, // Make batch size larger than number of events
		BatchIntervalSeconds: 1, // Short interval to trigger batching
	}
	p, err := NewBatchedPublisher(config, []interfaces.BatchStorageClient{storageClient}, logger)
	require.NoError(t, err, "Publisher should start without error")
	defer p.Stop()

	for idx := range 2 {
		p.Publish(t.Context(), types.StageTransitionEvent{NcaId: fmt.Sprintf("event%d", idx+1)})
	}

	// Wait for the batch interval to trigger
	assert.Eventually(t, func() bool {
		return storageClient.GetCallCount() == 1
	}, time.Second*3, time.Millisecond*100, "client should have been called once due to batch interval")

	batches := storageClient.GetBatches()
	assert.Len(t, batches, 1, "Should be one batch written")
	assert.Len(t, batches[0], 2, "Batch should contain 2 events")
	assert.Equal(t, "event1", batches[0][0].NcaId)
	assert.Equal(t, "event2", batches[0][1].NcaId)
}

func TestBatchedPublisher_Publish_QueueFull(t *testing.T) {
	logger := testutils.InitTestLogger(t)
	storageClient := &MockBatchStorageClient{}
	config := config2.BatchedPublisherConfig{
		QueueSize:            2, // Small queue size
		BatchSize:            2,
		BatchIntervalSeconds: 60, // Long interval, so queue full is the focus
	}
	p, err := NewBatchedPublisher(config, []interfaces.BatchStorageClient{storageClient}, logger)
	require.NoError(t, err, "Publisher should start without error")
	defer p.Stop()

	event1 := types.StageTransitionEvent{NcaId: "event1"}
	event2 := types.StageTransitionEvent{NcaId: "event2"}
	event3Dropped := types.StageTransitionEvent{NcaId: "eventDropped"}

	// Fill the queue
	p.Publish(t.Context(), event1)
	p.Publish(t.Context(), event2)
	// Try to publish one more, should be dropped
	// The run loop is active, but we haven't given it a chance to process the batch yet.
	p.Publish(t.Context(), event3Dropped)

	// Wait for the batch to be processed (containing only event1 and event2)
	assert.Eventually(t, func() bool {
		return storageClient.GetCallCount() >= 1 // Can be 1 if stop flushes, or if interval hits earlier for some reason.
	}, time.Second*10, time.Millisecond*100, "Client should have been called for the initial events")

	batches := storageClient.GetBatches()
	assert.NotEmpty(t, batches, "Batches should not be empty")

	// Check the contents of the first batch
	firstBatch := batches[0]
	assert.Len(t, firstBatch, 2, "Batch should contain 2 events that fit in the queue")
	assert.Equal(t, "event1", firstBatch[0].NcaId)
	assert.Equal(t, "event2", firstBatch[1].NcaId)

	// Ensure the dropped event is not in any batch
	for _, batch := range batches {
		for _, ev := range batch {
			assert.NotEqual(t, "eventDropped", ev.NcaId, "Dropped event should not be in any batch")
		}
	}
}

func TestBatchedPublisher_Stop_WritesFinalBatch(t *testing.T) {
	logger := otelzap.New(zap.NewNop())
	storageClient := &MockBatchStorageClient{}
	config := config2.BatchedPublisherConfig{
		QueueSize:            10,
		BatchSize:            5,  // Batch size larger than number of events
		BatchIntervalSeconds: 60, // Long interval to ensure Stop triggers the write
	}
	p, err := NewBatchedPublisher(config, []interfaces.BatchStorageClient{storageClient}, logger)
	require.NoError(t, err, "Publisher should start without error")

	events := []types.StageTransitionEvent{
		{NcaId: "final1"},
		{NcaId: "final2"},
	}

	for _, event := range events {
		p.Publish(t.Context(), event)
	}

	// Ensure no batch is written before stop (due to size or interval)
	time.Sleep(time.Millisecond * 200) // Yield to the publisher goroutine
	assert.Equal(t, 0, storageClient.GetCallCount(), "Client should not have been called before Stop")

	p.Stop() // This should trigger the flush of the pending batch

	assert.Equal(t, 1, storageClient.GetCallCount(), "Client should have been called once after Stop")
	batches := storageClient.GetBatches()
	assert.Len(t, batches, 1, "Should be one batch written")
	assert.Len(t, batches[0], 2, "Final batch should contain 2 events")
	assert.Equal(t, "final1", batches[0][0].NcaId)
	assert.Equal(t, "final2", batches[0][1].NcaId)
}

// Tests for the V2 functionality

func TestBatchedPublisher_PublishV2_BatchFull(t *testing.T) {
	logger := testutils.InitTestLogger(t)
	storageClient := &MockBatchStorageClient{}
	config := config2.BatchedPublisherConfig{
		QueueSize:            10,
		BatchSize:            3,
		BatchIntervalSeconds: 60, // High interval to ensure batch full triggers first
	}
	p, err := NewBatchedPublisher(config, []interfaces.BatchStorageClient{storageClient}, logger)
	require.NoError(t, err, "Publisher should start without error")
	defer p.Stop()

	// Create UUIDs for testing
	functionId := uuid.New()
	functionVersionId := uuid.New()
	deploymentId := uuid.New()

	// Publish V2 events
	for idx := range 3 {
		p.PublishV2(t.Context(), types.DeploymentStageTransitionEvent{
			NcaId:             fmt.Sprintf("nca%d", idx+1),
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
			DeploymentId:      deploymentId,
			InstanceId:        fmt.Sprintf("instance%d", idx+1),
			Event:             "building",
			EventType:         "test",
		})
	}

	// Wait for the batch to be processed
	assert.Eventually(t, func() bool {
		return storageClient.GetCallCountV2() == 1
	}, time.Second*2, time.Millisecond*50, "V2 client should have been called once for the full batch")

	batchesV2 := storageClient.GetBatchesV2()
	assert.Len(t, batchesV2, 1, "Should be one V2 batch written")
	assert.Len(t, batchesV2[0], 3, "First V2 batch should contain 3 events")
	assert.Equal(t, "nca1", batchesV2[0][0].NcaId)
	assert.Equal(t, "nca2", batchesV2[0][1].NcaId)
	assert.Equal(t, "nca3", batchesV2[0][2].NcaId)

	// Verify DeploymentId is preserved
	assert.Equal(t, deploymentId, batchesV2[0][0].DeploymentId)
}

func TestBatchedPublisher_PublishV2_BatchInterval(t *testing.T) {
	logger := testutils.InitTestLogger(t)
	storageClient := &MockBatchStorageClient{}
	config := config2.BatchedPublisherConfig{
		QueueSize:            10,
		BatchSize:            5, // Make batch size larger than number of events
		BatchIntervalSeconds: 1, // Short interval to trigger batching
	}
	p, err := NewBatchedPublisher(config, []interfaces.BatchStorageClient{storageClient}, logger)
	require.NoError(t, err, "Publisher should start without error")
	defer p.Stop()

	// Create UUIDs for testing
	functionId := uuid.New()
	functionVersionId := uuid.New()
	deploymentId := uuid.New()

	// Publish some V2 events but not enough to trigger batch size
	for idx := range 2 {
		p.PublishV2(t.Context(), types.DeploymentStageTransitionEvent{
			NcaId:             fmt.Sprintf("v2event%d", idx+1),
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
			DeploymentId:      deploymentId,
			InstanceId:        fmt.Sprintf("instance%d", idx+1),
			Event:             "building",
			EventType:         "test",
		})
	}

	// Wait for the batch interval to trigger
	assert.Eventually(t, func() bool {
		return storageClient.GetCallCountV2() == 1
	}, time.Second*3, time.Millisecond*100, "V2 client should have been called once due to batch interval")

	batchesV2 := storageClient.GetBatchesV2()
	assert.Len(t, batchesV2, 1, "Should be one V2 batch written")
	assert.Len(t, batchesV2[0], 2, "Batch should contain 2 V2 events")
	assert.Equal(t, "v2event1", batchesV2[0][0].NcaId)
	assert.Equal(t, "v2event2", batchesV2[0][1].NcaId)
}

func TestBatchedPublisher_PublishV2_QueueFull(t *testing.T) {
	logger := testutils.InitTestLogger(t)
	storageClient := &MockBatchStorageClient{}
	config := config2.BatchedPublisherConfig{
		QueueSize:            2, // Small queue size
		BatchSize:            2,
		BatchIntervalSeconds: 60, // Long interval, so queue full is the focus
	}
	p, err := NewBatchedPublisher(config, []interfaces.BatchStorageClient{storageClient}, logger)
	require.NoError(t, err, "Publisher should start without error")
	defer p.Stop()

	// Create common values for events
	functionId := uuid.New()
	functionVersionId := uuid.New()
	deploymentId := uuid.New()

	event1 := types.DeploymentStageTransitionEvent{
		NcaId:             "v2event1",
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
		DeploymentId:      deploymentId,
		InstanceId:        "instance1",
		Event:             "building",
	}
	event2 := types.DeploymentStageTransitionEvent{
		NcaId:             "v2event2",
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
		DeploymentId:      deploymentId,
		InstanceId:        "instance2",
		Event:             "building",
	}
	event3Dropped := types.DeploymentStageTransitionEvent{
		NcaId:             "v2eventDropped",
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
		DeploymentId:      deploymentId,
		InstanceId:        "instance3",
		Event:             "building",
	}

	// Fill the queue
	p.PublishV2(t.Context(), event1)
	p.PublishV2(t.Context(), event2)
	// Try to publish one more, should be dropped
	p.PublishV2(t.Context(), event3Dropped)

	// Wait for the batch to be processed
	assert.Eventually(t, func() bool {
		return storageClient.GetCallCountV2() >= 1
	}, time.Second*10, time.Millisecond*100, "V2 Client should have been called for the initial events")

	batchesV2 := storageClient.GetBatchesV2()
	assert.NotEmpty(t, batchesV2, "V2 Batches should not be empty")

	// Check the contents of the first batch
	firstBatch := batchesV2[0]
	assert.Len(t, firstBatch, 2, "V2 Batch should contain 2 events that fit in the queue")
	assert.Equal(t, "v2event1", firstBatch[0].NcaId)
	assert.Equal(t, "v2event2", firstBatch[1].NcaId)

	// Ensure the dropped event is not in any batch
	for _, batch := range batchesV2 {
		for _, ev := range batch {
			assert.NotEqual(t, "v2eventDropped", ev.NcaId, "Dropped V2 event should not be in any batch")
		}
	}
}

func TestBatchedPublisher_StopV2_WritesFinalBatch(t *testing.T) {
	logger := otelzap.New(zap.NewNop())
	storageClient := &MockBatchStorageClient{}
	config := config2.BatchedPublisherConfig{
		QueueSize:            10,
		BatchSize:            5,  // Batch size larger than number of events
		BatchIntervalSeconds: 60, // Long interval to ensure Stop triggers the write
	}
	p, err := NewBatchedPublisher(config, []interfaces.BatchStorageClient{storageClient}, logger)
	require.NoError(t, err, "Publisher should start without error")

	// Create common values for events
	functionId := uuid.New()
	functionVersionId := uuid.New()
	deploymentId := uuid.New()

	events := []types.DeploymentStageTransitionEvent{
		{
			NcaId:             "v2final1",
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
			DeploymentId:      deploymentId,
			InstanceId:        "instance1",
			Event:             "ready",
		},
		{
			NcaId:             "v2final2",
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
			DeploymentId:      deploymentId,
			InstanceId:        "instance2",
			Event:             "ready",
		},
	}

	for _, event := range events {
		p.PublishV2(t.Context(), event)
	}

	// Ensure no batch is written before stop (due to size or interval)
	time.Sleep(time.Millisecond * 200) // Yield to the publisher goroutine
	assert.Equal(t, 0, storageClient.GetCallCountV2(), "V2 Client should not have been called before Stop")

	p.Stop() // This should trigger the flush of the pending batch

	assert.Equal(t, 1, storageClient.GetCallCountV2(), "V2 Client should have been called once after Stop")
	batchesV2 := storageClient.GetBatchesV2()
	assert.Len(t, batchesV2, 1, "Should be one V2 batch written")
	assert.Len(t, batchesV2[0], 2, "Final V2 batch should contain 2 events")
	assert.Equal(t, "v2final1", batchesV2[0][0].NcaId)
	assert.Equal(t, "v2final2", batchesV2[0][1].NcaId)
}
