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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
	policyclient "github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/policy"
	pdpv1 "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/clients/pdp_types"
	"github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.uber.org/zap/zaptest"
	"google.golang.org/protobuf/types/known/structpb"
)

type stubPolicyClient struct {
	called  bool
	lastReq *pdpv1.RuleRequest
	result  map[string]interface{}
	empty   bool
	err     error
}

func (c *stubPolicyClient) Evaluate(_ context.Context, req *pdpv1.RuleRequest) (*pdpv1.RuleResponse, error) {
	c.called = true
	c.lastReq = req
	if c.err != nil {
		return nil, c.err
	}
	if c.empty {
		return &pdpv1.RuleResponse{}, nil
	}
	result, err := structpb.NewValue(c.result)
	if err != nil {
		return nil, err
	}
	return &pdpv1.RuleResponse{Result: result}, nil
}

func (c *stubPolicyClient) PolicyConfig() *policyclient.PolicyConfig {
	return &policyclient.PolicyConfig{Namespace: "testns", PolicyFQDN: "testpolicy"}
}

func allowResult(overrides map[string]interface{}) map[string]interface{} {
	result := map[string]interface{}{
		"allow":      true,
		"statusCode": 200,
		"actorId":    "user123",
		"orgName":    "org123",
		"actorType":  "user",
		"roles":      []interface{}{"admin", "user"},
		"reasons":    []interface{}{"authorized"},
	}
	for k, v := range overrides {
		result[k] = v
	}
	return result
}

func testLogger(t *testing.T) *otelzap.Logger {
	t.Helper()
	return otelzap.New(zaptest.NewLogger(t))
}

func servePolicy(t *testing.T, client policyclient.Authorizer, req *http.Request) (*httptest.ResponseRecorder, context.Context) {
	t.Helper()
	var capturedCtx context.Context
	handler := newPolicyMiddleware(client, "test-service", testLogger(t))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedCtx = r.Context()
		w.WriteHeader(http.StatusOK)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder, capturedCtx
}

func newSigningKeyAndJWKS(t *testing.T) (*ecdsa.PrivateKey, string, func()) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	key, err := jwk.FromRaw(&privateKey.PublicKey)
	require.NoError(t, err)
	require.NoError(t, key.Set(jwk.KeyIDKey, "test-key"))
	require.NoError(t, key.Set(jwk.AlgorithmKey, "ES256"))

	set := jwk.NewSet()
	require.NoError(t, set.AddKey(key))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(set))
	}))
	return privateKey, server.URL, server.Close
}

func signToken(t *testing.T, key *ecdsa.PrivateKey, scopes []string) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"sub":    "sis-api",
		"scopes": scopes,
		"exp":    time.Now().Add(time.Hour).Unix(),
	})
	token.Header[jwk.KeyIDKey] = "test-key"
	signed, err := token.SignedString(key)
	require.NoError(t, err)
	return signed
}

