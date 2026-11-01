// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package authorizers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.uber.org/zap"
)

// MaxJWTSize bounds how much bearer token material the introspection authorizer
// will read before rejecting the request. Self-managed cluster PSATs are well
// under 2 KiB; larger tokens are treated as abuse.
const MaxJWTSize = 2048

// ErrJWTTooLarge is returned when the bearer token exceeds MaxJWTSize.
var ErrJWTTooLarge = errors.New("bearer token exceeds maximum size of 2048 bytes")

const (
	// psatSubjectPrefix matches Kubernetes service-account tokens.
	psatSubjectPrefix = "system:serviceaccount:"
	// expectedPsatSAName is the only ServiceAccount name accepted for PSAT
	// subjects; the namespace is customer-configurable but the SA name is
	// always `nvca`. Subjects of shape `system:serviceaccount:<any-ns>:nvca`
	// pass; any other SA is rejected.
	expectedPsatSAName = "nvca"
	// spiffeSubjectPrefix matches SPIFFE SVID subjects.
	spiffeSubjectPrefix = "spiffe://"
	// spiffeNvcaSegment must be the terminal path segment of an accepted
	// SPIFFE SVID (matched with suffix so trailing-path attacks fail).
	spiffeNvcaSegment = "/nvca"

	icmsInstrumentation = "reval.authorizers.icms"
)

// IsValidNvcaSubject mirrors the upstream NVCA subject validator: PSAT callers
// must be `system:serviceaccount:<any-ns>:nvca`, SPIFFE callers must end with
// `/nvca`. Any other subject is rejected, anchoring identity to the NVCA
// workload rather than any service account that happens to run in the cluster.
func IsValidNvcaSubject(sub string) bool {
	if strings.HasPrefix(sub, psatSubjectPrefix) {
		v := strings.SplitN(sub, ":", 4)
		if len(v) == 4 {
			return v[3] == expectedPsatSAName
		}
		return false
	}
	if strings.HasPrefix(sub, spiffeSubjectPrefix) {
		return strings.HasSuffix(sub, spiffeNvcaSegment)
	}
	return false
}

// IntrospectRequest is the body sent to the introspection endpoint.
type IntrospectRequest struct {
	Token string `json:"token"`
}

// IntrospectResult is the response from a token introspection endpoint (RFC 7662).
//
// ClusterID carries an NVCF-specific resolved cluster identifier (not part of
// RFC 7662). It is optional and propagated to downstream authz context for logs.
type IntrospectResult struct {
	Active    bool   `json:"active"`
	Sub       string `json:"sub"`
	Aud       string `json:"aud"`
	Iss       string `json:"iss"`
	ClusterID string `json:"cluster_id"`
	TokenType string `json:"token_type"`
	Error     string `json:"error,omitempty"`
}

// cacheEntry stores an introspection result (allow or deny) with its expiration wall-clock.
type cacheEntry struct {
	result    *IntrospectResult
	allowed   bool
	expiresAt time.Time
}

// ICMSIntrospect verifies bearer tokens via a remote RFC 7662 introspection
// endpoint. Successful results and invalid-subject denials are cached up to
// CacheTTL (bounded by the JWT exp). active=false is not cached — clock skew
// or an nbf window can make the same token valid seconds later.
//
// On Allow, subject, clusterID, and tokenType are written to AuthzContext.Extra.
type ICMSIntrospect struct {
	introspectURL string
	httpClient    *http.Client
	logger        *zap.Logger
	cacheTTL      time.Duration
	cacheMu       sync.RWMutex
	cache         map[string]cacheEntry
}

