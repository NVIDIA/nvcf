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

package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/NVIDIA/nvcf/src/invocation-plan-services/nats-auth-callout/internal/plugins/types"
	"go.uber.org/zap/zaptest"
)

// Test server response types for different scenarios.
type testServerResponse struct {
	statusCode   int
	responseBody any
	delay        time.Duration
}

// createTestServer creates an HTTP test server with configurable responses.
func createTestServer(responses []testServerResponse) *httptest.Server {
	requestCount := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requestCount >= len(responses) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		response := responses[requestCount]
		requestCount++
		if response.delay > 0 {
			time.Sleep(response.delay)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(response.statusCode)
		if response.responseBody != nil {
			json.NewEncoder(w).Encode(response.responseBody)
		}
	}))
}

// createSuccessfulResponse creates a successful webhook response.
func createSuccessfulResponse() Response {
	return Response{
		UserID:  "test-user-123",
		Account: "test-account",
		Permissions: &types.Permissions{
			Publish: &types.PubPermissions{
				Allow: []string{"test.>"},
			},
			Subscribe: &types.SubPermissions{
				Allow: []string{"test.>"},
			},
		},
		TTL: time.Hour,
	}
}

func TestWebhookPlugin(t *testing.T) {
	logger := zaptest.NewLogger(t)

	t.Run("Configuration Validation", func(t *testing.T) {
		t.Run("when URL is missing", func(t *testing.T) {
			configData := map[string]any{}
			_, err := NewPlugin(configData, logger)
			assert.ErrorContains(t, err, "url is required")
		})
		t.Run("when configuration has empty URL", func(t *testing.T) {
			configData := Config{URL: ""}
			_, err := NewPlugin(configData, logger)
			assert.ErrorContains(t, err, "url is required")
		})
		t.Run("with valid configuration applies defaults", func(t *testing.T) {
			configData := map[string]any{"url": "https://example.com/webhook"}
			plugin, err := NewPlugin(configData, logger)
			assert.NoError(t, err)
			assert.NotNil(t, plugin)
			assert.Equal(t, "https://example.com/webhook", plugin.config.URL)
			assert.Equal(t, 30*time.Second, plugin.config.Timeout)
			assert.Equal(t, 3, plugin.config.RetryAttempts)
		})
		t.Run("with map[string]any configuration parses correctly", func(t *testing.T) {
			configData := map[string]any{
				"url":                  "https://example.com/webhook",
				"timeout":              "30s",
				"retry_attempts":       5,
				"insecure_skip_verify": true,
			}
			plugin, err := NewPlugin(configData, logger)
			assert.NoError(t, err)
			assert.NotNil(t, plugin)
			assert.Equal(t, "https://example.com/webhook", plugin.config.URL)
			assert.Equal(t, 30*time.Second, plugin.config.Timeout)
			assert.Equal(t, 5, plugin.config.RetryAttempts)
			assert.True(t, plugin.config.InsecureSkipVerify)
		})
		t.Run("with struct configuration parses correctly", func(t *testing.T) {
			configData := Config{
				URL:                "https://example.com/webhook",
				Timeout:            25 * time.Second,
				RetryAttempts:      2,
				InsecureSkipVerify: false,
			}
			plugin, err := NewPlugin(configData, logger)
			assert.NoError(t, err)
			assert.NotNil(t, plugin)
			assert.Equal(t, "https://example.com/webhook", plugin.config.URL)
			assert.Equal(t, 25*time.Second, plugin.config.Timeout)
			assert.Equal(t, 2, plugin.config.RetryAttempts)
			assert.False(t, plugin.config.InsecureSkipVerify)
		})
	})

	t.Run("Authentication Flow", func(t *testing.T) {
		t.Run("successful authentication with valid response", func(t *testing.T) {
			successResponse := createSuccessfulResponse()
			server := createTestServer([]testServerResponse{{statusCode: http.StatusOK, responseBody: successResponse}})
			defer server.Close()
			plugin, err := NewPlugin(Config{URL: server.URL, Timeout: 5 * time.Second, RetryAttempts: 1}, logger)
			assert.NoError(t, err)
			assert.NotNil(t, plugin)
			authReq := &types.Request{Account: "test-account", PluginName: "webhook", Payload: "test-token"}
			result, err := plugin.Authenticate(context.Background(), authReq)
			assert.NoError(t, err)
			assert.NotNil(t, result)
			assert.Equal(t, "test-user-123", result.UserID)
			assert.Equal(t, "test-account", result.Account)
			assert.Equal(t, time.Hour, result.TTL)
			assert.NotNil(t, result.Permissions)
			assert.Equal(t, []string{"test.>"}, result.Permissions.Publish.Allow)
			assert.Equal(t, []string{"test.>"}, result.Permissions.Subscribe.Allow)
		})

		t.Run("should send correct request format", func(t *testing.T) {
			var receivedRequest types.Request
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					t.Errorf("expected POST method, got %s", r.Method)
				}
				if r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
				}
				if r.Header.Get("User-Agent") != "auth-callout-service/1.0" {
					t.Errorf("expected User-Agent auth-callout-service/1.0, got %s", r.Header.Get("User-Agent"))
				}
				err := json.NewDecoder(r.Body).Decode(&receivedRequest)
				if err != nil {
					t.Fatalf("failed to decode request body: %v", err)
				}
				response := createSuccessfulResponse()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()

			plugin, err := NewPlugin(Config{URL: server.URL, Timeout: 5 * time.Second, RetryAttempts: 1}, logger)
			assert.NoError(t, err)
			assert.NotNil(t, plugin)
			authReq := &types.Request{Account: "my-account", PluginName: "my-plugin", Payload: "my-token"}
			_, err = plugin.Authenticate(context.Background(), authReq)
			assert.NoError(t, err)
			assert.Equal(t, "my-account", receivedRequest.Account)
			assert.Equal(t, "my-plugin", receivedRequest.PluginName)
			assert.Equal(t, "my-token", receivedRequest.Payload)
		})
	})

	t.Run("Error Handling", func(t *testing.T) {
		t.Run("authentication failures", func(t *testing.T) {
			t.Run("should return unauthorized error for 401 status", func(t *testing.T) {
				server := createTestServer([]testServerResponse{{statusCode: http.StatusUnauthorized}})
				defer server.Close()
				plugin, err := NewPlugin(Config{URL: server.URL, Timeout: 5 * time.Second, RetryAttempts: 1}, logger)
				assert.NoError(t, err)
				authReq := &types.Request{Account: "test-account", PluginName: "webhook", Payload: "invalid-token"}
				_, err = plugin.Authenticate(context.Background(), authReq)
				assert.Error(t, err)
				var authErr *types.Error
				assert.True(t, errors.As(err, &authErr), "expected error to be of type *types.Error, got %T", err)
				assert.Equal(t, types.ErrTypeUnauthorized, authErr.Type)
				assert.Equal(t, 401, authErr.Code)
				assert.False(t, authErr.IsRetryable())
			})

			t.Run("should return unauthorized error for 403 status", func(t *testing.T) {
				server := createTestServer([]testServerResponse{{statusCode: http.StatusForbidden}})
				defer server.Close()
				plugin, err := NewPlugin(Config{URL: server.URL, Timeout: 5 * time.Second, RetryAttempts: 1}, logger)
				assert.NoError(t, err)
				authReq := &types.Request{Account: "test-account", PluginName: "webhook", Payload: "forbidden-token"}
				_, err = plugin.Authenticate(context.Background(), authReq)
				assert.Error(t, err)
				var authErr *types.Error
				assert.True(t, errors.As(err, &authErr), "expected error to be of type *types.Error, got %T", err)
				assert.Equal(t, types.ErrTypeUnauthorized, authErr.Type)
				assert.Equal(t, 403, authErr.Code)
				assert.False(t, authErr.IsRetryable())
			})

			t.Run("should return internal error for 500 status", func(t *testing.T) {
				server := createTestServer([]testServerResponse{{statusCode: http.StatusInternalServerError}})
				defer server.Close()
				plugin, err := NewPlugin(Config{URL: server.URL, Timeout: 5 * time.Second, RetryAttempts: 1}, logger)
				assert.NoError(t, err)
				authReq := &types.Request{Account: "test-account", PluginName: "webhook", Payload: "test-token"}
				_, err = plugin.Authenticate(context.Background(), authReq)
				assert.Error(t, err)
				var authErr *types.Error
				assert.True(t, errors.As(err, &authErr), "expected error to be of type *types.Error, got %T", err)
				assert.Equal(t, types.ErrTypeInternalError, authErr.Type)
				assert.Equal(t, 500, authErr.Code)
			})

			t.Run("should return internal error for malformed JSON response", func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					w.Write([]byte("invalid-json{"))
				}))
				defer server.Close()
				plugin, err := NewPlugin(Config{URL: server.URL, Timeout: 5 * time.Second, RetryAttempts: 1}, logger)
				assert.NoError(t, err)
				authReq := &types.Request{Account: "test-account", PluginName: "webhook", Payload: "test-token"}
				_, err = plugin.Authenticate(context.Background(), authReq)
				assert.Error(t, err)
				var authErr *types.Error
				assert.True(t, errors.As(err, &authErr), "expected error to be of type *types.Error, got %T", err)
				assert.Equal(t, types.ErrTypeInternalError, authErr.Type)
				assert.Equal(t, 500, authErr.Code)
				assert.Contains(t, authErr.Message, "failed to decode webhook response")
			})
		})
	})

	t.Run("Retry Logic", func(t *testing.T) {
		t.Run("successful retry after failure", func(t *testing.T) {
			t.Run("should succeed on second attempt after first failure", func(t *testing.T) {
				successResponse := createSuccessfulResponse()
				server := createTestServer([]testServerResponse{
					{statusCode: http.StatusInternalServerError},
					{statusCode: http.StatusOK, responseBody: successResponse},
				})
				defer server.Close()
				plugin, err := NewPlugin(Config{URL: server.URL, Timeout: 5 * time.Second, RetryAttempts: 3}, logger)
				assert.NoError(t, err)
				authReq := &types.Request{Account: "test-account", PluginName: "webhook", Payload: "test-token"}
				result, err := plugin.Authenticate(context.Background(), authReq)
				assert.NoError(t, err)
				assert.NotNil(t, result)
				assert.Equal(t, "test-user-123", result.UserID)
			})
		})

		t.Run("retry exhaustion", func(t *testing.T) {
			t.Run("should fail after max retry attempts", func(t *testing.T) {
				server := createTestServer([]testServerResponse{
					{statusCode: http.StatusInternalServerError},
					{statusCode: http.StatusInternalServerError},
					{statusCode: http.StatusInternalServerError},
				})
				defer server.Close()
				plugin, err := NewPlugin(Config{URL: server.URL, Timeout: 5 * time.Second, RetryAttempts: 3}, logger)
				assert.NoError(t, err)
				assert.NotNil(t, plugin)
				authReq := &types.Request{Account: "test-account", PluginName: "webhook", Payload: "test-token"}
				_, err = plugin.Authenticate(context.Background(), authReq)
				var authErr *types.Error
				assert.ErrorAs(t, err, &authErr)
				assert.Equal(t, types.ErrTypeInternalError, authErr.Type)
			})
		})

		t.Run("no retry for authentication errors", func(t *testing.T) {
			t.Run("should not retry 401 errors", func(t *testing.T) {
				callCount := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					callCount++
					w.WriteHeader(http.StatusUnauthorized)
				}))
				defer server.Close()
				plugin, err := NewPlugin(map[string]any{"url": server.URL, "timeout": "5s", "retry_attempts": 3}, logger)
				assert.NoError(t, err)
				assert.NotNil(t, plugin)
				authReq := &types.Request{Account: "test-account", PluginName: "webhook", Payload: "test-token"}
				_, err = plugin.Authenticate(context.Background(), authReq)
				assert.Error(t, err)
				assert.Equal(t, 1, callCount)
			})

			t.Run("should not retry 403 errors", func(t *testing.T) {
				callCount := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					callCount++
					w.WriteHeader(http.StatusForbidden)
				}))
				defer server.Close()
				plugin, err := NewPlugin(map[string]any{"url": server.URL, "timeout": "5s", "retry_attempts": 3}, logger)
				assert.NoError(t, err)
				assert.NotNil(t, plugin)
				authReq := &types.Request{Account: "test-account", PluginName: "webhook", Payload: "test-token"}
				_, err = plugin.Authenticate(context.Background(), authReq)
				assert.Error(t, err)
				assert.Equal(t, 1, callCount)
			})

			t.Run("should not retry other 4xx client errors", func(t *testing.T) {
				callCount := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					callCount++
					w.WriteHeader(http.StatusBadRequest) // 400
				}))
				defer server.Close()
				plugin, err := NewPlugin(map[string]any{"url": server.URL, "timeout": "5s", "retry_attempts": 3}, logger)
				assert.NoError(t, err)
				authReq := &types.Request{Account: "test-account", PluginName: "webhook", Payload: "test-token"}
				_, err = plugin.Authenticate(context.Background(), authReq)
				assert.Error(t, err)
				assert.Equal(t, 1, callCount)
			})

			t.Run("should not retry 408 timeout errors", func(t *testing.T) {
				callCount := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					callCount++
					w.WriteHeader(http.StatusRequestTimeout) // 408
				}))
				defer server.Close()
				plugin, err := NewPlugin(map[string]any{"url": server.URL, "timeout": "5s", "retry_attempts": 2}, logger)
				assert.NoError(t, err)
				authReq := &types.Request{Account: "test-account", PluginName: "webhook", Payload: "test-token"}
				_, err = plugin.Authenticate(context.Background(), authReq)
				assert.Error(t, err)
				assert.Equal(t, 1, callCount)
			})

			t.Run("should retry 429 rate limit errors", func(t *testing.T) {
				callCount := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					callCount++
					w.WriteHeader(http.StatusTooManyRequests) // 429
				}))
				defer server.Close()
				plugin, err := NewPlugin(map[string]any{"url": server.URL, "timeout": "5s", "retry_attempts": 2}, logger)
				assert.NoError(t, err)
				authReq := &types.Request{Account: "test-account", PluginName: "webhook", Payload: "test-token"}
				_, err = plugin.Authenticate(context.Background(), authReq)
				assert.Error(t, err)
				assert.Equal(t, 3, callCount)
			})
		})
	})
}
