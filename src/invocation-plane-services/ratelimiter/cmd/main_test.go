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

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"ratelimiter"
	"ratelimiter/pb"
	"strconv"
	"sync"
	"testing"
	"time"

	olricConfig "github.com/olric-data/olric/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/exp/rand"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func TestRateLimiterService(t *testing.T) {
	ctx := context.Background()
	nvcfServer, err := NewMockNVCFAPI(ctx, t, []string{"region-2"})
	defer nvcfServer.Shutdown()
	t.Logf(" Started NVCF grpc server...")
	rateLimiter, baseServer, err := startGrpcServer(t, "0.0.0.0:7777", 3320, 3323, []string{})
	if err != nil {
		t.Logf("Failed to start grpc server: %v", err)
		t.Fatal(err)
	}
	t.Logf(" Started Rate Limit grpc server...")

	// OAuth2-style JWT for gRPC (test issuer + audience)
	const testOAuth2ClientID = "test-oauth2-client-id"
	goodCreds, publicKey, kid, err := CreateMockOAuth2Token(
		"http://localhost:8081",
		[]string{testOAuth2ClientID},
		[]string{"ratelimit:check_invocation"},
	)
	if err != nil {
		t.Fatal(err)
	}
	// start mock public key server
	err = StartPublicKeyServer(*publicKey, *kid)
	if err != nil {
		t.Fatal(err)
	}

	address := "localhost:7777"
	t.Run("testNoRateLimit", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testNoRateLimit(ctx, t, nvcfServer, client, rateLimiter)
	})

	t.Run("testRateLimitForSameKey", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testRateLimitForSameKey(ctx, t, client, rateLimiter)
	})

	t.Run("testLimitAll", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testLimitAll(ctx, t, nvcfServer, client, rateLimiter)
	})

	t.Run("testWithPerNcaIdRate", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testWithPerNcaIdRate(ctx, t, nvcfServer, client, rateLimiter)
	})

	t.Run("testOnlyPerNcaIdRate", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testPerNcaIdRateOnly(ctx, t, nvcfServer, client, rateLimiter)
	})

	t.Run("testPerNcaIdWithExclusions", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testPerNcaIdWithExclusions(ctx, t, nvcfServer, client, rateLimiter)
	})

	t.Run("testRateLimitWithExemption", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testRateLimitWithExemption(ctx, t, client, rateLimiter)
	})

	t.Run("testConstantLoad", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testConstantLoad(ctx, t, client, rateLimiter)
	})

	t.Run("testConstantLoadTooHigh", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testConstantLoadTooHigh(ctx, t, client, rateLimiter)
	})

	t.Run("testRateLimitForDifferentKey", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testRateLimitForDifferentKey(ctx, t, client, rateLimiter)
	})

	t.Run("testBadAuthNoIssuer", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		badCreds, _, _, err := CreateMockOAuth2Token(
			"http://fakeissuer",
			[]string{testOAuth2ClientID},
			[]string{"ratelimit:check_invocation"},
		)
		if err != nil {
			t.Fatal(err)
		}

		conn, client, cancel := startGrpcClient(ctx, badCreds, address, t)
		defer cancel()
		defer conn.Close()
		testBadAuthNoIssuer(ctx, t, client, rateLimiter)
	})

	t.Run("testBadAuthNoScope", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		badCreds, _, _, err := CreateMockOAuth2Token(
			"http://localhost:8081",
			[]string{testOAuth2ClientID},
			[]string{""},
		)
		if err != nil {
			t.Fatal(err)
		}

		conn, client, cancel := startGrpcClient(ctx, badCreds, address, t)
		defer cancel()
		defer conn.Close()
		testBadAuthNoScope(ctx, t, client, rateLimiter)
	})

	t.Run("testBadAuthWrongAudience", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		badCreds, _, _, err := CreateMockOAuth2Token(
			"http://localhost:8081",
			[]string{"wrongaudience"},
			[]string{"ratelimit:check_invocation"},
		)
		if err != nil {
			t.Fatal(err)
		}

		conn, client, cancel := startGrpcClient(ctx, badCreds, address, t)
		defer cancel()
		defer conn.Close()
		testBadAuthWrongAudience(ctx, t, client, rateLimiter)
	})

	t.Run("testUpdateRateLimitRate", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testRateLimitForSameKey(ctx, t, client, rateLimiter)
		testUpdateRateLimitRate(ctx, t, nvcfServer, client, rateLimiter)
		nvcfServer.lowRate = false
	})

	t.Run("testUpdateExemption", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testRateLimitForSameKey(ctx, t, client, rateLimiter)
		testUpdateExemption(ctx, t, nvcfServer, client, rateLimiter)
	})

	t.Run("testUpdateRateLimitBothRateAndExemption", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testRateLimitForSameKey(ctx, t, client, rateLimiter)
		testUpdateRateLimitBothRateAndExemption(ctx, t, nvcfServer, client, rateLimiter)
	})

	t.Run("testAddingRateLimitAfterwards", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testAddingRateLimitAfterwards(ctx, t, nvcfServer, client, rateLimiter)
	})

	t.Run("testTransitionGlobalToPerNcaId", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testTransitionGlobalToPerNcaId(ctx, t, nvcfServer, client, rateLimiter)
	})

	t.Run("testTransitionPerNcaIdToGlobal", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testTransitionPerNcaIdToGlobal(ctx, t, nvcfServer, client, rateLimiter)
	})

	t.Run("testImmediateGlobalRateChange", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testImmediateGlobalRateChange(ctx, t, nvcfServer, client, rateLimiter)
	})

	t.Run("testImmediatePerNcaIdRateChange", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testImmediatePerNcaIdRateChange(ctx, t, nvcfServer, client, rateLimiter)
	})

	t.Run("testPolicyCacheGrpcCallOptimization", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testPolicyCacheGrpcCallOptimization(ctx, t, nvcfServer, client, rateLimiter)
	})

	t.Run("testSingleFlightConcurrentRequests", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testSingleFlightConcurrentRequests(ctx, t, nvcfServer, client, rateLimiter)
	})

	t.Run("testLazyLoadingPerNcaIdLimiters", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testLazyLoadingPerNcaIdLimiters(ctx, t, nvcfServer, client, rateLimiter)
	})

	t.Run("testMultipleGlobalRates", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testMultipleGlobalRates(ctx, t, nvcfServer, client, rateLimiter)
	})

	t.Run("testMultiplePerNcaIdRates", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testMultiplePerNcaIdRates(ctx, t, nvcfServer, client, rateLimiter)
	})

	t.Run("testMultipleGlobalRatesExemption", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testMultipleGlobalRatesExemption(ctx, t, nvcfServer, client, rateLimiter)
	})

	t.Run("testMultiplePerNcaIdFallbackToGlobal", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testMultiplePerNcaIdFallbackToGlobal(ctx, t, nvcfServer, client, rateLimiter)
	})

	t.Run("testPerNcaIdOverrideReplacesGlobalMultiRates", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testPerNcaIdOverrideReplacesGlobalMultiRates(ctx, t, nvcfServer, client, rateLimiter)
	})

	t.Run("testPerNcaIdMultiRatesReplaceGlobalMultiRates", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testPerNcaIdMultiRatesReplaceGlobalMultiRates(ctx, t, nvcfServer, client, rateLimiter)
	})

	t.Run("testUnorderedMultipleRates", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testUnorderedMultipleRates(ctx, t, nvcfServer, client, rateLimiter)
	})

	t.Run("testTransitionSingleToMultiRate", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testTransitionSingleToMultiRate(ctx, t, nvcfServer, client, rateLimiter)
	})

	t.Run("testTransitionMultiToSingleRate", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testTransitionMultiToSingleRate(ctx, t, nvcfServer, client, rateLimiter)
	})

	t.Run("testTransitionMultiToMultiRate", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		conn, client, cancel := startGrpcClient(ctx, goodCreds, address, t)
		defer cancel()
		defer conn.Close()
		testTransitionMultiToMultiRate(ctx, t, nvcfServer, client, rateLimiter)
	})

	t.Run("testMultiInstances", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		if err != nil {
			t.Logf("Failed to start grpc server: %v", err)
			t.Fatal(err)
		}

		testMultiInstances(ctx, t, baseServer, rateLimiter, goodCreds)
	})

	t.Run("testMultiInstanceConstantLoad", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		if err != nil {
			t.Logf("Failed to start grpc server: %v", err)
			t.Fatal(err)
		}

		testMultiInstanceConstantLoad(ctx, t, baseServer, rateLimiter, goodCreds)
	})

	t.Run("testMultiInstanceConstantLoadTooHigh", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		if err != nil {
			t.Logf("Failed to start grpc server: %v", err)
			t.Fatal(err)
		}

		testMultiInstanceConstantLoadTooHigh(ctx, t, baseServer, rateLimiter, goodCreds)
	})

	t.Run("testMultiInstancesScaleDown", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		if err != nil {
			t.Logf("Failed to start grpc server: %v", err)
			t.Fatal(err)
		}
		testMultiInstancesScaleDown(ctx, t, baseServer, rateLimiter, goodCreds)
	})

	t.Run("testMultiInstanceMultipleRates", func(t *testing.T) {
		rateLimiter.ClearAllCaches()
		if err != nil {
			t.Logf("Failed to start grpc server: %v", err)
			t.Fatal(err)
		}
		testMultiInstanceMultipleRates(ctx, t, nvcfServer, baseServer, rateLimiter, goodCreds)
	})

	err = rateLimiter.Close()
	if err != nil {
		t.Fatal(err)
	}
	baseServer.Stop()
}

