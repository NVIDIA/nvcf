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
	"testing"

	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/plugins/types"
	"go.uber.org/zap/zaptest"
)

// TestInitializePlugins_NkeyDoesNotClobberAccountName is a regression test for
// the nkey plugin special-case in initializePlugins. The nkey plugin maintains
// its own internal account list, so it is registered under an empty account
// name. That special-case must not overwrite the account loop variable: a
// plugin processed after the nkey entry for the same account must still be
// registered under that account's name.
func TestInitializePlugins_NkeyDoesNotClobberAccountName(t *testing.T) {
	cfg := &config.ServiceConfig{
		AccountConfigs: map[string]config.AccountConfig{
			"APP": {
				EnabledPlugins: []config.EnabledPlugin{
					{ID: "nkey"},
					{ID: "webhook", Alias: "webhook"},
				},
			},
		},
		PluginConfigs: map[string]config.PluginConfig{
			"nkey": {
				PluginType: "nkey",
				Config:     map[string]any{},
			},
			"webhook": {
				PluginType: "webhook",
				Config: map[string]any{
					"url": "https://example.com/hook",
				},
			},
		},
	}

	pm := NewManager(cfg, zaptest.NewLogger(t))

	if _, ok := pm.plugins[PluginKey{AccountName: "APP", PluginName: "webhook"}]; !ok {
		t.Errorf("webhook plugin for account APP was not registered under APP; got keys %v", pluginKeys(pm.plugins))
	}
	if _, ok := pm.plugins[PluginKey{AccountName: "", PluginName: "webhook"}]; ok {
		t.Errorf("webhook plugin was registered under an empty account name")
	}
	if _, ok := pm.plugins[PluginKey{AccountName: "", PluginName: "nkey"}]; !ok {
		t.Errorf("nkey plugin was not registered under the empty account name; got keys %v", pluginKeys(pm.plugins))
	}
}

func pluginKeys(m map[PluginKey]types.AuthPlugin) []PluginKey {
	ks := make([]PluginKey, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
