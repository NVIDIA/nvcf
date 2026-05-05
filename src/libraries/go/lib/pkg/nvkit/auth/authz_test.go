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

package auth

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUAMPolicyConfig_AddCommandFlags_NilCommand(t *testing.T) {
	cfg := &UAMPolicyConfig{}
	result := cfg.AddCommandFlags(nil)
	assert.False(t, result, "expected false when cmd is nil")
}

func TestUAMPolicyConfig_AddCommandFlags_ValidCommand(t *testing.T) {
	cfg := &UAMPolicyConfig{}
	cmd := &cobra.Command{Use: "test"}

	result := cfg.AddCommandFlags(cmd)
	assert.True(t, result, "expected true when cmd is valid")

	// Verify the flags were registered
	nsFlag := cmd.Flags().Lookup("authz.policy.namespace")
	require.NotNil(t, nsFlag, "authz.policy.namespace flag should be registered")
	assert.Equal(t, "", nsFlag.DefValue, "namespace flag default should be empty string")
	assert.Equal(t, "policy namespace", nsFlag.Usage)

	nameFlag := cmd.Flags().Lookup("authz.policy.name")
	require.NotNil(t, nameFlag, "authz.policy.name flag should be registered")
	assert.Equal(t, "", nameFlag.DefValue, "name flag default should be empty string")
	assert.Equal(t, "policy fqdn", nameFlag.Usage)
}

func TestUAMPolicyConfig_AddCommandFlags_FlagValues(t *testing.T) {
	cfg := &UAMPolicyConfig{}
	cmd := &cobra.Command{Use: "test"}

	result := cfg.AddCommandFlags(cmd)
	require.True(t, result)

	// Parse flags and verify they update the struct fields
	err := cmd.ParseFlags([]string{
		"--authz.policy.namespace", "test-namespace",
		"--authz.policy.name", "test.policy.fqdn",
	})
	require.NoError(t, err)

	assert.Equal(t, "test-namespace", cfg.Namespace)
	assert.Equal(t, "test.policy.fqdn", cfg.PolicyFQDN)
}

func TestUAMPolicyConfig_ZeroValue(t *testing.T) {
	cfg := UAMPolicyConfig{}
	assert.Equal(t, "", cfg.Namespace)
	assert.Equal(t, "", cfg.PolicyFQDN)
}

func TestUAMPolicyConfig_Initialization(t *testing.T) {
	cfg := UAMPolicyConfig{
		Namespace:  "my-namespace",
		PolicyFQDN: "my.policy.fqdn",
	}
	assert.Equal(t, "my-namespace", cfg.Namespace)
	assert.Equal(t, "my.policy.fqdn", cfg.PolicyFQDN)
}