func newAuthTestHandler(t *testing.T, jwtOpts *JWTParserOptions, jwkCache *jwk.Cache, client *stubPolicyClient, selfManaged bool, requiredScopes Scopes) http.Handler {
	t.Helper()
	logger := testLogger(t)
	authMiddleware := NewAuthMiddleware(client, "nv-cloud-functions", jwtOpts, jwkCache, selfManaged, logger)
	scoped := MaybeRequireScopes(logger, true, requiredScopes, RequireAnyScopes)
	return authMiddleware(scoped(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
}

func TestGetContextValues(t *testing.T) {
	tests := []struct {
		name      string
		setupCtx  func(context.Context) context.Context
		actorID   string
		orgName   string
		actorType string
		roles     []string
		claims    map[string]interface{}
	}{
		{
			name: "Context with all values",
			setupCtx: func(ctx context.Context) context.Context {
				ctx = context.WithValue(ctx, policyActorIDContextKey, "user123")
				ctx = context.WithValue(ctx, policyOrgNameContextKey, "org123")
				ctx = context.WithValue(ctx, policyActorTypeContextKey, "user")
				ctx = context.WithValue(ctx, policyRolesContextKey, []string{"admin", "user"})
				ctx = context.WithValue(ctx, policyClaimsContextKey, map[string]interface{}{
					"sub":  "user123",
					"name": "Test User",
				})
				return ctx
			},
			actorID:   "user123",
			orgName:   "org123",
			actorType: "user",
			roles:     []string{"admin", "user"},
			claims: map[string]interface{}{
				"sub":  "user123",
				"name": "Test User",
			},
		},
		{
			name: "Context with no values",
			setupCtx: func(ctx context.Context) context.Context {
				return ctx
			},
			actorID:   "",
			orgName:   "",
			actorType: "",
			roles:     nil,
			claims:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.setupCtx(context.Background())

			assert.Equal(t, tt.actorID, GetActorID(ctx))
			assert.Equal(t, tt.orgName, GetOrgName(ctx))
			assert.Equal(t, tt.actorType, GetActorType(ctx))
			assert.Equal(t, tt.roles, GetRoles(ctx))
			assert.Equal(t, tt.claims, GetClaims(ctx))
		})
	}
}

func TestPolicyAuthzResponseUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name           string
		jsonData       string
		expectedResult PolicyAuthzResponse
	}{
		{
			name: "Valid JSON with int status code",
			jsonData: `{
				"allow": true,
				"statusCode": 200,
				"reasons": ["success"],
				"actorId": "user123",
				"orgName": "org123",
				"actorType": "user",
				"roles": ["admin", "user"]
			}`,
			expectedResult: PolicyAuthzResponse{
				Allow:      true,
				StatusCode: 200,
				Reasons:    []string{"success"},
				ActorID:    "user123",
				OrgName:    "org123",
				ActorType:  "user",
				Roles:      []string{"admin", "user"},
			},
		},
		{
			name: "Valid JSON with string status code",
			jsonData: `{
				"allow": false,
				"statusCode": "403",
				"reasons": ["unauthorized"],
				"actorId": "",
				"orgName": "",
				"actorType": ""
			}`,
			expectedResult: PolicyAuthzResponse{
				Allow:      false,
				StatusCode: 403,
				Reasons:    []string{"unauthorized"},
				ActorID:    "",
				OrgName:    "",
				ActorType:  "",
			},
		},
		{
			name: "Invalid string status code defaults to 403",
			jsonData: `{
				"allow": false,
				"statusCode": "invalid",
				"reasons": ["error"]
			}`,
			expectedResult: PolicyAuthzResponse{
				Allow:      false,
				StatusCode: 403,
				Reasons:    []string{"error"},
			},
		},
		{
			name: "Missing status code",
			jsonData: `{
				"allow": false,
				"reasons": ["unauthorized"]
			}`,
			expectedResult: PolicyAuthzResponse{
				Allow:      false,
				StatusCode: 403,
				Reasons:    []string{"unauthorized"},
			},
		},
		{
			name:     "api-keys-api verdict field",
			jsonData: `{"allowed":true}`,
			expectedResult: PolicyAuthzResponse{
				Allowed:    true,
				StatusCode: 403,
			},
		},
		{
			name:     "managed PDP verdict field",
			jsonData: `{"allow":true}`,
			expectedResult: PolicyAuthzResponse{
				Allow:      true,
				StatusCode: 403,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var response PolicyAuthzResponse
			require.NoError(t, json.Unmarshal([]byte(tt.jsonData), &response))
			assert.Equal(t, tt.expectedResult.Allow, response.Allow)
			assert.Equal(t, tt.expectedResult.Allowed, response.Allowed)
			assert.Equal(t, tt.expectedResult.StatusCode, response.StatusCode)
			assert.Equal(t, tt.expectedResult.Reasons, response.Reasons)
			assert.Equal(t, tt.expectedResult.ActorID, response.ActorID)
			assert.Equal(t, tt.expectedResult.OrgName, response.OrgName)
			assert.Equal(t, tt.expectedResult.ActorType, response.ActorType)
			assert.Equal(t, tt.expectedResult.Roles, response.Roles)
		})
	}
}

func TestPolicyAuthInputFields(t *testing.T) {
	subjectField, apiKeyField := policyInputFields(nil)
	assert.Equal(t, defaultAuthSubjectField, subjectField)
	assert.Equal(t, defaultAuthAPIKeyField, apiKeyField)

	subjectField, apiKeyField = policyInputFields(&policyclient.PolicyConfig{
		SubjectField: "actor",
		APIKeyField:  "credential",
	})
	assert.Equal(t, "actor", subjectField)
	assert.Equal(t, "credential", apiKeyField)

	authCtx := map[string]interface{}{}
	setAuthContextField(authCtx, subjectField, "user-1")
	setAuthContextField(authCtx, apiKeyField, "token-1")
	assert.Equal(t, "user-1", authCtx["actor"])
	assert.Equal(t, "token-1", authCtx["credential"])
}

