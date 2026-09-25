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

package middleware

// This middleware is a reimplementation of jwt.go from the nvkit framework:
// JWT parsing helpers for bearer-token authentication.
// It has been modified to be used with gorilla/mux instead of go-kit.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/logging"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/mux"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.uber.org/zap"

	api_error "github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/cmd/api/error"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
)

type contextKey string

const (
	claimsContextKey       contextKey = "claims"
	tenantClaimsContextKey contextKey = "tenant_claims"

	ErrFetchingJwk             = "failed to fetch jwk"
	ErrInsufficientPermissions = "insufficient permissions"
	ErrInvalidAuthFormat       = "invalid authorization format"
	ErrInvalidToken            = "invalid token"
	ErrMissingAuthHeader       = "missing authorization header"
	ErrMissingClaims           = "missing or invalid claims in context"
	ErrMissingJwk              = "jwk key not found"
	ErrMissingJWKSURL          = "missing jwks url"
	ErrMissingKid              = "missing kid header in jwt token"
	ErrRefreshJWKSUrl          = "failed to refresh jwks url"
	ErrRegisterJWKSUrl         = "failed to register jwks url"
)

type checkStatusResult struct {
	check string
	err   error
}

// runCheck performs a single health check against a URL
// Designed to be run in a goroutine
func runCheck(
	ctx context.Context,
	ctxLogger *logging.TraceLogger,
	resultsQueue chan<- checkStatusResult,
	check string,
	url string,
	httpConfig *config.HTTPClientConfig,
) {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Avoids crashing the entire application if a panic occurs
	defer func() {
		if r := recover(); r != nil {
			ctxLogger.ErrorContext(checkCtx,
				"panic during check",
				zap.String("check", check),
				zap.String("url", url),
				zap.Any("error", r))
			resultsQueue <- checkStatusResult{
				check: check,
				err:   fmt.Errorf("panic during check '%s': %v", check, r),
			}
		}
	}()

	client := GetSharedHTTPClient(httpConfig)

	req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, url, nil)
	if err != nil {
		ctxLogger.ErrorContext(checkCtx,
			"failed to create status check request",
			zap.Error(err),
			zap.String("check", check),
			zap.String("url", url))
		resultsQueue <- checkStatusResult{check: check, err: err}
		return
	}

	resp, err := client.Do(req)
	if err != nil {
		ctxLogger.ErrorContext(checkCtx,
			"failed to do status check request",
			zap.Error(err),
			zap.String("check", check),
			zap.String("url", url))
		resultsQueue <- checkStatusResult{check: check, err: err}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("received %d response", resp.StatusCode)
		ctxLogger.ErrorContext(checkCtx,
			"status check failed",
			zap.Int("status_code", resp.StatusCode),
			zap.String("check", check),
			zap.String("url", url))
		resultsQueue <- checkStatusResult{check: check, err: err}
		return
	} else {
		ctxLogger.InfoContext(checkCtx,
			"status check passed",
			zap.Int("status_code", resp.StatusCode),
			zap.String("check", check),
			zap.String("url", url))
	}

	resultsQueue <- checkStatusResult{check: check, err: nil}
}

func authCheckURLs(authConfig config.AuthConfig) map[string]string {
	if !authConfig.Enabled {
		return map[string]string{}
	}

	checks := make(map[string]string)
	if authConfig.JWKSetUrl != "" {
		checks["jwt_jwks"] = authConfig.JWKSetUrl
	}
	if authConfig.Provider == "policy" {
		if authConfig.Policy.TokenIssuerAddr != "" {
			checks["policy_token_issuer"] = authConfig.Policy.TokenIssuerAddr
		}
		if authConfig.Policy.PolicyEvaluatorAddr != "" {
			checks["policy_evaluator"] = authConfig.Policy.PolicyEvaluatorAddr
		}
	}
	return checks
}

