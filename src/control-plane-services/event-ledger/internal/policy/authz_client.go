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

package policy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/clients"
	pdpv1 "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/clients/pdp_types"
	nverrors "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/errors"
	"go.opentelemetry.io/otel"
	"go.uber.org/zap"
)

const (
	maxCacheDurationSec              = 24 * 60 * 60
	maxCacheSize                     = 1000
	maxPolicyResponseBodyBytes int64 = 1 << 20
)

// PolicyConfig captures the policy used for authorization evaluation.
type PolicyConfig struct {
	Namespace    string
	PolicyFQDN   string
	SubjectField string
	APIKeyField  string
}

// Authorizer is the narrow contract required by the policy middleware.
type Authorizer interface {
	Evaluate(ctx context.Context, request *pdpv1.RuleRequest) (*pdpv1.RuleResponse, error)
	PolicyConfig() *PolicyConfig
}

// AuthzCacheConfig captures configuration to enable caching during authz checks.
type AuthzCacheConfig struct {
	CacheDuration string `mapstructure:"duration"`
	CacheSize     int    `mapstructure:"size"`
}

// AuthZClient evaluates requests against externally hosted policies.
type AuthZClient struct {
	cfg       *clients.BaseClientConfig
	policyCfg *PolicyConfig
	client    evaluator
	cache     *authzCache
}

type evaluator interface {
	Evaluate(ctx context.Context, request *pdpv1.RuleRequest) (*pdpv1.RuleResponse, error)
}

// AuthZClientOption allows optional client behavior during creation.
type AuthZClientOption func(*AuthZClient) error

// WithCachingEnabled enables a fixed-TTL authorization decision cache.
func WithCachingEnabled(cacheCfg AuthzCacheConfig) AuthZClientOption {
	return func(c *AuthZClient) error {
		return c.initializeCache(cacheCfg)
	}
}

func (c *AuthZClient) initializeCache(cacheCfg AuthzCacheConfig) error {
	duration, err := time.ParseDuration(cacheCfg.CacheDuration)
	if err != nil {
		return &nverrors.ConfigError{
			FieldName: "cache-duration",
			Message:   err.Error(),
		}
	}
	if duration <= 0 || duration.Seconds() > maxCacheDurationSec {
		return &nverrors.ConfigError{
			FieldName: "cache-duration",
			Message:   fmt.Sprintf("invalid - valid-range: 0s-%ds", maxCacheDurationSec),
		}
	}
	if cacheCfg.CacheSize <= 0 || cacheCfg.CacheSize > maxCacheSize {
		return &nverrors.ConfigError{
			FieldName: "cache-size",
			Message:   fmt.Sprintf("invalid - valid-range: 0-%d", maxCacheSize),
		}
	}

	c.cache = newAuthzCache(duration, cacheCfg.CacheSize)
	return nil
}

// PolicyConfig returns the policy used by this client.
func (c *AuthZClient) PolicyConfig() *PolicyConfig {
	return c.policyCfg
}

// Evaluate evaluates the request, using the configured cache when enabled.
func (c *AuthZClient) Evaluate(ctx context.Context, request *pdpv1.RuleRequest) (*pdpv1.RuleResponse, error) {
	if c.cache == nil {
		return c.client.Evaluate(ctx, request)
	}

	reqHash := getRuleRequestHash(request)
	if cachedResp, ok := c.cache.get(reqHash); ok {
		return cachedResp, nil
	}

	resp, err := c.client.Evaluate(ctx, request)
	if err != nil {
		return nil, err
	}
	if resp != nil {
		c.cache.set(reqHash, *resp)
	}

	return resp, nil
}

// NewAuthzClient creates a policy authorization client.
func NewAuthzClient(config *clients.BaseClientConfig, policyCfg *PolicyConfig, opts ...AuthZClientOption) (Authorizer, error) {
	if config == nil {
		return nil, nverrors.ErrBadConfig
	}
	if policyCfg == nil {
		return nil, nverrors.ErrBadConfig
	}

	switch config.Type {
	case string(clients.ClientTypeGRPC):
		zap.L().Error("gRPC client requested")
		return nil, nverrors.ErrBadConfig
	case string(clients.ClientTypeHTTP):
		return newHTTPAuthzClient(config, policyCfg, opts...)
	}
	return nil, nverrors.ErrInvalidConfig
}

