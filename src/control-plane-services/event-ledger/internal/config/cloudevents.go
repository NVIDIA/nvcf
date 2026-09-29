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
	"errors"

	"go.uber.org/zap"
)

// CloudEvents configuration validation errors
var (
	ErrMissingCEEndpoint             = errors.New("cloudevents: endpoint is required")
	ErrMissingCECredentials          = errors.New("cloudevents: credentials are required when token_endpoint is specified")
	ErrInvalidCECredsRefreshInterval = errors.New("cloudevents: creds_refresh_interval must be greater than 0 when creds_file is specified")
)

type CloudEventsConfig struct {
	Enabled                    bool   `mapstructure:"enabled"`
	Endpoint                   string `mapstructure:"endpoint"`
	TokenEndpoint              string `mapstructure:"token_endpoint"`
	CredentialsFile            string `mapstructure:"creds_file"`
	CredentialsRefreshInterval int64  `mapstructure:"creds_refresh_interval"`
	ClientID                   string `mapstructure:"client_id"`
	ClientSecret               string `mapstructure:"client_secret"`
}

// UseCredentialsFile returns true if hot-reload mode is enabled via creds_file
func (c CloudEventsConfig) UseCredentialsFile() bool {
	return c.CredentialsFile != ""
}

// ValidateCloudEventsConfig validates CloudEvents exporter configuration.
func ValidateCloudEventsConfig(cfg CloudEventsConfig) error {
	if !cfg.Enabled {
		zap.L().Warn("CloudEvents client disabled")
		return nil
	}

	if cfg.Endpoint == "" {
		return ErrMissingCEEndpoint
	}

	if cfg.TokenEndpoint != "" {
		if cfg.ClientID == "" || cfg.ClientSecret == "" {
			if cfg.CredentialsFile == "" {
				return ErrMissingCECredentials
			}
		}
	}
	if cfg.CredentialsFile != "" && cfg.CredentialsRefreshInterval <= 0 {
		return ErrInvalidCECredsRefreshInterval
	}

	return nil
}