func startGrpcClient(ctx context.Context, creds grpc.DialOption, address string, t *testing.T) (*grpc.ClientConn, pb.RateLimitServiceClient, context.CancelFunc) {
	conn, err := grpc.Dial(
		address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		creds)
	if err != nil {
		t.Logf("Failed to connect to server: %v", err)
		t.Fatal(err)
	}
	client := pb.NewRateLimitServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Logf(" Started good grpc client...")
	return conn, client, cancel
}

func startGrpcServer(t *testing.T, address string, bindPort int, memberlistPort int, peers []string) (*ratelimiter.RateLimiter, *grpc.Server, error) {
	secretsPath := filepath.Join(t.TempDir(), "secrets.json")
	require.NoError(t, os.WriteFile(secretsPath, []byte(`{"nvcfApiToken":"test-nvcf-api-token"}`), 0o600))

	rateLimiterConfig := ratelimiter.Config{
		OAuth2Issuer:       "http://localhost:8081",
		Audience:           "test-oauth2-client-id",
		OAuth2ProviderHost: "localhost",
		NvcfApiUrl:         "http://localhost:9091",
		SecretsPath:        secretsPath,
		CacheTTL:           5, // set cache ttl low for testing purpose. In real life, it's 60s
		CollectMetrics:     false,
	}

	olricConf := olricConfig.New("local")
	olricConf.ReadRepair = true
	olricConf.BindAddr = "127.0.0.1"
	olricConf.BindPort = bindPort
	// The address + port that it will use to listen for other nodes to join
	olricConf.MemberlistConfig.BindAddr = "127.0.0.1"
	olricConf.MemberlistConfig.BindPort = memberlistPort
	olricConf.Peers = peers
	olricConf.DMaps.EvictionPolicy = olricConfig.LRUEviction
	olricConf.DMaps.MaxInuse = 100_000_000 // 100 MB

	rateLimiter, err := ratelimiter.NewRateLimiter(rateLimiterConfig, olricConf)
	if err != nil {
		return nil, nil, err
	}
	// pass in address
	grpcListener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, nil, err
	}
	baseServer, err := ratelimiter.MakeGrpcServer(rateLimiter, grpcListener, InterceptorLogger(zap.L()))
	if err != nil {
		return nil, nil, err
	}
	go func() {
		err := baseServer.Serve(grpcListener)
		if err != nil {
			return
		}
	}()
	return rateLimiter, baseServer, nil
}

func testNoRateLimit(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	nvcfServer.noConfig = true
	ncaId := "test_nca_id"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"
	resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)
	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
	nvcfServer.noConfig = false
}

// 0 rps
func testLimitAll(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	nvcfServer.limitAll = true
	ncaId := "test_nca_id_all"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"
	for i := 0; i < 8; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result)
	}

	// check exemption still works
	ncaId = "test_nca_id_exclude1"
	resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)

	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
	nvcfServer.limitAll = false
}

// per nca id Rate: "4-S", NcaId: "test_nca_id_1"
// global rate is 0-S
func testWithPerNcaIdRate(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	nvcfServer.withPerNcaIdRate = true
	ncaId := "test_nca_id_1"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"
	for i := 0; i < 4; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)
	}

	// check other nca id fall back to the global rate, which in this case is 0-S/none of the requests can go through
	ncaId2 := "test_nca_id_2"
	for i := 0; i < 4; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId2,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result)
	}

	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewPerNcaIdCacheKey(ncaId, functionVersionId), ncaId, functionVersionId)
	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId2, functionVersionId)

	nvcfServer.withPerNcaIdRate = false
}

// per nca id Rate: "4-S", NcaId: "test_nca_id_1"
// no global rate
func testPerNcaIdRateOnly(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	nvcfServer.perNcaIdRateOnly = true
	ncaId := "test_nca_id_1"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"
	for i := 0; i < 4; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)
	}

	// check other nca id fall back to the global rate, which doesn't exist/all requests should pass
	ncaId2 := "test_nca_id_2"
	for i := 0; i < 10; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId2,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)
	}

	// in ttlCache, only test_nca_id+functionVersionId exists
	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewPerNcaIdCacheKey(ncaId, functionVersionId), ncaId, functionVersionId)

	nvcfServer.perNcaIdRateOnly = false
}

// per nca id Rate: "2-S", NcaId: "test_nca_id_exclude1" (which is also in excluded list)
// global rate is "4-S"
// This test verifies that per-NCAID rate takes precedence over exclusions
func testPerNcaIdWithExclusions(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	nvcfServer.perNcaIdWithExclusions = true
	ncaId := "test_nca_id_exclude1" // This NCA ID is both in per-NCAID config and excluded list
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"

	// Test that per-NCAID rate (2-S) is applied, not the exclusion
	// Should allow 2 requests, then block
	for i := 0; i < 2; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "Per-NCAID rate should allow first 2 requests")
	}

	// 3rd request should be blocked by per-NCAID rate limit
	resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "Per-NCAID rate should block 3rd request")

	// Test another NCA ID that's only in excluded list (not in per-NCAID config)
	// Should use global rate (4-S) but be exempted due to exclusion
	ncaId2 := "test_nca_id_exclude2"
	for i := 0; i < 5; i++ { // More than global rate to test exemption
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId2,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "Excluded NCA ID should always be allowed")
	}

	// Test NCA ID that's not in excluded list and not in per-NCAID config
	// Should use global rate (4-S)
	ncaId3 := "test_nca_id_3"
	for i := 0; i < 4; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId3,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "Global rate should allow first 4 requests")
	}

	// 5th request should be blocked by global rate limit
	resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId3,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "Global rate should block 5th request")

	// Clean up cache
	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewPerNcaIdCacheKey(ncaId, functionVersionId), ncaId, functionVersionId)
	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId2, functionVersionId)
	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId3, functionVersionId)

	nvcfServer.perNcaIdWithExclusions = false
}

func testRateLimitForSameKey(ctx context.Context, t *testing.T, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	// within the rate limiter rps, the request should go through
	ncaId := "test_nca_id"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"
	for i := 0; i < 4; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)
	}

	// over rate limiter rps, the request should fail
	resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result)
	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
}

func testRateLimitWithExemption(ctx context.Context, t *testing.T, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	// the request should go always go through because nca id exempted
	ncaId := "test_nca_id_exclude1"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"
	for i := 0; i < 10; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)
	}
	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
}

