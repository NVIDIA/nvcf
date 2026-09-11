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

package plugins

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/plugins/nkey"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/plugins/oauth"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/plugins/types"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/plugins/webhook"
)

// PluginKey represents a composite key for identifying plugins.
type PluginKey struct {
	AccountName string
	PluginName  string
}

// Manager manages authentication plugins.
type Manager struct {
	plugins map[PluginKey]types.AuthPlugin
	logger  *zap.Logger
	config  *config.ServiceConfig
}

// NewManager creates a new plugin manager.
func NewManager(config *config.ServiceConfig, logger *zap.Logger) *Manager {
	pm := &Manager{
		plugins: make(map[PluginKey]types.AuthPlugin),
		logger:  logger,
		config:  config,
	}
	pm.initializePlugins()
	return pm
}

// Authenticate attempts authentication using plugins for the specified account.
func (pm *Manager) Authenticate(ctx context.Context, request *types.Request) (*types.Result, error) {
	// Use plugin name from request
	pluginName := request.PluginName
	if pluginName == "" {
		return nil, types.NewAuthError(types.ErrTypeInvalidRequest, "plugin name not specified", 400)
	}

	plugin, exists := pm.lookupPlugin(request.Account, pluginName)
	if !exists {
		return nil, types.NewAuthError(types.ErrTypePluginError, fmt.Sprintf("plugin %s not found for account %s", pluginName, request.Account), 404)
	}

	// Authenticate using the specified plugin
	result, err := plugin.Authenticate(ctx, request)
	if err != nil {
		pm.logger.Error("Authentication failed",
			zap.String("account", request.Account),
			zap.String("plugin", pluginName),
			zap.Error(err),
		)
		return nil, err
	}

	pm.logger.Info("Authentication successful",
		zap.String("account", request.Account),
		zap.String("plugin", pluginName),
		zap.String("user_id", result.UserID),
	)

	return result, nil
}

func (pm *Manager) lookupPlugin(accountName, pluginName string) (types.AuthPlugin, bool) {
	pluginKey := PluginKey{
		AccountName: accountName,
		PluginName:  pluginName,
	}
	plugin, exists := pm.plugins[pluginKey]
	if exists {
		return plugin, true
	}

	lowerAccountName := strings.ToLower(accountName)
	if lowerAccountName == accountName {
		return nil, false
	}
	pluginKey.AccountName = lowerAccountName
	plugin, exists = pm.plugins[pluginKey]
	return plugin, exists
}

// initializePlugins initializes all plugins based on current configuration.
func (pm *Manager) initializePlugins() {
	// Build plugin map
	pluginMap := make(map[PluginKey]types.AuthPlugin)

	// Collect all plugins from all accounts
	for accountName, account := range pm.config.AccountConfigs {
		for _, plugin := range account.EnabledPlugins {
			pluginAccountName := accountName
			alias := plugin.Alias
			if alias == "" {
				alias = plugin.ID // Default to plugin ID if no alias
			}
			if plugin.ID == "nkey" {
				// the nkey plugin maintains its own internal account list and is special cased.
				// we don't know the account name until the nkey is looked up by the plugin.
				pluginAccountName = ""
			}
			pluginKey := PluginKey{
				AccountName: pluginAccountName,
				PluginName:  alias,
			}

			// Find plugin config using plugin.ID as the key
			pluginConfig, pluginFound := pm.config.PluginConfigs[plugin.ID]
			if !pluginFound {
				pm.logger.Error("Plugin configuration not found",
					zap.String("account", pluginAccountName),
					zap.String("plugin", plugin.ID),
				)
				continue
			}

			// Create plugin instance
			pluginInstance, err := pm.createPlugin(pluginConfig.PluginType, pluginConfig.Config)
			if err != nil {
				pm.logger.Error("failed to create plugin",
					zap.String("account", pluginAccountName),
					zap.String("plugin", plugin.ID),
					zap.String("config_key", plugin.ID),
					zap.String("type", pluginConfig.PluginType),
					zap.Error(err),
				)
				continue
			}

			pluginMap[pluginKey] = pluginInstance

			pm.logger.Info("Plugin created",
				zap.String("account", pluginAccountName),
				zap.String("plugin", plugin.ID),
				zap.String("alias", alias),
				zap.String("config_key", plugin.ID),
				zap.String("type", pluginConfig.PluginType),
			)
		}
	}

	// Store the plugin map
	pm.plugins = pluginMap
}

// createPlugin creates a new plugin instance based on type.
func (pm *Manager) createPlugin(pluginType string, config any) (types.AuthPlugin, error) {
	switch pluginType {
	case "oauth":
		return oauth.NewPlugin(config, pm.logger)
	case "webhook":
		return webhook.NewPlugin(config, pm.logger)
	case "nkey":
		return nkey.NewPlugin(config, pm.logger)
	default:
		return nil, fmt.Errorf("unknown plugin type: %s", pluginType)
	}
}
