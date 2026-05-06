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
	"encoding/json"
	"fmt"
	"os"

	"nvcf-cli/internal/client"

	"github.com/spf13/cobra"
)

var adminCmd = &cobra.Command{
	Use:          "admin",
	Short:        "Admin commands for NVIDIA Super Admins",
	SilenceUsage: true,
	Long: `Admin commands for NVIDIA Super Admins to manage accounts, secrets, and cross-account operations.

These commands require special admin tokens with appropriate scopes:
- account_setup: Account management operations
- admin:update_secrets: Secret management operations  
- admin:queue_details: Queue information operations

All admin commands work across NVIDIA Cloud Accounts and require elevated privileges.`,
}

var secretsCmd = &cobra.Command{
	Use:          "secrets",
	Short:        "Manage secrets across accounts",
	SilenceUsage: true,
	Long: `Manage secrets for functions and telemetries across NVIDIA Cloud Accounts.

Required scope: admin:update_secrets

Examples:
  # Update secrets for a function version
  nvcf-cli admin secrets update-function --nca-id "account123" --function-id "func-456" --version-id "ver-789" --input-file secrets.json
  
  # Update secrets for telemetry
  nvcf-cli admin secrets update-telemetry --nca-id "account123" --telemetry-id "telem-456" --input-file secrets.json`,
}

var secretsUpdateFunctionCmd = &cobra.Command{
	Use:          "update-function",
	Short:        "Update secrets for a function version",
	SilenceUsage: true,
	Long: `Updates secrets for the specified function version across accounts.

Requires a bearer token with 'admin:update_secrets' scope in the HTTP Authorization header.
This command is typically used by NVIDIA Super Admins.`,
	RunE: runSecretsUpdateFunction,
}

var secretsUpdateTelemetryCmd = &cobra.Command{
	Use:          "update-telemetry",
	Short:        "Update secrets for a telemetry endpoint",
	SilenceUsage: true,
	Long: `Updates secrets for the specified telemetry endpoint across accounts.

Requires a bearer token with 'admin:update_secrets' scope in the HTTP Authorization header.
This command is typically used by NVIDIA Super Admins.`,
	RunE: runSecretsUpdateTelemetry,
}

// Secret update flags
var secretUpdateFlags struct {
	ncaId       string
	functionId  string
	versionId   string
	telemetryId string
	inputFile   string
	secretsJSON string
}

var queuesCmd = &cobra.Command{
	Use:          "queues",
	Short:        "Get queue details across accounts",
	SilenceUsage: true,
	Long: `Get queue details for functions across NVIDIA Cloud Accounts.

Required scope: admin:queue_details

Examples:
  # Get queue details for a function
  nvcf-cli admin queues function --nca-id "account123" --function-id "func-456"
  
  # Get queue details for a specific function version
  nvcf-cli admin queues version --nca-id "account123" --function-id "func-456" --version-id "ver-789"`,
}

var queuesFunctionCmd = &cobra.Command{
	Use:          "function",
	Short:        "Get queue details for a function",
	SilenceUsage: true,
	Long: `Retrieves queue details for the specified function across accounts.

Requires a bearer token with 'admin:queue_details' scope in the HTTP Authorization header.
This command is typically used by NVIDIA Super Admins.`,
	RunE: runQueuesFunction,
}

var queuesVersionCmd = &cobra.Command{
	Use:          "version",
	Short:        "Get queue details for a function version",
	SilenceUsage: true,
	Long: `Retrieves queue details for the specified function version across accounts.

Requires a bearer token with 'admin:queue_details' scope in the HTTP Authorization header.
This command is typically used by NVIDIA Super Admins.`,
	RunE: runQueuesVersion,
}

// Queue flags
var queueFlags struct {
	ncaId      string
	functionId string
	versionId  string
}

var accountsCmd = &cobra.Command{
	Use:          "accounts",
	Short:        "Manage NVIDIA Cloud Accounts",
	SilenceUsage: true,
	Long: `Manage NVIDIA Cloud Accounts including listing and updating account settings.

Required scope: account_setup

Examples:
  # List all accounts
  nvcf-cli admin accounts list
  
  # Update account settings
  nvcf-cli admin accounts update --nca-id "account123" --max-functions 10`,
}

var accountsListCmd = &cobra.Command{
	Use:          "list",
	Short:        "List all NVIDIA Cloud Accounts",
	SilenceUsage: true,
	Long: `Lists all NVIDIA Cloud Accounts onboarded with Cloud Functions.

Requires a bearer token with 'account_setup' scope in the HTTP Authorization header.
This command is typically used by NVIDIA Super Admins.`,
	RunE: runAccountsList,
}

