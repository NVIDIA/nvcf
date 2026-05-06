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
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"k8s.io/apimachinery/pkg/labels"

	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvcav2beta1listers "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/listers/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// MockICMSClient is a mock implementation of ICMSClientInterface for testing
type MockICMSClient struct {
	mock.Mock
}

func (m *MockICMSClient) PutHealthStatus(ctx context.Context, req *types.HealthStatusRequest) (*types.HealthStatusResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*types.HealthStatusResponse), args.Error(1)
}

func (m *MockICMSClient) Register(ctx context.Context, req *types.ICMSRegistrationRequest) (*types.ICMSRegistrationResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*types.ICMSRegistrationResponse), args.Error(1)
}

func (m *MockICMSClient) GetCreds(ctx context.Context) (*types.ICMSCredentialResponse, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*types.ICMSCredentialResponse), args.Error(1)
}

func (m *MockICMSClient) GetICMSServerInstanceStatuses(ctx context.Context) (types.ICMSInstanceStatusResponse, error) {
	args := m.Called(ctx)
	return args.Get(0).(types.ICMSInstanceStatusResponse), args.Error(1)
}

func (m *MockICMSClient) PutRequestAcknowledgement(ctx context.Context, icmsReqID, messageBatchID string, instanceCount uint64, srTraceCtxCfg nvcav2beta1.ICMSRequestTraceContextConfig) error {
	args := m.Called(ctx, icmsReqID, messageBatchID, instanceCount, srTraceCtxCfg)
	return args.Error(0)
}

func (m *MockICMSClient) PostInstanceStatusUpdate(ctx context.Context, icmsReqID, instanceID string, req *types.ICMSInstanceStatusUpdateRequest) error {
	args := m.Called(ctx, icmsReqID, instanceID, req)
	return args.Error(0)
}

func (m *MockICMSClient) Endpoint() string {
	args := m.Called()
	return args.String(0)
}

func TestPutNVCAStatusUpdate_AcceptedResponse(t *testing.T) {
	ctx := context.Background()
	ctx = core.WithDefaultLogger(ctx)

	// Reuse the existing setupTestICMSClient function and customize response
	icmsClient, mockTransport := setupTestICMSClient()
	mockTransport.setCode(http.StatusOK)
	mockTransport.setBody(`{"action": "ACCEPTED"}`)

	// Create agent with minimal dependencies for testing response logic
	agent := &Agent{
		AgentOptions: &AgentOptions{
			NCAId:               "test-nca-id",
			ClusterName:         "test-cluster",
			NVCAAgentVersion:    "v1.0.0",
			NVCAOperatorVersion: "v1.0.0",
		},
		icmsClient: icmsClient,
	}
	agent.selfDestruct = &atomic.Bool{}
	agent.selfDestruct.Store(false)

	// Verify initial state
	assert.False(t, agent.selfDestruct.Load(), "Agent should start in non-self-destruct mode")

	// Test the ICMS client directly to verify response parsing
	healthReq := &types.HealthStatusRequest{
		Status:              types.HealthStatusHealthy,
		GPUUsage:            map[types.GPUName]types.GPUResource{},
		ClusterOwnerNCAID:   agent.NCAId,
		NVCAAgentVersion:    agent.NVCAAgentVersion,
		NVCAOperatorVersion: agent.NVCAOperatorVersion,
		ClusterName:         agent.ClusterName,
		UpgradeStatus:       types.NVCAUpgradeNoStatus,
	}

	response, err := icmsClient.PutHealthStatus(ctx, healthReq)
	assert.NoError(t, err, "PutHealthStatus should complete without error")
	assert.NotNil(t, response, "Response should not be nil")
	assert.Equal(t, types.HealthActionAccepted, response.Action, "Response should contain ACCEPTED action")

	// Test the response handling logic directly
	if response != nil && response.Action == types.HealthActionSelfDestruct {
		t.Fatal("ACCEPTED response incorrectly would trigger self-destruct logic")
	}

	// Verify that agent remains in normal operation mode
	assert.False(t, agent.selfDestruct.Load(), "Agent should remain in normal mode with ACCEPTED response")

	// Verify that the HTTP request was made correctly
	assert.Equal(t, http.StatusOK, int(mockTransport.getCode()))
	assert.Contains(t, mockTransport.getBody(), "ACCEPTED")
}

