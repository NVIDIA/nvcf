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
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
)

func TestStatsConfigLegacyFilteredEventNamesKey(t *testing.T) {
	v := viper.New()
	v.Set("stats.ngc-stats-enabled-event-names", []string{"ready", "destroyed"})

	var cfg Config
	require.NoError(t, v.Unmarshal(&cfg))
	assert.Equal(t, []string{"ready", "destroyed"}, cfg.Stats.FilteredStatsEnabledEventNames)
}

func TestIndexerConfiguration(t *testing.T) {
	tests := []struct {
		name            string
		args            []string
		envVars         map[string]string
		expectedEnabled bool
	}{
		{
			name:            "default enabled",
			args:            []string{},
			envVars:         map[string]string{},
			expectedEnabled: false,
		},
		{
			name:            "disabled via CLI flag",
			args:            []string{"--indexer.enabled=false"},
			envVars:         map[string]string{},
			expectedEnabled: false,
		},
		{
			name:            "enabled via CLI flag",
			args:            []string{"--indexer.enabled=true"},
			envVars:         map[string]string{},
			expectedEnabled: true,
		},
		{
			name:            "disabled via environment variable",
			args:            []string{},
			envVars:         map[string]string{"INDEXER_ENABLED": "false"},
			expectedEnabled: false,
		},
		{
			name:            "enabled via environment variable",
			args:            []string{},
			envVars:         map[string]string{"INDEXER_ENABLED": "true"},
			expectedEnabled: true,
		},
		{
			name:            "CLI flag overrides environment variable",
			args:            []string{"--indexer.enabled=true"},
			envVars:         map[string]string{"INDEXER_ENABLED": "false"},
			expectedEnabled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear any existing environment variables
			os.Unsetenv("INDEXER_ENABLED")

			// Set test environment variables
			for key, value := range tt.envVars {
				os.Setenv(key, value)
			}
			defer func() {
				for key := range tt.envVars {
					os.Unsetenv(key)
				}
			}()

			// Create a new viper instance and cobra command for isolation
			v := viper.New()
			v.AutomaticEnv()

			// Enable environment variable binding
			v.BindEnv("indexer.enabled", "INDEXER_ENABLED")

			rootCmd := &cobra.Command{
				Use: "test",
				RunE: func(cmd *cobra.Command, args []string) error {
					return nil
				},
			}

			logger := &otelzap.SugaredLogger{}
			cliArgs := NewCliArgs(rootCmd, v, logger)
			cliArgs.SetupIndexer()

			// Parse the command line arguments
			rootCmd.SetArgs(tt.args)
			err := rootCmd.Execute()
			require.NoError(t, err)

			// Get the configuration
			var cfg Config
			err = v.Unmarshal(&cfg)
			require.NoError(t, err)

			// Verify the indexer configuration
			assert.Equal(t, tt.expectedEnabled, cfg.Indexer.Enabled, "Indexer.Enabled should match expected value")
		})
	}
}

func TestCloudEventsEnabledConfiguration(t *testing.T) {
	tests := []struct {
		name            string
		args            []string
		envVars         map[string]string
		expectedEnabled bool
	}{
		{
			name:            "default enabled",
			args:            []string{},
			envVars:         map[string]string{},
			expectedEnabled: true,
		},
		{
			name:            "disabled via CLI config flag",
			args:            []string{"--publisher.cloudevents.enabled=false"},
			envVars:         map[string]string{},
			expectedEnabled: false,
		},
		{
			name:            "enabled via CLI config flag",
			args:            []string{"--publisher.cloudevents.enabled=true"},
			envVars:         map[string]string{},
			expectedEnabled: true,
		},
		{
			name:            "disabled via environment variable",
			args:            []string{},
			envVars:         map[string]string{"PUBLISHER_CLOUDEVENTS_ENABLED": "false"},
			expectedEnabled: false,
		},
		{
			name:            "enabled via environment variable",
			args:            []string{},
			envVars:         map[string]string{"PUBLISHER_CLOUDEVENTS_ENABLED": "true"},
			expectedEnabled: true,
		},
		{
			name:            "CLI config flag overrides environment variable",
			args:            []string{"--publisher.cloudevents.enabled=true"},
			envVars:         map[string]string{"PUBLISHER_CLOUDEVENTS_ENABLED": "false"},
			expectedEnabled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Unsetenv("PUBLISHER_CLOUDEVENTS_ENABLED")

			for key, value := range tt.envVars {
				t.Setenv(key, value)
			}

			v := viper.New()
			v.AutomaticEnv()
			require.NoError(t, v.BindEnv("publisher.cloudevents.enabled", "PUBLISHER_CLOUDEVENTS_ENABLED"))

			rootCmd := &cobra.Command{
				Use: "test",
				RunE: func(cmd *cobra.Command, args []string) error {
					return nil
				},
			}

			logger := &otelzap.SugaredLogger{}
			cliArgs := NewCliArgs(rootCmd, v, logger)
			cliArgs.SetupPublisher()

			rootCmd.SetArgs(tt.args)
			require.NoError(t, rootCmd.Execute())

			var cfg Config
			require.NoError(t, v.Unmarshal(&cfg))
			assert.Equal(t, tt.expectedEnabled, cfg.Publisher.Cloudevents.Enabled)
		})
	}
}

func TestCloudEventsEnabledConfiguration_EventLedgerEnvPrefix(t *testing.T) {
	t.Setenv("EVENT_LEDGER_PUBLISHER_CLOUDEVENTS_ENABLED", "false")

	v := viper.New()
	v.AutomaticEnv()
	v.SetEnvPrefix("EVENT_LEDGER")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))

	rootCmd := &cobra.Command{
		Use: "test",
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}

	logger := &otelzap.SugaredLogger{}
	cliArgs := NewCliArgs(rootCmd, v, logger)
	cliArgs.SetupPublisher()

	require.NoError(t, rootCmd.Execute())

	var cfg Config
	require.NoError(t, v.Unmarshal(&cfg))
	assert.False(t, cfg.Publisher.Cloudevents.Enabled)
}

func TestAuthPolicyCredentialsRefreshIntervalDefault(t *testing.T) {
	v := viper.New()

	rootCmd := &cobra.Command{
		Use: "test",
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}

	logger := &otelzap.SugaredLogger{}
	cliArgs := NewCliArgs(rootCmd, v, logger)
	cliArgs.SetupAuth()

	require.NoError(t, rootCmd.Execute())

	var cfg Config
	require.NoError(t, v.Unmarshal(&cfg))
	assert.Equal(t, int64(300), cfg.Auth.Policy.CredentialsRefreshInterval)
}
