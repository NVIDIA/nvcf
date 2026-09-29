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

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/logging"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.uber.org/zap/zaptest"
)

func TestGetScopesFromClaims(t *testing.T) {
	tests := []struct {
		name          string
		claims        jwt.MapClaims
		expectedScope []string
		expectedOk    bool
	}{
		{
			name:          "No scopes in claims",
			claims:        jwt.MapClaims{},
			expectedScope: nil,
			expectedOk:    false,
		},
		{
			name: "Scopes as string slice",
			claims: jwt.MapClaims{
				"scopes": []interface{}{"read", "write"},
			},
			expectedScope: []string{"read", "write"},
			expectedOk:    true,
		},
		{
			name: "Empty scopes slice",
			claims: jwt.MapClaims{
				"scopes": []interface{}{},
			},
			expectedScope: []string{},
			expectedOk:    false,
		},
		{
			name: "Scopes with mixed types",
			claims: jwt.MapClaims{
				"scopes": []interface{}{"read", 123, true},
			},
			expectedScope: []string{"read", "123", "true"},
			expectedOk:    true,
		},
		{
			name: "Scopes as map with data field",
			claims: jwt.MapClaims{
				"scopes": []interface{}{
					map[string]interface{}{"data": "read"},
					map[string]interface{}{"data": "write"},
				},
			},
			expectedScope: []string{"read", "write"},
			expectedOk:    true,
		},
		{
			name: "Scopes as non-array",
			claims: jwt.MapClaims{
				"scopes": "read write",
			},
			expectedScope: nil,
			expectedOk:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scopes, ok := getScopesFromClaims(tt.claims)
			assert.Equal(t, tt.expectedOk, ok)
			assert.Equal(t, tt.expectedScope, scopes)
		})
	}
}

