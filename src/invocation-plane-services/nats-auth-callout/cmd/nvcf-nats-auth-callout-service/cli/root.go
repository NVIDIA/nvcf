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
	"github.com/spf13/cobra"
	"github.com/NVIDIA/nvcf/src/invocation-plan-services/nats-auth-callout/internal/config"
)

// SetEmbeddedConfig sets the embedded default configuration
func SetEmbeddedConfig(configData []byte) {
	config.SetEmbeddedDefaults(configData)
}

// Execute runs the root command
func Execute() error {
	rootCmd := NewRootCommand()
	return rootCmd.Execute()
}

func NewRootCommand() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:          "nvcf-nats-auth-callout-service",
		Short:        "http server",
		Long:         `provide ping and healthz http server`,
		SilenceUsage: true,
	}

	rootCmd.AddCommand(makeServerCmd())
	rootCmd.AddCommand(makeConfigCmd())
	rootCmd.AddCommand(makeVersionCmd())

	return rootCmd
}
