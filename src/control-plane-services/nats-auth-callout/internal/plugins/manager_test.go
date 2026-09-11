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
	"testing"

	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/internal/plugins/types"
)

type fakePlugin struct {
	result *types.Result
}

func (p fakePlugin) Authenticate(context.Context, *types.Request) (*types.Result, error) {
	return p.result, nil
}

func TestAuthenticateFallsBackToLowercaseAccountKey(t *testing.T) {
	manager := &Manager{
		plugins: map[PluginKey]types.AuthPlugin{
			{AccountName: "app", PluginName: "oidc"}: fakePlugin{
				result: &types.Result{
					Account: "APP",
					UserID:  "nvca",
				},
			},
		},
		logger: zap.NewNop(),
	}

	result, err := manager.Authenticate(context.Background(), &types.Request{
		Account:    "APP",
		PluginName: "oidc",
		Payload:    "token",
	})
	if err != nil {
		t.Fatalf("Authenticate returned error: %v", err)
	}
	if result.Account != "APP" {
		t.Fatalf("Authenticate returned account %q, want APP", result.Account)
	}
	if result.UserID != "nvca" {
		t.Fatalf("Authenticate returned user %q, want nvca", result.UserID)
	}
}

func TestInitializePluginsKeepsAccountNameAfterNKeyPlugin(t *testing.T) {
	manager := NewManager(&config.ServiceConfig{
		PluginConfigs: map[string]config.PluginConfig{
			"nkey": {
				PluginType: "nkey",
				Config: map[string]any{
					"nkey_mappings": []any{},
				},
			},
			"webhook": {
				PluginType: "webhook",
				Config: map[string]any{
					"url": "http://127.0.0.1:1",
				},
			},
		},
		AccountConfigs: map[string]config.AccountConfig{
			"app": {
				EnabledPlugins: []config.EnabledPlugin{
					{ID: "nkey"},
					{ID: "webhook", Alias: "oidc"},
				},
			},
		},
	}, zap.NewNop())

	if _, ok := manager.plugins[PluginKey{AccountName: "", PluginName: "nkey"}]; !ok {
		t.Fatal("nkey plugin was not registered with the shared lookup key")
	}
	if _, ok := manager.plugins[PluginKey{AccountName: "app", PluginName: "oidc"}]; !ok {
		t.Fatal("webhook plugin was not registered with the app account")
	}
	if _, ok := manager.plugins[PluginKey{AccountName: "", PluginName: "oidc"}]; ok {
		t.Fatal("webhook plugin was registered with the nkey shared lookup key")
	}
}