func testRateLimitForDifferentKey(ctx context.Context, t *testing.T, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	// within the rate limiter rps, the request should go through
	for i := 0; i < 20; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             "test_nca_id_2" + strconv.Itoa(i),
			FunctionId:        "test_function_id",
			FunctionVersionId: "test_function_version_id",
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)
	}
	ncaId := "test_nca_id"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"
	resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)
	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
}

func testBadAuthNoIssuer(ctx context.Context, t *testing.T, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	ncaId := "test_nca_id"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"
	_, err := client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.Equal(t, status.Code(err), codes.Unauthenticated)
	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
}

func testBadAuthNoScope(ctx context.Context, t *testing.T, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	ncaId := "test_nca_id"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"
	_, err := client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.Equal(t, status.Code(err), codes.Unauthenticated)
	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
}

func testBadAuthWrongAudience(ctx context.Context, t *testing.T, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	ncaId := "test_nca_id"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"
	_, err := client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.Equal(t, status.Code(err), codes.Unauthenticated)
	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
}

func testConstantLoad(ctx context.Context, t *testing.T, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	// within the rate limiter rps, the request should go through
	ncaId := "test_nca_id"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"
	rps := 3 // we are configured to allow 4 rps so 3 should pass with no issue
	duration := (1000 / time.Duration(rps)) * time.Millisecond
	testDurationSeconds := 10
	iterationCount := rps * testDurationSeconds
	defer func() {
		err := rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
		if err != nil {
			t.Fatal(err)
		}
	}()
	ticker := time.NewTicker(duration)
	defer ticker.Stop()

	for i := 0; i < iterationCount; i++ {
		<-ticker.C
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)
		t.Log("successful request")
	}
}

func testConstantLoadTooHigh(ctx context.Context, t *testing.T, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	// within the rate limiter rps, the request should go through
	ncaId := "test_nca_id"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"
	rps := 8
	tickerDuration := (1000 / time.Duration(rps)) * time.Millisecond
	testDurationSeconds := 10
	iterationCount := rps * testDurationSeconds
	defer func() {
		err := rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
		if err != nil {
			t.Fatal(err)
		}
	}()
	ticker := time.NewTicker(tickerDuration)
	defer ticker.Stop()

	success := 0
	failed := 0

	for i := 0; i < iterationCount; i++ {
		<-ticker.C
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		require.NoError(t, err)
		require.NotNil(t, resp)
		if resp.Result == pb.RateLimitResult_ALLOW {
			success++
		} else if resp != nil {
			failed++
		}
		t.Logf("successful request: %d, failed request: %d", success, failed)
	}

	// ensure at least 3 rps were successful (we are configured to allow 4 rps)
	assert.Greater(t, success, 3*testDurationSeconds)
}

// Confirmed this without ttlcache.WithDisableTouchOnHit[string, Metadata]() will fail
func testUpdateRateLimitRate(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	// cache ttl is 5s, right before it expires hit the cache again to see if it renews the cache
	time.Sleep(4 * time.Second)
	ncaId := "test_nca_id"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"
	for i := 0; i < 2; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)
	}

	// now cache should have expired/more than 5s
	time.Sleep(1 * time.Second)

	nvcfServer.lowRate = true
	// the updated rate is 2-M now, so first 2 should pass
	for i := 0; i < 2; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)
	}

	// over rate limiter 2-M, the request should fail
	resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result)
	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
	nvcfServer.lowRate = false
}

// Confirmed this without ttlcache.WithDisableTouchOnHit[string, Metadata]() will fail
func testUpdateExemption(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	// cache ttl is 5s, right before it expires hit the cache again to see if it renews the cache
	time.Sleep(4 * time.Second)
	ncaId := "test_nca_id_exclude1"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"
	for i := 0; i < 4; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)
	}

	// now cache should have expired/more than 5s
	time.Sleep(1 * time.Second)

	nvcfServer.noExemption = true

	// first 4 requests should still go through because rps is 4-S
	for i := 0; i < 4; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)
	}

	// over rate limiter rate, the request should fail because nca id not exempted anymore
	resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result)
	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
	nvcfServer.noExemption = false
}

// Confirmed this without ttlcache.WithDisableTouchOnHit[string, Metadata]() will fail
func testUpdateRateLimitBothRateAndExemption(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	// cache ttl is 5s, right before it expires hit the cache again to see if it renews the cache
	time.Sleep(4 * time.Second)
	ncaId := "test_nca_id_exclude1"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"
	for i := 0; i < 4; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)
	}

	// now cache should have expired/more than 5s
	time.Sleep(1 * time.Second)

	nvcfServer.updateBothRateAndExemption = true

	// the updated rate is 2-M now, so first 2 should pass
	for i := 0; i < 2; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)
	}

	// over rate limiter rate, the request should fail because nca id not exempted anymore
	resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result)

	// newly added test_nca_id_exclude3 should still be exempted
	for i := 0; i < 4; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             "test_nca_id_exclude3",
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)
	}

	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
	nvcfServer.updateBothRateAndExemption = false
}

func testAddingRateLimitAfterwards(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	// no rate limit first
	nvcfServer.noConfig = true
	ncaId := "test_nca_id"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"
	resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)
	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)

	// rate limit policy added - clear the cache so the new policy is fetched
	rateLimiter.ClearAllCaches()
	nvcfServer.noConfig = false
	testRateLimitForSameKey(ctx, t, client, rateLimiter)
}

// testTransitionGlobalToPerNcaId validates that rate transitions work smoothly WITHOUT state carryover
// Key findings:
// - Different rates use different Olric keys (rate included in key)
// - Transitioning from global (4-M) to per-NCA-ID (2-S) starts fresh - no state collision
// - Each rate configuration maintains independent state
func testTransitionGlobalToPerNcaId(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	ncaId := "test_nca_id_transition_global_to_per_nca"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"

	// Enable transition test mode
	nvcfServer.transitionTest = true

	// Phase 1: Start with global rate (4-M = 4 per minute)
	t.Log("Phase 1: Using global rate limiter (4-M)")

	// Use up the global rate limit (4/4)
	for i := 0; i < 4; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "Request %d allowed (4-M rate)", i+1)
	}

	// 5th request blocked (exceeds 4-M)
	resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "5th request blocked (exceeds 4-M)")

	t.Log("✅ Phase 1 complete: Global rate limiter (4-M) works")

	// Phase 2: Transition to per-NCA-ID rate (2-S = 2 per second)
	t.Log("Phase 2: Transitioning to per-NCA-ID rate limiter (2-S)")
	nvcfServer.transitionTest = false      // Phase 1 complete
	nvcfServer.transitionToPerNcaId = true // Enable Phase 2 config
	rateLimiter.ClearAllCaches()           // Force policy refresh

	// Different rates use different Olric keys - no state carryover
	t.Log("✅ Phase 2: No state carryover! Transition starts fresh")

	// First 2 requests allowed (2/2 under 2-S rate)
	for i := 0; i < 2; i++ {
		resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "Request %d allowed (2-S rate)", i+1)
	}

	// 3rd request blocked (exceeds 2-S)
	resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "3rd request blocked (exceeds 2-S)")

	t.Log("✅ Phase 2 complete: Per-NCA-ID limiter (2-S) works correctly")

	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewPerNcaIdCacheKey(ncaId, functionVersionId), ncaId, functionVersionId)
	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
	nvcfServer.transitionToPerNcaId = false
}

