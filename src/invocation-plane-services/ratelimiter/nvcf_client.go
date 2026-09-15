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
	"fmt"
	"reflect"
	"time"

	"github.com/jellydator/ttlcache/v3"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc"

	nvcfpb "ratelimiter/nvcf/pb"
)

var _ nvcfpb.RateLimitClient = (*CachedNVCFClient)(nil)

const (
	defaultPolicyCacheCapacity = 100
)

type cacheKey struct {
	FunctionId        string
	FunctionVersionId string
}

// String returns a string representation for deduplication
func (k cacheKey) String() string {
	return k.FunctionId + ":" + k.FunctionVersionId
}

// CachedNVCFClient wraps the NVCF gRPC client with response caching.
// Implements the nvcfpb.RateLimitClient interface.
// Uses ttlcache's SuppressedLoader with a shared singleflight.Group to prevent duplicate concurrent requests.
type CachedNVCFClient struct {
	client   nvcfpb.RateLimitClient
	policies *ttlcache.Cache[cacheKey, *nvcfpb.RateLimitPolicyResponse]
	group    *singleflight.Group // Shared group for all SuppressedLoaders
}

func init() {
	// Validate that cacheKey has the same fields as RateLimitPolicyRequest
	// This ensures the cache key stays in sync with the proto definition
	cacheKeyType := reflect.TypeOf(cacheKey{})
	requestType := reflect.TypeOf(nvcfpb.RateLimitPolicyRequest{})

	// Check each field in cacheKey exists in RateLimitPolicyRequest with the same type
	for i := 0; i < cacheKeyType.NumField(); i++ {
		cacheKeyField := cacheKeyType.Field(i)
		requestField, ok := requestType.FieldByName(cacheKeyField.Name)
		if !ok {
			panic(fmt.Sprintf(
				"cacheKey field '%s' not found in RateLimitPolicyRequest. "+
					"If RateLimitPolicyRequest proto was updated, please update cacheKey struct accordingly.",
				cacheKeyField.Name))
		}
		if cacheKeyField.Type != requestField.Type {
			panic(fmt.Sprintf(
				"cacheKey field '%s' has type %s but RateLimitPolicyRequest has type %s. "+
					"If RateLimitPolicyRequest proto was updated, please update cacheKey struct accordingly.",
				cacheKeyField.Name, cacheKeyField.Type, requestField.Type))
		}
	}

	// Count exported fields in RateLimitPolicyRequest (ignore internal protobuf fields)
	requestFieldCount := 0
	for _, field := range reflect.VisibleFields(requestType) {
		if field.IsExported() {
			requestFieldCount++
		}
	}

	// Ensure cacheKey has all the fields from RateLimitPolicyRequest
	if cacheKeyType.NumField() != requestFieldCount {
		panic(fmt.Sprintf(
			"cacheKey field count (%d) does not match RateLimitPolicyRequest exported field count (%d). "+
				"If RateLimitPolicyRequest proto was updated, please update cacheKey struct accordingly.",
			cacheKeyType.NumField(), requestFieldCount))
	}
}

func NewCachedNVCFClient(client nvcfpb.RateLimitClient, cacheTTL time.Duration) *CachedNVCFClient {
	policies := ttlcache.New(
		ttlcache.WithTTL[cacheKey, *nvcfpb.RateLimitPolicyResponse](cacheTTL),
		ttlcache.WithCapacity[cacheKey, *nvcfpb.RateLimitPolicyResponse](defaultPolicyCacheCapacity),
		ttlcache.WithDisableTouchOnHit[cacheKey, *nvcfpb.RateLimitPolicyResponse](),
	)
	go policies.Start()

	return &CachedNVCFClient{
		client:   client,
		policies: policies,
		group:    &singleflight.Group{}, // ONE shared group for single-flight
	}
}

// RateLimitPolicy fetches the rate limit policy with caching and single flight.
// Implements the nvcfpb.RateLimitClient interface.
// Uses ttlcache's SuppressedLoader with a shared singleflight.Group to prevent duplicate concurrent requests.
func (c *CachedNVCFClient) RateLimitPolicy(ctx context.Context, req *nvcfpb.RateLimitPolicyRequest, opts ...grpc.CallOption) (*nvcfpb.RateLimitPolicyResponse, error) {
	key := cacheKey{
		FunctionId:        req.FunctionId,
		FunctionVersionId: req.FunctionVersionId,
	}

	loaderFunc := ttlcache.LoaderFunc[cacheKey, *nvcfpb.RateLimitPolicyResponse](
		func(cache *ttlcache.Cache[cacheKey, *nvcfpb.RateLimitPolicyResponse], k cacheKey) *ttlcache.Item[cacheKey, *nvcfpb.RateLimitPolicyResponse] {
			zap.L().Debug("Fetching policy from NVCF",
				zap.String("function id", req.FunctionId),
				zap.String("function version id", req.FunctionVersionId))

			resp, err := c.client.RateLimitPolicy(ctx, req, opts...)
			if err != nil {
				zap.L().Error("Failed to fetch policy from NVCF",
					zap.String("function id", req.FunctionId),
					zap.String("function version id", req.FunctionVersionId),
					zap.Error(err))
				return nil
			}

			if resp == nil {
				zap.L().Error("Got nil response from NVCF",
					zap.String("function id", req.FunctionId),
					zap.String("function version id", req.FunctionVersionId))
				return nil
			}

			// Cache all responses (including nil configs)
			if resp.GetConfig() != nil {
				zap.L().Info("Fetched and cached policy from NVCF",
					zap.String("function id", req.FunctionId),
					zap.String("function version id", req.FunctionVersionId),
					zap.Any("policy", resp.GetConfig()))
			} else {
				zap.L().Debug("No rate limit config found, caching anyway",
					zap.String("function id", req.FunctionId),
					zap.String("function version id", req.FunctionVersionId))
			}
			return cache.Set(k, resp, ttlcache.DefaultTTL)
		},
	)

	// Create SuppressedLoader with the SHARED group - this enables single-flight across all requests
	suppressedLoader := ttlcache.NewSuppressedLoader(loaderFunc, c.group)
	item := c.policies.Get(key, ttlcache.WithLoader(suppressedLoader))

	if item == nil {
		return nil, fmt.Errorf("failed to fetch policy for function %s version %s", req.FunctionId, req.FunctionVersionId)
	}

	if !item.IsExpired() {
		zap.L().Debug("Using cached policy",
			zap.String("function id", req.FunctionId),
			zap.String("function version id", req.FunctionVersionId))
	}

	return item.Value(), nil
}

// ClearCache clears the policy cache. Used for testing.
func (c *CachedNVCFClient) ClearCache() {
	c.policies.DeleteAll()
}

func (c *CachedNVCFClient) Close() error {
	c.policies.Stop()
	return nil
}
