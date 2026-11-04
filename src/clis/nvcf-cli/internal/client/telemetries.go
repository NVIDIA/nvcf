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

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

const telemetriesEndpoint = "/v2/nvcf/telemetries"

// TelemetryRequest is the request body for POST /v2/nvcf/telemetries.
// The secret name is also the telemetry name.
type TelemetryRequest struct {
	Endpoint string    `json:"endpoint"`
	Protocol string    `json:"protocol"`
	Provider string    `json:"provider"`
	Types    []string  `json:"types"`
	Secret   SecretDto `json:"secret"`
}

// TelemetryDto describes a registered telemetry endpoint.
type TelemetryDto struct {
	TelemetryID string   `json:"telemetryId"`
	Name        string   `json:"name"`
	Endpoint    string   `json:"endpoint"`
	Protocol    string   `json:"protocol"`
	Provider    string   `json:"provider"`
	Types       []string `json:"types"`
	CreatedAt   string   `json:"createdAt"`
}

// TelemetryResponse wraps a single telemetry endpoint.
type TelemetryResponse struct {
	Telemetry TelemetryDto `json:"telemetry"`
}

// ListTelemetriesResponse is the response from GET /v2/nvcf/telemetries.
type ListTelemetriesResponse struct {
	Telemetries []TelemetryDto `json:"telemetries"`
}

// UpdateTelemetrySecretRequest is the request body for updating the
// credentials of a telemetry endpoint.
type UpdateTelemetrySecretRequest struct {
	Secret SecretDto `json:"secret"`
}

// CreateTelemetry registers a telemetry endpoint.
// Requires: manage_telemetries scope
func (c *Client) CreateTelemetry(ctx context.Context, req *TelemetryRequest) (*TelemetryResponse, error) {
	resp, err := c.makeRequest(ctx, "POST", telemetriesEndpoint, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf(apiErrorFormat, resp.StatusCode, string(body))
	}

	var result TelemetryResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &result, nil
}

// ListTelemetries lists the telemetry endpoints of the authenticated account.
// Requires: manage_telemetries scope
func (c *Client) ListTelemetries(ctx context.Context) (*ListTelemetriesResponse, error) {
	resp, err := c.makeRequest(ctx, "GET", telemetriesEndpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf(apiErrorFormat, resp.StatusCode, string(body))
	}

	var result ListTelemetriesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &result, nil
}

// GetTelemetry gets a telemetry endpoint by ID.
// Requires: manage_telemetries scope
func (c *Client) GetTelemetry(ctx context.Context, telemetryID string) (*TelemetryResponse, error) {
	resp, err := c.makeRequest(ctx, "GET", telemetriesEndpoint+"/"+url.PathEscape(telemetryID), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf(apiErrorFormat, resp.StatusCode, string(body))
	}

	var result TelemetryResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &result, nil
}

// DeleteTelemetry deletes a telemetry endpoint. The API rejects the request
// while a function still references the endpoint.
// Requires: manage_telemetries scope
func (c *Client) DeleteTelemetry(ctx context.Context, telemetryID string) error {
	resp, err := c.makeRequest(ctx, "DELETE", telemetriesEndpoint+"/"+url.PathEscape(telemetryID), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf(apiErrorFormat, resp.StatusCode, string(body))
	}
	return nil
}
