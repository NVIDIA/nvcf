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
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/logging"

	"github.com/spf13/cobra"
)

// registryCmd represents the registry-credential command group
var registryCmd = &cobra.Command{
	Use:   "registry-credential",
	Short: "Manage registry credentials",
	Long: `Manage registry credentials for accessing private container registries, Helm charts, models, and resources.

Registry credentials allow NVCF to access private registries for pulling container images and other artifacts.
All registry credential operations require the 'manage_registry_credentials' scope.

Available Commands:
  list              List all registry credentials
  add               Add a new registry credential
  get               Get details of a specific registry credential
  update            Update an existing registry credential
  delete            Delete a registry credential
  list-recognized   List all recognized registries

Examples:
  # List all registry credentials
  nvcf-cli registry-credential list

  # Add credentials for a private Docker registry
  nvcf-cli registry-credential add \
    --hostname myregistry.example.com \
    --username myuser \
    --password mypass \
    --artifact-type CONTAINER \
    --description "My private registry"

  # Get details of a specific credential
  nvcf-cli registry-credential get <credential-id>

  # Delete a credential
  nvcf-cli registry-credential delete <credential-id>

  # List recognized registries
  nvcf-cli registry-credential list-recognized`,
}

// registryListCmd represents the list subcommand
var registryListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all registry credentials",
	Long: `List all registry credentials associated with the authenticated NVIDIA Cloud Account.

You can filter credentials by artifact type and provisioned by (system or user).

Examples:
  # List all credentials
  nvcf-cli registry-credential list

  # List only container credentials
  nvcf-cli registry-credential list --artifact-type CONTAINER

  # List only user-provisioned credentials
  nvcf-cli registry-credential list --provisioned-by USER

  # List container and model credentials
  nvcf-cli registry-credential list --artifact-type CONTAINER --artifact-type MODEL`,
	RunE: runListRegistryCredentials,
}

// registryAddCmd represents the add subcommand
var registryAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a new registry credential",
	Long: `Add a new registry credential to access private registries.

You can provide credentials in two ways:
1. Separate username and password (CLI will encode them automatically)
2. Pre-encoded base64 secret in 'username:password' format

You must specify at least one artifact type that this credential can access.

Required flags:
  --hostname         Registry hostname (e.g., myregistry.example.com)
  --artifact-type    Artifact types (CONTAINER, HELM, MODEL, RESOURCE) - can be specified multiple times

Authentication (choose one):
  Option 1 - Separate credentials:
    --username       Registry username
    --password       Registry password
  
  Option 2 - Pre-encoded secret:
    --secret         Base64 encoded 'username:password' string

Optional flags:
  --description      Description of the credential
  --tag              Tags for the credential - can be specified multiple times

Examples:
  # Add credentials using username and password (CLI will encode them)
  nvcf-cli registry-credential add \
    --hostname myregistry.example.com \
    --username myuser \
    --password mypass \
    --artifact-type CONTAINER \
    --description "My private Docker registry"

  # Add credentials using pre-encoded secret
  nvcf-cli registry-credential add \
    --hostname nvcr.io \
    --secret "JG9hdXRodG9rZW46ZjRvYm5lbjVrcGhpamZvcTI5NHFnY3Rna3Y6YmQ4YWM0OTEtZDllMi00YWJiLWJmOTQtMTNhMjk2ZTgxYzUw" \
    --artifact-type CONTAINER \
    --description "NVIDIA Container Registry"

  # Add credentials for multiple artifact types with tags
  nvcf-cli registry-credential add \
    --hostname myregistry.example.com \
    --username myuser \
    --password mypass \
    --artifact-type CONTAINER \
    --artifact-type MODEL \
    --tag "environment:prod" \
    --tag "team:ml" \
    --description "Production ML registry"`,
	RunE: runAddRegistryCredential,
}

