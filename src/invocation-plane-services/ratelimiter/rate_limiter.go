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
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/auth0/go-jwt-middleware/v2/jwks"
	"github.com/auth0/go-jwt-middleware/v2/validator"
	grpcprom "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors"
	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/auth"
	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/recovery"
	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/selector"
	"github.com/jellydator/ttlcache/v3"
	"github.com/olric-data/olric"
	olricConfig "github.com/olric-data/olric/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/ulule/limiter/v3"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	v1reflectiongrpc "google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/grpc/status"

	nvauth "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/auth"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/clients"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/tracing"

	"ratelimiter/credentials"
	nvcfpb "ratelimiter/nvcf/pb"
	"ratelimiter/pb"
)

const (
	defaultLimiterCacheCapacity       = 300
	defaultIndexedPolicyCacheCapacity = 100
)

type FunctionRateLimitConfig struct {
	FunctionId        string              `yaml:"function_id"`
	FunctionVersionId string              `yaml:"function_version_id"`
	Rps               string              `yaml:"rate_per_second"`
	ExcludedNcaIds    map[string]struct{} `yaml:"excluded_nca_ids"`
}

type Config struct {
	// OAuth2Issuer is the expected `iss` claim string on inbound gRPC JWTs.
	OAuth2Issuer string `mapstructure:"OAUTH2_ISSUER"`
	// OAuth2JwksURL is the URL the validator fetches signing keys from. It is
	// set explicitly rather than derived from OAuth2Issuer so the binary can
	// run against any OAuth2/OIDC provider, whether it serves keys at the
	// standard `{issuer}/.well-known/jwks.json` path or at a different one.
	OAuth2JwksURL string `mapstructure:"OAUTH2_JWKS_URL"`
	Audience      string `mapstructure:"AUDIENCE"`
	// OAuth2ProviderHost is the hostname of the OAuth2 authorization server used for client-credentials
	// tokens when calling the NVCF API (see nvkit ProviderConfig.Host).
	OAuth2ProviderHost       string `mapstructure:"OAUTH2_PROVIDER_HOST"`
	OTELExporterOTLPEndpoint string `mapstructure:"OTEL_EXPORTER_OTLP_ENDPOINT"`
	TracingAccessToken       string `mapstructure:"TRACING_ACCESS_TOKEN"`
	SecretsPath              string `mapstructure:"SECRETS_PATH"`
	PodIP                    string `mapstructure:"POD_IP"`
	AWSRegion                string `mapstructure:"AWS_REGION"`
	NvcfApiUrl               string `mapstructure:"NVCF_API_URL"`
	CacheTTL                 int    `mapstructure:"CACHE_TTL"`
	CollectMetrics           bool   `mapstructure:"COLLECT_METRICS"`
}

type CustomClaims struct {
	Scopes []string `json:"scopes"`
}

func (c CustomClaims) Validate(ctx context.Context) error {
	if !slices.Contains(c.Scopes, "ratelimit:check_invocation") {
		return fmt.Errorf("rate limit check invocation")
	}
	return nil
}

type ExcludedNcaIds map[string]struct{}

// IndexedPolicy wraps the rate limit policy + a per-NCA-ID map for O(1) lookups.
type IndexedPolicy struct {
	Policy                *nvcfpb.RateLimitPolicyResponse_RateLimitConfig
	NcaIdToPerNcaIdConfig map[string]*nvcfpb.RateLimitPolicyResponse_RateLimitConfig_PerNcaIdConfigs
}

// CacheKey represents a strongly typed cache key for rate limiter metadata.
// ClientAuthSubject is non-empty only for per-user (caller-tier) entries.
type CacheKey struct {
	ClientAuthSubject string
	NcaId             string
	FunctionVersionId string
}

// String returns the string representation of the cache key
func (ck CacheKey) String() string {
	return ck.ClientAuthSubject + ":" + ck.NcaId + ":" + ck.FunctionVersionId
}

// NewPerNcaIdCacheKey creates a cache key for per nca id rate limiting
func NewPerNcaIdCacheKey(ncaId, functionVersionId string) CacheKey {
	return CacheKey{
		NcaId:             ncaId,
		FunctionVersionId: functionVersionId,
	}
}

// NewGlobalCacheKey creates a cache key for global rate limiting (NcaId is empty)
func NewGlobalCacheKey(functionVersionId string) CacheKey {
	return CacheKey{
		NcaId:             "",
		FunctionVersionId: functionVersionId,
	}
}