// CheckStatus checks the availability of authentication endpoints
func CheckStatus(ctx context.Context, ctxLogger *logging.TraceLogger, httpConfig *config.HTTPClientConfig, authConfig config.AuthConfig) map[string]error {
	// Tracing handled by external library
	traceCtx := ctx

	checkURLs := authCheckURLs(authConfig)
	totalChecks := len(checkURLs)
	results := make(map[string]error, totalChecks)
	if totalChecks == 0 {
		return results
	}
	resultsQueue := make(chan checkStatusResult, totalChecks)

	for check, url := range checkURLs {
		go runCheck(traceCtx, ctxLogger, resultsQueue, check, url, httpConfig)
	}

	checkTimeout := time.NewTimer(6 * time.Second) // overall timeout longer than individual timeouts
	defer checkTimeout.Stop()

	// Collect results with timeout handling
	completedChecks := 0
	for completedChecks < totalChecks {
		select {
		case result := <-resultsQueue:
			results[result.check] = result.err
			completedChecks++

		case <-checkTimeout.C:
			// Handle timeout by adding errors for any incomplete checks
			ctxLogger.ErrorContext(traceCtx,
				"overall status check timed out",
				zap.Int("completed_checks", completedChecks),
				zap.Int("total_checks", totalChecks))

			for check := range checkURLs {
				if _, ok := results[check]; !ok {
					results[check] = fmt.Errorf("status check timed out")
					ctxLogger.ErrorContext(traceCtx,
						"individual status check timed out",
						zap.String("check", check),
						zap.String("url", checkURLs[check]))
				}
			}

			// Break out of the loop
			completedChecks = totalChecks
		}
	}

	// Do not close resultsQueue. Timed-out checks may still send their result,
	// and the channel buffer has capacity for every check.
	return results
}

type JWTParserOptions struct {
	JwksURL                 string
	Method                  jwt.SigningMethod
	Claims                  jwt.Claims
	Issuer                  string
	Audience                string
	TenantClaim             string
	RequireExpiration       bool
	JwkCacheRefreshInterval time.Duration
	HTTPConfig              *config.HTTPClientConfig
}

func NewJWTParserOptions(jwksURL string, method jwt.SigningMethod, jwkCacheRefreshInterval time.Duration, httpConfig *config.HTTPClientConfig) JWTParserOptions {
	return JWTParserOptions{
		JwksURL:                 jwksURL,
		Method:                  method,
		JwkCacheRefreshInterval: jwkCacheRefreshInterval,
		HTTPConfig:              httpConfig,
	}
}

// MaybeRequireScopes This middleware can be chained with any http.Handler in order to place a scope(s) requirement
// on the request path. If auth is disabled, it will simply pass through the request.
// If requireAllScopes is true, all scopes must be present in the JWT token.
// If requireAllScopes is false, at least one of the scopes must be present in the JWT token.
func MaybeRequireScopes(logger *otelzap.Logger, authEnabled bool, requiredScopes Scopes, scopeRequirement ScopeRequirement) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !authEnabled {
			return next
		}
		return requireScopes(requiredScopes, scopeRequirement, false)(next)
	}
}

// MaybeRequireScopesAllowNVCA is MaybeRequireScopes, but also trusts an
// SIS-introspected NVCA identity for write routes. Use only where the
// handler also binds the identity to a specific cluster (see
// bindNVCAClusterID in cmd/api/service/v3.go).
func MaybeRequireScopesAllowNVCA(logger *otelzap.Logger, authEnabled bool, requiredScopes Scopes, scopeRequirement ScopeRequirement) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !authEnabled {
			return next
		}
		return requireScopes(requiredScopes, scopeRequirement, true)(next)
	}
}

