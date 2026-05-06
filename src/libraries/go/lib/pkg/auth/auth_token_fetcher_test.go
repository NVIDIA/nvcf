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

package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockTransport struct {
	sync.Mutex
	req *http.Request

	err  error
	code int
	body string
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	m.Lock()
	defer m.Unlock()
	m.req = req
	resp := &http.Response{StatusCode: m.code, Body: io.NopCloser(strings.NewReader(m.body))}
	return resp, m.err
}

func setupTokenFetcherTest(scope string, options ...TokenFetcherOption) (context.Context, *TokenFetcher, *mockTransport) {
	ctx := context.Background()

	f := NewTokenFetcher("http://localhost/v1/auth-token", "role1", "secret1", scope, options...)
	m := &mockTransport{
		code: http.StatusOK,
		body: `{
  "scope": "test-scope",
  "error": "",
  "token_type" : "bearer",
  "access_token": "test_jwt_token"
}`,
	}
	f.client.Transport = m
	return ctx, f, m
}

func TestRefreshClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	f := NewTokenFetcher("http://localhost/v1/auth-token", "clientID", "secretKey", "tokenScope")
	f.RefreshClient()

	f.FetchToken(ctx)
}

func TestAuthTokenFetcherOK(t *testing.T) {
	ctx, f, m := setupTokenFetcherTest("test-scope")

	token, err := f.FetchToken(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "test_jwt_token", token)
	assert.Equal(t, "Basic cm9sZTE6c2VjcmV0MQ==", m.req.Header.Get("Authorization"))
}

func TestFetcherFromFile(t *testing.T) {
	ctx := context.Background()
	f, err := NewTokenFetcherFromFile(ctx, "https://tokenurl", "tokenScope", "auth-clientID", "test/testfile.txt")
	require.NoError(t, err)
	f.RefreshClient()
}

func TestAuthTokenFetcherTransportFail(t *testing.T) {
	ctx, f, m := setupTokenFetcherTest("test-scope")

	f.RefreshClient()

	m.err = fmt.Errorf("network is down")

	_, err := f.FetchToken(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "network is down")
}

func TestAuthTokenFetcherNotStatusOK(t *testing.T) {
	ctx, f, m := setupTokenFetcherTest("test-scope")

	m.code = http.StatusUnauthorized
	m.body = "auth failed"

	f.RefreshClient()

	_, err := f.FetchToken(ctx)
	expErr := core.HTTPCodeError(0)
	assert.ErrorAs(t, err, &expErr)
	assert.EqualValues(t, http.StatusUnauthorized, expErr)
}

func TestAuthTokenFetcherWithStrictScopeVerification(t *testing.T) {
	ctx, f, m := setupTokenFetcherTest("scope_1 scope_2", WithScopeEnforcementEnabled(true))

	tokenResponse := authTokenResponse{
		Token: "test_jwt_token",
		Scope: "scope_2 scope_1",
	}

	b, err := json.Marshal(tokenResponse)
	require.NoError(t, err)

	m.code = http.StatusOK
	m.body = string(b)

	tok, err := f.FetchToken(ctx)
	assert.NotEmpty(t, tok)
	assert.NoError(t, err)

	// Now with it enabled and a non-match scope
	ctx, f, m = setupTokenFetcherTest("scope_1 scope_2 scope_3", WithScopeEnforcementEnabled(true))
	m.code = http.StatusOK
	m.body = string(b)
	tok, err = f.FetchToken(ctx)
	assert.Empty(t, tok)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), fmt.Sprintf("the requested scopes 'scope_1 scope_2 scope_3' did not match granted scopes 'scope_2 scope_1'. Please let the Auth service owner know that your client '%s' requires the following additional scopes: scope_3", f.clientID))
}

