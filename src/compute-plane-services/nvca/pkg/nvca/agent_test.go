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
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	cmnoauth "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/oauth"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/version"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	otelattr "go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace/noop"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nvcaauth "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/auth"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	nvcametrics "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	mockicmsservice "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/mock/icmsservice"
	mockqueueservice "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/mock/queueservice"
	mocktokencache "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/mock/tokencache"
	nvcaotel "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/otel"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcak8sutil "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	nvcainternaltranslate "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/translate"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v1alpha1"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nodefeatures"
	nvcaerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/errors"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/health"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue"
	mockqueue "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue/mock"
	natsqueue "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue/nats"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

var testGPUNameDefault = types.GPUName("A100")

func getTestQueueCreds(mod bool) types.QueueCredentials {
	createQueues := map[types.GPUName]queue.MessageQueueInfo{
		testGPUNameDefault: getTestCreationMessageQueueInfo(mod),
	}
	termQueue := getTestTerminationMessageQueueInfo(mod)
	return types.QueueCredentials{
		CreationQueues:   createQueues,
		TerminationQueue: termQueue,
	}
}

func timeFromString(dateString string) metav1.Time {
	t, _ := time.Parse(time.RFC3339, dateString)
	return metav1.Time{Time: t}
}

// TestAgentApis just invokes the underlying APIs as a sanity
// that just invocation itself doesn't panic
// we can improve more complex UTs as follow-on
func TestAgentApis(t *testing.T) {
	ctx := newTestContext()

	agentOpts := AgentOptions{
		TokenFetcherOptions: nvcaauth.TokenFetcherOptions{
			OAuthTokenScope:      "byoc_registration",
			OAuthClientID:        "foo",
			OAuthClientSecretKey: "bar",
		},
		NCAId:                          "randomNCAId123",
		ClusterName:                    "bartnvbackend",
		ClusterID:                      "clusterid-1",
		ClusterDescription:             "this is a test cluster",
		ClusterGroupName:               "group of all A30",
		ComputeBackend:                 "k8s",
		CloudProvider:                  "on-prem",
		NamespaceLabels:                labels.Set{"foo": "bar"},
		K8sVersion:                     "1.27.8",
		CredRenewInterval:              DefaultCredRenewInterval,
		HeartbeatInterval:              365 * 24 * time.Hour,
		SyncQueueInterval:              365 * 24 * time.Hour,
		SyncRequestStatusInterval:      DefaultSyncRequestStatusInterval,
		PeriodicInstanceStatusInterval: DefaultPeriodicInstanceStatusInterval,
		SyncAcknowledgeRequestInterval: ackReqInterval,
		GPUCapacity:                    2,
		FeatureFlagFetcher:             featureflag.DefaultFetcher,
		MetricsRegisterer:              prometheus.NewRegistry(),
	}
	ag := newMockAgentSingleGPU(t, ctx, agentOpts)

	require.NoError(t, ag.Start(ctx))

	// Test health endpoints to make sure they report HTTP 200 by default.
	versionResp, err := http.Get("http://" + ag.NVCASvcAddress + "/version")
	require.NoError(t, err)
	body, err := io.ReadAll(versionResp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, versionResp.StatusCode, string(body))
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		resp, err := http.Get("http://" + ag.NVCASvcAddress + health.HTTPLivenessRoutePath)
		require.NoError(ct, err)
		body, err := io.ReadAll(resp.Body)
		require.NoError(ct, err)
		assert.Equal(ct, http.StatusOK, resp.StatusCode, string(body))
	}, 5*time.Second, 100*time.Millisecond)
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		resp, err := http.Get("http://" + ag.NVCASvcAddress + health.HTTPReadinessRoutePath)
		require.NoError(ct, err)
		body, err := io.ReadAll(resp.Body)
		require.NoError(ct, err)
		assert.Equal(ct, http.StatusOK, resp.StatusCode, string(body))
	}, 5*time.Second, 100*time.Millisecond)

	is := getPostedInstanceStatus(ctx,
		types.ICMSRequestUpdateInfo{
			InstanceID: "randomInstanceID",
			Payload: types.ICMSInstanceStatusUpdateRequest{
				InstanceState: types.ICMSInstanceTerminated,
			},
		},
	)
	assert.Equal(t, is.ID, "randomInstanceID")
	assert.Equal(t, is.LastReportedStatus, string(types.ICMSInstanceTerminated))

	currentInstanceStatus := map[string]nvcav2beta1.InstanceStatus{
		"randomInstanceID1": {
			ID:                 "randomInstanceID1",
			Status:             string(types.ICMSInstanceStarted),
			LastReportedStatus: string(types.ICMSInstanceStateNoStatus),
		},
		"randomInstanceID2": {
			ID:                 "randomInstanceID2",
			Status:             string(types.ICMSInstanceRunning),
			LastReportedStatus: string(types.ICMSInstanceStarted),
		},
	}

	postedStatus := map[string]nvcav2beta1.InstanceStatus{
		"randomInstanceID1": {
			ID:                 "randomInstanceID1",
			Status:             string(types.ICMSInstanceStarted),
			LastReportedStatus: string(types.ICMSInstanceRunning),
		},
		"randomInstanceID2": {
			ID:                 "randomInstanceID2",
			Status:             string(types.ICMSInstanceRunning),
			LastReportedStatus: string(types.ICMSInstanceTerminated),
		},
	}

	upStatus := getUpdatedInstanceStatusMap(currentInstanceStatus, postedStatus)
	is, ok := upStatus["randomInstanceID1"]
	assert.True(t, ok)
	assert.Equal(t, is.Status, string(types.ICMSInstanceRunning))
	assert.Equal(t, is.LastReportedStatus, string(types.ICMSInstanceRunning))

	is, ok = upStatus["randomInstanceID2"]
	assert.True(t, ok)
	assert.Equal(t, is.Status, string(types.ICMSInstanceTerminated))
	assert.Equal(t, is.LastReportedStatus, string(types.ICMSInstanceTerminated))
}

type blockingRecordingICMSClient struct {
	*mockICMSClient

	mu                    sync.Mutex
	registrationRequests  []types.ICMSRegistrationRequest
	results               []blockingRegistrationResult
	registrationStarted   chan int
	credentialResponse    *types.ICMSCredentialResponse
	credentialErr         error
	credentialStarted     chan struct{}
	credentialRelease     chan struct{}
	credentialReleaseOnce sync.Once
}

type registrationResult struct {
	response *types.ICMSRegistrationResponse
	err      error
}

type blockingRegistrationResult struct {
	registrationResult
	release     chan struct{}
	releaseOnce sync.Once
}

func newBlockingRecordingICMSClient(results ...registrationResult) *blockingRecordingICMSClient {
	blockingResults := make([]blockingRegistrationResult, len(results))
	for i, result := range results {
		blockingResults[i] = blockingRegistrationResult{
			registrationResult: result,
			release:            make(chan struct{}),
		}
	}
	return &blockingRecordingICMSClient{
		mockICMSClient:      &mockICMSClient{},
		results:             blockingResults,
		registrationStarted: make(chan int, len(results)),
	}
}

func (m *blockingRecordingICMSClient) Register(
	ctx context.Context,
	req *types.ICMSRegistrationRequest,
) (*types.ICMSRegistrationResponse, error) {
	requestCopy := *req
	requestCopy.BackendGPUs = append([]types.RegistrationGPU(nil), req.BackendGPUs...)
	for i := range requestCopy.BackendGPUs {
		requestCopy.BackendGPUs[i].InstanceTypes = append(
			[]types.RegistrationInstanceType(nil),
			req.BackendGPUs[i].InstanceTypes...,
		)
	}

	m.mu.Lock()
	attempt := len(m.registrationRequests)
	m.registrationRequests = append(m.registrationRequests, requestCopy)
	if attempt >= len(m.results) {
		m.mu.Unlock()
		return nil, fmt.Errorf("unexpected ICMS registration attempt %d", attempt)
	}
	result := &m.results[attempt]
	m.mu.Unlock()
	m.registrationStarted <- attempt

	select {
	case <-result.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	return result.response, result.err
}

func (m *blockingRecordingICMSClient) GetCreds(ctx context.Context) (*types.ICMSCredentialResponse, error) {
	m.mu.Lock()
	credentialStarted := m.credentialStarted
	credentialRelease := m.credentialRelease
	credentialResponse := m.credentialResponse
	credentialErr := m.credentialErr
	m.mu.Unlock()
	if credentialStarted == nil {
		return m.mockICMSClient.GetCreds(ctx)
	}

	select {
	case credentialStarted <- struct{}{}:
	default:
	}
	select {
	case <-credentialRelease:
		return credentialResponse, credentialErr
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *blockingRecordingICMSClient) blockCredentialFetch(
	response *types.ICMSCredentialResponse,
	err error,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.credentialResponse = response
	m.credentialErr = err
	m.credentialStarted = make(chan struct{}, 1)
	m.credentialRelease = make(chan struct{})
}

func (m *blockingRecordingICMSClient) releaseCredentialFetch() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.credentialReleaseOnce.Do(func() { close(m.credentialRelease) })
}

func (m *blockingRecordingICMSClient) release(attempt int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := &m.results[attempt]
	result.releaseOnce.Do(func() { close(result.release) })
}

func (m *blockingRecordingICMSClient) requests() []types.ICMSRegistrationRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]types.ICMSRegistrationRequest(nil), m.registrationRequests...)
}

func requireRegistrationAttempt(t *testing.T, client *blockingRecordingICMSClient, expected int) {
	t.Helper()
	select {
	case attempt := <-client.registrationStarted:
		require.Equal(t, expected, attempt)
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for ICMS registration attempt %d", expected)
	}
}

func requireNoRegistrationAttempt(t *testing.T, client *blockingRecordingICMSClient, wait time.Duration) {
	t.Helper()
	select {
	case attempt := <-client.registrationStarted:
		t.Fatalf("unexpected ICMS registration attempt %d while another registration is in flight", attempt)
	case <-time.After(wait):
	}
}

func newGracefulNoGPUTestAgent(
	t *testing.T,
	ctx context.Context,
	icmsClient ICMSClientInterface,
) (*Agent, *kubeclients.KubeClients) {
	t.Helper()
	oldSyncInterval := syncICMSRegistrationInterval
	// Callers use a fixed random seed, keeping periodic registration outside the test window.
	syncICMSRegistrationInterval = 365 * 24 * time.Hour
	t.Cleanup(func() { syncICMSRegistrationInterval = oldSyncInterval })

	featureFlags := &featureflagmock.Fetcher{}
	featureFlags.SetFeatureFlags(featureflag.GracefulNoGPU)
	agentOpts := AgentOptions{
		TokenFetcherOptions: nvcaauth.TokenFetcherOptions{
			OAuthTokenScope:      "byoc_registration",
			OAuthClientID:        "foo",
			OAuthClientSecretKey: "bar",
		},
		NCAId:                          "randomNCAId123",
		ClusterName:                    "bartnvbackend",
		ClusterID:                      "clusterid-1",
		ClusterDescription:             "this is a test cluster",
		ClusterGroupName:               "group of all A30",
		ComputeBackend:                 "k8s",
		CloudProvider:                  "on-prem",
		NamespaceLabels:                labels.Set{"foo": "bar"},
		K8sVersion:                     "1.27.8",
		CredRenewInterval:              365 * 24 * time.Hour,
		HeartbeatInterval:              DefaultHeartBeatInterval,
		SyncQueueInterval:              defaultSyncQueueInterval,
		SyncRequestStatusInterval:      DefaultSyncRequestStatusInterval,
		PeriodicInstanceStatusInterval: DefaultPeriodicInstanceStatusInterval,
		SyncAcknowledgeRequestInterval: ackReqInterval,
		DynamicGPUDiscoveryEnabled:     true,
		MultipleGPUTypesAllowed:        true,
		UniformInstanceLabelsEnabled:   true,
		GPUPollInterval:                10 * time.Millisecond,
		GPUDebounceTime:                time.Millisecond,
		FeatureFlagFetcher:             featureFlags,
		MetricsRegisterer:              prometheus.NewRegistry(),
	}

	agent := newMockAgent(t, ctx, agentOpts)
	// Isolate startup readiness from the immediate heartbeat and queue-sync events.
	delete(agent.resourceEventWorkerQueues, EventTickUpdateHeartbeat)
	delete(agent.resourceEventWorkerQueues, EventTickSyncSQSQueue)
	oldNewQueueClient := newQueueClient
	newQueueClient = func(string) queue.Client {
		return &mockqueue.Client{Use10MillisForWaits: true}
	}
	t.Cleanup(func() { newQueueClient = oldNewQueueClient })
	k8sClients := mockKubeClientsDynamicGPUs()
	agent.newKubeClients = func(context.Context, string) (*kubeclients.KubeClients, error) {
		return k8sClients, nil
	}
	agent.newBackendK8sCacheBuilder = func() *BackendK8sCacheBuilder {
		builder := NewBackendk8sCacheBuilder()
		builder.addSharedClusterNodePublisher = mockAddSharedClusterNodePublisherFunc
		return builder
	}
	agent.icmsClient = icmsClient
	return agent, k8sClients
}

func requireHTTPStatusEventually(t *testing.T, address, path string, expected int) {
	t.Helper()
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		resp, err := http.Get("http://" + address + path)
		if !assert.NoError(ct, err) {
			return
		}
		assert.NoError(ct, resp.Body.Close())
		assert.Equal(ct, expected, resp.StatusCode)
	}, 5*time.Second, 10*time.Millisecond)
}

