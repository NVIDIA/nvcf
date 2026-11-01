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

// Registry Credential Management structures
type ArtifactType string

const (
	ArtifactTypeContainer ArtifactType = "CONTAINER"
	ArtifactTypeHelm      ArtifactType = "HELM"
	ArtifactTypeModel     ArtifactType = "MODEL"
	ArtifactTypeResource  ArtifactType = "RESOURCE"
)

type ProvisionedBy string

const (
	ProvisionedBySystem ProvisionedBy = "SYSTEM"
	ProvisionedByUser   ProvisionedBy = "USER"
)

type RegistrySecretDto struct {
	Name  string      `json:"name"`
	Value interface{} `json:"value,omitempty"`
}

type AddRegistryCredentialRequest struct {
	RegistryHostname string            `json:"registryHostname"`
	Secret           RegistrySecretDto `json:"secret"`
	ArtifactTypes    []ArtifactType    `json:"artifactTypes"`
	Tags             []string          `json:"tags,omitempty"`
	Description      string            `json:"description,omitempty"`
}

type RegistryCredentialDetailsDto struct {
	RegistryCredentialID   string         `json:"registryCredentialId"`
	NcaID                  string         `json:"ncaId"`
	RegistryCredentialName string         `json:"registryCredentialName"`
	RegistryName           string         `json:"registryName"`
	RegistryHostname       string         `json:"registryHostname"`
	ArtifactTypes          []ArtifactType `json:"artifactTypes"`
	Tags                   []string       `json:"tags,omitempty"`
	Description            string         `json:"description,omitempty"`
	ProvisionedBy          ProvisionedBy  `json:"provisionedBy"`
	LastUpdatedAt          string         `json:"lastUpdatedAt"`
	CreatedAt              string         `json:"createdAt"`
}

type ListRegistryCredentialDetailsResponse struct {
	RegistryCredentials []RegistryCredentialDetailsDto `json:"registryCredentials"`
}

type RegistryCredentialDetailsResponse struct {
	RegistryCredential RegistryCredentialDetailsDto `json:"registryCredential"`
}

type UpdateRegistryCredentialRequest struct {
	Secret            *RegistrySecretDto `json:"secret,omitempty"`
	ArtifactTypeEnums []ArtifactType     `json:"artifactTypeEnums,omitempty"` // Artifact types to ADD to existing ones (additive)
}

type RecognizedRegistriesResponse struct {
	RecognizedRegistries map[string][]map[string]string `json:"recognizedRegistries"`
}

// Registry Credential Management API methods

// ListRegistryCredentials lists all registry credentials
func (c *Client) ListRegistryCredentials(ctx context.Context, artifactTypes []ArtifactType, provisionedBy []ProvisionedBy) (*ListRegistryCredentialDetailsResponse, error) {
	// Use regular endpoint (works with both JWT and API key)
	endpoint := "/v2/nvcf/registry-credentials"

	// Build query parameters
	params := url.Values{}
	for _, artifactType := range artifactTypes {
		params.Add("artifactType", string(artifactType))
	}
	for _, provision := range provisionedBy {
		params.Add("provisionedBy", string(provision))
	}

	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}

	resp, err := c.makeRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	var result ListRegistryCredentialDetailsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// AddRegistryCredential adds a new registry credential
func (c *Client) AddRegistryCredential(ctx context.Context, req *AddRegistryCredentialRequest) (*RegistryCredentialDetailsResponse, error) {
	// Use regular endpoint (works with both JWT and API key)
	endpoint := "/v2/nvcf/registry-credentials"

	resp, err := c.makeRequest(ctx, "POST", endpoint, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	var result RegistryCredentialDetailsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// GetRegistryCredential gets details of a specific registry credential
func (c *Client) GetRegistryCredential(ctx context.Context, credentialID string) (*RegistryCredentialDetailsResponse, error) {
	// Use regular endpoint (works with both JWT and API key)
	endpoint := fmt.Sprintf("/v2/nvcf/registry-credentials/%s", credentialID)

	resp, err := c.makeRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	var result RegistryCredentialDetailsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// DeleteRegistryCredential deletes a registry credential
func (c *Client) DeleteRegistryCredential(ctx context.Context, credentialID string) error {
	// Use regular endpoint (works with both JWT and API key)
	endpoint := fmt.Sprintf("/v2/nvcf/registry-credentials/%s", credentialID)

	resp, err := c.makeRequest(ctx, "DELETE", endpoint, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// UpdateRegistryCredential updates a registry credential
func (c *Client) UpdateRegistryCredential(ctx context.Context, credentialID string, req *UpdateRegistryCredentialRequest) (*RegistryCredentialDetailsResponse, error) {
	// Use regular endpoint (works with both JWT and API key)
	endpoint := fmt.Sprintf("/v2/nvcf/registry-credentials/%s", credentialID)

	resp, err := c.makeRequest(ctx, "PATCH", endpoint, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	var result RegistryCredentialDetailsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// ListRecognizedRegistries lists all recognized registries
func (c *Client) ListRecognizedRegistries(ctx context.Context) (*RecognizedRegistriesResponse, error) {
	// Use regular endpoint (works with both JWT and API key)
	endpoint := "/v2/nvcf/recognized-registries"

	resp, err := c.makeRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	var result RecognizedRegistriesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}