// NewPerUserCacheKey creates a cache key for per-user rate limiting.
// NcaId is included to keep counters distinct when the same clientAuthSubject is
// used across multiple NCAs.
func NewPerUserCacheKey(clientAuthSubject, ncaId, functionVersionId string) CacheKey {
	return CacheKey{
		ClientAuthSubject: clientAuthSubject,
		NcaId:             ncaId,
		FunctionVersionId: functionVersionId,
	}
}

// RateLimiterEntry pairs a limiter instance with its rate string for Olric key construction.
type RateLimiterEntry struct {
	Limiter *limiter.Limiter
	Rate    string
}

type LimiterEntry struct {
	Rates          []RateLimiterEntry
	ExcludedNcaIds ExcludedNcaIds
}

// parseRates parses a comma-separated rate string (e.g. "5-S,300-H") into
// individual RateLimiterEntry instances, each with its own limiter. A single rate
// like "4-S" produces a one-element slice for backward compatibility.
// Malformed entries are skipped with a warning; an error is returned only
// when no valid rates remain (e.g. "foo,bar").
// Exact duplicate strings are silently removed. When multiple rates share
// the same time period (e.g. "4-S,5-S"), only the stricter (lower) limit
// is kept and a warning is logged.
func parseRates(store limiter.Store, rateStr string) ([]RateLimiterEntry, error) {
	parts := strings.Split(rateStr, ",")

	type parsed struct {
		entry RateLimiterEntry
		rate  limiter.Rate
	}

	seenStr := make(map[string]struct{}, len(parts))
	candidates := make([]parsed, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, dup := seenStr[part]; dup {
			continue
		}
		seenStr[part] = struct{}{}
		rate, err := limiter.NewRateFromFormatted(part)
		if err != nil {
			zap.L().Warn("Skipping malformed rate entry",
				zap.String("entry", part),
				zap.Error(err))
			continue
		}
		candidates = append(candidates, parsed{
			entry: RateLimiterEntry{
				Limiter: limiter.New(store, rate),
				Rate:    part,
			},
			rate: rate,
		})
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no valid rates found in %q", rateStr)
	}

	// Deduplicate by period, keeping the stricter (lower) limit
	type periodWinner struct {
		index int
		limit int64
	}
	byPeriod := make(map[time.Duration]periodWinner, len(candidates))
	for i, c := range candidates {
		if existing, ok := byPeriod[c.rate.Period]; ok {
			if c.rate.Limit < existing.limit {
				zap.L().Warn("Duplicate period in rate config, keeping stricter limit",
					zap.String("kept", c.entry.Rate),
					zap.String("dropped", candidates[existing.index].entry.Rate))
				byPeriod[c.rate.Period] = periodWinner{index: i, limit: c.rate.Limit}
			} else {
				zap.L().Warn("Duplicate period in rate config, keeping stricter limit",
					zap.String("kept", candidates[existing.index].entry.Rate),
					zap.String("dropped", c.entry.Rate))
			}
		} else {
			byPeriod[c.rate.Period] = periodWinner{index: i, limit: c.rate.Limit}
		}
	}

	entries := make([]RateLimiterEntry, 0, len(byPeriod))
	for _, c := range candidates {
		if w, ok := byPeriod[c.rate.Period]; ok && candidates[w.index].entry.Rate == c.entry.Rate {
			entries = append(entries, c.entry)
		}
	}

	return entries, nil
}

type RateLimiter struct {
	pb.UnimplementedRateLimitServiceServer
	config          Config
	Limiters        *ttlcache.Cache[CacheKey, LimiterEntry]
	indexedPolicies *ttlcache.Cache[string, *IndexedPolicy] // Caches indexed policies by function version ID
	ratelimitClient nvcfpb.RateLimitClient
	db              *olric.Olric
	store           limiter.Store
	errChan         chan error
}

