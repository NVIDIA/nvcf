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
package ratelimit_test

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf-go/pkg/nvkit/auth"
	"github.com/NVIDIA/nvcf-go/pkg/nvkit/clients"
	"github.com/jellydator/ttlcache/v3"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"

	"nvcf-grpc-proxy/proxy/ratelimit"
	pbRatelimiter "nvcf-grpc-proxy/ratelimiter/pb"
)

type RateLimitMock struct {
	callback func(ctx context.Context, req *pbRatelimiter.RateLimitRequest) (*pbRatelimiter.RateLimitResponse, error)
	pbRatelimiter.UnimplementedRateLimitServiceServer
}

func (mock *RateLimitMock) RateLimit(ctx context.Context, req *pbRatelimiter.RateLimitRequest) (*pbRatelimiter.RateLimitResponse, error) {
	return mock.callback(ctx, req)
}

type RateLimitMockServer struct {
	Address string
	server  *grpc.Server
	lis     net.Listener
}

func NewRateLimitMockServer(callback func(ctx context.Context, req *pbRatelimiter.RateLimitRequest) (*pbRatelimiter.RateLimitResponse, error)) (*RateLimitMockServer, error) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("failed to listen: %w", err)
	}

	server := grpc.NewServer()
	mock := &RateLimitMock{callback: callback}
	pbRatelimiter.RegisterRateLimitServiceServer(server, mock)
	reflection.Register(server)

	return &RateLimitMockServer{
		Address: lis.Addr().String(),
		server:  server,
		lis:     lis,
	}, nil
}

func (s *RateLimitMockServer) Start() {
	go func() {
		if err := s.server.Serve(s.lis); err != nil {
			log.Fatalf("failed to serve: %v", err)
		}
	}()
	log.Println("rate limit mock server started at", s.Address)
}

func (s *RateLimitMockServer) Stop() {
	s.server.Stop()
}

func NewRateLimitClientNoAuth(rateLimitAddress string) (pbRatelimiter.RateLimitServiceClient, error) {
	rateLimitUrl, err := url.Parse(rateLimitAddress)
	if err != nil {
		zap.L().Error("Error parsing rate limit url", zap.String("url", rateLimitAddress))
		return nil, err
	}

	grpcClientConfig := clients.GRPCClientConfig{BaseClientConfig: &clients.BaseClientConfig{
		Addr: rateLimitUrl.Host,
		TLS: auth.TLSConfigOptions{
			Enabled: false,
		},
	}}

	conn, err := grpcClientConfig.Dial()
	if err != nil {
		zap.L().Error("Error connecting to rate limit service", zap.Error(err))
		return nil, err
	}

	rateLimitServiceClient := pbRatelimiter.NewRateLimitServiceClient(conn)
	return rateLimitServiceClient, nil
}

func TestRateLimitService_IsRateLimited(t *testing.T) {
	rateLimitStatus := atomic.NewPointer(lo.ToPtr(pbRatelimiter.RateLimitResult_ALLOW))
	callback := func(ctx context.Context, req *pbRatelimiter.RateLimitRequest) (*pbRatelimiter.RateLimitResponse, error) {
		return &pbRatelimiter.RateLimitResponse{Result: *rateLimitStatus.Load()}, nil
	}

	server, err := NewRateLimitMockServer(callback)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	server.Start()
	defer server.Stop()

	conn, err := grpc.NewClient(server.Address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("failed to connect to server: %v", err)
	}
	defer conn.Close()

	u := url.URL{Scheme: "http", Host: server.Address}
	client, err := NewRateLimitClientNoAuth(u.String())

	if err != nil {
		t.Fatalf("failed to create new rate limit client: %v", err)
	}
	rateLimitService, err := ratelimit.NewRateLimitService(client)
	if err != nil {
		t.Fatalf("failed to create rate limit service: %v", err)
	}

	ctx := context.Background()
	ncaId := "test-nca-id"
	functionId := "test-function-id"
	functionVersionId := "test-function-version-id"

	t.Run("rate limit async cache miss and external check allow", func(t *testing.T) {
		result := rateLimitService.IsRateLimited(ctx, ncaId, functionId, functionVersionId, false)
		rateLimitService.RateLimitCache.DeleteAll()
		assert.False(t, result)
	})

	t.Run("rate limit async cache miss and external check disallow", func(t *testing.T) {
		result := rateLimitService.IsRateLimited(ctx, ncaId, functionId, functionVersionId, false)
		rateLimitService.RateLimitCache.DeleteAll()
		assert.False(t, result) // allow, then add to cache
	})

	t.Run("rate limit sync cache miss and external check disallow", func(t *testing.T) {
		rateLimitStatus.Store(lo.ToPtr(pbRatelimiter.RateLimitResult_DISALLOW))

		result := rateLimitService.IsRateLimited(ctx, ncaId, functionId, functionVersionId, true)
		rateLimitService.RateLimitCache.DeleteAll()
		assert.True(t, result)
	})

	t.Run("rate limit async cache hit and external check disallow", func(t *testing.T) {
		rateLimitStatus.Store(lo.ToPtr(pbRatelimiter.RateLimitResult_DISALLOW))
		rateLimitCacheKey := ratelimit.CacheKey{ncaId, functionId, functionVersionId}
		rateLimitService.RateLimitCache.Set(rateLimitCacheKey, struct{}{}, ttlcache.DefaultTTL)

		result := rateLimitService.IsRateLimited(ctx, ncaId, functionId, functionVersionId, false)
		rateLimitService.RateLimitCache.DeleteAll()
		assert.True(t, result)
	})

	t.Run("rate limit async cache miss 2x and external check disallow", func(t *testing.T) {
		rateLimitStatus.Store(lo.ToPtr(pbRatelimiter.RateLimitResult_DISALLOW))

		// The first call will be async and should return false
		result := rateLimitService.IsRateLimited(ctx, ncaId, functionId, functionVersionId, false)
		assert.False(t, result)

		// Wait for the async check to complete and then check again; should be true
		time.Sleep(100 * time.Millisecond)
		result = rateLimitService.IsRateLimited(ctx, ncaId, functionId, functionVersionId, false)
		rateLimitService.RateLimitCache.DeleteAll()
		assert.True(t, result)

	})

	t.Run("rate limit sync cache miss and external check disallow", func(t *testing.T) {
		rateLimitStatus.Store(lo.ToPtr(pbRatelimiter.RateLimitResult_DISALLOW))

		result := rateLimitService.IsRateLimited(ctx, ncaId, functionId, functionVersionId, true)
		assert.True(t, result)
	})
}

