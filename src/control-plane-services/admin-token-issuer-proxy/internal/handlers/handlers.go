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
	"encoding/json"
	"log"
	"net/http"

	"github.com/NVIDIA/nvcf/src/control-plane-services/admin-token-issuer-proxy/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/admin-token-issuer-proxy/internal/models"
	"github.com/NVIDIA/nvcf/src/control-plane-services/admin-token-issuer-proxy/internal/platform/vault"
	"github.com/NVIDIA/nvcf/src/control-plane-services/admin-token-issuer-proxy/internal/servicecache"
)

// vaultClientFactory is a function that creates a VaultSigner.
// Used for dependency injection in tests.
type vaultClientFactory func(addr string) (vault.VaultSigner, error)

// ServiceCache defines the interface for accessing cached service metadata
type ServiceCache interface {
	Get() *models.ServiceInfo
	IsReady() bool
}

// Handlers holds HTTP handlers and their dependencies
type Handlers struct {
	cfg            *config.Config
	newVaultClient vaultClientFactory
	serviceCache   ServiceCache
}

// New creates a new Handlers instance
func New(cfg *config.Config, cache ServiceCache) *Handlers {
	return &Handlers{
		cfg:            cfg,
		newVaultClient: vault.NewVaultClient,
		serviceCache:   cache,
	}
}

// NewWithCache creates a new Handlers instance with a real service cache
func NewWithCache(cfg *config.Config) *Handlers {
	cache := servicecache.New(cfg.ServiceMetadataURL)
	return New(cfg, cache)
}

// Health provides a simple health check endpoint
func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Debug {
		log.Printf("%s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("OK")); err != nil {
		log.Printf("Failed to write health check response: %v", err)
	}
}

// Ready reports whether the dependency-backed service metadata is initialized.
func (h *Handlers) Ready(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Debug {
		log.Printf("%s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.serviceCache.IsReady() {
		http.Error(w, "Service metadata not ready", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("OK")); err != nil {
		log.Printf("Failed to write readiness response: %v", err)
	}
}

// Keys handles POST /v1/admin/keys requests
func (h *Handlers) Keys(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Debug {
		log.Printf("%s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.serviceCache.IsReady() {
		log.Printf("Service metadata not ready")
		http.Error(w, "Service not ready", http.StatusServiceUnavailable)
		return
	}

	// Read Vault token from file (provided by Vault Agent)
	token, err := vault.ReadTokenFile(h.cfg.VaultTokenFile)
	if err != nil {
		log.Printf("Failed to read Vault token: %v", err)
		http.Error(w, "Failed to authenticate with Vault", http.StatusInternalServerError)
		return
	}

	// Create Vault client
	vaultClient, err := h.newVaultClient(h.cfg.VaultAddr)
	if err != nil {
		log.Printf("Failed to create Vault client: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	vaultClient.SetToken(token)

	// Call Vault sign endpoint
	resp, err := vaultClient.SignToken(r.Context(), h.cfg.SignPath, h.cfg.Role)
	if err != nil {
		log.Printf("Failed to call Vault sign endpoint: %v", err)
		http.Error(w, "Failed to sign token", http.StatusInternalServerError)
		return
	}

	if resp == nil || resp.Data == nil {
		log.Printf("Invalid response from Vault")
		http.Error(w, "Invalid response from Vault", http.StatusInternalServerError)
		return
	}

	// Extract token from response
	tokenValue, ok := resp.Data["token"].(string)
	if !ok {
		log.Printf("Token not found in Vault response")
		http.Error(w, "Invalid response format", http.StatusInternalServerError)
		return
	}

	requestID := resp.RequestID
	if requestID == "" {
		requestID = "unknown"
	}

	// Decode JWT to extract claims
	claims, err := vault.DecodeJWTClaims(tokenValue)
	if err != nil {
		log.Printf("Failed to decode JWT claims: %v", err)
		http.Error(w, "Invalid token issued by Vault", http.StatusInternalServerError)
		return
	}

	// Get service metadata from cache.
	serviceInfo := h.serviceCache.Get()

	// Build owner_id from JWT subject or use system default
	ownerID := "admin-issuer-proxy"
	if claims.Sub != "" {
		ownerID = claims.Sub
	}

	// Use service metadata for audience (not JWT aud claim)
	// The issuer, audience, and policy aud/auds should all be the same service
	audienceServiceIDs := serviceInfo.AudienceServiceIDs

	// Build authorization policies matching api-keys format
	policies := []models.Policy{
		{
			Aud:     serviceInfo.ServiceID,
			Auds:    serviceInfo.AudienceServiceIDs,
			Product: "nv-cloud-functions",
			Resources: []models.Resource{
				{ID: "*", Type: "account-functions"},
				{ID: "*", Type: "authorized-functions"},
			},
			Scopes: claims.Scopes,
		},
	}

	// Transform response to match api-keys format
	adminResp := &models.AdminKeyResponse{
		ID:                 requestID,
		Value:              tokenValue,
		Status:             "ACTIVE",
		OwnerType:          "SYSTEM",
		OwnerID:            ownerID,
		IssuerServiceID:    serviceInfo.ServiceID,
		AudienceServiceIDs: audienceServiceIDs,
		Description:        "Admin token",
		CreatedAt:          models.FormatTime(claims.IAT),
		ExpiresAt:          models.FormatTime(claims.EXP),
		Authorizations: models.Authorizations{
			Policies: policies,
		},
	}

	// Return JSON response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(adminResp); err != nil {
		log.Printf("Failed to encode response: %v", err)
	}
}