func NewRateLimiter(config Config, olricConfig *olricConfig.Config) (*RateLimiter, error) {
	nvcfApiClient, err := NewNVCFGrpcClient(config)

	if err != nil {
		return nil, err
	}

	// Create a cache to holder limiters related metadata, later during grpc communication time a loader func will be provided
	cacheTTL := 60 * time.Second
	if config.CacheTTL != 0 {
		cacheTTL = time.Duration(config.CacheTTL) * time.Second
	}
	cache := ttlcache.New(
		ttlcache.WithTTL[CacheKey, LimiterEntry](cacheTTL),
		ttlcache.WithCapacity[CacheKey, LimiterEntry](defaultLimiterCacheCapacity),
		ttlcache.WithDisableTouchOnHit[CacheKey, LimiterEntry](),
	)
	go cache.Start()

	// Create a cache for indexed policies
	indexedPoliciesCache := ttlcache.New(
		ttlcache.WithTTL[string, *IndexedPolicy](cacheTTL),
		ttlcache.WithCapacity[string, *IndexedPolicy](defaultIndexedPolicyCacheCapacity),
		ttlcache.WithDisableTouchOnHit[string, *IndexedPolicy](),
	)
	go indexedPoliciesCache.Start()

	// Wrap the NVCF client with caching to reduce redundant gRPC calls
	cachedNVCFClient := NewCachedNVCFClient(nvcfApiClient, cacheTTL)

	stopCaches := func() {
		cache.Stop()
		indexedPoliciesCache.Stop()
	}

	c := make(chan struct{})
	olricConfig.Started = func() {
		close(c)
	}

	// Create a new Olric instance.
	db, err := olric.New(olricConfig)
	if err != nil {
		stopCaches()
		return nil, fmt.Errorf("failed to create olric instance: %w", err)
	}

	errChannel := make(chan error, 1)
	go func() {
		// in background, blocking call
		err := db.Start()
		if err != nil {
			zap.L().Error("olric.Start returned an error", zap.Error(err))
			_ = db.Shutdown(context.Background())
			errChannel <- err
			close(errChannel)
		}
	}()
	select {
	case <-c:
		break
	case err := <-errChannel:
		stopCaches()
		return nil, err
	}

	store, err := NewStore(db.NewEmbeddedClient())
	if err != nil {
		zap.L().Error("Failed to create a Olric store", zap.Error(err))
		stopCaches()
		if shutdownErr := db.Shutdown(context.Background()); shutdownErr != nil {
			zap.L().Error("Failed to shut down olric instance", zap.Error(shutdownErr))
		}
		return nil, err
	}

	return &RateLimiter{
		config:          config,
		Limiters:        cache,
		indexedPolicies: indexedPoliciesCache,
		ratelimitClient: cachedNVCFClient,
		db:              db,
		store:           store,
		errChan:         errChannel,
	}, nil
}

func NewNVCFGrpcClient(config Config) (nvcfpb.RateLimitClient, error) {
	nvcfApiUrl, err := url.Parse(config.NvcfApiUrl)
	if err != nil {
		return nil, fmt.Errorf("error parsing nvcf api url `%s`: %w", config.NvcfApiUrl, err)
	}
	tlsEnabled := nvcfApiUrl.Scheme == "https"

	grpcClientConfig := clients.GRPCClientConfig{BaseClientConfig: &clients.BaseClientConfig{
		Addr: nvcfApiUrl.Host,
		TLS: nvauth.TLSConfigOptions{
			Enabled: tlsEnabled,
		},
	}}

	// Check for fixed bearer token
	tokenKey := "nvcfApiToken"
	if _, err := credentials.ReadTokenFromFile(config.SecretsPath, tokenKey); err == nil {
		// Create bearer token credentials with auto file watcher
		zap.L().Info("Using fixed bearer token authentication for NVCF client",
			zap.String("nvcf_host", nvcfApiUrl.Host),
			zap.Bool("tls_enabled", tlsEnabled))

		// Create bearer token credentials with automatic file watching
		bearerCredentials, err := credentials.NewBearerTokenCredentials(
			config.SecretsPath,
			tokenKey,
			!tlsEnabled,
		)
		if err != nil {
			return nil, err
		}

		// Get standard dial options
		dialOpts, err := grpcClientConfig.DialOptions()
		if err != nil {
			return nil, err
		}

		// Add our credential
		dialOpts = append(dialOpts, grpc.WithPerRPCCredentials(bearerCredentials))
		grpcClientConfig.DialOptOverrides = dialOpts
	} else {
		zap.L().Info("Fixed bearer token not found, falling back to OAuth2")

		// Configure OAuth2
		grpcClientConfig.BaseClientConfig.AuthnCfg = &nvauth.AuthnConfig{
			OIDCConfig: &nvauth.ProviderConfig{
				Host:            config.OAuth2ProviderHost,
				CredentialsFile: config.SecretsPath,
				Scopes:          []string{"ratelimit:check_invocation"},
			},
			RefreshConfig:            &nvauth.RefreshConfig{Interval: int64((5 * time.Minute).Seconds())},
			DisableTransportSecurity: !tlsEnabled,
		}
	}
	conn, err := grpcClientConfig.Dial()
	if err != nil {
		return nil, fmt.Errorf("error connecting to nvcf api service: %w", err)
	}
	return nvcfpb.NewRateLimitClient(conn), nil
}

func (r *RateLimiter) Health() error {
	select {
	case err := <-r.errChan:
		if err != nil {
			return err
		}
		return errors.New("olric db shutdown")
	default:
		return nil
	}
}

