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
	"context"
	"fmt"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/logging"

	"github.com/spf13/cobra"
)

// undeployCmd represents the undeploy command
var undeployCmd = &cobra.Command{
	Use:   "undeploy",
	Short: "Undeploy the current function",
	Long: `Undeploy the current function from the NVCF platform.

This command removes the function deployment, stopping all running instances
and making the function unavailable for invocation. The function definition
itself remains and can be redeployed later.

The function to undeploy is determined by the current CLI state (function ID
and version ID). Use 'nvcf-cli status' to see the current function context.

Examples:
  # Undeploy the current function
  nvcf-cli undeploy

  # Undeploy with debug output
  nvcf-cli --debug undeploy

Prerequisites:
  - Admin token (run 'nvcf-cli init' or 'nvcf-cli refresh')
  - Current function context (run 'nvcf-cli create' first)`,
	RunE: runUndeploy,
}

var (
	undeployFunctionID string
	undeployVersionID  string
)

func init() {
	// Add flags for explicit function/version specification
	undeployCmd.Flags().StringVar(&undeployFunctionID, "function-id", "", "function ID to undeploy (overrides state)")
	undeployCmd.Flags().StringVar(&undeployVersionID, "version-id", "", "version ID to undeploy (overrides state)")
}

func runUndeploy(cmd *cobra.Command, args []string) error {
	// Load state
	if err := LoadStateForCurrentCommand(); err != nil {
		logging.Warning("Could not load state: %v", err)
	}

	sm := GetStateManagerForCurrentCommand()
	currentState := sm.GetState()

	// Load configuration
	config, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Check authentication
	if !sm.IsTokenValid() {
		logging.Error("No valid admin token found")
		logging.Info("Please run 'nvcf-cli init' or 'nvcf-cli refresh' first")
		return fmt.Errorf("admin token required for undeploy operation")
	}

	// Determine function ID and version ID
	functionID := undeployFunctionID
	versionID := undeployVersionID

	// Use state if not specified via flags
	if functionID == "" {
		functionID = currentState.FunctionID
	}
	if versionID == "" {
		versionID = currentState.VersionID
	}

	// Validate that we have both IDs
	if functionID == "" || versionID == "" {
		logging.Error("No function found to undeploy")
		if !sm.HasFunction() {
			logging.Info("Please run 'nvcf-cli create' first to create a function, or")
			logging.Info("specify --function-id and --version-id flags")
		} else {
			logging.Error("Function context is incomplete (missing ID or version)")
		}
		return fmt.Errorf("function ID and version ID required")
	}

	logging.Info("Undeploying function %s version %s...", functionID, versionID)

	// Execute undeploy based on mode
	if config.ClusterMode {
		err = undeployFunctionCluster(config, currentState.Token, functionID, versionID)
	} else {
		err = undeployFunctionDirect(config, functionID, versionID)
	}

	if err != nil {
		return fmt.Errorf("failed to undeploy function: %w", err)
	}

	logging.Success("Function undeployment request sent")
	logging.Info("Function %s version %s is being undeployed", functionID, versionID)

	// Note: We don't clear the function state here because the function still exists,
	// it's just not deployed. Users can still redeploy the same function.

	return nil
}

// undeployFunctionCluster undeploys function using kubectl in cluster mode
func undeployFunctionCluster(config *client.Config, token, functionID, versionID string) error {
	if config.ClusterConfig == nil {
		return fmt.Errorf("cluster configuration not available")
	}

	// Build the API endpoint
	endpoint := fmt.Sprintf("http://%s/v2/nvcf/deployments/functions/%s/versions/%s",
		config.ClusterConfig.APIService, functionID, versionID)

	// Execute kubectl run with curl DELETE request
	args := []string{
		"curl", "-v", "-X", "DELETE", endpoint,
		"--header", "Authorization: Bearer " + token,
	}

	_, err := executeKubectlRun(config, "undeploy-function", args)
	if err != nil {
		return fmt.Errorf("kubectl undeploy request failed: %w", err)
	}

	return nil
}

// undeployFunctionDirect undeploys function using direct HTTP client
func undeployFunctionDirect(config *client.Config, functionID, versionID string) error {
	// Create client
	nvcfClient, err := client.NewClient(config)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer nvcfClient.Close()

	// Call the undeploy API (DELETE deployment)
	ctx := context.Background()
	err = nvcfClient.DeleteDeployment(ctx, functionID, versionID, false) // graceful=false for immediate undeploy
	if err != nil {
		return fmt.Errorf("undeploy API call failed: %w", err)
	}

	return nil
}
