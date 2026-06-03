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

package cmd

import (
	"fmt"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/logging"

	"github.com/spf13/cobra"
)

// refreshCmd represents the refresh command
var refreshCmd = &cobra.Command{
	Use:   "refresh",
	Short: "Refresh admin token (keeps function state)",
	Long: `Refresh the admin token while preserving the current function context.

This command:
- Loads the existing state including current function context
- Generates a fresh admin token from the API Keys service
- Preserves the current function ID and version ID
- Updates the saved token for subsequent commands

Unlike 'init', this command maintains your current function context,
making it useful when your token expires but you want to continue
working with the same function.

Examples:
  # Refresh token while keeping current function state
  nvcf-cli refresh

  # Refresh with custom API Keys service URL
  API_KEYS_SERVICE_URL=https://api-keys.shqa.stg.nvcf.nvidia.com nvcf-cli refresh

  # Refresh with dedicated admin token service URL
  API_KEYS_ADMIN_SERVICE_URL=https://api-keys-admin.shqa.stg.nvcf.nvidia.com nvcf-cli refresh`,
	RunE: runRefresh,
}

func init() {
	rootCmd.AddCommand(refreshCmd)
}

func runRefresh(cmd *cobra.Command, args []string) error {
	// Load current state (including function context)
	if err := LoadStateForCurrentCommand(); err != nil {
		logging.Warning("Could not load existing state: %v", err)
	}

	sm := GetStateManagerForCurrentCommand()
	currentState := sm.GetState()

	// Load configuration without requiring existing authentication
	config, err := client.LoadConfigWithoutAuth()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	logging.Info("Refreshing admin token (keeping function state)...")
	logging.Info("Generating new admin token from API Keys service...")

	// Generate new admin token via API endpoint
	token, expiration, err := generateAdminTokenViaAPI(config)
	if err != nil {
		return fmt.Errorf("failed to generate admin token: %w", err)
	}

	if token == "" {
		return fmt.Errorf("failed to extract admin token from API response")
	}

	// Update only the token, preserve function state
	sm.SetTokens(token, currentState.APIKey, expiration, currentState.APIKeyExpiration)
	sm.SetConfig("", config.KubeconfigPath, config.ClusterMode)

	// Set endpoints - use cluster account if available, otherwise default
	account := "nvcf-default"
	if config.ClusterConfig != nil && config.ClusterConfig.NVCFAccount != "" {
		account = config.ClusterConfig.NVCFAccount
	}
	sm.SetEndpoints(config.BaseHTTPURL, config.BaseInvokeURL, account)

	if err := SaveStateForCurrentCommand(); err != nil {
		logging.Warning("Failed to save state: %v", err)
	}

	logging.Success("Admin token refreshed")
	logging.Plain("New Token: %s", token)
	if !expiration.IsZero() {
		logging.Plain("Expires: %s", expiration.Format("2006-01-02 15:04:05"))
	}

	// Show preserved function state
	if sm.HasFunction() {
		logging.Plain("Function ID: %s", currentState.FunctionID)
		logging.Plain("Version ID: %s", currentState.VersionID)
		if currentState.FunctionName != "" {
			logging.Plain("Function Name: %s", currentState.FunctionName)
		}
	}

	if currentState.APIKey != "" {
		logging.Plain("API Key: <preserved>")
	}

	return nil
}