func TestHasAllRequiredScopes(t *testing.T) {
	tests := []struct {
		name           string
		scopesList     []string
		requiredScopes Scopes
		expected       bool
	}{
		{
			name:           "Empty required scopes",
			scopesList:     []string{"read", "write"},
			requiredScopes: "",
			expected:       true,
		},
		{
			name:           "Has all required scopes",
			scopesList:     []string{"read", "write", "admin"},
			requiredScopes: "read write",
			expected:       true,
		},
		{
			name:           "Missing some required scopes",
			scopesList:     []string{"read"},
			requiredScopes: "read write",
			expected:       false,
		},
		{
			name:           "Missing all required scopes",
			scopesList:     []string{"delete", "update"},
			requiredScopes: "read write",
			expected:       false,
		},
		{
			name:           "Empty scope list",
			scopesList:     []string{},
			requiredScopes: "read write",
			expected:       false,
		},
		{
			name:           "Whitespace in scopes",
			scopesList:     []string{" read ", "write "},
			requiredScopes: "read write",
			expected:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hasAllRequiredScopes(tt.scopesList, tt.requiredScopes)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestHasAnyRequiredScope(t *testing.T) {
	tests := []struct {
		name           string
		scopesList     []string
		requiredScopes Scopes
		expected       bool
	}{
		{
			name:           "Empty required scopes",
			scopesList:     []string{"read", "write"},
			requiredScopes: "",
			expected:       false, // No required scopes means none can match
		},
		{
			name:           "Has one required scope",
			scopesList:     []string{"read", "delete"},
			requiredScopes: "read write",
			expected:       true,
		},
		{
			name:           "Has all required scopes",
			scopesList:     []string{"read", "write", "admin"},
			requiredScopes: "read write",
			expected:       true,
		},
		{
			name:           "Missing all required scopes",
			scopesList:     []string{"delete", "update"},
			requiredScopes: "read write",
			expected:       false,
		},
		{
			name:           "Empty scope list",
			scopesList:     []string{},
			requiredScopes: "read write",
			expected:       false,
		},
		{
			name:           "Whitespace in scopes",
			scopesList:     []string{" admin ", "delete "},
			requiredScopes: "read write admin",
			expected:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hasAnyRequiredScope(tt.scopesList, tt.requiredScopes)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRequireScopes(t *testing.T) {
	logger := otelzap.New(zaptest.NewLogger(t))

	tests := []struct {
		name             string
		claims           jwt.MapClaims
		requiredScopes   Scopes
		scopeRequirement ScopeRequirement
		expectedStatus   int
	}{
		{
			name: "Has all required scopes",
			claims: jwt.MapClaims{
				"scopes": []interface{}{"read", "write", "admin"},
			},
			requiredScopes:   "read write",
			scopeRequirement: RequireAllScopes,
			expectedStatus:   http.StatusOK,
		},
		{
			name: "Missing some required scopes when all required",
			claims: jwt.MapClaims{
				"scopes": []interface{}{"read"},
			},
			requiredScopes:   "read write",
			scopeRequirement: RequireAllScopes,
			expectedStatus:   http.StatusForbidden,
		},
		{
			name: "Has one required scope when any required",
			claims: jwt.MapClaims{
				"scopes": []interface{}{"read"},
			},
			requiredScopes:   "read write",
			scopeRequirement: RequireAnyScopes,
			expectedStatus:   http.StatusOK,
		},
		{
			name: "Missing all required scopes when any required",
			claims: jwt.MapClaims{
				"scopes": []interface{}{"admin", "delete"},
			},
			requiredScopes:   "read write",
			scopeRequirement: RequireAnyScopes,
			expectedStatus:   http.StatusForbidden,
		},
		{
			name:             "Missing claims",
			claims:           nil,
			requiredScopes:   "read write",
			scopeRequirement: RequireAnyScopes,
			expectedStatus:   http.StatusUnauthorized,
		},
		{
			name: "No scopes in claims",
			claims: jwt.MapClaims{
				"sub": "user123",
			},
			requiredScopes:   "read write",
			scopeRequirement: RequireAnyScopes,
			expectedStatus:   http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test handler
			testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			// Create middleware
			middleware := requireScopes(tt.requiredScopes, tt.scopeRequirement)
			wrappedHandler := middleware(testHandler)

			// Create a test request
			req := httptest.NewRequest("GET", "/test", nil)

			// Create context with trace logger
			traceLogger := logging.NewTraceLogger(req.Context(), logger)
			ctx := context.WithValue(req.Context(), logging.LoggerKey, traceLogger)

			// Add claims to context if provided
			if tt.claims != nil {
				ctx = context.WithValue(ctx, claimsContextKey, tt.claims)
			}

			// Update request with the enriched context
			req = req.WithContext(ctx)

			// Record the response
			rec := httptest.NewRecorder()
			wrappedHandler.ServeHTTP(rec, req)

			// Check the status code
			assert.Equal(t, tt.expectedStatus, rec.Code)
		})
	}
}

func TestMaybeRequireScopes(t *testing.T) {
	logger := otelzap.New(zaptest.NewLogger(t))

	tests := []struct {
		name             string
		authEnabled      bool
		claims           jwt.MapClaims
		requiredScopes   Scopes
		scopeRequirement ScopeRequirement
		expectedStatus   int
	}{
		{
			name:             "Auth disabled - should skip checks",
			authEnabled:      false,
			claims:           jwt.MapClaims{}, // Even with no scopes
			requiredScopes:   "read write",
			scopeRequirement: RequireAllScopes,
			expectedStatus:   http.StatusOK, // Should pass through
		},
		{
			name:        "Auth enabled - has all scopes",
			authEnabled: true,
			claims: jwt.MapClaims{
				"scopes": []interface{}{"read", "write"},
			},
			requiredScopes:   "read write",
			scopeRequirement: RequireAllScopes,
			expectedStatus:   http.StatusOK,
		},
		{
			name:        "Auth enabled - missing some scopes",
			authEnabled: true,
			claims: jwt.MapClaims{
				"scopes": []interface{}{"read"},
			},
			requiredScopes:   "read write",
			scopeRequirement: RequireAllScopes,
			expectedStatus:   http.StatusForbidden,
		},
		{
			name:        "Auth enabled - has one required scope when any required",
			authEnabled: true,
			claims: jwt.MapClaims{
				"scopes": []interface{}{"write"},
			},
			requiredScopes:   "read write",
			scopeRequirement: RequireAnyScopes,
			expectedStatus:   http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test handler
			testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			// Create middleware
			middleware := MaybeRequireScopes(logger, tt.authEnabled, tt.requiredScopes, tt.scopeRequirement)
			wrappedHandler := middleware(testHandler)

			// Create a test request
			req := httptest.NewRequest("GET", "/test", nil)

			// Create context with trace logger
			traceLogger := logging.NewTraceLogger(req.Context(), logger)
			ctx := context.WithValue(req.Context(), logging.LoggerKey, traceLogger)

			// Add claims to context if provided
			if tt.claims != nil {
				ctx = context.WithValue(ctx, claimsContextKey, tt.claims)
			}

			// Update request with the enriched context
			req = req.WithContext(ctx)

			// Record the response
			rec := httptest.NewRecorder()
			wrappedHandler.ServeHTTP(rec, req)

			// Check the status code
			assert.Equal(t, tt.expectedStatus, rec.Code)
		})
	}
}

func TestMaybeRequirePathTenant(t *testing.T) {
	tests := []struct {
		name           string
		authorized     []string
		requested      string
		expectedStatus int
	}{
		{
			name:           "matching tenant",
			authorized:     []string{"tenant-a"},
			requested:      "tenant-a",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "different tenant",
			authorized:     []string{"tenant-a"},
			requested:      "tenant-b",
			expectedStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := MaybeRequirePathTenant(true)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest(http.MethodGet, "/v3/ledger/namespace/"+tt.requested+"/stats", nil)
			req = mux.SetURLVars(req, map[string]string{"namespace": tt.requested})
			ctx := context.WithValue(req.Context(), tenantClaimsContextKey, tt.authorized)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, req.WithContext(ctx))
			assert.Equal(t, tt.expectedStatus, recorder.Code)
		})
	}
}

func TestScopes_Integration(t *testing.T) {
	logger := otelzap.New(zaptest.NewLogger(t))

	tests := []struct {
		name           string
		setupHandler   func() http.Handler
		expectedStatus int
	}{
		{
			name: "Multiple chained scope checks - all pass",
			setupHandler: func() http.Handler {
				// Create a router
				router := mux.NewRouter()

				// Create test handler
				testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				})

				// Add multiple scope checks
				handler := MaybeRequireScopes(logger, true, "read", RequireAnyScopes)(
					MaybeRequireScopes(logger, true, "write", RequireAnyScopes)(
						testHandler,
					),
				)

				router.Handle("/test", handler)
				return router
			},
			expectedStatus: http.StatusOK,
		},
		{
			name: "Multiple chained scope checks - one fails",
			setupHandler: func() http.Handler {
				// Create a router
				router := mux.NewRouter()

				// Create test handler
				testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				})

				// Add multiple scope checks - second one will fail
				handler := MaybeRequireScopes(logger, true, "read", RequireAnyScopes)(
					MaybeRequireScopes(logger, true, "admin", RequireAnyScopes)(
						testHandler,
					),
				)

				router.Handle("/test", handler)
				return router
			},
			expectedStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test request
			req := httptest.NewRequest("GET", "/test", nil)

			// Create context with trace logger
			traceLogger := logging.NewTraceLogger(req.Context(), logger)
			ctx := context.WithValue(req.Context(), logging.LoggerKey, traceLogger)

			// Add claims with scopes
			claims := jwt.MapClaims{
				"scopes": []interface{}{"read", "write"},
			}
			ctx = context.WithValue(ctx, claimsContextKey, claims)
			req = req.WithContext(ctx)

			// Get the handler for this test
			handler := tt.setupHandler()

			// Record the response
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			// Check the status code
			assert.Equal(t, tt.expectedStatus, rec.Code)
		})
	}
}

