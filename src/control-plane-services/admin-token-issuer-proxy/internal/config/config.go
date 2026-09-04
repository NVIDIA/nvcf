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
	"fmt"
	"os"
)

// Config holds application configuration from environment variables
type Config struct {
	VaultAddr          string
	SignPath           string
	Role               string
	VaultTokenFile     string
	ListenAddr         string
	Debug              bool
	ServiceMetadataURL string
}

// Load loads configuration from environment variables with validation
func Load() (*Config, error) {
	cfg := &Config{
		VaultAddr:          os.Getenv("VAULT_ADDR"),
		SignPath:           os.Getenv("SIGN_PATH"),
		Role:               envOrDefault("ROLE", "admin-issuer-proxy"),
		VaultTokenFile:     envOrDefault("VAULT_TOKEN_FILE", "/vault/secrets/token"),
		ListenAddr:         envOrDefault("LISTEN_ADDR", ":8080"),
		Debug:              os.Getenv("DEBUG") == "true",
		ServiceMetadataURL: os.Getenv("SERVICE_METADATA_URL"),
	}

	// Validate required fields
	if cfg.VaultAddr == "" {
		return nil, fmt.Errorf("VAULT_ADDR must be set")
	}
	if cfg.SignPath == "" {
		return nil, fmt.Errorf("SIGN_PATH must be set")
	}
	if cfg.VaultTokenFile == "" {
		return nil, fmt.Errorf("VAULT_TOKEN_FILE must be set")
	}
	if cfg.ServiceMetadataURL == "" {
		return nil, fmt.Errorf("SERVICE_METADATA_URL must be set")
	}

	return cfg, nil
}

// envOrDefault fetches an env var or returns a default value.
func envOrDefault(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
