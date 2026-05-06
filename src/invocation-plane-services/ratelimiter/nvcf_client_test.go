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

package ratelimiter

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	nvcfpb "ratelimiter/nvcf/pb"
)

// mockRateLimitClient is a mock implementation of the RateLimitClient interface
type mockRateLimitClient struct {
	nvcfpb.RateLimitClient
	response  *nvcfpb.RateLimitPolicyResponse
	err       error
	callCount int
}

func (m *mockRateLimitClient) RateLimitPolicy(ctx context.Context, req *nvcfpb.RateLimitPolicyRequest, opts ...grpc.CallOption) (*nvcfpb.RateLimitPolicyResponse, error) {
	m.callCount++
	if m.err != nil {
		return nil, m.err
	}
	return m.response, nil
}

func init() {
	// Initialize logger for tests
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
}

// TestCachedClientCaching tests that the cached client properly caches responses
func TestCachedClientCaching(t *testing.T) {
	ctx := context.Background()

	rate := "10-S"
	mockResponse := &nvcfpb.RateLimitPolicyResponse{
		Config: &nvcfpb.RateLimitPolicyResponse_RateLimitConfig{
			Rate: &rate,
			PerNcaIdConfigs: []*nvcfpb.RateLimitPolicyResponse_RateLimitConfig_PerNcaIdConfigs{
				{NcaId: "nca_id_1", Rate: "5-S"},
			},
		},
	}

	mockClient := &mockRateLimitClient{
		response: mockResponse,
	}

	cachedClient := NewCachedNVCFClient(mockClient, 1*time.Minute)
	defer cachedClient.Close()

	req := &nvcfpb.RateLimitPolicyRequest{
		FunctionId:        "test_function",
		FunctionVersionId: "test_version",
	}

	// First call - should hit the backend
	resp1, err := cachedClient.RateLimitPolicy(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, resp1)
	assert.Equal(t, 1, mockClient.callCount, "First call should hit the backend")
	assert.Equal(t, mockResponse, resp1)

	// Second call - should use cache
	resp2, err := cachedClient.RateLimitPolicy(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, resp2)
	assert.Equal(t, 1, mockClient.callCount, "Second call should use cache")
	assert.Equal(t, mockResponse, resp2)
}

// TestCachedClientNilConfig tests handling of nil config
func TestCachedClientNilConfig(t *testing.T) {
	ctx := context.Background()

	mockResponse := &nvcfpb.RateLimitPolicyResponse{
		Config: nil,
	}

	mockClient := &mockRateLimitClient{
		response: mockResponse,
	}

	cachedClient := NewCachedNVCFClient(mockClient, 1*time.Minute)
	defer cachedClient.Close()

	req := &nvcfpb.RateLimitPolicyRequest{
		FunctionId:        "test_function",
		FunctionVersionId: "test_version",
	}

	resp, err := cachedClient.RateLimitPolicy(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Nil(t, resp.GetConfig(), "Response should have nil config")
}

// TestCachedClientClearCache tests that clearing the cache works correctly
func TestCachedClientClearCache(t *testing.T) {
	ctx := context.Background()

	rate := "10-S"
	mockResponse := &nvcfpb.RateLimitPolicyResponse{
		Config: &nvcfpb.RateLimitPolicyResponse_RateLimitConfig{
			Rate: &rate,
		},
	}

	mockClient := &mockRateLimitClient{
		response: mockResponse,
	}

	cachedClient := NewCachedNVCFClient(mockClient, 1*time.Minute)
	defer cachedClient.Close()

	req := &nvcfpb.RateLimitPolicyRequest{
		FunctionId:        "test_function",
		FunctionVersionId: "test_version",
	}

	// First call
	_, err := cachedClient.RateLimitPolicy(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, 1, mockClient.callCount)

	// Clear cache
	cachedClient.ClearCache()

	// Second call should hit the backend again
	_, err = cachedClient.RateLimitPolicy(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, 2, mockClient.callCount, "After clearing cache, should hit backend again")
}