var accountsUpdateCmd = &cobra.Command{
	Use:          "update",
	Short:        "Update NVIDIA Cloud Account settings",
	SilenceUsage: true,
	Long: `Updates the specified NVIDIA Cloud Account settings such as name, function limits, and task limits.

Requires a bearer token with 'account_setup' scope in the HTTP Authorization header.
This command is typically used by NVIDIA Super Admins.`,
	RunE: runAccountsUpdate,
}

// Account update flags
var accountUpdateFlags struct {
	ncaId                  string
	name                   string
	maxFunctions           int
	maxTasks               int
	maxTelemetries         int
	maxRegistryCredentials int
}

func init() {
	// COMMENTED OUT: Admin commands not currently exposed in CLI menu
	// rootCmd.AddCommand(adminCmd)

	// Add subcommands
	adminCmd.AddCommand(accountsCmd)
	adminCmd.AddCommand(secretsCmd)
	adminCmd.AddCommand(queuesCmd)

	// Account commands
	accountsCmd.AddCommand(accountsListCmd)
	accountsCmd.AddCommand(accountsUpdateCmd)

	// Secret commands
	secretsCmd.AddCommand(secretsUpdateFunctionCmd)
	secretsCmd.AddCommand(secretsUpdateTelemetryCmd)

	// Queue commands
	queuesCmd.AddCommand(queuesFunctionCmd)
	queuesCmd.AddCommand(queuesVersionCmd)

	// Account update flags
	accountsUpdateCmd.Flags().StringVar(&accountUpdateFlags.ncaId, "nca-id", "", "NVIDIA Cloud Account ID (required)")
	accountsUpdateCmd.Flags().StringVar(&accountUpdateFlags.name, "name", "", "Human readable account/customer name (4-36 chars)")
	accountsUpdateCmd.Flags().IntVar(&accountUpdateFlags.maxFunctions, "max-functions", -1, "Maximum number of functions allowed")
	accountsUpdateCmd.Flags().IntVar(&accountUpdateFlags.maxTasks, "max-tasks", -1, "Maximum number of tasks allowed")
	accountsUpdateCmd.Flags().IntVar(&accountUpdateFlags.maxTelemetries, "max-telemetries", -1, "Maximum number of telemetries allowed (max: 50)")
	accountsUpdateCmd.Flags().IntVar(&accountUpdateFlags.maxRegistryCredentials, "max-registry-credentials", -1, "Maximum number of registry credentials allowed (max: 50)")
	accountsUpdateCmd.MarkFlagRequired("nca-id")

	// Secret update function flags
	secretsUpdateFunctionCmd.Flags().StringVar(&secretUpdateFlags.ncaId, "nca-id", "", "NVIDIA Cloud Account ID (required)")
	secretsUpdateFunctionCmd.Flags().StringVar(&secretUpdateFlags.functionId, "function-id", "", "Function ID (required)")
	secretsUpdateFunctionCmd.Flags().StringVar(&secretUpdateFlags.versionId, "version-id", "", "Function version ID (required)")
	secretsUpdateFunctionCmd.Flags().StringVar(&secretUpdateFlags.inputFile, "input-file", "", "JSON file with secret data")
	secretsUpdateFunctionCmd.Flags().StringVar(&secretUpdateFlags.secretsJSON, "secrets", "", "JSON string with secret data")
	secretsUpdateFunctionCmd.MarkFlagRequired("nca-id")
	secretsUpdateFunctionCmd.MarkFlagRequired("function-id")
	secretsUpdateFunctionCmd.MarkFlagRequired("version-id")

	// Secret update telemetry flags
	secretsUpdateTelemetryCmd.Flags().StringVar(&secretUpdateFlags.ncaId, "nca-id", "", "NVIDIA Cloud Account ID (required)")
	secretsUpdateTelemetryCmd.Flags().StringVar(&secretUpdateFlags.telemetryId, "telemetry-id", "", "Telemetry ID (required)")
	secretsUpdateTelemetryCmd.Flags().StringVar(&secretUpdateFlags.inputFile, "input-file", "", "JSON file with secret data")
	secretsUpdateTelemetryCmd.Flags().StringVar(&secretUpdateFlags.secretsJSON, "secrets", "", "JSON string with secret data")
	secretsUpdateTelemetryCmd.MarkFlagRequired("nca-id")
	secretsUpdateTelemetryCmd.MarkFlagRequired("telemetry-id")

	// Queue function flags
	queuesFunctionCmd.Flags().StringVar(&queueFlags.ncaId, "nca-id", "", "NVIDIA Cloud Account ID (required)")
	queuesFunctionCmd.Flags().StringVar(&queueFlags.functionId, "function-id", "", "Function ID (required)")
	queuesFunctionCmd.MarkFlagRequired("nca-id")
	queuesFunctionCmd.MarkFlagRequired("function-id")

	// Queue version flags
	queuesVersionCmd.Flags().StringVar(&queueFlags.ncaId, "nca-id", "", "NVIDIA Cloud Account ID (required)")
	queuesVersionCmd.Flags().StringVar(&queueFlags.functionId, "function-id", "", "Function ID (required)")
	queuesVersionCmd.Flags().StringVar(&queueFlags.versionId, "version-id", "", "Function version ID (required)")
	queuesVersionCmd.MarkFlagRequired("nca-id")
	queuesVersionCmd.MarkFlagRequired("function-id")
	queuesVersionCmd.MarkFlagRequired("version-id")
}