func newHTTPAuthzClient(config *clients.BaseClientConfig, policyCfg *PolicyConfig, opts ...AuthZClientOption) (Authorizer, error) {
	httpClient, err := clients.DefaultHTTPClient(&clients.HTTPClientConfig{BaseClientConfig: config}, func(_ string, _ *http.Request) string {
		return "http.authz.policy"
	})
	if err != nil {
		return nil, err
	}

	authzClient := &AuthZClient{
		cfg:       config,
		policyCfg: policyCfg,
		client: &authzHTTPClient{
			client: httpClient,
		},
	}

	for _, opt := range opts {
		if err = opt(authzClient); err != nil {
			return nil, err
		}
	}

	return authzClient, nil
}

func getRuleRequestHash(request *pdpv1.RuleRequest) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(request.String())))
}

func getEvaluationURL(namespace string, policyFQDN string) string {
	return fmt.Sprintf("/v1/namespaces/%s/evaluations/%s", namespace, policyFQDN)
}

type authzHTTPClient struct {
	client *clients.HTTPClient
}

func readPolicyResponseBody(reader io.Reader) ([]byte, error) {
	limitedReader := io.LimitReader(reader, maxPolicyResponseBodyBytes+1)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxPolicyResponseBodyBytes {
		return nil, fmt.Errorf("policy response exceeds %d bytes", maxPolicyResponseBodyBytes)
	}
	return body, nil
}

func (a *authzHTTPClient) Evaluate(ctx context.Context, request *pdpv1.RuleRequest) (*pdpv1.RuleResponse, error) {
	reqBytes, err := json.Marshal(request)
	if err != nil {
		zap.L().Error("input read error", zap.Error(err))
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.client.Config.Addr+getEvaluationURL(request.Namespace, request.RuleName), bytes.NewReader(reqBytes))
	if err != nil {
		zap.L().Error("request create error", zap.Error(err))
		return nil, err
	}

	ctx, span := otel.GetTracerProvider().Tracer("policy").Start(ctx, "evaluate")
	defer span.End()

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")

	resp, err := a.client.Client(ctx).Do(req)
	if err != nil {
		zap.L().Error("evaluate client error", zap.Error(err))
		return nil, err
	}
	defer resp.Body.Close()

	respBytes, err := readPolicyResponseBody(resp.Body)
	if err != nil {
		zap.L().Error("response read error", zap.Error(err), zap.Int("status_code", resp.StatusCode))
		return nil, fmt.Errorf("cannot read response body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		zap.L().Error("invalid response status", zap.Int("status_code", resp.StatusCode))
		return nil, fmt.Errorf("invalid response status: %d", resp.StatusCode)
	}

	ruleResponse := &pdpv1.RuleResponse{}
	if err = json.Unmarshal(respBytes, ruleResponse); err != nil {
		zap.L().Error("response unmarshal error", zap.Error(err))
		return nil, fmt.Errorf("cannot unmarshal response: %w", err)
	}
	return ruleResponse, nil
}

type authzCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	maxSize int
	items   map[string]authzCacheEntry
	order   []string
}

type authzCacheEntry struct {
	response  pdpv1.RuleResponse
	expiresAt time.Time
}

func newAuthzCache(ttl time.Duration, maxSize int) *authzCache {
	return &authzCache{
		ttl:     ttl,
		maxSize: maxSize,
		items:   make(map[string]authzCacheEntry),
		order:   make([]string, 0, maxSize),
	}
}

func (c *authzCache) get(key string) (*pdpv1.RuleResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.items[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expiresAt) {
		c.delete(key)
		return nil, false
	}

	resp := entry.response
	return &resp, true
}

func (c *authzCache) set(key string, response pdpv1.RuleResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.items[key]; !exists {
		c.order = append(c.order, key)
	}
	c.items[key] = authzCacheEntry{
		response:  response,
		expiresAt: time.Now().Add(c.ttl),
	}

	for len(c.items) > c.maxSize && len(c.order) > 0 {
		c.delete(c.order[0])
	}
}

func (c *authzCache) delete(key string) {
	delete(c.items, key)
	for i, existingKey := range c.order {
		if existingKey == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}
