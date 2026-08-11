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
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/logging"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/policy"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/mux"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.uber.org/zap"

	pdpv1 "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/clients/pdp_types"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
)

// Policy context keys - using the contextKey type already defined in jwt.go
const (
	policyActorIDContextKey   contextKey = "policy_actor_id"
	policyOrgNameContextKey   contextKey = "policy_org_name"
	policyActorTypeContextKey contextKey = "policy_actor_type"
	policyRolesContextKey     contextKey = "policy_roles"
	policyClaimsContextKey    contextKey = "policy_claims"

	defaultAuthSubjectField = "subject"
	defaultAuthAPIKeyField  = "apiKey"
)

// PolicyAuthzResponse holds the response from Policy authorization
type PolicyAuthzResponse struct {
	Allow      bool     `json:"allow"`
	StatusCode int      `json:"statusCode"`
	Reasons    []string `json:"reasons"`
	ActorID    string   `json:"actorId"`
	OrgName    string   `json:"orgName"`
	ActorType  string   `json:"actorType"`
	Roles      []string `json:"roles,omitempty"`
}

// UnmarshalJSON handles string or int for status code
func (u *PolicyAuthzResponse) UnmarshalJSON(data []byte) error {
	type Alias PolicyAuthzResponse
	aux := struct {
		StatusCode interface{} `json:"statusCode"`
		*Alias
	}{
		Alias: (*Alias)(u),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	// Handle statusCode based on type
	switch sc := aux.StatusCode.(type) {
	case float64:
		u.StatusCode = int(sc)
	case string:
		code, err := strconv.Atoi(sc)
		if err != nil {
			u.StatusCode = http.StatusForbidden
		} else {
			u.StatusCode = code
		}
	default:
		u.StatusCode = http.StatusForbidden
	}

	return nil
}

// ContextError for error details
type ContextError struct {
	Message    string
	StatusCode int
	Err        error
}

func (e ContextError) Error() string {
	return e.Err.Error()
}

func policyInputFields(policyConfig *policy.PolicyConfig) (string, string) {
	subjectField := defaultAuthSubjectField
	apiKeyField := defaultAuthAPIKeyField
	if policyConfig == nil {
		return subjectField, apiKeyField
	}
	if policyConfig.SubjectField != "" {
		subjectField = policyConfig.SubjectField
	}
	if policyConfig.APIKeyField != "" {
		apiKeyField = policyConfig.APIKeyField
	}
	return subjectField, apiKeyField
}

func setAuthContextField(authCtx map[string]interface{}, field string, value string) {
	if field == "" || value == "" {
		return
	}
	authCtx[field] = value
}

func isJWTShapedToken(token string) bool {
	parts := strings.Split(token, ".")
	return len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] != ""
}

// GetActorID retrieves the actor ID from the context
func GetActorID(ctx context.Context) string {
	if v := ctx.Value(policyActorIDContextKey); v != nil {
		return v.(string)
	}
	return ""
}

// GetOrgName retrieves the organization name from the context
func GetOrgName(ctx context.Context) string {
	if v := ctx.Value(policyOrgNameContextKey); v != nil {
		return v.(string)
	}
	return ""
}

// GetActorType retrieves the actor type from the context
func GetActorType(ctx context.Context) string {
	if v := ctx.Value(policyActorTypeContextKey); v != nil {
		return v.(string)
	}
	return ""
}

// GetRoles retrieves roles from the context
func GetRoles(ctx context.Context) []string {
	if v := ctx.Value(policyRolesContextKey); v != nil {
		return v.([]string)
	}
	return nil
}

// GetClaims retrieves all claims/attributes from the Policy response
func GetClaims(ctx context.Context) map[string]interface{} {
	if v := ctx.Value(policyClaimsContextKey); v != nil {
		return v.(map[string]interface{})
	}
	return nil
}

// mergePolicyClaims preserves general JWT claims while ensuring the trusted
// authorization response owns identity and role fields.
func mergePolicyClaims(jwtClaims map[string]interface{}, authResponse PolicyAuthzResponse) map[string]interface{} {
	claims := make(map[string]interface{}, len(jwtClaims)+4)
	for k, v := range jwtClaims {
		claims[k] = v
	}
	claims["actorId"] = authResponse.ActorID
	claims["orgName"] = authResponse.OrgName
	claims["actorType"] = authResponse.ActorType
	claims["roles"] = authResponse.Roles
	return claims
}