// NewICMSIntrospect builds an introspection authorizer. introspectURL is required.
// A cacheTTL of 0 disables caching.
func NewICMSIntrospect(introspectURL string, cacheTTL time.Duration, logger *zap.Logger) (*ICMSIntrospect, error) {
	if strings.TrimSpace(introspectURL) == "" {
		return nil, fmt.Errorf("icms-introspect: introspect URL is required when oidc.enabled=true")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ICMSIntrospect{
		introspectURL: introspectURL,
		httpClient:    &http.Client{Timeout: 10 * time.Second},
		logger:        logger,
		cacheTTL:      cacheTTL,
		cache:         make(map[string]cacheEntry),
	}, nil
}

// Evaluate implements Authorizer.
func (i *ICMSIntrospect) Evaluate(ctx context.Context, ac *AuthzContext) (AuthzResult, error) {
	if ac == nil || ac.Input.BearerToken == "" {
		return AuthzResult{Allow: false}, nil
	}
	token := ac.Input.BearerToken
	if len(token) > MaxJWTSize {
		return AuthzResult{}, ErrJWTTooLarge
	}

	key := cacheKey(token)
	if cached, allowed, ok := i.cacheLookup(key); ok {
		if allowed {
			i.populateExtra(ac, cached)
			return AuthzResult{Allow: true}, nil
		}
		return AuthzResult{Allow: false}, nil
	}

	ctx, span := otel.Tracer(icmsInstrumentation).Start(ctx, "reval.authz.icms.introspect")
	defer span.End()

	result, err := i.callIntrospect(ctx, token)
	if err != nil {
		i.logger.Debug("introspection call failed", zap.Error(err))
		return AuthzResult{Allow: false}, nil
	}
	if !result.Active {
		// Not cached: clock skew / nbf windows can make the same token valid seconds later.
		i.logger.Debug("introspection returned inactive", zap.String("error", result.Error))
		return AuthzResult{Allow: false}, nil
	}
	if !IsValidNvcaSubject(result.Sub) {
		i.logger.Warn("introspection returned active token with non-NVCA subject; rejecting",
			zap.String("subject", result.Sub),
			zap.String("clusterID", result.ClusterID))
		i.cacheStore(key, result, token, false)
		return AuthzResult{Allow: false}, nil
	}

	i.cacheStore(key, result, token, true)
	i.populateExtra(ac, result)
	i.logger.Info("token verified via introspection",
		zap.String("subject", result.Sub),
		zap.String("clusterID", result.ClusterID),
		zap.String("tokenType", result.TokenType))
	return AuthzResult{Allow: true}, nil
}

func (i *ICMSIntrospect) callIntrospect(ctx context.Context, token string) (*IntrospectResult, error) {
	body, err := json.Marshal(IntrospectRequest{Token: token})
	if err != nil {
		return nil, fmt.Errorf("marshal introspect request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.introspectURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build introspect request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := i.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call introspect endpoint: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read introspect response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("introspect returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result IntrospectResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode introspect response: %w", err)
	}
	return &result, nil
}

func (i *ICMSIntrospect) populateExtra(ac *AuthzContext, r *IntrospectResult) {
	if ac.Extra == nil {
		ac.Extra = map[string]any{}
	}
	if r.Sub != "" {
		ac.Extra["subject"] = r.Sub
	}
	if r.ClusterID != "" {
		ac.Extra["clusterID"] = r.ClusterID
	}
	if r.TokenType != "" {
		ac.Extra["tokenType"] = r.TokenType
	}
}

// cacheKey returns a stable, non-reversible key for a token. Hashing keeps raw
// bearer material out of long-lived process memory.
func cacheKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// extractJWTExpiry decodes (without verifying) a JWT payload and returns the
// `exp` claim as an absolute time. Used only as an upper bound on cache TTL —
// the security boundary is the introspection result, not this local parse.
func extractJWTExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0), true
}

func (i *ICMSIntrospect) cacheLookup(key string) (*IntrospectResult, bool, bool) {
	if i.cacheTTL <= 0 {
		return nil, false, false
	}
	i.cacheMu.RLock()
	entry, ok := i.cache[key]
	i.cacheMu.RUnlock()
	if !ok {
		return nil, false, false
	}
	now := time.Now()
	if !now.Before(entry.expiresAt) {
		i.cacheMu.Lock()
		if current, ok := i.cache[key]; ok && !now.Before(current.expiresAt) {
			delete(i.cache, key)
		}
		i.cacheMu.Unlock()
		return nil, false, false
	}
	return entry.result, entry.allowed, true
}

func (i *ICMSIntrospect) cacheStore(key string, result *IntrospectResult, token string, allowed bool) {
	if i.cacheTTL <= 0 {
		return
	}
	ttl := i.cacheTTL
	if exp, ok := extractJWTExpiry(token); ok {
		if remaining := time.Until(exp); remaining < ttl {
			ttl = remaining
		}
	}
	if ttl <= 0 {
		return
	}
	now := time.Now()
	i.cacheMu.Lock()
	for k, e := range i.cache {
		if !now.Before(e.expiresAt) {
			delete(i.cache, k)
		}
	}
	i.cache[key] = cacheEntry{result: result, allowed: allowed, expiresAt: now.Add(ttl)}
	i.cacheMu.Unlock()
}
