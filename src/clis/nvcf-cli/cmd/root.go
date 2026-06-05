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
	"os"

	"time"

	"nvcf-cli/internal/logging"
	"nvcf-cli/internal/state"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

var (
	configStateManager    *state.StateManager
	configStateManagerKey string
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "nvcf-cli",
	Short: "NVIDIA Cloud Functions CLI",
	Long: `A command-line interface for managing NVIDIA Cloud Functions (NVCF).

This tool provides a simple way to create, deploy, delete, and invoke 
cloud functions using API key authentication.

Operation Mode:

All operations use direct HTTPS connections to NVCF API endpoints.
No kubectl or kubeconfig needed - everything works via external APIs!

Configuration:

Config file search order (priority):
  1. --config flag (explicit path)
  2. ./.nvcf-cli.yaml (current working directory)
  3. ~/.nvcf-cli.yaml (home directory)

Use --config flag to specify different environment configurations:
  --config dev.yaml    (development environment)  
  --config prod.yaml   (production environment)

Template file: .nvcf-cli.yaml.template (copy to .nvcf-cli.yaml and customize)

Authentication:

Generate tokens or use existing credentials:
  NVCF_API_KEY (default for list, invoke, queue details)
  NVCF_TOKEN (default for create, deploy, delete, and cluster management)

For NVCF API endpoints, either bearer type can be used when it includes the
required scope. Self-hosted SIS cluster management uses NVCF_TOKEN.

Token Generation:
  Run: nvcf-cli init
  - Calls API Keys service directly (no kubectl needed!)
  - Stores tokens in ~/.nvcf-cli.state
  - Works with both production and staging environments

API Endpoints:
  NVCF_BASE_HTTP_URL (default: https://api.nvcf.nvidia.com)
  NVCF_BASE_GRPC_URL (default: grpc.nvcf.nvidia.com:443)
  NVCF_INVOKE_URL (dedicated invocation endpoint)
  API_KEYS_SERVICE_URL (API key management endpoint)
  API_KEYS_ADMIN_SERVICE_URL (admin token generation endpoint; falls back to API_KEYS_SERVICE_URL)

Examples:
  # Token generation
  nvcf-cli init                                       # Generate admin token
  nvcf-cli refresh                                    # Refresh token
  
  # Function management
  nvcf-cli function create --input-file function.json
  nvcf-cli function list
  nvcf-cli function invoke --input-file invoke.json
  
  # Use different environments
  nvcf-cli --config staging.yaml init
  nvcf-cli --config prod.yaml function list
  
  # Staging environment with env vars
  API_KEYS_SERVICE_URL=https://api-keys.shqa.stg.nvcf.nvidia.com nvcf-cli init
  NVCF_BASE_HTTP_URL=https://api.shqa.stg.nvcf.nvidia.com nvcf-cli function list
  
  # Check status
  nvcf-cli status                                     # View current configuration`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		logging.SetJSONOutput(jsonOutput)
	},
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() error {
	return rootCmd.Execute()
}

func activeConfigFileForState() string {
	if cfgFile != "" {
		return cfgFile
	}
	return viper.ConfigFileUsed()
}

// GetCurrentConfigName returns the name of the current config file for state management
func GetCurrentConfigName() string {
	return activeConfigFileForState()
}

// GetStateManagerForCurrentCommand returns the appropriate state manager for the current config context
func GetStateManagerForCurrentCommand() *state.StateManager {
	configFile := activeConfigFileForState()
	if configFile == "" {
		return state.DefaultStateManager
	}

	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	key := home + "\x00" + configFile
	if configStateManager != nil && configStateManagerKey == key {
		return configStateManager
	}

	configStateManager = state.GetStateManagerForConfig(configFile)
	configStateManagerKey = key
	return configStateManager
}

// LoadStateForCurrentCommand loads state using the current config context
func LoadStateForCurrentCommand() error {
	sm := GetStateManagerForCurrentCommand()
	return sm.Load()
}