// GetStore returns the limiter store for metrics/debugging purposes
func (r *RateLimiter) GetStore() limiter.Store {
	return r.store
}

// ResetLimiter resets all rate limit counters for the given cache key. For testing.
func (r *RateLimiter) ResetLimiter(ctx context.Context, ttlCacheKey CacheKey, ncaId, functionVersionId string) error {
	selectedLimiter := r.Limiters.Get(ttlCacheKey)
	if selectedLimiter == nil {
		return fmt.Errorf("no limiter to be reset")
	}
	limiterEntry := selectedLimiter.Value()
	// Mirror the per-tier Olric key prefix from collectTiers: per-user entries
	// are namespaced with "user:" + clientAuthSubject, so a reset targets the
	// caller's counter rather than (and corrupting) the NCA-tier counter.
	olricPrefix := ncaId
	if ttlCacheKey.ClientAuthSubject != "" {
		olricPrefix = "user:" + ttlCacheKey.ClientAuthSubject + ":" + ncaId
	}
	for _, rateEntry := range limiterEntry.Rates {
		olricKey := olricPrefix + ":" + functionVersionId + ":" + rateEntry.Rate
		_, err := rateEntry.Limiter.Reset(ctx, olricKey)
		if err != nil {
			return fmt.Errorf("failed to reset limiter for rate %s: %w", rateEntry.Rate, err)
		}
	}
	return nil
}

// ClearAllCaches clears all caches: Limiters, indexed policies, and NVCF client's policy cache. Useful for testing.
func (r *RateLimiter) ClearAllCaches() {
	r.Limiters.DeleteAll()
	r.indexedPolicies.DeleteAll()
	// Clear cache if the client supports it (CachedNVCFClient)
	if cachedClient, ok := r.ratelimitClient.(*CachedNVCFClient); ok {
		cachedClient.ClearCache()
	}
}

func (r *RateLimiter) Close() error {
	// Close the client if it supports io.Closer
	if closer, ok := r.ratelimitClient.(io.Closer); ok {
		if err := closer.Close(); err != nil {
			zap.L().Error("Failed to close NVCF client", zap.Error(err))
		}
	}

	// Stop the caches
	r.Limiters.Stop()
	r.indexedPolicies.Stop()

	// Shutdown the Olric database
	err := r.db.Shutdown(context.Background())
	if err != nil {
		return err
	}

	return nil
}

// buildIndexedPolicy builds an IndexedPolicy from a raw gRPC response.
func buildIndexedPolicy(config *nvcfpb.RateLimitPolicyResponse_RateLimitConfig) *IndexedPolicy {
	indexedPolicy := &IndexedPolicy{
		Policy:                config,
		NcaIdToPerNcaIdConfig: make(map[string]*nvcfpb.RateLimitPolicyResponse_RateLimitConfig_PerNcaIdConfigs),
	}

	for _, perNcaConfig := range config.GetPerNcaIdConfigs() {
		if perNcaConfig != nil && perNcaConfig.GetNcaId() != "" {
			indexedPolicy.NcaIdToPerNcaIdConfig[perNcaConfig.GetNcaId()] = perNcaConfig
		}
	}

	return indexedPolicy
}

// fetchIndexedPolicy fetches the policy from NVCF (via cached client) and builds an IndexedPolicy.
// The indexed policy is cached separately to avoid rebuilding the O(N) map on every limiter creation.
// Returns the indexed policy, or nil with no error if NotFound.
// Returns an error if the fetch fails or if a nil policy is returned unexpectedly.
func (r *RateLimiter) fetchIndexedPolicy(ctx context.Context, request *pb.RateLimitRequest) (*IndexedPolicy, error) {
	// Check indexed policy cache first
	cacheKey := request.FunctionId + ":" + request.FunctionVersionId
	if cachedItem := r.indexedPolicies.Get(cacheKey); cachedItem != nil && !cachedItem.IsExpired() {
		return cachedItem.Value(), nil
	}

	// Cache miss - fetch from NVCF (this call is already cached at the gRPC layer)
	req := &nvcfpb.RateLimitPolicyRequest{
		FunctionId:        request.FunctionId,
		FunctionVersionId: request.FunctionVersionId,
	}
	resp, err := r.ratelimitClient.RateLimitPolicy(ctx, req)
	if err != nil {
		grpcStatus, ok := status.FromError(err)
		if ok && grpcStatus.Code() == codes.NotFound {
			zap.L().Warn("No rate limit config found", zap.Error(err))
			return nil, nil
		}
		zap.L().Error("Failed to fetch rate limit config", zap.Error(err))
		return nil, err
	}

	if resp == nil || resp.GetConfig() == nil {
		err := fmt.Errorf("nil rate limit config returned from NVCF")
		zap.L().Warn(err.Error())
		return nil, err
	}

	// Build and cache indexed policy
	indexedPolicy := buildIndexedPolicy(resp.GetConfig())
	r.indexedPolicies.Set(cacheKey, indexedPolicy, ttlcache.DefaultTTL)

	return indexedPolicy, nil
}

