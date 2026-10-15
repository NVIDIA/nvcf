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

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/admin-token-issuer-proxy/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/admin-token-issuer-proxy/internal/models"
	"github.com/NVIDIA/nvcf/src/control-plane-services/admin-token-issuer-proxy/internal/platform/vault"
	"github.com/hashicorp/vault/api"
)

// mockVaultClient is a mock implementation of the VaultSigner interface for testing.
type mockVaultClient struct {
	signFunc func(context.Context, string, string) (*api.Secret, error)
}

func (m *mockVaultClient) SetToken(token string) {}

func (m *mockVaultClient) SignToken(ctx context.Context, signPath, role string) (*api.Secret, error) {
	if m.signFunc != nil {
		return m.signFunc(ctx, signPath, role)
	}
	return nil, fmt.Errorf("mock signFunc not implemented")
}

// mockServiceCache is a mock implementation of the ServiceCache interface for testing.
type mockServiceCache struct {
	serviceInfo *models.ServiceInfo
	ready       bool
}

func (m *mockServiceCache) Get() *models.ServiceInfo {
	return m.serviceInfo
}

func (m *mockServiceCache) IsReady() bool {
	return m.ready
}

func newMockServiceCache() *mockServiceCache {
	return &mockServiceCache{
		serviceInfo: &models.ServiceInfo{
			ServiceID:          "example-service-id",
			ServiceName:        "nvcf-api",
			AudienceServiceIDs: []string{"example-service-id"},
		},
		ready: true,
	}
}