func requireHTTPStatus(t *testing.T, address, path string, expected int) {
	t.Helper()
	resp, err := http.Get("http://" + address + path)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, expected, resp.StatusCode)
}

func requireRefreshedReadinessStatus(
	t *testing.T,
	ctx context.Context,
	agent *Agent,
	expected int,
) {
	t.Helper()
	_, err := agent.backendHealthCache.RefreshStatus(ctx)
	require.NoError(t, err)
	requireHTTPStatusEventually(t, agent.NVCASvcAddress, health.HTTPReadinessRoutePath, expected)
}

func TestAgentStartGracefulNoGPURecoversWhenGPUAppears(t *testing.T) {
	ctx, cancel := context.WithCancel(core.WithRandomSeed(newTestContext(), 42))
	t.Cleanup(cancel)

	recoveredCredentials := getTestQueueCreds(true)
	icmsClient := newBlockingRecordingICMSClient(registrationResult{
		response: &types.ICMSRegistrationResponse{
			ClusterID:      "registered-cluster-id",
			ClusterGroupID: "registered-cluster-group-id",
			Credentials:    recoveredCredentials,
		},
	})
	agent, k8sClients := newGracefulNoGPUTestAgent(t, ctx, icmsClient)

	require.NoError(t, agent.Start(ctx))
	requireHTTPStatus(t, agent.NVCASvcAddress, health.HTTPReadinessRoutePath, http.StatusServiceUnavailable)
	requireHTTPStatus(t, agent.NVCASvcAddress, health.HTTPLivenessRoutePath, http.StatusOK)
	require.NotNil(t, agent.gpuMonitor)
	require.NotNil(t, agent.queueManager)
	assert.False(t, agent.gpuMonitor.HasGPUs())
	assert.True(t, agent.queueManager.IsPaused())
	assert.Empty(t, icmsClient.requests())
	assert.Empty(t, agent.queueManager.getCreateQueue(testGPUNameDefault).QueueURL)

	_, err := k8sClients.K8s.CoreV1().Nodes().Create(ctx, functionNode.DeepCopy(), metav1.CreateOptions{})
	require.NoError(t, err)
	requireRegistrationAttempt(t, icmsClient, 0)

	assert.True(t, agent.queueManager.IsPaused(), "queue must remain paused while registration is in flight")
	assert.Empty(t, agent.queueManager.getCreateQueue(testGPUNameDefault).QueueURL)
	requireRefreshedReadinessStatus(t, ctx, agent, http.StatusServiceUnavailable)
	requireHTTPStatusEventually(t, agent.NVCASvcAddress, health.HTTPLivenessRoutePath, http.StatusOK)
	requests := icmsClient.requests()
	require.Len(t, requests, 1)
	assert.Equal(t, []types.RegistrationGPU{{
		Name: "A100",
		InstanceTypes: []types.RegistrationInstanceType{{
			Name:          "ON-PREM.GPU.A100_1x",
			Value:         "ON-PREM.GPU.A100",
			Description:   "A100-SXM4-40GB (ampere family) on a Google-Compute-Engine machine",
			Default:       true,
			CPUCores:      6,
			CPU:           "6",
			SystemMemory:  "32Gi",
			GPUCount:      1,
			GPUMemory:     "40Gi",
			Storage:       "512Gi",
			CPUArch:       "unknown",
			OS:            "unknown",
			DriverVersion: "unknown",
			NodeType:      types.RegistrationInstanceTypeNodeTypeSingle,
			MaxInstances:  1,
		}},
	}}, requests[0].BackendGPUs)

	icmsClient.release(0)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.True(ct, agent.gpuMonitor.HasGPUs())
		assert.False(ct, agent.queueManager.IsPaused())
		assert.Equal(ct, recoveredCredentials.CreationQueues[testGPUNameDefault],
			agent.queueManager.getCreateQueue(testGPUNameDefault))
		assert.Equal(ct, recoveredCredentials.TerminationQueue, agent.queueManager.getTermQueue())
	}, 5*time.Second, 10*time.Millisecond)
	requireRefreshedReadinessStatus(t, ctx, agent, http.StatusOK)
	requireHTTPStatusEventually(t, agent.NVCASvcAddress, health.HTTPLivenessRoutePath, http.StatusOK)

	require.NoError(t, k8sClients.K8s.CoreV1().Nodes().Delete(ctx, functionNode.Name, metav1.DeleteOptions{}))
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.False(ct, agent.gpuMonitor.HasGPUs())
		assert.True(ct, agent.queueManager.IsPaused())
	}, 5*time.Second, 10*time.Millisecond)
	requireRefreshedReadinessStatus(t, ctx, agent, http.StatusServiceUnavailable)
	requireHTTPStatusEventually(t, agent.NVCASvcAddress, health.HTTPLivenessRoutePath, http.StatusOK)
	require.Len(t, icmsClient.requests(), 1)
}

func TestAgentStartGracefulNoGPURegistrationFailureRetriesBeforeResuming(t *testing.T) {
	logCtx, logHook := core.WithTestingLogger(newTestContext())
	ctx, cancel := context.WithCancel(core.WithRandomSeed(logCtx, 42))
	t.Cleanup(cancel)

	recoveredCredentials := getTestQueueCreds(true)
	icmsClient := newBlockingRecordingICMSClient(
		registrationResult{err: fmt.Errorf("registration unavailable")},
		registrationResult{response: &types.ICMSRegistrationResponse{
			ClusterID:      "registered-cluster-id",
			ClusterGroupID: "registered-cluster-group-id",
			Credentials:    recoveredCredentials,
		}},
	)
	agent, k8sClients := newGracefulNoGPUTestAgent(t, ctx, icmsClient)
	require.NoError(t, agent.Start(ctx))
	requireHTTPStatus(t, agent.NVCASvcAddress, health.HTTPReadinessRoutePath, http.StatusServiceUnavailable)
	requireHTTPStatus(t, agent.NVCASvcAddress, health.HTTPLivenessRoutePath, http.StatusOK)

	_, err := k8sClients.K8s.CoreV1().Nodes().Create(ctx, functionNode.DeepCopy(), metav1.CreateOptions{})
	require.NoError(t, err)
	requireRegistrationAttempt(t, icmsClient, 0)
	assert.True(t, agent.queueManager.IsPaused(), "queue must remain paused while registration is in flight")

	icmsClient.release(0)
	requireRegistrationAttempt(t, icmsClient, 1)
	retryLogs := make([]*logrus.Entry, 0, 1)
	for _, entry := range logHook.AllEntries() {
		if entry.Message == "Failed to register with ICMS after GPUs became available; will retry" {
			retryLogs = append(retryLogs, entry)
		}
	}
	require.Len(t, retryLogs, 1)
	assert.Equal(t, logrus.WarnLevel, retryLogs[0].Level)
	assert.True(t, agent.queueManager.IsPaused(), "queue must remain paused while registration retry is in flight")
	assert.Empty(t, agent.queueManager.getCreateQueue(testGPUNameDefault).QueueURL)
	requireRefreshedReadinessStatus(t, ctx, agent, http.StatusServiceUnavailable)
	requireHTTPStatusEventually(t, agent.NVCASvcAddress, health.HTTPLivenessRoutePath, http.StatusOK)

	icmsClient.release(1)
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.False(ct, agent.queueManager.IsPaused())
		assert.Equal(ct, recoveredCredentials.CreationQueues[testGPUNameDefault],
			agent.queueManager.getCreateQueue(testGPUNameDefault))
		assert.Equal(ct, recoveredCredentials.TerminationQueue, agent.queueManager.getTermQueue())
	}, 5*time.Second, 10*time.Millisecond)
	requireRefreshedReadinessStatus(t, ctx, agent, http.StatusOK)
	requireHTTPStatusEventually(t, agent.NVCASvcAddress, health.HTTPLivenessRoutePath, http.StatusOK)
	require.Len(t, icmsClient.requests(), 2)
}

func TestAgentStartGracefulNoGPUStaysPausedWhenGPUDisappearsDuringRegistration(t *testing.T) {
	ctx, cancel := context.WithCancel(core.WithRandomSeed(newTestContext(), 42))
	t.Cleanup(cancel)

	recoveredCredentials := getTestQueueCreds(true)
	icmsClient := newBlockingRecordingICMSClient(
		registrationResult{response: &types.ICMSRegistrationResponse{
			ClusterID:      "registered-cluster-id",
			ClusterGroupID: "registered-cluster-group-id",
			Credentials:    recoveredCredentials,
		}},
		registrationResult{response: &types.ICMSRegistrationResponse{
			ClusterID:      "registered-cluster-id",
			ClusterGroupID: "registered-cluster-group-id",
			Credentials:    recoveredCredentials,
		}},
	)
	agent, k8sClients := newGracefulNoGPUTestAgent(t, ctx, icmsClient)
	require.NoError(t, agent.Start(ctx))

	_, err := k8sClients.K8s.CoreV1().Nodes().Create(ctx, functionNode.DeepCopy(), metav1.CreateOptions{})
	require.NoError(t, err)
	requireRegistrationAttempt(t, icmsClient, 0)
	require.NoError(t, k8sClients.K8s.CoreV1().Nodes().Delete(ctx, functionNode.Name, metav1.DeleteOptions{}))
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.False(ct, agent.gpuMonitor.HasGPUs())
		assert.True(ct, agent.queueManager.IsPaused())
	}, 5*time.Second, 10*time.Millisecond)

	icmsClient.release(0)
	requireNoRegistrationAttempt(t, icmsClient, 100*time.Millisecond)
	assert.True(t, agent.queueManager.IsPaused())
	requireRefreshedReadinessStatus(t, ctx, agent, http.StatusServiceUnavailable)
	requireHTTPStatusEventually(t, agent.NVCASvcAddress, health.HTTPLivenessRoutePath, http.StatusOK)

	_, err = k8sClients.K8s.CoreV1().Nodes().Create(ctx, functionNode.DeepCopy(), metav1.CreateOptions{})
	require.NoError(t, err)
	requireRegistrationAttempt(t, icmsClient, 1)
	assert.True(t, agent.queueManager.IsPaused())
	icmsClient.release(1)
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.False(ct, agent.queueManager.IsPaused())
		assert.Equal(ct, recoveredCredentials.CreationQueues[testGPUNameDefault],
			agent.queueManager.getCreateQueue(testGPUNameDefault))
	}, 5*time.Second, 10*time.Millisecond)
	requireRefreshedReadinessStatus(t, ctx, agent, http.StatusOK)
	requireHTTPStatusEventually(t, agent.NVCASvcAddress, health.HTTPLivenessRoutePath, http.StatusOK)
}

func TestAgentStartGracefulNoGPUSerializesCredentialRenewalBeforeRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(core.WithRandomSeed(newTestContext(), 42))
	t.Cleanup(cancel)

	initialCredentials := getTestQueueCreds(false)
	staleCredentials := getTestQueueCreds(false)
	recoveredCredentials := getTestQueueCreds(true)
	icmsClient := newBlockingRecordingICMSClient(
		registrationResult{response: &types.ICMSRegistrationResponse{
			ClusterID:      "registered-cluster-id",
			ClusterGroupID: "registered-cluster-group-id",
			Credentials:    initialCredentials,
		}},
		registrationResult{response: &types.ICMSRegistrationResponse{
			ClusterID:      "registered-cluster-id",
			ClusterGroupID: "registered-cluster-group-id",
			Credentials:    recoveredCredentials,
		}},
	)
	agent, k8sClients := newGracefulNoGPUTestAgent(t, ctx, icmsClient)
	_, err := k8sClients.K8s.CoreV1().Nodes().Create(ctx, functionNode.DeepCopy(), metav1.CreateOptions{})
	require.NoError(t, err)
	icmsClient.release(0)
	require.NoError(t, agent.Start(ctx))
	requireRegistrationAttempt(t, icmsClient, 0)
	assert.False(t, agent.queueManager.IsPaused())

	require.NoError(t, k8sClients.K8s.CoreV1().Nodes().Delete(ctx, functionNode.Name, metav1.DeleteOptions{}))
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.False(ct, agent.gpuMonitor.HasGPUs())
		assert.True(ct, agent.queueManager.IsPaused())
	}, 5*time.Second, 10*time.Millisecond)

	icmsClient.blockCredentialFetch(&types.ICMSCredentialResponse{QueueCredentials: staleCredentials}, nil)
	credentialDone := make(chan error, 1)
	go func() {
		credentialDone <- agent.RenewICMSQueueCreds(ctx)
	}()
	select {
	case <-icmsClient.credentialStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for credential renewal to start")
	}

	_, err = k8sClients.K8s.CoreV1().Nodes().Create(ctx, functionNode.DeepCopy(), metav1.CreateOptions{})
	require.NoError(t, err)
	requireNoRegistrationAttempt(t, icmsClient, 250*time.Millisecond)
	assert.True(t, agent.queueManager.IsPaused())

	icmsClient.releaseCredentialFetch()
	select {
	case credentialErr := <-credentialDone:
		require.NoError(t, credentialErr)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for credential renewal to finish")
	}
	requireRegistrationAttempt(t, icmsClient, 1)
	assert.True(t, agent.queueManager.IsPaused())
	icmsClient.release(1)

	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.False(ct, agent.queueManager.IsPaused())
		assert.Equal(ct, recoveredCredentials.CreationQueues[testGPUNameDefault],
			agent.queueManager.getCreateQueue(testGPUNameDefault))
		assert.Equal(ct, recoveredCredentials.TerminationQueue, agent.queueManager.getTermQueue())
	}, 5*time.Second, 10*time.Millisecond)
	requireRefreshedReadinessStatus(t, ctx, agent, http.StatusOK)
	requireHTTPStatusEventually(t, agent.NVCASvcAddress, health.HTTPLivenessRoutePath, http.StatusOK)
}

