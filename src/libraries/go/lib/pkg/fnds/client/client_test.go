/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	types "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/fnds/common/core/types"
)

type mockTokenFetcher struct {
	token string
	err   error
}

func (m *mockTokenFetcher) FetchToken(_ context.Context) (string, error) {
	return m.token, m.err
}

func setupTestServer(_ *testing.T, handler http.HandlerFunc) (*httptest.Server, *FndsClient) {
	server := httptest.NewServer(handler)
	tokenFetcher := &mockTokenFetcher{token: "test-token"}
	client := NewFndsClient(server.URL, "test-nca", tokenFetcher)
	return server, client
}

func TestListInstances(t *testing.T) {
	functionVersionId := uuid.New()
	expectedInstances := []types.Instance{
		{InstanceId: "instance-1", LastEventDetails: json.RawMessage(`null`)},
		{InstanceId: "instance-2", LastEventDetails: json.RawMessage(`null`)},
	}

	server, client := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, fmt.Sprintf("/v1/ledger/versions/%s/instances", functionVersionId), r.URL.Path)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		// Send response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		err := json.NewEncoder(w).Encode(expectedInstances)
		require.NoError(t, err)
	})
	t.Cleanup(server.Close)

	instances, err := client.ListInstances(context.Background(), functionVersionId)
	require.NoError(t, err)
	assert.Equal(t, expectedInstances, instances)
}

func TestCreateEvent(t *testing.T) {
	event := types.StageTransitionEvent{
		FunctionVersionId: uuid.New(),
		InstanceId:        "test-instance",
		EventType:         "test-event",
		Details:           json.RawMessage(`null`),
	}

	server, client := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

		var receivedEvent types.StageTransitionEvent
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&receivedEvent))
		assert.Equal(t, event, receivedEvent)

		w.WriteHeader(http.StatusOK)
	})
	t.Cleanup(server.Close)

	err := client.CreateEvent(context.Background(), event)
	require.NoError(t, err)
}

func TestGetEvents(t *testing.T) {
	expectedEvents := []types.StageTransitionEvent{
		{
			FunctionVersionId: uuid.New(),
			InstanceId:        "test-instance",
			EventType:         "event-1",
			Details:           json.RawMessage(`null`),
		},
		{
			FunctionVersionId: uuid.New(),
			InstanceId:        "test-instance",
			EventType:         "event-2",
			Details:           json.RawMessage(`null`),
		},
	}

	server, client := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.NoError(t, json.NewEncoder(w).Encode(expectedEvents))
	})
	t.Cleanup(server.Close)

	events, err := client.GetEvents(context.Background(), uuid.New(), "test-instance")
	require.NoError(t, err)
	assert.Equal(t, expectedEvents, events)
}

func TestGetEventByEventType(t *testing.T) {
	expectedEvent := types.StageTransitionEvent{
		FunctionVersionId: uuid.New(),
		InstanceId:        "test-instance",
		EventType:         "test-event",
		Details:           json.RawMessage(`null`),
	}

	server, client := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.NoError(t, json.NewEncoder(w).Encode(expectedEvent))
	})
	t.Cleanup(server.Close)

	event, err := client.GetEventByEventType(context.Background(), uuid.New(), "test-instance", "test-event")
	require.NoError(t, err)
	assert.Equal(t, expectedEvent, event)
}

func TestDeleteInstanceEvents(t *testing.T) {
	server, client := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		w.WriteHeader(http.StatusOK)
	})
	t.Cleanup(server.Close)

	err := client.DeleteInstanceEvents(context.Background(), uuid.New(), "test-instance")
	require.NoError(t, err)
}

func TestDeleteFunctionVersionEvents(t *testing.T) {
	server, client := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		w.WriteHeader(http.StatusOK)
	})
	t.Cleanup(server.Close)

	err := client.DeleteFunctionVersionEvents(context.Background(), uuid.New())
	require.NoError(t, err)
}

