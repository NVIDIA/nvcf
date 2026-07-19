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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoopAuthorizer(t *testing.T) {
	authorizer := NewNoopAuthorizer()
	ctx := context.Background()

	t.Run("nil request returns ErrUnauthorized", func(t *testing.T) {
		res, err := authorizer.Authorize(ctx, nil)
		require.ErrorIs(t, err, ErrUnauthorized)
		assert.Nil(t, res)
	})

	t.Run("valid request is allowed", func(t *testing.T) {
		req := &AuthRequest{
			PrincipalID: "test-user",
			Action:      ActionReadEvents,
			ResourceID:  "ledger-1",
		}
		res, err := authorizer.Authorize(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.True(t, res.Allowed)
		assert.Equal(t, "test-user", res.PrincipalID)
		assert.Equal(t, []string{"*"}, res.Scopes)
	})

	t.Run("empty principal defaults to anonymous", func(t *testing.T) {
		req := &AuthRequest{
			Action: ActionWriteEvents,
		}
		res, err := authorizer.Authorize(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.True(t, res.Allowed)
		assert.Equal(t, "anonymous", res.PrincipalID)
	})
}

func TestWebhookAuthorizer(t *testing.T) {
	ctx := context.Background()

	t.Run("empty endpoint URL returns error", func(t *testing.T) {
		authorizer, err := NewWebhookAuthorizer("")
		require.Error(t, err)
		assert.Nil(t, authorizer)
	})

	t.Run("successful allowed authorization", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

			var req AuthRequest
			err := json.NewDecoder(r.Body).Decode(&req)
			require.NoError(t, err)
			assert.Equal(t, ActionReadEvents, req.Action)

			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(AuthResult{
				Allowed:     true,
				PrincipalID: "webhook-user",
				Scopes:      []string{"read:events"},
			})
		}))
		defer server.Close()

		authorizer, err := NewWebhookAuthorizer(server.URL)
		require.NoError(t, err)

		res, err := authorizer.Authorize(ctx, &AuthRequest{Action: ActionReadEvents})
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.True(t, res.Allowed)
		assert.Equal(t, "webhook-user", res.PrincipalID)
		assert.Equal(t, []string{"read:events"}, res.Scopes)
	})

	t.Run("webhook returns unauthorized status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		authorizer, err := NewWebhookAuthorizer(server.URL)
		require.NoError(t, err)

		res, err := authorizer.Authorize(ctx, &AuthRequest{Action: ActionReadEvents})
		require.ErrorIs(t, err, ErrUnauthorized)
		assert.Nil(t, res)
	})

	t.Run("webhook returns forbidden status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer server.Close()

		authorizer, err := NewWebhookAuthorizer(server.URL)
		require.NoError(t, err)

		res, err := authorizer.Authorize(ctx, &AuthRequest{Action: ActionReadEvents})
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.False(t, res.Allowed)
		assert.Equal(t, "forbidden by policy", res.Reason)
	})

	t.Run("webhook returns unexpected status code", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		authorizer, err := NewWebhookAuthorizer(server.URL)
		require.NoError(t, err)

		res, err := authorizer.Authorize(ctx, &AuthRequest{Action: ActionReadEvents})
		require.ErrorIs(t, err, ErrAuthorizerInternal)
		assert.Nil(t, res)
	})

	t.Run("custom HTTP client configuration", func(t *testing.T) {
		customClient := &http.Client{Timeout: 1 * time.Millisecond}
		authorizer, err := NewWebhookAuthorizer("http://127.0.0.1:0", WithHTTPClient(customClient))
		require.NoError(t, err)
		assert.Equal(t, customClient, authorizer.client)
	})
}