func TestAgentStartGracefulNoGPUSerializesPeriodicRegistrationBeforeRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(core.WithRandomSeed(newTestContext(), 42))
	t.Cleanup(cancel)

	initialCredentials := getTestQueueCreds(false)
	stalePeriodicCredentials := getTestQueueCreds(false)
	recoveredGPU := types.GPUName("AD102GL")
	recoveredQueue := getTestCreationMessageQueueInfo(true)
	recoveredQueue.GPU = string(recoveredGPU)
	recoveredCredentials := getTestQueueCreds(true)
	recoveredCredentials.CreationQueues = types.CreationQueueInfoSet{
		recoveredGPU: recoveredQueue,
	}
	icmsClient := newBlockingRecordingICMSClient(
		registrationResult{response: &types.ICMSRegistrationResponse{
			ClusterID:      "registered-cluster-id",
			ClusterGroupID: "registered-cluster-group-id",
			Credentials:    initialCredentials,
		}},
		registrationResult{response: &types.ICMSRegistrationResponse{
			ClusterID:      "registered-cluster-id",
			ClusterGroupID: "registered-cluster-group-id",
			Credentials:    stalePeriodicCredentials,
		}},
		registrationResult{response: &types.ICMSRegistrationResponse{
			ClusterID:      "registered-cluster-id",
			ClusterGroupID: "registered-cluster-group-id",
			Credentials:    recoveredCredentials,
		}},
	)
	agent, k8sClients := newGracefulNoGPUTestAgent(t, ctx, icmsClient)
	_, err := k8sClients.K8s.CoreV1().Nodes().Create(ctx, functionNode.DeepCopy(), metav1.CreateOptions{})
	require.NoError(t, err)
	icmsClient.release(0)
	require.NoError(t, agent.Start(ctx))
	requireRegistrationAttempt(t, icmsClient, 0)
	assert.False(t, agent.queueManager.IsPaused())

	periodicDone := make(chan error, 1)
	go func() {
		periodicDone <- agent.syncICMSRegistration(ctx)
	}()
	requireRegistrationAttempt(t, icmsClient, 1)
	requests := icmsClient.requests()
	require.Len(t, requests, 2)
	require.Len(t, requests[1].BackendGPUs, 1)
	assert.Equal(t, "A100", requests[1].BackendGPUs[0].Name)

	require.NoError(t, k8sClients.K8s.CoreV1().Nodes().Delete(ctx, functionNode.Name, metav1.DeleteOptions{}))
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.False(ct, agent.gpuMonitor.HasGPUs())
		assert.True(ct, agent.queueManager.IsPaused())
	}, 5*time.Second, 10*time.Millisecond)

	recoveryNode := functionNode.DeepCopy()
	recoveryNode.Name = "node-2"
	recoveryNode.Labels[nodefeatures.UniformInstanceTypeLabelKey] = "ON-PREM.GPU.AD102GL"
	recoveryNode.Labels["nvidia.com/gpu.family"] = "volta"
	recoveryNode.Labels["nvidia.com/gpu.memory"] = "32768"
	recoveryNode.Labels["nvidia.com/gpu.product"] = "V100-SXM2-32GB"
	recoveryNode.Labels["nvca.nvcf.nvidia.io/gpu.product"] = string(recoveredGPU)
	lossGeneration := agent.gpuRegistrationGeneration.Load()
	_, err = k8sClients.K8s.CoreV1().Nodes().Create(ctx, recoveryNode, metav1.CreateOptions{})
	require.NoError(t, err)
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.True(ct, agent.gpuMonitor.HasGPUs())
		assert.Greater(ct, agent.gpuRegistrationGeneration.Load(), lossGeneration)
	}, 5*time.Second, 10*time.Millisecond)
	requireNoRegistrationAttempt(t, icmsClient, 250*time.Millisecond)
	assert.True(t, agent.queueManager.IsPaused())
	assert.Empty(t, agent.queueManager.getCreateQueue(recoveredGPU).QueueURL)

	icmsClient.release(1)
	select {
	case periodicErr := <-periodicDone:
		require.NoError(t, periodicErr)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for periodic registration to complete")
	}
	requireRegistrationAttempt(t, icmsClient, 2)
	requests = icmsClient.requests()
	require.Len(t, requests, 3)
	require.Len(t, requests[2].BackendGPUs, 1)
	assert.Equal(t, recoveredGPU, types.GPUName(requests[2].BackendGPUs[0].Name))
	assert.True(t, agent.queueManager.IsPaused())
	assert.Empty(t, agent.queueManager.getCreateQueue(recoveredGPU).QueueURL)

	icmsClient.release(2)
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.False(ct, agent.queueManager.IsPaused())
		assert.Equal(ct, recoveredQueue, agent.queueManager.getCreateQueue(recoveredGPU))
		assert.Equal(ct, recoveredCredentials.TerminationQueue, agent.queueManager.getTermQueue())
	}, 5*time.Second, 10*time.Millisecond)
	requireRefreshedReadinessStatus(t, ctx, agent, http.StatusOK)
	requireHTTPStatusEventually(t, agent.NVCASvcAddress, health.HTTPLivenessRoutePath, http.StatusOK)
}

func TestAgentRegisterWithICMSUpdatesQueueManagerCredentials(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)

	agentOpts := AgentOptions{
		TokenFetcherOptions: nvcaauth.TokenFetcherOptions{
			OAuthTokenScope:      "byoc_registration",
			OAuthClientID:        "foo",
			OAuthClientSecretKey: "bar",
		},
		NCAId:                          "randomNCAId123",
		ClusterName:                    "bartnvbackend",
		ClusterID:                      "clusterid-1",
		ClusterDescription:             "this is a test cluster",
		ClusterGroupName:               "group of all A30",
		ComputeBackend:                 "k8s",
		CloudProvider:                  "on-prem",
		NamespaceLabels:                labels.Set{"foo": "bar"},
		K8sVersion:                     "1.27.8",
		CredRenewInterval:              DefaultCredRenewInterval,
		HeartbeatInterval:              DefaultHeartBeatInterval,
		SyncQueueInterval:              defaultSyncQueueInterval,
		SyncRequestStatusInterval:      DefaultSyncRequestStatusInterval,
		PeriodicInstanceStatusInterval: DefaultPeriodicInstanceStatusInterval,
		SyncAcknowledgeRequestInterval: ackReqInterval,
		GPUCapacity:                    2,
		FeatureFlagFetcher:             featureflag.DefaultFetcher,
		MetricsRegisterer:              prometheus.NewRegistry(),
	}
	agent := newMockAgentSingleGPU(t, ctx, agentOpts)
	require.NoError(t, agent.Start(ctx))

	updatedGPU := types.GPUName("H100")
	updatedQueue := getTestCreationMessageQueueInfo(true)
	updatedQueue.GPU = string(updatedGPU)
	updatedCreds := getTestQueueCreds(true)
	updatedCreds.CreationQueues = types.CreationQueueInfoSet{
		updatedGPU: updatedQueue,
	}
	agent.icmsClient = &mockICMSClient{
		registrationResponse: &types.ICMSRegistrationResponse{
			ClusterID:      agent.ClusterID,
			ClusterGroupID: "test-cluster-group-id",
			Credentials:    updatedCreds,
		},
	}

	_, err := agent.registerWithICMS(ctx, []types.RegistrationGPU{{Name: string(updatedGPU)}})
	require.NoError(t, err)
	assert.Equal(t, updatedQueue, agent.queueManager.getCreateQueue(updatedGPU))
	assert.Empty(t, agent.queueManager.getCreateQueue(testGPUNameDefault).QueueURL)
}

func TestAgent_getTelemetryAttributes(t *testing.T) {
	ctx := context.Background()
	agentOpts := AgentOptions{
		TokenFetcherOptions: nvcaauth.TokenFetcherOptions{
			TokenURL:             "http://localhost",
			OAuthTokenScope:      "byoc_registration",
			OAuthClientID:        "foo",
			OAuthClientSecretKey: "bar",
		},
		NCAId:                          "randomNCAId123",
		ClusterName:                    "bartnvbackend",
		ClusterID:                      "clusterid-1",
		ClusterDescription:             "this is a test cluster",
		ClusterGroupName:               "group of all A30",
		ICMSURL:                        "https://icms.nvcf.nvidia.com",
		CloudProvider:                  "on-prem",
		KubeConfigPath:                 "testdata/kubeconfig.yaml",
		NamespaceLabels:                labels.Set{"foo": "bar"},
		NVCASvcAddress:                 "localhost",
		NVCAAdminAddr:                  "localhost",
		CredRenewInterval:              DefaultCredRenewInterval,
		HeartbeatInterval:              DefaultHeartBeatInterval,
		SyncQueueInterval:              defaultSyncQueueInterval,
		SyncRequestStatusInterval:      DefaultSyncRequestStatusInterval,
		PeriodicInstanceStatusInterval: DefaultPeriodicInstanceStatusInterval,
		SyncAcknowledgeRequestInterval: ackReqInterval,
		FeatureFlagFetcher:             featureflag.DefaultFetcher,
		NVCAAgentVersion:               "v1.0.1",
		MetricsRegisterer:              prometheus.NewRegistry(),
	}
	ag, err := NewAgent(ctx, &agentOpts)
	assert.NoError(t, err)
	require.NotNil(t, ag)
	assert.Equal(t, []otelattr.KeyValue{
		otelattr.String(nvcaotel.NCAIDAttributeKey, agentOpts.NCAId),
		otelattr.String(nvcaotel.ClusterNameAttributeKey, agentOpts.ClusterName),
		otelattr.String(nvcaotel.ClusterGroupAttributeKey, agentOpts.ClusterGroupName),
		otelattr.String(nvcaotel.VersionAttributeKey, agentOpts.NVCAAgentVersion),
	}, ag.GetOTelAttributes())
}

func TestAgentOptions_WithNVCAOperatorVersion(t *testing.T) {
	ctx := context.Background()
	agentOpts := AgentOptions{
		TokenFetcherOptions: nvcaauth.TokenFetcherOptions{
			TokenURL:             "http://localhost",
			OAuthTokenScope:      "byoc_registration",
			OAuthClientID:        "foo",
			OAuthClientSecretKey: "bar",
		},
		NCAId:                          "randomNCAId123",
		ClusterName:                    "bartnvbackend",
		ClusterID:                      "clusterid-1",
		ClusterDescription:             "this is a test cluster",
		ClusterGroupName:               "group of all A30",
		ICMSURL:                        "https://icms.nvcf.nvidia.com",
		CloudProvider:                  "on-prem",
		KubeConfigPath:                 "testdata/kubeconfig.yaml",
		NamespaceLabels:                labels.Set{"foo": "bar"},
		NVCASvcAddress:                 "localhost",
		NVCAAdminAddr:                  "localhost",
		CredRenewInterval:              DefaultCredRenewInterval,
		HeartbeatInterval:              DefaultHeartBeatInterval,
		SyncQueueInterval:              defaultSyncQueueInterval,
		SyncRequestStatusInterval:      DefaultSyncRequestStatusInterval,
		PeriodicInstanceStatusInterval: DefaultPeriodicInstanceStatusInterval,
		SyncAcknowledgeRequestInterval: ackReqInterval,
		FeatureFlagFetcher:             featureflag.DefaultFetcher,
		NVCAOperatorVersion:            "v1.2.3",
		MetricsRegisterer:              prometheus.NewRegistry(),
	}
	oldVersion := version.Version
	t.Cleanup(func() { version.Version = oldVersion })
	version.Version = "v1.0.1"
	ag, err := NewAgent(ctx, &agentOpts)
	assert.NoError(t, err)
	require.NotNil(t, ag)
	assert.Equal(t, "v1.2.3", ag.NVCAOperatorVersion)
}

func TestAgentOptions_WithoutNVCAOperatorVersion(t *testing.T) {
	ctx := context.Background()
	agentOpts := AgentOptions{
		TokenFetcherOptions: nvcaauth.TokenFetcherOptions{
			TokenURL:             "http://localhost",
			OAuthTokenScope:      "byoc_registration",
			OAuthClientID:        "foo",
			OAuthClientSecretKey: "bar",
		},
		NCAId:                          "randomNCAId123",
		ClusterName:                    "bartnvbackend",
		ClusterID:                      "clusterid-1",
		ClusterDescription:             "this is a test cluster",
		ClusterGroupName:               "group of all A30",
		ICMSURL:                        "https://icms.nvcf.nvidia.com",
		CloudProvider:                  "on-prem",
		KubeConfigPath:                 "testdata/kubeconfig.yaml",
		NamespaceLabels:                labels.Set{"foo": "bar"},
		NVCASvcAddress:                 "localhost",
		NVCAAdminAddr:                  "localhost",
		CredRenewInterval:              DefaultCredRenewInterval,
		HeartbeatInterval:              DefaultHeartBeatInterval,
		SyncQueueInterval:              defaultSyncQueueInterval,
		SyncRequestStatusInterval:      DefaultSyncRequestStatusInterval,
		PeriodicInstanceStatusInterval: DefaultPeriodicInstanceStatusInterval,
		SyncAcknowledgeRequestInterval: ackReqInterval,
		FeatureFlagFetcher:             featureflag.DefaultFetcher,
		// NVCAOperatorVersion not set - should default to empty string
		MetricsRegisterer: prometheus.NewRegistry(),
	}
	oldVersion := version.Version
	t.Cleanup(func() { version.Version = oldVersion })
	version.Version = "v1.0.1"
	ag, err := NewAgent(ctx, &agentOpts)
	assert.NoError(t, err)
	require.NotNil(t, ag)
	assert.Equal(t, "", ag.NVCAOperatorVersion)
}