func TestGetStats(t *testing.T) {
	expectedStats := types.DeploymentStats{
		FunctionVersionId:          uuid.New(),
		Pending:                    10,
		PendingError:               8,
		Building:                   2,
		BuildingError:              2,
		DownloadingModel:           2,
		DownloadingModelError:      2,
		DownloadingContainer:       2,
		DownloadingContainerError:  2,
		InitializingContainer:      2,
		InitializingContainerError: 2,
		Ready:                      2,
		Destroyed:                  2,
	}

	server, client := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		json.NewEncoder(w).Encode(expectedStats)
	})
	t.Cleanup(server.Close)

	stats, err := client.GetStats(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.Equal(t, expectedStats, stats)
}

func TestErrorHandling(t *testing.T) {
	errorResponse := map[string]string{"error": "test error"}

	server, client := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(errorResponse)
	})
	t.Cleanup(server.Close)

	_, err := client.ListInstances(context.Background(), uuid.New())
	require.Error(t, err)

	apiErr, ok := err.(*SimpleAPIError)
	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
}

func TestFndsClientOptions(t *testing.T) {
	// Test default client
	tokenFetcher := &mockTokenFetcher{token: "test-token"}
	defaultClient := NewFndsClient("http://test", "test-nca", tokenFetcher)
	assert.Equal(t, 10*time.Second, defaultClient.httpClient.Timeout)

	// Test custom HTTP client
	customTimeout := 20 * time.Second
	customClient := &http.Client{
		Timeout: customTimeout,
	}
	clientWithCustomHTTP := NewFndsClient("http://test", "test-nca", tokenFetcher, WithHTTPClient(customClient))
	assert.Equal(t, customTimeout, clientWithCustomHTTP.httpClient.Timeout)
	assert.Equal(t, customClient, clientWithCustomHTTP.httpClient)

	// Test that other fields are set correctly
	assert.Equal(t, "http://test", clientWithCustomHTTP.BaseURL)
	assert.Equal(t, "test-nca", clientWithCustomHTTP.NcaId)
	assert.Equal(t, tokenFetcher, clientWithCustomHTTP.tokenFetcher)

	// Test with nil client option
	clientWithNilHTTP := NewFndsClient("http://test", "test-nca", tokenFetcher, WithHTTPClient(nil))
	assert.NotNil(t, clientWithNilHTTP.httpClient, "Client should not be nil when passing nil option")
	assert.Equal(t, 10*time.Second, clientWithNilHTTP.httpClient.Timeout, "Should use default timeout when passing nil client")

	// Test that client works with custom timeout
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond) // Simulate slow response that will definitely exceed the timeout
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	// Create client with very short timeout
	shortTimeoutClient := &http.Client{
		Timeout: 10 * time.Millisecond,
	}
	clientWithShortTimeout := NewFndsClient(server.URL, "test-nca", tokenFetcher, WithHTTPClient(shortTimeoutClient))

	// Should get timeout error
	_, err := clientWithShortTimeout.ListInstances(context.Background(), uuid.New())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Client.Timeout exceeded")
}

func TestListInstancesV2(t *testing.T) {
	functionVersionId := uuid.New()
	deploymentId := uuid.New()
	expectedInstances := []types.Instance{
		{InstanceId: "instance-1", LastEventDetails: json.RawMessage(`null`)},
		{InstanceId: "instance-2", LastEventDetails: json.RawMessage(`null`)},
	}

	server, client := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, fmt.Sprintf("/v2/ledger/versions/%s/deployments/%s/instances", functionVersionId, deploymentId), r.URL.Path)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		// Send response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		err := json.NewEncoder(w).Encode(expectedInstances)
		require.NoError(t, err)
	})
	t.Cleanup(server.Close)

	instances, err := client.ListInstancesV2(context.Background(), functionVersionId, deploymentId)
	require.NoError(t, err)
	assert.Equal(t, expectedInstances, instances)
}

func TestCreateEventV2(t *testing.T) {
	event := types.DeploymentStageTransitionEvent{
		FunctionVersionId: uuid.New(),
		DeploymentId:      uuid.New(),
		InstanceId:        "test-instance",
		EventType:         "test-event",
		Details:           json.RawMessage(`null`),
	}

	server, client := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, fmt.Sprintf("/v2/ledger/versions/%s/deployments/%s/instances/%s", event.FunctionVersionId, event.DeploymentId, event.InstanceId), r.URL.Path)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

		var receivedEvent types.DeploymentStageTransitionEvent
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&receivedEvent))
		assert.Equal(t, event, receivedEvent)

		w.WriteHeader(http.StatusOK)
	})
	t.Cleanup(server.Close)

	err := client.CreateEventV2(context.Background(), event)
	require.NoError(t, err)
}