func runAccountsList(cmd *cobra.Command, args []string) error {
	// Load client configuration
	clientConfig, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Create client
	nvcfClient, err := client.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("failed to create NVCF client: %w", err)
	}
	defer nvcfClient.Close()

	ctx := context.Background()

	fmt.Println("Listing all NVIDIA Cloud Accounts...")

	// List accounts
	resp, err := nvcfClient.ListAccounts(ctx)
	if err != nil {
		return fmt.Errorf("failed to list accounts: %w", err)
	}

	if len(resp.Accounts) == 0 {
		fmt.Println("No accounts found.")
		return nil
	}

	// Display accounts in a table format
	fmt.Printf("\n%-20s %-30s %-13s %-10s %-15s %-17s %-15s\n",
		"Account ID", "Name", "Max Functions", "Max Tasks", "Max Telemetries", "Max Reg Creds", "Admin Clients")
	fmt.Printf("%-20s %-30s %-13s %-10s %-15s %-17s %-15s\n",
		"----------", "----", "-------------", "---------", "---------------", "-------------", "-------------")

	for _, account := range resp.Accounts {
		clientIds := "None"
		if len(account.AdminClientIds) > 0 {
			if len(account.AdminClientIds) == 1 {
				clientIds = account.AdminClientIds[0]
			} else {
				clientIds = fmt.Sprintf("%s (+%d more)", account.AdminClientIds[0], len(account.AdminClientIds)-1)
			}
		}

		fmt.Printf("%-20s %-30s %-13d %-10d %-15d %-17d %-15s\n",
			account.NcaId,
			account.Name,
			account.MaxFunctionsAllowed,
			account.MaxTasksAllowed,
			account.MaxTelemetriesAllowed,
			account.MaxRegistryCredentialsAllowed,
			clientIds)
	}

	fmt.Printf("\nTotal: %d accounts\n", len(resp.Accounts))
	return nil
}