func TestDisableAuthentication(t *testing.T) {
	logger := otelzap.New(zaptest.NewLogger(t))

	// Create handlers that require different scopes
	readHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Read endpoint success"))
	})

	writeHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Write endpoint success"))
	})

	adminHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Admin endpoint success"))
	})

	// Setup two scenarios: with auth enabled and disabled
	tests := []struct {
		name          string
		authEnabled   bool
		expectedCodes map[string]int
	}{
		{
			name:        "Authentication enabled - should enforce scopes",
			authEnabled: true,
			expectedCodes: map[string]int{
				"/read":  http.StatusForbidden, // No read scope
				"/write": http.StatusOK,        // Has write scope
				"/admin": http.StatusForbidden, // No admin scope
			},
		},
		{
			name:        "Authentication disabled - should allow all requests",
			authEnabled: false,
			expectedCodes: map[string]int{
				"/read":  http.StatusOK, // Should pass through
				"/write": http.StatusOK, // Should pass through
				"/admin": http.StatusOK, // Should pass through
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a new router for this test
			router := mux.NewRouter()

			// Apply middleware with auth enabled/disabled
			router.Handle("/read", MaybeRequireScopes(logger, tt.authEnabled, "read", RequireAnyScopes)(readHandler))
			router.Handle("/write", MaybeRequireScopes(logger, tt.authEnabled, "write", RequireAnyScopes)(writeHandler))
			router.Handle("/admin", MaybeRequireScopes(logger, tt.authEnabled, "admin", RequireAnyScopes)(adminHandler))

			// Create a test JWT with only "write" scope
			claims := jwt.MapClaims{
				"scopes": []interface{}{"write"},
				"sub":    "user123",
			}

			// Test each endpoint
			for path, expectedCode := range tt.expectedCodes {
				t.Run(path, func(t *testing.T) {
					// Create a test request
					req := httptest.NewRequest("GET", path, nil)

					// Create context with trace logger
					traceLogger := logging.NewTraceLogger(req.Context(), logger)
					ctx := context.WithValue(req.Context(), logging.LoggerKey, traceLogger)

					// Add claims to context
					ctx = context.WithValue(ctx, claimsContextKey, claims)
					req = req.WithContext(ctx)

					// Record the response
					rec := httptest.NewRecorder()
					router.ServeHTTP(rec, req)

					// Check the status code
					assert.Equal(t, expectedCode, rec.Code)
				})
			}
		})
	}
}

