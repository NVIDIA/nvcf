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

package startup

import (
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/cmd/toolbox"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/constants"
)

const configEnvPrefix = "EVENT_LEDGER"

func configFilesFromEnv() []string {
	envConfigs := os.Getenv(configEnvPrefix + "_CONFIG")
	if envConfigs == "" {
		return nil
	}

	parts := strings.Split(envConfigs, ",")
	cfgFiles := make([]string, 0, len(parts))
	for _, part := range parts {
		if file := strings.TrimSpace(part); file != "" {
			cfgFiles = append(cfgFiles, file)
		}
	}

	return cfgFiles
}

func BuildRootCmd(logger *otelzap.Logger) *cobra.Command {
	var cfgFiles []string
	sugarLog := logger.Sugar()

	v := viper.New()
	rootCmd := &cobra.Command{
		Use:           "event-ledger-api",
		Short:         "Event Ledger API service",
		Long:          `Commands for the Event Ledger API service.`,
		SilenceUsage:  true, // Don't print usage on error
		SilenceErrors: true, // Don't print errors from Cobra directly
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			v.AutomaticEnv()
			v.SetEnvPrefix(configEnvPrefix)
			v.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))

			// Process config files set by environment variable
			cfgFiles = append(cfgFiles, configFilesFromEnv()...)

			for _, cfgFile := range cfgFiles {
				if cfgFile == "" {
					continue
				}

				v.SetConfigFile(cfgFile)
				if err := v.MergeInConfig(); err != nil {
					if _, ok := err.(viper.ConfigFileNotFoundError); ok {
						sugarLog.Warn("config file not found at ", cfgFile, ", using defaults")
						continue
					} else {
						sugarLog.Error(err)
						os.Exit(1)
					}
				} else {
					sugarLog.Warn("loaded config file: ", cfgFile)
				}
			}

			if v.ConfigFileUsed() == "" {
				sugarLog.Warn("no config files found, using defaults")
			}

			if len(cfgFiles) > 0 {
				sugarLog.Warn("config files attempted (in order): ")
				for i, file := range cfgFiles {
					sugarLog.Warnf("%d: %s", i, file)
				}
			}

			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			var cfg config.Config
			if err := v.Unmarshal(&cfg); err != nil {
				sugarLog.Error("failed to unmarshal config: ", err)
				return err
			}

			if err := applyRuntimeFlagOverrides(cmd, &cfg); err != nil {
				return err
			}

			return runService(cfg)
		},
	}

	// Specify config file(s)
	rootCmd.Flags().StringSliceVar(&cfgFiles, "config", []string{}, "Path to config file")
	err := v.BindPFlag("config", rootCmd.Flags().Lookup("config"))
	if err != nil {
		sugarLog.Fatal(err)
	}

	// Register CLI arguments and viper bindings
	cliArgs := config.NewCliArgs(rootCmd, v, sugarLog)
	cliArgs.Register(constants.ApiSvcName)

	rootCmd.AddCommand(toolbox.NewGenerateConfigCmd(sugarLog, constants.ApiSvcName))
	return rootCmd
}

func applyRuntimeFlagOverrides(cmd *cobra.Command, cfg *config.Config) error {
	if cmd.Flags().Changed("disable-authentication") {
		disableAuth, err := cmd.Flags().GetBool("disable-authentication")
		if err != nil {
			return err
		}
		cfg.Auth.Enabled = !disableAuth // Set the inverse so all the checks everywhere else are less confusing
	}

	if cmd.Flags().Changed("enable-profiling") {
		enableProfiling, err := cmd.Flags().GetBool("enable-profiling")
		if err != nil {
			return err
		}
		cfg.Profiling.Enabled = enableProfiling
	}

	if cmd.Flags().Changed("disable-cloudevent") {
		disableCloudEvent, err := cmd.Flags().GetBool("disable-cloudevent")
		if err != nil {
			return err
		}
		cfg.Publisher.Cloudevents.Enabled = !disableCloudEvent
	}

	return nil
}