// registryGetCmd represents the get subcommand
var registryGetCmd = &cobra.Command{
	Use:   "get <credential-id>",
	Short: "Get details of a specific registry credential",
	Long: `Get detailed information about a specific registry credential.

Displays all metadata including hostname, artifact types, tags, description,
creation time, and last update time.

Example:
  nvcf-cli registry-credential get 12345678-1234-1234-1234-123456789012`,
	Args: cobra.ExactArgs(1),
	RunE: runGetRegistryCredential,
}

// registryUpdateCmd represents the update subcommand
var registryUpdateCmd = &cobra.Command{
	Use:   "update <credential-id>",
	Short: "Update an existing registry credential",
	Long: `Update an existing registry credential.

You can update the password and add additional artifact types.
Note: Tags and description cannot be updated after creation.
Note: Artifact types are additive - they will be added to existing types, not replace them.

Optional flags:
  --username         New registry username (must be provided with --password)
  --password         New registry password (must be provided with --username)
  --artifact-type    Artifact type(s) to ADD (additive, not replace) - can be specified multiple times

Examples:
  # Update just the password
  nvcf-cli registry-credential update 12345678-1234-1234-1234-123456789012 \
    --username myuser \
    --password newpass

  # Add additional artifact types only (no credentials needed)
  nvcf-cli registry-credential update 12345678-1234-1234-1234-123456789012 \
    --artifact-type HELM \
    --artifact-type MODEL

  # Update both credentials and add artifact types
  nvcf-cli registry-credential update 12345678-1234-1234-1234-123456789012 \
    --username myuser \
    --password mypass \
    --artifact-type HELM`,
	Args: cobra.ExactArgs(1),
	RunE: runUpdateRegistryCredential,
}

// registryDeleteCmd represents the delete subcommand
var registryDeleteCmd = &cobra.Command{
	Use:   "delete <credential-id>",
	Short: "Delete a registry credential",
	Long: `Delete a registry credential permanently.

This action cannot be undone. The credential will no longer be available
for accessing the associated registry.

Example:
  nvcf-cli registry-credential delete 12345678-1234-1234-1234-123456789012

  # Skip confirmation prompt
  nvcf-cli registry-credential delete 12345678-1234-1234-1234-123456789012 --force`,
	Args: cobra.ExactArgs(1),
	RunE: runDeleteRegistryCredential,
}

// registryListRecognizedCmd represents the list-recognized subcommand
var registryListRecognizedCmd = &cobra.Command{
	Use:   "list-recognized",
	Short: "List all recognized registries",
	Long: `List all recognized registries that NVCF can access.

This shows the registry providers and endpoints that are officially supported
and recognized by NVCF.

Example:
  nvcf-cli registry-credential list-recognized`,
	RunE: runListRecognizedRegistries,
}

func init() {
	// Add flags for list command
	registryListCmd.Flags().StringSlice("artifact-type", []string{}, "Filter by artifact type (CONTAINER, HELM, MODEL, RESOURCE)")
	registryListCmd.Flags().StringSlice("provisioned-by", []string{}, "Filter by provisioned by (SYSTEM, USER)")

	// Add flags for add command
	registryAddCmd.Flags().String("hostname", "", "Registry hostname (required)")
	registryAddCmd.Flags().String("username", "", "Registry username (use with --password)")
	registryAddCmd.Flags().String("password", "", "Registry password (use with --username)")
	registryAddCmd.Flags().String("secret", "", "Base64 encoded 'username:password' string (alternative to --username/--password)")
	registryAddCmd.Flags().StringSlice("artifact-type", []string{}, "Artifact types (CONTAINER, HELM, MODEL, RESOURCE) - required, can be specified multiple times")
	registryAddCmd.Flags().String("description", "", "Description of the credential")
	registryAddCmd.Flags().StringSlice("tag", []string{}, "Tags for the credential - can be specified multiple times")

	// Mark required flags
	registryAddCmd.MarkFlagRequired("hostname")
	registryAddCmd.MarkFlagRequired("artifact-type")

	// Add flags for update command
	registryUpdateCmd.Flags().String("username", "", "New registry username (must be provided with --password)")
	registryUpdateCmd.Flags().String("password", "", "New registry password (must be provided with --username)")
	registryUpdateCmd.Flags().StringSlice("artifact-type", []string{}, "Artifact type(s) to ADD to existing types (CONTAINER, HELM, MODEL, RESOURCE) - can be specified multiple times")

	// Add flags for delete command
	registryDeleteCmd.Flags().Bool("force", false, "Skip confirmation prompt")

	// Add subcommands to registry command
	registryCmd.AddCommand(registryListCmd)
	registryCmd.AddCommand(registryAddCmd)
	registryCmd.AddCommand(registryGetCmd)
	registryCmd.AddCommand(registryUpdateCmd)
	registryCmd.AddCommand(registryDeleteCmd)
	registryCmd.AddCommand(registryListRecognizedCmd)

	// Add registry command to root
	rootCmd.AddCommand(registryCmd)
}