// testTransitionPerNcaIdToGlobal validates the reverse transition: per-NCA-ID → global
// Key findings:
// - Different rates use different Olric keys (rate included in key)
// - Transitioning from per-NCA-ID (2-S) to global (4-M) starts fresh - no state collision
// - Each rate configuration maintains independent state
func testTransitionPerNcaIdToGlobal(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	ncaId := "test_nca_id_transition_per_nca_to_global"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"

	// Phase 1: Start with per-NCA-ID rate (2-S = 2 per second)
	t.Log("Phase 1: Using per-NCA-ID rate limiter (2-S)")
	nvcfServer.transitionTest = false
	nvcfServer.transitionToPerNcaId = true // Per-NCA-ID config

	// Use up the per-NCA-ID rate limit (2/2)
	for i := 0; i < 2; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "Request %d allowed (2-S rate)", i+1)
	}

	// 3rd request blocked (exceeds 2-S)
	resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "3rd request blocked (exceeds 2-S)")

	t.Log("✅ Phase 1 complete: Per-NCA-ID rate limiter (2-S) works")

	// Phase 2: Transition to global rate (4-M = 4 per minute)
	t.Log("Phase 2: Transitioning to global rate limiter (4-M)")
	nvcfServer.transitionToPerNcaId = false // Disable per-NCA-ID config
	nvcfServer.transitionTest = true        // Enable global-only config
	rateLimiter.ClearAllCaches()            // Force policy refresh

	// Different rates use different Olric keys - no state carryover
	t.Log("✅ Phase 2: No state carryover! Transition starts fresh")

	// First 4 requests allowed (4/4 under 4-M rate)
	for i := 0; i < 4; i++ {
		resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "Request %d allowed (4-M rate)", i+1)
	}

	// 5th request blocked (exceeds 4-M)
	resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "5th request blocked (exceeds 4-M)")

	t.Log("✅ Phase 2 complete: Global limiter (4-M) works correctly")

	// Cleanup
	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewPerNcaIdCacheKey(ncaId, functionVersionId), ncaId, functionVersionId)
	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
	nvcfServer.transitionTest = false
}

// testImmediateGlobalRateChange validates immediate rate change (no TTL expiration gap)
// This validates rate-in-key prevents collision without relying on TTL
func testImmediateGlobalRateChange(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	ncaId := "test_nca_id_immediate"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"

	// Phase 1: Use default global rate (4-S = 4 per second)
	t.Log("Phase 1: Using global rate limiter (4-S)")

	// Use up the rate limit (4/4)
	for i := 0; i < 4; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "Request %d allowed (4-S rate)", i+1)
	}

	// 5th request blocked (exceeds 4-S)
	resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "5th request blocked (exceeds 4-S)")

	t.Log("✅ Phase 1 complete: Global rate limiter (4-S) works")

	// Phase 2: IMMEDIATELY change to 2-S (no sleep, no TTL expiration)
	t.Log("Phase 2: Immediately changing to 2-S rate (no delay)")
	nvcfServer.immediateGlobalRateChange = true
	rateLimiter.ClearAllCaches() // Force policy refresh

	// Different rates use different keys: old 4-S vs new 2-S = no collision
	t.Log("✅ No state carryover! New rate starts fresh")

	// First 2 requests allowed (2/2 under 2-S rate)
	for i := 0; i < 2; i++ {
		resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "Request %d allowed (2-S rate)", i+1)
	}

	// 3rd request blocked (exceeds 2-S)
	resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "3rd request blocked (exceeds 2-S)")

	t.Log("✅ Phase 2 complete: Immediate rate change works without collision")

	// Cleanup
	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
	nvcfServer.immediateGlobalRateChange = false
}

// testImmediatePerNcaIdRateChange validates immediate per-NCA-ID rate change (no TTL expiration gap)
func testImmediatePerNcaIdRateChange(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	ncaId := "test_nca_id_rate_change"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"

	// Phase 1: Start with default global rate (4-S), no per-NCA-ID config yet
	t.Log("Phase 1: Using default global rate (4-S)")

	// Use up the rate limit (4/4)
	var resp *pb.RateLimitResponse
	var err error
	for i := 0; i < 4; i++ {
		resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "Request %d allowed (4-S global rate)", i+1)
	}

	// 5th request blocked (exceeds 4-S)
	resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "5th request blocked (exceeds 4-S)")

	t.Log("✅ Phase 1 complete: Global rate limiter (4-S) works")

	// Phase 2: Add per-NCA-ID config with 10-S rate
	t.Log("Phase 2: Switching to per-NCA-ID rate (10-S)")
	nvcfServer.perNcaIdRateChange = true
	rateLimiter.ClearAllCaches() // Force policy refresh

	// Different rates use different keys: global 4-S vs per-NCA-ID 10-S = no collision
	t.Log("✅ No state carryover! Per-NCA-ID limiter starts fresh")

	// Can now make 10 requests (10-S per-NCA-ID rate)
	for i := 0; i < 10; i++ {
		resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "Request %d allowed (10-S per-NCA-ID rate)", i+1)
	}

	// 11th request blocked (exceeds 10-S)
	resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "11th request blocked (exceeds 10-S)")

	t.Log("✅ Phase 2 complete: Per-NCA-ID rate change (global → 10-S) works without collision")

	// Cleanup
	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewPerNcaIdCacheKey(ncaId, functionVersionId), ncaId, functionVersionId)
	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
	nvcfServer.perNcaIdRateChange = false
}

// testPolicyCacheGrpcCallOptimization verifies that the PolicyCache reduces gRPC calls
// When multiple NCA IDs request the same function version, we should only make 1 gRPC call
func testPolicyCacheGrpcCallOptimization(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"

	nvcfServer.ResetCallCount()

	// Make requests from 5 different NCA IDs for the same function version
	// Without PolicyCache, this would make 5 gRPC calls
	// With PolicyCache, this should only make 1 gRPC call
	ncaIds := []string{"nca_id_1", "nca_id_2", "nca_id_3", "nca_id_4", "nca_id_5"}

	for _, ncaId := range ncaIds {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)
	}

	callCount := nvcfServer.GetCallCount()
	t.Logf("Made %d gRPC calls for %d different NCA IDs (same function version)", callCount, len(ncaIds))

	// With PolicyCache optimization, we should only make 1 gRPC call
	// (The policy is fetched once and reused for all NCA IDs)
	assert.Equal(t, 1, callCount, "PolicyCache should reduce gRPC calls to 1 for same function version")

	for _, ncaId := range ncaIds {
		_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
	}
}

// testSingleFlightConcurrentRequests verifies that single-flight prevents duplicate concurrent loads
// When multiple CONCURRENT requests arrive for the same key, only 1 backend call should be made
// This test validates that the singleflight.Group properly deduplicates concurrent requests
func testSingleFlightConcurrentRequests(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"

	nvcfServer.ResetCallCount()

	// Add a small delay to the mock server to ensure requests truly overlap
	// This is critical for testing single-flight behavior - without delay, requests may complete too quickly
	nvcfServer.SetResponseDelay(50 * time.Millisecond)
	defer nvcfServer.SetResponseDelay(0)

	// Launch 10 concurrent requests for the same function version
	// All requests will arrive while the first is still being processed
	numConcurrentRequests := 10
	ncaIds := make([]string, numConcurrentRequests)
	for i := 0; i < numConcurrentRequests; i++ {
		ncaIds[i] = fmt.Sprintf("concurrent_nca_id_%d", i)
	}

	// Use WaitGroup to launch all requests simultaneously
	var wg sync.WaitGroup
	errors := make(chan error, numConcurrentRequests)

	// Launch all requests concurrently - they will all hit the cache at the same time
	for _, ncaId := range ncaIds {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
				NcaId:             id,
				FunctionId:        functionId,
				FunctionVersionId: functionVersionId,
			})
			if err != nil {
				errors <- err
				return
			}
			if resp.Result != pb.RateLimitResult_ALLOW {
				errors <- fmt.Errorf("expected ALLOW, got %v", resp.Result)
			}
		}(ncaId)
	}

	// Wait for all requests to complete
	wg.Wait()
	close(errors)

	// Check for any errors
	for err := range errors {
		assert.NoError(t, err)
	}

	callCount := nvcfServer.GetCallCount()
	t.Logf("Made %d gRPC calls for %d CONCURRENT requests (same function version)", callCount, numConcurrentRequests)

	// Single-flight should deduplicate concurrent requests - only 1 backend call should be made
	assert.Equal(t, 1, callCount, "Single-flight should prevent duplicate concurrent loads, only 1 gRPC call for %d concurrent requests", numConcurrentRequests)

	// Cleanup
	for _, ncaId := range ncaIds {
		_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
	}
}