func TestNewPolicyMiddleware(t *testing.T) {
	tests := []struct {
		name               string
		client             *stubPolicyClient
		token              string
		expectedStatusCode int
		expectedActorID    string
		expectedOrgName    string
		expectedActorType  string
		expectedRoles      []string
	}{
		{
			name:               "Successful Authorization",
			client:             &stubPolicyClient{result: allowResult(nil)},
			token:              "valid-token",
			expectedStatusCode: http.StatusOK,
			expectedActorID:    "user123",
			expectedOrgName:    "org123",
			expectedActorType:  "user",
			expectedRoles:      []string{"admin", "user"},
		},
		{
			name: "Failed Authorization",
			client: &stubPolicyClient{result: map[string]interface{}{
				"allow":      false,
				"statusCode": 403,
				"reasons":    []interface{}{"unauthorized access"},
			}},
			token:              "invalid-token",
			expectedStatusCode: http.StatusForbidden,
		},
		{
			name:               "Policy Service Error",
			client:             &stubPolicyClient{err: errors.New("service unavailable")},
			token:              "token",
			expectedStatusCode: http.StatusUnauthorized,
		},
		{
			name:               "Empty Result From Policy",
			client:             &stubPolicyClient{empty: true},
			token:              "token",
			expectedStatusCode: http.StatusUnauthorized,
		},
		{
			name: "No Token Provided",
			client: &stubPolicyClient{result: allowResult(map[string]interface{}{
				"actorId":   "anonymous",
				"orgName":   "anonymous",
				"actorType": "anonymous",
				"roles":     []interface{}{"guest"},
			})},
			token:              "",
			expectedStatusCode: http.StatusOK,
			expectedActorID:    "anonymous",
			expectedOrgName:    "anonymous",
			expectedActorType:  "anonymous",
			expectedRoles:      []string{"guest"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}

			recorder, capturedCtx := servePolicy(t, tt.client, req)
			assert.Equal(t, tt.expectedStatusCode, recorder.Code)
			assert.True(t, tt.client.called)

			if tt.expectedStatusCode != http.StatusOK {
				return
			}

			assert.Equal(t, tt.expectedActorID, GetActorID(capturedCtx))
			assert.Equal(t, tt.expectedOrgName, GetOrgName(capturedCtx))
			assert.Equal(t, tt.expectedActorType, GetActorType(capturedCtx))
			assert.Equal(t, tt.expectedRoles, GetRoles(capturedCtx))

			claims := GetClaims(capturedCtx)
			require.NotNil(t, claims)
			assert.Equal(t, tt.expectedActorID, claims["actorId"])
			assert.Equal(t, tt.expectedOrgName, claims["orgName"])
			assert.Equal(t, tt.expectedActorType, claims["actorType"])
			assert.True(t, isPDPAuthorized(capturedCtx))
		})
	}
}