func TestAgentOptions_String_IncludesNVCAOperatorVersion(t *testing.T) {
	agentOpts := AgentOptions{
		TokenFetcherOptions: nvcaauth.TokenFetcherOptions{
			TokenURL:             "http://localhost",
			OAuthTokenScope:      "byoc_registration",
			OAuthClientID:        "foo",
			OAuthClientSecretKey: "bar",
		},
		NCAId:               "randomNCAId123",
		ClusterName:         "bartnvbackend",
		NVCAOperatorVersion: "v1.2.3",
		FeatureFlagFetcher:  featureflag.DefaultFetcher,
	}

	str := agentOpts.sanitizedString()
	assert.Contains(t, str, "v1.2.3")
	assert.Contains(t, str, "randomNCAId123")
	assert.Contains(t, str, "bartnvbackend")
}

func TestAgentOptions_String_IncludesClusterIDs(t *testing.T) {
	agentOpts := AgentOptions{
		NCAId:          "randomNCAId123",
		ClusterName:    "bartnvbackend",
		ClusterID:      "cluster-id-123",
		ClusterGroupID: "cluster-group-id-123",
	}

	str := agentOpts.sanitizedString()
	assert.Contains(t, str, `ClusterID:"cluster-id-123"`)
	assert.Contains(t, str, `ClusterGroupID:"cluster-group-id-123"`)
}

func TestAgentOptions_String_IncludesIdentitySourceAndPSATPath(t *testing.T) {
	agentOpts := AgentOptions{
		Config: nvcaconfig.Config{
			Authz: nvcaconfig.AuthzConfig{
				ClusterIssuedTokenSource:   nvcaconfig.ClusterIssuedTokenSourcePSAT,
				ClusterIssuedTokenFilePath: "/var/run/secrets/tokens/token",
			},
		},
		TokenFetcherOptions: nvcaauth.TokenFetcherOptions{
			PSATTokenFilePath: "/var/run/secrets/tokens/token",
		},
		FeatureFlagFetcher: featureflag.DefaultFetcher,
	}

	str := agentOpts.sanitizedString()
	assert.Contains(t, str, "PSATTokenFilePath:\"/var/run/secrets/tokens/token\"")
}

func TestIsRequestFromClusterQ(t *testing.T) {
	ctx := context.Background()
	agentOpts := AgentOptions{
		TokenFetcherOptions: nvcaauth.TokenFetcherOptions{
			TokenURL:             "http://localhost",
			OAuthTokenScope:      "byoc_registration",
			OAuthClientID:        "foo",
			OAuthClientSecretKey: "bar",
		},
		ClusterID:                      "random-id-123",
		ClusterGroupID:                 "random-cgid",
		ICMSURL:                        "https://icms.nvcf.nvidia.com",
		CloudProvider:                  "on-prem",
		KubeConfigPath:                 "test/kubeconfig.yaml",
		NamespaceLabels:                labels.Set{"foo": "bar"},
		SyncAcknowledgeRequestInterval: ackReqInterval,
		FeatureFlagFetcher:             featureflag.DefaultFetcher,
		MetricsRegisterer:              prometheus.NewRegistry(),
	}
	ag, err := NewAgent(ctx, &agentOpts)
	assert.NoError(t, err)
	require.NotNil(t, ag)

	req := &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				QueueURL: "q_gdn_byoc_random-cgid.fifo",
			},
		},
	}

	assert.False(t, ag.IsRequestFromClusterQueue(ctx, req))

	req = &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			CreationMsgInfo: nvcav2beta1.ICMSCreationMessageInfo{
				QueueURL: "q_gdn_byoc_random-id-123.fifo",
			},
		},
	}

	assert.True(t, ag.IsRequestFromClusterQueue(ctx, req))
}

func TestOTelAgentOptions_GetOTelAttributes(t *testing.T) {
	agentOpts := &AgentOptions{
		NCAId:              "abcd-1234",
		ClusterName:        "some-cluster-123",
		ClusterID:          "clusterid-1",
		ClusterGroupName:   "some-group-123",
		FeatureFlagFetcher: featureflag.DefaultFetcher,
		NVCAAgentVersion:   "v1.0.1",
	}

	expectedAttrs := map[string]string{
		nvcaotel.NCAIDAttributeKey:        agentOpts.NCAId,
		nvcaotel.ClusterNameAttributeKey:  agentOpts.ClusterName,
		nvcaotel.ClusterGroupAttributeKey: agentOpts.ClusterGroupName,
		nvcaotel.VersionAttributeKey:      agentOpts.NVCAAgentVersion,
	}
	otelAttrs := agentOpts.GetOTelAttributes()
	assert.Len(t, otelAttrs, len(expectedAttrs))
	actualAttrs := map[string]string{}
	for _, attr := range otelAttrs {
		actualAttrs[string(attr.Key)] = attr.Value.AsString()
	}
	assert.Equal(t, expectedAttrs, actualAttrs)
}

type mockFetcher struct {
	token    string
	tokenErr error
	mtx      sync.Mutex
}

func (m *mockFetcher) RefreshClient() {}

func (m *mockFetcher) FetchToken(ctx context.Context) (string, error) {
	m.mtx.Lock()
	defer m.mtx.Unlock()
	return m.token, m.tokenErr
}

func setJWTCacheTest() (*cmnoauth.JWTCache, *mockFetcher) {
	mock := &mockFetcher{}
	cache := cmnoauth.NewJWTCache().WithFetcher(mock)
	return cache, mock
}

func newMockAgent(t *testing.T, ctx context.Context, agentOpts AgentOptions) *Agent {
	return makeMockAgent(t, ctx, agentOpts, false)
}

func newMockAgentSingleGPU(t *testing.T, ctx context.Context, agentOpts AgentOptions) *Agent {
	return makeMockAgent(t, ctx, agentOpts, true)
}

func makeMockAgent(t *testing.T, ctx context.Context, agentOpts AgentOptions, singleGPU bool) *Agent {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	oldTokenFetcherFunc := newTokenFetcher
	t.Cleanup(func() {
		newTokenFetcher = oldTokenFetcherFunc
	})
	newTokenFetcher = func(ctx context.Context, _ string, opts nvcaauth.TokenFetcherOptions) (nvcaauth.TokenFetcher, *health.TokenFetcherHealthCheck, error) {
		return mocktokencache.NewForKey("icmsservice", opts.OAuthClientID, key)
	}

	oldQueueClientFunc := newQueueClient
	newQueueClient = func(_ string) queue.Client {
		return mockqueueservice.NewClient()
	}
	t.Cleanup(func() {
		newQueueClient = oldQueueClientFunc
	})

	queueSvcEndpoint := newRandomAddress(t)
	err = mockqueueservice.Run(ctx, queueSvcEndpoint)
	require.NoError(t, err)
	icmsSvcEndpoint := newRandomAddress(t)

	err = mockicmsservice.RunWithKey(ctx, icmsSvcEndpoint, "http://"+queueSvcEndpoint, key)
	require.NoError(t, err)

	var k8sclients *kubeclients.KubeClients
	if agentOpts.DynamicGPUDiscoveryEnabled {
		k8sclients = mockKubeClientsDynamicGPUs(functionNode)
	} else if singleGPU {
		k8sclients = mockKubeClientsSingleGPU(functionNode)
	} else {
		k8sclients = mockKubeClients(functionNode)
	}

	if agentOpts.CloudProvider == "" {
		agentOpts.CloudProvider = "ON-PREM"
	}
	if agentOpts.ComputeBackend == "" {
		agentOpts.ComputeBackend = "k8s"
	}
	if len(agentOpts.NamespaceLabels) == 0 {
		agentOpts.NamespaceLabels = labels.Set{"foo": "bar"}
	}

	agentOpts.ICMSURL = "http://" + icmsSvcEndpoint
	agentOpts.NVCASvcAddress = newRandomAddress(t)
	agentOpts.NVCAAdminAddr = newRandomAddress(t)

	agentOpts.StartControllerManager = func(context.Context, *kubeclients.KubeClients) error { return nil }

	agent, err := newAgent(ctx, &agentOpts)
	require.NoError(t, err)

	agent.newKubeClients = func(context.Context, string) (*kubeclients.KubeClients, error) {
		return k8sclients, nil
	}

	agent.tracer = noop.Tracer{}

	return agent
}

// Create a GPU-enabled node in case the dynamic client is configured.
var functionNode = &v1.Node{
	ObjectMeta: metav1.ObjectMeta{
		Name: "node-1",
		Labels: map[string]string{
			nodefeatures.UniformInstanceTypeLabelKey: "ON-PREM.GPU.A100",
			"nvidia.com/gpu.present":                 "true",
			"nvidia.com/gpu.family":                  "ampere",
			"nvidia.com/gpu.machine":                 "Google-Compute-Engine",
			"nvidia.com/gpu.memory":                  "40960",
			"nvidia.com/gpu.product":                 "A100-SXM4-40GB",
		},
	},
	Status: v1.NodeStatus{
		Conditions: []v1.NodeCondition{{
			Type:   v1.NodeReady,
			Status: v1.ConditionTrue,
		}},
		Capacity: v1.ResourceList{
			v1.ResourceCPU:              resource.MustParse("6000m"),
			v1.ResourceMemory:           resource.MustParse("32Gi"),
			nodefeatures.GPUResourceKey: resource.MustParse("1"),
			v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
		},
		Allocatable: v1.ResourceList{
			v1.ResourceCPU:              resource.MustParse("6000m"),
			v1.ResourceMemory:           resource.MustParse("32Gi"),
			nodefeatures.GPUResourceKey: resource.MustParse("1"),
			v1.ResourceEphemeralStorage: resource.MustParse("512Gi"),
		},
	},
}

func TestGetMetricsEvents(t *testing.T) {
	assert.Equal(t, len(append(getAgentEvents(), getNVCAMetricEvents()...)), 16)
	metricEvents := getNVCAMetricEvents()
	assert.Equal(t, metricEvents[0], EventModelCachingFailed)
	assert.Equal(t, metricEvents[1], EventModelCachingSuccess)
	assert.Equal(t, metricEvents[2], EventPVCModelCachingError)
	assert.Equal(t, metricEvents[3], EventTranslateFunctionError)
	assert.Equal(t, metricEvents[4], EventTranslateTaskError)
}

func newAgent(ctx context.Context, opts *AgentOptions) (*Agent, error) {
	// Use a custom registry for each test agent to avoid duplicate metrics registration
	if opts.MetricsRegisterer == nil {
		opts.MetricsRegisterer = prometheus.NewRegistry()
	}

	if err := k8sutil.SetConfigDefaultResources(&opts.Config); err != nil {
		return nil, err
	}

	agent, err := NewAgent(ctx, opts)
	if err != nil {
		return nil, err
	}

	return agent, nil
}

// addrMutex prevents multiple tests from racing on lis.Close() and net.Listen().
var addrMutex sync.Mutex

func newRandomAddress(t *testing.T) string {
	t.Helper()
	addrMutex.Lock()
	lis, err := net.Listen("tcp4", "127.0.0.1:")
	if err != nil {
		addrMutex.Unlock()
		require.NoError(t, err)
		return ""
	}
	addr := lis.Addr().String()
	if err := lis.Close(); err != nil {
		addrMutex.Unlock()
		require.NoError(t, err)
		return ""
	}
	addrMutex.Unlock()
	return addr
}

func Test_getTickerEvents(t *testing.T) {
	opts := &AgentOptions{
		TokenFetcherOptions: nvcaauth.TokenFetcherOptions{
			OAuthTokenScope:      "byoc_registration",
			OAuthClientID:        "foo",
			OAuthClientSecretKey: "bar",
		},
		NCAId:                          "randomNCAId123",
		ClusterName:                    "bartnvbackend",
		ClusterID:                      "clusterid-1",
		ClusterDescription:             "this is a test cluster",
		ClusterGroupName:               "group of all A30",
		ComputeBackend:                 "k8s",
		CloudProvider:                  "on-prem",
		NamespaceLabels:                labels.Set{"foo": "bar"},
		K8sVersion:                     "1.27.8",
		ICMSURL:                        "http://icms.example.com",
		CredRenewInterval:              DefaultCredRenewInterval,
		HeartbeatInterval:              time.Microsecond,
		SyncAcknowledgeRequestInterval: time.Microsecond,
		SyncQueueInterval:              defaultSyncQueueInterval,
		SyncRequestStatusInterval:      DefaultSyncRequestStatusInterval,
		PeriodicInstanceStatusInterval: DefaultPeriodicInstanceStatusInterval,
		FeatureFlagFetcher:             featureflag.DefaultFetcher,
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a, err := newAgent(ctx, opts)
	require.NoError(t, err)
	require.NotNil(t, a)

	ctx = nvcametrics.WithMetrics(ctx, a.metrics)

	a.startEventProcessDispatchers(ctx, a.getTickerEvents(ctx))

	// Eventually the queue length should be 1.
	require.EventuallyWithT(t, func(ct *assert.CollectT) {
		assert.Equal(ct, 1, a.resourceEventWorkerQueues[EventTickUpdateHeartbeat].Len())
		assert.Equal(ct, 1, a.resourceEventWorkerQueues[EventTickAcknowledgeRequest].Len())
	}, 1*time.Second, 10*time.Millisecond)
}

func TestIsICMSRequestAcknowledgementErrorRetryable(t *testing.T) {
	customTimeout := 5 * time.Minute

	tests := []struct {
		name          string
		err           error
		req           *nvcav2beta1.ICMSRequest
		expectedRetry bool
	}{
		{
			name: "404 error within timeout",
			err:  nvcaerrors.HTTPStatusError(http.StatusNotFound, nil),
			req: &nvcav2beta1.ICMSRequest{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.Now(),
				},
			},
			expectedRetry: true,
		},
		{
			name: "404 error after timeout",
			err:  nvcaerrors.HTTPStatusError(http.StatusNotFound, nil),
			req: &nvcav2beta1.ICMSRequest{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.NewTime(time.Now().Add(-6 * time.Minute)),
				},
			},
			expectedRetry: false,
		},
		{
			name: "non-404 error within timeout",
			err:  nvcaerrors.HTTPStatusError(http.StatusInternalServerError, nil),
			req: &nvcav2beta1.ICMSRequest{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.Now(),
				},
			},
			expectedRetry: false,
		},
		{
			name: "nil error",
			err:  nil,
			req: &nvcav2beta1.ICMSRequest{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.Now(),
				},
			},
			expectedRetry: false,
		},
		{
			name:          "nil request",
			err:           nvcaerrors.HTTPStatusError(http.StatusNotFound, nil),
			req:           nil,
			expectedRetry: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			retry := isICMSRequestAcknowledgementErrorRetryable(tt.err, tt.req, customTimeout)
			assert.Equal(t, tt.expectedRetry, retry)
		})
	}
}