func runListRegistryCredentials(cmd *cobra.Command, args []string) error {
	// Load configuration
	config, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Create NVCF client
	nvcfClient, err := client.NewClient(config)
	if err != nil {
		return fmt.Errorf("failed to create NVCF client: %w", err)
	}

	logging.Info("Listing registry credentials...")

	ctx := context.Background()

	// Parse filters
	artifactTypeStrs, _ := cmd.Flags().GetStringSlice("artifact-type")
	var artifactTypes []client.ArtifactType
	for _, str := range artifactTypeStrs {
		artifactTypes = append(artifactTypes, client.ArtifactType(strings.ToUpper(str)))
	}

	provisionedByStrs, _ := cmd.Flags().GetStringSlice("provisioned-by")
	var provisionedBy []client.ProvisionedBy
	for _, str := range provisionedByStrs {
		provisionedBy = append(provisionedBy, client.ProvisionedBy(strings.ToUpper(str)))
	}

	// List registry credentials
	resp, err := nvcfClient.ListRegistryCredentials(ctx, artifactTypes, provisionedBy)
	if err != nil {
		return fmt.Errorf("failed to list registry credentials: %w", err)
	}
	if IsJSONOutput() {
		return OutputJSON(resp)
	}

	if len(resp.RegistryCredentials) == 0 {
		logging.Info("No registry credentials found")
		return nil
	}

	// Display results
	logging.Success("Found %d registry credential(s):", len(resp.RegistryCredentials))
	logging.Plain("")

	// Display table header
	logging.Plain("%-36s %-30s %-25s %-15s %-10s", "ID", "Registry", "Hostname", "Artifact Types", "Provisioned By")
	logging.Plain("%-36s %-30s %-25s %-15s %-10s",
		"------------------------------------",
		"------------------------------",
		"-------------------------",
		"---------------",
		"----------")

	for _, cred := range resp.RegistryCredentials {
		// Truncate fields if too long
		registry := cred.RegistryName
		if len(registry) > 28 {
			registry = registry[:25] + "..."
		}

		hostname := cred.RegistryHostname
		if len(hostname) > 23 {
			hostname = hostname[:20] + "..."
		}

		// Format artifact types
		var artifactTypeStrs []string
		for _, at := range cred.ArtifactTypes {
			artifactTypeStrs = append(artifactTypeStrs, string(at))
		}
		artifactTypesStr := strings.Join(artifactTypeStrs, ",")
		if len(artifactTypesStr) > 13 {
			artifactTypesStr = artifactTypesStr[:10] + "..."
		}

		logging.Plain("%-36s %-30s %-25s %-15s %-10s",
			cred.RegistryCredentialID,
			registry,
			hostname,
			artifactTypesStr,
			string(cred.ProvisionedBy))
	}

	logging.Plain("")
	logging.Plain("Use 'nvcf-cli registry-credential get <credential-id>' for detailed information about a specific credential.")

	return nil
}

