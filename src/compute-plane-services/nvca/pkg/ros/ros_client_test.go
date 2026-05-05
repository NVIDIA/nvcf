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

package ros

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// mockTokenFetcher implements nvcaauth.TokenFetcher for testing
type mockTokenFetcher struct {
	token string
	err   error
}

func (m *mockTokenFetcher) FetchToken(_ context.Context) (string, error) {
	return m.token, m.err
}

func TestNewROSClient(t *testing.T) {
	ctx := context.Background()
	tokenFetcher := &mockTokenFetcher{token: "test-token"}

	tests := []struct {
		name        string
		ncaID       string
		clusterID   string
		endpointURL string
	}{
		{
			name:        "basic initialization",
			ncaID:       "nca-123",
			clusterID:   "cluster-456",
			endpointURL: "https://api.ros.nvidia.com",
		},
		{
			name:        "endpoint with trailing slash",
			ncaID:       "nca-789",
			clusterID:   "cluster-012",
			endpointURL: "https://api.ros.nvidia.com/",
		},
		{
			name:        "empty values",
			ncaID:       "",
			clusterID:   "",
			endpointURL: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewROSClient(ctx, tt.ncaID, tt.clusterID, tt.endpointURL, tokenFetcher, nil)

			assert.NotNil(t, client)
			assert.NotNil(t, client.Client)
			assert.NotNil(t, client.tracer)
			assert.Equal(t, tt.ncaID, client.ncaID)
			assert.Equal(t, tt.clusterID, client.clusterID)
			// Endpoint should have trailing slash removed
			expectedEndpoint := tt.endpointURL
			if len(expectedEndpoint) > 0 && expectedEndpoint[len(expectedEndpoint)-1] == '/' {
				expectedEndpoint = expectedEndpoint[:len(expectedEndpoint)-1]
			}
			assert.Equal(t, expectedEndpoint, client.endpoint)
		})
	}
}

func TestROSClient_GetConfig(t *testing.T) {
	ctx := context.Background()
	tokenFetcher := &mockTokenFetcher{token: "test-token"}

	client := NewROSClient(ctx, "nca-123", "cluster-456", "https://api.ros.nvidia.com", tokenFetcher, nil)

	clusterID, endpoint := client.GetConfig()

	assert.Equal(t, "cluster-456", clusterID)
	assert.Equal(t, "https://api.ros.nvidia.com", endpoint)
}

func TestROSClient_PostFunctionInstanceStatusUpdate_Success(t *testing.T) {
	ctx := context.Background()

	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/v1/functions/status", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

		// Decode body and verify clusterID was set
		var updates []types.InstanceUpdateStatusDTO
		err := json.NewDecoder(r.Body).Decode(&updates)
		require.NoError(t, err)
		assert.Len(t, updates, 1)
		assert.Equal(t, "cluster-456", updates[0].ClusterID)

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	tokenFetcher := &mockTokenFetcher{token: "test-token"}
	client := NewROSClient(ctx, "nca-123", "cluster-456", server.URL, tokenFetcher, nil)

	updates := []types.InstanceUpdateStatusDTO{
		{
			InstanceID: "instance-1",
			FunctionID: "func-1",
		},
	}

	err := client.PostFunctionInstanceStatusUpdate(ctx, "icms-req-1", "instance-1", updates)
	assert.NoError(t, err)
}

func TestROSClient_PostFunctionInstanceStatusUpdate_NoToken(t *testing.T) {
	ctx := context.Background()

	// Create test server that accepts requests without auth
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify no Authorization header when token is empty
		assert.Empty(t, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	tokenFetcher := &mockTokenFetcher{token: ""}
	client := NewROSClient(ctx, "nca-123", "cluster-456", server.URL, tokenFetcher, nil)

	updates := []types.InstanceUpdateStatusDTO{
		{InstanceID: "instance-1"},
	}

	err := client.PostFunctionInstanceStatusUpdate(ctx, "icms-req-1", "instance-1", updates)
	assert.NoError(t, err)
}

func TestROSClient_PostFunctionInstanceStatusUpdate_TokenFetchError(t *testing.T) {
	ctx := context.Background()

	// Create a server that should never be called
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Server should not be called when token fetch fails")
	}))
	t.Cleanup(server.Close)

	tokenFetcher := &mockTokenFetcher{
		token: "",
		err:   errors.New("token fetch failed"),
	}
	client := NewROSClient(ctx, "nca-123", "cluster-456", server.URL, tokenFetcher, nil)

	updates := []types.InstanceUpdateStatusDTO{
		{InstanceID: "instance-1"},
	}

	err := client.PostFunctionInstanceStatusUpdate(ctx, "icms-req-1", "instance-1", updates)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "token fetch failed")
}