// NewPolicyMiddleware creates a new Policy middleware
func NewPolicyMiddleware(authzClient policy.Authorizer, serviceName string, jwtPubKeySetURL string, jwtTokenExpiration time.Duration, jwkCache *jwk.Cache, httpConfig *config.HTTPClientConfig, logger *otelzap.Logger) mux.MiddlewareFunc {
	if authzClient == nil {
		if logger != nil {
			logger.Error("policy client is nil - denying requests")
		}
		return func(_ http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
			})
		}
	}

	// Initialize JWT parser if URL is provided
	var jwtMiddleware mux.MiddlewareFunc
	if jwtPubKeySetURL != "" {
		jwtOpts := NewJWTParserOptions(
			jwtPubKeySetURL,
			nil, // Use default signing method
			jwtTokenExpiration,
			httpConfig,
		)
		jwtMiddleware = NewParseJWTMiddleware(jwtOpts, jwkCache)
		logger.Info("jwt middleware initialized successfully")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Tracing handled by external library
			traceCtx := r.Context()

			logger := logging.GetLogger(traceCtx)
			logger.InfoContext(traceCtx, "policy: processing request", zap.String("path", r.URL.Path), zap.String("method", r.Method))

			policyConfig := authzClient.PolicyConfig()
			subjectField, apiKeyField := policyInputFields(policyConfig)

			// 1. Extract the token (simple bearer token extraction)
			token := ""
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				token = strings.TrimPrefix(authHeader, "Bearer ")
				logger.InfoContext(traceCtx, "policy: token extracted", zap.String("token_length", strconv.Itoa(len(token))))
			} else {
				logger.WarnContext(traceCtx, "policy: no bearer token found in authorization header")
			}

			authCtx := map[string]interface{}{
				"path":    r.URL.Path,
				"method":  r.Method,
				"service": serviceName,
			}

			// Try to parse as JWT if parser is available
			var jwtClaims map[string]interface{}

			if jwtMiddleware != nil && token != "" && isJWTShapedToken(token) {
				// Create a handler that will capture the JWT claims
				claimsHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					claims, ok := r.Context().Value(claimsContextKey).(jwt.MapClaims)
					if ok {
						jwtClaims = map[string]interface{}(claims)
						logger.InfoContext(traceCtx, "policy: jwt token parsed successfully")

						// Add JWT specific fields to auth context
						if subj, ok := claims["sub"].(string); ok {
							setAuthContextField(authCtx, subjectField, subj)
						}
						if scopes, ok := claims["scopes"].([]interface{}); ok && len(scopes) > 0 {
							authCtx["scopes"] = scopes
						}
					}
				})

				// Apply JWT middleware to process the token
				jwtChain := jwtMiddleware(claimsHandler)

				// Create a fake ResponseWriter that doesn't actually write
				dummyWriter := &dummyResponseWriter{header: make(http.Header)}

				// Copy the request to avoid modifying the original
				reqCopy := r.Clone(traceCtx)
				jwtChain.ServeHTTP(dummyWriter, reqCopy)

				if jwtClaims == nil {
					logger.WarnContext(traceCtx, "policy: jwt-shaped token failed validation")
					http.Error(w, "Unauthorized", http.StatusUnauthorized)
					return
				}
			} else if token != "" {
				logger.InfoContext(traceCtx, "policy: token appears to be an api key, not a jwt")
				setAuthContextField(authCtx, apiKeyField, token)
			}

			// 3. Prepare the Policy request - use the policy config from the client
			logger.InfoContext(traceCtx, "policy: using policy config from client", zap.String("namespace", policyConfig.Namespace), zap.String("policy_fqdn", policyConfig.PolicyFQDN))

			policyRequest := struct {
				Namespace string                 `json:"namespace,omitempty"`
				RuleName  string                 `json:"rule_name,omitempty"`
				Input     map[string]interface{} `json:"input,omitempty"`
			}{
				Namespace: policyConfig.Namespace,
				RuleName:  policyConfig.PolicyFQDN,
				Input:     authCtx,
			}

			// Convert to JSON for logging
			reqBytes, _ := json.Marshal(policyRequest)

			// 4. Convert to RuleRequest
			authReq := &pdpv1.RuleRequest{}
			if err := json.Unmarshal(reqBytes, authReq); err != nil {
				logger.ErrorContext(traceCtx, "policy: failed to unmarshal to rulerequest", zap.Error(err))
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}

			// 5. Call Policy service
			logger.InfoContext(traceCtx, "policy: calling policy evaluate service")
			authzResp, err := authzClient.Evaluate(traceCtx, authReq)
			if err != nil {
				logger.ErrorContext(traceCtx, "policy: evaluation failed", zap.Error(err))
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			// 6. Process response
			if authzResp == nil || authzResp.Result == nil {
				logger.ErrorContext(traceCtx, "policy: empty or nil response")
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			respBytes, err := json.Marshal(authzResp.Result)
			if err != nil {
				logger.ErrorContext(traceCtx, "policy: failed to marshal response", zap.Error(err))
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}

			logger.DebugContext(traceCtx, "policy: response received", zap.Int("response_size", len(respBytes)))

			// 7. Parse the response
			var authResponse PolicyAuthzResponse
			if err := json.Unmarshal(respBytes, &authResponse); err != nil {
				logger.ErrorContext(traceCtx, "policy: failed to unmarshal response", zap.Error(err))
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}

			logger.DebugContext(
				traceCtx,
				"policy: response parsed",
				zap.Bool("allow", authResponse.Allow),
				zap.Int("status_code", authResponse.StatusCode),
				zap.Int("reason_count", len(authResponse.Reasons)),
			)

			// 8. Check if allowed
			if !authResponse.Allow {
				statusCode := authResponse.StatusCode
				if statusCode == 0 {
					statusCode = http.StatusUnauthorized
				} else if statusCode < http.StatusBadRequest || statusCode >= http.StatusInternalServerError {
					statusCode = http.StatusForbidden
				}

				message := http.StatusText(statusCode)
				if message == "" {
					message = http.StatusText(http.StatusForbidden)
				}

				logger.WarnContext(traceCtx, "policy: authorization denied", zap.Int("status_code", statusCode))

				http.Error(w, message, statusCode)
				return
			}

			// 9. Authorization succeeded - enrich context with user info
			logger.InfoContext(traceCtx, "policy: authorization successful")

			var requestCtx = r.Context()
			// Create enriched context
			if authResponse.ActorID != "" {
				requestCtx = context.WithValue(requestCtx, policyActorIDContextKey, authResponse.ActorID)
			}
			if authResponse.OrgName != "" {
				requestCtx = context.WithValue(requestCtx, policyOrgNameContextKey, authResponse.OrgName)
			}
			if authResponse.ActorType != "" {
				requestCtx = context.WithValue(requestCtx, policyActorTypeContextKey, authResponse.ActorType)
			}
			if len(authResponse.Roles) > 0 {
				requestCtx = context.WithValue(requestCtx, policyRolesContextKey, authResponse.Roles)
			}

			claims := mergePolicyClaims(jwtClaims, authResponse)

			requestCtx = context.WithValue(requestCtx, policyClaimsContextKey, claims)

			// 10. Log summary of auth info
			logger.DebugContext(traceCtx, "policy: request authorization details", zap.String("actor_id", authResponse.ActorID), zap.String("org_name", authResponse.OrgName), zap.String("actor_type", authResponse.ActorType), zap.Strings("roles", authResponse.Roles))

			// 11. Update request with enriched context and call next handler
			// Tracing is now handled by external library
			r = r.WithContext(requestCtx)
			next.ServeHTTP(w, r)
		})
	}
}

// dummyResponseWriter is a no-op ResponseWriter used to capture JWT claims
// without actually writing anything to the client
type dummyResponseWriter struct {
	header http.Header
}

func (d *dummyResponseWriter) Header() http.Header {
	return d.header
}

func (d *dummyResponseWriter) Write([]byte) (int, error) {
	return 0, nil
}

func (d *dummyResponseWriter) WriteHeader(statusCode int) {
	// Do nothing
}