func runAddRegistryCredential(cmd *cobra.Command, args []string) error {
	// Load configuration
	config, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Create NVCF client
	nvcfClient, err := client.NewClient(config)
	if err != nil {
		return fmt.Errorf("failed to create NVCF client: %w", err)
	}

	// Get flag values
	hostname, _ := cmd.Flags().GetString("hostname")
	username, _ := cmd.Flags().GetString("username")
	password, _ := cmd.Flags().GetString("password")
	secret, _ := cmd.Flags().GetString("secret")
	artifactTypeStrs, _ := cmd.Flags().GetStringSlice("artifact-type")
	description, _ := cmd.Flags().GetString("description")
	tags, _ := cmd.Flags().GetStringSlice("tag")

	encodedCredentials, err := validateAndEncodeCredentials(secret, username, password)
	if err != nil {
		return err
	}
	if config.Debug {
		if secret != "" {
			logging.Debug("Using pre-encoded secret (length: %d chars)", len(secret))
		} else {
			logging.Debug("Encoding username:password to base64 (length: %d chars)", len(encodedCredentials))
		}
	}

	artifactTypes, err := parseAndValidateArtifactTypes(artifactTypeStrs)
	if err != nil {
		return err
	}
	if len(artifactTypes) == 0 {
		return fmt.Errorf("at least one artifact type must be specified")
	}

	// Create request
	req := &client.AddRegistryCredentialRequest{
		RegistryHostname: hostname,
		Secret: client.RegistrySecretDto{
			Name:  "credentials",
			Value: encodedCredentials,
		},
		ArtifactTypes: artifactTypes,
		Tags:          tags,
		Description:   description,
	}

	logging.Info("Adding registry credential for hostname '%s'...", hostname)

	ctx := context.Background()
	resp, err := nvcfClient.AddRegistryCredential(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to add registry credential: %w", err)
	}
	if IsJSONOutput() {
		return OutputJSON(resp)
	}

	// Display success
	cred := resp.RegistryCredential
	logging.Success("Registry credential added successfully!")
	logging.Plain("")
	logging.Plain("Credential ID: %s", cred.RegistryCredentialID)
	logging.Plain("Registry Name: %s", cred.RegistryName)
	logging.Plain("Hostname: %s", cred.RegistryHostname)
	logging.Plain("Artifact Types: %s", formatArtifactTypes(cred.ArtifactTypes))
	logging.Plain("Provisioned By: %s", string(cred.ProvisionedBy))
	logging.Plain("Created At: %s", formatTimestamp(cred.CreatedAt))

	if cred.Description != "" {
		logging.Plain("Description: %s", cred.Description)
	}

	if len(cred.Tags) > 0 {
		logging.Plain("Tags: %s", strings.Join(cred.Tags, ", "))
	}

	return nil
}

func runGetRegistryCredential(cmd *cobra.Command, args []string) error {
	credentialID := args[0]

	// Load configuration
	config, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Create NVCF client
	nvcfClient, err := client.NewClient(config)
	if err != nil {
		return fmt.Errorf("failed to create NVCF client: %w", err)
	}

	logging.Info("Getting registry credential '%s'...", credentialID)

	ctx := context.Background()
	resp, err := nvcfClient.GetRegistryCredential(ctx, credentialID)
	if err != nil {
		return fmt.Errorf("failed to get registry credential: %w", err)
	}
	if IsJSONOutput() {
		return OutputJSON(resp)
	}

	// Display detailed information
	cred := resp.RegistryCredential
	logging.Success("Registry Credential Details:")
	logging.Plain("")
	logging.Plain("ID: %s", cred.RegistryCredentialID)
	logging.Plain("Name: %s", cred.RegistryCredentialName)
	logging.Plain("Registry Name: %s", cred.RegistryName)
	logging.Plain("Hostname: %s", cred.RegistryHostname)
	logging.Plain("NCA ID: %s", cred.NcaID)
	logging.Plain("Artifact Types: %s", formatArtifactTypes(cred.ArtifactTypes))
	logging.Plain("Provisioned By: %s", string(cred.ProvisionedBy))
	logging.Plain("Created At: %s", formatTimestamp(cred.CreatedAt))
	logging.Plain("Last Updated At: %s", formatTimestamp(cred.LastUpdatedAt))

	if cred.Description != "" {
		logging.Plain("Description: %s", cred.Description)
	}

	if len(cred.Tags) > 0 {
		logging.Plain("Tags: %s", strings.Join(cred.Tags, ", "))
	}

	return nil
}

