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

package nkey

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/plugins/types"
)

type Config struct {
	NkeyMappings []NkeyMapping `mapstructure:"nkey_mappings"`
}

// NkeyMapping represents a mapping of an nkey to an account
type NkeyMapping struct {
	Nkey    string `mapstructure:"nkey"`    // Public nkey (starting with 'U')
	Account string `mapstructure:"account"` // NATS account name
}

type Plugin struct {
	nkeyToAccount map[string]pubKeyAndAccount
	logger        *zap.Logger
}

type pubKeyAndAccount struct {
	publicKey nkeys.KeyPair
	account   string
}

func NewPlugin(configData any, logger *zap.Logger) (*Plugin, error) {
	nkeyConfig := &Config{}
	if err := config.DecodeConfig(configData, nkeyConfig); err != nil {
		return nil, fmt.Errorf("failed to decode config: %w", err)
	}
	nkeyMap := make(map[string]pubKeyAndAccount)
	for _, mapping := range nkeyConfig.NkeyMappings {
		// Validate that the nkey is a valid public user nkey
		nkey, err := validateUserNKey(mapping.Nkey)
		if err != nil {
			return nil, fmt.Errorf("invalid nkey %q: %w", mapping.Nkey, err)
		}
		nkeyMap[mapping.Nkey] = pubKeyAndAccount{
			publicKey: nkey,
			account:   mapping.Account,
		}
	}

	return &Plugin{
		nkeyToAccount: nkeyMap,
		logger:        logger,
	}, nil
}

// validateUserNKey validates that the given nkey is a valid public user nkey
func validateUserNKey(nkeyStr string) (nkeys.KeyPair, error) {
	if nkeyStr == "" {
		return nil, fmt.Errorf("nkey cannot be empty")
	}

	// Check if it starts with 'U' (user nkey prefix)
	if len(nkeyStr) < 2 || nkeyStr[0] != 'U' {
		return nil, fmt.Errorf("nkey must be a public user key (starting with 'U'), got: %s", nkeyStr)
	}

	// Check if it's a valid nkey format by attempting to parse it
	nkey, err := nkeys.FromPublicKey(nkeyStr)
	if err != nil {
		return nil, fmt.Errorf("invalid nkey format: %w", err)
	}

	return nkey, nil
}

func (n *Plugin) Authenticate(ctx context.Context, req *types.Request) (*types.Result, error) {
	account, err := n.validateAndGetAccount(req.FullRequest)
	if err != nil {
		return nil, err
	}
	user := req.FullRequest.ClientInformation.User
	if user == "" {
		user = req.FullRequest.UserNkey
	}
	return &types.Result{Account: account, UserID: user}, nil
}

// validateAndGetAccount validates nkey signature and returns account if valid
func (n *Plugin) validateAndGetAccount(req *jwt.AuthorizationRequest) (string, error) {
	if req.ConnectOptions.Nkey == "" || req.ConnectOptions.SignedNonce == "" {
		return "", types.NewAuthError(types.ErrTypeInvalidRequest, "missing nkey or signature", 400)
	}

	// Check if nkey is in our mappings
	pubKeyAndAccount, exists := n.nkeyToAccount[req.ConnectOptions.Nkey]
	if !exists {
		return "", types.NewAuthError(types.ErrTypeUnauthorized, "nkey not found in mappings", 403)
	}

	// Verify signature cryptographically
	signature, err := base64.RawURLEncoding.DecodeString(req.ConnectOptions.SignedNonce)
	if err != nil {
		return "", errors.Join(types.NewAuthError(types.ErrTypeUnauthorized, "invalid signature format", 403), err)
	}

	nonce := []byte(req.ClientInformation.Nonce)
	if err := pubKeyAndAccount.publicKey.Verify(nonce, signature); err != nil {
		return "", errors.Join(types.NewAuthError(types.ErrTypeUnauthorized, "signature verification failed", 403), err)
	}

	return pubKeyAndAccount.account, nil
}