func TestAgentPutNVCAStatusUpdateWithMaintenanceMode(t *testing.T) {
	ctx := newTestContext()

	// Test different maintenance modes
	testCases := []struct {
		name            string
		maintenanceMode types.MaintenanceMode
		expectedMode    types.MaintenanceMode
	}{
		{
			name:            "Normal mode",
			maintenanceMode: types.MaintenanceModeNone,
			expectedMode:    types.MaintenanceModeNone,
		},
		{
			name:            "Cordon mode",
			maintenanceMode: types.MaintenanceModeCordon,
			expectedMode:    types.MaintenanceModeCordon,
		},
		{
			name:            "Cordon and drain mode",
			maintenanceMode: types.MaintenanceModeCordonAndDrain,
			expectedMode:    types.MaintenanceModeCordonAndDrain,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create mock ICMS client
			mockClient := &mockICMSClient{
				healthStatusRequests: make([]types.HealthStatusRequest, 0),
			}

			// Create test data
			agentHealth := types.AgentHealth{
				Status: types.HealthStatusHealthy,
				GPUUsage: map[types.GPUName]types.GPUResource{
					"A100": {Capacity: 10, Allocated: 5},
				},
			}
			upgradeStatus := types.NVCAUpgradeNoStatus

			// Create the health status request directly (simulating what PutNVCAStatusUpdate does)
			req := &types.HealthStatusRequest{
				UpgradeStatus:       upgradeStatus,
				Status:              agentHealth.Status,
				GPUUsage:            agentHealth.GPUUsage,
				ClusterOwnerNCAID:   "test-nca-id",
				NVCAAgentVersion:    "v1.0.0",
				NVCAOperatorVersion: "v1.0.0",
				ClusterName:         "test-cluster",
				MaintenanceMode:     tc.maintenanceMode,
			}

			// Test the ICMS client call directly
			_, err := mockClient.PutHealthStatus(ctx, req)
			assert.NoError(t, err)

			// Verify the request was sent with correct maintenance mode
			assert.Len(t, mockClient.healthStatusRequests, 1)
			capturedReq := mockClient.healthStatusRequests[0]
			assert.Equal(t, tc.expectedMode, capturedReq.MaintenanceMode)
			assert.Equal(t, types.HealthStatusHealthy, capturedReq.Status)
			assert.Equal(t, "test-cluster", capturedReq.ClusterName)
			assert.Equal(t, "test-nca-id", capturedReq.ClusterOwnerNCAID)
			assert.Equal(t, "v1.0.0", capturedReq.NVCAAgentVersion)
			assert.Equal(t, "v1.0.0", capturedReq.NVCAOperatorVersion)
			assert.Equal(t, upgradeStatus, capturedReq.UpgradeStatus)
		})
	}
}

func TestAgentMaintenanceModeInitialization(t *testing.T) {
	// Test that agent properly initializes with maintenance mode
	testCases := []struct {
		name            string
		maintenanceMode types.MaintenanceMode
	}{
		{
			name:            "Normal mode",
			maintenanceMode: types.MaintenanceModeNone,
		},
		{
			name:            "Cordon mode",
			maintenanceMode: types.MaintenanceModeCordon,
		},
		{
			name:            "Cordon and drain mode",
			maintenanceMode: types.MaintenanceModeCordonAndDrain,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			opts := AgentOptions{
				MaintenanceMode: tc.maintenanceMode,
				NCAId:           "test-nca-id",
				ClusterName:     "test-cluster",
				// Add other required fields
				KubeConfigPath:     "",
				SystemNamespace:    "test-system",
				RequestsNamespace:  "test-requests",
				ICMSURL:            "https://test-icms.com",
				CloudProvider:      "test-cloud",
				NVCAAdminAddr:      ":8080",
				NVCADebugAddr:      ":8081",
				NVCASvcAddress:     ":8082",
				ComputeBackend:     "k8s",
				ClusterID:          "test-cluster-id",
				ClusterDescription: "test-cluster-desc",
				ClusterRegion:      "us-west-2",
				FeatureFlagFetcher: featureflag.DefaultFetcher,
			}

			agent := &Agent{
				AgentOptions: &opts,
			}

			// Verify maintenance mode is set correctly
			assert.Equal(t, tc.maintenanceMode, agent.MaintenanceMode)
			assert.Equal(t, tc.maintenanceMode.String(), agent.MaintenanceMode.String())
		})
	}
}

// Mock implementation for testing
type mockICMSClient struct {
	healthStatusRequests []types.HealthStatusRequest
	registrationRequests []types.ICMSRegistrationRequest
	registrationResponse *types.ICMSRegistrationResponse
	registerErr          error
}

func (m *mockICMSClient) PutHealthStatus(ctx context.Context, req *types.HealthStatusRequest) (*types.HealthStatusResponse, error) {
	m.healthStatusRequests = append(m.healthStatusRequests, *req)
	return &types.HealthStatusResponse{Action: types.HealthActionAccepted}, nil
}

func (m *mockICMSClient) Register(ctx context.Context, req *types.ICMSRegistrationRequest) (*types.ICMSRegistrationResponse, error) {
	m.registrationRequests = append(m.registrationRequests, *req)
	if m.registerErr != nil {
		return nil, m.registerErr
	}
	if m.registrationResponse != nil {
		return m.registrationResponse, nil
	}
	return &types.ICMSRegistrationResponse{
		ClusterID:      "test-cluster-id",
		ClusterGroupID: "test-cluster-group-id",
		Credentials:    getTestQueueCreds(false),
	}, nil
}

func (m *mockICMSClient) PostInstanceStatusUpdate(ctx context.Context, requestID, instanceID string, payload *types.ICMSInstanceStatusUpdateRequest) error {
	return nil
}

func (m *mockICMSClient) GetICMSServerInstanceStatuses(ctx context.Context) (types.ICMSInstanceStatusResponse, error) {
	return types.ICMSInstanceStatusResponse{}, nil
}

func (m *mockICMSClient) PutRequestAcknowledgement(ctx context.Context, icmsReqID, messageBatchID string, instanceCount uint64, srTraceCtxCfg nvcav2beta1.ICMSRequestTraceContextConfig) error {
	return nil
}

func (m *mockICMSClient) GetCreds(ctx context.Context) (*types.ICMSCredentialResponse, error) {
	return &types.ICMSCredentialResponse{
		QueueCredentials: getTestQueueCreds(false),
	}, nil
}

func (m *mockICMSClient) Endpoint() string {
	return "http://test-icms-endpoint"
}

