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
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/nats-io/nats.go"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/plugins/types"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/plugins/webhook"
	"golang.org/x/sync/errgroup"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// createMockWebhookServer creates a mock webhook server for testing
func createMockWebhookServer(t *testing.T) *httptest.Server {
	webhookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Expected POST request, got %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/webhook" {
			t.Errorf("Expected /webhook path, got %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Parse request
		var webhookReq webhook.Request
		if err := json.NewDecoder(r.Body).Decode(&webhookReq); err != nil {
			t.Logf("Failed to decode webhook request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Create response based on token
		var response webhook.Response

		switch webhookReq.Payload {
		case "valid-token":
			response = webhook.Response{
				UserID:  "test-user",
				Account: "test-account",
				Permissions: &types.Permissions{
					Publish: &types.PubPermissions{
						Allow: []string{"test.>"},
					},
					Subscribe: &types.SubPermissions{
						Allow: []string{"test.>"},
					},
				},
			}
		case "slow-token":
			time.Sleep(100 * time.Millisecond)
			response = webhook.Response{
				UserID:  "test-user",
				Account: "test-account",
				Permissions: &types.Permissions{
					Publish: &types.PubPermissions{
						Allow: []string{"test.>"},
					},
					Subscribe: &types.SubPermissions{
						Allow: []string{"test.>"},
					},
				},
			}
		case "timeout-token":
			time.Sleep(2 * time.Second)
			w.WriteHeader(http.StatusUnauthorized)
			return
		case "server-error-token":
			w.WriteHeader(http.StatusInternalServerError)
			return
		default:
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Logf("Failed to encode webhook response: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(webhookServer.Close)
	return webhookServer
}

func TestWebhookPluginE2E(t *testing.T) {
	// Set up common test infrastructure
	setup := setupCommonTestInfrastructure(t)

	// Create mock webhook server
	webhookServer := createMockWebhookServer(t)

	// Create test configuration for webhook plugin
	testConfig := setup.createBaseConfig()
	testConfig.AccountConfigs["test-account"] = config.AccountConfig{
		EnabledPlugins: []config.EnabledPlugin{{ID: "webhook"}},
	}
	testConfig.PluginConfigs["webhook"] = config.PluginConfig{
		PluginType: "webhook",
		Config: webhook.Config{
			URL:     webhookServer.URL + "/webhook",
			Timeout: 1 * time.Second,
		},
	}

	// Start auth service with webhook config
	setup.startAuthServiceWithConfig(t, testConfig)

	// Test successful authentication
	t.Run("successful_authentication", func(t *testing.T) {
		t.Parallel()
		// Try to connect to NATS with valid token
		// The NATS server should call our auth service during connection
		authReq := &types.Request{
			Account:    "test-account",
			PluginName: "webhook",
			Payload:    "valid-token",
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
			t.Fatalf("Expected successful connection with valid token, got error: %v", err)
		}
		defer nc.Close()

		// Verify connection is established
		if !nc.IsConnected() {
			t.Error("Expected NATS connection to be established")
		}

		// Test that we can publish (proving auth worked)
		err = nc.Publish("test.subject", []byte("test message"))
		if err != nil {
			t.Errorf("Expected to be able to publish, got error: %v", err)
		}
	})

	// Test successful authentication trimmed b64
	t.Run("successful_authentication_trimmed_b64", func(t *testing.T) {
		t.Parallel()
		// Try to connect to NATS with valid token
		// The NATS server should call our auth service during connection
		authReq := &types.Request{
			Account:    "test-account",
			PluginName: "webhook",
			Payload:    "valid-token",
		}
		authReqBytes, err := json.Marshal(authReq)
		if err != nil {
			t.Fatalf("Failed to marshal auth request: %v", err)
		}
		token := base64.RawURLEncoding.EncodeToString(authReqBytes)
		nc, err := nats.Connect(setup.NATSServer.ClientURL(),
			nats.Token(token),
			nats.Timeout(5*time.Second))
		if err != nil {
			t.Fatalf("Expected successful connection with valid token, got error: %v", err)
		}
		defer nc.Close()

		// Verify connection is established
		if !nc.IsConnected() {
			t.Error("Expected NATS connection to be established")
		}

		// Test that we can publish (proving auth worked)
		err = nc.Publish("test.subject", []byte("test message"))
		if err != nil {
			t.Errorf("Expected to be able to publish, got error: %v", err)
		}
	})

	t.Run("successful_authentication_concurrent", func(t *testing.T) {
		t.Parallel()
		// use a token that will take a while to process, and run it in parallel to make sure the processing happens concurrently
		authReq := &types.Request{
			Account:    "test-account",
			PluginName: "webhook",
			Payload:    "slow-token",
		}
		authReqBytes, err := json.Marshal(authReq)
		if err != nil {
			t.Fatalf("Failed to marshal auth request: %v", err)
		}
		token := base64.URLEncoding.EncodeToString(authReqBytes)

		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		eg, ctx := errgroup.WithContext(ctx)
		for range 64 {
			eg.Go(func() error {
				nc, err := nats.Connect(setup.NATSServer.ClientURL(),
					nats.Token(token),
					nats.Timeout(5*time.Second))
				if err != nil {
					return fmt.Errorf("expected successful connection with valid token, got error: %v", err)
				}
				defer nc.Close()
				// Verify connection is established
				if !nc.IsConnected() {
					return fmt.Errorf("expected NATS connection to be established")
				}

				// Test that we can publish (proving auth worked)
				err = nc.Publish("test.subject", []byte("test message"))
				if err != nil {
					return fmt.Errorf("expected to be able to publish, got error: %v", err)
				}
				return nil
			})
		}
		if err := eg.Wait(); err != nil {
			t.Fatalf("Expected successful connections with valid token, got error: %v", err)
		}
	})

	// Test failed authentication
	t.Run("failed_authentication", func(t *testing.T) {
		t.Parallel()
		// Try to connect to NATS with invalid token
		authReq := &types.Request{
			Account:    "test-account",
			PluginName: "webhook",
			Payload:    "invalid-token",
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
			t.Error("Expected connection to fail with invalid token")
		}
	})

	// Test webhook timeout
	t.Run("webhook_timeout", func(t *testing.T) {
		t.Parallel()
		// Try to connect with token that causes webhook timeout
		authReq := &types.Request{
			Account:    "test-account",
			PluginName: "webhook",
			Payload:    "timeout-token",
		}
		authReqBytes, err := json.Marshal(authReq)
		if err != nil {
			t.Fatalf("Failed to marshal auth request: %v", err)
		}
		token := base64.URLEncoding.EncodeToString(authReqBytes)
		_, err = nats.Connect(setup.NATSServer.ClientURL(),
			nats.Token(token),
			nats.Timeout(10*time.Second))
		if err == nil {
			t.Error("Expected connection to fail due to webhook timeout")
		}
	})

	// Test webhook server error
	t.Run("webhook_server_error", func(t *testing.T) {
		t.Parallel()
		// Try to connect with token that causes server error
		authReq := &types.Request{
			Account:    "test-account",
			PluginName: "webhook",
			Payload:    "server-error-token",
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
			t.Error("Expected connection to fail due to webhook server error")
		}
	})
}

func TestWebhookPluginRetryLogic(t *testing.T) {
	// Set up common test infrastructure
	setup := setupCommonTestInfrastructure(t)

	// Create a webhook server that fails the first few requests
	retryCount := 0
	webhookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		retryCount++
		if retryCount < 3 {
			// Fail the first 2 requests
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// Succeed on the 3rd request
		var webhookReq types.Request
		json.NewDecoder(r.Body).Decode(&webhookReq)

		response := types.Result{
			UserID:  "test-user",
			Account: "test-account",
			Permissions: &types.Permissions{
				Publish: &types.PubPermissions{
					Allow: []string{"test.>"},
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer webhookServer.Close()

	// Create webhook plugin with retry configuration
	pluginConfig := map[string]any{
		"url":            webhookServer.URL,
		"timeout":        "1s",
		"retry_attempts": 3,
	}

	plugin, err := webhook.NewPlugin(pluginConfig, setup.Logger)
	if err != nil {
		t.Fatalf("Failed to create webhook plugin: %v", err)
	}

	// Test authentication with retries
	authReq := &types.Request{
		Account:    "test-account",
		PluginName: "webhook",
		Payload:    "valid-token",
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	result, err := plugin.Authenticate(ctx, authReq)
	if err != nil {
		t.Fatalf("Expected successful authentication after retries, got error: %v", err)
	}

	if result.UserID != "test-user" {
		t.Errorf("Expected UserID 'test-user', got: %s", result.UserID)
	}

	if result.Account != "test-account" {
		t.Errorf("Expected Account 'test-account', got: %s", result.Account)
	}

	// Verify that retries were attempted
	if retryCount != 3 {
		t.Errorf("Expected 3 webhook calls (2 failures + 1 success), got: %d", retryCount)
	}
}
