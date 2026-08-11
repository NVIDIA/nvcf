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

package cloudevents

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/middleware"
)

// setupTestMetrics creates test metrics for testing
func setupTestMetrics() *middleware.CloudEventsMetrics {
	// Create a test meter provider
	meterProvider := sdkmetric.NewMeterProvider()
	otel.SetMeterProvider(meterProvider)

	// Create test metrics
	metrics, _ := middleware.CreateCloudEventsMetrics(otelzap.New(zap.NewNop()))
	return metrics
}

// CloudEventsClient interface for mocking
type CloudEventsClient interface {
	StoreBatch(ctx context.Context, batch []types.StageTransitionEvent) error
	StoreBatchV2(ctx context.Context, batch []types.DeploymentStageTransitionEvent) error
}

// MockCloudEventsClient mocks the CloudEventsClient interface
type MockCloudEventsClient struct {
	mock.Mock
}

func (m *MockCloudEventsClient) StoreBatch(ctx context.Context, batch []types.StageTransitionEvent) error {
	args := m.Called(ctx, batch)
	return args.Error(0)
}

func (m *MockCloudEventsClient) StoreBatchV2(ctx context.Context, batch []types.DeploymentStageTransitionEvent) error {
	args := m.Called(ctx, batch)
	return args.Error(0)
}

// MockResilienceHandler mocks the CloudEventsResilienceHandler
type MockResilienceHandler struct {
	mock.Mock
}

func (m *MockResilienceHandler) StoreFailedCloudEvent(ctx context.Context, eventType string, eventData []byte) error {
	args := m.Called(ctx, eventType, eventData)
	return args.Error(0)
}

func (m *MockResilienceHandler) StreamFailedCloudEvents(ctx context.Context, limit int) iter.Seq[data_access.FailedCloudEvent] {
	args := m.Called(ctx, limit)
	if args.Get(0) == nil {
		// Return an empty iterator
		return func(yield func(data_access.FailedCloudEvent) bool) {}
	}
	return args.Get(0).(iter.Seq[data_access.FailedCloudEvent])
}

func (m *MockResilienceHandler) UpdateFailedCloudEventRetry(ctx context.Context, id uuid.UUID, retryCount int) error {
	args := m.Called(ctx, id, retryCount)
	return args.Error(0)
}

func (m *MockResilienceHandler) DeleteFailedCloudEvent(ctx context.Context, id uuid.UUID) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *MockResilienceHandler) TryBecomeLeader(ctx context.Context, instanceID string, leaseDuration time.Duration) (bool, error) {
	args := m.Called(ctx, instanceID, leaseDuration)
	return args.Bool(0), args.Error(1)
}

// testableResilientClient wraps ResilientCloudEventsClient to use interface for testing
type testableResilientClient struct {
	cloudEventsClient CloudEventsClient
	resilienceHandler data_access.CloudEventsResilienceHandler
	logger            *otelzap.Logger
	config            ResilienceConfig
	stopCh            chan struct{}
	metrics           *middleware.CloudEventsMetrics
}

// StoreBatch with fallback - same logic as real implementation
func (c *testableResilientClient) StoreBatch(ctx context.Context, batch []types.StageTransitionEvent) error {
	// Try to send to CloudEvents first
	err := c.cloudEventsClient.StoreBatch(ctx, batch)
	if err == nil {
		c.logger.DebugContext(ctx, "successfully sent batch to CloudEvents", zap.Int("batch_size", len(batch)))
		return nil
	}

	// CloudEvents failed, store in Cassandra for later retry
	c.logger.WarnContext(ctx, "CloudEvents send failed, storing in Cassandra for retry",
		zap.Error(err), zap.Int("batch_size", len(batch)))

	// Serialize for storage
	eventData, marshalErr := json.Marshal(batch)
	if marshalErr != nil {
		return errors.New("CloudEvents failed and also failed to marshal for fallback")
	}

	// Store in Cassandra
	if storeErr := c.resilienceHandler.StoreFailedCloudEvent(ctx, "v1", eventData); storeErr != nil {
		c.logger.ErrorContext(ctx, "failed to store CloudEvents in Cassandra fallback", zap.Error(storeErr))
		return errors.New("CloudEvents failed and also failed to store fallback")
	}

	c.logger.DebugContext(ctx, "stored failed CloudEvents in Cassandra for retry")
	return nil // Return nil since we successfully handled the failure
}

