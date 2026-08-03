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

package test_e2e

import (
	"fmt"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/plugins/nkey"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
)

func TestNKeyPluginE2E(t *testing.T) {
	// Set up common test infrastructure
	setup := setupCommonTestInfrastructure(t)

	// Create test nkey pairs for authentication
	testUserKeyPair, err := nkeys.CreateUser()
	if err != nil {
		t.Fatalf("Failed to create test user nkey pair: %v", err)
	}

	testUserPublicKey, err := testUserKeyPair.PublicKey()
	if err != nil {
		t.Fatalf("Failed to get test user public key: %v", err)
	}

	// Create another nkey pair for testing unmapped nkey
	unmappedKeyPair, err := nkeys.CreateUser()
	if err != nil {
		t.Fatalf("Failed to create unmapped user nkey pair: %v", err)
	}

	unmappedPublicKey, err := unmappedKeyPair.PublicKey()
	if err != nil {
		t.Fatalf("Failed to get unmapped user public key: %v", err)
	}

	// Create test configuration for nkey plugin
	testConfig := setup.createBaseConfig()
	testConfig.AccountConfigs["test-account"] = config.AccountConfig{
		EnabledPlugins: []config.EnabledPlugin{{ID: "nkey"}},
	}
	testConfig.PluginConfigs["nkey"] = config.PluginConfig{
		PluginType: "nkey",
		Config: nkey.Config{
			NkeyMappings: []nkey.NkeyMapping{
				{
					Nkey:    testUserPublicKey,
					Account: "test-account",
				},
			},
		},
	}

	// Start auth service with nkey config
	setup.startAuthServiceWithConfig(t, testConfig)

	// Test successful authentication with valid nkey and signature
	t.Run("successful_authentication_with_valid_nkey", func(t *testing.T) {
		// Create nkey authentication option with signature callback
		nkeyOpt := nats.Nkey(testUserPublicKey, func(nonce []byte) ([]byte, error) {
			return testUserKeyPair.Sign(nonce)
		})

		nc, err := nats.Connect(setup.NATSServer.ClientURL(),
			nkeyOpt,
			nats.Timeout(5*time.Second))
		if err != nil {
			t.Fatalf("Expected successful connection with valid nkey, got error: %v", err)
		}
		defer nc.Close()

		// Verify connection is established
		if !nc.IsConnected() {
			t.Error("Expected NATS connection to be established")
		}

		// Test basic publish/subscribe functionality
		err = nc.Publish("test.message", []byte("hello world"))
		if err != nil {
			t.Errorf("Expected to be able to publish message, got error: %v", err)
		}
	})

	// Test failed authentication with unmapped nkey
	t.Run("failed_authentication_with_unmapped_nkey", func(t *testing.T) {
		// Create nkey authentication option with unmapped nkey
		nkeyOpt := nats.Nkey(unmappedPublicKey, func(nonce []byte) ([]byte, error) {
			return unmappedKeyPair.Sign(nonce)
		})

		_, err = nats.Connect(setup.NATSServer.ClientURL(),
			nkeyOpt,
			nats.Timeout(3*time.Second))
		if err == nil {
			t.Error("Expected connection to fail with unmapped nkey")
		}
	})

	// Test failed authentication with invalid nkey format
	t.Run("failed_authentication_with_invalid_nkey_format", func(t *testing.T) {
		// Try to use invalid nkey format - this should fail at the NATS client level
		// before it even reaches our auth service
		_, err = nats.Connect(setup.NATSServer.ClientURL(),
			nats.Nkey("INVALID_NKEY_FORMAT", func(nonce []byte) ([]byte, error) {
				return []byte("fake-signature"), nil
			}),
			nats.Timeout(3*time.Second))
		if err == nil {
			t.Error("Expected connection to fail with invalid nkey format")
		}
	})

	// Test failed authentication with signature error
	t.Run("failed_authentication_with_signature_error", func(t *testing.T) {
		// Use valid nkey but signature function that returns an error
		nkeyOpt := nats.Nkey(testUserPublicKey, func(nonce []byte) ([]byte, error) {
			return nil, fmt.Errorf("signing failed")
		})

		_, err = nats.Connect(setup.NATSServer.ClientURL(),
			nkeyOpt,
			nats.Timeout(3*time.Second))
		if err == nil {
			t.Error("Expected connection to fail when signature function returns error")
		}
	})
}