func runUpdateRegistryCredential(cmd *cobra.Command, args []string) error {
	credentialID := args[0]

	// Load configuration
	config, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Create NVCF client
	nvcfClient, err := client.NewClient(config)
	if err != nil {
		return fmt.Errorf("failed to create NVCF client: %w", err)
	}

	// Get flag values
	username, _ := cmd.Flags().GetString("username")
	password, _ := cmd.Flags().GetString("password")
	artifactTypeStrs, _ := cmd.Flags().GetStringSlice("artifact-type")

	// Build update request
	updateReq := &client.UpdateRegistryCredentialRequest{}
	hasUpdates := false

	// Update credentials if provided (both username and password must be provided together)
	if username != "" || password != "" {
		if username == "" || password == "" {
			return fmt.Errorf("both username and password must be provided together when updating credentials")
		}
		credentials := username + ":" + password
		encodedCredentials := base64.StdEncoding.EncodeToString([]byte(credentials))
		updateReq.Secret = &client.RegistrySecretDto{
			Name:  "credentials",
			Value: encodedCredentials,
		}
		hasUpdates = true
	}

	// Add artifact types if provided (additive - adds to existing types)
	if len(artifactTypeStrs) > 0 {
		artifactTypes, err := parseAndValidateArtifactTypes(artifactTypeStrs)
		if err != nil {
			return err
		}
		updateReq.ArtifactTypeEnums = artifactTypes
		hasUpdates = true
	}

	if !hasUpdates {
		return fmt.Errorf("no updates specified. Use --username/--password or --artifact-type flags")
	}

	logging.Info("Updating registry credential '%s'...", credentialID)

	ctx := context.Background()
	resp, err := nvcfClient.UpdateRegistryCredential(ctx, credentialID, updateReq)
	if err != nil {
		return fmt.Errorf("failed to update registry credential: %w", err)
	}
	if IsJSONOutput() {
		return OutputJSON(resp)
	}

	// Display success
	cred := resp.RegistryCredential
	logging.Success("Registry credential updated successfully!")
	logging.Plain("")
	logging.Plain("Credential ID: %s", cred.RegistryCredentialID)
	logging.Plain("Registry Name: %s", cred.RegistryName)
	logging.Plain("Hostname: %s", cred.RegistryHostname)
	logging.Plain("Artifact Types: %s", formatArtifactTypes(cred.ArtifactTypes))
	logging.Plain("Last Updated At: %s", formatTimestamp(cred.LastUpdatedAt))

	if cred.Description != "" {
		logging.Plain("Description: %s", cred.Description)
	}

	if len(cred.Tags) > 0 {
		logging.Plain("Tags: %s", strings.Join(cred.Tags, ", "))
	}

	return nil
}

func runDeleteRegistryCredential(cmd *cobra.Command, args []string) error {
	credentialID := args[0]

	// Load configuration
	config, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Create NVCF client
	nvcfClient, err := client.NewClient(config)
	if err != nil {
		return fmt.Errorf("failed to create NVCF client: %w", err)
	}

	// Check if force flag is set
	force, _ := cmd.Flags().GetBool("force")

	if !force {
		// Get credential details for confirmation
		ctx := context.Background()
		resp, err := nvcfClient.GetRegistryCredential(ctx, credentialID)
		if err != nil {
			return fmt.Errorf("failed to get registry credential for confirmation: %w", err)
		}

		cred := resp.RegistryCredential
		logging.Warning("You are about to delete the following registry credential:")
		logging.Plain("  ID: %s", cred.RegistryCredentialID)
		logging.Plain("  Registry: %s", cred.RegistryName)
		logging.Plain("  Hostname: %s", cred.RegistryHostname)
		logging.Plain("")
		logging.Warning("This action cannot be undone!")
		logging.Plain("")
		logging.Plain("Type 'yes' to confirm deletion: ")

		var confirmation string
		if _, err := fmt.Scanln(&confirmation); err != nil {
			return fmt.Errorf("failed to read confirmation: %w", err)
		}

		if strings.ToLower(strings.TrimSpace(confirmation)) != "yes" {
			logging.Info("Delete cancelled.")
			return nil
		}
	}

	logging.Info("Deleting registry credential '%s'...", credentialID)

	ctx := context.Background()
	if err := nvcfClient.DeleteRegistryCredential(ctx, credentialID); err != nil {
		return fmt.Errorf("failed to delete registry credential: %w", err)
	}
	if IsJSONOutput() {
		return OutputJSON(map[string]interface{}{
			"status":               "deleted",
			"registryCredentialId": credentialID,
		})
	}

	logging.Success("Registry credential deleted successfully!")

	return nil
}