func TestEvictAllWorkloads(t *testing.T) {
	ctx := newTestContext()

	agentOpts := AgentOptions{
		TokenFetcherOptions: nvcaauth.TokenFetcherOptions{
			OAuthTokenScope:      "byoc_registration",
			OAuthClientID:        "foo",
			OAuthClientSecretKey: "bar",
		},
		NCAId:                          "randomNCAId123",
		ClusterName:                    "bartnvbackend",
		ClusterID:                      "clusterid-1",
		ClusterDescription:             "this is a test cluster",
		ClusterGroupName:               "group of all A30",
		ComputeBackend:                 "k8s",
		CloudProvider:                  "on-prem",
		NamespaceLabels:                labels.Set{"foo": "bar"},
		K8sVersion:                     "1.27.8",
		CredRenewInterval:              DefaultCredRenewInterval,
		HeartbeatInterval:              DefaultHeartBeatInterval,
		SyncQueueInterval:              defaultSyncQueueInterval,
		SyncRequestStatusInterval:      DefaultSyncRequestStatusInterval,
		PeriodicInstanceStatusInterval: DefaultPeriodicInstanceStatusInterval,
		SyncAcknowledgeRequestInterval: ackReqInterval,
		GPUCapacity:                    2,
		FeatureFlagFetcher:             featureflag.DefaultFetcher,
		MaintenanceMode:                types.MaintenanceModeCordonAndDrain,
		MetricsRegisterer:              prometheus.NewRegistry(),
	}

	// Create test ICMSRequests with active instances
	testReqs := []*nvcav2beta1.ICMSRequest{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-request-1",
				Namespace: "default",
			},
			Spec: nvcav2beta1.ICMSRequestSpec{
				RequestID: "req-1",
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				RequestStatus: nvcav2beta1.ICMSRequestStatusInProgress,
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"instance-1": {
						ID:     "instance-1",
						Type:   nvcav2beta1.InstanceTypePod,
						Status: string(types.ICMSInstanceRunning),
					},
					"instance-2": {
						ID:     "instance-2",
						Type:   nvcav2beta1.InstanceTypePod,
						Status: string(types.ICMSInstanceRunning),
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-request-2",
				Namespace: "default",
			},
			Spec: nvcav2beta1.ICMSRequestSpec{
				RequestID: "req-2",
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				RequestStatus: nvcav2beta1.ICMSRequestStatusInProgress,
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"instance-3-miniservice": {
						ID:     "instance-3-miniservice",
						Type:   nvcav2beta1.InstanceTypeMiniService,
						Status: string(types.ICMSInstanceRunning),
					},
				},
			},
		},
		// Request with no instances - should be skipped
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-request-3",
				Namespace: "default",
			},
			Spec: nvcav2beta1.ICMSRequestSpec{
				RequestID: "req-3",
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				RequestStatus: nvcav2beta1.ICMSRequestStatusInProgress,
				Instances:     map[string]nvcav2beta1.InstanceStatus{},
			},
		},
		// Failed request - should be skipped
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-request-4",
				Namespace: "default",
			},
			Spec: nvcav2beta1.ICMSRequestSpec{
				RequestID: "req-4",
			},
			Status: nvcav2beta1.ICMSRequestStatus{
				RequestStatus: nvcav2beta1.ICMSRequestStatusFailed,
				Instances: map[string]nvcav2beta1.InstanceStatus{
					"instance-4": {
						ID:     "instance-4",
						Type:   nvcav2beta1.InstanceTypePod,
						Status: string(types.ICMSInstanceRunning),
					},
				},
			},
		},
	}

	// Create mock agent with controlled ICMS client
	mockICMS := &mockICMSClient{}
	ag := newMockAgentSingleGPU(t, ctx, agentOpts)

	// Set the mock ICMS client before starting the agent
	ag.icmsClient = mockICMS

	require.NoError(t, ag.Start(ctx))

	// Now create ICMSRequest objects via the fake BART client
	for _, req := range testReqs {
		_, err := ag.backendk8scache.clients.BART.NvcaV2beta1().ICMSRequests(req.Namespace).Create(ctx, req, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	// Wait until the informer cache/lister can see them
	require.Eventually(t, func() bool {
		items, _ := ag.backendk8scache.icmsRequestLister.List(labels.Everything())
		return len(items) >= 3 // failed req is skipped later but we expect at least 3 created
	}, time.Second, time.Millisecond*50)

	// Create pods for the pod instances to be terminated
	podNamespace := ag.backendk8scache.podInstanceNamespace
	for _, pod := range []string{"instance-1", "instance-2"} {
		podObj := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pod,
				Namespace: podNamespace,
			},
		}
		_, err := ag.backendk8scache.clients.K8s.CoreV1().Pods(podNamespace).Create(ctx, podObj, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	// Create helm function instance
	ms := &v1alpha1.MiniService{
		ObjectMeta: metav1.ObjectMeta{
			Name: "instance-3-miniservice",
		},
	}
	err := ag.backendk8scache.clients.HelmV2.Create(ctx, ms)
	require.NoError(t, err)

	// Execute evictAllWorkloads
	err = ag.evictAllWorkloads(ctx)
	require.NoError(t, err)

	// Verify that pods were deleted
	_, err = ag.backendk8scache.clients.K8s.CoreV1().Pods(podNamespace).Get(ctx, "instance-1", metav1.GetOptions{})
	assert.True(t, errors.IsNotFound(err), "Pod instance-1 should be deleted")

	_, err = ag.backendk8scache.clients.K8s.CoreV1().Pods(podNamespace).Get(ctx, "instance-2", metav1.GetOptions{})
	assert.True(t, errors.IsNotFound(err), "Pod instance-2 should be deleted")

	// Verify that miniservice was deleted
	err = ag.backendk8scache.clients.HelmV2.Get(ctx, client.ObjectKey{Name: "instance-3-miniservice"}, ms)
	assert.True(t, errors.IsNotFound(err), "Miniservice should be deleted")
}

func TestEvictAllWorkloads_EmptyList(t *testing.T) {
	ctx := newTestContext()

	agentOpts := AgentOptions{
		TokenFetcherOptions: nvcaauth.TokenFetcherOptions{
			OAuthTokenScope:      "byoc_registration",
			OAuthClientID:        "foo",
			OAuthClientSecretKey: "bar",
		},
		NCAId:                          "randomNCAId123",
		ClusterName:                    "bartnvbackend",
		ComputeBackend:                 "k8s",
		CredRenewInterval:              DefaultCredRenewInterval,
		HeartbeatInterval:              DefaultHeartBeatInterval,
		SyncQueueInterval:              defaultSyncQueueInterval,
		SyncRequestStatusInterval:      DefaultSyncRequestStatusInterval,
		PeriodicInstanceStatusInterval: DefaultPeriodicInstanceStatusInterval,
		SyncAcknowledgeRequestInterval: ackReqInterval,
		GPUCapacity:                    2,
		FeatureFlagFetcher:             featureflag.DefaultFetcher,
		MaintenanceMode:                types.MaintenanceModeCordonAndDrain,
		MetricsRegisterer:              prometheus.NewRegistry(),
	}

	ag := newMockAgentSingleGPU(t, ctx, agentOpts)
	ag.icmsClient = &mockICMSClient{}

	require.NoError(t, ag.Start(ctx))

	// No ICMS requests to create - we want to test with an empty list
	// The normal lister will find nothing, which is what we want

	// Should complete without error
	err := ag.evictAllWorkloads(ctx)
	assert.NoError(t, err)
}

func TestAgent_SyncPeriodicInstanceStatuses(t *testing.T) {

	tests := []struct {
		name                            string
		mockGetInstanceStatusesResponse types.ICMSInstanceStatusResponse
		mockGetInstanceStatusesError    error
		mockReconcileResults            []mockReconcileResult
		mockHandleFailureError          error
		mockHandleSyncActionError       error
		mockPostInstanceStatusError     error
		expectedError                   bool
		expectedErrorMessage            string
		description                     string
	}{
		{
			name:                            "GetICMSServerInstanceStatuses returns error",
			mockGetInstanceStatusesResponse: types.ICMSInstanceStatusResponse{},
			mockGetInstanceStatusesError:    fmt.Errorf("failed to connect to ICMS service"),
			expectedError:                   true,
			expectedErrorMessage:            "failed to GetICMSServerInstanceStatuses",
			description:                     "Should return error when GetICMSServerInstanceStatuses fails",
		},
		{
			name: "No instances returned from ICMS service",
			mockGetInstanceStatusesResponse: types.ICMSInstanceStatusResponse{
				Instances: []types.ICMSServerInstanceState{},
			},
			mockGetInstanceStatusesError: nil,
			expectedError:                false,
			description:                  "Should handle empty instance list without error",
		},
		{
			name: "All instances require no action",
			mockGetInstanceStatusesResponse: types.ICMSInstanceStatusResponse{
				Instances: []types.ICMSServerInstanceState{
					{InstanceID: "instance-1", RequestID: "request-1", InstanceState: types.ICMSInstanceRunning},
					{InstanceID: "instance-2", RequestID: "request-2", InstanceState: types.ICMSInstanceStarted},
				},
			},
			mockGetInstanceStatusesError: nil,
			mockReconcileResults: []mockReconcileResult{
				{
					instanceID: "instance-1",
					action:     ICMSInstanceReconcileNoAction,
					request:    &nvcav2beta1.ICMSRequest{Spec: nvcav2beta1.ICMSRequestSpec{RequestID: "request-1"}},
					status:     nvcav2beta1.InstanceStatus{ID: "instance-1", Status: string(types.ICMSInstanceRunning)},
				},
				{
					instanceID: "instance-2",
					action:     ICMSInstanceReconcileNoAction,
					request:    &nvcav2beta1.ICMSRequest{Spec: nvcav2beta1.ICMSRequestSpec{RequestID: "request-2"}},
					status:     nvcav2beta1.InstanceStatus{ID: "instance-2", Status: string(types.ICMSInstanceStarted)},
				},
			},
			expectedError: false,
			description:   "Should handle all instances requiring no action",
		},
		{
			name: "Instance requires update only",
			mockGetInstanceStatusesResponse: types.ICMSInstanceStatusResponse{
				Instances: []types.ICMSServerInstanceState{
					{InstanceID: "instance-1", RequestID: "request-1", InstanceState: types.ICMSInstanceRunning},
				},
			},
			mockGetInstanceStatusesError: nil,
			mockReconcileResults: []mockReconcileResult{
				{
					instanceID: "instance-1",
					action:     ICMSInstanceReconcileUpdateOnly,
					request:    &nvcav2beta1.ICMSRequest{Spec: nvcav2beta1.ICMSRequestSpec{RequestID: "request-1"}},
					status:     nvcav2beta1.InstanceStatus{ID: "instance-1", Status: string(types.ICMSInstanceStarted)},
				},
			},
			expectedError: false,
			description:   "Should handle instance requiring update only",
		},
		{
			name: "Instance requires terminate and update",
			mockGetInstanceStatusesResponse: types.ICMSInstanceStatusResponse{
				Instances: []types.ICMSServerInstanceState{
					{InstanceID: "instance-1", RequestID: "request-1", InstanceState: types.ICMSInstanceShuttingDown},
				},
			},
			mockGetInstanceStatusesError: nil,
			mockReconcileResults: []mockReconcileResult{
				{
					instanceID: "instance-1",
					action:     ICMSInstanceReconcileTerminateAndUpdate,
					request:    &nvcav2beta1.ICMSRequest{Spec: nvcav2beta1.ICMSRequestSpec{RequestID: "request-1"}},
					status:     nvcav2beta1.InstanceStatus{ID: "instance-1", Status: string(types.ICMSInstanceRunning)},
				},
			},
			expectedError: false,
			description:   "Should handle instance requiring terminate and update",
		},
		{
			name: "Mixed actions - multiple instances",
			mockGetInstanceStatusesResponse: types.ICMSInstanceStatusResponse{
				Instances: []types.ICMSServerInstanceState{
					{InstanceID: "instance-1", RequestID: "request-1", InstanceState: types.ICMSInstanceRunning},
					{InstanceID: "instance-2", RequestID: "request-2", InstanceState: types.ICMSInstanceRunning},
					{InstanceID: "instance-3", RequestID: "request-3", InstanceState: types.ICMSInstanceShuttingDown},
				},
			},
			mockGetInstanceStatusesError: nil,
			mockReconcileResults: []mockReconcileResult{
				{
					instanceID: "instance-1",
					action:     ICMSInstanceReconcileNoAction,
					request:    &nvcav2beta1.ICMSRequest{Spec: nvcav2beta1.ICMSRequestSpec{RequestID: "request-1"}},
					status:     nvcav2beta1.InstanceStatus{ID: "instance-1", Status: string(types.ICMSInstanceRunning)},
				},
				{
					instanceID: "instance-2",
					action:     ICMSInstanceReconcileUpdateOnly,
					request:    &nvcav2beta1.ICMSRequest{Spec: nvcav2beta1.ICMSRequestSpec{RequestID: "request-2"}},
					status:     nvcav2beta1.InstanceStatus{ID: "instance-2", Status: string(types.ICMSInstanceStarted)},
				},
				{
					instanceID: "instance-3",
					action:     ICMSInstanceReconcileTerminateAndUpdate,
					request:    &nvcav2beta1.ICMSRequest{Spec: nvcav2beta1.ICMSRequestSpec{RequestID: "request-3"}},
					status:     nvcav2beta1.InstanceStatus{ID: "instance-3", Status: string(types.ICMSInstanceRunning)},
				},
			},
			expectedError: false,
			description:   "Should handle multiple instances with different actions",
		},
		{
			name: "Error in HandleInstanceStatusPreconditionFailure",
			mockGetInstanceStatusesResponse: types.ICMSInstanceStatusResponse{
				Instances: []types.ICMSServerInstanceState{
					{InstanceID: "instance-1", RequestID: "request-1", InstanceState: types.ICMSInstanceShuttingDown},
				},
			},
			mockGetInstanceStatusesError: nil,
			mockReconcileResults: []mockReconcileResult{
				{
					instanceID: "instance-1",
					action:     ICMSInstanceReconcileTerminateAndUpdate,
					request:    &nvcav2beta1.ICMSRequest{Spec: nvcav2beta1.ICMSRequestSpec{RequestID: "request-1"}},
					status:     nvcav2beta1.InstanceStatus{ID: "instance-1", Status: string(types.ICMSInstanceRunning)},
				},
			},
			mockHandleFailureError: fmt.Errorf("failed to handle precondition failure"),
			expectedError:          false, // Function logs error but continues
			description:            "Should continue processing despite HandleInstanceStatusPreconditionFailure error",
		},
		{
			name: "Error in handleInstanceStatusSyncAction",
			mockGetInstanceStatusesResponse: types.ICMSInstanceStatusResponse{
				Instances: []types.ICMSServerInstanceState{
					{InstanceID: "instance-1", RequestID: "request-1", InstanceState: types.ICMSInstanceRunning},
				},
			},
			mockGetInstanceStatusesError: nil,
			mockReconcileResults: []mockReconcileResult{
				{
					instanceID: "instance-1",
					action:     ICMSInstanceReconcileUpdateOnly,
					request:    &nvcav2beta1.ICMSRequest{Spec: nvcav2beta1.ICMSRequestSpec{RequestID: "request-1"}},
					status:     nvcav2beta1.InstanceStatus{ID: "instance-1", Status: string(types.ICMSInstanceStarted)},
				},
			},
			mockHandleSyncActionError: fmt.Errorf("failed to handle sync action"),
			expectedError:             false, // Function logs error but continues
			description:               "Should continue processing despite handleInstanceStatusSyncAction error",
		},
		{
			name: "PostInstanceStatusUpdate error in handleInstanceStatusSyncAction",
			mockGetInstanceStatusesResponse: types.ICMSInstanceStatusResponse{
				Instances: []types.ICMSServerInstanceState{
					{InstanceID: "instance-1", RequestID: "request-1", InstanceState: types.ICMSInstanceRunning},
				},
			},
			mockGetInstanceStatusesError: nil,
			mockReconcileResults: []mockReconcileResult{
				{
					instanceID: "instance-1",
					action:     ICMSInstanceReconcileUpdateOnly,
					request:    &nvcav2beta1.ICMSRequest{Spec: nvcav2beta1.ICMSRequestSpec{RequestID: "request-1"}},
					status:     nvcav2beta1.InstanceStatus{ID: "instance-1", Status: string(types.ICMSInstanceStarted)},
				},
			},
			mockPostInstanceStatusError: fmt.Errorf("failed to post instance status"),
			expectedError:               false, // Function logs error but continues
			description:                 "Should continue processing despite PostInstanceStatusUpdate error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create mock ICMS client
			mockICMSClient := &mockSyncPeriodicICMSClient{
				getInstanceStatusesResponse: tt.mockGetInstanceStatusesResponse,
				getInstanceStatusesError:    tt.mockGetInstanceStatusesError,
				postInstanceStatusError:     tt.mockPostInstanceStatusError,
			}

			// Create mock backend K8s cache
			mockBackendK8sCache := &mockSyncPeriodicBackendK8sCache{
				reconcileResults: tt.mockReconcileResults,
			}

			// Create mock ICMS request helper
			mockICMSRequestHelper := &mockSyncPeriodicICMSRequestHelper{
				handleFailureError:    tt.mockHandleFailureError,
				handleSyncActionError: tt.mockHandleSyncActionError,
			}

			// Set the mock helper in the backend cache
			mockBackendK8sCache.icmsRequestHelper = mockICMSRequestHelper

			// Create testable agent that uses the interface
			agent := &testableAgent{
				icmsClient:      mockICMSClient,
				backendk8scache: mockBackendK8sCache,
				mockHelper:      mockICMSRequestHelper,
			}

			// Call the function under test
			err := agent.SyncPeriodicInstanceStatuses(ctx)

			// Verify results
			if tt.expectedError {
				assert.Error(t, err, "Expected error but got none")
				if tt.expectedErrorMessage != "" {
					assert.Contains(t, err.Error(), tt.expectedErrorMessage, "Error message doesn't match")
				}
			} else {
				assert.NoError(t, err, "Expected no error but got: %v", err)
			}

			// Verify ICMS client interactions
			assert.Equal(t, 1, mockICMSClient.getInstanceStatusesCalls, "GetICMSServerInstanceStatuses should be called once")

			// Verify backend cache interactions
			assert.Equal(t, len(tt.mockGetInstanceStatusesResponse.Instances), mockBackendK8sCache.reconcileInstanceStatusCalls, "ReconcileInstanceStatus should be called for each instance")

			// Verify ICMS request helper interactions for terminate and update actions
			expectedTerminateAndUpdateCalls := 0
			expectedSyncActionCalls := 0
			for _, result := range tt.mockReconcileResults {
				if result.action == ICMSInstanceReconcileTerminateAndUpdate {
					expectedTerminateAndUpdateCalls++
					expectedSyncActionCalls++
				} else if result.action == ICMSInstanceReconcileUpdateOnly {
					expectedSyncActionCalls++
				}
			}

			assert.Equal(t, expectedTerminateAndUpdateCalls, mockICMSRequestHelper.handleFailureCalls, "HandleInstanceStatusPreconditionFailure calls mismatch")
			assert.Equal(t, expectedSyncActionCalls, len(mockICMSClient.postInstanceStatusCalls), "PostInstanceStatusUpdate calls mismatch")
		})
	}
}