// testLazyLoadingPerNcaIdLimiters verifies that the loader only creates and caches
// the limiter for the REQUESTED NCA ID, not all NCA IDs in the policy.
// This tests the lazy-loading optimization where we create limiters on-demand.
func testLazyLoadingPerNcaIdLimiters(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	nvcfServer.lazyLoadMultipleNcaIds = true
	defer func() { nvcfServer.lazyLoadMultipleNcaIds = false }()

	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"

	// The mock returns 3 per-NCA-ID configs: lazy_nca_id_1, lazy_nca_id_2, lazy_nca_id_3
	// We'll request only lazy_nca_id_1 first

	// Make a request for lazy_nca_id_1
	resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             "lazy_nca_id_1",
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)

	// Check what's in the Limiters cache
	// Should only have lazy_nca_id_1, NOT lazy_nca_id_2 or lazy_nca_id_3
	perNcaId1Key := ratelimiter.NewPerNcaIdCacheKey("lazy_nca_id_1", functionVersionId)
	perNcaId2Key := ratelimiter.NewPerNcaIdCacheKey("lazy_nca_id_2", functionVersionId)
	perNcaId3Key := ratelimiter.NewPerNcaIdCacheKey("lazy_nca_id_3", functionVersionId)

	// lazy_nca_id_1 should exist in cache
	item1 := rateLimiter.Limiters.Get(perNcaId1Key)
	assert.NotNil(t, item1, "lazy_nca_id_1 limiter should be cached")
	assert.False(t, item1.IsExpired(), "lazy_nca_id_1 limiter should not be expired")

	// lazy_nca_id_2 should NOT exist in cache (lazy loading)
	item2 := rateLimiter.Limiters.Get(perNcaId2Key)
	assert.Nil(t, item2, "lazy_nca_id_2 limiter should NOT be cached (lazy loading)")

	// lazy_nca_id_3 should NOT exist in cache (lazy loading)
	item3 := rateLimiter.Limiters.Get(perNcaId3Key)
	assert.Nil(t, item3, "lazy_nca_id_3 limiter should NOT be cached (lazy loading)")

	t.Log("✓ Verified lazy loading: only requested NCA ID limiter was created")

	// Now make a request for lazy_nca_id_2
	resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             "lazy_nca_id_2",
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)

	// Now lazy_nca_id_2 should exist
	item2 = rateLimiter.Limiters.Get(perNcaId2Key)
	assert.NotNil(t, item2, "lazy_nca_id_2 limiter should now be cached")
	assert.False(t, item2.IsExpired(), "lazy_nca_id_2 limiter should not be expired")

	// But lazy_nca_id_3 still should NOT exist
	item3 = rateLimiter.Limiters.Get(perNcaId3Key)
	assert.Nil(t, item3, "lazy_nca_id_3 limiter should still NOT be cached")

	t.Log("✓ Verified on-demand loading: second NCA ID limiter was created only when requested")

	// Clean up
	_ = rateLimiter.ResetLimiter(ctx, perNcaId1Key, "lazy_nca_id_1", functionVersionId)
	_ = rateLimiter.ResetLimiter(ctx, perNcaId2Key, "lazy_nca_id_2", functionVersionId)
	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), "", functionVersionId)
}

// testTransitionSingleToMultiRate validates transitioning from a single global
// rate ("4-S") to multiple rates ("5-S,10-M"). After the transition the new
// multi-rate limits should be enforced independently.
func testTransitionSingleToMultiRate(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	defer func() { nvcfServer.transitionSingleToMulti = false }()

	ncaId := "test_nca_id_s2m"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"

	// Phase 1: default config is "4-S" (4 per second)
	for i := 0; i < 4; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId: ncaId, FunctionId: functionId, FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "Phase 1: request %d should be allowed (4-S)", i+1)
	}
	resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId: ncaId, FunctionId: functionId, FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "Phase 1: 5th request blocked (4-S)")

	// Phase 2: switch to "5-S,10-M"
	nvcfServer.transitionSingleToMulti = true
	rateLimiter.ClearAllCaches()

	// 5 per second should be allowed
	for i := 0; i < 5; i++ {
		resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId: ncaId, FunctionId: functionId, FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "Phase 2: request %d should be allowed (5-S,10-M)", i+1)
	}
	resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId: ncaId, FunctionId: functionId, FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "Phase 2: 6th request blocked (per-second)")

	// Wait for per-second window to reset, then fill the per-minute remainder.
	// The rejected 6th request also incremented per-minute, so per-minute is
	// at 6/10. Only 4 more fit.
	time.Sleep(1100 * time.Millisecond)

	for i := 0; i < 4; i++ {
		resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId: ncaId, FunctionId: functionId, FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "Phase 2 batch 2: request %d should be allowed", i+1)
	}

	// Per-minute now at 10/10; next should be blocked
	time.Sleep(1100 * time.Millisecond)
	resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId: ncaId, FunctionId: functionId, FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "Phase 2: blocked by per-minute")

	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
}

// testTransitionMultiToSingleRate validates transitioning from multiple rates
// ("5-S,10-M") back to a single rate ("4-S"). The new single rate should be
// enforced cleanly with no leftover state from the multi-rate config.
func testTransitionMultiToSingleRate(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	defer func() {
		nvcfServer.withMultipleGlobalRates = false
		nvcfServer.transitionMultiToSingle = false
	}()

	ncaId := "test_nca_id_m2s"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"

	// Phase 1: multi-rate "5-S,10-M"
	nvcfServer.withMultipleGlobalRates = true

	for i := 0; i < 5; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId: ncaId, FunctionId: functionId, FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "Phase 1: request %d should be allowed (5-S,10-M)", i+1)
	}
	resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId: ncaId, FunctionId: functionId, FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "Phase 1: 6th request blocked (per-second)")

	// Phase 2: switch to single rate "4-S"
	nvcfServer.withMultipleGlobalRates = false
	nvcfServer.transitionMultiToSingle = true
	rateLimiter.ClearAllCaches()

	for i := 0; i < 4; i++ {
		resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId: ncaId, FunctionId: functionId, FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "Phase 2: request %d should be allowed (4-S)", i+1)
	}
	resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId: ncaId, FunctionId: functionId, FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "Phase 2: 5th request blocked (4-S)")

	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
}

// testTransitionMultiToMultiRate validates transitioning from one multi-rate
// config ("5-S,10-M") to a different multi-rate config ("3-S,8-M"). Both
// sets of counters should start fresh after the transition.
func testTransitionMultiToMultiRate(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	defer func() {
		nvcfServer.withMultipleGlobalRates = false
		nvcfServer.transitionMultiToMulti = false
	}()

	ncaId := "test_nca_id_m2m"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"

	// Phase 1: multi-rate "5-S,10-M"
	nvcfServer.withMultipleGlobalRates = true

	for i := 0; i < 5; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId: ncaId, FunctionId: functionId, FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "Phase 1: request %d should be allowed (5-S,10-M)", i+1)
	}
	resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId: ncaId, FunctionId: functionId, FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "Phase 1: 6th request blocked (per-second)")

	// Phase 2: switch to "3-S,8-M"
	nvcfServer.withMultipleGlobalRates = false
	nvcfServer.transitionMultiToMulti = true
	rateLimiter.ClearAllCaches()

	for i := 0; i < 3; i++ {
		resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId: ncaId, FunctionId: functionId, FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "Phase 2: request %d should be allowed (3-S,8-M)", i+1)
	}
	resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId: ncaId, FunctionId: functionId, FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "Phase 2: 4th request blocked (3-S)")

	// Wait for per-second to reset, continue filling per-minute
	time.Sleep(1100 * time.Millisecond)

	for i := 0; i < 3; i++ {
		resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId: ncaId, FunctionId: functionId, FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "Phase 2 batch 2: request %d should be allowed", i+1)
	}

	// Wait for per-second to reset
	time.Sleep(1100 * time.Millisecond)

	// The rejected 4th request also incremented per-minute, so per-minute is
	// at 7/8 (not 6/8). Only 1 more fits.
	for i := 0; i < 1; i++ {
		resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId: ncaId, FunctionId: functionId, FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "Phase 2 batch 3: request %d should be allowed", i+1)
	}
	resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId: ncaId, FunctionId: functionId, FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "Phase 2: blocked by per-minute (8-M)")

	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
}

