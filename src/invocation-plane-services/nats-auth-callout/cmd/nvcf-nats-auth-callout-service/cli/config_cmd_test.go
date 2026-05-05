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

package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nvcf/src/invocation-plan-services/nats-auth-callout/internal/config"

	"github.com/spf13/cobra"
)

// setupTestConfig creates a temporary directory with a test config file
func setupTestConfig(t *testing.T) (string, func()) {
	// Create temporary directory
	tmpDir, err := os.MkdirTemp("", "config_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	// Create test config file
	configContent := `server:
  port: "8080"
service:
  name: "nvcf-nats-auth-callout-service"
  nats_url: "nats://localhost:4222"
  nkey_seed: "SUAOTESTSEEDKEYFORAUTHENTICATION"
  nkey_signature: "SAATESTSIGNATUREKEYFORJWTSIGNING"
`
	configPath := filepath.Join(tmpDir, "config.yaml")
	err = os.WriteFile(configPath, []byte(configContent), 0644)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to write test config: %v", err)
	}

	// Change to temp directory for test
	originalDir, err := os.Getwd()
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to get working directory: %v", err)
	}

	err = os.Chdir(tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to change to temp directory: %v", err)
	}

	// Reset config state
	config.ResetConfig()

	// Set up embedded config for tests
	SetEmbeddedConfig([]byte(configContent))

	// Return cleanup function
	cleanup := func() {
		os.Chdir(originalDir)
		os.RemoveAll(tmpDir)
		config.ResetConfig()
	}

	return tmpDir, cleanup
}