// This loader only creates and caches the limiter for the requested NCA ID
// We store these limiters into the ttlCache, and the key is ncaId + functionVersionId.
func (r *RateLimiter) loadPerNcaIdLimiters(ctx context.Context, cacheKey CacheKey, originalRequest *pb.RateLimitRequest) (*ttlcache.Item[CacheKey, LimiterEntry], error) {
	indexedPolicy, err := r.fetchIndexedPolicy(ctx, originalRequest)
	if err != nil {
		return nil, err
	}
	if indexedPolicy == nil || len(indexedPolicy.NcaIdToPerNcaIdConfig) == 0 {
		zap.L().Debug("No per-NCA-ID configs found, no per-NCA-ID limiter will be created",
			zap.String("function id", originalRequest.FunctionId),
			zap.String("function version id", originalRequest.FunctionVersionId))
		return nil, nil
	}

	requestedNcaId := cacheKey.NcaId
	perNcaIdConfig, found := indexedPolicy.NcaIdToPerNcaIdConfig[requestedNcaId]
	if !found {
		// No per nca id config found for this specific NCA ID
		zap.L().Debug("No per nca id config found for requested NCA ID",
			zap.String("nca id", requestedNcaId),
			zap.String("function version id", cacheKey.FunctionVersionId))
		return nil, nil
	}

	// Found the config for this NCA ID - create and cache only this limiter
	perNcaIdRate := perNcaIdConfig.GetRate()
	if perNcaIdRate == "" {
		err := fmt.Errorf("empty per-NCA-ID rate config for nca id %s", requestedNcaId)
		zap.L().Warn(err.Error() + ". Falling back to global rate limiting")
		return nil, err
	}
	rates, err := parseRates(r.store, perNcaIdRate)
	if err != nil {
		zap.L().Error("Failed to parse per nca id rate",
			zap.String("nca id", requestedNcaId),
			zap.Error(err))
		return nil, err
	}

	limiterEntry := LimiterEntry{
		Rates: rates,
	}
	item := r.Limiters.Set(cacheKey, limiterEntry, ttlcache.DefaultTTL)

	zap.L().Debug("Created per nca id limiter",
		zap.String("nca id", requestedNcaId),
		zap.String("function version id", cacheKey.FunctionVersionId),
		zap.String("rate", perNcaIdRate))

	return item, nil
}

// loadPerUserLimiters creates a per-user rate limiter for the requested
// (clientAuthSubject, ncaId, functionVersionId). When perUserRate is configured
// on the policy, the same single rate is enforced individually against every
// unique caller; counters are kept distinct via the clientAuthSubject in the
// cache key and Olric prefix. Returns nil when the policy has no perUserRate
// configured, so downstream code falls through to NCA-tier-only behavior.
func (r *RateLimiter) loadPerUserLimiters(ctx context.Context, cacheKey CacheKey, originalRequest *pb.RateLimitRequest) (*ttlcache.Item[CacheKey, LimiterEntry], error) {
	indexedPolicy, err := r.fetchIndexedPolicy(ctx, originalRequest)
	if err != nil {
		return nil, err
	}
	if indexedPolicy == nil || indexedPolicy.Policy == nil {
		return nil, nil
	}

	perUserRate := indexedPolicy.Policy.GetPerUserRate()
	if perUserRate == "" {
		zap.L().Debug("No per-user rate configured, no per-user limiter will be created",
			zap.String("function id", originalRequest.FunctionId),
			zap.String("function version id", originalRequest.FunctionVersionId))
		return nil, nil
	}

	rates, err := parseRates(r.store, perUserRate)
	if err != nil {
		zap.L().Error("Failed to parse per-user rate",
			zap.String("client auth subject", redactSubject(cacheKey.ClientAuthSubject)),
			zap.Error(err))
		return nil, err
	}

	limiterEntry := LimiterEntry{
		Rates: rates,
	}
	item := r.Limiters.Set(cacheKey, limiterEntry, ttlcache.DefaultTTL)

	zap.L().Debug("Created per-user limiter",
		zap.String("client auth subject", redactSubject(cacheKey.ClientAuthSubject)),
		zap.String("nca id", cacheKey.NcaId),
		zap.String("function version id", cacheKey.FunctionVersionId),
		zap.String("rate", perUserRate))

	return item, nil
}