func TestROSClient_PostFunctionInstanceStatusUpdate_Non200Response(t *testing.T) {
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal server error"))
	}))
	t.Cleanup(server.Close)

	tokenFetcher := &mockTokenFetcher{token: "test-token"}
	client := NewROSClient(ctx, "nca-123", "cluster-456", server.URL, tokenFetcher, nil)

	updates := []types.InstanceUpdateStatusDTO{
		{InstanceID: "instance-1"},
	}

	err := client.PostFunctionInstanceStatusUpdate(ctx, "icms-req-1", "instance-1", updates)
	assert.Error(t, err)
	// The retryable client wraps the error, so we just verify there's an error
	// The actual "error response from ROS" is returned when retries are exhausted
}

func TestROSClient_PostFunctionInstanceStatusUpdate_403Increments(t *testing.T) {
	ctx := context.Background()
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("forbidden"))
	}))
	t.Cleanup(server.Close)

	tokenFetcher := &mockTokenFetcher{token: "test-token"}
	client := NewROSClient(ctx, "nca-123", "cluster-456", server.URL, tokenFetcher, nil)

	updates := []types.InstanceUpdateStatusDTO{
		{InstanceID: "instance-1"},
	}

	// Make requests and verify 403 counter increments
	for i := 0; i < 5; i++ {
		err := client.PostFunctionInstanceStatusUpdate(ctx, "icms-req-1", "instance-1", updates)
		assert.Error(t, err)
		assert.Equal(t, uint64(i+1), client.http403ErrCount)
	}
	assert.Equal(t, 5, requestCount)
}

func TestROSClient_PostFunctionInstanceStatusUpdate_403ThresholdExceeded(t *testing.T) {
	ctx := context.Background()
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(server.Close)

	tokenFetcher := &mockTokenFetcher{token: "test-token"}
	client := NewROSClient(ctx, "nca-123", "cluster-456", server.URL, tokenFetcher, nil)
	// Set counter above threshold
	client.http403ErrCount = http403ErrThreshold + 1

	updates := []types.InstanceUpdateStatusDTO{
		{InstanceID: "instance-1"},
	}

	// Should return nil without making request when threshold exceeded
	err := client.PostFunctionInstanceStatusUpdate(ctx, "icms-req-1", "instance-1", updates)
	assert.NoError(t, err)
	assert.Equal(t, 0, requestCount, "No requests should be made when threshold exceeded")
}

func TestROSClient_PostFunctionInstanceStatusUpdate_ClusterIDSet(t *testing.T) {
	ctx := context.Background()
	expectedClusterID := "my-cluster-id"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var updates []types.InstanceUpdateStatusDTO
		err := json.NewDecoder(r.Body).Decode(&updates)
		require.NoError(t, err)

		// Verify clusterID was set on all updates
		for _, u := range updates {
			assert.Equal(t, expectedClusterID, u.ClusterID)
		}

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	tokenFetcher := &mockTokenFetcher{token: "test-token"}
	client := NewROSClient(ctx, "nca-123", expectedClusterID, server.URL, tokenFetcher, nil)

	updates := []types.InstanceUpdateStatusDTO{
		{InstanceID: "instance-1", ClusterID: "should-be-overwritten"},
		{InstanceID: "instance-2"},
		{InstanceID: "instance-3", ClusterID: "also-overwritten"},
	}

	err := client.PostFunctionInstanceStatusUpdate(ctx, "icms-req-1", "instance-1", updates)
	assert.NoError(t, err)
}

func TestROSClient_PostFunctionInstanceStatusUpdate_EmptyUpdates(t *testing.T) {
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var updates []types.InstanceUpdateStatusDTO
		err := json.NewDecoder(r.Body).Decode(&updates)
		require.NoError(t, err)
		assert.Len(t, updates, 0)

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	tokenFetcher := &mockTokenFetcher{token: "test-token"}
	client := NewROSClient(ctx, "nca-123", "cluster-456", server.URL, tokenFetcher, nil)

	updates := []types.InstanceUpdateStatusDTO{}

	err := client.PostFunctionInstanceStatusUpdate(ctx, "icms-req-1", "instance-1", updates)
	assert.NoError(t, err)
}

func TestROSClient_PostFunctionInstanceStatusUpdate_RequestTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// Use a mock server instead of public URL
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// This shouldn't be reached due to canceled context
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	tokenFetcher := &mockTokenFetcher{token: "test-token"}
	client := NewROSClient(context.Background(), "nca-123", "cluster-456", server.URL, tokenFetcher, nil)

	updates := []types.InstanceUpdateStatusDTO{
		{InstanceID: "instance-1"},
	}

	err := client.PostFunctionInstanceStatusUpdate(ctx, "icms-req-1", "instance-1", updates)
	assert.Error(t, err)
}
