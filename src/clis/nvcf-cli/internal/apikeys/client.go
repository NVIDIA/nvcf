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

package apikeys

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"nvcf-cli/internal/logging"
)

// Client provides API Keys service operations via direct HTTP calls
type Client struct {
	config     *Config
	httpClient *http.Client
}

// Config holds API Keys service configuration
type Config struct {
	ServiceURL    string
	ServiceID     string
	IssuerService string
	OwnerID       string
	JWTToken      string // JWT token for authentication
	HostHeader    string // Host header override for hostname-based routing (self-hosted)
	Debug         bool
}

// APIKey represents an API key returned by the service
type APIKey struct {
	ID          string    `json:"id"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	Status      string    `json:"status"`          // e.g., "active", "revoked"
	Value       string    `json:"value,omitempty"` // Only present on creation
}

var defaultScopes = []string{
	"invoke_function",
	"list_functions",
	"queue_details",
	"list_functions_details",
}

// DefaultScopes returns the scopes used when an API key is generated without
// an explicit --scopes value.
func DefaultScopes() []string {
	return append([]string(nil), defaultScopes...)
}

// NewClient creates a new API Keys service client
func NewClient(cfg *Config) *Client {
	// Build transport chain with host header override if configured
	var transport http.RoundTripper = http.DefaultTransport
	if cfg.HostHeader != "" {
		transport = &hostHeaderTransport{
			hostHeader: cfg.HostHeader,
			debug:      cfg.Debug,
			base:       transport,
		}
	}

	return &Client{
		config: cfg,
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
	}
}

// hostHeaderTransport is an HTTP transport that sets the Host header for hostname-based routing
type hostHeaderTransport struct {
	hostHeader string
	debug      bool
	base       http.RoundTripper
}

// RoundTrip implements the http.RoundTripper interface
func (t *hostHeaderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Host = t.hostHeader
	if t.debug {
		logging.Debug("Using Host header override: %s", t.hostHeader)
	}
	return t.base.RoundTrip(req)
}

// GenerateAPIKey creates a new API key via direct HTTP call
func (c *Client) GenerateAPIKey(ctx context.Context, description string, expiresAt time.Time, customScopes []string) (string, error) {
	if c.config.Debug {
		logging.Debug("Generating API key via direct HTTPS call to: %s", c.config.ServiceURL)
	}

	// Determine scopes to use - custom scopes if provided, otherwise default scopes
	scopes := customScopes
	if len(scopes) == 0 {
		// Default scopes for API keys used by invoke, list, and queue status commands.
		scopes = DefaultScopes()
		if c.config.Debug {
			logging.Debug("Using default API key scopes: %v", scopes)
		}
	} else {
		if c.config.Debug {
			logging.Debug("Using custom API key scopes: %v", scopes)
		}
	}

	// Build the API key request payload
	payload := map[string]interface{}{
		"description": description,
		"expires_at":  expiresAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		"authorizations": map[string]interface{}{
			"policies": []map[string]interface{}{
				{
					"aud":     c.config.ServiceID,
					"auds":    []string{c.config.ServiceID},
					"product": "nv-cloud-functions",
					"resources": []map[string]string{
						{"id": "*", "type": "account-functions"},
						{"id": "*", "type": "authorized-functions"},
					},
					"scopes": scopes,
				},
			},
		},
		"audience_service_ids": []string{c.config.ServiceID},
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal API key payload: %w", err)
	}

	if c.config.Debug {
		logging.Debug("API Key generation request payload: %s", string(payloadJSON))
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", c.config.ServiceURL+"/v1/keys", bytes.NewReader(payloadJSON))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.config.JWTToken)
	req.Header.Set("Key-Issuer-Service", c.config.IssuerService)
	req.Header.Set("Key-Issuer-Id", c.config.ServiceID)
	req.Header.Set("Key-Owner-Id", c.config.OwnerID)

	if c.config.Debug {
		logging.Debug("Request headers:")
		for name, values := range req.Header {
			for _, v := range values {
				if name == "Authorization" {
					logging.Debug("  %s: %s", name, redactBearerToken(v))
				} else {
					logging.Debug("  %s: %s", name, v)
				}
			}
		}
	}

	// Make the request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("API key generation request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if c.config.Debug {
		logging.Debug("API key generation response status: %d", resp.StatusCode)
		logging.Debug("API key generation response: %s", string(body))
	}

	// Check status code
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	// Parse the response
	var response map[string]interface{}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("failed to parse API key response: %w", err)
	}

	value, ok := response["value"].(string)
	if !ok || value == "" {
		return "", fmt.Errorf("API key value not found in response")
	}

	if c.config.Debug {
		logging.Debug("Successfully generated API key - Length: %d", len(value))
	}
	return value, nil
}

func redactBearerToken(value string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(value, prefix) {
		return "[REDACTED]"
	}
	token := strings.TrimPrefix(value, prefix)
	if len(token) <= 8 {
		return "Bearer [REDACTED]"
	}
	start := token[:4]
	end := token[len(token)-4:]
	return fmt.Sprintf("Bearer %s...%s", start, end)
}

// ListAPIKeys retrieves all API keys via direct HTTP call
func (c *Client) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	if c.config.Debug {
		logging.Debug("Listing API keys via direct HTTPS call to: %s", c.config.ServiceURL)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "GET", c.config.ServiceURL+"/v1/keys", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Authorization", "Bearer "+c.config.JWTToken)
	req.Header.Set("Key-Issuer-Service", c.config.IssuerService)
	req.Header.Set("Key-Issuer-Id", c.config.ServiceID)
	req.Header.Set("Key-Owner-Id", c.config.OwnerID)

	if c.config.Debug {
		logging.Debug("Request headers:")
		for name, values := range req.Header {
			for _, v := range values {
				if name == "Authorization" {
					logging.Debug("  %s: %s", name, redactBearerToken(v))
				} else {
					logging.Debug("  %s: %s", name, v)
				}
			}
		}
	}

	// Make the request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to list API keys: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if c.config.Debug {
		logging.Debug("List API keys response status: %d", resp.StatusCode)
		logging.Debug("List API keys response: %s", string(body))
	}

	// Check status code
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	// Parse the response
	var response struct {
		Keys []APIKey `json:"keys"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse API keys response: %w", err)
	}

	if c.config.Debug {
		logging.Debug("Successfully retrieved %d API keys", len(response.Keys))
	}
	return response.Keys, nil
}