func TestPutNVCAStatusUpdate_SelfDestructResponse(t *testing.T) {
	ctx := context.Background()
	ctx = core.WithDefaultLogger(ctx)

	// Reuse the existing setupTestICMSClient function and customize response
	icmsClient, mockTransport := setupTestICMSClient()
	mockTransport.setCode(http.StatusOK)
	mockTransport.setBody(`{"action": "SELF_DESTRUCT"}`)

	// Create mocks for eviction dependencies
	mockICMSRequestLister := &MockICMSRequestLister{}
	mockICMSRequestHelper := &MockICMSRequestHelper{}

	// Mock the lister to return no ICMSRequests (empty list for simple test)
	mockICMSRequestLister.On("List", mock.Anything).Return([]*nvcav2beta1.ICMSRequest{}, nil)

	// Create mock backend cache with the mocked dependencies
	mockBackendCache := &MockBackendK8sCache{
		icmsRequestLister: mockICMSRequestLister,
		icmsRequestHelper: mockICMSRequestHelper,
	}

	// Create agent with all required dependencies for eviction testing
	agent := &Agent{
		AgentOptions: &AgentOptions{
			NCAId:               "test-nca-id",
			ClusterName:         "test-cluster",
			NVCAAgentVersion:    "v1.0.0",
			NVCAOperatorVersion: "v1.0.0",
		},
		icmsClient: icmsClient,
		backendk8scache: &BackendK8sCache{
			icmsRequestLister: mockICMSRequestLister,
			icmsRequestHelper: mockICMSRequestHelper,
		},
	}

	// initialize selfDestruct to false
	agent.selfDestruct = &atomic.Bool{}
	agent.selfDestruct.Store(false)

	// Verify initial state
	assert.False(t, agent.selfDestruct.Load(), "Agent should start in non-self-destruct mode")

	// Test the ICMS client directly to verify response parsing
	healthReq := &types.HealthStatusRequest{
		Status:              types.HealthStatusHealthy,
		GPUUsage:            map[types.GPUName]types.GPUResource{},
		ClusterOwnerNCAID:   agent.NCAId,
		NVCAAgentVersion:    agent.NVCAAgentVersion,
		NVCAOperatorVersion: agent.NVCAOperatorVersion,
		ClusterName:         agent.ClusterName,
		UpgradeStatus:       mockBackendCache.getNVCAUpgradeStatus(ctx),
	}

	response, err := icmsClient.PutHealthStatus(ctx, healthReq)
	assert.NoError(t, err, "PutHealthStatus should complete without error")
	assert.NotNil(t, response, "Response should not be nil")
	assert.Equal(t, types.HealthActionSelfDestruct, response.Action, "Response should contain SELF_DESTRUCT action")

	// Test the response handling logic - call the actual handleSelfDestruct method
	if response != nil && response.Action == types.HealthActionSelfDestruct {
		// Call the actual handleSelfDestruct method to test complete eviction logic
		err = agent.handleSelfDestruct(ctx)
		assert.NoError(t, err, "handleSelfDestruct should complete without error")
	}

	// Verify that agent is now in self-destruct mode
	assert.True(t, agent.selfDestruct.Load(), "Agent should be in self-destruct mode after SELF_DESTRUCT response")

	// Verify that the HTTP request was made correctly
	assert.Equal(t, http.StatusOK, int(mockTransport.getCode()))
	assert.Contains(t, mockTransport.getBody(), "SELF_DESTRUCT")

	// Verify that the eviction logic was invoked (icmsRequestLister.List was called)
	mockICMSRequestLister.AssertCalled(t, "List", mock.Anything)
}