func TestNoOpRateLimitService_IsRateLimited(t *testing.T) {
	noOpService := &ratelimit.NoOpRateLimitService{}

	ctx := context.Background()
	ncaId := "test-nca-id"
	functionId := "test-function-id"
	functionVersionId := "test-function-version-id"

	t.Run("no-op rate limit async", func(t *testing.T) {
		result := noOpService.IsRateLimited(ctx, ncaId, functionId, functionVersionId, false)
		assert.False(t, result)
	})

	t.Run("no-op rate limit sync", func(t *testing.T) {
		result := noOpService.IsRateLimited(ctx, ncaId, functionId, functionVersionId, true)
		assert.False(t, result)
	})
}

func TestRateLimitMockServer(t *testing.T) {
	var rateLimitStatus pbRatelimiter.RateLimitResult = pbRatelimiter.RateLimitResult_ALLOW
	var mu sync.Mutex
	statusChangeCh := make(chan struct{})

	callback := func(ctx context.Context, req *pbRatelimiter.RateLimitRequest) (*pbRatelimiter.RateLimitResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		return &pbRatelimiter.RateLimitResponse{Result: rateLimitStatus}, nil
	}

	server, err := NewRateLimitMockServer(callback)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	server.Start()
	defer server.Stop()

	conn, err := grpc.NewClient(server.Address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("failed to connect to server: %v", err)
	}
	defer conn.Close()

	client := pbRatelimiter.NewRateLimitServiceClient(conn)

	makeRequest := func() (*pbRatelimiter.RateLimitResponse, error) {
		req := &pbRatelimiter.RateLimitRequest{
			NcaId:             "test-nca-id",
			FunctionId:        "test-function-id",
			FunctionVersionId: "test-function-version-id",
		}
		return client.RateLimit(context.Background(), req)
	}

	res, err := makeRequest()
	if err != nil {
		t.Fatalf("RateLimit call failed: %v", err)
	}
	assert.Equal(t, pbRatelimiter.RateLimitResult_ALLOW, res.Result)

	go func() {
		<-statusChangeCh
		mu.Lock()
		rateLimitStatus = pbRatelimiter.RateLimitResult_DISALLOW
		mu.Unlock()
	}()

	res, err = makeRequest()
	if err != nil {
		t.Fatalf("RateLimit call failed: %v", err)
	}
	assert.Equal(t, pbRatelimiter.RateLimitResult_ALLOW, res.Result)

	close(statusChangeCh)
	time.Sleep(100 * time.Millisecond)

	res, err = makeRequest()
	if err != nil {
		t.Fatalf("RateLimit call failed: %v", err)
	}
	assert.Equal(t, pbRatelimiter.RateLimitResult_DISALLOW, res.Result)

	mu.Lock()
	rateLimitStatus = pbRatelimiter.RateLimitResult_ALLOW
	mu.Unlock()

	res, err = makeRequest()
	if err != nil {
		t.Fatalf("RateLimit call failed: %v", err)
	}
	assert.Equal(t, pbRatelimiter.RateLimitResult_ALLOW, res.Result)

	mu.Lock()
	rateLimitStatus = pbRatelimiter.RateLimitResult_DISALLOW
	mu.Unlock()

	res, err = makeRequest()
	if err != nil {
		t.Fatalf("RateLimit call failed: %v", err)
	}
	assert.Equal(t, pbRatelimiter.RateLimitResult_DISALLOW, res.Result)
}