func runAccountsUpdate(cmd *cobra.Command, args []string) error {
	// Validate that at least one update field is provided
	if accountUpdateFlags.name == "" && accountUpdateFlags.maxFunctions == -1 &&
		accountUpdateFlags.maxTasks == -1 && accountUpdateFlags.maxTelemetries == -1 &&
		accountUpdateFlags.maxRegistryCredentials == -1 {
		return fmt.Errorf("at least one update field must be provided (--name, --max-functions, --max-tasks, --max-telemetries, --max-registry-credentials)")
	}

	// Load client configuration
	clientConfig, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Create client
	nvcfClient, err := client.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("failed to create NVCF client: %w", err)
	}
	defer nvcfClient.Close()

	// Prepare update request
	req := &client.AccountUpdateRequest{}

	if accountUpdateFlags.name != "" {
		req.Name = accountUpdateFlags.name
	}
	if accountUpdateFlags.maxFunctions >= 0 {
		req.MaxFunctionsAllowed = &accountUpdateFlags.maxFunctions
	}
	if accountUpdateFlags.maxTasks >= 0 {
		req.MaxTasksAllowed = &accountUpdateFlags.maxTasks
	}
	if accountUpdateFlags.maxTelemetries >= 0 {
		// Validate max telemetries limit (max: 50)
		if accountUpdateFlags.maxTelemetries > 50 {
			return fmt.Errorf("max-telemetries cannot exceed 50")
		}
		req.MaxTelemetriesAllowed = &accountUpdateFlags.maxTelemetries
	}
	if accountUpdateFlags.maxRegistryCredentials >= 0 {
		// Validate max registry credentials limit (max: 50)
		if accountUpdateFlags.maxRegistryCredentials > 50 {
			return fmt.Errorf("max-registry-credentials cannot exceed 50")
		}
		req.MaxRegistryCredentialsAllowed = &accountUpdateFlags.maxRegistryCredentials
	}

	ctx := context.Background()

	fmt.Printf("Updating account %s...\n", accountUpdateFlags.ncaId)

	// Update account
	resp, err := nvcfClient.UpdateAccount(ctx, accountUpdateFlags.ncaId, req)
	if err != nil {
		return fmt.Errorf("failed to update account: %w", err)
	}

	fmt.Printf("Account updated successfully!\n")
	fmt.Printf("Account ID: %s\n", resp.Account.NcaId)
	fmt.Printf("Name: %s\n", resp.Account.Name)
	fmt.Printf("Max Functions: %d\n", resp.Account.MaxFunctionsAllowed)
	fmt.Printf("Max Tasks: %d\n", resp.Account.MaxTasksAllowed)
	fmt.Printf("Max Telemetries: %d\n", resp.Account.MaxTelemetriesAllowed)
	fmt.Printf("Max Registry Credentials: %d\n", resp.Account.MaxRegistryCredentialsAllowed)

	if len(resp.Account.AdminClientIds) > 0 {
		fmt.Printf("Admin Client IDs: %s\n", formatClientIds(resp.Account.AdminClientIds))
	}

	return nil
}

func runSecretsUpdateFunction(cmd *cobra.Command, args []string) error {
	// Validate input - either file or JSON string must be provided
	if secretUpdateFlags.inputFile == "" && secretUpdateFlags.secretsJSON == "" {
		return fmt.Errorf("either --input-file or --secrets must be provided")
	}
	if secretUpdateFlags.inputFile != "" && secretUpdateFlags.secretsJSON != "" {
		return fmt.Errorf("only one of --input-file or --secrets can be provided")
	}

	// Load secret data
	var secretData interface{}
	if secretUpdateFlags.inputFile != "" {
		data, err := os.ReadFile(secretUpdateFlags.inputFile)
		if err != nil {
			return fmt.Errorf("failed to read input file '%s': %w", secretUpdateFlags.inputFile, err)
		}
		if err := json.Unmarshal(data, &secretData); err != nil {
			return fmt.Errorf("failed to parse JSON from file '%s': %w", secretUpdateFlags.inputFile, err)
		}
	} else {
		if err := json.Unmarshal([]byte(secretUpdateFlags.secretsJSON), &secretData); err != nil {
			return fmt.Errorf("failed to parse JSON from --secrets: %w", err)
		}
	}

	// Load client configuration
	clientConfig, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Create client
	nvcfClient, err := client.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("failed to create NVCF client: %w", err)
	}
	defer nvcfClient.Close()

	ctx := context.Background()

	fmt.Printf("Updating secrets for function %s version %s in account %s...\n",
		secretUpdateFlags.functionId, secretUpdateFlags.versionId, secretUpdateFlags.ncaId)

	// Update function secrets (cross-account admin operation)
	err = nvcfClient.UpdateFunctionSecretsAdmin(ctx, secretUpdateFlags.ncaId, secretUpdateFlags.functionId, secretUpdateFlags.versionId, secretData)
	if err != nil {
		return fmt.Errorf("failed to update function secrets: %w", err)
	}

	fmt.Printf("Function secrets updated successfully!\n")
	return nil
}