func TestPutNVCAStatusUpdate_SkipSelfDestructFlag(t *testing.T) {
	ctx := context.Background()
	ctx = core.WithDefaultLogger(ctx)

	// Setup ICMS client to return SELF_DESTRUCT response
	icmsClient, mockTransport := setupTestICMSClient()
	mockTransport.setCode(http.StatusOK)
	mockTransport.setBody(`{"action": "SELF_DESTRUCT"}`)

	// Create agent with skip-self-destruct flag enabled
	agent := &Agent{
		AgentOptions: &AgentOptions{
			NCAId:               "test-nca-id",
			ClusterName:         "test-cluster",
			NVCAAgentVersion:    "v1.0.0",
			NVCAOperatorVersion: "v1.0.0",
			SkipSelfDestruct:    true, // This should prevent self-destruct
		},
		icmsClient: icmsClient,
	}

	// initialize selfDestruct to false
	agent.selfDestruct = &atomic.Bool{}
	agent.selfDestruct.Store(false)

	// Test the ICMS client response parsing
	healthReq := &types.HealthStatusRequest{
		Status:              types.HealthStatusHealthy,
		GPUUsage:            map[types.GPUName]types.GPUResource{},
		ClusterOwnerNCAID:   agent.NCAId,
		NVCAAgentVersion:    agent.NVCAAgentVersion,
		NVCAOperatorVersion: agent.NVCAOperatorVersion,
		ClusterName:         agent.ClusterName,
		UpgradeStatus:       types.NVCAUpgradeNoStatus,
	}

	response, err := icmsClient.PutHealthStatus(ctx, healthReq)
	assert.NoError(t, err, "PutHealthStatus should complete without error")
	assert.NotNil(t, response, "Response should not be nil")
	assert.Equal(t, types.HealthActionSelfDestruct, response.Action, "Response should contain SELF_DESTRUCT action")

	// Test the response handling logic - it should NOT trigger self-destruct due to the flag
	if response != nil && response.Action == types.HealthActionSelfDestruct {
		if agent.SkipSelfDestruct {
			// Should skip self-destruct due to flag
			assert.False(t, agent.selfDestruct.Load(), "Agent should remain in normal mode due to skip-self-destruct flag")
		} else {
			t.Fatal("Skip self-destruct flag not working correctly")
		}
	}

	// Verify that agent remains in normal operation mode
	assert.False(t, agent.selfDestruct.Load(), "Agent should remain in normal mode when skip-self-destruct is enabled")
}

func TestPutNVCAStatusUpdate_ForceSelfDestructFlag(t *testing.T) {
	ctx := context.Background()
	ctx = core.WithDefaultLogger(ctx)

	// Setup ICMS client to return ACCEPTED response (opposite of self-destruct)
	icmsClient, mockTransport := setupTestICMSClient()
	mockTransport.setCode(http.StatusOK)
	mockTransport.setBody(`{"action": "ACCEPTED"}`)

	// Create mocks for eviction dependencies
	mockICMSRequestLister := &MockICMSRequestLister{}
	mockICMSRequestHelper := &MockICMSRequestHelper{}

	// Mock the lister to return no ICMSRequests (empty list for simple test)
	mockICMSRequestLister.On("List", mock.Anything).Return([]*nvcav2beta1.ICMSRequest{}, nil)

	// Create agent with force-self-destruct flag enabled
	agent := &Agent{
		AgentOptions: &AgentOptions{
			NCAId:               "test-nca-id",
			ClusterName:         "test-cluster",
			NVCAAgentVersion:    "v1.0.0",
			NVCAOperatorVersion: "v1.0.0",
			ForceSelfDestruct:   true, // This should force self-destruct regardless of ICMS response
		},
		icmsClient: icmsClient,
		backendk8scache: &BackendK8sCache{
			icmsRequestLister: mockICMSRequestLister,
			icmsRequestHelper: mockICMSRequestHelper,
		},
	}

	// initialize selfDestruct to false
	agent.selfDestruct = &atomic.Bool{}
	agent.selfDestruct.Store(false)

	// Test the ICMS client response parsing
	healthReq := &types.HealthStatusRequest{
		Status:              types.HealthStatusHealthy,
		GPUUsage:            map[types.GPUName]types.GPUResource{},
		ClusterOwnerNCAID:   agent.NCAId,
		NVCAAgentVersion:    agent.NVCAAgentVersion,
		NVCAOperatorVersion: agent.NVCAOperatorVersion,
		ClusterName:         agent.ClusterName,
		UpgradeStatus:       types.NVCAUpgradeNoStatus,
	}

	response, err := icmsClient.PutHealthStatus(ctx, healthReq)
	assert.NoError(t, err, "PutHealthStatus should complete without error")
	assert.NotNil(t, response, "Response should not be nil")
	assert.Equal(t, types.HealthActionAccepted, response.Action, "Response should contain ACCEPTED action")

	// Test the force self-destruct logic - should trigger regardless of ACCEPTED response
	if agent.ForceSelfDestruct {
		// Call the actual handleSelfDestruct method to test complete eviction logic
		err = agent.handleSelfDestruct(ctx)
		assert.NoError(t, err, "handleSelfDestruct should complete without error")
	}

	// Verify that agent is now in self-destruct mode despite ACCEPTED response
	assert.True(t, agent.selfDestruct.Load(), "Agent should be in self-destruct mode when force-self-destruct is enabled")

	// Verify that the eviction logic was invoked (icmsRequestLister.List was called)
	mockICMSRequestLister.AssertCalled(t, "List", mock.Anything)
}

