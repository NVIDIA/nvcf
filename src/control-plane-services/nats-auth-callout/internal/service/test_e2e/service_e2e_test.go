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
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nvcf/src/invocation-plan-services/nats-auth-callout/internal/config"
	"github.com/NVIDIA/nvcf/src/invocation-plan-services/nats-auth-callout/internal/service"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nkeys"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// TestSetup contains common test infrastructure
type TestSetup struct {
	TempDir     string
	NATSServer  *server.Server
	AuthService *service.Service
	ConfigPath  string
	Logger      *zap.Logger
	// Store the seeds for use in config creation
	AuthServiceLoginSeed string
	SigningKeySeed       string
}

// setupCommonTestInfrastructure sets up the common test infrastructure needed by both webhook and OAuth tests
func setupCommonTestInfrastructure(t *testing.T) *TestSetup {
	// Create test logger
	logger := zaptest.NewLogger(t)

	// Create temporary directory
	tempDir := t.TempDir()

	// Generate NKey pair for auth callout
	accountSigningKeyPair, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatalf("Failed to create account keypair: %v", err)
	}

	signingKeySeed, err := accountSigningKeyPair.Seed()
	if err != nil {
		t.Fatalf("Failed to get signing key seed: %v", err)
	}

	signingKeyPublic, err := accountSigningKeyPair.PublicKey()
	if err != nil {
		t.Fatalf("Failed to get public key: %v", err)
	}

	// Generate Nkey pair for auth service login
	authServiceLoginKeyPair, err := nkeys.CreateUser()
	if err != nil {
		t.Fatalf("Failed to create auth service login keypair: %v", err)
	}

	authServiceLoginPublicKey, err := authServiceLoginKeyPair.PublicKey()
	if err != nil {
		t.Fatalf("Failed to get auth service login public key: %v", err)
	}

	authServiceLoginSeed, err := authServiceLoginKeyPair.Seed()
	if err != nil {
		t.Fatalf("Failed to get auth service login seed: %v", err)
	}

	// Start embedded NATS server with auth callout using generated public key
	natsServer := runNATSServerWithAuthCallout(t, authServiceLoginPublicKey, signingKeyPublic)
	t.Cleanup(natsServer.Shutdown)

	return &TestSetup{
		TempDir:    tempDir,
		NATSServer: natsServer,
		Logger:     logger,
		// Store the seeds for use in config creation
		AuthServiceLoginSeed: string(authServiceLoginSeed),
		SigningKeySeed:       string(signingKeySeed),
	}
}

// startAuthServiceWithConfig creates and starts the auth service with the given config
func (setup *TestSetup) startAuthServiceWithConfig(t *testing.T, testConfig *config.ServiceConfig) {
	// Start our actual auth service
	authService, err := service.NewFromConfig(t.Context(), testConfig, setup.Logger)
	if err != nil {
		t.Fatalf("Failed to create auth service: %v", err)
	}
	setup.AuthService = authService
	t.Cleanup(authService.Stop)
}

// createBaseConfig creates the base NATS configuration that's common to all tests
func (setup *TestSetup) createBaseConfig() *config.ServiceConfig {
	return &config.ServiceConfig{
		Name:           "test-service",
		NatsURL:        setup.NATSServer.ClientURL(),
		NkeySeed:       setup.AuthServiceLoginSeed,
		NkeySignature:  setup.SigningKeySeed,
		PluginConfigs:  make(map[string]config.PluginConfig),
		AccountConfigs: make(map[string]config.AccountConfig),
	}
}

// runNATSServerWithAuthCallout starts an embedded NATS server with auth callout configuration using test utilities
func runNATSServerWithAuthCallout(t *testing.T, authServiceLoginPublicKey string, issuerPublicKey string) *server.Server {
	t.Helper()

	// Use t.TempDir() so the directory is unique per test, owned by the test process,
	// and automatically removed when the test ends -- avoiding predictable, world-writable paths.
	configPath := filepath.Join(t.TempDir(), "nats_auth_callout.conf")

	configContent := fmt.Sprintf(`
accounts {
  AUTH: {
    users: [ { nkey: %s } ]
  }
  APP: {}
  SYS: {}
  "test-account": {}
}

system_account: SYS

authorization {
  auth_callout {
    issuer: %s
    auth_users: [ %s ]
    account: AUTH
  }
}
`, authServiceLoginPublicKey, issuerPublicKey, authServiceLoginPublicKey)

	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		panic(fmt.Sprintf("Failed to create NATS config: %v", err))
	}

	// Use test utility to run server with config
	srv, _ := test.RunServerWithConfig(configPath)
	return srv
}