func TestGetEventsV2(t *testing.T) {
	functionVersionId := uuid.New()
	deploymentId := uuid.New()
	expectedEvents := []types.DeploymentStageTransitionEvent{
		{
			FunctionVersionId: functionVersionId,
			DeploymentId:      deploymentId,
			InstanceId:        "test-instance",
			EventType:         "event-1",
			Details:           json.RawMessage(`null`),
		},
		{
			FunctionVersionId: functionVersionId,
			DeploymentId:      deploymentId,
			InstanceId:        "test-instance",
			EventType:         "event-2",
			Details:           json.RawMessage(`null`),
		},
	}

	server, client := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, fmt.Sprintf("/v2/ledger/versions/%s/deployments/%s/instances/test-instance", functionVersionId, deploymentId), r.URL.Path)
		assert.NoError(t, json.NewEncoder(w).Encode(expectedEvents))
	})
	t.Cleanup(server.Close)

	events, err := client.GetEventsV2(context.Background(), functionVersionId, deploymentId, "test-instance")
	require.NoError(t, err)
	assert.Equal(t, expectedEvents, events)
}

func TestGetEventByEventTypeV2(t *testing.T) {
	functionVersionId := uuid.New()
	deploymentId := uuid.New()
	expectedEvent := types.DeploymentStageTransitionEvent{
		FunctionVersionId: functionVersionId,
		DeploymentId:      deploymentId,
		InstanceId:        "test-instance",
		EventType:         "test-event",
		Details:           json.RawMessage(`null`),
	}

	server, client := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, fmt.Sprintf("/v2/ledger/versions/%s/deployments/%s/instances/test-instance/events/test-event", functionVersionId, deploymentId), r.URL.Path)
		assert.NoError(t, json.NewEncoder(w).Encode(expectedEvent))
	})
	t.Cleanup(server.Close)

	event, err := client.GetEventByEventTypeV2(context.Background(), functionVersionId, deploymentId, "test-instance", "test-event")
	require.NoError(t, err)
	assert.Equal(t, expectedEvent, event)
}

func TestDeleteInstanceEventsV2(t *testing.T) {
	functionVersionId := uuid.New()
	deploymentId := uuid.New()

	server, client := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		assert.Equal(t, fmt.Sprintf("/v2/ledger/versions/%s/deployments/%s/instances/test-instance", functionVersionId, deploymentId), r.URL.Path)
		w.WriteHeader(http.StatusOK)
	})
	t.Cleanup(server.Close)

	err := client.DeleteInstanceEventsV2(context.Background(), functionVersionId, deploymentId, "test-instance")
	require.NoError(t, err)
}

func TestDeleteFunctionVersionEventsV2(t *testing.T) {
	functionVersionId := uuid.New()
	deploymentId := uuid.New()

	server, client := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		assert.Equal(t, fmt.Sprintf("/v2/ledger/versions/%s/deployments/%s/instances", functionVersionId, deploymentId), r.URL.Path)
		w.WriteHeader(http.StatusOK)
	})
	t.Cleanup(server.Close)

	err := client.DeleteFunctionVersionEventsV2(context.Background(), functionVersionId, deploymentId)
	require.NoError(t, err)
}

func TestGetStatsV2(t *testing.T) {
	functionVersionId := uuid.New()
	deploymentId := uuid.New()
	expectedStats := types.DeploymentStats{
		FunctionVersionId:          functionVersionId,
		Pending:                    10,
		PendingError:               8,
		Building:                   2,
		BuildingError:              2,
		DownloadingModel:           2,
		DownloadingModelError:      2,
		DownloadingContainer:       2,
		DownloadingContainerError:  2,
		InitializingContainer:      2,
		InitializingContainerError: 2,
		Ready:                      2,
		Destroyed:                  2,
	}

	server, client := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, fmt.Sprintf("/v2/ledger/versions/%s/deployments/%s/stats", functionVersionId, deploymentId), r.URL.Path)
		json.NewEncoder(w).Encode(expectedStats)
	})
	t.Cleanup(server.Close)

	stats, err := client.GetStatsV2(context.Background(), functionVersionId, deploymentId)
	require.NoError(t, err)
	assert.Equal(t, expectedStats, stats)
}