// This test makes 3 grpc servers and clients to simulate multi instance scenario. Each grpc server has its own Olric store
// Send requests from 3 different clients and validate that the Olric db is working fine across different instances
func testMultiInstances(ctx context.Context, t *testing.T, baseServer *grpc.Server, rateLimiter *ratelimiter.RateLimiter, creds grpc.DialOption) {
	// start 2 extra grpc servers and clients. Making it 3 total
	rateLimiter2, baseServer2, err := startGrpcServer(t, "0.0.0.0:7778", 3321, 3324, []string{"127.0.0.1:3323"})
	if err != nil {
		t.Logf("Failed to start grpc server: %v", err)
		t.Fatal(err)
	}
	defer func() { _ = rateLimiter2.Close() }()
	rateLimiter3, baseServer3, err := startGrpcServer(t, "0.0.0.0:7779", 3322, 3325, []string{"127.0.0.1:3323"})
	if err != nil {
		t.Logf("Failed to start grpc server: %v", err)
		t.Fatal(err)
	}
	defer func() { _ = rateLimiter3.Close() }()

	conn, client, cancel := startGrpcClient(ctx, creds, "localhost:7777", t)
	defer cancel()
	defer conn.Close()

	conn2, client2, cancel2 := startGrpcClient(ctx, creds, "localhost:7778", t)
	defer cancel2()
	defer conn2.Close()

	conn3, client3, cancel3 := startGrpcClient(ctx, creds, "localhost:7779", t)
	defer cancel3()
	defer conn3.Close()

	defer baseServer2.Stop()
	defer baseServer3.Stop()

	ncaId := "test_nca_id"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"

	// send 2 requests to first 2 instance. All 4 should be allowed
	for i := 0; i < 2; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)
	}
	for i := 0; i < 2; i++ {
		resp, err := client2.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)
	}

	// send 1 request to the 3rd instance, over rate limiter rps, the request should fail
	resp, err := client3.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result)

	// Eventually, wait for the rps config to expire. Then all 4 requests should pass/no longer be limited
	time.Sleep(1 * time.Second)
	for i := 0; i < 4; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)
	}

	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
	_ = rateLimiter2.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
	_ = rateLimiter3.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)

	rateLimiter2.ClearAllCaches()
	rateLimiter3.ClearAllCaches()
}

// This test creates 1 extra grpc server and client, making it 2 total.
// Then from either client, it's sending constant load of requests
func testMultiInstanceConstantLoad(ctx context.Context, t *testing.T, baseServer *grpc.Server, rateLimiter *ratelimiter.RateLimiter, creds grpc.DialOption) {
	// start 1 extra grpc servers and clients. Making it 2 total
	rateLimiter2, baseServer2, err := startGrpcServer(t, "0.0.0.0:7778", 3321, 3324, []string{"127.0.0.1:3323"})
	if err != nil {
		t.Logf("Failed to start grpc server: %v", err)
		t.Fatal(err)
	}
	defer func() { _ = rateLimiter2.Close() }()

	conn, client, cancel := startGrpcClient(ctx, creds, "localhost:7777", t)
	defer cancel()
	defer conn.Close()

	conn2, client2, cancel2 := startGrpcClient(ctx, creds, "localhost:7778", t)
	defer cancel2()
	defer conn2.Close()

	defer baseServer2.Stop()

	ncaId := "test_nca_id"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"

	// Wait for the olric cluster to fully form and sync before sending requests.
	// Without this, each node tracks counts independently during the join phase;
	// when they sync, the combined fixed-window count can exceed the limit and cause
	// spurious DISALLOW results even though the overall RPS is below the configured limit.
	time.Sleep(500 * time.Millisecond)

	// Reset any counters accumulated during cluster formation before the timed test begins.
	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
	_ = rateLimiter2.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)

	rps := 3 // we are configured to allow 4 rps so 3 should pass with no issue
	duration := (1000 / time.Duration(rps)) * time.Millisecond
	testDurationSeconds := 10
	iterationCount := rps * testDurationSeconds
	ticker := time.NewTicker(duration)
	defer ticker.Stop()
	rand.Seed(uint64(time.Now().UnixNano()))

	for i := 0; i < iterationCount; i++ {
		<-ticker.C
		var err error
		var resp *pb.RateLimitResponse
		randomInt := rand.Intn(100)
		if randomInt%2 == 0 {
			resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
				NcaId:             ncaId,
				FunctionId:        functionId,
				FunctionVersionId: functionVersionId,
			})
		} else {
			resp, err = client2.RateLimit(ctx, &pb.RateLimitRequest{
				NcaId:             ncaId,
				FunctionId:        functionId,
				FunctionVersionId: functionVersionId,
			})
		}
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)
		t.Log("successful request")
	}

	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
	_ = rateLimiter2.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
	rateLimiter.ClearAllCaches()
	rateLimiter2.ClearAllCaches()
}

// This test creates 1 extra grpc server and client, making it 2 total.
// Then from either client, it's sending constant high load of requests
func testMultiInstanceConstantLoadTooHigh(ctx context.Context, t *testing.T, baseServer *grpc.Server, rateLimiter *ratelimiter.RateLimiter, creds grpc.DialOption) {
	// start 1 extra grpc servers and clients. Making it 2 total
	rateLimiter2, baseServer2, err := startGrpcServer(t, "0.0.0.0:7778", 3321, 3324, []string{"127.0.0.1:3323"})
	if err != nil {
		t.Logf("Failed to start grpc server: %v", err)
		t.Fatal(err)
	}
	defer func() { _ = rateLimiter2.Close() }()

	conn, client, cancel := startGrpcClient(ctx, creds, "localhost:7777", t)
	defer cancel()
	defer conn.Close()

	conn2, client2, cancel2 := startGrpcClient(ctx, creds, "localhost:7778", t)
	defer cancel2()
	defer conn2.Close()

	defer baseServer2.Stop()

	ncaId := "test_nca_id"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"

	rand.Seed(uint64(time.Now().UnixNano()))

	rps := 8
	tickerDuration := (1000 / time.Duration(rps)) * time.Millisecond
	testDurationSeconds := 10
	iterationCount := rps * testDurationSeconds
	ticker := time.NewTicker(tickerDuration)
	defer ticker.Stop()

	success := 0
	failed := 0

	for i := 0; i < iterationCount; i++ {
		<-ticker.C
		randomInt := rand.Intn(100)
		var err error
		var resp *pb.RateLimitResponse
		if randomInt%2 == 0 {
			resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
				NcaId:             ncaId,
				FunctionId:        functionId,
				FunctionVersionId: functionVersionId,
			})
		} else {
			resp, err = client2.RateLimit(ctx, &pb.RateLimitRequest{
				NcaId:             ncaId,
				FunctionId:        functionId,
				FunctionVersionId: functionVersionId,
			})
		}
		require.NoError(t, err)
		require.NotNil(t, resp)
		if resp.Result == pb.RateLimitResult_ALLOW {
			success++
		} else if resp != nil {
			failed++
		}
		t.Logf("successful request: %d, failed request: %d", success, failed)
	}
	// ensure at least 3 rps were successful (we are configured to allow 4 rps)
	assert.Greater(t, success, 3*testDurationSeconds)

	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
	_ = rateLimiter2.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
	rateLimiter.ClearAllCaches()
	rateLimiter2.ClearAllCaches()
}