// DeleteAPIKey deletes an API key via direct HTTP call
func (c *Client) DeleteAPIKey(ctx context.Context, keyID string) error {
	if c.config.Debug {
		logging.Debug("Deleting API key %s via direct HTTPS call to: %s", keyID, c.config.ServiceURL)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "DELETE", c.config.ServiceURL+"/v1/keys/"+keyID, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Key-Issuer-Service", c.config.IssuerService)
	req.Header.Set("Key-Issuer-Id", c.config.ServiceID)
	req.Header.Set("Key-Owner-Id", c.config.OwnerID)

	if c.config.Debug {
		logging.Debug("Request headers:")
		for name, values := range req.Header {
			for _, v := range values {
				if name == "Authorization" {
					logging.Debug("  %s: %s", name, redactBearerToken(v))
				} else {
					logging.Debug("  %s: %s", name, v)
				}
			}
		}
	}

	// Make the request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to delete API key: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if c.config.Debug {
		logging.Debug("Delete API key response status: %d", resp.StatusCode)
		logging.Debug("Delete API key response: %s", string(body))
	}

	// Check status code
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	logging.Debug("Successfully deleted API key %s", keyID)
	return nil
}

// RevokeAPIKey revokes an API key via direct HTTP call
func (c *Client) RevokeAPIKey(ctx context.Context, keyID string) error {
	if c.config.Debug {
		logging.Debug("Revoking API key %s via direct HTTPS call to: %s", keyID, c.config.ServiceURL)
	}

	// Create HTTP request - using DELETE for revoke
	req, err := http.NewRequestWithContext(ctx, "DELETE", c.config.ServiceURL+"/v1/keys/"+keyID, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Key-Issuer-Service", c.config.IssuerService)
	req.Header.Set("Key-Issuer-Id", c.config.ServiceID)
	req.Header.Set("Key-Owner-Id", c.config.OwnerID)

	if c.config.Debug {
		logging.Debug("Request headers:")
		for name, values := range req.Header {
			for _, v := range values {
				if name == "Authorization" {
					logging.Debug("  %s: %s", name, redactBearerToken(v))
				} else {
					logging.Debug("  %s: %s", name, v)
				}
			}
		}
	}

	// Make the request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to revoke API key: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if c.config.Debug {
		logging.Debug("Revoke API key response status: %d", resp.StatusCode)
		logging.Debug("Revoke API key response: %s", string(body))
	}

	// Check status code - allow both 200 and 204
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		// Try to parse as error response
		var errorResponse map[string]interface{}
		if err := json.Unmarshal(body, &errorResponse); err == nil {
			if status, ok := errorResponse["status"].(float64); ok && status >= 400 {
				title, _ := errorResponse["title"].(string)
				detail, _ := errorResponse["detail"].(string)
				return fmt.Errorf("API error %d: %s - %s", int(status), title, detail)
			}
			if detail, ok := errorResponse["detail"].(string); ok {
				return fmt.Errorf("API error: %s", detail)
			}
		}
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	logging.Debug("Successfully revoked API key %s", keyID)
	return nil
}

// ValidateAPIKey validates an API key via direct HTTP call
func (c *Client) ValidateAPIKey(ctx context.Context, apiKey string) (bool, error) {
	if c.config.Debug {
		logging.Debug("Validating API key via direct HTTPS call to: %s", c.config.ServiceURL)
	}

	// Build the introspection request payload
	payload := map[string]string{
		"audience_service_id": c.config.ServiceID,
		"key":                 apiKey,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("failed to marshal introspection payload: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", c.config.ServiceURL+"/v1/introspect", bytes.NewReader(payloadJSON))
	if err != nil {
		return false, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.config.JWTToken)
	req.Header.Set("Key-Issuer-Service", c.config.IssuerService)
	req.Header.Set("Key-Issuer-Id", c.config.ServiceID)

	// Make the request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("API key validation request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("failed to read response: %w", err)
	}

	if c.config.Debug {
		logging.Debug("API key validation response status: %d", resp.StatusCode)
		logging.Debug("API key validation response: %s", string(body))
	}

	// Check if the key is valid based on status code
	return resp.StatusCode == http.StatusOK, nil
}
