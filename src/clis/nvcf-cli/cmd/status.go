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
	"strings"

	"nvcf-cli/internal/logging"

	"github.com/spf13/cobra"
)

// statusCmd represents the status command
var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current CLI state and configuration",
	Long: `Display the current state of the NVCF CLI including:

- Configuration settings (config file, cluster mode, kubeconfig)
- Authentication status (tokens, API keys, expiration times)
- Current function context (function ID, version ID, name)
- Last used endpoints and settings

This command helps you understand the current CLI state and troubleshoot
configuration issues.

Examples:
  # Show current status
  nvcf-cli status

  # Show status with debug information
  nvcf-cli --debug status`,
	RunE: runStatus,
}

var (
	statusShowTokens bool
)

func init() {
	rootCmd.AddCommand(statusCmd)

	// Add flags
	statusCmd.Flags().BoolVar(&statusShowTokens, "show-tokens", false, "show full tokens (security risk - only for debugging)")
}

func runStatus(cmd *cobra.Command, args []string) error {
	// Load current state
	if err := LoadStateForCurrentCommand(); err != nil {
		logging.Warning("Could not load state: %v", err)
		logging.Info("Starting with empty state")
	}

	currentState := GetCurrentState()
	sm := GetStateManagerForCurrentCommand()

	if IsJSONOutput() {
		configFileDisplay := "(default ~/.nvcf-cli.yaml)"
		if cfgFile != "" {
			configFileDisplay = cfgFile
		} else if currentState.ConfigFile != "" {
			configFileDisplay = currentState.ConfigFile
		}

		tokenStatus := "not_set"
		if currentState.Token != "" {
			if sm.IsTokenValid() {
				tokenStatus = "valid"
			} else {
				tokenStatus = "expired"
			}
		}

		apiKeyStatus := "not_set"
		if currentState.APIKey != "" {
			if sm.IsAPIKeyValid() {
				apiKeyStatus = "valid"
			} else {
				apiKeyStatus = "expired"
			}
		}

		warnings := []string{}
		if currentState.Token != "" && !sm.IsTokenValid() {
			warnings = append(warnings, "Admin token has expired")
		}
		if currentState.APIKey != "" && !sm.IsAPIKeyValid() {
			warnings = append(warnings, "API key has expired")
		}
		if !currentState.ClusterMode && (currentState.Token != "" || currentState.APIKey != "") {
			warnings = append(warnings, "Tokens present but not in cluster mode - may not be usable")
		}

		tokenValue := maskToken(currentState.Token)
		apiKeyValue := maskToken(currentState.APIKey)
		if statusShowTokens {
			tokenValue = currentState.Token
			apiKeyValue = currentState.APIKey
		}

		payload := map[string]interface{}{
			"config": map[string]interface{}{
				"configFile":  configFileDisplay,
				"clusterMode": currentState.ClusterMode,
				"kubeconfig":  valueOrDefault(currentState.KubeconfigPath, "(default kubectl config)"),
			},
			"auth": map[string]interface{}{
				"adminToken": map[string]interface{}{
					"status":     tokenStatus,
					"expiresAt":  currentState.TokenExpiration,
					"value":      tokenValue,
					"hasToken":   currentState.Token != "",
					"showTokens": statusShowTokens,
				},
				"apiKey": map[string]interface{}{
					"status":     apiKeyStatus,
					"expiresAt":  currentState.APIKeyExpiration,
					"value":      apiKeyValue,
					"hasKey":     currentState.APIKey != "",
					"showTokens": statusShowTokens,
				},
			},
			"currentFunction": map[string]interface{}{
				"hasFunction": sm.HasFunction(),
				"functionId":  currentState.FunctionID,
				"versionId":   currentState.VersionID,
				"name":        valueOrDefault(currentState.FunctionName, "(unknown)"),
			},
			"endpoints": map[string]interface{}{
				"baseUrl":   valueOrDefault(currentState.LastBaseURL, "(not set)"),
				"invokeUrl": valueOrDefault(currentState.LastInvokeURL, "(not set)"),
				"account":   valueOrDefault(currentState.LastAccount, "(not set)"),
			},
			"metadata": map[string]interface{}{
				"lastModified": currentState.LastModified,
				"cliVersion":   valueOrDefault(Version, "(unknown)"),
			},
			"warnings": warnings,
		}

		return OutputJSON(payload)
	}

	// Print header
	logging.Info("NVCF CLI Status")
	fmt.Println(strings.Repeat("=", 50))

	// Configuration section
	fmt.Printf("\n📁 Configuration:\n")

	// Show the actual config file being used, not just what's stored in state
	configFileDisplay := "(default ~/.nvcf-cli.yaml)"
	if cfgFile != "" {
		configFileDisplay = cfgFile
	} else if currentState.ConfigFile != "" {
		configFileDisplay = currentState.ConfigFile
	}
	fmt.Printf("   Config File: %s\n", configFileDisplay)
	fmt.Printf("   Cluster Mode: %v\n", currentState.ClusterMode)
	if currentState.ClusterMode {
		fmt.Printf("   Kubeconfig: %s\n", valueOrDefault(currentState.KubeconfigPath, "(default kubectl config)"))
	}

	// Authentication section
	fmt.Printf("\n🔐 Authentication:\n")

	// Admin Token
	if currentState.Token != "" {
		tokenStatus := "✅ Valid"
		if !sm.IsTokenValid() {
			tokenStatus = "❌ Expired"
		}

		tokenDisplay := currentState.Token
		if !statusShowTokens {
			tokenDisplay = maskToken(currentState.Token)
		}

		fmt.Printf("   Admin Token: %s [%s]\n", tokenDisplay, tokenStatus)
		if !currentState.TokenExpiration.IsZero() {
			fmt.Printf("   Token Expires: %s\n", currentState.TokenExpiration.Format("2006-01-02 15:04:05 MST"))
		}
	} else {
		fmt.Printf("   Admin Token: ❌ Not set\n")
		fmt.Printf("   💡 Run 'nvcf-cli init' to generate a token\n")
	}

	// API Key
	if currentState.APIKey != "" {
		apiKeyStatus := "✅ Valid"
		if !sm.IsAPIKeyValid() {
			apiKeyStatus = "❌ Expired"
		}

		keyDisplay := currentState.APIKey
		if !statusShowTokens {
			keyDisplay = maskToken(currentState.APIKey)
		}

		fmt.Printf("   API Key: %s [%s]\n", keyDisplay, apiKeyStatus)
		if !currentState.APIKeyExpiration.IsZero() {
			fmt.Printf("   API Key Expires: %s\n", currentState.APIKeyExpiration.Format("2006-01-02 15:04:05 MST"))
		}
	} else {
		fmt.Printf("   API Key: ❌ Not set\n")
	}

	// Current Function section
	fmt.Printf("\n🔧 Current Function:\n")
	if sm.HasFunction() {
		fmt.Printf("   Function ID: %s\n", currentState.FunctionID)
		fmt.Printf("   Version ID: %s\n", currentState.VersionID)
		fmt.Printf("   Name: %s\n", valueOrDefault(currentState.FunctionName, "(unknown)"))
		fmt.Printf("   Status: ✅ Ready for operations\n")
	} else {
		fmt.Printf("   Status: ❌ No function selected\n")
		fmt.Printf("   💡 Run 'nvcf-cli create' to create a function\n")
	}

	// API Endpoints section
	fmt.Printf("\n🌐 API Endpoints:\n")
	fmt.Printf("   Base URL: %s\n", valueOrDefault(currentState.LastBaseURL, "(not set)"))
	fmt.Printf("   Invoke URL: %s\n", valueOrDefault(currentState.LastInvokeURL, "(not set)"))
	fmt.Printf("   Account: %s\n", valueOrDefault(currentState.LastAccount, "(not set)"))

	// Metadata section
	fmt.Printf("\n📊 Metadata:\n")
	fmt.Printf("   Last Modified: %s\n", currentState.LastModified.Format("2006-01-02 15:04:05 MST"))
	fmt.Printf("   CLI Version: %s\n", valueOrDefault(Version, "(unknown)"))

	// Quick Actions section
	fmt.Printf("\n🚀 Quick Actions:\n")
	if !sm.IsTokenValid() {
		if currentState.Token == "" {
			fmt.Printf("   • nvcf-cli init              (generate initial token)\n")
		} else {
			fmt.Printf("   • nvcf-cli refresh           (refresh expired token)\n")
		}
	}

	if !sm.HasFunction() {
		fmt.Printf("   • nvcf-cli function create       (create a new function)\n")
	} else {
		fmt.Printf("   • nvcf-cli function deploy       (deploy current function)\n")
		fmt.Printf("   • nvcf-cli function invoke       (invoke current function)\n")
		fmt.Printf("   • nvcf-cli function delete       (delete current function)\n")
	}

	fmt.Printf("   • nvcf-cli function list         (list all functions)\n")

	// Warnings section
	warnings := []string{}

	if currentState.Token != "" && !sm.IsTokenValid() {
		warnings = append(warnings, "Admin token has expired")
	}

	if currentState.APIKey != "" && !sm.IsAPIKeyValid() {
		warnings = append(warnings, "API key has expired")
	}

	if !currentState.ClusterMode && (currentState.Token != "" || currentState.APIKey != "") {
		warnings = append(warnings, "Tokens present but not in cluster mode - may not be usable")
	}

	if len(warnings) > 0 {
		fmt.Printf("\n⚠️  Warnings:\n")
		for _, warning := range warnings {
			fmt.Printf("   • %s\n", warning)
		}
	}

	return nil
}

// Helper function to show default values
func valueOrDefault(value, defaultValue string) string {
	if value == "" {
		return defaultValue
	}
	return value
}

// Helper function to mask tokens
func maskToken(token string) string {
	if len(token) <= 20 {
		if len(token) <= 8 {
			return strings.Repeat("*", len(token))
		}
		return token[:4] + strings.Repeat("*", len(token)-8) + token[len(token)-4:]
	}
	return token[:10] + strings.Repeat("*", len(token)-20) + token[len(token)-10:]
}
