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
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/mitchellh/go-homedir"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

const (
	DefaultConfigPath = ".nvkit"
)

// InitConfig sets up the basic way to read CLI/File configs for services
// Configs are read in the following priority (left overrides right):
//
//	CLI-Flags -> Config-file -> Env-vars -> Flag-defaults
//
// NOTE:
//
//	Sometimes, it is not easy to represent all configs as flags and are best represented in config files - like array of structs.
//	To satisfy these cases we allow passing extraConfigs which will be unmarshalled from config file
func InitConfig(cmd *cobra.Command, cfgFile string, cfgPath string, envPrefix string, extraConfigs ...interface{}) (*viper.Viper, error) {
	v := viper.New()

	if cfgFile != "" {
		// Use config file from the flag.
		v.SetConfigFile(cfgFile)
	} else {
		// Find home directory.
		home, err := homedir.Dir()
		if err != nil {
			return nil, err
		}

		if cfgPath == "" {
			cfgPath = DefaultConfigPath
		}

		// Search config in home directory.
		v.AddConfigPath(fmt.Sprintf("%s/%s", home, cfgPath))
		// Search config in local directory
		v.AddConfigPath(cfgPath)
		v.SetConfigName("config")
		v.SetConfigType("yaml")
	}

	// Attempt to read the config file, gracefully ignoring errors
	// caused by a config file not being found. Return an error
	// if we cannot parse the config file.
	if err := v.ReadInConfig(); err != nil {
		// It's okay if there isn't a config file
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, err
		}
	}

	// Unmarshall any extra-configs
	for _, extraCfg := range extraConfigs {
		err := v.Unmarshal(extraCfg)
		if err != nil {
			return nil, fmt.Errorf("unmarshall to %+v failed: %+v", reflect.TypeOf(extraCfg), err)
		}
	}

	// Bind to environment variables
	v.AutomaticEnv()
	// Set prefix for env variables
	// e.g.: setting prefix of EXAMPLE will expose --flag as EXAMPLE_FLAG
	v.SetEnvPrefix(envPrefix)
	// Replace any flags with hyphens and dots with underscores
	// e.g.: --flag.dot.var -> FLAG_DOT_VAR, --flag-hypen-var -> FLAG_HYPHEN_VAR
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))

	// Bind the current command's flags to viper
	if err := bindFlags(cmd, v); err != nil {
		return nil, err
	}

	return v, nil
}

// Bind each cobra flag to its associated viper configuration (config file and environment variable)
func bindFlags(cmd *cobra.Command, v *viper.Viper) error {
	var errs []error
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		// Apply the viper config value to the flag when the flag is not set and viper has a value
		if !f.Changed && v.IsSet(f.Name) {
			val := v.Get(f.Name)
			if err := cmd.Flags().Set(f.Name, fmt.Sprintf("%v", val)); err != nil {
				errs = append(errs, err)
			}
		}
	})
	return errors.Join(errs...)
}

// SetupConfig is a helper function for config flags that exits if the config was not applied correctly
func SetupConfig(configName string, success bool) {
	if !success {
		fmt.Printf("Err: configuration setup failed for `%+v`\n", configName)
		os.Exit(1)
	}
}
