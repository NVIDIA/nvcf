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
	"testing"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/assert"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/plugins/types"
	"go.uber.org/zap/zaptest"
)

func TestNKeyPlugin(t *testing.T) {
	logger := zaptest.NewLogger(t)

	t.Run("Configuration Validation", func(t *testing.T) {
		t.Run("with empty configuration creates plugin successfully", func(t *testing.T) {
			configData := map[string]any{}
			plugin, err := NewPlugin(configData, logger)
			assert.NoError(t, err)
			assert.NotNil(t, plugin)
			assert.Empty(t, plugin.nkeyToAccount)
		})

		t.Run("with empty nkey mappings creates plugin successfully", func(t *testing.T) {
			configData := Config{
				NkeyMappings: []NkeyMapping{},
			}
			plugin, err := NewPlugin(configData, logger)
			assert.NoError(t, err)
			assert.NotNil(t, plugin)
			assert.Empty(t, plugin.nkeyToAccount)
		})

		t.Run("with valid map[string]any configuration parses correctly", func(t *testing.T) {
			// Generate valid user nkeys for testing
			userKey1, err := nkeys.CreateUser()
			assert.NoError(t, err)
			publicKey1, err := userKey1.PublicKey()
			assert.NoError(t, err)

			userKey2, err := nkeys.CreateUser()
			assert.NoError(t, err)
			publicKey2, err := userKey2.PublicKey()
			assert.NoError(t, err)

			configData := map[string]any{
				"nkey_mappings": []map[string]any{
					{
						"nkey":    publicKey1,
						"account": "test-account-1",
					},
					{
						"nkey":    publicKey2,
						"account": "test-account-2",
					},
				},
			}
			plugin, err := NewPlugin(configData, logger)
			assert.NoError(t, err)
			assert.NotNil(t, plugin)
			assert.Len(t, plugin.nkeyToAccount, 2)
			assert.Equal(t, "test-account-1", plugin.nkeyToAccount[publicKey1].account)
			assert.Equal(t, "test-account-2", plugin.nkeyToAccount[publicKey2].account)
		})

		t.Run("with struct configuration parses correctly", func(t *testing.T) {
			// Generate valid user nkeys for testing
			userKey3, err := nkeys.CreateUser()
			assert.NoError(t, err)
			publicKey3, err := userKey3.PublicKey()
			assert.NoError(t, err)

			userKey4, err := nkeys.CreateUser()
			assert.NoError(t, err)
			publicKey4, err := userKey4.PublicKey()
			assert.NoError(t, err)

			configData := Config{
				NkeyMappings: []NkeyMapping{
					{
						Nkey:    publicKey3,
						Account: "test-account-3",
					},
					{
						Nkey:    publicKey4,
						Account: "test-account-4",
					},
				},
			}
			plugin, err := NewPlugin(configData, logger)
			assert.NoError(t, err)
			assert.NotNil(t, plugin)
			assert.Len(t, plugin.nkeyToAccount, 2)
			assert.Equal(t, "test-account-3", plugin.nkeyToAccount[publicKey3].account)
			assert.Equal(t, "test-account-4", plugin.nkeyToAccount[publicKey4].account)
		})

		t.Run("with invalid configuration format returns error", func(t *testing.T) {
			configData := "invalid-config"
			_, err := NewPlugin(configData, logger)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "failed to decode config")
		})

		t.Run("with invalid nkey format returns error", func(t *testing.T) {
			configData := Config{
				NkeyMappings: []NkeyMapping{
					{
						Nkey:    "U123INVALID_BASE32_!@#", // Starts with U but invalid base32
						Account: "test-account",
					},
				},
			}
			_, err := NewPlugin(configData, logger)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "invalid nkey")
			assert.Contains(t, err.Error(), "invalid nkey format")
		})

		t.Run("with empty nkey returns error", func(t *testing.T) {
			configData := Config{
				NkeyMappings: []NkeyMapping{
					{
						Nkey:    "",
						Account: "test-account",
					},
				},
			}
			_, err := NewPlugin(configData, logger)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "nkey cannot be empty")
		})

		t.Run("with non-user nkey returns error", func(t *testing.T) {
			// Create an account nkey (starts with 'A') instead of user nkey
			accountKey, err := nkeys.CreateAccount()
			assert.NoError(t, err)
			accountPublicKey, err := accountKey.PublicKey()
			assert.NoError(t, err)

			configData := Config{
				NkeyMappings: []NkeyMapping{
					{
						Nkey:    accountPublicKey,
						Account: "test-account",
					},
				},
			}
			_, err = NewPlugin(configData, logger)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "nkey must be a public user key")
			assert.Contains(t, err.Error(), "starting with 'U'")
		})

		t.Run("with operator nkey returns error", func(t *testing.T) {
			// Create an operator nkey (starts with 'O') instead of user nkey
			operatorKey, err := nkeys.CreateOperator()
			assert.NoError(t, err)
			operatorPublicKey, err := operatorKey.PublicKey()
			assert.NoError(t, err)

			configData := Config{
				NkeyMappings: []NkeyMapping{
					{
						Nkey:    operatorPublicKey,
						Account: "test-account",
					},
				},
			}
			_, err = NewPlugin(configData, logger)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "nkey must be a public user key")
			assert.Contains(t, err.Error(), "starting with 'U'")
		})
	})

	t.Run("Authentication Flow", func(t *testing.T) {
		// Create test nkey pairs for testing
		testUserKeyPair, err := nkeys.CreateUser()
		assert.NoError(t, err)
		testUserPublicKey, err := testUserKeyPair.PublicKey()
		assert.NoError(t, err)

		unmappedKeyPair, err := nkeys.CreateUser()
		assert.NoError(t, err)
		unmappedPublicKey, err := unmappedKeyPair.PublicKey()
		assert.NoError(t, err)

		// Create plugin with test nkey mapping
		configData := Config{
			NkeyMappings: []NkeyMapping{
				{
					Nkey:    testUserPublicKey,
					Account: "test-account",
				},
			},
		}
		plugin, err := NewPlugin(configData, logger)
		assert.NoError(t, err)

		t.Run("successful authentication with valid nkey and signature", func(t *testing.T) {
			nonce := []byte("test-nonce-12345")
			signature, err := testUserKeyPair.Sign(nonce)
			assert.NoError(t, err)

			request := &types.Request{
				FullRequest: &jwt.AuthorizationRequest{
					ConnectOptions: jwt.ConnectOptions{
						Nkey:        testUserPublicKey,
						SignedNonce: base64.RawURLEncoding.EncodeToString(signature),
					},
					ClientInformation: jwt.ClientInformation{
						User:  "test-user",
						Nonce: "test-nonce-12345",
					},
					UserNkey: testUserPublicKey,
				},
			}

			result, err := plugin.Authenticate(context.Background(), request)
			assert.NoError(t, err)
			assert.NotNil(t, result)
			assert.Equal(t, "test-account", result.Account)
			assert.Equal(t, "test-user", result.UserID)
		})

		t.Run("successful authentication uses UserNkey when User is empty", func(t *testing.T) {
			nonce := []byte("test-nonce-67890")
			signature, err := testUserKeyPair.Sign(nonce)
			assert.NoError(t, err)

			request := &types.Request{
				FullRequest: &jwt.AuthorizationRequest{
					ConnectOptions: jwt.ConnectOptions{
						Nkey:        testUserPublicKey,
						SignedNonce: base64.RawURLEncoding.EncodeToString(signature),
					},
					ClientInformation: jwt.ClientInformation{
						User:  "", // Empty user
						Nonce: "test-nonce-67890",
					},
					UserNkey: testUserPublicKey,
				},
			}

			result, err := plugin.Authenticate(context.Background(), request)
			assert.NoError(t, err)
			assert.NotNil(t, result)
			assert.Equal(t, "test-account", result.Account)
			assert.Equal(t, testUserPublicKey, result.UserID) // Should use UserNkey
		})

		t.Run("failed authentication with missing nkey", func(t *testing.T) {
			request := &types.Request{
				FullRequest: &jwt.AuthorizationRequest{
					ConnectOptions: jwt.ConnectOptions{
						Nkey:        "", // Missing nkey
						SignedNonce: "some-signature",
					},
					ClientInformation: jwt.ClientInformation{
						User:  "test-user",
						Nonce: "test-nonce",
					},
				},
			}

			result, err := plugin.Authenticate(context.Background(), request)
			assert.Error(t, err)
			assert.Nil(t, result)
			assert.Contains(t, err.Error(), "missing nkey or signature")
		})

		t.Run("failed authentication with missing signature", func(t *testing.T) {
			request := &types.Request{
				FullRequest: &jwt.AuthorizationRequest{
					ConnectOptions: jwt.ConnectOptions{
						Nkey:        testUserPublicKey,
						SignedNonce: "", // Missing signature
					},
					ClientInformation: jwt.ClientInformation{
						User:  "test-user",
						Nonce: "test-nonce",
					},
					UserNkey: testUserPublicKey,
				},
			}

			result, err := plugin.Authenticate(context.Background(), request)
			assert.Error(t, err)
			assert.Nil(t, result)
			assert.Contains(t, err.Error(), "missing nkey or signature")
		})

		t.Run("failed authentication with nkey not in mappings", func(t *testing.T) {
			nonce := []byte("test-nonce-unmapped")
			signature, err := unmappedKeyPair.Sign(nonce)
			assert.NoError(t, err)

			request := &types.Request{
				FullRequest: &jwt.AuthorizationRequest{
					ConnectOptions: jwt.ConnectOptions{
						Nkey:        unmappedPublicKey, // Not in mappings
						SignedNonce: base64.RawURLEncoding.EncodeToString(signature),
					},
					ClientInformation: jwt.ClientInformation{
						User:  "unmapped-user",
						Nonce: "test-nonce-unmapped",
					},
					UserNkey: unmappedPublicKey,
				},
			}

			result, err := plugin.Authenticate(context.Background(), request)
			assert.Error(t, err)
			assert.Nil(t, result)
			assert.Contains(t, err.Error(), "nkey not found in mappings")
		})

		t.Run("failed authentication with invalid signature format", func(t *testing.T) {
			request := &types.Request{
				FullRequest: &jwt.AuthorizationRequest{
					ConnectOptions: jwt.ConnectOptions{
						Nkey:        testUserPublicKey,
						SignedNonce: "invalid-base64-!@#", // Invalid base64
					},
					ClientInformation: jwt.ClientInformation{
						User:  "test-user",
						Nonce: "test-nonce",
					},
					UserNkey: testUserPublicKey,
				},
			}

			result, err := plugin.Authenticate(context.Background(), request)
			assert.Error(t, err)
			assert.Nil(t, result)
			assert.Contains(t, err.Error(), "invalid signature format")
		})

		t.Run("failed authentication with signature verification failure", func(t *testing.T) {
			// Sign a different nonce than what's in the request
			wrongNonce := []byte("wrong-nonce")
			signature, err := testUserKeyPair.Sign(wrongNonce)
			assert.NoError(t, err)

			request := &types.Request{
				FullRequest: &jwt.AuthorizationRequest{
					ConnectOptions: jwt.ConnectOptions{
						Nkey:        testUserPublicKey,
						SignedNonce: base64.RawURLEncoding.EncodeToString(signature),
					},
					ClientInformation: jwt.ClientInformation{
						User:  "test-user",
						Nonce: "correct-nonce", // Different from signed nonce
					},
					UserNkey: testUserPublicKey,
				},
			}

			result, err := plugin.Authenticate(context.Background(), request)
			assert.Error(t, err)
			assert.Nil(t, result)
			assert.Contains(t, err.Error(), "signature verification failed")
		})

		t.Run("failed authentication with completely invalid signature", func(t *testing.T) {
			request := &types.Request{
				FullRequest: &jwt.AuthorizationRequest{
					ConnectOptions: jwt.ConnectOptions{
						Nkey:        testUserPublicKey,
						SignedNonce: base64.RawURLEncoding.EncodeToString([]byte("completely-invalid-signature")),
					},
					ClientInformation: jwt.ClientInformation{
						User:  "test-user",
						Nonce: "test-nonce",
					},
					UserNkey: testUserPublicKey,
				},
			}

			result, err := plugin.Authenticate(context.Background(), request)
			assert.Error(t, err)
			assert.Nil(t, result)
			assert.Contains(t, err.Error(), "signature verification failed")
		})
	})
}
