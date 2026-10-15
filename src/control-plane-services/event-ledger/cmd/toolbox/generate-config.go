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

package toolbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mitchellh/mapstructure"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"gopkg.in/yaml.v2"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/configutil"
)

func NewGenerateConfigCmd(sugarLog *otelzap.SugaredLogger, svcName string) *cobra.Command {
	v := viper.New()

	generateConfigCmd := &cobra.Command{
		Use:   "generate-config",
		Short: "Generate a default configuration file",
		Long: `Generate a default configuration file with all the available configuration options.

Specify --format {json|yaml} to generate the configuration in the desired format. (Default: yaml)

By default it will not overwrite an existing configuration file. Use --overwrite to force.

The generated configuration file will be named "config.{format}" in the current directory. Use --output to specify a different file name and path.

Examples:

	# Generate a JSON configuration file
	event-ledger-api generate-config --format json

	# Generate a YAML configuration file
	event-ledger-api generate-config --format yaml

	# Generate a configuration file to another path
	event-ledger-api generate-config --output /new/path/to/config.yml
		`,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dummyRoot := &cobra.Command{Use: "temp"}
			cliArgs := config.NewCliArgs(dummyRoot, v, sugarLog)
			cliArgs.Register(svcName)

			var cfg config.Config
			if err := v.Unmarshal(&cfg, viper.DecodeHook(mapstructure.StringToTimeDurationHookFunc())); err != nil {
				sugarLog.Errorw("failed to unmarshal config using mapstructure", "error", err)
				return err
			}

			configMap, err := configutil.StructToMap(cfg)
			if err != nil {
				sugarLog.Errorw("failed to convert config struct to map", "error", err)
				return err
			}

			var outputBytes []byte
			overwrite, _ := cmd.Flags().GetBool("overwrite")
			outputFormat, _ := cmd.Flags().GetString("format")
			outputFile, _ := cmd.Flags().GetString("output")

			// Infer output format if we specify it in the output name without specifying --format
			// If unknown or even outputFile is not specified, we default to yaml
			if outputFormat == "" {
				ext := strings.ToLower(filepath.Ext(outputFile))
				switch ext {
				case ".json":
					outputFormat = "json"
				case ".yaml", ".yml":
					outputFormat = "yaml"
				default:
					outputFormat = "yaml"
				}
			}

			// Set the default output file name
			if outputFile == "" {
				outputFile = "config." + outputFormat
			}

			// Create the path we want to write to if it doesn't exist
			configDir := filepath.Dir(outputFile)
			if err := os.MkdirAll(configDir, 0755); err != nil {
				sugarLog.Warnw("failed to create default config directory", "directory", configDir, "error", err)
			}

			switch strings.ToLower(outputFormat) {
			case "yaml":
				outputBytes, err = yaml.Marshal(configMap)
				if err != nil {
					sugarLog.Errorw("failed to marshal default config to YAML", "error", err)
					return err
				}
			case "json":
				outputBytes, err = json.MarshalIndent(configMap, "", "  ")
				if err != nil {
					sugarLog.Errorw("failed to marshal default config to JSON", "error", err)
					return err
				}
			default:
				sugarLog.Errorw("unsupported output format", "format", outputFormat)
				return fmt.Errorf("unsupported output format: %s", outputFormat)
			}

			if _, err := os.Stat(outputFile); err == nil {
				if !overwrite {
					return fmt.Errorf("file %s already exists. Use --overwrite to force", outputFile)
				}
			}
			if err := os.WriteFile(outputFile, outputBytes, 0644); err != nil {
				sugarLog.Errorw("failed to write default config to file", "file", outputFile, "error", err)
				return err
			}

			sugarLog.Infow("default configuration file generated", "file", outputFile, "format", outputFormat)
			return nil
		},
	}

	generateConfigCmd.Flags().StringP("format", "f", "", "Output format (options: json, yaml) (default \"yaml\")")
	generateConfigCmd.Flags().StringP("output", "o", "", "File path and name for generated config (default \"./config.yaml\")")
	generateConfigCmd.Flags().Bool("overwrite", false, "Overwrite existing config file")

	return generateConfigCmd
}