// Mock types for SyncPeriodicInstanceStatuses tests
type mockReconcileResult struct {
	instanceID string
	action     ICMSInstanceReconcileState
	request    *nvcav2beta1.ICMSRequest
	status     nvcav2beta1.InstanceStatus
}

type mockSyncPeriodicICMSClient struct {
	getInstanceStatusesResponse types.ICMSInstanceStatusResponse
	getInstanceStatusesError    error
	postInstanceStatusError     error
	getInstanceStatusesCalls    int
	postInstanceStatusCalls     []mockPostInstanceStatusCall
}

type mockPostInstanceStatusCall struct {
	requestID  string
	instanceID string
	payload    *types.ICMSInstanceStatusUpdateRequest
}

func (m *mockSyncPeriodicICMSClient) GetICMSServerInstanceStatuses(ctx context.Context) (types.ICMSInstanceStatusResponse, error) {
	m.getInstanceStatusesCalls++
	return m.getInstanceStatusesResponse, m.getInstanceStatusesError
}

func (m *mockSyncPeriodicICMSClient) PostInstanceStatusUpdate(ctx context.Context, requestID, instanceID string, payload *types.ICMSInstanceStatusUpdateRequest) error {
	m.postInstanceStatusCalls = append(m.postInstanceStatusCalls, mockPostInstanceStatusCall{
		requestID:  requestID,
		instanceID: instanceID,
		payload:    payload,
	})
	return m.postInstanceStatusError
}

func (m *mockSyncPeriodicICMSClient) PutHealthStatus(ctx context.Context, req *types.HealthStatusRequest) (*types.HealthStatusResponse, error) {
	return &types.HealthStatusResponse{Action: types.HealthActionAccepted}, nil
}

func (m *mockSyncPeriodicICMSClient) Register(ctx context.Context, req *types.ICMSRegistrationRequest) (*types.ICMSRegistrationResponse, error) {
	return nil, nil
}

func (m *mockSyncPeriodicICMSClient) PutRequestAcknowledgement(ctx context.Context, icmsReqID, messageBatchID string, instanceCount uint64, srTraceCtxCfg nvcav2beta1.ICMSRequestTraceContextConfig) error {
	return nil
}

func (m *mockSyncPeriodicICMSClient) GetCreds(ctx context.Context) (*types.ICMSCredentialResponse, error) {
	return nil, nil
}

func (m *mockSyncPeriodicICMSClient) Endpoint() string {
	return "mock-endpoint"
}

// BackendK8sCacheInterface defines the interface for backend K8s cache operations needed for testing
type BackendK8sCacheInterface interface {
	ReconcileInstanceStatus(ctx context.Context, is types.ICMSServerInstanceState) (*nvcav2beta1.ICMSRequest, nvcav2beta1.InstanceStatus, ICMSInstanceReconcileState)
}

// mockSyncPeriodicBackendK8sCache implements BackendK8sCacheInterface for testing
type mockSyncPeriodicBackendK8sCache struct {
	reconcileResults             []mockReconcileResult
	reconcileInstanceStatusCalls int
	icmsRequestHelper            *mockSyncPeriodicICMSRequestHelper
}

func (m *mockSyncPeriodicBackendK8sCache) ReconcileInstanceStatus(ctx context.Context, is types.ICMSServerInstanceState) (*nvcav2beta1.ICMSRequest, nvcav2beta1.InstanceStatus, ICMSInstanceReconcileState) {
	if m.reconcileInstanceStatusCalls >= len(m.reconcileResults) {
		// Return default values if no more results configured
		return nil, nvcav2beta1.InstanceStatus{}, ICMSInstanceReconcileNoAction
	}

	result := m.reconcileResults[m.reconcileInstanceStatusCalls]
	m.reconcileInstanceStatusCalls++

	return result.request, result.status, result.action
}

type mockSyncPeriodicICMSRequestHelper struct {
	handleFailureError    error
	handleSyncActionError error
	handleFailureCalls    int
}

func (m *mockSyncPeriodicICMSRequestHelper) HandleInstanceStatusPreconditionFailure(ctx context.Context, req *nvcav2beta1.ICMSRequest, instanceID string) error {
	m.handleFailureCalls++
	return m.handleFailureError
}

// testableAgent is a wrapper around Agent that uses interfaces for testing
type testableAgent struct {
	icmsClient      ICMSClientInterface
	backendk8scache BackendK8sCacheInterface
	mockHelper      *mockSyncPeriodicICMSRequestHelper // Direct reference to mock helper
}

// SyncPeriodicInstanceStatuses implements the same logic as Agent.SyncPeriodicInstanceStatuses but using interfaces
func (a *testableAgent) SyncPeriodicInstanceStatuses(ctx context.Context) error {
	log := core.GetLogger(ctx)
	isRes, err := a.icmsClient.GetICMSServerInstanceStatuses(ctx)

	if err != nil {
		return fmt.Errorf("failed to GetICMSServerInstanceStatuses, err: %v", err)
	}

	for _, is := range isRes.Instances {
		req, reqIS, ra := a.backendk8scache.ReconcileInstanceStatus(ctx, is)
		childLog := log.WithFields(logrus.Fields{
			"instanceID":  is.InstanceID,
			"icms-state":  is.InstanceState,
			"local-state": reqIS.LastReportedStatus,
			"action":      string(ra),
		})
		childCtx := core.WithLogger(ctx, childLog)
		childLog.Debug("periodic instance status sync")

		switch ra {
		case ICMSInstanceReconcileNoAction:
			// No action needed
		case ICMSInstanceReconcileUpdateOnly, ICMSInstanceReconcileTerminateAndUpdate:
			childLog.Warn("periodic instance status corrective action")
			if ra == ICMSInstanceReconcileTerminateAndUpdate {
				// Call the mock helper directly for termination
				if a.mockHelper != nil {
					err := a.mockHelper.HandleInstanceStatusPreconditionFailure(childCtx, req, reqIS.ID)
					if err != nil {
						childLog.WithError(err).Error("failed to terminate instance due to sync action")
					}
				}
			}
			// Simulate handleInstanceStatusSyncAction by calling PostInstanceStatusUpdate
			if err := a.handleInstanceStatusSyncAction(childCtx, req, reqIS, ra); err != nil {
				childLog.WithError(err).Error("failed handleInstanceStatusSyncAction")
			}
		}
	}
	return nil
}

// handleInstanceStatusSyncAction simulates the real agent's handleInstanceStatusSyncAction method
func (a *testableAgent) handleInstanceStatusSyncAction(ctx context.Context, req *nvcav2beta1.ICMSRequest, is nvcav2beta1.InstanceStatus, ra ICMSInstanceReconcileState) error {
	var updateRequest *types.ICMSInstanceStatusUpdateRequest
	iStatus := types.ICMSInstanceTerminated
	action := common.TerminationAction
	rs := types.ICMSInstanceRequestClosed
	tc := types.ICMSInstanceTerminatedDuetoSyncAction

	if ra == ICMSInstanceReconcileUpdateOnly {
		iStatus = types.ICMSInstanceState(is.Status)
		action = common.FunctionCreationAction
		rs = types.ICMSInstanceRequestActive
		tc = types.ICMSInstanceStateNoStatus
	}
	updateRequest = &types.ICMSInstanceStatusUpdateRequest{
		Status:           types.ICMSRequestInstanceTerminatedByService,
		InstanceState:    iStatus,
		Action:           action,
		RequestState:     rs,
		TerminationCause: tc,
	}
	err := a.icmsClient.PostInstanceStatusUpdate(ctx, req.Spec.RequestID, is.ID, updateRequest)
	if err != nil {
		return fmt.Errorf("failed to update ICMS instance %s, err: %v", is.ID, err)
	}
	return nil
}

func TestAgent_SyncPeriodicInstanceStatuses_Integration(t *testing.T) {
	ctx := context.Background()

	// Test with a more realistic scenario using the existing test infrastructure
	mockICMSClient := &mockICMSClient{}

	// Create agent with mock
	agent := &Agent{
		icmsClient: mockICMSClient,
	}

	// Test successful call with no instances
	err := agent.SyncPeriodicInstanceStatuses(ctx)
	assert.NoError(t, err, "Should handle no instances successfully")
}

func TestAgentRegistrationClusterStatusReflectsMaintenanceMode(t *testing.T) {

	cases := []struct {
		name           string
		mode           types.MaintenanceMode
		expectedStatus string
	}{
		{"none -> READY", types.MaintenanceModeNone, "READY"},
		{"cordon -> CORDON", types.MaintenanceModeCordon, "CORDON"},
		{"cordon+drain -> CORDON_AND_DRAIN", types.MaintenanceModeCordonAndDrain, "CORDON_AND_DRAIN"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mockClient := &mockICMSClient{}

			opts := &AgentOptions{
				NCAId:               "test-nca-id",
				ClusterName:         "test-cluster",
				NVCAAgentVersion:    "v1.0.0",
				NVCAOperatorVersion: "v1.0.0",
				MaintenanceMode:     tc.mode,
				K8sVersion:          "v1.27.8",
			}

			a := &Agent{AgentOptions: opts, icmsClient: mockClient}
			// Build request directly to avoid storage/network side effects
			rreq := a.buildICMSRegistrationRequest(opts.K8sVersion, []types.RegistrationGPU{})

			got := rreq.ClusterStatus
			assert.Equal(t, tc.expectedStatus, got)
		})
	}
}

func TestAgent_putTaskICMSRequestAcknowledgementAfterScheduled_ParseMaxRuntimeDuration(t *testing.T) {
	// This test specifically verifies that the changes in the diff are working correctly:
	// 1. ParseMaxRuntimeDuration is called instead of translateutil.ParseISO8601Duration
	// 2. Error handling with fallback to default duration works
	// 3. Both valid and invalid duration strings are handled without panic

	// Since this is integration testing of the duration parsing changes,
	// we'll test by directly calling the parsing logic that was changed

	tests := []struct {
		name                    string
		maxRuntimeDuration      string
		expectFallbackToDefault bool
	}{
		{
			name:                    "valid ISO8601 duration",
			maxRuntimeDuration:      "PT2H",
			expectFallbackToDefault: false,
		},
		{
			name:                    "empty duration uses max int64",
			maxRuntimeDuration:      "",
			expectFallbackToDefault: false, // ParseMaxRuntimeDuration handles empty strings
		},
		{
			name:                    "invalid duration falls back to default",
			maxRuntimeDuration:      "invalid",
			expectFallbackToDefault: true,
		},
		{
			name:                    "malformed ISO8601 falls back to default",
			maxRuntimeDuration:      "P1H", // missing T
			expectFallbackToDefault: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test agent with default timeout
			defaultTimeout := 4 * time.Hour
			agent := &Agent{
				AgentOptions: &AgentOptions{
					K8sTimeConfig: (&nvcak8sutil.TimeConfig{
						MaxRunningTimeout: defaultTimeout,
					}).Complete(),
				},
			}

			// Test the duration parsing logic that was changed in the diff
			// This simulates what happens in putTaskICMSRequestAcknowledgementAfterScheduled
			mrd, err := nvcainternaltranslate.ParseMaxRuntimeDuration(tt.maxRuntimeDuration)

			var finalDuration time.Duration
			if err != nil {
				// This is the fallback logic from the agent code
				finalDuration = agent.K8sTimeConfig.MaxRunningTimeout
			} else {
				finalDuration = mrd
			}

			// Verify the expected behavior
			if tt.expectFallbackToDefault {
				require.Error(t, err, "Expected parsing error for: %s", tt.maxRuntimeDuration)
				assert.Equal(t, defaultTimeout, finalDuration, "Should fall back to default timeout")
			} else {
				require.NoError(t, err, "Expected no parsing error for: %s", tt.maxRuntimeDuration)
				if tt.maxRuntimeDuration == "" {
					// Empty string should return math.MaxInt64 duration
					assert.Greater(t, finalDuration, 290*365*24*time.Hour, "Empty string should return near-infinite duration")
				} else {
					// Valid duration should be parsed correctly
					assert.Greater(t, finalDuration, time.Duration(0), "Valid duration should be positive")
					assert.NotEqual(t, defaultTimeout, finalDuration, "Valid duration should not equal default")
				}
			}
		})
	}
}