func TestHandlers(t *testing.T) {
	// Setup for success cases that need a valid token file
	tmpFile, err := os.CreateTemp("", "test-token-*")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	if _, err := tmpFile.WriteString("test-vault-token"); err != nil {
		t.Fatalf("failed to write to temp file: %v", err)
	}
	_ = tmpFile.Close()

	// Mock Vault client for success case
	mockSuccessClient := &mockVaultClient{
		signFunc: func(ctx context.Context, signPath, role string) (*api.Secret, error) {
			testToken := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE2NzI1MzEyMDAsImlhdCI6MTY3MjUyNzYwMCwic2NvcGVzIjpbInJlYWQiXX0.signature"
			return &api.Secret{
				RequestID: "test-request-id",
				Data:      map[string]interface{}{"token": testToken},
			}, nil
		},
	}

	testCases := []struct {
		name               string
		method             string
		path               string
		handlerFunc        func(h *Handlers, w http.ResponseWriter, r *http.Request)
		cfg                *config.Config
		mockVaultClient    *mockVaultClient
		mockServiceCache   *mockServiceCache
		newVaultClientErr  error
		expectedStatusCode int
		expectedBody       string
		validateJSON       func(t *testing.T, body *bytes.Buffer)
	}{
		// Health Handler Tests
		{
			name:               "health check success",
			method:             "GET",
			path:               "/healthz",
			handlerFunc:        func(h *Handlers, w http.ResponseWriter, r *http.Request) { h.Health(w, r) },
			cfg:                &config.Config{},
			expectedStatusCode: http.StatusOK,
			expectedBody:       "OK",
		},
		{
			name:               "health check method not allowed",
			method:             "POST",
			path:               "/healthz",
			handlerFunc:        func(h *Handlers, w http.ResponseWriter, r *http.Request) { h.Health(w, r) },
			cfg:                &config.Config{},
			expectedStatusCode: http.StatusMethodNotAllowed,
		},
		{
			name:               "readiness check reports unavailable before metadata initialization",
			method:             "GET",
			path:               "/readyz",
			handlerFunc:        func(h *Handlers, w http.ResponseWriter, r *http.Request) { h.Ready(w, r) },
			cfg:                &config.Config{},
			mockServiceCache:   &mockServiceCache{ready: false},
			expectedStatusCode: http.StatusServiceUnavailable,
			expectedBody:       "Service metadata not ready\n",
		},
		{
			name:               "readiness check succeeds after metadata initialization",
			method:             "GET",
			path:               "/readyz",
			handlerFunc:        func(h *Handlers, w http.ResponseWriter, r *http.Request) { h.Ready(w, r) },
			cfg:                &config.Config{},
			mockServiceCache:   newMockServiceCache(),
			expectedStatusCode: http.StatusOK,
			expectedBody:       "OK",
		},
		// Keys Handler Tests
		{
			name:        "keys success",
			method:      "POST",
			path:        "/v1/admin/keys",
			handlerFunc: func(h *Handlers, w http.ResponseWriter, r *http.Request) { h.Keys(w, r) },
			cfg: &config.Config{
				VaultTokenFile: tmpFile.Name(),
			},
			mockVaultClient:    mockSuccessClient,
			mockServiceCache:   newMockServiceCache(),
			expectedStatusCode: http.StatusOK,
			validateJSON: func(t *testing.T, body *bytes.Buffer) {
				var resp models.AdminKeyResponse
				if err := json.NewDecoder(body).Decode(&resp); err != nil {
					t.Fatalf("failed to decode response: %v", err)
				}
				if resp.ID != "test-request-id" {
					t.Errorf("expected ID 'test-request-id', got '%s'", resp.ID)
				}
				if resp.Status != "ACTIVE" {
					t.Errorf("expected status 'ACTIVE', got '%s'", resp.Status)
				}
				if resp.OwnerType != "SYSTEM" {
					t.Errorf("expected owner_type 'SYSTEM', got '%s'", resp.OwnerType)
				}
				if resp.IssuerServiceID != "example-service-id" {
					t.Errorf("expected issuer_service_id 'example-service-id', got '%s'", resp.IssuerServiceID)
				}
				if len(resp.Authorizations.Policies) == 0 {
					t.Error("expected at least one policy in authorizations")
				}
			},
		},
		{
			name:               "keys method not allowed",
			method:             "GET",
			path:               "/v1/admin/keys",
			handlerFunc:        func(h *Handlers, w http.ResponseWriter, r *http.Request) { h.Keys(w, r) },
			cfg:                &config.Config{},
			expectedStatusCode: http.StatusMethodNotAllowed,
		},
		{
			name:        "keys token file not found",
			method:      "POST",
			path:        "/v1/admin/keys",
			handlerFunc: func(h *Handlers, w http.ResponseWriter, r *http.Request) { h.Keys(w, r) },
			cfg: &config.Config{
				VaultTokenFile: "/nonexistent/file",
			},
			expectedStatusCode: http.StatusInternalServerError,
			expectedBody:       "Failed to authenticate with Vault\n",
		},
		{
			name:               "keys vault client creation fails",
			method:             "POST",
			path:               "/v1/admin/keys",
			handlerFunc:        func(h *Handlers, w http.ResponseWriter, r *http.Request) { h.Keys(w, r) },
			cfg:                &config.Config{VaultTokenFile: tmpFile.Name()},
			newVaultClientErr:  fmt.Errorf("boom"),
			expectedStatusCode: http.StatusInternalServerError,
			expectedBody:       "Internal server error\n",
		},
		{
			name:        "keys vault sign returns error",
			method:      "POST",
			path:        "/v1/admin/keys",
			handlerFunc: func(h *Handlers, w http.ResponseWriter, r *http.Request) { h.Keys(w, r) },
			cfg:         &config.Config{VaultTokenFile: tmpFile.Name()},
			mockVaultClient: &mockVaultClient{
				signFunc: func(ctx context.Context, signPath, role string) (*api.Secret, error) {
					return nil, fmt.Errorf("vault error")
				},
			},
			expectedStatusCode: http.StatusInternalServerError,
			expectedBody:       "Failed to sign token\n",
		},
		{
			name:        "keys rejects a signed token with invalid claims",
			method:      "POST",
			path:        "/v1/admin/keys",
			handlerFunc: func(h *Handlers, w http.ResponseWriter, r *http.Request) { h.Keys(w, r) },
			cfg:         &config.Config{VaultTokenFile: tmpFile.Name()},
			mockVaultClient: &mockVaultClient{
				signFunc: func(ctx context.Context, signPath, role string) (*api.Secret, error) {
					return &api.Secret{
						RequestID: "invalid-claims-request",
						Data:      map[string]interface{}{"token": "not-a-jwt"},
					}, nil
				},
			},
			mockServiceCache:   newMockServiceCache(),
			expectedStatusCode: http.StatusInternalServerError,
			expectedBody:       "Invalid token issued by Vault\n",
		},
		{
			name:        "keys service cache not ready",
			method:      "POST",
			path:        "/v1/admin/keys",
			handlerFunc: func(h *Handlers, w http.ResponseWriter, r *http.Request) { h.Keys(w, r) },
			cfg: &config.Config{
				VaultTokenFile: tmpFile.Name(),
			},
			mockVaultClient: mockSuccessClient,
			mockServiceCache: &mockServiceCache{
				serviceInfo: nil,
				ready:       false,
			},
			newVaultClientErr:  fmt.Errorf("vault must not be called while metadata is unready"),
			expectedStatusCode: http.StatusServiceUnavailable,
			expectedBody:       "Service not ready\n",
		},
		{
			name:        "keys with JWT containing aud and sub claims",
			method:      "POST",
			path:        "/v1/admin/keys",
			handlerFunc: func(h *Handlers, w http.ResponseWriter, r *http.Request) { h.Keys(w, r) },
			cfg: &config.Config{
				VaultTokenFile: tmpFile.Name(),
			},
			mockVaultClient: &mockVaultClient{
				signFunc: func(ctx context.Context, signPath, role string) (*api.Secret, error) {
					// JWT with aud (array) and sub claims
					// Payload: {"exp":1672531200,"iat":1672527600,"scopes":["read","write"],"aud":["custom-audience"],"sub":"user@example.com"}
					testToken := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE2NzI1MzEyMDAsImlhdCI6MTY3MjUyNzYwMCwic2NvcGVzIjpbInJlYWQiLCJ3cml0ZSJdLCJhdWQiOlsiY3VzdG9tLWF1ZGllbmNlIl0sInN1YiI6InVzZXJAZXhhbXBsZS5jb20ifQ.signature"
					return &api.Secret{
						RequestID: "test-request-with-claims",
						Data:      map[string]interface{}{"token": testToken},
					}, nil
				},
			},
			mockServiceCache:   newMockServiceCache(),
			expectedStatusCode: http.StatusOK,
			validateJSON: func(t *testing.T, body *bytes.Buffer) {
				var resp models.AdminKeyResponse
				if err := json.NewDecoder(body).Decode(&resp); err != nil {
					t.Fatalf("failed to decode response: %v", err)
				}
				if resp.OwnerID != "user@example.com" {
					t.Errorf("expected owner_id 'user@example.com', got '%s'", resp.OwnerID)
				}
				// audience_service_ids should come from service metadata, not JWT aud
				if len(resp.AudienceServiceIDs) != 1 || resp.AudienceServiceIDs[0] != "example-service-id" {
					t.Errorf("expected audience_service_ids from service metadata, got %v", resp.AudienceServiceIDs)
				}
				if len(resp.Authorizations.Policies) == 0 {
					t.Fatal("expected at least one policy")
				}
				policy := resp.Authorizations.Policies[0]
				// policy aud should come from service metadata, not JWT aud
				if policy.Aud != "example-service-id" {
					t.Errorf("expected policy aud from service metadata, got '%s'", policy.Aud)
				}
				if len(policy.Scopes) != 2 {
					t.Errorf("expected 2 scopes, got %d", len(policy.Scopes))
				}
				if policy.Product != "nv-cloud-functions" {
					t.Errorf("expected product 'nv-cloud-functions', got '%s'", policy.Product)
				}
				if len(policy.Resources) != 2 {
					t.Errorf("expected 2 resources, got %d", len(policy.Resources))
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Use provided mock cache or create a default one
			cache := tc.mockServiceCache
			if cache == nil {
				cache = newMockServiceCache()
			}

			h := &Handlers{
				cfg: tc.cfg,
				newVaultClient: func(addr string) (vault.VaultSigner, error) {
					if tc.newVaultClientErr != nil {
						return nil, tc.newVaultClientErr
					}
					return tc.mockVaultClient, nil
				},
				serviceCache: cache,
			}

			req := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)
			w := httptest.NewRecorder()
			tc.handlerFunc(h, w, req)

			if w.Code != tc.expectedStatusCode {
				t.Errorf("expected status %d, got %d", tc.expectedStatusCode, w.Code)
			}

			if tc.expectedBody != "" && w.Body.String() != tc.expectedBody {
				t.Errorf("expected body '%s', got '%s'", tc.expectedBody, w.Body.String())
			}

			if tc.validateJSON != nil {
				tc.validateJSON(t, w.Body)
			}
		})
	}
}

func TestKeysPropagatesRequestCancellationToVault(t *testing.T) {
	tokenFile := t.TempDir() + "/token"
	if err := os.WriteFile(tokenFile, []byte("test-vault-token"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	signStarted := make(chan struct{})
	signer := &mockVaultClient{
		signFunc: func(ctx context.Context, signPath, role string) (*api.Secret, error) {
			close(signStarted)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	h := &Handlers{
		cfg: &config.Config{
			VaultTokenFile: tokenFile,
		},
		newVaultClient: func(string) (vault.VaultSigner, error) {
			return signer, nil
		},
		serviceCache: newMockServiceCache(),
	}

	ctx, cancel := context.WithCancel(t.Context())
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/admin/keys", nil)
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.Keys(recorder, req)
		close(done)
	}()

	select {
	case <-signStarted:
	case <-time.After(time.Second):
		t.Fatal("Vault signing did not start")
	}
	cancel()

	select {
	case <-done:
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
		}
	case <-time.After(time.Second):
		t.Fatal("Keys did not stop after request cancellation")
	}
}