func runListRecognizedRegistries(cmd *cobra.Command, args []string) error {
	// Load configuration
	config, err := client.LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Create NVCF client
	nvcfClient, err := client.NewClient(config)
	if err != nil {
		return fmt.Errorf("failed to create NVCF client: %w", err)
	}

	logging.Info("Listing recognized registries...")

	ctx := context.Background()
	resp, err := nvcfClient.ListRecognizedRegistries(ctx)
	if err != nil {
		return fmt.Errorf("failed to list recognized registries: %w", err)
	}
	if IsJSONOutput() {
		return OutputJSON(resp)
	}

	if len(resp.RecognizedRegistries) == 0 {
		logging.Info("No recognized registries found")
		return nil
	}

	// Display results
	logging.Success("Recognized Registries:")
	logging.Plain("")

	for registryName, endpoints := range resp.RecognizedRegistries {
		logging.Plain("Registry: %s", registryName)

		for i, endpoint := range endpoints {
			logging.Plain("  Endpoint %d:", i+1)
			for key, value := range endpoint {
				logging.Plain("    %s: %s", key, value)
			}
		}
		logging.Plain("")
	}

	return nil
}

// Helper functions

// validateAndEncodeCredentials validates the mutually-exclusive auth flag combinations
// for the add command and returns the base64-encoded `username:password` value to send
// to the NVCF API. Callers must choose exactly one authentication method:
//
//   - --secret with a pre-encoded base64 string, OR
//   - both --username and --password (this function encodes them).
func validateAndEncodeCredentials(secret, username, password string) (string, error) {
	if secret != "" {
		if username != "" || password != "" {
			return "", fmt.Errorf("cannot use --secret with --username/--password. Choose one authentication method")
		}
		return secret, nil
	}
	if username == "" || password == "" {
		return "", fmt.Errorf("must provide either --secret OR both --username and --password")
	}
	credentials := username + ":" + password
	return base64.StdEncoding.EncodeToString([]byte(credentials)), nil
}

// parseAndValidateArtifactTypes converts user-supplied artifact type strings into
// typed values. Comparison is case-insensitive; unknown values produce an error
// listing the valid set.
func parseAndValidateArtifactTypes(strs []string) ([]client.ArtifactType, error) {
	validTypes := map[string]bool{
		"CONTAINER": true,
		"HELM":      true,
		"MODEL":     true,
		"RESOURCE":  true,
	}
	var artifactTypes []client.ArtifactType
	for _, s := range strs {
		upperStr := strings.ToUpper(s)
		if !validTypes[upperStr] {
			return nil, fmt.Errorf("invalid artifact type '%s'. Valid types: CONTAINER, HELM, MODEL, RESOURCE", s)
		}
		artifactTypes = append(artifactTypes, client.ArtifactType(upperStr))
	}
	return artifactTypes, nil
}

func formatArtifactTypes(types []client.ArtifactType) string {
	var strs []string
	for _, t := range types {
		strs = append(strs, string(t))
	}
	return strings.Join(strs, ", ")
}

func formatTimestamp(timestamp string) string {
	// Try to parse and format the timestamp for better readability
	if t, err := time.Parse(time.RFC3339, timestamp); err == nil {
		return t.Format("2006-01-02 15:04:05 MST")
	}
	// If parsing fails, return the original timestamp
	return timestamp
}
