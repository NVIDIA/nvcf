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

package test_e2e

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/plugins/oauth"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/plugins/oauth/test_helpers"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/plugins/types"
)

func TestOAuthPluginE2E(t *testing.T) {
	// Set up common test infrastructure
	setup := setupCommonTestInfrastructure(t)

	// Create mock JWKS server
	jwksServer := test_helpers.CreateMockJWKSServer(t)

	// Create test configuration for OAuth plugin
	testConfig := setup.createBaseConfig()
	testConfig.AccountConfigs["test-account"] = config.AccountConfig{
		EnabledPlugins: []config.EnabledPlugin{{ID: "oauth"}},
	}
	testConfig.PluginConfigs["oauth"] = config.PluginConfig{
		PluginType: "oauth",
		Config: oauth.Config{
			JWKSEndpointURL: jwksServer.URL + "/.well-known/jwks.json",
			Issuer:          "https://test-issuer.example.com",
			Audience:        "test-account",
			ScopePermissions: map[string]*types.Permissions{
				"read": {
					Publish: &types.PubPermissions{
						Allow: []string{"test.api.>"},
					},
					Subscribe: &types.SubPermissions{
						Allow: []string{"test.api.>", "_INBOX.>"},
					},
				},
				"write": {
					Publish: &types.PubPermissions{
						Allow: []string{"test.api.>", "test.events.>"},
					},
					Subscribe: &types.SubPermissions{
						Allow: []string{"test.api.>", "test.events.>", "_INBOX.>"},
					},
				},
			},
		},
	}

	// Start auth service with OAuth config
	setup.startAuthServiceWithConfig(t, testConfig)

	// Test successful authentication with valid JWT
	t.Run("successful_authentication_with_valid_jwt", func(t *testing.T) {
		// Create a valid JWT token
		validJWT := test_helpers.CreateValidJWT(t, "test-user", []string{"read", "write"})

		authReq := &types.Request{
			Account:    "test-account",
			PluginName: "oauth",
			Payload:    validJWT,
		}
		authReqBytes, err := json.Marshal(authReq)
		if err != nil {
			t.Fatalf("Failed to marshal auth request: %v", err)
		}
		token := base64.URLEncoding.EncodeToString(authReqBytes)

		nc, err := nats.Connect(setup.NATSServer.ClientURL(),
			nats.Token(token),
			nats.Timeout(5*time.Second))
		if err != nil {
			t.Fatalf("Expected successful connection with valid JWT, got error: %v", err)
		}
		defer nc.Close()

		// Verify connection is established
		if !nc.IsConnected() {
			t.Error("Expected NATS connection to be established")
		}

		// Test that we can publish to allowed subjects
		err = nc.Publish("test.api.hello", []byte("test message"))
		if err != nil {
			t.Errorf("Expected to be able to publish to test.api.hello, got error: %v", err)
		}

		err = nc.Publish("test.events.created", []byte("event message"))
		if err != nil {
			t.Errorf("Expected to be able to publish to test.events.created, got error: %v", err)
		}
	})

	// Test failed authentication with expired JWT
	t.Run("failed_authentication_with_expired_jwt", func(t *testing.T) {
		// Create an expired JWT token
		expiredJWT := test_helpers.CreateExpiredJWT(t, "test-user", []string{"read"})

		authReq := &types.Request{
			Account:    "test-account",
			PluginName: "oauth",
			Payload:    expiredJWT,
		}
		authReqBytes, err := json.Marshal(authReq)
		if err != nil {
			t.Fatalf("Failed to marshal auth request: %v", err)
		}
		token := base64.URLEncoding.EncodeToString(authReqBytes)

		_, err = nats.Connect(setup.NATSServer.ClientURL(),
			nats.Token(token),
			nats.Timeout(3*time.Second))
		if err == nil {
			t.Error("Expected connection to fail with expired JWT")
		}
	})

	// Test failed authentication with invalid audience
	t.Run("failed_authentication_with_invalid_audience", func(t *testing.T) {
		// Create JWT with wrong audience
		invalidAudienceJWT := test_helpers.CreateJWTWithAudience(t, "test-user", []string{"read"}, "wrong-account")

		authReq := &types.Request{
			Account:    "test-account",
			PluginName: "oauth",
			Payload:    invalidAudienceJWT,
		}
		authReqBytes, err := json.Marshal(authReq)
		if err != nil {
			t.Fatalf("Failed to marshal auth request: %v", err)
		}
		token := base64.URLEncoding.EncodeToString(authReqBytes)

		_, err = nats.Connect(setup.NATSServer.ClientURL(),
			nats.Token(token),
			nats.Timeout(3*time.Second))
		if err == nil {
			t.Error("Expected connection to fail with invalid audience")
		}
	})

	// Test failed authentication with no matching scopes
	t.Run("failed_authentication_with_no_matching_scopes", func(t *testing.T) {
		// Create JWT with unrecognized scopes
		noScopesJWT := test_helpers.CreateValidJWT(t, "test-user", []string{"admin", "superuser"})

		authReq := &types.Request{
			Account:    "test-account",
			PluginName: "oauth",
			Payload:    noScopesJWT,
		}
		authReqBytes, err := json.Marshal(authReq)
		if err != nil {
			t.Fatalf("Failed to marshal auth request: %v", err)
		}
		token := base64.URLEncoding.EncodeToString(authReqBytes)

		_, err = nats.Connect(setup.NATSServer.ClientURL(),
			nats.Token(token),
			nats.Timeout(3*time.Second))
		if err == nil {
			t.Error("Expected connection to fail with no matching scopes")
		}
	})

	// Test failed authentication with malformed JWT
	t.Run("failed_authentication_with_malformed_jwt", func(t *testing.T) {
		authReq := &types.Request{
			Account:    "test-account",
			PluginName: "oauth",
			Payload:    "malformed.jwt.token",
		}
		authReqBytes, err := json.Marshal(authReq)
		if err != nil {
			t.Fatalf("Failed to marshal auth request: %v", err)
		}
		token := base64.URLEncoding.EncodeToString(authReqBytes)

		_, err = nats.Connect(setup.NATSServer.ClientURL(),
			nats.Token(token),
			nats.Timeout(3*time.Second))
		if err == nil {
			t.Error("Expected connection to fail with malformed JWT")
		}
	})
}