func TestNewStageTransitionEvent(t *testing.T) {
	client := &FndsClient{}

	functionId := uuid.New()
	functionVersionId := uuid.New()
	detailsJSON := []byte(`{"key": "value"}`)

	// Test successful creation
	event, err := client.NewStageTransitionEvent("test-nca", functionId, functionVersionId, "instance-1", "event-name", "event-type", detailsJSON)
	require.NoError(t, err)
	assert.Equal(t, "test-nca", event.NcaId)
	assert.Equal(t, functionId, event.FunctionId)
	assert.Equal(t, functionVersionId, event.FunctionVersionId)
	assert.Equal(t, "instance-1", event.InstanceId)
	assert.Equal(t, "event-name", event.Event)
	assert.Equal(t, "event-type", event.EventType)
	assert.Equal(t, json.RawMessage(detailsJSON), event.Details)
	assert.WithinDuration(t, time.Now(), event.Timestamp, time.Second)

	// Test validation errors
	_, err = client.NewStageTransitionEvent("", functionId, functionVersionId, "instance-1", "event-name", "event-type", detailsJSON)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ncaId cannot be empty")

	_, err = client.NewStageTransitionEvent("test-nca", uuid.Nil, functionVersionId, "instance-1", "event-name", "event-type", detailsJSON)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "functionId cannot be nil UUID")

	_, err = client.NewStageTransitionEvent("test-nca", functionId, uuid.Nil, "instance-1", "event-name", "event-type", detailsJSON)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "functionVersionId cannot be nil UUID")

	_, err = client.NewStageTransitionEvent("test-nca", functionId, functionVersionId, "", "event-name", "event-type", detailsJSON)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "instanceId cannot be empty")

	_, err = client.NewStageTransitionEvent("test-nca", functionId, functionVersionId, "instance-1", "", "event-type", detailsJSON)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "event cannot be empty")

	_, err = client.NewStageTransitionEvent("test-nca", functionId, functionVersionId, "instance-1", "event-name", "", detailsJSON)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "eventType cannot be empty")
}

func TestNewDeploymentStageTransitionEvent(t *testing.T) {
	client := &FndsClient{}

	functionId := uuid.New()
	functionVersionId := uuid.New()
	deploymentId := uuid.New()
	detailsJSON := []byte(`{"key": "value"}`)

	// Test successful creation
	event, err := client.NewDeploymentStageTransitionEvent("test-nca", functionId, functionVersionId, deploymentId, "instance-1", "event-name", "event-type", detailsJSON)
	require.NoError(t, err)
	assert.Equal(t, "test-nca", event.NcaId)
	assert.Equal(t, functionId, event.FunctionId)
	assert.Equal(t, functionVersionId, event.FunctionVersionId)
	assert.Equal(t, deploymentId, event.DeploymentId)
	assert.Equal(t, "instance-1", event.InstanceId)
	assert.Equal(t, "event-name", event.Event)
	assert.Equal(t, "event-type", event.EventType)
	assert.Equal(t, json.RawMessage(detailsJSON), event.Details)
	assert.WithinDuration(t, time.Now(), event.Timestamp, time.Second)

	// Test validation errors
	_, err = client.NewDeploymentStageTransitionEvent("", functionId, functionVersionId, deploymentId, "instance-1", "event-name", "event-type", detailsJSON)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ncaId cannot be empty")

	_, err = client.NewDeploymentStageTransitionEvent("test-nca", uuid.Nil, functionVersionId, deploymentId, "instance-1", "event-name", "event-type", detailsJSON)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "functionId cannot be nil UUID")

	_, err = client.NewDeploymentStageTransitionEvent("test-nca", functionId, uuid.Nil, deploymentId, "instance-1", "event-name", "event-type", detailsJSON)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "functionVersionId cannot be nil UUID")

	_, err = client.NewDeploymentStageTransitionEvent("test-nca", functionId, functionVersionId, uuid.Nil, "instance-1", "event-name", "event-type", detailsJSON)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "deploymentId cannot be nil UUID")

	_, err = client.NewDeploymentStageTransitionEvent("test-nca", functionId, functionVersionId, deploymentId, "", "event-name", "event-type", detailsJSON)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "instanceId cannot be empty")

	_, err = client.NewDeploymentStageTransitionEvent("test-nca", functionId, functionVersionId, deploymentId, "instance-1", "", "event-type", detailsJSON)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "event cannot be empty")

	_, err = client.NewDeploymentStageTransitionEvent("test-nca", functionId, functionVersionId, deploymentId, "instance-1", "event-name", "", detailsJSON)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "eventType cannot be empty")
}

