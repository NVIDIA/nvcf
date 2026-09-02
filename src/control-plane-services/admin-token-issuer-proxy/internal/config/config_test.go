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

package config

import (
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	t.Setenv("VAULT_ADDR", "https://vault.example.com")
	t.Setenv("SIGN_PATH", "services/example/jwt/sign")
	t.Setenv("SERVICE_METADATA_URL", "https://metadata.example.com/v1/services")
	t.Setenv("ROLE", "")
	t.Setenv("VAULT_TOKEN_FILE", "")
	t.Setenv("LISTEN_ADDR", "")
	t.Setenv("DEBUG", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned an unexpected error: %v", err)
	}

	if cfg.VaultAddr != "https://vault.example.com" {
		t.Errorf("VaultAddr = %q, want %q", cfg.VaultAddr, "https://vault.example.com")
	}
	if cfg.SignPath != "services/example/jwt/sign" {
		t.Errorf("SignPath = %q, want %q", cfg.SignPath, "services/example/jwt/sign")
	}
	if cfg.ServiceMetadataURL != "https://metadata.example.com/v1/services" {
		t.Errorf("ServiceMetadataURL = %q, want %q", cfg.ServiceMetadataURL, "https://metadata.example.com/v1/services")
	}
	if cfg.Role != "admin-issuer-proxy" {
		t.Errorf("Role = %q, want %q", cfg.Role, "admin-issuer-proxy")
	}
	if cfg.VaultTokenFile != "/vault/secrets/token" {
		t.Errorf("VaultTokenFile = %q, want %q", cfg.VaultTokenFile, "/vault/secrets/token")
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":8080")
	}
	if !cfg.Debug {
		t.Error("Debug = false, want true")
	}
}

func TestLoadRequiresEndpointConfiguration(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{name: "vault address", key: "VAULT_ADDR"},
		{name: "sign path", key: "SIGN_PATH"},
		{name: "service metadata URL", key: "SERVICE_METADATA_URL"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("VAULT_ADDR", "https://vault.example.com")
			t.Setenv("SIGN_PATH", "services/example/jwt/sign")
			t.Setenv("SERVICE_METADATA_URL", "https://metadata.example.com/v1/services")
			t.Setenv(test.key, "")

			_, err := Load()
			if err == nil {
				t.Fatalf("Load() returned nil error with %s unset", test.key)
			}
			if !strings.Contains(err.Error(), test.key+" must be set") {
				t.Fatalf("Load() error = %q, want an error for %s", err, test.key)
			}
		})
	}
}
