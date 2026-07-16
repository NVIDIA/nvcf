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

package oauth

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/plugins/oauth/test_helpers"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/plugins/types"
	"go.uber.org/zap/zaptest"
)

func TestOAuthPlugin(t *testing.T) {
	logger := zaptest.NewLogger(t)

	t.Run("Authentication Flow", func(t *testing.T) {
		var (
			configData Config
			server     *httptest.Server
		)
		setup := func() {
			server = test_helpers.CreateMockJWKSServer(t)
			configData = Config{
				JWKSEndpointURL: server.URL + "/.well-known/jwks.json",
				Issuer:          "https://test-issuer.example.com",
				Audience:        "test-account",
				ScopePermissions: map[string]*types.Permissions{
					"read": {
						Publish:   &types.PubPermissions{Allow: []string{"test.api.>"}},
						Subscribe: &types.SubPermissions{Allow: []string{"test.api.>", "_INBOX.>"}},
					},
					"write": {
						Publish:   &types.PubPermissions{Allow: []string{"test.api.>", "test.events.>"}},
						Subscribe: &types.SubPermissions{Allow: []string{"test.api.>", "test.events.>", "_INBOX.>"}},
					},
				},
			}
		}
		teardown := func() { server.Close() }

		t.Run("successful authentication returns valid permissions", func(t *testing.T) {
			setup()
			defer teardown()
			token := test_helpers.CreateValidJWT(t, "test-user", []string{"read", "write"})
			plugin, err := NewPlugin(configData, logger)
			assert.NoError(t, err)
			request := &types.Request{
				Account:    "test-account",
				PluginName: "oauth",
				Payload:    token,
			}
			result, err := plugin.Authenticate(context.Background(), request)
			assert.NoError(t, err)
			assert.NotNil(t, result)
		})

		t.Run("unauthorized for expired JWT", func(t *testing.T) {
			setup()
			defer teardown()
			token := test_helpers.CreateExpiredJWT(t, "test-user", []string{"read"})
			plugin, err := NewPlugin(configData, logger)
			assert.NoError(t, err)
			request := &types.Request{
				Account:    "test-account",
				PluginName: "oauth",
				Payload:    token,
			}
			result, err := plugin.Authenticate(context.Background(), request)
			assert.Error(t, err)
			assert.Nil(t, result)
		})

		t.Run("unauthorized for invalid audience", func(t *testing.T) {
			setup()
			defer teardown()
			token := test_helpers.CreateJWTWithAudience(t, "test-user", []string{"read"}, "invalid-audience")
			plugin, err := NewPlugin(configData, logger)
			assert.NoError(t, err)
			request := &types.Request{
				Account:    "test-account",
				PluginName: "oauth",
				Payload:    token,
			}
			result, err := plugin.Authenticate(context.Background(), request)
			assert.Error(t, err)
			assert.Nil(t, result)
		})

		t.Run("unauthorized for invalid scope", func(t *testing.T) {
			setup()
			defer teardown()
			token := test_helpers.CreateValidJWT(t, "test-user", []string{"invalid-scope"})
			plugin, err := NewPlugin(configData, logger)
			assert.NoError(t, err)
			request := &types.Request{
				Account:    "test-account",
				PluginName: "oauth",
				Payload:    token,
			}
			result, err := plugin.Authenticate(context.Background(), request)
			assert.Error(t, err)
			assert.Nil(t, result)
		})

		t.Run("unauthorized when JWT is malformed", func(t *testing.T) {
			setup()
			defer teardown()
			token := "invalid-token"
			plugin, err := NewPlugin(configData, logger)
			assert.NoError(t, err)
			request := &types.Request{
				Account:    "test-account",
				PluginName: "oauth",
				Payload:    token,
			}
			result, err := plugin.Authenticate(context.Background(), request)
			assert.Error(t, err)
			assert.Nil(t, result)
		})
	})

	t.Run("Configuration Validation", func(t *testing.T) {
		logger := zaptest.NewLogger(t)
		t.Run("when JWKSEndpointURL is missing", func(t *testing.T) {
			configData := map[string]any{}
			_, err := NewPlugin(configData, logger)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "jwks_endpoint_url is required")
		})
		t.Run("when issuer is missing", func(t *testing.T) {
			configData := Config{
				JWKSEndpointURL: "https://example.com",
			}
			_, err := NewPlugin(configData, logger)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "issuer is required")
		})
		t.Run("when audience is missing", func(t *testing.T) {
			configData := Config{
				JWKSEndpointURL: "https://example.com",
				Issuer:          "https://example.com",
			}
			_, err := NewPlugin(configData, logger)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "audience is required")
		})
		t.Run("with valid configuration parses correctly", func(t *testing.T) {
			configData := map[string]any{
				"jwks_endpoint_url": "https://example.com",
				"issuer":            "https://example.com",
				"audience":          "https://example.com",
				"scope_permissions": map[string]any{
					"foo": map[string]any{
						"publish": map[string]any{
							"allow": []string{"foo", "bar"},
							"deny":  []string{"baz"},
						},
						"subscribe": map[string]any{
							"allow": []string{"foo", "bar"},
							"deny":  []string{"baz"},
						},
						"response": map[string]any{
							"max_msgs": 10,
							"ttl":      "1s",
						},
					},
				},
			}
			plugin, err := NewPlugin(configData, logger)
			assert.NoError(t, err)
			assert.NotNil(t, plugin)
			assert.Equal(t, plugin.config.JWKSEndpointURL, "https://example.com")
			assert.Equal(t, plugin.config.Issuer, "https://example.com")
			assert.Equal(t, plugin.config.Audience, "https://example.com")
			// Additional checks for ScopePermissions can be added as needed
		})
	})
}