func TestGetStatusCodeFromError(t *testing.T) {
	// Test with SimpleAPIError
	apiErr := &SimpleAPIError{StatusCode: 404, JSONString: `{"error": "not found"}`}
	statusCode, ok := GetStatusCodeFromError(apiErr)
	assert.True(t, ok)
	assert.Equal(t, 404, statusCode)

	// Test with non-API error
	regularErr := fmt.Errorf("regular error")
	statusCode, ok = GetStatusCodeFromError(regularErr)
	assert.False(t, ok)
	assert.Equal(t, 0, statusCode)
}

func TestIsAPIError(t *testing.T) {
	// Test with SimpleAPIError
	apiErr := &SimpleAPIError{StatusCode: 404, JSONString: `{"error": "not found"}`}
	assert.True(t, IsAPIError(apiErr))

	// Test with non-API error
	regularErr := fmt.Errorf("regular error")
	assert.False(t, IsAPIError(regularErr))
}
func TestListInstances_NilContext(t *testing.T) {
	functionVersionId := uuid.New()
	expectedInstances := []types.Instance{
		{InstanceId: "instance-1", LastEventDetails: json.RawMessage(`null`)},
	}

	server, client := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expectedInstances)
	})
	t.Cleanup(server.Close)

	// Pass nil context - should use Background()
	instances, err := client.ListInstances(nil, functionVersionId)
	require.NoError(t, err)
	assert.Equal(t, expectedInstances, instances)
}

func TestFndsClient_TokenFetchError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	// Client with token fetcher that returns error
	tokenFetcher := &mockTokenFetcher{
		token: "",
		err:   fmt.Errorf("failed to fetch token"),
	}
	client := NewFndsClient(server.URL, "test-nca", tokenFetcher)

	_, err := client.ListInstances(context.Background(), uuid.New())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to fetch authentication token")
}

func TestFndsClient_GetNcaId(t *testing.T) {
	tokenFetcher := &mockTokenFetcher{token: "test-token"}
	client := NewFndsClient("http://test", "test-nca-123", tokenFetcher)
	assert.Equal(t, "test-nca-123", client.GetNcaId())
}

func TestFndsClient_InvalidURLConstruction(t *testing.T) {
	tokenFetcher := &mockTokenFetcher{token: "test-token"}
	// Create client with invalid base URL that will cause url.JoinPath to fail
	client := NewFndsClient(":", "test-nca", tokenFetcher)

	_, err := client.ListInstances(context.Background(), uuid.New())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to build URL")
}

func TestFndsClient_ResponseDecodeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Send invalid JSON
		w.Write([]byte(`{invalid json`))
	}))
	t.Cleanup(server.Close)

	tokenFetcher := &mockTokenFetcher{token: "test-token"}
	client := NewFndsClient(server.URL, "test-nca", tokenFetcher)

	_, err := client.ListInstances(context.Background(), uuid.New())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode response body")
}

func TestFndsClient_ErrorResponseReadFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusBadRequest)
		// Don't write the full body that Content-Length promises
	}))
	t.Cleanup(server.Close)

	tokenFetcher := &mockTokenFetcher{token: "test-token"}
	client := NewFndsClient(server.URL, "test-nca", tokenFetcher)

	_, err := client.ListInstances(context.Background(), uuid.New())
	require.Error(t, err)
	// Should get an error (either SimpleAPIError with truncated body or read error)
	assert.NotNil(t, err)
}

