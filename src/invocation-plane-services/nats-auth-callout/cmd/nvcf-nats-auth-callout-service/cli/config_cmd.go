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
	"fmt"

	"github.com/spf13/cobra"
	"github.com/NVIDIA/nvcf/src/invocation-plan-services/nats-auth-callout/internal/config"
)

func makeConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Configuration management and debugging",
		Long: `View and debug configuration settings.

This command helps you understand how configuration is loaded and what values
are currently being used by the application.

Configuration precedence (highest to lowest):
  1. Command line flags
  2. Environment variables (NVCF_NATS_AUTH_CALLOUT_SERVICE prefix)
  3. Configuration file (cmd/nvcf-nats-auth-callout-service/config.yaml)
  4. Default values

Environment variables:
  NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVER_PORT              - Server port
  NVCF_NATS_AUTH_CALLOUT_SERVICE_SERVICE_NAME             - Service name

Examples:
  nvcf-nats-auth-callout-service config show             - Show current configuration
  nvcf-nats-auth-callout-service config debug           - Show debug information
  nvcf-nats-auth-callout-service config validate        - Validate configuration
`,
	}

	cmd.AddCommand(makeConfigShowCmd())
	cmd.AddCommand(makeConfigDebugCmd())
	cmd.AddCommand(makeConfigValidateCmd())

	return cmd
}

func makeConfigShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show current configuration values",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := config.InitConfig("nvcf-nats-auth-callout-service")
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
		},
	}
}

func makeConfigDebugCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "debug",
		Short: "Show detailed configuration debug information",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := config.InitConfig("nvcf-nats-auth-callout-service")
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
		},
	}
}

func makeConfigValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate current configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.InitConfig("nvcf-nats-auth-callout-service")
			if err != nil {
				return fmt.Errorf("failed to load configuration: %w", err)
			}

			if err := config.ValidateConfig(cfg); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Configuration validation failed: %v\n", err)
				return err
			}

			fields, err := config.GetConfigFields()
			if err != nil {
				return fmt.Errorf("failed to get config fields: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Configuration validation passed ✓\n")
			fmt.Fprint(cmd.OutOrStdout(), config.DisplayConfigFields(fields))

			return nil
		},
	}
}
