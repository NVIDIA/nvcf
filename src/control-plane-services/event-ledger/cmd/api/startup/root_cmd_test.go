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

package startup

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
)

func TestConfigFilesFromEnvUsesEventLedgerConfig(t *testing.T) {
	t.Setenv("EVENT_LEDGER_CONFIG", "/config/secrets.json, /config/override.json,, ")

	assert.Equal(t, []string{"/config/secrets.json", "/config/override.json"}, configFilesFromEnv())
}

func TestConfigDefaultsAuthenticationEnabled(t *testing.T) {
	v := newConfigViper()
	var cfg config.Config

	require.NoError(t, v.Unmarshal(&cfg))
	assert.True(t, cfg.Auth.Enabled)
}

func TestConfigAllowsAuthenticationToBeExplicitlyDisabled(t *testing.T) {
	v := newConfigViper()
	v.SetConfigType("json")
	require.NoError(t, v.ReadConfig(strings.NewReader(`{"auth":{"enabled":false}}`)))

	var cfg config.Config
	require.NoError(t, v.Unmarshal(&cfg))
	assert.False(t, cfg.Auth.Enabled)
}

func TestApplyRuntimeFlagOverrides_CloudEventsConfigPreservedWhenDisableFlagOmitted(t *testing.T) {
	cmd := testRuntimeFlagCommand(t)
	cfg := config.Config{
		Publisher: config.PublisherConfig{
			Cloudevents: config.CloudEventsConfig{Enabled: false},
		},
	}

	require.NoError(t, applyRuntimeFlagOverrides(cmd, &cfg))

	assert.False(t, cfg.Publisher.Cloudevents.Enabled)
}

func TestApplyRuntimeFlagOverrides_CloudEventsDisabledByLegacyFlag(t *testing.T) {
	cmd := testRuntimeFlagCommand(t)
	require.NoError(t, cmd.Flags().Set("disable-cloudevent", "true"))
	cfg := config.Config{
		Publisher: config.PublisherConfig{
			Cloudevents: config.CloudEventsConfig{Enabled: true},
		},
	}

	require.NoError(t, applyRuntimeFlagOverrides(cmd, &cfg))

	assert.False(t, cfg.Publisher.Cloudevents.Enabled)
}

func TestApplyRuntimeFlagOverrides_AuthAndProfilingConfigPreservedWhenFlagsOmitted(t *testing.T) {
	cmd := testRuntimeFlagCommand(t)
	cfg := config.Config{
		Auth:      config.AuthConfig{Enabled: false},
		Profiling: config.ProfilingConfig{Enabled: true},
	}

	require.NoError(t, applyRuntimeFlagOverrides(cmd, &cfg))

	assert.False(t, cfg.Auth.Enabled)
	assert.True(t, cfg.Profiling.Enabled)
}

func TestApplyRuntimeFlagOverrides_AuthAndProfilingOverrideWhenFlagsSet(t *testing.T) {
	cmd := testRuntimeFlagCommand(t)
	require.NoError(t, cmd.Flags().Set("disable-authentication", "true"))
	require.NoError(t, cmd.Flags().Set("enable-profiling", "true"))
	cfg := config.Config{
		Auth:      config.AuthConfig{Enabled: true},
		Profiling: config.ProfilingConfig{Enabled: false},
	}

	require.NoError(t, applyRuntimeFlagOverrides(cmd, &cfg))

	assert.False(t, cfg.Auth.Enabled)
	assert.True(t, cfg.Profiling.Enabled)
}

func testRuntimeFlagCommand(t *testing.T) *cobra.Command {
	t.Helper()

	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().Bool("disable-authentication", false, "")
	cmd.Flags().Bool("enable-profiling", false, "")
	cmd.Flags().Bool("disable-cloudevent", false, "")

	return cmd
}
