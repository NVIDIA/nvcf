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
package ratelimit

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/NVIDIA/nvcf-go/pkg/nvkit/auth"
	"github.com/NVIDIA/nvcf-go/pkg/nvkit/clients"
	"github.com/jellydator/ttlcache/v3"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	"nvcf-grpc-proxy/proxy/credentials"
	pbRatelimiter "nvcf-grpc-proxy/ratelimiter/pb"
)

type CacheKey struct {
	NcaId      string
	FunctionId string
	VersionId  string
}

type RateLimitService struct {
	client         pbRatelimiter.RateLimitServiceClient
	RateLimitCache *ttlcache.Cache[CacheKey, struct{}]
}

type NoOpRateLimitService struct{}

func (r NoOpRateLimitService) IsRateLimited(context.Context, string, string, string, bool) bool {
	return false
}

func NewRateLimitClient(ssaFqdn, secretsPath string, rateLimitAddress string) (pbRatelimiter.RateLimitServiceClient, error) {
	rateLimitUrl, err := url.Parse(rateLimitAddress)
	if err != nil {
		return nil, fmt.Errorf("error parsing rate limit url `%s`: %w", rateLimitAddress, err)
	}
	tlsEnabled := rateLimitUrl.Scheme == "https"

	// Create the base client config
	clientConfig := clients.GRPCClientConfig{
		BaseClientConfig: &clients.BaseClientConfig{
			Addr: rateLimitUrl.Host,
			TLS: auth.TLSConfigOptions{
				Enabled: tlsEnabled,
			},
		},
	}

	// Check if we have a bearer token to use
	tokenKey := "ratelimiterToken"
	if _, err := credentials.ReadTokenFromFile(secretsPath, tokenKey); err == nil {
		// Create bearer token credentials with automatic file watching
		zap.L().Info("Using fixed bearer token authentication for rate limit client",
			zap.String("rate_limit_host", rateLimitUrl.Host),
			zap.Bool("tls_enabled", tlsEnabled))

		bearerCredentials, err := credentials.NewBearerTokenCredentials(
			secretsPath,
			tokenKey,
			!tlsEnabled,
		)
		if err != nil {
			zap.L().Info("Error creating bearer token credentials, falling back to OAuth2", zap.Error(err))
		} else {
			// Get standard dial options
			dialOpts, err := clientConfig.DialOptions()
			if err != nil {
				return nil, err
			}

			// Add our credential
			dialOpts = append(dialOpts, grpc.WithPerRPCCredentials(bearerCredentials))
			clientConfig.DialOptOverrides = dialOpts
		}
	} else {
		// Fall back to OAuth2 authentication if no bearer token or token creation failed
		zap.L().Info("Using OAuth2 authentication for rate limit client")
		authnConfig := &auth.AuthnConfig{
			OIDCConfig: &auth.ProviderConfig{
				Host:            ssaFqdn,
				CredentialsFile: secretsPath,
				Scopes:          []string{"ratelimit:check_invocation"},
			},
			RefreshConfig:            &auth.RefreshConfig{Interval: int64((5 * time.Minute).Seconds())},
			DisableTransportSecurity: !tlsEnabled,
		}
		clientConfig.BaseClientConfig.AuthnCfg = authnConfig
	}
	conn, err := clientConfig.Dial()
	if err != nil {
		return nil, fmt.Errorf("error connecting to rate limit service: %w", err)
	}

	rateLimitServiceClient := pbRatelimiter.NewRateLimitServiceClient(conn)
	return rateLimitServiceClient, nil
}

func NewRateLimitService(rateLimitClient pbRatelimiter.RateLimitServiceClient) (*RateLimitService, error) {
	cache := ttlcache.New(
		ttlcache.WithTTL[CacheKey, struct{}](time.Second*60),
		ttlcache.WithDisableTouchOnHit[CacheKey, struct{}](),
	)
	go cache.Start()
	return &RateLimitService{
		client:         rateLimitClient,
		RateLimitCache: cache,
	}, nil
}

func (r *RateLimitService) IsRateLimited(ctx context.Context, ncaId, functionId, functionVersionId string, isSyncCheck bool) bool {
	rateLimitCacheKey := CacheKey{ncaId, functionId, functionVersionId}
	if isSyncCheck || r.RateLimitCache.Get(rateLimitCacheKey) != nil {
		// Either sync rate limiting is enabled or the key is in the cache so this function has been rate limited before
		// and needs to be rechecked synchronously
		rateLimitResult, err := r.checkRateExternal(ctx, ncaId, functionId, functionVersionId)
		if err != nil {
			zap.L().Warn("external rate limit check failed", zap.Error(err),
				zap.String("ncaId", ncaId), zap.String("functionId", functionId), zap.String("functionVersionId", functionVersionId))
			return false
		}
		if rateLimitResult == pbRatelimiter.RateLimitResult_DISALLOW {
			r.RateLimitCache.Set(rateLimitCacheKey, struct{}{}, ttlcache.DefaultTTL)
			return true
		}
		return false
	}
	// The key is not in the cache, so check is asynchronous
	go func() {
		// we expect the parent request to be cancelled if the response is quick, but we still want the rate limit check to go through
		ctx := context.WithoutCancel(ctx)
		rateLimitResult, err := r.checkRateExternal(ctx, ncaId, functionId, functionVersionId)
		if err != nil {
			zap.L().Warn("external rate limit check failed", zap.Error(err),
				zap.String("ncaId", ncaId), zap.String("functionId", functionId), zap.String("functionVersionId", functionVersionId))
			return
		}
		if rateLimitResult == pbRatelimiter.RateLimitResult_DISALLOW {
			r.RateLimitCache.Set(rateLimitCacheKey, struct{}{}, ttlcache.DefaultTTL)
		}
	}()
	return false
}

func (r *RateLimitService) checkRateExternal(ctx context.Context, ncaId, functionId, functionVersionId string) (pbRatelimiter.RateLimitResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()

	rateLimitRequest := &pbRatelimiter.RateLimitRequest{
		NcaId:             ncaId,
		FunctionId:        functionId,
		FunctionVersionId: functionVersionId,
	}
	rateLimitResponse, err := r.client.RateLimit(ctx, rateLimitRequest)
	if err != nil {
		return pbRatelimiter.RateLimitResult_ALLOW, err
	}
	return rateLimitResponse.Result, nil
}

func (r *RateLimitService) Close() error {
	r.RateLimitCache.Stop()
	return nil
}