func TestVerifyStrictScopeEnforcement(t *testing.T) {
	tests := []struct {
		name           string
		clientID       string
		requestScopes  string
		responseScopes string
		wantErr        bool
	}{
		{
			name:           "scopes match",
			clientID:       "test-client",
			requestScopes:  "scope1 scope2",
			responseScopes: "scope2 scope1",
			wantErr:        false,
		},
		{
			name:           "scopes do not match",
			clientID:       "test-client",
			requestScopes:  "scope1 scope2",
			responseScopes: "scope2 scope3",
			wantErr:        true,
		},
		{
			name:           "scopes are empty",
			clientID:       "test-client",
			requestScopes:  "",
			responseScopes: "",
			wantErr:        false,
		},
		{
			name:           "request scopes are empty",
			clientID:       "test-client",
			requestScopes:  "",
			responseScopes: "scope1 scope2",
			wantErr:        true,
		},
		{
			name:           "response scopes are empty",
			clientID:       "test-client",
			requestScopes:  "scope1 scope2",
			responseScopes: "",
			wantErr:        true,
		},
		{
			name:           "scopes have duplicates",
			clientID:       "test-client",
			requestScopes:  "scope1 scope1",
			responseScopes: "scope1 scope1",
			wantErr:        false,
		},
		{
			name:           "scopes match unordered",
			clientID:       "test-client",
			requestScopes:  "scope2 scope1",
			responseScopes: "scope1 scope2",
			wantErr:        false,
		},
		{
			name:           "scopes match but one too many in request",
			clientID:       "test-client",
			requestScopes:  "scope2 scope1 scope3",
			responseScopes: "scope1 scope2",
			wantErr:        true,
		},
		{
			name:           "scopes match but more in response",
			clientID:       "test-client",
			requestScopes:  "scope2 scope1 scope3",
			responseScopes: "scope1 scope2 scope3 scope4",
			wantErr:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := verifyStrictScopeEnforcement(tt.clientID, tt.requestScopes, tt.responseScopes)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.requestScopes != tt.responseScopes {
					assert.Contains(t, err.Error(), fmt.Sprintf("the requested scopes '%s' did not match granted scopes '%s'. Please let the Auth service owner know that your client '%s' requires the following additional scopes:", tt.requestScopes, tt.responseScopes, tt.clientID))
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func Test_getClientSecretFromEnvFile(t *testing.T) {
	assert.Equal(t, "some-key", getClientSecretFromEnvFile("OAUTH_CLIENT_SECRET_KEY=some-key", ""))
	assert.Equal(t, "alt-key", getClientSecretFromEnvFile("OAUTH_CLIENT_SECRET_KEY=some-key\nOTHER=alt-key", "OTHER"))
	assert.Empty(t, getClientSecretFromEnvFile("", ""))
	// If prefix doesn't exist, return raw file contents as the secret
	assert.Equal(t, "some-key", getClientSecretFromEnvFile("some-key", ""))
	assert.Equal(t, "raw-secret-value", getClientSecretFromEnvFile("raw-secret-value", ""))
	assert.Equal(t, "multi\nline\nsecret", getClientSecretFromEnvFile("  multi\nline\nsecret  ", ""))
	assert.Equal(t, "some-key", getClientSecretFromEnvFile("\nOAUTH_CLIENT_SECRET_KEY=some-key\n", ""))
	assert.Equal(t, "some-key", getClientSecretFromEnvFile("\nOAUTH_CLIENT_SECRET_KEY=some-key   \n", ""))
}

func TestTokenFetcherResultListener(t *testing.T) {
	listener := &mockResultListener{}
	ctx, f, m := setupTokenFetcherTest("test-scope", WithResultListener(listener))

	token, err := f.FetchToken(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "test_jwt_token", token)
	assert.Equal(t, "Basic cm9sZTE6c2VjcmV0MQ==", m.req.Header.Get("Authorization"))
	assert.Equal(t, http.StatusOK, listener.respStatusCode)

	m.err = fmt.Errorf("network is down")
	token, err = f.FetchToken(ctx)
	assert.Error(t, err)
	assert.Equal(t, 0, listener.respStatusCode)
}

type mockResultListener struct {
	respStatusCode int
}

func (m *mockResultListener) OnFetchTokenResponse(respStatusCode int) {
	m.respStatusCode = respStatusCode
}
func TestAuthTokenFetcherInvalidJSON(t *testing.T) {
	ctx, f, m := setupTokenFetcherTest("test-scope")

	m.code = http.StatusOK
	m.body = `{invalid json`

	token, err := f.FetchToken(ctx)
	assert.Error(t, err)
	assert.Empty(t, token)
	assert.Contains(t, err.Error(), "failed to deserialize resp.Body as TokenResponse")
}

func TestAuthTokenFetcherEmptyToken(t *testing.T) {
	ctx, f, m := setupTokenFetcherTest("test-scope")

	tokenResponse := authTokenResponse{
		Token: "",
		Scope: "test-scope",
	}
	b, err := json.Marshal(tokenResponse)
	require.NoError(t, err)

	m.code = http.StatusOK
	m.body = string(b)

	token, err := f.FetchToken(ctx)
	assert.Error(t, err)
	assert.Empty(t, token)
	assert.Contains(t, err.Error(), "authTokenResponse.Token is empty")
}

func TestAuthTokenFetcherURLError(t *testing.T) {
	ctx := context.Background()

	// Invalid URL that will cause NewRequestWithContext to fail
	f := NewTokenFetcher("://invalid-url", "clientID", "secretKey", "scope")

	token, err := f.FetchToken(ctx)
	assert.Error(t, err)
	assert.Empty(t, token)
}

func TestAuthTokenFetcher_WithEnvKey(t *testing.T) {
	f := NewTokenFetcher("http://localhost/v1/auth-token", "clientID", "secretKey", "scope",
		WithEnvKey("CUSTOM_ENV_VAR"))
	assert.NotNil(t, f)
	assert.Equal(t, "CUSTOM_ENV_VAR", f.envNameFromFile)

	// Test getClientSecretFromEnvFile with custom env
	secret := getClientSecretFromEnvFile("CUSTOM_ENV_VAR=custom-secret", "CUSTOM_ENV_VAR")
	assert.Equal(t, "custom-secret", secret)
}

func TestAuthTokenFetcher_GetAuthClientSecretError(t *testing.T) {
	ctx := context.Background()

	// Create a fetcher with a getAuthClientSecret function that returns an error
	getAuthClientSecretFunc := func(*TokenFetcher) (string, error) {
		return "", fmt.Errorf("failed to get client secret")
	}

	f := newTokenFetcher("http://localhost/v1/auth-token", "scope", "clientID", getAuthClientSecretFunc)

	token, err := f.FetchToken(ctx)
	assert.Error(t, err)
	assert.Empty(t, token)
	assert.Contains(t, err.Error(), "failed to get client secret")
}

func TestAuthTokenFetcherReadBodyError(t *testing.T) {
	ctx, f, m := setupTokenFetcherTest("test-scope")

	m.code = http.StatusInternalServerError
	m.body = "" // Empty body

	token, err := f.FetchToken(ctx)
	assert.Error(t, err)
	assert.Empty(t, token)
	expErr := core.HTTPCodeError(0)
	assert.ErrorAs(t, err, &expErr)
	assert.EqualValues(t, http.StatusInternalServerError, expErr)
}

func TestNewTokenFetcherFromFile_Error(t *testing.T) {
	ctx := context.Background()

	// Try to create a fetcher with non-existent file
	_, err := NewTokenFetcherFromFile(ctx, "http://localhost/token", "scope", "client-id", "/nonexistent/file")
	assert.Error(t, err)
}
