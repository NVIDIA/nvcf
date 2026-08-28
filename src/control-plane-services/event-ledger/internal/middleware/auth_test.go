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

type recordingPolicyClient struct {
	called  bool
	allowed bool
}

func (c *recordingPolicyClient) Evaluate(_ context.Context, _ *pdpv1.RuleRequest) (*pdpv1.RuleResponse, error) {
	c.called = true
	result, err := structpb.NewValue(map[string]interface{}{
		"allowed": c.allowed,
		"ncaId":   "nca-1",
		"ownerId": "owner-1",
	})
	if err != nil {
		return nil, err
	}
	return &pdpv1.RuleResponse{Result: result}, nil
}

func (c *recordingPolicyClient) PolicyConfig() *policyclient.PolicyConfig {
	return &policyclient.PolicyConfig{Namespace: "event-ledger", PolicyFQDN: "apikey.allow"}
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

func newAuthTestHandler(t *testing.T, jwtOpts *JWTParserOptions, jwkCache *jwk.Cache, client *recordingPolicyClient, selfManaged bool, requiredScopes Scopes) http.Handler {
	t.Helper()
	logger := otelzap.New(zaptest.NewLogger(t))
	authMiddleware := NewAuthMiddleware(client, "nv-cloud-functions", jwtOpts, jwkCache, selfManaged, logger)
	scoped := MaybeRequireScopes(logger, true, requiredScopes, RequireAnyScopes)
	return authMiddleware(scoped(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
}

func TestSelfManagedJWTNeverReachesPolicyDecisionPoint(t *testing.T) {
	key, jwksURL, closeServer := newSigningKeyAndJWKS(t)
	defer closeServer()

	jwtOpts := NewJWTParserOptions(jwksURL, nil, time.Minute, &config.HTTPClientConfig{})
	jwkCache := jwk.NewCache(context.Background(), jwk.WithRefreshWindow(time.Minute))

	client := &recordingPolicyClient{allowed: true}
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
			client := &recordingPolicyClient{allowed: true}
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
	client := &recordingPolicyClient{allowed: true}
	handler := newAuthTestHandler(t, nil, nil, client, true, ReadScopes)

	req := httptest.NewRequest(http.MethodGet, "/v3/ledger/namespace/nvcf/events", nil)
	req.Header.Set("Authorization", "Bearer nvapi-opaque-key")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.True(t, client.called, "API key must be authorized by api-keys-api")
}

func TestAPIKeyDeniedByPolicyDecisionPoint(t *testing.T) {
	client := &recordingPolicyClient{allowed: false}
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

	client := &recordingPolicyClient{allowed: true}
	logger := otelzap.New(zaptest.NewLogger(t))
	authMiddleware := NewAuthMiddleware(client, "nv-cloud-functions", &jwtOpts, jwkCache, false, logger)

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

func TestPolicyAuthzResponseAcceptsBothVerdictFieldNames(t *testing.T) {
	var apiKeysShaped PolicyAuthzResponse
	require.NoError(t, json.Unmarshal([]byte(`{"allowed":true}`), &apiKeysShaped))
	assert.True(t, apiKeysShaped.Allowed)
	assert.False(t, apiKeysShaped.Allow)

	var managedShaped PolicyAuthzResponse
	require.NoError(t, json.Unmarshal([]byte(`{"allow":true}`), &managedShaped))
	assert.True(t, managedShaped.Allow)
}