// This test makes 3 grpc servers and clients to simulate multi instance scenario. After sending requests to 2 instances,
// shut them down. Then send request to 3rd instance, make sure node leaving doesn't cause a problem
func testMultiInstancesScaleDown(ctx context.Context, t *testing.T, baseServer *grpc.Server, rateLimiter *ratelimiter.RateLimiter, creds grpc.DialOption) {

	// start 2 extra grpc servers and clients. Making it 3 total
	rateLimiter2, baseServer2, err := startGrpcServer(t, "0.0.0.0:7778", 3321, 3324, []string{"127.0.0.1:3323"})
	if err != nil {
		t.Logf("Failed to start grpc server: %v", err)
		t.Fatal(err)
	}
	defer func() {
		if rateLimiter2 != nil {
			_ = rateLimiter2.Close()
		}
	}()
	rateLimiter3, baseServer3, err := startGrpcServer(t, "0.0.0.0:7779", 3322, 3325, []string{"127.0.0.1:3323"})
	if err != nil {
		t.Logf("Failed to start grpc server: %v", err)
		t.Fatal(err)
	}
	defer func() {
		if rateLimiter3 != nil {
			_ = rateLimiter3.Close()
		}
	}()

	conn, client, cancel := startGrpcClient(ctx, creds, "localhost:7777", t)
	defer cancel()
	defer conn.Close()

	conn2, client2, cancel2 := startGrpcClient(ctx, creds, "localhost:7778", t)
	defer cancel2()
	defer conn2.Close()

	conn3, client3, cancel3 := startGrpcClient(ctx, creds, "localhost:7779", t)
	defer cancel3()
	defer conn3.Close()

	ncaId := "test_nca_id"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"

	// send 2 requests to 2 instances. Both should be allowed
	resp, err := client2.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)
	resp, err = client3.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)

	// shutdown these 2 new servers
	baseServer2.Stop()
	baseServer3.Stop()
	_ = rateLimiter2.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
	_ = rateLimiter3.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
	rateLimiter2.ClearAllCaches()
	rateLimiter3.ClearAllCaches()
	_ = rateLimiter2.Close()
	rateLimiter2 = nil
	_ = rateLimiter3.Close()
	rateLimiter3 = nil

	// send 1 request to the first instance, the request should still be allowed/no hang
	resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result)

	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
}

// testMultipleGlobalRates tests that multiple comma-separated global rates are
// enforced independently. Config: "5-S,10-M" means 5 per second AND 10 per minute.
// After 5 requests the per-second limit is hit even though per-minute still has room.
// After the per-second window resets, 5 more requests fill the per-minute limit.
func testMultipleGlobalRates(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	nvcfServer.withMultipleGlobalRates = true
	defer func() { nvcfServer.withMultipleGlobalRates = false }()

	ncaId := "test_nca_id_multi_global"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"

	// First 5 requests should all be ALLOW (within per-second limit of 5)
	for i := 0; i < 5; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "request %d should be allowed", i)
	}

	// 6th request should be DISALLOW (per-second limit of 5 reached)
	resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "6th request should be disallowed (per-second limit)")

	// Wait for the per-second window to reset
	time.Sleep(1100 * time.Millisecond)

	// The rejected 6th request also incremented the per-minute counter, so
	// per-minute is at 6/10. Only 4 more fit before per-minute is exhausted.
	for i := 0; i < 4; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "request %d after reset should be allowed", i)
	}

	// Per-minute is now at 10/10. Wait for per-second to reset again.
	time.Sleep(1100 * time.Millisecond)

	// Even though per-second has room, per-minute limit (10) is reached
	resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "should be disallowed (per-minute limit)")

	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
}

// testMultiplePerNcaIdRates tests that multiple rates on per-NCA-ID configs work.
// Config: per-NCA-ID "3-S,8-M" for test_nca_id_multi, global "5-S,10-M".
func testMultiplePerNcaIdRates(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	nvcfServer.withMultiplePerNcaIdRates = true
	defer func() { nvcfServer.withMultiplePerNcaIdRates = false }()

	ncaId := "test_nca_id_multi"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"

	// First 3 requests should be ALLOW (within per-NCA-ID per-second limit of 3)
	for i := 0; i < 3; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "request %d should be allowed", i)
	}

	// 4th request should be DISALLOW (per-NCA-ID per-second limit of 3)
	resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "4th request should be disallowed (per-second)")

	// Wait for per-second window to reset
	time.Sleep(1100 * time.Millisecond)

	// The rejected 4th request also incremented per-minute, so per-minute is
	// now at 4/8 (not 3/8). After 3 more allowed: per-minute = 7/8.
	for i := 0; i < 3; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "request %d second batch should be allowed", i)
	}

	// Wait for per-second window to reset again
	time.Sleep(1100 * time.Millisecond)

	// Per-minute is at 7/8, so only 1 more should be allowed
	for i := 0; i < 1; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "request %d third batch should be allowed", i)
	}

	// 9th total request should be DISALLOW (per-minute limit of 8 reached)
	resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "9th request should be disallowed (per-minute)")

	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewPerNcaIdCacheKey(ncaId, functionVersionId), ncaId, functionVersionId)
}

// testMultiplePerNcaIdFallbackToGlobal verifies that when a policy has both
// per-NCA-ID multi-rates and global multi-rates, a NCA ID without a specific
// per-NCA-ID config falls through to the global multi-rate limits.
// Policy: per-NCA-ID "3-S,8-M" for test_nca_id_multi, global "5-S,10-M".
// This test uses a NCA ID that is NOT test_nca_id_multi, so global applies.
func testMultiplePerNcaIdFallbackToGlobal(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	nvcfServer.withMultiplePerNcaIdRates = true
	defer func() { nvcfServer.withMultiplePerNcaIdRates = false }()

	ncaId := "test_nca_id_no_override"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"

	// Global multi-rate is "5-S,10-M". First 5 requests should be allowed.
	for i := 0; i < 5; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "request %d should be allowed (global 5-S)", i+1)
	}

	// 6th request should be blocked by global per-second limit (5-S)
	resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "6th request blocked (global per-second)")

	// Wait for per-second to reset
	time.Sleep(1100 * time.Millisecond)

	// The rejected 6th request also incremented per-minute, so per-minute is
	// at 6/10 (not 5/10). Only 4 more fit before per-minute is exhausted.
	for i := 0; i < 4; i++ {
		resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "batch 2 request %d should be allowed", i+1)
	}

	// Wait for per-second to reset
	time.Sleep(1100 * time.Millisecond)

	// Per-minute is now at 10/10; next should be blocked by per-minute
	resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "blocked by global per-minute (10-M)")

	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
}

// testPerNcaIdOverrideReplacesGlobalMultiRates verifies that a per-NCA-ID rate
// fully replaces the global multi-rate config for that NCA ID rather than
// merging with it.
// Policy: per-NCA-ID "2-S" for test_nca_id_override_single, global "5-S,4-M".
func testPerNcaIdOverrideReplacesGlobalMultiRates(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	nvcfServer.perNcaIdOverridesGlobalMulti = true
	defer func() { nvcfServer.perNcaIdOverridesGlobalMulti = false }()

	ncaId := "test_nca_id_override_single"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"

	// First 2 requests should be allowed under the per-NCA-ID 2-S limit.
	for i := 0; i < 2; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "request %d should be allowed by per-NCA-ID 2-S", i+1)
	}

	// 3rd request should be blocked by the per-NCA-ID per-second limit.
	resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "3rd request should be blocked by per-NCA-ID 2-S")

	// Wait for the per-second window to reset. If the global 4-M limiter were
	// still merged in, the rejected 3rd request would leave only one more
	// minute-slot available, so the 5th overall request would be blocked.
	time.Sleep(1100 * time.Millisecond)

	for i := 0; i < 2; i++ {
		resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "request %d after reset should still ignore global 4-M", i+4)
	}

	// A 6th overall request in the same second should still be blocked by 2-S,
	// confirming the per-NCA-ID limiter remains the only active limiter set.
	resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "6th request should be blocked by per-NCA-ID 2-S, not global 4-M")

	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewPerNcaIdCacheKey(ncaId, functionVersionId), ncaId, functionVersionId)
	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
}

