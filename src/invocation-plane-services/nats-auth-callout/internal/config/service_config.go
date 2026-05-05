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

// ServiceConfig holds the configuration for the nats-auth-callout service.
type ServiceConfig struct {
	Name string `mapstructure:"name" yaml:"name" json:"name"`

	// NatsUrl is the URL for the NATS server.
	NatsURL string `mapstructure:"nats_url" yaml:"nats_url" json:"nats_url"`

	// NkeySeed is the NKey seed for authentication.
	NkeySeed string `mapstructure:"nkey_seed" yaml:"nkey_seed" json:"nkey_seed"`

	// NkeySignature is the signing key seed for JWT signing.
	NkeySignature string `mapstructure:"nkey_signature" yaml:"nkey_signature" json:"nkey_signature"`

	// PluginConfigs map of plugin ID to plugin configuration.
	PluginConfigs map[string]PluginConfig `mapstructure:"plugin_configs" yaml:"plugin_configs" json:"plugin_configs"`

	// AccountConfigs map of (nats) account name to account configuration.
	AccountConfigs map[string]AccountConfig `mapstructure:"account_configs" yaml:"account_configs" json:"account_configs"`
}

type PluginConfig struct {
	PluginType string `mapstructure:"plugin_type" yaml:"plugin_type" json:"plugin_type"`
	Config     any    `mapstructure:"config" yaml:"config" json:"config"`
}

type AccountConfig struct {
	// EnabledPlugins is a list of plugin instances to enable for this account.
	EnabledPlugins []EnabledPlugin `mapstructure:"enabled_plugins" yaml:"enabled_plugins" json:"enabled_plugins"`
}

// EnabledPlugin defines a plugin that is enabled for an account.
type EnabledPlugin struct {
	Alias string `mapstructure:"alias,omitempty" yaml:"alias,omitempty" json:"alias,omitempty"`
	ID    string `mapstructure:"id" yaml:"id" json:"id"`
}