func TestNewPolicyMiddlewareNilClientReturnsServiceUnavailable(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder, _ := servePolicy(t, nil, req)
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestNewPolicyMiddlewareSendsAPIKeyInEvaluateInput(t *testing.T) {
	client := &stubPolicyClient{result: allowResult(nil)}
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer nvapi-opaque-key")

	recorder, _ := servePolicy(t, client, req)
	assert.Equal(t, http.StatusOK, recorder.Code)
	require.NotNil(t, client.lastReq)
	assert.Equal(t, "nvapi-opaque-key", client.lastReq.Input["apiKey"].GetStringValue())
	assert.Equal(t, "test-service", client.lastReq.Input["service"].GetStringValue())
}

func TestNewPolicyMiddlewareMergesJWTClaims(t *testing.T) {
	client := &stubPolicyClient{result: allowResult(map[string]interface{}{
		"actorId":   "user456",
		"orgName":   "org456",
		"actorType": "user",
		"roles":     []interface{}{"admin"},
	})}

	jwtClaims := jwt.MapClaims{
		"sub":    "user456",
		"name":   "JWT User",
		"email":  "test@example.com",
		"scopes": []interface{}{"read", "write"},
	}
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	req = req.WithContext(context.WithValue(req.Context(), claimsContextKey, jwtClaims))

	recorder, capturedCtx := servePolicy(t, client, req)
	assert.Equal(t, http.StatusOK, recorder.Code)

	claims := GetClaims(capturedCtx)
	require.NotNil(t, claims)
	assert.Equal(t, "user456", claims["actorId"])
	assert.Equal(t, "org456", claims["orgName"])
	assert.Equal(t, "user", claims["actorType"])
	assert.Equal(t, []string{"admin"}, claims["roles"])
	assert.Equal(t, "user456", claims["sub"])
	assert.Equal(t, "JWT User", claims["name"])
	assert.Equal(t, "test@example.com", claims["email"])
	assert.Equal(t, []string{"admin"}, GetRoles(capturedCtx))

	require.NotNil(t, client.lastReq)
	assert.Equal(t, "user456", client.lastReq.Input["subject"].GetStringValue())
	scopes := client.lastReq.Input["scopes"].GetListValue().AsSlice()
	assert.Equal(t, []interface{}{"read", "write"}, scopes)
}

func TestNewPolicyMiddlewareForwardsParsedJWTScopes(t *testing.T) {
	tests := []struct {
		name      string
		jwtScopes []interface{}
	}{
		{name: "JWT with read scope", jwtScopes: []interface{}{"read"}},
		{name: "JWT with multiple scopes", jwtScopes: []interface{}{"read", "write", "admin"}},
		{name: "JWT with no scopes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &stubPolicyClient{result: allowResult(nil)}
			jwtClaims := jwt.MapClaims{
				"sub":  "user456",
				"name": "JWT User",
			}
			if len(tt.jwtScopes) > 0 {
				jwtClaims["scopes"] = tt.jwtScopes
			}

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.Header.Set("Authorization", "Bearer test-token")
			req = req.WithContext(context.WithValue(req.Context(), claimsContextKey, jwtClaims))

			recorder, _ := servePolicy(t, client, req)
			assert.Equal(t, http.StatusOK, recorder.Code)
			require.NotNil(t, client.lastReq)

			scopesValue, exists := client.lastReq.Input["scopes"]
			if len(tt.jwtScopes) == 0 {
				assert.False(t, exists)
				return
			}
			require.True(t, exists)
			assert.Equal(t, tt.jwtScopes, scopesValue.GetListValue().AsSlice())
		})
	}
}

func TestNewPolicyMiddlewareDeniesWhenEvaluatorRejectsMissingScopes(t *testing.T) {
	denying := &stubPolicyClient{
		result: map[string]interface{}{
			"allow":      false,
			"statusCode": 403,
			"reasons":    []interface{}{"missing required scopes"},
		},
	}

	jwtClaims := jwt.MapClaims{
		"sub":  "user456",
		"name": "JWT User",
	}
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	req = req.WithContext(context.WithValue(req.Context(), claimsContextKey, jwtClaims))

	recorder, _ := servePolicy(t, denying, req)
	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), http.StatusText(http.StatusForbidden))
	_, exists := denying.lastReq.Input["scopes"]
	assert.False(t, exists)
}

func TestNewAuthMiddlewareRejectsJWTShapedTokenWhenParsingFails(t *testing.T) {
	client := &stubPolicyClient{result: allowResult(nil)}
	jwtOpts := NewJWTParserOptions(
		"https://issuer.test/.well-known/jwks.json",
		nil,
		time.Minute,
		&config.HTTPClientConfig{},
	)

	authMiddleware := NewAuthMiddleware(client, "test-service", &jwtOpts, jwk.NewCache(context.Background()), true, testLogger(t))
	handlerCalled := false
	handler := authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer not.valid.jwt")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
	assert.False(t, client.called)
	assert.False(t, handlerCalled)
}

func TestNewAuthMiddlewareRejectsRequestsWithNilClientAndLogger(t *testing.T) {
	authMiddleware := NewAuthMiddleware(nil, "test-service", nil, nil, true, nil)

	handlerCalled := false
	handler := authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	assert.False(t, handlerCalled)
}