// testPerNcaIdMultiRatesReplaceGlobalMultiRates verifies that per-NCA-ID
// multi-rates fully replace the global multi-rate config for that NCA ID.
// Policy: per-NCA-ID "3-S,8-M" for test_nca_id_override_multi, global "5-S,4-M".
func testPerNcaIdMultiRatesReplaceGlobalMultiRates(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	nvcfServer.perNcaIdMultiOverridesGlobal = true
	defer func() { nvcfServer.perNcaIdMultiOverridesGlobal = false }()

	ncaId := "test_nca_id_override_multi"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"

	// The per-NCA-ID 3-S limit should apply immediately.
	for i := 0; i < 3; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "request %d should be allowed by per-NCA-ID 3-S", i+1)
	}

	resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "4th request should be blocked by per-NCA-ID 3-S")

	// If the global 4-M were still merged, this next request after the
	// per-second reset would already be blocked because the 4th overall request
	// consumed the last minute-slot.
	time.Sleep(1100 * time.Millisecond)

	for i := 0; i < 3; i++ {
		resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "request %d after reset should still use per-NCA-ID 3-S,8-M", i+5)
	}

	resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "8th request should be blocked by per-NCA-ID 3-S")

	// At this point the per-minute counter is at 8/8. After another per-second
	// reset, the next request should be blocked by the per-NCA-ID 8-M limit.
	time.Sleep(1100 * time.Millisecond)

	resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "9th request should be blocked by per-NCA-ID 8-M, not global 4-M")

	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewPerNcaIdCacheKey(ncaId, functionVersionId), ncaId, functionVersionId)
	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
}

// testMultipleGlobalRatesExemption tests that excluded NCA IDs bypass all rate limits
// even when multiple rates are configured.
func testMultipleGlobalRatesExemption(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	nvcfServer.withMultipleGlobalRates = true
	defer func() { nvcfServer.withMultipleGlobalRates = false }()

	ncaId := "test_nca_id_exclude1"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"

	// Excluded NCA ID should always be allowed regardless of multiple rate limits
	for i := 0; i < 20; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "excluded NCA ID request %d should be allowed", i)
	}

	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
}

// testUnorderedMultipleRates verifies that rate limits work correctly when
// the rates are listed largest-window-first (e.g., "10-M,5-S" instead of
// "5-S,10-M"). The per-second limit should still be enforced even though
// it appears second in the list.
func testUnorderedMultipleRates(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, client pb.RateLimitServiceClient, rateLimiter *ratelimiter.RateLimiter) {
	nvcfServer.withUnorderedMultipleRates = true
	defer func() { nvcfServer.withUnorderedMultipleRates = false }()

	ncaId := "test_nca_id_unordered"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"

	// Config is "10-M,5-S" (per-minute first, per-second second)
	// First 5 requests should all be ALLOW (within per-second limit of 5)
	for i := 0; i < 5; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "request %d should be allowed", i)
	}

	// 6th request should be DISALLOW (per-second limit of 5 reached, even though
	// per-minute at 5/10 still has room and is listed first in "10-M,5-S").
	// The per-minute counter is also incremented for this rejected request.
	resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "6th request should be blocked by per-second limit")

	// Wait for per-second window to reset
	time.Sleep(1100 * time.Millisecond)

	// Per-minute is at 6/10 (rejected request was also counted). Only 4 more
	// requests fit before per-minute is exhausted.
	for i := 0; i < 4; i++ {
		resp, err := client.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId:             ncaId,
			FunctionId:        functionId,
			FunctionVersionId: functionVersionId,
		})
		assert.NoError(t, err)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "request %d second batch should be allowed", i)
	}

	// Wait for per-second to reset
	time.Sleep(1100 * time.Millisecond)

	// Per-minute is now at 10/10. Next request should be blocked.
	resp, err = client.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	})
	assert.NoError(t, err)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "should be blocked by per-minute limit")

	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
}

// testMultiInstanceMultipleRates verifies that multiple rate limits ("5-S,10-M")
// are enforced correctly across 3 Olric-backed instances. Requests are
// distributed across all instances and counters should be shared via Olric.
// The test checks that the per-second limit is enforced across instances, then
// after the per-second window resets, fills up the per-minute limit and
// verifies that it is also enforced across instances.
func testMultiInstanceMultipleRates(ctx context.Context, t *testing.T, nvcfServer *MockNVCFAPIServer, baseServer *grpc.Server, rateLimiter *ratelimiter.RateLimiter, creds grpc.DialOption) {
	nvcfServer.withMultipleGlobalRates = true
	defer func() { nvcfServer.withMultipleGlobalRates = false }()

	rateLimiter2, baseServer2, err := startGrpcServer(t, "0.0.0.0:7778", 3321, 3324, []string{"127.0.0.1:3323"})
	if err != nil {
		t.Logf("Failed to start grpc server: %v", err)
		t.Fatal(err)
	}
	defer func() { _ = rateLimiter2.Close() }()
	rateLimiter3, baseServer3, err := startGrpcServer(t, "0.0.0.0:7779", 3322, 3325, []string{"127.0.0.1:3323"})
	if err != nil {
		t.Logf("Failed to start grpc server: %v", err)
		t.Fatal(err)
	}
	defer func() { _ = rateLimiter3.Close() }()

	conn, client, cancel := startGrpcClient(ctx, creds, "localhost:7777", t)
	defer cancel()
	defer conn.Close()

	conn2, client2, cancel2 := startGrpcClient(ctx, creds, "localhost:7778", t)
	defer cancel2()
	defer conn2.Close()

	conn3, client3, cancel3 := startGrpcClient(ctx, creds, "localhost:7779", t)
	defer cancel3()
	defer conn3.Close()

	defer baseServer2.Stop()
	defer baseServer3.Stop()

	ncaId := "test_nca_id_multi_inst"
	functionId := "test_function_id"
	functionVersionId := "test_function_version_id"

	clients := []pb.RateLimitServiceClient{client, client2, client3}

	// Config is "5-S,10-M". Distribute 5 requests across 3 instances (2+2+1).
	for i := 0; i < 5; i++ {
		c := clients[i%3]
		resp, err := c.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId: ncaId, FunctionId: functionId, FunctionVersionId: functionVersionId,
		})
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "request %d should be allowed (5-S across instances)", i+1)
	}

	// 6th request from any instance should be blocked by per-second
	resp, err := client3.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId: ncaId, FunctionId: functionId, FunctionVersionId: functionVersionId,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "6th request blocked (per-second across instances)")

	// Wait for per-second window to reset
	time.Sleep(1100 * time.Millisecond)

	// The rejected 6th request also incremented per-minute, so per-minute is
	// at 6/10 (not 5/10). Only 4 more fit across instances.
	for i := 0; i < 4; i++ {
		c := clients[i%3]
		resp, err := c.RateLimit(ctx, &pb.RateLimitRequest{
			NcaId: ncaId, FunctionId: functionId, FunctionVersionId: functionVersionId,
		})
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, pb.RateLimitResult_ALLOW, resp.Result, "batch 2 request %d should be allowed", i+1)
	}

	// Wait for per-second window to reset
	time.Sleep(1100 * time.Millisecond)

	// Per-minute now at 10/10. Next request from any instance should be blocked.
	resp, err = client2.RateLimit(ctx, &pb.RateLimitRequest{
		NcaId: ncaId, FunctionId: functionId, FunctionVersionId: functionVersionId,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, pb.RateLimitResult_DISALLOW, resp.Result, "blocked by per-minute across instances")

	_ = rateLimiter.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
	_ = rateLimiter2.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)
	_ = rateLimiter3.ResetLimiter(ctx, ratelimiter.NewGlobalCacheKey(functionVersionId), ncaId, functionVersionId)

	rateLimiter2.ClearAllCaches()
	rateLimiter3.ClearAllCaches()
}