func TestAuthCheckURLs(t *testing.T) {
	tests := []struct {
		name     string
		auth     config.AuthConfig
		expected map[string]string
	}{
		{
			name:     "disabled auth has no checks",
			auth:     config.AuthConfig{Enabled: false},
			expected: map[string]string{},
		},
		{
			name: "jwt provider uses configured jwks url",
			auth: config.AuthConfig{
				Enabled:   true,
				Provider:  "jwt",
				JWKSetUrl: "https://issuer.test/.well-known/jwks.json",
			},
			expected: map[string]string{
				"jwt_jwks": "https://issuer.test/.well-known/jwks.json",
			},
		},
		{
			name: "policy provider uses configured auth dependencies",
			auth: config.AuthConfig{
				Enabled:   true,
				Provider:  "policy",
				JWKSetUrl: "https://issuer.test/.well-known/jwks.json",
				Policy: config.PolicyConfig{
					TokenIssuerAddr:     "https://issuer.test",
					PolicyEvaluatorAddr: "https://pdp.test",
				},
			},
			expected: map[string]string{
				"jwt_jwks":            "https://issuer.test/.well-known/jwks.json",
				"policy_token_issuer": "https://issuer.test",
				"policy_evaluator":    "https://pdp.test",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, authCheckURLs(tt.auth))
		})
	}
}

func TestParseJWTWithMethodRejectsUnexpectedAlgorithm(t *testing.T) {
	secret := []byte("test-secret")
	signedToken, err := jwt.NewWithClaims(
		jwt.SigningMethodHS256,
		jwt.MapClaims{"sub": "test-user"},
	).SignedString(secret)
	assert.NoError(t, err)

	keyFunc := func(_ *jwt.Token) (interface{}, error) {
		return secret, nil
	}

	_, err = parseJWTWithOptions(signedToken, jwt.MapClaims{}, keyFunc, JWTParserOptions{Method: jwt.SigningMethodES256})
	assert.Error(t, err)

	token, err := parseJWTWithOptions(signedToken, jwt.MapClaims{}, keyFunc, JWTParserOptions{Method: jwt.SigningMethodHS256})
	assert.NoError(t, err)
	assert.True(t, token.Valid)
}
