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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testActionRead  Action = "test.resource.read"
	testActionWrite Action = "test.resource.write"
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
			Action:      testActionRead,
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
			Action: testActionWrite,
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

	t.Run("nil option returns error", func(t *testing.T) {
		authorizer, err := NewWebhookAuthorizer("http://example.com", nil)
		require.Error(t, err)
		assert.Nil(t, authorizer)
	})

	t.Run("nil HTTP client returns error", func(t *testing.T) {
		authorizer, err := NewWebhookAuthorizer("http://example.com", WithHTTPClient(nil))
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
			assert.Equal(t, testActionRead, req.Action)

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

		res, err := authorizer.Authorize(ctx, &AuthRequest{Action: testActionRead})
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

		res, err := authorizer.Authorize(ctx, &AuthRequest{Action: testActionRead})
		require.ErrorIs(t, err, ErrUnauthorized)
		assert.Nil(t, res)
	})

	t.Run("webhook returns forbidden status with empty body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer server.Close()

		authorizer, err := NewWebhookAuthorizer(server.URL)
		require.NoError(t, err)

		res, err := authorizer.Authorize(ctx, &AuthRequest{Action: testActionRead})
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.False(t, res.Allowed)
		assert.Equal(t, "forbidden by policy", res.Reason)
	})

	t.Run("webhook returns forbidden status with body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("policy: read access denied for org"))
		}))
		defer server.Close()

		authorizer, err := NewWebhookAuthorizer(server.URL)
		require.NoError(t, err)

		res, err := authorizer.Authorize(ctx, &AuthRequest{Action: testActionRead})
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.False(t, res.Allowed)
		assert.Equal(t, "policy: read access denied for org", res.Reason)
	})

	t.Run("webhook returns unexpected status code with body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("upstream unavailable"))
		}))
		defer server.Close()

		authorizer, err := NewWebhookAuthorizer(server.URL)
		require.NoError(t, err)

		res, err := authorizer.Authorize(ctx, &AuthRequest{Action: testActionRead})
		require.ErrorIs(t, err, ErrAuthorizerInternal)
		assert.Contains(t, err.Error(), "upstream unavailable")
		assert.Nil(t, res)
	})

	t.Run("307 redirect does not replay POST to redirect target", func(t *testing.T) {
		redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// A replay reaching here would be a credential leak.
			t.Error("redirect target must never receive the POST")
			w.WriteHeader(http.StatusOK)
		}))
		defer redirectTarget.Close()

		primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, redirectTarget.URL, http.StatusTemporaryRedirect)
		}))
		defer primary.Close()

		authorizer, err := NewWebhookAuthorizer(primary.URL)
		require.NoError(t, err)

		res, err := authorizer.Authorize(ctx, &AuthRequest{
			Action:     testActionRead,
			Credential: "secret-token",
		})
		// The no-redirect policy causes the client to return the redirect response
		// directly, which is neither 200/401/403, so we expect ErrAuthorizerInternal.
		require.ErrorIs(t, err, ErrAuthorizerInternal)
		assert.Nil(t, res)
	})

	t.Run("308 redirect does not replay POST to redirect target", func(t *testing.T) {
		redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("redirect target must never receive the POST")
			w.WriteHeader(http.StatusOK)
		}))
		defer redirectTarget.Close()

		primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, redirectTarget.URL, http.StatusPermanentRedirect)
		}))
		defer primary.Close()

		authorizer, err := NewWebhookAuthorizer(primary.URL)
		require.NoError(t, err)

		res, err := authorizer.Authorize(ctx, &AuthRequest{
			Action:     testActionRead,
			Credential: "secret-token",
		})
		require.ErrorIs(t, err, ErrAuthorizerInternal)
		assert.Nil(t, res)
	})

	t.Run("injected client without redirect policy gets no-redirect applied on private copy", func(t *testing.T) {
		// A client with no CheckRedirect set receives noRedirectPolicy on a private copy;
		// the caller's original client is not mutated.
		injected := &http.Client{Timeout: 5 * time.Second}
		authorizer, err := NewWebhookAuthorizer("http://127.0.0.1:0", WithHTTPClient(injected))
		require.NoError(t, err)
		assert.NotNil(t, authorizer.client.CheckRedirect)
		// The constructor must never mutate the caller-owned client.
		assert.Nil(t, injected.CheckRedirect, "caller client must not be mutated")
		// The private copy must be a different pointer.
		assert.NotSame(t, injected, authorizer.client)
	})

	t.Run("injected client with custom CheckRedirect is still replaced by noRedirectPolicy", func(t *testing.T) {
		// A caller-supplied permissive redirect policy must be overridden so that
		// 307/308 responses cannot replay a POST carrying AuthRequest.Credential.
		allowRedirects := func(req *http.Request, via []*http.Request) error { return nil }
		injected := &http.Client{
			Timeout:       3 * time.Second,
			CheckRedirect: allowRedirects,
		}

		redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("redirect target must never receive the POST even with a custom CheckRedirect")
			w.WriteHeader(http.StatusOK)
		}))
		defer redirectTarget.Close()

		primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, redirectTarget.URL, http.StatusTemporaryRedirect)
		}))
		defer primary.Close()

		authorizer, err := NewWebhookAuthorizer(primary.URL, WithHTTPClient(injected))
		require.NoError(t, err)

		res, err := authorizer.Authorize(ctx, &AuthRequest{
			Action:     testActionRead,
			Credential: "secret-token",
		})
		// noRedirectPolicy causes the 307 to surface directly as ErrAuthorizerInternal,
		// not as a replayed POST to the redirect target.
		require.ErrorIs(t, err, ErrAuthorizerInternal)
		assert.Nil(t, res)
	})

	t.Run("injected client transport and timeout are preserved in private copy", func(t *testing.T) {
		customTransport := &http.Transport{MaxIdleConns: 7}
		injected := &http.Client{
			Transport: customTransport,
			Timeout:   3 * time.Second,
		}
		authorizer, err := NewWebhookAuthorizer("http://127.0.0.1:0", WithHTTPClient(injected))
		require.NoError(t, err)
		// Timeout must be carried over to the private copy.
		assert.Equal(t, 3*time.Second, authorizer.client.Timeout)
		// The private copy must enforce noRedirectPolicy.
		assert.NotNil(t, authorizer.client.CheckRedirect)
		// The private copy must be a different pointer from the injected client.
		assert.NotSame(t, injected, authorizer.client)
	})

	t.Run("custom HTTP client configuration uses private copy with noRedirectPolicy", func(t *testing.T) {
		customClient := &http.Client{
			Timeout:       1 * time.Millisecond,
			CheckRedirect: noRedirectPolicy,
		}
		authorizer, err := NewWebhookAuthorizer("http://127.0.0.1:0", WithHTTPClient(customClient))
		require.NoError(t, err)
		// The authorizer holds a private copy, not the original pointer.
		assert.NotSame(t, customClient, authorizer.client)
		// Timeout is preserved.
		assert.Equal(t, 1*time.Millisecond, authorizer.client.Timeout)
		// noRedirectPolicy is set.
		assert.NotNil(t, authorizer.client.CheckRedirect)
	})

	t.Run("transport failure returns ErrAuthorizerInternal", func(t *testing.T) {
		// Point at a port with no listener so the TCP dial fails immediately.
		authorizer, err := NewWebhookAuthorizer("http://127.0.0.1:1",
			WithHTTPClient(&http.Client{
				Timeout:       50 * time.Millisecond,
				CheckRedirect: noRedirectPolicy,
			}),
		)
		require.NoError(t, err)

		res, err := authorizer.Authorize(ctx, &AuthRequest{Action: testActionRead})
		require.ErrorIs(t, err, ErrAuthorizerInternal)
		assert.Nil(t, res)
	})

	t.Run("oversized response body returns ErrAuthorizerInternal", func(t *testing.T) {
		// Respond with a body larger than maxWebhookResponseBytes to verify the
		// size limit is enforced. io.LimitReader truncates cleanly so the read
		// itself does not error; the status is non-200 to exercise the error path.
		oversized := strings.Repeat("x", maxWebhookResponseBytes+1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(oversized))
		}))
		defer server.Close()

		authorizer, err := NewWebhookAuthorizer(server.URL)
		require.NoError(t, err)

		res, err := authorizer.Authorize(ctx, &AuthRequest{Action: testActionRead})
		require.ErrorIs(t, err, ErrAuthorizerInternal)
		// The error must not contain more than maxWebhookResponseBytes of body text.
		assert.LessOrEqual(t, len(err.Error()), maxWebhookResponseBytes+256)
		assert.Nil(t, res)
	})

	t.Run("malformed JSON on 200 returns ErrAuthorizerInternal", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("not-valid-json"))
		}))
		defer server.Close()

		authorizer, err := NewWebhookAuthorizer(server.URL)
		require.NoError(t, err)

		res, err := authorizer.Authorize(ctx, &AuthRequest{Action: testActionRead})
		require.ErrorIs(t, err, ErrAuthorizerInternal)
		assert.Nil(t, res)
	})
}