func TestSelfManagedJWTNeverReachesPolicyDecisionPoint(t *testing.T) {
	key, jwksURL, closeServer := newSigningKeyAndJWKS(t)
	defer closeServer()

	jwtOpts := NewJWTParserOptions(jwksURL, nil, time.Minute, &config.HTTPClientConfig{})
	jwkCache := jwk.NewCache(context.Background(), jwk.WithRefreshWindow(time.Minute))

	client := &stubPolicyClient{result: allowResult(nil)}
	handler := newAuthTestHandler(t, &jwtOpts, jwkCache, client, true, WriteScopes)

	req := httptest.NewRequest(http.MethodPost, "/v3/ledger/cloudevents", nil)
	req.Header.Set("Authorization", "Bearer "+signToken(t, key, []string{"fnds:createEvent"}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.False(t, client.called, "OpenBao JWT must not be sent to api-keys-api")
}

func TestSelfManagedJWTScopesEnforcedByRoute(t *testing.T) {
	key, jwksURL, closeServer := newSigningKeyAndJWKS(t)
	defer closeServer()

	jwtOpts := NewJWTParserOptions(jwksURL, nil, time.Minute, &config.HTTPClientConfig{})
	jwkCache := jwk.NewCache(context.Background(), jwk.WithRefreshWindow(time.Minute))

	tests := []struct {
		name       string
		tokenScope []string
		required   Scopes
		wantStatus int
	}{
		{"write scope on write route", []string{"fnds:createEvent"}, WriteScopes, http.StatusOK},
		{"archive scope on archive route", []string{"fnds:archiveEvents"}, ArchiveScopes, http.StatusOK},
		{"write scope on read route", []string{"fnds:createEvent"}, ReadScopes, http.StatusForbidden},
		{"read scope on read route", []string{"fnds:getEvents"}, ReadScopes, http.StatusOK},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &stubPolicyClient{result: allowResult(nil)}
			handler := newAuthTestHandler(t, &jwtOpts, jwkCache, client, true, tc.required)

			req := httptest.NewRequest(http.MethodGet, "/v3/ledger/namespace/nvcf/events", nil)
			req.Header.Set("Authorization", "Bearer "+signToken(t, key, tc.tokenScope))
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, req)

			assert.Equal(t, tc.wantStatus, recorder.Code, recorder.Body.String())
			assert.False(t, client.called)
		})
	}
}

func TestAPIKeySkipsJWTVerificationAndScopeCheck(t *testing.T) {
	client := &stubPolicyClient{result: map[string]interface{}{
		"allowed": true,
		"ncaId":   "nca-1",
		"ownerId": "owner-1",
	}}
	handler := newAuthTestHandler(t, nil, nil, client, true, ReadScopes)

	req := httptest.NewRequest(http.MethodGet, "/v3/ledger/namespace/nvcf/events", nil)
	req.Header.Set("Authorization", "Bearer nvapi-opaque-key")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.True(t, client.called, "API key must be authorized by api-keys-api")
}

func TestAPIKeyDeniedByPolicyDecisionPoint(t *testing.T) {
	client := &stubPolicyClient{result: map[string]interface{}{
		"allowed": false,
		"ncaId":   "nca-1",
		"ownerId": "owner-1",
	}}
	handler := newAuthTestHandler(t, nil, nil, client, true, ReadScopes)

	req := httptest.NewRequest(http.MethodGet, "/v3/ledger/namespace/nvcf/events", nil)
	req.Header.Set("Authorization", "Bearer nvapi-opaque-key")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	// api-keys-api omits statusCode, which PolicyAuthzResponse defaults to 403.
	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.True(t, client.called)
}

func TestManagedJWTStillDelegatesToPolicyDecisionPoint(t *testing.T) {
	key, jwksURL, closeServer := newSigningKeyAndJWKS(t)
	defer closeServer()

	jwtOpts := NewJWTParserOptions(jwksURL, nil, time.Minute, &config.HTTPClientConfig{})
	jwkCache := jwk.NewCache(context.Background(), jwk.WithRefreshWindow(time.Minute))

	client := &stubPolicyClient{result: allowResult(nil)}
	authMiddleware := NewAuthMiddleware(client, "nv-cloud-functions", &jwtOpts, jwkCache, false, testLogger(t))

	var capturedCtx context.Context
	handler := authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedCtx = r.Context()
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v3/ledger/namespace/nvcf/events", nil)
	req.Header.Set("Authorization", "Bearer "+signToken(t, key, []string{"fnds:getEvents"}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.True(t, client.called, "managed deployments must still consult the PDP")
	require.NotNil(t, capturedCtx)
	assert.Equal(t, "sis-api", GetClaims(capturedCtx)["sub"])
}
