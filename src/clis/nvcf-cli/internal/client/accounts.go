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

// AccountDto represents a NVIDIA Cloud Account
type AccountDto struct {
	NcaId                           string   `json:"ncaId"`                           // NVIDIA Cloud Account ID
	AdminClientIds                  []string `json:"adminClientIds"`                  // Client IDs associated with the account
	Name                            string   `json:"name"`                            // Account/customer name
	MaxFunctionsAllowed             int      `json:"maxFunctionsAllowed"`             // Max number of functions allowed
	MaxTasksAllowed                 int      `json:"maxTasksAllowed"`                 // Max number of tasks allowed
	MaxTelemetriesAllowed           int      `json:"maxTelemetriesAllowed"`           // Max number of telemetries allowed
	MaxRegistryCredentialsAllowed   int      `json:"maxRegistryCredentialsAllowed"`   // Max number of registry credentials allowed
}

// ListAccountsResponse represents the response from listing accounts
type ListAccountsResponse struct {
	Accounts []AccountDto `json:"cloudAccounts"`
}

// AccountUpdateRequest represents a request to update an account
type AccountUpdateRequest struct {
	Name                          string `json:"name,omitempty"`                          // Human readable account/customer name
	MaxFunctionsAllowed           *int   `json:"maxFunctionsAllowed,omitempty"`           // Max number of functions allowed
	MaxTasksAllowed               *int   `json:"maxTasksAllowed,omitempty"`               // Max number of tasks allowed
	MaxTelemetriesAllowed         *int   `json:"maxTelemetriesAllowed,omitempty"`         // Max number of telemetries allowed (max: 50)
	MaxRegistryCredentialsAllowed *int   `json:"maxRegistryCredentialsAllowed,omitempty"` // Max number of registry credentials allowed (max: 50)
}

// AccountResponse represents the response from updating an account
type AccountResponse struct {
	Account AccountDto `json:"account"`
}

// CreateAccountRequest represents a request to create a new account
type CreateAccountRequest struct {
	Name                          string                         `json:"name"`                          // Human readable account/customer name
	AdminClientId                 string                         `json:"adminClientId"`                 // Client ID
	RegistryCredentials           []AddRegistryCredentialRequest `json:"registryCredentials,omitempty"` // Registry credentials
	MaxFunctionsAllowed           int                            `json:"maxFunctionsAllowed,omitempty"` // Max number of functions allowed
	MaxTasksAllowed               int                            `json:"maxTasksAllowed,omitempty"`     // Max number of tasks allowed
	MaxTelemetriesAllowed         int                            `json:"maxTelemetriesAllowed,omitempty"`         // Max number of telemetries allowed (max: 50)
	MaxRegistryCredentialsAllowed int                            `json:"maxRegistryCredentialsAllowed,omitempty"` // Max number of registry credentials allowed (max: 50)
}

// CreateAccountResponse represents the response from creating an account
type CreateAccountResponse struct {
	Account AccountDto `json:"account"`
}

// ListAccounts retrieves all NVIDIA Cloud Accounts
func (c *Client) ListAccounts(ctx context.Context) (*ListAccountsResponse, error) {
	endpoint := "/v2/nvcf/accounts"

	resp, err := c.makeRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	var result ListAccountsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// CreateAccount creates a new NVIDIA Cloud Account
func (c *Client) CreateAccount(ctx context.Context, req *CreateAccountRequest) (*CreateAccountResponse, error) {
	endpoint := fmt.Sprintf("/v2/nvcf/accounts/%s", url.PathEscape(req.AdminClientId))

	resp, err := c.makeRequest(ctx, "POST", endpoint, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	var result CreateAccountResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// UpdateAccount updates an existing NVIDIA Cloud Account
func (c *Client) UpdateAccount(ctx context.Context, ncaId string, req *AccountUpdateRequest) (*AccountResponse, error) {
	endpoint := fmt.Sprintf("/v2/nvcf/accounts/%s", url.PathEscape(ncaId))

	resp, err := c.makeRequest(ctx, "PATCH", endpoint, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	var result AccountResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// DeleteAccount deletes a NVIDIA Cloud Account
func (c *Client) DeleteAccount(ctx context.Context, ncaId string) error {
	endpoint := fmt.Sprintf("/v2/nvcf/accounts/%s", url.PathEscape(ncaId))

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