func TestConfigShowCmd(t *testing.T) {
	tmpDir, cleanup := setupTestConfig(t)
	defer cleanup()

	// Create a buffer to capture output
	var buf bytes.Buffer

	// Create the command and set it to use the temp config file
	cmd := makeConfigShowCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	// Override the command to pass the config file path
	configPath := filepath.Join(tmpDir, "config.yaml")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		_, err := config.InitConfig("nvcf-nats-auth-callout-service", configPath)
		if err != nil {
			return fmt.Errorf("failed to load configuration: %w", err)
		}

		fields, err := config.GetConfigFields()
		if err != nil {
			return fmt.Errorf("failed to get config fields: %w", err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Current Configuration:\n")
		fmt.Fprintf(cmd.OutOrStdout(), "=====================\n")
		fmt.Fprint(cmd.OutOrStdout(), config.DisplayConfigFields(fields))

		return nil
	}

	// Execute the command
	err := cmd.Execute()
	if err != nil {
		t.Fatalf("Config show command failed: %v", err)
	}

	output := buf.String()

	// Check that expected content is in output
	if !strings.Contains(output, "Current Configuration") {
		t.Errorf("Expected 'Current Configuration' in output, got: '%s'", output)
	}
	if !strings.Contains(output, "Server Port") {
		t.Errorf("Expected 'Server Port' in output, got: '%s'", output)
	}
	if !strings.Contains(output, "Service Name") {
		t.Errorf("Expected 'Service Name' in output, got: '%s'", output)
	}
	// Check that we have actual values
	if !strings.Contains(output, "8080") {
		t.Errorf("Expected port value '8080' in output, got: '%s'", output)
	}
	if !strings.Contains(output, "nvcf-nats-auth-callout-service") {
		t.Errorf("Expected service name 'nvcf-nats-auth-callout-service' in output, got: '%s'", output)
	}
}

func TestConfigShowCmdWithEnvironmentVariables(t *testing.T) {
	_, cleanup := setupTestConfig(t)
	defer cleanup()

	// Set environment variables
	os.Setenv("NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT", "9999")
	os.Setenv("NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NAME", "test-env-service")
	defer os.Unsetenv("NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT")
	defer os.Unsetenv("NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NAME")

	// Reset config to pick up env vars, then re-setup embedded config
	config.ResetConfig()
	testConfigContent := `server:
  port: "8080"
service:
  name: "nvcf-nats-auth-callout-service"
  nats_url: "nats://localhost:4222"
  nkey_seed: "SUAOTESTSEEDKEYFORAUTHENTICATION"
  nkey_signature: "SAATESTSIGNATUREKEYFORJWTSIGNING"
`
	SetEmbeddedConfig([]byte(testConfigContent))

	// Create a buffer to capture output
	var buf bytes.Buffer

	// Create the command
	cmd := makeConfigShowCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	// Execute the command
	err := cmd.Execute()
	if err != nil {
		t.Fatalf("Config show command failed: %v", err)
	}

	output := buf.String()

	// Check that environment variables are reflected in the output
	if !strings.Contains(output, "9999") {
		t.Errorf("Expected port '9999' from env var in output, got: %s", output)
	}
	if !strings.Contains(output, "test-env-service") {
		t.Errorf("Expected service name 'test-env-service' from env var in output, got: %s", output)
	}
}

func TestConfigDebugCmd(t *testing.T) {
	tmpDir, cleanup := setupTestConfig(t)
	defer cleanup()

	// Create a buffer to capture output
	var buf bytes.Buffer

	// Create the command and set it to use the temp config file
	cmd := makeConfigDebugCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	// Override the command to pass the config file path
	configPath := filepath.Join(tmpDir, "config.yaml")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		_, err := config.InitConfig("nvcf-nats-auth-callout-service", configPath)
		if err != nil {
			return fmt.Errorf("failed to load configuration: %w", err)
		}

		debug := config.Debug()
		fields, err := config.GetConfigFields()
		if err != nil {
			return fmt.Errorf("failed to get config fields: %w", err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Configuration Debug Information:\n")
		fmt.Fprintf(cmd.OutOrStdout(), "===============================\n")
		fmt.Fprintf(cmd.OutOrStdout(), "Environment Prefix:  %s\n", debug["env_prefix"])

		if paths, ok := debug["config_paths"].([]string); ok {
			fmt.Fprintf(cmd.OutOrStdout(), "Config Search Paths:\n")
			for _, path := range paths {
				fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", path)
			}
		}

		fmt.Fprintf(cmd.OutOrStdout(), "\nAll Settings:\n")
		if settings, ok := debug["all_settings"].(map[string]any); ok {
			for key, value := range settings {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s: %v\n", key, value)
			}
		}

		fmt.Fprintf(cmd.OutOrStdout(), "\nCurrent Configuration:\n")
		for _, field := range fields {
			fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", field.Name+":", field.Value)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "\nEnvironment Variables (if set):\n")
		// Show environment variables that are actually set
		for _, field := range fields {
			// Convert field source info to potential env var name
			if field.Source == "environment" {
				fmt.Fprintf(cmd.OutOrStdout(), "  Environment variables are currently active\n")
				break
			}
		}

		return nil
	}

	// Execute the command
	err := cmd.Execute()
	if err != nil {
		t.Fatalf("Config debug command failed: %v", err)
	}

	output := buf.String()

	// Check that expected debug content is in output
	expectedStrings := []string{
		"Configuration Debug Information",
		"Environment Prefix",
		"All Settings",
		"Current Configuration",
		"Environment Variables",
		"NVCF_NATS_AUTH_CALLOUT_SERVICE",
	}

	for _, expected := range expectedStrings {
		if !strings.Contains(output, expected) {
			t.Errorf("Expected '%s' in debug output, got: %s", expected, output)
		}
	}

	// Check for actual configuration values
	if !strings.Contains(output, "8080") {
		t.Errorf("Expected port value in debug output, got: %s", output)
	}
	if !strings.Contains(output, "nvcf-nats-auth-callout-service") {
		t.Errorf("Expected service name in debug output, got: %s", output)
	}
}

func TestConfigValidateCmd(t *testing.T) {
	_, cleanup := setupTestConfig(t)
	defer cleanup()

	// Create a buffer to capture output
	var buf bytes.Buffer

	// Create the command
	cmd := makeConfigValidateCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	// Execute the command
	err := cmd.Execute()
	if err != nil {
		t.Fatalf("Config validate command failed: %v", err)
	}

	output := buf.String()

	// Check that validation passed
	if !strings.Contains(output, "Configuration validation passed") {
		t.Errorf("Expected 'Configuration validation passed' in output, got: %s", output)
	}
	if !strings.Contains(output, "Server Port") {
		t.Errorf("Expected 'Server Port' in validation output, got: %s", output)
	}
	if !strings.Contains(output, "Service Name") {
		t.Errorf("Expected 'Service Name' in validation output, got: %s", output)
	}

	// Check for actual values
	if !strings.Contains(output, "8080") {
		t.Errorf("Expected port value in validation output, got: %s", output)
	}
	if !strings.Contains(output, "nvcf-nats-auth-callout-service") {
		t.Errorf("Expected service name in validation output, got: %s", output)
	}
}

func TestConfigCmd(t *testing.T) {
	// Test the main config command
	cmd := makeConfigCmd()

	// Check that subcommands are added
	subCommands := cmd.Commands()
	if len(subCommands) != 3 {
		t.Errorf("Expected 3 subcommands, got %d", len(subCommands))
	}

	// Check subcommand names
	expectedSubcommands := map[string]bool{
		"show":     false,
		"debug":    false,
		"validate": false,
	}

	for _, subCmd := range subCommands {
		if _, exists := expectedSubcommands[subCmd.Name()]; exists {
			expectedSubcommands[subCmd.Name()] = true
		}
	}

	for cmdName, found := range expectedSubcommands {
		if !found {
			t.Errorf("Expected subcommand '%s' not found", cmdName)
		}
	}
}

func TestConfigCmdHelp(t *testing.T) {
	// Create a buffer to capture output
	var buf bytes.Buffer

	// Create the command
	cmd := makeConfigCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--help"})

	// Execute the command
	err := cmd.Execute()
	if err != nil {
		t.Fatalf("Config help command failed: %v", err)
	}

	output := buf.String()

	// Check that help content includes expected information
	expectedHelpContent := []string{
		"Configuration",                  // Part of "Configuration management and debugging"
		"precedence",                     // Should be in the help text
		"NVCF_NATS_AUTH_CALLOUT_SERVICE", // Environment prefix mentioned
		"show",                           // Subcommand
		"debug",                          // Subcommand
		"validate",                       // Subcommand
	}

	for _, expected := range expectedHelpContent {
		if !strings.Contains(output, expected) {
			t.Errorf("Expected '%s' in help output, got: %s", expected, output)
		}
	}
}

func TestConfigShowCmdIntegration(t *testing.T) {
	_, cleanup := setupTestConfig(t)
	defer cleanup()

	// Create and execute command
	var buf bytes.Buffer
	cmd := makeConfigShowCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("Config show integration test failed: %v", err)
	}

	output := buf.String()

	// Should contain actual configuration values (check for both field names and values)
	if !strings.Contains(output, "Server Port") || !strings.Contains(output, "8080") {
		t.Errorf("Expected Server Port configuration in output, got: %s", output)
	}
	if !strings.Contains(output, "Service Name") || !strings.Contains(output, "nvcf-nats-auth-callout-service") {
		t.Errorf("Expected Service Name configuration in output, got: %s", output)
	}
}

func TestConfigValidateCmdWithValidConfig(t *testing.T) {
	_, cleanup := setupTestConfig(t)
	defer cleanup()

	var buf bytes.Buffer
	cmd := makeConfigValidateCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.Execute()
	// With current valid config, this should not error
	if err != nil {
		t.Fatalf("Config validate should pass with current config: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "passed") && !strings.Contains(output, "✓") {
		t.Errorf("Expected validation success indicator in output, got: %s", output)
	}
}

func TestConfigShowCmdWithCustomConfig(t *testing.T) {
	// Test with a custom config file content
	tmpDir, err := os.MkdirTemp("", "config_test_custom_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create custom config
	customConfigContent := `server:
  port: "3000"
service:
  name: "custom-service"
  nats_url: "nats://localhost:4222"
  nkey_seed: "SUAOTESTSEEDKEYFORAUTHENTICATION"
  nkey_signature: "SAATESTSIGNATUREKEYFORJWTSIGNING"
`
	configPath := filepath.Join(tmpDir, "config.yaml")
	err = os.WriteFile(configPath, []byte(customConfigContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write custom config: %v", err)
	}

	// Change to temp directory
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	defer os.Chdir(originalDir)

	err = os.Chdir(tmpDir)
	if err != nil {
		t.Fatalf("Failed to change to temp directory: %v", err)
	}

	// Reset config
	config.ResetConfig()
	defer config.ResetConfig()

	// Set up embedded config for this test
	SetEmbeddedConfig([]byte(customConfigContent))

	// Create and execute command
	var buf bytes.Buffer
	cmd := makeConfigShowCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err = cmd.Execute()
	if err != nil {
		t.Fatalf("Config show command failed: %v", err)
	}

	output := buf.String()

	// Check that custom values are shown
	if !strings.Contains(output, "3000") {
		t.Errorf("Expected custom port '3000' in output, got: %s", output)
	}
	if !strings.Contains(output, "custom-service") {
		t.Errorf("Expected custom service name 'custom-service' in output, got: %s", output)
	}
}

func TestConfigValidateCmdWithInvalidConfig(t *testing.T) {
	// Test with invalid config (empty port)
	tmpDir, err := os.MkdirTemp("", "config_test_invalid_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create invalid config with empty port
	invalidConfigContent := `server:
  port: ""
service:
  name: "test-service"
`
	configPath := filepath.Join(tmpDir, "config.yaml")
	err = os.WriteFile(configPath, []byte(invalidConfigContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write invalid config: %v", err)
	}

	// Change to temp directory
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	defer os.Chdir(originalDir)

	err = os.Chdir(tmpDir)
	if err != nil {
		t.Fatalf("Failed to change to temp directory: %v", err)
	}

	// Reset config
	config.ResetConfig()
	defer config.ResetConfig()

	// Set up embedded config for this test (even though it's invalid, we need embedded config)
	SetEmbeddedConfig([]byte(invalidConfigContent))

	var buf bytes.Buffer
	cmd := makeConfigValidateCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err = cmd.Execute()
	// This should error because the config is invalid
	if err == nil {
		t.Errorf("Expected config validate to fail with invalid config, but it passed")
	}

	output := buf.String()
	// Should contain validation failure message
	if !strings.Contains(output, "validation failed") && !strings.Contains(output, "failed") {
		t.Errorf("Expected validation failure message in output, got: '%s'", output)
	}
}

// Helper function to reset configuration state for testing
func resetConfig() {
	// Reset the config package state for testing
	config.ResetConfig()
}
