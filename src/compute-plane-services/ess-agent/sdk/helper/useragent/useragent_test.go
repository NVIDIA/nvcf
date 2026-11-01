/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package useragent

import (
	"testing"

	"github.com/hashicorp/vault/sdk/logical"
)

func TestUserAgent(t *testing.T) {
	projectURL = "https://vault-test.com"
	rt = "go5.0"
	versionFunc = func() string { return "1.2.3" }

	type args struct {
		comments []string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "User agent",
			args: args{},
			want: "Vault/1.2.3 (+https://vault-test.com; go5.0)",
		},
		{
			name: "User agent with additional comment",
			args: args{
				comments: []string{"pid-abcdefg"},
			},
			want: "Vault/1.2.3 (+https://vault-test.com; go5.0; pid-abcdefg)",
		},
		{
			name: "User agent with additional comments",
			args: args{
				comments: []string{"pid-abcdefg", "cloud-provider"},
			},
			want: "Vault/1.2.3 (+https://vault-test.com; go5.0; pid-abcdefg; cloud-provider)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := String(tt.args.comments...); got != tt.want {
				t.Errorf("String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUserAgentPlugin(t *testing.T) {
	projectURL = "https://vault-test.com"
	rt = "go5.0"

	type args struct {
		pluginName string
		pluginEnv  *logical.PluginEnvironment
		comments   []string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "Plugin user agent with nil plugin env",
			args: args{
				pluginEnv: nil,
			},
			want: "",
		},
		{
			name: "Plugin user agent without plugin name",
			args: args{
				pluginEnv: &logical.PluginEnvironment{
					VaultVersion: "1.2.3",
				},
			},
			want: "Vault/1.2.3 (+https://vault-test.com; go5.0)",
		},
		{
			name: "Plugin user agent without plugin name",
			args: args{
				pluginEnv: &logical.PluginEnvironment{
					VaultVersion: "1.2.3",
				},
			},
			want: "Vault/1.2.3 (+https://vault-test.com; go5.0)",
		},
		{
			name: "Plugin user agent with plugin name",
			args: args{
				pluginName: "azure-auth",
				pluginEnv: &logical.PluginEnvironment{
					VaultVersion: "1.2.3",
				},
			},
			want: "Vault/1.2.3 (+https://vault-test.com; azure-auth; go5.0)",
		},
		{
			name: "Plugin user agent with plugin name and additional comment",
			args: args{
				pluginName: "azure-auth",
				pluginEnv: &logical.PluginEnvironment{
					VaultVersion: "1.2.3",
				},
				comments: []string{"pid-abcdefg"},
			},
			want: "Vault/1.2.3 (+https://vault-test.com; azure-auth; go5.0; pid-abcdefg)",
		},
		{
			name: "Plugin user agent with plugin name and additional comments",
			args: args{
				pluginName: "azure-auth",
				pluginEnv: &logical.PluginEnvironment{
					VaultVersion: "1.2.3",
				},
				comments: []string{"pid-abcdefg", "cloud-provider"},
			},
			want: "Vault/1.2.3 (+https://vault-test.com; azure-auth; go5.0; pid-abcdefg; cloud-provider)",
		},
		{
			name: "Plugin user agent with no plugin name and additional comments",
			args: args{
				pluginEnv: &logical.PluginEnvironment{
					VaultVersion: "1.2.3",
				},
				comments: []string{"pid-abcdefg", "cloud-provider"},
			},
			want: "Vault/1.2.3 (+https://vault-test.com; go5.0; pid-abcdefg; cloud-provider)",
		},
		{
			name: "Plugin user agent with version prerelease",
			args: args{
				pluginName: "azure-auth",
				pluginEnv: &logical.PluginEnvironment{
					VaultVersion:           "1.2.3",
					VaultVersionPrerelease: "dev",
				},
				comments: []string{"pid-abcdefg", "cloud-provider"},
			},
			want: "Vault/1.2.3-dev (+https://vault-test.com; azure-auth; go5.0; pid-abcdefg; cloud-provider)",
		},
		{
			name: "Plugin user agent with version metadata",
			args: args{
				pluginName: "azure-auth",
				pluginEnv: &logical.PluginEnvironment{
					VaultVersion:         "1.2.3",
					VaultVersionMetadata: "ent",
				},
				comments: []string{"pid-abcdefg", "cloud-provider"},
			},
			want: "Vault/1.2.3+ent (+https://vault-test.com; azure-auth; go5.0; pid-abcdefg; cloud-provider)",
		},
		{
			name: "Plugin user agent with version prerelease and metadata",
			args: args{
				pluginName: "azure-auth",
				pluginEnv: &logical.PluginEnvironment{
					VaultVersion:           "1.2.3",
					VaultVersionPrerelease: "dev",
					VaultVersionMetadata:   "ent",
				},
				comments: []string{"pid-abcdefg", "cloud-provider"},
			},
			want: "Vault/1.2.3-dev+ent (+https://vault-test.com; azure-auth; go5.0; pid-abcdefg; cloud-provider)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PluginString(tt.args.pluginEnv, tt.args.pluginName, tt.args.comments...); got != tt.want {
				t.Errorf("PluginString() = %v, want %v", got, tt.want)
			}
		})
	}
}