// StoreBatchV2 with fallback
func (c *testableResilientClient) StoreBatchV2(ctx context.Context, batch []types.DeploymentStageTransitionEvent) error {
	// Try to send to CloudEvents first
	err := c.cloudEventsClient.StoreBatchV2(ctx, batch)
	if err == nil {
		c.logger.DebugContext(ctx, "successfully sent v2 batch to CloudEvents", zap.Int("batch_size", len(batch)))
		return nil
	}

	// CloudEvents failed, store in Cassandra for later retry
	c.logger.WarnContext(ctx, "CloudEvents v2 send failed, storing in Cassandra for retry",
		zap.Error(err), zap.Int("batch_size", len(batch)))

	// Serialize for storage
	eventData, marshalErr := json.Marshal(batch)
	if marshalErr != nil {
		return errors.New("CloudEvents v2 failed and also failed to marshal for fallback")
	}

	// Store in Cassandra
	if storeErr := c.resilienceHandler.StoreFailedCloudEvent(ctx, "v2", eventData); storeErr != nil {
		c.logger.ErrorContext(ctx, "failed to store CloudEvents v2 in Cassandra fallback", zap.Error(storeErr))
		return errors.New("CloudEvents v2 failed and also failed to store fallback")
	}

	c.logger.DebugContext(ctx, "stored failed CloudEvents v2 in Cassandra for retry")
	return nil // Return nil since we successfully handled the failure
}

func TestStoreBatch_Success(t *testing.T) {
	// Arrange
	ctx := context.Background()
	mockCloudEvents := new(MockCloudEventsClient)
	mockResilience := new(MockResilienceHandler)

	client := &testableResilientClient{
		cloudEventsClient: mockCloudEvents,
		resilienceHandler: mockResilience,
		logger:            otelzap.New(zap.NewNop()),
		config:            DefaultResilienceConfig(),
		stopCh:            make(chan struct{}),
		metrics:           setupTestMetrics(),
	}

	batch := []types.StageTransitionEvent{
		{
			NcaId:             "nca-123",
			FunctionId:        uuid.New(),
			FunctionVersionId: uuid.New(),
			InstanceId:        "instance-123",
			Event:             "building",
			EventType:         "stage_transition",
			Timestamp:         time.Now(),
			Details:           json.RawMessage(`{}`),
		},
	}

	// CloudEvents succeeds
	mockCloudEvents.On("StoreBatch", ctx, batch).Return(nil)

	// Act
	err := client.StoreBatch(ctx, batch)

	// Assert
	assert.NoError(t, err)
	mockCloudEvents.AssertExpectations(t)
	// Should NOT fallback to Cassandra
	mockResilience.AssertNotCalled(t, "StoreFailedCloudEvent")
}

func TestStoreBatch_FallbackToCassandra(t *testing.T) {
	// Arrange
	ctx := context.Background()
	mockCloudEvents := new(MockCloudEventsClient)
	mockResilience := new(MockResilienceHandler)

	client := &testableResilientClient{
		cloudEventsClient: mockCloudEvents,
		resilienceHandler: mockResilience,
		logger:            otelzap.New(zap.NewNop()),
		config:            DefaultResilienceConfig(),
		stopCh:            make(chan struct{}),
		metrics:           setupTestMetrics(),
	}

	batch := []types.StageTransitionEvent{
		{
			NcaId:             "nca-123",
			FunctionId:        uuid.New(),
			FunctionVersionId: uuid.New(),
			InstanceId:        "instance-123",
			Event:             "building",
			EventType:         "stage_transition",
			Timestamp:         time.Now(),
			Details:           json.RawMessage(`{}`),
		},
	}

	cloudEventsErr := errors.New("CloudEvents API unavailable")

	// CloudEvents fails
	mockCloudEvents.On("StoreBatch", ctx, batch).Return(cloudEventsErr)
	// Cassandra fallback succeeds
	mockResilience.On("StoreFailedCloudEvent", ctx, "v1", mock.AnythingOfType("[]uint8")).Return(nil)

	// Act
	err := client.StoreBatch(ctx, batch)

	// Assert
	assert.NoError(t, err) // Should return nil since fallback succeeded
	mockCloudEvents.AssertExpectations(t)
	mockResilience.AssertExpectations(t)
}

