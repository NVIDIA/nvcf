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

package vault

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/NVIDIA/nvcf/src/control-plane-services/admin-token-issuer-proxy/internal/models"
	"github.com/hashicorp/vault/api"
)

const vaultTokenHeader = "X-Vault-Token"

// VaultSigner defines the interface for interacting with Vault's signing endpoint.
type VaultSigner interface {
	SetToken(token string)
	SignToken(ctx context.Context, signPath, role string) (*api.Secret, error)
}

// VaultClient wraps the Vault API client
type VaultClient struct {
	client *api.Client
}

// ensure VaultClient implements VaultSigner
var _ VaultSigner = &VaultClient{}

// NewVaultClient creates a new Vault client, returning the VaultSigner interface.
func NewVaultClient(addr string) (VaultSigner, error) {
	client, err := api.NewClient(&api.Config{
		Address: addr,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Vault client: %w", err)
	}
	return &VaultClient{client: client}, nil
}

// SetToken sets the Vault token for authentication.
func (v *VaultClient) SetToken(token string) {
	v.client.SetToken(token)

	headers := v.client.Headers()
	if headers == nil {
		headers = make(http.Header)
	}
	if token == "" {
		headers.Del(vaultTokenHeader)
	} else {
		headers.Set(vaultTokenHeader, token)
	}
	v.client.SetHeaders(headers)
}

// SignToken calls the Vault sign endpoint to mint a JWT
// If role is empty, uses signPath as-is. Otherwise appends role to signPath.
func (v *VaultClient) SignToken(ctx context.Context, signPath, role string) (*api.Secret, error) {
	path := signPath
	if role != "" {
		path = fmt.Sprintf("%s/%s", signPath, role)
	}
	secret, err := v.client.Logical().WriteWithContext(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("write Vault signing path %q: %w", path, err)
	}
	return secret, nil
}

// ReadTokenFile reads the Vault token from the file provided by Vault Agent
func ReadTokenFile(tokenFile string) (string, error) {
	data, err := os.ReadFile(tokenFile)
	if err != nil {
		return "", fmt.Errorf("failed to read token file: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("token file is empty")
	}
	return token, nil
}

// DecodeJWTClaims decodes the JWT payload to extract claims
func DecodeJWTClaims(token string) (*models.JWTClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT format")
	}

	// Decode the payload (second part)
	payload := parts[1]

	// Base64URL decode
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to decode JWT payload: %w", err)
	}

	var claims models.JWTClaims
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JWT claims: %w", err)
	}

	return &claims, nil
}
