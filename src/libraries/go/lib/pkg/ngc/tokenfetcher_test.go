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

package ngc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchToken(t *testing.T) {
	ctx := context.Background()

	// Error case no api keys specified
	tokenFetcher, err := NewTokenFetcher()
	assert.Error(t, err)
	assert.Nil(t, tokenFetcher)

	tokenFetcher, err = NewTokenFetcher(WithAuthAPIKey("some-api-key"))
	require.NoError(t, err)
	require.NotNil(t, tokenFetcher)
	token, err := tokenFetcher.FetchToken(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "some-api-key", token)

	failTokenRequest := &atomic.Bool{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failTokenRequest.Load() {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		tokenResp := struct {
			Token     string `json:"token"`
			ExpiresIn int    `json:"expires_in"`
		}{
			Token:     "some-jwt-value",
			ExpiresIn: 1000,
		}
		require.NoError(t, json.NewEncoder(w).Encode(tokenResp))
	}))
	t.Cleanup(func() { s.Close() })

	tokenFetcher, err = NewTokenFetcher(WithNGCAPIKey("some-api-key", "some-ngc-org"), WithNGCAuthURL(s.URL))
	require.NoError(t, err)
	require.NotNil(t, tokenFetcher)
	token, err = tokenFetcher.FetchToken(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "some-jwt-value", token)

	// Now fail the token request
	failTokenRequest.Store(true)
	token, err = tokenFetcher.FetchToken(ctx)
	assert.Error(t, err)
	assert.Empty(t, token)
}

func TestTokenFetcher_InvalidJSON(t *testing.T) {
	ctx := context.Background()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("invalid json"))
	}))
	defer s.Close()

	tokenFetcher, err := NewTokenFetcher(WithNGCAPIKey("test-key", "test-org"), WithNGCAuthURL(s.URL))
	require.NoError(t, err)
	token, err := tokenFetcher.FetchToken(ctx)
	assert.Error(t, err)
	assert.Empty(t, token)
}

func TestTokenFetcher_HTTPError(t *testing.T) {
	ctx := context.Background()
	// Server that returns error
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer s.Close()

	tokenFetcher, err := NewTokenFetcher(WithNGCAPIKey("test-key", "test-org"), WithNGCAuthURL(s.URL))
	require.NoError(t, err)
	token, err := tokenFetcher.FetchToken(ctx)
	assert.Error(t, err)
	assert.Empty(t, token)
}

func TestTokenFetcher_NetworkError(t *testing.T) {
	ctx := context.Background()
	// Use invalid URL to trigger network error
	tokenFetcher, err := NewTokenFetcher(WithNGCAPIKey("test-key", "test-org"), WithNGCAuthURL("http://invalid-host-that-does-not-exist:99999"))
	require.NoError(t, err)
	token, err := tokenFetcher.FetchToken(ctx)
	assert.Error(t, err)
	assert.Empty(t, token)
}

func TestTokenFetcher_AllOptions(t *testing.T) {
	ctx := context.Background()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request headers and auth
		username, password, ok := r.BasicAuth()
		assert.True(t, ok)
		assert.Equal(t, "$oauthtoken", username)
		assert.Equal(t, "test-api-key", password)

		tokenResp := struct {
			Token     string `json:"token"`
			ExpiresIn int    `json:"expires_in"`
		}{
			Token:     "test-token",
			ExpiresIn: 3600,
		}
		require.NoError(t, json.NewEncoder(w).Encode(tokenResp))
	}))
	defer s.Close()

	tokenFetcher, err := NewTokenFetcher(
		WithNGCAPIKey("test-api-key", "test-org"),
		WithNGCAuthURL(s.URL),
	)
	require.NoError(t, err)
	token, err := tokenFetcher.FetchToken(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "test-token", token)
}

func TestTokenFetcher_PriorityAuthAPIKey(t *testing.T) {
	ctx := context.Background()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Should not call NGC auth when AuthAPIKey is set")
	}))
	defer s.Close()

	// When both are set, AuthAPIKey takes priority
	tokenFetcher, err := NewTokenFetcher(
		WithNGCAPIKey("ngc-key", "test-org"),
		WithAuthAPIKey("auth-key"),
		WithNGCAuthURL(s.URL),
	)
	require.NoError(t, err)
	token, err := tokenFetcher.FetchToken(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "auth-key", token)
}