// loadGlobalLimiters creates global rate limiter based on the cache key.
// Build the "global" rate limiter for this function version.
// We store it into the ttlCache, and the key is functionVersionId.
// The policy is fetched from the cached NVCF client
func (r *RateLimiter) loadGlobalLimiters(ctx context.Context, cacheKey CacheKey, originalRequest *pb.RateLimitRequest) (*ttlcache.Item[CacheKey, LimiterEntry], error) {
	indexedPolicy, err := r.fetchIndexedPolicy(ctx, originalRequest)
	if err != nil {
		return nil, err
	}
	if indexedPolicy == nil || indexedPolicy.Policy == nil {
		zap.L().Debug("No rate limit policy found, no global limiter will be created",
			zap.String("function id", originalRequest.FunctionId),
			zap.String("function version id", originalRequest.FunctionVersionId))
		return nil, nil
	}

	rateLimitPolicy := indexedPolicy.Policy
	if rateLimitPolicy.GetRate() == "" {
		err := fmt.Errorf("empty global rate limit config found when building global rate limiter")
		zap.L().Warn(err.Error() + ". No global rate limiting will be applied")
		return nil, err
	}

	rates, err := parseRates(r.store, rateLimitPolicy.GetRate())
	if err != nil {
		zap.L().Error("Failed to parse rate", zap.Error(err))
		return nil, err
	}

	excludedNcaIds := make(ExcludedNcaIds)
	for _, item := range rateLimitPolicy.GetExcludedNcaIds() {
		excludedNcaIds[item] = struct{}{}
	}
	limiterEntry := LimiterEntry{
		Rates:          rates,
		ExcludedNcaIds: excludedNcaIds,
	}
	item := r.Limiters.Set(cacheKey, limiterEntry, ttlcache.DefaultTTL)
	return item, nil
}

// tierCheck represents one rate-limit tier evaluation. olricPrefix is prepended
// to the per-rate Olric key so counters from different tiers stay in distinct
// namespaces even when they share function-version + rate.
type tierCheck struct {
	name        string
	entry       LimiterEntry
	olricPrefix string
}

// redactSubject hashes clientAuthSubject for logs/traces. The value may be PII
// (e.g. an email-shaped OIDC id), so never emit it raw; the stable hash still
// allows per-caller correlation.
func redactSubject(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])[:16]
}

func (r *RateLimiter) RateLimit(ctx context.Context, request *pb.RateLimitRequest) (*pb.RateLimitResponse, error) {
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(
		attribute.String("nca_id", request.NcaId),
		attribute.String("function_id", request.FunctionId),
		attribute.String("function_version_id", request.FunctionVersionId),
		attribute.String("client_auth_subject", redactSubject(request.ClientAuthSubject)))

	if len(request.NcaId) == 0 ||
		len(request.FunctionId) == 0 ||
		len(request.FunctionVersionId) == 0 {
		return nil, errors.New("invalid request")
	}

	tiers := r.collectTiers(ctx, request)

	isAllowed := true
	if len(tiers) > 0 {
		var err error
		isAllowed, err = r.rateLimit(ctx, tiers, request)
		if err != nil {
			zap.L().Error("Failed to rate limit", zap.Error(err))
			return nil, err
		}
	} else {
		zap.L().Debug("No rate limit metadata was constructed",
			zap.String("nca id", request.NcaId),
			zap.String("function id", request.FunctionId),
			zap.String("version id", request.FunctionVersionId))
	}

	var response *pb.RateLimitResponse
	if isAllowed {
		response = &pb.RateLimitResponse{
			Result: pb.RateLimitResult_ALLOW,
		}
	} else {
		response = &pb.RateLimitResponse{
			Result: pb.RateLimitResult_DISALLOW,
		}
	}
	span.SetAttributes(attribute.Stringer("result", response.Result))
	return response, nil
}

