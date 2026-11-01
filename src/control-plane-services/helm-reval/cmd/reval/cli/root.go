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
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"

	autobind "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/cobraautobind"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/authorizers"
	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/reval/config"
)

const (
	defaultConfigEnvPrefix = "REVAL"
	configFileFlagName     = "config"
)

// AuthorizerFactory builds the authorizer chain after config is parsed.
// v exposes the raw Viper instance for unmarshalling extra keys.
// Return a nil slice to disable authorization middleware.
type AuthorizerFactory func(
	ctx context.Context,
	v *viper.Viper,
	cfg *config.RevalConfig,
	logger *zap.Logger,
) ([]authorizers.Authorizer, error)

// Options configures the CLI entry-point.
// A nil AuthorizerFactory falls back to authorizers.BuildChain.
type Options struct {
	AuthorizerFactory AuthorizerFactory
}

// NewRootCommand sets up the main settings for the reval service.
// Configures command line flags (cobra) and configuration files (viper).
func NewRootCommand(logger *zap.Logger, version, gitCommit string, opts Options) *cobra.Command {
	var cfgFiles []string

	v := viper.New()
	v.AutomaticEnv()
	v.SetEnvPrefix(defaultConfigEnvPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))

	rootCmd := &cobra.Command{
		Use:           "reval",
		Version:       fmt.Sprintf("%s (%s)", version, gitCommit),
		Short:         "Reval service",
		Long:          `Commands for Reval service.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Don't worry about no config file being set, ParseConfigFiles will handle the empty string as config file
			cfgFiles = append(cfgFiles, v.GetString(configFileFlagName))

			return config.ParseConfigFiles(cfgFiles, v, logger)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			var cfg config.RevalConfig
			if err := v.Unmarshal(&cfg); err != nil {
				return err
			}
			cfg.Telemetry.ServiceVersion = version
			cfg.Telemetry.GitCommit = gitCommit
			return runServer(&cfg, v, opts.AuthorizerFactory)
		},
	}
	// Load config files from the command line
	rootCmd.Flags().StringSliceVar(&cfgFiles, configFileFlagName, []string{}, "Path to config file")
	err := v.BindPFlag(configFileFlagName, rootCmd.Flags().Lookup(configFileFlagName))
	if err != nil {
		logger.Panic("failed to bind config file flag", zap.Error(err))
	}

	err = autobind.AutobindFlagsFromStruct(rootCmd, v, &config.RevalConfig{})
	if err != nil {
		logger.Panic("failed to autobind flags", zap.Error(err))
	}

	return rootCmd
}
