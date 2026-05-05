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

package config

import (
	"fmt"

	"github.com/spf13/cobra"
)

type exampleConfig struct {
	defaultVar string
	flagVar    string
	nested     nestedConfig
}
type nestedConfig struct {
	nVar       string
	deepNested deepNestedConfig
}
type deepNestedConfig struct {
	dnVar string
}

type exampleComplexConfig struct {
	Cfg *ComplexConfigInfo `mapstructure:"complexVar"`
}
type ComplexConfig struct {
	Name  string `mapstructure:"name"`
	Prop1 string `mapstructure:"prop1"`
	Prop2 string `mapstructure:"prop2"`
}
type ComplexConfigInfo struct {
	Configs []ComplexConfig `mapstructure:"config"`
}

var complexVarTest = exampleComplexConfig{
	Cfg: &ComplexConfigInfo{},
}

func NewTestCommand() *cobra.Command {
	// Store the result of binding cobra flags and viper config. In a
	// real application these would be data structures, most likely
	// custom structs per command. This is simplified for the demo app and is
	// not recommended that you use one-off variables. The point is that we
	// aren't retrieving the values directly from viper or flags, we read the values
	// from standard Go data structures.
	cfg := exampleConfig{}
	var cfgFile string
	// Define our command
	rootCmd := &cobra.Command{
		Use:          "test",
		Short:        "Test configuration using cobra and viper",
		Long:         `Demonstrate how to get cobra flags to bind to viper properly`,
		SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// You can bind cobra and viper in a few locations, but PersistencePreRunE on the root command works well
			_, err := InitConfig(cmd, cfgFile, "", "TEST", &complexVarTest)
			return err
		},
		Run: func(cmd *cobra.Command, args []string) {
			// Working with OutOrStdout/OutOrStderr allows us to unit test our command easier
			out := cmd.OutOrStdout()

			// Print the final resolved value from binding cobra flags and viper config
			fmt.Fprintln(out, "cfg.defaultVar:", cfg.defaultVar)
			fmt.Fprintln(out, "cfg.flagVar:", cfg.flagVar)
			fmt.Fprintln(out, "cfg.nested.nVar:", cfg.nested.nVar)
			fmt.Fprintln(out, "cfg.nested.deepNested.dnVar:", cfg.nested.deepNested.dnVar)
			fmt.Fprintln(out, "complexVar:", complexVarTest.Cfg)
		},
	}

	// Define cobra flags, the default value has the lowest (least significant) precedence
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "./example-config.yaml", "config file (default is $HOME/.uamtest/config.yaml)")
	rootCmd.Flags().StringVarP(&cfg.defaultVar, "default-var", "", "default-var", "Should come from default value")
	rootCmd.Flags().StringVarP(&cfg.flagVar, "flag-var", "", "flag-default-var", "Should come from flag value")
	rootCmd.Flags().StringVarP(&cfg.nested.nVar, "nested.var", "", "nested-default-var",
		"Should come from flag first, then env var TEST_NESTEDVAR_VAR1 then the config file, then the default last")
	rootCmd.Flags().StringVarP(&cfg.nested.deepNested.dnVar, "nested.deepnested.var", "", "deep-nested-default-var",
		"Should come from flag first, then env var TEST_NESTEDVAR_DEEPNESTEDVAR_VAR1 then the config file, then the default last")

	return rootCmd
}
