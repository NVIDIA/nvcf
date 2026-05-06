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

package utils

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testClientID     = "test-client-id"
	testClientSecret = "test-client-secret"
	testAccessToken  = "test-access-token"
)

func basicAuthHeader(clientID, clientSecret string) string {
	return fmt.Sprintf("Basic %s", base64.StdEncoding.EncodeToString([]byte(clientID+":"+clientSecret)))
}

func TestMockAuthCall_HappyPath(t *testing.T) {
	responder := MockAuthCall(t, testClientID, testClientSecret, testAccessToken)

	req, err := http.NewRequest(http.MethodPost, "http://example.com/token", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", basicAuthHeader(testClientID, testClientSecret))

	resp, err := responder(req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NoError(t, resp.Body.Close())
}

func TestMockAuthCallWithValidation_NoScopes(t *testing.T) {
	responder := MockAuthCallWithValidation(t, testClientID, testClientSecret, testAccessToken, nil, 3600)

	formData := url.Values{"grant_type": {"client_credentials"}}
	body := strings.NewReader(formData.Encode())
	req, err := http.NewRequest(http.MethodPost, "http://example.com/token", body)
	require.NoError(t, err)
	req.Header.Set("Authorization", basicAuthHeader(testClientID, testClientSecret))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := responder(req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NoError(t, resp.Body.Close())
}

func TestMockAuthCallWithValidation_WithScopes(t *testing.T) {
	scopes := []string{"openid", "profile"}
	responder := MockAuthCallWithValidation(t, testClientID, testClientSecret, testAccessToken, scopes, 3600)

	formData := url.Values{
		"grant_type": {"client_credentials"},
		"scope":      {strings.Join(scopes, " ")},
	}
	body := strings.NewReader(formData.Encode())
	req, err := http.NewRequest(http.MethodPost, "http://example.com/token", body)
	require.NoError(t, err)
	req.Header.Set("Authorization", basicAuthHeader(testClientID, testClientSecret))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := responder(req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NoError(t, resp.Body.Close())
}

func TestMockAuthErrorResponse(t *testing.T) {
	responder := MockAuthErrorResponse(http.StatusUnauthorized, "invalid_client", "bad credentials")

	req, err := http.NewRequest(http.MethodPost, "http://example.com/token", nil)
	require.NoError(t, err)

	resp, err := responder(req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.NoError(t, resp.Body.Close())
}

func TestMockAuthCallCounter_CountsCallsAndReturnsToken(t *testing.T) {
	callCount := 0
	responder := MockAuthCallCounter(t, testClientID, testClientSecret, testAccessToken, 3600, &callCount)

	req, err := http.NewRequest(http.MethodPost, "http://example.com/token", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", basicAuthHeader(testClientID, testClientSecret))

	resp, err := responder(req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 1, callCount)
	assert.NoError(t, resp.Body.Close())

	// Second invocation increments the counter
	resp2, err := responder(req)
	require.NoError(t, err)
	require.NotNil(t, resp2)
	assert.Equal(t, 2, callCount)
	assert.NoError(t, resp2.Body.Close())
}