// collectTiers builds the ordered set of tier checks applicable to this request.
// User tier (when ClientAuthSubject set and perUserRate configured) is enforced
// alongside the NCA tier (per-NCA-ID config preferred over global). Both tiers
// must allow for the request to be allowed.
func (r *RateLimiter) collectTiers(ctx context.Context, request *pb.RateLimitRequest) []tierCheck {
	tiers := make([]tierCheck, 0, 2)

	// User tier; only when caller identity is present.
	if request.ClientAuthSubject != "" {
		userKey := NewPerUserCacheKey(request.ClientAuthSubject, request.NcaId, request.FunctionVersionId)
		userLimiters := r.Limiters.Get(userKey, ttlcache.WithLoader(ttlcache.LoaderFunc[CacheKey, LimiterEntry](func(c *ttlcache.Cache[CacheKey, LimiterEntry], k CacheKey) *ttlcache.Item[CacheKey, LimiterEntry] {
			item, err := r.loadPerUserLimiters(ctx, k, request)
			if err != nil {
				zap.L().Error("Failed to load per-user limiter metadata", zap.Error(err))
				return nil
			}
			return item
		})))
		if userLimiters != nil {
			tiers = append(tiers, tierCheck{
				name:        "user",
				entry:       userLimiters.Value(),
				olricPrefix: "user:" + request.ClientAuthSubject + ":" + request.NcaId,
			})
		}
	}

	// NCA tier; per-NCA-ID config takes precedence over global.
	perNcaIdKey := NewPerNcaIdCacheKey(request.NcaId, request.FunctionVersionId)
	perNcaIdLimiters := r.Limiters.Get(perNcaIdKey, ttlcache.WithLoader(ttlcache.LoaderFunc[CacheKey, LimiterEntry](func(c *ttlcache.Cache[CacheKey, LimiterEntry], k CacheKey) *ttlcache.Item[CacheKey, LimiterEntry] {
		item, err := r.loadPerNcaIdLimiters(ctx, k, request)
		if err != nil {
			zap.L().Error("Failed to load per nca id limiter metadata", zap.Error(err))
			return nil
		}
		return item
	})))
	if perNcaIdLimiters != nil {
		tiers = append(tiers, tierCheck{
			name:        "nca",
			entry:       perNcaIdLimiters.Value(),
			olricPrefix: request.NcaId,
		})
		return tiers
	}

	globalKey := NewGlobalCacheKey(request.FunctionVersionId)
	globalLimiters := r.Limiters.Get(globalKey, ttlcache.WithLoader(ttlcache.LoaderFunc[CacheKey, LimiterEntry](func(c *ttlcache.Cache[CacheKey, LimiterEntry], k CacheKey) *ttlcache.Item[CacheKey, LimiterEntry] {
		item, err := r.loadGlobalLimiters(ctx, k, request)
		if err != nil {
			zap.L().Error("Failed to load global limiter metadata", zap.Error(err))
			return nil
		}
		return item
	})))
	if globalLimiters != nil {
		tiers = append(tiers, tierCheck{
			name:        "global",
			entry:       globalLimiters.Value(),
			olricPrefix: request.NcaId,
		})
	}

	return tiers
}

// rateLimit returns allowed only if every tier allows (AND). Tiers run in
// priority order (per-user, then NCA axis); evaluation stops at the first
// denying tier so a blocked request doesn't consume lower tiers' shared budget.
// excludedNcaIds bypasses the global tier.
func (r *RateLimiter) rateLimit(ctx context.Context, tiers []tierCheck, request *pb.RateLimitRequest) (bool, error) {
	allowed := true
	for _, t := range tiers {
		// stop once denied: don't burn lower-priority shared tiers
		if !allowed {
			break
		}
		if _, ok := t.entry.ExcludedNcaIds[request.NcaId]; ok {
			continue
		}
		for _, rateEntry := range t.entry.Rates {
			key := t.olricPrefix + ":" + request.FunctionVersionId + ":" + rateEntry.Rate
			rateLimitContext, err := rateEntry.Limiter.Get(ctx, key)
			if err != nil {
				return false, err
			}
			if rateLimitContext.Reached {
				allowed = false
			}
		}
	}
	return allowed, nil
}