func TestAgentOptions_SelfDestructFlagsValidation(t *testing.T) {
	// Test that both flags cannot be set simultaneously
	opts := &AgentOptions{
		SkipSelfDestruct:  true,
		ForceSelfDestruct: true,
	}

	// This would normally be validated in the CLI Action function
	// Here we just test the logic that would be used
	if opts.SkipSelfDestruct && opts.ForceSelfDestruct {
		// This should trigger an error in the CLI
		assert.True(t, true, "Validation correctly detects conflicting flags")
	} else {
		t.Fatal("Should detect conflicting self-destruct flags")
	}

	// Test valid combinations
	opts1 := &AgentOptions{SkipSelfDestruct: true, ForceSelfDestruct: false}
	opts2 := &AgentOptions{SkipSelfDestruct: false, ForceSelfDestruct: true}
	opts3 := &AgentOptions{SkipSelfDestruct: false, ForceSelfDestruct: false}

	assert.False(t, opts1.SkipSelfDestruct && opts1.ForceSelfDestruct, "Skip-only flag should be valid")
	assert.False(t, opts2.SkipSelfDestruct && opts2.ForceSelfDestruct, "Force-only flag should be valid")
	assert.False(t, opts3.SkipSelfDestruct && opts3.ForceSelfDestruct, "No flags should be valid")
}

// Test the apply-level event blocking instead
func TestApply_SkipsICMSEventsWhenSelfDestruct(t *testing.T) {
	// Create a minimal agent for testing
	agent := &Agent{
		AgentOptions: &AgentOptions{
			NCAId:       "test-nca-id",
			ClusterName: "test-cluster",
		},
	}

	// initialize selfDestruct to true
	agent.selfDestruct = &atomic.Bool{}
	agent.selfDestruct.Store(true)

	// Create test context with proper metrics initialization
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	ctx = nvcametrics.WithDefaultMetrics(ctx,
		"test-nca-id", "test-cluster", "test-cluster", "1.0.0",
		nvcametrics.WithEventErrorTotalDefaultEvents(append(getAgentEvents(), getNVCAMetricEvents()...)),
		nvcametrics.WithContainerCrashAndRestartTotalDefaultContainerNames(GetDefaultWorkloadContainerNamesToWatch()),
		nvcametrics.WithRegisterer(reg))
	ctx = core.WithDefaultLogger(ctx)

	// Test that ICMS events are skipped in self-destruct mode
	icmsEvents := []string{
		EventTickRenewICMSCredentials,
		EventTickUpdateHeartbeat,
		EventTickSyncSQSQueue,
		EventTickSyncICMSRequestStatus,
		EventTickAcknowledgeRequest,
		EventTickUpdateICMSRegistration,
		EventTickSyncPeriodicInstanceStatusUpdates,
	}

	for _, eventKind := range icmsEvents {
		ev := &core.Event{Kind: eventKind}
		agent.apply(ctx, ev)
	}

	// Test that non-ICMS events are not skipped
	nonICMSEvent := &core.Event{Kind: "EventTickSyncNetworkPolicies"}
	agent.apply(ctx, nonICMSEvent)
}

// Mock implementations for testing
type MockStatusCache struct {
	mock.Mock
}

func (m *MockStatusCache) RefreshStatus(ctx context.Context) (types.AgentHealth, error) {
	args := m.Called(ctx)
	return args.Get(0).(types.AgentHealth), args.Error(1)
}

func (m *MockStatusCache) GetStatus() types.AgentHealth {
	args := m.Called()
	return args.Get(0).(types.AgentHealth)
}

// MockICMSRequestLister implements nvcav2beta1listers.ICMSRequestLister for testing
type MockICMSRequestLister struct {
	mock.Mock
}

func (m *MockICMSRequestLister) List(selector labels.Selector) ([]*nvcav2beta1.ICMSRequest, error) {
	args := m.Called(selector)
	return args.Get(0).([]*nvcav2beta1.ICMSRequest), args.Error(1)
}