func TestFndsClient_StatusAccepted(t *testing.T) {
	functionVersionId := uuid.New()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted) // 202 should also be treated as success
		w.Write([]byte(`[]`))
	}))
	t.Cleanup(server.Close)

	tokenFetcher := &mockTokenFetcher{token: "test-token"}
	client := NewFndsClient(server.URL, "test-nca", tokenFetcher)

	instances, err := client.ListInstances(context.Background(), functionVersionId)
	require.NoError(t, err)
	assert.NotNil(t, instances)
}

func TestFndsClient_V2Methods_NilContext(t *testing.T) {
	functionVersionId := uuid.New()
	deploymentId := uuid.New()
	instanceId := "test-instance"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		// Different responses based on endpoint
		if strings.Contains(r.URL.Path, "/stats") {
			json.NewEncoder(w).Encode(types.DeploymentStats{})
		} else if r.Method == "DELETE" || r.Method == "POST" {
			w.Write([]byte(`{}`))
		} else if strings.Contains(r.URL.Path, "/events/") && !strings.Contains(r.URL.Path, "/events/delete") {
			if strings.HasSuffix(r.URL.Path, "/TEST") {
				json.NewEncoder(w).Encode(types.DeploymentStageTransitionEvent{})
			} else {
				json.NewEncoder(w).Encode([]types.DeploymentStageTransitionEvent{})
			}
		} else {
			json.NewEncoder(w).Encode([]types.Instance{})
		}
	}))
	t.Cleanup(server.Close)

	tokenFetcher := &mockTokenFetcher{token: "test-token"}
	client := NewFndsClient(server.URL, "test-nca", tokenFetcher)

	// Test ListInstancesV2 with nil context
	instances, err := client.ListInstancesV2(nil, functionVersionId, deploymentId)
	require.NoError(t, err)
	assert.NotNil(t, instances)

	// Test CreateEventV2 with nil context
	event, err := client.NewDeploymentStageTransitionEvent("nca1", functionVersionId, functionVersionId, deploymentId, instanceId, "STARTED", "TEST", []byte("{}"))
	require.NoError(t, err)
	err = client.CreateEventV2(nil, event)
	require.NoError(t, err)

	// Test GetEventsV2 with nil context
	events, err := client.GetEventsV2(nil, functionVersionId, deploymentId, instanceId)
	require.NoError(t, err)
	assert.NotNil(t, events)

	// Test GetEventByEventTypeV2 with nil context
	eventByType, err := client.GetEventByEventTypeV2(nil, functionVersionId, deploymentId, instanceId, "TEST")
	require.NoError(t, err)
	assert.NotNil(t, eventByType)

	// Test DeleteInstanceEventsV2 with nil context
	err = client.DeleteInstanceEventsV2(nil, functionVersionId, deploymentId, instanceId)
	require.NoError(t, err)

	// Test DeleteFunctionVersionEventsV2 with nil context
	err = client.DeleteFunctionVersionEventsV2(nil, functionVersionId, deploymentId)
	require.NoError(t, err)

	// Test GetStatsV2 with nil context
	stats, err := client.GetStatsV2(nil, functionVersionId, deploymentId)
	require.NoError(t, err)
	assert.NotNil(t, stats)
}

func TestFndsClient_V1Methods_NilContext(t *testing.T) {
	// Setup test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Return empty array for GetEvents
		w.Write([]byte(`[]`))
	}))
	t.Cleanup(server.Close)

	tokenFetcher := &mockTokenFetcher{token: "test-token"}
	client := NewFndsClient(server.URL, "test-nca", tokenFetcher)

	functionVersionId := uuid.New()
	functionId := uuid.New()
	instanceId := "test-instance"

	// Test CreateEvent with nil context
	event, err := client.NewStageTransitionEvent("nca1", functionId, functionVersionId, instanceId, "STARTED", "TEST", []byte("{}"))
	require.NoError(t, err)
	err = client.CreateEvent(nil, event)
	require.NoError(t, err)

	// Test GetEvents with nil context (line 185)
	_, err = client.GetEvents(nil, functionVersionId, instanceId)
	require.NoError(t, err)
}

func TestSimpleAPIError_Error(t *testing.T) {
	// Test SimpleAPIError.Error() method (line 117)
	err := &SimpleAPIError{
		StatusCode: 400,
		JSONString: `{"error": "test error"}`,
	}
	assert.Equal(t, `{"error": "test error"}`, err.Error())
}