func MakeGrpcServer(rateLimiter *RateLimiter, listener net.Listener, logger logging.Logger) (*grpc.Server, error) {
	rateLimiterConfig := rateLimiter.config
	otelUrl, err := url.Parse(rateLimiterConfig.OTELExporterOTLPEndpoint)
	if err != nil {
		zap.L().Error("Failed to parse otel url", zap.Error(err))
		return nil, err
	}
	issuer, err := url.Parse(rateLimiterConfig.OAuth2Issuer)
	if err != nil {
		zap.L().Error("Failed to parse OAuth2 issuer", zap.Error(err))
		return nil, err
	}
	jwkUrl, err := url.Parse(rateLimiterConfig.OAuth2JwksURL)
	if err != nil {
		return nil, err
	}
	provider := jwks.NewCachingProvider(issuer, 12*time.Hour, jwks.WithCustomJWKSURI(jwkUrl))
	jwtValidator, err := validator.New(
		provider.KeyFunc,
		validator.ES256,
		issuer.String(),
		[]string{rateLimiterConfig.Audience},
		validator.WithCustomClaims(
			func() validator.CustomClaims {
				return &CustomClaims{}
			},
		),
		validator.WithAllowedClockSkew(10*time.Second),
	)
	if err != nil {
		zap.L().Error("Failed to create jwt validator", zap.Error(err))
		return nil, err
	}
	hostName, _ := os.Hostname()
	_, err = tracing.SetupOTELTracer(&tracing.OTELConfig{
		Enabled:     otelUrl.Host != "",
		Endpoint:    otelUrl.Host,
		Insecure:    otelUrl.Scheme == "http",
		AccessToken: rateLimiterConfig.TracingAccessToken,
		Attributes: tracing.Attributes{
			ServiceName:    "nvcf-rate-limiter-service",
			ServiceVersion: getVersion(),
			Extra: map[string]string{
				"host.id": hostName,
				"host.ip": rateLimiterConfig.PodIP,
				"host.dc": rateLimiterConfig.AWSRegion,
			},
		},
	})
	if err != nil {
		zap.L().Error("Failed to set up otel tracer", zap.Error(err))
		return nil, err
	}

	srvMetrics := grpcprom.NewServerMetrics(
		grpcprom.WithServerHandlingTimeHistogram(
			grpcprom.WithHistogramBuckets([]float64{0.001, 0.01, 0.1, 0.3, 0.6, 1, 3, 6, 9, 20, 30, 60, 90, 120}),
		),
	)
	if rateLimiterConfig.CollectMetrics {
		err = prometheus.Register(srvMetrics)
		if err != nil {
			return nil, err
		}
	}
	exemplarFromContext := func(ctx context.Context) prometheus.Labels {
		if span := trace.SpanContextFromContext(ctx); span.IsSampled() {
			return prometheus.Labels{"traceID": span.TraceID().String()}
		}
		return nil
	}
	logTraceID := func(ctx context.Context) logging.Fields {
		if span := trace.SpanContextFromContext(ctx); span.IsSampled() {
			return logging.Fields{"traceID", span.TraceID().String()}
		}
		return nil
	}
	authFn := func(ctx context.Context) (context.Context, error) {
		token, err := auth.AuthFromMD(ctx, "bearer")
		if err != nil {
			zap.L().Error("Failed to get auth token", zap.Error(err))
			return nil, err
		}

		_, err = jwtValidator.ValidateToken(ctx, token)
		if err != nil {
			zap.L().Error("Failed to validate token", zap.Error(err))
			return nil, status.Error(codes.Unauthenticated, err.Error())
		}

		// NOTE: You can also pass the token in the context for further interceptors or gRPC service code.
		return ctx, nil
	}
	allButHealthZ := func(ctx context.Context, callMeta interceptors.CallMeta) bool {
		return healthpb.Health_ServiceDesc.ServiceName != callMeta.Service &&
			v1reflectiongrpc.ServerReflection_ServiceDesc.ServiceName != callMeta.Service
	}
	grpcPanicRecoveryHandler := func(p any) (err error) {
		zap.L().Error("recovered from panic")
		return status.Errorf(codes.Internal, "%s", p)
	}
	baseServer := grpc.NewServer(grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(
			srvMetrics.UnaryServerInterceptor(grpcprom.WithExemplarFromContext(exemplarFromContext)),
			logging.UnaryServerInterceptor(logger, logging.WithFieldsFromContext(logTraceID), logging.WithLogOnEvents(logging.FinishCall)),
			selector.UnaryServerInterceptor(auth.UnaryServerInterceptor(authFn), selector.MatchFunc(allButHealthZ)),
			recovery.UnaryServerInterceptor(recovery.WithRecoveryHandler(grpcPanicRecoveryHandler)),
		), grpc.ChainStreamInterceptor(
			srvMetrics.StreamServerInterceptor(grpcprom.WithExemplarFromContext(exemplarFromContext)),
			logging.StreamServerInterceptor(logger, logging.WithFieldsFromContext(logTraceID), logging.WithLogOnEvents(logging.FinishCall)),
			selector.StreamServerInterceptor(auth.StreamServerInterceptor(authFn), selector.MatchFunc(allButHealthZ)),
			recovery.StreamServerInterceptor(recovery.WithRecoveryHandler(grpcPanicRecoveryHandler)),
		))
	pb.RegisterRateLimitServiceServer(baseServer, rateLimiter)
	healthServer := health.NewServer()
	grpc_health_v1.RegisterHealthServer(baseServer, healthServer)
	reflection.Register(baseServer)
	srvMetrics.InitializeMetrics(baseServer)

	zap.L().Info("starting server", zap.String("address", listener.Addr().String()))
	return baseServer, nil
}