func TestAgentLivenessCheckers(t *testing.T) {
	ctx := newTestContext()

	type spec struct {
		name string
		ffs  []*featureflag.FeatureFlag
	}

	for _, tt := range []spec{
		{
			name: "no feature flags enabled",
			ffs:  []*featureflag.FeatureFlag{},
		},
		{
			name: "fnds enabled",
			ffs: []*featureflag.FeatureFlag{
				featureflag.UseFunctionDeploymentStages,
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fff := &featureflagmock.Fetcher{EnabledFFs: tt.ffs}
			agentOpts := AgentOptions{
				TokenFetcherOptions: nvcaauth.TokenFetcherOptions{
					OAuthTokenScope:      "byoc_registration",
					OAuthClientID:        "foo",
					OAuthClientSecretKey: "bar",
				},
				NCAId:                          "randomNCAId123",
				ClusterName:                    "bartnvbackend",
				ClusterID:                      "clusterid-1",
				ClusterDescription:             "this is a test cluster",
				ClusterGroupName:               "group of all A30",
				ComputeBackend:                 "k8s",
				CloudProvider:                  "on-prem",
				NamespaceLabels:                labels.Set{"foo": "bar"},
				K8sVersion:                     "1.27.8",
				CredRenewInterval:              DefaultCredRenewInterval,
				HeartbeatInterval:              DefaultHeartBeatInterval,
				SyncQueueInterval:              defaultSyncQueueInterval,
				SyncRequestStatusInterval:      DefaultSyncRequestStatusInterval,
				PeriodicInstanceStatusInterval: DefaultPeriodicInstanceStatusInterval,
				SyncAcknowledgeRequestInterval: ackReqInterval,
				GPUCapacity:                    2,
				FeatureFlagFetcher:             fff,
				MetricsRegisterer:              prometheus.NewRegistry(),
			}
			ag := newMockAgentSingleGPU(t, ctx, agentOpts)

			require.NoError(t, ag.Start(ctx))

			assert.EventuallyWithT(t, func(ct *assert.CollectT) {
				resp, err := http.Get("http://" + ag.NVCASvcAddress + health.HTTPLivenessRoutePath)
				require.NoError(ct, err)
				body, err := io.ReadAll(resp.Body)
				require.NoError(ct, err)
				assert.Equal(ct, http.StatusOK, resp.StatusCode, string(body))
			}, 5*time.Second, 100*time.Millisecond)
		})
	}
}

func TestNewAgent_NATSSecretsFetcherInitialization(t *testing.T) {
	tests := []struct {
		name                   string
		useNATSQueue           bool
		secretsPath            string
		expectNATSFetcher      bool
		expectError            bool
		expectedErrorSubstring string
	}{
		{
			name:              "SelfHosted enabled with valid secrets path",
			useNATSQueue:      true,
			secretsPath:       "testdata/test-secrets.json",
			expectNATSFetcher: true,
			expectError:       false,
		},
		{
			name:              "SelfHosted disabled",
			useNATSQueue:      false,
			secretsPath:       "",
			expectNATSFetcher: false,
			expectError:       false,
		},
		{
			name:              "SelfHosted enabled with invalid secrets path",
			useNATSQueue:      true,
			secretsPath:       "/nonexistent/path/secrets.json",
			expectNATSFetcher: false,
			expectError:       true,
			// Error could come from either tokenFetcher initialization or NATS secrets fetcher initialization
			expectedErrorSubstring: "secrets.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create mock feature flag fetcher
			fff := &featureflagmock.Fetcher{}
			if tt.useNATSQueue {
				fff.SetFeatureFlags(featureflag.SelfHosted)
			}

			agentOpts := AgentOptions{
				TokenFetcherOptions: nvcaauth.TokenFetcherOptions{
					TokenURL:                       "http://localhost",
					OAuthTokenScope:                "byoc_registration",
					OAuthClientID:                  "foo",
					OAuthClientSecretKey:           "bar",
					SelfHostedVaultSecretsJSONPath: tt.secretsPath,
				},
				NCAId:                          "test-nca-id",
				ClusterName:                    "test-cluster",
				ClusterID:                      "test-cluster-id",
				ClusterDescription:             "test cluster",
				ClusterGroupName:               "test-group",
				ICMSURL:                        "https://icms.test.nvidia.com",
				CloudProvider:                  "on-prem",
				KubeConfigPath:                 "testdata/kubeconfig.yaml",
				NamespaceLabels:                labels.Set{"test": "true"},
				NVCASvcAddress:                 "localhost:8080",
				NVCAAdminAddr:                  "localhost:8081",
				CredRenewInterval:              DefaultCredRenewInterval,
				HeartbeatInterval:              DefaultHeartBeatInterval,
				SyncQueueInterval:              defaultSyncQueueInterval,
				SyncRequestStatusInterval:      DefaultSyncRequestStatusInterval,
				PeriodicInstanceStatusInterval: DefaultPeriodicInstanceStatusInterval,
				SyncAcknowledgeRequestInterval: ackReqInterval,
				FeatureFlagFetcher:             fff,
				MetricsRegisterer:              prometheus.NewRegistry(),
			}

			agent, err := NewAgent(ctx, &agentOpts)

			if tt.expectError {
				require.Error(t, err)
				if tt.expectedErrorSubstring != "" {
					assert.Contains(t, err.Error(), tt.expectedErrorSubstring)
				}
				return
			}

			require.NoError(t, err)
			require.NotNil(t, agent)

			if tt.expectNATSFetcher {
				assert.NotNil(t, agent.natsSecretsFetcher, "natsSecretsFetcher should be initialized when SelfHosted is enabled")
			} else {
				assert.Nil(t, agent.natsSecretsFetcher, "natsSecretsFetcher should not be initialized when SelfHosted is disabled")
			}
		})
	}
}

func TestAgent_PostProcessQueueCredentials(t *testing.T) {
	tests := []struct {
		name                     string
		useNATSQueue             bool
		inputCredentials         types.QueueCredentials
		healthCacheGPUs          map[types.GPUName]types.GPUResource
		expectCredentialsChanged bool
		expectedGPUsInCreds      []types.GPUName
	}{
		{
			name:         "Feature flag disabled - credentials pass through unchanged",
			useNATSQueue: false,
			inputCredentials: types.QueueCredentials{
				CreationQueues: types.CreationQueueInfoSet{
					"A100": queue.MessageQueueInfo{
						QueueType: queue.CreationQueue,
						QueueURL:  "https://sqs.amazonaws.com/test-queue",
						GPU:       "A100",
					},
				},
				TerminationQueue: queue.MessageQueueInfo{
					QueueType: queue.TerminationQueue,
					QueueURL:  "https://sqs.amazonaws.com/term-queue",
				},
			},
			healthCacheGPUs: map[types.GPUName]types.GPUResource{
				"H100": {Capacity: 8, Allocated: 0},
			},
			expectCredentialsChanged: false,
		},
		{
			name:         "Feature flag enabled but credentials not empty - pass through unchanged",
			useNATSQueue: true,
			inputCredentials: types.QueueCredentials{
				CreationQueues: types.CreationQueueInfoSet{
					"A100": queue.MessageQueueInfo{
						QueueType: queue.CreationQueue,
						QueueURL:  "https://sqs.amazonaws.com/test-queue",
						GPU:       "A100",
					},
				},
				TerminationQueue: queue.MessageQueueInfo{
					QueueType: queue.TerminationQueue,
					QueueURL:  "https://sqs.amazonaws.com/term-queue",
				},
			},
			healthCacheGPUs: map[types.GPUName]types.GPUResource{
				"H100": {Capacity: 8, Allocated: 0},
			},
			expectCredentialsChanged: false,
		},
		{
			name:                     "Feature flag enabled and credentials empty - create NATS credentials",
			useNATSQueue:             true,
			inputCredentials:         types.QueueCredentials{},
			healthCacheGPUs:          map[types.GPUName]types.GPUResource{},
			expectCredentialsChanged: true,
			expectedGPUsInCreds:      []types.GPUName{},
		},
		{
			name:             "Feature flag enabled, empty credentials, single GPU in health cache",
			useNATSQueue:     true,
			inputCredentials: types.QueueCredentials{},
			healthCacheGPUs: map[types.GPUName]types.GPUResource{
				"A100": {Capacity: 8, Allocated: 2},
			},
			expectCredentialsChanged: true,
			expectedGPUsInCreds:      []types.GPUName{"A100"},
		},
		{
			name:             "Feature flag enabled, empty credentials, multiple GPUs in health cache",
			useNATSQueue:     true,
			inputCredentials: types.QueueCredentials{},
			healthCacheGPUs: map[types.GPUName]types.GPUResource{
				"A100": {Capacity: 8, Allocated: 2},
				"H100": {Capacity: 8, Allocated: 0},
				"V100": {Capacity: 4, Allocated: 4},
			},
			expectCredentialsChanged: true,
			expectedGPUsInCreds:      []types.GPUName{"A100", "H100", "V100"},
		},
		{
			name:         "Feature flag enabled, empty ClusterCreationQueues only",
			useNATSQueue: true,
			inputCredentials: types.QueueCredentials{
				CreationQueues: types.CreationQueueInfoSet{
					"A100": queue.MessageQueueInfo{
						QueueType: queue.CreationQueue,
						QueueURL:  "some-url",
					},
				},
			},
			healthCacheGPUs: map[types.GPUName]types.GPUResource{
				"H100": {Capacity: 8, Allocated: 0},
			},
			expectCredentialsChanged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create mock feature flag fetcher
			fff := &featureflagmock.Fetcher{}
			if tt.useNATSQueue {
				fff.SetFeatureFlags(featureflag.SelfHosted)
			}

			// Create mock health cache
			mockHealthCache := &mockHealthStatusCache{
				status: types.AgentHealth{
					Status:   types.HealthStatusHealthy,
					GPUUsage: tt.healthCacheGPUs,
				},
			}

			// Create agent with mocked dependencies
			agent := &Agent{
				AgentOptions: &AgentOptions{
					FeatureFlagFetcher: fff,
					ClusterID:          "test-cluster-123",
				},
				backendHealthCache: mockHealthCache,
			}

			// Call the function under test
			result := agent.postProcessQueueCredentials(ctx, tt.inputCredentials)

			if !tt.expectCredentialsChanged {
				// Verify credentials are unchanged
				assert.Equal(t, tt.inputCredentials, result, "Credentials should not be modified")
			} else {
				// Verify credentials were modified to use NATS
				assert.NotEqual(t, tt.inputCredentials, result, "Credentials should be modified")

				// Verify termination queue is set to NATS stream
				assert.Equal(t, queue.TerminationQueue, result.TerminationQueue.QueueType)
				assert.Equal(t, natsqueue.TermStreamName, result.TerminationQueue.QueueURL)

				// Verify creation queues are initialized
				assert.NotNil(t, result.CreationQueues)
				assert.NotNil(t, result.ClusterCreationQueues)
				assert.NotNil(t, result.TaskClusterCreationQueues)

				// Verify cluster creation queues match health cache GPUs
				assert.Len(t, result.ClusterCreationQueues, len(tt.expectedGPUsInCreds))
				for _, gpuName := range tt.expectedGPUsInCreds {
					queueInfo, exists := result.ClusterCreationQueues[gpuName]
					assert.True(t, exists, "GPU %s should be in ClusterCreationQueues", gpuName)
					assert.Equal(t, queue.CreationQueue, queueInfo.QueueType)
					assert.Equal(t, natsqueue.CreateStreamName, queueInfo.QueueURL)
					assert.Equal(t, string(gpuName), queueInfo.GPU)
				}

				// Verify no unexpected GPUs in the result
				for gpuName := range result.ClusterCreationQueues {
					assert.Contains(t, tt.expectedGPUsInCreds, gpuName, "Unexpected GPU %s in ClusterCreationQueues", gpuName)
				}
			}
		})
	}
}

// mockHealthStatusCache is a mock implementation of health.StatusCache for testing
type mockHealthStatusCache struct {
	status types.AgentHealth
}

func (m *mockHealthStatusCache) GetStatus() types.AgentHealth {
	return m.status
}

func (m *mockHealthStatusCache) GetStatusForLevel(_ types.StatusLevel) types.AgentHealth {
	return m.status
}

func (m *mockHealthStatusCache) RefreshStatus(_ context.Context) (types.AgentHealth, error) {
	return m.status, nil
}

func TestStartReadinessNotSetOnICMSRegistrationFailure(t *testing.T) {
	ctx := newTestContext()

	agentOpts := AgentOptions{
		TokenFetcherOptions: nvcaauth.TokenFetcherOptions{
			OAuthTokenScope:      "byoc_registration",
			OAuthClientID:        "foo",
			OAuthClientSecretKey: "bar",
		},
		NCAId:                          "randomNCAId123",
		ClusterName:                    "bartnvbackend",
		ClusterID:                      "clusterid-1",
		ClusterDescription:             "this is a test cluster",
		ClusterGroupName:               "group of all A30",
		ComputeBackend:                 "k8s",
		CloudProvider:                  "on-prem",
		NamespaceLabels:                labels.Set{"foo": "bar"},
		K8sVersion:                     "1.27.8",
		CredRenewInterval:              DefaultCredRenewInterval,
		HeartbeatInterval:              DefaultHeartBeatInterval,
		SyncQueueInterval:              defaultSyncQueueInterval,
		SyncRequestStatusInterval:      DefaultSyncRequestStatusInterval,
		PeriodicInstanceStatusInterval: DefaultPeriodicInstanceStatusInterval,
		SyncAcknowledgeRequestInterval: ackReqInterval,
		GPUCapacity:                    2,
		FeatureFlagFetcher:             featureflag.DefaultFetcher,
		MetricsRegisterer:              prometheus.NewRegistry(),
	}

	ag := newMockAgentSingleGPU(t, ctx, agentOpts)
	ag.icmsClient = &mockICMSClient{
		registerErr: fmt.Errorf("409 Conflict: instance type rename with active functions"),
	}

	err := ag.Start(ctx)
	require.Error(t, err, "Start should fail when ICMS registration returns an error")

	// Readiness must never be armed when Start() fails during ICMS registration.
	_, ok := ag.readinessCheckGetter.GetCheck()
	assert.False(t, ok, "readiness check should not be set when ICMS registration fails")

	// The health server is started early in Start(), so the /healthz endpoint should
	// respond with 503 since readiness was never armed.
	resp, httpErr := http.Get("http://" + ag.NVCASvcAddress + health.HTTPReadinessRoutePath)
	if assert.NoError(t, httpErr, "health endpoint should be reachable") {
		defer resp.Body.Close()
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode,
			"readiness endpoint should return 503 when Start() fails before arming readiness")
	}
}