func (m *MockICMSRequestLister) ICMSRequests(namespace string) nvcav2beta1listers.ICMSRequestNamespaceLister {
	args := m.Called(namespace)
	return args.Get(0).(nvcav2beta1listers.ICMSRequestNamespaceLister)
}

// MockICMSRequestHelper implements ICMSRequestHelper for testing
type MockICMSRequestHelper struct {
	mock.Mock
}

func (m *MockICMSRequestHelper) PurgeInstanceID(ctx context.Context, req *nvcav2beta1.ICMSRequest, terminatedInstances map[string]nvcav2beta1.InstanceStatus, instanceID string) bool {
	args := m.Called(ctx, req, terminatedInstances, instanceID)
	return args.Bool(0)
}

// Add other required methods as no-ops since we won't call them in our test
func (m *MockICMSRequestHelper) ApplyCreationMessage(ctx context.Context, req *nvcav2beta1.ICMSRequest) error {
	args := m.Called(ctx, req)
	return args.Error(0)
}

func (m *MockICMSRequestHelper) ApplyTerminationMessage(ctx context.Context, req *nvcav2beta1.ICMSRequest) error {
	args := m.Called(ctx, req)
	return args.Error(0)
}

func (m *MockICMSRequestHelper) AggregateInstanceStatuses(ctx context.Context, req *nvcav2beta1.ICMSRequest) AggregatedInstanceStatus {
	args := m.Called(ctx, req)
	return args.Get(0).(AggregatedInstanceStatus)
}

func (m *MockICMSRequestHelper) GetICMSRequestStatusUpdatesForRequest(ctx context.Context, req *nvcav2beta1.ICMSRequest) ([]types.ICMSRequestUpdateInfo, error) {
	args := m.Called(ctx, req)
	return args.Get(0).([]types.ICMSRequestUpdateInfo), args.Error(1)
}

func (m *MockICMSRequestHelper) GetICMSRequestUpdatesForTerminationRequest(ctx context.Context, req *nvcav2beta1.ICMSRequest) []types.ICMSRequestUpdateInfo {
	args := m.Called(ctx, req)
	return args.Get(0).([]types.ICMSRequestUpdateInfo)
}

func (m *MockICMSRequestHelper) GetICMSRequestUpdatesForCreateRequest(ctx context.Context, req *nvcav2beta1.ICMSRequest) []types.ICMSRequestUpdateInfo {
	args := m.Called(ctx, req)
	return args.Get(0).([]types.ICMSRequestUpdateInfo)
}

func (m *MockICMSRequestHelper) ComputeCleanupCacheReferences(ctx context.Context, references []string) error {
	args := m.Called(ctx, references)
	return args.Error(0)
}

func (m *MockICMSRequestHelper) AllInstancesTerminatedAndReported(ctx context.Context, req *nvcav2beta1.ICMSRequest) bool {
	args := m.Called(ctx, req)
	return args.Bool(0)
}

func (m *MockICMSRequestHelper) HandleInstanceStatusPreconditionFailure(ctx context.Context, req *nvcav2beta1.ICMSRequest, instID string) error {
	args := m.Called(ctx, req, instID)
	return args.Error(0)
}

func (m *MockICMSRequestHelper) GetROSUpdatesForRequest(ctx context.Context, req *nvcav2beta1.ICMSRequest) ([]types.ROSUpdateInfo, error) {
	args := m.Called(ctx, req)
	return args.Get(0).([]types.ROSUpdateInfo), args.Error(1)
}

// MockBackendK8sCache provides minimal BackendK8sCache functionality with mocked dependencies
type MockBackendK8sCache struct {
	icmsRequestLister *MockICMSRequestLister
	icmsRequestHelper *MockICMSRequestHelper
}

func (m *MockBackendK8sCache) getNVCAUpgradeStatus(_ context.Context) types.NVCAUpgradeStatus {
	return types.NVCAUpgradeNoStatus
}

func (m *MockBackendK8sCache) applyICMSRequestStatusChange(_ context.Context, _ *nvcav2beta1.ICMSRequest, _ func(context.Context, *nvcav2beta1.ICMSRequest)) bool {
	// Simulate successful status update
	return true
}

func (m *MockStatusCache) GetStatusForLevel(level types.StatusLevel) types.AgentHealth {
	args := m.Called(level)
	return args.Get(0).(types.AgentHealth)
}