// Extract string scopes from claims
func getScopesFromClaims(claims jwt.MapClaims) ([]string, bool) {
	scopesRaw, ok := claims["scopes"]
	if !ok {
		return nil, false
	}

	// Try to cast directly to []interface{}
	scopesArray, ok := scopesRaw.([]interface{})
	if !ok {
		return nil, false
	}

	// Convert each interface{} value to string
	result := make([]string, 0, len(scopesArray))
	for _, scope := range scopesArray {
		switch s := scope.(type) {
		case string:
			// Direct string
			result = append(result, s)
		case fmt.Stringer:
			// Types that implement String() method
			result = append(result, s.String())
		case map[string]interface{}:
			// If it's a map with a data field
			if data, ok := s["data"].(string); ok {
				result = append(result, data)
			}
		default:
			// Try using fmt.Sprintf as a fallback
			str := fmt.Sprintf("%v", scope)
			if str != "" {
				result = append(result, str)
			}
		}
	}

	return result, len(result) > 0
}

func requireScopes(requiredScopes Scopes, scopeRequirement ScopeRequirement, allowNVCAIdentity bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			parentCtx := r.Context()
			// Tracing handled by external library
			traceCtx := parentCtx

			logger := logging.GetLogger(traceCtx)
			errType := "Required Scopes Error"

			claims, ok := r.Context().Value(claimsContextKey).(jwt.MapClaims)
			if !ok {
				// API keys carry no scopes.
				if isPDPAuthorized(parentCtx) {
					next.ServeHTTP(w, r)
					return
				}
				if _, nvcaOK := NVCAIdentityFromContext(parentCtx); nvcaOK {
					// NVCA's PSAT carries no scopes; only trust it on routes
					// that opt in and bind it to a specific cluster (see
					// bindNVCAClusterID in cmd/api/service/v3.go).
					if allowNVCAIdentity && requiredScopes == WriteScopes {
						next.ServeHTTP(w, r)
						return
					}
					logger.WarnContext(traceCtx, ErrInsufficientPermissions)
					status := http.StatusForbidden
					api_error.GenerateErrorResponse(traceCtx, errType, "Forbidden", r.URL.Path, status, errors.New(ErrInsufficientPermissions), w)
					logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
					return
				}
				logger.WarnContext(traceCtx, ErrMissingClaims)
				status := http.StatusUnauthorized
				// http.Error(w, ErrMissingClaims, status)
				api_error.GenerateErrorResponse(traceCtx, errType, "Unauthorized", r.URL.Path, status, errors.New(ErrMissingClaims), w)
				logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
				return
			}

			scopesList, ok := getScopesFromClaims(claims)
			if scopeRequirement == RequireAllScopes {
				if !ok || !hasAllRequiredScopes(scopesList, requiredScopes) {
					logger.WarnContext(traceCtx, ErrInsufficientPermissions)
					status := http.StatusForbidden
					// http.Error(w, ErrInsufficientPermissions, status)
					api_error.GenerateErrorResponse(traceCtx, errType, "Forbidden", r.URL.Path, status, errors.New(ErrInsufficientPermissions), w)
					logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
					return
				}
			} else {
				if !ok || !hasAnyRequiredScope(scopesList, requiredScopes) {
					logger.WarnContext(traceCtx, ErrInsufficientPermissions)
					status := http.StatusForbidden
					// http.Error(w, ErrInsufficientPermissions, status)
					api_error.GenerateErrorResponse(traceCtx, errType, "Forbidden", r.URL.Path, status, errors.New(ErrInsufficientPermissions), w)
					logging.LogHTTPResponse(traceCtx, logger, status, w.Header())
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func hasAllRequiredScopes(scopesList []string, requiredScopes Scopes) bool {
	requiredScopesList := strings.Fields(string(requiredScopes))

	for _, required := range requiredScopesList {
		found := false
		for _, scope := range scopesList {
			// Trim any potential whitespace and compare
			if strings.TrimSpace(scope) == strings.TrimSpace(required) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func hasAnyRequiredScope(scopesList []string, requiredScopes Scopes) bool {
	requiredScopesList := strings.Fields(string(requiredScopes))
	for _, required := range requiredScopesList {
		for _, scope := range scopesList {
			if strings.TrimSpace(scope) == strings.TrimSpace(required) {
				return true
			}
		}
	}
	return false
}

func NewParseJWTMiddleware(opts JWTParserOptions, jwkCache *jwk.Cache) mux.MiddlewareFunc {
	return newParseJWTMiddleware(opts, jwkCache)
}

// processJWTToken encapsulates all the work related to parsing and validating a JWT token
func parseJWTWithOptions(tokenString string, claims jwt.MapClaims, keyFunc jwt.Keyfunc, opts JWTParserOptions) (*jwt.Token, error) {
	if opts.Method == nil {
		opts.Method = jwt.SigningMethodES256
	}
	parserOptions := []jwt.ParserOption{jwt.WithValidMethods([]string{opts.Method.Alg()})}
	if opts.RequireExpiration {
		parserOptions = append(parserOptions, jwt.WithExpirationRequired())
	}
	if opts.Issuer != "" {
		parserOptions = append(parserOptions, jwt.WithIssuer(opts.Issuer))
	}
	if opts.Audience != "" {
		parserOptions = append(parserOptions, jwt.WithAudience(opts.Audience))
	}
	return jwt.ParseWithClaims(
		tokenString,
		claims,
		keyFunc,
		parserOptions...,
	)
}

func tenantValuesFromClaim(value interface{}) []string {
	switch values := value.(type) {
	case string:
		if values != "" {
			return []string{values}
		}
	case []string:
		return values
	case []interface{}:
		result := make([]string, 0, len(values))
		for _, value := range values {
			if tenant, ok := value.(string); ok && tenant != "" {
				result = append(result, tenant)
			}
		}
		return result
	}
	return nil
}

// IsTenantAuthorized reports whether a JWT-authenticated request may access a
// tenant. Other authentication providers continue through their own policy.
func IsTenantAuthorized(ctx context.Context, tenant string) bool {
	authorizedTenants, ok := ctx.Value(tenantClaimsContextKey).([]string)
	if !ok {
		return true
	}
	for _, authorizedTenant := range authorizedTenants {
		if authorizedTenant == tenant {
			return true
		}
	}
	return false
}

// MaybeRequirePathTenant binds account and namespace route variables to the
// tenant claim for the local JWT provider.
func MaybeRequirePathTenant(enabled bool) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		if !enabled {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			vars := mux.Vars(r)
			for _, key := range []string{"ncaId", "namespace"} {
				if tenant := vars[key]; tenant != "" && !IsTenantAuthorized(r.Context(), tenant) {
					http.Error(w, ErrInsufficientPermissions, http.StatusForbidden)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// processJWTToken parses and validates a JWT from the request's Authorization
// header. When writeResponse is false, it returns the error without writing
// an HTTP response, so a caller can fall back to another verification path
// (e.g. SIS introspection) before deciding what to send the client.
func processJWTToken(opts JWTParserOptions, jwkCache *jwk.Cache, w http.ResponseWriter, r *http.Request, writeResponse bool) (context.Context, error) {
	ctx := r.Context()
	// Safe guard against nil context
	if ctx == nil {
		ctx = context.Background()
	}

	// Tracing handled by external library
	traceCtx := ctx

	ctxLogger := logging.GetLogger(traceCtx)
	if ctxLogger == nil {
		// Can't log this, but we can avoid a nil pointer panic
		return nil, fmt.Errorf("nil logger provided to processJWTToken")
	}

	errType := "Process JWT Token Error"

	respondUnauthorized := func(err error) {
		if !writeResponse {
			return
		}
		api_error.GenerateErrorResponse(traceCtx, errType, "Unauthorized", r.URL.Path, http.StatusUnauthorized, err, w)
		logging.LogHTTPResponse(traceCtx, ctxLogger, http.StatusUnauthorized, w.Header())
	}

	// writeResponse=false means a caller (the PSAT fallback path) has another
	// verification method to try before treating this as a real failure, so
	// log quietly here and let that caller log once both paths are done.
	logFailure := ctxLogger.WarnContext
	if !writeResponse {
		logFailure = ctxLogger.DebugContext
	}

	if opts.JwksURL == "" {
		logFailure(traceCtx, ErrMissingJWKSURL)
		err := errors.New(ErrMissingJWKSURL)
		respondUnauthorized(err)
		return nil, err
	}

	// Get the token from the Authorization header
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		logFailure(traceCtx, ErrMissingAuthHeader)
		err := errors.New(ErrMissingAuthHeader)
		respondUnauthorized(err)
		return nil, err
	}

	// Remove authorization header after we're done with it
	r.Header.Del("Authorization")

	tokenString := strings.TrimPrefix(authHeader, "Bearer ")
	if tokenString == authHeader {
		logFailure(traceCtx, ErrInvalidAuthFormat)
		err := errors.New(ErrInvalidAuthFormat)
		respondUnauthorized(err)
		return nil, err
	}

	// Get the key function for token verification
	keyFunc := newJWKKeyFunc(traceCtx, opts, jwkCache, ctxLogger, !writeResponse)

	// Parse and validate the token
	claims := jwt.MapClaims{}
	token, err := parseJWTWithOptions(tokenString, claims, keyFunc, opts)
	if err != nil {
		logFailure(traceCtx, "invalid token", zap.Error(err))
		err = fmt.Errorf("%s: %v", ErrInvalidToken, err)
		respondUnauthorized(err)
		return nil, err
	}

	if !token.Valid {
		logFailure(traceCtx, ErrInvalidToken)
		err = errors.New(ErrInvalidToken)
		respondUnauthorized(err)
		return nil, err
	}

	newCtx := context.WithValue(ctx, claimsContextKey, token.Claims)
	if opts.TenantClaim != "" {
		authorizedTenants := tenantValuesFromClaim(claims[opts.TenantClaim])
		if len(authorizedTenants) == 0 {
			logFailure(traceCtx, "missing or invalid tenant claim", zap.String("claim", opts.TenantClaim))
			err = errors.New(ErrInvalidToken)
			respondUnauthorized(err)
			return nil, err
		}
		newCtx = context.WithValue(newCtx, tenantClaimsContextKey, authorizedTenants)
	}
	ctxLogger.DebugContext(traceCtx, "jwt token parsed successfully")
	return newCtx, nil
}

func newParseJWTMiddleware(opts JWTParserOptions, jwkCache *jwk.Cache) mux.MiddlewareFunc {
	if opts.Method == nil {
		opts.Method = jwt.SigningMethodES256
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			newContext, err := processJWTToken(opts, jwkCache, w, r, true)
			if err != nil {
				return
			}
			next.ServeHTTP(w, r.WithContext(newContext))

		})
	}
}

func newJWKKeyFunc(ctx context.Context, opts JWTParserOptions, jwkCache *jwk.Cache, ctxLogger *logging.TraceLogger, quiet bool) jwt.Keyfunc {
	// Create a safe context if nil
	if ctx == nil {
		ctx = context.Background()
	}

	logFailure := ctxLogger.ErrorContext
	if quiet {
		logFailure = ctxLogger.DebugContext
	}

	return func(token *jwt.Token) (interface{}, error) {
		if token == nil {
			return nil, errors.New("nil token provided to key function")
		}

		keySet, err := fetchJwk(ctx, opts, jwkCache, ctxLogger)
		if err != nil {
			logFailure(ctx, "failed to fetch jwk", zap.Error(err))
			return nil, errors.New(ErrFetchingJwk)
		}

		kid, err := extractKidFromTokenHeaders(token.Header)
		if err != nil {
			logFailure(ctx, "failed to extract kid from token headers", zap.Error(err))
			// this is already assured to be of type nverror
			return nil, err
		}

		key, ok := keySet.LookupKeyID(kid)
		if !ok {
			logFailure(ctx, "jwk not found for kid", zap.String("kid", kid))
			return nil, errors.New(ErrMissingJwk)
		}

		var cryptoKey interface{}
		if err := key.Raw(&cryptoKey); err != nil {
			logFailure(ctx, "failed to get raw crypto key", zap.Error(err))
			return nil, fmt.Errorf("failed to get raw crypto key: %w", err)
		}

		return cryptoKey, nil
	}
}

// fetchJwk decides whether the cache can be used, or the jwk.Set must be fetched anew
func fetchJwk(ctx context.Context, opts JWTParserOptions, jwkCache *jwk.Cache, ctxLogger *logging.TraceLogger) (jwk.Set, error) {
	// Guard against nil context or cache
	if ctx == nil {
		ctx = context.Background()
	}
	if jwkCache == nil {
		ctxLogger.ErrorContext(ctx, "jwk cache is nil, creating a new cache")
		jwkCache = jwk.NewCache(ctx)
	}

	// Guard against nil logger
	if ctxLogger == nil {
		// Can't log this, but at least avoid a nil pointer panic
		return nil, fmt.Errorf("nil logger provided to fetchJwk")
	}

	// Create a recovery function to prevent panics from propagating
	defer func() {
		if r := recover(); r != nil {
			ctxLogger.ErrorContext(ctx, "recovered from panic in fetchjwk",
				zap.Any("recovered", r),
				zap.String("stack", string(debug.Stack())))
		}
	}()

	// Use shared HTTP client with context wrapper
	client := GetSharedHTTPClient(opts.HTTPConfig)
	// client := &http.Client{
	// 	// The jwk library does not seem to propagate the context passed to
	// 	// jwk.Get(). This wrapper allows us to inject the context directly into
	// 	// the http client.
	// 	Transport: &transportWithCtx{
	// 		base: baseClient.Transport,
	// 		ctx:  ctx,
	// 	},
	// 	Timeout: 10 * time.Second, // Add a reasonable timeout
	// }

	// When opts.JwkCacheRefreshInterval is zero, bypass the cache by not registering it at all.
	if opts.JwkCacheRefreshInterval == 0 {
		ctxLogger.WarnContext(ctx, "jwks cache refresh interval is zero, cache is disabled")
		// Don't use cache at all
		keySet, err := jwk.Fetch(ctx, opts.JwksURL, jwk.WithHTTPClient(client))
		if err != nil {
			ctxLogger.ErrorContext(ctx, "failed to fetch jwks directly",
				zap.Error(err),
				zap.String("url", opts.JwksURL))
			return nil, fmt.Errorf("%s: %w", ErrFetchingJwk, err)
		}
		return keySet, nil
	}

	// Handle normal cached case
	if !jwkCache.IsRegistered(opts.JwksURL) {
		ctxLogger.DebugContext(ctx, "registering jwks url in cache for the first time",
			zap.String("url", opts.JwksURL),
			zap.Duration("refreshInterval", opts.JwkCacheRefreshInterval))

		err := jwkCache.Register(opts.JwksURL,
			jwk.WithHTTPClient(client),
			jwk.WithRefreshInterval(opts.JwkCacheRefreshInterval))
		if err != nil {
			ctxLogger.ErrorContext(ctx, "failed to register jwks url in cache",
				zap.Error(err),
				zap.String("url", opts.JwksURL))
			return nil, fmt.Errorf("%s: %w", ErrRegisterJWKSUrl, err)
		}
		ctxLogger.WarnContext(ctx, "jwks url registered in cache successfully",
			zap.String("url", opts.JwksURL),
			zap.Duration("refreshInterval", opts.JwkCacheRefreshInterval))
	}

	keySet, err := jwkCache.Get(ctx, opts.JwksURL)
	if err != nil {
		ctxLogger.ErrorContext(ctx, "failed to get jwks from cache",
			zap.Error(err),
			zap.String("url", opts.JwksURL))
		return nil, fmt.Errorf("%s: %w", ErrFetchingJwk, err)
	}

	ctxLogger.WarnContext(ctx, "jwks retrieved from cache successfully",
		zap.String("url", opts.JwksURL),
		zap.Int("key_count", keySet.Len()))

	return keySet, nil
}

func extractKidFromTokenHeaders(tokenHeaders map[string]interface{}) (string, error) {
	kid, ok := tokenHeaders["kid"]
	if !ok {
		return "", errors.New(ErrMissingKid)
	}

	return fmt.Sprintf("%s", kid), nil
}