// SaveStateForCurrentCommand saves state using the current config context
func SaveStateForCurrentCommand() error {
	sm := GetStateManagerForCurrentCommand()
	configFile := activeConfigFileForState()

	// Update config file path in state when a config file is active.
	if configFile != "" {
		currentState := sm.GetState()
		sm.SetConfig(configFile, currentState.KubeconfigPath, currentState.ClusterMode)
	}

	return sm.Save()
}

// GetCurrentState returns the state for the current config context
func GetCurrentState() *state.State {
	sm := GetStateManagerForCurrentCommand()
	return sm.GetState()
}

// SetCurrentFunction sets the current function using the current config context
func SetCurrentFunction(functionID, versionID, functionName string) {
	sm := GetStateManagerForCurrentCommand()
	sm.SetFunction(functionID, versionID, functionName)
}

// SetCurrentTokens sets the tokens using the current config context
func SetCurrentTokens(token, apiKey string, tokenExp, apiKeyExp time.Time) {
	sm := GetStateManagerForCurrentCommand()
	sm.SetTokens(token, apiKey, tokenExp, apiKeyExp)
}

// HasCurrentFunction checks if there's a function in the current config context
func HasCurrentFunction() bool {
	sm := GetStateManagerForCurrentCommand()
	return sm.HasFunction()
}

func init() {
	cobra.OnInitialize(initConfig)

	// Global flags
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.nvcf-cli.yaml)")
	rootCmd.PersistentFlags().Bool("debug", false, "enable debug logging")
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "output JSON for automation")

	// Bind flags to viper
	viper.BindPFlag("debug", rootCmd.PersistentFlags().Lookup("debug"))
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	if cfgFile != "" {
		// Use config file from the flag.
		viper.SetConfigFile(cfgFile)
	} else {
		// Search for config file in multiple locations (priority order):
		// 1. Current working directory
		// 2. Home directory

		// Get current working directory
		cwd, err := os.Getwd()
		if err == nil {
			viper.AddConfigPath(cwd)
		}

		// Find home directory
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error finding home directory: %v\n", err)
			os.Exit(1)
		}

		// Add home directory as second search path
		viper.AddConfigPath(home)
		viper.SetConfigType("yaml")
		viper.SetConfigName(".nvcf-cli")
	}

	// Set up automatic environment variable mapping
	// This allows config keys to automatically map to NVCF_* environment variables
	// Example: base_http_url in config → NVCF_BASE_HTTP_URL env var
	viper.SetEnvPrefix("NVCF")
	viper.AutomaticEnv()

	// Bind environment variables that don't use the NVCF_ prefix
	viper.BindEnv("api_keys_service_url", "API_KEYS_SERVICE_URL")
	viper.BindEnv("api_keys_admin_service_url", "API_KEYS_ADMIN_SERVICE_URL")
	viper.BindEnv("api_keys_service_id", "API_KEYS_SERVICE_ID")
	viper.BindEnv("api_keys_issuer_service", "API_KEYS_ISSUER_SERVICE")
	viper.BindEnv("api_keys_owner_id", "API_KEYS_OWNER_ID")

	// Host header overrides for hostname-based routing (self-hosted deployments)
	viper.BindEnv("api_keys_host", "API_KEYS_HOST")
	viper.BindEnv("api_host", "API_HOST")
	viper.BindEnv("invoke_host", "INVOKE_HOST")
	// icms_host uses the NVCF_-prefixed form to match the documented
	// env var in clusters.go and the os.Getenv("NVCF_ICMS_HOST") read
	// in self_hosted_control_plane_profile.go's sisHost resolution.
	viper.BindEnv("icms_host", "NVCF_ICMS_HOST")
	// Cluster-validator image is sourced from the same config layer as the
	// other endpoints so operators can flip between staging and prod by
	// swapping --config files. The env name matches the historical
	// NVCF_CLI_CLUSTER_VALIDATOR_IMAGE override.
	viper.BindEnv("cluster_validator_image", "NVCF_CLI_CLUSTER_VALIDATOR_IMAGE")

	// If a config file is found, read it in.
	if err := viper.ReadInConfig(); err == nil && viper.GetBool("debug") {
		fmt.Fprintf(os.Stderr, "Using config file: %s\n", viper.ConfigFileUsed())
	}
}