func TestStoreBatch_BothFail(t *testing.T) {
	// Arrange
	ctx := context.Background()
	mockCloudEvents := new(MockCloudEventsClient)
	mockResilience := new(MockResilienceHandler)

	client := &testableResilientClient{
		cloudEventsClient: mockCloudEvents,
		resilienceHandler: mockResilience,
		logger:            otelzap.New(zap.NewNop()),
		config:            DefaultResilienceConfig(),
		stopCh:            make(chan struct{}),
		metrics:           setupTestMetrics(),
	}

	batch := []types.StageTransitionEvent{
		{
			NcaId:             "nca-123",
			FunctionId:        uuid.New(),
			FunctionVersionId: uuid.New(),
			InstanceId:        "instance-123",
			Event:             "building",
			EventType:         "stage_transition",
			Timestamp:         time.Now(),
			Details:           json.RawMessage(`{}`),
		},
	}

	cloudEventsErr := errors.New("CloudEvents API unavailable")
	cassandraErr := errors.New("Cassandra connection failed")

	// Both CloudEvents and Cassandra fail
	mockCloudEvents.On("StoreBatch", ctx, batch).Return(cloudEventsErr)
	mockResilience.On("StoreFailedCloudEvent", ctx, "v1", mock.AnythingOfType("[]uint8")).Return(cassandraErr)

	// Act
	err := client.StoreBatch(ctx, batch)

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CloudEvents failed")
	assert.Contains(t, err.Error(), "also failed to store fallback")
	mockCloudEvents.AssertExpectations(t)
	mockResilience.AssertExpectations(t)
}

func TestStoreBatchV2_Success(t *testing.T) {
	// Arrange
	ctx := context.Background()
	mockCloudEvents := new(MockCloudEventsClient)
	mockResilience := new(MockResilienceHandler)

	client := &testableResilientClient{
		cloudEventsClient: mockCloudEvents,
		resilienceHandler: mockResilience,
		logger:            otelzap.New(zap.NewNop()),
		config:            DefaultResilienceConfig(),
		stopCh:            make(chan struct{}),
		metrics:           setupTestMetrics(),
	}

	batch := []types.DeploymentStageTransitionEvent{
		{
			NcaId:             "nca-456",
			FunctionId:        uuid.New(),
			FunctionVersionId: uuid.New(),
			DeploymentId:      uuid.New(),
			InstanceId:        "instance-456",
			Event:             "deploying",
			EventType:         "deployment_stage",
			Timestamp:         time.Now(),
			Details:           json.RawMessage(`{}`),
		},
	}

	// CloudEvents succeeds
	mockCloudEvents.On("StoreBatchV2", ctx, batch).Return(nil)

	// Act
	err := client.StoreBatchV2(ctx, batch)

	// Assert
	assert.NoError(t, err)
	mockCloudEvents.AssertExpectations(t)
	// Should NOT fallback to Cassandra
	mockResilience.AssertNotCalled(t, "StoreFailedCloudEvent")
}

func TestStoreBatchV2_FallbackToCassandra(t *testing.T) {
	// Arrange
	ctx := context.Background()
	mockCloudEvents := new(MockCloudEventsClient)
	mockResilience := new(MockResilienceHandler)

	client := &testableResilientClient{
		cloudEventsClient: mockCloudEvents,
		resilienceHandler: mockResilience,
		logger:            otelzap.New(zap.NewNop()),
		config:            DefaultResilienceConfig(),
		stopCh:            make(chan struct{}),
		metrics:           setupTestMetrics(),
	}

	batch := []types.DeploymentStageTransitionEvent{
		{
			NcaId:             "nca-456",
			FunctionId:        uuid.New(),
			FunctionVersionId: uuid.New(),
			DeploymentId:      uuid.New(),
			InstanceId:        "instance-456",
			Event:             "deploying",
			EventType:         "deployment_stage",
			Timestamp:         time.Now(),
			Details:           json.RawMessage(`{}`),
		},
	}

	cloudEventsErr := errors.New("CloudEvents API unavailable")

	// CloudEvents fails
	mockCloudEvents.On("StoreBatchV2", ctx, batch).Return(cloudEventsErr)
	// Cassandra fallback succeeds
	mockResilience.On("StoreFailedCloudEvent", ctx, "v2", mock.AnythingOfType("[]uint8")).Return(nil)

	// Act
	err := client.StoreBatchV2(ctx, batch)

	// Assert
	assert.NoError(t, err) // Should return nil since fallback succeeded
	mockCloudEvents.AssertExpectations(t)
	mockResilience.AssertExpectations(t)
}