func runSecretsUpdateTelemetry(cmd *cobra.Command, args []string) error {
	// Validate input - either file or JSON string must be provided
	if secretUpdateFlags.inputFile == "" && secretUpdateFlags.secretsJSON == "" {
		return fmt.Errorf("either --input-file or --secrets must be provided")
	}
	if secretUpdateFlags.inputFile != "" && secretUpdateFlags.secretsJSON != "" {
		return fmt.Errorf("only one of --input-file or --secrets can be provided")
	}

	// Load secret data
	var secretData interface{}
	if secretUpdateFlags.inputFile != "" {
		data, err := os.ReadFile(secretUpdateFlags.inputFile)
		if err != nil {
			return fmt.Errorf("failed to read input file '%s': %w", secretUpdateFlags.inputFile, err)
		}
		if err := json.Unmarshal(data, &secretData); err != nil {
			return fmt.Errorf("failed to parse JSON from file '%s': %w", secretUpdateFlags.inputFile, err)
		}
	} else {
		if err := json.Unmarshal([]byte(secretUpdateFlags.secretsJSON), &secretData); err != nil {
			return fmt.Errorf("failed to parse JSON from --secrets: %w", err)
		}
	}

	// Load client configuration
	clientConfig, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Create client
	nvcfClient, err := client.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("failed to create NVCF client: %w", err)
	}
	defer nvcfClient.Close()

	ctx := context.Background()

	fmt.Printf("Updating secrets for telemetry %s in account %s...\n",
		secretUpdateFlags.telemetryId, secretUpdateFlags.ncaId)

	// Update telemetry secrets (cross-account admin operation)
	err = nvcfClient.UpdateTelemetrySecretsAdmin(ctx, secretUpdateFlags.ncaId, secretUpdateFlags.telemetryId, secretData)
	if err != nil {
		return fmt.Errorf("failed to update telemetry secrets: %w", err)
	}

	fmt.Printf("Telemetry secrets updated successfully!\n")
	return nil
}

func runQueuesFunction(cmd *cobra.Command, args []string) error {
	// Load client configuration
	clientConfig, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Create client
	nvcfClient, err := client.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("failed to create NVCF client: %w", err)
	}
	defer nvcfClient.Close()

	ctx := context.Background()

	fmt.Printf("Getting queue details for function %s in account %s...\n",
		queueFlags.functionId, queueFlags.ncaId)

	// Get queue details
	resp, err := nvcfClient.GetFunctionQueueDetails(ctx, queueFlags.ncaId, queueFlags.functionId)
	if err != nil {
		return fmt.Errorf("failed to get queue details: %w", err)
	}

	// Display queue details
	if len(resp.Queues) == 0 {
		fmt.Println("No queue details found for this function.")
		return nil
	}

	fmt.Printf("\n%-40s %-40s %-30s %-15s %-10s\n", "Function ID", "Version ID", "Function Name", "Status", "Queue Depth")
	fmt.Printf("%-40s %-40s %-30s %-15s %-10s\n", "----------", "----------", "-------------", "------", "-----------")

	for _, queue := range resp.Queues {
		fmt.Printf("%-40s %-40s %-30s %-15s %-10d\n",
			resp.FunctionID,
			queue.FunctionVersionID,
			queue.FunctionName,
			queue.FunctionStatus,
			queue.QueueDepth)
	}

	return nil
}

func runQueuesVersion(cmd *cobra.Command, args []string) error {
	// Load client configuration
	clientConfig, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Create client
	nvcfClient, err := client.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("failed to create NVCF client: %w", err)
	}
	defer nvcfClient.Close()

	ctx := context.Background()

	fmt.Printf("Getting queue details for function %s version %s in account %s...\n",
		queueFlags.functionId, queueFlags.versionId, queueFlags.ncaId)

	// Get queue details for specific version
	resp, err := nvcfClient.GetFunctionVersionQueueDetails(ctx, queueFlags.ncaId, queueFlags.functionId, queueFlags.versionId)
	if err != nil {
		return fmt.Errorf("failed to get queue details: %w", err)
	}

	// Display queue details
	if len(resp.Queues) == 0 {
		fmt.Println("No queue details found for this function version.")
		return nil
	}

	fmt.Printf("\n%-40s %-40s %-30s %-15s %-10s\n", "Function ID", "Version ID", "Function Name", "Status", "Queue Depth")
	fmt.Printf("%-40s %-40s %-30s %-15s %-10s\n", "----------", "----------", "-------------", "------", "-----------")

	for _, queue := range resp.Queues {
		fmt.Printf("%-40s %-40s %-30s %-15s %-10d\n",
			resp.FunctionID,
			queue.FunctionVersionID,
			queue.FunctionName,
			queue.FunctionStatus,
			queue.QueueDepth)
	}

	return nil
}

// Helper function to format client IDs for display
func formatClientIds(clientIds []string) string {
	if len(clientIds) == 0 {
		return "None"
	}
	if len(clientIds) == 1 {
		return clientIds[0]
	}

	// Convert to JSON for multiple IDs
	jsonBytes, err := json.Marshal(clientIds)
	if err != nil {
		return fmt.Sprintf("%v", clientIds)
	}
	return string(jsonBytes)
}
